package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
)

// A backtest of the LLM agent as the controller runs it: the model asked on
// the candles of the period, each call recorded with its tokens and its cost,
// the run stopped at its budget (budget_exhausted); the context of the model
// never holds what a backtest can't rebuild without the future (the higher
// timeframes, the correlated markets, the calendar); the bridge never asked.
func TestBacktest_TheModelOnItsBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the agent")
	}
	var mu sync.Mutex
	var inputs []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		inputs = append(inputs, string(body))
		mu.Unlock()
		decision, _ := json.Marshal(map[string]any{"action": "WAIT", "confidence": 0.4, "entry": nil,
			"stop_loss": nil, "take_profit": nil, "reason": "sem confluência", "sentiment": 50, "levels": []any{}})
		content := string(decision)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 1000, "completion_tokens": 100}})
	}))
	defer model.Close()
	var bridgeHits int
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		bridgeHits++
		w.WriteHeader(http.StatusTeapot)
	}))
	defer bridge.Close()

	dir := t.TempDir()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var csv strings.Builder
	csv.WriteString("time,open,close,low,high,volume,spread\n")
	for i := range 3 * 24 * 60 {
		at := start.Add(time.Duration(i) * time.Minute)
		price := 4000 + 30*math.Sin(float64(i)/180)
		fmt.Fprintf(&csv, "%d,%.3f,%.3f,%.3f,%.3f,1,20\n", at.Unix(), price, price+0.2, price-0.6, price+0.7)
	}
	file := filepath.Join(dir, "XAUUSDm.csv")
	require.NoError(t, os.WriteFile(file, []byte(csv.String()), 0o600))
	report := filepath.Join(dir, "report.json")
	cfg := agentapi.BacktestConfig{From: start.Add(48 * time.Hour).Unix(), To: start.Add(72 * time.Hour).Unix(),
		Balance: 10000, Symbols: map[string]agentapi.BacktestSymbol{"XAUUSDm": {Point: 0.001}}, Report: report,
		LLMMaxCalls: 3, LLMPriceInput: 1, LLMPriceOutput: 10}
	config := filepath.Join(dir, "config.json")
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(config, data, 0o600))

	cmd := exec.Command("go", "run", ".", "-mode=backtest", "-pair=XAUUSDm", "-timeframe=15m", "-lot=XAUUSDm=0.01",
		"-csv="+file, "-csv-timeframe=1m", "-mt5-contract-size=100", "-backtest-config="+config,
		"-llm-provider=openai", "-llm-model=test-model", "-llm-base-url="+model.URL, "-mt5-bridge="+bridge.URL)
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY=sk-test")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)

	data, err = os.ReadFile(report)
	require.NoError(t, err)
	var got agentapi.BacktestReport
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, agentapi.BacktestStatusBudgetExhausted, got.Status, got.Error)
	var usage llm.RecorderReport
	require.NoError(t, json.Unmarshal(got.Extra, &usage))
	assert.Equal(t, 3, usage.Calls, "stopped at its ceiling")
	assert.True(t, usage.Exhausted)
	assert.Equal(t, 3000, usage.InputTokens)
	require.NotNil(t, usage.Cost)
	assert.InDelta(t, 3*(1000*1+100*10)/1e6, *usage.Cost, 1e-12)
	require.Len(t, usage.Recorded, 3)
	assert.Equal(t, "WAIT", usage.Recorded[0].Action)
	assert.GreaterOrEqual(t, usage.Recorded[0].At, cfg.From, "the simulated time, not the wall clock")
	assert.Less(t, usage.Recorded[0].At, cfg.To)
	assert.NotEmpty(t, usage.Recorded[0].InputHash)
	assert.Equal(t, "openai test-model", usage.Model)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, inputs, 3)
	for _, input := range inputs {
		for _, absent := range []string{`higher_timeframes`, `correlated`, `calendar`} {
			assert.NotContains(t, input, `\"`+absent+`\":`, "no %s in the context of a backtest", absent)
		}
	}
	assert.Zero(t, bridgeHits, "the bridge never asked")
}
