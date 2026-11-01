//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// stopping reports whether the process of name is being stopped.
func stopping(s *Supervisor, name string) bool {
	s.mu.Lock()
	p := s.processes[name]
	s.mu.Unlock()
	if p == nil {
		return false
	}
	select {
	case <-p.stopping:
		return true
	default:
		return false
	}
}

// slowStops makes the agents of the runner of c ignore their interrupt: a
// stop of one takes stopTimeout, as an agent sending its last records.
func slowStops(c *managed, stopTimeout time.Duration) {
	c.manager.supervisor.stopTimeout = stopTimeout
	c.manager.agents.Commands["trend"] = os.Args[0] + " " + fakeBotPrefix + "stubborn"
}

// starts returns how many times the agent of name started.
func (c *managed) starts(name string) int {
	return strings.Count(c.stdout.String(), "["+name+"] started")
}

// pausedBot is a Telegram bot pausing the first Reconcile reaching at, once
// it read the database: "relays", as it relays the events of the agents, or
// "apply", as it applies the settings of the bot. paused tells it, release
// ends it. It keeps the settings applied.
type pausedBot struct {
	at      string
	pause   atomic.Bool
	paused  chan struct{}
	release chan struct{}

	mu      sync.Mutex
	applied []TelegramConfig
}

func newPausedBot(at string) *pausedBot {
	b := &pausedBot{at: at, paused: make(chan struct{}), release: make(chan struct{})}
	b.pause.Store(true)
	return b
}

func (b *pausedBot) wait(at string) {
	if b.at == at && b.pause.CompareAndSwap(true, false) {
		close(b.paused)
		<-b.release
	}
}

func (b *pausedBot) Apply(cfg TelegramConfig) {
	b.wait("apply")
	b.mu.Lock()
	defer b.mu.Unlock()
	b.applied = append(b.applied, cfg)
}

func (b *pausedBot) SyncRelays([]string) { b.wait("relays") }

// last returns the settings applied last.
func (b *pausedBot) last() TelegramConfig {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.applied) == 0 {
		return TelegramConfig{}
	}
	return b.applied[len(b.applied)-1]
}

// reconcileAround runs a Reconcile paused by bot, then the change, then
// another Reconcile, and ends the pause: the second one either waits for the
// first one or runs before it ends.
func reconcileAround(t *testing.T, c *managed, bot *pausedBot, change func()) {
	t.Helper()
	ctx := context.Background()
	first := make(chan error, 1)
	go func() { first <- c.manager.Reconcile(ctx) }()
	<-bot.paused // it read the database
	change()
	second := make(chan error, 1)
	go func() { second <- c.manager.Reconcile(ctx) }()
	select {
	case err := <-second: // it didn't wait for the first one
		second <- err
	case <-time.After(200 * time.Millisecond):
	}
	close(bot.release)
	require.NoError(t, <-first)
	require.NoError(t, <-second)
}

// Once StopAll stopped the processes nothing starts, eg: a restart in flight
// as the controller stops, nor a process waiting to be started again.
func TestSupervisor_NothingStartsAfterStopAll(t *testing.T) {
	previous := restartDelay
	restartDelay = 200 * time.Millisecond
	t.Cleanup(func() { restartDelay = previous })
	s, stdout, _ := testSupervisor(t, time.Second)
	require.NoError(t, s.Start(fakeSpec("orb", "bot"), nil))
	crash := fakeSpec("vwap", "crash")
	crash.Restart = true
	require.NoError(t, s.Start(crash, nil))
	require.Eventually(t, func() bool {
		return strings.Contains(stdout.String(), "[orb] started") && s.State("vwap").Restarting
	}, 5*time.Second, time.Millisecond)
	state := s.State("vwap")
	assert.True(t, state.Running, "waiting to be started again")
	assert.Zero(t, state.PID)
	assert.Equal(t, "exit status 1", state.LastExit)

	s.StopAll()
	assert.False(t, s.Running("orb"))
	assert.False(t, s.Running("vwap"))
	assert.ErrorIs(t, s.Start(fakeSpec("orb", "bot"), nil), ErrStopped)
	assert.False(t, s.Running("orb"))
	assert.Never(t, func() bool { return strings.Count(stdout.String(), "[vwap] crashed") > 1 },
		400*time.Millisecond, 10*time.Millisecond, "started again once stopped")
	assert.Equal(t, 1, strings.Count(stdout.String(), "[orb] started"))
}

// A restart in flight as the controller stops: the agent it stopped is never
// launched again once the controller stopped its agents.
func TestManager_RestartInFlightAsTheControllerStops(t *testing.T) {
	c := newManaged(t)
	slowStops(c, 500*time.Millisecond)
	c.agent(t, "matrend", "trend", true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := make(chan struct{})
	go func() {
		c.manager.Run(ctx)
		close(ran)
	}()
	require.Eventually(t, func() bool { return c.starts("matrend") == 1 }, 5*time.Second, 10*time.Millisecond)

	restarted := make(chan error, 1)
	go func() { restarted <- c.manager.Restart(context.Background(), "matrend") }()
	require.Eventually(t, func() bool { return stopping(c.manager.supervisor, "matrend") }, 5*time.Second,
		time.Millisecond)
	cancel() // the controller stops while the restart waits for the agent
	<-ran
	err := <-restarted
	assert.False(t, c.manager.supervisor.Running("matrend"), "running after the controller stopped")
	assert.Never(t, func() bool { return c.starts("matrend") > 1 }, 300*time.Millisecond, 10*time.Millisecond,
		"launched again after the controller stopped")
	assert.Error(t, err, "the restart can't launch it anymore")
	assert.ErrorContains(t, c.manager.Restart(context.Background(), "matrend"), "stopping")
}

// An agent disabled while it restarts stays stopped, and the settings apply
// without waiting for its stop.
func TestManager_RestartInFlightOfAnAgentDisabledMeanwhile(t *testing.T) {
	c := newManaged(t)
	slowStops(c, time.Second)
	trend := c.agent(t, "matrend", "trend", true)
	c.agent(t, "llm", "llm", false)
	call, _ := settingsSession(t, c)
	ctx := context.Background()
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return c.starts("matrend") == 1 }, 5*time.Second, 10*time.Millisecond)

	restarted := make(chan error, 1)
	go func() { restarted <- c.manager.Restart(ctx, "matrend") }()
	require.Eventually(t, func() bool { return stopping(c.manager.supervisor, "matrend") }, 5*time.Second,
		time.Millisecond)
	start := time.Now()
	resp, body := call(http.MethodPatch, "/api/settings/agents/matrend", ui.AgentSettingsRequest{
		Enabled: boolPtr(false)})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	resp, body = call(http.MethodPatch, "/api/settings/agents/llm", ui.AgentSettingsRequest{Enabled: boolPtr(true)})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	resp, body = call(http.MethodGet, "/api/settings/agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Less(t, time.Since(start), 600*time.Millisecond, "the settings waited for the agent to stop")
	require.True(t, c.manager.supervisor.Running("llm"), "another agent launched meanwhile")

	require.NoError(t, <-restarted)
	assert.False(t, c.manager.supervisor.Running("matrend"), "disabled while it restarted")
	require.NoError(t, c.manager.Reconcile(ctx))
	assert.Never(t, func() bool { return c.starts("matrend") > 1 }, 300*time.Millisecond, 10*time.Millisecond)

	// enabled again, a restart launches it
	_, err := c.store.UpdateAgent(ctx, trend.ID, store.AgentUpdate{Enabled: ptrTo(true)})
	require.NoError(t, err)
	require.NoError(t, c.manager.Restart(ctx, "matrend"))
	require.Eventually(t, func() bool { return c.starts("matrend") == 2 }, 5*time.Second, 10*time.Millisecond)
	assert.True(t, c.manager.supervisor.Running("matrend"))
}

// B10: a restart asked or in flight as the controller stops answers 503, the
// controller stopping (ErrStopped), or the request canceled with it (the
// requests in the context of its signals): neither 500 nor an error logged,
// and nothing launched.
func TestSettingsAPI_RestartAsTheControllerStops(t *testing.T) {
	t.Run("in flight", func(t *testing.T) {
		c := newManaged(t)
		slowStops(c, time.Second)
		c.agent(t, "matrend", "trend", true)
		base, signal := context.WithCancel(context.Background())
		defer signal()
		call, _ := settingsSessionIn(t, c, base)
		require.NoError(t, c.manager.Reconcile(context.Background()))
		require.Eventually(t, func() bool { return c.starts("matrend") == 1 }, 5*time.Second, 10*time.Millisecond)

		type answer struct {
			status int
			body   string
		}
		restarted := make(chan answer, 1)
		go func() {
			resp, body := call(http.MethodPost, "/api/settings/agents/matrend/restart", nil)
			restarted <- answer{resp.StatusCode, body}
		}()
		require.Eventually(t, func() bool { return stopping(c.manager.supervisor, "matrend") }, 5*time.Second,
			time.Millisecond)
		signal() // as the controller stops
		got := <-restarted
		assert.Equal(t, http.StatusServiceUnavailable, got.status, got.body)
		assert.Contains(t, got.body, "stopping")
		assert.Never(t, func() bool { return c.starts("matrend") > 1 }, 300*time.Millisecond, 10*time.Millisecond)
	})

	t.Run("asked once stopping", func(t *testing.T) {
		c := newManaged(t)
		c.agent(t, "matrend", "trend", true)
		call, _ := settingsSession(t, c)
		require.NoError(t, c.manager.Reconcile(context.Background()))
		require.Eventually(t, func() bool { return c.starts("matrend") == 1 }, 5*time.Second, 10*time.Millisecond)
		c.manager.close()
		resp, body := call(http.MethodPost, "/api/settings/agents/matrend/restart", nil)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, body)
		assert.Contains(t, body, ErrStopped.Error())
		assert.Equal(t, 1, c.starts("matrend"))
	})
}

// Two Reconciles at once: the one of the database read last applies last, an
// agent disabled meanwhile stopped whatever the one read before it does.
func TestManager_ConcurrentReconciles(t *testing.T) {
	c := newManaged(t)
	bot := newPausedBot("relays")
	c.manager.bot = bot
	trend := c.agent(t, "matrend", "trend", true)
	ctx := context.Background()

	reconcileAround(t, c, bot, func() { // the first one read the agent enabled
		_, err := c.store.UpdateAgent(ctx, trend.ID, store.AgentUpdate{Enabled: ptrTo(false)})
		require.NoError(t, err)
	})
	require.Eventually(t, func() bool { return !c.manager.supervisor.Running("matrend") }, 5*time.Second,
		10*time.Millisecond, "disabled, running")
	require.NoError(t, c.manager.Reconcile(ctx))
	assert.False(t, c.manager.supervisor.Running("matrend"))
}

// Two Reconciles at once apply the Telegram bot in the order they read it: a
// bot disabled meanwhile stays disabled.
func TestManager_ConcurrentReconcilesApplyTheBotInOrder(t *testing.T) {
	c := newManaged(t)
	bot := newPausedBot("apply")
	c.manager.bot = bot
	ctx := context.Background()
	require.NoError(t, c.store.SetTelegram(ctx, ptrTo(true), secret("123:token")))
	_, err := c.store.AddTelegramUser(ctx, 42, "admin")
	require.NoError(t, err)

	reconcileAround(t, c, bot, func() { // the first one read the bot enabled
		require.NoError(t, c.store.SetTelegram(ctx, ptrTo(false), nil))
	})
	assert.False(t, bot.last().Enabled, "disabled meanwhile, enabled again")
}

// An agent without Restart that exits isn't launched again, until restarted,
// its settings changed, or disabled and enabled again: its state tells it,
// and a change of its switches doesn't launch it.
func TestManager_AgentWithoutRestartExited(t *testing.T) {
	c := newManaged(t)
	c.manager.agents.Commands["trend"] = os.Args[0] + " " + fakeBotPrefix + "crash"
	trend := c.agent(t, "matrend", "trend", true, func(a *store.Agent) { a.Restart = false })
	call, _ := settingsSession(t, c)
	ctx := context.Background()
	crashes := func() int { return strings.Count(c.stdout.String(), "[matrend] crashed") }
	exited := func(want int) {
		t.Helper()
		require.Eventually(t, func() bool { return crashes() == want && !c.manager.supervisor.Running("matrend") },
			5*time.Second, 10*time.Millisecond)
		for range 3 {
			require.NoError(t, c.manager.Reconcile(ctx))
		}
		assert.Never(t, func() bool { return crashes() > want }, 300*time.Millisecond, 10*time.Millisecond,
			"launched again")
		assert.False(t, c.manager.supervisor.Running("matrend"))
		assert.True(t, c.manager.State("matrend").Exited)
	}

	require.NoError(t, c.manager.Reconcile(ctx))
	exited(1)
	state := c.manager.State("matrend")
	assert.Equal(t, "exit status 1", state.LastExit)
	resp, body := call(http.MethodGet, "/api/settings/agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var list ui.AgentSettingsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	require.Len(t, list.Agents, 1)
	assert.True(t, list.Agents[0].Exited)
	assert.False(t, list.Agents[0].Running)
	assert.Equal(t, "exit status 1", list.Agents[0].LastExit)

	// its switches, applied while it runs, don't launch it
	resp, body = call(http.MethodPatch, "/api/settings/agents/matrend", ui.AgentSettingsRequest{
		TradePanel: boolPtr(true)})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.True(t, agentSettingsOf(t, body).Exited)
	exited(1)

	resp, body = call(http.MethodPost, "/api/settings/agents/matrend/restart", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	exited(2)

	resp, body = call(http.MethodPatch, "/api/settings/agents/matrend", ui.AgentSettingsRequest{
		Params: map[string]string{"pair": "XAUUSDm", "lot": "0.02"}})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	exited(3)

	_, err := c.store.UpdateAgent(ctx, trend.ID, store.AgentUpdate{Enabled: ptrTo(false)})
	require.NoError(t, err)
	require.NoError(t, c.manager.Reconcile(ctx))
	assert.False(t, c.manager.State("matrend").Exited, "disabled")
	_, err = c.store.UpdateAgent(ctx, trend.ID, store.AgentUpdate{Enabled: ptrTo(true)})
	require.NoError(t, err)
	require.NoError(t, c.manager.Reconcile(ctx))
	exited(4)
}

// An agent without Restart that exits with a change of its settings pending
// isn't launched with it: the change waits for its restart.
func TestManager_AgentWithoutRestartExitedWithAChangePending(t *testing.T) {
	c := newManaged(t)
	trend := c.agent(t, "matrend", "trend", true, func(a *store.Agent) { a.Restart = false })
	call, _ := settingsSession(t, c)
	ctx := context.Background()
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return c.starts("matrend") == 1 }, 5*time.Second, 10*time.Millisecond)

	resp, body := call(http.MethodPatch, "/api/settings/agents/matrend", ui.AgentSettingsRequest{
		Params: map[string]string{"pair": "XAUUSDm", "lot": "0.02"}})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.True(t, agentSettingsOf(t, body).RestartPending)

	// it crashes
	pid := c.manager.supervisor.State("matrend").PID
	require.Positive(t, pid)
	require.NoError(t, syscall.Kill(-pid, syscall.SIGKILL))
	require.Eventually(t, func() bool { return !c.manager.supervisor.Running("matrend") }, 5*time.Second,
		10*time.Millisecond)
	for range 3 {
		require.NoError(t, c.manager.Reconcile(ctx))
	}
	assert.Never(t, func() bool { return c.starts("matrend") > 1 }, 300*time.Millisecond, 10*time.Millisecond,
		"launched with the change pending")
	assert.True(t, c.manager.State("matrend").Exited)

	// restarted, with it
	require.NoError(t, c.manager.Restart(ctx, "matrend"))
	require.Eventually(t, func() bool { return c.starts("matrend") == 2 }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, c.manager.Reconcile(ctx))
	state := c.manager.State("matrend")
	assert.False(t, state.Exited)
	assert.False(t, state.RestartPending)
	a, err := c.store.Agent(ctx, trend.ID)
	require.NoError(t, err)
	assert.Equal(t, "0.02", c.manager.launchedAs(a).Params["lot"], "the settings it runs with")
}

// B17: an agent without Restart exiting as a change of its settings was saved,
// the Reconcile of that change the first one to see the exit, isn't launched
// with it: saved while it ran, the change waits for its restart.
func TestManager_AgentWithoutRestartExitedAsAChangeWasSaved(t *testing.T) {
	c := newManaged(t)
	trend := c.agent(t, "matrend", "trend", true, func(a *store.Agent) { a.Restart = false })
	ctx := context.Background()
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return c.starts("matrend") == 1 }, 5*time.Second, 10*time.Millisecond)

	// saved, as by the dashboard, its Reconcile not run yet
	_, err := c.store.UpdateAgent(ctx, trend.ID, store.AgentUpdate{Params: store.Params{"pair": "XAUUSDm",
		"lot": "0.02"}})
	require.NoError(t, err)
	pid := c.manager.supervisor.State("matrend").PID
	require.Positive(t, pid)
	require.NoError(t, syscall.Kill(-pid, syscall.SIGKILL))
	require.Eventually(t, func() bool { return !c.manager.supervisor.Running("matrend") }, 5*time.Second,
		10*time.Millisecond)

	require.NoError(t, c.manager.Reconcile(ctx)) // the one of the change
	assert.Never(t, func() bool { return c.starts("matrend") > 1 }, 300*time.Millisecond, 10*time.Millisecond,
		"launched with the change saved while it ran")
	assert.True(t, c.manager.State("matrend").Exited)

	// a change once it exited launches it
	_, err = c.store.UpdateAgent(ctx, trend.ID, store.AgentUpdate{Params: store.Params{"pair": "XAUUSDm",
		"lot": "0.03"}})
	require.NoError(t, err)
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return c.starts("matrend") == 2 }, 5*time.Second, 10*time.Millisecond)
	assert.False(t, c.manager.State("matrend").Exited)
}

// B17: an agent without Restart that exits is told stopped at once, by the
// Reconcile its exit triggers, not by the next one of the ticker.
func TestManager_AgentWithoutRestartExitedToldAtOnce(t *testing.T) {
	previous := reconcileEvery
	reconcileEvery = time.Hour
	t.Cleanup(func() { reconcileEvery = previous })
	c := newManaged(t)
	c.manager.agents.Commands["trend"] = os.Args[0] + " " + fakeBotPrefix + "crash"
	c.agent(t, "matrend", "trend", true, func(a *store.Agent) { a.Restart = false })
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		c.manager.Run(ctx)
		close(ran)
	}()
	t.Cleanup(func() {
		cancel()
		<-ran
	})
	require.Eventually(t, func() bool { return c.manager.State("matrend").Exited }, 5*time.Second,
		10*time.Millisecond)
	assert.Equal(t, 1, strings.Count(c.stdout.String(), "[matrend] crashed"))
}

// An agent of Restart that exits is started again by the supervisor, later
// each time, its state telling it, and never launched by the Reconciles
// meanwhile.
func TestManager_AgentWithRestartExited(t *testing.T) {
	previousDelay, previousMax := restartDelay, restartMaxDelay
	restartDelay, restartMaxDelay = 300*time.Millisecond, 2*time.Second
	t.Cleanup(func() { restartDelay, restartMaxDelay = previousDelay, previousMax })
	c := newManaged(t)
	c.manager.agents.Commands["trend"] = os.Args[0] + " " + fakeBotPrefix + "crash"
	c.agent(t, "matrend", "trend", true, func(a *store.Agent) { a.Restart = true })
	call, _ := settingsSession(t, c)
	ctx := context.Background()
	crashes := func() int { return strings.Count(c.stdout.String(), "[matrend] crashed") }

	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return c.manager.State("matrend").Restarting }, 5*time.Second,
		time.Millisecond)
	state := c.manager.State("matrend")
	assert.True(t, state.Running, "waits to be started again")
	assert.Zero(t, state.PID)
	assert.Equal(t, "exit status 1", state.LastExit)
	require.NoError(t, c.manager.Reconcile(ctx))
	assert.Equal(t, 1, crashes(), "never launched by a Reconcile meanwhile")
	resp, body := call(http.MethodGet, "/api/settings/agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var list ui.AgentSettingsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	require.Len(t, list.Agents, 1)
	assert.True(t, list.Agents[0].Restarting)
	assert.False(t, list.Agents[0].Exited)

	require.Eventually(t, func() bool { return crashes() == 3 && c.manager.State("matrend").Restarts == 2 },
		10*time.Second, 10*time.Millisecond, "300ms, then 600ms later")
	assert.Never(t, func() bool { return crashes() > 3 }, 800*time.Millisecond, 10*time.Millisecond,
		"1.2s later")
	assert.False(t, c.manager.State("matrend").Exited)
	resp, body = call(http.MethodGet, "/api/settings/agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	assert.GreaterOrEqual(t, list.Agents[0].Restarts, 2)
}

// An agent started again by the supervisor once it exited runs with the
// settings of its launch, the ones the dashboard serves: a change made while
// it ran waits for its restart, its switches and its secrets applied.
func TestManager_StartedAgainWithTheSettingsOfItsLaunch(t *testing.T) {
	previous := restartDelay
	restartDelay = 10 * time.Millisecond
	t.Cleanup(func() { restartDelay = previous })
	c := newManaged(t)
	ctx := context.Background()
	llm := c.agent(t, "llm", "llm", true, func(a *store.Agent) { a.Mode, a.Restart = "paper", true })
	srv := httptest.NewServer(c.manager.InternalHandler())
	t.Cleanup(srv.Close)
	tokenRe := regexp.MustCompile(`\[llm\] XTRADER_AGENT_TOKEN=(\S+)`)
	tokens := func() []string {
		var found []string
		for _, match := range tokenRe.FindAllStringSubmatch(c.stdout.String(), -1) {
			found = append(found, match[1])
		}
		return found
	}
	configOf := func(token string) agentapi.Config {
		t.Helper()
		var cfg agentapi.Config
		resp := internalCall(t, srv, token, http.MethodGet, "/config", nil, &cfg)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		return cfg
	}
	served := func() string {
		t.Helper()
		require.NoError(t, c.manager.Reconcile(ctx))
		a, ok := c.dashboard.agents.get("llm")
		require.True(t, ok)
		return a.Address
	}

	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return len(tokens()) == 1 }, 5*time.Second, 10*time.Millisecond)
	cfg := configOf(tokens()[0])
	assert.Equal(t, "paper", cfg.Mode)
	assert.Equal(t, llm.APIAddress, cfg.APIAddress)
	assert.False(t, cfg.Reanalysis)

	// changed while it runs: live, another address, lot and token of the
	// bridge, and its switches and its prompt, which apply while it runs
	moved := "localhost:15601"
	_, err := c.store.UpdateAgent(ctx, llm.ID, store.AgentUpdate{Mode: ptrTo("live"), APIAddress: &moved,
		TradePanel: ptrTo(true), Reanalysis: ptrTo(true), Params: store.Params{"pair": "XAUUSDm", "lot": "0.05"}})
	require.NoError(t, err)
	require.NoError(t, c.store.SetAgentSecrets(ctx, llm.ID, store.AgentSecrets{BridgeToken: secret("bridge-new")}))
	prompt, err := c.store.CreatePrompt(ctx, "lifecycle", "a test", "# lifecycle\n\nFollow the trend.", "test")
	require.NoError(t, err)
	require.NoError(t, c.store.SetAgentPrompt(ctx, llm.ID, &prompt.ID))
	assert.Equal(t, llm.APIAddress, served())
	assert.True(t, c.manager.State("llm").RestartPending)

	// it crashes: started again by the supervisor
	pid := c.manager.supervisor.State("llm").PID
	require.Positive(t, pid)
	require.NoError(t, syscall.Kill(-pid, syscall.SIGKILL))
	require.Eventually(t, func() bool { return len(tokens()) == 2 }, 5*time.Second, 10*time.Millisecond)
	cfg = configOf(tokens()[1])
	assert.Equal(t, "paper", cfg.Mode, "the mode waits for the restart")
	assert.Equal(t, llm.APIAddress, cfg.APIAddress)
	assert.Equal(t, "0.01", cfg.Params["lot"])
	require.NotNil(t, cfg.Account)
	assert.Equal(t, "bridge-new", cfg.Account.BridgeToken, "a secret is no setting of trading: the one of now")
	assert.True(t, cfg.TradePanel, "its switches apply while it runs")
	assert.True(t, cfg.Reanalysis, "its reanalysis too")
	require.NotNil(t, cfg.Prompt, "its prompt applies while it runs")
	assert.Equal(t, prompt.ID, cfg.Prompt.ID)
	assert.Equal(t, llm.APIAddress, served(), "the address it listens at")
	state := c.manager.State("llm")
	assert.True(t, state.RestartPending)
	assert.Equal(t, 1, state.Restarts)
	assert.Empty(t, state.LaunchError)

	// restarted: the settings of the database
	require.NoError(t, c.manager.Restart(ctx, "llm"))
	require.Eventually(t, func() bool { return len(tokens()) == 3 }, 5*time.Second, 10*time.Millisecond)
	cfg = configOf(tokens()[2])
	assert.Equal(t, "live", cfg.Mode)
	assert.Equal(t, moved, cfg.APIAddress)
	assert.Equal(t, "0.05", cfg.Params["lot"])
	assert.Equal(t, "bridge-new", cfg.Account.BridgeToken)
	assert.Equal(t, moved, served())
	assert.False(t, c.manager.State("llm").RestartPending)
}

// B23: an agent started again by the supervisor once it exited reads the
// secrets of now, eg: a token of the bridge revoked and replaced, rather than
// failing with the one of its launch until it is restarted; each one with its
// endpoint only, the one of the launch kept with the endpoint of the launch.
func TestManager_StartedAgainWithTheSecretsOfNow(t *testing.T) {
	previous := restartDelay
	restartDelay = 10 * time.Millisecond
	t.Cleanup(func() { restartDelay = previous })
	c := newManaged(t)
	ctx := context.Background()
	llm := c.agent(t, "llm", "llm", true, func(a *store.Agent) {
		a.Mode, a.Restart = "paper", true
	})
	srv := httptest.NewServer(c.manager.InternalHandler())
	t.Cleanup(srv.Close)
	tokenRe := regexp.MustCompile(`\[llm\] XTRADER_AGENT_TOKEN=(\S+)`)
	started := func(n int) agentapi.Config {
		t.Helper()
		var tokens []string
		require.Eventually(t, func() bool {
			tokens = nil
			for _, match := range tokenRe.FindAllStringSubmatch(c.stdout.String(), -1) {
				tokens = append(tokens, match[1])
			}
			return len(tokens) == n
		}, 5*time.Second, 10*time.Millisecond)
		var cfg agentapi.Config
		require.Equal(t, http.StatusOK, internalCall(t, srv, tokens[n-1], http.MethodGet, "/config", nil, &cfg).StatusCode)
		require.NoError(t, c.manager.Reconcile(ctx))
		return cfg
	}
	crash := func() {
		t.Helper()
		pid := c.manager.supervisor.State("llm").PID
		require.Positive(t, pid)
		require.NoError(t, syscall.Kill(-pid, syscall.SIGKILL))
	}

	require.NoError(t, c.manager.Reconcile(ctx))
	cfg := started(1)
	assert.Equal(t, "bridge-llm", cfg.Account.BridgeToken)
	assert.Equal(t, "sk-llm", cfg.LLM.APIKey)
	assert.Equal(t, &agentapi.News{}, cfg.News, "the news off")

	// the secrets replaced while it runs: pending, the process has the old ones;
	// the news of all the agents set
	require.NoError(t, c.store.SetAgentSecrets(ctx, llm.ID, store.AgentSecrets{BridgeToken: secret("bridge-new"),
		LLMAPIKey: secret("sk-new")}))
	on := true
	_, err := c.store.SetNews(ctx, store.NewsUpdate{Enabled: &on, Token: secret("mcp-shared"), By: "test"})
	require.NoError(t, err)
	require.NoError(t, c.manager.Reconcile(ctx))
	assert.True(t, c.manager.State("llm").RestartPending)

	crash()
	cfg = started(2)
	assert.Equal(t, "bridge-new", cfg.Account.BridgeToken)
	assert.Equal(t, "sk-new", cfg.LLM.APIKey)
	assert.Equal(t, &agentapi.News{Enabled: true, URL: "http://mt5:22347/mcp", Token: "mcp-shared", BlockBefore: 15,
		BlockAfter: 15, BlockImportance: "high"}, cfg.News, "the news of now, with the rules of the seeds")
	state := c.manager.State("llm")
	assert.False(t, state.RestartPending, "the secrets of now, read")
	assert.Equal(t, 1, state.Restarts)

	// another bridge, pending, with its token: the new token never goes to the
	// bridge of the launch; the news of now, whatever the launch
	otherBridge := "http://127.0.0.1:18813"
	_, err = c.store.UpdateAgent(ctx, llm.ID, store.AgentUpdate{Account: &store.AccountUpdate{BridgeURL: &otherBridge}})
	require.NoError(t, err)
	require.NoError(t, c.store.SetAgentSecrets(ctx, llm.ID, store.AgentSecrets{BridgeToken: secret("bridge-other"),
		LLMAPIKey: secret("sk-newer")}))
	crash()
	cfg = started(3)
	assert.Equal(t, "http://127.0.0.1:18812", cfg.Account.BridgeURL, "the bridge waits for the restart")
	assert.Equal(t, "bridge-new", cfg.Account.BridgeToken, "with the token it had")
	assert.Equal(t, "mcp-shared", cfg.News.Token, "the token of the news, whatever the launch")
	assert.Equal(t, "http://mt5:22347/mcp", cfg.News.URL, "the server of the news, of the seeds")
	assert.Equal(t, 15, cfg.News.BlockBefore)
	assert.Equal(t, "sk-newer", cfg.LLM.APIKey, "the same provider")
	assert.True(t, c.manager.State("llm").RestartPending)

	// restarted: the settings of the database
	require.NoError(t, c.manager.Restart(ctx, "llm"))
	cfg = started(4)
	assert.Equal(t, otherBridge, cfg.Account.BridgeURL)
	assert.Equal(t, "bridge-other", cfg.Account.BridgeToken)
	assert.Equal(t, "mcp-shared", cfg.News.Token)
	assert.False(t, c.manager.State("llm").RestartPending)
}

// An agent of an account of MT5 inactive, or whose terminal switches account,
// is stopped and not launched, told why: launched again once its account is
// the active one, the switch done.
func TestManager_AgentHeldByItsMT5Account(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	trend := c.agent(t, "matrend", "trend", true)
	account, err := c.store.CreateMT5Account(ctx, store.MT5AccountInput{Name: ptrTo("Real"),
		Login: ptrTo(int64(478378599)), Server: ptrTo("Exness-MT5Real11"),
		BridgeURL: ptrTo("http://127.0.0.1:18812")}, true, "", store.MT5Change{By: "test"})
	require.NoError(t, err)
	require.NoError(t, c.store.SetAgentMT5Account(ctx, trend.ID, &account.ID, store.MT5Change{By: "test"}))
	running := func(want bool) {
		t.Helper()
		require.Eventually(t, func() bool {
			_ = c.manager.Reconcile(ctx)
			return c.manager.supervisor.Running("matrend") == want
		}, 5*time.Second, 20*time.Millisecond)
	}

	running(true)
	_, err = c.store.ActivateMT5Account(ctx, account.ID, false, store.MT5Change{By: "test"})
	require.NoError(t, err)
	running(false)
	assert.Contains(t, c.manager.State("matrend").LaunchError, "está inativa")

	_, err = c.store.ActivateMT5Account(ctx, account.ID, true, store.MT5Change{By: "test"})
	require.NoError(t, err)
	running(true)
	assert.Empty(t, c.manager.State("matrend").LaunchError)

	release := c.dashboard.settings.mt5.hold([]string{"matrend"})
	running(false)
	assert.Contains(t, c.manager.State("matrend").LaunchError, "trocando de conta")
	release()
	running(true)
	assert.Empty(t, c.manager.State("matrend").LaunchError)
}
