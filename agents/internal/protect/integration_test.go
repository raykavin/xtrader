package protect

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/ui"
)

// paperMarket is the paper wallet with the candles of the tests: none, its ATR
// from the closed candles told.
type paperMarket struct {
	*exchange.PaperWallet
}

func (paperMarket) CandlesByLimit(context.Context, string, string, int) ([]model.Candle, error) {
	return nil, nil
}

func (paperMarket) CandlesByPeriod(context.Context, string, string, time.Time, time.Time) ([]model.Candle, error) {
	return nil, nil
}

// The protection on the order controller and the paper wallet, end to end:
// the trailing stop tightens the leg at the wallet; a strategy with an old
// copy loosening it at once is refused by the stop guard, the stop never
// below the most protective; the XTrader AI arms the breakeven; a manager
// started again goes on from the stop at the wallet.
func TestManager_EndToEndOnThePaperWallet(t *testing.T) {
	ctx := context.Background()
	const btc = "BTCUSDT"
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 100000))
	at := start
	candle := func(close float64, complete bool) model.Candle {
		return model.Candle{Pair: btc, Time: at, Open: close, Close: close, High: close + 0.5, Low: close - 0.5,
			Complete: complete}
	}
	for range 20 { // closed candles of a range of 1: an ATR of 1
		wallet.OnCandle(candle(100, true))
		at = at.Add(time.Minute)
	}
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	controller := order.NewController(ctx, wallet, memory, order.NewOrderFeed())
	orders, err := controller.CreateOrderMarketProtected(model.SideTypeBuy, btc, 1, 98, 130)
	require.NoError(t, err)
	var stop model.Order
	for _, o := range orders {
		if o.Type == model.OrderTypeStopLoss {
			stop = o
		}
	}

	var clockMu sync.Mutex
	clock := start.Add(time.Hour)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	newManager := func() *Manager {
		m := New(ctx, controller, paperMarket{wallet}, "1m", "paper:test")
		m.now = now
		controller.SetStopGuard(m.Guarded)
		m.Apply(&ui.ProtectionSettings{Unit: ui.ProtectionUnitATR,
			Breakeven: ui.BreakevenSettings{Supervisor: true, Trigger: 1, Margin: 0.1},
			Trailing:  ui.TrailingSettings{Enabled: true, Trigger: 1, Distance: 0.5, Step: 0.2}}, nil)
		at = start
		for range 20 {
			m.OnCandle(candle(100, true))
			at = at.Add(time.Minute)
		}
		return m
	}
	m := newManager()
	// a candle of its own for each price, in progress, of a narrow range: the
	// extremes of a candle in progress never retreat
	moment := start.Add(time.Hour)
	price := func(m *Manager, close float64) {
		t.Helper()
		clockMu.Lock()
		clock = clock.Add(3 * time.Second)
		clockMu.Unlock()
		moment = moment.Add(time.Minute)
		c := model.Candle{Pair: btc, Time: moment, Open: close, Close: close, High: close + 0.1, Low: close - 0.1}
		wallet.OnCandle(c)
		m.OnCandle(c)
		m.Wait()
	}
	live := func() float64 {
		got, err := wallet.Order(btc, stop.ExchangeID)
		require.NoError(t, err)
		return order.StopLevel(got)
	}

	price(m, 101.5)
	require.Equal(t, 101.0, live(), "the trail at the wallet")

	// a strategy with its old copy at 98 moving to 99.5 while the price rises
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 10 {
			_, _ = controller.UpdateProtection(stop, 99.5)
		}
	}()
	for _, close := range []float64{101.8, 102.1, 102.4} {
		price(m, close)
	}
	wg.Wait()
	require.Equal(t, 101.9, live(), "never below the trail: the old copy refused")

	// the XTrader AI arms the breakeven: the trail is more protective already
	response := m.Arm(ui.SupervisionProtectRequest{ArmID: 1, Pair: btc, PositionRef: m.Protection(btc).PositionRef,
		Feature: ui.ProtectionBreakeven, Reason: "proteger o lucro"})
	require.Equal(t, ui.ProtectArmed, response.Status)
	require.Equal(t, 101.9, live())

	// started again: on from the stop at the wallet, never back
	restarted := newManager()
	price(restarted, 102.2)
	require.Equal(t, 101.9, live(), "the trail from 102.2 is 101.7: kept")
	price(restarted, 103)
	require.Equal(t, 102.5, live())
}
