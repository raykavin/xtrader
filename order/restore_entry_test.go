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

// R11: a pending entry of the bot (limit or stop) filled while it was off
// leaves at the exchange a position the storage doesn't tell yet, or a larger
// one: its fill comes as Start loads the open orders, so the restart warns
// rather than logging an error, and the position is then the one held.
func TestController_RestoreWithAnEntryFilledWhileOff(t *testing.T) {
	ctx := context.Background()

	t.Run("no position stored", func(t *testing.T) {
		db, err := storage.FromMemory()
		require.NoError(t, err)
		storedFill(t, db, 0, model.SideTypeBuy, model.OrderTypeMarket, model.OrderStatusTypeFilled, 100, 1, nil)
		storedFill(t, db, 1, model.SideTypeSell, model.OrderTypeMarket, model.OrderStatusTypeFilled, 110, 1, nil)
		entry := storedFill(t, db, 2, model.SideTypeBuy, model.OrderTypeLimit, model.OrderStatusTypeNew, 95, 0.5, nil)
		exch := newHeldExchange(ctx)
		exch.held["BTCUSDm"] = model.Order{Side: model.SideTypeBuy, Quantity: 0.5}

		hook := logtest.NewGlobal()
		defer hook.Reset()
		c := NewController(ctx, exch, db, NewOrderFeed())
		require.False(t, logged(hook, log.ErrorLevel, "BTCUSDm"), "an error for an entry filled while off")
		require.True(t, logged(hook, log.WarnLevel, "BTCUSDm: the exchange holds a position (BUY 0.5) the storage "+
			"doesn't tell yet: an open order of the bot on its side"))

		c.loadPendingOrders() // as Start does
		entry.Status = model.OrderStatusTypeFilled
		exch.setOrder(entry)
		c.UpdateOrders()
		require.Equal(t, model.SideTypeBuy, c.position["BTCUSDm"].Side)
		require.Equal(t, 0.5, c.position["BTCUSDm"].Quantity, "the position held, once its fill came")
	})

	t.Run("a position stored", func(t *testing.T) {
		db, err := storage.FromMemory()
		require.NoError(t, err)
		storedFill(t, db, 0, model.SideTypeBuy, model.OrderTypeMarket, model.OrderStatusTypeFilled, 100, 1, nil)
		entry := storedFill(t, db, 1, model.SideTypeBuy, model.OrderTypeLimit, model.OrderStatusTypeNew, 95, 0.5, nil)
		exch := newHeldExchange(ctx)
		exch.held["BTCUSDm"] = model.Order{Side: model.SideTypeBuy, Quantity: 1.5}

		hook := logtest.NewGlobal()
		defer hook.Reset()
		c := NewController(ctx, exch, db, NewOrderFeed())
		require.False(t, logged(hook, log.ErrorLevel, "BTCUSDm"), "an error for an entry filled while off")
		require.True(t, logged(hook, log.WarnLevel, "BTCUSDm: the position replayed from the storage (BUY 1 at 100) "+
			"is smaller than the one held at the exchange (BUY 1.5)"))
		require.Equal(t, 1.0, c.position["BTCUSDm"].Quantity, "kept")

		c.loadPendingOrders()
		entry.Status = model.OrderStatusTypeFilled
		exch.setOrder(entry)
		c.UpdateOrders()
		require.Equal(t, 1.5, c.position["BTCUSDm"].Quantity, "the position held, once its fill came")
	})
}

// R11: what the open orders of the storage don't explain is still an error:
// none on the side held, too small, or only on the other side, eg: the legs.
func TestController_RestoreUnexplainedPositionIsAnError(t *testing.T) {
	ctx := context.Background()
	group := int64(7)
	cases := []struct {
		name   string
		stored func(t *testing.T, db storage.Storage)
		held   model.Order
		text   string
	}{
		{"no open order", func(t *testing.T, db storage.Storage) {
			storedFill(t, db, 1, model.SideTypeSell, model.OrderTypeMarket, model.OrderStatusTypeFilled, 110, 1, nil)
		}, model.Order{Side: model.SideTypeBuy, Quantity: 0.5}, "the exchange holds a position (BUY 0.5) the storage " +
			"doesn't tell:"},
		{"an entry on the other side", func(t *testing.T, db storage.Storage) {
			storedFill(t, db, 1, model.SideTypeSell, model.OrderTypeMarket, model.OrderStatusTypeFilled, 110, 1, nil)
			storedFill(t, db, 2, model.SideTypeSell, model.OrderTypeLimit, model.OrderStatusTypeNew, 120, 0.5, nil)
		}, model.Order{Side: model.SideTypeBuy, Quantity: 0.5}, "the exchange holds a position (BUY 0.5) the storage " +
			"doesn't tell:"},
		{"an entry too small", func(t *testing.T, db storage.Storage) {
			storedFill(t, db, 2, model.SideTypeBuy, model.OrderTypeLimit, model.OrderStatusTypeNew, 95, 0.2, nil)
		}, model.Order{Side: model.SideTypeBuy, Quantity: 1.5}, "the position replayed from the storage (BUY 1 at " +
			"100) differs from the one held at the exchange (BUY 1.5)"},
		{"its legs only", func(t *testing.T, db storage.Storage) {
			storedFill(t, db, 2, model.SideTypeSell, model.OrderTypeStopLoss, model.OrderStatusTypeNew, 90, 1, &group)
		}, model.Order{Side: model.SideTypeBuy, Quantity: 1.5}, "the position replayed from the storage (BUY 1 at " +
			"100) differs from the one held at the exchange (BUY 1.5)"},
		{"held on the other side", func(t *testing.T, db storage.Storage) {
			storedFill(t, db, 2, model.SideTypeBuy, model.OrderTypeLimit, model.OrderStatusTypeNew, 95, 1, nil)
		}, model.Order{Side: model.SideTypeSell, Quantity: 1}, "the position replayed from the storage (BUY 1 at " +
			"100) differs from the one held at the exchange (SELL 1)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := storage.FromMemory()
			require.NoError(t, err)
			storedFill(t, db, 0, model.SideTypeBuy, model.OrderTypeMarket, model.OrderStatusTypeFilled, 100, 1, nil)
			tc.stored(t, db)
			exch := newHeldExchange(ctx)
			exch.held["BTCUSDm"] = tc.held

			hook := logtest.NewGlobal()
			defer hook.Reset()
			NewController(ctx, exch, db, NewOrderFeed())
			require.True(t, logged(hook, log.ErrorLevel, "BTCUSDm: "+tc.text), "logged: %v", hook.AllEntries())
		})
	}
}
