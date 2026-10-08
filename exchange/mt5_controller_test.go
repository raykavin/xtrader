package exchange_test

// The MT5 adapter behind the order controller, end to end: order.NewController
// with the adapter and an in-memory storage, UpdateOrders settling the fills
// and the profits in units. In package exchange_test, as the order package
// imports exchange; the fake bridge is reached through exchange.MT5TestFake
// (mt5_fake_test.go).

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/storage"
)

const m5cPair = "XAUUSDm"

// m5cSettle runs UpdateOrders until the stored order id has status, as the
// ticker of a started controller would.
func m5cSettle(t *testing.T, c *order.Controller, st storage.Storage, id int64,
	status model.OrderStatusType) model.Order {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.UpdateOrders()
		orders, err := st.Orders()
		require.NoError(t, err)
		for _, stored := range orders {
			if stored.ID == id && stored.Status == status {
				return *stored
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for the order %d %s", id, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func m5cLegs(legs []model.Order) (tp, sl model.Order) {
	for _, leg := range legs {
		if leg.Type == model.OrderTypeStopLoss {
			sl = leg
		} else {
			tp = leg
		}
	}
	return tp, sl
}

func TestMT5ControllerEndToEnd(t *testing.T) {
	fake := exchange.NewMT5TestFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m, err := exchange.NewMT5(ctx, fake.Options()...)
	require.NoError(t, err)

	st, err := storage.FromMemory()
	require.NoError(t, err)
	c := order.NewController(ctx, m, st, order.NewOrderFeed())

	// a long of 2 ounces, closed in two halves: the profit is the price
	// difference times the ounces, in USD
	buy, err := c.CreateOrderMarket(model.SideTypeBuy, m5cPair, 2)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeFilled, buy.Status)
	require.Equal(t, 2.0, buy.Quantity)
	require.Equal(t, 4286.157, buy.Price)
	require.InDelta(t, 0.02, fake.NetLots(m5cPair), 1e-9, "2 ounces are 0.02 lot")

	fake.SetPrice(m5cPair, 4290.000, 4290.260)
	sell, err := c.CreateOrderMarket(model.SideTypeSell, m5cPair, 1)
	require.NoError(t, err)
	require.InDelta(t, (4290.000-4286.157)*1, sell.ProfitValue, 1e-9)
	require.Len(t, c.Results[m5cPair].WinLong, 1)
	_, err = c.CreateOrderMarket(model.SideTypeSell, m5cPair, 1)
	require.NoError(t, err)
	require.Len(t, c.Results[m5cPair].WinLong, 2)
	asset, _, err := c.Position(m5cPair)
	require.NoError(t, err)
	require.Zero(t, asset)
	require.Zero(t, fake.Positions())

	// a protected long stopped out below its level moved
	orders, err := c.CreateOrderMarketProtected(model.SideTypeBuy, m5cPair, 2, 4280, 4300)
	require.NoError(t, err)
	require.Len(t, orders, 3)
	entry := orders[0]
	tp, sl := m5cLegs(orders[1:])
	require.Equal(t, 4290.260, entry.Price)
	c.UpdateOrders()
	for _, stored := range []model.Order{tp, sl} {
		fresh, err := c.Order(m5cPair, stored.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeNew, fresh.Status)
	}
	moved, err := c.UpdateProtection(sl, 4281)
	require.NoError(t, err)
	require.Equal(t, 4281.0, moved.Price)

	fake.TriggerSL(entry.ExchangeID, 4280.5)
	stopped := m5cSettle(t, c, st, sl.ID, model.OrderStatusTypeFilled)
	require.Equal(t, 4280.5, stopped.Price)
	require.Equal(t, 2.0, stopped.Quantity)
	m5cSettle(t, c, st, tp.ID, model.OrderStatusTypeCanceled)
	require.Len(t, c.Results[m5cPair].LoseLong, 1)
	require.InDelta(t, (4280.5-4290.260)*2, c.Results[m5cPair].LoseLong[0], 1e-9)

	// a limit entry, closed by a stop order: its fill is netted with close-by
	fake.SetPrice(m5cPair, 4275.000, 4275.260)
	limit, err := c.CreateOrderLimit(model.SideTypeBuy, m5cPair, 3, 4270)
	require.NoError(t, err)
	fake.FillPending(limit.ExchangeID)
	filled := m5cSettle(t, c, st, limit.ID, model.OrderStatusTypeFilled)
	require.Equal(t, 4270.0, filled.Price)
	require.Equal(t, 3.0, filled.Quantity)

	stop, err := c.CreateOrderStop(m5cPair, 3, 4260)
	require.NoError(t, err)
	fake.FillPending(stop.ExchangeID)
	closed := m5cSettle(t, c, st, stop.ID, model.OrderStatusTypeFilled)
	require.Equal(t, 4260.0, closed.Price)
	require.Equal(t, 3.0, closed.Quantity)
	require.Len(t, c.Results[m5cPair].LoseLong, 2)
	require.InDelta(t, (4260.0-4270.0)*3, c.Results[m5cPair].LoseLong[1], 1e-9)
	require.Eventually(t, func() bool { return fake.Positions() == 0 }, 5*time.Second, 10*time.Millisecond,
		"the fill netted by close-by")

	// a short protected by an OCO, taken profit
	short, err := c.CreateOrderMarket(model.SideTypeSell, m5cPair, 1)
	require.NoError(t, err)
	require.Equal(t, 4275.000, short.Price)
	legs, err := c.CreateOrderOCO(model.SideTypeBuy, m5cPair, 1, 4265, 4285, 4285)
	require.NoError(t, err)
	require.Len(t, legs, 2)
	ocoTP, ocoSL := m5cLegs(legs)
	fake.TriggerTP(short.ExchangeID)
	profit := m5cSettle(t, c, st, ocoTP.ID, model.OrderStatusTypeFilled)
	require.Equal(t, 4265.0, profit.Price)
	m5cSettle(t, c, st, ocoSL.ID, model.OrderStatusTypeCanceled)
	require.Len(t, c.Results[m5cPair].WinShort, 1)
	require.InDelta(t, 10.0, c.Results[m5cPair].WinShort[0], 1e-9)

	// a pending entry canceled with its legs
	protected, err := c.CreateOrderLimitProtected(model.SideTypeBuy, m5cPair, 1, 4250, 4240, 4300)
	require.NoError(t, err)
	require.NoError(t, c.Cancel(protected[0]))
	for _, stored := range protected {
		m5cSettle(t, c, st, stored.ID, model.OrderStatusTypeCanceled)
	}

	// nothing left pending, nothing left open
	open, err := st.Orders(storage.WithStatusIn(model.OrderStatusTypeNew, model.OrderStatusTypePartiallyFilled,
		model.OrderStatusTypePendingCancel))
	require.NoError(t, err)
	require.Empty(t, open)
	asset, _, err = c.Position(m5cPair)
	require.NoError(t, err)
	require.Zero(t, asset)
	require.Zero(t, fake.NetLots(m5cPair))

	// the pending orders are followed from the book: UpdateOrders adds no
	// request to the ones of the poll
	pendingLimit, err := c.CreateOrderLimit(model.SideTypeBuy, m5cPair, 1, 4200)
	require.NoError(t, err)
	fake.ClearRequests()
	for i := 0; i < 50; i++ {
		c.UpdateOrders()
	}
	// only the poll loop's requests, a pair per poll, whatever the calls
	require.InDelta(t, fake.Requests("GET", "/orders"), fake.Requests("GET", "/positions"), 1)
	require.Zero(t, fake.Requests("GET", "/tick"))
	require.Zero(t, fake.Requests("GET", "/deals"))
	require.Zero(t, fake.Requests("POST", "/order"))
	require.NoError(t, c.Cancel(pendingLimit))
	m5cSettle(t, c, st, pendingLimit.ID, model.OrderStatusTypeCanceled)
}

// TestMT5Controller_ClosesWithTheLegsInPlace closes a protected position at the
// market through the order controller, as the LLM trader does where the exchange
// closes it with its legs in place (service.ProtectedCloser): a close refused
// leaves the position with its levels and its legs stored open, and the close
// done settles them CANCELED, no other side opened.
func TestMT5Controller_ClosesWithTheLegsInPlace(t *testing.T) {
	fake := exchange.NewMT5TestFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m, err := exchange.NewMT5(ctx, fake.Options()...)
	require.NoError(t, err)
	st, err := storage.FromMemory()
	require.NoError(t, err)
	c := order.NewController(ctx, m, st, order.NewOrderFeed())
	require.True(t, c.ClosesProtected(), "delegated to the adapter")

	orders, err := c.CreateOrderMarketProtected(model.SideTypeBuy, m5cPair, 2, 4280, 4300)
	require.NoError(t, err)
	entry := orders[0]
	tp, sl := m5cLegs(orders[1:])

	fake.RefuseNextTrade()
	_, err = c.CreateOrderMarket(model.SideTypeSell, m5cPair, 2)
	require.Error(t, err)
	stop, profit, open := fake.Levels(entry.ExchangeID)
	require.True(t, open)
	require.Equal(t, 4280.0, stop)
	require.Equal(t, 4300.0, profit)
	c.UpdateOrders()
	for _, leg := range []model.Order{tp, sl} {
		m5cSettle(t, c, st, leg.ID, model.OrderStatusTypeNew)
	}

	_, err = c.CreateOrderMarket(model.SideTypeSell, m5cPair, 2)
	require.NoError(t, err)
	for _, leg := range []model.Order{tp, sl} {
		m5cSettle(t, c, st, leg.ID, model.OrderStatusTypeCanceled)
	}
	require.Zero(t, fake.Positions())
	require.Zero(t, fake.NetLots(m5cPair))
}

// R1: a trade request whose answer is lost holds, while the adapter waits for
// its deadline, the orders of its pair only: the order controller follows the
// pending orders, answers its reads and trades the other pairs meanwhile.
func TestMT5Controller_LostAnswerHoldsItsPairOnly(t *testing.T) {
	fake := exchange.NewMT5TestFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m, err := exchange.NewMT5(ctx, fake.Options(exchange.WithMT5Symbols(m5cPair, "BTCUSDm"))...)
	require.NoError(t, err)
	st, err := storage.FromMemory()
	require.NoError(t, err)
	c := order.NewController(ctx, m, st, order.NewOrderFeed())
	limit, err := c.CreateOrderLimit(model.SideTypeBuy, m5cPair, 1, 4000) // followed by UpdateOrders
	require.NoError(t, err)

	fake.FailNextTrade()
	fake.ClearRequests()
	started := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := c.CreateOrderMarket(model.SideTypeBuy, m5cPair, 1)
		done <- err
	}()
	require.Eventually(t, func() bool { return fake.Requests("GET", "/history_orders") > 0 }, time.Second,
		time.Millisecond, "looked for in the account at once, then waiting for its deadline")

	quick := func(what string, fn func()) {
		t.Helper()
		at := time.Now()
		fn()
		require.Less(t, time.Since(at), 400*time.Millisecond, what)
	}
	quick("an order of another pair", func() {
		order, err := c.CreateOrderMarket(model.SideTypeBuy, "BTCUSDm", 0.01)
		require.NoError(t, err)
		require.Equal(t, 0.01, order.Quantity)
	})
	quick("UpdateOrders", c.UpdateOrders)
	quick("the status", func() { c.Status() })
	quick("the position", func() {
		_, _, err := c.Position(m5cPair)
		require.NoError(t, err)
	})
	quick("the order", func() {
		got, err := c.Order(m5cPair, limit.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeNew, got.Status)
	})
	select {
	case err := <-done:
		t.Fatalf("returned before its deadline: %v", err)
	default:
	}

	err = <-done
	require.ErrorContains(t, err, "not executed, absent from the account after its deadline")
	require.GreaterOrEqual(t, time.Since(started), time.Second)
	require.Zero(t, fake.NetLots(m5cPair))
}

// TestMT5ControllerRestart stops a controller with a protected entry pending,
// fills it and triggers its take profit while the bot is off, then starts a new
// adapter and controller on the same storage: the orders of the previous run
// settle as they happened.
func TestMT5ControllerRestart(t *testing.T) {
	fake := exchange.NewMT5TestFake(t)
	st, err := storage.FromMemory()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	first, err := exchange.NewMT5(ctx, fake.Options()...)
	require.NoError(t, err)
	c := order.NewController(ctx, first, st, order.NewOrderFeed())
	protected, err := c.CreateOrderLimitProtected(model.SideTypeBuy, m5cPair, 2, 4250, 4240, 4300)
	require.NoError(t, err)
	tp, sl := m5cLegs(protected[1:])
	cancel()

	fake.FillPending(protected[0].ExchangeID)
	fake.TriggerTP(protected[0].ExchangeID)

	ctx, cancel = context.WithCancel(context.Background())
	t.Cleanup(cancel)
	second, err := exchange.NewMT5(ctx, fake.Options()...)
	require.NoError(t, err)
	c = order.NewController(ctx, second, st, order.NewOrderFeed())
	c.Start()
	t.Cleanup(c.Stop)

	require.Eventually(t, func() bool {
		orders, err := st.Orders(storage.WithStatusIn(model.OrderStatusTypeNew))
		return err == nil && len(orders) == 0
	}, 10*time.Second, 20*time.Millisecond, "the orders of the previous run settled")
	orders, err := st.Orders()
	require.NoError(t, err)
	statuses := make(map[int64]model.Order)
	for _, stored := range orders {
		statuses[stored.ID] = *stored
	}
	require.Equal(t, model.OrderStatusTypeFilled, statuses[protected[0].ID].Status)
	require.Equal(t, 4250.0, statuses[protected[0].ID].Price)
	require.Equal(t, model.OrderStatusTypeFilled, statuses[tp.ID].Status)
	require.Equal(t, 4300.0, statuses[tp.ID].Price)
	require.Equal(t, 2.0, statuses[tp.ID].Quantity)
	require.Equal(t, model.OrderStatusTypeCanceled, statuses[sl.ID].Status)
}

// m5cStored returns the stored order id.
func m5cStored(t *testing.T, st storage.Storage, id int64) model.Order {
	t.Helper()
	orders, err := st.Orders()
	require.NoError(t, err)
	for _, stored := range orders {
		if stored.ID == id {
			return *stored
		}
	}
	t.Fatalf("no order %d stored", id)
	return model.Order{}
}

// B6: a protected long closed by its stop loss while the history of the
// terminal lags beyond mt5ResolveWait: its legs stay NEW in the order
// controller while the exits are awaited (ExitsAwaited), never settled
// CANCELED meanwhile, then, once the history lists the exit, the stop loss
// FILLED at its price with the loss in the results of the controller, and the
// loss booked by ClosedProfit for the position the stop loss closed
// (FillPositions), as the daily limits read them.
func TestMT5Controller_StopLossHitHistoryLate(t *testing.T) {
	const wait = 200 * time.Millisecond
	fake := exchange.NewMT5TestFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m, err := exchange.NewMT5(ctx, fake.Options(exchange.MT5TestHistoryWaits(wait, time.Minute))...)
	require.NoError(t, err)
	st, err := storage.FromMemory()
	require.NoError(t, err)
	c := order.NewController(ctx, m, st, order.NewOrderFeed())

	orders, err := c.CreateOrderMarketProtected(model.SideTypeBuy, m5cPair, 2, 4280, 4300)
	require.NoError(t, err)
	require.Len(t, orders, 3)
	entry := orders[0]
	tp, sl := m5cLegs(orders[1:])
	_, awaited := m.ExitsAwaited(m5cPair)
	require.False(t, awaited)

	fake.HoldHistory()
	fake.TriggerSL(entry.ExchangeID, 4280.5)
	require.Eventually(t, func() bool { _, awaited := m.ExitsAwaited(m5cPair); return awaited }, 5*time.Second,
		5*time.Millisecond, "the position out of the book, its exit awaited")
	for deadline := time.Now().Add(5 * wait); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		c.UpdateOrders()
		for _, leg := range []model.Order{tp, sl} {
			require.Equal(t, model.OrderStatusTypeNew, m5cStored(t, st, leg.ID).Status,
				"the leg %s settled before the exit shows", leg.Type)
		}
	}
	_, known := m.ClosedProfit(sl, entry)
	require.False(t, known)
	_, awaited = m.ExitsAwaited(m5cPair)
	require.True(t, awaited, "still awaited past mt5ResolveWait")

	fake.ReleaseHistory()
	stopped := m5cSettle(t, c, st, sl.ID, model.OrderStatusTypeFilled)
	require.Equal(t, 4280.5, stopped.Price)
	require.Equal(t, 2.0, stopped.Quantity)
	m5cSettle(t, c, st, tp.ID, model.OrderStatusTypeCanceled)
	loss := (4280.5 - entry.Price) * 2
	require.Negative(t, loss)
	require.Len(t, c.Results[m5cPair].LoseLong, 1)
	require.InDelta(t, loss, c.Results[m5cPair].LoseLong[0], 1e-9)
	require.InDelta(t, loss, m5cStored(t, st, sl.ID).ProfitValue, 1e-9, "stored with the stop loss")

	opened, closed, ok := m.FillPositions(stopped)
	require.True(t, ok)
	require.Zero(t, opened)
	require.Equal(t, []int64{entry.ExchangeID}, closed, "the stop loss closes the position of the entry")
	booked, known := m.ClosedProfit(stopped, entry)
	require.True(t, known, "booked once the history lists the exit")
	require.InDelta(t, loss, booked, 0.01)
	_, awaited = m.ExitsAwaited(m5cPair)
	require.False(t, awaited)
}

// B6: the bot restarted while the exit of a position closed by its stop loss
// isn't in the history yet: the new adapter awaits it from its entry, as the
// one that saw it close would (ExitsAwaited, from the start), its legs NEW in
// the order controller rather than CANCELED, then the stop loss FILLED with
// the loss once the history lists the exit. A position closed with its exits
// in the history, and one open, aren't awaited.
func TestMT5Controller_RestartDuringHistoryLag(t *testing.T) {
	const wait = 200 * time.Millisecond
	fake := exchange.NewMT5TestFake(t)
	st, err := storage.FromMemory()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	first, err := exchange.NewMT5(ctx, fake.Options(exchange.MT5TestHistoryWaits(wait, time.Minute))...)
	require.NoError(t, err)
	c := order.NewController(ctx, first, st, order.NewOrderFeed())
	_, err = c.CreateOrderMarket(model.SideTypeBuy, m5cPair, 1) // closed with its exit in the history
	require.NoError(t, err)
	_, err = c.CreateOrderMarket(model.SideTypeSell, m5cPair, 1)
	require.NoError(t, err)
	orders, err := c.CreateOrderMarketProtected(model.SideTypeBuy, m5cPair, 2, 4280, 4300)
	require.NoError(t, err)
	entry := orders[0]
	tp, sl := m5cLegs(orders[1:])
	_, err = first.CreateOrderLimitProtected(model.SideTypeBuy, m5cPair, 1, 4200, 4100, 4300) // pending, not awaited
	require.NoError(t, err)

	fake.HoldHistory()
	fake.TriggerSL(entry.ExchangeID, 4280.5)
	require.Eventually(t, func() bool { _, awaited := first.ExitsAwaited(m5cPair); return awaited }, 5*time.Second,
		5*time.Millisecond)
	cancel()

	restart := time.Now()
	ctx, cancel = context.WithCancel(context.Background())
	t.Cleanup(cancel)
	second, err := exchange.NewMT5(ctx, fake.Options(exchange.MT5TestHistoryWaits(wait, time.Minute))...)
	require.NoError(t, err)
	since, awaited := second.ExitsAwaited(m5cPair)
	require.True(t, awaited, "awaited from the start")
	require.False(t, since.Before(restart), "its close noticed at the start: %s, restarted %s", since, restart)
	c = order.NewController(ctx, second, st, order.NewOrderFeed())
	c.Start() // the stored orders followed again
	t.Cleanup(c.Stop)
	for deadline := time.Now().Add(4 * wait); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		c.UpdateOrders()
		for _, leg := range []model.Order{tp, sl} {
			require.Equal(t, model.OrderStatusTypeNew, m5cStored(t, st, leg.ID).Status,
				"the leg %s settled before the exit shows", leg.Type)
		}
	}
	_, known := second.ClosedProfit(m5cStored(t, st, sl.ID), entry)
	require.False(t, known)
	_, awaited = second.ExitsAwaited(m5cPair)
	require.True(t, awaited, "still awaited past mt5ResolveWait")

	fake.ReleaseHistory()
	stopped := m5cSettle(t, c, st, sl.ID, model.OrderStatusTypeFilled)
	require.Equal(t, 4280.5, stopped.Price)
	m5cSettle(t, c, st, tp.ID, model.OrderStatusTypeCanceled)
	loss := (4280.5 - entry.Price) * 2
	require.InDelta(t, loss, stopped.ProfitValue, 1e-9)
	booked, known := second.ClosedProfit(stopped, entry)
	require.True(t, known, "booked once the history lists the exit")
	require.InDelta(t, loss, booked, 0.01)
	require.Eventually(t, func() bool { _, awaited := second.ExitsAwaited(m5cPair); return !awaited }, 5*time.Second,
		5*time.Millisecond, "nothing awaited any more")
}
