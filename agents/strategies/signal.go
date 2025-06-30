package strategies

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

// SignalConfig holds the inputs of a signal strategy, see NewSignal, with its
// defaults in DefaultSignalConfig. The periods of the rules are the classic
// ones, fixed: see signals.
type SignalConfig struct {
	Timeframe string

	// TrailingATR, above 0, trails the stop loss at the broker that many ATR
	// of ATRLen candles behind the best price since the entry, moved on every
	// candle close and never back, as the Turtles did: see donchianatr. 0
	// leaves it where the entry placed it.
	TrailingATR float64

	// The exits and the sizing, see BracketConfig, TakeProfitATR 0 placing no
	// take profit. The opposite signal also closes the position at the market,
	// and opens the other side.
	BracketConfig
}

// DefaultSignalConfig returns the defaults of the signal strategy name, on the
// 15m timeframe: the exits of DefaultBracketConfig, a stop loss 1 ATR and a
// take profit 1.5 ATR away, unless the strategy has its own, eg: donchianatr.
func DefaultSignalConfig(name string) SignalConfig {
	cfg := SignalConfig{Timeframe: "15m", BracketConfig: DefaultBracketConfig()}
	if spec, ok := signals[name]; ok && spec.exits != nil {
		spec.exits(&cfg)
	}
	return cfg
}

func (c SignalConfig) validate() error {
	exits := c.BracketConfig
	if exits.TakeProfitATR == 0 {
		exits.TakeProfitATR = 1 // none, the rest of the exits checked as for the other strategies
	}
	if err := exits.validate(); err != nil {
		return err
	}

	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case c.TrailingATR < 0:
		return errors.New("trailing ATR multiple can't be negative")
	}
	_, _, err := timeframeCandles(c.Timeframe, time.Hour)
	return err
}

// signalSpec is a strategy of NewSignal: what it trades, its rule on a
// timeframe, and its exits when they aren't the ones of DefaultBracketConfig.
type signalSpec struct {
	about string
	rule  func(timeframe time.Duration) signalRule
	exits func(cfg *SignalConfig)
}

// signals are the strategies of NewSignal by name, the one of the command
// line. Each trades both sides, a SELL on the mirror of the rule of a BUY given
// here; the rules are in signal_trend.go, signal_momentum.go and
// signal_breakout.go.
var signals = map[string]signalSpec{
	// trend following
	"emacross": {about: "the EMA 9 crossing the EMA 21", rule: fixed(emaCross{})},
	"ema200":   {about: "the close crossing a rising EMA 200", rule: fixed(emaTrend{})},
	"ribbon":   {about: "the EMAs 8/13/21/34/55 lining up in order", rule: fixed(ribbon{})},
	"supertrend": {about: "the flips of the Supertrend 10/3",
		rule: fixed(superTrend{})},
	"emasupertrend": {about: "the flips of the Supertrend 10/3 on the side of the EMA 200",
		rule: fixed(superTrend{trendLen: 200})},
	"psarema": {about: "the flips of the Parabolic SAR 0.02/0.2 on the side of the EMA 50",
		rule: fixed(sarTrend{})},
	"atrtrailing": {about: "the flips of the ATR trailing stop, the Chandelier Exit 22/3",
		rule: fixed(chandelierTrend{})},
	"ichimoku": {about: "the Tenkan 9 crossing the Kijun 26 beyond the Ichimoku cloud",
		rule: fixed(ichimokuTrend{})},
	"ichimokuadx": {about: "the Tenkan 9 crossing the Kijun 26 beyond the Ichimoku cloud with the ADX 14 above 25",
		rule: fixed(ichimokuTrend{adx: 25})},
	"mtftrend": {about: "the EMA 20 crossing the EMA 50 with the EMA 50 of the 4x timeframe moving its way",
		rule: newMTFTrend},
	"mtfema": {about: "the EMA 20 crossing the EMA 50 with them lined up the same way on the 4x and 16x timeframes",
		rule: newMTFAlignment},

	// momentum
	"adxema": {about: "the close beyond the EMA 50 with the ADX 14 above 25 and the DI on its side",
		rule: fixed(adxTrend{})},
	"macdtrend": {about: "the MACD 12/26/9 crossing its signal line beyond zero", rule: fixed(macdTrend{})},
	"emamacd": {about: "the EMA 9 crossing the EMA 21 with the MACD histogram on its side",
		rule: fixed(emaMACD{})},
	"emarsiadx": {about: "the EMA 9 crossing the EMA 21 with the RSI 14 beyond 50 and the ADX 14 above 20",
		rule: fixed(emaRSIADX{})},
	"rsitrend": {about: "the RSI 14 crossing back 50 on the side of the EMA 50", rule: fixed(rsiTrend{})},
	"stochpullback": {about: "the Stochastic 14/3/3 crossing out of the extreme zone on the side of the EMA 50",
		rule: fixed(stochPullback{})},
	"papullback": {about: "the close beyond the previous candle after a pullback, on the side of a rising EMA 50",
		rule: fixed(priceActionPullback{})},

	// breakout
	"donchian": {about: "the close beyond the Donchian channel of 20 candles", rule: fixed(channelBreakout{length: 20})},
	"channel":  {about: "the close beyond the channel of 55 candles", rule: fixed(channelBreakout{length: 55})},
	"adxdonchian": {about: "the close beyond the Donchian channel of 20 candles with the ADX 14 above 25",
		rule: fixed(channelBreakout{length: 20, adx: 25})},
	"donchianatr": {about: "the close beyond the Donchian channel of 20 candles, its stop loss 2 ATR away trailing, " +
		"as the Turtles", rule: fixed(channelBreakout{length: 20}), exits: turtleExits},
	"bbbreakout": {about: "the close crossing the Bollinger Band 20/2 with its middle moving its way",
		rule: fixed(bollingerBreakout{})},
	"keltner": {about: "the close crossing the Keltner Channel of the EMA 20 and 2 ATR 10",
		rule: fixed(keltnerBreakout{})},
	"volumebreakout": {about: "the close beyond the channel of 20 candles on 1.5 times the average volume",
		rule: fixed(volumeBreakout{})},
	"highlow": {about: "the close crossing the high or the low of the previous day (UTC)", rule: newDayBreakout},
	"momentumbreakout": {about: "the close beyond the channel of 20 candles on a candle of a large body, the ROC 10 " +
		"on its side", rule: fixed(momentumBreakout{})},
}

// SignalAbout returns what the signal strategy name trades, and whether there is
// one of that name, see NewSignal.
func SignalAbout(name string) (string, bool) {
	spec, ok := signals[name]
	return spec.about, ok
}

// turtleExits are the exits of donchianatr: a stop loss 2 ATR away trailing 2
// ATR behind the best price, without a take profit, as the Turtles.
func turtleExits(cfg *SignalConfig) {
	cfg.StopATR, cfg.TakeProfitATR, cfg.TrailingATR = 2, 0, 2
}

// signalRule is the rule of a SignalStrategy: the candles it needs, the
// indicators it adds to the dataframe, the ones drawn on the chart, and its
// signal on the last candle, none without one.
type signalRule interface {
	warmup() int
	indicators(df *model.Dataframe) []strategy.ChartIndicator
	signal(df *model.Dataframe) model.SideType
}

// fixed returns the rule of every timeframe.
func fixed(rule signalRule) func(time.Duration) signalRule {
	return func(time.Duration) signalRule { return rule }
}

// SignalStrategy trades the signals of a classic rule, one position at a time
// per pair, as the intraday strategies do: a BUY or a SELL placed at the market
// on the candle the rule fires, with its stop loss and take profit at the
// broker, see BracketConfig. The opposite signal closes the position held at
// the market, and opens the other side. See SignalConfig and signals.
type SignalStrategy struct {
	cfg     SignalConfig
	warmup  int
	bracket *bracket
	rule    signalRule
	trails  map[string]*signalTrail // the trailing of the positions, by pair
}

// signalTrail is the best price since the entry of the position of trade, whose
// stop loss follows it, see SignalConfig.TrailingATR.
type signalTrail struct {
	trade   *scalpTrade
	extreme float64 // highest high of a long, lowest low of a short, from the entry close
}

// NewSignal validates the configuration and creates the signal strategy name,
// see signals.
func NewSignal(name string, cfg SignalConfig) (*SignalStrategy, error) {
	spec, ok := signals[name]
	if !ok {
		return nil, fmt.Errorf("invalid signal strategy %q", name)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	_, timeframe, err := timeframeCandles(cfg.Timeframe, time.Hour)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	rule := spec.rule(timeframe)
	return &SignalStrategy{
		cfg:     cfg,
		warmup:  max(rule.warmup(), seedWarmup(cfg.ATRLen)),
		bracket: newBracket(strings.ToUpper(name), cfg.Timeframe, cfg.BracketConfig),
		rule:    rule,
		trails:  make(map[string]*signalTrail),
	}, nil
}

func (s *SignalStrategy) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *SignalStrategy) WarmupPeriod() int {
	return s.warmup
}

func (s *SignalStrategy) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["sg_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)
	return append(s.rule.indicators(df),
		pane(df, fmt.Sprintf("ATR %d", s.cfg.ATRLen), s.cfg.ATRLen, line(df.Metadata["sg_atr"], "ATR", colorOther)))
}

func (s *SignalStrategy) OnCandle(df *model.Dataframe, broker service.Broker) {
	entry := s.rule.signal(df)
	opposite := func(_ *model.Dataframe, held model.SideType) string {
		if entry != "" && entry != held {
			return "OPPOSITE"
		}
		return ""
	}
	if !s.bracket.manage(df, broker, opposite) {
		s.trail(df, broker)
		return
	}
	if entry != "" && s.bracket.open(df, broker, entry, df.Metadata["sg_atr"].Last(0)) {
		s.trails[df.Pair] = &signalTrail{trade: s.bracket.trades[df.Pair], extreme: df.Close.Last(0)}
	}
}

// trail moves the stop loss at the broker of the position held on the pair to
// TrailingATR from the best price since the entry, with the ATR of the last
// candle, as RKTrendConfluence.trailBroker does: it only tightens, and stays on
// the losing side of the close, where the broker takes it.
func (s *SignalStrategy) trail(df *model.Dataframe, broker service.Broker) {
	trade := s.bracket.trades[df.Pair]
	protected, ok := broker.(service.ProtectionBroker)
	if s.cfg.TrailingATR <= 0 || s.cfg.StopValue > 0 || trade == nil || !ok {
		return
	}

	trail := s.trails[df.Pair]
	if trail == nil || trail.trade != trade {
		// a position found after a restart: trailed from now on
		trail = &signalTrail{trade: trade, extreme: df.Close.Last(0)}
		s.trails[df.Pair] = trail
	}
	sign := rkSign(trade.side)
	if trade.side == model.SideTypeBuy {
		trail.extreme = math.Max(trail.extreme, df.High.Last(0))
	} else {
		trail.extreme = math.Min(trail.extreme, df.Low.Last(0))
	}
	atr := df.Metadata["sg_atr"].Last(0)
	if isNa(atr) || atr <= 0 {
		return
	}
	stop := trail.extreme - sign*s.cfg.TrailingATR*atr

	for i, leg := range trade.legs {
		if leg.Type != model.OrderTypeStopLoss {
			continue
		}
		current := leg.Price
		if leg.Stop != nil {
			current = *leg.Stop
		}
		if sign*stop <= sign*current || sign*stop >= sign*df.Close.Last(0) {
			return
		}
		moved, err := protected.UpdateProtection(leg, stop)
		if err != nil {
			log.Error(err)
			return
		}
		trade.legs[i] = moved
		log.Infof("[%s] %s %s | TRAIL | stop %f", s.bracket.name, df.Pair, s.cfg.Timeframe, stop)
	}
}

// the colors of the lines of the signal strategies
const (
	colorUp    = "#00bd4e"
	colorDown  = "#c90032"
	colorLine  = "#fd8700"
	colorOther = "#8e44ad"
	colorSlow  = "#2962ff"
)

// line returns a metric of the chart drawn as a line.
func line(values []float64, name, color string) strategy.IndicatorMetric {
	return strategy.IndicatorMetric{Values: values, Name: name, Color: color, Style: strategy.StyleLine}
}

// overlay returns the indicators drawn over the candles, from the candle of
// index warmup on, their first values meaningless before, eg: the SMA 20 from
// the 20th candle.
func overlay(df *model.Dataframe, group string, warmup int,
	metrics ...strategy.IndicatorMetric) []strategy.ChartIndicator {
	return []strategy.ChartIndicator{{Overlay: true, GroupName: group, Time: df.Time, Warmup: warmup,
		Metrics: metrics}}
}

// pane returns indicators drawn in a pane of their own, from the candle of index
// warmup on.
func pane(df *model.Dataframe, group string, warmup int, metrics ...strategy.IndicatorMetric) strategy.ChartIndicator {
	return strategy.ChartIndicator{GroupName: group, Time: df.Time, Warmup: warmup, Metrics: metrics}
}

// guard returns the values of an indicator of the indicator package, computed by
// compute, when df holds the candles it needs: those of talib fail on fewer, eg:
// when the chart draws a strategy still warming up. Missing values otherwise.
func guard(df *model.Dataframe, candles int, compute func() []float64) []float64 {
	if len(df.Close) < candles {
		return missing(len(df.Close))
	}
	return compute()
}

// missing returns n missing values.
func missing(n int) []float64 {
	values := make([]float64, n)
	for i := range values {
		values[i] = na
	}
	return values
}

// complete reports whether df holds n candles at least, and the series a value
// for each of them: the ones of Indicators.
func complete(df *model.Dataframe, n int, series ...[]float64) bool {
	if len(df.Close) < n {
		return false
	}
	for _, s := range series {
		if len(s) != len(df.Close) {
			return false
		}
	}
	return true
}

// sides are the signals of the rules, checked in this order.
var sides = []model.SideType{model.SideTypeBuy, model.SideTypeSell}

// crossed reports whether a crossed b the way of sign on candle i: over it for a
// BUY (sign 1), under it for a SELL.
func crossed(a, b []float64, i int, sign float64) bool {
	if sign > 0 {
		return crossover(a, b, i)
	}
	return crossunder(a, b, i)
}

// crossedLevel reports whether src crossed level the way of sign on candle i:
// beyond it then, not the candle before.
func crossedLevel(src []float64, level float64, i int, sign float64) bool {
	return i > 0 && sign*(src[i]-level) > 0 && sign*(src[i-1]-level) <= 0
}

// flipped reports whether the close crossed to the side of sign of line, the
// line of a trend following indicator that flips to the other side of the
// price with the trend, eg: the Supertrend or the Parabolic SAR.
func flipped(close, line []float64, i int, sign float64) bool {
	return i > 0 && sign*(close[i]-line[i]) > 0 && sign*(close[i-1]-line[i-1]) < 0
}

// emas adds the EMAs of fast and slow closes to the dataframe, sg_fast and
// sg_slow, and returns their chart.
func emas(df *model.Dataframe, group string, fast, slow int) []strategy.ChartIndicator {
	df.Metadata["sg_fast"] = pineEMA(df.Close, fast)
	df.Metadata["sg_slow"] = pineEMA(df.Close, slow)
	return overlay(df, group, slow, line(df.Metadata["sg_fast"], fmt.Sprintf("EMA %d", fast), colorUp),
		line(df.Metadata["sg_slow"], fmt.Sprintf("EMA %d", slow), colorDown))
}

// trendEMA adds the EMA of length closes of the trend filter to the dataframe,
// sg_trend, and returns its line.
func trendEMA(df *model.Dataframe, length int) strategy.IndicatorMetric {
	df.Metadata["sg_trend"] = pineEMA(df.Close, length)
	return line(df.Metadata["sg_trend"], fmt.Sprintf("EMA %d", length), colorSlow)
}

// adx adds the DMI of 14 candles to the dataframe, sg_plus, sg_minus and sg_adx,
// and returns its pane.
func adx(df *model.Dataframe) strategy.ChartIndicator {
	plus, minus, strength := pineDMI(df.High, df.Low, df.Close, 14, 14)
	df.Metadata["sg_plus"], df.Metadata["sg_minus"], df.Metadata["sg_adx"] = plus, minus, strength
	return pane(df, "ADX 14", 14+14, line(strength, "ADX", colorLine), line(plus, "+DI", colorUp),
		line(minus, "-DI", colorDown))
}

// adxWarmup is the warmup of the DMI of adx.
var adxWarmup = seedWarmup(14 + 14)

// midpoints returns the middle of the highest high and the lowest low of the
// last length candles, the lines of Ichimoku.
func midpoints(high, low []float64, length int) []float64 {
	highest, lowest := pineHighest(high, length), pineLowest(low, length)
	out := make([]float64, len(high))
	for i := range out {
		out[i] = (highest[i] + lowest[i]) / 2
	}
	return out
}

// ichimoku returns the conversion line (Tenkan), the base line (Kijun) and the
// two spans of the cloud where they are drawn, displacement candles ahead,
// counting the one they are computed on as TradingView does: the cloud of
// candle i is the one computed on candle i-displacement+1, NaN before.
func ichimoku(high, low []float64, tenkanLen, kijunLen, spanBLen, displacement int) (tenkan, kijun, spanA,
	spanB []float64) {
	tenkan, kijun = midpoints(high, low, tenkanLen), midpoints(high, low, kijunLen)
	b := midpoints(high, low, spanBLen)
	spanA, spanB = make([]float64, len(high)), make([]float64, len(high))
	for i := range high {
		spanA[i], spanB[i] = na, na
		if k := i - displacement + 1; k >= 0 {
			spanA[i], spanB[i] = (tenkan[k]+kijun[k])/2, b[k]
		}
	}
	return tenkan, kijun, spanA, spanB
}

// keltner returns the Keltner channel: the EMA of emaLen closes, mult ATR of
// atrLen candles above and below it.
func keltner(high, low, close []float64, emaLen, atrLen int, mult float64) (upper, middle, lower []float64) {
	middle, atr := pineEMA(close, emaLen), pineATR(high, low, close, atrLen)
	upper, lower = make([]float64, len(close)), make([]float64, len(close))
	for i := range close {
		upper[i], lower[i] = middle[i]+mult*atr[i], middle[i]-mult*atr[i]
	}
	return upper, middle, lower
}

// chandelier returns the side of the Chandelier Exit for every candle, 1 once
// the close crosses above the stop of the shorts, the lowest low of length
// candles plus mult ATR of length candles, -1 once it crosses below the stop of
// the longs, the highest high minus mult ATR, NaN before; and the stop of its
// side. Each stop only tightens while the close holds beyond it.
func chandelier(high, low, close []float64, length int, mult float64) (side, stop []float64) {
	atr := pineATR(high, low, close, length)
	highest, lowest := pineHighest(high, length), pineLowest(low, length)
	side, stop = make([]float64, len(close)), make([]float64, len(close))
	longStop, shortStop, dir := na, na, na
	for i := range close {
		side[i], stop[i] = na, na
		if isNa(atr[i]) || isNa(highest[i]) || isNa(lowest[i]) {
			continue
		}
		long, short := highest[i]-mult*atr[i], lowest[i]+mult*atr[i]
		if !isNa(longStop) {
			if close[i-1] > longStop {
				long = math.Max(long, longStop)
			}
			if close[i-1] < shortStop {
				short = math.Min(short, shortStop)
			}
			switch {
			case close[i] > shortStop:
				dir = 1
			case close[i] < longStop:
				dir = -1
			}
		}
		longStop, shortStop = long, short
		side[i] = dir
		switch dir {
		case 1:
			stop[i] = longStop
		case -1:
			stop[i] = shortStop
		}
	}
	return side, stop
}

// htfEMA returns, for every candle, the EMA of length closes of the higher
// timeframe htf at its last closed bar, and at the bar before: the bars built
// from the candles, see htfBarStart, read closed only as htfBias does, without
// a look ahead. NaN while the EMA, seeded with an SMA, warms up.
func htfEMA(times []time.Time, close []float64, htf time.Duration, length int) (last, previous []float64) {
	last, previous = make([]float64, len(close)), make([]float64, len(close))
	var closes, ema []float64 // of the closed bars
	var bar time.Time
	alpha := 2 / float64(length+1)
	for i := range close {
		start := htfBarStart(times[i], htf)
		if i > 0 && !start.Equal(bar) {
			// the bar of the previous candle is closed
			closes = append(closes, close[i-1])
			value := na
			switch n := len(closes); {
			case n == length:
				var sum float64
				for _, c := range closes {
					sum += c
				}
				value = sum / float64(length)
			case n > length:
				value = alpha*close[i-1] + (1-alpha)*ema[n-2]
			}
			ema = append(ema, value)
		}
		bar = start

		last[i], previous[i] = na, na
		if n := len(ema); n >= 2 {
			last[i], previous[i] = ema[n-1], ema[n-2]
		}
	}
	return last, previous
}

// htfWarmup is the warmup of htfEMA of length closes on the timeframe mult times
// the one of the candles: its bars seeded as the EMAs of the candles, the last
// two closed.
func htfWarmup(length, mult int) int {
	return (seedWarmup(length) + 2) * mult
}

// previousDay returns, for every candle, the high and the low of the previous
// UTC day, NaN until one is seen whole.
func previousDay(times []time.Time, high, low []float64) (highs, lows []float64) {
	highs, lows = make([]float64, len(high)), make([]float64, len(high))
	dayHigh, dayLow := na, na
	previousHigh, previousLow := na, na
	var day time.Time
	whole := false // the first day seen may be partial
	for i := range high {
		d := times[i].UTC().Truncate(24 * time.Hour)
		if i > 0 && !d.Equal(day) {
			if whole {
				previousHigh, previousLow = dayHigh, dayLow
			}
			whole = true
			dayHigh, dayLow = na, na
		}
		day = d
		if isNa(dayHigh) || high[i] > dayHigh {
			dayHigh = high[i]
		}
		if isNa(dayLow) || low[i] < dayLow {
			dayLow = low[i]
		}
		highs[i], lows[i] = previousHigh, previousLow
	}
	return highs, lows
}
