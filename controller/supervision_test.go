//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	glog "gorm.io/gorm/logger"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

func init() {
	closeCheckFor, closeCheckEvery = 2*time.Second, 50*time.Millisecond
}

// answer is an answer of the fake model to a request.
type answer func(system string, messages []llm.ChatMessage, tools []llm.ToolSpec) (llm.ChatAnswer, error)

// fakeChat is the language model of the tests: it answers with the answers of
// its script in turn, then with a text, recording what it was asked; block,
// when set, holds each answer until it is closed.
type fakeChat struct {
	mu      sync.Mutex
	answers []answer
	asked   [][]llm.ChatMessage
	systems []string
	block   chan struct{}
}

func (f *fakeChat) Complete(ctx context.Context, system string, messages []llm.ChatMessage,
	tools []llm.ToolSpec) (llm.ChatAnswer, error) {
	f.mu.Lock()
	f.asked = append(f.asked, append([]llm.ChatMessage(nil), messages...))
	f.systems = append(f.systems, system)
	var next answer
	if len(f.answers) > 0 {
		next, f.answers = f.answers[0], f.answers[1:]
	}
	block := f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return llm.ChatAnswer{}, ctx.Err()
		}
	}
	if next == nil {
		return llm.ChatAnswer{Content: "Nada a acrescentar."}, nil
	}
	return next(system, messages, tools)
}

func (f *fakeChat) String() string { return "fake model" }

// script adds answers to the script of the model.
func (f *fakeChat) script(answers ...answer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, answers...)
}

// requests returns what the model was asked.
func (f *fakeChat) requests() [][]llm.ChatMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]llm.ChatMessage(nil), f.asked...)
}

// calling answers with calls of tools.
func calling(calls ...llm.ToolCall) answer {
	return func(string, []llm.ChatMessage, []llm.ToolSpec) (llm.ChatAnswer, error) {
		return llm.ChatAnswer{ToolCalls: calls}, nil
	}
}

// saying answers with text.
func saying(content string) answer {
	return func(string, []llm.ChatMessage, []llm.ToolSpec) (llm.ChatAnswer, error) {
		return llm.ChatAnswer{Content: content}, nil
	}
}

// failing answers with err.
func failing(err error) answer {
	return func(string, []llm.ChatMessage, []llm.ToolSpec) (llm.ChatAnswer, error) {
		return llm.ChatAnswer{}, err
	}
}

// tool returns a call of the tool name with args.
func tool(id, name string, args any) llm.ToolCall {
	data, _ := json.Marshal(args)
	return llm.ToolCall{ID: id, Name: name, Arguments: data}
}

// report is a call of the report of an analysis.
func report(findings ...ui.SupervisorFinding) llm.ToolCall {
	return tool("report", "report", map[string]any{"summary": "análise feita", "findings": findings})
}

// fakeAgent is the API of an agent of the tests as the supervisor reaches it:
// its entries, its positions, the ones of its magic number on its bridge,
// closed there as it is asked but on the pairs of closeStatus, answered as
// told, its pending entries, and the requests it got.
type fakeAgent struct {
	*httptest.Server
	mu          sync.Mutex
	bridge      *fakeBridge
	magic       int64
	blocked     bool
	reason      string
	revision    int64
	pending     []ui.SupervisedOrder
	closeStatus map[string]string
	requests    []string
	protections []ui.PositionProtection
	protect     ui.SupervisionProtectResponse // the answer to an arm
	armed       []ui.SupervisionProtectRequest
	drains      int // the blocks asking to wait for the orders being sent
	// fills are the positions the pending entries of their IDs open as they
	// are canceled; closes the close requests it got
	fills  map[int64]bridgePosition
	closes []ui.SupervisionCloseRequest
}

func newFakeAgent(t *testing.T, bridge *fakeBridge, magic int64) *fakeAgent {
	t.Helper()
	a := &fakeAgent{bridge: bridge, magic: magic, closeStatus: map[string]string{}}
	a.Server = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.Close)
	return a
}

// address returns the address of the API of the agent, host:port.
func (a *fakeAgent) address() string { return strings.TrimPrefix(a.URL, "http://") }

// positions returns the positions of the agent on its bridge, by pair.
func (a *fakeAgent) positions() map[string]float64 {
	held := make(map[string]float64)
	for _, p := range a.bridge.held() {
		if p.Magic != a.magic {
			continue
		}
		if p.Type == 1 {
			held[p.Symbol] -= p.Volume * 100
		} else {
			held[p.Symbol] += p.Volume * 100
		}
	}
	return held
}

func (a *fakeAgent) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.requests = append(a.requests, r.Method+" "+r.URL.Path)
	a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/api/pairs" {
		_ = json.NewEncoder(w).Encode(ui.PairsResponse{Pairs: []string{"XAUUSDm", "BTCUSDm"}})
		return
	}
	if r.Header.Get(ui.ChannelHeader) != string(ui.ChannelSupervisor) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := func() ui.SupervisionState {
		state := ui.SupervisionState{EntriesBlocked: a.blocked, EntriesReason: a.reason, EntriesRevision: a.revision,
			Status: "running", Positions: []ui.SupervisedPosition{}, Pending: append([]ui.SupervisedOrder{}, a.pending...)}
		for pair, asset := range a.positions() {
			side := "BUY"
			if asset < 0 {
				side, asset = "SELL", -asset
			}
			state.Positions = append(state.Positions, ui.SupervisedPosition{Pair: pair, Side: side, Quantity: asset})
		}
		state.Protections = append([]ui.PositionProtection(nil), a.protections...)
		return state
	}
	switch r.URL.Path {
	case "/api/supervision":
		_ = json.NewEncoder(w).Encode(state())
	case "/api/supervision/protect":
		var request ui.SupervisionProtectRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		a.armed = append(a.armed, request)
		_ = json.NewEncoder(w).Encode(a.protect)
	case "/api/supervision/entries":
		var request ui.SupervisionEntriesRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Revision >= a.revision {
			a.blocked, a.reason, a.revision = request.Blocked, request.Reason, request.Revision
		}
		if request.Drain {
			a.drains++
		}
		_ = json.NewEncoder(w).Encode(state())
	case "/api/supervision/close":
		var request ui.SupervisionCloseRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		a.closes = append(a.closes, request)
		if request.Block && request.Revision >= a.revision {
			a.blocked, a.reason, a.revision = true, request.Reason, request.Revision
		}
		response := ui.SupervisionCloseResponse{}
		if request.CancelPending {
			response.Canceled = a.cancel()
		}
		pairs := request.Pairs
		if len(pairs) == 0 {
			for pair := range a.positions() {
				pairs = append(pairs, pair)
			}
		}
		for _, pair := range pairs {
			if status := a.closeStatus[pair]; status != "" {
				response.Results = append(response.Results, ui.SupervisionCloseResult{Pair: pair, Status: status,
					Error: "as told"})
				continue
			}
			a.bridge.mu.Lock()
			var kept []bridgePosition
			for _, p := range a.bridge.positions {
				if p.Magic != a.magic || p.Symbol != pair {
					kept = append(kept, p)
				}
			}
			a.bridge.positions = kept
			a.bridge.mu.Unlock()
			response.Results = append(response.Results, ui.SupervisionCloseResult{Pair: pair, Status: ui.SupervisionClosed})
		}
		_ = json.NewEncoder(w).Encode(response)
	case "/api/supervision/cancel":
		_ = json.NewEncoder(w).Encode(ui.SupervisionCancelResponse{Results: a.cancel()})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// cancel cancels the pending entries of the agent, the ones of fills filled
// as they are canceled (gone, their position opened). The caller holds a.mu.
func (a *fakeAgent) cancel() []ui.SupervisionCancelResult {
	results := []ui.SupervisionCancelResult{}
	for _, o := range a.pending {
		if !o.Entry {
			continue
		}
		result := ui.SupervisionCancelResult{ID: o.ID, Pair: o.Pair, Status: ui.SupervisionCanceled}
		if filled, ok := a.fills[o.ID]; ok {
			result.Status = ui.SupervisionGone
			a.bridge.mu.Lock()
			a.bridge.positions = append(a.bridge.positions, filled)
			a.bridge.mu.Unlock()
		}
		results = append(results, result)
	}
	a.pending = slices.DeleteFunc(a.pending, func(o ui.SupervisedOrder) bool { return o.Entry })
	return results
}

// closeRequests returns the close requests the agent got.
func (a *fakeAgent) closeRequests() []ui.SupervisionCloseRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ui.SupervisionCloseRequest(nil), a.closes...)
}

// entries returns the entries of the agent.
func (a *fakeAgent) entries() (bool, int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.blocked, a.revision
}

// called returns whether the agent got a request of path.
func (a *fakeAgent) called(path string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.ContainsFunc(a.requests, func(r string) bool { return strings.HasSuffix(r, " "+path) })
}

// supervised is a controller of the tests with its supervisor: the model of
// the tests, the agents running as running says, their APIs served by fakes.
type supervised struct {
	*managed
	sup     *Supervision
	chat    *fakeChat
	bridge  *fakeBridge
	mu      sync.Mutex
	running map[string]bool
	apis    []Agent
}

func newSupervised(t *testing.T) *supervised {
	t.Helper()
	c := newManaged(t)
	s := &supervised{managed: c, chat: &fakeChat{}, running: map[string]bool{},
		bridge: newFakeBridge(t, "bridge-token", 198974329, "BRL")}
	s.sup = c.dashboard.ServeSupervision(c.store, c.manager)
	s.sup.newChat = func(store.SupervisorSettings, string) (llm.Chat, error) { return s.chat, nil }
	s.sup.stateOf = func(name string) AgentState {
		s.mu.Lock()
		defer s.mu.Unlock()
		state := c.manager.State(name)
		state.Running = s.running[name]
		return state
	}
	_, err := c.store.SetSupervisor(context.Background(), store.SupervisorUpdate{Enabled: boolPtr(true),
		Provider: ptrTo("openai"), Model: ptrTo("gpt-test"), APIKey: secret("sk-supervisor"),
		Autonomy: &store.Autonomy{BlockEntries: true, StopWhenFlat: true, MaxActionsPerRun: 3}})
	require.NoError(t, err)
	return s
}

// liveAgent creates an agent on the bridge of the tests, of magic, trading
// XAUUSDm, running with the API of a fake agent when running.
func (s *supervised) liveAgent(t *testing.T, name string, magic int64, running bool) *fakeAgent {
	t.Helper()
	a := s.agent(t, name, "trend", true, onBridge(s.bridge.URL, magic, "XAUUSDm"))
	require.NoError(t, s.store.SetAgentSecrets(context.Background(), a.ID,
		store.AgentSecrets{BridgeToken: secret("bridge-token")}))
	api := newFakeAgent(t, s.bridge, magic)
	s.mu.Lock()
	s.running[name] = running
	if running {
		s.apis = append(s.apis, Agent{Name: name, Address: api.address(), Kind: "trend"})
	}
	apis := append([]Agent(nil), s.apis...)
	s.mu.Unlock()
	require.NoError(t, s.dashboard.SetAgents(apis))
	return api
}

// protected returns a long position of XAUUSDm of magic with its stop loss.
func protected(ticket, magic int64) bridgePosition {
	p := position(ticket, magic, "XAUUSDm", false, 0.01, 12.5, 0)
	p.SL, p.TP = 4150, 4180
	return p
}

// The settings of the supervisor on the dashboard: its key written only, its
// interval and its timeout in their bounds, enabled only with its model.
func TestSupervisionAPI_Settings(t *testing.T) {
	s := newSupervised(t)
	call, _ := settingsSession(t, s.managed)

	resp, body := call(http.MethodGet, "/api/settings/supervisor", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var settings ui.SupervisorSettingsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &settings))
	assert.True(t, settings.Configured)
	assert.True(t, settings.Enabled)
	assert.True(t, settings.APIKeySet)
	assert.Equal(t, 900, settings.IntervalSeconds)
	assert.Equal(t, store.SupervisorMinInterval, settings.MinInterval)
	assert.NotContains(t, body, "sk-supervisor", "never the key")

	for _, request := range []ui.SupervisorSettingsRequest{{IntervalSeconds: ptrTo(30)},
		{IntervalSeconds: ptrTo(100000)}, {TimeoutSeconds: ptrTo(1)}, {APIKey: secret("")}} {
		resp, body = call(http.MethodPatch, "/api/settings/supervisor", request)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
	}
	resp, body = call(http.MethodPatch, "/api/settings/supervisor", ui.SupervisorSettingsRequest{
		IntervalSeconds: ptrTo(600), Instructions: ptrTo("vigie o ouro"), APIKey: secret("sk-rotated"),
		Autonomy: &ui.SupervisorAutonomy{BlockEntries: true, MaxActionsPerRun: 1}})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &settings))
	assert.Equal(t, 600, settings.IntervalSeconds)
	assert.Equal(t, "vigie o ouro", settings.Instructions)
	assert.False(t, settings.Autonomy.StopWhenFlat)
	assert.NotContains(t, body, "sk-rotated")
	key, err := s.store.SupervisorKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "sk-rotated", key)

	// without a session, nothing
	srv := httptest.NewServer(s.dashboard.auth.protect(s.dashboard.router(bundle{dir: t.TempDir(), version: "v1"},
		nil)))
	defer srv.Close()
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/settings/supervisor", "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// An analysis sends the model a snapshot of the agents, without a secret, and
// records its report; the model unavailable fails it, the agents untouched;
// an analysis asked while another runs is skipped.
func TestSupervision_Analysis(t *testing.T) {
	s := newSupervised(t)
	ctx := context.Background()
	s.liveAgent(t, "matrend", 87654, true)
	s.bridge.set(protected(1, 87654), position(9, 0, "EURUSDm", false, 0.1, 50, 0)) // a position by hand too

	s.chat.script(calling(report(ui.SupervisorFinding{Agent: "matrend", Category: store.CategoryRisk,
		Severity: "warning", Summary: "posição comprada", Evidence: "12,50 BRL em aberto"})))
	run, err := s.sup.Analyze(ctx, store.RunManual, "admin")
	require.NoError(t, err)
	got, err := s.store.SupervisorRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, store.RunOK, got.Status)
	assert.Equal(t, "análise feita", got.Summary)
	assert.Contains(t, string(got.Findings), "posição comprada")

	var snapshot ui.SupervisorSnapshot
	require.NoError(t, json.Unmarshal(got.Snapshot, &snapshot))
	require.Len(t, snapshot.Agents, 1)
	agent := snapshot.Agents[0]
	assert.Equal(t, "running", agent.State)
	assert.Equal(t, "conta 1", agent.Account)
	require.Len(t, agent.Positions, 1, "the position by hand left out")
	assert.Equal(t, 4150.0, agent.Positions[0].StopLoss)
	assert.Equal(t, []ui.CurrencyAmount{{Currency: "BRL", Amount: 12.5}}, agent.OpenProfit)
	assert.Equal(t, "0.01", agent.Risk["lot"])
	require.Len(t, snapshot.Exposure, 1)
	assert.Equal(t, 1, snapshot.Exposure[0].BuyPositions)
	for _, secret := range []string{"sk-supervisor", "bridge-token", s.bridge.URL, "198974329", "87654"} {
		assert.NotContains(t, string(got.Snapshot), secret, "never sent to the model: %s", secret)
	}
	requests := s.chat.requests()
	require.NotEmpty(t, requests)
	assert.Contains(t, requests[0][0].Content, "Retrato dos agentes")

	// unavailable: failed, nothing done
	s.chat.script(failing(llm.ErrUnavailable))
	run, err = s.sup.Analyze(ctx, store.RunSchedule, "")
	require.ErrorIs(t, err, llm.ErrUnavailable)
	got, err = s.store.SupervisorRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, store.RunFailed, got.Status)
	_, _, failures := s.sup.state()
	assert.Equal(t, 1, failures)
	actions, err := s.store.SupervisorActions(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, actions, "the agents untouched")

	// one at a time
	s.chat.block = make(chan struct{})
	s.chat.script(calling(report()))
	first := make(chan error, 1)
	go func() {
		_, err := s.sup.Analyze(ctx, store.RunSchedule, "")
		first <- err
	}()
	require.Eventually(t, func() bool { return len(s.chat.requests()) == 3 }, 5*time.Second, time.Millisecond)
	skipped, err := s.sup.Analyze(ctx, store.RunManual, "admin")
	require.ErrorIs(t, err, errAnalyzing)
	got, err = s.store.SupervisorRun(ctx, skipped.ID)
	require.NoError(t, err)
	assert.Equal(t, store.RunSkipped, got.Status)
	close(s.chat.block)
	require.NoError(t, <-first)
}

// The analyses are due an interval apart while enabled, the first one an
// interval after it is enabled; one left running by a controller before
// fails.
func TestSupervision_Schedule(t *testing.T) {
	s := newSupervised(t)
	ctx := context.Background()
	orphan, err := s.store.StartSupervisorRun(ctx, store.RunSchedule, store.RunRunning, "", "")
	require.NoError(t, err)
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.Local)
	var mu sync.Mutex
	s.sup.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	running, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		s.sup.Run(running) // cleans up, a tick, then waits
		close(stopped)
	}()
	require.Eventually(t, func() bool {
		_, next, _ := s.sup.state()
		return !next.IsZero()
	}, 5*time.Second, time.Millisecond)
	cancel()
	<-stopped
	got, err := s.store.SupervisorRun(ctx, orphan.ID)
	require.NoError(t, err)
	assert.Equal(t, store.RunFailed, got.Status)
	_, next, _ := s.sup.state()
	assert.Equal(t, now.Add(900*time.Second), next, "an interval from now")

	s.chat.script(calling(report()))
	advance(899 * time.Second)
	s.sup.tick(ctx)
	runs, err := s.store.SupervisorRuns(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, runs, 1, "not due yet")
	advance(time.Second)
	s.sup.tick(ctx)
	require.Eventually(t, func() bool {
		runs, _ := s.store.SupervisorRuns(ctx, 10)
		return len(runs) == 2 && runs[0].Status == store.RunOK
	}, 5*time.Second, 10*time.Millisecond)

	// disabled: none due
	_, err = s.store.SetSupervisor(ctx, store.SupervisorUpdate{Enabled: boolPtr(false)})
	require.NoError(t, err)
	s.sup.tick(ctx)
	_, next, _ = s.sup.state()
	assert.True(t, next.IsZero())
}

// Within its autonomy, the supervisor blocks the entries of an agent, which
// confirms them; it stops an agent once two analyses in a row ask for it,
// its pending entries canceled, disabled once it holds nothing; beyond its
// autonomy, or its count, or on incomplete data, it proposes or refuses.
func TestSupervision_AutonomousActions(t *testing.T) {
	s := newSupervised(t)
	ctx := context.Background()
	orb := s.liveAgent(t, "orb", 303, true)
	ribbon := s.liveAgent(t, "ribbon", 404, true)
	s.bridge.set(protected(1, 404))
	ribbon.pending = []ui.SupervisedOrder{{ID: 7, Pair: "XAUUSDm", Side: "BUY", Type: "LIMIT", Entry: true}}

	block := tool("b1", store.ActionBlockEntries, map[string]string{"agent": "orb", "reason": "perdas seguidas",
		"category": store.CategoryPerformance, "evidence": "3 perdas, -51,49 BRL hoje"})
	stop := tool("s1", store.ActionStopAgent, map[string]string{"agent": "ribbon", "reason": "loop de reinício",
		"category": store.CategoryOperational, "evidence": "155 reinícios"})
	s.chat.script(calling(block, stop), calling(report()))
	_, err := s.sup.Analyze(ctx, store.RunSchedule, "")
	require.NoError(t, err)
	blocked, revision := orb.entries()
	assert.True(t, blocked, "confirmed by the agent")
	a, err := s.store.AgentByName(ctx, "orb")
	require.NoError(t, err)
	assert.True(t, a.SupervisionOf().EntriesBlocked)
	assert.Equal(t, a.SupervisionOf().EntriesRevision, revision)
	doc, _, ok := s.manager.runtimes.get(a.ID)
	require.True(t, ok)
	assert.True(t, doc.EntriesBlocked, "its runtime too")
	a, err = s.store.AgentByName(ctx, "ribbon")
	require.NoError(t, err)
	assert.Equal(t, store.StopNone, a.SupervisionOf().StopState, "a stop alone waits for the next analysis")

	// asked again: stopped once flat
	s.chat.script(calling(stop), calling(report()))
	_, err = s.sup.Analyze(ctx, store.RunSchedule, "")
	require.NoError(t, err)
	a, err = s.store.AgentByName(ctx, "ribbon")
	require.NoError(t, err)
	assert.Equal(t, store.StopPendingFlat, a.SupervisionOf().StopState)
	assert.True(t, a.SupervisionOf().EntriesBlocked)
	assert.True(t, ribbon.called("/api/supervision/cancel"), "its pending entries canceled")
	assert.True(t, a.Enabled, "it holds a position: it manages it")

	previous := flatFor
	flatFor = 0
	t.Cleanup(func() { flatFor = previous })
	s.sup.watchStops(ctx)
	a, err = s.store.AgentByName(ctx, "ribbon")
	require.NoError(t, err)
	assert.True(t, a.Enabled, "not flat")
	s.bridge.set() // its position closed by its stop loss
	s.sup.watchStops(ctx)
	a, err = s.store.AgentByName(ctx, "ribbon")
	require.NoError(t, err)
	assert.True(t, a.Enabled, "seen flat once")
	s.sup.watchStops(ctx)
	a, err = s.store.AgentByName(ctx, "ribbon")
	require.NoError(t, err)
	assert.False(t, a.Enabled, "seen flat twice: stopped")
	assert.Equal(t, store.StopStopped, a.SupervisionOf().StopState)
	actions, err := s.store.SupervisorActions(ctx, 10)
	require.NoError(t, err)
	stopped := actions[0]
	assert.Equal(t, store.ActionStopAgent, stopped.Action)
	assert.Contains(t, string(stopped.Result), "agente desligado")

	// beyond its autonomy: proposed
	_, err = s.store.SetSupervisor(ctx, store.SupervisorUpdate{Autonomy: &store.Autonomy{MaxActionsPerRun: 3}})
	require.NoError(t, err)
	cancelOrb := tool("c1", store.ActionCancelPending, map[string]string{"agent": "orb", "reason": "limpar",
		"category": store.CategoryRisk, "evidence": "pendentes antigas"})
	s.chat.script(calling(cancelOrb), calling(report()))
	_, err = s.sup.Analyze(ctx, store.RunSchedule, "")
	require.NoError(t, err)
	actions, err = s.store.SupervisorActions(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, store.ActionProposed, actions[0].Status)
	assert.NotNil(t, actions[0].ExpiresAt)

	// incomplete data: refused
	_, err = s.store.SetSupervisor(ctx, store.SupervisorUpdate{Autonomy: &store.Autonomy{BlockEntries: true,
		MaxActionsPerRun: 3}})
	require.NoError(t, err)
	s.bridge.mu.Lock()
	s.bridge.status = http.StatusInternalServerError
	s.bridge.mu.Unlock()
	s.chat.script(calling(block), calling(report()))
	_, err = s.sup.Analyze(ctx, store.RunSchedule, "")
	require.NoError(t, err)
	requests := s.chat.requests()
	last := requests[len(requests)-1]
	assert.Contains(t, last[len(last)-1].Content, "incomplete", "the tool refused on incomplete data")
	count, err := s.store.SupervisorActions(ctx, 100)
	require.NoError(t, err)
	assert.Len(t, count, 3, "nothing more done")
}

// A stop now is refused while a position lacks its stop loss at the broker,
// never liquidating it; with its stop loss, the agent is disabled, its
// positions protected at the broker.
func TestSupervision_StopNow(t *testing.T) {
	s := newSupervised(t)
	ctx := context.Background()
	s.liveAgent(t, "keltner", 505, true)
	bare := position(1, 505, "XAUUSDm", false, 0.01, -3, 0)
	s.bridge.set(bare)
	stop := func() store.SupervisorAction {
		scope, _ := json.Marshal(ui.SupervisorScope{Agents: []string{"keltner"}})
		action, err := s.store.CreateSupervisorAction(ctx, store.SupervisorAction{Source: store.SourceUser,
			Action: store.ActionStopAgent, Mode: store.ModeNow, Scope: scope, Reason: "parar já",
			Status: store.ActionExecuting, RequestedBy: "admin"})
		require.NoError(t, err)
		return action
	}
	status, result := s.sup.execute(ctx, stop())
	assert.Equal(t, store.ActionFailed, status)
	assert.Contains(t, result.Items[0].Detail, "sem stop loss")
	a, err := s.store.AgentByName(ctx, "keltner")
	require.NoError(t, err)
	assert.True(t, a.Enabled)
	assert.Empty(t, s.bridge.tradeRequests(), "nothing liquidated")

	s.bridge.set(protected(1, 505))
	status, _ = s.sup.execute(ctx, stop())
	assert.Equal(t, store.ActionSucceeded, status)
	a, err = s.store.AgentByName(ctx, "keltner")
	require.NoError(t, err)
	assert.False(t, a.Enabled)
	assert.Equal(t, store.StopStopped, a.SupervisionOf().StopState)
	assert.Len(t, s.bridge.held(), 1, "its position left with its levels at the broker")

	// the supervisor never enables it again: a user does, clearing its stop
	call, _ := settingsSession(t, s.managed)
	resp, body := call(http.MethodPatch, "/api/settings/agents/keltner", ui.AgentSettingsRequest{Enabled: boolPtr(true)})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var settings ui.AgentSettings
	require.NoError(t, json.Unmarshal([]byte(body), &settings))
	require.NotNil(t, settings.Supervision)
	assert.Equal(t, store.StopNone, settings.Supervision.StopState)
	assert.False(t, settings.Supervision.EntriesBlocked, "the block of the stop cleared with it")
}

// The conversation proposes, the user confirms: the positions of the agents of
// the application closed, through the API of an agent running, on the bridge
// for one not running (its launch fenced meanwhile), one whose outcome is
// unknown never sent again, a position by hand untouched; each one checked on
// the account. A second confirmation is refused, an expired one too.
func TestSupervision_ChatCloseAll(t *testing.T) {
	s := newSupervised(t)
	ctx := context.Background()
	s.liveAgent(t, "fibonacci", 101, true)
	vwap := s.liveAgent(t, "vwap", 102, true)
	s.liveAgent(t, "emacross", 103, false)
	byHand := position(9, 0, "EURUSDm", false, 0.1, 50, 0)
	s.bridge.set(protected(1, 101), protected(2, 102), protected(3, 103), byHand)
	vwap.closeStatus["XAUUSDm"] = ui.SupervisionUnknown // its answer lost, the position left
	var fenced []bool
	s.bridge.onTrade = func(map[string]any) { fenced = append(fenced, s.manager.fenced("emacross")) }

	call, _ := settingsSession(t, s.managed)
	s.chat.script(
		calling(tool("p1", "propose_action", map[string]any{"action": store.ActionClosePositions, "all": true,
			"reason": "o usuário pediu para encerrar tudo"})),
		saying("Preparei a proposta: 3 posições de 3 agentes."))
	resp, body := call(http.MethodPost, "/api/supervisor/messages", ui.SupervisorMessageRequest{Text: "feche tudo"})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var chat ui.SupervisorMessagesResponse
	require.NoError(t, json.Unmarshal([]byte(body), &chat))
	require.Len(t, chat.Messages, 3)
	assert.Equal(t, []string{store.MessageQuestion, store.MessageProposal, store.MessageAnswer},
		[]string{chat.Messages[0].Kind, chat.Messages[1].Kind, chat.Messages[2].Kind})
	proposal := chat.Messages[1].Action
	require.NotNil(t, proposal)
	assert.Equal(t, store.ActionProposed, proposal.Status)
	assert.Len(t, proposal.Scope.Positions, 3, "the positions of the agents, never the one by hand")
	assert.True(t, proposal.Options.KeepBlocked, "the entries kept blocked by default")
	assert.NotNil(t, chat.Messages[2].DataAt)
	assert.Empty(t, s.bridge.tradeRequests(), "nothing executed before the confirmation")

	resp, body = call(http.MethodPost, "/api/supervisor/actions/"+itoa(proposal.ID)+"/confirm", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var confirmed confirmResponse
	require.NoError(t, json.Unmarshal([]byte(body), &confirmed))
	assert.Equal(t, store.ActionUnknown, confirmed.Action.Status, "one outcome unknown, none failed")
	require.NotNil(t, confirmed.Message)
	assert.Equal(t, store.MessageResult, confirmed.Message.Kind)
	statuses := map[int64]string{}
	for _, item := range confirmed.Action.Result.Items {
		if item.Kind == "position" {
			statuses[item.Ticket] = item.Status
		}
	}
	assert.Equal(t, map[int64]string{1: itemDone, 2: itemUnknown, 3: itemDone}, statuses)
	assert.Equal(t, []bridgePosition{protected(2, 102), byHand}, s.bridge.held())
	trades := s.bridge.tradeRequests()
	require.Len(t, trades, 1, "the one of the agent not running, once")
	assert.EqualValues(t, 3, trades[0]["position"])
	assert.EqualValues(t, 103, trades[0]["magic"])
	assert.Equal(t, []bool{true}, fenced, "its launch fenced as it was closed")
	assert.False(t, s.manager.fenced("emacross"), "released after")
	for _, name := range []string{"fibonacci", "vwap", "emacross"} {
		a, err := s.store.AgentByName(ctx, name)
		require.NoError(t, err)
		assert.True(t, a.SupervisionOf().EntriesBlocked, name)
	}

	resp, body = call(http.MethodPost, "/api/supervisor/actions/"+itoa(proposal.ID)+"/confirm", nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)

	// expired
	s.chat.script(
		calling(tool("p2", "propose_action", map[string]any{"action": store.ActionBlockEntries,
			"agents": []string{"vwap"}, "reason": "bloquear o vwap"})),
		saying("Proposta pronta."))
	resp, body = call(http.MethodPost, "/api/supervisor/messages", ui.SupervisorMessageRequest{Text: "bloqueie o vwap"})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &chat))
	id := chat.Messages[1].Action.ID
	require.NoError(t, s.sqlExec("UPDATE supervisor_actions SET expires_at = now() - interval '1 second' WHERE id = ?",
		id))
	resp, body = call(http.MethodPost, "/api/supervisor/actions/"+itoa(id)+"/confirm", nil)
	assert.Equal(t, http.StatusGone, resp.StatusCode, body)

	// the history of the user, the proposals with their action
	resp, body = call(http.MethodGet, "/api/supervisor/messages", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &chat))
	assert.Len(t, chat.Messages, 7)
	assert.Equal(t, store.ActionExpired, chat.Messages[5].Action.Status)
}

// An ambiguous request isn't proposed: the tool refuses a scope that names
// neither the agents nor all of them, or an agent unknown, and the model asks.
func TestSupervision_ChatAmbiguity(t *testing.T) {
	s := newSupervised(t)
	s.liveAgent(t, "fibonacci", 101, true)
	var results []string
	s.chat.script(
		calling(tool("p1", "propose_action", map[string]any{"action": store.ActionStopAgent,
			"reason": "pare o agente do ouro"}),
			tool("p2", "propose_action", map[string]any{"action": store.ActionStopAgent, "agents": []string{"ouro"},
				"reason": "pare o agente do ouro"})),
		func(_ string, messages []llm.ChatMessage, _ []llm.ToolSpec) (llm.ChatAnswer, error) {
			for _, m := range messages {
				if m.Role == llm.ChatTool {
					results = append(results, m.Content)
				}
			}
			return llm.ChatAnswer{Content: "Qual agente: fibonacci?"}, nil
		})
	added, err := s.sup.Ask(context.Background(), userOfTests(t, s), "admin", "pare o agente do ouro")
	require.NoError(t, err)
	require.Len(t, added, 2, "the question and the answer, no proposal")
	assert.Equal(t, store.MessageAnswer, added[1].Kind)
	require.Len(t, results, 2)
	assert.Contains(t, results[0], "ambiguous")
	assert.Contains(t, results[1], "unknown agents ouro")

	// the model unavailable: an error message, nothing done
	s.chat.script(failing(llm.ErrTimeout))
	added, err = s.sup.Ask(context.Background(), userOfTests(t, s), "admin", "como estão os agentes?")
	require.NoError(t, err)
	assert.Equal(t, store.MessageError, added[len(added)-1].Kind)
	assert.Contains(t, added[len(added)-1].Content, "tempo esgotado")
}

// The entries of an agent blocked and allowed again by a user, from the
// dashboard, through an action recorded.
func TestSupervisionAPI_Entries(t *testing.T) {
	s := newSupervised(t)
	orb := s.liveAgent(t, "orb", 303, true)
	call, _ := settingsSession(t, s.managed)
	resp, body := call(http.MethodPost, "/api/supervisor/agents/orb/entries",
		ui.SupervisorEntriesRequest{Blocked: true, Reason: "revisar a estratégia"})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var result confirmResponse
	require.NoError(t, json.Unmarshal([]byte(body), &result))
	assert.Equal(t, store.ActionSucceeded, result.Action.Status)
	assert.Equal(t, "admin", result.Action.RequestedBy)
	blocked, _ := orb.entries()
	assert.True(t, blocked)

	resp, body = call(http.MethodGet, "/api/settings/agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Contains(t, body, "revisar a estratégia")

	resp, body = call(http.MethodPost, "/api/supervisor/agents/orb/entries", ui.SupervisorEntriesRequest{})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	blocked, _ = orb.entries()
	assert.False(t, blocked)
	resp, _ = call(http.MethodPost, "/api/supervisor/agents/nobody/entries", ui.SupervisorEntriesRequest{})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp, body = call(http.MethodGet, "/api/supervisor/actions", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var actions ui.SupervisorActionsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &actions))
	assert.Len(t, actions.Actions, 2)
}

// Every position of an agent closed by a user from the dashboard, through a
// close_positions recorded: the ones of an agent running through it, of one
// stopped on its bridge; the other agents and the positions by hand
// untouched, the entries left free; refused without a position open.
func TestSupervisionAPI_ClosePositions(t *testing.T) {
	s := newSupervised(t)
	ctx := context.Background()
	fibonacci := s.liveAgent(t, "fibonacci", 101, true)
	vwap := s.liveAgent(t, "vwap", 102, true)
	s.liveAgent(t, "emacross", 103, false)
	byHand := position(9, 0, "EURUSDm", false, 0.1, 50, 0)
	s.bridge.set(protected(1, 101), protected(2, 102), protected(3, 103), byHand)
	vwap.pending = []ui.SupervisedOrder{{ID: 21, Pair: "XAUUSDm", Side: "SELL", Type: "STOP", Entry: true}}
	call, _ := settingsSession(t, s.managed)

	resp, body := call(http.MethodPost, "/api/supervisor/agents/fibonacci/close", ui.SupervisorCloseRequest{})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var result confirmResponse
	require.NoError(t, json.Unmarshal([]byte(body), &result))
	assert.Equal(t, store.ActionClosePositions, result.Action.Action)
	assert.Equal(t, store.ActionSucceeded, result.Action.Status)
	assert.Equal(t, "admin", result.Action.RequestedBy)
	assert.Equal(t, "posições encerradas por admin", result.Action.Reason)
	assert.True(t, result.Action.Options.Whole)
	assert.False(t, result.Action.Options.KeepBlocked)
	require.NotNil(t, result.Action.Result)
	var closed []int64
	for _, item := range result.Action.Result.Items {
		if item.Kind == "position" && item.Status == itemDone {
			closed = append(closed, item.Ticket)
		}
	}
	assert.Equal(t, []int64{1}, closed)
	assert.Equal(t, []bridgePosition{protected(2, 102), protected(3, 103), byHand}, s.bridge.held())
	assert.Empty(t, s.bridge.tradeRequests(), "closed through the agent running")
	closes := fibonacci.closeRequests()
	require.Len(t, closes, 1, "at once, no confirmation asked")
	assert.Equal(t, []string{"XAUUSDm"}, closes[0].Pairs, "the pairs of its positions")
	assert.False(t, closes[0].CancelPending, "its positions only")
	assert.Empty(t, vwap.closeRequests(), "the other agents untouched")
	assert.False(t, vwap.called("/api/supervision/cancel"))
	assert.Len(t, vwap.pending, 1, "their pending entries too")
	a, err := s.store.AgentByName(ctx, "fibonacci")
	require.NoError(t, err)
	assert.False(t, a.SupervisionOf().EntriesBlocked, "the entries left free")

	resp, body = call(http.MethodPost, "/api/supervisor/agents/fibonacci/close", ui.SupervisorCloseRequest{})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)
	assert.Contains(t, body, "o agente não tem posição aberta")
	resp, _ = call(http.MethodPost, "/api/supervisor/agents/nobody/close", ui.SupervisorCloseRequest{})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp, body = call(http.MethodPost, "/api/supervisor/agents/emacross/close",
		ui.SupervisorCloseRequest{Reason: "zerar o emacross"})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &result))
	assert.Equal(t, store.ActionSucceeded, result.Action.Status)
	assert.Equal(t, "zerar o emacross", result.Action.Reason)
	trades := s.bridge.tradeRequests()
	require.Len(t, trades, 1, "the agent stopped: on its bridge")
	assert.EqualValues(t, 3, trades[0]["position"])
	assert.Equal(t, []bridgePosition{protected(2, 102), byHand}, s.bridge.held())
}

// itemsOf returns the statuses of the items of kind of result by ticket, and
// the items of kind without a ticket.
func itemsOf(result *ui.SupervisorResult, kind string) (map[int64]string, []ui.SupervisorResultItem) {
	byTicket := map[int64]string{}
	var others []ui.SupervisorResultItem
	for _, item := range result.Items {
		switch {
		case item.Kind != kind:
		case item.Ticket != 0:
			byTicket[item.Ticket] = item.Status
		default:
			others = append(others, item)
		}
	}
	return byTicket, others
}

// Every position of the agents closed and their pending entries canceled by
// a user from the dashboard: an agent running cancels and closes together,
// its entries held a candle, the entry filled as it was canceled closed too;
// an agent stopped, on its bridge, its orders removed first; the positions
// and the orders by hand untouched; the entries, the enablement and the
// limits of the agents left; the result checked on the account.
func TestSupervisionAPI_CloseAll(t *testing.T) {
	s := newSupervised(t)
	ctx := context.Background()
	fibonacci := s.liveAgent(t, "fibonacci", 101, true)
	vwap := s.liveAgent(t, "vwap", 102, true)
	s.liveAgent(t, "emacross", 103, false)
	byHand := position(9, 0, "EURUSDm", false, 0.1, 50, 0)
	s.bridge.set(protected(1, 101), protected(3, 103), byHand)
	handOrder := bridgeOrder{Ticket: 90, Type: 2, Magic: 0, Symbol: "EURUSDm", PriceOpen: 1.1, VolumeCurrent: 0.1}
	s.bridge.mu.Lock()
	s.bridge.orders = []bridgeOrder{handOrder,
		{Ticket: 31, Type: 2, Magic: 103, Symbol: "XAUUSDm", PriceOpen: 4100, VolumeCurrent: 0.01}}
	s.bridge.mu.Unlock()
	vwap.pending = []ui.SupervisedOrder{{ID: 21, Pair: "XAUUSDm", Side: "SELL", Type: "STOP", Entry: true}}
	vwap.fills = map[int64]bridgePosition{21: position(22, 102, "XAUUSDm", true, 0.01, -1, 0)} // filled meanwhile
	fibonacci.pending = []ui.SupervisedOrder{{ID: 11, Pair: "BTCUSDm", Side: "BUY", Type: "LIMIT", Entry: true}}
	before, err := s.store.Agents(ctx)
	require.NoError(t, err)
	call, _ := settingsSession(t, s.managed)

	resp, body := call(http.MethodPost, "/api/supervisor/close-all", ui.SupervisorCloseRequest{})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var result confirmResponse
	require.NoError(t, json.Unmarshal([]byte(body), &result))
	assert.Equal(t, store.ActionClosePositions, result.Action.Action)
	assert.Equal(t, store.ActionSucceeded, result.Action.Status, body)
	assert.Equal(t, "todas as posições zeradas e as pendentes canceladas por admin", result.Action.Reason)
	assert.ElementsMatch(t, []string{"fibonacci", "vwap", "emacross"}, result.Action.Scope.Agents)
	assert.True(t, result.Action.Options.Whole)
	assert.True(t, result.Action.Options.CancelPending)
	assert.False(t, result.Action.Options.KeepBlocked, "never blocked for good")
	require.NotNil(t, result.Action.Result)

	positions, opened := itemsOf(result.Action.Result, "position")
	assert.Equal(t, map[int64]string{1: itemDone, 3: itemDone}, positions)
	require.Len(t, opened, 1, "the entry filled as it was canceled")
	assert.Equal(t, ui.SupervisorResultItem{Agent: "vwap", Kind: "position", Pair: "XAUUSDm", Status: itemDone,
		Detail: "posição aberta durante o encerramento: fechada pelo agente"}, opened[0])
	orders, _ := itemsOf(result.Action.Result, "order")
	assert.Equal(t, map[int64]string{11: itemDone, 21: itemAlready, 31: itemDone}, orders,
		"never the order by hand")

	assert.Equal(t, []bridgePosition{byHand}, s.bridge.held(), "only the position by hand left")
	s.bridge.mu.Lock()
	assert.Equal(t, []bridgeOrder{handOrder}, s.bridge.orders, "only the order by hand left")
	s.bridge.mu.Unlock()
	trades := s.bridge.tradeRequests()
	require.Len(t, trades, 2, "the agent stopped on its bridge: its order removed, then its position closed")
	assert.EqualValues(t, 31, trades[0]["order"])
	assert.EqualValues(t, 3, trades[1]["position"])
	for _, api := range []*fakeAgent{fibonacci, vwap} {
		closes := api.closeRequests()
		require.Len(t, closes, 1)
		assert.True(t, closes[0].CancelPending, "canceled and closed together")
		assert.Empty(t, closes[0].Pairs, "every pair of the agent")
		assert.False(t, closes[0].Block)
		assert.Positive(t, closes[0].HoldSeconds, "held a candle")
		assert.False(t, api.called("/api/supervision/cancel"), "no cancel apart")
	}
	after, err := s.store.Agents(ctx)
	require.NoError(t, err)
	for i, a := range after {
		assert.Equal(t, before[i].Enabled, a.Enabled, a.Name)
		assert.False(t, a.SupervisionOf().EntriesBlocked, a.Name)
		assert.Equal(t, before[i].DailyLimitOf(), a.DailyLimitOf(), a.Name)
	}
}

// The close of every position told as it went, never as a success because it
// was asked: a close whose outcome is unknown never sent again, a close
// refused by the bridge failed, an order the bridge didn't remove still
// pending; one at a time, and none of an agent meanwhile.
func TestSupervisionAPI_CloseAllPartial(t *testing.T) {
	s := newSupervised(t)
	fibonacci := s.liveAgent(t, "fibonacci", 101, true)
	vwap := s.liveAgent(t, "vwap", 102, true)
	s.liveAgent(t, "emacross", 103, false)
	s.bridge.set(protected(1, 101), protected(2, 102), protected(3, 103))
	s.bridge.mu.Lock()
	s.bridge.orders = []bridgeOrder{{Ticket: 31, Type: 2, Magic: 103, Symbol: "XAUUSDm", PriceOpen: 4100,
		VolumeCurrent: 0.01}}
	s.bridge.mu.Unlock()
	vwap.closeStatus["XAUUSDm"] = ui.SupervisionUnknown      // its answer lost, the position left
	s.bridge.tradeAnswer = func(map[string]any) (int, any) { // the terminal refusing
		return http.StatusOK, map[string]any{"retcode": 10018, "comment": "Market closed"}
	}
	call, _ := settingsSession(t, s.managed)

	// one at a time: refused while one runs, the one of an agent too
	release, refusal := s.sup.beginClose("")
	require.Empty(t, refusal)
	resp, body := call(http.MethodPost, "/api/supervisor/close-all", ui.SupervisorCloseRequest{})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)
	assert.Contains(t, body, "já está em andamento")
	resp, body = call(http.MethodPost, "/api/supervisor/agents/fibonacci/close", ui.SupervisorCloseRequest{})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)
	release()
	release, _ = s.sup.beginClose("vwap")
	resp, body = call(http.MethodPost, "/api/supervisor/agents/vwap/close", ui.SupervisorCloseRequest{})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)
	assert.Contains(t, body, "deste agente já está em andamento")
	resp, _ = call(http.MethodPost, "/api/supervisor/close-all", ui.SupervisorCloseRequest{})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	release()
	assert.Empty(t, fibonacci.closeRequests(), "nothing sent while refused")
	assert.Empty(t, s.bridge.tradeRequests())

	resp, body = call(http.MethodPost, "/api/supervisor/close-all", ui.SupervisorCloseRequest{})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var result confirmResponse
	require.NoError(t, json.Unmarshal([]byte(body), &result))
	assert.Equal(t, store.ActionPartial, result.Action.Status)
	positions, _ := itemsOf(result.Action.Result, "position")
	assert.Equal(t, map[int64]string{1: itemDone, 2: itemUnknown, 3: itemFailed}, positions)
	orders, _ := itemsOf(result.Action.Result, "order")
	assert.Equal(t, map[int64]string{31: itemFailed}, orders, "told once, still pending")
	assert.Len(t, vwap.closeRequests(), 1, "the close of an outcome unknown never sent again")
	assert.Equal(t, []bridgePosition{protected(2, 102), protected(3, 103)}, s.bridge.held(),
		"the positions left keep their stops")
	trades := s.bridge.tradeRequests()
	require.Len(t, trades, 2, "each refused once, never sent again")
}

// itoa returns the decimal of id.
func itoa(id int64) string { return strconv.FormatInt(id, 10) }

// userOfTests returns the ID of the user of the tests, created once.
func userOfTests(t *testing.T, s *supervised) int64 {
	t.Helper()
	users, err := s.store.Users(context.Background())
	require.NoError(t, err)
	for _, u := range users {
		if u.Username == "tester" {
			return u.ID
		}
	}
	u, err := s.store.CreateUser(context.Background(), "tester", "tester-password")
	require.NoError(t, err)
	return u.ID
}

// sqlExec runs a statement on the database of the tests.
func (s *supervised) sqlExec(query string, args ...any) error {
	db, err := gorm.Open(postgres.Open(s.dsn), &gorm.Config{Logger: glog.Discard})
	if err != nil {
		return err
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	return db.Exec(query, args...).Error
}

// A close on the bridge is sent once: refused with sent false, or a refusal of
// the terminal, nothing done (failed); a failure after it was sent, or no
// answer, maybe done (unknown), never sent again; a real account the agent may
// not trade refused before anything is sent.
func TestBridgeTrader(t *testing.T) {
	bridge := newFakeBridge(t, "bridge-token", 198974329, "BRL")
	trader := newBridgeTrader()
	ctx := context.Background()
	agent := bridgeAgent{magic: 101, comment: "nb:fibonacci"}
	p := protected(1, 101)

	bridge.set(p)
	status, detail := trader.close(ctx, bridge.URL, "bridge-token", p, agent)
	assert.Equal(t, itemDone, status, detail)
	request := bridge.tradeRequests()[0]
	assert.EqualValues(t, 1, request["action"])
	assert.EqualValues(t, 1, request["type"], "a long closed by a sell")
	assert.EqualValues(t, 4160.0, request["price"], "at the bid")
	assert.EqualValues(t, "nb:fibonacci", request["comment"])
	assert.EqualValues(t, mt5FillFOK, request["type_filling"])

	status, detail = trader.close(ctx, bridge.URL, "bridge-token", p, agent)
	assert.Equal(t, itemFailed, status, "gone: refused by the terminal")
	assert.Contains(t, detail, "retcode 10013")

	answers := []struct {
		status int
		body   any
		want   string
	}{
		{http.StatusServiceUnavailable, map[string]any{"error": "terminal busy", "sent": false}, itemFailed},
		{http.StatusBadRequest, map[string]any{"error": "unknown fields"}, itemFailed},
		{http.StatusInternalServerError, map[string]any{"error": "order_send: lost"}, itemUnknown},
	}
	for _, answer := range answers {
		bridge.mu.Lock()
		bridge.tradeAnswer = func(map[string]any) (int, any) { return answer.status, answer.body }
		bridge.mu.Unlock()
		status, detail = trader.close(ctx, bridge.URL, "bridge-token", p, agent)
		assert.Equal(t, answer.want, status, detail)
	}

	// no answer: unknown
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/order":
			hj, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, _, err := hj.Hijack()
			require.NoError(t, err)
			_ = conn.Close() // the answer lost
		case "/account":
			_, _ = w.Write([]byte(`{"login": 1, "trade_mode": 0}`))
		default:
			_, _ = w.Write([]byte(`{"digits": 3, "filling_mode": 1, "bid": 1, "ask": 1}`))
		}
	}))
	defer silent.Close()
	status, _ = trader.close(ctx, silent.URL, "", p, agent)
	assert.Equal(t, itemUnknown, status)

	// a real account the agent may not trade
	bridge.mu.Lock()
	bridge.tradeMode, bridge.tradeAnswer, bridge.trades = 2, nil, nil
	bridge.mu.Unlock()
	status, detail = trader.close(ctx, bridge.URL, "bridge-token", p, agent)
	assert.Equal(t, itemFailed, status)
	assert.Contains(t, detail, "conta real")
	assert.Empty(t, bridge.tradeRequests(), "nothing sent")
	agent.realAllowed = true
	bridge.set(p)
	status, _ = trader.close(ctx, bridge.URL, "bridge-token", p, agent)
	assert.Equal(t, itemDone, status)
}

// The dashboard never reaches the channel of the supervisor through its proxy:
// its requests go as the web channel, which the routes of the supervisor
// refuse.
func TestSupervision_ProxyNeverReachesTheSupervisorChannel(t *testing.T) {
	s := newSupervised(t)
	orb := s.liveAgent(t, "orb", 303, true)
	s.bridge.set(protected(1, 303))
	call, _ := settingsSession(t, s.managed)
	resp, body := call(http.MethodPost, "/api/agents/orb/supervision/close", map[string]any{})
	assert.NotEqual(t, http.StatusOK, resp.StatusCode, body)
	resp, body = call(http.MethodPost, "/api/agents/orb/supervision/entries", map[string]any{"blocked": true})
	assert.NotEqual(t, http.StatusOK, resp.StatusCode, body)
	assert.Len(t, s.bridge.held(), 1, "nothing closed")
	blocked, _ := orb.entries()
	assert.False(t, blocked)
}

// An answer the model cut at its max tokens before any text or call, as a
// model that reasons spends them thinking, fails the analysis and the
// conversation telling the user the tokens spent, the limit and what to
// change; a text cut is kept, told cut.
func TestSupervision_CutAtMaxTokens(t *testing.T) {
	s := newSupervised(t)
	ctx := context.Background()
	cut := func(content string) answer {
		return func(string, []llm.ChatMessage, []llm.ToolSpec) (llm.ChatAnswer, error) {
			return llm.ChatAnswer{Content: content, Truncated: true,
				Usage: llm.Usage{Output: llm.DefaultMaxTokens, Reasoning: llm.DefaultMaxTokens}}, nil
		}
	}

	s.chat.script(cut(""))
	run, err := s.sup.Analyze(ctx, store.RunManual, "admin")
	require.ErrorContains(t, err, "Máximo de tokens")
	got, err := s.store.SupervisorRun(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, store.RunFailed, got.Status)
	assert.Contains(t, got.Error, "4096 tokens, 4096 deles raciocinando; o limite é 4096")
	assert.Contains(t, got.Error, "Máximo de tokens")

	s.chat.script(cut(""))
	added, err := s.sup.Ask(ctx, userOfTests(t, s), "admin", "como estão os agentes?")
	require.NoError(t, err)
	last := added[len(added)-1]
	assert.Equal(t, store.MessageError, last.Kind)
	assert.Contains(t, last.Content, "Aumente o Máximo de tokens")

	s.chat.script(cut("Metade da"))
	added, err = s.sup.Ask(ctx, userOfTests(t, s), "admin", "e agora?")
	require.NoError(t, err)
	last = added[len(added)-1]
	assert.Equal(t, store.MessageAnswer, last.Kind)
	assert.Equal(t, "Metade da\n\n*(resposta cortada no limite de tokens)*", last.Content)
}
