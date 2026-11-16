package protect

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/ui"
)

// limitsMarket tells the least distance of a stop loss from the market.
type limitsMarket struct {
	fakeMarket
	stops float64
}

func (m *limitsMarket) ProtectionLimits(string) (service.ProtectionLimits, bool) {
	return service.ProtectionLimits{StopsLevel: m.stops, Tick: 0.01}, true
}

// The stop of the daily loss limits of a long, the protection otherwise off:
// the stop guard on while it is asked; the stop loss tightened to its level on
// the next price, never loosened by a looser one; within the least distance of
// the broker, the nearest level it takes; past the price, nothing moved; none
// asked, the guard off again.
func TestManager_DailyStopOfALong(t *testing.T) {
	h := newHarness(t, model.SideTypeBuy, 95, &limitsMarket{fakeMarket: fakeMarket{closed: atrCandles(20, 100)},
		stops: 0.5})
	require.False(t, h.m.Guarded(pair))
	h.m.ApplyDailyStops([]ui.DailyStop{{Pair: pair, Side: "BUY", Level: 97, Reason: "limite de perda diária"}})
	require.True(t, h.m.Guarded(pair), "the strategies may only tighten the stop meanwhile")

	h.price(100)
	tightened, _ := h.broker.moves()
	require.Equal(t, []float64{97}, tightened)
	state := h.state()
	require.NotNil(t, state.Daily)
	require.Equal(t, ui.ProtectionActive, state.Daily.State)
	require.InDelta(t, 97, state.Daily.Level, 1e-9)
	require.NotNil(t, state.Daily.Last)
	require.Equal(t, ui.MoveConfirmed, state.Daily.Last.Status)

	// a looser level: kept
	h.m.ApplyDailyStops([]ui.DailyStop{{Pair: pair, Side: "BUY", Level: 96}})
	h.price(100)
	tightened, _ = h.broker.moves()
	require.Len(t, tightened, 1, "never loosened")
	require.InDelta(t, 97, h.state().Daily.Level, 1e-9, "the stop as tight already")

	// within the least distance of the broker (0.5): the nearest level it takes
	h.m.ApplyDailyStops([]ui.DailyStop{{Pair: pair, Side: "BUY", Level: 99.8}})
	h.price(100)
	tightened, _ = h.broker.moves()
	require.Equal(t, []float64{97, 99.49}, tightened, "a tick beyond the least distance")
	require.Contains(t, h.state().Daily.Detail, "distância mínima da corretora")

	// past the price: nothing moved, the close of the limit sees to it
	h.m.ApplyDailyStops([]ui.DailyStop{{Pair: pair, Side: "BUY", Level: 101}})
	h.price(100)
	tightened, _ = h.broker.moves()
	require.Len(t, tightened, 2)
	require.Equal(t, ui.ProtectionWaiting, h.state().Daily.State)

	h.m.ApplyDailyStops(nil)
	require.False(t, h.m.Guarded(pair))
}

// The stop of the daily loss limits of a short, on the ask; one asked of the
// other side (another position than the one the limits weighed) refused.
func TestManager_DailyStopOfAShort(t *testing.T) {
	h := newHarness(t, model.SideTypeSell, 105, nil)
	h.m.ApplyDailyStops([]ui.DailyStop{{Pair: pair, Side: "SELL", Level: 102}})
	h.price(100) // the ask at 100.2
	tightened, _ := h.broker.moves()
	require.Equal(t, []float64{102}, tightened)

	h.m.ApplyDailyStops([]ui.DailyStop{{Pair: pair, Side: "BUY", Level: 101}})
	h.price(100)
	tightened, _ = h.broker.moves()
	require.Len(t, tightened, 1)
	require.Equal(t, ui.ProtectionOff, h.state().Daily.State)
}

// A position without a stop loss gets the one of the daily loss limits.
func TestManager_DailyStopSetsAStop(t *testing.T) {
	h := newHarness(t, model.SideTypeBuy, 0, nil)
	h.m.ApplyDailyStops([]ui.DailyStop{{Pair: pair, Side: "BUY", Level: 96}})
	h.price(100)
	_, set := h.broker.moves()
	require.Equal(t, []float64{96}, set)
}

// The stops of the daily loss limits asked by the controller, applied at once:
// applied with the stop the broker confirmed; as tight already, applied with
// the stop it is at; a more protective breakeven applied in its place; the
// broker refusing, failed, the stop kept; its answer lost, unknown; without a
// position, or on another side, refused.
func TestManager_DailyStops(t *testing.T) {
	h := newHarness(t, model.SideTypeBuy, 95, nil)
	h.price(100)

	answer := h.m.DailyStops(ui.SupervisionDailyStopsRequest{Stops: []ui.DailyStop{{Pair: pair, Side: "BUY",
		Level: 97}}})
	require.Len(t, answer.Results, 1)
	got := answer.Results[0]
	require.Equal(t, ui.ProtectApplied, got.Status)
	require.InDelta(t, 95, got.Previous, 1e-9)
	require.InDelta(t, 97, got.Confirmed, 1e-9)

	h.now = h.now.Add(minInterval)
	got = h.m.DailyStops(ui.SupervisionDailyStopsRequest{Stops: []ui.DailyStop{{Pair: pair, Side: "BUY",
		Level: 96}}}).Results[0]
	require.Equal(t, ui.ProtectApplied, got.Status, "as tight already")
	require.InDelta(t, 97, got.Confirmed, 1e-9)
	require.Contains(t, got.Detail, "já estava")

	// a breakeven more protective, the price up: applied in its place
	h.price(103)
	h.m.Apply(settings(ui.BreakevenSettings{Enabled: true, Trigger: 0, Margin: 1}, ui.TrailingSettings{}), nil)
	h.now = h.now.Add(minInterval)
	got = h.m.DailyStops(ui.SupervisionDailyStopsRequest{Stops: []ui.DailyStop{{Pair: pair, Side: "BUY",
		Level: 97.5}}}).Results[0]
	require.Equal(t, ui.ProtectApplied, got.Status)
	require.InDelta(t, 101, got.Confirmed, 1e-9, "the breakeven, entry plus 1 ATR")
	require.Contains(t, got.Detail, "breakeven")
	h.m.Apply(nil, nil)

	// refused by the broker, then its answer lost
	h.broker.mu.Lock()
	h.broker.stop, h.broker.next = 95, refusal("trade_stops_level")
	h.broker.mu.Unlock()
	h.now = h.now.Add(coolDown)
	got = h.m.DailyStops(ui.SupervisionDailyStopsRequest{Stops: []ui.DailyStop{{Pair: pair, Side: "BUY",
		Level: 97}}}).Results[0]
	require.Equal(t, ui.ProtectFailed, got.Status)
	require.Contains(t, got.Detail, "trade_stops_level")
	h.broker.mu.Lock()
	h.broker.next = exchange.ErrMT5OutcomeUnknown
	h.broker.mu.Unlock()
	got = h.m.DailyStops(ui.SupervisionDailyStopsRequest{Stops: []ui.DailyStop{{Pair: pair, Side: "BUY",
		Level: 97}}}).Results[0]
	require.Equal(t, ui.ProtectUnknown, got.Status, "asked now: the cool-down skipped")

	got = h.m.DailyStops(ui.SupervisionDailyStopsRequest{Stops: []ui.DailyStop{{Pair: pair, Side: "SELL",
		Level: 103}}}).Results[0]
	require.Equal(t, ui.ProtectRefused, got.Status)
	h.broker.mu.Lock()
	h.broker.position.Quantity = 0 // closed
	h.broker.mu.Unlock()
	got = h.m.DailyStops(ui.SupervisionDailyStopsRequest{Stops: []ui.DailyStop{{Pair: pair, Level: 1}}}).Results[0]
	require.Equal(t, ui.ProtectRefused, got.Status)
	require.Contains(t, got.Detail, "nenhuma posição aberta")
}

// refusal returns an error of the broker refusing a move.
func refusal(text string) error { return &brokerError{text: text} }

type brokerError struct{ text string }

func (e *brokerError) Error() string { return "mt5: retcode 10016 invalid stops: " + e.text }
