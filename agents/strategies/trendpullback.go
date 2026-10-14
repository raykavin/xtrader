package strategies

import (
	"errors"
	"fmt"
	"math"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

// TrendPullbackConfig holds the inputs of TrendPullback, with its defaults in
// DefaultTrendPullbackConfig: the rules of the setup trend_pullback of the
// prompt default of the LLM agent, their numbers set as the user approved
// them; its setup free, the context weighed by the model, has no rules to
// code. Periods are counted in candles of Timeframe. The levels of the context
// (the higher timeframes, the reference levels) and the economic calendar are
// left out.
type TrendPullbackConfig struct {
	Timeframe string

	// The trend: the EMA of FastLen candles beyond the one of SlowLen, the close
	// beyond the SMA of TrendLen candles, the ADX of ADXLen candles at MinADX at
	// least.
	FastLen  int
	SlowLen  int
	TrendLen int
	ADXLen   int
	MinADX   float64

	// The pullback and its end: over the last PullbackBars candles the low back
	// at the fast EMA (the high for a short) without a close beyond the SMA of
	// the trend; then the candle closes beyond the fast EMA, the MACD histogram
	// (MACDFast, MACDSlow, MACDSignal) turning its way or the RSI of RSILen
	// candles crossing 50 its way, the RSI at MaxRSI at most for a long (at
	// 100-MaxRSI at least for a short).
	PullbackBars int
	MACDFast     int
	MACDSlow     int
	MACDSignal   int
	RSILen       int
	MaxRSI       float64

	// The exits: the stop loss beyond the extreme of the pullback, StopMinATR
	// ATR away at least; the take profit at the highest high of the last
	// TargetBars candles (the lowest low for a short), TargetR times the risk
	// without one beyond the close; no entry offering less than MinRR. The stop
	// loss to the entry from BreakEvenR; from TrailR behind the last swing low
	// of SwingPivot candles on each side (high for a short) or TrailATR ATR
	// behind the close, the nearer; closed on a close beyond the slow EMA
	// against it.
	StopMinATR float64
	TargetBars int
	TargetR    float64
	MinRR      float64
	BreakEvenR float64
	TrailR     float64
	TrailATR   float64
	SwingPivot int

	// The ATR, the cost filter and the sizing, see BracketConfig: its stop
	// loss, take profit and max bars aren't read, the strategy sets its own.
	BracketConfig
}

// DefaultTrendPullbackConfig returns the defaults of TrendPullback, on the 1h
// timeframe.
func DefaultTrendPullbackConfig() TrendPullbackConfig {
	return TrendPullbackConfig{
		Timeframe: "1h",

		FastLen:  9,
		SlowLen:  21,
		TrendLen: 200,
		ADXLen:   14,
		MinADX:   20,

		PullbackBars: 5,
		MACDFast:     12,
		MACDSlow:     26,
		MACDSignal:   9,
		RSILen:       14,
		MaxRSI:       70,

		StopMinATR: 1,
		TargetBars: 50,
		TargetR:    2,
		MinRR:      1.5,
		BreakEvenR: 1,
		TrailR:     2,
		TrailATR:   1,
		SwingPivot: 5,

		BracketConfig: DefaultBracketConfig(),
	}
}

func (c TrendPullbackConfig) validate() error {
	if err := c.BracketConfig.validate(); err != nil {
		return err
	}
	for _, period := range []int{c.FastLen, c.SlowLen, c.TrendLen, c.ADXLen, c.PullbackBars, c.MACDFast, c.MACDSlow,
		c.MACDSignal, c.RSILen, c.TargetBars, c.SwingPivot} {
		if period < 1 {
			return errors.New("periods must be at least 1")
		}
	}
	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case c.FastLen >= c.SlowLen || c.MACDFast >= c.MACDSlow:
		return errors.New("the fast periods must be shorter than the slow ones")
	case c.MinADX < 0 || c.MinADX > 100 || c.MaxRSI <= 50 || c.MaxRSI >= 100:
		return errors.New("the min ADX must be from 0 to 100, the max RSI between 50 and 100")
	case c.StopMinATR <= 0 || c.TargetR <= 0 || c.MinRR <= 0:
		return errors.New("the stop loss, the target and the risk reward must be positive")
	case c.BreakEvenR < 0 || c.TrailR < 0 || c.TrailATR < 0:
		return errors.New("the moves of the stop loss can't be negative")
	}
	return nil
}

// TrendPullback trades the end of the pullbacks of a trend: a BUY when the
// close resumes the trend of the EMAs over the SMA of the trend after a
// pullback to the fast EMA, confirmed by the momentum, a SELL in the mirror
// case. See TrendPullbackConfig.
type TrendPullback struct {
	cfg     TrendPullbackConfig
	warmup  int
	bracket *bracket
	plan    rPlan
}

// NewTrendPullback validates the configuration and creates the strategy.
func NewTrendPullback(cfg TrendPullbackConfig) (*TrendPullback, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("trendpullback: %w", err)
	}
	s := &TrendPullback{
		cfg: cfg,
		warmup: max(cfg.TrendLen, cfg.TargetBars+1, cfg.PullbackBars+2, 2*cfg.SwingPivot+2,
			seedWarmup(max(cfg.ATRLen, 2*cfg.ADXLen, cfg.SlowLen, cfg.MACDSlow+cfg.MACDSignal, cfg.RSILen))) + 1,
		bracket: newBracket("TRENDPULLBACK", cfg.Timeframe, cfg.BracketConfig),
	}
	s.plan = rPlan{breakEvenR: cfg.BreakEvenR, trailR: cfg.TrailR, trail: s.trailLevel}
	return s, nil
}

func (s *TrendPullback) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *TrendPullback) WarmupPeriod() int {
	return s.warmup
}

func (s *TrendPullback) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["tp_fast"] = pineEMA(df.Close, s.cfg.FastLen)
	df.Metadata["tp_slow"] = pineEMA(df.Close, s.cfg.SlowLen)
	df.Metadata["tp_trend"] = pineSMA(df.Close, s.cfg.TrendLen)
	df.Metadata["tp_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)
	df.Metadata["tp_rsi"] = pineRSI(df.Close, s.cfg.RSILen)
	_, _, df.Metadata["tp_hist"] = pineMACD(df.Close, s.cfg.MACDFast, s.cfg.MACDSlow, s.cfg.MACDSignal)
	_, _, df.Metadata["tp_adx"] = pineDMI(df.High, df.Low, df.Close, s.cfg.ADXLen, s.cfg.ADXLen)
	return overlay(df, "Trend", s.cfg.SlowLen,
		line(df.Metadata["tp_fast"], fmt.Sprintf("EMA %d", s.cfg.FastLen), colorLine),
		line(df.Metadata["tp_slow"], fmt.Sprintf("EMA %d", s.cfg.SlowLen), colorSlow),
		line(df.Metadata["tp_trend"], fmt.Sprintf("SMA %d", s.cfg.TrendLen), colorOther))
}

// signal returns the entry of the last candle, if any, with its stop loss and
// take profit.
func (s *TrendPullback) signal(df *model.Dataframe) (model.SideType, float64, float64) {
	m, ok := meta(df, "tp_fast", "tp_slow", "tp_trend", "tp_atr", "tp_rsi", "tp_hist", "tp_adx")
	i := len(df.Close) - 1
	if !ok || i < max(s.cfg.PullbackBars, s.cfg.TargetBars, 2) {
		return "", 0, 0
	}
	fast, slow, trend, hist, rsi := m["tp_fast"], m["tp_slow"], m["tp_trend"], m["tp_hist"], m["tp_rsi"]
	closePrice, atr := df.Close[i], m["tp_atr"][i]
	for _, key := range []string{"tp_fast", "tp_slow", "tp_trend", "tp_atr", "tp_rsi", "tp_adx"} {
		if isNa(m[key][i]) {
			return "", 0, 0
		}
	}
	if isNa(hist[i]) || isNa(hist[i-1]) || isNa(hist[i-2]) || isNa(rsi[i-1]) ||
		!(m["tp_adx"][i] >= s.cfg.MinADX) || s.bracket.belowCost(closePrice, atr) {
		return "", 0, 0
	}

	for _, side := range []model.SideType{model.SideTypeBuy, model.SideTypeSell} {
		sign := rkSign(side)
		if !(sign*(fast[i]-slow[i]) > 0) || !(sign*(closePrice-trend[i]) > 0) || !(sign*(closePrice-fast[i]) > 0) {
			continue
		}
		touched, held := false, true
		for j := i - s.cfg.PullbackBars; j <= i; j++ {
			reach := df.Low[j]
			if side == model.SideTypeSell {
				reach = df.High[j]
			}
			if j < i {
				touched = touched || sign*(reach-fast[j]) <= 0
			}
			held = held && sign*(df.Close[j]-trend[j]) >= 0
		}
		turned := sign*(hist[i]-hist[i-1]) > 0 && sign*(hist[i-1]-hist[i-2]) <= 0
		crossed := sign*(rsi[i]-50) > 0 && sign*(rsi[i-1]-50) <= 0
		chase := side == model.SideTypeBuy && rsi[i] > s.cfg.MaxRSI || side == model.SideTypeSell &&
			rsi[i] < 100-s.cfg.MaxRSI
		if !touched || !held || !turned && !crossed || chase {
			continue
		}

		extreme := pineLowest(df.Low, s.cfg.PullbackBars+1)[i]
		target := pineHighest(df.High[:i], s.cfg.TargetBars)
		if side == model.SideTypeSell {
			extreme = pineHighest(df.High, s.cfg.PullbackBars+1)[i]
			target = pineLowest(df.Low[:i], s.cfg.TargetBars)
		}
		risk := math.Max(sign*(closePrice-extreme), s.cfg.StopMinATR*atr)
		stop := closePrice - sign*risk
		profit := target[len(target)-1]
		if isNa(profit) || sign*(profit-closePrice) <= 0 {
			profit = closePrice + sign*s.cfg.TargetR*risk // no level of the candles beyond the close
		}
		if !enoughReward(side, closePrice, stop, profit, s.cfg.MinRR) {
			continue
		}
		return side, stop, profit
	}
	return "", 0, 0
}

// trailLevel returns the level of the trailing stop loss of a position of
// side: the nearer of the last swing and TrailATR ATR behind the close.
func (s *TrendPullback) trailLevel(df *model.Dataframe, side model.SideType) float64 {
	atr := df.Metadata["tp_atr"]
	if len(atr) != len(df.Close) || isNa(atr.Last(0)) {
		return na
	}
	sign := rkSign(side)
	level := df.Close.Last(0) - sign*s.cfg.TrailATR*atr.Last(0)
	if swing := lastSwing(df, side, s.cfg.SwingPivot); !isNa(swing) && sign*(swing-level) > 0 {
		level = swing
	}
	return level
}

// exit closes the position on a close beyond the slow EMA against it: the trend
// that justified it broken.
func (s *TrendPullback) exit(df *model.Dataframe, held model.SideType) string {
	if slow := df.Metadata["tp_slow"]; len(slow) == len(df.Close) && rkSign(held)*(df.Close.Last(0)-slow.Last(0)) < 0 {
		return "TREND"
	}
	return ""
}

func (s *TrendPullback) OnCandle(df *model.Dataframe, broker service.Broker) {
	s.bracket.trackR(df)
	entry, stop, profit := s.signal(df)
	if !s.bracket.manage(df, broker, s.exit) {
		s.bracket.moveR(df, broker, s.plan)
		return
	}
	if entry != "" {
		s.bracket.openAt(df, broker, entry, stop, profit)
	}
}
