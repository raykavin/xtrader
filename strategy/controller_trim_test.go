package strategy

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/testdata/mocks"
)

// seenFrame is what a strategy was handed: its candles, by their close and
// time, copied when handed.
type seenFrame struct {
	close   []float64
	time    []time.Time
	funding []float64
}

func seen(df *model.Dataframe) seenFrame {
	return seenFrame{close: slices.Clone(df.Close), time: slices.Clone(df.Time),
		funding: slices.Clone(df.Metadata["funding"])}
}

// recordingStrategy keeps a copy of what it is handed, as it is handed.
type recordingStrategy struct {
	warmup     int
	indicators []seenFrame
	onCandle   []seenFrame
	onPartial  []seenFrame
}

func (r *recordingStrategy) Timeframe() string { return "1h" }
func (r *recordingStrategy) WarmupPeriod() int { return r.warmup }
func (r *recordingStrategy) Indicators(df *model.Dataframe) []ChartIndicator {
	r.indicators = append(r.indicators, seen(df))
	return nil
}
func (r *recordingStrategy) OnCandle(df *model.Dataframe, _ service.Broker) {
	r.onCandle = append(r.onCandle, seen(df))
}
func (r *recordingStrategy) OnPartialCandle(df *model.Dataframe, _ service.Broker) {
	r.onPartial = append(r.onPartial, seen(df))
}

// M7: a bot running for months keeps warmupsKept warmups of candles only, the
// strategy handed at each close the same candles as with the whole history
// kept: its warmup, the metadata of the candles in step.
func TestController_DataframeKeepsItsWarmups(t *testing.T) {
	const warmup, candles = 20, 500
	strategy := &recordingStrategy{warmup: warmup}
	controller := NewStrategyController("BTCUSDT", strategy, &mocks.Broker{})
	controller.Start()

	var all []model.Candle
	for i := range candles {
		c := candle(i, 100+float64(i%37)+float64(i)/10)
		c.Metadata = map[string]float64{"funding": float64(i)}
		all = append(all, c)
		require.NoError(t, controller.OnCandle(c))
		require.LessOrEqual(t, len(controller.dataframe.Time), warmupsKept*warmup+1, "candle %d", i)
		require.Len(t, controller.dataframe.Close, len(controller.dataframe.Time))
		require.Len(t, controller.dataframe.Metadata["funding"], len(controller.dataframe.Time))
	}
	require.Len(t, controller.dataframe.Time, warmupsKept*warmup+1)
	require.Equal(t, all[candles-1].Time, controller.dataframe.Time[warmupsKept*warmup])
	require.Equal(t, all[candles-1-warmupsKept*warmup].Time, controller.dataframe.Time[0], "the last ones")

	require.Len(t, strategy.onCandle, candles-warmup+1)
	for n, got := range strategy.onCandle {
		last := warmup - 1 + n // the candle of the close
		want := all[last-warmup+1 : last+1]
		require.Len(t, got.close, warmup)
		for i, c := range want {
			require.Equal(t, c.Close, got.close[i], "close %d of the sample of the candle %d", i, last)
			require.Equal(t, c.Time, got.time[i])
			require.Equal(t, c.Metadata["funding"], got.funding[i], "the metadata in step")
		}
	}
	require.Equal(t, strategy.onCandle, strategy.indicators, "the indicators on the same candles")
	require.Equal(t, all[candles-1].Time, controller.dataframe.LastUpdate)
}

// M7: a high frequency strategy is handed on each tick the candles kept, the
// one in progress the last: warmupsKept warmups of the candles closed before
// it, the last ones of the whole history, never all of them.
func TestController_PartialCandleWithTheCandlesKept(t *testing.T) {
	const warmup, candles = 10, 300
	strategy := &recordingStrategy{warmup: warmup}
	controller := NewStrategyController("BTCUSDT", strategy, &mocks.Broker{})
	controller.Start()

	var closes []float64
	var times []time.Time
	for i := range candles {
		c := candle(i, 100+float64(i))
		closes, times = append(closes, c.Close), append(times, c.Time)
		require.NoError(t, controller.OnCandle(c))
	}
	partial := candle(candles, 1000)
	partial.Complete = false
	require.NoError(t, controller.OnPartialCandle(partial))
	partial.Close = 1001
	require.NoError(t, controller.OnPartialCandle(partial))

	require.Len(t, strategy.onPartial, 2)
	kept := warmupsKept * warmup // closed, before the one in progress
	for i, got := range strategy.onPartial {
		require.Len(t, got.close, kept+1, "tick %d: the candles kept, then the one in progress", i)
		require.Equal(t, closes[candles-kept:], got.close[:kept], "tick %d: the last closes of the history", i)
		require.Equal(t, times[candles-kept:], got.time[:kept])
		require.Equal(t, partial.Time, got.time[kept])
	}
	require.Equal(t, 1000.0, strategy.onPartial[0].close[kept])
	require.Equal(t, 1001.0, strategy.onPartial[1].close[kept], "the tick of now, in place")
	require.Equal(t, strategy.onPartial, strategy.indicators[len(strategy.indicators)-2:],
		"the indicators on the same candles")

	// the candle closes in place of the one in progress: the strategy handed
	// its warmup, as ever
	partial.Complete, partial.Close = true, 1002
	require.NoError(t, controller.OnCandle(partial))
	last := strategy.onCandle[len(strategy.onCandle)-1]
	require.Equal(t, append(slices.Clone(closes[candles-warmup+1:]), 1002), last.close)
	require.Len(t, controller.dataframe.Time, kept+1)
}
