package exchange

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// A reduce-only market order closes the bot's opposite positions, first in,
// first out, and never opens one with the rest: more than held closes what is
// held only.
func TestMT5_CreateOrderMarketReduceClosesWithoutOpening(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)

	first, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 2)
	require.NoError(t, err)
	_, err = m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)

	f.clearRequests()
	reduced, err := m.CreateOrderMarketReduce(model.SideTypeSell, m5fPair, 5)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeFilled, reduced.Status)
	require.Equal(t, 3.0, reduced.Quantity, "what was held, not the 5 asked")
	require.Empty(t, f.positionsOf(m5fPair, m5fMagic), "flat, no short opened with the rest")
	closes := f.tradeRequests(mt5ActionDeal)
	require.Len(t, closes, 2)
	require.Equal(t, float64(first.ExchangeID), closes[0]["position"], "first in, first out")
	for _, request := range closes {
		require.Contains(t, request, "position", "every request closes a position by its ticket")
	}
}

// With nothing held against its side, eg: the position closed by its stop
// loss before the order, a reduce-only order is refused, ErrNothingToReduce,
// where a market order would open the other side.
func TestMT5_CreateOrderMarketReduceWithNothingHeld(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)

	long, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 2)
	require.NoError(t, err)

	// held on the side of the order: nothing to reduce
	_, err = m.CreateOrderMarketReduce(model.SideTypeBuy, m5fPair, 1)
	require.ErrorIs(t, err, ErrNothingToReduce)
	require.Len(t, f.positionsOf(m5fPair, m5fMagic), 1)

	// closed outside the order, the book polled since
	f.closeByHand(long.ExchangeID, 0.02)
	require.Eventually(t, func() bool {
		_, quantity, ok := m.HeldPosition(m5fPair)
		return ok && quantity == 0
	}, 2*time.Second, 10*time.Millisecond)
	f.clearRequests()
	_, err = m.CreateOrderMarketReduce(model.SideTypeSell, m5fPair, 2)
	require.ErrorIs(t, err, ErrNothingToReduce)
	require.Empty(t, f.tradeRequests(mt5ActionDeal), "nothing sent")
	require.Empty(t, f.positionsOf(m5fPair, m5fMagic))
}

// The position closed at the terminal while the book still holds it: the
// close of its ticket is refused by the terminal, and nothing opens.
func TestMT5_CreateOrderMarketReduceOfAPositionGoneMeanwhile(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour)) // the book never polled again

	long, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 2)
	require.NoError(t, err)
	f.closeByHand(long.ExchangeID, 0.02)

	_, err = m.CreateOrderMarketReduce(model.SideTypeSell, m5fPair, 2)
	require.Error(t, err)
	require.Empty(t, f.positionsOf(m5fPair, m5fMagic), "no short opened")
}
