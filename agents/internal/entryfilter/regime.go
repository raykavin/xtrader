package entryfilter

import (
	"math"

	"github.com/raykavin/xtrader/agents/internal/ta"
	"github.com/raykavin/xtrader/model"
)

// The regime of the market, as the filter measures it on the candles handed to
// the strategy, up to the last one (never a later one):
//
//   - er: Kaufman's efficiency ratio of n candles (Perry Kaufman, "Trading
//     Systems and Methods", the ratio of his adaptive moving average),
//     |C[t]-C[t-n]| / Σ|C[i]-C[i-1]|: 1 a straight line, near 0 a market going
//     nowhere; trending at or above the threshold (0.30 by default, a
//     threshold commonly read into his ratio, ours);
//   - adx: Wilder's ADX of n (J. Welles Wilder, "New Concepts in Technical
//     Trading Systems", 1978), ta.DMI(n, n): trending at or above the threshold
//     (25 by default, the usual reading of Wilder's index);
//   - hurst: the Hurst exponent of the log returns of the last n candles by
//     rescaled range (H. E. Hurst, "Long-term storage capacity of reservoirs",
//     1951; B. Mandelbrot and J. Wallis, 1969), corrected for small samples
//     (A. A. Anis and E. H. Lloyd, "The expected value of the adjusted
//     rescaled Hurst range of independent normal summands", 1976): 0.5 plus
//     the slope of log(R/S) less the log of the R/S a random walk is expected
//     to show, on log(size), over sub-periods of 8, 16, 32... returns up to
//     n/2. The plain slope overstates H on such
//     short windows (a random walk of 100 returns gives about 0.6); corrected,
//     above 0.5 is a persistent (trending) series, 0.5 a random walk, below it
//     an anti-persistent (mean reverting) one; trending above the threshold.
//
// The volatility filter ranks the ATR of 14 of the last candle among the ATRs
// of the window of candles before it, as squeezebreakout ranks its own: its
// percentile is the share of them below it, in %.

// regimeValue returns the value of the method of c on the last candle of df,
// NaN when df is too short.
func regimeValue(c Config, df *model.Dataframe) float64 {
	n := len(df.Close)
	if n == 0 {
		return math.NaN()
	}
	switch c.RegimeMethod {
	case MethodER:
		if n <= c.RegimeLen {
			return math.NaN()
		}
		return ta.EfficiencyRatio(df.Close[n-c.RegimeLen-1:], c.RegimeLen)[c.RegimeLen]
	case MethodADX:
		_, _, adx := ta.DMI(df.High, df.Low, df.Close, c.RegimeLen, c.RegimeLen)
		return adx[n-1]
	case MethodHurst:
		if n <= c.RegimeLen {
			return math.NaN()
		}
		return hurst(df.Close[n-c.RegimeLen-1:])
	}
	return math.NaN()
}

// regimeSeries returns the value of the method of c on each candle of df, for
// the chart: NaN until df holds enough candles.
func regimeSeries(c Config, df *model.Dataframe) []float64 {
	n := len(df.Close)
	switch c.RegimeMethod {
	case MethodER:
		return ta.EfficiencyRatio(df.Close, c.RegimeLen)
	case MethodADX:
		_, _, adx := ta.DMI(df.High, df.Low, df.Close, c.RegimeLen, c.RegimeLen)
		return adx
	case MethodHurst:
		out := ta.Missing(n)
		for t := c.RegimeLen; t < n; t++ {
			out[t] = hurst(df.Close[t-c.RegimeLen : t+1])
		}
		return out
	}
	return ta.Missing(n)
}

// trending reports whether value is a trending market for c, see the methods.
func trending(c Config, value float64) bool {
	if c.RegimeMethod == MethodHurst {
		return value > c.RegimeThreshold
	}
	return value >= c.RegimeThreshold
}

// regimeWarmup returns the candles the methods of c need: er n+1; adx the 11
// lengths of the seed of the strategies (its value from the candle 2n on, its
// seed washed out over the rest); hurst n+1; the volatility the ATR of 14 and
// its window.
func regimeWarmup(c Config) int {
	warm := 0
	if c.Regime != "" {
		switch c.RegimeMethod {
		case MethodER, MethodHurst:
			warm = c.RegimeLen + 1
		case MethodADX:
			warm = 11 * c.RegimeLen
		}
	}
	if c.RegimeMaxATRPct > 0 {
		warm = max(warm, 11*atrLen, c.RegimeATRWindow+atrLen+1)
	}
	return warm
}

// atrPercentile returns the percentile of the ATR of 14 of the last candle of
// df among the ATRs of the window candles before it: the share of them below
// it, in %; NaN when df is too short.
func atrPercentile(df *model.Dataframe, window int) float64 {
	n := len(df.Close)
	atr := ta.ATR(df.High, df.Low, df.Close, atrLen)
	if n < window+1 || math.IsNaN(atr[n-1]) {
		return math.NaN()
	}
	below := 0
	for k := n - 1 - window; k < n-1; k++ {
		if math.IsNaN(atr[k]) {
			return math.NaN()
		}
		if atr[k] < atr[n-1] {
			below++
		}
	}
	return float64(below) / float64(window) * 100
}

// hurst returns the Hurst exponent of closes by rescaled range, corrected
// for small samples: their log returns split into sub-periods of 8, 16, 32...
// returns up to half of them, the most recent ones in each split (the oldest
// returns left over); in each sub-period, the range of the cumulative
// deviations from its mean over its population standard deviation (a flat
// one skipped), averaged by size; 0.5 plus the slope of log(R/S) less log of
// expectedRS on log(size), by least squares. NaN for fewer than two sizes
// with a value, or a price not above 0.
func hurst(closes []float64) float64 {
	returns := make([]float64, 0, len(closes))
	for i := 1; i < len(closes); i++ {
		if !(closes[i] > 0) || !(closes[i-1] > 0) {
			return math.NaN()
		}
		returns = append(returns, math.Log(closes[i]/closes[i-1]))
	}
	n := len(returns)
	var xs, ys []float64
	for size := 8; size <= n/2; size *= 2 {
		var sum float64
		var count int
		for end := n; end-size >= 0; end -= size {
			chunk := returns[end-size : end]
			var mean float64
			for _, r := range chunk {
				mean += r
			}
			mean /= float64(size)
			var cumulative, high, low, variance float64
			for k, r := range chunk {
				cumulative += r - mean
				if k == 0 || cumulative > high {
					high = cumulative
				}
				if k == 0 || cumulative < low {
					low = cumulative
				}
				variance += (r - mean) * (r - mean)
			}
			deviation := math.Sqrt(variance / float64(size))
			if deviation == 0 {
				continue
			}
			sum += (high - low) / deviation
			count++
		}
		if count > 0 && sum > 0 {
			xs = append(xs, math.Log(float64(size)))
			ys = append(ys, math.Log(sum/float64(count))-math.Log(expectedRS(size)))
		}
	}
	if len(xs) < 2 {
		return math.NaN()
	}
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx /= float64(len(xs))
	my /= float64(len(xs))
	var sxy, sxx float64
	for i := range xs {
		sxy += (xs[i] - mx) * (ys[i] - my)
		sxx += (xs[i] - mx) * (xs[i] - mx)
	}
	return 0.5 + sxy/sxx
}

// expectedRS returns the rescaled range a random walk of n returns is
// expected to show (Anis and Lloyd, 1976): Γ((n-1)/2) / (√π Γ(n/2)) Σ
// √((n-i)/i) over i from 1 to n-1, the ratio of the gammas by their
// logarithms, and for n above 340, where it is no longer accurate, 1/√(nπ/2)
// in place of it. Peters' (n-1/2)/n factor (1994) is left out: with it the
// random walks of the filter's sub-periods (8 to 64 returns) centre near
// 0.47, without it on 0.5 (see TestHurst_RandomWalksCentreOnHalf).
func expectedRS(n int) float64 {
	size := float64(n)
	var sum float64
	for i := 1; i < n; i++ {
		sum += math.Sqrt((size - float64(i)) / float64(i))
	}
	var gammas float64
	if n <= 340 {
		a, _ := math.Lgamma((size - 1) / 2)
		b, _ := math.Lgamma(size / 2)
		gammas = math.Exp(a-b) / math.Sqrt(math.Pi)
	} else {
		gammas = 1 / math.Sqrt(size*math.Pi/2)
	}
	return gammas * sum
}
