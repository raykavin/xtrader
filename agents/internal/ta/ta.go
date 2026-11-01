// Package ta holds the series of the technical analysis shared by the entry
// filters (agents/internal/entryfilter) and the features of the meta-labeling
// (agents/metalabel), following the conventions of Pine Script, as the helpers
// of the strategies do (agents/strategies/pine.go, unexported there): a missing
// value is NaN, the counterpart of Pine's na, comparisons involving NaN are
// false, and the recursive averages (EMA, RMA and everything built on them:
// ATR, RSI, DMI) are seeded with the SMA of their first length values, so they
// are NaN until then, like the built-ins. Every function reads its inputs
// only, never past their last value.
package ta

import "math"

// NaN is a missing value, Pine's na.
var NaN = math.NaN()

// Missing returns n missing values.
func Missing(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = NaN
	}
	return out
}

// SMA is ta.sma: the mean of the last length values, NaN until the window is
// full and while it holds a NaN.
func SMA(src []float64, length int) []float64 {
	out := make([]float64, len(src))
	var sum float64
	var missing int
	for i, v := range src {
		if math.IsNaN(v) {
			missing++
		} else {
			sum += v
		}
		if i >= length {
			if old := src[i-length]; math.IsNaN(old) {
				missing--
			} else {
				sum -= old
			}
		}
		out[i] = NaN
		if length > 0 && i >= length-1 && missing == 0 {
			out[i] = sum / float64(length)
		}
	}
	return out
}

// EMA is ta.ema: alpha = 2/(length+1).
func EMA(src []float64, length int) []float64 {
	return smooth(src, length, 2/float64(length+1))
}

// RMA is ta.rma, Wilder's moving average: alpha = 1/length.
func RMA(src []float64, length int) []float64 {
	return smooth(src, length, 1/float64(length))
}

// smooth is an exponential average seeded with the SMA of the first length
// values. Leading NaNs are skipped, so it also runs on derived series, and
// later ones leave the average unchanged.
func smooth(src []float64, length int, alpha float64) []float64 {
	out := make([]float64, len(src))
	value := NaN
	var sum float64
	var count int
	for i, v := range src {
		switch {
		case math.IsNaN(v) || length <= 0:
		case !math.IsNaN(value):
			value = alpha*v + (1-alpha)*value
		default:
			sum += v
			count++
			if count == length {
				value = sum / float64(length)
			}
		}
		out[i] = value
	}
	return out
}

// TrueRange is ta.tr(handleNa). The first bar has no previous close: its true
// range is high-low when handleNa is set, NaN otherwise.
func TrueRange(high, low, close []float64, handleNa bool) []float64 {
	out := make([]float64, len(close))
	for i := range close {
		switch {
		case i > 0:
			out[i] = math.Max(high[i]-low[i], math.Max(math.Abs(high[i]-close[i-1]), math.Abs(low[i]-close[i-1])))
		case handleNa:
			out[i] = high[i] - low[i]
		default:
			out[i] = NaN
		}
	}
	return out
}

// ATR is ta.atr: the RMA of ta.tr(true) (Wilder, "New Concepts in Technical
// Trading Systems", 1978).
func ATR(high, low, close []float64, length int) []float64 {
	return RMA(TrueRange(high, low, close, true), length)
}

// RSI is ta.rsi (Wilder 1978): 100 while the average loss is zero, 0 while the
// average gain is.
func RSI(src []float64, length int) []float64 {
	gain, loss := make([]float64, len(src)), make([]float64, len(src))
	for i := range src {
		if i == 0 {
			gain[i], loss[i] = NaN, NaN
			continue
		}
		change := src[i] - src[i-1]
		gain[i], loss[i] = math.Max(change, 0), math.Max(-change, 0)
	}
	avgGain, avgLoss := RMA(gain, length), RMA(loss, length)
	out := make([]float64, len(src))
	for i := range src {
		switch {
		case math.IsNaN(avgGain[i]) || math.IsNaN(avgLoss[i]):
			out[i] = NaN
		case avgLoss[i] == 0:
			out[i] = 100
		case avgGain[i] == 0:
			out[i] = 0
		default:
			out[i] = 100 - 100/(1+avgGain[i]/avgLoss[i])
		}
	}
	return out
}

// DMI is ta.dmi (Wilder 1978): +DI, -DI and the ADX. A bar whose average true
// range is zero keeps the previous DI values, as fixnan does in the reference
// script. The ADX of length n is known from the bar 2n-1 on.
func DMI(high, low, close []float64, diLength, adxSmoothing int) (plusDI, minusDI, adx []float64) {
	n := len(close)
	plusDM, minusDM := make([]float64, n), make([]float64, n)
	for i := range close {
		if i == 0 {
			plusDM[i], minusDM[i] = NaN, NaN
			continue
		}
		up, down := high[i]-high[i-1], low[i-1]-low[i]
		if up > down && up > 0 {
			plusDM[i] = up
		}
		if down > up && down > 0 {
			minusDM[i] = down
		}
	}
	tr := RMA(TrueRange(high, low, close, false), diLength)
	plus, minus := RMA(plusDM, diLength), RMA(minusDM, diLength)

	plusDI, minusDI = make([]float64, n), make([]float64, n)
	dx := make([]float64, n)
	lastPlus, lastMinus := NaN, NaN
	for i := range close {
		if tr[i] > 0 && !math.IsNaN(plus[i]) && !math.IsNaN(minus[i]) {
			lastPlus, lastMinus = 100*plus[i]/tr[i], 100*minus[i]/tr[i]
		}
		plusDI[i], minusDI[i] = lastPlus, lastMinus
		sum := lastPlus + lastMinus
		if sum == 0 {
			sum = 1
		}
		dx[i] = math.Abs(lastPlus-lastMinus) / sum
	}
	adx = RMA(dx, adxSmoothing)
	for i := range adx {
		adx[i] *= 100
	}
	return plusDI, minusDI, adx
}

// Stdev is ta.stdev(biased = true): the population standard deviation of the
// last length values, NaN until the window is full and while it holds a NaN.
func Stdev(src []float64, length int) []float64 {
	mean := SMA(src, length)
	out := make([]float64, len(src))
	for i := range src {
		out[i] = NaN
		if math.IsNaN(mean[i]) {
			continue
		}
		var sum float64
		for _, v := range src[i-length+1 : i+1] {
			sum += (v - mean[i]) * (v - mean[i])
		}
		out[i] = math.Sqrt(sum / float64(length))
	}
	return out
}

// EfficiencyRatio is Kaufman's efficiency ratio of length n (Perry Kaufman,
// "Trading Systems and Methods", the ratio of the adaptive moving average):
// the net move of the close over n bars over the sum of the absolute moves of
// each bar, |C[t]-C[t-n]| / Σ|C[i]-C[i-1]| for i from t-n+1 to t, from 0 (all
// noise) to 1 (a straight line); 0 for a close that didn't move at all, NaN
// for the first n bars.
func EfficiencyRatio(close []float64, n int) []float64 {
	out := Missing(len(close))
	if n <= 0 {
		return out
	}
	for t := n; t < len(close); t++ {
		var path float64
		for i := t - n + 1; i <= t; i++ {
			path += math.Abs(close[i] - close[i-1])
		}
		switch net := math.Abs(close[t] - close[t-n]); {
		case math.IsNaN(path) || math.IsNaN(net):
		case path == 0:
			out[t] = 0
		default:
			out[t] = net / path
		}
	}
	return out
}
