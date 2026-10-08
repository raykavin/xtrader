package llm

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/indicator"
	"github.com/raykavin/xtrader/model"
)

// wave returns candles closing at closes, 1 around each: open at the close
// before, high and low 1 above and below the higher and the lower of both.
func wave(closes ...float64) []model.Candle {
	base := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	candles := make([]model.Candle, len(closes))
	for i, c := range closes {
		open := c
		if i > 0 {
			open = closes[i-1]
		}
		candles[i] = model.Candle{Time: base.Add(time.Duration(i) * 15 * time.Minute), Open: open, Close: c,
			High: math.Max(open, c) + 1, Low: math.Min(open, c) - 1, Volume: 100 + float64(i)}
	}
	return candles
}

func TestPivots(t *testing.T) {
	// a top at index 4 (106), a bottom at index 8 (99), another top at 12 (106.2)
	candles := wave(100, 102, 103, 104, 105, 104, 102, 100, 98, 100, 102, 104, 105.2, 104, 102, 101)
	found := pivots(candles, 3)
	require.Contains(t, found, pivot{index: 4, price: 106, high: true})
	require.Contains(t, found, pivot{index: 8, price: 97})
	require.Contains(t, found, pivot{index: 12, price: 106.2, high: true})
	for _, p := range found {
		require.GreaterOrEqual(t, p.index, 3, "the strength before it")
		require.Less(t, p.index, len(candles)-3, "the strength after it, confirmed")
	}
}

func TestLevels(t *testing.T) {
	found := []pivot{
		{index: 10, price: 100, high: false}, {index: 30, price: 100.2, high: false}, // a support touched twice
		{index: 20, price: 110, high: true}, {index: 40, price: 109.8, high: true}, // a resistance twice
		{index: 45, price: 104, high: true},   // a single swing near the price
		{index: 5, price: 90, high: false},    // far below, once
		{index: 25, price: 120, high: true},   // far above, once
		{index: 35, price: 99.9, high: false}, // the support, a third time
	}
	zones := levels(found, 1, 105, 50, 4)
	require.Equal(t, LevelSupport, zones[0].Kind, "a swing high below the price: a support, its role swapped")
	require.Equal(t, number(104), zones[0].Price, "the nearest first")

	var support, resistance *Zone
	for i := range zones {
		if zones[i].Touches >= 2 && zones[i].Kind == LevelSupport {
			support = &zones[i]
		}
		if zones[i].Touches >= 2 && zones[i].Kind == LevelResistance {
			resistance = &zones[i]
		}
	}
	require.NotNil(t, support)
	require.Equal(t, 3, support.Touches)
	require.InDelta(t, 100.0333, float64(support.Price), 1e-3)
	require.Equal(t, number(99.9), support.Low)
	require.Equal(t, number(100.2), support.High)
	require.Equal(t, 15, support.LastTouch, "the swing of index 35, of 50")
	require.InDelta(t, -4.9667, float64(support.DistanceATR), 1e-3)
	require.NotNil(t, resistance)
	require.Equal(t, 2, resistance.Touches)
	require.Len(t, zones, 4, "the twice touched ones, completed with single swings to two a side")
	require.Equal(t, number(120), zones[3].Price, "the single resistance completing its side")
	require.Empty(t, levels(nil, 1, 105, 50, 4))
}

func TestSwingStructure(t *testing.T) {
	h := func(prices ...float64) []pivot {
		list := make([]pivot, len(prices))
		for i, p := range prices {
			list[i] = pivot{index: i, price: p}
		}
		return list
	}
	require.Equal(t, "higher highs and higher lows (uptrend)", swingStructure(h(10, 12), h(5, 6)))
	require.Equal(t, "lower highs and lower lows (downtrend)", swingStructure(h(12, 10), h(6, 5)))
	require.Equal(t, "higher high and lower low (expanding)", swingStructure(h(10, 12), h(6, 5)))
	require.Equal(t, "lower high and higher low (contracting)", swingStructure(h(12, 10), h(5, 6)))
	require.Equal(t, "unclear: too few swings", swingStructure(h(10), h(5, 6)))
}

func TestRoundLevels(t *testing.T) {
	require.Equal(t, []number{84000, 84500, 85000}, roundLevels(84200))
	require.Equal(t, []number{4200, 4250, 4300}, roundLevels(4285.9))
	require.Equal(t, []number{1, 1.05, 1.1}, roundLevels(1.0801))
	require.Empty(t, roundLevels(0))
}

func TestDescribeCandle(t *testing.T) {
	previous := model.Candle{Open: 105, Close: 101, High: 106, Low: 100}
	hammer := describeCandle(model.Candle{Open: 101, Close: 102, High: 102.5, Low: 96}, &previous, 2)
	require.Equal(t, "up", hammer.Direction)
	require.Contains(t, hammer.Pattern, "bullish pin bar")
	require.InDelta(t, 3.25, float64(hammer.RangeATR), 1e-9)
	require.InDelta(t, 1/6.5*100, float64(hammer.BodyPct), 1e-9)

	engulfing := describeCandle(model.Candle{Open: 100.5, Close: 106, High: 106.5, Low: 100}, &previous, 2)
	require.Contains(t, engulfing.Pattern, "bullish engulfing")
	require.Contains(t, engulfing.Pattern, "strong bullish candle")
	require.InDelta(t, 6/6.5, float64(engulfing.CloseInRange), 1e-9)

	inside := describeCandle(model.Candle{Open: 103, Close: 103.1, High: 104, Low: 102}, &previous, 2)
	require.Equal(t, "doji, inside bar", inside.Pattern)

	flat := describeCandle(model.Candle{Open: 1, Close: 1, High: 1, Low: 1}, nil, 2)
	require.Equal(t, "flat", flat.Direction)
	require.Empty(t, flat.Pattern)
}

func TestComputeStructure(t *testing.T) {
	closes := []float64{100, 102, 104, 106, 104, 102, 100, 102, 104, 106.2, 104, 102, 100.3, 102, 104, 105, 103}
	candles := wave(closes...)
	atr := make([]float64, len(candles))
	for i := range atr {
		atr[i] = math.NaN()
		if i >= 5 {
			atr[i] = 2 + float64(i)/100
		}
	}
	s := computeStructure(candles, atr)
	require.NotNil(t, s)
	require.Equal(t, len(candles), s.Candles)
	require.Equal(t, "higher highs and higher lows (uptrend)", s.SwingStructure)
	require.Equal(t, number(107.2), s.SwingHighs[0].Price, "the newest first")
	require.Equal(t, 7, s.SwingHighs[0].CandlesAgo)
	require.NotEmpty(t, s.Levels)
	require.Len(t, s.LastCandles, 3)
	require.Equal(t, "down", s.LastCandles[0].Direction, "the last candle, 105 to 103")
	require.Equal(t, ratio(100), s.ATRPercentile, "the last ATR, the highest")
	require.Equal(t, ratio(100), s.VolumePercentile)
	require.Equal(t, 17, s.RecentRange.Candles)
	require.Nil(t, computeStructure(candles[:5], atr[:5]), "too few candles")
}

func TestPercentile(t *testing.T) {
	require.Equal(t, 50.0, percentile([]float64{1, 2, 3, 4}, 2))
	require.Equal(t, 100.0, percentile([]float64{1, math.NaN(), 3}, 3))
	require.True(t, math.IsNaN(percentile([]float64{math.NaN()}, 1)))
	require.True(t, math.IsNaN(percentile([]float64{1}, math.NaN())))
}

// B37: with a short warmup, the window reaches back to the first ATRLen
// candles, where talib writes 0 rather than NaN: those zeros aren't ATRs, the
// percentile leaves them out rather than counting them as the quietest ones.
func TestComputeStructure_ATRPercentileShortWarmup(t *testing.T) {
	const n, length = 40, 14
	base := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	candles := make([]model.Candle, n)
	high, low, closes := make([]float64, n), make([]float64, n), make([]float64, n)
	for i := range candles {
		width := 5 - 4*float64(i)/(n-1) // narrowing: the last ATR the lowest
		candles[i] = model.Candle{Time: base.Add(time.Duration(i) * time.Hour), Open: 100, Close: 100,
			High: 100 + width, Low: 100 - width, Volume: 1}
		high[i], low[i], closes[i] = candles[i].High, candles[i].Low, candles[i].Close
	}
	atr := indicator.ATR(high, low, closes, length)
	require.Zero(t, atr[0], "talib warming up")
	zeros := 0
	for _, v := range atr {
		if v == 0 {
			zeros++
		}
	}
	require.Positive(t, zeros)

	s := computeStructure(candles, atr)
	require.NotNil(t, s)
	require.InDelta(t, 100/float64(n-zeros), float64(s.ATRPercentile), 1e-3, "the lowest of the known ATRs")

	// all of them warming up: unknown
	require.True(t, math.IsNaN(float64(computeStructure(candles[:length], atr[:length]).ATRPercentile)))
}

// The derived numbers go with 4 significant digits, and a zone of a single
// swing without its span, its price.
func TestStructureJSON(t *testing.T) {
	data, err := json.Marshal(Zone{Kind: LevelSupport, Price: 84480.22, Low: 84480.22, High: 84480.22, Touches: 1,
		LastTouch: 11, DistanceATR: -0.12832305})
	require.NoError(t, err)
	require.JSONEq(t, `{"kind":"support","price":84480.22,"touches":1,"last_touch_candles_ago":11,"distance_atr":-0.1283}`,
		string(data))

	data, err = json.Marshal(Zone{Kind: LevelResistance, Price: 100, Low: 99.5, High: 100.5, Touches: 2,
		DistanceATR: ratio(math.NaN())})
	require.NoError(t, err)
	require.JSONEq(t, `{"kind":"resistance","price":100,"low":99.5,"high":100.5,"touches":2,`+
		`"last_touch_candles_ago":0,"distance_atr":null}`, string(data))
}
