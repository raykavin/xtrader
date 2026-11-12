package strategies

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/xhit/go-str2duration/v2"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

type (
	// RKSlopeMode is the unit of the trend slope threshold.
	RKSlopeMode string
	// RKVolatilityMode is the unit of the volatility band.
	RKVolatilityMode string
	// RKHTFMode selects how the higher timeframe of the HTF gate is chosen.
	RKHTFMode string
	// RKDirection restricts the side of the positions the strategy opens.
	RKDirection string
)

const (
	// RKSlopeATR measures the slope per bar in ATR units: scale free.
	RKSlopeATR RKSlopeMode = "atr"
	// RKSlopePercent measures the slope per bar in percent of price, which must
	// be retuned per symbol and timeframe.
	RKSlopePercent RKSlopeMode = "percent"

	// RKVolatilityRelative compares the ATR with its own long-run average.
	RKVolatilityRelative RKVolatilityMode = "relative"
	// RKVolatilityPercent compares the ATR with the price, which is asset
	// specific.
	RKVolatilityPercent RKVolatilityMode = "percent"

	// RKHTFAuto uses the chart timeframe times HTFMult.
	RKHTFAuto RKHTFMode = "auto"
	// RKHTFManual uses HTFManual, when it is higher than the chart timeframe.
	RKHTFManual RKHTFMode = "manual"

	RKDirectionBoth     RKDirection = "both"
	RKDirectionBuyOnly  RKDirection = "buy"
	RKDirectionSellOnly RKDirection = "sell"
)

const (
	rkDay   = 24 * time.Hour
	rkWeek  = 7 * rkDay
	rkMonth = 30 * rkDay // the month of the indicator's auto timeframe

	// rkSeedFactor sizes the part of the warmup that lets the seed of the
	// recursive series (EMA, ATR, RSI, DMI, MACD) wash out, in multiples of the
	// longest recursive period. The strategy only receives the last
	// WarmupPeriod candles, and with this margin their values match the ones
	// of a chart computed over the whole history.
	rkSeedFactor = 10

	// rkMinPrice keeps the ATR % of price finite, where the indicator uses the
	// symbol tick size.
	rkMinPrice = 1e-12
)

// RKTrendConfluenceConfig holds the inputs of the indicator, with its default
// values in DefaultRKTrendConfluenceConfig. Periods are counted in candles of
// Timeframe.
type RKTrendConfluenceConfig struct {
	Timeframe string

	// Trend & moving averages
	EMAFastLen       int // must be strictly shorter than EMASlowLen
	EMASlowLen       int
	TrendMALen       int     // SMA that defines the regime
	RequireSlowTrend bool    // the slow EMA must be on the trend side of the trend MA too
	RegimeBufferATR  float64 // distance price and slow EMA must clear the trend MA by, in ATR

	// MACD
	MACDFast             int
	MACDSlow             int
	MACDSignal           int
	RequireHistExpansion bool
	RequireMACDZeroSide  bool

	// RSI band. An EMA cross never happens at an RSI extreme, so it only
	// rejects signals once EntryWindow is widened.
	UseRSIFilter bool
	RSILen       int
	RSIMidline   float64 // a buy needs RSI above it, a sell below 100 minus it
	RSIExtreme   float64 // a buy is rejected at or above it, a sell at or below 100 minus it

	// Trend slope, measured per bar, so SlopeLookback smooths it without making
	// the filter stricter
	UseSlopeFilter        bool
	SlopeLookback         int
	SlopeMode             RKSlopeMode
	SlopeATRThreshold     float64 // ATR per bar
	SlopePercentThreshold float64 // % of price per bar

	// ADX / direction
	UseADXFilter       bool
	ADXDILength        int
	ADXSmoothing       int
	ADXThreshold       float64
	RequireDIDirection bool

	// Volatility. ATRLen also drives the ATR normalized slope, the regime
	// buffer and the EMA separation, so it matters with the filter off too.
	ATRLen         int
	UseATRFilter   bool
	ATRFilterMode  RKVolatilityMode
	VolBaselineLen int
	MinVolRatio    float64 // ATR / average ATR, relative mode
	MaxVolRatio    float64
	MinATRPct      float64 // ATR % of price, percent mode
	MaxATRPct      float64

	// Higher timeframe
	UseHTFFilter bool
	HTFMode      RKHTFMode
	HTFMult      int
	HTFManual    string // eg: 4h, 1d
	HTFTrendLen  int

	// Signal
	Direction      RKDirection
	EntryWindow    int     // bars after the cross a signal may still fire, 1 = the cross bar only
	CooldownBars   int     // minimum bars between signals of the same side, raised to EntryWindow
	MinCrossSepATR float64 // minimum distance between the EMAs at the signal, in ATR

	// Exits. The indicator has no exit rule, so by default a position is only
	// closed by the opposite signal; these close it earlier. They are checked
	// on the candle close, like the signals, so a wick alone never triggers
	// them. Both are in ATR, and 0 disables them.
	StopATR     float64 // stop from the entry close
	TrailingATR float64 // trailing stop from the best price since the entry: highest high, lowest low for a short

	// A stop placed at the broker with the entry, like the stops above in ATR
	// and 0 to disable them: BrokerStopATR from the entry close, and
	// BrokerTrailingATR from the best price since the entry close, the tighter
	// of them. The position stays protected while the bot is off. The broker
	// closes the position as soon as the price touches the stop, a wick
	// included, so at the distances of the stops on the close or beyond it only
	// backs them up. The trailing is moved at the broker on every candle close
	// while the bot runs, with the ATR of the candle, and only ever tightens.
	// The broker must place stops with the orders, see service.ProtectionBroker.
	BrokerStopATR     float64
	BrokerTrailingATR float64

	// StopValue and TakeProfitValue, above 0, place a stop loss and a take
	// profit at the broker with the entry at a fixed value: what the position
	// loses, or earns, when the price reaches them, in the currency of the
	// account (in the paper wallet, of the simulation, the quote), eg: 5 and 10
	// for five and ten reais on a BRL account, at that value over the size of
	// the position times its point value (see service.PointValuer) from the
	// entry close, whatever the ATR.
	// StopValue replaces the stops in ATR above, which are then unused. The
	// broker must place them with the orders, see service.ProtectionBroker.
	StopValue       float64
	TakeProfitValue float64

	// TakeProfitATR, above 0, places a take profit at the broker with the
	// entry, TakeProfitATR ATR away from the entry close: the position closed
	// there, before the opposite signal. TakeProfitValue still replaces it. 0
	// for none. The broker must place it with the order, see
	// service.ProtectionBroker.
	TakeProfitATR float64

	// MinPositionValue is the value, in the currency of the account (in the
	// paper wallet, the quote), below which a balance is dust rather than a
	// position: 10 by default, as in the other example strategies.
	MinPositionValue float64

	// Sizing. A position commits PositionFraction of the balance available for
	// new positions (the cash of the broker's Position, in the currency of the
	// account), 1 by default: all of it. Its value is the committed balance
	// times Leverage, the leverage of the account on the market, 1 by default:
	// none. With leverage, PositionFraction must be below 1, so the whole
	// balance is never leveraged at once.
	//
	// PositionSize, by pair, sets instead a fixed size for the positions, a
	// lot: in the units the broker trades the market in, eg: 0.01 BTC of
	// BTCUSDm on MT5, once its lots are converted. PositionFraction is then
	// unused, and a position is refused when worth more than the balance
	// available times Leverage; a pair without a size opens none.
	PositionFraction float64
	Leverage         float64
	PositionSize     map[string]float64

	// MaxRiskPct, above 0, skips an entry losing more than that % of the
	// balance available (the quote of the broker's Position) at its stop loss:
	// the tightest of the stops in ATR, on the close and at the broker, or the
	// one of StopValue. It needs one of them. 0 sets no limit.
	MaxRiskPct float64
}

// DefaultRKTrendConfluenceConfig returns the defaults of the indicator, on the
// 1h timeframe.
func DefaultRKTrendConfluenceConfig() RKTrendConfluenceConfig {
	return RKTrendConfluenceConfig{
		Timeframe: "1h",

		EMAFastLen:       9,
		EMASlowLen:       21,
		TrendMALen:       200,
		RequireSlowTrend: true,

		MACDFast:   12,
		MACDSlow:   26,
		MACDSignal: 9,

		RSILen:     14,
		RSIMidline: 50,
		RSIExtreme: 70,

		UseSlopeFilter:        true,
		SlopeLookback:         5,
		SlopeMode:             RKSlopeATR,
		SlopeATRThreshold:     0.02,
		SlopePercentThreshold: 0.01,

		UseADXFilter:       true,
		ADXDILength:        14,
		ADXSmoothing:       14,
		ADXThreshold:       20,
		RequireDIDirection: true,

		ATRLen:         14,
		ATRFilterMode:  RKVolatilityRelative,
		VolBaselineLen: 100,
		MinVolRatio:    0.7,
		MaxVolRatio:    2.5,
		MinATRPct:      0.1,
		MaxATRPct:      10,

		HTFMode:     RKHTFAuto,
		HTFMult:     4,
		HTFManual:   "1h",
		HTFTrendLen: 50,

		Direction:   RKDirectionBoth,
		EntryWindow: 1,

		MinPositionValue: 10,

		PositionFraction: 1,
		Leverage:         1,
	}
}

func (c RKTrendConfluenceConfig) validate() error {
	periods := []int{c.EMAFastLen, c.EMASlowLen, c.TrendMALen, c.MACDFast, c.MACDSlow, c.MACDSignal, c.RSILen,
		c.SlopeLookback, c.ADXDILength, c.ADXSmoothing, c.ATRLen, c.VolBaselineLen, c.HTFTrendLen, c.EntryWindow}
	for _, period := range periods {
		if period < 1 {
			return errors.New("periods, lookbacks and the entry window must be at least 1")
		}
	}
	if err := rkCheckSizes(c.PositionSize); err != nil {
		return err
	}

	// Halting is correct here, as in the indicator: with the fast EMA longer
	// than the slow one the crossover trigger silently inverts its meaning.
	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case c.EMAFastLen >= c.EMASlowLen:
		return errors.New("fast EMA period must be strictly shorter than the slow EMA period")
	case c.MACDFast >= c.MACDSlow:
		return errors.New("MACD fast length must be strictly shorter than the MACD slow length")
	case c.UseATRFilter && c.ATRFilterMode == RKVolatilityPercent && c.MinATRPct > c.MaxATRPct:
		return errors.New("volatility filter: minimum ATR % must be <= maximum ATR %")
	case c.UseATRFilter && c.ATRFilterMode == RKVolatilityRelative && c.MinVolRatio > c.MaxVolRatio:
		return errors.New("volatility filter: min ATR / average ATR must be <= max ATR / average ATR")
	case c.UseRSIFilter && c.RSIMidline >= c.RSIExtreme:
		return errors.New("RSI filter: momentum floor must be below the overbought limit")
	case c.UseHTFFilter && c.HTFMult < 2:
		return errors.New("auto HTF multiple must be at least 2")
	case c.CooldownBars < 0:
		return errors.New("cooldown bars can't be negative")
	case c.StopATR < 0 || c.TrailingATR < 0 || c.BrokerStopATR < 0 || c.BrokerTrailingATR < 0:
		return errors.New("stop and trailing stop ATR multiples can't be negative")
	case c.TakeProfitATR < 0:
		return errors.New("the take profit ATR multiple can't be negative")
	case c.StopValue < 0 || c.TakeProfitValue < 0:
		return errors.New("the fixed stop loss and take profit values can't be negative")
	case c.MaxRiskPct < 0:
		return errors.New("the max risk can't be negative")
	case c.MaxRiskPct > 0 && c.StopValue == 0 && c.StopATR == 0 && c.TrailingATR == 0 && c.BrokerStopATR == 0 &&
		c.BrokerTrailingATR == 0:
		return errors.New("the max risk is what the position loses at its stop loss: it needs one (stop-atr, " +
			"trailing-atr, broker-stop-atr, broker-trailing-atr or stop-value)")
	case c.MinPositionValue <= 0:
		return errors.New("min position value must be positive")
	case c.PositionFraction <= 0 || c.PositionFraction > 1:
		return errors.New("position fraction must be above 0 and at most 1")
	case c.Leverage < 1:
		return errors.New("leverage must be at least 1")
	case c.Leverage > 1 && c.PositionFraction >= 1 && len(c.PositionSize) == 0:
		return errors.New("with leverage, the position fraction must be below 1: a position can't commit the " +
			"whole balance")
	case c.SlopeMode != RKSlopeATR && c.SlopeMode != RKSlopePercent:
		return fmt.Errorf("invalid slope mode: %q", c.SlopeMode)
	case c.ATRFilterMode != RKVolatilityRelative && c.ATRFilterMode != RKVolatilityPercent:
		return fmt.Errorf("invalid volatility mode: %q", c.ATRFilterMode)
	case c.HTFMode != RKHTFAuto && c.HTFMode != RKHTFManual:
		return fmt.Errorf("invalid HTF mode: %q", c.HTFMode)
	case c.Direction != RKDirectionBoth && c.Direction != RKDirectionBuyOnly && c.Direction != RKDirectionSellOnly:
		return fmt.Errorf("invalid direction: %q", c.Direction)
	}
	return nil
}

// higherTimeframe resolves the timeframe of the HTF gate like the indicator:
// the manual one when it is strictly higher than the chart timeframe, otherwise
// the chart timeframe times HTFMult, rounded down to whole minutes, days, weeks
// or months (at most 12).
func (c RKTrendConfluenceConfig) higherTimeframe(chart time.Duration) (time.Duration, error) {
	if c.HTFMode == RKHTFManual {
		manual, err := str2duration.ParseDuration(c.HTFManual)
		if err != nil {
			return 0, fmt.Errorf("invalid manual higher timeframe %q: %w", c.HTFManual, err)
		}
		if manual > chart {
			return manual, nil
		}
	}

	auto := chart * time.Duration(c.HTFMult)
	switch {
	case auto < time.Minute:
		return time.Minute, nil
	case auto < rkDay:
		return auto.Truncate(time.Minute), nil
	case auto < rkWeek:
		return auto.Truncate(rkDay), nil
	case auto < rkMonth:
		return auto.Truncate(rkWeek), nil
	default:
		return min(auto/rkMonth, 12) * rkMonth, nil
	}
}

// RKTrendConfluence trades the signals of the RK Trend Confluence TradingView
// indicator (RK TCF).
//
// The trigger is the fast EMA crossing the slow EMA, optionally still valid for
// a few bars. The gates are the trend MA regime, the trend MA slope, ADX/DI,
// MACD momentum and, optionally, an RSI band, the volatility context and a
// higher timeframe confirmation. Thresholds are expressed in units that mean
// the same thing on every asset: ATR, the ATR relative to its own average, and
// bounded oscillators.
//
// Signals are evaluated once the candle closes, the indicator's default. Its
// intrabar mode is not offered: those signals may vanish before the close.
//
// The indicator has no exit rule, so the strategy follows its signals: a BUY
// closes a short and opens a long, a SELL closes a long and opens a short.
// Direction restricts the side of the positions opened, an opposite signal
// always closes the open one. StopATR and TrailingATR add ATR stops, checked
// before the signals of each candle. Positions are opened with the whole quote
// balance.
type RKTrendConfluence struct {
	cfg    RKTrendConfluenceConfig
	sizing rkSizing
	htf    time.Duration // timeframe of the HTF gate, 0 while it is disabled
	warmup int
	trades map[string]*rkTrade // positions followed by the stops, by pair

	protection map[string]*rkProtection  // stops placed at the broker with the positions, by pair
	exits      map[string]*rkExit        // closes that failed, retried on the next candles, by pair
	followed   map[string]model.SideType // the side of the positions followed, by pair, see follows

	mu      sync.Mutex                      // guards decided: the chart reads it from another goroutine
	decided map[string]map[int64]rkDecision // signals of the closed candles of the window, by pair and time
}

// rkTrade is a position followed by the stops: the best price reached since
// the entry and the stop, which only ever tightens.
type rkTrade struct {
	side    model.SideType // BUY for a long, SELL for a short
	extreme float64        // highest high of a long, lowest low of a short
	stop    float64

	// a position found held, eg: opened before a restart: set up on its last
	// candle, its stop applying from the next (fresh); or, see MATrend.adopt,
	// its stop at the broker to move to the one followed (adopted)
	fresh, adopted bool
}

// sign is 1 for a long and -1 for a short, so that prices compare the same way
// on both sides.
func (t *rkTrade) sign() float64 {
	return rkSign(t.side)
}

func rkSign(side model.SideType) float64 {
	if side == model.SideTypeSell {
		return -1
	}
	return 1
}

// rkLossLevel returns the level at which the position of size opened on side
// at price loses value, in the currency of the cash (the account's at MT5),
// point its point value (see rkCash): below price for a long, above for a
// short. See the StopValue of the configs.
func rkLossLevel(side model.SideType, price, size, point, value float64) float64 {
	return price - rkSign(side)*value/(size*point)
}

// rkProfitLevel returns the level at which the position of size opened on side
// at price earns value, in the currency of the cash, point its point value.
// See the TakeProfitValue of the configs.
func rkProfitLevel(side model.SideType, price, size, point, value float64) float64 {
	return price + rkSign(side)*value/(size*point)
}

// rkProtectionLeg returns the stop loss among the legs protecting a position,
// the ones a protected order returns after its entry, one at least, or else its
// take profit. Canceling either leg removes both, see service.ProtectionBroker.
func rkProtectionLeg(legs []model.Order) model.Order {
	for _, leg := range legs {
		if leg.Type == model.OrderTypeStopLoss {
			return leg
		}
	}
	return legs[0]
}

// rkProtection is the stop placed at the broker with a position: its leg, its
// level and the best price since the entry, which the trailing follows, and
// the level of the take profit placed with it, 0 for none.
type rkProtection struct {
	side    model.SideType // of the position
	leg     model.Order
	stop    float64
	extreme float64 // highest high of a long, lowest low of a short, from the entry close
	profit  float64
}

// tighten moves the stop to price when that is closer to the market.
func (t *rkTrade) tighten(price float64) {
	if t.sign()*price > t.sign()*t.stop {
		t.stop = price
	}
}

// NewRKTrendConfluence validates the configuration and creates the strategy.
func NewRKTrendConfluence(cfg RKTrendConfluenceConfig) (*RKTrendConfluence, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("rk trend confluence: %w", err)
	}

	s := &RKTrendConfluence{
		cfg: cfg,
		sizing: rkSizing{sizes: cfg.PositionSize, fraction: cfg.PositionFraction, leverage: cfg.Leverage,
			maxRisk:  cfg.MaxRiskPct,
			minValue: cfg.MinPositionValue},
		trades:     make(map[string]*rkTrade),
		protection: make(map[string]*rkProtection),
		exits:      make(map[string]*rkExit),
		followed:   make(map[string]model.SideType),
		decided:    make(map[string]map[int64]rkDecision),
	}
	var chart time.Duration
	if cfg.UseHTFFilter {
		var err error
		chart, err = str2duration.ParseDuration(cfg.Timeframe)
		if err != nil {
			return nil, fmt.Errorf("rk trend confluence: invalid timeframe %q: %w", cfg.Timeframe, err)
		}

		s.htf, err = cfg.higherTimeframe(chart)
		if err != nil {
			return nil, fmt.Errorf("rk trend confluence: %w", err)
		}
	}
	s.warmup = s.warmupPeriod(chart)

	return s, nil
}

func (s *RKTrendConfluence) warmupPeriod(chart time.Duration) int {
	c := s.cfg
	bars := max(
		c.TrendMALen+c.SlopeLookback, // trend MA and its value SlopeLookback bars back
		c.ATRLen+c.VolBaselineLen,    // average ATR of the volatility filter
		c.MACDSlow+c.MACDSignal,      // histogram and its previous value
		c.ADXDILength+c.ADXSmoothing,
		c.RSILen+1,
		c.EMASlowLen+c.EntryWindow,
	)
	if s.htf > 0 {
		// the HTF trend MA of the last two closed HTF bars
		barsPerHTF := int((s.htf + chart - 1) / chart)
		bars = max(bars, (c.HTFTrendLen+3)*barsPerHTF)
	}

	// signals are evaluated over the window, so a signal that still blocks the
	// current one by cooldown must be inside it
	cooldown := max(c.CooldownBars, c.EntryWindow)
	recursive := max(c.EMASlowLen, c.MACDSlow+c.MACDSignal, c.ATRLen, c.ADXDILength+c.ADXSmoothing, c.RSILen)

	return bars + cooldown + rkSeedFactor*recursive
}

func (s *RKTrendConfluence) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *RKTrendConfluence) WarmupPeriod() int {
	return s.warmup
}

func (s *RKTrendConfluence) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	series := s.series(df)
	s.mu.Lock()
	buy, sell := s.signals(series, df.Time, s.decided[df.Pair])
	s.mu.Unlock()

	df.Metadata["rk_ema_fast"] = series.emaFast
	df.Metadata["rk_ema_slow"] = series.emaSlow
	df.Metadata["rk_trend_ma"] = series.trendMA
	df.Metadata["rk_atr"] = series.atr
	df.Metadata["rk_buy"] = flags(buy)
	df.Metadata["rk_sell"] = flags(sell)

	return []strategy.ChartIndicator{
		{
			Overlay:   true,
			GroupName: "RK Trend Confluence",
			Time:      df.Time,
			Warmup:    s.cfg.TrendMALen,
			Metrics: []strategy.IndicatorMetric{
				{
					Values: df.Metadata["rk_ema_fast"],
					Name:   fmt.Sprintf("Fast EMA %d", s.cfg.EMAFastLen),
					Color:  "#00bd4e",
					Style:  strategy.StyleLine,
				},
				{
					Values: df.Metadata["rk_ema_slow"],
					Name:   fmt.Sprintf("Slow EMA %d", s.cfg.EMASlowLen),
					Color:  "#c90032",
					Style:  strategy.StyleLine,
				},
				{
					Values: df.Metadata["rk_trend_ma"],
					Name:   fmt.Sprintf("Trend MA %d", s.cfg.TrendMALen),
					Color:  "#fd8700",
					Style:  strategy.StyleLine,
				},
			},
		},
	}
}

func (s *RKTrendConfluence) OnCandle(df *model.Dataframe, broker service.Broker) {
	s.remember(df)

	var side model.SideType
	switch {
	case df.Metadata["rk_buy"].Last(0) > 0:
		side = model.SideTypeBuy
	case df.Metadata["rk_sell"].Last(0) > 0:
		side = model.SideTypeSell
	}

	fixed := s.cfg.StopValue > 0 // in place of the stops in ATR
	stops := !fixed && (s.cfg.StopATR > 0 || s.cfg.TrailingATR > 0)
	if side == "" && !stops && (fixed || s.cfg.BrokerTrailingATR == 0) && s.exits[df.Pair] == nil {
		return
	}

	closePrice := df.Close.Last(0)
	if side != "" {
		log.Infof("[RK TCF] %s %s | %s | close %f | ATR %f",
			df.Pair, s.cfg.Timeframe, side, closePrice, df.Metadata["rk_atr"].Last(0))
	}

	_, held, err := s.position(df, broker)
	if err != nil {
		log.Error(err)
		return
	}
	if exit, ok := s.exits[df.Pair]; ok && exit.side != held {
		delete(s.exits, df.Pair) // closed meanwhile, by its legs or by someone else
	}
	if s.followed[df.Pair] != held {
		delete(s.followed, df.Pair)
	}
	if held == "" {
		delete(s.protection, df.Pair) // the broker stop closed the position, if any
	} else if !s.follows(df.Pair, held, stops) {
		s.adopt(df, broker, held, stops) // a position opened before a restart, or by someone else
	}

	// a close that failed is retried before anything else, the position
	// protected meanwhile
	if _, ok := s.exits[df.Pair]; ok {
		log.Infof("[RK TCF] %s %s | RETRY | close %f", df.Pair, s.cfg.Timeframe, closePrice)
		if !s.close(df, broker, held) {
			return
		}
		held = ""
	}

	// the stops come first: a signal on the candle of a stop applies to the
	// flat position it leaves
	if stops {
		if held != "" && s.stopped(df, held) {
			log.Infof("[RK TCF] %s %s | STOP | close %f", df.Pair, s.cfg.Timeframe, closePrice)
			if !s.close(df, broker, held) {
				return
			}
			held = ""
		}

		if held == "" {
			delete(s.trades, df.Pair)
		}
	}

	// an opposite signal closes the position rather than trailing its stop
	if held != "" && (side == "" || side == held) {
		s.trailBroker(df, broker, held)
	}

	if side == "" || side == held {
		return // no signal, or already positioned on its side
	}

	// a BUY closes a short and a SELL closes a long
	if held != "" {
		if !s.close(df, broker, held) {
			return
		}
	}

	if !s.opens(side) {
		return
	}

	cash, err := rkCashOf(broker, df.Pair)
	if err != nil {
		log.Error(err)
		return
	}

	size, value, ok := s.sizing.position(df.Pair, cash, closePrice)
	if !ok || s.sizing.risky(df.Pair, closePrice, s.entryStop(df, side, size, cash.point), size, cash) {
		return
	}

	if s.cfg.BrokerStopATR > 0 || s.cfg.BrokerTrailingATR > 0 || fixed || s.cfg.TakeProfitValue > 0 ||
		s.cfg.TakeProfitATR > 0 {
		err = s.openProtected(df, broker, side, size, cash.point)
	} else {
		err = s.sizing.open(broker, side, df.Pair, size, value)
	}
	if err != nil {
		log.Error(err)
	}

	// an order reported as failed may have been filled even so, eg: a request
	// that timed out, so the stops follow whatever position is held now
	if err == nil || s.holds(df, broker, side) {
		s.followed[df.Pair] = side
		if stops {
			s.trades[df.Pair] = s.newTrade(df, side)
		}
	}
}

// openProtected opens a position of side of size with its legs placed at the
// broker: the stop at StopValue, else the tighter of BrokerStopATR and
// BrokerTrailingATR from the close, if any, and the take profit at
// TakeProfitValue, else TakeProfitATR from the close, if any, point the point
// value of the pair.
func (s *RKTrendConfluence) openProtected(df *model.Dataframe, broker service.Broker, side model.SideType,
	size, point float64) error {

	protected, ok := broker.(service.ProtectionBroker)
	if !ok {
		return errors.New("rk trend confluence: the broker can't place the stop with the order")
	}

	closePrice, atr, sign := df.Close.Last(0), df.Metadata["rk_atr"].Last(0), rkSign(side)
	var stop, profit float64 // 0 for none
	if s.cfg.StopValue > 0 {
		stop = rkLossLevel(side, closePrice, size, point, s.cfg.StopValue)
	} else {
		for _, distance := range []float64{s.cfg.BrokerStopATR, s.cfg.BrokerTrailingATR} {
			if level := closePrice - sign*distance*atr; distance > 0 && (stop == 0 || sign*level > sign*stop) {
				stop = level
			}
		}
	}
	switch {
	case s.cfg.TakeProfitValue > 0:
		profit = rkProfitLevel(side, closePrice, size, point, s.cfg.TakeProfitValue)
	case s.cfg.TakeProfitATR > 0 && !isNa(atr):
		profit = closePrice + sign*s.cfg.TakeProfitATR*atr
	}

	orders, err := protected.CreateOrderMarketProtected(side, df.Pair, size, stop, profit)
	if len(orders) > 1 {
		s.protection[df.Pair] = &rkProtection{side: side, leg: rkProtectionLeg(orders[1:]), stop: stop,
			extreme: closePrice, profit: rkLevelsOf(orders[1:]).profit}
	}
	return err
}

// entryStop returns the stop loss of an entry of side of size on the last
// candle of df: the tightest of the stops on the close (StopATR, TrailingATR)
// and of the one at the broker (BrokerStopATR, BrokerTrailingATR), or the one
// of StopValue, point the point value of the pair; 0 for none.
func (s *RKTrendConfluence) entryStop(df *model.Dataframe, side model.SideType, size, point float64) float64 {
	closePrice, atr, sign := df.Close.Last(0), df.Metadata["rk_atr"].Last(0), rkSign(side)
	if s.cfg.StopValue > 0 {
		return rkLossLevel(side, closePrice, size, point, s.cfg.StopValue)
	}
	stop := 0.0
	if s.cfg.StopATR > 0 || s.cfg.TrailingATR > 0 {
		if trade := s.newTrade(df, side); !math.IsInf(trade.stop, 0) && !isNa(trade.stop) {
			stop = trade.stop
		}
	}
	for _, distance := range []float64{s.cfg.BrokerStopATR, s.cfg.BrokerTrailingATR} {
		if level := closePrice - sign*distance*atr; distance > 0 && !isNa(level) && (stop == 0 || sign*level > sign*stop) {
			stop = level
		}
	}
	return stop
}

// follows reports whether the strategy follows the position held on side of
// pair: one it opened, or one found held (see adopt), with its stops, if any.
func (s *RKTrendConfluence) follows(pair string, held model.SideType, stops bool) bool {
	if stops {
		trade, ok := s.trades[pair]
		return ok && trade.side == held
	}
	if protection, ok := s.protection[pair]; ok && protection.side == held {
		return true
	}
	if exit, ok := s.exits[pair]; ok && exit.side == held {
		return true
	}
	return s.followed[pair] == held
}

// adopt follows the position held on side found on the last candle of df,
// eg: opened before a restart: with its legs at the broker, when it tells them
// (see rkFind), whose stop it trails from the best price since the entry, and
// with its stops, as they stood on the previous candle, replayed from the
// candle of its entry when df holds it; else set up from the last candle on,
// as for an entry.
func (s *RKTrendConfluence) adopt(df *model.Dataframe, broker service.Broker, held model.SideType, stops bool) {
	s.followed[df.Pair] = held
	found := rkFind(df, broker, "RK TCF", held)
	_, first, known := found.held(df, s.cfg.Timeframe)
	replay := known && first >= 1 && first < len(df.Close)

	delete(s.protection, df.Pair)
	if len(found.legs) > 0 {
		levels := rkLevelsOf(found.legs)
		protection := &rkProtection{side: held, leg: rkProtectionLeg(found.legs), stop: levels.stop,
			profit: levels.profit, extreme: df.Close.Last(0)}
		if replay {
			protection.extreme = rkExtreme(df, held, first)
		}
		s.protection[df.Pair] = protection
	}

	if !stops {
		return
	}
	atr := df.Metadata["rk_atr"]
	if !replay || len(atr) != len(df.Close) || len(df.High) != len(df.Close) || len(df.Low) != len(df.Close) ||
		isNa(atr[first-1]) {
		trade := s.newTrade(df, held)
		trade.fresh = true
		s.trades[df.Pair] = trade
		return
	}
	trade := s.newTradeAt(df, held, first-1)
	for i := first; i < len(df.Close)-1; i++ {
		if held == model.SideTypeBuy {
			trade.extreme = math.Max(trade.extreme, df.High[i])
		} else {
			trade.extreme = math.Min(trade.extreme, df.Low[i])
		}
		if !isNa(atr[i]) {
			s.trailAt(trade, df, i)
		}
	}
	s.trades[df.Pair] = trade
}

// close closes the position held on the pair at the market, and reports
// whether the pair is flat now. Where the broker needs it, its stop at the
// broker is canceled first, as it reserves the position: the close would
// otherwise open the other side, eg: in the paper wallet; a close that fails
// places it again, see rkCloseProtected, and is retried on the next candles.
func (s *RKTrendConfluence) close(df *model.Dataframe, broker service.Broker, held model.SideType) bool {
	var legs []model.Order
	var levels rkLevels
	if exit, ok := s.exits[df.Pair]; ok {
		levels = exit.bare
	}
	protection := s.protection[df.Pair]
	if protection != nil {
		legs = []model.Order{protection.leg}
		levels = rkLevels{stop: protection.stop, profit: protection.profit}
	}

	flat, kept, bare := rkCloseProtected(df, broker, "RK TCF", held, s.cfg.MinPositionValue, legs, levels)
	delete(s.protection, df.Pair)
	if flat {
		delete(s.exits, df.Pair)
		delete(s.trades, df.Pair)
		delete(s.followed, df.Pair)
		return true
	}
	s.exits[df.Pair] = &rkExit{side: held, bare: bare}
	if len(kept) > 0 {
		extreme := df.Close.Last(0)
		if protection != nil {
			extreme = protection.extreme
		}
		s.protection[df.Pair] = &rkProtection{side: held, leg: rkProtectionLeg(kept), stop: levels.stop,
			profit: levels.profit, extreme: extreme}
	}
	return false
}

// trailBroker moves the stop at the broker of the position held on the pair to
// BrokerTrailingATR from the best price since the entry, with the ATR of the
// last candle. It only tightens, and stays on the losing side of the close,
// where the broker takes it.
func (s *RKTrendConfluence) trailBroker(df *model.Dataframe, broker service.Broker, held model.SideType) {
	protection, ok := s.protection[df.Pair]
	if !ok || protection.side != held || s.cfg.BrokerTrailingATR <= 0 || s.cfg.StopValue > 0 ||
		protection.leg.Type != model.OrderTypeStopLoss {
		return
	}
	protected, ok := broker.(service.ProtectionBroker)
	if !ok {
		return
	}

	sign := rkSign(held)
	if held == model.SideTypeBuy {
		protection.extreme = math.Max(protection.extreme, df.High.Last(0))
	} else {
		protection.extreme = math.Min(protection.extreme, df.Low.Last(0))
	}
	stop := protection.extreme - sign*s.cfg.BrokerTrailingATR*df.Metadata["rk_atr"].Last(0)
	if sign*stop <= sign*protection.stop || sign*stop >= sign*df.Close.Last(0) {
		return
	}

	leg, err := protected.UpdateProtection(protection.leg, stop)
	if live, ok := tighterAlready(err); ok {
		// the stop guard kept a more protective stop: followed from it
		protection.leg, protection.stop = live, rkLegLevel(live)
		log.Infof("[RK TCF] %s %s | BROKER STOP kept at %f, more protective than %f", df.Pair, s.cfg.Timeframe,
			protection.stop, stop)
		return
	}
	if err != nil {
		log.Error(err)
		return
	}
	protection.leg, protection.stop = leg, stop
	log.Infof("[RK TCF] %s %s | BROKER STOP %f", df.Pair, s.cfg.Timeframe, stop)
}

// position returns the asset held on the pair and the side of the position:
// BUY for a long, SELL for a short, none for dust.
func (s *RKTrendConfluence) position(df *model.Dataframe, broker service.Broker) (float64, model.SideType, error) {
	asset, _, err := broker.Position(df.Pair)
	if err != nil {
		return 0, "", err
	}

	return asset, rkHeld(rkWorth(broker, df.Pair, asset, df.Close.Last(0)), s.cfg.MinPositionValue), nil
}

// rkHeld returns the side of a position worth value, negative for a short:
// none when it is worth less than minValue, dust.
func rkHeld(value, minValue float64) model.SideType {
	switch {
	case value >= minValue:
		return model.SideTypeBuy
	case value <= -minValue:
		return model.SideTypeSell
	}
	return ""
}

// rkClose closes the position held on side at the market, without legs at the
// broker, and reports whether the pair is flat now, see rkCloseProtected.
func rkClose(df *model.Dataframe, broker service.Broker, side model.SideType, minValue float64) bool {
	flat, _, _ := rkCloseProtected(df, broker, "", side, minValue, nil, rkLevels{})
	return flat
}

// rkCash is the cash available for a new position on a pair and the point
// value of the pair: the value of a move of 1 of its price for 1 unit, both in
// the currency of the cash, the account's at MT5 (see service.PointValuer), 1
// in the paper wallet, valuing the pairs in their quote.
type rkCash struct {
	free, point float64
}

// rkCashOf reads the cash of broker for a new position on pair.
func rkCashOf(broker service.Broker, pair string) (rkCash, error) {
	_, free, err := broker.Position(pair)
	if err != nil {
		return rkCash{}, err
	}
	point, err := rkPointValue(broker, pair)
	if err != nil {
		return rkCash{}, err
	}
	return rkCash{free: free, point: point}, nil
}

// rkPointValue returns the point value of pair at broker, see rkCash, an error
// when it isn't positive.
func rkPointValue(broker service.Broker, pair string) (float64, error) {
	point, err := service.PointValue(broker, pair)
	switch {
	case err != nil:
		return 0, fmt.Errorf("%s: reading the point value: %w", pair, err)
	case math.IsNaN(point) || math.IsInf(point, 0) || point <= 0:
		return 0, fmt.Errorf("%s: no point value, %g", pair, point)
	}
	return point, nil
}

// rkWorth returns the value of asset units of pair at price in the currency of
// the cash, to tell dust apart: by the point value of the pair, or 1 while it
// can't be read.
func rkWorth(broker service.Broker, pair string, asset, price float64) float64 {
	point, err := rkPointValue(broker, pair)
	if err != nil {
		log.Debug(err)
		point = 1
	}
	return asset * price * point
}

// rkSizing sizes the positions of a strategy, see the Sizing of
// RKTrendConfluenceConfig: sizes are the PositionSize of the pairs, if any.
// The money is in the currency of the cash, see rkCash.
type rkSizing struct {
	sizes              map[string]float64
	fraction, leverage float64
	minValue           float64
	maxRisk            float64 // % of the balance lost at the stop loss at most, 0 for no limit, see risky
}

// position returns the size, in the asset, and the value, in the currency of
// the cash, of the position to open on pair at price. ok is false when there
// is none: a share of the balance worth less than minValue, or a fixed size
// missing, worth less than minValue, it would be taken for dust, or more than
// the cash times the leverage, all the balance can take.
func (z rkSizing) position(pair string, cash rkCash, price float64) (size, value float64, ok bool) {
	if len(z.sizes) == 0 {
		value = cash.free * z.fraction * z.leverage
		return value / (price * cash.point), value, value >= z.minValue
	}

	size, ok = z.sizes[pair]
	value = size * price * cash.point
	switch {
	case !ok:
		log.Errorf("%s: no position size set for the pair", pair)
	case value < z.minValue:
		log.Errorf("%s: a position of %g is worth %f, under the min position value of %g: it would be taken "+
			"for dust", pair, size, value, z.minValue)
	case value > cash.free*z.leverage:
		log.Errorf("%s: a position of %g is worth %f, more than the %f available times the leverage of %g",
			pair, size, value, cash.free, z.leverage)
	default:
		return size, value, true
	}
	return 0, 0, false
}

// open opens the position of side on pair at the market: by its size when the
// sizes are fixed, else by its value, in the currency of the cash.
func (z rkSizing) open(broker service.Broker, side model.SideType, pair string, size, value float64) error {
	var err error
	if len(z.sizes) > 0 {
		_, err = broker.CreateOrderMarket(side, pair, size)
	} else {
		_, err = broker.CreateOrderMarketQuote(side, pair, value)
	}
	return err
}

// risky reports whether a position of size opened on pair at price, its stop
// loss at stop, would lose more than maxRisk % of the free cash at it, both in
// the currency of the cash, logged as why the entry is skipped: never without
// a limit (maxRisk 0), nor without a stop loss (stop 0).
func (z rkSizing) risky(pair string, price, stop, size float64, cash rkCash) bool {
	if z.maxRisk <= 0 || stop <= 0 || cash.free <= 0 {
		return false
	}
	risk := math.Abs(price-stop) * size * cash.point
	if risk <= cash.free*z.maxRisk/100 {
		return false
	}
	log.Infof("%s: an entry of %g at %f would lose %f at its stop loss %f, %.2f%% of the free balance of %f, "+
		"above the max risk of %g%% (max-risk-pct): skipped", pair, size, price, risk, stop, risk/cash.free*100,
		cash.free, z.maxRisk)
	return true
}

// rkCheckSizes checks the PositionSize of a configuration.
func rkCheckSizes(sizes map[string]float64) error {
	for pair, size := range sizes {
		if math.IsNaN(size) || math.IsInf(size, 0) || size <= 0 {
			return fmt.Errorf("the position size of %s must be positive, not %g", pair, size)
		}
	}
	return nil
}

// holds reports whether a position of side is held on the pair.
func (s *RKTrendConfluence) holds(df *model.Dataframe, broker service.Broker, side model.SideType) bool {
	_, held, err := s.position(df, broker)
	if err != nil {
		log.Error(err)
	}
	return err == nil && held == side
}

// remember keeps the signals of the closed candles of the window, so the next
// window continues their cooldown chain. Older candles are forgotten.
func (s *RKTrendConfluence) remember(df *model.Dataframe) {
	buy, sell := df.Metadata["rk_buy"], df.Metadata["rk_sell"]
	if len(df.Time) == 0 || len(buy) != len(df.Time) || len(sell) != len(df.Time) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	decided := s.decided[df.Pair]
	if decided == nil {
		decided = make(map[int64]rkDecision, len(df.Time))
		s.decided[df.Pair] = decided
	}

	for i, t := range df.Time {
		decided[t.Unix()] = rkDecision{buy: buy[i] > 0, sell: sell[i] > 0}
	}

	oldest := df.Time[0].Unix()
	for key := range decided {
		if key < oldest {
			delete(decided, key)
		}
	}
}

// newTrade starts following a position of side from the last candle: its stop
// is StopATR from the close and TrailingATR from the candle's high (low for a
// short), whichever is tighter.
func (s *RKTrendConfluence) newTrade(df *model.Dataframe, side model.SideType) *rkTrade {
	return s.newTradeAt(df, side, len(df.Close)-1)
}

// newTradeAt is newTrade on the candle i of df.
func (s *RKTrendConfluence) newTradeAt(df *model.Dataframe, side model.SideType, i int) *rkTrade {
	trade := &rkTrade{side: side, extreme: df.High[i]}
	if side == model.SideTypeSell {
		trade.extreme = df.Low[i]
	}
	trade.stop = math.Inf(-1) * trade.sign()

	if s.cfg.StopATR > 0 {
		trade.tighten(df.Close[i] - trade.sign()*s.cfg.StopATR*df.Metadata["rk_atr"][i])
	}
	s.trailAt(trade, df, i)

	return trade
}

// stopped reports whether the last candle closed through the stop of the
// position held on the pair, and otherwise trails the stop with the candle.
func (s *RKTrendConfluence) stopped(df *model.Dataframe, held model.SideType) bool {
	trade, ok := s.trades[df.Pair]
	if !ok || trade.side != held {
		// a position opened by someone else meanwhile: its stops are
		// followed from this candle on, see adopt
		s.trades[df.Pair] = s.newTrade(df, held)
		return false
	}
	if trade.fresh {
		trade.fresh = false // set up on this candle, as for an entry
		return false
	}

	// the stop set up to the previous candle applies to this close
	if trade.sign()*df.Close.Last(0) <= trade.sign()*trade.stop {
		return true
	}

	if held == model.SideTypeBuy {
		trade.extreme = math.Max(trade.extreme, df.High.Last(0))
	} else {
		trade.extreme = math.Min(trade.extreme, df.Low.Last(0))
	}
	s.trail(trade, df)

	return false
}

// trail tightens the trailing stop to TrailingATR from the best price.
func (s *RKTrendConfluence) trail(trade *rkTrade, df *model.Dataframe) {
	s.trailAt(trade, df, len(df.Close)-1)
}

// trailAt is trail with the ATR of the candle i of df.
func (s *RKTrendConfluence) trailAt(trade *rkTrade, df *model.Dataframe, i int) {
	if s.cfg.TrailingATR > 0 {
		trade.tighten(trade.extreme - trade.sign()*s.cfg.TrailingATR*df.Metadata["rk_atr"][i])
	}
}

func oppositeSide(side model.SideType) model.SideType {
	if side == model.SideTypeBuy {
		return model.SideTypeSell
	}
	return model.SideTypeBuy
}

// opens reports whether Direction lets the strategy open a position on side.
func (s *RKTrendConfluence) opens(side model.SideType) bool {
	return rkOpens(s.cfg.Direction, side)
}

// rkOpens reports whether direction lets a strategy open a position on side.
func rkOpens(direction RKDirection, side model.SideType) bool {
	switch direction {
	case RKDirectionBuyOnly:
		return side == model.SideTypeBuy
	case RKDirectionSellOnly:
		return side == model.SideTypeSell
	}
	return true
}

// rkSeries holds the series read by the gates, one value per candle.
type rkSeries struct {
	close    []float64
	emaFast  []float64
	emaSlow  []float64
	trendMA  []float64
	macd     []float64
	hist     []float64
	rsi      []float64
	plusDI   []float64
	minusDI  []float64
	adx      []float64
	atr      []float64
	volRatio []float64 // ATR / its average over VolBaselineLen
	htfBias  []float64 // nil while the HTF gate is disabled
}

func (s *RKTrendConfluence) series(df *model.Dataframe) rkSeries {
	c := s.cfg
	series := rkSeries{
		close:    df.Close,
		emaFast:  pineEMA(df.Close, c.EMAFastLen),
		emaSlow:  pineEMA(df.Close, c.EMASlowLen),
		trendMA:  pineSMA(df.Close, c.TrendMALen),
		rsi:      pineRSI(df.Close, c.RSILen),
		atr:      pineATR(df.High, df.Low, df.Close, c.ATRLen),
		volRatio: make([]float64, len(df.Close)),
	}
	series.macd, _, series.hist = pineMACD(df.Close, c.MACDFast, c.MACDSlow, c.MACDSignal)
	series.plusDI, series.minusDI, series.adx = pineDMI(df.High, df.Low, df.Close, c.ADXDILength, c.ADXSmoothing)

	baseline := pineSMA(series.atr, c.VolBaselineLen)
	for i := range series.volRatio {
		series.volRatio[i] = na
		if baseline[i] > 0 {
			series.volRatio[i] = series.atr[i] / baseline[i]
		}
	}

	if s.htf > 0 {
		series.htfBias = htfBias(df.Time, df.Close, s.htf, c.HTFTrendLen)
	}

	return series
}

// signals evaluates the indicator on every candle and returns its confirmed
// BUY and SELL signals. Both sides are evaluated whatever the Direction, which
// only decides what the strategy does with them.
//
// Whether a signal passes the cooldown depends on the whole chain of previous
// signals, which Pine keeps in a var. The candles already in decided keep the
// signals the strategy decided for them, so the chain continues from there:
// only the ones it hasn't seen yet are rebuilt from the candles.
func (s *RKTrendConfluence) signals(series rkSeries, times []time.Time,
	decided map[int64]rkDecision) (buy, sell []bool) {

	c := s.cfg
	buy, sell = make([]bool, len(series.close)), make([]bool, len(series.close))

	// The cooldown is raised to the entry window, so one cross produces one
	// signal at most, and kept per side, so it never blocks a reversal.
	cooldown := max(c.CooldownBars, c.EntryWindow)
	lastCrossUp, lastCrossDown := -1, -1 // -1: never, Pine's na
	lastBuy, lastSell := -1, -1

	for i := range series.close {
		closePrice, trendMA, emaSlow, atr := series.close[i], series.trendMA[i], series.emaSlow[i], series.atr[i]

		// trend regime
		historyOk := !isNa(trendMA) && !isNa(atr)
		buffer := c.RegimeBufferATR * nz(atr)
		bullishRegime := historyOk && closePrice > trendMA+buffer && (!c.RequireSlowTrend || emaSlow > trendMA+buffer)
		bearishRegime := historyOk && closePrice < trendMA-buffer && (!c.RequireSlowTrend || emaSlow < trendMA-buffer)

		// trend slope
		slopeUp, slopeDown, slopeStrong := s.slope(series, i)
		buySlopeOk := gateOk(c.UseSlopeFilter, slopeStrong, true, slopeUp)
		sellSlopeOk := gateOk(c.UseSlopeFilter, slopeStrong, true, slopeDown)

		// ADX / DI
		adxStrong := series.adx[i] >= c.ADXThreshold
		buyADXOk := gateOk(c.UseADXFilter, adxStrong, c.RequireDIDirection, series.plusDI[i] > series.minusDI[i])
		sellADXOk := gateOk(c.UseADXFilter, adxStrong, c.RequireDIDirection, series.minusDI[i] > series.plusDI[i])

		// MACD momentum: a positive histogram is the MACD line above its signal
		hist, prevHist := series.hist[i], na
		if i > 0 {
			prevHist = series.hist[i-1]
		}
		macdBullish := hist > 0 && (!c.RequireHistExpansion || hist > prevHist) &&
			(!c.RequireMACDZeroSide || series.macd[i] > 0)
		macdBearish := hist < 0 && (!c.RequireHistExpansion || hist < prevHist) &&
			(!c.RequireMACDZeroSide || series.macd[i] < 0)

		// RSI band, the sell side mirrored about 50
		rsi := series.rsi[i]
		buyRSIOk := !c.UseRSIFilter || (rsi > c.RSIMidline && rsi < c.RSIExtreme)
		sellRSIOk := !c.UseRSIFilter || (rsi < 100-c.RSIMidline && rsi > 100-c.RSIExtreme)

		// volatility context
		volatilityOk := !c.UseATRFilter || s.volatilityOk(series, i)

		// higher timeframe
		buyHTFOk, sellHTFOk := true, true
		if series.htfBias != nil {
			buyHTFOk, sellHTFOk = series.htfBias[i] > 0, series.htfBias[i] < 0
		}

		// trigger: the cross, still valid EntryWindow bars while the EMAs keep
		// their order
		if crossover(series.emaFast, series.emaSlow, i) {
			lastCrossUp = i
		}
		if crossunder(series.emaFast, series.emaSlow, i) {
			lastCrossDown = i
		}
		triggerUp := series.emaFast[i] > emaSlow && lastCrossUp >= 0 && i-lastCrossUp < c.EntryWindow
		triggerDown := series.emaFast[i] < emaSlow && lastCrossDown >= 0 && i-lastCrossDown < c.EntryWindow

		separation := 0.0
		if atr > 0 && !isNa(series.emaFast[i]) && !isNa(emaSlow) {
			separation = math.Abs(series.emaFast[i]-emaSlow) / atr
		}
		separationOk := separation >= c.MinCrossSepATR

		rawBuy := bullishRegime && buySlopeOk && buyADXOk && macdBullish && buyRSIOk && volatilityOk &&
			buyHTFOk && separationOk && triggerUp
		rawSell := bearishRegime && sellSlopeOk && sellADXOk && macdBearish && sellRSIOk && volatilityOk &&
			sellHTFOk && separationOk && triggerDown

		if decision, ok := decided[unixTime(times, i)]; ok {
			buy[i], sell[i] = decision.buy, decision.sell
		} else {
			buy[i] = rawBuy && (lastBuy < 0 || i-lastBuy >= cooldown)
			sell[i] = rawSell && (lastSell < 0 || i-lastSell >= cooldown)
		}

		if buy[i] {
			lastBuy = i
		}
		if sell[i] {
			lastSell = i
		}
	}

	return buy, sell
}

// rkDecision holds the signals decided for a closed candle.
type rkDecision struct {
	buy, sell bool
}

// unixTime is the key of candle i in the decided signals, -1 without times.
func unixTime(times []time.Time, i int) int64 {
	if i >= len(times) {
		return -1
	}
	return times[i].Unix()
}

// slope reports the direction of the trend MA over SlopeLookback bars and
// whether its change per bar clears the threshold. Per bar, the threshold does
// not depend on the lookback.
func (s *RKTrendConfluence) slope(series rkSeries, i int) (up, down, strong bool) {
	c := s.cfg
	if i < c.SlopeLookback {
		return false, false, false
	}

	previous := series.trendMA[i-c.SlopeLookback]
	delta := series.trendMA[i] - previous
	perBar := delta / float64(c.SlopeLookback)

	strength, threshold := na, c.SlopeATRThreshold
	if c.SlopeMode == RKSlopePercent {
		threshold = c.SlopePercentThreshold
		if !isNa(previous) && previous != 0 {
			strength = math.Abs(perBar/previous) * 100
		}
	} else if !isNa(series.atr[i]) && series.atr[i] != 0 {
		strength = math.Abs(perBar) / series.atr[i]
	}

	return delta > 0, delta < 0, strength >= threshold
}

func (s *RKTrendConfluence) volatilityOk(series rkSeries, i int) bool {
	c := s.cfg
	if c.ATRFilterMode == RKVolatilityPercent {
		atrPct := nz(series.atr[i]) / math.Max(math.Abs(series.close[i]), rkMinPrice) * 100
		return atrPct >= c.MinATRPct && atrPct <= c.MaxATRPct
	}
	return series.volRatio[i] >= c.MinVolRatio && series.volRatio[i] <= c.MaxVolRatio
}

// gateOk reports whether a gate lets a signal through: it is disabled, or it
// is both strong enough and pointing the right way.
func gateOk(enabled, strong, dirRequired, dirOk bool) bool {
	return !enabled || (strong && (!dirRequired || dirOk))
}

// htfBias is the higher timeframe gate for every candle: 1 when the last closed
// HTF bar closed above a rising HTF trend MA, -1 in the mirror case, 0
// otherwise and NaN while the MA warms up. Reading closed HTF bars only is what
// the [1]/[2] offsets inside request.security(..., lookahead_on) do in the
// indicator: no future leak, at the cost of lagging up to one HTF bar.
func htfBias(times []time.Time, close []float64, htf time.Duration, length int) []float64 {
	bias := make([]float64, len(close))
	var closes, trend []float64 // closed HTF bars: close and trend MA
	var sum float64
	var bar time.Time
	for i := range close {
		start := htfBarStart(times[i], htf)
		if i > 0 && !start.Equal(bar) {
			// the HTF bar of the previous candle is closed
			closes = append(closes, close[i-1])
			sum += close[i-1]
			if len(closes) > length {
				sum -= closes[len(closes)-1-length]
			}

			ma := na
			if len(closes) >= length {
				ma = sum / float64(length)
			}
			trend = append(trend, ma)
		}
		bar = start

		bias[i] = na
		if k := len(trend); k >= 2 && !isNa(trend[k-2]) {
			last, ma, prev := closes[k-1], trend[k-1], trend[k-2]
			switch {
			case last > ma && ma > prev:
				bias[i] = 1
			case last < ma && ma < prev:
				bias[i] = -1
			default:
				bias[i] = 0
			}
		}
	}
	return bias
}

// htfBarStart returns the open time of the HTF bar holding t, for a market that
// trades around the clock: months start on the 1st, weeks on Monday, and
// intraday bars that don't divide the day restart at midnight UTC.
func htfBarStart(t time.Time, htf time.Duration) time.Time {
	t = t.UTC()
	switch {
	case htf >= rkMonth:
		months := int(htf / rkMonth)
		index := t.Year()*12 + int(t.Month()) - 1
		index -= index % months
		return time.Date(index/12, time.Month(index%12+1), 1, 0, 0, 0, 0, time.UTC)
	case htf%rkWeek == 0:
		monday := time.Date(1970, 1, 5, 0, 0, 0, 0, time.UTC)
		return monday.Add(t.Sub(monday) / htf * htf)
	case htf%rkDay == 0 || rkDay%htf == 0:
		epoch := time.Unix(0, 0).UTC()
		return epoch.Add(t.Sub(epoch) / htf * htf)
	default:
		midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		return midnight.Add(t.Sub(midnight) / htf * htf)
	}
}

// flags converts signals to a series: 1 on the candles with a signal, else 0.
func flags(signals []bool) model.Series[float64] {
	series := make(model.Series[float64], len(signals))
	for i, signal := range signals {
		if signal {
			series[i] = 1
		}
	}
	return series
}
