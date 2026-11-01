package entryfilter

import (
	"math"
	"time"

	"github.com/raykavin/xtrader/agents/metalabel"
	"github.com/raykavin/xtrader/model"
)

// The theoretical trades of the strategy, shared by the equity curve and the
// labels of the samples: every entry the strategy attempts, allowed or refused
// (by the filter or by the bot), opens one on its pair at its entry price (the
// close for an entry at the market; the level of a limit or stop entry, filled
// only once a later candle touches it within the horizon, else discarded),
// with the stop loss and the take profit of the order (0 for none), its side
// and its time. It ends on the candles after it, never the candle of its
// entry: its stop touched, -1 R; its target touched, the reward over the risk
// in R; both on one candle, the stop (the worst case, the order of the prices
// inside a candle unknown; on the candle filling a pending entry, its stop
// only, the target not taken then); neither by its horizon, at the close of
// the last candle, the R of the move. A trade the bot took (its entry
// accepted), whose position was seen, ends at the close of the candle its
// position of the pair goes flat on, eg: closed by the strategy.
//
// The equity curve counts the trades with a stop loss (their risk known), one
// at a time on a pair, as the strategy would hold one position: an entry
// attempted while a counted trade of its pair is pending or open opens a
// trade for the labels only. The labels take every entry, a stop or not (its
// R then 0, its label the side of its result).
//
// It is kept in memory only: an agent restarted starts it again from nothing.
// Live, the candles before the start only warm the indicators up, never the
// strategy (see strategy.Controller): the trades start with the agent; in a
// backtest, the strategy runs over the warm-up too (its entries held), its
// attempts there never counted by the equity curve (warm, decided before
// Config.RecordFrom, the start of the period), so that it starts the period
// empty, as it starts live.

// entryKind is how an entry is placed: at the market, a limit or a stop order.
type entryKind int

const (
	kindMarket entryKind = iota
	kindLimit
	kindStop
)

// candle is a closed candle of a pair, as the tracker reads it.
type candle struct {
	time                   time.Time
	open, high, low, close float64
}

// view is how a consumer of a trade (the equity curve, the labels) follows it:
// its horizon in candles, and done once its outcome is known or the trade
// discarded for it.
type view struct {
	horizon int
	done    bool
}

// theoretical is a theoretical trade, see the package doc of tracker.go.
type theoretical struct {
	pair         string
	side         model.SideType
	kind         entryKind
	price        float64   // the close of a market entry, the level of a pending one
	stop, target float64   // 0 for none
	at           time.Time // the open of the candle of the attempt
	filled       bool
	entry        float64   // the price it filled at
	filledAt     time.Time // the open of the candle it filled on
	waited, bars int       // candles waited for the fill, and held since it
	real         bool      // the entry accepted by the bot
	warm         bool      // decided in the warm-up of a backtest: never counted by the equity curve
	held         bool      // its position seen
	equity       *view     // nil when not counted
	label        *view     // nil when not recorded
	sample       metalabel.Sample
}

// sign returns 1 for a buy, -1 for a sell.
func (t *theoretical) sign() float64 {
	if t.side == model.SideTypeSell {
		return -1
	}
	return 1
}

// risk returns the distance of the entry to its stop loss, 0 without a stop on
// the losing side of the entry.
func (t *theoretical) risk(entry float64) float64 {
	if t.stop <= 0 || t.sign()*(entry-t.stop) <= 0 {
		return 0
	}
	return math.Abs(entry - t.stop)
}

// stopped reports whether c touched the stop loss of the trade.
func (t *theoretical) stopped(c candle) bool {
	if t.risk(t.entry) == 0 {
		return false
	}
	if t.side == model.SideTypeSell {
		return c.high >= t.stop
	}
	return c.low <= t.stop
}

// reached reports whether c touched the take profit of the trade, on the
// winning side of its entry.
func (t *theoretical) reached(c candle) bool {
	if t.target <= 0 || t.sign()*(t.target-t.entry) <= 0 {
		return false
	}
	if t.side == model.SideTypeSell {
		return c.low <= t.target
	}
	return c.high >= t.target
}

// touched reports whether c touched the level of a pending entry.
func (t *theoretical) touched(c candle) bool {
	buy := t.side == model.SideTypeBuy
	if (t.kind == kindLimit) == buy {
		return c.low <= t.price // a buy limit, a sell stop: below
	}
	return c.high >= t.price // a sell limit, a buy stop: above
}

// done reports whether every view of the trade is done.
func (t *theoretical) done() bool {
	return (t.equity == nil || t.equity.done) && (t.label == nil || t.label.done)
}

// tracker follows the theoretical trades, see the package doc of tracker.go.
type tracker struct {
	equityBars, labelBars int // the horizons, 0 for a view off
	trades                map[string][]*theoretical
	curve                 []float64 // the equity after each counted trade ended, in R
	total                 float64
	record                func(metalabel.Sample) // the samples labeled, nil for none
}

func newTracker(equityBars, labelBars int, record func(metalabel.Sample)) *tracker {
	return &tracker{equityBars: equityBars, labelBars: labelBars, trades: make(map[string][]*theoretical),
		record: record}
}

// add opens the theoretical trade of an entry attempted: counted by the equity
// curve when on, the trade has a stop, wasn't decided in the warm-up of a
// backtest and no counted trade of its pair is pending or open; recorded for
// the labels when on and sample has features.
func (k *tracker) add(t *theoretical, sample *metalabel.Sample) {
	if k.equityBars > 0 && !t.warm && t.risk(t.price) > 0 && !k.counting(t.pair) {
		t.equity = &view{horizon: k.equityBars}
	}
	if k.labelBars > 0 && sample != nil {
		t.label = &view{horizon: k.labelBars}
		t.sample = *sample
	}
	if t.equity == nil && t.label == nil {
		return
	}
	if t.kind == kindMarket {
		t.filled, t.entry, t.filledAt = true, t.price, t.at
	}
	k.trades[t.pair] = append(k.trades[t.pair], t)
}

// counting reports whether a trade of pair counted by the equity curve is
// pending or open.
func (k *tracker) counting(pair string) bool {
	for _, t := range k.trades[pair] {
		if t.equity != nil && !t.equity.done {
			return true
		}
	}
	return false
}

// onCandle follows the trades of pair over c, a candle closed after their
// entry; held tells whether the agent holds a position of pair on a side.
func (k *tracker) onCandle(pair string, c candle, held func(model.SideType) bool) {
	for _, t := range k.trades[pair] {
		if !c.time.After(t.at) || t.done() {
			continue
		}
		if !t.filled {
			if t.touched(c) {
				t.filled, t.entry, t.filledAt = true, t.price, c.time
				if t.stopped(c) {
					k.end(t, -1, metalabel.OutcomeStop, c.time, false)
				}
				continue
			}
			t.waited++
			for _, v := range []*view{t.equity, t.label} {
				if v != nil && !v.done && t.waited >= v.horizon {
					v.done = true // never filled: discarded
				}
			}
			continue
		}
		if !c.time.After(t.filledAt) {
			continue
		}
		t.bars++
		switch risk := t.risk(t.entry); {
		case t.stopped(c):
			k.end(t, -1, metalabel.OutcomeStop, c.time, false)
		case t.reached(c):
			k.end(t, rOf(math.Abs(t.target-t.entry), risk), metalabel.OutcomeTarget, c.time, true)
		case t.real && t.held && !held(t.side):
			k.end(t, rOf(t.sign()*(c.close-t.entry), risk), metalabel.OutcomeTime, c.time,
				t.sign()*(c.close-t.entry) > 0)
		default:
			for _, v := range []*view{t.equity, t.label} {
				if v != nil && !v.done && t.bars >= v.horizon {
					k.endView(t, v, rOf(t.sign()*(c.close-t.entry), risk), metalabel.OutcomeTime, c.time,
						t.sign()*(c.close-t.entry) > 0)
				}
			}
		}
	}
	k.prune(pair)
}

// observe follows the positions of pair after the strategy ran on the candle
// c: a trade the bot took is held once its position is seen, and ends at the
// close of c as its position goes flat, on a candle after its entry.
func (k *tracker) observe(pair string, c candle, held func(model.SideType) bool) {
	for _, t := range k.trades[pair] {
		if !t.real || t.done() {
			continue
		}
		holding := held(t.side)
		switch {
		case !t.held && holding:
			t.held = true
		case t.held && !holding && t.filled && c.time.After(t.filledAt):
			k.end(t, rOf(t.sign()*(c.close-t.entry), t.risk(t.entry)), metalabel.OutcomeTime, c.time,
				t.sign()*(c.close-t.entry) > 0)
		}
	}
	k.prune(pair)
}

// end ends every view of t with r, outcome at at; won tells the label of a
// trade ended by time.
func (k *tracker) end(t *theoretical, r float64, outcome string, at time.Time, won bool) {
	for _, v := range []*view{t.equity, t.label} {
		if v != nil && !v.done {
			k.endView(t, v, r, outcome, at, won)
		}
	}
}

// endView ends the view v of t with r, outcome at at: a point of the equity
// curve, or the sample labeled.
func (k *tracker) endView(t *theoretical, v *view, r float64, outcome string, at time.Time, won bool) {
	v.done = true
	switch v {
	case t.equity:
		k.total += r
		k.curve = append(k.curve, k.total)
	case t.label:
		if k.record == nil {
			return
		}
		s := t.sample
		s.Entry, s.R, s.Outcome, s.ExitTime = t.entry, r, outcome, at.Unix()
		s.Label = 0
		if outcome == metalabel.OutcomeTarget || outcome == metalabel.OutcomeTime && won {
			s.Label = 1
		}
		k.record(s)
	}
}

// prune drops the trades of pair done.
func (k *tracker) prune(pair string) {
	kept := k.trades[pair][:0]
	for _, t := range k.trades[pair] {
		if !t.done() {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(k.trades, pair)
		return
	}
	k.trades[pair] = kept
}

// paused reports whether the equity curve is under its SMA of its last n
// points, judged once it has n of them; and the equity and the SMA then.
func (k *tracker) paused(n int) (bool, float64, float64) {
	if n <= 0 || len(k.curve) < n {
		return false, 0, 0
	}
	var sum float64
	for _, e := range k.curve[len(k.curve)-n:] {
		sum += e
	}
	sma := sum / float64(n)
	last := k.curve[len(k.curve)-1]
	return last < sma, last, sma
}

// rOf returns move in R of risk, 0 without a risk.
func rOf(move, risk float64) float64 {
	if risk <= 0 {
		return 0
	}
	return move / risk
}
