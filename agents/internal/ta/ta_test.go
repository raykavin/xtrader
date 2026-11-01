package ta

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSMAAndEMA_SeededAsPine(t *testing.T) {
	src := []float64{1, 2, 3, 4, 5}
	sma := SMA(src, 3)
	assert.True(t, math.IsNaN(sma[0]) && math.IsNaN(sma[1]))
	assert.Equal(t, []float64{2, 3, 4}, sma[2:])

	ema := EMA(src, 3) // seeded with the SMA of the first 3, then alpha 0.5
	assert.True(t, math.IsNaN(ema[1]))
	assert.InDelta(t, 2, ema[2], 1e-12)
	assert.InDelta(t, 3, ema[3], 1e-12)
	assert.InDelta(t, 4, ema[4], 1e-12)

	assert.True(t, math.IsNaN(SMA([]float64{1, math.NaN(), 3}, 2)[2]), "a NaN in the window")
}

func TestEfficiencyRatio(t *testing.T) {
	line := []float64{1, 2, 3, 4, 5, 6}
	er := EfficiencyRatio(line, 3)
	assert.True(t, math.IsNaN(er[2]), "the first n bars")
	assert.InDelta(t, 1, er[5], 1e-12, "a straight line")

	zigzag := []float64{1, 2, 1, 2, 1, 2, 1}
	assert.InDelta(t, 0, EfficiencyRatio(zigzag, 4)[6], 1e-12, "back where it was: all noise")
	assert.InDelta(t, 1.0/3, EfficiencyRatio(zigzag, 3)[3], 1e-12, "1 net over 3 of path")
	assert.Equal(t, 0.0, EfficiencyRatio([]float64{5, 5, 5, 5}, 2)[3], "a flat close: no efficiency")
}

func TestATRAndDMI(t *testing.T) {
	n := 60
	high, low, closes := make([]float64, n), make([]float64, n), make([]float64, n)
	for i := range closes {
		closes[i] = 100 + float64(i) // a steady rise
		high[i], low[i] = closes[i]+0.5, closes[i]-0.5
	}
	atr := ATR(high, low, closes, 14)
	assert.True(t, math.IsNaN(atr[12]))
	assert.InDelta(t, 1.5, atr[n-1], 0.01, "the true range of each bar: 1.5, the seed washed out")

	plus, minus, adx := DMI(high, low, closes, 14, 14)
	assert.True(t, math.IsNaN(adx[26]), "the ADX of 14 from the bar 27")
	require.False(t, math.IsNaN(adx[27]))
	assert.Greater(t, plus[n-1], minus[n-1])
	assert.InDelta(t, 100, adx[n-1], 1e-9, "only up moves: an ADX of 100")
}

func TestRSIAndStdev(t *testing.T) {
	up := []float64{1, 2, 3, 4, 5, 6}
	assert.Equal(t, 100.0, RSI(up, 3)[5], "no loss")
	assert.InDelta(t, math.Sqrt(2.0/3), Stdev([]float64{1, 2, 3}, 3)[2], 1e-12, "the population one")
}
