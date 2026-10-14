package order

import (
	"context"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
)

// A position closed outside the bot while it runs is discarded where it was:
// the discard kept after the last fill of its pair, the replay of a restart
// drops it again there, the trades after it settled on their own as they were,
// not averaged with it.
func TestController_DiscardReplayedAfterARestart(t *testing.T) {
	c, exch, stop, target := runningWithLegs(t)
	db := c.storage
	require.NoError(t, db.UpdateOrder(fillNow(10, model.SideTypeBuy, 100, 1))) // the entry, as stored

	delete(exch.held, "BTCUSDm") // closed by hand
	for _, leg := range []model.Order{stop, target} {
		leg.Status = model.OrderStatusTypeCanceled
		exch.setOrder(leg)
		c.UpdateOrders()
	}
	require.NotContains(t, c.position, "BTCUSDm", "discarded")
	discards, err := db.(storage.DiscardStorage).Discards()
	require.NoError(t, err)
	require.Len(t, discards, 1)
	require.Equal(t, "BTCUSDm", discards[0].Pair)
	require.Equal(t, model.SideTypeBuy, discards[0].Side)
	require.Equal(t, 1.0, discards[0].Quantity)
	require.Equal(t, int64(10), discards[0].AfterID, "after the entry of the position")
	require.False(t, discards[0].At.IsZero())

	// the next trade, on the same side: bought at 200, sold at 210, 5 %
	entry := fillNow(20, model.SideTypeBuy, 200, 1)
	close := fillNow(21, model.SideTypeSell, 210, 1)
	entry.UpdatedAt = entry.UpdatedAt.Add(time.Hour)
	close.UpdatedAt = close.UpdatedAt.Add(2 * time.Hour)
	require.NoError(t, db.UpdateOrder(entry))
	require.NoError(t, db.UpdateOrder(close))
	c.mtx.Lock()
	c.processTrade(entry)
	c.processTrade(close)
	c.mtx.Unlock()
	require.NoError(t, db.UpdateOrder(close)) // its profit, as the controller stores it
	require.InDelta(t, 0.05, close.Profit, 1e-9)

	// a restart: the stored fills replayed, the discard at its place
	hook := logtest.NewGlobal()
	defer hook.Reset()
	exch.held = map[string]model.Order{} // flat
	again := NewController(context.Background(), exch, db, NewOrderFeed())
	require.NotContains(t, again.position, "BTCUSDm")
	require.InDeltaSlice(t, []float64{10}, again.Results["BTCUSDm"].Win(), 1e-9, "not averaged with the one gone")
	require.True(t, logged(hook, log.InfoLevel, "BTCUSDm: the position BUY 1 at 100 discarded after the fill 10"))
	require.False(t, logged(hook, log.WarnLevel, "is not held at the exchange"), "nothing left to discard")
	stored := storedByID(t, db, close.ID)
	require.InDelta(t, 0.05, stored.Profit, 1e-9, "the profit stored kept")
	require.InDelta(t, 10, stored.ProfitValue, 1e-9)
}

// The profits a replay without the discards stored averaged with a position
// gone (before the discards were kept) are written back right once the
// discard is there: the replay settles the trades as they were.
func TestController_ReplayWritesBackTheProfitsOfADiscard(t *testing.T) {
	ctx := context.Background()
	db, err := storage.FromMemory()
	require.NoError(t, err)
	gone := storedFill(t, db, 0, model.SideTypeBuy, model.OrderTypeMarket, model.OrderStatusTypeFilled, 100, 1, nil)
	storedFill(t, db, 10, model.SideTypeBuy, model.OrderTypeMarket, model.OrderStatusTypeFilled, 200, 1, nil)
	close := storedFill(t, db, 20, model.SideTypeSell, model.OrderTypeStopLoss, model.OrderStatusTypeFilled, 190, 1,
		nil)
	// as an earlier replay stored it: settled against the average of both, 150
	close.ProfitValue, close.Profit = 40, 40.0/150
	require.NoError(t, db.UpdateOrder(&close))
	require.NoError(t, db.(storage.DiscardStorage).CreateDiscard(model.Discard{Pair: "BTCUSDm",
		Side: model.SideTypeBuy, Quantity: 1, AfterID: gone.ID, At: reconcileAt.Add(5 * 60e9)}))

	c := NewController(ctx, newHeldExchange(ctx), db, NewOrderFeed())
	require.NotContains(t, c.position, "BTCUSDm", "the position gone dropped, the second one closed")
	require.InDeltaSlice(t, []float64{-10}, c.Results["BTCUSDm"].Lose(), 1e-9, "sold at 190 what was bought at 200")
	stored := storedByID(t, db, close.ID)
	require.InDelta(t, -10, stored.ProfitValue, 1e-9, "written back")
	require.InDelta(t, -0.05, stored.Profit, 1e-9)
}

// A position replayed that the exchange doesn't hold at the start is discarded
// after the last fill of its pair, kept: the next restart drops it there, not
// warned again, and keeps it once.
func TestController_RestoreKeepsTheDiscardOfAPositionNotHeld(t *testing.T) {
	ctx := context.Background()
	db, err := storage.FromMemory()
	require.NoError(t, err)
	fill := storedFill(t, db, 0, model.SideTypeBuy, model.OrderTypeMarket, model.OrderStatusTypeFilled, 100, 1, nil)
	exch := newHeldExchange(ctx) // flat

	NewController(ctx, exch, db, NewOrderFeed())
	discards, err := db.(storage.DiscardStorage).Discards()
	require.NoError(t, err)
	require.Equal(t, []model.Discard{{Pair: "BTCUSDm", Side: model.SideTypeBuy, Quantity: 1, AfterID: fill.ID,
		At: discards[0].At}}, discards)

	hook := logtest.NewGlobal()
	defer hook.Reset()
	again := NewController(ctx, exch, db, NewOrderFeed())
	require.NotContains(t, again.position, "BTCUSDm")
	require.False(t, logged(hook, log.WarnLevel, "is not held at the exchange"), "dropped at its place")
	discards, err = db.(storage.DiscardStorage).Discards()
	require.NoError(t, err)
	require.Len(t, discards, 1, "kept once")
}

// A discard of a side the position replayed there doesn't have is warned and
// left aside, the position kept as replayed.
func TestController_ReplayLeavesADiscardOfTheOtherSide(t *testing.T) {
	ctx := context.Background()
	db, err := storage.FromMemory()
	require.NoError(t, err)
	fill := storedFill(t, db, 0, model.SideTypeBuy, model.OrderTypeMarket, model.OrderStatusTypeFilled, 100, 1, nil)
	require.NoError(t, db.(storage.DiscardStorage).CreateDiscard(model.Discard{Pair: "BTCUSDm",
		Side: model.SideTypeSell, Quantity: 1, AfterID: fill.ID}))
	exch := newHeldExchange(ctx)
	exch.held["BTCUSDm"] = model.Order{Side: model.SideTypeBuy, Quantity: 1}

	hook := logtest.NewGlobal()
	defer hook.Reset()
	c := NewController(ctx, exch, db, NewOrderFeed())
	require.Contains(t, c.position, "BTCUSDm", "kept, held")
	require.True(t, logged(hook, log.WarnLevel, "is of the other side"))
}

// storedByID returns the order of id of db.
func storedByID(t *testing.T, db storage.Storage, id int64) model.Order {
	t.Helper()
	orders, err := db.Orders()
	require.NoError(t, err)
	for _, order := range orders {
		if order.ID == id {
			return *order
		}
	}
	t.Fatalf("no order %d", id)
	return model.Order{}
}
