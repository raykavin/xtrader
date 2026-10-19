package order

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
)

// legOfType returns the leg of type among orders.
func legOfType(t *testing.T, orders []model.Order, kind model.OrderType) model.Order {
	t.Helper()
	for _, order := range orders {
		if order.Type == kind {
			return order
		}
	}
	t.Fatalf("no %s", kind)
	return model.Order{}
}

// liveStop returns the level of the stop loss leg at the wallet.
func liveStop(t *testing.T, wallet *exchange.PaperWallet, leg model.Order) float64 {
	t.Helper()
	got, err := wallet.Order("BTCUSDT", leg.ExchangeID)
	require.NoError(t, err)
	return StopLevel(got)
}

// The stop guard on, a stop loss of an open position only tightens, against
// its live level, whatever the mover's copy: a looser level is a
// *service.StopNotTighterError with the leg as it is, as tight is no move; a
// user moves it freely; the take profits and the legs of a pending entry are
// never guarded; the guard off, everything moves as before.
func TestController_StopGuard(t *testing.T) {
	controller, wallet := newWalletController(t)
	orders, err := controller.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 1, 90, 120)
	require.NoError(t, err)
	stop := legOfType(t, orders, model.OrderTypeStopLoss)
	profit := legOfType(t, orders, model.OrderTypeLimitMaker)

	// the guard off: loosened as before
	moved, err := controller.UpdateProtection(stop, 85)
	require.NoError(t, err)
	require.Equal(t, 85.0, liveStop(t, wallet, stop))

	controller.SetStopGuard(func(string) bool { return true })
	require.True(t, controller.StopGuarded("BTCUSDT"))

	_, err = controller.UpdateProtection(moved, 80)
	var refused *service.StopNotTighterError
	require.ErrorAs(t, err, &refused)
	require.ErrorIs(t, err, service.ErrStopNotTighter)
	require.Equal(t, 85.0, refused.Current)
	require.Equal(t, 85.0, StopLevel(refused.Leg), "the leg as it is")
	require.Equal(t, 85.0, liveStop(t, wallet, stop), "nothing moved")

	same, err := controller.UpdateProtection(stop, 85) // a stale copy at 90, asking the live level
	require.NoError(t, err, "as tight: no move, no error")
	require.Equal(t, 85.0, StopLevel(same))

	tighter, err := controller.UpdateProtection(stop, 95)
	require.NoError(t, err)
	require.Equal(t, 95.0, StopLevel(tighter))

	// a stale copy at 85 asking 92, between it and the live 95: refused, never loosened
	_, err = controller.UpdateProtection(moved, 92)
	require.ErrorAs(t, err, &refused)
	require.Equal(t, 95.0, liveStop(t, wallet, stop))

	// the take profit moves both ways
	_, err = controller.UpdateProtection(profit, 130)
	require.NoError(t, err)
	_, err = controller.UpdateProtection(profit, 115)
	require.NoError(t, err)

	// a user loosens it
	loosened, err := controller.UpdateProtectionManual(tighter, 88)
	require.NoError(t, err)
	require.Equal(t, 88.0, StopLevel(loosened))

	// the guard off: TightenProtection guards all the same
	controller.SetStopGuard(nil)
	require.False(t, controller.StopGuarded("BTCUSDT"))
	_, err = controller.TightenProtection(loosened, 86)
	require.ErrorAs(t, err, &refused)
	_, err = controller.TightenProtection(loosened, 89)
	require.NoError(t, err)
	require.Equal(t, 89.0, liveStop(t, wallet, stop))
}

// A short: tighter is lower.
func TestController_StopGuardOfAShort(t *testing.T) {
	controller, wallet := newWalletController(t)
	orders, err := controller.CreateOrderMarketProtected(model.SideTypeSell, "BTCUSDT", 1, 110, 80)
	require.NoError(t, err)
	stop := legOfType(t, orders, model.OrderTypeStopLoss)
	controller.SetStopGuard(func(string) bool { return true })

	_, err = controller.UpdateProtection(stop, 112)
	require.ErrorIs(t, err, service.ErrStopNotTighter)
	_, err = controller.UpdateProtection(stop, 105)
	require.NoError(t, err)
	require.Equal(t, 105.0, liveStop(t, wallet, stop))
}

// The legs of a pending entry move with it, guard or not: they protect no
// position yet.
func TestController_StopGuardSparesThePendingEntries(t *testing.T) {
	controller, wallet := newWalletController(t)
	orders, err := controller.CreateOrderLimitProtected(model.SideTypeBuy, "BTCUSDT", 1, 95, 90, 120)
	require.NoError(t, err)
	stop := legOfType(t, orders, model.OrderTypeStopLoss)
	controller.SetStopGuard(func(string) bool { return true })

	_, err = controller.UpdateProtection(stop, 85)
	require.NoError(t, err)
	require.Equal(t, 85.0, liveStop(t, wallet, stop))
}

// Two movers at once, eg: a strategy with an old copy loosening and the
// protection of the positions tightening: the stop loss ends at the most
// protective level asked, never below it.
func TestController_StopGuardUnderConcurrentMovers(t *testing.T) {
	controller, wallet := newWalletController(t)
	orders, err := controller.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 1, 90, 120)
	require.NoError(t, err)
	stop := legOfType(t, orders, model.OrderTypeStopLoss)
	controller.SetStopGuard(func(string) bool { return true })

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = controller.UpdateProtection(stop, 91+float64(i%5)) // the strategy, a stale copy
		}()
		go func() {
			defer wg.Done()
			_, _ = controller.TightenProtection(stop, 96+float64(i)/10) // the protection
		}()
	}
	wg.Wait()
	require.InDelta(t, 97.9, liveStop(t, wallet, stop), 1e-9, "the most protective asked")
}

// The stop loss of a position held, as it is now, and one set on a position
// without; none on a position not held.
func TestController_PositionStopAndSetPositionStop(t *testing.T) {
	controller, wallet := newWalletController(t)
	_, _, ok := controller.PositionStop("BTCUSDT")
	require.False(t, ok, "flat")

	_, err := controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
	require.NoError(t, err)
	position, _, ok := controller.PositionStop("BTCUSDT")
	require.False(t, ok, "held without a stop loss")
	held, isHeld := controller.HeldPosition("BTCUSDT")
	require.True(t, isHeld)
	require.Equal(t, model.SideTypeBuy, held.Side)
	require.Equal(t, 1.0, position.Quantity, "the position, without a leg")

	legs, err := controller.SetPositionStop("BTCUSDT", model.SideTypeBuy, 92)
	require.NoError(t, err)
	require.Len(t, legs, 1)
	position, stop, ok := controller.PositionStop("BTCUSDT")
	require.True(t, ok)
	require.Equal(t, 1.0, position.Quantity)
	require.Equal(t, 92.0, StopLevel(stop))
	require.Equal(t, 92.0, liveStop(t, wallet, stop))

	_, err = controller.SetPositionStop("BTCUSDT", model.SideTypeSell, 108)
	require.True(t, errors.Is(err, ErrNoPosition), "not held on that side")
}

// A leg whose level the exchange moved, eg: by hand in the terminal, is
// recorded as moved; another order, or a leg ended, isn't.
func TestLevelMoved(t *testing.T) {
	group := int64(7)
	level := func(price float64) *float64 { return &price }
	stop := model.Order{GroupID: &group, Type: model.OrderTypeStopLoss, Status: model.OrderStatusTypeNew, Price: 90,
		Stop: level(90)}

	moved := stop
	moved.Price, moved.Stop = 95, level(95)
	require.True(t, levelMoved(stop, moved))
	require.False(t, levelMoved(stop, stop), "the same level")

	ended := moved
	ended.Status = model.OrderStatusTypeFilled
	require.False(t, levelMoved(stop, ended), "ended: its status changed anyway")

	entry := model.Order{Type: model.OrderTypeLimit, Status: model.OrderStatusTypeNew, Price: 100}
	other := entry
	other.Price = 101
	require.False(t, levelMoved(entry, other), "not a leg")
}
