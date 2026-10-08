package order

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
)

// The legs of the position held, as a strategy finds them after a restart:
// the open legs of its entry, never the ones of an older entry, eg: of a
// position closed by its stop loss whose exit isn't booked yet.
func TestController_HeldLegsAfterARestart(t *testing.T) {
	ctx := context.Background()
	store, err := storage.FromMemory()
	require.NoError(t, err)
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 10000))
	at := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: at, Close: 100, High: 100, Low: 100, Complete: true})

	group := int64(1)
	stale := model.Order{Pair: "BTCUSDT", Side: model.SideTypeSell, Type: model.OrderTypeStopLoss,
		Status: model.OrderStatusTypeNew, Price: 90, Quantity: 10, GroupID: &group, CreatedAt: at.Add(-time.Hour)}
	require.NoError(t, store.CreateOrder(&stale))

	controller := NewController(ctx, wallet, store, NewOrderFeed())
	orders, err := controller.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 10, 99, 102)
	require.NoError(t, err)
	require.Len(t, orders, 3)

	check := func(c *Controller) {
		t.Helper()
		legs, opened, err := c.HeldLegs("BTCUSDT", model.SideTypeBuy)
		require.NoError(t, err)
		require.Equal(t, at, opened)
		require.Len(t, legs, 2)
		for i, leg := range legs {
			require.Equal(t, orders[i+1].ID, leg.ID)
			require.Equal(t, orders[i+1].ExchangeID, leg.ExchangeID)
		}

		legs, opened, err = c.HeldLegs("BTCUSDT", model.SideTypeSell)
		require.NoError(t, err)
		require.Zero(t, opened, "no short held")
		require.Empty(t, legs)
	}
	check(controller)
	check(NewController(ctx, wallet, store, NewOrderFeed())) // restarted over the orders stored

	// once a leg is canceled, the protection is gone: none is open
	require.NoError(t, controller.Cancel(orders[2]))
	controller.UpdateOrders()
	legs, _, err := NewController(ctx, wallet, store, NewOrderFeed()).HeldLegs("BTCUSDT", model.SideTypeBuy)
	require.NoError(t, err)
	require.Empty(t, legs)
}

// tickets is the paper wallet telling, as MT5 does by its tickets (see
// fillPositions), the position each fill opened and the ones each leg
// protects, by their ExchangeID: the others unknown.
type tickets struct {
	*exchange.PaperWallet
	opened    map[int64]int64
	protected map[int64][]int64
}

func (w tickets) FillPositions(fill model.Order) (int64, []int64, bool) {
	if fill.GroupID != nil {
		positions, ok := w.protected[fill.ExchangeID]
		return 0, positions, ok
	}
	position, ok := w.opened[fill.ExchangeID]
	return position, nil, ok
}

// B29, the live MT5 path: after a restart, the legs of the position held are
// the ones the exchange tells protect its ticket, whatever their IDs, a leg
// it doesn't know placed after the entry, and the position opened at the time
// MT5 stamped its fill, a moment after the close of the candle that placed it,
// rather than at the open of that candle as the paper wallet stamps it.
func TestController_HeldLegsOfMT5AfterARestart(t *testing.T) {
	ctx := context.Background()
	store, err := storage.FromMemory()
	require.NoError(t, err)
	wallet := exchange.NewPaperWallet(ctx, "USD", exchange.WithPaperAsset("USD", 10000))
	candle := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	wallet.OnCandle(model.Candle{Pair: "XAUUSDm", Time: candle, Close: 100, High: 100, Low: 100, Complete: true})
	filled := candle.Add(30*time.Minute + 2*time.Second) // the close of the 30m candle, and a moment

	group, older := int64(7), int64(3)
	entry := model.Order{ExchangeID: 5001, Pair: "XAUUSDm", Side: model.SideTypeBuy,
		Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 100, Quantity: 1, CreatedAt: filled}
	leg := func(ticket int64, kind model.OrderType, price float64, group *int64) model.Order {
		return model.Order{ExchangeID: ticket, Pair: "XAUUSDm", Side: model.SideTypeSell, Type: kind,
			Status: model.OrderStatusTypeNew, Price: price, Quantity: 1, GroupID: group, CreatedAt: filled}
	}
	stop := leg(6011, model.OrderTypeStopLoss, 98, &group)
	profit := leg(6009, model.OrderTypeLimitMaker, 104, &group)
	other := leg(6012, model.OrderTypeStopLoss, 95, &older)      // of another position, stored after the entry
	unknown := leg(6013, model.OrderTypeLimitMaker, 106, &group) // a ticket the exchange doesn't tell
	// numbered as stored: the take profit before the entry
	for _, o := range []model.Order{profit, entry, stop, other, unknown} {
		require.NoError(t, store.CreateOrder(&o))
	}
	mt5 := tickets{PaperWallet: wallet, opened: map[int64]int64{5001: 5001},
		protected: map[int64][]int64{6011: {5001}, 6009: {5001}, 6012: {4000}}}

	legs, opened, err := NewController(ctx, mt5, store, NewOrderFeed()).HeldLegs("XAUUSDm", model.SideTypeBuy)
	require.NoError(t, err)
	require.Equal(t, filled, opened, "the time of the fill")
	var found []int64
	for _, l := range legs {
		found = append(found, l.ExchangeID)
	}
	require.Equal(t, []int64{6009, 6011, 6013}, found, "the legs of its ticket, and the one unknown placed after it")
}
