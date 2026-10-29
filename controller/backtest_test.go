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

	mu    sync.Mutex
	paths []string
	since map[string]time.Time // the first candle the broker keeps of a timeframe of MT5, eg: M1
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
	b := &historyBridge{start: start, since: map[string]time.Time{}}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.paths = append(b.paths, r.Method+" "+r.URL.Path)
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
	script := "#!/bin/sh\necho 'PROGRESS 1 100' >&2\ntrap 'exit 0' INT TERM\nwhile true; do sleep 0.1; done\n"
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
	} {
		request := ok
		change(&request)
		_, _, err := b.plan(context.Background(), request, "admin", time.Now())
		assert.ErrorIs(t, err, store.ErrInvalid, name)
	}

}
