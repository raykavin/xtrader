package remote

import (
	"context"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/ui"
)

// testConfig returns the settings of an LLM agent of the tests.
func testConfig() agentapi.Config {
	magic, comment := int64(4242), "nb:llm"
	return agentapi.Config{ID: 1, Name: "llm", Kind: "llm", Mode: "live", APIAddress: "localhost:5564",
		TradePanel: true, Params: map[string]string{"pair": "XAUUSDm", "lot": "0.01", "mode": "paper"},
		Account: &agentapi.Account{BridgeURL: "http://127.0.0.1:18812", BridgeToken: "bridge-secret",
			Magic: &magic, Comment: &comment},
		LLM: &agentapi.LLM{Provider: "openai", Model: "gpt-5", APIKey: "sk-secret", ReasoningEffort: "low",
			MaxTokens: 2000,
			Params:    map[string]string{"llm-min-stop-atr": "-1.5", "llm-widen-stop": "true"}},
		Prompt: &agentapi.Prompt{PromptRef: agentapi.PromptRef{ID: 2, Name: "trend", Version: 1},
			Content: "# t\n\nTrend."}}
}

// The flags of the settings parse as the command line did, the columns over
// the params, the secrets never among them.
func TestAgent_Args(t *testing.T) {
	a := &Agent{Config: testConfig()}
	args := a.Args()
	for _, arg := range args {
		assert.NotContains(t, arg, "secret")
	}

	flags := flag.NewFlagSet("llmagent", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var (
		mode      = flags.String("mode", "backtest", "")
		pair      = flags.String("pair", "", "")
		lot       = flags.String("lot", "", "")
		api       = flags.String("api", "", "")
		panel     = flags.Bool("trade-panel", false, "")
		telegram  = flags.Bool("telegram-control", false, "")
		bridge    = flags.String("mt5-bridge", "", "")
		magic     = flags.Int64("mt5-magic", 0, "")
		comment   = flags.String("mt5-comment", "", "")
		live      = flags.Bool("mt5-live", false, "")
		provider  = flags.String("llm-provider", "", "")
		model     = flags.String("llm-model", "", "")
		baseURL   = flags.String("llm-base-url", "", "")
		reasoning = flags.String("llm-reasoning", "", "")
		tokens    = flags.Int("llm-max-tokens", 0, "")
		calendar  = flags.String("calendar-mcp", "", "")
		minStop   = flags.Float64("llm-min-stop-atr", 0, "")
		widen     = flags.Bool("llm-widen-stop", false, "")
	)
	require.NoError(t, flags.Parse(args))
	assert.Equal(t, "live", *mode, "the column over the param")
	assert.Equal(t, "XAUUSDm", *pair)
	assert.Equal(t, "0.01", *lot)
	assert.Equal(t, "localhost:5564", *api)
	assert.True(t, *panel)
	assert.False(t, *telegram)
	assert.Equal(t, "http://127.0.0.1:18812", *bridge)
	assert.Equal(t, int64(4242), *magic)
	assert.Equal(t, "nb:llm", *comment)
	assert.False(t, *live)
	assert.Equal(t, "openai", *provider)
	assert.Equal(t, "gpt-5", *model)
	assert.Empty(t, *baseURL)
	assert.Equal(t, "low", *reasoning)
	assert.Equal(t, 2000, *tokens)
	assert.Empty(t, *calendar, "the server of the news never a flag: in the news of the config")
	assert.Equal(t, -1.5, *minStop, "a negative number is a value")
	assert.True(t, *widen)
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	assert.False(t, set["llm-base-url"], "only the ones set")
	assert.False(t, set["telegram-control"])
}

// The reanalysis of an LLM agent is a flag of its own, set when allowed; the
// agents of the other kinds, whose binaries have no such flag, never get it.
func TestAgent_ArgsReanalysis(t *testing.T) {
	parse := func(cfg agentapi.Config, withFlag bool) (allowed, set bool) {
		t.Helper()
		flags := flag.NewFlagSet(cfg.Kind+"agent", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		for _, name := range []string{"mode", "api", "mt5-bridge", "mt5-magic", "mt5-comment", "llm-provider",
			"llm-model", "llm-reasoning", "llm-max-tokens", "calendar-mcp", "llm-min-stop-atr", "llm-widen-stop"} {
			flags.String(name, "", "")
		}
		for name := range cfg.Params {
			if flags.Lookup(name) == nil {
				flags.String(name, "", "")
			}
		}
		flags.Bool("trade-panel", false, "")
		flags.Bool("telegram-control", false, "")
		reanalysis := new(bool)
		if withFlag {
			reanalysis = flags.Bool("reanalysis", false, "")
		}
		require.NoError(t, flags.Parse((&Agent{Config: cfg}).Args()))
		flags.Visit(func(f *flag.Flag) { set = set || f.Name == "reanalysis" })
		return *reanalysis, set
	}

	cfg := testConfig()
	cfg.Reanalysis = true
	allowed, set := parse(cfg, true)
	assert.True(t, allowed)
	assert.True(t, set)
	assert.Contains(t, (&Agent{Config: cfg}).Args(), "-reanalysis=true")

	cfg.Reanalysis = false
	allowed, set = parse(cfg, true)
	assert.False(t, allowed, "the default of the flag: forbidden")
	assert.False(t, set)

	trend := agentapi.Config{ID: 2, Name: "matrend", Kind: "trend", Mode: "live", APIAddress: "localhost:5570",
		Reanalysis: true, Params: map[string]string{"strategy": "matrend"}}
	_, set = parse(trend, false) // its binary without the flag: refused if given
	assert.False(t, set)
}

func TestConnect(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) { f.cfg = testConfig() })
	a, err := connect(context.Background(), f.client(), "llm")
	require.NoError(t, err)
	assert.Equal(t, "sk-secret", a.Config.LLM.APIKey)
	_, err = connect(context.Background(), f.client(), "other")
	require.ErrorContains(t, err, "the settings of llm to other")

	t.Setenv(agentapi.EnvToken, "")
	a, err = Connect(context.Background())
	require.NoError(t, err)
	assert.Nil(t, a, "on its own")
	t.Setenv(agentapi.EnvToken, "xta_9_nope")
	t.Setenv(agentapi.EnvURL, f.srv.URL)
	_, err = Connect(context.Background())
	require.ErrorIs(t, err, ErrUnauthorized)

	// the key of its API, apart from the token, which its API wants
	t.Setenv(agentapi.EnvToken, testToken)
	t.Setenv(agentapi.EnvAgent, "llm")
	t.Setenv(agentapi.EnvAPIKey, "the-key")
	a, err = Connect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "the-key", a.APIKey)
}

// Run applies the runtime as it changes, the prompt read beforehand, and ends
// the agent when the controller refuses it.
func TestAgent_Run(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.cfg = testConfig()
		f.runtime = agentapi.Runtime{Version: 1, TradePanel: true,
			Prompt: &agentapi.PromptRef{ID: 2, Name: "trend", Version: 1}}
	})
	a, err := connect(context.Background(), f.client(), "llm")
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, a.Open(dir))
	assert.FileExists(t, filepath.Join(dir, "llm-1.db"), "named after the agent and its ID")
	assert.Equal(t, llm.Prompt{Ref: ui.DecisionPrompt{ID: 2, Name: "trend", Version: 1}, Content: "# t\n\nTrend."},
		a.Prompt())

	var mu sync.Mutex
	var applied []agentapi.Runtime
	runtimes := func() []agentapi.Runtime {
		mu.Lock()
		defer mu.Unlock()
		return append([]agentapi.Runtime(nil), applied...)
	}
	ctx := a.Run(context.Background(), func(r agentapi.Runtime) {
		mu.Lock()
		defer mu.Unlock()
		applied = append(applied, r)
	})
	require.Eventually(t, func() bool { return len(runtimes()) == 1 }, 5*time.Second, time.Millisecond)
	assert.True(t, runtimes()[0].TradePanel)

	// a prompt of its own, switched while it runs
	f.set(func(f *fakeController) {
		f.prompt = &agentapi.Prompt{PromptRef: agentapi.PromptRef{ID: 5, Name: "pullback", Version: 3},
			Content: "# p\n\nPullback."}
	})
	f.setRuntime(agentapi.Runtime{TelegramControl: true, Prompt: &agentapi.PromptRef{ID: 5, Name: "pullback",
		Version: 3}})
	require.Eventually(t, func() bool { return len(runtimes()) == 2 }, 5*time.Second, time.Millisecond)
	last := runtimes()[1]
	assert.False(t, last.TradePanel)
	assert.True(t, last.TelegramControl)
	p, changed := a.Prompts().Current(ui.DecisionPrompt{ID: 2, Name: "trend", Version: 1})
	require.True(t, changed)
	assert.Equal(t, "# p\n\nPullback.", p.Content, "read before the runtime is applied")

	// the records reach the controller
	a.Prompts().ReportPrompt(p.Ref, nil)
	require.Eventually(t, func() bool { return len(f.written()) == 1 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, agentapi.KindPromptStatus, f.written()[0].Kind)

	// refused: the agent ends
	f.set(func(f *fakeController) { f.refuse = true })
	f.setRuntime(agentapi.Runtime{})
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the agent refused goes on")
	}
	require.NoError(t, a.Close())
}

// Close sends the records left before the agent exits.
func TestAgent_CloseFlushes(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.cfg = testConfig()
		f.cfg.LLM, f.cfg.Prompt = nil, nil
		f.runtime = agentapi.Runtime{Version: 1}
	})
	a, err := connect(context.Background(), f.client(), "")
	require.NoError(t, err)
	require.NoError(t, a.Open(t.TempDir()))
	assert.Nil(t, a.Prompts(), "an agent of the runner")
	assert.Equal(t, llm.DefaultPrompt(), a.Prompt())
	f.set(func(f *fakeController) { f.down = true })
	ctx, cancel := context.WithCancel(context.Background())
	a.Run(ctx, nil)
	require.NoError(t, a.outbox.Add(agentapi.KindOrder, "1", map[string]int{"id": 1}))
	cancel()
	f.set(func(f *fakeController) { f.down = false })
	require.NoError(t, a.Close())
	require.Len(t, f.written(), 1)
	assert.Equal(t, "1", f.written()[0].Key)
}

// Serve returns as the context of the agent is done, once the bot stopped
// with it (see TestAgent_ServeStopsTheBotBeforeTheOutboxCloses),
// ErrUnauthorized when the controller refused the agent.
func TestAgent_Serve(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.cfg = testConfig()
		f.runtime = agentapi.Runtime{Version: 1}
	})
	forever := func(ctx context.Context) error { // as the bot: until its context is done
		<-ctx.Done()
		return nil
	}

	a, err := connect(context.Background(), f.client(), "llm")
	require.NoError(t, err)
	require.NoError(t, a.Open(t.TempDir()))
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- a.Serve(ctx, nil, forever) }()
	cancel() // a signal
	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("still serving")
	}
	require.NoError(t, a.Close())

	b, err := connect(context.Background(), f.client(), "llm")
	require.NoError(t, err)
	require.NoError(t, b.Open(t.TempDir()))
	f.set(func(f *fakeController) { f.refuse = true })
	go func() { served <- b.Serve(context.Background(), nil, forever) }()
	select {
	case err := <-served:
		require.ErrorIs(t, err, ErrUnauthorized)
	case <-time.After(5 * time.Second):
		t.Fatal("still serving, refused")
	}
	require.NoError(t, b.Close())

	f.set(func(f *fakeController) { f.refuse = false })
	c, err := connect(context.Background(), f.client(), "llm")
	require.NoError(t, err)
	require.NoError(t, c.Open(t.TempDir()))
	failed := errors.New("the feed failed")
	require.ErrorIs(t, c.Serve(context.Background(), nil, func(context.Context) error { return failed }), failed)
	require.NoError(t, c.Close())
}

// fakeEntries is the order controller of a bot whose entries the controller
// blocks, see Supervise.
type fakeEntries struct {
	mu       sync.Mutex
	blocked  bool
	reason   string
	revision int64
}

func (f *fakeEntries) BlockEntries(revision int64, reason string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if revision < f.revision {
		return false
	}
	f.blocked, f.reason, f.revision = true, reason, revision
	return true
}

func (f *fakeEntries) AllowEntries(revision int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if revision < f.revision {
		return false
	}
	f.blocked, f.reason, f.revision = false, "", revision
	return true
}

func (f *fakeEntries) state() (bool, string, int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocked, f.reason, f.revision
}

// The entries of the bot follow the ones of the settings at once, then the
// ones of the runtime, before its prompt: a prompt that can't be read delays
// the rest of the runtime, never a block.
func TestAgent_SuperviseEntries(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.cfg = testConfig()
		f.cfg.Entries = agentapi.Entries{EntriesBlocked: true, EntriesReason: "stopped by the supervisor",
			EntriesRevision: 2}
		f.runtime = agentapi.Runtime{Version: 1, Entries: f.cfg.Entries}
	})
	a, err := connect(context.Background(), f.client(), "llm")
	require.NoError(t, err)
	require.NoError(t, a.Open(t.TempDir()))

	entries := &fakeEntries{}
	a.Supervise(entries)
	blocked, reason, revision := entries.state()
	require.True(t, blocked, "before the bot trades")
	require.Equal(t, "stopped by the supervisor", reason)
	require.EqualValues(t, 2, revision)

	var mu sync.Mutex
	applied := 0
	a.Run(context.Background(), func(agentapi.Runtime) {
		mu.Lock()
		defer mu.Unlock()
		applied++
	})
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return applied == 1
	}, 5*time.Second, time.Millisecond)

	// allowed again with a prompt the controller can't serve
	f.setRuntime(agentapi.Runtime{Entries: agentapi.Entries{EntriesRevision: 3},
		Prompt: &agentapi.PromptRef{ID: 9, Name: "missing", Version: 1}})
	require.Eventually(t, func() bool {
		blocked, _, revision := entries.state()
		return !blocked && revision == 3
	}, 5*time.Second, time.Millisecond)

	// an older block, eg: read before, is ignored
	f.setRuntime(agentapi.Runtime{Entries: agentapi.Entries{EntriesBlocked: true, EntriesRevision: 1}})
	time.Sleep(50 * time.Millisecond)
	blocked, _, revision = entries.state()
	require.False(t, blocked)
	require.EqualValues(t, 3, revision)
	require.NoError(t, a.Close())
}

// fakeProtection records the protection the agent applies.
type fakeProtection struct {
	mu       sync.Mutex
	settings *ui.ProtectionSettings
	arms     []ui.ProtectionArm
	applied  int
}

func (p *fakeProtection) Apply(settings *ui.ProtectionSettings, arms []ui.ProtectionArm) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.settings, p.arms, p.applied = settings, arms, p.applied+1
}

func (p *fakeProtection) state() (*ui.ProtectionSettings, []ui.ProtectionArm, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settings, p.arms, p.applied
}

// The protection of the positions follows the controller: the one of the
// settings before the bot trades, then the one of the runtime as it changes,
// whatever its prompt.
func TestAgent_Protect(t *testing.T) {
	trailing := &ui.ProtectionSettings{Unit: ui.ProtectionUnitATR,
		Trailing: ui.TrailingSettings{Enabled: true, Trigger: 1, Distance: 0.5, Step: 0.1}}
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.cfg = testConfig()
		f.cfg.Protection = agentapi.Protection{ProtectionSettings: trailing}
		f.runtime = agentapi.Runtime{Version: 1, Protection: f.cfg.Protection}
	})
	a, err := connect(context.Background(), f.client(), "llm")
	require.NoError(t, err)
	require.NoError(t, a.Open(t.TempDir()))

	protection := &fakeProtection{}
	a.Protect(protection)
	settings, _, applied := protection.state()
	require.Equal(t, 1, applied, "before the bot trades")
	require.Equal(t, trailing, settings)

	a.Run(context.Background(), nil)
	arm := ui.ProtectionArm{ID: 7, Pair: "XAUUSDm", PositionRef: "mt5:1", Feature: ui.ProtectionTrailing,
		Reason: "devolução de lucro"}
	f.setRuntime(agentapi.Runtime{Protection: agentapi.Protection{ProtectionSettings: trailing,
		ProtectionArms: []ui.ProtectionArm{arm}}, Prompt: &agentapi.PromptRef{ID: 9, Name: "missing", Version: 1}})
	require.Eventually(t, func() bool {
		_, arms, _ := protection.state()
		return len(arms) == 1 && arms[0] == arm
	}, 5*time.Second, time.Millisecond, "the arms of the runtime, its prompt failing")

	f.setRuntime(agentapi.Runtime{})
	require.Eventually(t, func() bool {
		settings, arms, _ := protection.state()
		return settings == nil && len(arms) == 0
	}, 5*time.Second, time.Millisecond, "off")
	require.NoError(t, a.Close())
}
