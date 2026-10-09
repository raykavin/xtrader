package ui

import (
	"encoding/json"
	"math"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// analysisLLM is fakeLLM drawing the analysis of each pair on the chart.
type analysisLLM struct {
	*fakeLLM

	analysisMu sync.Mutex
	analyses   map[string]Analysis
}

func (a *analysisLLM) ChartAnalysis(pair string) (Analysis, bool) {
	a.analysisMu.Lock()
	defer a.analysisMu.Unlock()
	analysis, ok := a.analyses[pair]
	return analysis, ok
}

func price(v float64) *float64 { return &v }

// The last analysis of the model on a pair goes with its snapshot and with
// each decision kept, its drawings and its intent; none for a pair without
// one, nor for a strategy that draws none. The numbers that aren't finite
// are left out, as JSON can't write them.
func TestChart_Analysis(t *testing.T) {
	_, llm := newLLMChart(t)
	to := int64(1790000900)
	strategy := &analysisLLM{fakeLLM: llm, analyses: map[string]Analysis{"BTCUSDm": {DecisionID: "d1",
		Timeframe: "30m", Candle: 1790000000, DecidedAt: 1790001800, Action: "BUY_LIMIT", Outcome: "refused",
		Drawings: []Drawing{
			{ID: "fvg", Kind: DrawingZone, Label: "FVG", Bias: BiasBullish, Timeframe: "4h", From: 1789990000, To: &to,
				Low: price(83500), High: price(83800)},
			{ID: "bad", Kind: DrawingLevel, Label: "BSL", Bias: BiasBullish, Timeframe: "30m", From: 1789990000,
				Price: price(math.Inf(1))},
		},
		Intent: &Intent{Action: "BUY_LIMIT", Side: "BUY", OrderType: IntentLimit, Entry: price(83700),
			StopLoss: price(83400), TakeProfit: price(84600), Outcome: "refused", Error: "the risk:reward"}}}}
	c := newTestChart(t, WithStrategyIndicators(strategy))
	at := time.Date(2026, 9, 27, 13, 0, 0, 0, time.UTC)
	c.OnCandle(candleAt("BTCUSDm", at, 84000))
	c.OnCandle(candleAt("XAUUSDm", at, 4300))
	srv := newTestServer(t, c)

	resp, body := send(t, http.MethodGet, srv.URL+"/api/BTCUSDm/snapshot", "", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var snapshot Snapshot
	require.NoError(t, json.Unmarshal([]byte(body), &snapshot))
	require.NotNil(t, snapshot.Analysis)
	require.Equal(t, "d1", snapshot.Analysis.DecisionID)
	require.Len(t, snapshot.Analysis.Drawings, 1, "the drawing of a price not finite left out")
	require.Equal(t, "fvg", snapshot.Analysis.Drawings[0].ID)
	require.Equal(t, to, *snapshot.Analysis.Drawings[0].To)
	require.Equal(t, "the risk:reward", snapshot.Analysis.Intent.Error)

	_, body = send(t, http.MethodGet, srv.URL+"/api/XAUUSDm/snapshot", "", "")
	require.NotContains(t, body, `"analysis"`, "none for the pair")

	events := c.events.subscribe()
	defer c.events.unsubscribe(events)
	strategy.listener(Decision{ID: "d3", Pair: "BTCUSDm", Action: "WAIT"})
	event := <-events
	var decision DecisionEvent
	require.NoError(t, json.Unmarshal(event.data, &decision))
	require.NotNil(t, decision.Analysis, "the analysis of the pair once the decision is kept")
	require.Equal(t, "d1", decision.Analysis.DecisionID)

	// a strategy that draws none: no analysis
	plain, _ := newLLMChart(t)
	snapshotOf := plain.snapshot("BTCUSDm")
	require.Nil(t, snapshotOf.Analysis)
}
