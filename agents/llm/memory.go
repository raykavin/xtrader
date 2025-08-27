package llm

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

const (
	// maxRemembered are the decisions of a pair kept for the memory.
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

// PastDecision is a decision CandlesAgo candles before the one of now.
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

// remembered is a decision kept for the memory: its candle, and its action.
type remembered struct {
	at         time.Time
	action     Action
	confidence float64
	reason     string
}

// remember keeps the decision of the candle at on pair for the memory.
func (t *Trader) remember(pair string, at time.Time, d Decision) {
	reason := d.Reason
	if utf8.RuneCountInString(reason) > maxReason {
		reason = string([]rune(reason)[:maxReason-1]) + "…"
	}
	list := append(t.remembered[pair], remembered{at: at, action: d.Action, confidence: d.Confidence, reason: reason})
	if len(list) > maxRemembered {
		list = list[len(list)-maxRemembered:]
	}
	t.remembered[pair] = list
}

// memory returns the memory of pair at the candle at: its Config.Memory last
// decisions before it and trades closed.
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
		trades := closedTrades(orders)
		for i := len(trades) - 1; i >= 0 && len(memory.Trades) < n; i-- {
			trade := trades[i]
			memory.Trades = append(memory.Trades, PastTrade{Side: describeSide(trade.side), Entry: number(trade.entry),
				Exit: number(trade.exit), ExitBy: trade.exitBy, ResultPct: ratio(trade.resultPct),
				HeldCandles: candlesAgo(trade.opened) - candlesAgo(trade.closed), ClosedCandlesAgo: candlesAgo(trade.closed)})
		}
	}
	return memory
}

// closedTrade is a trade of the fills of a pair, see closedTrades.
type closedTrade struct {
	side           model.SideType
	entry, exit    float64
	exitBy         string
	resultPct      float64
	opened, closed time.Time
}

// closedTrades returns the trades the fills of orders closed, oldest first:
// the position opened by the fills of a side (its average price), closed by
// the ones of the other, the exit by a stop loss, a take profit (a grouped
// limit order), or else at the market.
func closedTrades(orders []model.Order) []closedTrade {
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
	var net, entry float64 // net signed, positive long; the average price of the open side
	var opened time.Time
	for _, fill := range fills {
		signed := fill.Quantity
		if fill.Side == model.SideTypeSell {
			signed = -signed
		}
		switch {
		case net == 0 || math.Signbit(net) == math.Signbit(signed):
			if net == 0 {
				opened = fill.UpdatedAt
			}
			entry = (entry*math.Abs(net) + fill.Price*math.Abs(signed)) / (math.Abs(net) + math.Abs(signed))
			net += signed
		default:
			side := model.SideTypeBuy
			if net < 0 {
				side = model.SideTypeSell
			}
			trades = append(trades, closedTrade{side: side, entry: entry, exit: fill.Price, exitBy: exitBy(fill),
				resultPct: sideSign(side) * (fill.Price/entry - 1) * 100, opened: opened, closed: fill.UpdatedAt})
			if math.Abs(signed) > math.Abs(net)+1e-12 { // a reversal: the rest opens at its price
				entry, opened = fill.Price, fill.UpdatedAt
			}
			net += signed
		}
		if math.Abs(net) < 1e-12 {
			net, entry = 0, 0
		}
	}
	return trades
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
// context: its pair, the candle it was taken on, and its answer.
type Remembered struct {
	Pair       string
	Candle     time.Time
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
	recent, err := journal.Recent(maxRemembered)
	if err != nil {
		return fmt.Errorf("llm: the journal: %w", err)
	}
	for _, r := range recent {
		t.remember(r.Pair, r.Candle, Decision{Action: r.Action, Confidence: r.Confidence,
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
