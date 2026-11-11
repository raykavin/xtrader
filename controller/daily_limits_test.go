//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// belem is the zone of the daily limits of the tests.
func belem(t *testing.T) *time.Location {
	t.Helper()
	zone, err := time.LoadLocation("America/Belem")
	require.NoError(t, err)
	return zone
}

// The result of the day: the exits of the day with their costs and the costs
// of the entries of the day, of the agent of the entry of each position (its
// exit closed by hand without the magic), each deal once whatever the agents
// sharing the account; the profit open of their positions; the positions and
// deals of nobody left out; a position gone without its exit yet carried a
// while, never counted twice once its exit is read; an account not read told,
// its agents unread.
func TestDailyMetrics(t *testing.T) {
	zone := belem(t)
	day := time.Date(2026, 10, 28, 0, 0, 0, 0, zone)
	now := day.Add(14 * time.Hour)
	agent := func(name string, magic int64) bookedAgent {
		return bookedAgent{agent: store.Agent{Name: name, Enabled: true},
			book: agentapi.MT5Book{Magic: magic, Pairs: []string{"XAUUSDm"}}}
	}
	shared := liveAccount{bridge: "http://mt5", agents: []bookedAgent{agent("trend", 101), agent("orb", 202)}}
	other := liveAccount{bridge: "http://other", agents: []bookedAgent{agent("far", 303)}}
	at := func(d time.Duration) time.Time { return day.Add(d) }
	deals := []bridgeDeal{
		// trend: entered yesterday, closed by hand today (no magic on the exit)
		deal(1, 10, 101, dealEntryIn, at(-3*time.Hour), 0, 0, -1, 0),
		deal(2, 10, 0, dealEntryOut, at(9*time.Hour), -30, -0.5, -0.25, 0),
		// orb: entered today (its commission counted), closed today
		deal(3, 20, 202, dealEntryIn, at(10*time.Hour), 0, 0, -0.4, 0),
		deal(4, 20, 202, dealEntryOut, at(11*time.Hour), 12, 0, -0.4, 0),
		// by hand: nobody's
		deal(5, 30, 0, dealEntryIn, at(10*time.Hour), 0, 0, -1, 0),
		deal(6, 30, 0, dealEntryOut, at(12*time.Hour), 500, 0, 0, 0),
		// the same deal twice in the answer: once
		deal(4, 20, 202, dealEntryOut, at(11*time.Hour), 12, 0, -0.4, 0),
	}
	positions := []bridgePosition{position(40, 101, "XAUUSDm", false, 0.01, -20, -0.3),
		position(41, 0, "XAUUSDm", false, 0.01, 99, 0)}
	owners := map[string]dailyOwner{}
	window := closedRange{from: day, to: now}
	m := dailyMetrics(window, []liveAccount{shared, other}, []dailyRead{
		{account: bridgeAccount{Login: 1, Currency: "brl"}, deals: deals, positions: positions},
		{gap: gapUnreachable},
	}, owners, nil)
	require.InDelta(t, -30.75, m.byAgent["trend"]["BRL"].realized, 1e-9, "its exit, of the magic of its entry")
	require.InDelta(t, -20.3, m.byAgent["trend"]["BRL"].open, 1e-9)
	require.InDelta(t, 11.2, m.byAgent["orb"]["BRL"].realized, 1e-9, "the entry's commission and the exit")
	require.Len(t, m.byAgent, 2, "nobody's deals and positions left out")
	require.True(t, m.unread["far"])
	require.Len(t, m.missing, 1)
	require.Len(t, m.positions, 1)

	// the position closed, its exit not in the deals yet: carried
	window.to = now.Add(5 * time.Second)
	m = dailyMetrics(window, []liveAccount{shared}, []dailyRead{
		{account: bridgeAccount{Login: 1, Currency: "BRL"}, deals: deals},
	}, owners, m.carry)
	require.InDelta(t, -20.3, m.byAgent["trend"]["BRL"].open, 1e-9, "its last profit open, while its exit lags")
	// its exit read: realized, no longer carried
	window.to = now.Add(10 * time.Second)
	exit := deal(7, 40, 101, dealEntryOut, now.Add(time.Second), -20, -0.3, 0, 0)
	m = dailyMetrics(window, []liveAccount{shared}, []dailyRead{
		{account: bridgeAccount{Login: 1, Currency: "BRL"}, deals: append(deals, exit)},
	}, owners, m.carry)
	require.InDelta(t, -51.05, m.byAgent["trend"]["BRL"].realized, 1e-9)
	require.Zero(t, m.byAgent["trend"]["BRL"].open, "never counted twice")
	// gone for long without its exit: dropped
	m2 := dailyMetrics(closedRange{from: day, to: now.Add(2 * time.Minute)}, []liveAccount{shared},
		[]dailyRead{{account: bridgeAccount{Login: 1, Currency: "BRL"}, deals: deals}}, owners,
		map[string]dailyCarry{"1/40": {account: "1", agent: "trend", currency: "BRL", open: -20, seen: now}})
	require.Zero(t, m2.byAgent["trend"]["BRL"].open)
}

// A limit is reached by the cents of the result, on its side.
func TestDailyBreach(t *testing.T) {
	loss, gain := 50.0, 100.0
	limits := ui.DailyLimitSettings{Enabled: true, MaxLoss: &loss, MaxGain: &gain}
	for total, want := range map[float64]string{-50: store.TriggerLoss, -49.996: store.TriggerLoss,
		-49.99: "", 99.99: "", 100: store.TriggerGain, 250: store.TriggerGain} {
		got, _ := breach(total, limits)
		assert.Equal(t, want, got, "%v", total)
	}
}

// dailyClock is the clock of the limiter of the tests, moved by them.
type dailyClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *dailyClock) get() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *dailyClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *dailyClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// newDailySupervised returns a supervisor of the tests whose limiter runs on a
// clock of the tests, in Belém, and an agent trend live (magic 101) running.
func newDailySupervised(t *testing.T) (*supervised, *dailyClock, *fakeAgent) {
	t.Helper()
	s := newSupervised(t)
	clock := &dailyClock{now: time.Date(2026, 10, 28, 14, 0, 0, 0, belem(t))}
	s.sup.now = clock.get
	s.sup.daily = newDailyLimiter(s.sup)
	s.sup.daily.zone = belem(t)
	trend := s.liveAgent(t, "trend", 101, true)
	return s, clock, trend
}

func setGlobal(t *testing.T, s *supervised, loss, gain float64) {
	t.Helper()
	settings := ui.DailyLimitSettings{Enabled: true}
	if loss > 0 {
		settings.MaxLoss = &loss
	}
	if gain > 0 {
		settings.MaxGain = &gain
	}
	_, _, err := s.store.SetGlobalDailyLimit(context.Background(), settings,
		store.DailyChange{Day: "2026-10-28", Via: store.ViaSettings, By: "admin"})
	require.NoError(t, err)
}

// The daily limit of all the agents reached (realized plus open) in two reads
// in a row: the guard tripped once, the entries of the agents blocked
// (waiting for the orders being sent), their pending entries canceled and
// their positions closed, the manual ones untouched; recorded; a supervisor
// "allow entries" never lifting it; a controller started again keeping it
// without closing again; the next day releasing it.
func TestDailyLimiter_LossClosesEverythingAndBlocksUntilTheNextDay(t *testing.T) {
	s, clock, trend := newDailySupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	setGlobal(t, s, 50, 0)
	day := time.Date(2026, 10, 28, 0, 0, 0, 0, belem(t))
	s.bridge.mu.Lock()
	s.bridge.deals = []bridgeDeal{deal(1, 9, 101, dealEntryIn, day.Add(9*time.Hour), 0, 0, -1, 0),
		deal(2, 9, 101, dealEntryOut, day.Add(10*time.Hour), -30, 0, -1, 0)}
	s.bridge.mu.Unlock()
	s.bridge.set(position(10, 101, "XAUUSDm", false, 0.01, -10, 0), position(11, 0, "XAUUSDm", false, 0.01, -500, 0))
	trend.mu.Lock()
	trend.pending = []ui.SupervisedOrder{{ID: 77, Pair: "XAUUSDm", Entry: true}}
	trend.mu.Unlock()

	// under the limit: free
	require.NoError(t, l.evaluate(ctx))
	status := l.Status()
	require.Equal(t, ui.DailyFree, status.Global.State)
	require.InDelta(t, -42, status.Global.Result.Total, 1e-9, "-32 realized, -10 open, the manual one left out")
	require.Equal(t, "2026-10-28", status.Day)

	// beyond it: the first read alone doesn't trip
	s.bridge.set(position(10, 101, "XAUUSDm", false, 0.01, -25, 0), position(11, 0, "XAUUSDm", false, 0.01, -500, 0))
	require.NoError(t, l.evaluate(ctx))
	require.Equal(t, ui.DailyFree, l.Status().Global.State)
	clock.add(5 * time.Second)
	require.NoError(t, l.evaluate(ctx))
	status = l.Status()
	require.Equal(t, ui.DailyBlocked, status.Global.State)
	require.NotNil(t, status.Global.Guard)
	assert.Equal(t, store.TriggerLoss, status.Global.Guard.Trigger)
	assert.Contains(t, status.Global.Guard.Reason, "Perda diária de todos os agentes atingida: −57,00 BRL")

	a, err := s.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	blocked, _, _, err := s.store.DailyEntries(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, blocked)
	require.Eventually(t, func() bool {
		guards, err := s.store.TrippedDailyGuards(ctx)
		return err == nil && len(guards) == 1 && guards[0].FlattenStatus == store.FlattenDone
	}, 20*time.Second, 50*time.Millisecond, "its positions closed")
	assert.Equal(t, []bridgePosition{position(11, 0, "XAUUSDm", false, 0.01, -500, 0)}, s.bridge.held(),
		"the manual position untouched")
	trend.mu.Lock()
	assert.Positive(t, trend.drains, "the entries blocked, the orders being sent waited for")
	assert.Empty(t, trend.pending, "its pending entries canceled")
	trend.mu.Unlock()
	blockedNow, _ := trend.entries()
	assert.True(t, blockedNow)
	actions, err := s.store.SupervisorActions(ctx, 10)
	require.NoError(t, err)
	require.NotEmpty(t, actions)
	assert.Equal(t, store.ActionClosePositions, actions[0].Action)
	assert.Equal(t, dailyBy, actions[0].RequestedBy)
	assert.Equal(t, store.SourceSupervisor, actions[0].Source)
	assert.Equal(t, originRule, actionInfo(actions[0]).Origin, "the daily limit, never the supervisor")

	// the supervisor allows the entries: still blocked by the guard
	item := s.sup.setEntries(ctx, a, false, "liberar", "admin", nil)
	assert.Contains(t, item.Detail, "continuam bloqueadas pelo limite diário")
	a, err = s.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	entries, err := entriesOf(ctx, s.store, a)
	require.NoError(t, err)
	assert.True(t, entries.EntriesBlocked)

	// the controller started again: still blocked, nothing closed again
	trades := len(s.bridge.tradeRequests())
	restarted := newDailyLimiter(s.sup)
	restarted.zone = belem(t)
	clock.add(time.Minute)
	for range 3 {
		require.NoError(t, restarted.evaluate(ctx))
		clock.add(20 * time.Second)
	}
	assert.Equal(t, ui.DailyBlocked, restarted.Status().Global.State)
	assert.Equal(t, trades, len(s.bridge.tradeRequests()))

	// the next day: released, the entries back
	revision := entries.EntriesRevision
	clock.set(time.Date(2026, 10, 29, 0, 0, 30, 0, belem(t)))
	s.bridge.mu.Lock()
	s.bridge.deals = nil
	s.bridge.mu.Unlock()
	require.NoError(t, restarted.evaluate(ctx))
	assert.Equal(t, ui.DailyFree, restarted.Status().Global.State)
	entries, err = entriesOf(ctx, s.store, a)
	require.NoError(t, err)
	assert.False(t, entries.EntriesBlocked)
	assert.Greater(t, entries.EntriesRevision, revision, "the revision grew")
}

// The limit of all the agents applies in each currency of the accounts apart,
// none converted: a BRL account and a USD one each under it, whatever their
// sum, free; the USD one reaching it, tripped, the reason in USD.
func TestDailyLimiter_ByCurrency(t *testing.T) {
	s, clock, _ := newDailySupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	usd := newFakeBridge(t, "bridge-token", 198974330, "USD")
	a := s.agent(t, "usd", "trend", true, onBridge(usd.URL, 303, "XAUUSDm"))
	require.NoError(t, s.store.SetAgentSecrets(ctx, a.ID, store.AgentSecrets{BridgeToken: secret("bridge-token")}))
	setGlobal(t, s, 50, 0)
	s.bridge.set(position(10, 101, "XAUUSDm", false, 0.01, -40, 0))
	usd.set(position(30, 303, "XAUUSDm", false, 0.01, -30, 0))

	for range 2 {
		require.NoError(t, l.evaluate(ctx))
		clock.add(5 * time.Second)
	}
	status := l.Status()
	require.Equal(t, ui.DailyFree, status.Global.State, "-40 BRL and -30 USD: each under 50")
	require.Len(t, status.Global.Results, 2)
	assert.Equal(t, "BRL", status.Global.Results[0].Currency)
	assert.InDelta(t, -40, status.Global.Results[0].Total, 1e-9)
	assert.Equal(t, "USD", status.Global.Results[1].Currency)
	assert.InDelta(t, -30, status.Global.Results[1].Total, 1e-9)
	assert.Equal(t, "BRL", status.Global.Result.Currency, "the closest to the limit weighed")

	usd.set(position(30, 303, "XAUUSDm", false, 0.01, -55, 0))
	for range 2 {
		require.NoError(t, l.evaluate(ctx))
		clock.add(5 * time.Second)
	}
	status = l.Status()
	require.Equal(t, ui.DailyBlocked, status.Global.State)
	assert.Contains(t, status.Global.Guard.Reason, "−55,00 USD (limite 50,00 USD)")
	require.Eventually(t, func() bool {
		guards, err := s.store.TrippedDailyGuards(ctx)
		return err == nil && len(guards) == 1 && guards[0].FlattenStatus != store.FlattenRunning
	}, 20*time.Second, 50*time.Millisecond, "its close ended")
}

// The result unknown (the account not read) in two reads in a row: the
// entries blocked, nothing closed; read again under the limits, released.
func TestDailyLimiter_IncompleteBlocksTheEntries(t *testing.T) {
	s, clock, _ := newDailySupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	setGlobal(t, s, 50, 0)
	s.bridge.set(position(10, 101, "XAUUSDm", false, 0.01, -10, 0))
	s.bridge.mu.Lock()
	s.bridge.status = http.StatusInternalServerError
	s.bridge.mu.Unlock()

	require.NoError(t, l.evaluate(ctx))
	require.Equal(t, ui.DailyFree, l.Status().Global.State, "one read failed")
	clock.add(5 * time.Second)
	require.NoError(t, l.evaluate(ctx))
	status := l.Status()
	require.Equal(t, ui.DailyIncomplete, status.Global.State)
	require.False(t, status.Global.Result.Complete)
	a, err := s.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	blocked, reason, _, err := s.store.DailyEntries(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, blocked)
	assert.Contains(t, reason, "incompleto")
	assert.Empty(t, s.bridge.tradeRequests(), "nothing closed for data missing")

	s.bridge.mu.Lock()
	s.bridge.status = 0
	s.bridge.mu.Unlock()
	clock.add(5 * time.Second)
	require.NoError(t, l.evaluate(ctx))
	assert.Equal(t, ui.DailyFree, l.Status().Global.State)
	blocked, _, _, err = s.store.DailyEntries(ctx, a.ID)
	require.NoError(t, err)
	assert.False(t, blocked)
}

// A change of the limits: previewed, refused (409) when it trips or releases
// the guard now unless confirmed; a limit raised above the result releases
// the guard at once, recorded; the limit of an agent, its gain reached, trips
// only its own guard.
func TestDailyLimits_Changes(t *testing.T) {
	s, clock, _ := newDailySupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	s.liveAgent(t, "orb", 202, true)
	s.bridge.set(position(10, 101, "XAUUSDm", false, 0.01, -60, 0), position(20, 202, "XAUUSDm", false, 0.01, 30, 0))
	call, _ := settingsSession(t, s.managed)

	// a limit already reached: refused without the confirmation, its preview told
	request := ui.DailyLimitsRequest{Settings: ui.DailyLimitSettings{Enabled: true, MaxLoss: ptrTo(20.0)},
		Reason: "teste"}
	resp, body := call(http.MethodPut, "/api/settings/daily-limits", request)
	require.Equal(t, http.StatusConflict, resp.StatusCode, body)
	var preview ui.DailyLimitPreview
	require.NoError(t, json.Unmarshal([]byte(body), &preview))
	assert.Equal(t, ui.DailyEffectTrip, preview.Effect)
	assert.InDelta(t, -30, preview.Result.Total, 1e-9)
	_, found, err := s.store.GlobalDailyLimit(ctx)
	require.NoError(t, err)
	require.False(t, found, "nothing saved")

	request.Confirm = true
	resp, body = call(http.MethodPut, "/api/settings/daily-limits", request)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	for range 2 {
		require.NoError(t, l.evaluate(ctx))
		clock.add(5 * time.Second)
	}
	require.Equal(t, ui.DailyBlocked, l.Status().Global.State)
	require.Eventually(t, func() bool {
		guards, err := s.store.TrippedDailyGuards(ctx)
		return err == nil && len(guards) == 1 && guards[0].FlattenStatus != store.FlattenRunning
	}, 20*time.Second, 50*time.Millisecond)

	// raised far above the result: the guard released at once
	request.Settings.MaxLoss, request.Confirm = ptrTo(1000.0), false
	resp, body = call(http.MethodPut, "/api/settings/daily-limits", request)
	require.Equal(t, http.StatusConflict, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &preview))
	assert.Equal(t, ui.DailyEffectRelease, preview.Effect)
	resp, body = call(http.MethodPut, "/api/settings/daily-limits", ui.DailyLimitsRequest{Settings: request.Settings,
		Confirm: true, Reason: "dia de teste"})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var response ui.DailyLimitsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	assert.Equal(t, ui.DailyEffectRelease, response.Effect)
	guards, err := s.store.TrippedDailyGuards(ctx)
	require.NoError(t, err)
	assert.Empty(t, guards)

	// the gain of the agent orb: only its guard
	s.bridge.set(position(10, 101, "XAUUSDm", false, 0.01, -1, 0), position(20, 202, "XAUUSDm", false, 0.01, 30, 0))
	resp, body = call(http.MethodPut, "/api/settings/agents/orb/daily-limit", ui.DailyLimitsRequest{
		Settings: ui.DailyLimitSettings{Enabled: true, MaxGain: ptrTo(25.0)}, Confirm: true})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	for range 2 {
		clock.add(5 * time.Second)
		require.NoError(t, l.evaluate(ctx))
	}
	status := l.Status()
	assert.Equal(t, ui.DailyFree, status.Global.State)
	require.Len(t, status.Agents, 1)
	assert.Equal(t, "orb", status.Agents[0].Agent)
	assert.Equal(t, ui.DailyBlocked, status.Agents[0].State)
	assert.Equal(t, store.TriggerGain, status.Agents[0].Guard.Trigger)
	for name, want := range map[string]bool{"orb": true, "trend": false} {
		a, err := s.store.AgentByName(ctx, name)
		require.NoError(t, err)
		blocked, _, _, err := s.store.DailyEntries(ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, want, blocked, name)
	}
	events, err := s.store.DailyLimitEvents(ctx, 50)
	require.NoError(t, err)
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Kind]++
	}
	assert.Equal(t, 3, kinds[store.EventLimitChanged])
	assert.Equal(t, 1, kinds[store.EventReleased])
	assert.Equal(t, 2, kinds[store.EventTripped])
}

// The gain of all the agents reached: the position of an agent live not
// running closed on its bridge (its launch fenced meanwhile), checked on the
// account.
func TestDailyLimiter_GainClosesAnAgentNotRunningOnItsBridge(t *testing.T) {
	s, clock, _ := newDailySupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	s.liveAgent(t, "idle", 202, false)
	setGlobal(t, s, 0, 40)
	s.bridge.set(position(20, 202, "XAUUSDm", false, 0.01, 45, 0))
	for range 2 {
		require.NoError(t, l.evaluate(ctx))
		clock.add(5 * time.Second)
	}
	status := l.Status()
	require.Equal(t, ui.DailyBlocked, status.Global.State)
	assert.Equal(t, store.TriggerGain, status.Global.Guard.Trigger)
	require.Eventually(t, func() bool { return len(s.bridge.held()) == 0 }, 20*time.Second, 50*time.Millisecond)
	trades := s.bridge.tradeRequests()
	require.Len(t, trades, 1)
	assert.EqualValues(t, 1, trades[0]["action"], "a close by its ticket")
	assert.EqualValues(t, 20, trades[0]["position"])
	require.Eventually(t, func() bool {
		guards, err := s.store.TrippedDailyGuards(ctx)
		return err == nil && len(guards) == 1 && guards[0].FlattenStatus == store.FlattenDone
	}, 20*time.Second, 50*time.Millisecond)
}

// An explicit and whole request in the conversation executes at once, without
// the confirmation of the interface: every position of the agents closed, the
// manual one untouched, their entries kept blocked, recorded as asked by the
// user (the message the evidence), its result added by the system; a request
// missing an option refused, nothing done; the periodic analysis never gets
// the tools of the conversation.
func TestSupervision_ChatExecutesAnExplicitRequest(t *testing.T) {
	s := newSupervised(t)
	ctx := context.Background()
	s.liveAgent(t, "fibonacci", 101, true)
	s.liveAgent(t, "emacross", 103, false)
	byHand := position(9, 0, "EURUSDm", false, 0.1, 50, 0)
	s.bridge.set(protected(1, 101), protected(3, 103), byHand)
	call, _ := settingsSession(t, s.managed)

	// missing the options: refused, the model asks
	var told string
	s.chat.script(
		calling(tool("e0", "execute_action", map[string]any{"action": store.ActionClosePositions, "all": true,
			"reason": "encerrar tudo"})),
		func(_ string, messages []llm.ChatMessage, _ []llm.ToolSpec) (llm.ChatAnswer, error) {
			told = messages[len(messages)-1].Content
			return llm.ChatAnswer{Content: "Quer cancelar as pendentes também?"}, nil
		})
	resp, body := call(http.MethodPost, "/api/supervisor/messages", ui.SupervisorMessageRequest{Text: "feche tudo"})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Contains(t, told, "ambiguous")
	assert.Empty(t, s.bridge.tradeRequests())

	s.chat.script(
		calling(tool("e1", "execute_action", map[string]any{"action": store.ActionClosePositions, "all": true,
			"cancel_pending": true, "keep_blocked": true, "reason": "o usuário pediu para encerrar tudo",
			"category": store.CategoryRisk})),
		saying("Feito: posições encerradas e agentes bloqueados."))
	resp, body = call(http.MethodPost, "/api/supervisor/messages", ui.SupervisorMessageRequest{
		Text: "encerre todas as posições e bloqueie todos os agentes"})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var chat ui.SupervisorMessagesResponse
	require.NoError(t, json.Unmarshal([]byte(body), &chat))
	kinds := []string{}
	for _, m := range chat.Messages {
		kinds = append(kinds, m.Kind)
	}
	assert.Equal(t, []string{store.MessageQuestion, store.MessageResult, store.MessageAnswer}, kinds,
		"no proposal: the result written by the system")
	require.Eventually(t, func() bool { return len(s.bridge.held()) == 1 }, 20*time.Second, 50*time.Millisecond)
	assert.Equal(t, []bridgePosition{byHand}, s.bridge.held(), "the manual position untouched")
	actions, err := s.store.SupervisorActions(ctx, 10)
	require.NoError(t, err)
	require.NotEmpty(t, actions)
	action := actions[0]
	assert.Equal(t, store.SourceUser, action.Source)
	assert.Equal(t, "admin", action.ConfirmedBy)
	assert.Contains(t, string(action.Evidence), "encerre todas as posições e bloqueie todos os agentes")
	assert.NotEqual(t, store.ActionProposed, action.Status)
	for _, name := range []string{"fibonacci", "emacross"} {
		a, err := s.store.AgentByName(ctx, name)
		require.NoError(t, err)
		assert.True(t, a.SupervisionOf().EntriesBlocked, name)
	}

	names := func(tools []llm.ToolSpec) []string {
		var out []string
		for _, tool := range tools {
			out = append(out, tool.Name)
		}
		return out
	}
	assert.Subset(t, names(chatTools()), []string{"execute_action", "get_daily_limits", "set_daily_limits"})
	for _, name := range names(analysisTools(store.Autonomy{BlockEntries: true, StopWhenFlat: true,
		CancelPending: true, MaxActionsPerRun: 3})) {
		assert.NotContains(t, []string{"execute_action", "set_daily_limits", "get_daily_limits"}, name)
	}
}

// The daily limits through the conversation: the scope required, never
// guessed; one limit of all the agents together; the same limit of each agent
// live, the paper ones told apart; recorded as changed through the chat.
func TestSupervision_ChatSetsDailyLimits(t *testing.T) {
	s, _, _ := newDailySupervised(t)
	ctx := context.Background()
	s.liveAgent(t, "orb", 202, true)
	s.agent(t, "paperbot", "trend", true, func(a *store.Agent) { a.Mode = "paper" })
	user := userOfTests(t, s)
	results := func(messages []llm.ChatMessage) string { return messages[len(messages)-1].Content }

	var told string
	s.chat.script(
		calling(tool("d0", "set_daily_limits", map[string]any{"max_loss": 150, "reason": "limitar a perda"})),
		func(_ string, messages []llm.ChatMessage, _ []llm.ToolSpec) (llm.ChatAnswer, error) {
			told = results(messages)
			return llm.ChatAnswer{Content: "É o total de todos ou cada agente?"}, nil
		})
	_, err := s.sup.Ask(adminCtx(ctx), user, "admin", "limite de perda de 150 por dia")
	require.NoError(t, err)
	assert.Contains(t, told, "aggregate_all")
	_, found, err := s.store.GlobalDailyLimit(ctx)
	require.NoError(t, err)
	assert.False(t, found, "nothing set")

	s.chat.script(
		calling(tool("d1", "set_daily_limits", map[string]any{"scope": "aggregate_all", "max_loss": 150,
			"max_gain": 300, "reason": "limite somado de todos os agentes"})),
		saying("Pronto."))
	added, err := s.sup.Ask(adminCtx(ctx), user, "admin", "perda máxima de 150 e ganho de 300 no total de todos os agentes")
	require.NoError(t, err)
	global, found, err := s.store.GlobalDailyLimit(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, global.Enabled)
	assert.Equal(t, 150.0, *global.MaxLoss)
	assert.Empty(t, global.Settings().Currency, "in the currency of each account")
	var result string
	for _, m := range added {
		if m.Kind == store.MessageResult {
			result = m.Content
		}
	}
	assert.Contains(t, result, "perda máxima 150,00")

	s.chat.script(
		calling(tool("d2", "set_daily_limits", map[string]any{"scope": "each_agent", "all": true, "max_loss": 40,
			"reason": "limite para cada agente"})),
		func(_ string, messages []llm.ChatMessage, _ []llm.ToolSpec) (llm.ChatAnswer, error) {
			told = results(messages)
			return llm.ChatAnswer{Content: "Pronto."}, nil
		})
	_, err = s.sup.Ask(adminCtx(ctx), user, "admin", "e um limite de perda de 40 para cada agente")
	require.NoError(t, err)
	for _, name := range []string{"trend", "orb"} {
		a, err := s.store.AgentByName(ctx, name)
		require.NoError(t, err)
		assert.Equal(t, 40.0, *a.DailyLimitOf().MaxLoss, name)
	}
	paper, err := s.store.AgentByName(ctx, "paperbot")
	require.NoError(t, err)
	assert.False(t, paper.DailyLimitOf().Enabled, "not of an agent in paper")
	assert.Contains(t, told, "não se aplica")
	events, err := s.store.DailyLimitEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, store.ViaChat, events[0].Via)
	assert.Equal(t, "admin", events[0].RequestedBy)
}
