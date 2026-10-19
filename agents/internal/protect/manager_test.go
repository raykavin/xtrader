package protect

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/ui"
)

const pair = "XAUUSDm"

// fakeBroker is the order controller of the tests: one position, its stop
// loss, the moves asked, and the next error.
type fakeBroker struct {
	mu        sync.Mutex
	position  order.Position
	stop      float64
	next      error
	tightened []float64
	set       []float64
	reads     int
}

func (b *fakeBroker) PositionStop(string) (order.Position, model.Order, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reads++
	if b.position.Quantity <= 0 {
		return order.Position{}, model.Order{}, false
	}
	if b.stop <= 0 {
		return b.position, model.Order{}, false
	}
	return b.position, b.leg(), true
}

func (b *fakeBroker) leg() model.Order {
	level := b.stop
	group := int64(1)
	return model.Order{ID: 9, Pair: pair, Type: model.OrderTypeStopLoss, GroupID: &group, Price: level, Stop: &level}
}

func (b *fakeBroker) TightenProtection(_ model.Order, level float64) (model.Order, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.next; err != nil {
		b.next = nil
		return model.Order{}, err
	}
	if !tighter(b.position.Side, level, b.stop, 0.001) {
		return model.Order{}, &service.StopNotTighterError{Leg: b.leg(), Current: b.stop, Requested: level}
	}
	b.stop = level
	b.tightened = append(b.tightened, level)
	return b.leg(), nil
}

func (b *fakeBroker) SetPositionStop(_ string, _ model.SideType, level float64) ([]model.Order, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.next; err != nil {
		b.next = nil
		return nil, err
	}
	b.stop = level
	b.set = append(b.set, level)
	return []model.Order{b.leg()}, nil
}

func (b *fakeBroker) Tick(string) float64 { return 0.01 }

func (b *fakeBroker) moves() ([]float64, []float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]float64(nil), b.tightened...), append([]float64(nil), b.set...)
}

// fakeMarket is the exchange of the candles of the tests, and its costs.
type fakeMarket struct {
	closed []model.Candle // CandlesByLimit
	since  []model.Candle // CandlesByPeriod
}

func (m *fakeMarket) CandlesByLimit(context.Context, string, string, int) ([]model.Candle, error) {
	return m.closed, nil
}

func (m *fakeMarket) CandlesByPeriod(context.Context, string, string, time.Time, time.Time) ([]model.Candle,
	error) {
	return m.since, nil
}

// costedMarket tells the costs of the positions.
type costedMarket struct {
	fakeMarket
	costs service.PositionCosts
}

func (m *costedMarket) PositionCosts(string) service.PositionCosts { return m.costs }

var start = time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

// atrCandles returns n closed candles around price, each of a range of 1:
// their ATR(14) is 1.
func atrCandles(n int, price float64) []model.Candle {
	candles := make([]model.Candle, n)
	for i := range candles {
		candles[i] = model.Candle{Pair: pair, Time: start.Add(time.Duration(i-n) * time.Minute), Open: price,
			Close: price, High: price + 0.5, Low: price - 0.5, Complete: true}
	}
	return candles
}

// harness is a manager of the tests on a fake broker and market, its clock
// moved by hand.
type harness struct {
	t      *testing.T
	m      *Manager
	broker *fakeBroker
	now    time.Time
}

func newHarness(t *testing.T, side model.SideType, stop float64, market Market) *harness {
	t.Helper()
	broker := &fakeBroker{position: order.Position{Side: side, AvgPrice: 100, Quantity: 1,
		Entry: model.Order{ID: 1, ExchangeID: 111}}, stop: stop}
	if market == nil {
		market = &fakeMarket{closed: atrCandles(20, 100)}
	}
	h := &harness{t: t, broker: broker, now: start}
	h.m = New(context.Background(), broker, market, "1m", "mt5")
	h.m.now = func() time.Time { return h.now }
	// the candles of the ATR, read once
	h.m.OnCandle(model.Candle{Pair: pair, Time: start, Open: 100, Close: 100, High: 100, Low: 100})
	require.Eventually(t, func() bool {
		h.m.mu.Lock()
		defer h.m.mu.Unlock()
		return h.m.pairs[pair].loaded
	}, time.Second, time.Millisecond)
	return h
}

// price tells the manager a candle in progress closing at close (the bid),
// its ask the spread of 0.2 above, a second later, and waits for its move.
func (h *harness) price(close float64) {
	h.t.Helper()
	h.now = h.now.Add(3 * time.Second)
	h.m.OnCandle(model.Candle{Pair: pair, Time: start, Open: 100, Close: close, High: close, Low: close,
		Metadata: map[string]float64{"ask": close + 0.2, "spread": 0.2}})
	h.m.Wait()
}

func (h *harness) state() *ui.PositionProtection {
	h.t.Helper()
	state := h.m.Protection(pair)
	require.NotNil(h.t, state)
	return state
}

func settings(breakeven ui.BreakevenSettings, trailing ui.TrailingSettings) *ui.ProtectionSettings {
	return &ui.ProtectionSettings{Unit: ui.ProtectionUnitATR, Breakeven: breakeven, Trailing: trailing}
}

// Off, the positions aren't even read, nor the stop guard on.
func TestManager_Off(t *testing.T) {
	h := newHarness(t, model.SideTypeBuy, 98, nil)
	h.price(105)
	require.Nil(t, h.m.Protection(pair))
	require.Zero(t, h.broker.reads)
	require.False(t, h.m.Guarded(pair))
	h.m.Apply(settings(ui.BreakevenSettings{Enabled: true, Trigger: 1, Margin: 0.1}, ui.TrailingSettings{}), nil)
	require.True(t, h.m.Guarded(pair))
}

// The breakeven of a long: waiting short of its trigger, then the stop loss at
// the entry plus the margin; of a short, mirrored on the ask; the costs added.
func TestManager_Breakeven(t *testing.T) {
	t.Run("a long", func(t *testing.T) {
		h := newHarness(t, model.SideTypeBuy, 98, nil)
		h.m.Apply(settings(ui.BreakevenSettings{Enabled: true, Trigger: 1, Margin: 0.1}, ui.TrailingSettings{}), nil)

		h.price(100.5)
		state := h.state()
		require.InDelta(t, 1, state.ATR, 1e-9)
		require.Equal(t, ui.ProtectionWaiting, state.Breakeven.State)
		require.InDelta(t, 0.5, state.Breakeven.Remaining, 1e-9)
		tightened, _ := h.broker.moves()
		require.Empty(t, tightened)

		h.price(101.2)
		tightened, _ = h.broker.moves()
		require.Equal(t, []float64{100.1}, tightened)
		state = h.state()
		require.Equal(t, ui.ProtectionActive, state.Breakeven.State)
		require.Equal(t, ui.MoveConfirmed, state.Breakeven.Last.Status)
		require.Equal(t, 98.0, state.Breakeven.Last.Previous)
		require.Equal(t, 100.1, state.Breakeven.Last.Confirmed)

		// back near the entry: the trigger was reached, nothing more to do
		h.price(100.4)
		tightened, _ = h.broker.moves()
		require.Len(t, tightened, 1, "never moved again, never loosened")
	})

	t.Run("a short, on the ask", func(t *testing.T) {
		h := newHarness(t, model.SideTypeSell, 102, nil)
		h.m.Apply(settings(ui.BreakevenSettings{Enabled: true, Trigger: 1, Margin: 0.1}, ui.TrailingSettings{}), nil)
		h.price(99) // the ask 99.2: 0.8 in its favour
		tightened, _ := h.broker.moves()
		require.Empty(t, tightened)
		h.price(98.7) // the ask 98.9: 1.1
		tightened, _ = h.broker.moves()
		require.Equal(t, []float64{99.9}, tightened)
	})

	t.Run("the costs known added", func(t *testing.T) {
		market := &costedMarket{fakeMarket: fakeMarket{closed: atrCandles(20, 100)},
			costs: service.PositionCosts{Price: 0.05, Known: true}}
		h := newHarness(t, model.SideTypeBuy, 98, market)
		h.m.Apply(settings(ui.BreakevenSettings{Enabled: true, Trigger: 1, Margin: 0.1}, ui.TrailingSettings{}), nil)
		h.price(101.5)
		tightened, _ := h.broker.moves()
		require.Equal(t, []float64{100.15}, tightened)
		require.True(t, h.state().CostsKnown)
	})

	t.Run("a stop loss more protective already", func(t *testing.T) {
		h := newHarness(t, model.SideTypeBuy, 100.5, nil)
		h.m.Apply(settings(ui.BreakevenSettings{Enabled: true, Trigger: 1, Margin: 0.1}, ui.TrailingSettings{}), nil)
		h.price(101.5)
		tightened, _ := h.broker.moves()
		require.Empty(t, tightened, "nothing sent")
		require.Equal(t, ui.ProtectionActive, h.state().Breakeven.State)
	})
}

// The trailing stop: active from its trigger, the distance behind the price,
// moved by its step at least, never back when the price falls; a short on
// the ask.
func TestManager_Trailing(t *testing.T) {
	h := newHarness(t, model.SideTypeBuy, 98, nil)
	h.m.Apply(settings(ui.BreakevenSettings{}, ui.TrailingSettings{Enabled: true, Trigger: 1, Distance: 0.5,
		Step: 0.2}), nil)

	h.price(100.8)
	require.Equal(t, ui.ProtectionWaiting, h.state().Trailing.State)
	h.price(101.5)
	h.price(101.6) // 0.1 better: within its step
	h.price(101.8)
	h.price(101.0) // back: never loosened
	tightened, _ := h.broker.moves()
	require.Equal(t, []float64{101.0, 101.3}, tightened)
	require.Equal(t, 101.3, h.state().Trailing.Level, "the stop loss, tighter than the trail now")

	t.Run("a short", func(t *testing.T) {
		h := newHarness(t, model.SideTypeSell, 102, nil)
		h.m.Apply(settings(ui.BreakevenSettings{}, ui.TrailingSettings{Enabled: true, Trigger: 1, Distance: 0.5,
			Step: 0.2}), nil)
		h.price(98.5) // the ask 98.7
		tightened, _ := h.broker.moves()
		require.Equal(t, []float64{99.2}, tightened)
	})
}

// The breakeven and the trailing stop at once: the most protective one sent.
func TestManager_TheMostProtectiveWins(t *testing.T) {
	h := newHarness(t, model.SideTypeBuy, 98, nil)
	h.m.Apply(settings(ui.BreakevenSettings{Enabled: true, Trigger: 1, Margin: 0.1},
		ui.TrailingSettings{Enabled: true, Trigger: 1, Distance: 0.5, Step: 0.2}), nil)
	h.price(102)
	tightened, _ := h.broker.moves()
	require.Equal(t, []float64{101.5}, tightened, "the trail, above the breakeven at 100.1")
	require.Equal(t, ui.MoveConfirmed, h.state().Trailing.Last.Status)
}

// A move refused by the broker keeps the stop loss, and waits before the
// next; one whose answer was lost too; at most one move every two seconds.
func TestManager_BrokerFailures(t *testing.T) {
	h := newHarness(t, model.SideTypeBuy, 98, nil)
	h.m.Apply(settings(ui.BreakevenSettings{}, ui.TrailingSettings{Enabled: true, Trigger: 1, Distance: 0.5,
		Step: 0.2}), nil)

	h.broker.next = errors.New("mt5: retcode 10016 invalid stops")
	h.price(101.5)
	last := h.state().Trailing.Last
	require.Equal(t, ui.MoveRejected, last.Status)
	require.Equal(t, 98.0, h.broker.stop, "kept")
	h.price(101.6) // within the wait
	tightened, _ := h.broker.moves()
	require.Empty(t, tightened)

	h.now = h.now.Add(coolDown)
	h.broker.next = exchange.ErrMT5OutcomeUnknown
	h.price(101.6)
	require.Equal(t, ui.MoveUnknown, h.state().Trailing.Last.Status)

	h.now = h.now.Add(coolDown)
	h.price(101.7)
	tightened, _ = h.broker.moves()
	require.Equal(t, []float64{101.2}, tightened)

	// a second later only: the next waits for the interval
	h.now = h.now.Add(-2 * time.Second)
	h.m.OnCandle(model.Candle{Pair: pair, Time: start, Close: 102.5, High: 102.5, Low: 102.5})
	h.m.Wait()
	tightened, _ = h.broker.moves()
	require.Len(t, tightened, 1, "within the interval")
}

// A position without a stop loss gets one once the trigger is reached.
func TestManager_SetsAStop(t *testing.T) {
	h := newHarness(t, model.SideTypeBuy, 0, nil)
	h.m.Apply(settings(ui.BreakevenSettings{Enabled: true, Trigger: 1, Margin: 0.1}, ui.TrailingSettings{}), nil)
	h.price(101.5)
	_, set := h.broker.moves()
	require.Equal(t, []float64{100.1}, set)
}

// The XTrader AI arms a feature: refused without its permission or on another
// position; armed short of its trigger, applied once it is reached; applied
// at once when it is.
func TestManager_Arm(t *testing.T) {
	h := newHarness(t, model.SideTypeBuy, 98, nil)
	request := ui.SupervisionProtectRequest{ArmID: 5, Pair: pair, PositionRef: "mt5:111",
		Feature: ui.ProtectionTrailing, Reason: "momento perdendo força"}

	require.Equal(t, ui.ProtectRefused, h.m.Arm(request).Status, "off")
	h.m.Apply(settings(ui.BreakevenSettings{}, ui.TrailingSettings{Trigger: 1, Distance: 0.5, Step: 0.2}), nil)
	response := h.m.Arm(request)
	require.Equal(t, ui.ProtectRefused, response.Status, "not allowed to the supervisor")
	require.Contains(t, response.Detail, "permissão")

	h.m.Apply(settings(ui.BreakevenSettings{}, ui.TrailingSettings{Supervisor: true, Trigger: 1, Distance: 0.5,
		Step: 0.2}), nil)
	other := request
	other.PositionRef = "mt5:222"
	require.Equal(t, ui.ProtectRefused, h.m.Arm(other).Status, "another position")

	h.price(100.5)
	response = h.m.Arm(request)
	require.Equal(t, ui.ProtectArmed, response.Status)
	require.Contains(t, response.Detail, "aguardando o gatilho")
	state := h.state()
	require.True(t, state.Trailing.Armed)
	require.Equal(t, int64(5), state.Trailing.ArmID)
	require.Equal(t, ui.ProtectionWaiting, state.Trailing.State)

	h.price(101.6)
	tightened, _ := h.broker.moves()
	require.Equal(t, []float64{101.1}, tightened, "applied once its trigger was reached, by the agent")

	t.Run("applied at once, its trigger reached", func(t *testing.T) {
		h := newHarness(t, model.SideTypeBuy, 98, nil)
		h.m.Apply(settings(ui.BreakevenSettings{Supervisor: true, Trigger: 1, Margin: 0.1}, ui.TrailingSettings{}),
			nil)
		h.price(101.5)
		response := h.m.Arm(ui.SupervisionProtectRequest{ArmID: 6, Pair: pair, PositionRef: "mt5:111",
			Feature: ui.ProtectionBreakeven, Reason: "devolução de lucro"})
		require.Equal(t, ui.ProtectApplied, response.Status)
		require.Equal(t, 98.0, response.Previous)
		require.Equal(t, 100.1, response.Requested)
		require.Equal(t, 100.1, response.Confirmed)
	})
}

// After a restart, the best price since the entry is read from the candles:
// its trigger reached before, the trailing stop goes on from the price now.
func TestManager_Restart(t *testing.T) {
	market := &fakeMarket{closed: atrCandles(20, 100), since: []model.Candle{
		{Pair: pair, Time: start.Add(-30 * time.Minute), Close: 101, High: 103, Low: 100},
	}}
	broker := &fakeBroker{position: order.Position{Side: model.SideTypeBuy, AvgPrice: 100, Quantity: 1,
		Entry: model.Order{ID: 1, ExchangeID: 111}, CreatedAt: start.Add(-time.Hour)}, stop: 98}
	m := New(context.Background(), broker, market, "1m", "mt5")
	now := start
	m.now = func() time.Time { return now }
	m.Apply(settings(ui.BreakevenSettings{}, ui.TrailingSettings{Enabled: true, Trigger: 2.5, Distance: 0.5,
		Step: 0.2}), nil)
	m.OnCandle(model.Candle{Pair: pair, Time: start, Close: 100.8, High: 100.8, Low: 100.8})
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		ps := m.pairs[pair]
		return ps.loaded && ps.rebuilt
	}, time.Second, time.Millisecond)

	now = now.Add(3 * time.Second)
	m.OnCandle(model.Candle{Pair: pair, Time: start, Close: 100.9, High: 100.9, Low: 100.9})
	m.Wait()
	require.Equal(t, []float64{100.4}, broker.tightened, "the trigger, 3 ATR before the restart; the trail from now")
}

// A candle in progress never changes the ATR: the closed ones only.
func TestManager_ATROfTheClosedCandles(t *testing.T) {
	h := newHarness(t, model.SideTypeBuy, 98, nil)
	h.m.Apply(settings(ui.BreakevenSettings{Enabled: true, Trigger: 1, Margin: 0.1}, ui.TrailingSettings{}), nil)
	h.m.OnCandle(model.Candle{Pair: pair, Time: start, Open: 100, Close: 100.2, High: 130, Low: 70})
	h.m.Wait()
	require.InDelta(t, 1, h.state().ATR, 1e-9)
}
