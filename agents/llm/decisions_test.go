package llm

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/ui"
)

// last returns the last decision kept of the pair of the runner.
func (r *traderRunner) lastDecision() ui.Decision {
	r.t.Helper()
	decisions, err := r.trader.Decisions(testPair, 1)
	require.NoError(r.t, err)
	require.Len(r.t, decisions, 1, "no decision kept")
	return decisions[0]
}

// numbering is the paper wallet numbering the orders it places, as the order
// controller of the bot does, the broker of the strategies: the wallet leaves
// their ID to it.
type numbering struct {
	*exchange.PaperWallet
}

func (n numbering) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	order, err := n.PaperWallet.CreateOrderMarket(side, pair, size)
	order.ID = order.ExchangeID
	return order, err
}

func (n numbering) CreateOrderMarketProtected(side model.SideType, pair string, size, stop,
	profit float64) ([]model.Order, error) {
	orders, err := n.PaperWallet.CreateOrderMarketProtected(side, pair, size, stop, profit)
	for i := range orders {
		orders[i].ID = orders[i].ExchangeID
	}
	return orders, err
}

func (n numbering) UpdateProtection(leg model.Order, level float64) (model.Order, error) {
	moved, err := n.PaperWallet.UpdateProtection(leg, level)
	moved.ID = moved.ExchangeID
	return moved, err
}

// Every decision is kept with its justification, the orders it placed or moved
// linked to it, whatever came of it.
func TestTrader_KeepsDecisions(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	r.broker = numbering{r.wallet}
	var _ strategy.LLMStrategy = r.trader
	var _ ui.DecisionSource = r.trader
	require.Equal(t, "fake test-model", r.trader.LLM())
	var heard []ui.Decision
	r.trader.ListenDecisions(func(d ui.Decision) { heard = append(heard, d) })

	// WAIT: no order, kept all the same
	wait := decide(ActionWait, 0.4, nil, nil, nil)
	wait.decision.Levels = []Level{{Kind: LevelSupport, Price: 98}}
	r.step(100, wait)
	d := r.lastDecision()
	require.Equal(t, "WAIT", d.Action)
	require.Equal(t, "test", d.Reason)
	require.Equal(t, OutcomeApplied, d.Outcome)
	require.Equal(t, TriggerCandle, d.Trigger)
	require.Equal(t, []ui.Level{{Kind: "support", Price: 98}}, d.Levels)
	require.Empty(t, d.OrderIDs)
	require.Equal(t, r.df.Time[len(r.df.Time)-1].Unix(), d.Candle)
	require.Len(t, heard, 1, "the listeners hear it")

	// an entry: its order and its legs, with the notes and the image of the
	// answer
	entry := enterLong(100.5)
	entry.decision.Notes = []Note{{Kind: "text", Text: "the range broke"}}
	entry.decision.Attachments = []Attachment{{MediaType: "image/png", Data: []byte("PNG")}}
	r.step(100.5, entry)
	d = r.lastDecision()
	require.Equal(t, "ENTER_LONG", d.Action)
	require.Len(t, d.OrderIDs, 3, "the entry and its two legs")
	for _, id := range d.OrderIDs {
		require.Positive(t, id)
		require.Equal(t, d.ID, r.trader.OrderDecision(id))
	}
	require.Empty(t, r.trader.OrderDecision(d.OrderIDs[2]+100), "an order of no decision")
	entered := d
	require.Equal(t, []ui.DecisionNote{{Kind: "text", Text: "the range broke"}}, d.Notes)
	require.Equal(t, []ui.DecisionAttachment{{Index: 0, MediaType: "image/png", Size: 3}}, d.Attachments)
	mediaType, data, err := r.trader.Attachment(d.ID, 0)
	require.NoError(t, err)
	require.Equal(t, "image/png", mediaType)
	require.Equal(t, []byte("PNG"), data)
	_, _, err = r.trader.Attachment(d.ID, 1)
	require.ErrorIs(t, err, ui.ErrUnknownDecision)
	require.Equal(t, "ENTER_LONG", heard[len(heard)-1].Action, "heard with its orders")
	require.Len(t, heard[len(heard)-1].OrderIDs, 3)

	// the stop loss moved: the leg moved, which keeps the decision that placed
	// it, as the order controller keeps its ID
	r.step(101, decide(ActionModifySL, 0.8, nil, ptr(99.5), nil))
	d = r.lastDecision()
	require.Len(t, d.OrderIDs, 1)
	require.Contains(t, entered.OrderIDs, d.OrderIDs[0], "the stop loss of the entry")
	require.Equal(t, entered.ID, r.trader.OrderDecision(d.OrderIDs[0]))

	// a decision refused by the rules: its justification kept, no order
	refused := decide(ActionModifySL, 0.8, nil, ptr(120), nil)
	refused.decision.Reason = "tighten it"
	r.step(101.2, refused)
	d = r.lastDecision()
	require.Equal(t, OutcomeRefused, d.Outcome)
	require.Equal(t, "tighten it", d.Reason)
	require.Contains(t, d.Error, "the stop loss 120 of a LONG")
	require.Equal(t, "LONG", d.Position)

	// ignored under the min confidence
	r.step(101.3, decide(ActionExit, 0.3, nil, nil, nil))
	d = r.lastDecision()
	require.Equal(t, OutcomeIgnored, d.Outcome)
	require.Empty(t, d.OrderIDs)

	// the exit: its order at the market
	r.step(101.4, decide(ActionExit, 0.9, nil, nil, nil))
	d = r.lastDecision()
	require.Len(t, d.OrderIDs, 1)
	require.NotContains(t, entered.OrderIDs, d.OrderIDs[0])
	require.Equal(t, d.ID, r.trader.OrderDecision(d.OrderIDs[0]))
	require.Zero(t, r.position())

	// a broker that doesn't number its orders: none linked
	r.broker = r.wallet
	r.step(101.6, enterLong(101.6))
	require.Empty(t, r.lastDecision().OrderIDs)
	r.broker = numbering{r.wallet}
	r.step(101.7, decide(ActionExit, 0.9, nil, nil, nil))

	// an answer failed: no decision, the error kept
	r.step(101.5, answer{err: ErrUnavailable})
	d = r.lastDecision()
	require.Equal(t, OutcomeFailed, d.Outcome)
	require.Empty(t, d.Action)
	require.Empty(t, d.Reason)
	require.Contains(t, d.Error, "unavailable")

	all, err := r.trader.Decisions("", 100)
	require.NoError(t, err)
	require.Len(t, all, 9)
	require.Equal(t, "WAIT", all[len(all)-1].Action, "newest first")
	found, ok, err := r.trader.Decision(all[len(all)-1].ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, all[len(all)-1], found)
	_, ok, err = r.trader.Decision("none")
	require.NoError(t, err)
	require.False(t, ok)
}

// recordingStore is a DecisionStore keeping what it is given in memory.
type recordingStore struct {
	*MemoryDecisions
	saved       []ui.Decision
	attachments [][]Attachment
}

func (r *recordingStore) Save(d ui.Decision, attachments []Attachment) error {
	r.saved = append(r.saved, d)
	r.attachments = append(r.attachments, attachments)
	return r.MemoryDecisions.Save(d, attachments)
}

// The decisions go to the store of SetDecisionStore, with the content of their
// attachments by index, the empty ones and the ones beyond the limits left
// out.
func TestTrader_DecisionStore(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	r.broker = numbering{r.wallet}
	store := &recordingStore{MemoryDecisions: NewMemoryDecisions(0)}
	r.trader.SetDecisionStore(store)
	entry := enterLong(100)
	entry.decision.Attachments = []Attachment{{MediaType: "image/jpeg", Data: []byte("JPEG")},
		{MediaType: "image/png"}, // empty, left out
		{MediaType: "image/png", Data: make([]byte, maxAttachmentSize+1)}}
	r.step(100, entry)

	require.Len(t, store.saved, 1)
	kept := store.saved[0]
	require.Equal(t, kept, r.lastDecision(), "read from the store")
	require.Equal(t, []ui.DecisionAttachment{{Index: 0, MediaType: "image/jpeg", Size: 4}}, kept.Attachments)
	require.Equal(t, []Attachment{{MediaType: "image/jpeg", Data: []byte("JPEG")}}, store.attachments[0])
	require.Len(t, kept.Notes, 1)
	require.Contains(t, kept.Notes[0].Text, "attachment 2 left out")
	require.Len(t, kept.OrderIDs, 3)
	require.Equal(t, kept.ID, r.trader.OrderDecision(kept.OrderIDs[0]))
	mediaType, data, err := r.trader.Attachment(kept.ID, 0)
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", mediaType)
	require.Equal(t, []byte("JPEG"), data)
}

func TestMemoryDecisions(t *testing.T) {
	decision := func(id string, at int64, pair string, orders ...int64) ui.Decision {
		return ui.Decision{ID: id, DecidedAt: at, Pair: pair, OrderIDs: orders}
	}
	m := NewMemoryDecisions(3)
	require.Error(t, m.Save(ui.Decision{}, nil), "no ID")
	require.NoError(t, m.Save(decision("b", 20, "BTC", 1, 2), []Attachment{{MediaType: "image/png", Data: []byte("x")}}))
	require.NoError(t, m.Save(decision("a", 10, "ETH"), nil))
	require.NoError(t, m.Save(decision("c", 20, "BTC", 2), nil)) // moves the order 2 placed by b

	list, err := m.Decisions("", 10)
	require.NoError(t, err)
	require.Equal(t, []string{"c", "b", "a"}, ids(list), "newest first, then by ID")
	list, _ = m.Decisions("BTC", 1)
	require.Equal(t, []string{"c"}, ids(list))
	require.Equal(t, "b", m.OrderDecision(2), "the first decision listing it placed it")

	// loaded from elsewhere: the ones saved take precedence, the attachments
	// read elsewhere
	loaded := decision("b", 20, "BTC", 1, 2)
	loaded.Reason = "stale"
	loaded.Attachments = []ui.DecisionAttachment{{Index: 0, MediaType: "image/png", Size: 1}}
	m.Load([]ui.Decision{loaded, decision("z", 5, "BTC")}, map[int64]string{2: "z", 7: "z"})
	found, ok, err := m.Decision("b")
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, found.Reason)
	require.Equal(t, "b", m.OrderDecision(2))
	require.Equal(t, "z", m.OrderDecision(7))

	// beyond the capacity the oldest go, their orders still told
	d, attachments := withAttachments(decision("d", 30, "ETH", 9), []Attachment{{MediaType: "image/png",
		Data: []byte("d")}})
	require.NoError(t, m.Save(d, attachments))
	list, _ = m.Decisions("", 10)
	require.Equal(t, []string{"d", "c", "b"}, ids(list))
	_, ok, _ = m.Decision("a")
	require.False(t, ok)
	require.Equal(t, "z", m.OrderDecision(7))
	_, data, err := m.Attachment("d", 0)
	require.NoError(t, err)
	require.Equal(t, []byte("d"), data)
	_, _, err = m.Attachment("d", 1)
	require.ErrorIs(t, err, ui.ErrUnknownDecision)
	_, _, err = m.Attachment("a", 0)
	require.ErrorIs(t, err, ui.ErrUnknownDecision)

	// saved again, eg: its orders linked
	require.NoError(t, m.Save(decision("c", 20, "BTC", 2, 3), nil))
	list, _ = m.Decisions("", 10)
	require.Equal(t, []string{"d", "c", "b"}, ids(list))
	require.Equal(t, "c", m.OrderDecision(3))
}

// ids returns the IDs of decisions.
func ids(decisions []ui.Decision) []string {
	list := make([]string, 0, len(decisions))
	for _, d := range decisions {
		list = append(list, d.ID)
	}
	return list
}

// journalLine is a line of the journal of the version before the decisions
// were kept.
func journalLine(t *testing.T, at time.Time, candle time.Time, position string, decision *Decision,
	failure string) string {
	t.Helper()
	input := map[string]any{"symbol": testPair, "timeframe": "15m", "time": candle, "price": 100,
		"rules": map[string]any{"min_confidence": 0.6}, "position": nil}
	if position != "" {
		input["position"] = map[string]any{"side": position}
	}
	entry := map[string]any{"at": at, "model": "openai gpt-test", "latency_ms": 900, "input": input}
	if decision != nil {
		entry["decision"] = decision
	}
	if failure != "" {
		entry["error"] = failure
	}
	data, err := json.Marshal(entry)
	require.NoError(t, err)
	return string(data)
}

// The decisions of the journal of a bot before it kept them are read back,
// their orders linked by their time.
func TestJournalDecisions(t *testing.T) {
	at := time.Date(2026, 9, 27, 9, 0, 3, 0, time.UTC)
	candle := time.Date(2026, 9, 27, 8, 45, 0, 0, time.UTC)
	group := int64(3)
	stop := 98.0
	orders := []model.Order{
		{ID: 1, Pair: testPair, Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
			Status: model.OrderStatusTypeFilled, CreatedAt: at.Add(2 * time.Second)},
		{ID: 2, Pair: testPair, Side: model.SideTypeSell, Type: model.OrderTypeLimitMaker, GroupID: &group,
			Status: model.OrderStatusTypeCanceled, CreatedAt: at.Add(2 * time.Second)},
		{ID: 3, Pair: testPair, Side: model.SideTypeSell, Type: model.OrderTypeStopLoss, GroupID: &group, Stop: &stop,
			Status: model.OrderStatusTypeCanceled, CreatedAt: at.Add(2 * time.Second)},
		{ID: 4, Pair: testPair, Side: model.SideTypeSell, Type: model.OrderTypeMarket,
			Status: model.OrderStatusTypeFilled, CreatedAt: at.Add(30*time.Minute + time.Second)},
		{ID: 5, Pair: testPair, Side: model.SideTypeBuy, Type: model.OrderTypeMarket, // a manual order, later
			Status: model.OrderStatusTypeFilled, CreatedAt: at.Add(3 * time.Hour)},
	}
	lines := []string{
		journalLine(t, at, candle, "", &Decision{Action: ActionEnterLong, Confidence: 0.8, Entry: ptr(100),
			StopLoss: ptr(98), TakeProfit: ptr(104), Reason: "the support held",
			Levels: []Level{{Kind: LevelSupport, Price: 98}}}, ""),
		journalLine(t, at.Add(15*time.Minute), candle.Add(15*time.Minute), "LONG",
			&Decision{Action: ActionHold, Confidence: 0.7, Reason: "the trend holds"}, ""),
		journalLine(t, at.Add(30*time.Minute), candle.Add(30*time.Minute), "LONG",
			&Decision{Action: ActionExit, Confidence: 0.75, Reason: "the resistance"}, ""),
		journalLine(t, at.Add(45*time.Minute), candle.Add(45*time.Minute), "", nil, "openai: HTTP 500"),
		journalLine(t, at.Add(time.Hour), candle.Add(time.Hour), "",
			&Decision{Action: ActionEnterShort, Confidence: 0.3, Reason: "weak"}, ""),
		"not JSON",
	}
	read := 0
	decisions := JournalDecisions([]byte(strings.Join(lines, "\n")+"\n"), func(pair string) []model.Order {
		read++
		require.Equal(t, testPair, pair)
		return orders
	})
	require.Len(t, decisions, 5)
	require.Equal(t, 1, read, "the orders of a pair read once")

	long, hold, exit, failed, short := decisions[0], decisions[1], decisions[2], decisions[3], decisions[4]
	require.Equal(t, TriggerCandle, hold.Trigger, "asked 3s after the close of its candle")
	require.Equal(t, "the support held", long.Reason)
	require.Equal(t, []int64{1, 2, 3}, long.OrderIDs, "the entry and its legs")
	require.Equal(t, []ui.Level{{Kind: "support", Price: 98}}, long.Levels)
	require.Equal(t, TriggerCandle, long.Trigger)
	require.Equal(t, candle.Unix(), long.Candle)
	require.Equal(t, "openai gpt-test", long.Model)
	require.Equal(t, decisionID(at, testPair), long.ID, "the ID of its time")
	require.Empty(t, hold.OrderIDs)
	require.Equal(t, []int64{4}, exit.OrderIDs, "the close of the long")
	require.Equal(t, "LONG", exit.Position)
	require.Equal(t, OutcomeFailed, failed.Outcome)
	require.Equal(t, "openai: HTTP 500", failed.Error)
	require.Equal(t, OutcomeIgnored, short.Outcome, "under the min confidence of its context")
	require.Empty(t, short.OrderIDs)
}

// testJournal is a Journal in memory, with the entries it was given.
type testJournal struct {
	entries []JournalEntry
}

func (j *testJournal) Append(entry JournalEntry) error {
	j.entries = append(j.entries, entry)
	return nil
}

// Recent reads the entries back as the store of the controller does: the
// valid decisions, the last perPair of each pair, oldest first.
func (j *testJournal) Recent(perPair int) ([]Remembered, error) {
	counts := make(map[string]int)
	var list []Remembered
	for i := len(j.entries) - 1; i >= 0; i-- {
		e := j.entries[i]
		if e.Decision == nil || e.Error != "" || counts[e.Input.Symbol] >= perPair {
			continue
		}
		counts[e.Input.Symbol]++
		list = append(list, Remembered{Pair: e.Input.Symbol, Candle: e.Input.Time, Action: e.Decision.Action,
			Confidence: e.Decision.Confidence, Reason: e.Decision.Reason})
	}
	slices.Reverse(list)
	return list, nil
}

// The journal keeps the decision refused beside its error, and the ID of the
// decision kept: the memory reads back the decisions applied only.
func TestTrader_JournalOfRefusals(t *testing.T) {
	journal := &testJournal{}
	r := newTraderRunner(t, nil, 0)
	require.NoError(t, r.trader.SetJournal(journal))
	refused := decide(ActionEnterLong, 0.8, ptr(100), ptr(101), ptr(104))
	refused.decision.Reason = "a breakout"
	r.step(100, refused)

	require.Len(t, journal.entries, 1)
	entry := journal.entries[0]
	require.Equal(t, r.lastDecision().ID, entry.ID)
	require.Equal(t, TriggerCandle, entry.Trigger)
	require.Equal(t, "a breakout", entry.Decision.Reason)
	require.Contains(t, entry.Error, "invalid decision")

	again := newTraderRunner(t, nil, 0)
	require.NoError(t, again.trader.SetJournal(journal))
	require.Empty(t, again.trader.remembered[testPair], "a refusal isn't remembered")
}

func TestLinkByTime(t *testing.T) {
	at := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	group := int64(9)
	orders := []model.Order{
		{ID: 10, Side: model.SideTypeSell, Type: model.OrderTypeStopLoss, GroupID: &group, CreatedAt: at.Add(time.Second)},
		{ID: 11, Side: model.SideTypeSell, Type: model.OrderTypeLimitMaker, GroupID: &group, CreatedAt: at.Add(time.Second)},
		{ID: 12, Side: model.SideTypeSell, Type: model.OrderTypeMarket, CreatedAt: at.Add(3 * time.Minute)},
	}
	require.Equal(t, []int64{10}, linkByTime(ActionModifySL, "LONG", at, orders), "the stop loss moved")
	require.Equal(t, []int64{11}, linkByTime(ActionModifyTP, "LONG", at, orders))
	require.Empty(t, linkByTime(ActionExit, "LONG", at, orders), "too late")
	require.Empty(t, linkByTime(ActionExit, "", at.Add(2*time.Minute), orders), "the side held unknown")
	require.Equal(t, []int64{12}, linkByTime(ActionExit, "LONG", at.Add(2*time.Minute), orders))
	require.Empty(t, linkByTime(ActionEnterShort, "", at, orders), "no entry")
}

func TestJournalTrigger(t *testing.T) {
	candle := time.Date(2026, 9, 27, 13, 15, 0, 0, time.UTC) // closed at 13:30
	require.Equal(t, TriggerCandle, journalTrigger(candle.Add(15*time.Minute+12*time.Second), candle, "15m"))
	require.Equal(t, TriggerStart, journalTrigger(candle.Add(28*time.Minute), candle, "15m"), "a restart at 13:43")
	require.Equal(t, TriggerCandle, journalTrigger(candle.Add(time.Hour), candle, "x"), "the period unknown")
}
