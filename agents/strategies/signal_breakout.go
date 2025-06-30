package strategies

import (
	"fmt"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/strategy"
)

// The breakout rules of the signal strategies, see signals.

// channelBreakout is Donchian Breakout: a BUY on the close above the highest
// high of the length candles before, Channel Breakout with 55 of them; with
// adx, the ADX of 14 candles above it too, ADX + Donchian Breakout.
type channelBreakout struct {
	length int
	adx    float64 // 0 without the ADX
}

func (r channelBreakout) warmup() int {
	bars := r.length + 1
	if r.adx > 0 {
		bars = max(bars, adxWarmup+1)
	}
	return bars
}

func (r channelBreakout) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	chart := channel(df, fmt.Sprintf("Donchian %d", r.length), r.length)
	if r.adx > 0 {
		chart = append(chart, adx(df))
	}
	return chart
}

func (r channelBreakout) signal(df *model.Dataframe) model.SideType {
	strength := df.Metadata["sg_adx"]
	if r.adx > 0 && len(strength) != len(df.Close) {
		return ""
	}
	side := channelBroken(df)
	if side == "" || r.adx > 0 && !(strength[len(df.Close)-1] > r.adx) {
		return ""
	}
	return side
}

// channel adds the channel of length candles to the dataframe, the highest high
// sg_upper and the lowest low sg_lower, and returns its chart.
func channel(df *model.Dataframe, group string, length int) []strategy.ChartIndicator {
	df.Metadata["sg_upper"], df.Metadata["sg_lower"] = pineHighest(df.High, length), pineLowest(df.Low, length)
	return overlay(df, group, length, line(df.Metadata["sg_upper"], "Upper", colorUp),
		line(df.Metadata["sg_lower"], "Lower", colorDown))
}

// channelBroken returns the side of the close beyond the channel of channel of
// the candles before it: a BUY above its highest high, a SELL below its lowest
// low, none within.
func channelBroken(df *model.Dataframe) model.SideType {
	upper, lower := df.Metadata["sg_upper"], df.Metadata["sg_lower"]
	i := len(df.Close) - 1
	if !complete(df, 2, upper, lower) {
		return ""
	}
	for _, side := range sides {
		level := upper[i-1]
		if side == model.SideTypeSell {
			level = lower[i-1]
		}
		if crossedLevel(df.Close, level, i, rkSign(side)) {
			return side
		}
	}
	return ""
}

// bollingerBreakout is Bollinger Band Trend Breakout: a BUY on the close
// crossing over the upper Bollinger Band of 20 closes and 2 deviations while
// its middle, the SMA 20, rises; a SELL on the close crossing under the lower
// band while it falls.
type bollingerBreakout struct{}

func (bollingerBreakout) warmup() int {
	return 20 + 1
}

func (bollingerBreakout) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	middle, deviation := pineSMA(df.Close, 20), pineStdev(df.Close, 20)
	upper, lower := make([]float64, len(middle)), make([]float64, len(middle))
	for i := range middle {
		upper[i], lower[i] = middle[i]+2*deviation[i], middle[i]-2*deviation[i]
	}
	df.Metadata["sg_upper"], df.Metadata["sg_middle"], df.Metadata["sg_lower"] = upper, middle, lower
	return overlay(df, "Bollinger Bands 20/2", 20, line(upper, "Upper", colorUp), line(middle, "SMA 20", colorLine),
		line(lower, "Lower", colorDown))
}

func (bollingerBreakout) signal(df *model.Dataframe) model.SideType {
	upper, middle, lower := df.Metadata["sg_upper"], df.Metadata["sg_middle"], df.Metadata["sg_lower"]
	i := len(df.Close) - 1
	if !complete(df, 2, upper, middle, lower) {
		return ""
	}
	for _, side := range sides {
		sign, band := rkSign(side), upper
		if side == model.SideTypeSell {
			band = lower
		}
		if crossed(df.Close, band, i, sign) && sign*(middle[i]-middle[i-1]) > 0 {
			return side
		}
	}
	return ""
}

// keltnerBreakout is Keltner Channel Breakout: a BUY on the close crossing over
// the upper band of the Keltner channel, the EMA 20 plus 2 ATR of 10 candles; a
// SELL on the close crossing under its lower band.
type keltnerBreakout struct{}

func (keltnerBreakout) warmup() int {
	return seedWarmup(20) + 1
}

func (keltnerBreakout) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	upper, middle, lower := keltner(df.High, df.Low, df.Close, 20, 10, 2)
	df.Metadata["sg_upper"], df.Metadata["sg_middle"], df.Metadata["sg_lower"] = upper, middle, lower
	return overlay(df, "Keltner Channel 20/10/2", 20, line(upper, "Upper", colorUp),
		line(middle, "EMA 20", colorLine), line(lower, "Lower", colorDown))
}

func (keltnerBreakout) signal(df *model.Dataframe) model.SideType {
	upper, lower := df.Metadata["sg_upper"], df.Metadata["sg_lower"]
	i := len(df.Close) - 1
	if !complete(df, 2, upper, lower) {
		return ""
	}
	for _, side := range sides {
		band := upper
		if side == model.SideTypeSell {
			band = lower
		}
		if crossed(df.Close, band, i, rkSign(side)) {
			return side
		}
	}
	return ""
}

// volumeBreakout is Volume + Breakout: a BUY on the close above the highest
// high of the 20 candles before, its volume 1.5 times the average of 20 candles
// at least.
type volumeBreakout struct{}

func (volumeBreakout) warmup() int {
	return 20 + 1
}

func (volumeBreakout) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["sg_volume"] = pineSMA(df.Volume, 20)
	return append(channel(df, "Channel 20", 20),
		pane(df, "Volume SMA 20", 20, line(df.Metadata["sg_volume"], "SMA 20", colorLine)))
}

func (volumeBreakout) signal(df *model.Dataframe) model.SideType {
	average := df.Metadata["sg_volume"]
	if !complete(df, 1, average, df.Volume) {
		return ""
	}
	i := len(df.Close) - 1
	if !(df.Volume[i] >= 1.5*average[i]) { // a missing average failing it
		return ""
	}
	return channelBroken(df)
}

// dayBreakout is High/Low Breakout: a BUY on the close crossing over the high
// of the previous day, a SELL under its low, the days of UTC.
type dayBreakout struct {
	timeframe time.Duration
}

func newDayBreakout(timeframe time.Duration) signalRule {
	return dayBreakout{timeframe: timeframe}
}

func (r dayBreakout) warmup() int {
	// the first day may be partial: the whole one after it, and the candles of
	// the day so far
	return int(3*24*time.Hour/r.timeframe) + 1
}

func (dayBreakout) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["sg_day_high"], df.Metadata["sg_day_low"] = previousDay(df.Time, df.High, df.Low)
	return overlay(df, "Previous Day", 1, line(df.Metadata["sg_day_high"], "High", colorUp),
		line(df.Metadata["sg_day_low"], "Low", colorDown))
}

func (dayBreakout) signal(df *model.Dataframe) model.SideType {
	high, low := df.Metadata["sg_day_high"], df.Metadata["sg_day_low"]
	i := len(df.Close) - 1
	if !complete(df, 2, high, low) {
		return ""
	}
	for _, side := range sides {
		level := high[i]
		if side == model.SideTypeSell {
			level = low[i]
		}
		if crossedLevel(df.Close, level, i, rkSign(side)) {
			return side
		}
	}
	return ""
}

// momentumBreakout is Momentum Breakout: a BUY on the close above the highest
// high of the 20 candles before with the ROC of 10 closes above zero, on a
// candle up of a body of 60% of its range at least.
type momentumBreakout struct{}

func (momentumBreakout) warmup() int {
	return 20 + 1
}

func (momentumBreakout) indicators(df *model.Dataframe) []strategy.ChartIndicator {
	return channel(df, "Channel 20", 20)
}

func (momentumBreakout) signal(df *model.Dataframe) model.SideType {
	side := channelBroken(df)
	i := len(df.Close) - 1
	if side == "" || !complete(df, 11, df.Open, df.High, df.Low) {
		return ""
	}
	sign := rkSign(side)
	// the ROC of 10 closes on the side of the breakout, and the body of the
	// candle that way
	momentum := sign*(df.Close[i]-df.Close[i-10]) > 0
	body := sign*(df.Close[i]-df.Open[i]) >= 0.6*(df.High[i]-df.Low[i]) && df.High[i] > df.Low[i]
	if !momentum || !body {
		return ""
	}
	return side
}
