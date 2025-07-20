package strategies

import (
	"errors"
	"fmt"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

// ORBConfig holds the inputs of ORB, with its defaults in DefaultORBConfig.
// Periods are counted in candles of Timeframe.
type ORBConfig struct {
	Timeframe string

	// The session, in the time zone Timezone, an IANA name, eg:
	// America/New_York: the opening range is the high and the low of the
	// candles opened over the OpenRange from SessionStart, HH:MM, and its
	// breakouts are traded from then until SessionEnd, HH:MM, when a position
	// still open is closed at the market.
	Timezone     string
	SessionStart string
	OpenRange    time.Duration
	SessionEnd   string

	// A breakout is the first close beyond the range by BufferATR, in ATR of
	// ATRLen candles, and MaxTrades is the number of positions opened per
	// session at most.
	BufferATR float64
	MaxTrades int

	// The filters of the breakouts, each disabled at 0 or false:
	// MaxRangeATR skips the sessions whose range is wider than that many ATR,
	// the move already done; TrendLen is the period of an EMA the close must
	// be on the side of; VWAP needs the close on the side of the VWAP of the
	// session, from SessionStart.
	MaxRangeATR float64
	TrendLen    int
	VWAP        bool

	// The exits and the sizing, see BracketConfig. The close back past the
	// middle of the range also closes the position: the breakout failed.
	BracketConfig
}

// DefaultORBConfig returns the defaults of ORB: the first 15 minutes of the
// New York session, on the 5m timeframe, traded until noon.
func DefaultORBConfig() ORBConfig {
	exits := DefaultBracketConfig()
	exits.TakeProfitATR = 2

	return ORBConfig{
		Timeframe: "5m",

		Timezone:     "America/New_York",
		SessionStart: "09:30",
		OpenRange:    15 * time.Minute,
		SessionEnd:   "12:00",

		BufferATR: 0.1,
		MaxTrades: 1,

		VWAP: true,

		BracketConfig: exits,
	}
}

// rules validates the configuration and lays out its sessions.
func (c ORBConfig) rules() (breakoutRules, error) {
	if err := c.validate(); err != nil {
		return breakoutRules{}, err
	}

	switch {
	case c.Timeframe == "":
		return breakoutRules{}, errors.New("timeframe is required")
	case c.OpenRange <= 0 || c.OpenRange%time.Minute != 0:
		return breakoutRules{}, errors.New("the opening range must be a positive number of minutes")
	case c.BufferATR < 0:
		return breakoutRules{}, errors.New("buffer ATR multiple can't be negative")
	case c.MaxTrades < 1:
		return breakoutRules{}, errors.New("max trades must be at least 1")
	case c.MaxRangeATR < 0:
		return breakoutRules{}, errors.New("max range ATR multiple can't be negative")
	case c.TrendLen < 0:
		return breakoutRules{}, errors.New("trend EMA period can't be negative")
	}

	trade, err := newSession(c.SessionStart, c.SessionEnd, c.Timezone)
	if err != nil {
		return breakoutRules{}, err
	}
	if c.OpenRange >= trade.length() {
		return breakoutRules{}, errors.New("the opening range must end before the session")
	}

	// the range, then its breakouts until the end of the session
	rng := session{start: trade.start, end: (trade.start + dayTime(c.OpenRange)) % dayTime(rkDay), loc: trade.loc}
	trade.start = rng.end
	return breakoutRules{
		layout:      rangeBreakout{rng: rng, trade: trade},
		bufferATR:   c.BufferATR,
		maxTrades:   c.MaxTrades,
		maxRangeATR: c.MaxRangeATR,
		trendLen:    c.TrendLen,
		vwap:        c.VWAP,
	}, nil
}

// ORB trades the breakouts of the opening range of a session: a BUY on the
// first close above the high of the candles of its first minutes, a SELL on
// the first close below their low, with the filters of the configuration. See
// ORBConfig.
type ORB struct {
	cfg ORBConfig
	*breakoutTrader
}

// NewORB validates the configuration and creates the strategy.
func NewORB(cfg ORBConfig) (*ORB, error) {
	rules, err := cfg.rules()
	if err != nil {
		return nil, fmt.Errorf("orb: %w", err)
	}

	trader, err := newBreakoutTrader("ORB", "orb_", cfg.Timeframe, rules, cfg.BracketConfig)
	if err != nil {
		return nil, fmt.Errorf("orb: %w", err)
	}
	return &ORB{cfg: cfg, breakoutTrader: trader}, nil
}

func (s *ORB) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *ORB) WarmupPeriod() int {
	return s.warmup
}

func (s *ORB) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	return s.indicators(df)
}

func (s *ORB) OnCandle(df *model.Dataframe, broker service.Broker) {
	s.onCandle(df, broker)
}
