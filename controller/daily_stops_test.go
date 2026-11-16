//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// unitSymbol is a symbol of the tests whose move of 1 in the price is worth 1
// in the currency of the account per lot: a tick of 0.01 worth 0.01.
var unitSymbol = dailySymbol{Digits: 2, Point: 0.01, TickSize: 0.01, TickValue: 0.01}

// holding returns an open position of the tests of 1 lot of unitSymbol at
// price, its stop loss stop (0 for none), short when sell.
func holding(name string, sell bool, price, stop float64) dailyHolding {
	return dailyHolding{agent: store.Agent{Name: name}, account: "1", pair: "XAUUSDm", sell: sell, price: price,
		stop: stop, volume: 1, symbol: unitSymbol, currency: "USD"}
}

// The stops within the loss left of a limit: the limit plus the result of the
// day less the exit costs; nothing when the stops now fit in it or nothing is
// left; shared among the positions in proportion to the loss to their stops,
// a long's below its price, a short's above it; a position without a stop
// weighed by its exposure, the ones whose stop is tighter than their share
// keeping it and leaving the rest; the realized losses shrinking it; rounded
// toward the price to the tick; beyond the least distance of the broker; a
// stop only moved by more than a step; the excluded ones never stopped.
func TestDailyStopTargets(t *testing.T) {
	t.Run("the stops now fit in the loss left", func(t *testing.T) {
		targets, _, headroom := dailyStopTargets(100, -10, []dailyHolding{holding("a", false, 4160, 4150)})
		assert.Empty(t, targets)
		assert.InDelta(t, 90, headroom, 1e-9)
	})
	t.Run("a long beyond it", func(t *testing.T) {
		targets, _, headroom := dailyStopTargets(100, -10, []dailyHolding{holding("a", false, 4160, 4000)})
		require.Len(t, targets, 1)
		assert.InDelta(t, 90, headroom, 1e-9)
		assert.InDelta(t, 4070, targets[0].level, 1e-9, "its whole share: 90 below the price")
		assert.InDelta(t, 90, targets[0].share, 1e-9)
		require.NotNil(t, targets[0].risk)
		assert.InDelta(t, 160, *targets[0].risk, 1e-9)
	})
	t.Run("a short beyond it", func(t *testing.T) {
		targets, _, _ := dailyStopTargets(100, -10, []dailyHolding{holding("a", true, 4160, 4300)})
		require.Len(t, targets, 1)
		assert.InDelta(t, 4250, targets[0].level, 1e-9, "above its price")
	})
	t.Run("several positions share it in proportion to their risk", func(t *testing.T) {
		holdings := []dailyHolding{holding("a", false, 4160, 4060), holding("b", true, 2000, 2050)}
		targets, _, headroom := dailyStopTargets(100, -10, holdings)
		require.Len(t, targets, 2)
		assert.InDelta(t, 90, headroom, 1e-9)
		// risks 100 and 50 of 150 for 90: 60 and 30, never 90 each
		assert.InDelta(t, 4100, targets[0].level, 1e-9)
		assert.InDelta(t, 2030, targets[1].level, 1e-9)
		assert.InDelta(t, 90, targets[0].share+targets[1].share, 1e-9, "the loss left, once")
	})
	t.Run("a position without a stop weighed by its exposure", func(t *testing.T) {
		holdings := []dailyHolding{holding("a", false, 100, 90), holding("b", false, 100, 0)}
		targets, _, _ := dailyStopTargets(20, 0, holdings)
		// risk 10 for an exposure of 100: the one without a stop weighs 10 too, 10 each: the first fits
		require.Len(t, targets, 1)
		assert.Equal(t, 1, targets[0].holding)
		assert.InDelta(t, 90, targets[0].level, 1e-9)
		assert.Nil(t, targets[0].risk)
	})
	t.Run("a stop tighter than its share leaves the rest to the others", func(t *testing.T) {
		holdings := []dailyHolding{holding("a", false, 100, 98), holding("b", false, 100, 0)}
		targets, _, _ := dailyStopTargets(20, 0, holdings)
		require.Len(t, targets, 1)
		assert.InDelta(t, 82, targets[0].level, 1e-9, "20 left, 2 taken by the first: 18 for the second")
	})
	t.Run("the realized losses and the costs shrink it", func(t *testing.T) {
		h := holding("a", false, 4160, 4000)
		h.costs = -2
		targets, costs, headroom := dailyStopTargets(100, -60, []dailyHolding{h})
		require.Len(t, targets, 1)
		assert.InDelta(t, 2, costs, 1e-9)
		assert.InDelta(t, 38, headroom, 1e-9)
		assert.InDelta(t, 4122, targets[0].level, 1e-9)
	})
	t.Run("nothing left", func(t *testing.T) {
		targets, _, headroom := dailyStopTargets(100, -100, []dailyHolding{holding("a", false, 4160, 4000)})
		assert.Empty(t, targets, "the close of the limit sees to it")
		assert.LessOrEqual(t, headroom, 0.0)
	})
	t.Run("rounded toward the price", func(t *testing.T) {
		targets, _, _ := dailyStopTargets(100, -10.005, []dailyHolding{holding("a", false, 4160, 4000)})
		require.Len(t, targets, 1)
		assert.InDelta(t, 4070.01, targets[0].level, 1e-9, "4070.005 up: never looser than its share")
		targets, _, _ = dailyStopTargets(100, -10.005, []dailyHolding{holding("a", true, 4160, 4300)})
		require.Len(t, targets, 1)
		assert.InDelta(t, 4249.99, targets[0].level, 1e-9)
	})
	t.Run("beyond the least distance of the broker", func(t *testing.T) {
		h := holding("a", false, 4160, 4000)
		h.symbol.StopsLevel = 300 // 3.00
		targets, _, _ := dailyStopTargets(1, 0, []dailyHolding{h})
		require.Len(t, targets, 1)
		assert.InDelta(t, 4156.99, targets[0].level, 1e-9, "a tick beyond the 3.00 of the broker")
		assert.True(t, targets[0].clamped)
	})
	t.Run("a stop moved only by more than a step", func(t *testing.T) {
		targets, _, _ := dailyStopTargets(100, -10, []dailyHolding{holding("a", false, 4160, 4069)})
		assert.Empty(t, targets, "4070 against 4069: under 2% of the distance")
	})
	t.Run("the excluded ones never stopped, their costs weighed", func(t *testing.T) {
		pair := holding("a", false, 4160, 0)
		pair.excluded, pair.costs = "pairs", -5
		targets, costs, headroom := dailyStopTargets(100, -10, []dailyHolding{pair, holding("b", false, 100, 0)})
		require.Len(t, targets, 1)
		assert.Equal(t, 1, targets[0].holding)
		assert.InDelta(t, 5, costs, 1e-9)
		assert.InDelta(t, 85, headroom, 1e-9)
		assert.InDelta(t, 15, targets[0].level, 1e-9)
	})
}

// newStopsSupervised returns a supervisor of the tests whose limiter moves the
// stops before evaluate returns, and the agent trend running.
func newStopsSupervised(t *testing.T) (*supervised, *dailyClock, *fakeAgent) {
	t.Helper()
	s, clock, trend := newDailySupervised(t)
	s.sup.daily.stopsSync = true
	return s, clock, trend
}

// The loss limit of all the agents with its stops on: a position of an agent
// running beyond the loss left gets its stop asked of the agent, confirmed by
// the broker and recorded with its budget; the price going against it leaves
// the stop where it is; a minute later nothing sent again; the stops off, the
// stop ended and the runtime of the agent without it.
func TestDailyLimiter_StopsThroughTheAgent(t *testing.T) {
	s, clock, trend := newStopsSupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	setGlobal(t, s, 50, 0)
	// 0.01 lot, a tick of 0.001 worth 0.5 a lot: 5 per 1 of the price
	p := position(10, 101, "XAUUSDm", false, 0.01, -10, 0)
	p.SL = 4100
	s.bridge.set(p)

	require.NoError(t, l.evaluate(ctx))
	trend.mu.Lock()
	require.Len(t, trend.dailyStops, 1)
	request := trend.dailyStops[0]
	trend.mu.Unlock()
	require.Len(t, request.Stops, 1)
	assert.Equal(t, "XAUUSDm", request.Stops[0].Pair)
	assert.Equal(t, "BUY", request.Stops[0].Side)
	// 50 - 10 left = 40: 8 of the price below 4161
	assert.InDelta(t, 4153, request.Stops[0].Level, 1e-9)
	assert.Equal(t, 4153.0, s.bridge.held()[0].SL, "at the broker")

	status := l.Status()
	require.Len(t, status.Stops, 1)
	stop := status.Stops[0]
	assert.Equal(t, "trend", stop.Agent)
	assert.Equal(t, ui.DailyStopConfirmed, stop.Move)
	assert.Equal(t, store.StopViaAgent, stop.Via)
	assert.InDelta(t, 4100, stop.Previous, 1e-9)
	assert.InDelta(t, 4153, stop.Confirmed, 1e-9)
	assert.InDelta(t, 40, stop.Headroom, 1e-9)
	assert.InDelta(t, 40, stop.Share, 1e-9)
	assert.Equal(t, []int64{10}, stop.Tickets)
	a, err := s.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	protection, err := protectionOf(ctx, s.store, a)
	require.NoError(t, err)
	require.Len(t, protection.DailyStops, 1, "in the runtime: kept across a restart of the agent")

	// the price against it, a minute later: the stop stays, nothing sent again
	clock.add(time.Minute)
	p = position(10, 101, "XAUUSDm", false, 0.01, -20, 0)
	p.PriceCurrent, p.SL = 4159, 4153
	s.bridge.set(p)
	require.NoError(t, l.evaluate(ctx))
	trend.mu.Lock()
	assert.Len(t, trend.dailyStops, 1, "the stop where the loss left asks it already")
	trend.mu.Unlock()

	// the stops off: ended, the runtime without them
	off := false
	loss := 50.0
	_, _, err = s.store.SetGlobalDailyLimit(ctx, ui.DailyLimitSettings{Enabled: true, MaxLoss: &loss,
		BrokerStops: &off}, store.DailyChange{Day: "2026-10-28", Via: store.ViaSettings, By: "admin"})
	require.NoError(t, err)
	clock.add(time.Minute)
	require.NoError(t, l.evaluate(ctx))
	inForce, err := s.store.DailyStopsInForce(ctx, 0)
	require.NoError(t, err)
	assert.Empty(t, inForce)
	assert.Empty(t, l.Status().Stops)
	protection, err = protectionOf(ctx, s.store, a)
	require.NoError(t, err)
	assert.Empty(t, protection.DailyStops)
	assert.Equal(t, 4153.0, s.bridge.held()[0].SL, "the stop at the broker left as it is")
}

// The stops of the agents stopped moved on their bridge, ticket by ticket,
// checked on the account; the loss left shared with the position of the
// agent running; the limit off, every stop ended.
func TestDailyLimiter_StopsOnTheBridge(t *testing.T) {
	s, clock, trend := newStopsSupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	s.liveAgent(t, "gold", 303, false)
	setGlobal(t, s, 60, 0)
	running := position(10, 101, "XAUUSDm", false, 0.01, 0, 0)
	running.SL = 4141 // risk 20 * 5 = 100
	stopped := position(30, 303, "XAUUSDm", true, 0.01, 0, 0)
	stopped.PriceCurrent, stopped.SL, stopped.TP = 4160.2, 4200.2, 4100 // risk 40 * 5 = 200
	s.bridge.set(running, stopped)

	require.NoError(t, l.evaluate(ctx))
	// 60 for risks 100 and 200: 20 and 40, ie 4 and 8 of the price
	trend.mu.Lock()
	require.Len(t, trend.dailyStops, 1)
	assert.InDelta(t, 4157, trend.dailyStops[0].Stops[0].Level, 1e-9)
	trend.mu.Unlock()
	var sltp map[string]any
	for _, trade := range s.bridge.tradeRequests() {
		if trade["action"] == 6.0 {
			sltp = trade
		}
	}
	require.NotNil(t, sltp, "on the bridge of the agent stopped")
	assert.Equal(t, 30.0, sltp["position"])
	assert.InDelta(t, 4168.2, sltp["sl"], 1e-9)
	assert.InDelta(t, 4100, sltp["tp"], 1e-9, "its take profit kept")

	stops := l.Status().Stops
	require.Len(t, stops, 2)
	byAgent := map[string]ui.DailyStopInfo{}
	for _, stop := range stops {
		byAgent[stop.Agent] = stop
	}
	assert.Equal(t, store.StopViaBridge, byAgent["gold"].Via)
	assert.Equal(t, ui.DailyStopConfirmed, byAgent["gold"].Move)
	assert.InDelta(t, 4168.2, byAgent["gold"].Confirmed, 1e-9, "read back on the account")
	assert.Equal(t, "SELL", byAgent["gold"].Side)

	// the limit off: every stop ended
	_, _, err := s.store.SetGlobalDailyLimit(ctx, ui.DailyLimitSettings{}, store.DailyChange{Day: "2026-10-28",
		Via: store.ViaSettings, By: "admin"})
	require.NoError(t, err)
	clock.add(time.Minute)
	require.NoError(t, l.evaluate(ctx))
	inForce, err := s.store.DailyStopsInForce(ctx, 0)
	require.NoError(t, err)
	assert.Empty(t, inForce)
}

// The loss left exhausted: no stop asked (the close of the limit sees to the
// positions); a stop refused by the broker recorded, the stop kept, asked
// again at the next round.
func TestDailyLimiter_StopsRefusedAndExhausted(t *testing.T) {
	s, clock, trend := newStopsSupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	setGlobal(t, s, 50, 0)
	p := position(10, 101, "XAUUSDm", false, 0.01, -49, 0)
	p.SL = 4100
	s.bridge.set(p)
	trend.mu.Lock()
	trend.magic = 999 // its positions not found: the stops refused
	trend.mu.Unlock()

	require.NoError(t, l.evaluate(ctx))
	stops := l.Status().Stops
	require.Len(t, stops, 1)
	assert.Equal(t, ui.DailyStopRejected, stops[0].Move)
	assert.Contains(t, stops[0].Detail, "nenhuma posição aberta")
	assert.InDelta(t, 4160.8, stops[0].Level, 1e-9, "1 left: 0.2 below the price")

	// beyond the limit: nothing left, no stop asked again
	trend.mu.Lock()
	sent := len(trend.dailyStops)
	trend.mu.Unlock()
	p.Profit = -60
	s.bridge.set(p)
	clock.add(time.Minute)
	require.NoError(t, l.evaluate(ctx))
	trend.mu.Lock()
	assert.Len(t, trend.dailyStops, sent+1, "the stop in force asked again, none tighter")
	trend.mu.Unlock()
}

// The switch of the stops at the broker through the API: on while never set,
// turned off and on for all the agents and for an agent, apart; a change
// computing the stops again at once, without waiting for the next minute.
func TestDailyLimits_BrokerStopsThroughTheAPI(t *testing.T) {
	s, _, trend := newStopsSupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	call, _ := settingsSession(t, s.managed)
	p := position(10, 101, "XAUUSDm", false, 0.01, 0, 0)
	p.SL = 4000
	s.bridge.set(p)

	resp, body := call(http.MethodGet, "/api/settings/daily-limits", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var got ui.DailyLimitsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	require.NotNil(t, got.Settings.BrokerStops)
	assert.True(t, *got.Settings.BrokerStops, "on while never set")

	off := false
	request := ui.DailyLimitsRequest{Settings: ui.DailyLimitSettings{Enabled: true, MaxLoss: ptrTo(100.0),
		BrokerStops: &off}}
	resp, body = call(http.MethodPut, "/api/settings/daily-limits", request)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	global, _, err := s.store.GlobalDailyLimit(ctx)
	require.NoError(t, err)
	assert.False(t, *global.Settings().BrokerStops)
	require.NoError(t, l.evaluate(ctx))
	trend.mu.Lock()
	assert.Empty(t, trend.dailyStops, "off: no stop asked")
	trend.mu.Unlock()

	on := true
	request.Settings.BrokerStops = &on
	resp, body = call(http.MethodPut, "/api/settings/daily-limits", request)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, l.evaluate(ctx)) // the same minute: due at once after the change
	trend.mu.Lock()
	assert.Len(t, trend.dailyStops, 1, "on again: computed at once")
	trend.mu.Unlock()

	// of an agent, apart
	resp, body = call(http.MethodPut, "/api/settings/agents/trend/daily-limit", ui.DailyLimitsRequest{
		Settings: ui.DailyLimitSettings{Enabled: true, MaxLoss: ptrTo(30.0), BrokerStops: &off}})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	a, err := s.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	assert.False(t, *a.DailyLimitOf().BrokerStops)
	global, _, err = s.store.GlobalDailyLimit(ctx)
	require.NoError(t, err)
	assert.True(t, *global.Settings().BrokerStops, "all the agents untouched")
}
