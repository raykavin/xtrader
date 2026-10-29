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

// newWalletController wires a controller to a paper wallet funded with USDT,
// at a price of 100, and an in-memory storage.
func newWalletController(t *testing.T) (*Controller, *exchange.PaperWallet) {
	t.Helper()
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 100000))
	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), Open: 100,
		High: 100, Low: 100, Close: 100, Complete: true})
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	return NewController(context.Background(), wallet, memory, NewOrderFeed()), wallet
}

// heldAsset returns the position the wallet holds on BTCUSDT.
func heldAsset(t *testing.T, wallet *exchange.PaperWallet) float64 {
	t.Helper()
	asset, _, err := wallet.Position("BTCUSDT")
	require.NoError(t, err)
	return asset
}

// The entries blocked, every order that would open or add to a position is
// refused, ErrEntriesBlocked, without reaching the exchange.
func TestController_BlockedEntriesRefuseTheOpenings(t *testing.T) {
	controller, wallet := newWalletController(t)
	require.True(t, controller.BlockEntries(1, "asked by the supervisor"))

	blocked, reason, revision := controller.EntriesBlocked()
	require.True(t, blocked)
	require.Equal(t, "asked by the supervisor", reason)
	require.EqualValues(t, 1, revision)

	_, err := controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
	require.ErrorIs(t, err, ErrEntriesBlocked)
	require.ErrorIs(t, err, exchange.ErrNothingToReduce, "flat: nothing to reduce")
	_, err = controller.CreateOrderMarketQuote(model.SideTypeBuy, "BTCUSDT", 100)
	require.ErrorIs(t, err, ErrEntriesBlocked)
	_, err = controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 90)
	require.ErrorIs(t, err, ErrEntriesBlocked)
	_, err = controller.CreateOrderStop("BTCUSDT", 1, 90)
	require.ErrorIs(t, err, ErrEntriesBlocked)
	_, err = controller.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 1, 90, 120)
	require.ErrorIs(t, err, ErrEntriesBlocked)
	_, err = controller.CreateOrderLimitProtected(model.SideTypeBuy, "BTCUSDT", 1, 95, 90, 120)
	require.ErrorIs(t, err, ErrEntriesBlocked)
	_, err = controller.CreateOrderStopProtected(model.SideTypeBuy, "BTCUSDT", 1, 105, 90, 120)
	require.ErrorIs(t, err, ErrEntriesBlocked)
	_, err = controller.CreateOrderOCO(model.SideTypeSell, "BTCUSDT", 1, 120, 90, 90)
	require.ErrorIs(t, err, ErrEntriesBlocked, "an OCO of a flat pair is an entry")

	require.Zero(t, heldAsset(t, wallet))
	orders, err := controller.storage.Orders()
	require.NoError(t, err)
	require.Empty(t, orders, "nothing reached the exchange")
}

// The entries blocked, the position held is reduced, closed, protected and its
// orders canceled: a market order against it only reduces it, never opening
// the other side.
func TestController_BlockedEntriesLetThePositionBeReduced(t *testing.T) {
	controller, wallet := newWalletController(t)
	orders, err := controller.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 10, 90, 120)
	require.NoError(t, err)
	legs := orders[1:]
	pending, err := controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 80)
	require.NoError(t, err)

	controller.BlockEntries(1, "blocked")

	// the legs of the position move, a pending entry doesn't
	moved, err := controller.UpdateProtection(legs[1], 85)
	require.NoError(t, err)
	_, err = controller.UpdateLimit(pending, 82)
	require.ErrorIs(t, err, ErrEntriesBlocked)
	require.NoError(t, controller.Cancel(pending))

	// adding to the long is refused
	_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
	require.ErrorIs(t, err, ErrEntriesBlocked)

	// legs canceled, as a strategy does before closing on the paper wallet
	require.NoError(t, controller.Cancel(moved))
	// more than held: the long closed, no short
	closed, err := controller.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 15)
	require.NoError(t, err)
	require.Equal(t, 10.0, closed.Quantity)
	require.Zero(t, heldAsset(t, wallet))

	// flat again: an entry is refused
	_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
	require.ErrorIs(t, err, ErrEntriesBlocked)
}

// The legs of an OCO order protecting the position held pass while the entries
// are blocked.
func TestController_BlockedEntriesLetTheLegsOfAPositionBePlaced(t *testing.T) {
	controller, _ := newWalletController(t)
	_, err := controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 2)
	require.NoError(t, err)
	controller.BlockEntries(1, "blocked")

	_, err = controller.CreateOrderOCO(model.SideTypeSell, "BTCUSDT", 2, 120, 90, 90)
	require.NoError(t, err, "the legs of the long")
	_, err = controller.CreateOrderOCO(model.SideTypeBuy, "BTCUSDT", 2, 80, 110, 110)
	require.ErrorIs(t, err, ErrEntriesBlocked, "on the side of the long: an entry")
}

// AllowEntries opens the entries again, and a block or an allowance older than
// the last change is ignored.
func TestController_EntriesFollowTheRevisions(t *testing.T) {
	controller, wallet := newWalletController(t)
	require.True(t, controller.BlockEntries(5, "blocked"))
	require.False(t, controller.AllowEntries(4), "older: ignored")
	blocked, _, revision := controller.EntriesBlocked()
	require.True(t, blocked)
	require.EqualValues(t, 5, revision)

	require.True(t, controller.AllowEntries(6))
	require.False(t, controller.BlockEntries(5, "late"), "older: ignored")
	blocked, reason, _ := controller.EntriesBlocked()
	require.False(t, blocked)
	require.Empty(t, reason)

	_, err := controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
	require.NoError(t, err)
	require.Equal(t, 1.0, heldAsset(t, wallet))
}

// HoldEntries refuses the entries until released, whatever the blocks.
func TestController_HoldEntries(t *testing.T) {
	controller, _ := newWalletController(t)
	release := controller.HoldEntries("closing the positions")
	blocked, reason, _ := controller.EntriesBlocked()
	require.True(t, blocked)
	require.Equal(t, "closing the positions", reason)
	_, err := controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
	require.ErrorIs(t, err, ErrEntriesBlocked)

	controller.AllowEntries(1)
	_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
	require.ErrorIs(t, err, ErrEntriesBlocked, "held whatever the allowance")

	release()
	release() // twice is harmless
	_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
	require.NoError(t, err)
}

// ClosePosition closes the whole position, its legs gone with it, on a stopped
// bot too, which refuses its other orders.
func TestController_ClosePosition(t *testing.T) {
	controller, wallet := newWalletController(t)
	orders, err := controller.CreateOrderMarketProtected(model.SideTypeSell, "BTCUSDT", 5, 110, 80)
	require.NoError(t, err)
	controller.Start()
	controller.Stop()
	_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 5)
	require.ErrorIs(t, err, ErrBotStopped)

	closed, err := controller.ClosePosition("BTCUSDT")
	require.NoError(t, err)
	require.Equal(t, model.SideTypeBuy, closed.Side)
	require.Equal(t, 5.0, closed.Quantity)
	require.Zero(t, heldAsset(t, wallet))
	for _, leg := range orders[1:] {
		got, err := wallet.Order("BTCUSDT", leg.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, got.Status)
	}

	_, err = controller.ClosePosition("BTCUSDT")
	require.ErrorIs(t, err, exchange.ErrNothingToReduce, "flat")

	controller.Close()
	_, err = controller.ClosePosition("BTCUSDT")
	require.ErrorIs(t, err, ErrBotStopped, "never once closed")
}

// WaitIdle waits for the orders being sent to end, its bound at most: true at
// once without one, true once the last ends, false when one lasts beyond it.
func TestController_WaitIdle(t *testing.T) {
	controller, _ := newWalletController(t)
	require.True(t, controller.WaitIdle(time.Second), "none being sent")

	end, err := controller.begin("BTCUSDT", false)
	require.NoError(t, err)
	go func() {
		time.Sleep(50 * time.Millisecond)
		end()
	}()
	start := time.Now()
	require.True(t, controller.WaitIdle(2*time.Second))
	require.GreaterOrEqual(t, time.Since(start), 40*time.Millisecond, "waited for the order")

	end, err = controller.begin("BTCUSDT", false)
	require.NoError(t, err)
	defer end()
	start = time.Now()
	require.False(t, controller.WaitIdle(80*time.Millisecond), "still being sent")
	require.Less(t, time.Since(start), time.Second)
}
