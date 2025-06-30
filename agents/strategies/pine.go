package strategies

import "math"

// Pine Script compatible series, used to reproduce TradingView indicators bar
// for bar. A missing value is NaN, the counterpart of Pine's na: comparisons
// involving NaN are false, as they are in Pine.
//
// The recursive averages (EMA, RMA and everything built on them: ATR, RSI, DMI
// and MACD) are seeded with the SMA of their first `length` values, so they
// are NaN until then, like the built-ins.

// na is Pine's na.
var na = math.NaN()

func isNa(v float64) bool {
	return math.IsNaN(v)
}

// nz is Pine's nz(): 0 for a missing value.
func nz(v float64) float64 {
	if isNa(v) {
		return 0
	}
	return v
}

// pineSMA is ta.sma: the mean of the last length values, NaN until the window
// is full and while it holds a NaN.
func pineSMA(src []float64, length int) []float64 {
	out := make([]float64, len(src))
	var sum float64
	var missing int
	for i, v := range src {
		if isNa(v) {
			missing++
		} else {
			sum += v
		}

		if i >= length {
			if old := src[i-length]; isNa(old) {
				missing--
			} else {
				sum -= old
			}
		}

		out[i] = na
		if i >= length-1 && missing == 0 {
			out[i] = sum / float64(length)
		}
	}
	return out
}

// pineEMA is ta.ema: alpha = 2/(length+1).
func pineEMA(src []float64, length int) []float64 {
	return smooth(src, length, 2/float64(length+1))
}

// pineRMA is ta.rma, Wilder's moving average: alpha = 1/length.
func pineRMA(src []float64, length int) []float64 {
	return smooth(src, length, 1/float64(length))
}

// smooth is an exponential average seeded with the SMA of the first length
// values. Leading NaNs are skipped, so it also runs on derived series such as
// the MACD line, and later ones leave the average unchanged.
func smooth(src []float64, length int, alpha float64) []float64 {
	out := make([]float64, len(src))
	value := na
	var sum float64
	var count int
	for i, v := range src {
		switch {
		case isNa(v):
		case !isNa(value):
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

// trueRange is ta.tr(handleNa). The first bar has no previous close: its true
// range is high-low when handleNa is set, NaN otherwise.
func trueRange(high, low, close []float64, handleNa bool) []float64 {
	out := make([]float64, len(close))
	for i := range close {
		switch {
		case i > 0:
			out[i] = math.Max(high[i]-low[i], math.Max(math.Abs(high[i]-close[i-1]), math.Abs(low[i]-close[i-1])))
		case handleNa:
			out[i] = high[i] - low[i]
		default:
			out[i] = na
		}
	}
	return out
}

// pineATR is ta.atr: the RMA of ta.tr(true).
func pineATR(high, low, close []float64, length int) []float64 {
	return pineRMA(trueRange(high, low, close, true), length)
}

// pineRSI is ta.rsi: 100 while the average loss is zero, 0 while the average
// gain is.
func pineRSI(src []float64, length int) []float64 {
	gain, loss := make([]float64, len(src)), make([]float64, len(src))
	for i := range src {
		if i == 0 {
			gain[i], loss[i] = na, na
			continue
		}
		change := src[i] - src[i-1]
		gain[i], loss[i] = math.Max(change, 0), math.Max(-change, 0)
	}

	avgGain, avgLoss := pineRMA(gain, length), pineRMA(loss, length)
	out := make([]float64, len(src))
	for i := range src {
		switch {
		case isNa(avgGain[i]) || isNa(avgLoss[i]):
			out[i] = na
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

// pineDMI is ta.dmi: +DI, -DI and ADX. A bar whose average true range is zero
// keeps the previous DI values, as fixnan does in the reference script.
func pineDMI(high, low, close []float64, diLength, adxSmoothing int) (plusDI, minusDI, adx []float64) {
	n := len(close)
	plusDM, minusDM := make([]float64, n), make([]float64, n)
	for i := range close {
		if i == 0 {
			plusDM[i], minusDM[i] = na, na
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

	tr := pineRMA(trueRange(high, low, close, false), diLength)
	plus, minus := pineRMA(plusDM, diLength), pineRMA(minusDM, diLength)

	plusDI, minusDI = make([]float64, n), make([]float64, n)
	dx := make([]float64, n)
	lastPlus, lastMinus := na, na
	for i := range close {
		if tr[i] > 0 && !isNa(plus[i]) && !isNa(minus[i]) {
			lastPlus, lastMinus = 100*plus[i]/tr[i], 100*minus[i]/tr[i]
		}
		plusDI[i], minusDI[i] = lastPlus, lastMinus

		sum := lastPlus + lastMinus
		if sum == 0 {
			sum = 1
		}
		dx[i] = math.Abs(lastPlus-lastMinus) / sum
	}

	adx = pineRMA(dx, adxSmoothing)
	for i := range adx {
		adx[i] *= 100
	}
	return plusDI, minusDI, adx
}

// pineMACD is ta.macd: the MACD line, its signal line and the histogram.
func pineMACD(src []float64, fast, slow, signal int) (macd, signalLine, hist []float64) {
	fastEMA, slowEMA := pineEMA(src, fast), pineEMA(src, slow)
	macd = make([]float64, len(src))
	for i := range src {
		macd[i] = fastEMA[i] - slowEMA[i]
	}

	signalLine = pineEMA(macd, signal)
	hist = make([]float64, len(src))
	for i := range src {
		hist[i] = macd[i] - signalLine[i]
	}
	return macd, signalLine, hist
}

// crossover is ta.crossover(a, b) on bar i.
func crossover(a, b []float64, i int) bool {
	return i > 0 && a[i] > b[i] && a[i-1] <= b[i-1]
}

// crossunder is ta.crossunder(a, b) on bar i.
func crossunder(a, b []float64, i int) bool {
	return i > 0 && a[i] < b[i] && a[i-1] >= b[i-1]
}

// pineHighest is ta.highest: the highest of the last length values, NaN until
// the window is full and while it holds a NaN.
func pineHighest(src []float64, length int) []float64 {
	return extreme(src, length, math.Max)
}

// pineLowest is ta.lowest: the lowest of the last length values, NaN until the
// window is full and while it holds a NaN.
func pineLowest(src []float64, length int) []float64 {
	return extreme(src, length, math.Min)
}

// extreme picks the extreme of the last length values with pick.
func extreme(src []float64, length int, pick func(a, b float64) float64) []float64 {
	out := make([]float64, len(src))
	for i := range src {
		out[i] = na
		if i < length-1 {
			continue
		}

		value := src[i]
		for _, v := range src[i-length+1 : i+1] {
			if isNa(v) {
				value = na
				break
			}
			value = pick(value, v)
		}
		out[i] = value
	}
	return out
}

// pineStdev is ta.stdev(biased = true): the population standard deviation of
// the last length values, NaN until the window is full and while it holds a
// NaN.
func pineStdev(src []float64, length int) []float64 {
	mean := pineSMA(src, length)
	out := make([]float64, len(src))
	for i := range src {
		out[i] = na
		if isNa(mean[i]) {
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

// pivotHigh marks the swing highs: the value of each candle strictly higher
// than the length candles on each side of it, at that candle, NaN for the
// others. As ta.pivothigh, a swing high is only known length candles later, so
// the last length candles are never one.
func pivotHigh(src []float64, length int) []float64 {
	return pivots(src, length, 1)
}

// pivotLow marks the swing lows, as pivotHigh the swing highs.
func pivotLow(src []float64, length int) []float64 {
	return pivots(src, length, -1)
}

// pivots marks the values beyond the length ones on each side of them: above
// for a sign of 1, below for -1.
func pivots(src []float64, length int, sign float64) []float64 {
	out := make([]float64, len(src))
	for i := range src {
		out[i] = na
		if i < length || i+length >= len(src) || isNa(src[i]) {
			continue
		}

		pivot := true
		for k := i - length; k <= i+length && pivot; k++ {
			pivot = k == i || sign*(src[i]-src[k]) > 0
		}
		if pivot {
			out[i] = src[i]
		}
	}
	return out
}
