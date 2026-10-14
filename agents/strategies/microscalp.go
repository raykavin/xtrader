package strategies

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/xhit/go-str2duration/v2"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

// MicroScalpConfig holds the inputs of MicroScalp, with the ones of the
// prompts scalp_1m and scalp_1m_btc of the LLM agent in DefaultScalp1mConfig
// and DefaultScalp1mBTCConfig, their numbers set as the user approved them.
// Periods are counted in candles of Timeframe. The economic calendar and the
// spread, which the candles don't hold, are left out: MinATRPct of the
// BracketConfig skips the markets too quiet for their costs.
type MicroScalpConfig struct {
	Timeframe string
	Name      string // of the logs, eg: SCALP1M

	// The direction, a clear micro trend: the EMA of FastLen candles beyond the
	// one of SlowLen by ApartATR ATR at least, both sloping its way over the
	// last SlopeBars candles, the close beyond both; with TrendSide, beyond the
	// SMA of TrendLen candles too; and on HigherTimeframes, eg: 15m and 1h, the
	// EMAs of FastLen and SlowLen closes not against it.
	FastLen          int
	SlowLen          int
	ApartATR         float64
	SlopeBars        int
	TrendLen         int
	TrendSide        bool
	HigherTimeframes []string

	// The activity: the ADX of ADXLen candles over MinADX, rising with
	// ADXRising, the DI of the side on top with DISide; the volume
	// MinVolume times its SMA of VolumeLen candles at least; with Sessions,
	// London or New York open (see majorSessionOpen).
	ADXLen    int
	MinADX    float64
	ADXRising bool
	DISide    bool
	VolumeLen int
	MinVolume float64
	Sessions  bool

	// The entry, a micro pullback: over the last PullbackBars candles the low
	// back at the fast EMA, without a close beyond the slow EMA, beyond the
	// fast one with HoldFast; then the candle closes beyond the high of the
	// one before, and beyond the fast EMA with CloseBeyondFast, the MACD
	// histogram (MACDFast, MACDSlow, MACDSignal) growing, or with RSICross the
	// RSI of RSILen candles crossing 50 its way. Not chased: after StrongBars
	// strong candles its way in a row (a body StrongBodyATR ATR at least),
	// MaxExtensionATR ATR beyond the fast EMA, nor with the RSI over MaxRSI
	// for a long (under 100-MaxRSI for a short).
	PullbackBars    int
	HoldFast        bool
	CloseBeyondFast bool
	RSICross        bool
	MACDFast        int
	MACDSlow        int
	MACDSignal      int
	RSILen          int
	StrongBars      int
	StrongBodyATR   float64
	MaxExtensionATR float64
	MaxRSI          float64

	// The exits: the stop loss beyond the low of the pullback (its high for a
	// short), StopMinATR to StopMaxATR ATR away; the take profit TargetR times
	// the risk, TargetMinATR ATR away at least, before the high of the last
	// CapBars candles when nearer (0: none); no entry with a high (a low for a
	// short) of the last RoomBars candles less than RoomATR ATR ahead (0:
	// none), nor offering less than MinRR. The stop loss to the entry from
	// BreakEvenR, TrailATR ATR behind the close from TrailR; closed on a close
	// beyond the slow EMA against it, with ExitMomentum on the ADX falling with
	// the other DI on top, and after TimeBars candles without reaching
	// TimeReach.
	StopMinATR   float64
	StopMaxATR   float64
	TargetR      float64
	TargetMinATR float64
	CapBars      int
	RoomBars     int
	RoomATR      float64
	MinRR        float64
	BreakEvenR   float64
	TrailR       float64
	TrailATR     float64
	ExitMomentum bool
	TimeBars     int
	TimeReach    float64

	// The ATR, the cost filter and the sizing, see BracketConfig: its stop
	// loss, take profit and max bars aren't read, the strategy sets its own.
	BracketConfig
}

// DefaultScalp1mConfig returns the rules of the prompt scalp_1m: scalping the
// micro pullbacks of the micro trend on 1 minute candles.
func DefaultScalp1mConfig() MicroScalpConfig {
	return MicroScalpConfig{
		Timeframe: "1m",
		Name:      "SCALP1M",

		FastLen:          9,
		SlowLen:          21,
		ApartATR:         0.1,
		SlopeBars:        3,
		TrendLen:         200,
		HigherTimeframes: []string{"15m", "1h"},

		ADXLen:    14,
		MinADX:    18,
		VolumeLen: 20,
		MinVolume: 0.8,
		Sessions:  true,

		PullbackBars:    5,
		CloseBeyondFast: true,
		RSICross:        true,
		MACDFast:        12,
		MACDSlow:        26,
		MACDSignal:      9,
		RSILen:          14,
		StrongBars:      3,
		StrongBodyATR:   0.5,
		MaxExtensionATR: 1,
		MaxRSI:          75,

		StopMinATR: 1,
		StopMaxATR: 1.5,
		TargetR:    1.5,
		CapBars:    20,
		MinRR:      1.2,
		BreakEvenR: 0.8,
		TrailR:     1.5,
		TrailATR:   1,
		TimeBars:   10,
		TimeReach:  0.5,

		BracketConfig: DefaultBracketConfig(),
	}
}

// DefaultScalp1mBTCConfig returns the rules of the prompt scalp_1m_btc:
// scalping only the strong bursts of momentum on 1 minute candles, for a
// spread wide next to the moves, eg: BTCUSDm on Exness.
func DefaultScalp1mBTCConfig() MicroScalpConfig {
	cfg := DefaultScalp1mConfig()
	cfg.Name = "SCALP1MBTC"
	cfg.TrendSide = true
	cfg.MinADX, cfg.ADXRising, cfg.DISide = 20, true, true
	cfg.MinVolume = 1.2
	cfg.PullbackBars, cfg.HoldFast, cfg.CloseBeyondFast, cfg.RSICross = 3, true, false, false
	cfg.StrongBars, cfg.MaxExtensionATR, cfg.MaxRSI = 4, 1.5, 78
	cfg.StopMinATR, cfg.StopMaxATR = 1.5, 2
	cfg.TargetR, cfg.TargetMinATR, cfg.CapBars = 2, 3, 0
	cfg.RoomBars, cfg.RoomATR = 60, 3
	cfg.BreakEvenR, cfg.TrailR, cfg.TrailATR = 1, 1.5, 1.5
	cfg.ExitMomentum = true
	cfg.TimeBars = 15
	return cfg
}

func (c MicroScalpConfig) validate() error {
	if err := c.BracketConfig.validate(); err != nil {
		return err
	}
	for _, period := range []int{c.FastLen, c.SlowLen, c.SlopeBars, c.TrendLen, c.ADXLen, c.VolumeLen,
		c.PullbackBars, c.MACDFast, c.MACDSlow, c.MACDSignal, c.RSILen, c.StrongBars} {
		if period < 1 {
			return errors.New("periods must be at least 1")
		}
	}
	tf, err := str2duration.ParseDuration(c.Timeframe)
	if err != nil || tf <= 0 {
		return fmt.Errorf("invalid timeframe %q", c.Timeframe)
	}
	for _, higher := range c.HigherTimeframes {
		if htf, err := str2duration.ParseDuration(higher); err != nil || htf <= tf || htf%tf != 0 {
			return fmt.Errorf("invalid higher timeframe %q: a multiple of %s", higher, c.Timeframe)
		}
	}
	switch {
	case c.Name == "":
		return errors.New("name is required")
	case c.FastLen >= c.SlowLen || c.MACDFast >= c.MACDSlow:
		return errors.New("the fast periods must be shorter than the slow ones")
	case c.ApartATR < 0 || c.MinADX < 0 || c.MinADX > 100 || c.MinVolume < 0 || c.StrongBodyATR <= 0:
		return errors.New("the filters can't be negative, the ADX at most 100")
	case c.MaxExtensionATR <= 0 || c.MaxRSI <= 50 || c.MaxRSI >= 100:
		return errors.New("the extension must be positive, the max RSI between 50 and 100")
	case c.StopMinATR <= 0 || c.StopMaxATR < c.StopMinATR || c.TargetR <= 0 || c.TargetMinATR < 0:
		return errors.New("the stop loss range and the target must be positive, the range in order")
	case c.CapBars < 0 || c.RoomBars < 0 || c.RoomATR < 0 || c.MinRR <= 0:
		return errors.New("the room, the cap and the risk reward can't be negative")
	case c.BreakEvenR < 0 || c.TrailR < 0 || c.TrailATR < 0 || c.TimeBars < 0:
		return errors.New("the moves of the stop loss and the time stop can't be negative")
	}
	return nil
}

// MicroScalp scalps the pullbacks of a clear micro trend, see
// MicroScalpConfig.
type MicroScalp struct {
	cfg     MicroScalpConfig
	tf      time.Duration
	htfs    []time.Duration
	warmup  int
	bracket *bracket
	plan    rPlan
}

// NewMicroScalp validates the configuration and creates the strategy.
func NewMicroScalp(cfg MicroScalpConfig) (*MicroScalp, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", cfg.Name, err)
	}
	s := &MicroScalp{cfg: cfg, bracket: newBracket(cfg.Name, cfg.Timeframe, cfg.BracketConfig)}
	s.tf, _ = str2duration.ParseDuration(cfg.Timeframe)
	s.warmup = max(cfg.TrendLen, cfg.VolumeLen, cfg.CapBars, cfg.RoomBars, cfg.PullbackBars+1, cfg.SlopeBars+1,
		seedWarmup(max(cfg.ATRLen, 2*cfg.ADXLen, cfg.SlowLen, cfg.MACDSlow+cfg.MACDSignal, cfg.RSILen))) + 1
	for _, higher := range cfg.HigherTimeframes {
		htf, _ := str2duration.ParseDuration(higher)
		s.htfs = append(s.htfs, htf)
		s.warmup = max(s.warmup, htfWarmup(cfg.SlowLen, int(htf/s.tf)))
	}
	s.plan = rPlan{breakEvenR: cfg.BreakEvenR, trailR: cfg.TrailR, trail: func(df *model.Dataframe,
		side model.SideType) float64 {
		atr := df.Metadata["ms_atr"]
		if len(atr) != len(df.Close) || cfg.TrailATR <= 0 {
			return na
		}
		return df.Close.Last(0) - rkSign(side)*cfg.TrailATR*atr.Last(0)
	}}
	return s, nil
}

func (s *MicroScalp) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *MicroScalp) WarmupPeriod() int {
	return s.warmup
}

func (s *MicroScalp) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["ms_fast"] = pineEMA(df.Close, s.cfg.FastLen)
	df.Metadata["ms_slow"] = pineEMA(df.Close, s.cfg.SlowLen)
	df.Metadata["ms_trend"] = pineSMA(df.Close, s.cfg.TrendLen)
	df.Metadata["ms_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)
	df.Metadata["ms_rsi"] = pineRSI(df.Close, s.cfg.RSILen)
	_, _, df.Metadata["ms_hist"] = pineMACD(df.Close, s.cfg.MACDFast, s.cfg.MACDSlow, s.cfg.MACDSignal)
	df.Metadata["ms_pdi"], df.Metadata["ms_mdi"], df.Metadata["ms_adx"] = pineDMI(df.High, df.Low, df.Close,
		s.cfg.ADXLen, s.cfg.ADXLen)
	df.Metadata["ms_volume"] = pineSMA(df.Volume, s.cfg.VolumeLen)
	for k, htf := range s.htfs {
		df.Metadata[fmt.Sprintf("ms_htf_fast_%d", k)], _ = htfEMA(df.Time, df.Close, htf, s.cfg.FastLen)
		df.Metadata[fmt.Sprintf("ms_htf_slow_%d", k)], _ = htfEMA(df.Time, df.Close, htf, s.cfg.SlowLen)
	}
	return overlay(df, "Micro trend", s.cfg.SlowLen,
		line(df.Metadata["ms_fast"], fmt.Sprintf("EMA %d", s.cfg.FastLen), colorLine),
		line(df.Metadata["ms_slow"], fmt.Sprintf("EMA %d", s.cfg.SlowLen), colorSlow))
}

// signal returns the entry of the last candle, if any, with its stop loss and
// take profit.
func (s *MicroScalp) signal(df *model.Dataframe) (model.SideType, float64, float64) {
	keys := []string{"ms_fast", "ms_slow", "ms_trend", "ms_atr", "ms_rsi", "ms_hist", "ms_pdi", "ms_mdi", "ms_adx",
		"ms_volume"}
	for k := range s.htfs {
		keys = append(keys, fmt.Sprintf("ms_htf_fast_%d", k), fmt.Sprintf("ms_htf_slow_%d", k))
	}
	m, ok := meta(df, keys...)
	i := len(df.Close) - 1
	need := max(s.cfg.PullbackBars, s.cfg.SlopeBars, s.cfg.StrongBars, s.cfg.CapBars, s.cfg.RoomBars, 1)
	if !ok || i < need {
		return "", 0, 0
	}
	for _, key := range keys {
		if isNa(m[key][i]) {
			return "", 0, 0
		}
	}
	closePrice, atr := df.Close[i], m["ms_atr"][i]
	if s.bracket.belowCost(closePrice, atr) || !s.active(df, m) {
		return "", 0, 0
	}

	for _, side := range []model.SideType{model.SideTypeBuy, model.SideTypeSell} {
		if !s.trending(df, m, side) || !s.pullback(df, m, side) || s.chased(df, m, side) {
			continue
		}
		sign := rkSign(side)
		extreme := pineLowest(df.Low, s.cfg.PullbackBars+1)[i]
		if side == model.SideTypeSell {
			extreme = pineHighest(df.High, s.cfg.PullbackBars+1)[i]
		}
		risk := math.Min(math.Max(sign*(closePrice-extreme), s.cfg.StopMinATR*atr), s.cfg.StopMaxATR*atr)
		stop := closePrice - sign*risk
		profit := closePrice + sign*math.Max(s.cfg.TargetR*risk, s.cfg.TargetMinATR*atr)
		if s.cfg.CapBars > 0 {
			recent := pineHighest(df.High[:i], s.cfg.CapBars)
			if side == model.SideTypeSell {
				recent = pineLowest(df.Low[:i], s.cfg.CapBars)
			}
			if level := recent[len(recent)-1]; !isNa(level) && sign*(level-closePrice) > 0 &&
				sign*(level-profit) < 0 {
				profit = level // the micro moves stall at the recent high
			}
		}
		if s.cfg.RoomBars > 0 && !s.room(df, side, atr) {
			continue
		}
		if !enoughReward(side, closePrice, stop, profit, s.cfg.MinRR) {
			continue
		}
		return side, stop, profit
	}
	return "", 0, 0
}

// active reports whether the market is active enough on the last candle: the
// ADX, the volume and the session, see MicroScalpConfig.
func (s *MicroScalp) active(df *model.Dataframe, m map[string]model.Series[float64]) bool {
	i := len(df.Close) - 1
	adx := m["ms_adx"]
	switch {
	case !(adx[i] > s.cfg.MinADX), s.cfg.ADXRising && !(adx[i] > adx[i-1]):
		return false
	case !(df.Volume[i] >= s.cfg.MinVolume*m["ms_volume"][i]):
		return false
	case s.cfg.Sessions && !majorSessionOpen(df.Time[i].Add(s.tf)):
		return false
	}
	return true
}

// trending reports whether a clear micro trend of side holds on the last
// candle, see MicroScalpConfig.
func (s *MicroScalp) trending(df *model.Dataframe, m map[string]model.Series[float64], side model.SideType) bool {
	i, sign := len(df.Close)-1, rkSign(side)
	fast, slow, closePrice := m["ms_fast"], m["ms_slow"], df.Close[i]
	j := i - s.cfg.SlopeBars
	switch {
	case !(sign*(fast[i]-slow[i]) > 0 && sign*(fast[i]-slow[i]) >= s.cfg.ApartATR*m["ms_atr"][i]):
		return false
	case !(sign*(fast[i]-fast[j]) > 0) || !(sign*(slow[i]-slow[j]) > 0):
		return false
	case !(sign*(closePrice-fast[i]) > 0) || !(sign*(closePrice-slow[i]) > 0):
		return false
	case s.cfg.TrendSide && !(sign*(closePrice-m["ms_trend"][i]) > 0):
		return false
	case s.cfg.DISide && !(sign*(m["ms_pdi"][i]-m["ms_mdi"][i]) > 0):
		return false
	}
	for k := range s.htfs {
		if sign*(m[fmt.Sprintf("ms_htf_fast_%d", k)][i]-m[fmt.Sprintf("ms_htf_slow_%d", k)][i]) < 0 {
			return false // the higher timeframe against it
		}
	}
	return true
}

// pullback reports whether the last candle resumes the micro trend of side
// after a micro pullback, see MicroScalpConfig.
func (s *MicroScalp) pullback(df *model.Dataframe, m map[string]model.Series[float64], side model.SideType) bool {
	i, sign := len(df.Close)-1, rkSign(side)
	fast, hold := m["ms_fast"], m["ms_slow"]
	if s.cfg.HoldFast {
		hold = fast
	}
	touched := false
	for j := i - s.cfg.PullbackBars; j < i; j++ {
		reach := df.Low[j]
		if side == model.SideTypeSell {
			reach = df.High[j]
		}
		touched = touched || sign*(reach-fast[j]) <= 0
	}
	for j := i - s.cfg.PullbackBars; j <= i; j++ {
		if sign*(df.Close[j]-hold[j]) < 0 {
			return false // a close beyond the EMA held
		}
	}
	beyond := df.High[i-1]
	if side == model.SideTypeSell {
		beyond = df.Low[i-1]
	}
	hist, rsi := m["ms_hist"], m["ms_rsi"]
	momentum := sign*(hist[i]-hist[i-1]) > 0
	if s.cfg.RSICross && !momentum {
		momentum = sign*(rsi[i]-50) > 0 && sign*(rsi[i-1]-50) <= 0
	}
	return touched && sign*(df.Close[i]-beyond) > 0 && momentum &&
		(!s.cfg.CloseBeyondFast || sign*(df.Close[i]-fast[i]) > 0)
}

// chased reports whether an entry of side on the last candle chases the move,
// see MicroScalpConfig.
func (s *MicroScalp) chased(df *model.Dataframe, m map[string]model.Series[float64], side model.SideType) bool {
	i, sign := len(df.Close)-1, rkSign(side)
	atr := m["ms_atr"]
	strong := 0
	for k := i; k > i-s.cfg.StrongBars; k-- {
		if sign*(df.Close[k]-df.Open[k]) >= s.cfg.StrongBodyATR*atr[k] {
			strong++
		}
	}
	rsi := m["ms_rsi"][i]
	return strong >= s.cfg.StrongBars ||
		sign*(df.Close[i]-m["ms_fast"][i]) > s.cfg.MaxExtensionATR*atr[i] ||
		side == model.SideTypeBuy && rsi > s.cfg.MaxRSI || side == model.SideTypeSell && rsi < 100-s.cfg.MaxRSI
}

// room reports whether the move of side has RoomATR ATR ahead of the last
// close: no high (low for a short) of the last RoomBars candles in the way.
func (s *MicroScalp) room(df *model.Dataframe, side model.SideType, atr float64) bool {
	i, sign := len(df.Close)-1, rkSign(side)
	closePrice := df.Close[i]
	for j := max(0, i-s.cfg.RoomBars); j < i; j++ {
		level := df.High[j]
		if side == model.SideTypeSell {
			level = df.Low[j]
		}
		if ahead := sign * (level - closePrice); ahead > 0 && ahead < s.cfg.RoomATR*atr {
			return false
		}
	}
	return true
}

// exit closes the position on a close beyond the slow EMA against it, on the
// momentum turning against it, or going nowhere.
func (s *MicroScalp) exit(df *model.Dataframe, held model.SideType) string {
	m, ok := meta(df, "ms_slow", "ms_adx", "ms_pdi", "ms_mdi")
	if !ok {
		return ""
	}
	i, sign := len(df.Close)-1, rkSign(held)
	if sign*(df.Close[i]-m["ms_slow"][i]) < 0 {
		return "EMA"
	}
	if s.cfg.ExitMomentum && i > 0 && m["ms_adx"][i] < m["ms_adx"][i-1] &&
		sign*(m["ms_mdi"][i]-m["ms_pdi"][i]) > 0 {
		return "MOMENTUM"
	}
	return timeR(df, s.bracket.trades[df.Pair], s.cfg.TimeBars, s.cfg.TimeReach, true)
}

func (s *MicroScalp) OnCandle(df *model.Dataframe, broker service.Broker) {
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
