//go:build unix

package remote

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/controller"
	"github.com/raykavin/xtrader/internal/pgtest"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// fakeAgentArg runs the test binary as an agent the controller launches, see
// fakeAgent.
const fakeAgentArg = "remote-agent"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == fakeAgentArg {
		os.Exit(fakeAgent())
	}
	os.Exit(m.Run())
}

// fakeAgent is an LLM agent launched by the controller: it keeps an order, a
// decision and an entry of its journal through the controller, tells the
// runtime it applies, and stops when interrupted, as the agents do.
func fakeAgent() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fail := func(what string, err error) int {
		fmt.Printf("%s: %v\n", what, err)
		return 1
	}
	agent, err := Connect(ctx)
	if err != nil || agent == nil {
		return fail("connect", err)
	}
	fmt.Println("args:", strings.Join(agent.Args(), " "))
	if err := agent.Open(filepath.Join("data", "outbox")); err != nil {
		return fail("open", err)
	}
	defer func() { _ = agent.Close() }()

	orders, err := agent.Orders(ctx)
	if err != nil {
		return fail("orders", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	order := &model.Order{Pair: "XAUUSDm", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Price: 2000, Quantity: 0.01, CreatedAt: now, UpdatedAt: now}
	if err := orders.CreateOrder(order); err != nil {
		return fail("order", err)
	}
	decisions, err := agent.Decisions(ctx)
	if err != nil {
		return fail("decisions", err)
	}
	prompt := agent.Prompt()
	id := fmt.Sprintf("%s-XAUUSDm-%d", now.Format("20060102T150405"), order.ID)
	d := ui.Decision{ID: id, Pair: "XAUUSDm", Timeframe: "15m", Candle: now.Add(-15 * time.Minute).Unix(),
		DecidedAt: now.Unix(), Trigger: "candle", Model: "openai gpt-5", Prompt: &prompt.Ref, Price: 2000,
		Action: "ENTER_LONG", Confidence: 0.8, Reason: "the trend", Outcome: "applied", OrderIDs: []int64{order.ID},
		Levels: []ui.Level{}, Notes: []ui.DecisionNote{},
		Attachments: []ui.DecisionAttachment{{Index: 0, MediaType: "image/png", Size: 3}}}
	if err := decisions.Save(d, []llm.Attachment{{MediaType: "image/png", Data: []byte("PNG")}}); err != nil {
		return fail("decision", err)
	}
	if err := agent.Journal().Append(llm.JournalEntry{At: now, ID: id, Trigger: "candle", Model: "openai gpt-5",
		Prompt: &prompt.Ref, Input: llm.Input{Symbol: "XAUUSDm", Timeframe: "15m", Time: now.Add(-15 * time.Minute)},
		Decision: &llm.Decision{Action: llm.ActionEnterLong, Confidence: 0.8, Reason: "the trend"}}); err != nil {
		return fail("journal", err)
	}
	agent.Prompts().ReportPrompt(prompt.Ref, nil)

	fmt.Printf("records kept, order %d\n", order.ID)
	err = agent.Serve(ctx, func(r agentapi.Runtime) {
		p := agent.Prompt()
		fmt.Printf("runtime trade_panel=%t telegram=%t prompt=%s v%d %q\n", r.TradePanel, r.TelegramControl,
			p.Ref.Name, p.Ref.Version, strings.TrimSpace(strings.SplitAfter(p.Content, "\n\n")[1]))
	}, func(context.Context) error { select {} }) // a bot deaf to its context, as the one of xtrader
	fmt.Println("stopping:", err)
	return 0
}

// lockedBuffer is a buffer safe for the tests to read while processes write
// it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The controller launches the agent, which gets its settings, keeps its
// records in the database without waiting for it, applies its runtime as it
// changes, and stops when disabled, its records sent.
func TestAgentOfTheController(t *testing.T) {
	dsn := pgtest.Migrated(t, filepath.Join("..", "..", ".."))
	key := []byte(strings.Repeat("k", 32))
	st, err := store.Open(config.Database{DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 4,
		ConnMaxLifetime: time.Minute, LogLevel: "silent"}, key)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	prompt, err := st.CreatePrompt(ctx, "remote_test", "a test", "# remote_test\n\nFollow the trend.", "test")
	require.NoError(t, err)
	magic := int64(777)
	secret := func(value string) *string { return &value }
	created, err := st.CreateAgent(ctx, store.Agent{Name: "llm", Kind: "llm", Enabled: true, Mode: "live",
		APIAddress: "localhost:15564", Launched: true, TradePanel: true, Params: store.Params{"pair": "XAUUSDm"},
		Account: &store.Account{BridgeURL: "http://127.0.0.1:18812", Magic: &magic},
		LLM:     &store.LLMSettings{Provider: "openai", Model: "gpt-5", PromptID: &prompt.ID, Params: store.Params{}}},
		store.AgentSecrets{LLMAPIKey: secret("sk-remote-test"), BridgeToken: secret("bridge-remote-test")})
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	stdout := &lockedBuffer{}
	sup := controller.NewSupervisor(stdout, stdout, 5*time.Second)
	dashboard, err := controller.NewDashboard(nil)
	require.NoError(t, err)
	manager := controller.NewManager(st, config.Agents{API: listener.Addr().String(), Dir: t.TempDir(),
		Commands: map[string]string{"llm": os.Args[0] + " " + fakeAgentArg}, StopTimeout: 5 * time.Second},
		sup, dashboard, nil)
	server := &http.Server{Handler: manager.InternalHandler(), ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		sup.StopAll()
		_ = server.Close()
	})
	eventually := func(what string, condition func() bool) {
		t.Helper()
		require.Eventually(t, condition, 20*time.Second, 20*time.Millisecond, "%s: %s", what, stdout.String())
	}

	require.NoError(t, manager.Reconcile(ctx))
	eventually("started", func() bool { return strings.Contains(stdout.String(), "records kept") })
	output := stdout.String()
	assert.Contains(t, output, "-mt5-magic=777")
	assert.Contains(t, output, "-llm-model=gpt-5")
	assert.NotContains(t, output, "remote-test", "no secret in the flags")
	eventually("the runtime applied", func() bool {
		return strings.Contains(stdout.String(),
			`runtime trade_panel=true telegram=false prompt=remote_test v1 "Follow the trend."`)
	})

	// the records in the database
	var orders []model.Order
	eventually("the order", func() bool {
		orders, err = st.Orders(ctx, created.ID, 0, 10)
		return err == nil && len(orders) == 1
	})
	assert.Equal(t, 2000.0, orders[0].Price)
	decisions, err := st.Decisions(ctx, created.ID, "XAUUSDm", 10)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	d := decisions[0]
	assert.Equal(t, []int64{orders[0].ID}, d.OrderIDs)
	require.NotNil(t, d.Prompt)
	assert.Equal(t, ui.DecisionPrompt{ID: prompt.ID, Name: "remote_test", Version: 1}, *d.Prompt)
	mediaType, data, err := st.DecisionAttachment(ctx, created.ID, d.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, "image/png", mediaType)
	assert.Equal(t, []byte("PNG"), data)
	links, err := st.DecisionLinks(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, map[int64]string{orders[0].ID: d.ID}, links)
	eventually("the journal", func() bool {
		remembered, err := st.JournalMemory(ctx, created.ID, 5)
		return err == nil && len(remembered) == 1 && remembered[0].Reason == "the trend"
	})
	eventually("the status of the prompt", func() bool {
		a, err := st.Agent(ctx, created.ID)
		return err == nil && a.LLM.PromptStatus != nil && a.LLM.PromptStatus.Applied &&
			a.LLM.PromptStatus.Version == 1
	})

	// the switches and the prompt, changed while it runs
	on := true
	_, err = st.UpdateAgent(ctx, created.ID, store.AgentUpdate{TelegramControl: &on})
	require.NoError(t, err)
	content := "# remote_test\n\nFollow the trend, v2."
	_, err = st.UpdatePrompt(ctx, prompt.ID, store.PromptEdit{Content: &content, Version: 1}, "test")
	require.NoError(t, err)
	require.NoError(t, manager.Reconcile(ctx))
	eventually("the runtime switched", func() bool {
		return strings.Contains(stdout.String(),
			`runtime trade_panel=true telegram=true prompt=remote_test v2 "Follow the trend, v2."`)
	})
	assert.False(t, manager.State("llm").RestartPending, "applied while it runs")

	// disabled: stopped; enabled again: numbered after its orders
	off := false
	_, err = st.UpdateAgent(ctx, created.ID, store.AgentUpdate{Enabled: &off})
	require.NoError(t, err)
	require.NoError(t, manager.Reconcile(ctx))
	stopping := time.Now()
	eventually("stopped", func() bool {
		return strings.Contains(stdout.String(), "stopping: <nil>") && !manager.State("llm").Running
	})
	assert.Less(t, time.Since(stopping), 4*time.Second, "stopped by its interrupt, not killed after the timeout")
	assert.NotContains(t, stdout.String(), "killed")
	_, err = st.UpdateAgent(ctx, created.ID, store.AgentUpdate{Enabled: &on})
	require.NoError(t, err)
	require.NoError(t, manager.Reconcile(ctx))
	eventually("started again", func() bool { return strings.Count(stdout.String(), "records kept") == 2 })
	eventually("its second order", func() bool {
		orders, err = st.Orders(ctx, created.ID, 0, 10)
		return err == nil && len(orders) == 2
	})
	assert.Greater(t, orders[1].ID, orders[0].ID)
}
