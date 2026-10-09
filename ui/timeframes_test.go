package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// timeframeStrategy is fakeStrategy trading on its timeframe.
type timeframeStrategy struct {
	fakeStrategy
	timeframe string
}

func (s timeframeStrategy) Timeframe() string { return s.timeframe }

// fakeSource returns the candles of each timeframe, the last limit of them,
// and records the reads.
type fakeSource struct {
	mu      sync.Mutex
	candles map[string][]model.Candle
	err     error
	reads   []string
}

func (f *fakeSource) CandlesByLimit(_ context.Context, pair, period string, limit int) ([]model.Candle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, pair+" "+period)
	if f.err != nil {
		return nil, f.err
	}
	list := f.candles[period]
	return list[max(len(list)-limit, 0):], nil
}

// candlesOf returns the candles of the response of the request of url.
func candlesOf(t *testing.T, url string) CandlesResponse {
	t.Helper()
	resp, body := get(t, url)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var candles CandlesResponse
	require.NoError(t, json.Unmarshal([]byte(body), &candles))
	return candles
}

// at30 is the open of the candle i of 30 minutes of the tests.
func at30(i int) time.Time {
	return time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * 30 * time.Minute)
}

// The timeframes of the chart: the ones of the dashboard and the one of the
// strategy, by their period, the one of the strategy told apart; without a
// source, only the ones built from its candles available.
func TestChart_ViewTimeframes(t *testing.T) {
	c := newTestChart(t, WithStrategyIndicators(timeframeStrategy{timeframe: "30m"}))
	require.Equal(t, []ViewTimeframe{
		{Timeframe: "1m"}, {Timeframe: "5m"}, {Timeframe: "15m"},
		{Timeframe: "30m", Execution: true, Available: true},
		{Timeframe: "4h", Available: true}, {Timeframe: "1d", Available: true},
	}, c.viewTimeframes(), "the ones below the strategy unavailable without a source")

	c = newTestChart(t, WithStrategyIndicators(timeframeStrategy{timeframe: "1h"}), WithCandleSource(&fakeSource{}))
	views := c.viewTimeframes()
	list := make([]string, 0, len(views))
	for _, view := range views {
		require.True(t, view.Available, view.Timeframe)
		list = append(list, view.Timeframe)
	}
	require.Equal(t, []string{"1m", "5m", "15m", "30m", "1h", "4h", "1d"}, list, "the one of the strategy among them")

	c.OnCandle(candleAt("BTCUSDm", at30(0), 100))
	snapshot := c.snapshot("BTCUSDm")
	require.Equal(t, "1h", snapshot.Timeframe)
	require.Len(t, snapshot.Timeframes, 7)

	require.Equal(t, "1d", normalizeTimeframe("D"))
	require.Equal(t, "1d", normalizeTimeframe(" d1 "))
	require.Equal(t, "4h", normalizeTimeframe("4H"))
	require.Equal(t, "1M", normalizeTimeframe("1M"), "the months as they are")
}

// The candles of the timeframe of the strategy are its own, the one in
// progress last; the ones above it, without a source, are built from them,
// each with the open of its first, the close of its last, the extremes of all
// and their volumes summed.
func TestChart_CandlesOfTheStrategyAndResampled(t *testing.T) {
	c := newTestChart(t, WithStrategyIndicators(timeframeStrategy{timeframe: "30m"}))
	for i := range 10 { // 00:00 to 04:30
		c.OnCandle(candleAt("BTCUSDm", at30(i), float64(100+i)))
	}
	partial := candleAt("BTCUSDm", at30(10), 111)
	partial.Complete = false
	c.OnCandle(partial)
	srv := newTestServer(t, c)

	own := candlesOf(t, srv.URL+"/api/BTCUSDm/candles")
	require.Equal(t, "30m", own.Timeframe)
	require.Equal(t, "30m", own.ExecutionTimeframe)
	require.Equal(t, SourceStrategy, own.Source)
	require.Len(t, own.Candles, 11)
	require.Equal(t, 111.0, own.Candles[10].Close, "the one in progress last")
	require.Len(t, candlesOf(t, srv.URL+"/api/BTCUSDm/candles?timeframe=30m&limit=3").Candles, 3)

	four := candlesOf(t, srv.URL+"/api/BTCUSDm/candles?timeframe=4h")
	require.Equal(t, SourceResampled, four.Source)
	require.Equal(t, []Candle{
		{Time: at30(0).Unix(), Open: 99, High: 109, Low: 98, Close: 107, Volume: 800},
		{Time: at30(8).Unix(), Open: 107, High: 113, Low: 106, Close: 111, Volume: 300},
	}, four.Candles, "00:00 to 04:00 complete, 04:00 in progress")

	daily := candlesOf(t, srv.URL+"/api/BTCUSDm/candles?timeframe=D")
	require.Equal(t, "1d", daily.Timeframe)
	require.Len(t, daily.Candles, 1)
	require.Equal(t, at30(0).Unix(), daily.Candles[0].Time)

	resp, body := get(t, srv.URL+"/api/BTCUSDm/candles?timeframe=5m")
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, body, "below the timeframe of the strategy")
	resp, body = get(t, srv.URL+"/api/BTCUSDm/candles?timeframe=2h")
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, body, "one of 1m, 5m, 15m, 30m, 4h, 1d")
	resp, _ = get(t, srv.URL+"/api/ETHUSDm/candles?timeframe=4h")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// With a source, the candles of a timeframe are its complete ones, kept for a
// while for the dashboards, followed by the ones in progress since: above the
// timeframe of the strategy, its candles merged in the grid of the ones read;
// below it, the prices it received, the open the close before.
func TestChart_CandlesOfTheSource(t *testing.T) {
	source := &fakeSource{candles: map[string][]model.Candle{
		"4h": {{Time: at30(-8), Open: 90, High: 99, Low: 89, Close: 98, Volume: 10},
			{Time: at30(0), Open: 98, High: 105, Low: 97, Close: 104, Volume: 20}},
		"5m": {{Time: at30(4), Open: 103, High: 104, Low: 102, Close: 103.5, Volume: 5}},
	}}
	c := newTestChart(t, WithStrategyIndicators(timeframeStrategy{timeframe: "30m"}), WithCandleSource(source))
	now := at30(0)
	c.clock = func() time.Time { return now }
	for i := range 10 { // 00:00 to 04:30, the last one in the 4h of 04:00 not read yet
		now = at30(i).Add(30 * time.Minute)
		c.OnCandle(candleAt("BTCUSDm", at30(i), float64(100+i)))
	}
	srv := newTestServer(t, c)

	four := candlesOf(t, srv.URL+"/api/BTCUSDm/candles?timeframe=4h")
	require.Equal(t, SourceBroker, four.Source)
	require.Equal(t, "30m", four.ExecutionTimeframe)
	require.Len(t, four.Candles, 3)
	require.Equal(t, Candle{Time: at30(8).Unix(), Open: 107, High: 111, Low: 106, Close: 109, Volume: 200},
		four.Candles[2], "the 4h in progress from the candles of the strategy")
	candlesOf(t, srv.URL+"/api/BTCUSDm/candles?timeframe=4h")
	require.Len(t, source.reads, 1, "read once for a while")

	// below the timeframe of the strategy: after the last 5m read (04:55), the
	// prices received since, the close of 04:30 at 05:00 the first
	source.mu.Lock()
	source.candles["5m"] = []model.Candle{{Time: at30(9).Add(25 * time.Minute), Open: 108, High: 109.5, Low: 108,
		Close: 109, Volume: 5}}
	source.mu.Unlock()
	partial := candleAt("BTCUSDm", at30(10), 104)
	partial.Complete = false
	for _, tick := range []struct {
		minutes int
		price   float64
	}{{6, 104}, {8, 106}, {11, 105}} {
		now = at30(10).Add(time.Duration(tick.minutes) * time.Minute)
		partial.Close = tick.price
		c.OnCandle(partial)
	}
	five := candlesOf(t, srv.URL+"/api/BTCUSDm/candles?timeframe=5m")
	require.Equal(t, []Candle{
		{Time: at30(9).Add(25 * time.Minute).Unix(), Open: 108, High: 109.5, Low: 108, Close: 109, Volume: 5},
		{Time: at30(10).Unix(), Open: 109, High: 109, Low: 109, Close: 109},
		{Time: at30(10).Add(5 * time.Minute).Unix(), Open: 109, High: 109, Low: 104, Close: 106},
		{Time: at30(10).Add(10 * time.Minute).Unix(), Open: 106, High: 106, Low: 105, Close: 105},
	}, five.Candles, "the ones in progress from the prices received, each opening at the close before")

	// every timeframe of the dashboard may be shown
	for _, timeframe := range ViewTimeframes {
		source.mu.Lock()
		source.candles[timeframe] = append(source.candles[timeframe], model.Candle{Time: at30(0), Open: 1, High: 2,
			Low: 1, Close: 2})
		source.mu.Unlock()
		got := candlesOf(t, srv.URL+"/api/BTCUSDm/candles?timeframe="+timeframe)
		require.Equal(t, timeframe, got.Timeframe)
		require.NotEmpty(t, got.Candles, timeframe)
	}

	source.err = errors.New("bridge down")
	c.viewMu.Lock()
	c.viewCache = make(map[string]viewCached)
	c.viewMu.Unlock()
	resp, body := get(t, srv.URL+"/api/BTCUSDm/candles?timeframe=15m")
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	require.Contains(t, body, "bridge down")
}
