package strategies

import (
	"math"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/tools/log"
)

// The positions of a bracket measured in R: the profit of a close now in units
// of the initial risk of the position, the distance from its entry to its first
// stop loss. The strategies of the rules of the prompts of the LLM agent (mean
// reversion, the scalps, the trend pullback, the squeeze breakout) move their
// stop loss by it: to the entry once a close reaches an R (the break-even, the
// spread unknown to the candles), and behind the price from another, never
// back; and end the trades going nowhere by it.

// rPlan is how a strategy moves the stop loss of its positions by their R
// multiple: to the entry from breakEvenR, behind the price from trailR at the
// level trail returns for the side held (NaN for none); 0 never.
type rPlan struct {
	breakEvenR float64
	trailR     float64
	trail      func(df *model.Dataframe, side model.SideType) float64
}

// rMultiple returns the R multiple of the position of the trade at price,
// false when its risk is unknown, eg: a position adopted after a restart.
func (t *scalpTrade) rMultiple(price float64) (float64, bool) {
	if t == nil || t.risk <= 0 {
		return 0, false
	}
	return rkSign(t.side) * (price - t.entry) / t.risk, true
}

// trackR records the best R multiple of the position followed on the pair of
// df on its last close, before the strategy reads it, see scalpTrade.bestR.
func (b *bracket) trackR(df *model.Dataframe) {
	trade := b.trades[df.Pair]
	if r, ok := trade.rMultiple(df.Close.Last(0)); ok {
		trade.bestR = math.Max(trade.bestR, r)
	}
}

// moveR moves the stop loss at the broker of the position followed on the pair
// of df per plan, see rPlan: nothing while its close fails, or its risk is
// unknown.
func (b *bracket) moveR(df *model.Dataframe, broker service.Broker, plan rPlan) {
	trade := b.trades[df.Pair]
	if trade == nil || trade.exiting {
		return
	}
	r, ok := trade.rMultiple(df.Close.Last(0))
	protected, can := broker.(service.ProtectionBroker)
	if !ok || !can {
		return
	}
	sign := rkSign(trade.side)
	stop, reason := na, ""
	if plan.breakEvenR > 0 && r >= plan.breakEvenR {
		stop, reason = trade.entry, "BREAK EVEN"
	}
	if plan.trailR > 0 && r >= plan.trailR && plan.trail != nil {
		if level := plan.trail(df, trade.side); !isNa(level) && (isNa(stop) || sign*level > sign*stop) {
			stop, reason = level, "TRAIL"
		}
	}
	if !isNa(stop) {
		b.moveStop(df, protected, trade, stop, reason)
	}
}

// moveStop moves the stop loss leg of the position of trade at the broker to
// stop, when it tightens it and stays on the losing side of the last close,
// where the broker takes it, and reports whether it moved; reason names the
// move in the log, eg: TRAIL.
func (b *bracket) moveStop(df *model.Dataframe, protected service.ProtectionBroker, trade *scalpTrade, stop float64,
	reason string) bool {

	sign := rkSign(trade.side)
	for i, leg := range trade.legs {
		if leg.Type != model.OrderTypeStopLoss {
			continue
		}
		current := leg.Price
		if leg.Stop != nil {
			current = *leg.Stop
		}
		if sign*stop <= sign*current || sign*stop >= sign*df.Close.Last(0) {
			return false
		}
		moved, err := protected.UpdateProtection(leg, stop)
		if live, ok := tighterAlready(err); ok {
			trade.legs[i] = live // the stop guard kept a more protective stop: followed as it is
			log.Infof("[%s] %s %s | %s | stop kept at %f, more protective than %f", b.name, df.Pair, b.timeframe,
				reason, rkLegLevel(live), stop)
			return false
		}
		if err != nil {
			log.Error(err)
			return false
		}
		trade.legs[i] = moved
		log.Infof("[%s] %s %s | %s | stop %f", b.name, df.Pair, b.timeframe, reason, stop)
		return true
	}
	return false
}

// timeR returns TIME when the position of trade has been held bars candles at
// least without its R multiple reaching reach (its best R since its opening
// when best, else the one of the last close): a trade going nowhere; with its
// risk unknown, after bars candles.
func timeR(df *model.Dataframe, trade *scalpTrade, bars int, reach float64, best bool) string {
	if trade == nil || bars <= 0 || trade.bars < bars {
		return ""
	}
	r, ok := trade.rMultiple(df.Close.Last(0))
	if best {
		r = trade.bestR
	}
	if !ok || r < reach {
		return "TIME"
	}
	return ""
}

// lastSwing returns the low of the last swing low of df confirmed by pivot
// candles on each side before the last candle, the high of the last swing high
// for a short: NaN without one.
func lastSwing(df *model.Dataframe, side model.SideType, pivot int) float64 {
	last := len(df.Close) - 1
	for i := last - pivot; i >= pivot; i-- {
		swing := true
		for k := i - pivot; k <= i+pivot && swing; k++ {
			if k == i {
				continue
			}
			if side == model.SideTypeBuy {
				swing = df.Low[i] < df.Low[k]
			} else {
				swing = df.High[i] > df.High[k]
			}
		}
		if swing {
			if side == model.SideTypeBuy {
				return df.Low[i]
			}
			return df.High[i]
		}
	}
	return na
}

// majorSessions are the sessions of London and of New York of the context of
// the LLM agent (agents/llm/session.go), in the time of each city, on its
// weekdays: from 08:00 until 17:00.
var majorSessions = func() []session {
	var list []session
	for _, zone := range []string{"Europe/London", "America/New_York"} {
		s, err := newSession("08:00", "17:00", zone)
		if err != nil {
			panic(err)
		}
		list = append(list, s)
	}
	return list
}()

// majorSessionOpen reports whether London or New York is open at t, eg: the
// close of a candle, as the context of the LLM agent tells it: within the hours
// of a session, on a weekday of its city.
func majorSessionOpen(t time.Time) bool {
	for _, s := range majorSessions {
		start, end := s.bounds(t)
		if day := start.In(s.loc).Weekday(); day == time.Saturday || day == time.Sunday {
			continue
		}
		if !t.Before(start) && t.Before(end) {
			return true
		}
	}
	return false
}
