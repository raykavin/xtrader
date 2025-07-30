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
			MaxTokens: 2000, CalendarURL: "http://127.0.0.1:22346/mcp", CalendarToken: "mcp-secret",
			Params: map[string]string{"llm-min-stop-atr": "-1.5", "llm-widen-stop": "true"}},
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
	assert.Equal(t, "http://127.0.0.1:22346/mcp", *calendar)
	assert.Equal(t, -1.5, *minStop, "a negative number is a value")
	assert.True(t, *widen)
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	assert.False(t, set["llm-base-url"], "only the ones set")
	assert.False(t, set["telegram-control"])
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

// Serve returns as the context of the agent is done, the bot never stopping
// with it, ErrUnauthorized when the controller refused the agent.
func TestAgent_Serve(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.cfg = testConfig()
		f.runtime = agentapi.Runtime{Version: 1}
	})
	forever := func(context.Context) error { select {} } // as the bot: deaf to its context

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
