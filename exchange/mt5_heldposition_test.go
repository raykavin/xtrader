package exchange

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
)

// R11: the position the bot holds as the terminal reports it, for the order
// controller to check the positions it replays from the storage: the
// positions of its magic number netted, the manual trades and the other bots
// left out; unknown without the book or for a pair not traded.
func TestMT5HeldPosition_TheBotsOwn(t *testing.T) {
	f := newM5F(t)
	f.openPosition(m5fPair, mt5TypeSell, 0.05, 0, "")       // manual
	f.openPosition(m5fPair, mt5TypeBuy, 0.07, 777, "")      // another bot
	f.openPosition(m5fPair, mt5TypeBuy, 0.02, m5fMagic, "") // of a previous run
	m := newM5FAdapter(t, f)
	var _ service.PositionReporter = m

	side, quantity, ok := m.HeldPosition(m5fPair)
	require.True(t, ok)
	require.Equal(t, model.SideTypeBuy, side)
	require.Equal(t, m5fNet(t, m), quantity, "as Position nets it")
	require.Positive(t, quantity)

	_, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, quantity)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		side, quantity, ok = m.HeldPosition(m5fPair)
		return ok && quantity == 0
	}, 5*time.Second, 10*time.Millisecond, "flat")
	require.Empty(t, side)

	_, _, ok = m.HeldPosition("EURUSDm")
	require.False(t, ok, "a pair not traded")
	_, _, ok = newM5FAdapter(t, f, WithMT5DataOnly()).HeldPosition(m5fPair)
	require.False(t, ok, "no book")
}
