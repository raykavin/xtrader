package strategies

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

// LiquiditySweepConfig holds the inputs of LiquiditySweep, with its defaults in
// DefaultLiquiditySweepConfig. Periods are counted in candles of Timeframe.
type LiquiditySweepConfig struct {
	Timeframe string

	// The levels where the liquidity rests: the swing highs and lows, the
	// candles beyond the SwingLen ones on each side of them, of the last
	// Lookback candles, not taken out since. With DayLevels, the high and the
	// low of the previous day too, the day starting at midnight in the time
	// zone Timezone, an IANA name, eg: UTC.
	SwingLen  int
	Lookback  int
	DayLevels bool
	Timezone  string

	// The sweep: a wick beyond a level by MinSweepATR at least and by
	// MaxSweepATR at most, 0 for no limit, beyond which it is a breakout, then
	// the close back on the other side of the level within SweepBars candles
	// of the wick. The reversal confirms it: the close in its direction, and
	// ConfirmATR from the extreme of the sweep at least. The thresholds are in
	// ATR of ATRLen candles.
	MinSweepATR float64
	MaxSweepATR float64
	SweepBars   int
	ConfirmATR  float64

	// The exits and the sizing, see BracketConfig.
	BracketConfig
}

// DefaultLiquiditySweepConfig returns the defaults of LiquiditySweep, on the 5m
// timeframe.
func DefaultLiquiditySweepConfig() LiquiditySweepConfig {
	exits := DefaultBracketConfig()
	exits.TakeProfitATR = 2
	exits.MaxBars = 24

	return LiquiditySweepConfig{
		Timeframe: "5m",

		SwingLen: 3,
		Lookback: 50,
		Timezone: "UTC",

		MinSweepATR: 0.05,
		MaxSweepATR: 1.5,
		SweepBars:   3,
		ConfirmATR:  0.5,

		BracketConfig: exits,
	}
}

func (c LiquiditySweepConfig) validate() error {
	if err := c.BracketConfig.validate(); err != nil {
		return err
	}

	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case c.SwingLen < 1 || c.SweepBars < 1:
		return errors.New("periods must be at least 1")
	case c.Lookback <= 2*c.SwingLen:
		return errors.New("the lookback must hold a swing: more than twice the swing period")
	case c.MinSweepATR < 0 || c.MaxSweepATR < 0 || c.ConfirmATR < 0:
		return errors.New("the sweep ATR thresholds can't be negative")
	case c.MaxSweepATR > 0 && c.MinSweepATR >= c.MaxSweepATR:
		return errors.New("the min sweep must be below the max one")
	}
	return nil
}

// LiquiditySweep trades the reversals after a sweep of the liquidity resting
// beyond a swing, or the previous day: a BUY on the close back above a low
// its wick took out, a SELL on the close back below a high its wick took out.
// The market structure is read from the candles of the window. See
// LiquiditySweepConfig.
type LiquiditySweep struct {
	cfg     LiquiditySweepConfig
	loc     *time.Location // of the day levels, nil without them
	warmup  int
	bracket *bracket
}

// NewLiquiditySweep validates the configuration and creates the strategy.
func NewLiquiditySweep(cfg LiquiditySweepConfig) (*LiquiditySweep, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("liquidity sweep: %w", err)
	}

	// the swings of the lookback before the first candle of the sweep, and the
	// seed of the ATR washed out
	s := &LiquiditySweep{
		cfg:     cfg,
		warmup:  max(cfg.Lookback+cfg.SweepBars, seedWarmup(cfg.ATRLen)) + 1,
		bracket: newBracket("SWEEP", cfg.Timeframe, cfg.BracketConfig),
	}
	if cfg.DayLevels {
		day, err := newSession("00:00", "00:00", cfg.Timezone)
		if err != nil {
			return nil, fmt.Errorf("liquidity sweep: %w", err)
		}
		// the window reaches back to the start of the previous day
		candles, _, err := timeframeCandles(cfg.Timeframe, 2*rkDay+time.Hour)
		if err != nil {
			return nil, fmt.Errorf("liquidity sweep: %w", err)
		}
		s.loc, s.warmup = day.loc, max(s.warmup, candles+cfg.SweepBars+1)
	}
	return s, nil
}

func (s *LiquiditySweep) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *LiquiditySweep) WarmupPeriod() int {
	return s.warmup
}

func (s *LiquiditySweep) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["ls_swing_high"] = pivotHigh(df.High, s.cfg.SwingLen)
	df.Metadata["ls_swing_low"] = pivotLow(df.Low, s.cfg.SwingLen)
	df.Metadata["ls_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)

	return []strategy.ChartIndicator{
		{
			Overlay:   true,
			GroupName: "Liquidity Sweep",
			Time:      df.Time,
			Metrics: []strategy.IndicatorMetric{
				{
					Values: df.Metadata["ls_swing_high"],
					Name:   "Swing high",
					Color:  "#c90032",
					Style:  strategy.StyleScatter,
				},
				{
					Values: df.Metadata["ls_swing_low"],
					Name:   "Swing low",
					Color:  "#00bd4e",
					Style:  strategy.StyleScatter,
				},
			},
		},
	}
}

// signal returns the entry of the last candle, if any, and the level swept.
func (s *LiquiditySweep) signal(df *model.Dataframe) (model.SideType, float64) {
	atrs := df.Metadata["ls_atr"]
	i := len(df.Close) - 1
	if i < 1 || len(atrs) != len(df.Close) || len(df.Open) != len(df.Close) ||
		len(df.Metadata["ls_swing_high"]) != len(df.Close) || len(df.Metadata["ls_swing_low"]) != len(df.Close) ||
		isNa(atrs[i]) {
		return "", 0
	}

	for _, side := range []model.SideType{model.SideTypeBuy, model.SideTypeSell} {
		sign := rkSign(side)
		// the reversal candle, in the direction of the entry
		if sign*(df.Close[i]-df.Open[i]) <= 0 {
			continue
		}

		// the wick of the sweep, on the last SweepBars candles
		for j := i; j > i-s.cfg.SweepBars && j > 0; j-- {
			if level, ok := s.swept(df, side, j); ok {
				return side, level
			}
		}
	}
	return "", 0
}

// swept returns the level of side swept by the wick of candle j, reclaimed and
// confirmed by the last close: a low for a BUY, a high for a SELL.
func (s *LiquiditySweep) swept(df *model.Dataframe, side model.SideType, j int) (float64, bool) {
	sign, atrs := rkSign(side), df.Metadata["ls_atr"]
	i := len(df.Close) - 1
	extreme := s.wicks(df, side)

	// the extreme of the sweep, from its wick to the last candle
	far := extreme[j]
	for m := j; m <= i; m++ {
		far = math.Min(sign*far, sign*extreme[m]) * sign
	}
	if sign*(df.Close[i]-far) < s.cfg.ConfirmATR*atrs[i] {
		return 0, false
	}

	// the deepest level taken by the wick, within the limits of a sweep
	best, found := 0.0, false
	for _, level := range s.levels(df, side, j) {
		depth := sign * (level - extreme[j])
		if isNa(atrs[j]) || depth <= 0 || depth < s.cfg.MinSweepATR*atrs[j] ||
			s.cfg.MaxSweepATR > 0 && depth > s.cfg.MaxSweepATR*atrs[j] {
			continue
		}

		// the last close is the first one back on the other side of the level
		reclaimed := sign*(df.Close[i]-level) > 0
		for m := j; m < i && reclaimed; m++ {
			reclaimed = sign*(df.Close[m]-level) <= 0
		}
		if reclaimed && (!found || sign*level < sign*best) {
			best, found = level, true
		}
	}
	return best, found
}

// wicks returns the extremes of the candles on the side of a sweep before an
// entry of side: the lows for a BUY, the highs for a SELL.
func (s *LiquiditySweep) wicks(df *model.Dataframe, side model.SideType) []float64 {
	if side == model.SideTypeSell {
		return df.High
	}
	return df.Low
}

// levels returns the levels of side resting before candle j, not taken out
// since: the swing lows for a BUY, the swing highs for a SELL, and the low or
// the high of the previous day with DayLevels.
func (s *LiquiditySweep) levels(df *model.Dataframe, side model.SideType, j int) []float64 {
	sign, extreme := rkSign(side), s.wicks(df, side)
	swings := df.Metadata["ls_swing_low"]
	if side == model.SideTypeSell {
		swings = df.Metadata["ls_swing_high"]
	}

	// untaken reports whether no candle from index from to j excluded went
	// beyond level
	untaken := func(level float64, from int) bool {
		for m := from; m < j; m++ {
			if sign*(level-extreme[m]) > 0 {
				return false
			}
		}
		return true
	}

	var levels []float64
	// a swing is known SwingLen candles after it, before the wick
	for k := max(0, j-s.cfg.Lookback); k <= j-1-s.cfg.SwingLen; k++ {
		if !isNa(swings[k]) && untaken(swings[k], k+1) {
			levels = append(levels, swings[k])
		}
	}

	if s.loc != nil {
		day := dayTime(0).last(df.Time[j], s.loc)
		previous := dayTime(0).last(day.Add(-time.Nanosecond), s.loc)
		if !df.Time[0].After(previous) {
			level, first := na, j
			for m, t := range df.Time[:j] {
				if t.Before(previous) {
					continue
				}
				if !t.Before(day) {
					first = min(first, m)
					continue
				}
				if isNa(level) || sign*extreme[m] < sign*level {
					level = extreme[m]
				}
			}
			if !isNa(level) && untaken(level, first) {
				levels = append(levels, level)
			}
		}
	}
	return levels
}

func (s *LiquiditySweep) OnCandle(df *model.Dataframe, broker service.Broker) {
	entry, level := s.signal(df)
	// no exit of its own: the stop loss, the take profit and the time
	if !s.bracket.manage(df, broker, func(*model.Dataframe, model.SideType) string { return "" }) || entry == "" {
		return
	}

	log.Debugf("[SWEEP] %s %s | %s | level %f", df.Pair, s.cfg.Timeframe, entry, level)
	s.bracket.open(df, broker, entry, df.Metadata["ls_atr"].Last(0))
}
