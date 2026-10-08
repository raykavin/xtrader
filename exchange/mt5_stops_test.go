package exchange

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// R17f: the stops level, the freeze level and the tick size of a symbol.

// m5fSymbolSet sets a field of the symbol_info of a symbol of the fake, before
// an adapter reads it.
func m5fSymbolSet(f *m5fFake, symbol, key string, value float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.symbols[symbol].raw[key] = value
}

// m5fNotSent asserts that err is a trade request refused before it was sent,
// with retcode, and returns its message.
func m5fNotSent(t *testing.T, err error, retcode int) string {
	t.Helper()
	var bridgeErr *MT5Error
	require.True(t, errors.As(err, &bridgeErr), "an *MT5Error: %v", err)
	require.True(t, bridgeErr.NotSent, "not sent: %v", err)
	require.Equal(t, retcode, bridgeErr.Retcode, "%v", err)
	require.True(t, mt5Refused(err), "surely changed nothing")
	return err.Error()
}

// The levels and the pending prices closer to the market than the stops
// level (300 points, 0.3 on XAUUSDm, bid 4285.897 ask 4286.157) are refused
// with a clear error before they are sent, as the server would.
func TestMT5Stops_WithinTheStopsLevelNotSent(t *testing.T) {
	f := newM5F(t)
	m5fSymbolSet(f, m5fPair, "trade_stops_level", 300)
	m := newM5FAdapter(t, f)
	f.clearRequests()

	// a protected market entry: nothing opened
	_, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4285.7, 4300)
	require.ErrorIs(t, err, ErrInvalidProtection)
	message := m5fNotSent(t, err, mt5CodeInvalidStops)
	require.Contains(t, message, "the stop loss 4285.7 is 197 points from 4285.897, short of the stops level of 300 "+
		"points of XAUUSDm")
	_, err = m.CreateOrderMarketProtected(model.SideTypeSell, m5fPair, 1, 4290, 4286)
	require.ErrorIs(t, err, ErrInvalidProtection, "the take profit 0.157 from the ask")
	require.Empty(t, f.tradeRequests(mt5ActionDeal))
	require.Zero(t, f.openCount())

	// at the stops level, accepted
	orders, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4285.597, 4286.197)
	require.NoError(t, err)
	tp, sl := m5fLegs(t, orders[1:])

	// the levels of a position: the one that changes is checked
	f.clearRequests()
	_, err = m.UpdateProtection(sl, 4285.8)
	require.ErrorIs(t, err, ErrInvalidProtection)
	m5fNotSent(t, err, mt5CodeInvalidStops)
	_, err = m.UpdateProtection(tp, 4286.0)
	require.ErrorIs(t, err, ErrInvalidProtection)
	require.Empty(t, f.tradeRequests(mt5ActionSLTP))
	// the market moved, the stop loss 103 points from the bid, within the
	// stops level: unchanged, it isn't checked again with the take profit
	f.setPrice(m5fPair, 4285.7, 4285.96)
	moved, err := m.UpdateProtection(tp, 4300)
	require.NoError(t, err, "the stop loss, within the stops level and unchanged, isn't checked again")
	require.Equal(t, 4300.0, moved.Price)
	_, err = m.UpdateProtection(sl, 4285.6)
	message = m5fNotSent(t, err, mt5CodeInvalidStops)
	require.Contains(t, message, "the stop loss 4285.6 is 100 points from 4285.7")
	f.setPrice(m5fPair, 4285.897, 4286.157)

	// the price of a pending order from the market it is triggered by
	f.clearRequests()
	_, err = m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4286.0)
	message = m5fNotSent(t, err, mt5CodeInvalidPrice)
	require.Contains(t, message, "the price 4286 of the buy limit order is 157 points from the market 4286.157")
	_, err = m.CreateOrderStop(m5fPair, 1, 4285.7)
	m5fNotSent(t, err, mt5CodeInvalidPrice)
	// its levels from its price
	_, err = m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 1, 4280, 4279.9, 4300)
	require.ErrorIs(t, err, ErrInvalidProtection)
	m5fNotSent(t, err, mt5CodeInvalidStops)
	require.Empty(t, f.tradeRequests(mt5ActionPending))
	limit, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4285.0)
	require.NoError(t, err)

	// a pending order moved closer than the stops level
	_, err = m.UpdateLimit(limit, 4286.0)
	m5fNotSent(t, err, mt5CodeInvalidPrice)
	require.Empty(t, f.tradeRequests(mt5ActionModify))
	_, err = m.UpdateLimit(limit, 4284.0)
	require.NoError(t, err)
}

// A pending order or the levels of a position within the freeze level of the
// market (200 points) are not modified: refused before they are sent.
func TestMT5Stops_WithinTheFreezeLevelNotSent(t *testing.T) {
	f := newM5F(t)
	m5fSymbolSet(f, m5fPair, "trade_freeze_level", 200)
	m := newM5FAdapter(t, f)

	limit, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4285.0)
	require.NoError(t, err)
	orders, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4280, 4300)
	require.NoError(t, err)
	tp, sl := m5fLegs(t, orders[1:])

	f.setPrice(m5fPair, 4285.0, 4285.1) // the ask 100 points above the limit
	f.clearRequests()
	_, err = m.UpdateLimit(limit, 4284)
	message := m5fNotSent(t, err, mt5CodeFrozen)
	require.Contains(t, message, "is 100 points from the market 4285.1, within its freeze level of 200 points")
	f.setPrice(m5fPair, 4285.1, 4285.2) // at the freeze level: MT5 asks for more than it
	_, err = m.UpdateLimit(limit, 4284)
	m5fNotSent(t, err, mt5CodeFrozen)
	require.Empty(t, f.tradeRequests(mt5ActionModify))

	f.setPrice(m5fPair, 4280.1, 4280.4) // the bid 100 points above the stop loss
	_, err = m.UpdateProtection(tp, 4310)
	message = m5fNotSent(t, err, mt5CodeFrozen)
	require.Contains(t, message, "the stop loss 4280 of the position")
	require.Empty(t, f.tradeRequests(mt5ActionSLTP))

	// away from it again
	f.setPrice(m5fPair, 4290, 4290.3)
	_, err = m.UpdateProtection(sl, 4281)
	require.NoError(t, err)
	_, err = m.UpdateLimit(limit, 4284)
	require.NoError(t, err)
}

// Without a stops level nor a freeze level (eg: on Exness), nothing is checked
// and no tick is read for it.
func TestMT5Stops_WithoutLevelsNoTickRead(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	orders, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4280, 4300)
	require.NoError(t, err)
	_, sl := m5fLegs(t, orders[1:])

	f.clearRequests()
	_, err = m.UpdateProtection(sl, 4285.8)
	require.NoError(t, err)
	_, err = m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4286.0)
	require.NoError(t, err)
	require.Zero(t, f.requestCount("GET", "/tick"))
}

// The prices sent are rounded to the tick size of the symbol when it is
// coarser than its point.
func TestMT5Price_RoundedToTheTickSize(t *testing.T) {
	f := newM5F(t)
	m5fSymbolSet(f, m5fPair, "trade_tick_size", 0.005)
	m := newM5FAdapter(t, f)
	require.Equal(t, 0.005, m.AssetsInfo(m5fPair).TickSize)

	orders, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4280.0034, 4300.0074)
	require.NoError(t, err)
	_, sl := m5fLegs(t, orders[1:])
	request := f.tradeRequests(mt5ActionDeal)[0]
	require.Equal(t, 4280.005, request["sl"])
	require.Equal(t, 4300.005, request["tp"])

	moved, err := m.UpdateProtection(sl, 4282.0024)
	require.NoError(t, err)
	require.Equal(t, 4282.0, moved.Price)
	limit, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4250.0026)
	require.NoError(t, err)
	require.Equal(t, 4250.005, limit.Price)
	require.Equal(t, 4250.005, f.tradeRequests(mt5ActionPending)[0]["price"])
}
