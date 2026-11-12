package strategies

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

// How Fibonacci finds the impulses, see FibonacciConfig.Swing.
const (
	FibSwingPivot = "pivot"
	FibSwingRange = "range"
)

// How Fibonacci enters, see FibonacciConfig.Trigger.
const (
	FibTriggerClose = "close"
	FibTriggerLimit = "limit"
)

// Where Fibonacci takes its profit, see FibonacciConfig.Target.
const (
	FibTargetExtreme   = "extreme"
	FibTargetExtension = "extension"
	FibTargetRR        = "rr"
	FibTargetNone      = "none"
)

// FibonacciConfig holds the inputs of Fibonacci, with its defaults in
// DefaultFibonacciConfig. Periods are counted in candles of Timeframe. The
// levels are retracements of the impulse: 0 at its extreme, 1 at its origin,
// eg: 0.618 for 61.8%.
type FibonacciConfig struct {
	Timeframe string

	// The impulse, the move whose pullback is traded, found in the last
	// Lookback candles. With Swing pivot, from a swing to the opposite one
	// after it, the swings being the candles beyond the PivotLen ones on each
	// side of them (known PivotLen candles later): the last swing high, from
	// the lowest low since the swing low before it, is the impulse of a BUY;
	// the last swing low, from the highest high since the swing high before
	// it, the one of a SELL. With Swing range, from the lowest low to the
	// highest high of the Lookback candles, a BUY when the high came after
	// the low, a SELL otherwise. An impulse is at least MinImpulseATR long,
	// in ATR of ATRLen candles at its extreme, and is traded until a price
	// beyond its extreme (it goes on, the levels moving with it) or a close
	// beyond its origin (it failed), once at most while the agent runs: a
	// restart forgets the impulses entered.
	Swing         string
	PivotLen      int
	Lookback      int
	MinImpulseATR float64

	// TrendEMA, above 0, trades only the pullbacks on the side of the EMA of
	// that period: a BUY with the close above it, a SELL below it. 0 trades
	// every impulse.
	TrendEMA int

	// The entry, in the zone from the retracement EntryLow to EntryHigh. With
	// Trigger close, at the market on the close of a candle rejecting it:
	// its wick reaching EntryLow at least, and closing in the direction of
	// the impulse (above its open for a BUY) no further than EntryHigh. With
	// Trigger limit, a limit order at the retracement LimitLevel, within the
	// zone, placed while the price is on the side of the extreme of it and
	// canceled once the impulse ends or after PendingBars candles, 0 never.
	// A limit placed before a restart is no longer followed: the position it
	// opens is, as the others, but it is left at the broker otherwise.
	EntryLow    float64
	EntryHigh   float64
	Trigger     string
	LimitLevel  float64
	PendingBars int

	// The stop loss, at the retracement StopLevel, StopBufferATR in ATR of the
	// entry candle beyond it, eg: 0.786 and 0.25; 1 places it at the origin.
	StopLevel     float64
	StopBufferATR float64

	// The take profit: with Target extreme, at the extreme of the impulse;
	// extension, at TargetLevel times the impulse from its origin, eg: 1.272
	// or 1.618; rr, RewardRisk times the distance from the entry to the stop
	// loss; none, without one, which needs TrailingATR.
	Target      string
	TargetLevel float64
	RewardRisk  float64

	// TrailingATR, above 0, trails the stop loss at the broker that many ATR
	// behind the best price since the entry, as SignalConfig.TrailingATR.
	TrailingATR float64

	// The sizing and the rest of the exits, see BracketConfig: MaxBars, the
	// fixed values of StopValue and TakeProfitValue, which replace the levels
	// of the impulse, and MinATRPct. StopATR and TakeProfitATR replace them
	// when asked only (ATRStop and ATRTakeProfit), from the entry.
	BracketConfig
}

// DefaultFibonacciConfig returns the defaults of Fibonacci, on the 15m
// timeframe: the impulses between swings of 5 candles, 3 ATR long at least,
// their pullbacks entered on the close of a candle rejecting the zone from
// 50% to 61.8%, the stop loss 0.25 ATR beyond 78.6% and the take profit at
// the extreme of the impulse.
func DefaultFibonacciConfig() FibonacciConfig {
	return FibonacciConfig{
		Timeframe: "15m",

		Swing:         FibSwingPivot,
		PivotLen:      5,
		Lookback:      300,
		MinImpulseATR: 3,

		EntryLow:    0.5,
		EntryHigh:   0.618,
		Trigger:     FibTriggerClose,
		LimitLevel:  0.618,
		PendingBars: 20,

		StopLevel:     0.786,
		StopBufferATR: 0.25,

		Target:      FibTargetExtreme,
		TargetLevel: 1.272,
		RewardRisk:  2,

		BracketConfig: DefaultBracketConfig(),
	}
}

func (c FibonacciConfig) validate() error {
	exits := c.BracketConfig
	exits.StopATR, exits.TakeProfitATR = 1, 1 // unused unless asked: the levels of the impulse in their place
	if err := exits.validate(); err != nil {
		return err
	}

	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case c.Swing != FibSwingPivot && c.Swing != FibSwingRange:
		return fmt.Errorf("invalid swing %q: pivot or range", c.Swing)
	case c.PivotLen < 1:
		return errors.New("periods must be at least 1")
	case c.Lookback < 3 || c.Swing == FibSwingPivot && c.Lookback <= 2*c.PivotLen+1:
		return errors.New("the lookback must hold an impulse: more than twice the pivot period and 2 candles")
	case c.MinImpulseATR < 0 || c.StopBufferATR < 0:
		return errors.New("the ATR multiples can't be negative")
	case c.TrailingATR < 0:
		return errors.New("trailing ATR multiple can't be negative")
	case c.TrendEMA < 0:
		return errors.New("the trend EMA period can't be negative")
	case !(c.EntryLow > 0 && c.EntryLow < c.EntryHigh && c.EntryHigh < c.StopLevel && c.StopLevel <= 1):
		return errors.New("the levels must be 0 < entry low < entry high < stop level <= 1")
	case c.Trigger != FibTriggerClose && c.Trigger != FibTriggerLimit:
		return fmt.Errorf("invalid trigger %q: close or limit", c.Trigger)
	case c.Trigger == FibTriggerLimit && (c.LimitLevel < c.EntryLow || c.LimitLevel > c.EntryHigh):
		return errors.New("the limit level must be within the entry zone")
	case c.PendingBars < 0:
		return errors.New("pending bars can't be negative")
	case c.Target != FibTargetExtreme && c.Target != FibTargetExtension && c.Target != FibTargetRR &&
		c.Target != FibTargetNone:
		return fmt.Errorf("invalid target %q: extreme, extension, rr or none", c.Target)
	case c.Target == FibTargetExtension && c.TargetLevel <= 1:
		return errors.New("the target extension must be above 1")
	case c.Target == FibTargetRR && c.RewardRisk <= 0:
		return errors.New("the reward to risk must be positive")
	case c.Target == FibTargetNone && c.TrailingATR <= 0 && c.TakeProfitValue <= 0 && !c.ATRTakeProfit:
		return errors.New("a target of none needs a trailing stop")
	}
	return nil
}

// Fibonacci trades the pullbacks of the impulses in their direction, at their
// Fibonacci retracements: a BUY in the zone of 50% to 61.8% of a move up, a
// SELL in the one of a move down by default, its stop loss beyond 78.6% and
// its take profit at the extreme of the move. One position at a time per
// pair. See FibonacciConfig.
type Fibonacci struct {
	cfg     FibonacciConfig
	warmup  int
	bracket *bracket
	traded  map[string]map[fibKey]bool // the impulses entered, by pair
	pending map[string]*fibPending     // the limit entries waiting, by pair
	trails  map[string]*signalTrail    // the trailing of the positions, by pair
}

// fibLeg is an impulse: from its origin to its extreme, the low and the high
// of a move up, the side of its entries a BUY, the high and the low of a move
// down, a SELL; from and to are the candles of its origin and of its extreme
// in the dataframe it was found in.
type fibLeg struct {
	side            model.SideType
	origin, extreme float64
	from, to        int
}

// level returns the price of the retracement r of the leg: its extreme at 0,
// its origin at 1, beyond its extreme below 0.
func (l fibLeg) level(r float64) float64 {
	return l.extreme - r*(l.extreme-l.origin)
}

// fibKey tells an impulse apart, entered once at most: its side, and the
// times of its origin and of its extreme.
type fibKey struct {
	side     model.SideType
	from, to time.Time
}

// fibPending is a limit entry waiting for its fill: the impulse it trades, its
// orders at the broker, the entry then its legs, and the candles it waited.
type fibPending struct {
	leg   fibLeg
	entry model.Order
	legs  []model.Order
	bars  int
}

// NewFibonacci validates the configuration and creates the strategy.
func NewFibonacci(cfg FibonacciConfig) (*Fibonacci, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("fibonacci: %w", err)
	}

	// the candles of the lookback, with the ATR at the extreme of an impulse
	// seeded, and the trend EMA seeded on the last one
	warmup := cfg.Lookback + seedWarmup(cfg.ATRLen)
	if cfg.TrendEMA > 0 {
		warmup = max(warmup, seedWarmup(cfg.TrendEMA))
	}
	return &Fibonacci{
		cfg:     cfg,
		warmup:  warmup + 1,
		bracket: newBracket("FIBONACCI", cfg.Timeframe, cfg.BracketConfig),
		traded:  make(map[string]map[fibKey]bool),
		pending: make(map[string]*fibPending),
		trails:  make(map[string]*signalTrail),
	}, nil
}

func (s *Fibonacci) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *Fibonacci) WarmupPeriod() int {
	return s.warmup
}

func (s *Fibonacci) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["fib_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)
	if s.cfg.Swing == FibSwingPivot {
		df.Metadata["fib_swing_high"] = pivotHigh(df.High, s.cfg.PivotLen)
		df.Metadata["fib_swing_low"] = pivotLow(df.Low, s.cfg.PivotLen)
	}
	if s.cfg.TrendEMA > 0 {
		df.Metadata["fib_ema"] = pineEMA(df.Close, s.cfg.TrendEMA)
	}

	metrics := s.levels(df)
	if s.cfg.Swing == FibSwingPivot {
		metrics = append(metrics,
			strategy.IndicatorMetric{Values: df.Metadata["fib_swing_high"], Name: "Swing high", Color: colorDown,
				Style: strategy.StyleScatter},
			strategy.IndicatorMetric{Values: df.Metadata["fib_swing_low"], Name: "Swing low", Color: colorUp,
				Style: strategy.StyleScatter})
	}
	indicators := overlay(df, "Fibonacci", 1, metrics...)
	if s.cfg.TrendEMA > 0 {
		indicators = append(indicators, overlay(df, fmt.Sprintf("EMA %d", s.cfg.TrendEMA), s.cfg.TrendEMA,
			line(df.Metadata["fib_ema"], fmt.Sprintf("EMA %d", s.cfg.TrendEMA), colorSlow))...)
	}
	return append(indicators,
		pane(df, fmt.Sprintf("ATR %d", s.cfg.ATRLen), s.cfg.ATRLen, line(df.Metadata["fib_atr"], "ATR", colorOther)))
}

// levels returns the lines of the levels of the last impulse of df still
// traded, from its origin on: its extreme, the entry zone, the stop level,
// its origin, and the extension of the target. None without one.
func (s *Fibonacci) levels(df *model.Dataframe) []strategy.IndicatorMetric {
	type level struct {
		name  string
		ratio float64
		color string
	}
	list := []level{
		{"Fib 0", 0, "#787b86"},
		{"Fib " + ratio(s.cfg.EntryLow), s.cfg.EntryLow, colorLine},
		{"Fib " + ratio(s.cfg.EntryHigh), s.cfg.EntryHigh, colorLine},
		{"Fib " + ratio(s.cfg.StopLevel), s.cfg.StopLevel, colorDown},
	}
	if s.cfg.StopLevel < 1 {
		list = append(list, level{"Fib 1", 1, "#787b86"})
	}
	if s.cfg.Target == FibTargetExtension {
		list = append(list, level{"Fib target " + ratio(s.cfg.TargetLevel), 1 - s.cfg.TargetLevel, colorSlow})
	}

	var last *fibLeg
	i := len(df.Close) - 1
	for _, leg := range s.legs(df) {
		if s.holds(df, leg, i) && s.long(df, leg) && (last == nil || leg.to > last.to) {
			last = &leg
		}
	}

	metrics := make([]strategy.IndicatorMetric, 0, len(list))
	for _, l := range list {
		values := missing(len(df.Close))
		for k := 0; last != nil && k < len(values); k++ {
			if k >= last.from {
				values[k] = last.level(l.ratio)
			}
		}
		metrics = append(metrics, line(values, l.name, l.color))
	}
	return metrics
}

// ratio formats a level for the name of its line, eg: 0.618.
func ratio(r float64) string {
	return strconv.FormatFloat(r, 'f', -1, 64)
}

// legs returns the impulses of the last candle of df, see FibonacciConfig: at
// most one of each side, whether still traded or not (see holds).
func (s *Fibonacci) legs(df *model.Dataframe) []fibLeg {
	i := len(df.Close) - 1
	if i < 2 || len(df.High) != len(df.Close) || len(df.Low) != len(df.Close) {
		return nil
	}
	start := max(0, i-s.cfg.Lookback+1)

	if s.cfg.Swing == FibSwingRange {
		high, low := start, start
		for k := start; k <= i; k++ {
			if df.High[k] > df.High[high] {
				high = k
			}
			if df.Low[k] < df.Low[low] {
				low = k
			}
		}
		switch {
		case high > low:
			return []fibLeg{{side: model.SideTypeBuy, origin: df.Low[low], extreme: df.High[high], from: low, to: high}}
		case low > high:
			return []fibLeg{{side: model.SideTypeSell, origin: df.High[high], extreme: df.Low[low], from: high, to: low}}
		}
		return nil
	}

	highs, lows := df.Metadata["fib_swing_high"], df.Metadata["fib_swing_low"]
	if len(highs) != len(df.Close) || len(lows) != len(df.Close) {
		return nil
	}
	// latest returns the last swing of swings in the lookback before the
	// candle before, -1 for none
	latest := func(swings []float64, before int) int {
		for k := before - 1; k >= start; k-- {
			if !isNa(swings[k]) {
				return k
			}
		}
		return -1
	}

	var legs []fibLeg
	if high := latest(highs, i+1); high >= 0 {
		if low := latest(lows, high); low >= 0 {
			from := low
			for k := low; k <= high; k++ {
				if df.Low[k] < df.Low[from] {
					from = k
				}
			}
			legs = append(legs, fibLeg{side: model.SideTypeBuy, origin: df.Low[from], extreme: df.High[high],
				from: from, to: high})
		}
	}
	if low := latest(lows, i+1); low >= 0 {
		if high := latest(highs, low); high >= 0 {
			from := high
			for k := high; k <= low; k++ {
				if df.High[k] > df.High[from] {
					from = k
				}
			}
			legs = append(legs, fibLeg{side: model.SideTypeSell, origin: df.High[from], extreme: df.Low[low],
				from: from, to: low})
		}
	}
	return legs
}

// holds reports whether the leg is still traded on candle i: no price beyond
// its extreme since its origin, and no close beyond its origin since its
// extreme.
func (s *Fibonacci) holds(df *model.Dataframe, leg fibLeg, i int) bool {
	sign, beyond := rkSign(leg.side), df.High
	if leg.side == model.SideTypeSell {
		beyond = df.Low
	}
	for k := leg.from; k <= i; k++ {
		if sign*(beyond[k]-leg.extreme) > 0 {
			return false
		}
	}
	for k := leg.to + 1; k <= i; k++ {
		if sign*(df.Close[k]-leg.origin) < 0 {
			return false
		}
	}
	return true
}

// long reports whether the leg is MinImpulseATR long at least, in ATR at its
// extreme.
func (s *Fibonacci) long(df *model.Dataframe, leg fibLeg) bool {
	atrs := df.Metadata["fib_atr"]
	if len(atrs) != len(df.Close) || isNa(atrs[leg.to]) || leg.extreme == leg.origin {
		return false
	}
	return math.Abs(leg.extreme-leg.origin) >= s.cfg.MinImpulseATR*atrs[leg.to]
}

// key returns the key of the leg of df, see fibKey.
func (s *Fibonacci) key(df *model.Dataframe, leg fibLeg) fibKey {
	return fibKey{side: leg.side, from: df.Time[leg.from], to: df.Time[leg.to]}
}

// setup returns the impulse of the last candle of df whose pullback may be
// entered on it: still traded, long enough, not entered yet, on the side of
// the trend EMA and of Direction, the last candle after its extreme.
func (s *Fibonacci) setup(df *model.Dataframe) []fibLeg {
	i := len(df.Close) - 1
	if len(df.Time) != len(df.Close) || len(df.Open) != len(df.Close) {
		return nil
	}
	var setups []fibLeg
	for _, leg := range s.legs(df) {
		if i <= leg.to || !rkOpens(s.cfg.Direction, leg.side) || !s.holds(df, leg, i) || !s.long(df, leg) ||
			s.traded[df.Pair][s.key(df, leg)] || !s.trend(df, leg.side) {
			continue
		}
		setups = append(setups, leg)
	}
	return setups
}

// trend reports whether the last close is on the side of the trend EMA of the
// entry side, always without it.
func (s *Fibonacci) trend(df *model.Dataframe, side model.SideType) bool {
	if s.cfg.TrendEMA == 0 {
		return true
	}
	ema := df.Metadata["fib_ema"]
	return len(ema) == len(df.Close) && !isNa(ema.Last(0)) && rkSign(side)*(df.Close.Last(0)-ema.Last(0)) > 0
}

// rejects reports whether the last candle of df rejects the entry zone of the
// leg: its wick reaching EntryLow, and its close in the direction of the leg,
// no further than EntryHigh.
func (s *Fibonacci) rejects(df *model.Dataframe, leg fibLeg) bool {
	sign, wick := rkSign(leg.side), df.Low.Last(0)
	if leg.side == model.SideTypeSell {
		wick = df.High.Last(0)
	}
	closePrice := df.Close.Last(0)
	return sign*(leg.level(s.cfg.EntryLow)-wick) >= 0 && sign*(closePrice-df.Open.Last(0)) > 0 &&
		sign*(closePrice-leg.level(s.cfg.EntryHigh)) >= 0
}

// exits returns the stop loss and the take profit, 0 for none, of an entry at
// price in the pullback of the leg, with atr the ATR of the last candle, and
// whether they are positive and on their sides of price.
func (s *Fibonacci) exits(leg fibLeg, price, atr float64) (stop, profit float64, ok bool) {
	sign := rkSign(leg.side)
	stop = leg.level(s.cfg.StopLevel) - sign*s.cfg.StopBufferATR*atr
	switch s.cfg.Target {
	case FibTargetExtreme:
		profit = leg.level(0)
	case FibTargetExtension:
		profit = leg.level(1 - s.cfg.TargetLevel)
	case FibTargetRR:
		profit = price + sign*s.cfg.RewardRisk*math.Abs(price-stop)
	}
	return stop, profit, stop > 0 && sign*(price-stop) > 0 && (profit == 0 || profit > 0 && sign*(profit-price) > 0)
}

func (s *Fibonacci) OnCandle(df *model.Dataframe, broker service.Broker) {
	s.forget(df)
	if p := s.pending[df.Pair]; p != nil {
		s.checkPending(df, broker, p)
	}
	// no exit of its own: the stop loss, the take profit and the time
	if !s.bracket.manage(df, broker, func(*model.Dataframe, model.SideType) string { return "" }) {
		s.bracket.trail(df, broker, s.trails, s.cfg.TrailingATR, df.Metadata["fib_atr"])
		return
	}
	if s.pending[df.Pair] != nil {
		return // one entry at a time: the limit waits for its fill
	}

	atrs := df.Metadata["fib_atr"]
	if len(atrs) != len(df.Close) {
		return
	}
	atr, closePrice := atrs.Last(0), df.Close.Last(0)
	if isNa(atr) || atr <= 0 || s.cfg.MinATRPct > 0 && atr/closePrice*100 < s.cfg.MinATRPct {
		return
	}

	for _, leg := range s.setup(df) {
		if s.cfg.Trigger == FibTriggerLimit {
			if s.placeLimit(df, broker, leg, atr) {
				return
			}
			continue
		}
		if !s.rejects(df, leg) {
			continue
		}
		stop, profit, ok := s.exits(leg, closePrice, atr)
		if !ok {
			log.Debugf("[FIBONACCI] %s %s | %s | stop %f or take profit %f on the wrong side of the close %f",
				df.Pair, s.cfg.Timeframe, leg.side, stop, profit, closePrice)
			continue
		}
		log.Debugf("[FIBONACCI] %s %s | %s | impulse %f -> %f", df.Pair, s.cfg.Timeframe, leg.side, leg.origin,
			leg.extreme)
		if s.bracket.openAt(df, broker, leg.side, stop, profit) {
			s.enter(df, leg)
			s.trails[df.Pair] = &signalTrail{trade: s.bracket.trades[df.Pair], extreme: closePrice}
			return
		}
	}
}

// placeLimit places the limit entry of the pullback of the leg at LimitLevel,
// with atr the ATR of the last candle, while the last close is on the side of
// the extreme of it, and reports whether an entry was made: pending, or filled
// at once.
func (s *Fibonacci) placeLimit(df *model.Dataframe, broker service.Broker, leg fibLeg, atr float64) bool {
	limit := leg.level(s.cfg.LimitLevel)
	if rkSign(leg.side)*(df.Close.Last(0)-limit) <= 0 {
		return false // the price already at or beyond it
	}
	stop, profit, ok := s.exits(leg, limit, atr)
	if !ok {
		return false
	}
	orders, held := s.bracket.place(df, broker, leg.side, limit, stop, profit)
	switch {
	case held:
		s.trails[df.Pair] = &signalTrail{trade: s.bracket.trades[df.Pair], extreme: df.Close.Last(0)}
	case len(orders) > 0:
		s.pending[df.Pair] = &fibPending{leg: leg, entry: orders[0], legs: orders[1:]}
	default:
		return false
	}
	s.enter(df, leg)
	return true
}

// checkPending follows the limit entry p of the pair of df: filled, its
// position is followed with its legs; ended at the broker otherwise (eg:
// canceled), it is dropped; still waiting, it is canceled on a position held
// on the pair, which it would add to, a price beyond the extreme of its
// impulse, a close beyond its origin, or after PendingBars candles. A cancel
// that fails is tried again on the next candle.
func (s *Fibonacci) checkPending(df *model.Dataframe, broker service.Broker, p *fibPending) {
	_, held, err := s.bracket.position(df, broker)
	if err != nil {
		log.Error(err)
		return
	}
	if held == p.leg.side {
		delete(s.pending, df.Pair)
		trade := &scalpTrade{side: held}
		for _, leg := range p.legs {
			if current, err := broker.Order(df.Pair, leg.ExchangeID); err == nil {
				current.ID = leg.ID
				leg = current
			}
			if !leg.Status.Final() {
				trade.legs = append(trade.legs, leg)
			}
		}
		s.bracket.trades[df.Pair] = trade
		log.Infof("[FIBONACCI] %s %s | %s LIMIT filled | limit %f", df.Pair, s.cfg.Timeframe, held, p.entry.Price)
		return
	}
	if order, err := broker.Order(df.Pair, p.entry.ExchangeID); err == nil && order.Status.Final() {
		delete(s.pending, df.Pair)
		log.Infof("[FIBONACCI] %s %s | %s LIMIT %s at the broker", df.Pair, s.cfg.Timeframe, p.leg.side,
			order.Status)
		return
	}

	p.bars++
	sign, beyond := rkSign(p.leg.side), df.High.Last(0)
	if p.leg.side == model.SideTypeSell {
		beyond = df.Low.Last(0)
	}
	reason := ""
	switch {
	case held != "":
		reason = "POSITION"
	case sign*(beyond-p.leg.extreme) > 0:
		reason = "EXTENDED"
	case sign*(df.Close.Last(0)-p.leg.origin) < 0:
		reason = "INVALIDATED"
	case s.cfg.PendingBars > 0 && p.bars >= s.cfg.PendingBars:
		reason = "EXPIRED"
	}
	if reason == "" {
		return
	}
	log.Infof("[FIBONACCI] %s %s | %s LIMIT canceled | %s | limit %f", df.Pair, s.cfg.Timeframe, p.leg.side,
		reason, p.entry.Price)
	if err := broker.Cancel(p.entry); err != nil {
		log.Warnf("[FIBONACCI] %s %s | the cancel of the limit: %v", df.Pair, s.cfg.Timeframe, err)
		return
	}
	delete(s.pending, df.Pair)
}

// enter marks the leg of df as entered: its pullback isn't entered again.
func (s *Fibonacci) enter(df *model.Dataframe, leg fibLeg) {
	if s.traded[df.Pair] == nil {
		s.traded[df.Pair] = make(map[fibKey]bool)
	}
	s.traded[df.Pair][s.key(df, leg)] = true
}

// forget drops the impulses entered on the pair of df whose extreme left its
// candles: no longer found, they can't be entered again.
func (s *Fibonacci) forget(df *model.Dataframe) {
	if len(df.Time) == 0 {
		return
	}
	for key := range s.traded[df.Pair] {
		if key.to.Before(df.Time[0]) {
			delete(s.traded[df.Pair], key)
		}
	}
}
