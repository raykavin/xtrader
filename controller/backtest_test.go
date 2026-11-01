//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// historyBridge is a bridge of the tests serving the history of a symbol: its
// specs and its candles of 1m from start, generated; every request recorded.
type historyBridge struct {
	*httptest.Server
	start time.Time

	mu      sync.Mutex
	paths   []string
	symbols map[string]int       // the requests of each symbol
	since   map[string]time.Time // the first candle the broker keeps of a timeframe of MT5, eg: M1
}

// keep makes the broker keep the candles of the timeframes of MT5 since at
// only, eg: M1 for a few months.
func (b *historyBridge) keep(at time.Time, timeframes ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, tf := range timeframes {
		b.since[tf] = at
	}
}

func newHistoryBridge(t *testing.T, start time.Time, minutes int) *historyBridge {
	t.Helper()
	b := &historyBridge{start: start, since: map[string]time.Time{}, symbols: map[string]int{}}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.paths = append(b.paths, r.Method+" "+r.URL.Path)
		b.symbols[r.URL.Query().Get("symbol")]++
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/symbol":
			_ = json.NewEncoder(w).Encode(map[string]any{"point": 0.001, "volume_step": 0.01, "volume_min": 0.01,
				"trade_contract_size": 100.0, "digits": 3})
		case "/rates":
			from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
			to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
			timeframe := r.URL.Query().Get("timeframe")
			step := map[string]int{"M1": 1, "M5": 5, "M15": 15, "H1": 60}[timeframe]
			b.mu.Lock()
			since, kept := b.since[timeframe]
			b.mu.Unlock()
			rates := []bridgeRate{}
			for i := 0; i < minutes; i += max(step, 1) {
				at := start.Add(time.Duration(i) * time.Minute).Unix()
				if at < from || at > to || (kept && at < since.Unix()) {
					continue
				}
				p := 4000 + 30*math.Sin(float64(i)/180) + 10*math.Sin(float64(i)/23)
				rates = append(rates, bridgeRate{Time: at, Open: p, High: p + 0.7, Low: p - 0.6, Close: p + 0.2,
					Volume: 1, Spread: 20})
			}
			_ = json.NewEncoder(w).Encode(rates)
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(b.Close)
	return b
}

// requested returns the requests the bridge got.
func (b *historyBridge) requested() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.paths...)
}

// asked returns how many requests of symbol the bridge got.
func (b *historyBridge) asked(symbol string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.symbols[symbol]
}

// newBacktested returns a controller of the tests running backtests with the
// real agents (go run from the root of the repository), concurrency at once,
// and an agent trend on bridge.
func newBacktested(t *testing.T, bridge *historyBridge, concurrency int) (*managed, *Backtests) {
	t.Helper()
	c := newManaged(t)
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	agents := config.Agents{Dir: root, Commands: map[string]string{"trend": "go run ./agents/trendagent"},
		Env: config.Env{"GOFLAGS=-mod=mod", "XTRADER_AGENT_TOKEN=never", "MT5_BRIDGE_TOKEN=never"}}
	b := c.dashboard.ServeBacktests(c.store, agents, config.Backtest{Concurrency: concurrency,
		Dir: t.TempDir(), MaxPeriodDays: 30, MaxRuns: 10, LLMMaxCalls: 10, LLMMaxCost: 1})
	c.agent(t, "trend", "trend", true, onBridge(bridge.URL, 101, "XAUUSDm"), func(a *store.Agent) {
		a.Params = store.Params{"pair": "XAUUSDm", "lot": "XAUUSDm=0.01", "strategy": "emapullback",
			"timeframe": "15m"}
	})
	a, err := c.store.AgentByName(context.Background(), "trend")
	require.NoError(t, err)
	require.NoError(t, c.store.SetAgentSecrets(context.Background(), a.ID,
		store.AgentSecrets{BridgeToken: secret("bridge-token")}))
	return c, b
}

// waitRuns waits for the runs of batch to end, and returns them.
func waitRuns(t *testing.T, c *managed, batch int64) []store.BacktestRun {
	t.Helper()
	var runs []store.BacktestRun
	require.Eventually(t, func() bool {
		var err error
		runs, err = c.store.BacktestRuns(context.Background(), []int64{batch}, true)
		require.NoError(t, err)
		for _, run := range runs {
			if !run.Final() {
				return false
			}
		}
		return len(runs) > 0
	}, 4*time.Minute, 200*time.Millisecond)
	return runs
}

// A batch asked on the dashboard runs the real program of the agent in
// backtest mode on the candles of its bridge: the fit and the validation
// apart, their summary and report recorded; the bridge asked for the specs
// and the candles only, never an order; the agent untouched.
func TestBacktests_ABatchRunsTheAgentsOnTheirHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the agents")
	}
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -12)
	bridge := newHistoryBridge(t, start, 12*24*60)
	c, b := newBacktested(t, bridge, 2)
	call, _ := settingsSession(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(done)
	}()
	before, err := c.store.AgentByName(ctx, "trend")
	require.NoError(t, err)

	resp, body := call(http.MethodGet, "/api/backtests", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var list ui.BacktestsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	require.Len(t, list.Agents, 1)
	assert.True(t, list.Agents[0].Compatible, list.Agents[0].Reason)

	request := ui.BacktestRequest{Agents: []string{"trend"}, From: start.Add(4 * 24 * time.Hour).Unix(),
		To: start.Add(11 * 24 * time.Hour).Unix(), ValidationShare: 0.3, Balance: 10000, Fee: 0.0001,
		SpreadPoints: 20, SlippagePoints: 5}
	resp, body = call(http.MethodPost, "/api/backtests", request)
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var batch ui.BacktestBatchInfo
	require.NoError(t, json.Unmarshal([]byte(body), &batch))
	require.Len(t, batch.Runs, 2)
	assert.Equal(t, store.SegmentFit, batch.Runs[0].Segment)
	assert.Equal(t, store.SegmentValidation, batch.Runs[1].Segment)
	assert.Equal(t, batch.Runs[0].To, batch.Runs[1].From, "the validation follows the fit")

	runs := waitRuns(t, c, batch.ID)
	for _, run := range runs {
		require.Equal(t, store.BacktestSucceeded, run.Status, run.Error)
		var summary ui.BacktestSummary
		require.NoError(t, json.Unmarshal(run.Summary, &summary))
		assert.Equal(t, 10000.0, summary.InitialBalance)
		assert.Equal(t, "USD", summary.Currency)
		assert.Positive(t, run.Total)
	}

	resp, body = call(http.MethodGet, fmt.Sprintf("/api/backtests/runs/%d", runs[0].ID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var detail struct {
		Config backtestSnapshot `json:"config"`
		Report struct {
			Status string `json:"status"`
			From   int64  `json:"from"`
		} `json:"report"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &detail))
	assert.Equal(t, "succeeded", detail.Report.Status)
	assert.Equal(t, request.From, detail.Report.From)
	assert.Contains(t, detail.Config.Args, "-strategy=emapullback")
	for _, arg := range detail.Config.Args {
		assert.NotContains(t, arg, "mt5-bridge", "no flag of the account")
		assert.NotContains(t, arg, "mt5-magic")
	}
	assert.NotContains(t, body, "bridge-token", "never a secret")

	for _, path := range bridge.requested() {
		assert.Contains(t, []string{"GET /symbol", "GET /rates"}, path, "the history only")
	}
	after, err := c.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	assert.Equal(t, before.UpdatedAt, after.UpdatedAt, "the agent untouched")
	assert.Equal(t, before.Params, after.Params)

	cancel()
	<-done
}

// A broker keeping the finer candles for less time than the period: the run
// goes on a coarser timeframe, told, else fails for want of data.
func TestBacktests_ACoarserTimeframeWhenTheBrokerLacksTheFinerCandles(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the agents")
	}
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -12)
	bridge := newHistoryBridge(t, start, 12*24*60)
	bridge.keep(start.Add(8*24*time.Hour), "M1")
	c, b := newBacktested(t, bridge, 1)
	call, _ := settingsSession(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(done)
	}()

	batchOf := func(to time.Time) int64 {
		request := ui.BacktestRequest{Agents: []string{"trend"}, From: start.Add(4 * 24 * time.Hour).Unix(),
			To: to.Unix(), Balance: 10000}
		resp, body := call(http.MethodPost, "/api/backtests", request)
		require.Equal(t, http.StatusCreated, resp.StatusCode, body)
		var batch ui.BacktestBatchInfo
		require.NoError(t, json.Unmarshal([]byte(body), &batch))
		assert.Equal(t, "1m", batch.Assumptions.ExecutionTimeframe, "the finest the period allows")
		return batch.ID
	}

	runs := waitRuns(t, c, batchOf(start.Add(11*24*time.Hour)))
	require.Len(t, runs, 1)
	require.Equal(t, store.BacktestSucceeded, runs[0].Status, runs[0].Error)
	var summary ui.BacktestSummary
	require.NoError(t, json.Unmarshal(runs[0].Summary, &summary))
	assert.Equal(t, "5m", summary.ExecutionTimeframe, "M5 kept for the whole period")
	var report agentapi.BacktestReport
	require.NoError(t, json.Unmarshal(runs[0].Result, &report))
	require.NotEmpty(t, report.Warnings)
	assert.Equal(t, "execução em M5: a corretora não tem candles de M1 de todo o período", report.Warnings[0])

	bridge.keep(start.Add(8*24*time.Hour), "M5", "M15", "H1")
	runs = waitRuns(t, c, batchOf(start.Add(10*24*time.Hour))) // another range: the candles read again
	require.Len(t, runs, 1)
	assert.Equal(t, store.BacktestFailed, runs[0].Status)
	assert.Contains(t, runs[0].Error, "dados insuficientes: os candles de XAUUSDm começam em")

	cancel()
	<-done
}

// slowAgent writes a program of the tests standing for an agent: it tells
// its progress and runs until interrupted, without a report.
func slowAgent(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "slowagent")
	// as the agents, it tells the warmup of its strategy at once when asked
	script := "#!/bin/sh\ncase \" $* \" in *' -print-warmup '*) echo '{\"timeframe\":\"15m\",\"candles\":100}'; " +
		"exit 0;; esac\necho 'PROGRESS 1 100' >&2\ntrap 'exit 0' INT TERM\nwhile true; do sleep 0.1; done\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// The runs of a batch run concurrency at once at most; a run queued is
// canceled at once, a running one interrupted; a batch with runs queued or
// running can't be deleted.
func TestBacktests_ConcurrencyAndCancel(t *testing.T) {
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -25)
	bridge := newHistoryBridge(t, start, 25*24*60)
	c, b := newBacktested(t, bridge, 1)
	b.agents.Commands = map[string]string{"trend": slowAgent(t)}
	call, _ := settingsSession(t, c)
	request := ui.BacktestRequest{Agents: []string{"trend"}, From: start.Add(5 * 24 * time.Hour).Unix(),
		To: start.Add(24 * 24 * time.Hour).Unix(), ValidationShare: 0.5, Balance: 10000}
	resp, body := call(http.MethodPost, "/api/backtests", request)
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var batch ui.BacktestBatchInfo
	require.NoError(t, json.Unmarshal([]byte(body), &batch))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(done)
	}()
	require.Eventually(t, func() bool {
		runs, err := c.store.BacktestRuns(ctx, []int64{batch.ID}, false)
		require.NoError(t, err)
		return runs[0].Status == store.BacktestRunning && runs[0].Processed > 0
	}, time.Minute, 50*time.Millisecond, "the first one runs, its progress told")
	runs, err := c.store.BacktestRuns(ctx, []int64{batch.ID}, false)
	require.NoError(t, err)
	assert.Equal(t, store.BacktestQueued, runs[1].Status, "one at a time")

	resp, _ = call(http.MethodDelete, fmt.Sprintf("/api/backtests/%d", batch.ID), nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "busy")
	resp, _ = call(http.MethodPost, fmt.Sprintf("/api/backtests/runs/%d/cancel", runs[1].ID), nil)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp, _ = call(http.MethodPost, fmt.Sprintf("/api/backtests/runs/%d/cancel", runs[0].ID), nil)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	ended := waitRuns(t, c, batch.ID)
	assert.Equal(t, store.BacktestCanceled, ended[0].Status, ended[0].Error)
	assert.Equal(t, store.BacktestCanceled, ended[1].Status)
	resp, _ = call(http.MethodPost, fmt.Sprintf("/api/backtests/runs/%d/cancel", runs[1].ID), nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "ended")
	resp, _ = call(http.MethodDelete, fmt.Sprintf("/api/backtests/%d", batch.ID), nil)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// a run of a controller stopping: interrupted
	resp, body = call(http.MethodPost, "/api/backtests", request)
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &batch))
	require.Eventually(t, func() bool {
		runs, err := c.store.BacktestRuns(context.Background(), []int64{batch.ID}, false)
		require.NoError(t, err)
		return runs[0].Status == store.BacktestRunning
	}, time.Minute, 50*time.Millisecond)
	cancel()
	<-done
	stopped, err := c.store.BacktestRuns(context.Background(), []int64{batch.ID}, false)
	require.NoError(t, err)
	assert.Equal(t, store.BacktestInterrupted, stopped[0].Status)
}

// The flags of an agent a backtest passes: never the ones of its account nor
// of the controller; the pairs and the timeframe replaced, -lot cut to the
// pairs kept.
func TestBacktests_FilterArgs(t *testing.T) {
	args := []string{"-lot=XAUUSDm=0.01;BTCUSDm=0.02", "-pair=XAUUSDm;BTCUSDm", "-strategy=matrend",
		"-timeframe=5m", "-mode=live", "-api=localhost:5570", "-mt5-bridge=http://mt5:18812", "-mt5-magic=7",
		"-mt5-comment=nb:x", "-mt5-live=true", "-trade-panel=true", "-telegram-control=true", "-leverage=400"}
	assert.Equal(t, []string{"-lot=XAUUSDm=0.01", "-strategy=matrend", "-leverage=400", "-timeframe=1h"},
		filterArgs(args, []string{"XAUUSDm"}, "1h", false))
	assert.Contains(t, filterArgs(args, []string{"XAUUSDm"}, "", false), "-timeframe=5m", "the agent's kept")
	llm := []string{"-llm-provider=openai", "-calendar-currencies=USD", "-news-block-before=15m",
		"-llm-higher-trend-required=true", "-llm-correlated=XAUUSDm=DXYm", "-llm-consult-interval=15m"}
	assert.Equal(t, []string{"-llm-provider=openai", "-llm-consult-interval=15m"},
		filterArgs(llm, []string{"XAUUSDm"}, "", true), "the context a backtest can't rebuild left out")
	// the entry filter of the agent goes with it, its reference and its samples included
	filters := []string{"-regime=trend", "-regime-method=adx", "-regime-max-atr-pct=90", "-equity-curve=20",
		"-meta-model=models/gold.json", "-meta-record=samples/gold.jsonl", "-intermarket-symbol=DXYm",
		"-intermarket-pairs=XAUUSDm"}
	assert.Equal(t, filters, filterArgs(filters, []string{"XAUUSDm"}, "", false))
}

// The environment of a run: never a token of the controller nor of a bridge;
// the key of the model of an LLM agent only.
func TestBacktests_Environment(t *testing.T) {
	t.Setenv("XTRADER_SECRETS_KEY", "never")
	agents := config.Agents{Env: config.Env{"XTRADER_AGENT_TOKEN=x", "MT5_BRIDGE_TOKEN=y", "GOFLAGS=-mod=mod"}}
	env := strings.Join(backtestEnv(agents, backtestSnapshot{}, store.AgentSecretValues{LLMAPIKey: "sk"}), "\n")
	assert.NotContains(t, env, "XTRADER_")
	assert.NotContains(t, env, "MT5_BRIDGE_TOKEN")
	assert.NotContains(t, env, "OPENAI_API_KEY", "not an LLM agent")
	assert.Contains(t, env, "GOFLAGS=-mod=mod")
	env = strings.Join(backtestEnv(agents, backtestSnapshot{LLM: &backtestLLM{Provider: "openai"}},
		store.AgentSecretValues{LLMAPIKey: "sk"}), "\n")
	assert.Contains(t, env, "OPENAI_API_KEY=sk")
}

// A batch out of the limits, or of an agent that can't be backtested, is
// refused.
func TestBacktests_PlanRefused(t *testing.T) {
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -40)
	bridge := newHistoryBridge(t, start, 10)
	c, b := newBacktested(t, bridge, 1)
	c.agent(t, "llm", "llm", false, onBridge(bridge.URL, 202, "XAUUSDm"))
	c.agent(t, "llmnokey", "llm", false, onBridge(bridge.URL, 203, "XAUUSDm"))
	nokey, err := c.store.AgentByName(context.Background(), "llmnokey")
	require.NoError(t, err)
	require.NoError(t, c.store.SetAgentSecrets(context.Background(), nokey.ID, store.AgentSecrets{LLMAPIKey: secret("")}))
	ok := ui.BacktestRequest{Agents: []string{"trend"}, From: start.Unix(), To: start.Add(10 * 24 * time.Hour).Unix(),
		Balance: 1000}
	_, runs, err := b.plan(context.Background(), ok, "admin", time.Now())
	require.NoError(t, err)
	require.Len(t, runs, 1)
	var snap backtestSnapshot
	require.NoError(t, json.Unmarshal(runs[0].Config, &snap))
	assert.Equal(t, "15m", snap.Timeframe)
	assert.True(t, snap.Assumptions.Conservative)
	assert.Equal(t, "USD", snap.Assumptions.Currency)
	assert.Nil(t, snap.Assumptions.Hours, "no trading hours unless asked")

	// the trading hours of the batch, copied to the assumptions of each run
	hours := ok
	hours.Hours = &ui.BacktestHours{Start: "22:00", End: "02:00", Flatten: true}
	_, runs, err = b.plan(context.Background(), hours, "admin", time.Now())
	require.NoError(t, err)
	var hoursSnap backtestSnapshot
	require.NoError(t, json.Unmarshal(runs[0].Config, &hoursSnap))
	assert.Equal(t, hours.Hours, hoursSnap.Assumptions.Hours)

	// an LLM agent: its model, its ceilings and its prompt copied, flagged
	llmRequest := ok
	llmRequest.Agents = []string{"llm"}
	_, runs, err = b.plan(context.Background(), llmRequest, "admin", time.Now())
	require.NoError(t, err)
	var llmSnap backtestSnapshot
	require.NoError(t, json.Unmarshal(runs[0].Config, &llmSnap))
	require.NotNil(t, llmSnap.LLM)
	assert.Equal(t, "openai", llmSnap.LLM.Provider)
	assert.Equal(t, 10, llmSnap.LLM.MaxCalls, "the default of the controller")
	assert.Contains(t, strings.Join(llmSnap.Notes, " "), "não determinístico")
	assert.NotContains(t, string(runs[0].Config), "sk-llm", "never the key")

	for name, change := range map[string]func(r *ui.BacktestRequest){
		"no agent":                               func(r *ui.BacktestRequest) { r.Agents = nil },
		"the period":                             func(r *ui.BacktestRequest) { r.To = r.From },
		"the future":                             func(r *ui.BacktestRequest) { r.To = time.Now().Add(time.Hour).Unix() },
		"too long":                               func(r *ui.BacktestRequest) { r.From = start.AddDate(0, 0, -40).Unix() },
		"the balance":                            func(r *ui.BacktestRequest) { r.Balance = 0 },
		"the validation":                         func(r *ui.BacktestRequest) { r.ValidationShare = 0.6 },
		"the timeframe":                          func(r *ui.BacktestRequest) { r.Timeframe = "7m" },
		"an LLM without the key of its provider": func(r *ui.BacktestRequest) { r.Agents = []string{"llmnokey"} },
		"an unknown agent":                       func(r *ui.BacktestRequest) { r.Agents = []string{"ghost"} },
		"a pair not traded":                      func(r *ui.BacktestRequest) { r.Pairs = []string{"BTCUSDm"} },
		"the hours apart": func(r *ui.BacktestRequest) {
			r.Hours = &ui.BacktestHours{Start: "09:00", End: "09:00"}
		},
		"the hours HH:MM": func(r *ui.BacktestRequest) { r.Hours = &ui.BacktestHours{Start: "9:00", End: "17:00"} },
	} {
		request := ok
		change(&request)
		_, _, err := b.plan(context.Background(), request, "admin", time.Now())
		assert.ErrorIs(t, err, store.ErrInvalid, name)
	}

}

// The candles read before the period cover the warmup of the strategy, as the
// agent tells it: mtfema, 8832 candles of 15m, over the calendar (half of it
// more, and a day); the default for an LLM agent, and for an agent that can't
// tell it, told.
func TestBacktests_WarmupOfTheStrategy(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the agents")
	}
	bridge := newHistoryBridge(t, time.Now().AddDate(0, 0, -5), 10)
	_, b := newBacktested(t, bridge, 1)
	ctx := context.Background()
	snap := backtestSnapshot{Agent: "trend", Kind: "trend", Pairs: []string{"XAUUSDm"}, Timeframe: "15m",
		Command: strings.Fields(b.agents.Command("trend")), Args: []string{"-strategy=mtfema", "-lot=XAUUSDm=0.01"}}
	warm, told, note := b.warmupOf(ctx, snap, backtestEnv(b.agents, snap, store.AgentSecretValues{}))
	assert.Equal(t, 8832*15*time.Minute*3/2+24*time.Hour, warm, "about 139 days")
	assert.Empty(t, note)
	assert.Equal(t, agentapi.Warmup{Timeframe: "15m", Candles: 8832}, told)

	// the entry filter of the agent: its warmup and its reference told
	filtered := snap
	filtered.Args = []string{"-strategy=emacross", "-lot=XAUUSDm=0.01", "-regime=trend", "-regime-method=adx",
		"-intermarket-symbol=DXYm"}
	_, told, note = b.warmupOf(ctx, filtered, backtestEnv(b.agents, filtered, store.AgentSecretValues{}))
	assert.Empty(t, note)
	assert.Equal(t, []string{"DXYm"}, told.References)
	assert.Equal(t, 232, told.Candles, "the strategy's, more than the filters' (the ADX 154, the reference 160)")
	assert.False(t, told.Releases)

	short := snap
	short.Args = []string{"-strategy=emacross", "-lot=XAUUSDm=0.01"}
	warm, _, _ = b.warmupOf(ctx, short, backtestEnv(b.agents, short, store.AgentSecretValues{}))
	assert.Equal(t, time.Duration(backtestWarmupCandles)*15*time.Minute, warm, "the default when larger")

	llm := snap
	llm.LLM = &backtestLLM{}
	warm, _, note = b.warmupOf(ctx, llm, nil)
	assert.Equal(t, time.Duration(backtestWarmupCandles)*15*time.Minute, warm)
	assert.Empty(t, note)

	broken := snap
	broken.Args = []string{"-strategy=nonexistent"}
	warm, _, note = b.warmupOf(ctx, broken, backtestEnv(b.agents, broken, store.AgentSecretValues{}))
	assert.Equal(t, time.Duration(backtestWarmupCandles)*15*time.Minute, warm)
	assert.Contains(t, note, "não pôde ser lido do agente")
}

// fakeCalendar is the MCP server of the economic calendar of a MetaTrader 5
// terminal, for the tests: USD only, a release of the payrolls (high, exact) at
// 12:30 and of the FOMC (medium, its time known once released) at 18:00 every
// day, their values set (never to be carried into a backtest); every call
// recorded.
type fakeCalendar struct {
	*httptest.Server
	mu    sync.Mutex
	calls []string
}

func newFakeCalendar(t *testing.T, token string) *fakeCalendar {
	t.Helper()
	f := &fakeCalendar{}
	answer := func(w http.ResponseWriter, id int64, result any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	text := func(w http.ResponseWriter, id int64, value any) {
		data, _ := json.Marshal(value)
		answer(w, id, map[string]any{"isError": false, "content": []map[string]any{{"type": "text",
			"text": string(data)}}})
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var message struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch message.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session")
			answer(w, message.ID, map[string]any{"protocolVersion": "2025-06-18"})
			return
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, message.Params.Name)
		f.mu.Unlock()
		switch message.Params.Name {
		case "economic_calendar_list_events_by_currency":
			if message.Params.Arguments["currency_code"] != "USD" {
				answer(w, message.ID, map[string]any{"isError": true, "content": []map[string]any{{"type": "text",
					"text": "unknown currency code"}}})
				return
			}
			text(w, message.ID, map[string]any{"ok": true, "events": []map[string]any{
				{"id": "1", "name": "Payrolls", "event_code": "nfp", "importance": "High importance",
					"unit": "Thousands", "time_mode": "Source publishes an exact time of an event"},
				{"id": "2", "name": "FOMC", "event_code": "fomc", "importance": "Medium importance",
					"unit": "None", "time_mode": "Source publishes a day of an event rather than its exact time."},
			}})
		case "economic_calendar_list_values":
			from, _ := time.Parse("2006-01-02T15:04:05", fmt.Sprint(message.Params.Arguments["datetime_from"]))
			to, _ := time.Parse("2006-01-02T15:04:05", fmt.Sprint(message.Params.Arguments["datetime_to"]))
			values := []map[string]any{}
			for day := from.Truncate(24 * time.Hour); day.Before(to); day = day.Add(24 * time.Hour) {
				for id, at := range map[string]time.Duration{"1": 12*time.Hour + 30*time.Minute, "2": 18 * time.Hour} {
					if when := day.Add(at); !when.Before(from) && when.Before(to) {
						values = append(values, map[string]any{"event_id": id,
							"time": when.Format("2006-01-02T15:04:05"), "actual_value": 250.0,
							"forecast_value": 180.0, "prev_value": 150.0})
					}
				}
			}
			text(w, message.ID, map[string]any{"ok": true, "values": values})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// called returns how many calls the calendar got.
func (f *fakeCalendar) called() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// referenceAgent writes a program of the tests standing for an agent whose
// strategy reads DXYm and the releases of the calendar: it tells so with its
// warmup, and its run copies the config of its backtest to copy, then writes a
// report succeeded.
func referenceAgent(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "referenceagent")
	script := `#!/bin/sh
case " $* " in *' -print-warmup '*)
	echo '{"timeframe":"15m","candles":100,"references":["DXYm"],"releases":true}'; exit 0;; esac
for arg in "$@"; do case "$arg" in -backtest-config=*) config="${arg#-backtest-config=}";; esac; done
cp "$config" "$CONFIG_COPY"
echo '{"status":"succeeded","currency":"USD","pairs":["XAUUSDm"],"timeframe":"15m","trades":[],"open":[],"equity":[],"warnings":[]}' > "$(dirname "$config")/report.json"
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// The run of an agent whose strategy reads a symbol it doesn't trade and the
// releases of the calendar, as it tells with its warmup: the candles of the
// reference read at its bridge with the same timeframe and range as the pairs,
// their file in the config of the run; the releases of the period read from
// the news of the dashboard (one calendar a URL, its days read once across the
// runs), their times and importance only, never a value; the news off, a
// warning and none.
func TestBacktests_ReferencesAndReleases(t *testing.T) {
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -12)
	bridge := newHistoryBridge(t, start, 12*24*60)
	c, b := newBacktested(t, bridge, 1)
	copied := filepath.Join(t.TempDir(), "config.json")
	b.agents.Commands = map[string]string{"trend": referenceAgent(t)}
	b.agents.Env = append(b.agents.Env, "CONFIG_COPY="+copied)
	calendar := newFakeCalendar(t, "mcp-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := c.store.SetNews(ctx, store.NewsUpdate{Enabled: boolPtr(true), Token: secret("mcp-token"),
		URL: secret(calendar.URL + "/mcp"), By: "test"})
	require.NoError(t, err)
	call, _ := settingsSession(t, c)
	done := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(done)
	}()
	request := ui.BacktestRequest{Agents: []string{"trend"}, From: start.Add(4 * 24 * time.Hour).Unix(),
		To: start.Add(11 * 24 * time.Hour).Unix(), Balance: 10000}
	runOnce := func() (agentapi.BacktestConfig, agentapi.BacktestReport, store.BacktestRun) {
		t.Helper()
		resp, body := call(http.MethodPost, "/api/backtests", request)
		require.Equal(t, http.StatusCreated, resp.StatusCode, body)
		var batch ui.BacktestBatchInfo
		require.NoError(t, json.Unmarshal([]byte(body), &batch))
		runs := waitRuns(t, c, batch.ID)
		require.Len(t, runs, 1)
		require.Equal(t, store.BacktestSucceeded, runs[0].Status, runs[0].Error)
		data, err := os.ReadFile(copied)
		require.NoError(t, err)
		var cfg agentapi.BacktestConfig
		require.NoError(t, json.Unmarshal(data, &cfg))
		var report agentapi.BacktestReport
		require.NoError(t, json.Unmarshal(runs[0].Result, &report))
		return cfg, report, runs[0]
	}

	cfg, report, run := runOnce()
	assert.Equal(t, "trend", cfg.Agent)
	assert.Equal(t, run.ID, cfg.Run, "the run, for the samples it records")
	assert.Equal(t, store.SegmentFit, cfg.Segment)
	require.Contains(t, cfg.References, "DXYm")
	assert.Contains(t, filepath.Base(cfg.References["DXYm"]), "DXYm_1m_", "the timeframe of the pairs")
	assert.FileExists(t, cfg.References["DXYm"])
	assert.Positive(t, bridge.asked("DXYm"), "read at the bridge of the agent")
	releases := cfg.Releases["XAUUSDm"]
	require.NotEmpty(t, releases)
	dataFrom := cfg.From - int64((250*time.Hour)/time.Second) // the default warmup of 1000 candles of 15m
	codes := map[string]int{}
	for _, r := range releases {
		codes[r.Code]++
		assert.GreaterOrEqual(t, r.Time, dataFrom, "within the candles read")
		assert.LessOrEqual(t, r.Time, cfg.To)
		assert.Equal(t, "USD", r.Currency)
		switch r.Code {
		case "nfp":
			assert.Equal(t, [3]any{3, true, false}, [3]any{r.Importance, r.Timed, r.Tentative}, r.Name)
		case "fomc":
			assert.Equal(t, [3]any{2, true, true}, [3]any{r.Importance, r.Timed, r.Tentative}, r.Name)
		}
	}
	assert.InDelta(t, codes["nfp"], codes["fomc"], 1, "one a day each, the edges of the range aside")
	assert.Positive(t, codes["nfp"])
	data, err := os.ReadFile(copied)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "250", "never a value released")
	assert.Contains(t, strings.Join(report.Warnings, "\n"), "calendário econômico: ")
	asked := calendar.called()

	cfg, _, again := runOnce()
	assert.Equal(t, asked, calendar.called(), "the days of the period read once across the runs, over and cached")
	assert.Equal(t, again.ID, cfg.Run, "the run of its own")
	assert.NotEqual(t, run.ID, again.ID)

	_, err = c.store.SetNews(ctx, store.NewsUpdate{Enabled: boolPtr(false), By: "test"})
	require.NoError(t, err)
	cfg, report, _ = runOnce()
	assert.Nil(t, cfg.Releases)
	assert.Contains(t, strings.Join(report.Warnings, "\n"), "as notícias estão desligadas no painel")
	assert.Contains(t, cfg.References, "DXYm", "the reference still read")

	cancel()
	<-done
}

// A real agent filtered by a reference it doesn't trade runs on the candles of
// both: the reference read at the bridge, never traded, the run succeeded.
func TestBacktests_AnAgentFilteredByAReference(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the agents")
	}
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -12)
	bridge := newHistoryBridge(t, start, 12*24*60)
	c, b := newBacktested(t, bridge, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.agent(t, "filtered", "trend", true, onBridge(bridge.URL, 102, "XAUUSDm"), func(a *store.Agent) {
		a.Params = store.Params{"pair": "XAUUSDm", "lot": "XAUUSDm=0.01", "strategy": "emapullback",
			"timeframe": "15m", "intermarket-symbol": "DXYm", "intermarket-ema": "20", "regime": "trend"}
	})
	a, err := c.store.AgentByName(ctx, "filtered")
	require.NoError(t, err)
	require.NoError(t, c.store.SetAgentSecrets(ctx, a.ID, store.AgentSecrets{BridgeToken: secret("bridge-token")}))
	call, _ := settingsSession(t, c)
	done := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(done)
	}()
	request := ui.BacktestRequest{Agents: []string{"filtered"}, From: start.Add(4 * 24 * time.Hour).Unix(),
		To: start.Add(11 * 24 * time.Hour).Unix(), Balance: 10000, SpreadPoints: 20}
	resp, body := call(http.MethodPost, "/api/backtests", request)
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var batch ui.BacktestBatchInfo
	require.NoError(t, json.Unmarshal([]byte(body), &batch))
	runs := waitRuns(t, c, batch.ID)
	require.Len(t, runs, 1)
	require.Equal(t, store.BacktestSucceeded, runs[0].Status, runs[0].Error)
	var report agentapi.BacktestReport
	require.NoError(t, json.Unmarshal(runs[0].Result, &report))
	assert.Contains(t, report.Assumptions.References, "DXYm")
	assert.Equal(t, []string{"XAUUSDm"}, report.Pairs)
	for _, trade := range report.Trades {
		assert.Equal(t, "XAUUSDm", trade.Pair, "the reference never traded")
	}
	assert.Positive(t, bridge.asked("DXYm"))
	cancel()
	<-done
}
