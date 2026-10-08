package order

import (
	"context"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
)

// runningWithLegs returns a controller of a bot running, holding a position of
// BTCUSDm bought at 100 with its stop loss and its take profit open, held at
// the exchange too.
func runningWithLegs(t *testing.T) (*Controller, *heldExchange, model.Order, model.Order) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.FromMemory()
	require.NoError(t, err)
	exch := newHeldExchange(ctx)
	c := NewController(ctx, exch, db, NewOrderFeed())

	exch.held["BTCUSDm"] = model.Order{Side: model.SideTypeBuy, Quantity: 1}
	c.mtx.Lock()
	c.processTrade(fillNow(10, model.SideTypeBuy, 100, 1))
	c.mtx.Unlock()
	group := int64(7)
	stop := storedFill(t, db, 1, model.SideTypeSell, model.OrderTypeStopLoss, model.OrderStatusTypeNew, 90, 1, &group)
	target := storedFill(t, db, 2, model.SideTypeSell, model.OrderTypeLimitMaker, model.OrderStatusTypeNew, 120, 1,
		&group)
	c.loadPendingOrders() // as Start does
	exch.setOrder(stop)
	exch.setOrder(target)
	require.Contains(t, c.position, "BTCUSDm")
	return c, exch, stop, target
}

// R11, with the bot running: a position closed outside the bot, eg: by hand in
// the terminal or by a stop out, leaves no fill, its legs canceled; once they
// end, the exchange holding none, it is discarded, without any order, rather
// than averaged with the next entry of its side.
func TestController_PositionClosedOutsideWhileRunningIsDiscarded(t *testing.T) {
	c, exch, stop, target := runningWithLegs(t)
	hook := logtest.NewGlobal()
	defer hook.Reset()

	delete(exch.held, "BTCUSDm") // closed by hand
	stop.Status = model.OrderStatusTypeCanceled
	exch.setOrder(stop)
	c.UpdateOrders()
	require.Contains(t, c.position, "BTCUSDm", "its take profit may close it still")

	target.Status = model.OrderStatusTypeCanceled
	exch.setOrder(target)
	c.UpdateOrders()
	require.NotContains(t, c.position, "BTCUSDm", "closed outside the bot: discarded")
	require.True(t, logged(hook, log.WarnLevel, "BTCUSDm: the position (BUY 1 at 100) is not held at the exchange "+
		"any more"))
	require.Empty(t, c.Results["BTCUSDm"].Win())
	require.Empty(t, c.Results["BTCUSDm"].Lose())

	// the next trade settled on its own: bought at 200, sold at 210, 5 %
	c.mtx.Lock()
	c.processTrade(fillNow(20, model.SideTypeBuy, 200, 1))
	sell := fillNow(21, model.SideTypeSell, 210, 1)
	c.processTrade(sell)
	c.mtx.Unlock()
	require.InDelta(t, 0.05, sell.Profit, 1e-9, "not averaged with the position closed outside")
	require.InDeltaSlice(t, []float64{10}, c.Results["BTCUSDm"].Win(), 1e-9)
}

// R11, with the bot running: legs canceled with the position held, eg: by the
// bot moving its protection, leave it as it is; a filled one closes it with
// its profit, as before; an exchange that doesn't know is never asked.
func TestController_PositionHeldKeptAsItsLegsEnd(t *testing.T) {
	t.Run("held", func(t *testing.T) {
		c, exch, stop, target := runningWithLegs(t)
		hook := logtest.NewGlobal()
		defer hook.Reset()
		for _, leg := range []model.Order{stop, target} {
			leg.Status = model.OrderStatusTypeCanceled
			exch.setOrder(leg)
		}
		c.UpdateOrders()
		require.Equal(t, 1.0, c.position["BTCUSDm"].Quantity, "held: kept")
		require.False(t, logged(hook, log.WarnLevel, "BTCUSDm"))
		require.False(t, logged(hook, log.ErrorLevel, "BTCUSDm"))
	})

	t.Run("its stop loss filled", func(t *testing.T) {
		c, exch, stop, target := runningWithLegs(t)
		delete(exch.held, "BTCUSDm")
		stop.Status = model.OrderStatusTypeFilled
		exch.setOrder(stop)
		target.Status = model.OrderStatusTypeCanceled
		exch.setOrder(target)
		c.UpdateOrders()
		require.NotContains(t, c.position, "BTCUSDm")
		require.InDeltaSlice(t, []float64{-10}, c.Results["BTCUSDm"].Lose(), 1e-9, "its loss")
	})

	t.Run("unknown", func(t *testing.T) {
		c, exch, stop, target := runningWithLegs(t)
		exch.known = false
		delete(exch.held, "BTCUSDm")
		for _, leg := range []model.Order{stop, target} {
			leg.Status = model.OrderStatusTypeCanceled
			exch.setOrder(leg)
		}
		c.UpdateOrders()
		require.Contains(t, c.position, "BTCUSDm", "unknown: kept")
	})
}

// R11, with the bot running: the position isn't checked while an order of its
// pair is in flight, eg: an exit at the market filled at the exchange, its
// legs canceled with it, whose fill isn't settled yet (it would then open a
// position on the other side); checked by the next update once it is.
func TestController_PositionNotCheckedWithAnOrderInFlight(t *testing.T) {
	c, exch, stop, target := runningWithLegs(t)
	delete(exch.held, "BTCUSDm")
	for _, leg := range []model.Order{stop, target} {
		leg.Status = model.OrderStatusTypeCanceled
		exch.setOrder(leg)
	}
	unlock := c.lockPair("BTCUSDm") // an order of the pair in flight
	asked := exch.asked
	c.UpdateOrders()
	require.Contains(t, c.position, "BTCUSDm", "not checked with an order in flight")
	require.Equal(t, asked, exch.asked)
	require.Empty(t, c.pending, "its legs ended")
	unlock()

	c.UpdateOrders()
	require.NotContains(t, c.position, "BTCUSDm", "checked once none is in flight")

	// the exit of the in flight case settled first: closed by it, as before
	c, exch, stop, target = runningWithLegs(t)
	delete(exch.held, "BTCUSDm")
	for _, leg := range []model.Order{stop, target} {
		leg.Status = model.OrderStatusTypeCanceled
		exch.setOrder(leg)
	}
	unlock = c.lockPair("BTCUSDm")
	c.UpdateOrders()
	c.mtx.Lock()
	exit := fillNow(30, model.SideTypeSell, 110, 1)
	c.processTrade(exit)
	c.mtx.Unlock()
	unlock()
	c.UpdateOrders()
	require.NotContains(t, c.position, "BTCUSDm", "closed by its exit, none opened on the other side")
	require.InDelta(t, 0.1, exit.Profit, 1e-9)
	require.InDeltaSlice(t, []float64{10}, c.Results["BTCUSDm"].Win(), 1e-9)
}
