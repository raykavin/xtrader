package llm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// fakeFeeder serves the candles of candles, by pair and timeframe, counting
// the reads.
type fakeFeeder struct {
	candles map[string][]model.Candle // by "pair timeframe"
	reads   map[string]int
	fail    bool
}

func (f *fakeFeeder) CandlesByLimit(_ context.Context, pair, period string, limit int) ([]model.Candle, error) {
	key := pair + " " + period
	f.reads[key]++
	if f.fail {
		return nil, errors.New("the bridge is down")
	}
	candles := f.candles[key]
	return candles[max(len(candles)-limit, 0):], nil
}

// sineCandles returns n candles of period ending with the one opened at last,
// closing on a sine around price.
func sineCandles(n int, period time.Duration, last time.Time, price, swing float64) []model.Candle {
	candles := make([]model.Candle, n)
	for i := range candles {
		c := price + swing*math.Sin(float64(i)/5)
		open := price + swing*math.Sin(float64(i-1)/5)
		candles[i] = model.Candle{Time: last.Add(-time.Duration(n-1-i) * period), Open: open, Close: c,
			High: math.Max(open, c) + swing/10, Low: math.Min(open, c) - swing/10, Volume: 100}
	}
	return candles
}

func TestDefaultHigherTimeframes(t *testing.T) {
	require.Equal(t, []string{"15m", "1h"}, defaultHigherTimeframes("1m"))
	require.Equal(t, []string{"1h", "4h"}, defaultHigherTimeframes("15m"))
	require.Equal(t, []string{"4h", "1d"}, defaultHigherTimeframes("1h"))
	require.Equal(t, []string{"1d", "1w"}, defaultHigherTimeframes("4h"))
	require.Equal(t, []string{"1w"}, defaultHigherTimeframes("1d"))
	require.Nil(t, defaultHigherTimeframes("x"))
}

func TestTrader_HigherTimeframes(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.Correlated = map[string][]string{testPair: {"USTECm", testPair, "XAUUSDm"}} }, 0)
	for range 4 {
		r.add(100) // the next close at 04:45, in the middle of a candle of 4h
	}
	closeAt := r.nextClose() // the close of the candle added by step
	now := closeAt.Add(2 * time.Second)
	r.trader.now = func() time.Time { return now }
	hour := closeAt.Truncate(time.Hour).Add(-time.Hour) // the last complete hour
	feeder := &fakeFeeder{reads: map[string]int{}, candles: map[string][]model.Candle{
		testPair + " 1h": sineCandles(200, time.Hour, hour, 100, 3),
		testPair + " 4h": sineCandles(200, 4*time.Hour, closeAt.Truncate(4*time.Hour).Add(-4*time.Hour), 100, 6),
		testPair + " 1d": sineCandles(3, 24*time.Hour, closeAt.Truncate(24*time.Hour).Add(-24*time.Hour), 100, 5),
		testPair + " 1w": sineCandles(2, 7*24*time.Hour, closeAt.Add(-8*24*time.Hour), 100, 8),
		// the pair itself, left out of its correlated markets
		testPair + " 15m": sineCandles(100, 15*time.Minute, closeAt.Add(-15*time.Minute), 100, 3),
		// its returns following the ones of the pair
		"USTECm 15m": sineCandles(100, 15*time.Minute, closeAt.Add(-15*time.Minute), 30000, 50),
		// its market closed three days ago
		"XAUUSDm 15m": sineCandles(100, 15*time.Minute, closeAt.Add(-72*time.Hour), 4300, 5),
	}}
	r.trader.SetFeeder(feeder)

	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	in := r.provider.last()
	require.Len(t, in.HigherTimeframes, 2, "the ones of 15m: 1h and 4h")
	frame := in.HigherTimeframes[0]
	require.Equal(t, "1h", frame.Timeframe)
	require.Len(t, frame.Candles, 8, "Config.HigherCandles sent as rows")
	require.NotEmpty(t, frame.Levels)
	require.LessOrEqual(t, len(frame.Levels), 2*frameLevelsPerSide)
	require.Contains(t, []string{"up", "down", "mixed"}, frame.Trend)
	require.Len(t, frame.EMAFast, 2, "the last two values of the EMA 20")
	require.Len(t, frame.EMASlow, 2)
	switch frame.Trend {
	case "up":
		require.Greater(t, float64(frame.EMAFast[1]), float64(frame.EMASlow[1]))
	case "down":
		require.Less(t, float64(frame.EMAFast[1]), float64(frame.EMASlow[1]))
	}
	require.Positive(t, float64(frame.ATR))

	require.NotNil(t, in.Reference)
	day := feeder.candles[testPair+" 1d"][2]
	require.Equal(t, number(day.High), in.Reference.PreviousDayHigh)
	require.Equal(t, number(day.Close), in.Reference.PreviousDayClose)
	week := feeder.candles[testPair+" 1w"][1]
	require.Equal(t, number(week.Low), in.Reference.PreviousWeekLow)
	require.True(t, known(float64(in.Reference.DayHigh)), "the day so far, from its hours and the candles")

	require.Len(t, in.Correlated, 2, "the pair itself left out")
	require.Equal(t, "USTECm", in.Correlated[0].Symbol)
	require.False(t, in.Correlated[0].MarketClosed)
	require.True(t, in.Correlated[1].MarketClosed, "XAUUSDm closed three days ago")

	// read once per candle of each timeframe: the next decision, at 05:00,
	// reads the 1h and the 15m candles again (one closed), not the 4h, the
	// daily and the weekly ones
	require.Equal(t, time.Date(2026, 9, 27, 4, 45, 0, 0, time.UTC), closeAt)
	reads := fmt.Sprint(feeder.reads)
	now = now.Add(15 * time.Minute)
	r.step(100.5, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, 1, feeder.reads[testPair+" 4h"], "kept until the next close of 4h, was %s", reads)
	require.Equal(t, 1, feeder.reads[testPair+" 1d"])
	require.Equal(t, 2, feeder.reads["USTECm 15m"], "a new candle of 15m")
}

func TestTrader_FeederDown(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	feeder := &fakeFeeder{reads: map[string]int{}, fail: true}
	r.trader.SetFeeder(feeder)
	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	in := r.provider.last()
	require.Empty(t, in.HigherTimeframes, "decided without them")
	require.NotNil(t, in.Reference, "the day so far, from the candles of the bot")
	require.True(t, known(float64(in.Reference.DayHigh)))
	require.False(t, known(float64(in.Reference.PreviousDayHigh)), "unknown without the daily candles")
	require.NotNil(t, in.Structure, "computed from the candles")

	reads := fmt.Sprint(feeder.reads)
	r.step(100.5, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, reads, fmt.Sprint(feeder.reads), "not read again within a minute of a failure")
}

func TestTrendAndCorrelation(t *testing.T) {
	up := make([]float64, 80)
	for i := range up {
		up[i] = 100 + float64(i)
	}
	require.Equal(t, "up", trendOf(up))
	down := make([]float64, 80)
	for i := range down {
		down[i] = 200 - float64(i)
	}
	require.Equal(t, "down", trendOf(down))
	require.Equal(t, "unknown", trendOf(up[:30]))

	require.InDelta(t, 1, pearson([]float64{1, 2, 3}, []float64{2, 4, 6}), 1e-12)
	require.InDelta(t, -1, pearson([]float64{1, 2, 3}, []float64{3, 2, 1}), 1e-12)
	require.True(t, math.IsNaN(pearson([]float64{1, 1, 1}, []float64{1, 2, 3})))
	require.InDelta(t, 10, changePct([]float64{100, 105, 110}), 1e-12)
}
