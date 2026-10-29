//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The excursions of the trades of a range: the extremes of the price while
// each one was open, from the candles of its symbol at the bridge of its
// account (the ask of a short: its bid plus the spread), valued as the trade
// itself (its gross over its move; its lots at the tick value of the symbol
// when the price didn't move); a trade without its opening told; each trade
// read once for good.
func TestExcursions_ThePathOfEachTrade(t *testing.T) {
	previous := performanceTTL
	performanceTTL = 0
	t.Cleanup(func() { performanceTTL = previous })
	c := newManaged(t)
	ctx := context.Background()
	brl := newFakeBridge(t, "bridge-token", 198974329, "BRL")
	c.agent(t, "llm", "llm", true, onBridge(brl.URL, 101, "XAUUSDm"))
	a, err := c.store.AgentByName(ctx, "llm")
	require.NoError(t, err)
	require.NoError(t, c.store.SetAgentSecrets(ctx, a.ID, store.AgentSecrets{BridgeToken: secret("bridge-token")}))
	call, srv := settingsSession(t, c)

	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-48 * time.Hour)
	next := day.Add(24 * time.Hour)
	at := func(d time.Duration) int64 { return day.Add(d).Unix() }
	brl.mu.Lock()
	brl.deals = []bridgeDeal{
		// a long: 10 up, 50 booked, 5 a unit of the price
		traded(1, 10, 101, dealEntryIn, true, day.Add(time.Hour), 0.01, 4000, 0, 0),
		traded(2, 10, 101, dealEntryOut, false, day.Add(3*time.Hour), 0.01, 4010, 50, 0),
		// a short: 10 down, 50 booked
		traded(3, 20, 101, dealEntryIn, false, day.Add(4*time.Hour), 0.01, 4100, 0, 0),
		traded(4, 20, 101, dealEntryOut, true, day.Add(5*time.Hour), 0.01, 4090, 50, 0),
		// its opening older than the deals read
		traded(5, 30, 101, dealEntryOut, false, day.Add(5*time.Hour+30*time.Minute), 0.01, 4000, 10, 0),
		// a long closed at its entry: valued by the tick value of the symbol
		traded(6, 40, 101, dealEntryIn, true, day.Add(6*time.Hour), 0.01, 4000, 0, 0),
		traded(7, 40, 101, dealEntryOut, false, day.Add(7*time.Hour), 0.01, 4000, 0, 0),
	}
	brl.rates = []excursionRate{
		{Time: at(time.Hour), High: 4005, Low: 3990},
		{Time: at(2 * time.Hour), High: 4030, Low: 4001},
		{Time: at(4 * time.Hour), High: 4105, Low: 4095, Spread: 200},
		{Time: at(4*time.Hour + 30*time.Minute), High: 4098, Low: 4085, Spread: 200},
		{Time: at(6 * time.Hour), High: 4002, Low: 3998},
	}
	brl.mu.Unlock()

	read := func() ui.PerformanceExcursionsResponse {
		t.Helper()
		resp, body := call(http.MethodGet, fmt.Sprintf("/api/performance/excursions?from=%d&to=%d", day.Unix(),
			next.Unix()), nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		var response ui.PerformanceExcursionsResponse
		require.NoError(t, json.Unmarshal([]byte(body), &response))
		return response
	}
	got := read()
	assert.True(t, got.Complete)
	require.Len(t, got.Excursions, 4, "one per trade, in their order")
	byPosition := make(map[int64]ui.PerformanceExcursion)
	for _, x := range got.Excursions {
		byPosition[x.Position] = x
	}
	assert.Equal(t, ui.PerformanceExcursion{Account: "198974329", Position: 10, Timeframe: "M1", High: 4030, Low: 3990,
		MFE: 150, MAE: 50}, byPosition[10], "30 up and 10 down at 5 a unit of the price")
	short := byPosition[20]
	assert.InDelta(t, 4105.2, short.High, 1e-9, "the ask: the bid plus 200 points of 0.001")
	assert.InDelta(t, 4085.2, short.Low, 1e-9)
	assert.InDelta(t, 74, short.MFE, 1e-9, "(4100 - 4085.2) x 5")
	assert.InDelta(t, 26, short.MAE, 1e-9, "(4105.2 - 4100) x 5")
	assert.Equal(t, "abertura desconhecida", byPosition[30].Error)
	flat := byPosition[40]
	assert.Empty(t, flat.Error)
	assert.InDelta(t, 10, flat.MFE, 1e-9, "2 up: 0.01 lot at 0.5 a tick of 0.001")
	assert.InDelta(t, 10, flat.MAE, 1e-9)

	brl.mu.Lock()
	reads := len(brl.ratesRead)
	brl.mu.Unlock()
	assert.Equal(t, 3, reads, "the candles of the three trades with their opening")
	read()
	brl.mu.Lock()
	assert.Equal(t, reads, len(brl.ratesRead), "read once for good")
	brl.mu.Unlock()

	resp, _ := call(http.MethodGet, "/api/performance/excursions?from=10&to=5", nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, _ = do(t, http.MethodGet, fmt.Sprintf("%s/api/performance/excursions?from=%d&to=%d", srv.URL, day.Unix(),
		next.Unix()), "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// The candles of a trade: the finest timeframe whose candles over its
// duration are 5000 at most; its extremes widened to its entry and exit.
func TestExcursions_TimeframeAndWidening(t *testing.T) {
	for duration, want := range map[time.Duration]string{time.Hour: "M1", 3 * 24 * time.Hour: "M1",
		4 * 24 * time.Hour: "M5", 30 * 24 * time.Hour: "M15", 300 * 24 * time.Hour: "H4"} {
		name, _ := timeframeOf(int64(duration.Seconds()))
		assert.Equal(t, want, name, "%v", duration)
	}
	trade := ui.PerformanceTrade{Side: "BUY", Volume: 0.01, Entry: 4000, Exit: 4020, Gross: 100}
	x, ok := excursionOf(trade, []excursionRate{{High: 4010, Low: 3995}}, excursionSymbol{})
	require.True(t, ok)
	assert.Equal(t, 4020.0, x.High, "the exit above the candles read")
	assert.Equal(t, 100.0, x.MFE, "at least what it booked")
	assert.Equal(t, 25.0, x.MAE)
	_, ok = excursionOf(ui.PerformanceTrade{Side: "BUY", Entry: 4000, Exit: 4000}, []excursionRate{{High: 1, Low: 1}},
		excursionSymbol{})
	assert.False(t, ok, "no value without a move nor a tick value")
}
