package exchange_test

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

// R17g, end to end: a limit order canceled after a part of it was executed is
// settled by the order controller as a fill of that part, the position of the
// bot what was executed.
func TestMT5Controller_CanceledAfterAPartialFill(t *testing.T) {
	fake := exchange.NewMT5TestFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m, err := exchange.NewMT5(ctx, fake.Options()...)
	require.NoError(t, err)
	st, err := storage.FromMemory()
	require.NoError(t, err)
	c := order.NewController(ctx, m, st, order.NewOrderFeed())

	limit, err := c.CreateOrderLimit(model.SideTypeBuy, m5cPair, 3, 4270)
	require.NoError(t, err)
	fake.FillPendingPartly(limit.ExchangeID, 0.01)
	m5cSettle(t, c, st, limit.ID, model.OrderStatusTypePartiallyFilled)

	require.NoError(t, c.Cancel(limit))
	filled := m5cSettle(t, c, st, limit.ID, model.OrderStatusTypeFilled)
	require.Equal(t, 1.0, filled.Quantity, "what was executed")
	require.Equal(t, 4270.0, filled.Price)
	asset, _, err := c.Position(m5cPair)
	require.NoError(t, err)
	require.Equal(t, 1.0, asset)
	require.InDelta(t, 0.01, fake.NetLots(m5cPair), 1e-9)
}

// m5cPartialSetup is an order controller on an adapter on the fake that polls
// only when the test asks it (exchange.MT5TestPoll), with a protected limit
// order of 3 units at 4270, 1 of it executed since the last snapshot.
func m5cPartialSetup(t *testing.T) (*exchange.MT5TestFake, *exchange.MT5, *order.Controller, storage.Storage,
	[]model.Order) {
	t.Helper()
	fake := exchange.NewMT5TestFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m, err := exchange.NewMT5(ctx, fake.Options(exchange.WithMT5PollInterval(time.Hour))...)
	require.NoError(t, err)
	st, err := storage.FromMemory()
	require.NoError(t, err)
	c := order.NewController(ctx, m, st, order.NewOrderFeed())
	orders, err := c.CreateOrderLimitProtected(model.SideTypeBuy, m5cPair, 3, 4270, 4260, 4300)
	require.NoError(t, err)
	fake.FillPendingPartly(orders[0].ExchangeID, 0.01) // unseen by the book
	return fake, m, c, st, orders
}

// m5cPollSettle polls the adapter and settles the orders until the order id
// is stored with status.
func m5cPollSettle(t *testing.T, m *exchange.MT5, c *order.Controller, st storage.Storage, id int64,
	status model.OrderStatusType) model.Order {
	t.Helper()
	for range 20 {
		require.NoError(t, exchange.MT5TestPoll(m))
		c.UpdateOrders()
		if stored := m5cStored(t, st, id); stored.Status == status {
			return stored
		}
	}
	t.Fatalf("the order %d never %s: %s", id, status, m5cStored(t, st, id).Status)
	return model.Order{}
}

// R17g, end to end: the ticker of the controller reads the order while Cancel
// reads the account after the removal: PENDING_CANCEL, never CANCELED (final,
// the part executed never settled), its legs followed; then FILLED of the
// part executed, the position of the bot what was executed.
func TestMT5Controller_ReadWhileCancelChecksTheAccount(t *testing.T) {
	fake, m, c, st, orders := m5cPartialSetup(t)
	entry := orders[0]
	tp, sl := m5cLegs(orders[1:])

	reached, release := fake.HoldNext("GET", "/positions", "symbol")
	canceled := make(chan error, 1)
	go func() { canceled <- c.Cancel(entry) }()
	<-reached
	c.UpdateOrders() // the ticker of the controller, meanwhile
	require.Equal(t, model.OrderStatusTypePendingCancel, m5cStored(t, st, entry.ID).Status)
	release()
	require.NoError(t, <-canceled)

	filled := m5cPollSettle(t, m, c, st, entry.ID, model.OrderStatusTypeFilled)
	require.Equal(t, 1.0, filled.Quantity, "what was executed")
	for _, leg := range []model.Order{tp, sl} {
		require.Equal(t, model.OrderStatusTypeNew, m5cStored(t, st, leg.ID).Status, "its position protected")
	}
	asset, _, err := c.Position(m5cPair)
	require.NoError(t, err)
	require.Equal(t, 1.0, asset)
	require.InDelta(t, 0.01, fake.NetLots(m5cPair), 1e-9)
}

// R17g, end to end: the account unread after the removal: PENDING_CANCEL with
// its legs, then FILLED of the part executed once read, and the stop loss of
// its position settled by the controller, which settled its entry.
func TestMT5Controller_AccountUnreadAfterTheRemoval(t *testing.T) {
	fake, m, c, st, orders := m5cPartialSetup(t)
	entry := orders[0]
	tp, sl := m5cLegs(orders[1:])

	fake.FailReads("/positions")
	require.NoError(t, c.Cancel(entry))
	c.UpdateOrders()
	require.Equal(t, model.OrderStatusTypePendingCancel, m5cStored(t, st, entry.ID).Status)
	for _, leg := range []model.Order{tp, sl} {
		require.Equal(t, model.OrderStatusTypeNew, m5cStored(t, st, leg.ID).Status, "kept")
	}
	fake.ClearFailedReads("/positions")

	filled := m5cPollSettle(t, m, c, st, entry.ID, model.OrderStatusTypeFilled)
	require.Equal(t, 1.0, filled.Quantity)
	asset, _, err := c.Position(m5cPair)
	require.NoError(t, err)
	require.Equal(t, 1.0, asset)

	fake.TriggerSL(entry.ExchangeID, 0)
	stopped := m5cPollSettle(t, m, c, st, sl.ID, model.OrderStatusTypeFilled)
	require.Equal(t, 1.0, stopped.Quantity)
	require.Equal(t, model.OrderStatusTypeCanceled, m5cPollSettle(t, m, c, st, tp.ID,
		model.OrderStatusTypeCanceled).Status)
	asset, _, err = c.Position(m5cPair)
	require.NoError(t, err)
	require.Zero(t, asset)
}
