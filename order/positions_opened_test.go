package order

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
)

// B35: the openings of the positions of a pair, replayed from the storage on
// the start, a reversal included, and the ones of this run.
func TestController_PositionsOpened(t *testing.T) {
	ctx := context.Background()
	db, err := storage.FromMemory()
	require.NoError(t, err)
	storedFill(t, db, 0, model.SideTypeBuy, model.OrderTypeMarket, model.OrderStatusTypeFilled, 100, 1, nil)
	storedFill(t, db, 1, model.SideTypeSell, model.OrderTypeMarket, model.OrderStatusTypeFilled, 101, 1, nil)
	storedFill(t, db, 2, model.SideTypeSell, model.OrderTypeMarket, model.OrderStatusTypeFilled, 102, 1, nil)
	storedFill(t, db, 3, model.SideTypeBuy, model.OrderTypeMarket, model.OrderStatusTypeFilled, 101, 2, nil) // reversal
	storedFill(t, db, 4, model.SideTypeBuy, model.OrderTypeMarket, model.OrderStatusTypeFilled, 101, 1, nil) // added
	exch := newHeldExchange(ctx)
	exch.held["BTCUSDm"] = model.Order{Side: model.SideTypeBuy, Quantity: 2}

	c := NewController(ctx, exch, db, NewOrderFeed())
	minute := func(m int) time.Time { return reconcileAt.Add(time.Duration(m) * time.Minute) }
	require.Equal(t, 3, c.PositionsOpened("BTCUSDm", minute(0)))
	require.Equal(t, 2, c.PositionsOpened("BTCUSDm", minute(1)), "the short and the reversal")
	require.Equal(t, 1, c.PositionsOpened("BTCUSDm", minute(3)), "the reversal")
	require.Zero(t, c.PositionsOpened("BTCUSDm", minute(4)), "an addition opens none")
	require.Zero(t, c.PositionsOpened("ETHUSDm", time.Time{}))

	c.mtx.Lock()
	c.processTrade(fillNow(10, model.SideTypeSell, 103, 2)) // closes
	c.processTrade(fillNow(11, model.SideTypeSell, 103, 1)) // opens
	c.mtx.Unlock()
	require.Equal(t, 1, c.PositionsOpened("BTCUSDm", reconcileAt.Add(time.Hour)))
}

// B35: PositionsReplayed tells the openings replayed from the storage at the
// start only, the ones of the previous runs: not the ones of this run.
func TestController_PositionsReplayed(t *testing.T) {
	ctx := context.Background()
	db, err := storage.FromMemory()
	require.NoError(t, err)
	storedFill(t, db, 0, model.SideTypeBuy, model.OrderTypeMarket, model.OrderStatusTypeFilled, 100, 1, nil)
	storedFill(t, db, 1, model.SideTypeSell, model.OrderTypeMarket, model.OrderStatusTypeFilled, 101, 1, nil)
	storedFill(t, db, 2, model.SideTypeSell, model.OrderTypeMarket, model.OrderStatusTypeFilled, 102, 1, nil)
	exch := newHeldExchange(ctx)
	exch.held["BTCUSDm"] = model.Order{Side: model.SideTypeSell, Quantity: 1}

	c := NewController(ctx, exch, db, NewOrderFeed())
	var _ service.PositionOpenings = c // the strategies' broker
	minute := func(m int) time.Time { return reconcileAt.Add(time.Duration(m) * time.Minute) }
	require.Equal(t, 2, c.PositionsReplayed("BTCUSDm", minute(0)), "the long, then the short")
	require.Equal(t, 1, c.PositionsReplayed("BTCUSDm", minute(1)))
	require.Zero(t, c.PositionsReplayed("ETHUSDm", time.Time{}))

	c.mtx.Lock()
	c.processTrade(fillNow(10, model.SideTypeBuy, 103, 1)) // closes
	c.processTrade(fillNow(11, model.SideTypeBuy, 103, 1)) // opens
	c.mtx.Unlock()
	require.Equal(t, 3, c.PositionsOpened("BTCUSDm", minute(0)))
	require.Equal(t, 2, c.PositionsReplayed("BTCUSDm", minute(0)), "not the ones of this run")
	require.Zero(t, c.PositionsReplayed("BTCUSDm", reconcileAt.Add(time.Hour)))
}
