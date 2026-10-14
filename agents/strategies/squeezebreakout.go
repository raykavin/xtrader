package strategies

import (
	"errors"
	"fmt"
	"math"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

// SqueezeBreakoutConfig holds the inputs of SqueezeBreakout, with its defaults
// in DefaultSqueezeBreakoutConfig: the rules of the prompt breakout of the LLM
// agent, their numbers set as the user approved them. Periods are counted in
// candles of Timeframe. The levels of the higher timeframes and the economic
// calendar are left out.
type SqueezeBreakoutConfig struct {
	Timeframe string

	// The compression: the box of the BoxBars candles before the breakout, its
	// height MaxBoxATR ATR at most, the ATR of the candle before it under the
	// percentile MaxATRPercentile of the ATRs of the last PercentileBars
	// candles.
	BoxBars          int
	MaxBoxATR        float64
	PercentileBars   int
	MaxATRPercentile float64

	// The breakout: a close beyond the box with a body MinBodyPct of the candle
	// at least, a volume MinVolume times its SMA of VolumeLen candles at least
	// (AgainstTrendVolume against the SMA of TrendLen candles), the ADX of
	// ADXLen candles rising with the DI of its side on top.
	MinBodyPct         float64
	VolumeLen          int
	MinVolume          float64
	AgainstTrendVolume float64
	TrendLen           int
	ADXLen             int

	// The entry: on the close of the breakout within MaxChaseATR ATR of the
	// box; farther, on a retest within RetestBars candles: a candle back within
	// RetestATR ATR of the edge closing beyond it, then a candle closing beyond
	// its high (its low for a short).
	MaxChaseATR float64
	RetestBars  int
	RetestATR   float64

	// The exits: the stop loss at the middle of the box, or beyond the candle
	// of the breakout when nearer and StopMinATR ATR away at least; the take
	// profit the height of the box projected from its edge; no entry offering
	// less than MinRR. The stop loss to the entry from BreakEvenR; from TrailR
	// behind the EMA of FastLen candles or the extreme of the last 2 candles,
	// the nearer; closed on a close back inside the box.
	StopMinATR float64
	MinRR      float64
	BreakEvenR float64
	TrailR     float64
	FastLen    int

	// The ATR, the cost filter and the sizing, see BracketConfig: its stop
	// loss, take profit and max bars aren't read, the strategy sets its own.
	BracketConfig
}

// DefaultSqueezeBreakoutConfig returns the defaults of SqueezeBreakout, on the
// 15m timeframe.
func DefaultSqueezeBreakoutConfig() SqueezeBreakoutConfig {
	return SqueezeBreakoutConfig{
		Timeframe: "15m",

		BoxBars:          10,
		MaxBoxATR:        3,
		PercentileBars:   100,
		MaxATRPercentile: 30,

		MinBodyPct:         0.5,
		VolumeLen:          20,
		MinVolume:          1.5,
		AgainstTrendVolume: 2,
		TrendLen:           200,
		ADXLen:             14,

		MaxChaseATR: 0.5,
		RetestBars:  5,
		RetestATR:   0.25,

		StopMinATR: 1,
		MinRR:      2,
		BreakEvenR: 1,
		TrailR:     1.5,
		FastLen:    9,

		BracketConfig: DefaultBracketConfig(),
	}
}

func (c SqueezeBreakoutConfig) validate() error {
	if err := c.BracketConfig.validate(); err != nil {
		return err
	}
	for _, period := range []int{c.BoxBars, c.PercentileBars, c.VolumeLen, c.TrendLen, c.ADXLen, c.FastLen} {
		if period < 1 {
			return errors.New("periods must be at least 1")
		}
	}
	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case c.MaxBoxATR <= 0 || c.MaxATRPercentile <= 0 || c.MaxATRPercentile > 100:
		return errors.New("the box must be positive, the ATR percentile from above 0 to 100")
	case c.MinBodyPct < 0 || c.MinBodyPct > 1 || c.MinVolume < 0 || c.AgainstTrendVolume < c.MinVolume:
		return errors.New("the body from 0 to 1, the volumes not negative, the one against the trend at least " +
			"the other")
	case c.MaxChaseATR < 0 || c.RetestBars < 0 || c.RetestATR < 0:
		return errors.New("the chase and the retest can't be negative")
	case c.StopMinATR <= 0 || c.MinRR <= 0 || c.BreakEvenR < 0 || c.TrailR < 0:
		return errors.New("the stop loss and the risk reward must be positive, the moves of the stop loss not " +
			"negative")
	}
	return nil
}

// squeezeBox is the box of a breakout of side: its high and low, and the
// extreme of the candle of the breakout on the side of the box.
type squeezeBox struct {
	side      model.SideType
	high, low float64
	breakout  float64 // the low of the candle of a breakout up, the high of one down
}

// edge returns the edge of the box the breakout crossed: its high for a
// breakout up.
func (b squeezeBox) edge() float64 {
	if b.side == model.SideTypeBuy {
		return b.high
	}
	return b.low
}

// squeezeRetest is a breakout farther than MaxChaseATR from its box, waiting
// for its retest: the candles left, and the extreme of the candle of the
// retest when it came (its high for a breakout up), NaN before.
type squeezeRetest struct {
	box     squeezeBox
	left    int
	resumed float64
}

// SqueezeBreakout trades the release of a compression with the volume: a BUY
// on a close beyond the top of a tight box of quiet candles, a SELL beyond its
// bottom, closed back inside the box. See SqueezeBreakoutConfig.
type SqueezeBreakout struct {
	cfg     SqueezeBreakoutConfig
	warmup  int
	bracket *bracket
	plan    rPlan
	boxes   map[string]squeezeBox    // the boxes of the positions, by pair
	retests map[string]squeezeRetest // the breakouts waiting for their retest, by pair
}

// NewSqueezeBreakout validates the configuration and creates the strategy.
func NewSqueezeBreakout(cfg SqueezeBreakoutConfig) (*SqueezeBreakout, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("squeezebreakout: %w", err)
	}
	s := &SqueezeBreakout{
		cfg: cfg,
		warmup: max(cfg.TrendLen, cfg.VolumeLen, cfg.BoxBars+cfg.PercentileBars+1,
			seedWarmup(max(cfg.ATRLen, 2*cfg.ADXLen, cfg.FastLen))+cfg.PercentileBars) + 1,
		bracket: newBracket("SQUEEZE", cfg.Timeframe, cfg.BracketConfig),
		boxes:   make(map[string]squeezeBox),
		retests: make(map[string]squeezeRetest),
	}
	s.plan = rPlan{breakEvenR: cfg.BreakEvenR, trailR: cfg.TrailR, trail: s.trailLevel}
	return s, nil
}

func (s *SqueezeBreakout) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *SqueezeBreakout) WarmupPeriod() int {
	return s.warmup
}

func (s *SqueezeBreakout) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["sq_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)
	df.Metadata["sq_volume"] = pineSMA(df.Volume, s.cfg.VolumeLen)
	df.Metadata["sq_trend"] = pineSMA(df.Close, s.cfg.TrendLen)
	df.Metadata["sq_fast"] = pineEMA(df.Close, s.cfg.FastLen)
	df.Metadata["sq_pdi"], df.Metadata["sq_mdi"], df.Metadata["sq_adx"] = pineDMI(df.High, df.Low, df.Close,
		s.cfg.ADXLen, s.cfg.ADXLen)
	high, low := make([]float64, len(df.Close)), make([]float64, len(df.Close))
	highest, lowest := pineHighest(df.High, s.cfg.BoxBars), pineLowest(df.Low, s.cfg.BoxBars)
	for i := range high {
		high[i], low[i] = na, na
		if i > 0 {
			high[i], low[i] = highest[i-1], lowest[i-1] // the box before each candle
		}
	}
	df.Metadata["sq_box_high"], df.Metadata["sq_box_low"] = high, low
	return overlay(df, "Box", s.cfg.BoxBars+1,
		line(high, fmt.Sprintf("Box %d high", s.cfg.BoxBars), colorUp),
		line(low, fmt.Sprintf("Box %d low", s.cfg.BoxBars), colorDown),
		line(df.Metadata["sq_fast"], fmt.Sprintf("EMA %d", s.cfg.FastLen), colorLine))
}

// breakout returns the box the last candle breaks out of, false for none, see
// SqueezeBreakoutConfig.
func (s *SqueezeBreakout) breakout(df *model.Dataframe, m map[string]model.Series[float64]) (squeezeBox, bool) {
	i := len(df.Close) - 1
	if i < s.cfg.BoxBars+s.cfg.PercentileBars {
		return squeezeBox{}, false
	}
	atr, high, low := m["sq_atr"], m["sq_box_high"][i], m["sq_box_low"][i]
	quiet := atr[i-1]
	if isNa(high) || isNa(low) || isNa(quiet) || high-low > s.cfg.MaxBoxATR*quiet {
		return squeezeBox{}, false
	}
	below := 0
	for k := i - s.cfg.PercentileBars; k < i; k++ {
		if atr[k] < quiet {
			below++
		}
	}
	if float64(below)/float64(s.cfg.PercentileBars)*100 >= s.cfg.MaxATRPercentile {
		return squeezeBox{}, false // not a quiet market
	}

	closePrice, span := df.Close[i], df.High[i]-df.Low[i]
	box := squeezeBox{high: high, low: low}
	switch {
	case closePrice > high:
		box.side, box.breakout = model.SideTypeBuy, df.Low[i]
	case closePrice < low:
		box.side, box.breakout = model.SideTypeSell, df.High[i]
	default:
		return squeezeBox{}, false
	}
	sign := rkSign(box.side)
	volume := s.cfg.MinVolume
	if sign*(closePrice-m["sq_trend"][i]) < 0 {
		volume = s.cfg.AgainstTrendVolume
	}
	adx, pdi, mdi := m["sq_adx"], m["sq_pdi"], m["sq_mdi"]
	if span <= 0 || math.Abs(closePrice-df.Open[i]) < s.cfg.MinBodyPct*span ||
		!(df.Volume[i] >= volume*m["sq_volume"][i]) || !(adx[i] > adx[i-1]) || !(sign*(pdi[i]-mdi[i]) > 0) {
		return squeezeBox{}, false
	}
	return box, true
}

// levels returns the stop loss and the take profit of an entry of box at the
// last close, false when it offers less than MinRR.
func (s *SqueezeBreakout) levels(df *model.Dataframe, box squeezeBox, atr float64) (float64, float64, bool) {
	closePrice, sign := df.Close.Last(0), rkSign(box.side)
	stop := (box.high + box.low) / 2
	if sign*(box.breakout-stop) > 0 && sign*(closePrice-box.breakout) >= s.cfg.StopMinATR*atr {
		stop = box.breakout // beyond the candle of the breakout, nearer and still far enough
	}
	profit := box.edge() + sign*(box.high-box.low)
	return stop, profit, enoughReward(box.side, closePrice, stop, profit, s.cfg.MinRR)
}

// signal returns the entry of the last candle, if any, with its box, its stop
// loss and its take profit: on a breakout near its box, or on the end of the
// retest of one farther, see SqueezeBreakoutConfig.
func (s *SqueezeBreakout) signal(df *model.Dataframe) (squeezeBox, float64, float64) {
	m, ok := meta(df, "sq_atr", "sq_volume", "sq_trend", "sq_adx", "sq_pdi", "sq_mdi", "sq_box_high", "sq_box_low")
	i := len(df.Close) - 1
	if !ok || i < 1 {
		return squeezeBox{}, 0, 0
	}
	closePrice, atr := df.Close[i], m["sq_atr"][i]
	if s.bracket.belowCost(closePrice, atr) {
		delete(s.retests, df.Pair)
		return squeezeBox{}, 0, 0
	}

	if retest, waiting := s.retests[df.Pair]; waiting {
		delete(s.retests, df.Pair) // kept again below while it waits
		box, sign := retest.box, rkSign(retest.box.side)
		switch {
		case !isNa(retest.resumed) && sign*(closePrice-retest.resumed) > 0:
			// the breakout resumed after its retest
			if stop, profit, ok := s.levels(df, box, atr); ok {
				return box, stop, profit
			}
			return squeezeBox{}, 0, 0
		case sign*(closePrice-box.edge()) <= 0:
			// back inside the box: the breakout failed
		default:
			reach, resumed := df.Low[i], df.High[i]
			if box.side == model.SideTypeSell {
				reach, resumed = df.High[i], df.Low[i]
			}
			if sign*(reach-box.edge()) <= s.cfg.RetestATR*atr {
				retest.resumed = resumed // the retest, holding the edge
			}
			// within RetestBars candles, and the one after a retest on the last
			if retest.left--; retest.left > 0 || retest.left == 0 && !isNa(retest.resumed) {
				s.retests[df.Pair] = retest
			}
		}
	}

	box, ok := s.breakout(df, m)
	if !ok {
		return squeezeBox{}, 0, 0
	}
	if rkSign(box.side)*(closePrice-box.edge()) > s.cfg.MaxChaseATR*atr {
		if s.cfg.RetestBars > 0 {
			s.retests[df.Pair] = squeezeRetest{box: box, left: s.cfg.RetestBars, resumed: na}
		}
		return squeezeBox{}, 0, 0
	}
	delete(s.retests, df.Pair)
	stop, profit, ok := s.levels(df, box, atr)
	if !ok {
		return squeezeBox{}, 0, 0
	}
	return box, stop, profit
}

// trailLevel returns the level of the trailing stop loss of a position of
// side: the nearer of the fast EMA and the extreme of the last 2 candles.
func (s *SqueezeBreakout) trailLevel(df *model.Dataframe, side model.SideType) float64 {
	fast := df.Metadata["sq_fast"]
	i := len(df.Close) - 1
	if len(fast) != len(df.Close) || isNa(fast[i]) || i < 1 {
		return na
	}
	if side == model.SideTypeBuy {
		return math.Max(fast[i], math.Min(df.Low[i], df.Low[i-1]))
	}
	return math.Min(fast[i], math.Max(df.High[i], df.High[i-1]))
}

// exit closes the position on a close back inside its box: the breakout
// failed.
func (s *SqueezeBreakout) exit(df *model.Dataframe, held model.SideType) string {
	if box, ok := s.boxes[df.Pair]; ok && box.side == held && rkSign(held)*(df.Close.Last(0)-box.edge()) < 0 {
		return "FAILED"
	}
	return ""
}

func (s *SqueezeBreakout) OnCandle(df *model.Dataframe, broker service.Broker) {
	s.bracket.trackR(df)
	box, stop, profit := s.signal(df)
	if !s.bracket.manage(df, broker, s.exit) {
		s.bracket.moveR(df, broker, s.plan)
		return
	}
	delete(s.boxes, df.Pair) // flat
	if box.side != "" && s.bracket.openAt(df, broker, box.side, stop, profit) {
		s.boxes[df.Pair] = box
	}
}
