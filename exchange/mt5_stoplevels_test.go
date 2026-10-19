package exchange

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// The live level of a stop loss over several positions is the most protective
// one: the highest of a long, the lowest of a short; the legs of a pending
// entry don't protect a position.
func TestMT5_LiveProtection(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)

	positions, _, sl := m5fProtected(t, f, m, 2) // long, stop loss 4280 on both
	live, protects, err := m.LiveProtection(sl)
	require.NoError(t, err)
	require.True(t, protects)
	require.Equal(t, 4280.0, *live.Stop)

	f.setLevelsByHand(positions[1], 4285, 4300) // one tightened in the terminal
	require.Eventually(t, func() bool {
		live, _, err := m.LiveProtection(sl)
		return err == nil && *live.Stop == 4285
	}, 2*time.Second, 10*time.Millisecond, "the highest stop loss of a long")
	order, err := m.Order(m5fPair, sl.ExchangeID)
	require.NoError(t, err)
	require.Equal(t, 4285.0, *order.Stop, "the leg as the order controller reads it")

	t.Run("a short: the lowest stop loss", func(t *testing.T) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		var tickets []int64
		for range 2 {
			short, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 1)
			require.NoError(t, err)
			tickets = append(tickets, short.ExchangeID)
		}
		legs, err := m.CreateOrderOCO(model.SideTypeBuy, m5fPair, 2, 4270, 4310, 4310)
		require.NoError(t, err)
		_, stop := m5fLegs(t, legs)
		f.setLevelsByHand(tickets[0], 4305, 4270)
		require.Eventually(t, func() bool {
			live, protects, err := m.LiveProtection(stop)
			return err == nil && protects && *live.Stop == 4305
		}, 2*time.Second, 10*time.Millisecond, "never the loosest, 4310")
	})

	t.Run("the legs of a pending entry", func(t *testing.T) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		orders, err := m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 1, 4200, 4150, 4300)
		require.NoError(t, err)
		_, stop := m5fLegs(t, orders[1:])
		_, protects, err := m.LiveProtection(stop)
		require.NoError(t, err)
		require.False(t, protects, "it moves with its entry")
	})
}

// A position without a stop loss gets one, its take profit kept: the legs of
// both levels returned, the stop loss among them; one with a stop loss is
// refused.
func TestMT5_SetStop(t *testing.T) {
	t.Run("without levels", func(t *testing.T) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		long, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
		require.NoError(t, err)

		legs, err := m.SetStop(model.SideTypeBuy, m5fPair, 1, 4280)
		require.NoError(t, err)
		require.Len(t, legs, 1)
		require.Equal(t, model.OrderTypeStopLoss, legs[0].Type)
		p, ok := f.position(long.ExchangeID)
		require.True(t, ok)
		require.Equal(t, 4280.0, p.sl)
		require.Zero(t, p.tp)

		_, err = m.SetStop(model.SideTypeBuy, m5fPair, 1, 4285)
		require.ErrorIs(t, err, ErrNotSupported, "a stop loss already: moved with UpdateProtection")
	})

	t.Run("with a take profit set by hand", func(t *testing.T) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		long, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
		require.NoError(t, err)
		f.setLevelsByHand(long.ExchangeID, 0, 4300)
		require.Eventually(t, func() bool {
			positions := m.botPositions(m5fPair)
			return len(positions) == 1 && positions[0].TP == 4300
		}, 2*time.Second, 10*time.Millisecond)

		legs, err := m.SetStop(model.SideTypeBuy, m5fPair, 1, 4280)
		require.NoError(t, err)
		tp, sl := m5fLegs(t, legs)
		require.Equal(t, 4300.0, tp.Price)
		require.Equal(t, 4280.0, *sl.Stop)
		p, ok := f.position(long.ExchangeID)
		require.True(t, ok)
		require.Equal(t, [2]float64{4280, 4300}, [2]float64{p.sl, p.tp}, "the take profit kept")
	})

	t.Run("on the other side of the market: refused, nothing sent", func(t *testing.T) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		long, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
		require.NoError(t, err)
		f.setLevelsByHand(long.ExchangeID, 0, 4300)
		require.Eventually(t, func() bool {
			positions := m.botPositions(m5fPair)
			return len(positions) == 1 && positions[0].TP == 4300
		}, 2*time.Second, 10*time.Millisecond)
		f.clearRequests()
		_, err = m.SetStop(model.SideTypeBuy, m5fPair, 1, 4310)
		require.ErrorIs(t, err, ErrInvalidProtection, "a stop loss beyond the take profit")
		require.Empty(t, f.tradeRequests(mt5ActionSLTP))
	})
}

// The constraints of the levels come from the symbol: its tick, and its stops
// and freeze levels in price.
func TestMT5_ProtectionLimits(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	limits, ok := m.ProtectionLimits(m5fPair)
	require.True(t, ok)
	symbol := m.instruments[m5fPair]
	require.Equal(t, symbol.tick(), limits.Tick)
	require.InDelta(t, float64(symbol.StopsLevel)*symbol.Point, limits.StopsLevel, 1e-12)
	_, ok = m.ProtectionLimits("NOPE")
	require.False(t, ok)
}
