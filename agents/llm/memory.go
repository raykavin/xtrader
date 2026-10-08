package llm

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

const (
	// maxRemembered are the decisions of a pair kept for the memory, at least:
	// Config.Memory when more.
	maxRemembered = 20
	// maxReason bounds the reason of a decision remembered, in characters.
	maxReason = 220
)

// Memory is what the model decided and traded lately on the pair, newest
// first: its last decisions, and its last trades closed with their outcome,
// so it builds on them rather than starting over at every candle.
type Memory struct {
	Decisions []PastDecision `json:"recent_decisions"`
	Trades    []PastTrade    `json:"recent_trades"`
}

// PastDecision is a decision taken CandlesAgo candles before now, the time of
// the analysis, rounded: 0 within the last half candle, eg: an analysis of the
// candle in progress (see Config.ConsultInterval).
type PastDecision struct {
	CandlesAgo int    `json:"candles_ago"`
	Action     Action `json:"action"`
	Confidence ratio  `json:"confidence"`
	Reason     string `json:"reason"`
}

// PastTrade is a trade closed ClosedCandlesAgo candles before now: its side,
// its average entry, its exit and what exited it (stop loss, take profit or
// exit at the market), its result in % of the entry, and its candles held.
type PastTrade struct {
	Side             string `json:"side"`
	Entry            number `json:"entry_price"`
	Exit             number `json:"exit_price"`
	ExitBy           string `json:"exit_by"`
	ResultPct        ratio  `json:"result_pct"`
	HeldCandles      int    `json:"held_candles"`
	ClosedCandlesAgo int    `json:"closed_candles_ago"`
}

// remembered is a decision kept for the memory: when it was taken (see
// prepared), and its action.
type remembered struct {
	at         time.Time
	action     Action
	confidence float64
	reason     string
}

// remember keeps the decision taken at at on pair for the memory: by its time,
// not by its candle, so that the analyses of the candle in progress see the
// ones taken before them in it.
func (t *Trader) remember(pair string, at time.Time, d Decision) {
	reason := d.Reason
	if utf8.RuneCountInString(reason) > maxReason {
		reason = string([]rune(reason)[:maxReason-1]) + "…"
	}
	list := append(t.remembered[pair], remembered{at: at, action: d.Action, confidence: d.Confidence, reason: reason})
	if limit := t.rememberedLimit(); len(list) > limit {
		list = list[len(list)-limit:]
	}
	t.remembered[pair] = list
}

// rememberedLimit returns the decisions of a pair kept for the memory.
func (t *Trader) rememberedLimit() int {
	return max(maxRemembered, t.cfg.Memory)
}

// memory returns the memory of pair at at, the time of the analysis (see
// dayClock): its Config.Memory last decisions taken before it and trades
// closed, their candles counted back from it.
func (t *Trader) memory(pair string, at time.Time) *Memory {
	n := t.cfg.Memory
	memory := &Memory{Decisions: []PastDecision{}, Trades: []PastTrade{}}
	candlesAgo := func(then time.Time) int {
		if t.period <= 0 {
			return 0
		}
		return int(math.Round(float64(at.Sub(then)) / float64(t.period)))
	}
	decisions := t.remembered[pair]
	for i := len(decisions) - 1; i >= 0 && len(memory.Decisions) < n; i-- {
		if d := decisions[i]; d.at.Before(at) {
			memory.Decisions = append(memory.Decisions, PastDecision{CandlesAgo: candlesAgo(d.at), Action: d.action,
				Confidence: ratio(d.confidence), Reason: d.reason})
		}
	}
	if t.orders != nil {
		orders, err := t.orders(pair)
		if err != nil {
			log.Warnf("[LLM] %s | the stored orders, for the memory: %v", pair, err)
		}
		trades, _, _ := t.pairTrades(pair, orders)
		for i := len(trades) - 1; i >= 0 && len(memory.Trades) < n; i-- {
			trade := trades[i]
			memory.Trades = append(memory.Trades, PastTrade{Side: describeSide(trade.side), Entry: number(trade.entry),
				Exit: number(trade.exit), ExitBy: trade.exitBy, ResultPct: ratio(trade.resultPct),
				HeldCandles: candlesAgo(trade.opened) - candlesAgo(trade.closed), ClosedCandlesAgo: candlesAgo(trade.closed)})
		}
	}
	return memory
}

// closedTrade is a trade of the fills of a pair, see closedTrades: its side,
// its size closed, its average entry and its exit, what exited it, and the
// fills that opened it (the first one) and closed it.
type closedTrade struct {
	side           model.SideType
	size           float64
	entry, exit    float64
	exitBy         string
	resultPct      float64
	opened, closed time.Time
	open, close    model.Order
}

// openTrade is the position the fills of a pair leave open, see tradesOf: its
// side, its size, its average entry, and the fill that opened it.
type openTrade struct {
	side   model.SideType
	size   float64
	entry  float64
	opened time.Time
	open   model.Order
}

// closedTrades returns the trades the fills of orders closed, oldest first,
// told apart by position with link (nil for none), see tradesOf: the position
// opened by a fill, closed by the ones of the other side, the exit by a stop
// loss, a take profit (a grouped limit order), or else at the market.
func closedTrades(orders []model.Order, link fillLink) []closedTrade {
	closed, _, _, _ := tradesOf(orders, link)
	return closed
}

// pairTrades returns the trades of orders, the stored ones of pair, see
// tradesOf: told apart by position as the broker tells them, when it does
// (fillPositions). The orphans are logged once.
func (t *Trader) pairTrades(pair string, orders []model.Order) ([]closedTrade, *openTrade, map[int64]string) {
	var link fillLink
	if positions, ok := t.reporter.(fillPositions); ok {
		link = positions.FillPositions
	}
	closed, open, roles, orphans := tradesOf(orders, link)
	for _, fill := range orphans {
		if t.orphans == nil {
			t.orphans = make(map[int64]bool)
		}
		if t.orphans[fill.ID] {
			continue
		}
		t.orphans[fill.ID] = true
		log.Warnf("[LLM] %s | the fill of the order %d (%s %s %g at %g, %s) closes a position whose entry isn't "+
			"among the orders of the bot, or more than it: that part is left out of its trades and of the results "+
			"of its day", pair, fill.ID, fill.Side, fill.Type, fill.Quantity, fill.Price,
			fill.UpdatedAt.UTC().Format(time.RFC3339))
	}
	return closed, open, roles
}

// lotDust is the size under which what is left of a position, or of a fill,
// is rounding: nothing.
const lotDust = 1e-9

// fillLink tells the positions of the account the fill of an order opened, 0
// for none, and closed, false when unknown: see fillPositions.
type fillLink func(fill model.Order) (opened int64, closed []int64, ok bool)

// fillPositions is an AccountReporter telling the positions of the account
// the fills of the bot opened and closed, eg: the MT5 adapter, by their
// tickets: the trades are told apart by position with it, see tradesOf.
type fillPositions interface {
	FillPositions(fill model.Order) (opened int64, closed []int64, ok bool)
}

// lot is a position the fills of a pair opened, see tradesOf: its side, its
// size left, its entry, the fill that opened it, and its position at the
// broker, the one link told, else the ExchangeID of the fill (the ticket of
// the position on MT5).
type lot struct {
	side     model.SideType
	size     float64
	entry    float64
	opened   time.Time
	open     model.Order
	position int64
}

// tradesOf returns the trades the fills of orders closed, oldest first (see
// closedTrades), the position they leave open, nil when flat, the role of each
// fill ("entry", opening a position, or "exit", closing (a part of) one), and
// the orphans: the fills closing a position whose entry isn't among them, left
// out of the trades, never taken for an entry.
//
// Each entry opens a position of its own, as on MT5. A fill closes, of the
// positions open on the other side:
//   - the ones link tells, when it knows the fill (eg: MT5, by their
//     tickets), the rest opening the position it tells, if any;
//   - for a leg (a grouped order: a stop loss or a take profit), the newest
//     ones opened before the leg was placed, never more;
//   - else the oldest ones first, the rest opening a position (a reversal).
//
// So a fill missing, eg: an entry unstored or a position closed by hand,
// leaves its own position aside rather than shifting every one after it.
func tradesOf(orders []model.Order, link fillLink) ([]closedTrade, *openTrade, map[int64]string, []model.Order) {
	var fills []model.Order
	for _, order := range orders {
		if order.Status == model.OrderStatusTypeFilled && order.Quantity > 0 && order.Price > 0 {
			fills = append(fills, order)
		}
	}
	sort.SliceStable(fills, func(i, j int) bool {
		if a, b := fills[i].UpdatedAt, fills[j].UpdatedAt; !a.Equal(b) {
			return a.Before(b)
		}
		return fills[i].ID < fills[j].ID
	})

	var trades []closedTrade
	var orphans []model.Order
	roles := make(map[int64]string, len(fills))
	var lots []*lot // open, oldest first
	for _, fill := range fills {
		var opened int64
		var positions []int64
		linked := false
		if link != nil {
			opened, positions, linked = link(fill)
		}
		leg := fill.GroupID != nil

		var targets []*lot // the positions it closes, in turn
		matched := 0       // of positions
		for _, l := range lots {
			switch {
			case l.side == fill.Side:
			case linked:
				if slices.Contains(positions, l.position) {
					targets = append(targets, l)
					matched++
				}
			case leg:
				if placedAfter(fill, l.open) {
					targets = append([]*lot{l}, targets...) // the newest first
				}
			default:
				targets = append(targets, l)
			}
		}
		left := fill.Quantity
		closeLots := func(targets []*lot) {
			for _, l := range targets {
				if left <= lotDust {
					return
				}
				size := math.Min(left, l.size)
				trades = append(trades, closedTrade{side: l.side, size: size, entry: l.entry, exit: fill.Price,
					exitBy: exitBy(fill), resultPct: sideSign(l.side) * (fill.Price/l.entry - 1) * 100,
					opened: l.opened, closed: fill.UpdatedAt, open: l.open, close: fill})
				l.size -= size
				left -= size
			}
		}
		closeLots(targets)
		if linked && !leg && opened == 0 && len(positions) == 1 && matched == 1 && left > lotDust {
			// an order of a previous run closing several positions, the
			// broker telling the first one only: the next ones in turn. Never
			// when it tells several, one of them unknown: the rest is that
			// one's, an orphan, the others still open
			var others []*lot
			for _, l := range lots {
				if l.side != fill.Side && l.size > lotDust && !slices.Contains(targets, l) {
					others = append(others, l)
				}
			}
			closeLots(others)
		}
		lots = slices.DeleteFunc(lots, func(l *lot) bool { return l.size <= lotDust })

		exited := left < fill.Quantity-lotDust
		switch {
		case leg && left > lotDust, !leg && linked && (matched < len(positions) || opened == 0 && left > lotDust):
			// the exit of a position whose entry isn't among the fills
			orphans = append(orphans, fill)
			exited = true
		}
		if left > lotDust && !leg && (!linked || opened != 0) {
			position := fill.ExchangeID
			if linked {
				position = opened
			}
			lots = append(lots, &lot{side: fill.Side, size: left, entry: fill.Price, opened: fill.UpdatedAt,
				open: fill, position: position})
		}
		roles[fill.ID] = "entry"
		if exited {
			roles[fill.ID] = "exit"
		}
	}
	return trades, openOf(lots), roles, orphans
}

// placedAfter reports whether the leg was placed after the fill opened, its
// entry: by their IDs, numbered in turn, else by their creation.
func placedAfter(leg, entry model.Order) bool {
	if leg.ID > 0 && entry.ID > 0 {
		return leg.ID > entry.ID
	}
	return !leg.CreatedAt.Before(entry.CreatedAt)
}

// openOf returns the position lots leave open: the ones of the side of the
// newest, their average entry, from the oldest of them; nil when none.
func openOf(lots []*lot) *openTrade {
	if len(lots) == 0 {
		return nil
	}
	side := lots[len(lots)-1].side
	var open *openTrade
	var cost float64
	for _, l := range lots {
		if l.side != side {
			continue
		}
		if open == nil {
			open = &openTrade{side: side, opened: l.opened, open: l.open}
		}
		open.size += l.size
		cost += l.size * l.entry
	}
	open.entry = cost / open.size
	return open
}

// exitBy names what the fill closing a trade was.
func exitBy(fill model.Order) string {
	switch {
	case fill.Type == model.OrderTypeStopLoss || fill.Type == model.OrderTypeStopLossLimit:
		return "stop loss"
	case fill.GroupID != nil || fill.Type == model.OrderTypeTakeProfit || fill.Type == model.OrderTypeTakeProfitLimit:
		return "take profit"
	}
	return "exit at the market"
}

// JournalEntry is a call of the Trader to its model, see Journal: when, the
// decision kept of it (ID, see ui.Decision), why it was asked (Trigger, and
// Requested, the channel of the reanalysis it answers), the model and the
// prompt, the whole context it was asked with, and its answer, refused with
// Error too, or its error.
type JournalEntry struct {
	At        time.Time          `json:"at"`
	ID        string             `json:"id,omitempty"`
	Trigger   string             `json:"trigger,omitempty"`
	Requested string             `json:"requested_by,omitempty"`
	Model     string             `json:"model"`
	Prompt    *ui.DecisionPrompt `json:"prompt,omitempty"`
	LatencyMS int64              `json:"latency_ms"`
	Input     Input              `json:"input"`
	Decision  *Decision          `json:"decision,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// Remembered is a decision of a Journal read back for the memory of the
// context: its pair, the candle it was taken on, when (the time of its entry,
// zero when unknown: the close of the candle then), and its answer.
type Remembered struct {
	Pair       string
	Candle     time.Time
	At         time.Time
	Action     Action
	Confidence float64
	Reason     string
}

// Journal keeps the calls of the Trader to its model with their whole context,
// see SetJournal: the journal to measure the decisions against their outcomes
// (the orders of the bot), and the memory of the context across the restarts.
// Called in the flow of the strategy.
type Journal interface {
	// Append keeps the entry. It must not wait for a store far away: the
	// flow of the strategy waits for it.
	Append(entry JournalEntry) error

	// Recent returns the last decisions of each pair, perPair at most,
	// oldest first: the calls answered with a valid decision only.
	Recent(perPair int) ([]Remembered, error)
}

// SetJournal journals every call of the Trader to its model to journal, with
// the whole context it was asked with and its answer or its error, and reads
// back the last decisions of each pair it holds for the memory of the context.
// Before the bot runs.
func (t *Trader) SetJournal(journal Journal) error {
	recent, err := journal.Recent(t.rememberedLimit())
	if err != nil {
		return fmt.Errorf("llm: the journal: %w", err)
	}
	for _, r := range recent {
		at := r.At
		if at.IsZero() {
			at = r.Candle.Add(t.period) // the decision of its close, the earliest
		}
		t.remember(r.Pair, at, Decision{Action: r.Action, Confidence: r.Confidence,
			Reason: strings.TrimSpace(r.Reason)})
	}
	if len(recent) > 0 {
		log.Infof("[SETUP] LLM: %d decisions of the journal remembered", len(recent))
	}
	t.journal = journal
	return nil
}

// record journals the call to the model with input, answered with decision
// or err, with the prompt of st: a decision refused with it, id the one of the
// decision kept, trigger why it was asked and requestedBy the channel of the
// reanalysis it answers.
func (t *Trader) record(st *promptState, input Input, latency time.Duration, decision Decision, err error, id,
	trigger, requestedBy string) {
	if t.journal == nil {
		return
	}
	entry := JournalEntry{At: t.now().UTC(), ID: id, Trigger: trigger, Requested: requestedBy, Model: t.name,
		Prompt: st.decisionPrompt(), LatencyMS: latency.Milliseconds(), Input: input}
	if err != nil {
		entry.Error = err.Error()
	}
	if decision.Action != "" {
		entry.Decision = &decision
	}
	if err := t.journal.Append(entry); err != nil {
		log.Warnf("[LLM] %s | the journal: %v", input.Symbol, err)
	}
}
