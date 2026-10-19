package protect

import (
	"math"
	"strconv"

	"github.com/raykavin/xtrader/model"
)

// The arithmetic of the protection of the positions: pure functions of the
// side of a position, its prices and the step of the prices.

// sign returns 1 for a long position, -1 for a short one: the direction its
// stop loss tightens to.
func sign(side model.SideType) float64 {
	if side == model.SideTypeSell {
		return -1
	}
	return 1
}

// spreadOf returns the spread of candle, from its metadata (MT5 candles), 0
// when unknown.
func spreadOf(candle model.Candle) float64 {
	if spread := candle.Metadata["spread"]; spread > 0 && !math.IsInf(spread, 0) {
		return spread
	}
	return 0
}

// exitPrice returns the price a position of side would exit at on candle now:
// the bid of a long (the close: the candles are bid prices), the ask of a
// short (its metadata, else the close plus the spread).
func exitPrice(side model.SideType, candle model.Candle) float64 {
	if side != model.SideTypeSell {
		return candle.Close
	}
	if ask := candle.Metadata["ask"]; ask > 0 && !math.IsInf(ask, 0) {
		return ask
	}
	return candle.Close + spreadOf(candle)
}

// bestExit returns the best exit price of a position of side candle reached:
// its high for a long; its low plus its spread for a short.
func bestExit(side model.SideType, candle model.Candle) float64 {
	if side != model.SideTypeSell {
		return candle.High
	}
	return candle.Low + spreadOf(candle)
}

// better reports whether price is better than best for a position of side to
// exit at, best 0 for none yet.
func better(side model.SideType, price, best float64) bool {
	return best == 0 || sign(side)*(price-best) > 0
}

// decimals returns the digits of tick, eg: 2 for 0.01.
func decimals(tick float64) int {
	text := strconv.FormatFloat(tick, 'f', -1, 64)
	for i, c := range text {
		if c == '.' {
			return len(text) - i - 1
		}
	}
	return 0
}

// clean rounds value to the digits of tick, removing the float noise.
func clean(value, tick float64) float64 {
	value, _ = strconv.ParseFloat(strconv.FormatFloat(value, 'f', decimals(tick), 64), 64)
	return value
}

// roundProtective rounds level to tick toward more protection for a position
// of side: up for a long, down for a short, so that it never falls short of
// the level, eg: the entry plus a margin. A tick of 0 leaves it as it is.
func roundProtective(side model.SideType, level, tick float64) float64 {
	if tick <= 0 {
		return level
	}
	steps := level / tick
	if side == model.SideTypeSell {
		return clean(math.Floor(steps+1e-9)*tick, tick)
	}
	return clean(math.Ceil(steps-1e-9)*tick, tick)
}

// roundAway rounds level to tick away from the market for a position of side:
// down for a long, up for a short, so that it is never nearer than the
// distance it keeps from the price.
func roundAway(side model.SideType, level, tick float64) float64 {
	if tick <= 0 {
		return level
	}
	steps := level / tick
	if side == model.SideTypeSell {
		return clean(math.Ceil(steps-1e-9)*tick, tick)
	}
	return clean(math.Floor(steps+1e-9)*tick, tick)
}

// favorable returns how far best went in favour of a position of side opened
// at entry, in ATR.
func favorable(side model.SideType, entry, best, atr float64) float64 {
	if atr <= 0 || best == 0 || entry <= 0 {
		return 0
	}
	return math.Max(0, sign(side)*(best-entry)/atr)
}

// breakevenLevel returns the stop loss of the breakeven of a position of side
// opened at entry: beyond it by margin ATR and the costs (a distance of the
// price), rounded toward protection.
func breakevenLevel(side model.SideType, entry, margin, atr, costs, tick float64) float64 {
	return roundProtective(side, entry+sign(side)*(margin*atr+costs), tick)
}

// trailingLevel returns the stop loss of the trailing stop of a position of
// side exiting at exit: distance ATR behind it, rounded away from the market.
func trailingLevel(side model.SideType, exit, distance, atr, tick float64) float64 {
	return roundAway(side, exit-sign(side)*distance*atr, tick)
}

// tighter reports whether level protects a position of side more than current
// by more than step (a tick at least): any level, without a stop loss now.
func tighter(side model.SideType, level, current, step float64) bool {
	if current <= 0 {
		return level > 0
	}
	return sign(side)*(level-current) > step-1e-12
}

// onLosingSide reports whether level is on the losing side of exit for a
// position of side, by more than stops (the least distance of the broker): a
// stop loss the broker takes, not one the price is past already.
func onLosingSide(side model.SideType, level, exit, stops float64) bool {
	return level > 0 && sign(side)*(exit-level) > math.Max(stops, 0)
}
