package order

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
)

// heldExchange is the paper wallet reporting the positions the bot holds, as
// MT5 does (see service.PositionReporter), and the orders of a previous run as
// the exchange tells them.
type heldExchange struct {
	*exchange.PaperWallet
	mtx    sync.Mutex
	held   map[string]model.Order // the side and the quantity held, by pair; none: flat
	orders map[int64]model.Order  // by ExchangeID
	known  bool                   // false: the positions held unknown
	asked  int                    // HeldPosition calls
}

func newHeldExchange(ctx context.Context) *heldExchange {
	return &heldExchange{
		PaperWallet: exchange.NewPaperWallet(ctx, "USD", exchange.WithPaperAsset("USD", 100000)),
		held:        make(map[string]model.Order),
		orders:      make(map[int64]model.Order),
		known:       true,
	}
}

func (e *heldExchange) HeldPosition(pair string) (model.SideType, float64, bool) {
	e.mtx.Lock()
	defer e.mtx.Unlock()
	e.asked++
	held := e.held[pair]
	return held.Side, held.Quantity, e.known
}

func (e *heldExchange) Order(pair string, id int64) (model.Order, error) {
	e.mtx.Lock()
	order, ok := e.orders[id]
	e.mtx.Unlock()
	if ok {
		return order, nil
	}
	return e.PaperWallet.Order(pair, id)
}

// setOrder sets the state of an order of a previous run, as the exchange
// tells it.
func (e *heldExchange) setOrder(order model.Order) {
	e.mtx.Lock()
	defer e.mtx.Unlock()
	e.orders[order.ExchangeID] = order
}

var reconcileAt = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)

// storedFill stores a fill of BTCUSDm of a previous run, grouped (a leg) with
// group.
func storedFill(t *testing.T, db storage.Storage, minute int, side model.SideType, kind model.OrderType,
	status model.OrderStatusType, price, quantity float64, group *int64) model.Order {
	t.Helper()
	at := reconcileAt.Add(time.Duration(minute) * time.Minute)
	order := model.Order{ExchangeID: int64(1000 + minute), Pair: "BTCUSDm", Side: side, Type: kind,
		Status: status, Price: price, Quantity: quantity, GroupID: group, CreatedAt: at, UpdatedAt: at}
	require.NoError(t, db.CreateOrder(&order))
	return order
}

// fillNow is a fill of this run of BTCUSDm.
func fillNow(id int64, side model.SideType, price, quantity float64) *model.Order {
	at := reconcileAt.Add(time.Hour)
	return &model.Order{ID: id, ExchangeID: id, Pair: "BTCUSDm", Side: side, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Price: price, Quantity: quantity, CreatedAt: at, UpdatedAt: at}
}

// R11: a position of the storage closed outside the bot, eg: by hand in the
// terminal, isn't held at the exchange: the restart discards it, without any
// order, rather than settling the next trades of its pair against it, their
// profits and results wrong.
func TestController_RestoreDiscardsAPositionClosedOutside(t *testing.T) {
	ctx := context.Background()
	db, err := storage.FromMemory()
	require.NoError(t, err)
	storedFill(t, db, 0, model.SideTypeBuy, model.OrderTypeMarket, model.OrderStatusTypeFilled, 100, 1, nil)
	exch := newHeldExchange(ctx) // flat

	hook := logtest.NewGlobal()
	defer hook.Reset()
	c := NewController(ctx, exch, db, NewOrderFeed())
	require.NotContains(t, c.position, "BTCUSDm", "discarded")
	require.True(t, logged(hook, log.WarnLevel, "BTCUSDm: the position replayed from the storage (BUY 1 at 100) "+
		"is not held at the exchange"))
	orders, err := db.Orders()
	require.NoError(t, err)
	require.Len(t, orders, 1, "no order sent nor stored")

	// the next trade settled on its own: bought at 200, sold at 210, 5 %
	c.processTrade(fillNow(10, model.SideTypeBuy, 200, 1))
	close := fillNow(11, model.SideTypeSell, 210, 1)
	c.processTrade(close)
	require.InDelta(t, 0.05, close.Profit, 1e-9, "not averaged with the position closed outside")
	require.InDeltaSlice(t, []float64{10}, c.Results["BTCUSDm"].Win(), 1e-9)
	require.NotContains(t, c.position, "BTCUSDm")
}

// R11: a position held at the exchange as replayed is kept; one held otherwise,
// eg: another size, kept too, an error logged; nothing checked with an
// exchange that doesn't know.
func TestController_RestoreChecksThePositionHeld(t *testing.T) {
	ctx := context.Background()
	db, err := storage.FromMemory()
	require.NoError(t, err)
	storedFill(t, db, 0, model.SideTypeBuy, model.OrderTypeMarket, model.OrderStatusTypeFilled, 100, 0.3, nil)

	exch := newHeldExchange(ctx)
	exch.held["BTCUSDm"] = model.Order{Side: model.SideTypeBuy, Quantity: 0.1 + 0.2}
	hook := logtest.NewGlobal()
	defer hook.Reset()
	c := NewController(ctx, exch, db, NewOrderFeed())
	require.Contains(t, c.position, "BTCUSDm", "held")
	require.False(t, logged(hook, log.ErrorLevel, "BTCUSDm"))

	exch.held["BTCUSDm"] = model.Order{Side: model.SideTypeBuy, Quantity: 0.1}
	c = NewController(ctx, exch, db, NewOrderFeed())
	require.Equal(t, 0.3, c.position["BTCUSDm"].Quantity, "kept: what differs is unknown")
	require.True(t, logged(hook, log.ErrorLevel, "BTCUSDm: the position replayed from the storage (BUY 0.3 at 100) "+
		"differs from the one held at the exchange (BUY 0.1)"))

	exch.known = false
	c = NewController(ctx, exch, db, NewOrderFeed())
	require.Contains(t, c.position, "BTCUSDm", "unknown: kept")
}

// R11: the position is kept while an open order of the storage may have closed
// it, eg: its stop loss filled while the bot was off, whose fill closes it
// with its profit; discarded once those orders end without a fill, eg: the
// legs canceled with the position closed by hand.
func TestController_RestoreKeepsAPositionItsLegsMayClose(t *testing.T) {
	ctx := context.Background()
	group := int64(7)
	setup := func(t *testing.T) (*Controller, *heldExchange, model.Order, model.Order) {
		db, err := storage.FromMemory()
		require.NoError(t, err)
		storedFill(t, db, 0, model.SideTypeBuy, model.OrderTypeMarket, model.OrderStatusTypeFilled, 100, 1, nil)
		stop := storedFill(t, db, 1, model.SideTypeSell, model.OrderTypeStopLoss, model.OrderStatusTypeNew, 90, 1,
			&group)
		target := storedFill(t, db, 2, model.SideTypeSell, model.OrderTypeLimitMaker, model.OrderStatusTypeNew, 120,
			1, &group)
		exch := newHeldExchange(ctx) // flat
		c := NewController(ctx, exch, db, NewOrderFeed())
		require.Contains(t, c.position, "BTCUSDm", "kept for its legs")
		c.loadPendingOrders() // as Start does
		exch.setOrder(stop)
		exch.setOrder(target)
		return c, exch, stop, target
	}

	t.Run("its stop loss filled", func(t *testing.T) {
		c, exch, stop, target := setup(t)
		stop.Status = model.OrderStatusTypeFilled
		exch.setOrder(stop)
		target.Status = model.OrderStatusTypeCanceled
		exch.setOrder(target)
		c.UpdateOrders()
		require.NotContains(t, c.position, "BTCUSDm", "closed by its stop loss")
		require.InDeltaSlice(t, []float64{-10}, c.Results["BTCUSDm"].Lose(), 1e-9, "its loss")
	})

	t.Run("its legs canceled", func(t *testing.T) {
		c, exch, stop, target := setup(t)
		stop.Status = model.OrderStatusTypeCanceled
		exch.setOrder(stop)
		c.UpdateOrders()
		require.Contains(t, c.position, "BTCUSDm", "its take profit open still")

		target.Status = model.OrderStatusTypeCanceled
		exch.setOrder(target)
		exch.held["BTCUSDm"] = model.Order{Side: model.SideTypeBuy, Quantity: 1} // eg: read before the close
		c.UpdateOrders()
		require.Contains(t, c.position, "BTCUSDm", "held at the exchange: kept")

		c, exch, stop, target = setup(t)
		for _, leg := range []model.Order{stop, target} {
			leg.Status = model.OrderStatusTypeCanceled
			exch.setOrder(leg)
		}
		c.UpdateOrders()
		require.NotContains(t, c.position, "BTCUSDm", "closed outside the bot: discarded")
		require.Empty(t, c.Results["BTCUSDm"].Win())
		require.Empty(t, c.Results["BTCUSDm"].Lose())
	})

	t.Run("a fill of this run", func(t *testing.T) {
		c, exch, _, _ := setup(t)
		asked := exch.asked
		c.mtx.Lock()
		c.processTrade(fillNow(10, model.SideTypeBuy, 200, 1))
		c.mtx.Unlock()
		require.Equal(t, 2.0, c.position["BTCUSDm"].Quantity, "added to, as before: not checked any more")
		require.NotContains(t, c.unheld, "BTCUSDm")
		require.Equal(t, asked, exch.asked)
	})
}

// logged reports whether hook has an entry of level containing text.
func logged(hook *logtest.Hook, level log.Level, text string) bool {
	for _, entry := range hook.AllEntries() {
		if entry.Level == level && strings.Contains(entry.Message, text) {
			return true
		}
	}
	return false
}
