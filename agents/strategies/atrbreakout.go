package strategies

import (
	"errors"
	"fmt"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

// ATRBreakoutConfig holds the inputs of ATRBreakout, with its defaults in
// DefaultATRBreakoutConfig. Periods are counted in candles of Timeframe.
type ATRBreakoutConfig struct {
	Timeframe string

	// The expansion of the volatility: the ATR of ATRLen candles Expansion
	// times its average at least, the SMA of the ATR over the BaselineLen
	// candles before the signal candle.
	BaselineLen int
	Expansion   float64

	// The breakout: the close beyond the highest high, for a BUY, or the
	// lowest low, for a SELL, of the BreakoutLen candles before it by
	// BufferATR, in ATR. The directional candle confirms it: its body is
	// BodyATR at least, in the direction of the breakout.
	BreakoutLen int
	BufferATR   float64
	BodyATR     float64

	// The exits and the sizing, see BracketConfig.
	BracketConfig
}

// DefaultATRBreakoutConfig returns the defaults of ATRBreakout, on the 5m
// timeframe.
func DefaultATRBreakoutConfig() ATRBreakoutConfig {
	exits := DefaultBracketConfig()
	exits.StopATR = 1.5
	exits.TakeProfitATR = 2.5
	exits.MaxBars = 12

	return ATRBreakoutConfig{
		Timeframe: "5m",

		BaselineLen: 50,
		Expansion:   1.2,

		BreakoutLen: 20,
		BodyATR:     0.8,

		BracketConfig: exits,
	}
}

func (c ATRBreakoutConfig) validate() error {
	if err := c.BracketConfig.validate(); err != nil {
		return err
	}

	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case c.BaselineLen < 1 || c.BreakoutLen < 1:
		return errors.New("periods must be at least 1")
	case c.Expansion <= 0:
		return errors.New("the expansion must be positive: a multiple of the average ATR")
	case c.BufferATR < 0 || c.BodyATR < 0:
		return errors.New("the buffer and body ATR multiples can't be negative")
	}
	return nil
}

// ATRBreakout trades the breakouts of the recent range on an expansion of the
// volatility: a BUY on a candle closing above the highest high of the last
// candles with a large body up while the ATR is well above its average, a SELL
// on one closing below their lowest low with a large body down. See
// ATRBreakoutConfig.
type ATRBreakout struct {
	cfg     ATRBreakoutConfig
	warmup  int
	bracket *bracket
}

// NewATRBreakout validates the configuration and creates the strategy.
func NewATRBreakout(cfg ATRBreakoutConfig) (*ATRBreakout, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("atr breakout: %w", err)
	}

	// the average of the seeded ATR up to the previous candle, and the
	// channel of the candles before the last one
	return &ATRBreakout{
		cfg:     cfg,
		warmup:  max(seedWarmup(cfg.ATRLen)+cfg.BaselineLen, cfg.BreakoutLen) + 1,
		bracket: newBracket("ATR BREAKOUT", cfg.Timeframe, cfg.BracketConfig),
	}, nil
}

func (s *ATRBreakout) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *ATRBreakout) WarmupPeriod() int {
	return s.warmup
}

func (s *ATRBreakout) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["ab_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)
	df.Metadata["ab_atr_avg"] = pineSMA(df.Metadata["ab_atr"], s.cfg.BaselineLen)
	df.Metadata["ab_high"] = pineHighest(df.High, s.cfg.BreakoutLen)
	df.Metadata["ab_low"] = pineLowest(df.Low, s.cfg.BreakoutLen)

	return []strategy.ChartIndicator{
		{
			Overlay:   true,
			GroupName: fmt.Sprintf("Channel %d", s.cfg.BreakoutLen),
			Time:      df.Time,
			Metrics: []strategy.IndicatorMetric{
				{
					Values: df.Metadata["ab_high"],
					Name:   "Highest high",
					Color:  "#00bd4e",
					Style:  strategy.StyleLine,
				},
				{
					Values: df.Metadata["ab_low"],
					Name:   "Lowest low",
					Color:  "#c90032",
					Style:  strategy.StyleLine,
				},
			},
		},
		{
			Overlay:   false,
			GroupName: fmt.Sprintf("ATR %d", s.cfg.ATRLen),
			Time:      df.Time,
			Metrics: []strategy.IndicatorMetric{
				{
					Values: df.Metadata["ab_atr"],
					Name:   "ATR",
					Color:  "#fd8700",
					Style:  strategy.StyleLine,
				},
				{
					Values: df.Metadata["ab_atr_avg"],
					Name:   fmt.Sprintf("SMA %d", s.cfg.BaselineLen),
					Color:  "#8e44ad",
					Style:  strategy.StyleLine,
				},
			},
		},
	}
}

// signal returns the entry of the last candle, if any.
func (s *ATRBreakout) signal(df *model.Dataframe) model.SideType {
	atrs, average := df.Metadata["ab_atr"], df.Metadata["ab_atr_avg"]
	high, low := df.Metadata["ab_high"], df.Metadata["ab_low"]
	i := len(df.Close) - 1
	if i < 1 || len(atrs) != len(df.Close) || len(average) != len(df.Close) || len(high) != len(df.Close) ||
		len(low) != len(df.Close) || len(df.Open) != len(df.Close) || isNa(atrs[i]) {
		return ""
	}

	// the volatility expands: a missing average fails it
	atr := atrs[i]
	if !(atr >= s.cfg.Expansion*average[i-1]) {
		return ""
	}

	// the channel of the candles before the last one
	var entry model.SideType
	buffer, closePrice := s.cfg.BufferATR*atr, df.Close[i]
	switch {
	case closePrice > high[i-1]+buffer:
		entry = model.SideTypeBuy
	case closePrice < low[i-1]-buffer:
		entry = model.SideTypeSell
	default:
		return ""
	}

	if body := rkSign(entry) * (closePrice - df.Open[i]); body <= 0 || body < s.cfg.BodyATR*atr {
		return ""
	}
	return entry
}

func (s *ATRBreakout) OnCandle(df *model.Dataframe, broker service.Broker) {
	entry := s.signal(df)
	// no exit of its own: the stop loss, the take profit and the time
	if !s.bracket.manage(df, broker, func(*model.Dataframe, model.SideType) string { return "" }) || entry == "" {
		return
	}
	s.bracket.open(df, broker, entry, df.Metadata["ab_atr"].Last(0))
}
