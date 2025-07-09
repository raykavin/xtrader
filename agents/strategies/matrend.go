package strategies

import (
	"errors"
	"fmt"
	"math"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

// MATrendConfig holds the inputs of MATrend, with its defaults in
// DefaultMATrendConfig. Periods are counted in candles of Timeframe.
type MATrendConfig struct {
	Timeframe string

	// The moving averages of the entries and reversals, the ones of
	// RKTrendConfluence: two EMAs, which must already be on the side of an
	// entry, and the SMA whose break by the close triggers it.
	FastLen  int // EMA, must be strictly shorter than SlowLen
	SlowLen  int // EMA
	TrendLen int // SMA

	// MaxTrendDistancePct is how far the close may be from the trend MA at an
	// entry, in % of the trend MA: a break closing far from it comes late in
	// the move. The initial stop is beyond the trend MA, so it also caps the
	// initial risk of a position, with StopBufferATR.
	MaxTrendDistancePct float64

	// Exits, in ATR of ATRLen candles, which only manage the positions and
	// never trigger an entry. The stop starts StopBufferATR beyond the trend MA
	// at the entry, below it for a long and above it for a short, and from the
	// next candle on it trails the best price since the entry close
	// TrailingATR away, with the ATR of each candle: it only ever tightens. It
	// is checked on the candle close, like the signals, so a wick alone never
	// triggers it.
	ATRLen        int
	StopBufferATR float64
	TrailingATR   float64

	// BrokerStop also places the stop at the broker with the entry, and moves
	// it there as it trails: the position stays protected while the bot is
	// off, and the broker closes it as soon as the price touches the stop, a
	// wick included. The broker must place stops with the orders, see
	// service.ProtectionBroker.
	BrokerStop bool

	// StopValue and TakeProfitValue, above 0, place a stop loss and a take
	// profit at the broker with the entry at a fixed value: what the position
	// loses, or earns, when the price reaches them, in quote currency, eg: 1
	// and 2 for a dollar and two on a pair quoted in USD, at that value over
	// the size of the position from the entry close, whatever the ATR.
	// StopValue replaces the stop in ATR above, beyond the trend MA and
	// trailing, which is then unused: the broker closes the position at it.
	// The broker must place them with the orders, see service.ProtectionBroker.
	StopValue       float64
	TakeProfitValue float64

	// Direction, MinPositionValue, PositionFraction, Leverage and PositionSize
	// work as in RKTrendConfluenceConfig.
	Direction        RKDirection
	MinPositionValue float64
	PositionFraction float64
	Leverage         float64
	PositionSize     map[string]float64
}

// DefaultMATrendConfig returns the defaults of MATrend, on the 1h timeframe.
// MaxTrendDistancePct depends on the volatility of the market and of the
// timeframe more than the other inputs: tune it with a backtest.
func DefaultMATrendConfig() MATrendConfig {
	return MATrendConfig{
		Timeframe: "1h",

		FastLen:  9,
		SlowLen:  21,
		TrendLen: 200,

		MaxTrendDistancePct: 3,

		ATRLen:        14,
		StopBufferATR: 1,
		TrailingATR:   3,

		Direction:        RKDirectionBoth,
		MinPositionValue: 10,
		PositionFraction: 1,
		Leverage:         1,
	}
}

func (c MATrendConfig) validate() error {
	for _, period := range []int{c.FastLen, c.SlowLen, c.TrendLen, c.ATRLen} {
		if period < 1 {
			return errors.New("periods must be at least 1")
		}
	}
	if err := rkCheckSizes(c.PositionSize); err != nil {
		return err
	}

	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case c.FastLen >= c.SlowLen:
		return errors.New("fast EMA period must be strictly shorter than the slow EMA period")
	case c.MaxTrendDistancePct <= 0:
		return errors.New("max distance from the trend MA must be positive: the filter is part of the strategy")
	case c.StopBufferATR < 0:
		return errors.New("stop buffer ATR multiple can't be negative")
	case c.TrailingATR <= 0:
		return errors.New("trailing stop ATR multiple must be positive: the ATR moves the stop")
	case c.StopValue < 0 || c.TakeProfitValue < 0:
		return errors.New("the fixed stop loss and take profit values can't be negative")
	case c.MinPositionValue <= 0:
		return errors.New("min position value must be positive")
	case c.PositionFraction <= 0 || c.PositionFraction > 1:
		return errors.New("position fraction must be above 0 and at most 1")
	case c.Leverage < 1:
		return errors.New("leverage must be at least 1")
	case c.Leverage > 1 && c.PositionFraction >= 1 && len(c.PositionSize) == 0:
		return errors.New("with leverage, the position fraction must be below 1: a position can't commit the " +
			"whole balance")
	case c.Direction != RKDirectionBoth && c.Direction != RKDirectionBuyOnly && c.Direction != RKDirectionSellOnly:
		return fmt.Errorf("invalid direction: %q", c.Direction)
	}
	return nil
}

// MATrend trades the break of a trend SMA in the direction of two EMAs: a BUY
// when the close crosses above the trend MA with the fast EMA already above
// the slow one, a SELL when it crosses below with the slow EMA above the fast
// one, in both cases with the close close enough to the trend MA. The entry
// is the close of the candle of the break, and needs no cross of the EMAs.
// Their opposite cross closes the position at once, and a break of the other
// side on the same candle opens it. An ATR stop manages the positions, see
// MATrendConfig.
type MATrend struct {
	cfg    MATrendConfig
	sizing rkSizing
	warmup int
	trades map[string]*rkTrade     // positions followed by the stop, by pair
	legs   map[string]*model.Order // stops placed at the broker, by pair
}

// NewMATrend validates the configuration and creates the strategy.
func NewMATrend(cfg MATrendConfig) (*MATrend, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("ma trend: %w", err)
	}

	// the cross needs the previous candle, and the recursive series (the EMAs
	// and the ATR) their seed washed out, as in RKTrendConfluence
	recursive := max(cfg.SlowLen, cfg.ATRLen)
	return &MATrend{
		cfg: cfg,
		sizing: rkSizing{sizes: cfg.PositionSize, fraction: cfg.PositionFraction, leverage: cfg.Leverage,
			minValue: cfg.MinPositionValue},
		warmup: max(cfg.TrendLen, recursive) + 1 + rkSeedFactor*recursive,
		trades: make(map[string]*rkTrade),
		legs:   make(map[string]*model.Order),
	}, nil
}

func (s *MATrend) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *MATrend) WarmupPeriod() int {
	return s.warmup
}

func (s *MATrend) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["ma_fast"] = pineEMA(df.Close, s.cfg.FastLen)
	df.Metadata["ma_slow"] = pineEMA(df.Close, s.cfg.SlowLen)
	df.Metadata["ma_trend"] = pineSMA(df.Close, s.cfg.TrendLen)
	df.Metadata["ma_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)

	return []strategy.ChartIndicator{
		{
			Overlay:   true,
			GroupName: "MA Trend",
			Time:      df.Time,
			Warmup:    s.cfg.TrendLen,
			Metrics: []strategy.IndicatorMetric{
				{
					Values: df.Metadata["ma_fast"],
					Name:   fmt.Sprintf("EMA %d", s.cfg.FastLen),
					Color:  "#00bd4e",
					Style:  strategy.StyleLine,
				},
				{
					Values: df.Metadata["ma_slow"],
					Name:   fmt.Sprintf("EMA %d", s.cfg.SlowLen),
					Color:  "#c90032",
					Style:  strategy.StyleLine,
				},
				{
					Values: df.Metadata["ma_trend"],
					Name:   fmt.Sprintf("SMA %d", s.cfg.TrendLen),
					Color:  "#fd8700",
					Style:  strategy.StyleLine,
				},
			},
		},
	}
}

// signal returns the entry of the last candle, if any, and the side of the
// cross of the EMAs on it, if any: an entry is the close crossing the trend
// MA, with the EMAs already on its side and the close close enough to the
// trend MA, while any cross of the EMAs reverses a position.
func (s *MATrend) signal(df *model.Dataframe) (entry, cross model.SideType) {
	fast, slow, trend := df.Metadata["ma_fast"], df.Metadata["ma_slow"], df.Metadata["ma_trend"]
	i := len(df.Close) - 1
	if i < 1 || len(fast) != len(df.Close) || len(slow) != len(df.Close) || len(trend) != len(df.Close) {
		return "", ""
	}

	switch {
	case crossover(fast, slow, i):
		cross = model.SideTypeBuy
	case crossunder(fast, slow, i):
		cross = model.SideTypeSell
	}

	switch {
	case crossover(df.Close, trend, i) && fast[i] > slow[i]:
		entry = model.SideTypeBuy
	case crossunder(df.Close, trend, i) && slow[i] > fast[i]:
		entry = model.SideTypeSell
	default:
		return "", cross
	}

	// the break is on the side of the trend MA: its distance is positive
	if distance := rkSign(entry) * (df.Close[i] - trend[i]) / trend[i] * 100; distance > s.cfg.MaxTrendDistancePct {
		return "", cross
	}
	return entry, cross
}

func (s *MATrend) OnCandle(df *model.Dataframe, broker service.Broker) {
	entry, cross := s.signal(df)
	closePrice := df.Close.Last(0)

	_, held, err := s.position(df, broker)
	if err != nil {
		log.Error(err)
		return
	}
	if held == "" {
		delete(s.trades, df.Pair)
		delete(s.legs, df.Pair) // the broker stop closed the position, if any
	}

	// the stop comes first: a cross on the candle of a stop applies to the
	// flat position it leaves
	if held != "" && s.stopped(df, broker, held) {
		log.Infof("[MA TREND] %s %s | STOP | close %f", df.Pair, s.cfg.Timeframe, closePrice)
		if !s.close(df, broker, held) {
			return
		}
		held = ""
	}

	// the opposite cross of the EMAs closes the position at once, without
	// waiting for the stop
	if held != "" && cross != "" && cross != held {
		log.Infof("[MA TREND] %s %s | REVERSAL %s | close %f", df.Pair, s.cfg.Timeframe, cross, closePrice)
		if !s.close(df, broker, held) {
			return
		}
		held = ""
	}

	if entry == "" || held != "" || !rkOpens(s.cfg.Direction, entry) {
		return
	}

	atr, trendMA := df.Metadata["ma_atr"].Last(0), df.Metadata["ma_trend"].Last(0)
	if isNa(atr) {
		return
	}
	log.Infof("[MA TREND] %s %s | %s | close %f | SMA %d %f", df.Pair, s.cfg.Timeframe, entry, closePrice,
		s.cfg.TrendLen, trendMA)

	_, quote, err := broker.Position(df.Pair)
	if err != nil {
		log.Error(err)
		return
	}
	size, value, ok := s.sizing.position(df.Pair, quote, closePrice)
	if !ok {
		return
	}

	// the initial stop, beyond the trend MA, or at its fixed value
	trade := &rkTrade{side: entry, extreme: closePrice, stop: trendMA - rkSign(entry)*s.cfg.StopBufferATR*atr}
	if s.cfg.StopValue > 0 {
		trade.stop = rkLossLevel(entry, closePrice, size, s.cfg.StopValue)
	}
	if s.cfg.BrokerStop || s.cfg.StopValue > 0 || s.cfg.TakeProfitValue > 0 {
		err = s.openProtected(df, broker, trade, size)
	} else {
		err = s.sizing.open(broker, entry, df.Pair, size, value)
	}
	if err != nil {
		log.Error(err)
	}

	// an order reported as failed may have been filled even so, eg: a request
	// that timed out, so the stop follows whatever position is held now
	if _, now, err := s.position(df, broker); err == nil && now == entry {
		s.trades[df.Pair] = trade
	}
}

// position returns the asset held on the pair and the side of the position,
// none for dust.
func (s *MATrend) position(df *model.Dataframe, broker service.Broker) (float64, model.SideType, error) {
	asset, _, err := broker.Position(df.Pair)
	if err != nil {
		return 0, "", err
	}
	return asset, rkHeld(asset*df.Close.Last(0), s.cfg.MinPositionValue), nil
}

// stopped reports whether the last candle closed through the stop of the
// position held on the pair, and otherwise trails the stop with the candle. The
// fixed stop is left to the broker.
func (s *MATrend) stopped(df *model.Dataframe, broker service.Broker, held model.SideType) bool {
	if s.cfg.StopValue > 0 {
		return false
	}
	trade, ok := s.trades[df.Pair]
	if !ok || trade.side != held {
		// a position opened before a restart, or by someone else: its stop
		// is followed from this candle on, as for an entry
		stop := df.Metadata["ma_trend"].Last(0) - rkSign(held)*s.cfg.StopBufferATR*df.Metadata["ma_atr"].Last(0)
		s.trades[df.Pair] = &rkTrade{side: held, extreme: df.Close.Last(0), stop: stop}
		delete(s.legs, df.Pair)
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
	previous := trade.stop
	trade.tighten(trade.extreme - trade.sign()*s.cfg.TrailingATR*df.Metadata["ma_atr"].Last(0))

	// the broker takes a stop on the losing side of the price only: beyond
	// it, the close of the next candle stops the position
	if leg, ok := s.legs[df.Pair]; ok && leg.Type == model.OrderTypeStopLoss && trade.stop != previous &&
		trade.sign()*trade.stop < trade.sign()*df.Close.Last(0) {
		if protected, ok := broker.(service.ProtectionBroker); ok {
			moved, err := protected.UpdateProtection(*leg, trade.stop)
			if err != nil {
				log.Error(err)
			} else {
				s.legs[df.Pair] = &moved
			}
		}
	}
	return false
}

// openProtected opens a position of size with its legs placed at the broker:
// the stop of trade, with BrokerStop or at its fixed value, and the take profit
// at TakeProfitValue, if any.
func (s *MATrend) openProtected(df *model.Dataframe, broker service.Broker, trade *rkTrade, size float64) error {
	protected, ok := broker.(service.ProtectionBroker)
	if !ok {
		return errors.New("ma trend: the broker can't place the stop with the order")
	}

	var stop, profit float64 // 0 for none
	if s.cfg.BrokerStop || s.cfg.StopValue > 0 {
		stop = trade.stop
	}
	if s.cfg.TakeProfitValue > 0 {
		profit = rkProfitLevel(trade.side, df.Close.Last(0), size, s.cfg.TakeProfitValue)
	}

	orders, err := protected.CreateOrderMarketProtected(trade.side, df.Pair, size, stop, profit)
	if len(orders) > 1 {
		leg := rkProtectionLeg(orders[1:])
		s.legs[df.Pair] = &leg
	}
	return err
}

// close closes the position held on the pair at the market, and reports
// whether the pair is flat now. The stop at the broker is canceled first, as it reserves the
// position: the close would otherwise open the other side, eg: in the paper
// wallet.
func (s *MATrend) close(df *model.Dataframe, broker service.Broker, held model.SideType) bool {
	if leg, ok := s.legs[df.Pair]; ok {
		if err := broker.Cancel(*leg); err != nil {
			log.Debugf("[MA TREND] %s | broker stop: %v", df.Pair, err) // already filled or canceled
		}
		delete(s.legs, df.Pair)
	}

	if !rkClose(df, broker, held, s.cfg.MinPositionValue) {
		return false
	}
	delete(s.trades, df.Pair)
	return true
}
