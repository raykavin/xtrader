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
