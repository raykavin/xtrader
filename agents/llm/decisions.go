package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

// The triggers of a decision, see ui.Decision: at the close of a candle, on
// the first candle after the start, asked again between the closes (see
// Trader.Reanalyze), or on the consult interval between the closes (see
// Config.ConsultInterval).
const (
	TriggerCandle     = "candle"
	TriggerStart      = "start"
	TriggerReanalysis = "reanalysis"
	TriggerInterval   = "interval"
)

// The outcomes of a decision, see ui.Decision: applied (WAIT and HOLD
// included), ignored under the min confidence, refused by the rules of its
// context, or failed without a decision; skipped, an analysis that didn't ask
// the model, its Reason the code of why (one of the Skip constants) and its
// Error the detail.
const (
	OutcomeApplied = "applied"
	OutcomeIgnored = "ignored"
	OutcomeRefused = "refused"
	OutcomeFailed  = "failed"
	OutcomeSkipped = "skipped"
)

// The codes of why an analysis didn't ask the model, see OutcomeSkipped.
const (
	SkipPosition     = "position"      // the position unread from the broker
	SkipCloseRetry   = "close_retry"   // a close that failed, being retried
	SkipProtection   = "protection"    // the broker can't place the legs with the order
	SkipDailyLimit   = "daily_limit"   // the entries refused by a daily limit
	SkipNews         = "news"          // the entries refused around a release
	SkipSize         = "size"          // no position fits the balance and the sizing
	SkipSpread       = "spread"        // the spread wider than allowed
	SkipSetup        = "setup"         // no trend for the setup
	SkipDirection    = "direction"     // no entry the direction of the positions allows
	SkipHigherTrend  = "higher_trend"  // no trend of the higher timeframes, one required
	SkipAgainstTrend = "against_trend" // the entries allowed all against the higher trend
	SkipRateLimited  = "rate_limited"  // the provider rate limited
)

// skip is why an analysis doesn't ask the model, see Trader.prepare: its code,
// one of the Skip constants, and its detail; none for the zero value.
type skip struct {
	code, detail string
}

// Note is a note the provider returned with a decision beside its
// justification: Kind text, words of the model beside the decision; reasoning,
// the summary of its reasoning; citation, an annotation of the API with its
// Title and URL; image, an image the answer links to, at URL.
type Note struct {
	Kind  string `json:"kind"`
	Text  string `json:"text,omitempty"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

// Attachment is a file the provider returned with a decision, eg: an image, its
// media type and its content, kept with it, see DecisionStore.
type Attachment struct {
	MediaType string
	Data      []byte
}

// The limits of the attachments kept of a decision, of their count, of the
// size of each and of their whole, which the records of the controller take:
// beyond them, a note tells of the files left out.
const (
	maxAttachments     = 8
	maxAttachmentSize  = 10 << 20
	maxAttachmentsSize = 40 << 20
)

// DecisionStore keeps the decisions of a Trader with their justification, see
// SetDecisionStore: in memory by default (see MemoryDecisions). Save is called
// in the flow of the strategy, the others by the handlers of the chart too:
// they must be safe for concurrent use.
type DecisionStore interface {
	// Save keeps d, attachments the content of d.Attachments by their index,
	// and the decision that placed each of its orders: the first one listing
	// it, the ones that move it later listing it among theirs only, as the
	// order controller keeps the ID of a leg moved.
	Save(d ui.Decision, attachments []Attachment) error

	// Decision returns the decision of id, false when unknown.
	Decision(id string) (ui.Decision, bool, error)

	// Decisions returns the last decisions of pair, of every pair when empty,
	// newest first, limit at most.
	Decisions(pair string, limit int) ([]ui.Decision, error)

	// OrderDecision returns the decision that placed the order, empty for
	// none. Called by the chart holding its lock, it must not block.
	OrderDecision(orderID int64) string

	// Attachment returns the content of the attachment index of the decision
	// id and its media type, ui.ErrUnknownDecision when there is none.
	Attachment(id string, index int) (mediaType string, data []byte, err error)
}

// MemoryDecisions is a DecisionStore in memory, the one of the Trader by
// default: the decisions of a backtest or of a paper wallet, or the cache of a
// store kept elsewhere (see Load), which then holds the last ones, capacity at
// most, with the decisions that placed the orders.
type MemoryDecisions struct {
	mu       sync.RWMutex
	capacity int                        // of decisions kept, 0 for all
	byID     map[string]*memoryDecision // the decisions kept
	sorted   []*memoryDecision          // the same, as they came: by decided_at, then by ID
	placed   map[int64]string           // the decision that placed each order
}

// memoryDecision is a decision of MemoryDecisions with its attachments, nil
// when their content is kept elsewhere.
type memoryDecision struct {
	decision    ui.Decision
	attachments []Attachment
}

// NewMemoryDecisions returns a store of the decisions in memory, holding the
// last capacity of them at most, all of them for 0.
func NewMemoryDecisions(capacity int) *MemoryDecisions {
	return &MemoryDecisions{capacity: max(capacity, 0), byID: make(map[string]*memoryDecision),
		placed: make(map[int64]string)}
}

// Save implements DecisionStore.
func (m *MemoryDecisions) Save(d ui.Decision, attachments []Attachment) error {
	if d.ID == "" {
		return errors.New("llm: a decision without an ID")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.put(d, slices.Clone(attachments))
	for _, order := range d.OrderIDs {
		if _, ok := m.placed[order]; !ok {
			m.placed[order] = d.ID
		}
	}
	return nil
}

// Load keeps decisions kept elsewhere, without the content of their
// attachments, and the decisions that placed the orders of placed, eg: the
// last ones read from the controller at the start, the ones saved since
// taking precedence.
func (m *MemoryDecisions) Load(decisions []ui.Decision, placed map[int64]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range decisions {
		if _, ok := m.byID[d.ID]; !ok && d.ID != "" {
			m.put(d, nil)
		}
	}
	for order, id := range placed {
		if _, ok := m.placed[order]; !ok {
			m.placed[order] = id
		}
	}
}

// put keeps d with its attachments in its place, dropping the oldest decision
// beyond the capacity.
func (m *MemoryDecisions) put(d ui.Decision, attachments []Attachment) {
	kept := &memoryDecision{decision: d, attachments: attachments}
	if old, ok := m.byID[d.ID]; ok {
		i := slices.Index(m.sorted, old)
		m.sorted = slices.Delete(m.sorted, i, i+1)
	}
	i := sort.Search(len(m.sorted), func(i int) bool { return before(d, m.sorted[i].decision) })
	m.sorted = slices.Insert(m.sorted, i, kept)
	m.byID[d.ID] = kept
	if m.capacity > 0 && len(m.sorted) > m.capacity {
		for _, dropped := range m.sorted[:len(m.sorted)-m.capacity] {
			delete(m.byID, dropped.decision.ID)
		}
		m.sorted = slices.Delete(m.sorted, 0, len(m.sorted)-m.capacity)
	}
}

// before reports whether the decision a came before b: by decided_at, then
// by ID, which starts with its time.
func before(a, b ui.Decision) bool {
	if a.DecidedAt != b.DecidedAt {
		return a.DecidedAt < b.DecidedAt
	}
	return a.ID < b.ID
}

// Decision implements DecisionStore.
func (m *MemoryDecisions) Decision(id string) (ui.Decision, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	kept, ok := m.byID[id]
	if !ok {
		return ui.Decision{}, false, nil
	}
	return kept.decision, true, nil
}

// Decisions implements DecisionStore.
func (m *MemoryDecisions) Decisions(pair string, limit int) ([]ui.Decision, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var list []ui.Decision
	for i := len(m.sorted) - 1; i >= 0 && len(list) < limit; i-- {
		if d := m.sorted[i].decision; pair == "" || d.Pair == pair {
			list = append(list, d)
		}
	}
	return list, nil
}

// OrderDecision implements DecisionStore.
func (m *MemoryDecisions) OrderDecision(orderID int64) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.placed[orderID]
}

// Attachment implements DecisionStore: ui.ErrUnknownDecision for the content
// of an attachment kept elsewhere, see Load.
func (m *MemoryDecisions) Attachment(id string, index int) (string, []byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	kept, ok := m.byID[id]
	if !ok || index < 0 || index >= len(kept.attachments) || index >= len(kept.decision.Attachments) {
		return "", nil, ui.ErrUnknownDecision
	}
	return kept.decision.Attachments[index].MediaType, kept.attachments[index].Data, nil
}

// withAttachments returns d with the summaries of the attachments it keeps
// among attachments, and their content, by index: the ones beyond the limits
// left out, told by a note.
func withAttachments(d ui.Decision, attachments []Attachment) (ui.Decision, []Attachment) {
	var kept []Attachment
	total := 0
	d.Attachments = []ui.DecisionAttachment{}
	d.Notes = slices.Clone(d.Notes)
	for i, attachment := range attachments {
		switch {
		case len(kept) >= maxAttachments || len(attachment.Data) > maxAttachmentSize ||
			total+len(attachment.Data) > maxAttachmentsSize:
			d.Notes = append(d.Notes, ui.DecisionNote{Kind: "text",
				Text: fmt.Sprintf("attachment %d left out: %s of %d bytes", i, attachment.MediaType, len(attachment.Data))})
			continue
		case len(attachment.Data) == 0:
			continue
		}
		d.Attachments = append(d.Attachments, ui.DecisionAttachment{Index: len(kept),
			MediaType: attachment.MediaType, Size: len(attachment.Data)})
		kept = append(kept, attachment)
		total += len(attachment.Data)
	}
	return d, kept
}

// decisionID returns the ID of a decision of pair at: its time, to the
// microsecond, then its pair, so that the IDs sort as the decisions came.
func decisionID(at time.Time, pair string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return '-'
	}, pair)
	return at.UTC().Format("20060102T150405.000000") + "-" + clean
}

// uiLevels returns the levels of a decision in their API form.
func uiLevels(levels []Level) []ui.Level {
	list := make([]ui.Level, 0, len(levels))
	for _, level := range levels {
		list = append(list, ui.Level{Kind: level.Kind, Price: level.Price})
	}
	return list
}

// uiNotes returns the notes of a decision in their API form.
func uiNotes(notes []Note) []ui.DecisionNote {
	list := make([]ui.DecisionNote, 0, len(notes))
	for _, note := range notes {
		list = append(list, ui.DecisionNote{Kind: note.Kind, Text: note.Text, Title: note.Title, URL: note.URL})
	}
	return list
}

// JournalDecisions returns the decisions of the lines of a journal, oldest
// first: the decisions of a bot of the versions before they were kept apart,
// each with the orders of its pair (of orders, read once a pair) it placed or
// moved, found by their time (see linkByTime), and its trigger told by its
// time when the entry has none (see journalTrigger). The lines of other
// versions are left out.
func JournalDecisions(data []byte, orders func(pair string) []model.Order) []ui.Decision {
	type imported struct {
		decision ui.Decision
		at       time.Time
		held     string
	}
	var decisions []imported
	for _, line := range strings.Split(string(data), "\n") {
		var entry struct {
			At        time.Time `json:"at"`
			Model     string    `json:"model"`
			LatencyMS int64     `json:"latency_ms"`
			ID        string    `json:"id"`
			Trigger   string    `json:"trigger"`
			Requested string    `json:"requested_by"`
			Input     struct {
				Symbol    string    `json:"symbol"`
				Timeframe string    `json:"timeframe"`
				Time      time.Time `json:"time"`
				Note      string    `json:"reanalysis_note"`
				Price     *float64  `json:"price"`
				Position  *struct {
					Side string `json:"side"`
				} `json:"position"`
				Rules struct {
					MinConfidence *float64 `json:"min_confidence"`
				} `json:"rules"`
			} `json:"input"`
			Decision *Decision `json:"decision"`
			Error    string    `json:"error"`
		}
		if strings.TrimSpace(line) == "" || json.Unmarshal([]byte(line), &entry) != nil ||
			entry.Input.Symbol == "" || entry.At.IsZero() {
			continue
		}
		d := ui.Decision{ID: entry.ID, Pair: entry.Input.Symbol, Timeframe: entry.Input.Timeframe,
			Candle: entry.Input.Time.Unix(), DecidedAt: entry.At.Unix(), Trigger: entry.Trigger, Model: entry.Model,
			LatencyMS: entry.LatencyMS, Levels: []ui.Level{}, Notes: []ui.DecisionNote{},
			Attachments: []ui.DecisionAttachment{}, OrderIDs: []int64{}, Error: entry.Error,
			RequestedBy: entry.Requested, RequestNote: entry.Input.Note}
		if d.ID == "" {
			d.ID = decisionID(entry.At, entry.Input.Symbol)
		}
		if d.Trigger == "" {
			d.Trigger = journalTrigger(entry.At, entry.Input.Time, entry.Input.Timeframe)
		}
		if entry.Input.Price != nil {
			d.Price = *entry.Input.Price
		}
		if entry.Input.Position != nil {
			d.Position = entry.Input.Position.Side
		}
		switch {
		case entry.Decision != nil:
			d.Action, d.Confidence, d.Reason = string(entry.Decision.Action), entry.Decision.Confidence,
				entry.Decision.Reason
			d.Entry, d.StopLoss, d.TakeProfit = entry.Decision.Entry, entry.Decision.StopLoss, entry.Decision.TakeProfit
			d.Levels, d.Notes = uiLevels(entry.Decision.Levels), uiNotes(entry.Decision.Notes)
			d.Outcome = OutcomeApplied
			if entry.Error != "" {
				d.Outcome = OutcomeRefused
			} else if minimum := entry.Input.Rules.MinConfidence; entry.Decision.Action.trades() && minimum != nil &&
				entry.Decision.Confidence < *minimum {
				d.Outcome = OutcomeIgnored
			}
		default:
			d.Outcome = OutcomeFailed
		}
		decisions = append(decisions, imported{decision: d, at: entry.At, held: d.Position})
	}

	// the orders of each pair, placed or moved by the decisions applied
	byPair := make(map[string][]model.Order)
	for _, d := range decisions {
		if _, ok := byPair[d.decision.Pair]; ok || orders == nil {
			continue
		}
		byPair[d.decision.Pair] = orders(d.decision.Pair)
	}
	list := make([]ui.Decision, 0, len(decisions))
	for i := range decisions {
		d := decisions[i].decision
		if d.Outcome == OutcomeApplied && Action(d.Action).trades() {
			d.OrderIDs = linkByTime(Action(d.Action), decisions[i].held, decisions[i].at, byPair[d.Pair])
		}
		list = append(list, d)
	}
	return list
}

// startDelay is how long after the close of its candle a decision of the
// journal without its trigger is taken for the one of a start, see
// journalTrigger: the model is asked within seconds of the close.
const startDelay = 2 * time.Minute

// journalTrigger returns the trigger of a decision of the journal of the
// version before it kept one, asked at at about the candle of candle: the one
// of a start when asked well after the close of the candle, else the one of
// its close.
func journalTrigger(at, candle time.Time, timeframe string) string {
	period, err := timeframeDuration(timeframe)
	if err == nil && at.Sub(candle.Add(period)) > startDelay {
		return TriggerStart
	}
	return TriggerCandle
}

// importWindow is how long after a decision of the journal its orders may be
// created: the Trader places them right after it, the broker answering within
// seconds.
const importWindow = 2 * time.Minute

// linkByTime returns the orders of a decision of the journal, taken at at with
// held the side of the position then, among orders: the ones created within
// importWindow after it that the action places, the entry of its side and the
// legs placed with it for an entry, the order at the market on the other side
// for an exit, the leg moved for a MODIFY.
func linkByTime(action Action, held string, at time.Time, orders []model.Order) []int64 {
	var ids []int64
	var entry *model.Order
	for i := range orders {
		order := &orders[i]
		created := order.CreatedAt
		if created.Before(at.Add(-time.Second)) || created.After(at.Add(importWindow)) {
			continue
		}
		isStop := order.Type == model.OrderTypeStopLoss || order.Type == model.OrderTypeStopLossLimit
		switch action {
		case ActionEnterLong, ActionEnterShort:
			if order.GroupID == nil && order.Side == action.side() && order.Type == model.OrderTypeMarket &&
				(entry == nil || order.ID < entry.ID) {
				entry = order
			}
		case ActionExit:
			if held != "" && order.GroupID == nil && order.Type == model.OrderTypeMarket &&
				order.Side == oppositeSide(sideOfHeld(held)) {
				return []int64{order.ID}
			}
		case ActionModifySL, ActionModifyTP:
			if order.GroupID != nil && isStop == (action == ActionModifySL) {
				ids = append(ids, order.ID)
			}
		}
	}
	if entry == nil {
		return ids
	}
	ids = []int64{entry.ID}
	for _, order := range orders {
		// the legs of the entry: grouped, on the other side, placed with it
		if order.GroupID != nil && order.Side != entry.Side && order.ID > entry.ID &&
			!order.CreatedAt.After(entry.CreatedAt.Add(5*time.Second)) && !order.CreatedAt.Before(entry.CreatedAt) {
			ids = append(ids, order.ID)
		}
	}
	return ids
}

// sideOfHeld returns the side of a position of the context, LONG or SHORT.
func sideOfHeld(held string) model.SideType {
	switch held {
	case "LONG":
		return model.SideTypeBuy
	case "SHORT":
		return model.SideTypeSell
	}
	return ""
}

// SetDecisionStore sets where the Trader keeps its decisions with their
// justification, in memory by default, eg: the store of the controller: each
// decision whether or not it traded, and the orders it placed or moved. Before
// the bot runs.
func (t *Trader) SetDecisionStore(store DecisionStore) {
	t.decisionsMtx.Lock()
	defer t.decisionsMtx.Unlock()
	t.decisions = store
}

// store returns the store of the decisions.
func (t *Trader) store() DecisionStore {
	t.decisionsMtx.RLock()
	defer t.decisionsMtx.RUnlock()
	return t.decisions
}

// LLM names the model of the Trader, eg: openai gpt-5-mini. It implements
// strategy.LLMStrategy: the application offers the justifications of its
// decisions and their reanalysis.
func (t *Trader) LLM() string {
	return t.name
}

// Decisions returns the last decisions of pair, of every pair when empty,
// newest first, limit at most. It implements ui.DecisionSource, as the methods
// below.
func (t *Trader) Decisions(pair string, limit int) ([]ui.Decision, error) {
	return t.store().Decisions(pair, limit)
}

// Decision returns the decision of id, false when unknown.
func (t *Trader) Decision(id string) (ui.Decision, bool, error) {
	return t.store().Decision(id)
}

// OrderDecision returns the decision that placed the order, empty for none:
// the leg of a position keeps the decision of its entry when a later one moves
// it.
func (t *Trader) OrderDecision(orderID int64) string {
	return t.store().OrderDecision(orderID)
}

// Attachment returns the content of the attachment index of the decision id,
// and its media type.
func (t *Trader) Attachment(id string, index int) (string, []byte, error) {
	return t.store().Attachment(id, index)
}

// ListenDecisions calls listener with every decision kept from then on, eg:
// ui.Chart, which sends them to the dashboards.
func (t *Trader) ListenDecisions(listener func(ui.Decision)) {
	t.decisionsMtx.Lock()
	defer t.decisionsMtx.Unlock()
	t.listeners = append(t.listeners, listener)
}

// keep keeps the decision with its attachments, and hands it to the
// listeners: the record of the skips it repeats, see grouped. The records of a
// pair are grouped, saved and told one at a time, in the same order: a record
// kept again in its place ends with its last state, never an older one.
func (t *Trader) keep(d ui.Decision, attachments []Attachment) {
	if d.ID == "" {
		return // no record
	}
	keeping := t.keeping(d.Pair)
	keeping.Lock()
	defer keeping.Unlock()
	d = t.grouped(d)
	d, attachments = withAttachments(d, attachments)
	if err := t.store().Save(d, attachments); err != nil {
		log.Warnf("[LLM] %s | the decision %s: %v", d.Pair, d.ID, err)
		return
	}
	t.decisionsMtx.RLock()
	listeners := slices.Clone(t.listeners)
	t.decisionsMtx.RUnlock()
	for _, listener := range listeners {
		listener(d)
	}
}

// keeping returns the lock of the records of pair, see keep: the listeners
// must not keep a decision.
func (t *Trader) keeping(pair string) *sync.Mutex {
	t.skipsMtx.Lock()
	defer t.skipsMtx.Unlock()
	if t.keepMtx == nil {
		t.keepMtx = make(map[string]*sync.Mutex)
	}
	mtx, ok := t.keepMtx[pair]
	if !ok {
		mtx = new(sync.Mutex)
		t.keepMtx[pair] = mtx
	}
	return mtx
}

// skipRun is the analyses of a pair skipped in a row for the same reason,
// kept as one record, see grouped: the first one, the triggers of all of them,
// and the last one.
type skipRun struct {
	key      string
	first    ui.Decision
	triggers map[string]int
	count    int
	last     ui.Decision
}

// grouped returns the record to keep of d: d itself, or the record of the
// skips it repeats. The analyses of a pair at the close of a candle and on the
// consult interval skipped for the same reason, with the same position and
// prompt, since any other record of the pair, are kept as their first record,
// eg: the entries blocked around a release, one record rather than one every
// few seconds: of the same ID, kept again in its place with a note of how many
// they are, of their triggers, and of the last one, its time, its price and its
// detail when another. The start and the reanalyses are kept apart, as any
// decision.
func (t *Trader) grouped(d ui.Decision) ui.Decision {
	t.skipsMtx.Lock()
	defer t.skipsMtx.Unlock()
	if d.Outcome != OutcomeSkipped || d.RequestedBy != "" ||
		d.Trigger != TriggerCandle && d.Trigger != TriggerInterval {
		delete(t.skips, d.Pair)
		return d
	}
	key := strings.Join([]string{d.Reason, d.Position, d.Timeframe, describeRecordPrompt(d.Prompt)}, "\x00")
	run := t.skips[d.Pair]
	if run == nil || run.key != key {
		t.skips[d.Pair] = &skipRun{key: key, first: d, triggers: map[string]int{d.Trigger: 1}, count: 1, last: d}
		return d
	}
	run.count++
	run.triggers[d.Trigger]++
	run.last = d
	record := run.first
	record.Notes = append(slices.Clone(run.first.Notes), ui.DecisionNote{Kind: "text", Text: run.describe()})
	return record
}

// describe tells of the skips of the run, eg: skipped 41 times in a row for
// the same reason (candle 1, interval 40), the last at 2026-10-07 10:57:03 UTC,
// at the price of 4154.25.
func (r *skipRun) describe() string {
	triggers := make([]string, 0, len(r.triggers))
	for trigger, count := range r.triggers {
		triggers = append(triggers, fmt.Sprintf("%s %d", trigger, count))
	}
	sort.Strings(triggers)
	text := fmt.Sprintf("skipped %d times in a row for the same reason (%s), the last at %s, at the price of %s",
		r.count, strings.Join(triggers, ", "), time.Unix(r.last.DecidedAt, 0).UTC().Format("2006-01-02 15:04:05 UTC"),
		strconv.FormatFloat(r.last.Price, 'f', -1, 64))
	if r.last.Error != r.first.Error {
		text += ": " + r.last.Error
	}
	return text
}

// describeRecordPrompt names the prompt of a record, empty for none.
func describeRecordPrompt(p *ui.DecisionPrompt) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%d/%s/%d", p.ID, p.Name, p.Version)
}

// decisionRecord returns the record of the decision asked with input at at,
// see ui.Decision: refused when err is a decision breaking the rules, failed
// for any other error, its outcome left to apply otherwise.
// skipRecord returns the record of the analysis of the closed candles of df,
// at price, that didn't ask the model for s (see OutcomeSkipped), trigger
// telling why it ran, requested the reanalysis it answers, if any, and tr the
// position followed, if any.
func (t *Trader) skipRecord(df *model.Dataframe, st *promptState, trigger string, requested *reanalysis,
	price float64, tr *trade, s skip) ui.Decision {

	at := t.now()
	record := ui.Decision{ID: decisionID(at, df.Pair), Pair: df.Pair, Timeframe: t.cfg.Timeframe,
		DecidedAt: at.Unix(), Trigger: trigger, Prompt: st.decisionPrompt(), Price: price, Reason: s.code,
		Outcome: OutcomeSkipped, Error: s.detail, Levels: []ui.Level{}, Notes: []ui.DecisionNote{},
		Attachments: []ui.DecisionAttachment{}, OrderIDs: []int64{}}
	if len(df.Time) > 0 {
		record.Candle = df.Time[len(df.Time)-1].Unix()
	}
	if requested != nil {
		record.RequestedBy, record.RequestNote = string(requested.channel), requested.note
	}
	if tr != nil {
		record.Position = describeSide(tr.side)
	}
	return record
}

func (t *Trader) decisionRecord(st *promptState, input Input, trigger, requestedBy string, at time.Time,
	latency time.Duration, d Decision, err error) ui.Decision {

	record := ui.Decision{ID: decisionID(at, input.Symbol), Pair: input.Symbol, Timeframe: input.Timeframe,
		Candle: input.Time.Unix(), DecidedAt: at.Unix(), Trigger: trigger, RequestedBy: requestedBy,
		RequestNote: input.ReanalysisNote, Model: t.name, Prompt: st.decisionPrompt(),
		LatencyMS: latency.Milliseconds(), Price: float64(input.Price), Levels: []ui.Level{},
		Notes: []ui.DecisionNote{}, Attachments: []ui.DecisionAttachment{}, OrderIDs: []int64{}}
	if input.Position != nil {
		record.Position = describeSide(input.Position.Side)
	}
	if d.Action != "" {
		record.Action, record.Confidence, record.Reason = string(d.Action), d.Confidence, d.Reason
		record.Entry, record.StopLoss, record.TakeProfit = d.Entry, d.StopLoss, d.TakeProfit
		record.Levels, record.Notes = uiLevels(d.Levels), uiNotes(d.Notes)
	}
	switch {
	case err == nil:
	case d.Action != "" && errors.Is(err, ErrInvalidDecision):
		record.Outcome, record.Error = OutcomeRefused, err.Error()
	default:
		record.Outcome, record.Error = OutcomeFailed, err.Error()
		record.Action, record.Confidence, record.Reason = "", 0, ""
	}
	return record
}
