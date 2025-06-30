package strategies

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/indicator"
)

// requireSeries compares two series value by value, NaN only matches NaN.
func requireSeries(t *testing.T, want, got []float64, tolerance float64) {
	t.Helper()
	require.Len(t, got, len(want))
	for i := range want {
		if isNa(want[i]) {
			require.Truef(t, isNa(got[i]), "index %d: want NaN, got %v", i, got[i])
			continue
		}
		require.InDeltaf(t, want[i], got[i], tolerance, "index %d", i)
	}
}

// requireConverged compares got with a TA-Lib reference from index from on,
// with a tolerance relative to the reference.
func requireConverged(t *testing.T, want, got []float64, from int, tolerance float64) {
	t.Helper()
	require.Len(t, got, len(want))
	for i := from; i < len(want); i++ {
		require.InDeltaf(t, want[i], got[i], tolerance*math.Max(1, math.Abs(want[i])), "index %d", i)
	}
}

func TestPineSMA(t *testing.T) {
	got := pineSMA([]float64{1, 2, 3, 4, na, 6, 7, 8}, 3)
	requireSeries(t, []float64{na, na, 2, 3, na, na, na, 7}, got, 1e-12)
}

func TestPineEMA_SeededWithSMA(t *testing.T) {
	// the seed is the SMA of the first 3 values, then alpha = 2/(3+1)
	got := pineEMA([]float64{na, 1, 2, 3, 4}, 3)
	requireSeries(t, []float64{na, na, na, 2, 3}, got, 1e-12)
}

func TestPineRMA_SeededWithSMA(t *testing.T) {
	// the seed is the SMA of the first 2 values, then alpha = 1/2
	got := pineRMA([]float64{2, 4, 6, 8}, 2)
	requireSeries(t, []float64{na, 3, 4.5, 6.25}, got, 1e-12)
}

func TestTrueRange(t *testing.T) {
	high := []float64{10, 12, 11}
	low := []float64{8, 11, 7}
	closes := []float64{9, 11.5, 8}

	// the gaps to the previous close count: |12-9| and |7-11.5|
	requireSeries(t, []float64{2, 3, 4.5}, trueRange(high, low, closes, true), 1e-12)
	requireSeries(t, []float64{na, 3, 4.5}, trueRange(high, low, closes, false), 1e-12)
}

func TestPineRSI_Extremes(t *testing.T) {
	requireSeries(t, []float64{na, na, 100, 100}, pineRSI([]float64{1, 2, 3, 4}, 2), 1e-12)
	requireSeries(t, []float64{na, na, 0, 0}, pineRSI([]float64{4, 3, 2, 1}, 2), 1e-12)
}

func TestCrossoverCrossunder(t *testing.T) {
	a := []float64{1, 2, 2, 3, 1}
	b := []float64{2, 2, 2, 2, 2}

	require.False(t, crossover(a, b, 0))
	require.False(t, crossover(a, b, 1), "touching is not crossing")
	require.False(t, crossover(a, b, 2))
	require.True(t, crossover(a, b, 3), "from touching to above is a cross")
	require.True(t, crossunder(a, b, 4))
	require.False(t, crossunder(a, b, 3))
	require.False(t, crossover([]float64{na, 3}, []float64{2, 2}, 1), "a missing value never crosses")
}

// The TA-Lib implementations wrapped by the indicator package are an
// independent reference: EMA and RSI share the Pine seed and match exactly,
// ATR, DMI and MACD are seeded differently and converge after the warmup.
func TestPineSeries_MatchTALib(t *testing.T) {
	df := btcDataframe(t)
	high, low, closes := df.High, df.Low, df.Close

	t.Run("EMA", func(t *testing.T) {
		got := pineEMA(closes, 21)
		require.True(t, isNa(got[19]))
		requireConverged(t, indicator.EMA(closes, 21), got, 20, 1e-9)
	})

	t.Run("RSI", func(t *testing.T) {
		got := pineRSI(closes, 14)
		require.True(t, isNa(got[13]))
		requireConverged(t, indicator.RSI(closes, 14), got, 14, 1e-9)
	})

	t.Run("ATR", func(t *testing.T) {
		got := pineATR(high, low, closes, 14)
		require.True(t, isNa(got[12]))
		require.False(t, isNa(got[13]), "ta.atr counts the first bar's high-low")
		requireConverged(t, indicator.ATR(high, low, closes, 14), got, 300, 1e-6)
	})

	t.Run("DMI", func(t *testing.T) {
		plusDI, minusDI, adx := pineDMI(high, low, closes, 14, 14)
		require.True(t, isNa(adx[26]))
		require.False(t, isNa(adx[27]))
		requireConverged(t, indicator.PlusDI(high, low, closes, 14), plusDI, 400, 1e-6)
		requireConverged(t, indicator.MinusDI(high, low, closes, 14), minusDI, 400, 1e-6)
		requireConverged(t, indicator.ADX(high, low, closes, 14), adx, 400, 1e-6)
	})

	t.Run("MACD", func(t *testing.T) {
		macd, signal, hist := pineMACD(closes, 12, 26, 9)
		wantMACD, wantSignal, wantHist := indicator.MACD(closes, 12, 26, 9)
		requireConverged(t, wantMACD, macd, 300, 1e-6)
		requireConverged(t, wantSignal, signal, 300, 1e-6)
		requireConverged(t, wantHist, hist, 300, 1e-6)
	})
}

func TestPineHighestLowest(t *testing.T) {
	src := []float64{3, 1, 4, 1, 5, na, 2, 6, 5}
	requireSeries(t, []float64{na, na, 4, 4, 5, na, na, na, 6}, pineHighest(src, 3), 1e-12)
	requireSeries(t, []float64{na, na, 1, 1, 1, na, na, na, 2}, pineLowest(src, 3), 1e-12)
	requireSeries(t, src, pineHighest(src, 1), 1e-12)
}

func TestPineStdev(t *testing.T) {
	// the population deviation: of 2, 4, 4, 4, 5, 5, 7, 9, it is 2
	src := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	got := pineStdev(src, 8)
	requireSeries(t, []float64{na, na, na, na, na, na, na, 2}, got, 1e-12)

	// and TA-Lib's, once its window is full
	closes := make([]float64, 200)
	for i := range closes {
		closes[i] = 100 + 10*math.Sin(float64(i)/7) + float64(i%5)
	}
	requireConverged(t, indicator.StdDev(closes, 20, 1), pineStdev(closes, 20), 19, 1e-9)
}

func TestPivots(t *testing.T) {
	src := []float64{1, 3, 2, 2, 5, 4, 4, 1, 0, 1}
	// strictly beyond the neighbors: the flat 4, 4 is no swing high, and the
	// last candle can't be one yet
	requireSeries(t, []float64{na, 3, na, na, 5, na, na, na, na, na}, pivotHigh(src, 1), 1e-12)
	requireSeries(t, []float64{na, na, na, na, na, na, na, na, 0, na}, pivotLow(src, 1), 1e-12)
	requireSeries(t, []float64{na, na, na, na, 5, na, na, na, na, na}, pivotHigh(src, 2), 1e-12)
}
