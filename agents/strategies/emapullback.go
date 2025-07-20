package strategies

import (
	"errors"
	"fmt"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

// EMAPullbackConfig holds the inputs of EMAPullback, with its defaults in
// DefaultEMAPullbackConfig. Periods are counted in candles of Timeframe.
type EMAPullbackConfig struct {
	Timeframe string

	// The trend: the fast EMA above the slow one over the last TrendBars
	// candles, and higher than TrendBars candles ago, for a BUY; below it and
	// lower for a SELL.
	FastLen   int // must be strictly shorter than SlowLen
	SlowLen   int
	TrendBars int

	// The pullback: a candle of the PullbackBars before the signal one
	// reaching back to the fast EMA, its low PullbackATR above it at most for
	// a BUY, in ATR of ATRLen candles, while the closes hold beyond the slow
	// EMA.
	PullbackBars int
	PullbackATR  float64

	// The confirmation candle closes in the direction of the trend, beyond the
	// fast EMA and beyond the extreme of the previous candle: its high for a
	// BUY. With RSILen above 0, the momentum confirms it too: the RSI of that
	// period above Momentum and rising for a BUY, below 100 - Momentum and
	// falling for a SELL.
	RSILen   int
	Momentum float64

	// The exits and the sizing, see BracketConfig. The close back across the
	// slow EMA also closes the position.
	BracketConfig
}

// DefaultEMAPullbackConfig returns the defaults of EMAPullback, on the 5m
// timeframe.
func DefaultEMAPullbackConfig() EMAPullbackConfig {
	exits := DefaultBracketConfig()
	exits.TakeProfitATR = 2
	exits.MaxBars = 24

	return EMAPullbackConfig{
		Timeframe: "5m",

		FastLen:   20,
		SlowLen:   50,
		TrendBars: 5,

		PullbackBars: 5,
		PullbackATR:  0.25,

		RSILen:   14,
		Momentum: 50,

		BracketConfig: exits,
	}
}

func (c EMAPullbackConfig) validate() error {
	if err := c.BracketConfig.validate(); err != nil {
		return err
	}

	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case c.FastLen < 1 || c.TrendBars < 1 || c.PullbackBars < 1:
		return errors.New("periods must be at least 1")
	case c.FastLen >= c.SlowLen:
		return errors.New("fast EMA period must be strictly shorter than the slow EMA period")
	case c.PullbackATR < 0:
		return errors.New("pullback ATR multiple can't be negative")
	case c.RSILen < 0:
		return errors.New("RSI period can't be negative")
	case c.RSILen > 0 && (c.Momentum <= 0 || c.Momentum >= 100):
		return errors.New("the momentum RSI level must be above 0 and below 100")
	}
	return nil
}

// EMAPullback trades the pullbacks to the fast EMA in a trend of two EMAs: a
// BUY on a candle closing up above the previous high after the price came back
// to the fast EMA, with the fast EMA rising above the slow one and the RSI
// rising above its level; a SELL on the opposite. See EMAPullbackConfig.
type EMAPullback struct {
	cfg     EMAPullbackConfig
	warmup  int
	bracket *bracket
}

// NewEMAPullback validates the configuration and creates the strategy.
func NewEMAPullback(cfg EMAPullbackConfig) (*EMAPullback, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("ema pullback: %w", err)
	}

	// the candles of the trend and the pullback, with the recursive series
	// (the EMAs, the ATR and the RSI) seeded
	return &EMAPullback{
		cfg:     cfg,
		warmup:  seedWarmup(max(cfg.SlowLen, cfg.ATRLen, cfg.RSILen)) + max(cfg.TrendBars, cfg.PullbackBars) + 1,
		bracket: newBracket("EMA PULLBACK", cfg.Timeframe, cfg.BracketConfig),
	}, nil
}

func (s *EMAPullback) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *EMAPullback) WarmupPeriod() int {
	return s.warmup
}

func (s *EMAPullback) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["ep_fast"] = pineEMA(df.Close, s.cfg.FastLen)
	df.Metadata["ep_slow"] = pineEMA(df.Close, s.cfg.SlowLen)
	df.Metadata["ep_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)
	if s.cfg.RSILen > 0 {
		df.Metadata["ep_rsi"] = pineRSI(df.Close, s.cfg.RSILen)
	}

	return []strategy.ChartIndicator{
		{
			Overlay:   true,
			GroupName: "EMA Pullback",
			Time:      df.Time,
			Metrics: []strategy.IndicatorMetric{
				{
					Values: df.Metadata["ep_fast"],
					Name:   fmt.Sprintf("EMA %d", s.cfg.FastLen),
					Color:  "#00bd4e",
					Style:  strategy.StyleLine,
				},
				{
					Values: df.Metadata["ep_slow"],
					Name:   fmt.Sprintf("EMA %d", s.cfg.SlowLen),
					Color:  "#c90032",
					Style:  strategy.StyleLine,
				},
			},
		},
	}
}

// signal returns the entry of the last candle, if any.
func (s *EMAPullback) signal(df *model.Dataframe) model.SideType {
	fast, slow, atr := df.Metadata["ep_fast"], df.Metadata["ep_slow"], df.Metadata["ep_atr"]
	back := max(s.cfg.TrendBars, s.cfg.PullbackBars)
	i := len(df.Close) - 1
	if i < back || len(fast) != len(df.Close) || len(slow) != len(df.Close) || len(atr) != len(df.Close) ||
		len(df.Open) != len(df.Close) {
		return ""
	}

	for _, side := range []model.SideType{model.SideTypeBuy, model.SideTypeSell} {
		sign := rkSign(side)

		// the trend: the fast EMA beyond the slow one, moving away, a missing
		// value failing it
		trend := sign*(fast[i]-fast[i-s.cfg.TrendBars]) > 0
		for k := 0; k < s.cfg.TrendBars && trend; k++ {
			trend = sign*(fast[i-k]-slow[i-k]) > 0
		}

		// the pullback to the fast EMA, the closes beyond the slow one: the
		// low of a long, the high of a short
		extreme := df.Low
		if side == model.SideTypeSell {
			extreme = df.High
		}
		pulled, held := false, true
		for k := 0; k <= s.cfg.PullbackBars && held; k++ {
			held = sign*(df.Close[i-k]-slow[i-k]) > 0
			if k > 0 && sign*(extreme[i-k]-fast[i-k]) <= s.cfg.PullbackATR*atr[i-k] {
				pulled = true
			}
		}

		// the confirmation candle: the previous extreme on the other side, the
		// high of a long
		previous := df.High[i-1]
		if side == model.SideTypeSell {
			previous = df.Low[i-1]
		}
		confirmed := sign*(df.Close[i]-df.Open[i]) > 0 && sign*(df.Close[i]-fast[i]) > 0 &&
			sign*(df.Close[i]-previous) > 0

		if trend && pulled && held && confirmed && s.momentum(df, sign) {
			return side
		}
	}
	return ""
}

// momentum reports whether the RSI is beyond its level on the side of sign and
// moving that way, always without it.
func (s *EMAPullback) momentum(df *model.Dataframe, sign float64) bool {
	if s.cfg.RSILen == 0 {
		return true
	}
	rsi := df.Metadata["ep_rsi"]
	if len(rsi) != len(df.Close) {
		return false
	}
	// the level of the side: Momentum for a long, 100 - Momentum for a short
	level := 50 + sign*(s.cfg.Momentum-50)
	return sign*(rsi.Last(0)-level) > 0 && sign*(rsi.Last(0)-rsi.Last(1)) > 0
}

// exit closes the position on the close back across the slow EMA.
func (s *EMAPullback) exit(df *model.Dataframe, held model.SideType) string {
	if slow := df.Metadata["ep_slow"]; len(slow) == len(df.Close) &&
		rkSign(held)*(df.Close.Last(0)-slow.Last(0)) < 0 {
		return "INVALIDATION"
	}
	return ""
}

func (s *EMAPullback) OnCandle(df *model.Dataframe, broker service.Broker) {
	entry := s.signal(df)
	if !s.bracket.manage(df, broker, s.exit) || entry == "" {
		return
	}
	s.bracket.open(df, broker, entry, df.Metadata["ep_atr"].Last(0))
}
