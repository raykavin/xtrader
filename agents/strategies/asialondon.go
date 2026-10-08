package strategies

import (
	"errors"
	"fmt"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

// AsiaLondonConfig holds the inputs of AsiaLondon, with its defaults in
// DefaultAsiaLondonConfig. Periods are counted in candles of Timeframe.
type AsiaLondonConfig struct {
	Timeframe string

	// The sessions, HH:MM in the time zone Timezone, an IANA name, eg:
	// Europe/London: the Asia range is the high and the low of the candles
	// opened from AsiaStart to AsiaEnd, and its breakouts are traded from
	// LondonStart to LondonEnd, when a position still open is closed at the
	// market. London starts at or after the end of Asia, the same day.
	Timezone    string
	AsiaStart   string
	AsiaEnd     string
	LondonStart string
	LondonEnd   string

	// A breakout is the first close beyond the range by BufferATR, in ATR of
	// ATRLen candles: above Asia High + buffer for a BUY, below Asia Low -
	// buffer for a SELL. MaxTrades is the number of positions the strategy
	// opens per London session at most, the ones of the session opened before
	// a restart counted as in ORBConfig.
	BufferATR float64
	MaxTrades int

	// The optional confirmations of the breakouts, each disabled at 0 or
	// false: the Asia range between MinRangeATR and MaxRangeATR; the breakout
	// candle's body BodyATR at least, in its direction; the close on the side
	// of an EMA of TrendLen candles, and of the VWAP from AsiaStart with VWAP;
	// the volume of the breakout candle VolumeMult times its average over the
	// VolumeLen candles before it at least.
	MinRangeATR float64
	MaxRangeATR float64
	BodyATR     float64
	TrendLen    int
	VWAP        bool
	VolumeLen   int
	VolumeMult  float64

	// The exits and the sizing, see BracketConfig. The close back past the
	// middle of the Asia range also closes the position: the breakout failed.
	BracketConfig
}

// DefaultAsiaLondonConfig returns the defaults of AsiaLondon, on the 5m
// timeframe: the range from midnight to 7:00 in London, traded until 10:00.
func DefaultAsiaLondonConfig() AsiaLondonConfig {
	exits := DefaultBracketConfig()
	exits.TakeProfitATR = 2

	return AsiaLondonConfig{
		Timeframe: "5m",

		Timezone:    "Europe/London",
		AsiaStart:   "00:00",
		AsiaEnd:     "07:00",
		LondonStart: "07:00",
		LondonEnd:   "10:00",

		BufferATR: 0.1,
		MaxTrades: 1,

		BodyATR: 0.5,

		BracketConfig: exits,
	}
}

// rules validates the configuration and lays out its sessions.
func (c AsiaLondonConfig) rules() (breakoutRules, error) {
	if err := c.validate(); err != nil {
		return breakoutRules{}, err
	}

	switch {
	case c.Timeframe == "":
		return breakoutRules{}, errors.New("timeframe is required")
	case c.BufferATR < 0:
		return breakoutRules{}, errors.New("buffer ATR multiple can't be negative")
	case c.MaxTrades < 1:
		return breakoutRules{}, errors.New("max trades must be at least 1")
	case c.MinRangeATR < 0 || c.MaxRangeATR < 0:
		return breakoutRules{}, errors.New("range ATR multiples can't be negative")
	case c.MaxRangeATR > 0 && c.MinRangeATR > c.MaxRangeATR:
		return breakoutRules{}, errors.New("the min range ATR multiple must be at most the max one")
	case c.BodyATR < 0:
		return breakoutRules{}, errors.New("body ATR multiple can't be negative")
	case c.TrendLen < 0:
		return breakoutRules{}, errors.New("trend EMA period can't be negative")
	case c.VolumeLen < 0 || c.VolumeMult < 0:
		return breakoutRules{}, errors.New("volume period and multiple can't be negative")
	case c.VolumeLen > 0 && c.VolumeMult == 0:
		return breakoutRules{}, errors.New("the volume filter needs a volume multiple")
	}

	asia, err := newSession(c.AsiaStart, c.AsiaEnd, c.Timezone)
	if err != nil {
		return breakoutRules{}, err
	}
	london, err := newSession(c.LondonStart, c.LondonEnd, c.Timezone)
	if err != nil {
		return breakoutRules{}, err
	}
	// London follows the Asia range of its day
	if asia.length()+asia.end.until(london.start)%rkDay+london.length() > rkDay {
		return breakoutRules{}, errors.New("the London session must end before the next Asia session starts")
	}

	return breakoutRules{
		layout:      rangeBreakout{rng: asia, trade: london},
		bufferATR:   c.BufferATR,
		maxTrades:   c.MaxTrades,
		minRangeATR: c.MinRangeATR,
		maxRangeATR: c.MaxRangeATR,
		bodyATR:     c.BodyATR,
		trendLen:    c.TrendLen,
		vwap:        c.VWAP,
		volumeLen:   c.VolumeLen,
		volumeMult:  c.VolumeMult,
	}, nil
}

// AsiaLondon trades the breakouts of the Asia range in the London session: a
// BUY on the first close above the high of the Asia session, a SELL on the
// first close below its low, with the confirmations of the configuration. See
// AsiaLondonConfig.
type AsiaLondon struct {
	cfg AsiaLondonConfig
	*breakoutTrader
}

// NewAsiaLondon validates the configuration and creates the strategy.
func NewAsiaLondon(cfg AsiaLondonConfig) (*AsiaLondon, error) {
	rules, err := cfg.rules()
	if err != nil {
		return nil, fmt.Errorf("asia london: %w", err)
	}

	trader, err := newBreakoutTrader("ASIA LONDON", "al_", cfg.Timeframe, rules, cfg.BracketConfig)
	if err != nil {
		return nil, fmt.Errorf("asia london: %w", err)
	}
	return &AsiaLondon{cfg: cfg, breakoutTrader: trader}, nil
}

func (s *AsiaLondon) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *AsiaLondon) WarmupPeriod() int {
	return s.warmup
}

func (s *AsiaLondon) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	return s.indicators(df)
}

func (s *AsiaLondon) OnCandle(df *model.Dataframe, broker service.Broker) {
	s.onCandle(df, broker)
}
