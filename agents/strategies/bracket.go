package strategies

import (
	"errors"
	"fmt"
	"math"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/tools/log"
)

// BracketConfig holds the exits and the sizing of the intraday strategies:
// ORB, VWAPReclaim, LiquiditySweep, ATRBreakout, ZScoreReversion, EMAPullback
// and AsiaLondon. Its defaults are in DefaultBracketConfig, each strategy
// adjusting them to its own.
type BracketConfig struct {
	// The exits work as in ScalpConfig: each entry is placed at the market
	// with its stop loss StopATR and its take profit TakeProfitATR away from
	// the entry close, in ATR of ATRLen candles, at the broker, which must
	// place them with the orders, see service.ProtectionBroker. MaxBars closes
	// a trade still open after that many candles at the market, 0 never, and
	// the strategies close it earlier on their own exits. TakeProfitATR 0,
	// which only the signal strategies take (see SignalConfig), places no take
	// profit.
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

	// MinATRPct skips the entries while the ATR is under that % of the close,
	// as in ScalpConfig: a target that small is eaten by the costs of the
	// trade, eg: the spread, which the candles don't hold. 0 disables it.
	MinATRPct float64

	// Direction, MinPositionValue, PositionFraction, Leverage and PositionSize
	// work as in RKTrendConfluenceConfig.
	Direction        RKDirection
	MinPositionValue float64
	PositionFraction float64
	Leverage         float64
	PositionSize     map[string]float64

	// MaxRiskPct, above 0, skips an entry losing more than that % of the
	// balance available (the quote of the broker's Position) at its stop
	// loss. 0 sets no limit.
	MaxRiskPct float64
}

// DefaultBracketConfig returns the defaults of BracketConfig.
func DefaultBracketConfig() BracketConfig {
	return BracketConfig{
		ATRLen:        14,
		StopATR:       1,
		TakeProfitATR: 1.5,

		Direction:        RKDirectionBoth,
		MinPositionValue: 10,
		PositionFraction: 1,
		Leverage:         1,
	}
}

func (c BracketConfig) validate() error {
	if err := rkCheckSizes(c.PositionSize); err != nil {
		return err
	}

	switch {
	case c.ATRLen < 1:
		return errors.New("periods must be at least 1")
	case c.StopATR <= 0 || c.TakeProfitATR <= 0:
		return errors.New("the stop loss and take profit ATR multiples must be positive")
	case c.StopValue < 0 || c.TakeProfitValue < 0:
		return errors.New("the fixed stop loss and take profit values can't be negative")
	case c.MaxBars < 0:
		return errors.New("max bars can't be negative")
	case c.MinATRPct < 0:
		return errors.New("min ATR % can't be negative")
	case c.MaxRiskPct < 0:
		return errors.New("the max risk can't be negative")
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

// bracket trades the positions of an intraday strategy, one at a time per
// pair, as Scalp does: it opens them with their stop loss and take profit at
// the broker, and closes them at the market on an exit of the strategy or after
// MaxBars candles.
type bracket struct {
	name      string // of the logs, eg: ORB
	timeframe string
	cfg       BracketConfig
	sizing    rkSizing
	trades    map[string]*scalpTrade // the positions followed, by pair
}

// bracketExit returns the reason to close the position held on side at the
// market on the last candle, none to keep it.
type bracketExit func(df *model.Dataframe, held model.SideType) string

func newBracket(name, timeframe string, cfg BracketConfig) *bracket {
	return &bracket{
		name:      name,
		timeframe: timeframe,
		cfg:       cfg,
		sizing: rkSizing{sizes: cfg.PositionSize, fraction: cfg.PositionFraction, leverage: cfg.Leverage,
			maxRisk:  cfg.MaxRiskPct,
			minValue: cfg.MinPositionValue},
		trades: make(map[string]*scalpTrade),
	}
}

// manage follows the position held on the pair on the last candle, closing it
// on exit or after MaxBars candles, and reports whether the pair is flat now,
// so that the strategy may open the next position.
func (b *bracket) manage(df *model.Dataframe, broker service.Broker, exit bracketExit) bool {
	_, held, err := b.position(df, broker)
	if err != nil {
		log.Error(err)
		return false
	}
	if held == "" {
		delete(b.trades, df.Pair) // its stop loss or take profit closed it, if any
		return true
	}

	trade, ok := b.trades[df.Pair]
	if !ok || trade.side != held {
		// a position opened before a restart, or by someone else
		trade = adoptTrade(df, broker, b.name, b.timeframe, held)
		b.trades[df.Pair] = trade
	}
	trade.bars++

	reason := ""
	switch {
	case trade.exiting:
		reason = "RETRY" // a close that failed, the position protected meanwhile
	default:
		reason = exit(df, held)
		if reason == "" && b.cfg.MaxBars > 0 && trade.bars >= b.cfg.MaxBars {
			reason = "TIME"
		}
	}
	if reason == "" {
		return false // one position at a time, left to its stop loss and take profit
	}

	log.Infof("[%s] %s %s | %s | close %f", b.name, df.Pair, b.timeframe, reason, df.Close.Last(0))
	return b.close(df, broker, trade)
}

// open opens a position of entry on the last candle, with its stop loss and
// take profit in atr from its close, or at their fixed values, and reports
// whether it is held now.
func (b *bracket) open(df *model.Dataframe, broker service.Broker, entry model.SideType, atr float64) bool {
	closePrice := df.Close.Last(0)
	if isNa(atr) || atr <= 0 || b.cfg.MinATRPct > 0 && atr/closePrice*100 < b.cfg.MinATRPct {
		return false
	}

	sign := rkSign(entry)
	profit := 0.0 // none
	if b.cfg.TakeProfitATR > 0 {
		profit = closePrice + sign*b.cfg.TakeProfitATR*atr
	}
	return b.openAt(df, broker, entry, closePrice-sign*b.cfg.StopATR*atr, profit)
}

// openAt opens a position of entry on the last candle at the market, with its
// stop loss at stop and its take profit at profit, 0 for none, unless their
// fixed values replace them (StopValue and TakeProfitValue), and reports
// whether it is held now.
func (b *bracket) openAt(df *model.Dataframe, broker service.Broker, entry model.SideType, stop,
	profit float64) bool {

	closePrice := df.Close.Last(0)
	if entry == "" || !rkOpens(b.cfg.Direction, entry) {
		return false
	}

	protected, ok := broker.(service.ProtectionBroker)
	if !ok {
		log.Errorf("%s: the broker can't place the stop loss and take profit with the order", b.name)
		return false
	}

	_, quote, err := broker.Position(df.Pair)
	if err != nil {
		log.Error(err)
		return false
	}
	size, _, ok := b.sizing.position(df.Pair, quote, closePrice)
	if !ok {
		return false
	}

	stop, profit = b.fixed(entry, closePrice, size, stop, profit)
	if b.sizing.risky(df.Pair, closePrice, stop, size, quote) {
		return false
	}
	log.Infof("[%s] %s %s | %s | close %f | stop %f | take profit %f", b.name, df.Pair, b.timeframe, entry,
		closePrice, stop, profit)

	orders, err := protected.CreateOrderMarketProtected(entry, df.Pair, size, stop, profit)
	if err != nil {
		log.Error(err)
	}
	if !b.follow(df, broker, entry, orders) {
		return false
	}
	trade := b.trades[df.Pair]
	trade.entry, trade.risk = closePrice, math.Abs(closePrice-stop)
	return true
}

// place places a limit order of entry at limit on the pair of df, with its
// stop loss at stop and its take profit at profit, 0 for none, unless their
// fixed values replace them, see openAt. It returns the orders of the entry
// still pending, the entry then its legs, none when no entry is pending: not
// placed, or filled at once, the position then followed, held reporting it.
func (b *bracket) place(df *model.Dataframe, broker service.Broker, entry model.SideType, limit, stop,
	profit float64) (pending []model.Order, held bool) {

	if entry == "" || !rkOpens(b.cfg.Direction, entry) {
		return nil, false
	}

	protected, ok := broker.(service.ProtectionBroker)
	if !ok {
		log.Errorf("%s: the broker can't place the stop loss and take profit with the order", b.name)
		return nil, false
	}

	_, quote, err := broker.Position(df.Pair)
	if err != nil {
		log.Error(err)
		return nil, false
	}
	size, _, ok := b.sizing.position(df.Pair, quote, limit)
	if !ok {
		return nil, false
	}

	stop, profit = b.fixed(entry, limit, size, stop, profit)
	if b.sizing.risky(df.Pair, limit, stop, size, quote) {
		return nil, false
	}
	log.Infof("[%s] %s %s | %s LIMIT | limit %f | stop %f | take profit %f | close %f", b.name, df.Pair,
		b.timeframe, entry, limit, stop, profit, df.Close.Last(0))

	orders, err := protected.CreateOrderLimitProtected(entry, df.Pair, size, limit, stop, profit)
	if err != nil {
		log.Error(err)
	}
	if b.follow(df, broker, entry, orders) {
		return nil, true
	}
	if len(orders) > 0 && orders[0].Type == model.OrderTypeLimit && !orders[0].Status.Final() {
		return orders, false
	}
	return nil, false
}

// fixed returns the stop loss and the take profit of a position of entry at
// price of size, stop and profit unless StopValue or TakeProfitValue replace
// them.
func (b *bracket) fixed(entry model.SideType, price, size, stop, profit float64) (float64, float64) {
	if b.cfg.StopValue > 0 {
		stop = rkLossLevel(entry, price, size, b.cfg.StopValue)
	}
	if b.cfg.TakeProfitValue > 0 {
		profit = rkProfitLevel(entry, price, size, b.cfg.TakeProfitValue)
	}
	return stop, profit
}

// follow follows the position of entry held on the pair of df after its orders
// were sent, the entry then its legs, and reports whether it is held. An order
// reported as failed may have been filled even so, eg: a request that timed
// out, so whatever position is held now is followed.
func (b *bracket) follow(df *model.Dataframe, broker service.Broker, entry model.SideType,
	orders []model.Order) bool {

	if _, now, err := b.position(df, broker); err == nil && now == entry {
		trade := &scalpTrade{side: entry}
		if len(orders) > 1 {
			trade.legs = orders[1:]
		}
		b.trades[df.Pair] = trade
		return true
	}
	return false
}

// position returns the asset held on the pair and the side of the position,
// none for dust.
func (b *bracket) position(df *model.Dataframe, broker service.Broker) (float64, model.SideType, error) {
	asset, _, err := broker.Position(df.Pair)
	if err != nil {
		return 0, "", err
	}
	return asset, rkHeld(asset*df.Close.Last(0), b.cfg.MinPositionValue), nil
}

// close closes the position of trade at the market, and reports whether the
// pair is flat now, as Scalp.close does.
func (b *bracket) close(df *model.Dataframe, broker service.Broker, trade *scalpTrade) bool {
	if !trade.close(df, broker, b.name, b.cfg.MinPositionValue) {
		return false
	}
	delete(b.trades, df.Pair)
	return true
}

// seedWarmup is the warmup of the recursive series of length periods (EMA, RMA,
// ATR, RSI, DMI) with their seed washed out, as in RKTrendConfluence.
func seedWarmup(length int) int {
	return length + rkSeedFactor*length
}
