package strategies

import (
	"errors"
	"fmt"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

// ScalpConfig holds the inputs of Scalp, with its defaults in
// DefaultScalpConfig. Periods are counted in candles of Timeframe.
type ScalpConfig struct {
	Timeframe string

	// TrendLen is the EMA of the context: a long needs the close above it, a
	// short below it, and a close back across it ends the trade.
	TrendLen int

	// The entry is the RSI turning back from an extreme in the direction of
	// the context: crossing up Oversold for a long, down Overbought for a
	// short.
	RSILen     int
	Oversold   float64
	Overbought float64

	// The exits, in ATR of ATRLen candles from the entry close, are placed at
	// the broker with the entry: the stop loss StopATR away and the take
	// profit TakeProfitATR away, so the position is protected from its
	// opening and they close it as soon as the price touches them. MaxBars
	// closes a trade still open after that many candles at the market, 0
	// never.
	ATRLen        int
	StopATR       float64
	TakeProfitATR float64
	MaxBars       int

	// StopValue and TakeProfitValue, above 0, place the stop loss and the take
	// profit at a fixed value instead: what the position loses, or earns, when
	// the price reaches them, in quote currency, eg: 1 and 2 for a dollar and
	// two on a pair quoted in USD. The level is that value over the size of
	// the position from the entry close, whatever the ATR. 0 keeps the ATR.
	StopValue       float64
	TakeProfitValue float64

	// MinATRPct skips the entries while the ATR is under that % of the close:
	// a target that small is eaten by the costs of the trade, eg: the spread.
	// It depends on the costs of each market, so 0, the default, disables it:
	// about twice the spread in % of the price, eg: 0.025 for a 0.30 spread
	// on gold at 2400, keeps the target a few times the cost of a trade.
	MinATRPct float64

	// Direction, MinPositionValue, PositionFraction, Leverage and PositionSize
	// work as in RKTrendConfluenceConfig.
	Direction        RKDirection
	MinPositionValue float64
	PositionFraction float64
	Leverage         float64
	PositionSize     map[string]float64
}

// DefaultScalpConfig returns the defaults of Scalp, on the 1m timeframe.
func DefaultScalpConfig() ScalpConfig {
	return ScalpConfig{
		Timeframe: "1m",

		TrendLen: 50,

		RSILen:     7,
		Oversold:   30,
		Overbought: 70,

		ATRLen:        14,
		StopATR:       1,
		TakeProfitATR: 1.5,
		MaxBars:       15,

		Direction:        RKDirectionBoth,
		MinPositionValue: 10,
		PositionFraction: 1,
		Leverage:         1,
	}
}

func (c ScalpConfig) validate() error {
	for _, period := range []int{c.TrendLen, c.RSILen, c.ATRLen} {
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
	case c.Oversold <= 0 || c.Overbought >= 100 || c.Oversold >= c.Overbought:
		return errors.New("RSI levels: the oversold level must be above 0 and below the overbought one, below 100")
	case c.StopATR <= 0 || c.TakeProfitATR <= 0:
		return errors.New("the stop loss and take profit ATR multiples must be positive")
	case c.StopValue < 0 || c.TakeProfitValue < 0:
		return errors.New("the fixed stop loss and take profit values can't be negative")
	case c.MaxBars < 0:
		return errors.New("max bars can't be negative")
	case c.MinATRPct < 0:
		return errors.New("min ATR % can't be negative")
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

// Scalp trades short pullbacks in the direction of an EMA: a BUY when the RSI
// crosses back up its oversold level with the close above the EMA, a SELL when
// it crosses back down its overbought level with the close below it. Each
// entry is placed with its stop loss and take profit in ATR at the broker,
// which must place them with the orders, see service.ProtectionBroker. A trade
// ends at one of them, or at the market on the close back across the EMA or
// after MaxBars candles. See ScalpConfig.
type Scalp struct {
	cfg    ScalpConfig
	sizing rkSizing
	warmup int
	trades map[string]*scalpTrade // the positions followed, by pair
}

// scalpTrade is a position of Scalp: its side, the candles since its entry,
// the legs of its stop loss and take profit at the broker, whether its exit
// was decided, retried on each candle while it fails, and the levels of the
// legs a close that failed left it without, if any (see rkCloseProtected). A
// position found held, eg: opened before a restart, has its opening too, zero
// when unknown, see adoptTrade.
type scalpTrade struct {
	side    model.SideType
	bars    int
	legs    []model.Order
	exiting bool
	bare    rkLevels
	opened  time.Time
}

// adoptTrade returns the trade of a position held on side found on the last
// candle of df, which the strategy name doesn't follow, eg: opened before a
// restart, or by someone else: with its legs at the broker, which it moves and
// cancels, and the candles held since its opening, minus the last one, which
// the strategy counts, when the broker tells them (see rkFind); else from the
// last candle on, without legs known.
func adoptTrade(df *model.Dataframe, broker service.Broker, name, timeframe string,
	side model.SideType) *scalpTrade {

	found := rkFind(df, broker, name, side)
	trade := &scalpTrade{side: side, legs: found.legs, opened: found.opened}
	if bars, _, ok := found.held(df, timeframe); ok && bars > 0 {
		trade.bars = bars - 1
	}
	return trade
}

// close closes the position of trade at the market, see rkCloseProtected, and
// reports whether the pair is flat now. The trade is marked as exiting, its
// close retried on the next candles while it fails, the position protected by
// its legs meanwhile.
func (t *scalpTrade) close(df *model.Dataframe, broker service.Broker, name string, minValue float64) bool {
	t.exiting = true
	levels := t.bare
	if len(t.legs) > 0 {
		levels = rkLevelsOf(t.legs)
	}
	var flat bool
	flat, t.legs, t.bare = rkCloseProtected(df, broker, name, t.side, minValue, t.legs, levels)
	return flat
}

// NewScalp validates the configuration and creates the strategy.
func NewScalp(cfg ScalpConfig) (*Scalp, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("scalp: %w", err)
	}

	// the cross needs the previous candle, and the recursive series (the EMA,
	// the RSI and the ATR) their seed washed out, as in RKTrendConfluence
	recursive := max(cfg.TrendLen, cfg.RSILen, cfg.ATRLen)
	return &Scalp{
		cfg: cfg,
		sizing: rkSizing{sizes: cfg.PositionSize, fraction: cfg.PositionFraction, leverage: cfg.Leverage,
			minValue: cfg.MinPositionValue},
		warmup: recursive + 1 + rkSeedFactor*recursive,
		trades: make(map[string]*scalpTrade),
	}, nil
}

func (s *Scalp) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *Scalp) WarmupPeriod() int {
	return s.warmup
}

func (s *Scalp) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	df.Metadata["sc_ema"] = pineEMA(df.Close, s.cfg.TrendLen)
	df.Metadata["sc_rsi"] = pineRSI(df.Close, s.cfg.RSILen)
	df.Metadata["sc_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)

	return []strategy.ChartIndicator{
		{
			Overlay:   true,
			GroupName: "Scalp",
			Time:      df.Time,
			Warmup:    s.cfg.TrendLen,
			Metrics: []strategy.IndicatorMetric{
				{
					Values: df.Metadata["sc_ema"],
					Name:   fmt.Sprintf("EMA %d", s.cfg.TrendLen),
					Color:  "#fd8700",
					Style:  strategy.StyleLine,
				},
			},
		},
		{
			Overlay:   false,
			GroupName: fmt.Sprintf("RSI %d", s.cfg.RSILen),
			Time:      df.Time,
			Warmup:    s.cfg.RSILen,
			Metrics: []strategy.IndicatorMetric{
				{
					Values: df.Metadata["sc_rsi"],
					Name:   "RSI",
					Color:  "#8e44ad",
					Style:  strategy.StyleLine,
				},
			},
		},
	}
}

// signal returns the entry of the last candle, if any.
func (s *Scalp) signal(df *model.Dataframe) model.SideType {
	ema, rsi, atr := df.Metadata["sc_ema"], df.Metadata["sc_rsi"], df.Metadata["sc_atr"]
	i := len(df.Close) - 1
	if i < 1 || len(ema) != len(df.Close) || len(rsi) != len(df.Close) || len(atr) != len(df.Close) ||
		isNa(ema[i]) || isNa(rsi[i]) || isNa(rsi[i-1]) || isNa(atr[i]) {
		return ""
	}

	closePrice := df.Close[i]
	if s.cfg.MinATRPct > 0 && atr[i]/closePrice*100 < s.cfg.MinATRPct {
		return ""
	}

	switch {
	case closePrice > ema[i] && rsi[i-1] <= s.cfg.Oversold && rsi[i] > s.cfg.Oversold:
		return model.SideTypeBuy
	case closePrice < ema[i] && rsi[i-1] >= s.cfg.Overbought && rsi[i] < s.cfg.Overbought:
		return model.SideTypeSell
	}
	return ""
}

func (s *Scalp) OnCandle(df *model.Dataframe, broker service.Broker) {
	entry := s.signal(df)
	closePrice := df.Close.Last(0)

	_, held, err := s.position(df, broker)
	if err != nil {
		log.Error(err)
		return
	}
	if held == "" {
		delete(s.trades, df.Pair) // its stop loss or take profit closed it, if any
	}

	if held != "" {
		trade, ok := s.trades[df.Pair]
		if !ok || trade.side != held {
			// a position opened before a restart, or by someone else
			trade = adoptTrade(df, broker, "SCALP", s.cfg.Timeframe, held)
			s.trades[df.Pair] = trade
		}
		trade.bars++

		reason := ""
		switch {
		case trade.exiting:
			reason = "RETRY" // a close that failed, the position protected meanwhile
		case rkSign(held)*(closePrice-df.Metadata["sc_ema"].Last(0)) <= 0:
			reason = "INVALIDATION" // the close back across the EMA: the pullback became a reversal
		case s.cfg.MaxBars > 0 && trade.bars >= s.cfg.MaxBars:
			reason = "TIME"
		default:
			return // one position at a time, left to its stop loss and take profit
		}

		log.Infof("[SCALP] %s %s | %s | close %f", df.Pair, s.cfg.Timeframe, reason, closePrice)
		if !s.close(df, broker, trade) {
			return
		}
		// flat now: a signal on this candle opens the next trade
	}

	if entry == "" || !rkOpens(s.cfg.Direction, entry) {
		return
	}

	protected, ok := broker.(service.ProtectionBroker)
	if !ok {
		log.Error("scalp: the broker can't place the stop loss and take profit with the order")
		return
	}

	_, quote, err := broker.Position(df.Pair)
	if err != nil {
		log.Error(err)
		return
	}
	size, _, ok := s.sizing.position(df.Pair, quote, closePrice)
	if !ok {
		return
	}

	atr, sign := df.Metadata["sc_atr"].Last(0), rkSign(entry)
	stop := closePrice - sign*s.cfg.StopATR*atr
	if s.cfg.StopValue > 0 {
		stop = rkLossLevel(entry, closePrice, size, s.cfg.StopValue)
	}
	profit := closePrice + sign*s.cfg.TakeProfitATR*atr
	if s.cfg.TakeProfitValue > 0 {
		profit = rkProfitLevel(entry, closePrice, size, s.cfg.TakeProfitValue)
	}
	log.Infof("[SCALP] %s %s | %s | close %f | stop %f | take profit %f", df.Pair, s.cfg.Timeframe, entry,
		closePrice, stop, profit)

	orders, err := protected.CreateOrderMarketProtected(entry, df.Pair, size, stop, profit)
	if err != nil {
		log.Error(err)
	}

	// an order reported as failed may have been filled even so, eg: a request
	// that timed out, so whatever position is held now is followed
	if _, now, err := s.position(df, broker); err == nil && now == entry {
		trade := &scalpTrade{side: entry}
		if len(orders) > 1 {
			trade.legs = orders[1:]
		}
		s.trades[df.Pair] = trade
	}
}

// position returns the asset held on the pair and the side of the position,
// none for dust.
func (s *Scalp) position(df *model.Dataframe, broker service.Broker) (float64, model.SideType, error) {
	asset, _, err := broker.Position(df.Pair)
	if err != nil {
		return 0, "", err
	}
	return asset, rkHeld(asset*df.Close.Last(0), s.cfg.MinPositionValue), nil
}

// close closes the position of trade at the market, and reports whether the
// pair is flat now. Where the broker needs it, its stop loss and take profit
// are canceled first, as they reserve the position: the close would otherwise
// open the other side, eg: in the paper wallet; a close that fails places them
// again, see rkCloseProtected. The trade is marked as exiting, and the close
// retried on the next candles.
func (s *Scalp) close(df *model.Dataframe, broker service.Broker, trade *scalpTrade) bool {
	if !trade.close(df, broker, "SCALP", s.cfg.MinPositionValue) {
		return false
	}
	delete(s.trades, df.Pair)
	return true
}
