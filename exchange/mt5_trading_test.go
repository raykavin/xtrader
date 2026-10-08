package exchange

import (
	"context"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
)

// The XAUUSDm tick of testdata/mt5.
const (
	m5fBid = 4285.897
	m5fAsk = 4286.157
)

// m5fStatus returns the status of an order of the bot.
func m5fOrderOf(t *testing.T, m *MT5, id int64) model.Order {
	t.Helper()
	order, err := m.Order(m5fPair, id)
	require.NoError(t, err)
	return order
}

// m5fWaitStatus waits until the adapter reports the order id with status, as
// the order controller asks every second, and returns it.
func m5fWaitStatus(t *testing.T, m *MT5, id int64, status model.OrderStatusType) model.Order {
	t.Helper()
	var order model.Order
	require.Eventually(t, func() bool {
		var err error
		order, err = m.Order(m5fPair, id)
		return err == nil && order.Status == status
	}, 5*time.Second, 5*time.Millisecond, "order %d never %s (last %s)", id, status, order.Status)
	return order
}

// m5fLegs returns the take profit and the stop loss among legs.
func m5fLegs(t *testing.T, legs []model.Order) (tp, sl model.Order) {
	t.Helper()
	require.Len(t, legs, 2)
	for _, leg := range legs {
		if leg.Type == model.OrderTypeStopLoss {
			sl = leg
		} else {
			tp = leg
		}
	}
	require.Equal(t, model.OrderTypeLimitMaker, tp.Type)
	require.Equal(t, model.OrderTypeStopLoss, sl.Type)
	require.NotNil(t, tp.GroupID)
	require.Equal(t, *tp.GroupID, *sl.GroupID, "the legs of one OCO group")
	return tp, sl
}

func m5fNet(t *testing.T, m *MT5) float64 {
	t.Helper()
	asset, _, err := m.Position(m5fPair)
	require.NoError(t, err)
	return asset
}

func TestMT5MarketNetting(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)

	buy, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 3)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeFilled, buy.Status)
	require.Equal(t, model.OrderTypeMarket, buy.Type)
	require.Equal(t, 3.0, buy.Quantity)
	require.Equal(t, m5fAsk, buy.Price)
	positions := f.positionsOf(m5fPair, m5fMagic)
	require.Len(t, positions, 1)
	require.Equal(t, buy.ExchangeID, positions[0].ticket, "the ticket of the position opened")
	require.Equal(t, 0.03, positions[0].volume)
	require.Regexp(t, `^nb:test#[0-9a-z]{6}$`, positions[0].comment, "the bot's comment and the tag of the request")

	request := f.tradeRequests(mt5ActionDeal)[0]
	require.Equal(t, float64(mt5TypeBuy), request["type"])
	require.Equal(t, 0.03, request["volume"])
	require.Equal(t, m5fAsk, request["price"])
	require.Equal(t, float64(mt5DefaultDeviation), request["deviation"])
	require.Equal(t, float64(m5fMagic), request["magic"])
	require.Equal(t, positions[0].comment, request["comment"])
	require.Equal(t, float64(mt5FillFOK), request["type_filling"], "filling_mode 3 allows fill or kill")
	require.Equal(t, float64(mt5TimeGTC), request["type_time"])
	require.NotContains(t, request, "position")
	require.Equal(t, 3.0, m5fNet(t, m))

	second, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 2)
	require.NoError(t, err)
	require.Len(t, f.positionsOf(m5fPair, m5fMagic), 2, "hedging: a position per order")

	// against the net: the oldest position closed whole, the next one partially
	f.clearRequests()
	sell, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 4)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeFilled, sell.Status)
	require.Equal(t, 4.0, sell.Quantity)
	require.Equal(t, m5fBid, sell.Price)
	closes := f.tradeRequests(mt5ActionDeal)
	require.Len(t, closes, 2)
	require.Equal(t, float64(buy.ExchangeID), closes[0]["position"])
	require.Equal(t, 0.03, closes[0]["volume"])
	require.Equal(t, float64(second.ExchangeID), closes[1]["position"])
	require.Equal(t, 0.01, closes[1]["volume"])
	positions = f.positionsOf(m5fPair, m5fMagic)
	require.Len(t, positions, 1)
	require.Equal(t, second.ExchangeID, positions[0].ticket)
	require.Equal(t, 0.01, positions[0].volume)
	require.NotEqual(t, buy.ExchangeID, sell.ExchangeID, "the ticket of its first closing order")
	require.Equal(t, 1.0, m5fNet(t, m))

	// a reversal: 1 closed, 2 opened, at the average price of the fills
	f.setDrift(1)
	reverse, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 3)
	require.NoError(t, err)
	require.Equal(t, 3.0, reverse.Quantity)
	require.Equal(t, mt5Round((1*m5fBid+2*(m5fBid+1))/3, 3), reverse.Price)
	short := f.positionsOf(m5fPair, m5fMagic)
	require.Len(t, short, 1)
	require.Equal(t, mt5TypeSell, short[0].kind)
	require.Equal(t, 0.02, short[0].volume)
	require.Equal(t, reverse.ExchangeID, short[0].ticket)
	require.Equal(t, -2.0, m5fNet(t, m))

	// an answer without price: the tick's, never 0
	f.setDrift(0)
	f.setZeroPrice(true)
	closing, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 2)
	require.NoError(t, err)
	require.Equal(t, m5fAsk+2, closing.Price)
	require.Zero(t, m5fNet(t, m))
	require.Empty(t, f.positionsOf(m5fPair, m5fMagic))

	// the market orders are FILLED as returned
	for _, order := range []model.Order{buy, second, sell, reverse, closing} {
		require.Equal(t, order, m5fOrderOf(t, m, order.ExchangeID))
	}

	// by quote: the size worth it at the ask
	quoted, err := m.CreateOrderMarketQuote(model.SideTypeBuy, m5fPair, 3*(m5fAsk+2))
	require.NoError(t, err)
	require.Equal(t, 3.0, quoted.Quantity)
}

func TestMT5MarketErrors(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)

	f.resultNext(mt5CodeNoMoney, "No money")
	_, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.ErrorIs(t, err, ErrInsufficientFunds)
	var mt5Err *MT5Error
	require.ErrorAs(t, err, &mt5Err)
	require.Equal(t, mt5CodeNoMoney, mt5Err.Retcode)
	require.Equal(t, "No money", mt5Err.Message)
	var orderErr *OrderError
	require.ErrorAs(t, err, &orderErr)
	require.Equal(t, m5fPair, orderErr.Pair)

	f.resultNext(mt5CodeInvalidVolume, "Invalid volume")
	_, err = m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.ErrorIs(t, err, ErrInvalidQuantity)

	f.resultNext(mt5CodeMarketClosed, "Market closed")
	_, err = m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.ErrorAs(t, err, &mt5Err)
	require.Equal(t, mt5CodeMarketClosed, mt5Err.Retcode)
	require.Empty(t, f.positionsOf(m5fPair, m5fMagic))

	// a trade request is never sent again: it may have been executed
	f.clearRequests()
	f.failNext("POST", "/order", http.StatusBadGateway, `{"error":"order_send: timeout","retcode":-10005}`)
	_, err = m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.Error(t, err)
	require.Equal(t, 1, f.requestCount("POST", "/order"))

	// a close failing after another: FILLED with the quantity done
	_, err = m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	_, err = m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	f.resultAt(2, mt5CodeRequote, "Requote")
	partial, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 3)
	require.NoError(t, err)
	require.Equal(t, 1.0, partial.Quantity)
	require.Equal(t, 1.0, m5fNet(t, m))
	require.Len(t, f.positionsOf(m5fPair, m5fMagic), 1)
}

// Only the positions of the bot's magic number count: the manual trades and
// the other bots share the account untouched.
func TestMT5MagicScoping(t *testing.T) {
	f := newM5F(t)
	manual := f.openPosition(m5fPair, mt5TypeBuy, 0.05, 0, "")
	other := f.openPosition(m5fPair, mt5TypeSell, 0.02, 7, "nb:other")
	m := newM5FAdapter(t, f)

	require.Zero(t, m5fNet(t, m))
	account, err := m.Account()
	require.NoError(t, err)
	require.Len(t, account.Balances, 1, "the cash of the account only")

	sell, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 1)
	require.NoError(t, err)
	bot := f.positionsOf(m5fPair, m5fMagic)
	require.Len(t, bot, 1)
	require.Equal(t, sell.ExchangeID, bot[0].ticket, "a new short, the manual long untouched")
	require.Equal(t, -1.0, m5fNet(t, m))
	account, err = m.Account()
	require.NoError(t, err)
	require.Len(t, account.Balances, 2)
	require.Equal(t, "XAU", account.Balances[1].Asset)
	require.Equal(t, -1.0, account.Balances[1].Free)

	_, err = m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	require.Empty(t, f.positionsOf(m5fPair, m5fMagic))
	p, ok := f.position(manual)
	require.True(t, ok)
	require.Equal(t, 0.05, p.volume)
	p, ok = f.position(other)
	require.True(t, ok)
	require.Equal(t, 0.02, p.volume)

	// the other bot's short is no net of this one: a protected long is accepted
	orders, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4280, 4300)
	require.NoError(t, err)
	require.Len(t, orders, 3)
}

// A BRL account trading a symbol quoted in USD: the cash is converted with
// USDBRLm, inverted.
func TestMT5Wallet(t *testing.T) {
	f := newM5F(t)
	f.setAccount("balance", 100000.0)
	f.setAccount("margin", 500.0)
	f.setAccount("margin_free", 99000.0)
	m := newM5FAdapter(t, f)
	usdbrl := mid(5.1807, 5.1861)

	asset, quote, err := m.Position(m5fPair)
	require.NoError(t, err)
	require.Zero(t, asset)
	require.InDelta(t, 100000/usdbrl, quote, 1e-6)

	_, err = m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 2)
	require.NoError(t, err)
	committed := 2 * m5fAsk * usdbrl
	asset, quote, err = m.Position(m5fPair)
	require.NoError(t, err)
	require.Equal(t, 2.0, asset)
	require.InDelta(t, (100000-committed)/usdbrl, quote, 1e-6)

	// a manual trade of a symbol not traded commits its value too
	f.openPosition("BTCUSDm", mt5TypeBuy, 0.01, 0, "")
	btc := 0.01 * 83939.35 * usdbrl
	require.Eventually(t, func() bool {
		account, err := m.Account()
		return err == nil && math.Abs(account.Balances[0].Lock-(committed+btc)/usdbrl) < 1e-6
	}, 5*time.Second, 5*time.Millisecond)
	account, err := m.Account()
	require.NoError(t, err)
	require.Equal(t, "USD", account.Balances[0].Asset, "in the quote of the symbols, as the pairs are valued")
	require.InDelta(t, (100000-committed-btc)/usdbrl, account.Balances[0].Free, 1e-6)
	require.Equal(t, model.Balance{Asset: "XAU", Free: 2}, account.Balances[1])

	// with leverage: the free margin
	leveraged := newM5FAdapter(t, f, WithMT5Leverage())
	account, err = leveraged.Account()
	require.NoError(t, err)
	require.Equal(t, "USD", account.Balances[0].Asset)
	require.InDelta(t, 99000/usdbrl, account.Balances[0].Free, 1e-6)
	require.InDelta(t, 500/usdbrl, account.Balances[0].Lock, 1e-6)
	require.Equal(t, model.Balance{Asset: "XAU", Free: 2, Leverage: 2000}, account.Balances[1])
	_, quote, err = leveraged.Position(m5fPair)
	require.NoError(t, err)
	require.InDelta(t, 99000/usdbrl, quote, 1e-6)
}

// Symbols of several quote currencies: the cash stays in the account currency.
func TestMT5AccountCurrencyOfMixedQuotes(t *testing.T) {
	f := newM5F(t)
	f.setAccount("balance", 100000.0)
	f.setAccount("margin_free", 100000.0)

	account, err := newM5FAdapter(t, f, WithMT5Symbols("BTCUSDm", "EURUSDm")).Account()
	require.NoError(t, err)
	require.Equal(t, "USD", account.Balances[0].Asset, "one quote, USD")

	f.symbols["USDBRLm"].raw["trade_mode"] = 4.0 // full, disabled on the account read
	account, err = newM5FAdapter(t, f, WithMT5Symbols("BTCUSDm", "USDBRLm")).Account()
	require.NoError(t, err)
	require.Equal(t, model.Balance{Asset: "BRL", Free: 100000}, account.Balances[0], "USD and BRL: the account's")
}

func TestMT5ProtectedMarket(t *testing.T) {
	open := func(t *testing.T) (*m5fFake, *MT5, model.Order, model.Order, model.Order) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		orders, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 2, 4280, 4300)
		require.NoError(t, err)
		require.Len(t, orders, 3)
		entry := orders[0]
		require.Equal(t, model.OrderStatusTypeFilled, entry.Status)
		require.Equal(t, m5fAsk, entry.Price)
		require.Equal(t, 2.0, entry.Quantity)
		tp, sl := m5fLegs(t, orders[1:])
		require.Equal(t, 4300.0, tp.Price)
		require.Equal(t, 4280.0, sl.Price)
		require.Equal(t, 4280.0, *sl.Stop)
		require.Equal(t, model.SideTypeSell, sl.Side)
		require.Equal(t, 2.0, sl.Quantity)
		p, ok := f.position(entry.ExchangeID)
		require.True(t, ok)
		require.Equal(t, 4280.0, p.sl)
		require.Equal(t, 4300.0, p.tp)
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, tp.ExchangeID).Status)
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, sl.ExchangeID).Status)
		return f, m, entry, tp, sl
	}

	t.Run("stop loss", func(t *testing.T) {
		f, m, entry, tp, sl := open(t)
		f.triggerSL(entry.ExchangeID, 4279.5)
		stopped := m5fWaitStatus(t, m, sl.ExchangeID, model.OrderStatusTypeFilled)
		require.Equal(t, 4279.5, stopped.Price, "at the price of its exit")
		require.Equal(t, 2.0, stopped.Quantity)
		m5fWaitStatus(t, m, tp.ExchangeID, model.OrderStatusTypeCanceled)
		require.Zero(t, m5fNet(t, m))
	})

	t.Run("take profit", func(t *testing.T) {
		f, m, entry, tp, sl := open(t)
		f.triggerTP(entry.ExchangeID)
		taken := m5fWaitStatus(t, m, tp.ExchangeID, model.OrderStatusTypeFilled)
		require.Equal(t, 4300.0, taken.Price)
		m5fWaitStatus(t, m, sl.ExchangeID, model.OrderStatusTypeCanceled)
	})

	t.Run("closed by the bot", func(t *testing.T) {
		_, m, _, tp, sl := open(t)
		_, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 2)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, m5fOrderOf(t, m, tp.ExchangeID).Status,
			"known at once from the answer")
		require.Equal(t, model.OrderStatusTypeCanceled, m5fOrderOf(t, m, sl.ExchangeID).Status)
	})

	t.Run("closed by the bot with its legs in place, refused first", func(t *testing.T) {
		// service.ProtectedCloser: no leg canceled before the close, so that a
		// close refused leaves the position protected
		f, m, entry, tp, sl := open(t)
		require.True(t, m.ClosesProtected())
		f.clearRequests()
		f.resultNext(mt5CodeMarketClosed, "Market closed")
		_, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 2)
		require.Error(t, err)
		require.Empty(t, f.tradeRequests(mt5ActionSLTP), "the levels never touched")
		p, ok := f.position(entry.ExchangeID)
		require.True(t, ok)
		require.Equal(t, 4280.0, p.sl)
		require.Equal(t, 4300.0, p.tp)
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, tp.ExchangeID).Status)
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, sl.ExchangeID).Status)
		require.Equal(t, 2.0, m5fNet(t, m))

		_, err = m.CreateOrderMarket(model.SideTypeSell, m5fPair, 2)
		require.NoError(t, err)
		require.Zero(t, m5fNet(t, m))
		require.Zero(t, f.openCount(), "closed, no other side opened")
		require.Equal(t, model.OrderStatusTypeCanceled, m5fOrderOf(t, m, tp.ExchangeID).Status)
		require.Equal(t, model.OrderStatusTypeCanceled, m5fOrderOf(t, m, sl.ExchangeID).Status)
		require.ErrorIs(t, m.Cancel(sl), ErrOrderNotOpen, "the legs left canceled once flat: gone already")
		require.Empty(t, f.tradeRequests(mt5ActionSLTP))
	})

	t.Run("closed by hand", func(t *testing.T) {
		f, m, entry, tp, sl := open(t)
		f.closeByHand(entry.ExchangeID, 0.02)
		m5fWaitStatus(t, m, tp.ExchangeID, model.OrderStatusTypeCanceled)
		m5fWaitStatus(t, m, sl.ExchangeID, model.OrderStatusTypeCanceled)
	})

	t.Run("half closed by hand then stopped", func(t *testing.T) {
		f, m, entry, tp, sl := open(t)
		f.closeByHand(entry.ExchangeID, 0.01)
		require.Eventually(t, func() bool { return m5fNet(t, m) == 1 }, 5*time.Second, 5*time.Millisecond)
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, sl.ExchangeID).Status)
		f.triggerSL(entry.ExchangeID, 0)
		stopped := m5fWaitStatus(t, m, sl.ExchangeID, model.OrderStatusTypeFilled)
		require.Equal(t, 1.0, stopped.Quantity, "the volume left")
		require.Equal(t, 4280.0, stopped.Price)
		m5fWaitStatus(t, m, tp.ExchangeID, model.OrderStatusTypeCanceled)
	})

	t.Run("stop out", func(t *testing.T) {
		f, m, entry, tp, sl := open(t)
		f.stopOut(entry.ExchangeID)
		m5fWaitStatus(t, m, tp.ExchangeID, model.OrderStatusTypeCanceled)
		m5fWaitStatus(t, m, sl.ExchangeID, model.OrderStatusTypeCanceled)
	})

	t.Run("protection canceled", func(t *testing.T) {
		f, m, entry, tp, sl := open(t)
		f.clearRequests()
		require.NoError(t, m.Cancel(sl))
		requests := f.tradeRequests(mt5ActionSLTP)
		require.Len(t, requests, 1)
		require.Equal(t, float64(entry.ExchangeID), requests[0]["position"])
		require.Zero(t, requests[0]["sl"])
		require.Zero(t, requests[0]["tp"])
		p, _ := f.position(entry.ExchangeID)
		require.Zero(t, p.sl)
		require.Zero(t, p.tp)
		require.Equal(t, model.OrderStatusTypeCanceled, m5fOrderOf(t, m, sl.ExchangeID).Status)
		require.Equal(t, model.OrderStatusTypeCanceled, m5fOrderOf(t, m, tp.ExchangeID).Status, "both legs")
		require.ErrorIs(t, m.Cancel(tp), ErrOrderNotOpen)
		require.ErrorIs(t, m.Cancel(entry), ErrOrderNotOpen, "a market order is filled")
		require.Equal(t, 2.0, m5fNet(t, m), "the position stays open")
	})

	t.Run("refused", func(t *testing.T) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		_, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 1)
		require.NoError(t, err)
		f.clearRequests()
		_, err = m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4280, 4300)
		require.ErrorIs(t, err, ErrNotSupported, "against the net")
		_, err = m.CreateOrderMarketProtected(model.SideTypeSell, m5fPair, 1, 4280, 4300)
		require.ErrorIs(t, err, ErrInvalidProtection, "a stop loss of a short below its take profit")
		_, err = m.CreateOrderMarketProtected(model.SideTypeSell, m5fPair, 1, 0, 0)
		require.ErrorIs(t, err, ErrInvalidProtection)
		require.Empty(t, f.requestsTo("POST", "/order"), "refused before any request")

		f.resultNext(mt5CodeInvalidStops, "Invalid stops")
		_, err = m.CreateOrderMarketProtected(model.SideTypeSell, m5fPair, 1, 4300, 4280)
		require.ErrorIs(t, err, ErrInvalidProtection)
	})
}

func TestMT5ProtectedPending(t *testing.T) {
	t.Run("limit filled then taken profit", func(t *testing.T) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		orders, err := m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 1, 4250, 4240, 4300)
		require.NoError(t, err)
		require.Len(t, orders, 3)
		entry := orders[0]
		require.Equal(t, model.OrderTypeLimit, entry.Type)
		require.Equal(t, model.OrderStatusTypeNew, entry.Status)
		require.Equal(t, 4250.0, entry.Price)
		tp, sl := m5fLegs(t, orders[1:])
		pending, ok := f.pending(entry.ExchangeID)
		require.True(t, ok)
		require.Equal(t, mt5TypeBuyLimit, pending.kind)
		require.Equal(t, 4240.0, pending.sl)
		require.Equal(t, 4300.0, pending.tp)
		require.Equal(t, mt5FillReturn, pending.typeFilling)
		require.Equal(t, mt5TimeGTC, pending.typeTime)
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, tp.ExchangeID).Status)

		f.fillPending(entry.ExchangeID)
		filled := m5fWaitStatus(t, m, entry.ExchangeID, model.OrderStatusTypeFilled)
		require.Equal(t, 4250.0, filled.Price)
		require.Equal(t, 1.0, filled.Quantity)
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, tp.ExchangeID).Status, "the position is open")
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, sl.ExchangeID).Status)

		f.triggerTP(entry.ExchangeID)
		require.Equal(t, 4300.0, m5fWaitStatus(t, m, tp.ExchangeID, model.OrderStatusTypeFilled).Price)
		m5fWaitStatus(t, m, sl.ExchangeID, model.OrderStatusTypeCanceled)
	})

	t.Run("stop canceled", func(t *testing.T) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		orders, err := m.CreateOrderStopProtected(model.SideTypeSell, m5fPair, 1, 4270, 4280, 4250)
		require.NoError(t, err)
		entry := orders[0]
		require.Equal(t, model.OrderTypeStopLoss, entry.Type)
		require.Equal(t, 4270.0, *entry.Stop)
		pending, _ := f.pending(entry.ExchangeID)
		require.Equal(t, mt5TypeSellStop, pending.kind)

		require.NoError(t, m.Cancel(entry))
		require.Len(t, f.tradeRequests(mt5ActionRemove), 1)
		_, ok := f.pending(entry.ExchangeID)
		require.False(t, ok)
		for _, order := range orders {
			require.Equal(t, model.OrderStatusTypeCanceled, m5fOrderOf(t, m, order.ExchangeID).Status)
		}
		require.ErrorIs(t, m.Cancel(entry), ErrOrderNotOpen)
	})

	t.Run("expired", func(t *testing.T) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		orders, err := m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 1, 4250, 4240, 4300)
		require.NoError(t, err)
		f.expirePending(orders[0].ExchangeID)
		m5fWaitStatus(t, m, orders[0].ExchangeID, model.OrderStatusTypeExpired)
		m5fWaitStatus(t, m, orders[1].ExchangeID, model.OrderStatusTypeCanceled)
		m5fWaitStatus(t, m, orders[2].ExchangeID, model.OrderStatusTypeCanceled)
	})

	t.Run("canceled by hand", func(t *testing.T) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		orders, err := m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 1, 4250, 4240, 4300)
		require.NoError(t, err)
		f.cancelPendingByHand(orders[0].ExchangeID)
		for _, order := range orders {
			m5fWaitStatus(t, m, order.ExchangeID, model.OrderStatusTypeCanceled)
		}
	})

	t.Run("refused", func(t *testing.T) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		_, err := m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 1, 4250, 4260, 4300)
		require.ErrorIs(t, err, ErrInvalidProtection, "a stop loss above the entry of a long")
		f.resultNext(mt5CodeInvalidPrice, "Invalid price")
		_, err = m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 1, 4250, 4240, 4300)
		var mt5Err *MT5Error
		require.ErrorAs(t, err, &mt5Err)
		require.Equal(t, mt5CodeInvalidPrice, mt5Err.Retcode)
	})
}

func TestMT5OCO(t *testing.T) {
	t.Run("single position", func(t *testing.T) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		_, err := m.CreateOrderOCO(model.SideTypeSell, m5fPair, 2, 4300, 4280, 4280)
		require.ErrorIs(t, err, ErrNotSupported, "no position")

		long, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 2)
		require.NoError(t, err)
		_, err = m.CreateOrderOCO(model.SideTypeBuy, m5fPair, 2, 4300, 4280, 4280)
		require.ErrorIs(t, err, ErrNotSupported, "adding to the position")
		_, err = m.CreateOrderOCO(model.SideTypeSell, m5fPair, 1, 4300, 4280, 4280)
		require.ErrorIs(t, err, ErrInvalidQuantity, "part of the position")
		_, err = m.CreateOrderOCO(model.SideTypeSell, m5fPair, 2, 4280, 4300, 4300)
		require.ErrorIs(t, err, ErrInvalidProtection)

		legs, err := m.CreateOrderOCO(model.SideTypeSell, m5fPair, 2, 4300, 4280, 4280)
		require.NoError(t, err)
		tp, sl := m5fLegs(t, legs)
		require.Equal(t, 4300.0, tp.Price)
		require.Equal(t, 4280.0, sl.Price)
		require.Equal(t, model.SideTypeSell, tp.Side)
		p, _ := f.position(long.ExchangeID)
		require.Equal(t, 4280.0, p.sl)
		require.Equal(t, 4300.0, p.tp)
		_, err = m.CreateOrderOCO(model.SideTypeSell, m5fPair, 2, 4300, 4280, 4280)
		require.ErrorIs(t, err, ErrNotSupported, "protected already")

		f.triggerTP(long.ExchangeID)
		require.Equal(t, 4300.0, m5fWaitStatus(t, m, tp.ExchangeID, model.OrderStatusTypeFilled).Price)
		m5fWaitStatus(t, m, sl.ExchangeID, model.OrderStatusTypeCanceled)
	})

	t.Run("several positions", func(t *testing.T) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		first, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
		require.NoError(t, err)
		second, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
		require.NoError(t, err)
		legs, err := m.CreateOrderOCO(model.SideTypeSell, m5fPair, 2, 4300, 4280, 4280)
		require.NoError(t, err)
		tp, sl := m5fLegs(t, legs)
		require.Equal(t, 2.0, sl.Quantity)
		require.Len(t, f.tradeRequests(mt5ActionSLTP), 2, "the levels of each position")

		f.triggerSL(first.ExchangeID, 4279)
		require.Eventually(t, func() bool { return m5fNet(t, m) == 1 }, 5*time.Second, 5*time.Millisecond)
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, sl.ExchangeID).Status, "a position left")
		f.triggerSL(second.ExchangeID, 4277)
		stopped := m5fWaitStatus(t, m, sl.ExchangeID, model.OrderStatusTypeFilled)
		require.Equal(t, 2.0, stopped.Quantity)
		require.Equal(t, 4278.0, stopped.Price, "the average of the exits")
		m5fWaitStatus(t, m, tp.ExchangeID, model.OrderStatusTypeCanceled)
	})

	t.Run("protected again", func(t *testing.T) {
		f := newM5F(t)
		m := newM5FAdapter(t, f)
		long, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
		require.NoError(t, err)
		legs, err := m.CreateOrderOCO(model.SideTypeSell, m5fPair, 1, 4300, 4280, 4280)
		require.NoError(t, err)
		oldTP, oldSL := m5fLegs(t, legs)
		require.NoError(t, m.Cancel(oldTP))

		legs, err = m.CreateOrderOCO(model.SideTypeSell, m5fPair, 1, 4310, 4270, 4270)
		require.NoError(t, err)
		tp, sl := m5fLegs(t, legs)
		require.NotEqual(t, oldTP.ExchangeID, tp.ExchangeID, "new ids: the old legs stay canceled")
		require.NotEqual(t, oldSL.ExchangeID, sl.ExchangeID)
		require.Equal(t, model.OrderStatusTypeCanceled, m5fOrderOf(t, m, oldSL.ExchangeID).Status)
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, sl.ExchangeID).Status)

		f.triggerSL(long.ExchangeID, 0)
		require.Equal(t, 4270.0, m5fWaitStatus(t, m, sl.ExchangeID, model.OrderStatusTypeFilled).Price)
		require.Equal(t, model.OrderStatusTypeCanceled, m5fOrderOf(t, m, oldSL.ExchangeID).Status)
	})
}

func TestMT5UpdateProtection(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	orders, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4280, 4300)
	require.NoError(t, err)
	entry := orders[0]
	tp, sl := m5fLegs(t, orders[1:])

	f.clearRequests()
	moved, err := m.UpdateProtection(sl, 4282.0004)
	require.NoError(t, err)
	require.Equal(t, 4282.0, moved.Price, "rounded to the digits")
	require.Equal(t, 4282.0, *moved.Stop)
	require.Equal(t, sl.ExchangeID, moved.ExchangeID)
	requests := f.tradeRequests(mt5ActionSLTP)
	require.Len(t, requests, 1)
	require.Equal(t, 4282.0, requests[0]["sl"])
	require.Equal(t, 4300.0, requests[0]["tp"], "the other level sent again")
	p, _ := f.position(entry.ExchangeID)
	require.Equal(t, 4282.0, p.sl)

	moved, err = m.UpdateProtection(tp, 4310)
	require.NoError(t, err)
	require.Equal(t, 4310.0, moved.Price)
	p, _ = f.position(entry.ExchangeID)
	require.Equal(t, 4282.0, p.sl)
	require.Equal(t, 4310.0, p.tp)

	_, err = m.UpdateProtection(sl, 4315)
	require.ErrorIs(t, err, ErrInvalidProtection, "a stop loss beyond the take profit")
	_, err = m.UpdateProtection(entry, 4290)
	require.ErrorIs(t, err, ErrNotSupported, "not a leg")

	// a level removed by hand: the leg is canceled and never set again
	f.setLevelsByHand(entry.ExchangeID, 0, 4310)
	m5fWaitStatus(t, m, sl.ExchangeID, model.OrderStatusTypeCanceled)
	_, err = m.UpdateProtection(sl, 4283)
	require.ErrorIs(t, err, ErrOrderNotOpen)

	// the legs of a pending entry: its levels move with its price
	orders, err = m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 1, 4250, 4240, 4300)
	require.NoError(t, err)
	_, pendingSL := m5fLegs(t, orders[1:])
	f.clearRequests()
	_, err = m.UpdateProtection(pendingSL, 4245)
	require.NoError(t, err)
	modify := f.tradeRequests(mt5ActionModify)
	require.Len(t, modify, 1)
	require.Equal(t, float64(orders[0].ExchangeID), modify[0]["order"])
	require.Equal(t, 4250.0, modify[0]["price"])
	require.Equal(t, 4245.0, modify[0]["sl"])
	require.Equal(t, 4300.0, modify[0]["tp"])
}

func TestMT5UpdateLimit(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	limit, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4250)
	require.NoError(t, err)

	f.clearRequests()
	moved, err := m.UpdateLimit(limit, 4260)
	require.NoError(t, err)
	require.Equal(t, 4260.0, moved.Price)
	requests := f.tradeRequests(mt5ActionModify)
	require.Len(t, requests, 1)
	require.Equal(t, float64(limit.ExchangeID), requests[0]["order"])
	require.Equal(t, 4260.0, requests[0]["price"])
	require.Zero(t, requests[0]["sl"])
	require.Zero(t, requests[0]["tp"])
	require.Equal(t, float64(mt5TimeGTC), requests[0]["type_time"])
	require.Zero(t, requests[0]["expiration"])
	pending, _ := f.pending(limit.ExchangeID)
	require.Equal(t, 4260.0, pending.priceOpen)
	require.Equal(t, 4260.0, m5fOrderOf(t, m, limit.ExchangeID).Price)

	// a protected one keeps its levels
	orders, err := m.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 1, 4250, 4240, 4300)
	require.NoError(t, err)
	_, err = m.UpdateLimit(orders[0], 4255)
	require.NoError(t, err)
	pending, _ = f.pending(orders[0].ExchangeID)
	require.Equal(t, 4255.0, pending.priceOpen)
	require.Equal(t, 4240.0, pending.sl)
	require.Equal(t, 4300.0, pending.tp)
	_, err = m.UpdateLimit(orders[0], 4235)
	require.ErrorIs(t, err, ErrInvalidProtection, "below its stop loss")
	_, err = m.UpdateLimit(orders[1], 4255)
	require.ErrorIs(t, err, ErrNotSupported, "a leg")
	_, err = m.UpdateLimit(limit, 0)
	require.ErrorIs(t, err, ErrInvalidProtection)

	stop, err := m.CreateOrderStop(m5fPair, 1, 4200)
	require.NoError(t, err)
	_, err = m.UpdateLimit(stop, 4210)
	require.ErrorIs(t, err, ErrNotSupported, "a stop order")

	f.fillPending(limit.ExchangeID)
	m5fWaitStatus(t, m, limit.ExchangeID, model.OrderStatusTypeFilled)
	_, err = m.UpdateLimit(limit, 4270)
	require.ErrorIs(t, err, ErrOrderNotOpen)
}

// A pending order filled against the bot's net is closed by the opposite
// position (close-by), on the next poll.
func TestMT5PendingFillCloseBy(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)

	long, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 3)
	require.NoError(t, err)
	stop, err := m.CreateOrderStop(m5fPair, 3, 4260)
	require.NoError(t, err)
	require.Equal(t, model.OrderTypeStopLoss, stop.Type)
	require.Equal(t, model.SideTypeSell, stop.Side)
	pending, _ := f.pending(stop.ExchangeID)
	require.Equal(t, mt5TypeSellStop, pending.kind)

	f.fillPending(stop.ExchangeID)
	require.Eventually(t, func() bool { return len(f.positionsOf(m5fPair, m5fMagic)) == 0 }, 5*time.Second,
		5*time.Millisecond, "the fill netted")
	closeBy := f.tradeRequests(mt5ActionCloseBy)
	require.Len(t, closeBy, 1)
	require.Equal(t, float64(stop.ExchangeID), closeBy[0]["position"], "the fill")
	require.Equal(t, float64(long.ExchangeID), closeBy[0]["position_by"], "by the oldest opposite position")
	filled := m5fWaitStatus(t, m, stop.ExchangeID, model.OrderStatusTypeFilled)
	require.Equal(t, 4260.0, filled.Price)
	require.Equal(t, 3.0, filled.Quantity)
	require.Zero(t, m5fNet(t, m))

	// partially: the rest stays on the larger position
	long, err = m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 3)
	require.NoError(t, err)
	limit, err := m.CreateOrderLimit(model.SideTypeSell, m5fPair, 1, 4300)
	require.NoError(t, err)
	f.fillPending(limit.ExchangeID)
	require.Eventually(t, func() bool {
		positions := f.positionsOf(m5fPair, m5fMagic)
		return len(positions) == 1 && positions[0].ticket == long.ExchangeID && positions[0].volume == 0.02
	}, 5*time.Second, 5*time.Millisecond)
	require.Equal(t, 4300.0, m5fWaitStatus(t, m, limit.ExchangeID, model.OrderStatusTypeFilled).Price)
	require.Eventually(t, func() bool { return m5fNet(t, m) == 2 }, 5*time.Second, 5*time.Millisecond)
}

// The fills of the pending orders while the bot was off are netted when it
// starts, before NewMT5 returns.
func TestMT5CloseByOnStartup(t *testing.T) {
	f := newM5F(t)
	long := f.openPosition(m5fPair, mt5TypeBuy, 0.03, m5fMagic, "nb:test")
	limit := f.placePending(m5fPair, mt5TypeSellLimit, 0.01, 4300, 0, 0, m5fMagic)
	f.fillPending(limit)
	require.Len(t, f.positionsOf(m5fPair, m5fMagic), 2)

	m := newM5FAdapter(t, f)
	positions := f.positionsOf(m5fPair, m5fMagic)
	require.Len(t, positions, 1)
	require.Equal(t, long, positions[0].ticket)
	require.Equal(t, 0.02, positions[0].volume)
	require.Equal(t, 2.0, m5fNet(t, m))
	filled := m5fOrderOf(t, m, limit)
	require.Equal(t, model.OrderStatusTypeFilled, filled.Status, "rebuilt from the history")
	require.Equal(t, 4300.0, filled.Price)
	require.Equal(t, model.SideTypeSell, filled.Side)
	require.Equal(t, 1.0, filled.Quantity)
}

// The order controller asks every pending order every second: the adapter
// answers from its book, polled apart.
func TestMT5OrdersFromBook(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour))

	limit, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4250)
	require.NoError(t, err)
	orders, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4280, 4300)
	require.NoError(t, err)
	f.clearRequests()
	for i := 0; i < 100; i++ {
		require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, limit.ExchangeID).Status)
		for _, leg := range orders[1:] {
			order, err := m.OrderSince(m5fPair, leg.ExchangeID, leg.CreatedAt)
			require.NoError(t, err)
			require.Equal(t, model.OrderStatusTypeNew, order.Status)
		}
		require.Equal(t, model.OrderStatusTypeFilled, m5fOrderOf(t, m, orders[0].ExchangeID).Status)
	}
	f.mu.Lock()
	requests := len(f.requests)
	f.mu.Unlock()
	require.Zero(t, requests, "no request per call")

	// a poll brings the fill
	f.fillPending(limit.ExchangeID)
	require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, limit.ExchangeID).Status, "not polled yet")
	require.NoError(t, m.poll(context.Background()))
	require.Equal(t, model.OrderStatusTypeFilled, m5fOrderOf(t, m, limit.ExchangeID).Status)
}

// The orders of a previous run are rebuilt from the book and the history,
// eg: a protected entry filled and taken profit while the bot was off.
func TestMT5Restart(t *testing.T) {
	f := newM5F(t)
	ctx, cancel := context.WithCancel(context.Background())
	first, err := NewMT5(ctx, f.options()...)
	require.NoError(t, err)

	since := time.Now().Add(-time.Second)
	protected, err := first.CreateOrderLimitProtected(model.SideTypeBuy, m5fPair, 1, 4250, 4240, 4300)
	require.NoError(t, err)
	entryTP, entrySL := m5fLegs(t, protected[1:])
	limit, err := first.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4200)
	require.NoError(t, err)
	long, err := first.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	legs, err := first.CreateOrderOCO(model.SideTypeSell, m5fPair, 1, 4310, 4270, 4270)
	require.NoError(t, err)
	_, ocoSL := m5fLegs(t, legs)
	cancel()

	// while the bot is off
	f.fillPending(protected[0].ExchangeID)
	f.triggerTP(protected[0].ExchangeID)

	m := newM5FAdapter(t, f)
	entry, err := m.OrderSince(m5fPair, protected[0].ExchangeID, since)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeFilled, entry.Status)
	require.Equal(t, 4250.0, entry.Price)
	require.Equal(t, 1.0, entry.Quantity)
	require.Equal(t, model.SideTypeBuy, entry.Side)

	taken, err := m.OrderSince(m5fPair, entryTP.ExchangeID, since)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeFilled, taken.Status)
	require.Equal(t, 4300.0, taken.Price)
	require.Equal(t, 1.0, taken.Quantity)
	require.Equal(t, model.SideTypeSell, taken.Side)
	stopped, err := m.OrderSince(m5fPair, entrySL.ExchangeID, since)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeCanceled, stopped.Status)

	pending, err := m.OrderSince(m5fPair, limit.ExchangeID, since)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeNew, pending.Status)
	require.Equal(t, 4200.0, pending.Price)
	require.NoError(t, m.Cancel(limit))
	require.Equal(t, model.OrderStatusTypeCanceled, m5fOrderOf(t, m, limit.ExchangeID).Status)

	open, err := m.OrderSince(m5fPair, ocoSL.ExchangeID, since)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeNew, open.Status)
	require.Equal(t, 4270.0, open.Price)
	moved, err := m.UpdateProtection(ocoSL, 4275)
	require.NoError(t, err)
	require.Equal(t, 4275.0, moved.Price)
	p, _ := f.position(long.ExchangeID)
	require.Equal(t, 4275.0, p.sl)
	require.Equal(t, 4310.0, p.tp)

	_, err = m.OrderSince(m5fPair, mt5HashID("unknown"), since)
	require.ErrorIs(t, err, ErrOrderNotFound)
	_, err = m.OrderSince(m5fPair, 12345, since)
	require.ErrorIs(t, err, ErrOrderNotFound)
}

// The orders of the history may be listed by their setup time: a pending
// order placed days ago and canceled now is found all the same.
func TestMT5HistoryFromSetup(t *testing.T) {
	f := newM5F(t)
	ticket := f.placePending(m5fPair, mt5TypeBuyLimit, 0.01, 4200, 0, 0, m5fMagic)
	f.mu.Lock()
	f.orders[ticket].setupMsc = time.Now().Add(-10 * 24 * time.Hour).UnixMilli()
	f.mu.Unlock()

	m := newM5FAdapter(t, f)
	require.Equal(t, model.OrderStatusTypeNew, m5fOrderOf(t, m, ticket).Status)
	f.cancelPendingByHand(ticket)
	canceled := m5fWaitStatus(t, m, ticket, model.OrderStatusTypeCanceled)
	require.Equal(t, 4200.0, canceled.Price)
}

// A close-by refused is not sent again every poll: the pair waits
// mt5SettleRetry, but the next trading call of the pair nets it first.
func TestMT5CloseByRefused(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	_, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 3)
	require.NoError(t, err)
	stop, err := m.CreateOrderStop(m5fPair, 1, 4260)
	require.NoError(t, err)

	f.resultNext(mt5CodeInvalid, "Invalid request")
	f.fillPending(stop.ExchangeID)
	require.Eventually(t, func() bool { return len(f.tradeRequests(mt5ActionCloseBy)) == 1 }, 5*time.Second,
		5*time.Millisecond)
	time.Sleep(20 * m.pollInterval)
	require.Len(t, f.tradeRequests(mt5ActionCloseBy), 1, "not sent again every poll")
	require.Len(t, f.positionsOf(m5fPair, m5fMagic), 2, "both sides still open")
	require.Equal(t, 2.0, m5fNet(t, m), "the net is right all the same")

	_, err = m.CreateOrderMarket(model.SideTypeSell, m5fPair, 1)
	require.NoError(t, err)
	require.Len(t, f.tradeRequests(mt5ActionCloseBy), 2, "netted first by the trading call")
	positions := f.positionsOf(m5fPair, m5fMagic)
	require.Len(t, positions, 1)
	require.Equal(t, 0.01, positions[0].volume)
	require.Equal(t, 1.0, m5fNet(t, m))
}

// The net position of the bot as the terminal reports it, its profit
// included: the manual trades and the other bots left out.
func TestMT5OpenPosition(t *testing.T) {
	f := newM5F(t)
	f.openPosition(m5fPair, mt5TypeSell, 0.05, 0, "") // manual
	m := newM5FAdapter(t, f)
	_, ok := m.OpenPosition(m5fPair)
	require.False(t, ok, "flat")

	buy, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 2)
	require.NoError(t, err)
	var position MT5OpenPosition
	require.Eventually(t, func() bool {
		position, ok = m.OpenPosition(m5fPair)
		return ok
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, model.SideTypeBuy, position.Side)
	require.InDelta(t, 2, position.Quantity, 1e-9)
	require.InDelta(t, buy.Price, position.Entry, 1e-9)
	require.Equal(t, "BRL", position.Currency)

	_, err = m.CreateOrderMarket(model.SideTypeSell, m5fPair, 2)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, ok = m.OpenPosition(m5fPair)
		return !ok
	}, 5*time.Second, 10*time.Millisecond, "closed")
}

// The account and the profits of the trades as the terminal books them, in the
// currency of the account: for the display.
func TestMT5AccountReporter(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	var _ service.AccountReporter = m

	summary, ok := m.AccountSummary()
	require.True(t, ok)
	require.Equal(t, "BRL", summary.Currency)

	orders, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4280, 4300)
	require.NoError(t, err)
	entry, profit, stop := orders[0], orders[1], orders[2]
	if profit.Type == model.OrderTypeStopLoss {
		profit, stop = stop, profit
	}
	_, known := m.ClosedProfit(profit, entry)
	require.False(t, known, "open")

	f.triggerTP(entry.ExchangeID)
	var booked float64
	require.Eventually(t, func() bool {
		booked, known = m.ClosedProfit(profit, entry)
		return known
	}, 5*time.Second, 10*time.Millisecond)
	deals := f.dealsOf(entry.ExchangeID)
	require.NotEmpty(t, deals)
	var want float64
	for _, deal := range deals {
		if deal.entry == mt5EntryOut {
			want += deal.profit
		}
	}
	require.InDelta(t, want, booked, 1e-9, "the profit of its exit, in BRL")
	require.NotZero(t, booked)

	_, known = m.ClosedProfit(stop, entry)
	require.False(t, known, "the stop loss didn't close it")
}

// A position closed at the market, eg: the EXIT of a strategy: the profit the
// account booked is the one of its exit in the history, never the zero of the
// deal recorded from the answer of the close.
func TestMT5ClosedProfitOfAMarketClose(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	orders, err := m.CreateOrderMarketProtected(model.SideTypeSell, m5fPair, 1, 4400, 0)
	require.NoError(t, err)
	entry := orders[0]
	f.setPrice(m5fPair, 4270, 4270.3) // the price fell: the short earns

	closed, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	var want float64
	for _, deal := range f.dealsOf(entry.ExchangeID) {
		if deal.entry == mt5EntryOut {
			want += deal.profit
		}
	}
	require.Positive(t, want)
	booked, known := m.ClosedProfit(closed, entry)
	require.True(t, known, "read at once")
	require.InDelta(t, want, booked, 1e-9, "the profit of its exit, in BRL")
}

// The history unread at the close: the profit is unknown, not zero, until a
// poll reads the exit.
func TestMT5ClosedProfitAwaitsTheHistory(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour)) // polled when asked only
	orders, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4200, 0)
	require.NoError(t, err)
	entry := orders[0]
	f.setPrice(m5fPair, 4262, 4262.3) // the price fell: the long loses

	for range mt5Retries + 1 { // each attempt of the read
		f.failNext("GET", "/deals", http.StatusInternalServerError, `{"error":"the terminal is busy"}`)
	}
	closed, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 1)
	require.NoError(t, err, "the close done, whatever the read after it")
	_, known := m.ClosedProfit(closed, entry)
	require.False(t, known, "its deal known from the answer only")

	var want float64
	for _, deal := range f.dealsOf(entry.ExchangeID) {
		if deal.entry == mt5EntryOut {
			want += deal.profit
		}
	}
	require.Negative(t, want)
	m.requestPoll()
	var booked float64
	require.Eventually(t, func() bool {
		booked, known = m.ClosedProfit(closed, entry)
		return known
	}, 5*time.Second, 10*time.Millisecond, "read by a poll")
	require.InDelta(t, want, booked, 1e-9)
}
