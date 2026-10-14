package strategies

import (
	"errors"
	"fmt"
	"math"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

// MeanReversionConfig holds the inputs of MeanReversion, with its defaults in
// DefaultMeanReversionConfig: the rules of the prompt mean_reversion of the LLM
// agent, their numbers set as the user approved them. Periods are counted in
// candles of Timeframe. The context the candles don't hold (the swing
// structure, the higher timeframes, the economic calendar) is left out.
type MeanReversionConfig struct {
	Timeframe string

	// The regime, a range: the ADX of ADXLen candles under MaxADX and not
	// rising, the EMAs of FastLen and SlowLen candles closer than FlatATR ATR,
	// and the close crossing the middle band MinCrosses times at least over the
	// last CrossBars candles.
	ADXLen     int
	MaxADX     float64
	FastLen    int
	SlowLen    int
	FlatATR    float64
	CrossBars  int
	MinCrosses int

	// The extreme and its rejection: the low of the candle at or under the
	// lower Bollinger Band of BBLen candles and BBMult deviations, the RSI of
	// RSILen candles under Oversold on the candle or the one before, the candle
	// closing back inside the band, bullish, its lower wick WickPct of its range
	// at least; the mirror for a short, the RSI over Overbought.
	BBLen      int
	BBMult     float64
	RSILen     int
	Oversold   float64
	Overbought float64
	WickPct    float64

	// The exits: the stop loss StopBufferATR beyond the extreme of the last
	// StopBars candles, the take profit at the middle band, no entry offering
	// less than MinRR; closed at the middle band, on an ADX over TrendADX with
	// the DI against the position, or on a close beyond the band again; the
	// stop loss to the entry from BreakEvenR; closed after TimeBars candles
	// under TimeR.
	StopBars      int
	StopBufferATR float64
	MinRR         float64
	TrendADX      float64
	BreakEvenR    float64
	TimeBars      int
	TimeR         float64

	// The ATR, the cost filter and the sizing, see BracketConfig: its stop
	// loss, take profit and max bars aren't read, the strategy sets its own.
	BracketConfig
}

// DefaultMeanReversionConfig returns the defaults of MeanReversion, on the 15m
// timeframe.
func DefaultMeanReversionConfig() MeanReversionConfig {
	return MeanReversionConfig{
		Timeframe: "15m",

		ADXLen:     14,
		MaxADX:     20,
		FastLen:    9,
		SlowLen:    21,
		FlatATR:    0.25,
		CrossBars:  20,
		MinCrosses: 2,

		BBLen:      20,
		BBMult:     2,
		RSILen:     14,
		Oversold:   30,
		Overbought: 70,
		WickPct:    0.5,

		StopBars:      5,
		StopBufferATR: 0.5,
		MinRR:         1.2,
		TrendADX:      25,
		BreakEvenR:    0.7,
		TimeBars:      8,
		TimeR:         0.2,

		BracketConfig: DefaultBracketConfig(),
	}
}

func (c MeanReversionConfig) validate() error {
	if err := c.BracketConfig.validate(); err != nil {
		return err
	}
	for _, period := range []int{c.ADXLen, c.FastLen, c.SlowLen, c.CrossBars, c.BBLen, c.RSILen, c.StopBars} {
		if period < 1 {
			return errors.New("periods must be at least 1")
		}
	}
	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case c.MaxADX <= 0 || c.TrendADX < c.MaxADX || c.TrendADX > 100:
		return errors.New("the ADX of the range must be positive, at most the one of a trend, at most 100")
	case c.FlatATR <= 0 || c.BBMult <= 0 || c.StopBufferATR < 0:
		return errors.New("the flat EMAs, the band and the buffer of the stop loss must be positive")
	case c.MinCrosses < 0 || c.MinCrosses > c.CrossBars:
		return errors.New("the crosses of the middle band must be at most the candles counted")
	case c.Oversold <= 0 || c.Overbought >= 100 || c.Oversold >= c.Overbought:
		return errors.New("RSI levels: the oversold level must be above 0 and below the overbought one, below 100")
	case c.WickPct < 0 || c.WickPct > 1:
		return errors.New("the wick of the rejection must be from 0 to 1 of its candle")
	case c.MinRR <= 0 || c.BreakEvenR < 0 || c.TimeBars < 0:
		return errors.New("the risk reward, the break-even and the time stop can't be negative")
	}
	return nil
}

// MeanReversion fades the extremes of a ranging market back to its mean: a BUY
// on the rejection of the lower Bollinger Band with the RSI oversold, a SELL on
// the one of the upper band, closed at the middle band. See
// MeanReversionConfig.
type MeanReversion struct {
	cfg     MeanReversionConfig
	warmup  int
	bracket *bracket
	plan    rPlan
}

// NewMeanReversion validates the configuration and creates the strategy.
func NewMeanReversion(cfg MeanReversionConfig) (*MeanReversion, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("meanreversion: %w", err)
	}
	return &MeanReversion{
		cfg: cfg,
		warmup: max(cfg.BBLen, cfg.CrossBars+1, cfg.StopBars,
			seedWarmup(max(cfg.ATRLen, 2*cfg.ADXLen, cfg.SlowLen, cfg.FastLen, cfg.RSILen))) + 1,
		bracket: newBracket("MEANREVERSION", cfg.Timeframe, cfg.BracketConfig),
		plan:    rPlan{breakEvenR: cfg.BreakEvenR},
	}, nil
}

func (s *MeanReversion) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *MeanReversion) WarmupPeriod() int {
	return s.warmup
}

func (s *MeanReversion) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	mid, deviation := pineSMA(df.Close, s.cfg.BBLen), pineStdev(df.Close, s.cfg.BBLen)
	upper, lower := make([]float64, len(mid)), make([]float64, len(mid))
	for i := range mid {
		upper[i], lower[i] = mid[i]+s.cfg.BBMult*deviation[i], mid[i]-s.cfg.BBMult*deviation[i]
	}
	df.Metadata["mr_mid"], df.Metadata["mr_upper"], df.Metadata["mr_lower"] = mid, upper, lower
	df.Metadata["mr_fast"] = pineEMA(df.Close, s.cfg.FastLen)
	df.Metadata["mr_slow"] = pineEMA(df.Close, s.cfg.SlowLen)
	df.Metadata["mr_rsi"] = pineRSI(df.Close, s.cfg.RSILen)
	df.Metadata["mr_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)
	df.Metadata["mr_pdi"], df.Metadata["mr_mdi"], df.Metadata["mr_adx"] = pineDMI(df.High, df.Low, df.Close,
		s.cfg.ADXLen, s.cfg.ADXLen)

	return append(overlay(df, "Bollinger Bands", s.cfg.BBLen,
		line(upper, fmt.Sprintf("BB %d upper", s.cfg.BBLen), colorOther),
		line(mid, fmt.Sprintf("BB %d", s.cfg.BBLen), colorLine),
		line(lower, fmt.Sprintf("BB %d lower", s.cfg.BBLen), colorOther)),
		pane(df, "RSI", s.cfg.RSILen, line(df.Metadata["mr_rsi"], fmt.Sprintf("RSI %d", s.cfg.RSILen), colorOther)))
}

// meta returns the series of the indicators of key, false unless each has a
// value for every candle.
func meta(df *model.Dataframe, keys ...string) (map[string]model.Series[float64], bool) {
	series := make(map[string]model.Series[float64], len(keys))
	for _, key := range keys {
		values := df.Metadata[key]
		if len(values) != len(df.Close) {
			return nil, false
		}
		series[key] = values
	}
	return series, true
}

// ranging reports whether the market ranges on the last candle, see
// MeanReversionConfig.
func (s *MeanReversion) ranging(df *model.Dataframe, m map[string]model.Series[float64]) bool {
	i := len(df.Close) - 1
	adx, atr := m["mr_adx"], m["mr_atr"]
	if isNa(adx[i]) || isNa(adx[i-1]) || isNa(atr[i]) || !(adx[i] < s.cfg.MaxADX) || adx[i] > adx[i-1] ||
		!(math.Abs(m["mr_fast"][i]-m["mr_slow"][i]) < s.cfg.FlatATR*atr[i]) {
		return false
	}
	crosses := 0
	for k := max(1, i-s.cfg.CrossBars+1); k <= i; k++ {
		now, before := df.Close[k]-m["mr_mid"][k], df.Close[k-1]-m["mr_mid"][k-1]
		if now*before < 0 {
			crosses++
		}
	}
	return crosses >= s.cfg.MinCrosses
}

// signal returns the entry of the last candle, if any, with its stop loss and
// take profit.
func (s *MeanReversion) signal(df *model.Dataframe) (model.SideType, float64, float64) {
	m, ok := meta(df, "mr_mid", "mr_upper", "mr_lower", "mr_fast", "mr_slow", "mr_rsi", "mr_atr", "mr_adx")
	i := len(df.Close) - 1
	if !ok || i < max(1, s.cfg.StopBars-1) || !s.ranging(df, m) {
		return "", 0, 0
	}
	open, closePrice, high, low := df.Open[i], df.Close[i], df.High[i], df.Low[i]
	atr, rsi, span := m["mr_atr"][i], m["mr_rsi"], high-low
	if span <= 0 || isNa(rsi[i]) || isNa(rsi[i-1]) || s.bracket.belowCost(closePrice, atr) {
		return "", 0, 0
	}

	var side model.SideType
	var stop float64
	switch {
	case low <= m["mr_lower"][i] && closePrice > m["mr_lower"][i] && closePrice > open &&
		(math.Min(open, closePrice)-low)/span >= s.cfg.WickPct &&
		(rsi[i] < s.cfg.Oversold || rsi[i-1] < s.cfg.Oversold):
		side = model.SideTypeBuy
		stop = pineLowest(df.Low, s.cfg.StopBars)[i] - s.cfg.StopBufferATR*atr
	case high >= m["mr_upper"][i] && closePrice < m["mr_upper"][i] && closePrice < open &&
		(high-math.Max(open, closePrice))/span >= s.cfg.WickPct &&
		(rsi[i] > s.cfg.Overbought || rsi[i-1] > s.cfg.Overbought):
		side = model.SideTypeSell
		stop = pineHighest(df.High, s.cfg.StopBars)[i] + s.cfg.StopBufferATR*atr
	default:
		return "", 0, 0
	}
	profit := m["mr_mid"][i]
	if !enoughReward(side, closePrice, stop, profit, s.cfg.MinRR) {
		return "", 0, 0
	}
	return side, stop, profit
}

// exit closes the position at the middle band, on a trend against it, on a
// close beyond the band again, or going nowhere.
func (s *MeanReversion) exit(df *model.Dataframe, held model.SideType) string {
	m, ok := meta(df, "mr_mid", "mr_upper", "mr_lower", "mr_adx", "mr_pdi", "mr_mdi")
	if !ok {
		return ""
	}
	i, sign := len(df.Close)-1, rkSign(held)
	closePrice := df.Close[i]
	against := m["mr_mdi"][i] > m["mr_pdi"][i]
	band := closePrice < m["mr_lower"][i]
	if held == model.SideTypeSell {
		against, band = m["mr_pdi"][i] > m["mr_mdi"][i], closePrice > m["mr_upper"][i]
	}
	switch {
	case sign*(closePrice-m["mr_mid"][i]) >= 0:
		return "MEAN"
	case m["mr_adx"][i] > s.cfg.TrendADX && against:
		return "TREND"
	case band:
		return "BAND"
	}
	return timeR(df, s.bracket.trades[df.Pair], s.cfg.TimeBars, s.cfg.TimeR, false)
}

func (s *MeanReversion) OnCandle(df *model.Dataframe, broker service.Broker) {
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

// enoughReward reports whether a position of side opened at price, its stop
// loss at stop and its take profit at profit, both on their side of the price,
// offers minRR of its risk at least.
func enoughReward(side model.SideType, price, stop, profit, minRR float64) bool {
	sign := rkSign(side)
	risk, reward := sign*(price-stop), sign*(profit-price)
	return risk > 0 && reward > 0 && reward/risk >= minRR
}

// belowCost reports whether an entry is skipped for its cost, see
// BracketConfig.MinATRPct: the ATR under that % of the close.
func (b *bracket) belowCost(closePrice, atr float64) bool {
	return isNa(atr) || atr <= 0 || b.cfg.MinATRPct > 0 && atr/closePrice*100 < b.cfg.MinATRPct
}
