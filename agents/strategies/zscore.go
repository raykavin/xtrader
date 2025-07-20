package strategies

import (
	"errors"
	"fmt"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

// ZScoreReversionConfig holds the inputs of ZScoreReversion, with its defaults
// in DefaultZScoreReversionConfig. Periods are counted in candles of
// Timeframe.
type ZScoreReversionConfig struct {
	Timeframe string

	// The Z-Score of the close: Z = (close - mean) / standard deviation, the
	// mean the SMA of MeanLen candles and the standard deviation the one of
	// StdLen candles.
	MeanLen int
	StdLen  int

	// The entries: a BUY on Z crossing down to -EntryZ, a SELL on Z crossing
	// up to EntryZ. The position is closed at the market once Z reverts to
	// ExitZ from its side: -ExitZ for a long, ExitZ for a short, 0 at the mean.
	EntryZ float64
	ExitZ  float64

	// The regime filter: the entries only while the ADX of ADXLen candles is
	// under MaxADX, the market ranging. 0 for ADXLen disables it.
	ADXLen int
	MaxADX float64

	// The exits and the sizing, see BracketConfig.
	BracketConfig
}

// DefaultZScoreReversionConfig returns the defaults of ZScoreReversion, on the
// 5m timeframe.
func DefaultZScoreReversionConfig() ZScoreReversionConfig {
	exits := DefaultBracketConfig()
	exits.StopATR = 1.5
	exits.TakeProfitATR = 3
	exits.MaxBars = 36

	return ZScoreReversionConfig{
		Timeframe: "5m",

		MeanLen: 20,
		StdLen:  20,

		EntryZ: 2,
		ExitZ:  0,

		ADXLen: 14,
		MaxADX: 25,

		BracketConfig: exits,
	}
}

func (c ZScoreReversionConfig) validate() error {
	if err := c.BracketConfig.validate(); err != nil {
		return err
	}

	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case c.MeanLen < 1 || c.StdLen < 2:
		return errors.New("the mean period must be at least 1, the standard deviation one at least 2")
	case c.EntryZ <= 0:
		return errors.New("the entry Z-Score must be positive")
	case c.ExitZ <= -c.EntryZ || c.ExitZ >= c.EntryZ:
		return errors.New("the exit Z-Score must be between -EntryZ and EntryZ")
	case c.ADXLen < 0:
		return errors.New("ADX period can't be negative")
	case c.ADXLen > 0 && (c.MaxADX <= 0 || c.MaxADX > 100):
		return errors.New("the max ADX must be above 0 and at most 100")
	}
	return nil
}

// ZScoreReversion trades the reversion of the close to its mean: a BUY when the
// Z-Score of the close crosses down to -EntryZ, a SELL when it crosses up to
// EntryZ, while the ADX shows a ranging market, closed once the Z-Score comes
// back to ExitZ. See ZScoreReversionConfig.
type ZScoreReversion struct {
	cfg     ZScoreReversionConfig
	warmup  int
	bracket *bracket
}

// NewZScoreReversion validates the configuration and creates the strategy.
func NewZScoreReversion(cfg ZScoreReversionConfig) (*ZScoreReversion, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("zscore: %w", err)
	}

	// the cross needs the previous candle, and the recursive series (the ATR
	// and the DMI, whose ADX is smoothed over another ADXLen) their seed
	// washed out
	return &ZScoreReversion{
		cfg:     cfg,
		warmup:  max(cfg.MeanLen, cfg.StdLen, seedWarmup(max(cfg.ATRLen, 2*cfg.ADXLen))) + 1,
		bracket: newBracket("ZSCORE", cfg.Timeframe, cfg.BracketConfig),
	}, nil
}

func (s *ZScoreReversion) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *ZScoreReversion) WarmupPeriod() int {
	return s.warmup
}

func (s *ZScoreReversion) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	mean, deviation := pineSMA(df.Close, s.cfg.MeanLen), pineStdev(df.Close, s.cfg.StdLen)
	z := make(model.Series[float64], len(df.Close))
	for i := range z {
		z[i] = na
		if deviation[i] > 0 {
			z[i] = (df.Close[i] - mean[i]) / deviation[i]
		}
	}
	df.Metadata["zs_mean"] = mean
	df.Metadata["zs_z"] = z
	df.Metadata["zs_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)
	if s.cfg.ADXLen > 0 {
		_, _, df.Metadata["zs_adx"] = pineDMI(df.High, df.Low, df.Close, s.cfg.ADXLen, s.cfg.ADXLen)
	}

	return []strategy.ChartIndicator{
		{
			Overlay:   true,
			GroupName: "Z-Score mean",
			Time:      df.Time,
			Metrics: []strategy.IndicatorMetric{
				{
					Values: mean,
					Name:   fmt.Sprintf("SMA %d", s.cfg.MeanLen),
					Color:  "#fd8700",
					Style:  strategy.StyleLine,
				},
			},
		},
		{
			Overlay:   false,
			GroupName: "Z-Score",
			Time:      df.Time,
			Metrics: []strategy.IndicatorMetric{
				{
					Values: z,
					Name:   "Z",
					Color:  "#8e44ad",
					Style:  strategy.StyleLine,
				},
			},
		},
	}
}

// signal returns the entry of the last candle, if any.
func (s *ZScoreReversion) signal(df *model.Dataframe) model.SideType {
	z := df.Metadata["zs_z"]
	i := len(df.Close) - 1
	if i < 1 || len(z) != len(df.Close) || isNa(z[i]) || isNa(z[i-1]) {
		return ""
	}

	if s.cfg.ADXLen > 0 {
		// a missing ADX fails the filter
		if adx := df.Metadata["zs_adx"]; len(adx) != len(df.Close) || !(adx[i] < s.cfg.MaxADX) {
			return ""
		}
	}

	switch {
	case z[i] <= -s.cfg.EntryZ && z[i-1] > -s.cfg.EntryZ:
		return model.SideTypeBuy
	case z[i] >= s.cfg.EntryZ && z[i-1] < s.cfg.EntryZ:
		return model.SideTypeSell
	}
	return ""
}

// exit closes the position once the Z-Score reverted to ExitZ.
func (s *ZScoreReversion) exit(df *model.Dataframe, held model.SideType) string {
	if z := df.Metadata["zs_z"]; len(z) == len(df.Close) && rkSign(held)*z.Last(0) >= -s.cfg.ExitZ {
		return "MEAN"
	}
	return ""
}

func (s *ZScoreReversion) OnCandle(df *model.Dataframe, broker service.Broker) {
	entry := s.signal(df)
	if !s.bracket.manage(df, broker, s.exit) || entry == "" {
		return
	}
	s.bracket.open(df, broker, entry, df.Metadata["zs_atr"].Last(0))
}
