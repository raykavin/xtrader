package llm

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// smcCandles are the candles of a scenario of SMC, 15 minutes apart: a swing
// high at 3 (104) and a swing low at 8 (97), broken down at 14 (the first
// break); a swing high at 12 (102); a swing low at 16 (94), swept at 21 (a
// wick to 93.5 closing back above it), a swing low itself; then a
// displacement up breaking the swing high of 12 at 23 (a change of
// character), leaving a fair value gap from 96 to 98.7 (the candles 21 to 23)
// and another one from 99 to 101.5 (22 to 24), partly filled since, and the
// order block of 20, the last bearish candle before the low of the leg.
func smcCandles() []model.Candle {
	rows := [][4]float64{ // open, high, low, close
		{100, 101, 99, 100}, {100, 102, 99.5, 101.5}, {101.5, 103, 101, 102.5}, {102.5, 104, 102, 103},
		{103, 103.5, 101, 101.5}, {101.5, 102, 100, 100.5}, {100.5, 101, 99, 99.5}, {99.5, 100, 98, 98.5},
		{98.5, 99, 97, 97.5}, {97.5, 99.5, 97.5, 99}, {99, 100, 98.5, 99.5}, {99.5, 101, 99, 100.5},
		{100.5, 102, 100, 101.5}, {101.5, 101.8, 99, 99.5}, {99.5, 100, 96.5, 96.8}, {96.8, 97, 95, 95.5},
		{95.5, 96, 94, 94.5}, {94.5, 96.5, 94.2, 96.2}, {96.2, 97, 95.5, 96.8}, {96.8, 98, 96.5, 97.8},
		{97.8, 98, 95, 95.5}, {95.5, 96, 93.5, 95.8}, {95.8, 99, 95.5, 98.8}, {98.8, 102.8, 98.7, 102.6},
		{102.6, 103, 101.5, 102.2}, {102.2, 102.5, 100.5, 101}, {101, 101.5, 99.5, 100},
	}
	candles := make([]model.Candle, len(rows))
	for i, row := range rows {
		candles[i] = model.Candle{Time: smcTime(i), Open: row[0], High: row[1], Low: row[2], Close: row[3],
			Volume: 100}
	}
	return candles
}

// smcTime is the open time of the candle i of smcCandles.
func smcTime(i int) time.Time {
	return time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * 15 * time.Minute)
}

func TestComputeSMC(t *testing.T) {
	candles := smcCandles()
	atr := make([]float64, len(candles))
	for i := range atr {
		atr[i] = 1
	}
	smc := computeSMC(candles, atr, 100)
	require.NotNil(t, smc)

	require.Equal(t, []StructureBreak{
		{Kind: breakCHoCH, Direction: "up", Level: 102, SwingTime: smcTime(12), Time: smcTime(23), CandlesAgo: 3},
		{Kind: breakFirst, Direction: "down", Level: 97, SwingTime: smcTime(8), Time: smcTime(14), CandlesAgo: 12},
	}, smc.Breaks, "newest first")

	require.JSONEq(t, `[
		{"direction": "bullish", "low": 99, "high": 99.5, "time": "2026-09-25T05:45:00Z", "candles_ago": 3,
			"filled_pct": 80, "size_atr": 2.5, "distance_atr": -0.75},
		{"direction": "bullish", "low": 96, "high": 98.7, "time": "2026-09-25T05:30:00Z", "candles_ago": 4,
			"filled_pct": 0, "size_atr": 2.7, "distance_atr": -2.65}
	]`, mustJSON(t, smc.FairValueGaps), "the ones not filled, the nearest first: the bearish ones filled since")

	require.Equal(t, []OrderBlock{
		{Direction: BiasBullish, Low: 95, High: 98, Time: smcTime(20), CandlesAgo: 6, BreakTime: smcTime(23),
			DistanceATR: -3.5},
	}, smc.OrderBlocks, "the bearish one of the break down, broken by the close of 23, left out")

	require.Equal(t, []LiquiditySweep{
		{Side: "sell_side", Level: 94, SwingTime: smcTime(16), Extreme: 93.5, Time: smcTime(21), CandlesAgo: 5},
	}, smc.Sweeps, "the swings broken by a close aren't swept")

	require.Equal(t, &DealingRange{Direction: "up", High: 103, HighTime: smcTime(24), Low: 93.5,
		LowTime: smcTime(21), Equilibrium: 98.25, Position: ratio(6.5 / 9.5)}, smc.DealingRange,
		"the leg of the change of character, from its low to the high since")

	require.Nil(t, computeSMC(candles[:2*pivotStrength], atr, 100), "too few candles")
	nan := make([]float64, len(candles))
	for i := range nan {
		nan[i] = math.NaN()
	}
	require.NotNil(t, computeSMC(candles, nan, 100), "the ATR unknown: the average range")
}

// The order block of a break is mitigated once the price trades back into it,
// and left out once a close goes through it.
func TestComputeSMC_OrderBlockMitigated(t *testing.T) {
	candles := smcCandles()
	last := candles[len(candles)-1]
	at := func(i int) time.Time { return last.Time.Add(time.Duration(i) * 15 * time.Minute) }
	atr := func(n int) []float64 {
		values := make([]float64, n)
		for i := range values {
			values[i] = 1
		}
		return values
	}

	into := append(candles, model.Candle{Time: at(1), Open: 100, High: 100.2, Low: 97.5, Close: 99})
	smc := computeSMC(into, atr(len(into)), 99)
	require.Len(t, smc.OrderBlocks, 1)
	require.True(t, smc.OrderBlocks[0].Mitigated)

	through := append(candles, model.Candle{Time: at(1), Open: 100, High: 100.2, Low: 94, Close: 94.5})
	smc = computeSMC(through, atr(len(through)), 94.5)
	require.Empty(t, smc.OrderBlocks, "broken by a close below it")
}
