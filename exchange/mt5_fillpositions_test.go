package exchange

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// The positions of the fills of the bot: the entry opens its ticket, its leg
// closes it, a market order closes the positions of its closing orders, first
// in first out, an inversion closes and opens. A leg of a previous run is
// unknown, its position told by the stored orders, as is a market order of a
// previous run that opened a position.
func TestMT5FillPositions(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	orders, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4280, 4300)
	require.NoError(t, err)
	entry, stop := orders[0], orders[1]
	if stop.Type != model.OrderTypeStopLoss {
		stop = orders[2]
	}
	opened, closed, ok := m.FillPositions(entry)
	require.True(t, ok)
	require.Equal(t, entry.ExchangeID, opened)
	require.Empty(t, closed)

	f.triggerSL(entry.ExchangeID, 0)
	require.Eventually(t, func() bool {
		_, open := m.OpenPosition(m5fPair)
		return !open
	}, 5*time.Second, 5*time.Millisecond, "the stop loss seen")
	opened, closed, ok = m.FillPositions(stop)
	require.True(t, ok)
	require.Zero(t, opened)
	require.Equal(t, []int64{entry.ExchangeID}, closed, "the position it protects")

	first, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	second, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	both, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 2)
	require.NoError(t, err)
	opened, closed, ok = m.FillPositions(both)
	require.True(t, ok)
	require.Zero(t, opened, "it only closed")
	require.Equal(t, []int64{first.ExchangeID, second.ExchangeID}, closed)

	long, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	inverted, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 2)
	require.NoError(t, err)
	opened, closed, ok = m.FillPositions(inverted)
	require.True(t, ok)
	require.Equal(t, inverted.ExchangeID, opened)
	require.Equal(t, []int64{long.ExchangeID}, closed)

	// after a restart, from the history read on startup
	restarted := newM5FAdapter(t, f)
	_, _, ok = restarted.FillPositions(entry)
	require.False(t, ok, "a market order of a previous run that opened: what it closed first unknown")
	_, _, ok = restarted.FillPositions(inverted)
	require.False(t, ok, "an inversion of a previous run")
	opened, closed, ok = restarted.FillPositions(both)
	require.True(t, ok)
	require.Zero(t, opened)
	require.Contains(t, closed, first.ExchangeID, "the position of its ticket, its first closing order")
	_, _, ok = restarted.FillPositions(stop)
	require.False(t, ok, "a leg of a previous run")
	_, _, ok = restarted.FillPositions(model.Order{ExchangeID: 987654, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled})
	require.False(t, ok, "not in the history")
}

// A stop order without a group filled against the long is netted by close-by:
// its fill opened its own ticket and closed the long; the long, opened before,
// closed nothing.
func TestMT5FillPositions_ClosedBy(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	entry, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	stop, err := m.CreateOrderStop(m5fPair, 1, 4260)
	require.NoError(t, err)
	f.setPrice(m5fPair, 4259, 4259.3)
	f.fillPending(stop.ExchangeID)
	require.Eventually(t, func() bool { return len(f.positionsOf(m5fPair, m5fMagic)) == 0 }, 5*time.Second,
		5*time.Millisecond, "the fill netted")
	filled := m5fWaitStatus(t, m, stop.ExchangeID, model.OrderStatusTypeFilled)

	require.Eventually(t, func() bool {
		_, closed, _ := m.FillPositions(filled)
		return len(closed) > 0
	}, 5*time.Second, 10*time.Millisecond, "the close-by in the history")
	opened, closed, ok := m.FillPositions(filled)
	require.True(t, ok)
	require.Equal(t, stop.ExchangeID, opened)
	require.Equal(t, []int64{entry.ExchangeID}, closed)

	opened, closed, ok = m.FillPositions(entry)
	require.True(t, ok)
	require.Equal(t, entry.ExchangeID, opened)
	require.Empty(t, closed, "closed by the fill, not closing it")
}

// A market order of this run closing 3 positions, the answer of the close of
// the second one without its deal and the history lagging: unknown until its
// exit is in the book, rather than the first and the third positions only,
// which would leave the second one open for the trades of the bot (see
// tradesOf of the LLM) and the rest of the order an orphan; then the 3 of
// them.
func TestMT5FillPositions_MarketCloseWithoutDeal(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	var longs []int64
	for range 3 {
		long, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
		require.NoError(t, err)
		longs = append(longs, long.ExchangeID)
	}

	f.holdHistory()
	f.answerWithoutDeal(2) // the close of the second long
	all, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 3)
	require.NoError(t, err)
	require.Empty(t, f.positionsOf(m5fPair, m5fMagic), "the 3 longs closed")
	opened, closed, ok := m.FillPositions(all)
	require.False(t, ok, "the exit of the second long not in the book yet: %d %v", opened, closed)

	f.releaseHistory()
	require.Eventually(t, func() bool {
		_, _, ok := m.FillPositions(all)
		return ok
	}, 5*time.Second, 10*time.Millisecond, "its exit read from the history")
	opened, closed, ok = m.FillPositions(all)
	require.True(t, ok)
	require.Zero(t, opened)
	require.Equal(t, longs, closed)
}
