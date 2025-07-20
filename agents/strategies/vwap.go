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

// VWAPReclaimConfig holds the inputs of VWAPReclaim, with its defaults in
// DefaultVWAPReclaimConfig. Periods are counted in candles of Timeframe.
type VWAPReclaimConfig struct {
	Timeframe string

	// The VWAP is anchored to Anchor, HH:MM in the time zone Timezone, an IANA
	// name, eg: UTC: it starts over there every day.
	Timezone string
	Anchor   string

	// The setups traded, at least one: Reclaim, the close crossing the VWAP
	// after ConfirmBars closes on the other side of it, and ending ReclaimATR
	// beyond it at least; Reject, the price coming back to the VWAP after
	// ConfirmBars closes on one side of it, with a wick RejectATR from it at
	// most, and closing on that side again, the wick RejectWick of the range
	// of the candle at least. The thresholds are in ATR of ATRLen candles.
	Reclaim     bool
	Reject      bool
	ConfirmBars int
	ReclaimATR  float64
	RejectATR   float64
	RejectWick  float64

	// The signal candle confirms the setup: it closes in its direction, and
	// with RSILen above 0 the RSI of that period is above 50 for a BUY and
	// below 50 for a SELL, the momentum.
	RSILen int

	// The exits and the sizing, see BracketConfig. The close back across the
	// VWAP also closes the position.
	BracketConfig
}

// DefaultVWAPReclaimConfig returns the defaults of VWAPReclaim, on the 5m
// timeframe with the VWAP of the UTC day.
func DefaultVWAPReclaimConfig() VWAPReclaimConfig {
	exits := DefaultBracketConfig()
	exits.MaxBars = 24

	return VWAPReclaimConfig{
		Timeframe: "5m",

		Timezone: "UTC",
		Anchor:   "00:00",

		Reclaim:     true,
		Reject:      true,
		ConfirmBars: 3,
		ReclaimATR:  0.1,
		RejectATR:   0.1,
		RejectWick:  0.4,

		RSILen: 14,

		BracketConfig: exits,
	}
}

func (c VWAPReclaimConfig) validate() error {
	if err := c.BracketConfig.validate(); err != nil {
		return err
	}

	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case !c.Reclaim && !c.Reject:
		return errors.New("no setup to trade: enable reclaim, reject or both")
	case c.ConfirmBars < 1:
		return errors.New("confirmation bars must be at least 1")
	case c.ReclaimATR < 0 || c.RejectATR < 0:
		return errors.New("the reclaim and reject ATR thresholds can't be negative")
	case c.RejectWick < 0 || c.RejectWick > 1:
		return errors.New("the reject wick must be a share of the candle range, from 0 to 1")
	case c.RSILen < 0:
		return errors.New("RSI period can't be negative")
	}
	return nil
}

// VWAPReclaim trades the VWAP of the session: its reclaim, a BUY on the close
// crossing above it after closes below it, a SELL on the close crossing below
// it after closes above it; and its reject, a BUY on a wick down to it from
// above rejected by a close above it, a SELL on a wick up to it from below
// rejected by a close below it. See VWAPReclaimConfig.
type VWAPReclaim struct {
	cfg     VWAPReclaimConfig
	anchor  dayTime
	loc     *time.Location
	warmup  int
	bracket *bracket
}

// NewVWAPReclaim validates the configuration and creates the strategy.
func NewVWAPReclaim(cfg VWAPReclaimConfig) (*VWAPReclaim, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("vwap: %w", err)
	}
	day, err := newSession(cfg.Anchor, cfg.Anchor, cfg.Timezone)
	if err != nil {
		return nil, fmt.Errorf("vwap: %w", err)
	}
	candles, _, err := timeframeCandles(cfg.Timeframe, rkDay+time.Hour)
	if err != nil {
		return nil, fmt.Errorf("vwap: %w", err)
	}

	// the window always reaches back to the anchor, with the recursive series
	// (the ATR and the RSI) seeded
	return &VWAPReclaim{
		cfg:     cfg,
		anchor:  day.start,
		loc:     day.loc,
		warmup:  max(candles, seedWarmup(max(cfg.ATRLen, cfg.RSILen))) + cfg.ConfirmBars + 1,
		bracket: newBracket("VWAP", cfg.Timeframe, cfg.BracketConfig),
	}, nil
}

func (s *VWAPReclaim) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *VWAPReclaim) WarmupPeriod() int {
	return s.warmup
}

func (s *VWAPReclaim) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["vw_vwap"] = anchoredVWAP(df, s.anchor, s.loc)
	df.Metadata["vw_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)
	if s.cfg.RSILen > 0 {
		df.Metadata["vw_rsi"] = pineRSI(df.Close, s.cfg.RSILen)
	}

	return []strategy.ChartIndicator{
		{
			Overlay:   true,
			GroupName: "VWAP",
			Time:      df.Time,
			Metrics: []strategy.IndicatorMetric{
				{
					Values: df.Metadata["vw_vwap"],
					Name:   "VWAP",
					Color:  "#8e44ad",
					Style:  strategy.StyleLine,
				},
			},
		},
	}
}

// signal returns the entry of the last candle, if any, and its setup: RECLAIM
// or REJECT.
func (s *VWAPReclaim) signal(df *model.Dataframe) (model.SideType, string) {
	vwap, atrs := df.Metadata["vw_vwap"], df.Metadata["vw_atr"]
	i := len(df.Close) - 1
	if i < s.cfg.ConfirmBars || len(vwap) != len(df.Close) || len(atrs) != len(df.Close) ||
		len(df.Open) != len(df.Close) || isNa(vwap[i]) || isNa(atrs[i]) {
		return "", ""
	}

	atr, closePrice, open := atrs[i], df.Close[i], df.Open[i]
	for _, side := range []model.SideType{model.SideTypeBuy, model.SideTypeSell} {
		sign := rkSign(side)
		if sign*(closePrice-open) <= 0 || sign*(closePrice-vwap[i]) <= 0 || !s.momentum(df, sign) {
			continue // the candle closes in the direction of the entry, beyond the VWAP
		}

		if s.cfg.Reclaim && s.held(df, -sign) && sign*(closePrice-vwap[i]) >= s.cfg.ReclaimATR*atr {
			return side, "RECLAIM"
		}

		// the wick toward the VWAP: the low of a long, the high of a short
		wick, body := df.Low[i], math.Min(open, closePrice)
		if side == model.SideTypeSell {
			wick, body = df.High[i], math.Max(open, closePrice)
		}
		if s.cfg.Reject && s.held(df, sign) && sign*(wick-vwap[i]) <= s.cfg.RejectATR*atr &&
			sign*(body-wick) >= s.cfg.RejectWick*(df.High[i]-df.Low[i]) {
			return side, "REJECT"
		}
	}
	return "", ""
}

// held reports whether the ConfirmBars closes before the last one are all on
// a side of the VWAP: above for a sign of 1, below for -1.
func (s *VWAPReclaim) held(df *model.Dataframe, sign float64) bool {
	vwap := df.Metadata["vw_vwap"]
	i := len(df.Close) - 1
	for k := 1; k <= s.cfg.ConfirmBars; k++ {
		if !(sign*(df.Close[i-k]-vwap[i-k]) > 0) {
			return false
		}
	}
	return true
}

// momentum reports whether the RSI is on the side of sign, always without it.
func (s *VWAPReclaim) momentum(df *model.Dataframe, sign float64) bool {
	if s.cfg.RSILen == 0 {
		return true
	}
	rsi := df.Metadata["vw_rsi"]
	return len(rsi) == len(df.Close) && sign*(rsi.Last(0)-50) > 0
}

// exit closes the position on the close back across the VWAP.
func (s *VWAPReclaim) exit(df *model.Dataframe, held model.SideType) string {
	if vwap := df.Metadata["vw_vwap"]; len(vwap) == len(df.Close) &&
		rkSign(held)*(df.Close.Last(0)-vwap.Last(0)) < 0 {
		return "INVALIDATION"
	}
	return ""
}

func (s *VWAPReclaim) OnCandle(df *model.Dataframe, broker service.Broker) {
	entry, setup := s.signal(df)
	if !s.bracket.manage(df, broker, s.exit) || entry == "" {
		return
	}

	log.Debugf("[VWAP] %s %s | %s %s | VWAP %f", df.Pair, s.cfg.Timeframe, setup, entry,
		df.Metadata["vw_vwap"].Last(0))
	s.bracket.open(df, broker, entry, df.Metadata["vw_atr"].Last(0))
}
