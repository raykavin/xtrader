package strategies

import (
	"github.com/raykavin/xtrader/indicator"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/strategy"
)

// The momentum rules of the signal strategies, see signals.

// adxTrend is ADX + EMA: a BUY on the candle the close is above the EMA 50 with
// the ADX of 14 candles above 25 and the +DI above the -DI, all of them.
type adxTrend struct{}

func (adxTrend) warmup() int {
	return max(seedWarmup(50), adxWarmup) + 1
}

func (adxTrend) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	return append(overlay(df, "ADX + EMA", 50, trendEMA(df, 50)), adx(df))
}

func (adxTrend) signal(df *model.Dataframe) model.SideType {
	trend, plus, minus, strength := df.Metadata["sg_trend"], df.Metadata["sg_plus"], df.Metadata["sg_minus"],
		df.Metadata["sg_adx"]
	i := len(df.Close) - 1
	if !complete(df, 2, trend, plus, minus, strength) {
		return ""
	}
	// the trend on candle j the way of sign, a missing value failing it
	trending := func(j int, sign float64) bool {
		return sign*(df.Close[j]-trend[j]) > 0 && strength[j] > 25 && sign*(plus[j]-minus[j]) > 0
	}
	for _, side := range sides {
		sign := rkSign(side)
		if trending(i, sign) && !trending(i-1, sign) {
			return side
		}
	}
	return ""
}

// macdTrend is MACD Trend Following: a BUY on the MACD of 12 and 26 closes
// crossing over its signal line of 9, both above zero.
type macdTrend struct{}

func (macdTrend) warmup() int {
	return seedWarmup(26+9) + 1
}

func (macdTrend) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	return []strategy.ChartIndicator{macd(df)}
}

func (macdTrend) signal(df *model.Dataframe) model.SideType {
	value, signalLine := df.Metadata["sg_macd"], df.Metadata["sg_macd_signal"]
	i := len(df.Close) - 1
	if !complete(df, 2, value, signalLine) {
		return ""
	}
	for _, side := range sides {
		sign := rkSign(side)
		if crossed(value, signalLine, i, sign) && sign*value[i] > 0 && sign*signalLine[i] > 0 {
			return side
		}
	}
	return ""
}

// macd adds the MACD of 12 and 26 closes and its signal line of 9 to the
// dataframe, sg_macd, sg_macd_signal and sg_macd_hist, and returns its pane.
func macd(df *model.Dataframe) strategy.ChartIndicator {
	value, signalLine, hist := pineMACD(df.Close, 12, 26, 9)
	df.Metadata["sg_macd"], df.Metadata["sg_macd_signal"], df.Metadata["sg_macd_hist"] = value, signalLine, hist
	return pane(df, "MACD 12/26/9", 26+9, line(value, "MACD", colorSlow), line(signalLine, "Signal", colorLine),
		strategy.IndicatorMetric{Values: hist, Name: "Histogram", Color: colorOther, Style: strategy.StyleHistogram})
}

// emaMACD is EMA + MACD: a BUY on the EMA 9 crossing over the EMA 21 with the
// histogram of the MACD 12/26/9 above zero.
type emaMACD struct{}

func (emaMACD) warmup() int {
	return seedWarmup(26+9) + 1
}

func (emaMACD) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	return append(emas(df, "EMA + MACD", 9, 21), macd(df))
}

func (emaMACD) signal(df *model.Dataframe) model.SideType {
	fast, slow, hist := df.Metadata["sg_fast"], df.Metadata["sg_slow"], df.Metadata["sg_macd_hist"]
	i := len(df.Close) - 1
	if !complete(df, 2, fast, slow, hist) {
		return ""
	}
	for _, side := range sides {
		sign := rkSign(side)
		if crossed(fast, slow, i, sign) && sign*hist[i] > 0 {
			return side
		}
	}
	return ""
}

// emaRSIADX is EMA + RSI + ADX: a BUY on the EMA 9 crossing over the EMA 21 with
// the RSI of 14 closes above 50 and the ADX of 14 candles above 20.
type emaRSIADX struct{}

func (emaRSIADX) warmup() int {
	return adxWarmup + 1
}

func (emaRSIADX) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	return append(emas(df, "EMA + RSI + ADX", 9, 21), rsi(df), adx(df))
}

func (emaRSIADX) signal(df *model.Dataframe) model.SideType {
	fast, slow, momentum, strength := df.Metadata["sg_fast"], df.Metadata["sg_slow"], df.Metadata["sg_rsi"],
		df.Metadata["sg_adx"]
	i := len(df.Close) - 1
	if !complete(df, 2, fast, slow, momentum, strength) {
		return ""
	}
	for _, side := range sides {
		sign := rkSign(side)
		if crossed(fast, slow, i, sign) && sign*(momentum[i]-50) > 0 && strength[i] > 20 {
			return side
		}
	}
	return ""
}

// rsi adds the RSI of 14 closes to the dataframe, sg_rsi, and returns its pane.
func rsi(df *model.Dataframe) strategy.ChartIndicator {
	df.Metadata["sg_rsi"] = pineRSI(df.Close, 14)
	return pane(df, "RSI 14", 14, line(df.Metadata["sg_rsi"], "RSI", colorOther))
}

// rsiTrend is RSI Trend Continuation: a BUY on the RSI of 14 closes crossing
// back over 50 with the close above the EMA 50, the end of a dip in an uptrend.
type rsiTrend struct{}

func (rsiTrend) warmup() int {
	return seedWarmup(50) + 1
}

func (rsiTrend) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	return append(overlay(df, "RSI Trend Continuation", 50, trendEMA(df, 50)), rsi(df))
}

func (rsiTrend) signal(df *model.Dataframe) model.SideType {
	trend, momentum := df.Metadata["sg_trend"], df.Metadata["sg_rsi"]
	i := len(df.Close) - 1
	if !complete(df, 2, trend, momentum) {
		return ""
	}
	for _, side := range sides {
		sign := rkSign(side)
		if sign*(df.Close[i]-trend[i]) > 0 && crossedLevel(momentum, 50, i, sign) {
			return side
		}
	}
	return ""
}

// stochPullback is Stochastic Trend Pullback: a BUY on the %K of the Slow
// Stochastic 14/3/3 crossing over its %D under 20 with the close above the EMA
// 50; a SELL on the %K crossing under the %D above 80 below it.
type stochPullback struct{}

func (stochPullback) warmup() int {
	return seedWarmup(50) + 1
}

func (stochPullback) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	// the ones of talib fail on fewer candles than they need, see guard
	k, d := missing(len(df.Close)), missing(len(df.Close))
	if len(df.Close) >= 14+3+3 {
		k, d = indicator.Stoch(df.High, df.Low, df.Close, 14, 3, indicator.TypeSMA, 3, indicator.TypeSMA)
	}
	df.Metadata["sg_stoch_k"], df.Metadata["sg_stoch_d"] = k, d
	return append(overlay(df, "Stochastic Trend Pullback", 50, trendEMA(df, 50)),
		pane(df, "Stochastic 14/3/3", 14+3+3, line(k, "%K", colorSlow), line(d, "%D", colorLine)))
}

func (stochPullback) signal(df *model.Dataframe) model.SideType {
	trend, k, d := df.Metadata["sg_trend"], df.Metadata["sg_stoch_k"], df.Metadata["sg_stoch_d"]
	i := len(df.Close) - 1
	if !complete(df, 2, trend, k, d) {
		return ""
	}
	for _, side := range sides {
		sign := rkSign(side)
		zone := 50 - sign*30 // 20 for a BUY, 80 for a SELL
		if sign*(df.Close[i]-trend[i]) > 0 && crossed(k, d, i, sign) && sign*(k[i]-zone) < 0 {
			return side
		}
	}
	return ""
}

// priceActionPullback is Price Action Pullback: a BUY on a candle closing above
// the high of the previous one after 2 candles down at least, the close above
// a rising EMA 50.
type priceActionPullback struct{}

func (priceActionPullback) warmup() int {
	return seedWarmup(50) + 2
}

func (priceActionPullback) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	return overlay(df, "Price Action Pullback", 50, trendEMA(df, 50))
}

func (priceActionPullback) signal(df *model.Dataframe) model.SideType {
	trend := df.Metadata["sg_trend"]
	i := len(df.Close) - 1
	if !complete(df, 3, trend, df.Open, df.High, df.Low) {
		return ""
	}
	for _, side := range sides {
		sign := rkSign(side)
		rising := sign*(df.Close[i]-trend[i]) > 0 && sign*(trend[i]-trend[i-1]) > 0
		// the pullback: the 2 candles before against the trend
		pulled := sign*(df.Close[i-1]-df.Open[i-1]) < 0 && sign*(df.Close[i-2]-df.Open[i-2]) < 0
		// the previous extreme on the other side: the high for a BUY
		previous := df.High[i-1]
		if side == model.SideTypeSell {
			previous = df.Low[i-1]
		}
		if rising && pulled && sign*(df.Close[i]-previous) > 0 {
			return side
		}
	}
	return ""
}
