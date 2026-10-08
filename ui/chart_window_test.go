package ui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/strategy"
)

// sizedStrategy records the candles its indicators are computed on.
type sizedStrategy struct {
	warmupStrategy
	sizes *[]int
}

func (s sizedStrategy) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	*s.sizes = append(*s.sizes, len(df.Close))
	return s.warmupStrategy.Indicators(df)
}

// M7: a chart running for months keeps the last ChartCandles candles of a
// pair, with the warmup of its indicators before them: the snapshot and the
// indicators, computed again on every close and every snapshot, stay the same
// size, drawn on every candle kept past the warmup.
func TestChart_KeepsTheLastCandlesOnly(t *testing.T) {
	var sizes []int
	c := newTestChart(t, WithStrategyIndicators(sizedStrategy{sizes: &sizes}))
	c.events.subscribe() // a dashboard connected: the indicators of every close
	const warmup = 5     // of warmupStrategy
	keep := ChartCandles + warmup
	base := time.Unix(1_700_000_000, 0).UTC()
	total := keep + 1500
	for i := range total {
		c.OnCandle(candleAt("ETHUSDT", base.Add(time.Duration(i)*time.Minute), float64(1000+i)))
		require.LessOrEqual(t, len(c.candles["ETHUSDT"]), keep)
	}
	partial := candleAt("ETHUSDT", base.Add(time.Duration(total)*time.Minute), 1)
	partial.Complete = false
	c.OnCandle(partial)

	require.Len(t, c.candles["ETHUSDT"], keep)
	df := c.dataframe["ETHUSDT"]
	require.Len(t, df.Close, keep)
	require.Len(t, df.Time, keep)
	require.Equal(t, float64(1000+total-1), df.Close.Last(0), "the last ones")
	require.Equal(t, base.Add(time.Duration(total-keep)*time.Minute), df.Time[0])
	for _, size := range sizes {
		require.LessOrEqual(t, size, keep, "the indicators of the closes on the candles kept")
	}

	sizes = nil
	snapshot := getSnapshot(t, c, "ETHUSDT")
	require.Len(t, snapshot.Candles, keep+1, "the candles kept, then the one in progress")
	require.Equal(t, base.Add(time.Duration(total-keep)*time.Minute).Unix(), snapshot.Candles[0].Time)
	require.Equal(t, partial.Time.Unix(), snapshot.Candles[keep].Time)
	require.Equal(t, []int{keep}, sizes)
	undeclared := snapshot.Indicators[1]
	require.Equal(t, "undeclared", undeclared.Name)
	require.Len(t, undeclared.Metrics[0].Points, ChartCandles, "drawn past the warmup")
	require.Equal(t, snapshot.Candles[warmup].Time, undeclared.Metrics[0].Points[0].Time)
}

// A backtest, served once it ends, keeps all its candles.
func TestChart_WithoutAWindowKeepsAll(t *testing.T) {
	c := newTestChart(t, WithStrategyIndicators(warmupStrategy{}), WithCandleWindow(0))
	base := time.Unix(1_700_000_000, 0).UTC()
	total := ChartCandles + 100
	for i := range total {
		c.OnCandle(candleAt("ETHUSDT", base.Add(time.Duration(i)*time.Minute), float64(1000+i)))
	}
	require.Len(t, c.candles["ETHUSDT"], total)
	require.Len(t, getSnapshot(t, c, "ETHUSDT").Candles, total)

	small := newTestChart(t, WithCandleWindow(10), WithCustomIndicators(&fakeIndicator{}))
	for i := range 100 {
		small.OnCandle(candleAt("ETHUSDT", base.Add(time.Duration(i)*time.Minute), float64(1000+i)))
	}
	require.Len(t, small.candles["ETHUSDT"], 10, "no warmup: the window of 10")
}
