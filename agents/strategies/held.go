package strategies

import (
	"errors"
	"math"
	"sort"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/xhit/go-str2duration/v2"
)

// rkLevels are the levels of the stop loss and the take profit protecting a
// position at the broker, 0 for none.
type rkLevels struct {
	stop, profit float64
}

func (l rkLevels) none() bool {
	return l.stop <= 0 && l.profit <= 0
}

// rkIsStop reports whether leg is a stop loss, not a take profit.
func rkIsStop(leg model.Order) bool {
	return leg.Type == model.OrderTypeStopLoss || leg.Type == model.OrderTypeStopLossLimit
}

// rkLegLevel returns the level of a stop loss or take profit leg.
func rkLegLevel(leg model.Order) float64 {
	if leg.Stop != nil && rkIsStop(leg) {
		return *leg.Stop
	}
	return leg.Price
}

// rkLevelsOf returns the levels of legs, the stop loss and the take profit of
// a position.
func rkLevelsOf(legs []model.Order) rkLevels {
	var levels rkLevels
	for _, leg := range legs {
		if rkIsStop(leg) {
			levels.stop = rkLegLevel(leg)
		} else {
			levels.profit = rkLegLevel(leg)
		}
	}
	return levels
}

// rkFound is a position found held that a strategy doesn't follow, eg: opened
// before a restart: the legs protecting it at the broker, and when it opened,
// zero when unknown, as the broker tells them (service.PositionLegs).
type rkFound struct {
	legs   []model.Order
	opened time.Time
}

// rkFind looks up the legs and the opening of the position held on side of the
// pair of df, which the strategy name starts following: none when the broker
// doesn't tell them, the position then followed from the last candle on.
func rkFind(df *model.Dataframe, broker service.Broker, name string, side model.SideType) rkFound {
	finder, ok := broker.(service.PositionLegs)
	if !ok {
		return rkFound{}
	}
	legs, opened, err := finder.HeldLegs(df.Pair, side)
	if err != nil {
		log.Warnf("[%s] %s | the legs of the %s position found: %v: followed without them", name, df.Pair, side,
			err)
		return rkFound{}
	}
	since := "unknown"
	if !opened.IsZero() {
		since = opened.UTC().Format(time.RFC3339)
	}
	log.Infof("[%s] %s | the %s position found followed with its %d legs, opened %s", name, df.Pair, side,
		len(legs), since)
	return rkFound{legs: legs, opened: opened}
}

// held returns the candles of df closed since the position opened, the last
// one included, those before df counted by the time, with timeframe the one of
// the candles: as many as the strategies count from the candle whose close
// opened it, its entry, which is first-1, the index of the first candle held;
// first is 0 when the entry isn't in df. known is false when the opening or
// the timeframe isn't.
func (f rkFound) held(df *model.Dataframe, timeframe string) (bars, first int, known bool) {
	period, err := str2duration.ParseDuration(timeframe)
	if f.opened.IsZero() || err != nil || period <= 0 {
		return 0, 0, false
	}
	n := len(df.Time)
	first = sort.Search(n, func(i int) bool { return df.Time[i].Add(period).After(f.opened) })
	bars = n - first
	if first == 0 && n > 0 {
		if before := df.Time[0].Sub(f.opened); before > 0 {
			bars += int(math.Ceil(float64(before) / float64(period)))
		}
	}
	return bars, first, true
}

// rkExtreme returns the best price of a position held on side since its entry,
// from the close of its entry, the candle first-1 of df, through the highs (the
// lows of a short) of the candles up to the one before the last, the strategies
// following it with the last: as they reached it had they followed it all
// along. Its entry out of df (first 0), from the candles of df only.
func rkExtreme(df *model.Dataframe, side model.SideType, first int) float64 {
	n := len(df.Close)
	extreme := df.Close[max(first-1, 0)]
	if len(df.High) != n || len(df.Low) != n {
		return extreme
	}
	for i := first; i < n-1; i++ {
		if side == model.SideTypeBuy {
			extreme = math.Max(extreme, df.High[i])
		} else {
			extreme = math.Min(extreme, df.Low[i])
		}
	}
	return extreme
}

// rkCloseProtected closes the position held on side of the pair of df at the
// market, and reports whether the pair is flat now, with the legs protecting
// the position still held, and the levels of the ones it is left without, if
// any: legs are the legs in place (canceling either removes them all, see
// service.ProtectionBroker) at levels, or, with none in place, levels are the
// ones of the legs a close that failed canceled, which were not placed again.
//
// The position is never left without its legs by a close that fails: where
// the order closes it with its legs in place (service.ProtectedCloser, eg:
// MT5), they stay until it is flat, the ones left then canceled; elsewhere
// they reserve the position, eg: in the paper wallet, and the order would
// open the other side, so they are canceled first, and placed again at their
// levels should the close fail (service.PositionProtector).
func rkCloseProtected(df *model.Dataframe, broker service.Broker, name string, side model.SideType,
	minValue float64, legs []model.Order, levels rkLevels) (flat bool, kept []model.Order, bare rkLevels) {

	if len(legs) > 0 && !rkClosesProtected(broker) {
		for _, leg := range legs {
			if err := broker.Cancel(leg); err != nil {
				log.Debugf("[%s] %s | %s: %v", name, df.Pair, leg.Type, err) // already filled or canceled
			}
		}
		legs = nil
	}
	if len(legs) == 0 {
		bare = levels // none to place again without any
	}

	// read again rather than trust the position read before the cancels: a leg
	// filled while its cancel was in flight may have closed it meanwhile, and
	// the order would then open the other side, without legs
	asset, _, err := broker.Position(df.Pair)
	if err != nil {
		log.Error(err)
		return false, legs, rkLeftBare(df, name, side, bare)
	}
	if held := rkHeld(asset*df.Close.Last(0), minValue); held != side {
		rkCancelLeft(df, broker, name, legs)
		return held == "", nil, rkLevels{} // closed by a leg meanwhile
	}

	_, failure := broker.CreateOrderMarket(oppositeSide(side), df.Pair, math.Abs(asset))
	if failure == nil {
		rkCancelLeft(df, broker, name, legs)
		return true, nil, rkLevels{}
	}
	log.Errorf("[%s] %s | the close failed: %v: retried on the next candle", name, df.Pair, failure)

	// the close may be done whatever the error says, eg: a request that timed out
	asset, _, err = broker.Position(df.Pair)
	if err != nil {
		log.Error(err)
		return false, legs, rkLeftBare(df, name, side, bare)
	}
	if held := rkHeld(asset*df.Close.Last(0), minValue); held != side {
		rkCancelLeft(df, broker, name, legs)
		return held == "", nil, rkLevels{}
	}
	if bare.none() {
		return false, legs, rkLevels{} // it keeps its legs, or had none
	}

	protector, ok := broker.(service.PositionProtector)
	if !ok {
		log.Errorf("[%s] %s | the broker can't protect a position held", name, df.Pair)
		return false, nil, rkLeftBare(df, name, side, bare)
	}
	placed, err := protector.Protect(side, df.Pair, math.Abs(asset), bare.stop, bare.profit)
	if len(placed) == 0 {
		if err == nil {
			log.Errorf("[%s] %s | no leg placed again", name, df.Pair)
		} else {
			log.Errorf("[%s] %s | the legs placed again: %v", name, df.Pair, err)
		}
		return false, nil, rkLeftBare(df, name, side, bare)
	}
	if err != nil {
		log.Error(err) // placed, a part of them unstored
	}
	log.Warnf("[%s] %s | the stop loss %g and take profit %g placed again on the %s position of %g", name, df.Pair,
		bare.stop, bare.profit, side, math.Abs(asset))
	return false, placed, rkLevels{}
}

// rkLeftBare logs the position held on side left without the legs of levels,
// and returns them, to place again on the next attempt to close it.
func rkLeftBare(df *model.Dataframe, name string, side model.SideType, levels rkLevels) rkLevels {
	if !levels.none() {
		log.Errorf("[%s] %s | the %s position is left WITHOUT its stop loss %g and take profit %g until its close, "+
			"retried on the next candle", name, df.Pair, side, levels.stop, levels.profit)
	}
	return levels
}

// rkCancelLeft cancels the legs a close left in place, its position gone, so
// that none outlives it, eg: gone with the position already at MT5.
func rkCancelLeft(df *model.Dataframe, broker service.Broker, name string, legs []model.Order) {
	for _, leg := range legs {
		if err := broker.Cancel(leg); err != nil {
			log.Debugf("[%s] %s | %s: %v", name, df.Pair, leg.Type, err) // gone with the position
		}
	}
}

// rkClosesProtected reports whether a market order of broker closes a protected
// position with its legs in place, see service.ProtectedCloser.
func rkClosesProtected(broker service.Broker) bool {
	closer, ok := broker.(service.ProtectedCloser)
	return ok && closer.ClosesProtected()
}

// rkExit is a close of a position decided that failed, retried on the next
// candles: the side of the position, and the levels of the legs it was left
// without, see rkCloseProtected.
type rkExit struct {
	side model.SideType
	bare rkLevels
}

// tighterAlready returns the leg as the broker holds it, and true, when err
// tells that the stop guard kept a stop loss more protective than the one
// asked (see service.StopNotTighterError): the strategy follows it from
// there, never loosening it.
func tighterAlready(err error) (model.Order, bool) {
	var tighter *service.StopNotTighterError
	if errors.As(err, &tighter) {
		return tighter.Leg, true
	}
	return model.Order{}, false
}
