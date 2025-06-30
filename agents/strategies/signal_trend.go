package strategies

import (
	"fmt"
	"math"
	"time"

	"github.com/raykavin/xtrader/indicator"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/strategy"
)

// The trend following rules of the signal strategies, see signals.

// emaCross is EMA Crossover: a BUY on the EMA 9 crossing over the EMA 21.
type emaCross struct{}

func (emaCross) warmup() int {
	return seedWarmup(21) + 1
}

func (emaCross) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	return emas(df, "EMA Crossover", 9, 21)
}

func (emaCross) signal(df *model.Dataframe) model.SideType {
	fast, slow := df.Metadata["sg_fast"], df.Metadata["sg_slow"]
	i := len(df.Close) - 1
	if !complete(df, 2, fast, slow) {
		return ""
	}
	for _, side := range sides {
		if crossed(fast, slow, i, rkSign(side)) {
			return side
		}
	}
	return ""
}

// emaTrend is EMA 200 Trend Following: a BUY on the close crossing over the EMA
// 200 while it rises.
type emaTrend struct{}

func (emaTrend) warmup() int {
	return seedWarmup(200) + 1
}

func (emaTrend) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	return overlay(df, "EMA 200 Trend", 200, trendEMA(df, 200))
}

func (emaTrend) signal(df *model.Dataframe) model.SideType {
	trend := df.Metadata["sg_trend"]
	i := len(df.Close) - 1
	if !complete(df, 2, trend) {
		return ""
	}
	for _, side := range sides {
		sign := rkSign(side)
		if crossed(df.Close, trend, i, sign) && sign*(trend[i]-trend[i-1]) > 0 {
			return side
		}
	}
	return ""
}

// ribbonLens are the periods of the EMAs of ribbon, the fastest first.
var ribbonLens = []int{8, 13, 21, 34, 55}

// ribbon is Moving Average Ribbon: a BUY on the candle the EMAs of ribbonLens
// line up in order, each above the slower ones.
type ribbon struct{}

func (ribbon) warmup() int {
	return seedWarmup(ribbonLens[len(ribbonLens)-1]) + 1
}

func (ribbon) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	colors := []string{colorUp, colorLine, colorOther, colorSlow, colorDown}
	metrics := make([]strategy.IndicatorMetric, len(ribbonLens))
	for k, length := range ribbonLens {
		key := fmt.Sprintf("sg_ribbon%d", k)
		df.Metadata[key] = pineEMA(df.Close, length)
		metrics[k] = line(df.Metadata[key], fmt.Sprintf("EMA %d", length), colors[k])
	}
	return overlay(df, "Moving Average Ribbon", ribbonLens[len(ribbonLens)-1], metrics...)
}

func (ribbon) signal(df *model.Dataframe) model.SideType {
	lines := make([][]float64, len(ribbonLens))
	for k := range ribbonLens {
		lines[k] = df.Metadata[fmt.Sprintf("sg_ribbon%d", k)]
	}
	i := len(df.Close) - 1
	if !complete(df, 2, lines...) {
		return ""
	}
	// lined up on candle j the way of sign, a missing value failing it
	aligned := func(j int, sign float64) bool {
		for k := 0; k+1 < len(lines); k++ {
			if !(sign*(lines[k][j]-lines[k+1][j]) > 0) {
				return false
			}
		}
		return true
	}
	for _, side := range sides {
		sign := rkSign(side)
		if aligned(i, sign) && !aligned(i-1, sign) {
			return side
		}
	}
	return ""
}

// superTrend is Supertrend: a BUY on the Supertrend of an ATR of 10 candles and
// a factor of 3 flipping below the close; with trendLen, the close above the
// EMA of that period too, EMA + Supertrend.
type superTrend struct {
	trendLen int // 0 without the EMA
}

func (r superTrend) warmup() int {
	return seedWarmup(max(10, r.trendLen)) + 1
}

func (r superTrend) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["sg_supertrend"] = guard(df, 10+2, func() []float64 {
		return indicator.SuperTrend(df.High, df.Low, df.Close, 10, 3)
	})
	metrics := []strategy.IndicatorMetric{line(df.Metadata["sg_supertrend"], "Supertrend 10/3", colorLine)}
	group := "Supertrend"
	if r.trendLen > 0 {
		metrics = append(metrics, trendEMA(df, r.trendLen))
		group = "EMA + Supertrend"
	}
	return overlay(df, group, max(10, r.trendLen), metrics...)
}

func (r superTrend) signal(df *model.Dataframe) model.SideType {
	st := df.Metadata["sg_supertrend"]
	i := len(df.Close) - 1
	if !complete(df, 2, st) {
		return ""
	}
	trend := df.Metadata["sg_trend"]
	if r.trendLen > 0 && len(trend) != len(df.Close) {
		return ""
	}
	for _, side := range sides {
		sign := rkSign(side)
		if flipped(df.Close, st, i, sign) && (r.trendLen == 0 || sign*(df.Close[i]-trend[i]) > 0) {
			return side
		}
	}
	return ""
}

// sarTrend is Parabolic SAR + EMA: a BUY on the Parabolic SAR of an acceleration
// of 0.02 up to 0.2 flipping below the close, the close above the EMA 50.
type sarTrend struct{}

func (sarTrend) warmup() int {
	return seedWarmup(50) + 1
}

func (sarTrend) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["sg_sar"] = guard(df, 3, func() []float64 { return indicator.SAR(df.High, df.Low, 0.02, 0.2) })
	sar := strategy.IndicatorMetric{Values: df.Metadata["sg_sar"], Name: "SAR 0.02/0.2", Color: colorLine,
		Style: strategy.StyleScatter}
	return overlay(df, "Parabolic SAR + EMA", 50, sar, trendEMA(df, 50))
}

func (sarTrend) signal(df *model.Dataframe) model.SideType {
	sar, trend := df.Metadata["sg_sar"], df.Metadata["sg_trend"]
	i := len(df.Close) - 1
	if !complete(df, 2, sar, trend) {
		return ""
	}
	for _, side := range sides {
		sign := rkSign(side)
		if flipped(df.Close, sar, i, sign) && sign*(df.Close[i]-trend[i]) > 0 {
			return side
		}
	}
	return ""
}

// chandelierTrend is ATR Trailing Trend: a BUY on the Chandelier Exit of 22
// candles and 3 ATR flipping up, the close crossing above the stop of the
// shorts, see chandelier.
type chandelierTrend struct{}

func (chandelierTrend) warmup() int {
	return seedWarmup(22) + 1
}

func (chandelierTrend) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["sg_chandelier"], df.Metadata["sg_chandelier_stop"] = chandelier(df.High, df.Low, df.Close, 22, 3)
	return overlay(df, "ATR Trailing Trend", 22,
		line(df.Metadata["sg_chandelier_stop"], "Chandelier 22/3", colorLine))
}

func (chandelierTrend) signal(df *model.Dataframe) model.SideType {
	side := df.Metadata["sg_chandelier"]
	i := len(df.Close) - 1
	if !complete(df, 2, side) {
		return ""
	}
	for _, s := range sides {
		if sign := rkSign(s); side[i] == sign && side[i-1] == -sign {
			return s
		}
	}
	return ""
}

// ichimokuTrend is Ichimoku Trend: a BUY on the Tenkan (9) crossing over the
// Kijun (26) with the close above the cloud (Senkou B of 52, drawn 26 candles
// ahead); with adx, the ADX of 14 candles above it too, Ichimoku + ADX.
type ichimokuTrend struct {
	adx float64 // 0 without the ADX
}

func (r ichimokuTrend) warmup() int {
	bars := 52 + 26 - 1 + 1 // the cloud of the last two candles
	if r.adx > 0 {
		bars = max(bars, adxWarmup+1)
	}
	return bars
}

func (r ichimokuTrend) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	tenkan, kijun, spanA, spanB := ichimoku(df.High, df.Low, 9, 26, 52, 26)
	df.Metadata["sg_tenkan"], df.Metadata["sg_kijun"] = tenkan, kijun
	df.Metadata["sg_span_a"], df.Metadata["sg_span_b"] = spanA, spanB
	chart := overlay(df, "Ichimoku", 52+26-1, line(tenkan, "Tenkan 9", colorSlow), line(kijun, "Kijun 26", colorDown),
		line(spanA, "Senkou A", colorUp), line(spanB, "Senkou B 52", colorLine))
	if r.adx > 0 {
		chart = append(chart, adx(df))
	}
	return chart
}

func (r ichimokuTrend) signal(df *model.Dataframe) model.SideType {
	tenkan, kijun := df.Metadata["sg_tenkan"], df.Metadata["sg_kijun"]
	spanA, spanB := df.Metadata["sg_span_a"], df.Metadata["sg_span_b"]
	i := len(df.Close) - 1
	if !complete(df, 2, tenkan, kijun, spanA, spanB) {
		return ""
	}
	strength := df.Metadata["sg_adx"]
	if r.adx > 0 && len(strength) != len(df.Close) {
		return ""
	}
	for _, side := range sides {
		sign := rkSign(side)
		// beyond the cloud: above its top for a BUY, below its bottom for a
		// SELL, a missing span failing it
		beyond := sign*df.Close[i] > math.Max(sign*spanA[i], sign*spanB[i])
		if crossed(tenkan, kijun, i, sign) && beyond && (r.adx == 0 || strength[i] > r.adx) {
			return side
		}
	}
	return ""
}

// mtfTrend is Multi-Timeframe Trend: a BUY on the EMA 20 crossing over the EMA
// 50 while the EMA 50 of the timeframe 4 times the one of the candles rises,
// its last two closed bars, see htfEMA.
type mtfTrend struct {
	htf time.Duration
}

func newMTFTrend(timeframe time.Duration) signalRule {
	return mtfTrend{htf: 4 * timeframe}
}

func (mtfTrend) warmup() int {
	return htfWarmup(50, 4)
}

func (r mtfTrend) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	chart := emas(df, "Multi-Timeframe Trend", 20, 50)
	df.Metadata["sg_htf"], df.Metadata["sg_htf_previous"] = htfEMA(df.Time, df.Close, r.htf, 50)
	chart[0].Metrics = append(chart[0].Metrics, line(df.Metadata["sg_htf"], "EMA 50 x4", colorSlow))
	chart[0].Warmup = (50 + 2) * 4
	return chart
}

func (mtfTrend) signal(df *model.Dataframe) model.SideType {
	fast, slow := df.Metadata["sg_fast"], df.Metadata["sg_slow"]
	htf, previous := df.Metadata["sg_htf"], df.Metadata["sg_htf_previous"]
	i := len(df.Close) - 1
	if !complete(df, 2, fast, slow, htf, previous) {
		return ""
	}
	for _, side := range sides {
		sign := rkSign(side)
		if crossed(fast, slow, i, sign) && sign*(htf[i]-previous[i]) > 0 {
			return side
		}
	}
	return ""
}

// mtfAlignment is Multi-Timeframe EMA Alignment: a BUY on the EMA 20 crossing
// over the EMA 50 while the EMA 20 is above the EMA 50 on the timeframes 4 and
// 16 times the one of the candles too (15m, 1h and 4h), their last closed bars,
// see htfEMA.
type mtfAlignment struct {
	htfs [2]time.Duration
}

func newMTFAlignment(timeframe time.Duration) signalRule {
	return mtfAlignment{htfs: [2]time.Duration{4 * timeframe, 16 * timeframe}}
}

func (mtfAlignment) warmup() int {
	return htfWarmup(50, 16)
}

func (r mtfAlignment) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	chart := emas(df, "Multi-Timeframe EMA Alignment", 20, 50)
	for k, htf := range r.htfs {
		fast, _ := htfEMA(df.Time, df.Close, htf, 20)
		slow, _ := htfEMA(df.Time, df.Close, htf, 50)
		df.Metadata[fmt.Sprintf("sg_htf%d_fast", k)], df.Metadata[fmt.Sprintf("sg_htf%d_slow", k)] = fast, slow
	}
	return chart
}

func (mtfAlignment) signal(df *model.Dataframe) model.SideType {
	fast, slow := df.Metadata["sg_fast"], df.Metadata["sg_slow"]
	htfFast := [2][]float64{df.Metadata["sg_htf0_fast"], df.Metadata["sg_htf1_fast"]}
	htfSlow := [2][]float64{df.Metadata["sg_htf0_slow"], df.Metadata["sg_htf1_slow"]}
	i := len(df.Close) - 1
	if !complete(df, 2, fast, slow, htfFast[0], htfSlow[0], htfFast[1], htfSlow[1]) {
		return ""
	}
	for _, side := range sides {
		sign := rkSign(side)
		aligned := crossed(fast, slow, i, sign)
		for k := range htfFast {
			aligned = aligned && sign*(htfFast[k][i]-htfSlow[k][i]) > 0
		}
		if aligned {
			return side
		}
	}
	return ""
}
