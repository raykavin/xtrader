package exchange

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/tools/log"
)

// This file owns the account and the orders of the MT5 adapter: the book of
// the account, polled from the bridge, the wallet, the netting scoped to the
// bot's magic number and every order method.
//
// The book. The order controller asks Order/OrderSince every second for every
// pending order, so they answer from the book, never with a request per call.
// The book holds every open position and pending order of the account, the
// positions seen open and gone since, and the history of the traded symbols:
// the deals (entries and exits of the positions) and the orders out of the
// book with their final state. It is fed by:
//   - the poll, every poll interval (and at once after a trade request that
//     failed): GET /orders, then GET /positions (in that order, so a pending
//     order filled between the two shows its position), between two GET
//     /account (a snapshot of another account is not applied, see
//     checkAccount), then the history (GET /deals and /history_orders) since
//     the last read, when a position or a pending order of the bot left the
//     book, until their outcome shows (mt5ResolveWait at most); a read failing
//     applies nothing;
//   - the answers of the trade requests, applied before the methods return, so
//     that Position and Order see them at once: the position opened with its
//     entry deal, the volume closed with its exit deal, the pending order
//     placed, moved or removed, the levels set. The deals of the answers carry
//     their real tickets, so the history replaces them.
//
// A snapshot replaces the entries of the book but the ones an answer changed
// after its first request started (touched), which are newer.
//
// Ownership: a position or a pending order is the bot's when its magic is the
// bot's (WithMT5Magic); the exits are linked to their positions by
// position_id. The ticket of a position is the one of the order that opened
// it, so a pending order, the position it opens and the legs protecting it
// share that ticket.
//
// ExchangeIDs:
//   - a pending order, or a market order that opened a position: its ticket;
//   - a market order that only closed positions: the ticket of its first
//     closing order;
//   - the legs of the protection of the position opened by ticket T, pending
//     or open: mt5LegID(tp|sl, T, G), in the group mt5GroupID(T, G), G the
//     number of the protection of T: 0, but for a position protected again by
//     CreateOrderOCO after its protection was removed. They are derived, so
//     the legs stored by a bot are found after it restarts.
//
// Netting, scoped to the bot's positions of a pair, under lockPair:
//   - a market order against the bot's net first closes its opposite positions
//     first in, first out (time_msc, then ticket), the last one partially if
//     needed (TRADE_ACTION_DEAL with position), and only the rest opens a
//     position. It is FILLED on return at the average price of its fills;
//   - a pending order filled against the net opens a position on the other
//     side: settlePair nets it with TRADE_ACTION_CLOSE_BY against the oldest
//     opposite position, after every poll finding both sides and on startup;
//   - the protected entries (and OCO orders) must open or add to the net:
//     against it they are refused with ErrNotSupported, as their levels would
//     protect nothing.

// mt5Position is an open position of GET /positions.
type mt5Position struct {
	Ticket       int64   `json:"ticket"`
	Identifier   int64   `json:"identifier"`
	Time         int64   `json:"time"`
	TimeMsc      int64   `json:"time_msc"`
	Type         int     `json:"type"`
	Magic        int64   `json:"magic"`
	Reason       int     `json:"reason"`
	Volume       float64 `json:"volume"`
	PriceOpen    float64 `json:"price_open"`
	SL           float64 `json:"sl"`
	TP           float64 `json:"tp"`
	PriceCurrent float64 `json:"price_current"`
	Swap         float64 `json:"swap"`
	Profit       float64 `json:"profit"`
	Symbol       string  `json:"symbol"`
	Comment      string  `json:"comment"`
}

// id is the identifier of the position, the ticket of the order that opened
// it.
func (p mt5Position) id() int64 {
	if p.Identifier != 0 {
		return p.Identifier
	}
	return p.Ticket
}

func (p mt5Position) opened() time.Time {
	if p.TimeMsc > 0 {
		return mt5Time(p.TimeMsc)
	}
	return time.Unix(p.Time, 0).UTC()
}

// mt5Order is a pending order of GET /orders, or an order of GET
// /history_orders.
type mt5Order struct {
	Ticket         int64   `json:"ticket"`
	TimeSetup      int64   `json:"time_setup"`
	TimeSetupMsc   int64   `json:"time_setup_msc"`
	TimeDone       int64   `json:"time_done"`
	TimeDoneMsc    int64   `json:"time_done_msc"`
	TimeExpiration int64   `json:"time_expiration"`
	Type           int     `json:"type"`
	TypeTime       int     `json:"type_time"`
	TypeFilling    int     `json:"type_filling"`
	State          int     `json:"state"`
	Magic          int64   `json:"magic"`
	PositionID     int64   `json:"position_id"`
	PositionByID   int64   `json:"position_by_id"`
	Reason         int     `json:"reason"`
	VolumeInitial  float64 `json:"volume_initial"`
	VolumeCurrent  float64 `json:"volume_current"`
	PriceOpen      float64 `json:"price_open"`
	SL             float64 `json:"sl"`
	TP             float64 `json:"tp"`
	PriceCurrent   float64 `json:"price_current"`
	Symbol         string  `json:"symbol"`
	Comment        string  `json:"comment"`
}

func (o mt5Order) setup() time.Time {
	if o.TimeSetupMsc > 0 {
		return mt5Time(o.TimeSetupMsc)
	}
	return time.Unix(o.TimeSetup, 0).UTC()
}

func (o mt5Order) done() time.Time {
	switch {
	case o.TimeDoneMsc > 0:
		return mt5Time(o.TimeDoneMsc)
	case o.TimeDone > 0:
		return time.Unix(o.TimeDone, 0).UTC()
	}
	return time.Time{}
}

// mt5Deal is a deal of GET /deals.
type mt5Deal struct {
	Ticket     int64   `json:"ticket"`
	Order      int64   `json:"order"`
	Time       int64   `json:"time"`
	TimeMsc    int64   `json:"time_msc"`
	Type       int     `json:"type"`
	Entry      int     `json:"entry"`
	Magic      int64   `json:"magic"`
	PositionID int64   `json:"position_id"`
	Reason     int     `json:"reason"`
	Volume     float64 `json:"volume"`
	Price      float64 `json:"price"`
	Commission float64 `json:"commission"`
	Swap       float64 `json:"swap"`
	Profit     float64 `json:"profit"`
	Fee        float64 `json:"fee"`
	Symbol     string  `json:"symbol"`
	Comment    string  `json:"comment"`

	// provisional is a deal recorded from the answer of a trade, before the
	// history lists it: its profit, swap, commission and fee unknown, zero,
	// until the one of the history replaces it (see recordDeal).
	provisional bool
}

func (d mt5Deal) at() time.Time {
	if d.TimeMsc > 0 {
		return mt5Time(d.TimeMsc)
	}
	return time.Unix(d.Time, 0).UTC()
}

// exit reports whether the deal closes (part of) its position.
func (d mt5Deal) exit() bool {
	return d.Entry == mt5EntryOut || d.Entry == mt5EntryOutBy
}

// mt5Result is the answer of POST /order (order_send).
type mt5Result struct {
	Retcode int     `json:"retcode"`
	Deal    int64   `json:"deal"`
	Order   int64   `json:"order"`
	Volume  float64 `json:"volume"`
	Price   float64 `json:"price"`
	Bid     float64 `json:"bid"`
	Ask     float64 `json:"ask"`
	Comment string  `json:"comment"`
}

// mt5Final is a pending order out of the book as the adapter learned it: its
// state (filled or canceled) and when.
type mt5Final struct {
	order mt5Order
	state int
	at    time.Time
}

// mt5Gone is a position or a pending order gone from the book whose outcome is
// awaited in the history: when it left, and the setup of a pending order.
type mt5Gone struct {
	at    time.Time
	setup time.Time
}

// mt5Known is an order the adapter created, moved or canceled, or rebuilt for
// a previous run.
type mt5Known struct {
	ticket     int64   // the pending order, the position of a leg (its anchor), the closing order
	tickets    []int64 // the positions protected by the legs of an OCO order, the anchor first
	leg        string  // mt5TakeProfit or mt5StopLoss for a leg
	generation int     // of the protection of a leg, see mt5LegID
	order      model.Order
	canceled   bool // by Cancel
}

// protected returns the positions the legs protect.
func (k mt5Known) protected() []int64 {
	if len(k.tickets) > 0 {
		return k.tickets
	}
	return []int64{k.ticket}
}

// mt5Book is the state of the account and of the orders.
type mt5Book struct {
	mtx        sync.RWMutex
	positions  map[int64]mt5Position // open positions of the account, by identifier
	orders     map[int64]mt5Order    // pending orders of the account, by ticket
	closed     map[int64]mt5Position // positions seen open and gone since, by identifier
	gone       map[int64]mt5Gone     // bot's positions and pending orders gone, outcome unknown yet, by ticket
	finals     map[int64]mt5Final    // pending orders out of the book as the adapter learned it, by ticket
	fills      map[int64]bool        // positions opened by the fill of a pending order, by identifier
	deals      map[int64]mt5Deal     // deals of the traded symbols, by ticket
	entries    map[int64]mt5Deal     // entry deal of each position, by position_id
	exits      map[int64][]mt5Deal   // exit deals of each position, oldest first, by position_id
	awaited    map[int64]time.Time   // provisional exit deals, their profit awaited from the history, by ticket
	byOrder    map[int64][]mt5Deal   // deals by the ticket of their order
	history    map[int64]mt5Order    // orders of the history, by ticket
	touched    map[int64]time.Time   // last answer applied, by ticket, see applySnapshot
	snapshotAt time.Time             // start of the last snapshot applied
	account    mt5Account
	accountAt  time.Time // of the account read, zero to read it again
	switched   error     // the account last read is not the bot's, see checkAccount

	pollMtx  sync.Mutex // one poll at a time
	pruned   time.Time  // of the last prune, under pollMtx
	pollNext chan struct{}

	historyMtx  sync.Mutex // one history read at a time
	historyFrom time.Time  // range of the history read
	historyTo   time.Time

	knownMtx    sync.Mutex
	known       map[int64]mt5Known // by ExchangeID
	generations map[int64]int      // protection in use of each ticket, see mt5LegID

	pairsMtx  sync.Mutex
	pairs     map[string]*sync.Mutex  // serialize the trading of each pair, see lockPair
	unsettled map[string]mt5Unsettled // the trade request of an unknown outcome of each pair, see unsettle

	settle chan string // pairs to net, see settler

	settleMtx    sync.Mutex
	settleFailed map[string]time.Time // last failure of the netting of each pair, see settleBackoff
	settleErrs   map[string]string    // last error of the netting of each pair, logged once
}

// mt5Unsettled is a trade request whose answer was lost, its outcome unknown,
// the account unreadable since, see unsettle.
type mt5Unsettled struct {
	request map[string]any
	sent    time.Time
}

func newMT5Book() *mt5Book {
	return &mt5Book{
		positions:    make(map[int64]mt5Position),
		orders:       make(map[int64]mt5Order),
		closed:       make(map[int64]mt5Position),
		gone:         make(map[int64]mt5Gone),
		finals:       make(map[int64]mt5Final),
		fills:        make(map[int64]bool),
		deals:        make(map[int64]mt5Deal),
		entries:      make(map[int64]mt5Deal),
		exits:        make(map[int64][]mt5Deal),
		awaited:      make(map[int64]time.Time),
		byOrder:      make(map[int64][]mt5Deal),
		history:      make(map[int64]mt5Order),
		touched:      make(map[int64]time.Time),
		pollNext:     make(chan struct{}, 1),
		known:        make(map[int64]mt5Known),
		generations:  make(map[int64]int),
		pairs:        make(map[string]*sync.Mutex),
		unsettled:    make(map[string]mt5Unsettled),
		settle:       make(chan string, 64),
		settleFailed: make(map[string]time.Time),
		settleErrs:   make(map[string]string),
	}
}

// startTrading starts the book for NewMT5: a first poll with the history of
// mt5HistoryLookback, synchronous, whose error is returned; the netting of the
// pairs holding both sides (fills of pending orders while the bot was off);
// then, until ctx is done, the poll loop and the settler.
func (m *MT5) startTrading(ctx context.Context) error {
	m.book = newMT5Book()
	if err := m.poll(ctx); err != nil {
		return fmt.Errorf("mt5: reading the account %d: %w", m.login, err)
	}
	for _, symbol := range m.symbols {
		if err := m.settlePair(ctx, symbol); err != nil {
			log.Errorf("[MT5] %v", err)
		}
	}
	go m.pollLoop(ctx)
	go m.settler(ctx)
	return nil
}

// pollLoop polls every poll interval, and at once when asked (requestPoll),
// until ctx is done; an error is logged once, and when it ends.
func (m *MT5) pollLoop(ctx context.Context) {
	timer := time.NewTimer(m.pollInterval)
	defer timer.Stop()
	var failure string
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-m.book.pollNext:
		}
		err := m.poll(ctx)
		timer.Reset(m.pollInterval)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil && err.Error() != failure:
			failure = err.Error()
			log.Warnf("[MT5] polling the account: %v", err)
		case err == nil && failure != "":
			failure = ""
			log.Info("[MT5] the account is polled again")
		}
	}
}

// requestPoll asks the poll loop for a poll now, without blocking.
func (m *MT5) requestPoll() {
	select {
	case m.book.pollNext <- struct{}{}:
	default:
	}
}

// poll reads a snapshot of the account (GET /account, /orders, then
// /positions, then /account again) and applies it (applySnapshot), reads the
// history when something of the bot left the book or is still unresolved
// (readHistory), then queues the netting of the pairs where the bot holds
// both sides. One at a time. A read failing applies nothing: an error is never
// taken for no positions. Nor is the snapshot of another account
// (checkAccount), which would close the bot's positions in the book: the
// account read before and after the lists, the terminal back on the bot's
// account between them never passes them for its own.
func (m *MT5) poll(ctx context.Context) error {
	b := m.book
	b.pollMtx.Lock()
	defer b.pollMtx.Unlock()

	if err := m.pollAccount(ctx); err != nil {
		return err
	}
	started := time.Now()
	var orders []mt5Order
	if err := m.get(ctx, "/orders", nil, &orders); err != nil {
		return fmt.Errorf("reading the pending orders: %w", err)
	}
	var positions []mt5Position
	if err := m.get(ctx, "/positions", nil, &positions); err != nil {
		return fmt.Errorf("reading the positions: %w", err)
	}
	if err := m.pollAccount(ctx); err != nil {
		return err
	}
	m.applySnapshot(started, positions, orders)

	b.historyMtx.Lock()
	first := b.historyTo.IsZero()
	from := b.historyTo.Add(-mt5HistoryOverlap)
	if first {
		from = started.Add(-mt5HistoryLookback)
	}
	b.historyMtx.Unlock()
	if waiting, since := m.unresolved(); first || waiting {
		// the orders of the history may be listed by their setup time: from the
		// setup of the pending orders gone
		if !since.IsZero() && since.Before(from) {
			from = since.Add(-mt5HistoryOverlap)
		}
		if err := m.readHistory(ctx, from, time.Now().Add(mt5HistoryAhead)); err != nil {
			return err
		}
	}

	for _, symbol := range m.symbols {
		if m.bothSides(symbol) && !m.settleBackoff(symbol) {
			m.queueSettle(symbol)
		}
	}
	if time.Since(b.pruned) >= mt5PruneInterval {
		m.prune(time.Now().Add(-mt5HistoryKept))
		b.pruned = time.Now()
	}
	return nil
}

// applySnapshot replaces the positions and the pending orders of the book with
// the ones read from started on, but for the entries touched by an answer
// after started. Positions gone move to closed; pending orders gone are
// filled when a position of their ticket is known, and otherwise wait for their
// outcome in the history (gone). A new position of a pending order is a fill,
// and one of the bot queues the netting of its pair.
func (m *MT5) applySnapshot(started time.Time, positions []mt5Position, orders []mt5Order) {
	b := m.book
	b.mtx.Lock()
	defer b.mtx.Unlock()

	now := time.Now()
	newer := func(id int64) bool {
		return b.touched[id].After(started)
	}

	listed := make(map[int64]bool, len(positions))
	for _, position := range positions {
		id := position.id()
		if id == 0 {
			continue
		}
		listed[id] = true
		if newer(id) {
			continue
		}
		if _, known := b.positions[id]; !known {
			if _, pending := b.orders[id]; pending {
				b.fills[id] = true
			}
		}
		b.positions[id] = position
		delete(b.closed, id)
	}
	for id, position := range b.positions {
		if listed[id] || newer(id) {
			continue
		}
		delete(b.positions, id)
		b.closed[id] = position
		if m.ours(position.Magic, position.Symbol) && !m.covered(id) {
			b.gone[id] = mt5Gone{at: now}
		}
	}

	pending := make(map[int64]bool, len(orders))
	for _, order := range orders {
		ticket := order.Ticket
		if ticket == 0 || !mt5PendingKind(order.Type) {
			continue
		}
		pending[ticket] = true
		if newer(ticket) {
			continue
		}
		if final, ok := b.finals[ticket]; ok && !final.at.Before(started) {
			continue // learned after the snapshot started
		}
		delete(b.finals, ticket)
		b.orders[ticket] = order
	}
	for ticket, order := range b.orders {
		if pending[ticket] || newer(ticket) {
			continue
		}
		delete(b.orders, ticket)
		_, open := b.positions[ticket]
		_, closed := b.closed[ticket]
		if open || closed {
			b.finals[ticket] = mt5Final{order: order, state: mt5StateFilled, at: now}
			b.fills[ticket] = true
			continue
		}
		if m.ours(order.Magic, order.Symbol) {
			b.gone[ticket] = mt5Gone{at: now, setup: order.setup()}
		}
	}

	b.snapshotAt = started
	for id, at := range b.touched {
		if !at.After(started) {
			delete(b.touched, id)
		}
	}
}

// ours reports whether an order or a position of that magic and symbol is the
// bot's.
func (m *MT5) ours(magic int64, symbol string) bool {
	_, traded := m.instruments[symbol]
	return traded && magic == m.magic
}

// unresolved reports whether a position or a pending order of the bot left the
// book less than mt5ResolveWait ago without an outcome in the history, with the
// earliest setup of the pending orders among them, and forgets the older ones.
func (m *MT5) unresolved() (waiting bool, since time.Time) {
	b := m.book
	b.mtx.Lock()
	defer b.mtx.Unlock()
	for ticket, at := range b.awaited {
		if time.Since(at) >= mt5ResolveWait {
			log.Warnf("[MT5] the deal %d isn't in the history since %s: its profit is unknown", ticket,
				at.Format(time.RFC3339))
			delete(b.awaited, ticket)
			continue
		}
		waiting = true
	}
	for ticket, gone := range b.gone {
		switch {
		case m.resolved(ticket):
			delete(b.gone, ticket)
		case time.Since(gone.at) >= mt5ResolveWait:
			log.Warnf("[MT5] no outcome in the history for the ticket %d, gone from the book since %s", ticket,
				gone.at.Format(time.RFC3339))
			delete(b.gone, ticket)
		default:
			waiting = true
			if !gone.setup.IsZero() && (since.IsZero() || gone.setup.Before(since)) {
				since = gone.setup
			}
		}
	}
	return waiting, since
}

// resolved reports whether the history shows what happened to the ticket gone:
// the exits of its position, or the final state of its pending order. The
// caller holds the lock of the book.
func (m *MT5) resolved(ticket int64) bool {
	b := m.book
	if _, open := b.positions[ticket]; open {
		return true // a pending order filled
	}
	if _, closed := b.closed[ticket]; closed {
		return m.covered(ticket)
	}
	if order, ok := b.history[ticket]; ok && order.State != mt5StatePlaced && order.State != mt5StateStarted {
		return true
	}
	_, entered := b.entries[ticket]
	return entered
}

// covered reports whether the exits recorded of the position id cover the
// volume it opened with, or any exit when its entry is older than the history.
// The caller holds the lock of the book.
func (m *MT5) covered(id int64) bool {
	b := m.book
	exits := b.exits[id]
	entry, ok := b.entries[id]
	if !ok {
		return len(exits) > 0
	}
	symbol := m.instrument(entry.Symbol)
	var closed int64
	for _, exit := range exits {
		closed += symbol.steps(exit.Volume)
	}
	return closed >= symbol.steps(entry.Volume)
}

// readHistory reads the deals and the orders of the history between from and
// to, records the ones of the traded symbols and widens the range read. One
// read at a time.
func (m *MT5) readHistory(ctx context.Context, from, to time.Time) error {
	b := m.book
	b.historyMtx.Lock()
	defer b.historyMtx.Unlock()
	return m.readHistoryLocked(ctx, from, to)
}

// readHistoryLocked is readHistory under historyMtx.
func (m *MT5) readHistoryLocked(ctx context.Context, from, to time.Time) error {
	query := url.Values{
		"from": {strconv.FormatInt(from.Unix(), 10)},
		"to":   {strconv.FormatInt(to.Unix(), 10)},
	}
	var deals []mt5Deal
	if err := m.get(ctx, "/deals", query, &deals); err != nil {
		return fmt.Errorf("reading the deals: %w", err)
	}
	var orders []mt5Order
	if err := m.get(ctx, "/history_orders", query, &orders); err != nil {
		return fmt.Errorf("reading the orders of the history: %w", err)
	}

	b := m.book
	b.mtx.Lock()
	for _, deal := range deals {
		if _, traded := m.instruments[deal.Symbol]; traded && deal.PositionID != 0 &&
			(deal.Type == mt5TypeBuy || deal.Type == mt5TypeSell) {
			m.recordDeal(deal)
		}
	}
	for _, order := range orders {
		if _, traded := m.instruments[order.Symbol]; traded && order.Ticket != 0 {
			b.history[order.Ticket] = order
		}
	}
	for ticket := range b.gone {
		if m.resolved(ticket) {
			delete(b.gone, ticket)
		}
	}
	b.mtx.Unlock()

	// the range read ends now: the records after it are still to come
	if now := time.Now(); to.After(now) {
		to = now
	}
	switch {
	case b.historyFrom.IsZero() && b.historyTo.IsZero():
		b.historyFrom, b.historyTo = from, to
	case from.After(b.historyTo) || to.Before(b.historyFrom):
		// apart from the range read: it would count the gap as read
	default:
		if from.Before(b.historyFrom) {
			b.historyFrom = from
		}
		if to.After(b.historyTo) {
			b.historyTo = to
		}
	}
	return nil
}

// ensureHistory makes sure the history since since was read: when since is
// before the range read, the range between them is read once. OrderSince calls
// it only for the orders the book can't answer, so it costs at most one read
// per range, not one per call.
func (m *MT5) ensureHistory(ctx context.Context, since time.Time) error {
	if since.IsZero() {
		return nil
	}
	since = since.Add(-mt5HistoryOverlap) // the clocks of the bot and of the server may differ a little
	b := m.book
	b.historyMtx.Lock()
	defer b.historyMtx.Unlock()
	to := b.historyFrom
	if to.IsZero() {
		to = time.Now().Add(mt5HistoryAhead)
	}
	if !since.Before(to) {
		return nil
	}
	return m.readHistoryLocked(ctx, since, to)
}

// recordDeal records a deal of the history or of an answer, replacing the one
// of the same ticket: but a deal of an answer, provisional, never replaces one
// of the history, and waits for it when it exits a position (see unresolved).
// The caller holds the lock of the book.
func (m *MT5) recordDeal(deal mt5Deal) {
	b := m.book
	if deal.Ticket != 0 {
		if known, ok := b.deals[deal.Ticket]; ok && deal.provisional && !known.provisional {
			return
		}
		if deal.provisional && deal.exit() {
			b.awaited[deal.Ticket] = time.Now()
		} else {
			delete(b.awaited, deal.Ticket)
		}
		if _, known := b.deals[deal.Ticket]; known {
			drop := func(deals []mt5Deal) []mt5Deal {
				return slices.DeleteFunc(deals, func(d mt5Deal) bool { return d.Ticket == deal.Ticket })
			}
			b.exits[deal.PositionID] = drop(b.exits[deal.PositionID])
			b.byOrder[deal.Order] = drop(b.byOrder[deal.Order])
		}
		b.deals[deal.Ticket] = deal
	}
	if deal.Order != 0 {
		b.byOrder[deal.Order] = append(b.byOrder[deal.Order], deal)
	}
	switch {
	case deal.Entry == mt5EntryIn:
		b.entries[deal.PositionID] = deal
	case deal.exit():
		exits := append(b.exits[deal.PositionID], deal)
		sort.SliceStable(exits, func(i, j int) bool { return exits[i].at().Before(exits[j].at()) })
		b.exits[deal.PositionID] = exits
	}
}

// prune forgets what the book learned before horizon of the positions closed
// and the orders out of the book, so that the book of a bot running for months
// stays small, and moves the start of the history read up to horizon:
// ensureHistory reads it again when an order older than that is asked.
func (m *MT5) prune(horizon time.Time) {
	b := m.book
	b.historyMtx.Lock()
	defer b.historyMtx.Unlock()
	if b.historyFrom.IsZero() || !b.historyFrom.Before(horizon) {
		return
	}
	b.mtx.Lock()
	defer b.mtx.Unlock()
	for id, exits := range b.exits {
		if _, open := b.positions[id]; open || len(exits) == 0 || !exits[len(exits)-1].at().Before(horizon) {
			continue
		}
		delete(b.exits, id)
		delete(b.entries, id)
		delete(b.closed, id)
		delete(b.fills, id)
	}
	for ticket, deal := range b.deals {
		if _, open := b.positions[deal.PositionID]; !open && deal.at().Before(horizon) {
			delete(b.deals, ticket)
			delete(b.byOrder, deal.Order)
		}
	}
	for ticket, order := range b.history {
		if done := order.done(); !done.IsZero() && done.Before(horizon) {
			delete(b.history, ticket)
		}
	}
	for ticket, final := range b.finals {
		if final.at.Before(horizon) {
			delete(b.finals, ticket)
		}
	}
	b.historyFrom = horizon
}

// lockPair serializes the trading of a pair, the netting of the market orders
// and settlePair, returning the unlock. Only the trading of the pair waits for
// it: the reads, the poll and the netting of the other pairs never do (see
// settler), as a trade request may hold it long, eg: its answer lost, looked
// for in the account (reconcile).
func (m *MT5) lockPair(pair string) func() {
	mtx := m.pairMutex(pair)
	mtx.Lock()
	return mtx.Unlock
}

// tryLockPair is lockPair unless the pair is locked already: then it reports
// false.
func (m *MT5) tryLockPair(pair string) (func(), bool) {
	mtx := m.pairMutex(pair)
	if !mtx.TryLock() {
		return nil, false
	}
	return mtx.Unlock, true
}

// pairMutex returns the mutex of lockPair of pair.
func (m *MT5) pairMutex(pair string) *sync.Mutex {
	b := m.book
	b.pairsMtx.Lock()
	defer b.pairsMtx.Unlock()
	mtx, ok := b.pairs[pair]
	if !ok {
		mtx = new(sync.Mutex)
		b.pairs[pair] = mtx
	}
	return mtx
}

// botPositions returns the bot's open positions of pair, first in first out:
// by time_msc, then ticket.
func (m *MT5) botPositions(pair string) []mt5Position {
	b := m.book
	b.mtx.RLock()
	defer b.mtx.RUnlock()
	var positions []mt5Position
	for _, position := range b.positions {
		if position.Symbol == pair && position.Magic == m.magic {
			positions = append(positions, position)
		}
	}
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].TimeMsc != positions[j].TimeMsc {
			return positions[i].TimeMsc < positions[j].TimeMsc
		}
		return positions[i].id() < positions[j].id()
	})
	return positions
}

// netSteps returns the net volume of the bot's positions of pair in volume
// steps, negative when short.
func (m *MT5) netSteps(pair string) int64 {
	symbol := m.instruments[pair]
	var net int64
	for _, position := range m.botPositions(pair) {
		steps := symbol.steps(position.Volume)
		if position.Type == mt5TypeSell {
			steps = -steps
		}
		net += steps
	}
	return net
}

// bothSides reports whether the bot holds positions of pair on both sides.
func (m *MT5) bothSides(pair string) bool {
	var buy, sell bool
	for _, position := range m.botPositions(pair) {
		if position.Type == mt5TypeSell {
			sell = true
		} else {
			buy = true
		}
	}
	return buy && sell
}

// queueSettle asks the settler to net pair, without blocking.
func (m *MT5) queueSettle(pair string) {
	select {
	case m.book.settle <- pair:
	default:
	}
}

// settleBackoff reports whether the netting of pair failed less than
// mt5SettleRetry ago: the poll doesn't queue it again meanwhile, so that a
// close-by refused is not sent every poll interval. The trading calls of the
// pair net it first anyway.
func (m *MT5) settleBackoff(pair string) bool {
	b := m.book
	b.settleMtx.Lock()
	defer b.settleMtx.Unlock()
	failed, ok := b.settleFailed[pair]
	return ok && time.Since(failed) < mt5SettleRetry
}

// settler nets the pairs queued until ctx is done, see settleLocked, each one
// apart, so that a close-by holding a pair long, eg: its answer lost, never
// holds the netting of the others. A pair trading meanwhile is skipped: its
// trading calls net it first, and the next poll queues it again. An error
// repeated by the next attempts is logged once, and the pair waits
// mt5SettleRetry before the next one (settleBackoff).
func (m *MT5) settler(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case pair := <-m.book.settle:
			if _, traded := m.instruments[pair]; !traded {
				continue
			}
			unlock, ok := m.tryLockPair(pair)
			if !ok {
				continue
			}
			go func() {
				defer unlock()
				m.settled(ctx, pair, m.settleLocked(ctx, pair))
			}()
		}
	}
}

// settled records the outcome of the netting of pair by the settler, see
// settleBackoff, logging an error once.
func (m *MT5) settled(ctx context.Context, pair string, err error) {
	b := m.book
	b.settleMtx.Lock()
	defer b.settleMtx.Unlock()
	switch {
	case err == nil:
		delete(b.settleFailed, pair)
		delete(b.settleErrs, pair)
		return
	case ctx.Err() != nil:
		return
	}
	b.settleFailed[pair] = time.Now()
	if err.Error() != b.settleErrs[pair] {
		b.settleErrs[pair] = err.Error()
		log.Errorf("[MT5] %v", err)
	}
}

// settlePair nets the bot's positions of pair holding both sides, under
// lockPair, see settleLocked.
func (m *MT5) settlePair(ctx context.Context, pair string) error {
	if _, traded := m.instruments[pair]; !traded {
		return nil
	}
	unlock := m.lockPair(pair)
	defer unlock()
	return m.settleLocked(ctx, pair)
}

// settleLocked nets the bot's positions of pair, under the lock of the pair:
// while both sides are open, the fill (see mt5CloseByPair) is closed by the
// oldest opposite position with TRADE_ACTION_CLOSE_BY, which closes the smaller
// volume on both, the rest staying on the larger one. It stops at the first
// error, the next poll trying again.
func (m *MT5) settleLocked(ctx context.Context, pair string) error {
	for attempts := 2 * len(m.botPositions(pair)); attempts > 0; attempts-- {
		fill, oldest, ok := mt5CloseByPair(m.botPositions(pair), m.pendingFill)
		if !ok {
			return nil
		}
		if err := m.closeBy(ctx, fill, oldest); err != nil {
			return fmt.Errorf("mt5: netting %s, closing the position %d by %d: %w", pair, fill.id(), oldest.id(),
				err)
		}
	}
	return nil
}

// pendingFill reports whether the position id was opened by the fill of a
// pending order, as the book or the adapter know it.
func (m *MT5) pendingFill(id int64) bool {
	b := m.book
	b.mtx.RLock()
	fill := b.fills[id]
	order, inHistory := b.history[id]
	b.mtx.RUnlock()
	if fill || (inHistory && mt5PendingKind(order.Type)) {
		return true
	}
	known, ok := m.known(id)
	if !ok || known.leg != "" {
		return false
	}
	switch known.order.Type {
	case model.OrderTypeLimit, model.OrderTypeLimitMaker, model.OrderTypeStopLoss, model.OrderTypeStopLossLimit,
		model.OrderTypeTakeProfit, model.OrderTypeTakeProfitLimit:
		return true
	}
	return false
}

// mt5CloseByPair returns the positions to close by one another among positions
// first in first out, when they hold both sides: the fill to net, and the
// oldest position on the other side. The fill is the newest position without
// levels opened by a pending order, or else the newest one without levels, or
// else the newest one opened by a pending order, or else the newest one: never
// a protected position opened after the fill, whose levels the netting would
// drop while an older position stays unprotected.
func mt5CloseByPair(positions []mt5Position, pendingFill func(int64) bool) (fill, oldest mt5Position, ok bool) {
	var buy, sell bool
	for _, position := range positions {
		if position.Type == mt5TypeSell {
			sell = true
		} else {
			buy = true
		}
	}
	if !buy || !sell {
		return mt5Position{}, mt5Position{}, false
	}

	newest := func(match func(mt5Position) bool) (mt5Position, bool) {
		for i := len(positions) - 1; i >= 0; i-- {
			if match(positions[i]) {
				return positions[i], true
			}
		}
		return mt5Position{}, false
	}
	unprotected := func(position mt5Position) bool { return position.SL == 0 && position.TP == 0 }
	fill, found := newest(func(position mt5Position) bool {
		return unprotected(position) && pendingFill(position.id())
	})
	if !found {
		fill, found = newest(unprotected)
	}
	if !found {
		fill, found = newest(func(position mt5Position) bool { return pendingFill(position.id()) })
	}
	if !found {
		fill = positions[len(positions)-1]
	}
	for _, position := range positions {
		if position.Type != fill.Type {
			return fill, position, true
		}
	}
	return mt5Position{}, mt5Position{}, false
}

// closeBy closes fill by oldest (TRADE_ACTION_CLOSE_BY) and applies it to the
// book: the smaller volume leaves both. The exits come with the history.
func (m *MT5) closeBy(ctx context.Context, fill, oldest mt5Position) error {
	symbol := m.instrument(fill.Symbol)
	request := map[string]any{
		"action":      mt5ActionCloseBy,
		"symbol":      fill.Symbol,
		"position":    fill.id(),
		"position_by": oldest.id(),
		"magic":       m.magic,
		"comment":     m.comment,
	}
	if _, err := m.trade(ctx, request); err != nil {
		return err
	}
	steps := min(symbol.steps(fill.Volume), symbol.steps(oldest.Volume))

	b := m.book
	b.mtx.Lock()
	defer b.mtx.Unlock()
	now := time.Now()
	for _, position := range []mt5Position{fill, oldest} {
		m.applyClose(position, steps, now)
		if _, open := b.positions[position.id()]; !open {
			b.gone[position.id()] = mt5Gone{at: now} // its exits come with the history
		}
	}
	b.accountAt = time.Time{}
	return nil
}

// applyClose takes steps of volume from position in the book, while the book
// still holds the volume it was closed from (otherwise a snapshot showed the
// close already): the position leaves for closed when nothing is left. The
// caller holds the lock of the book.
func (m *MT5) applyClose(position mt5Position, steps int64, now time.Time) {
	b := m.book
	symbol := m.instrument(position.Symbol)
	id := position.id()
	if current, open := b.positions[id]; open && symbol.steps(current.Volume) == symbol.steps(position.Volume) {
		if left := symbol.steps(position.Volume) - steps; left > 0 {
			current.Volume = symbol.lotsOf(left)
			b.positions[id] = current
		} else {
			delete(b.positions, id)
			b.closed[id] = current
		}
	}
	b.touched[id] = now
}

// trade sends a trade request (POST /order) and checks its retcode: done,
// placed or done partially, and no changes for the modifications. Any other is
// an *MT5Error. The request carries a deadline (tradeDeadline), and the ones
// creating records (a deal, a pending order, a close-by) a comment of their own
// (tagComment). One refused by the bridge before order_send (mt5Unsent) fails
// as it is; one whose answer is lost is looked for in the account first
// (reconcile), its result returned when found. None is sent while the
// terminal is logged in another account (ErrMT5AccountChanged), nor one
// opening or closing a position of a pair unsettled (ErrMT5Unsettled). The
// book is polled at once after a failure.
func (m *MT5) trade(ctx context.Context, request map[string]any) (mt5Result, error) {
	if err := m.accountSwitched(); err != nil {
		return mt5Result{}, err
	}
	if err := m.checkUnsettled(request); err != nil {
		return mt5Result{}, err
	}
	if _, ok := request["comment"]; ok {
		request["comment"] = m.tagComment()
	}
	sent, deadline := time.Now(), m.tradeDeadline()
	var result mt5Result
	if err := m.post(ctx, "/order", request, deadline, &result); err != nil {
		m.requestPoll()
		if mt5Unsent(err) {
			return mt5Result{}, err
		}
		if result, err = m.reconcile(ctx, request, err, sent, deadline); err != nil {
			return mt5Result{}, err
		}
	}
	switch result.Retcode {
	case mt5CodeDone, mt5CodePlaced, mt5CodeDonePartial:
		return result, nil
	case mt5CodeNoChanges:
		if action, _ := request["action"].(int); action == mt5ActionSLTP || action == mt5ActionModify {
			return result, nil
		}
	}
	return mt5Result{}, &MT5Error{Method: "POST", Path: "/order", Status: 200, Retcode: result.Retcode,
		Message: result.Comment}
}

// mt5TagLength is the length of the tag of a trade request, and mt5TagSpace
// the number of tags (base 36).
const (
	mt5TagLength = 6
	mt5TagSpace  = 2176782336 // 36^6
)

// tagComment returns the comment of a trade request: the bot's, cut to make
// room when needed, then "#" and a random tag, eg: "nb:llm#k3f9a2", so that the
// records of the request are found in the account when its answer is lost
// (reconcile). Ownership stays with the magic number.
func (m *MT5) tagComment() string {
	var random [8]byte
	_, _ = rand.Read(random[:]) // never fails, see crypto/rand
	tag := strconv.FormatUint(binary.LittleEndian.Uint64(random[:])%mt5TagSpace, 36)
	tag = strings.Repeat("0", mt5TagLength-len(tag)) + tag
	base := m.comment
	if room := mt5MaxComment - mt5TagLength - 1; len(base) > room {
		base = base[:room]
	}
	return base + "#" + tag
}

// mt5Tagged reports whether a comment ends with the tag of a trade request,
// see tagComment.
func mt5Tagged(comment string) bool {
	if len(comment) < mt5TagLength+2 || comment[len(comment)-mt5TagLength-1] != '#' {
		return false
	}
	for _, r := range comment[len(comment)-mt5TagLength:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

// The outcomes of a trade request looked for in the account, see lookupTrade.
const (
	mt5TradeAbsent    = iota // not found
	mt5TradeFound            // executed, its result rebuilt
	mt5TradeAmbiguous        // not found by its comment, but a record of the bot could be its own
)

// reconcile settles a trade request whose answer was lost (failed, a
// transport error, a timeout, or an error of the bridge after order_send): it
// is looked for in the account (lookupTrade) at once and, when not found or
// the reads failing, once more margin after its deadline, the reads of then
// bounded by mt5ReconcileReads. The bridge never sends a request to the
// terminal after its deadline and serves one request at a time, so a request
// absent then was not executed: failed is returned, said so. One found returns
// its result, as if its answer had come. Otherwise, the reads failing, a
// record ambiguous or a bridge without both deadlines (confirmsAbsence), the
// error is ErrMT5OutcomeUnknown: it may have been executed. A request opening
// or closing a position whose reads failed leaves its pair unsettled (see
// unsettle). The wait holds only the lock of the pair its caller holds, if
// any: the trading of the other pairs, the poll and the reads go on.
func (m *MT5) reconcile(ctx context.Context, request map[string]any, failed error, sent,
	deadline time.Time) (mt5Result, error) {
	unknown := func(reason string) (mt5Result, error) {
		log.Errorf("[MT5] the answer of the trade request %v was lost (%v): %s, it may have been executed", request,
			failed, reason)
		return mt5Result{}, fmt.Errorf("%w, %s: %w", ErrMT5OutcomeUnknown, reason, failed)
	}
	final := deadline.Add(m.margin) // the bridge can no longer send it
	for {
		last := !time.Now().Before(final)
		reads, cancel := context.WithDeadline(ctx, final)
		if last {
			reads, cancel = context.WithTimeout(ctx, mt5ReconcileReads)
		}
		result, outcome, err := m.lookupTrade(reads, request, sent)
		cancel()
		switch {
		case err != nil && last && ctx.Err() == nil:
			m.unsettle(request, sent)
			return unknown(fmt.Sprintf("looking for it in the account: %v", err))
		case err != nil && last:
			return unknown(fmt.Sprintf("looking for it in the account: %v", err))
		case err != nil: // again past the deadline, eg: the bridge restarting
		case outcome == mt5TradeFound:
			log.Warnf("[MT5] the answer of the trade request %v was lost (%v): found executed, order %d", request,
				failed, result.Order)
			return result, nil
		case outcome == mt5TradeAmbiguous:
			return unknown("a record of the bot since it was sent has no comment of a request")
		case last && !m.confirmsAbsence():
			return unknown("not found, but the bridge doesn't honor both deadlines")
		case last:
			log.Warnf("[MT5] the answer of the trade request %v was lost (%v): not executed, absent from the "+
				"account after its deadline", request, failed)
			return mt5Result{}, fmt.Errorf("%w: not executed, absent from the account after its deadline", failed)
		}
		if err := sleep(ctx, time.Until(final)); err != nil {
			return unknown(fmt.Sprintf("interrupted: %v", err))
		}
	}
}

// mt5Exposes reports whether a trade request opens or closes a position: a
// deal, a pending order or a close-by. The others, the levels of a position
// and the changes of a pending order, sent twice, leave the same state.
func mt5Exposes(request map[string]any) bool {
	switch action, _ := request["action"].(int); action {
	case mt5ActionDeal, mt5ActionPending, mt5ActionCloseBy:
		return true
	}
	return false
}

// unsettle leaves the pair of a trade request opening or closing a position
// unsettled, its outcome unknown, the account unreadable after its deadline:
// no other one of the pair is sent (checkUnsettled), which could double it,
// until a read of the account tells the outcome (awaitOutcome). The requests
// of the other kinds, and the ones sent to a bridge that doesn't confirm an
// absence (confirmsAbsence), leave none.
func (m *MT5) unsettle(request map[string]any, sent time.Time) {
	pair, _ := request["symbol"].(string)
	if !mt5Exposes(request) || !m.confirmsAbsence() || pair == "" {
		return
	}
	b := m.book
	b.pairsMtx.Lock()
	_, waiting := b.unsettled[pair]
	b.unsettled[pair] = mt5Unsettled{request: request, sent: sent}
	b.pairsMtx.Unlock()
	if !waiting {
		go m.awaitOutcome(pair)
	}
}

// checkUnsettled refuses a trade request opening or closing a position of a
// pair unsettled, see unsettle.
func (m *MT5) checkUnsettled(request map[string]any) error {
	pair, _ := request["symbol"].(string)
	if !mt5Exposes(request) {
		return nil
	}
	b := m.book
	b.pairsMtx.Lock()
	unsettled, ok := b.unsettled[pair]
	b.pairsMtx.Unlock()
	if !ok {
		return nil
	}
	return fmt.Errorf("%w: the answer of the trade request %v sent at %s was lost, and the account can't be "+
		"read since: nothing opening or closing a position of %s is sent until it is", ErrMT5Unsettled,
		unsettled.request, unsettled.sent.Format(time.TimeOnly), pair)
}

// awaitOutcome looks for the trade request leaving pair unsettled (see
// unsettle) until a read of the account tells its outcome, waiting more after
// each failure, mt5UnsettledRetry at most, then settles the pair: the request
// found executed shows in the book with the next poll. Until the adapter
// stops: the pair stays unsettled then.
func (m *MT5) awaitOutcome(pair string) {
	b := m.book
	delay := m.retryDelay
	var failure string
	for {
		if err := sleep(m.ctx, delay); err != nil {
			return
		}
		b.pairsMtx.Lock()
		unsettled := b.unsettled[pair]
		b.pairsMtx.Unlock()
		reads, cancel := context.WithTimeout(m.ctx, mt5ReconcileReads)
		result, outcome, err := m.lookupTrade(reads, unsettled.request, unsettled.sent)
		cancel()
		if err != nil {
			if m.ctx.Err() != nil {
				return
			}
			if err.Error() != failure {
				failure = err.Error()
				log.Errorf("[MT5] %s stays unsettled, the trade request %v of an unknown outcome: %v", pair,
					unsettled.request, err)
			}
			delay = min(max(2*delay, time.Millisecond), mt5UnsettledRetry)
			continue
		}
		switch outcome {
		case mt5TradeFound:
			log.Warnf("[MT5] %s settled: the trade request %v of an unknown outcome was executed, order %d", pair,
				unsettled.request, result.Order)
			m.requestPoll()
		case mt5TradeAmbiguous:
			log.Errorf("[MT5] %s settled: the trade request %v of an unknown outcome may have been executed, a "+
				"record of the bot since it was sent has no comment of a request", pair, unsettled.request)
			m.requestPoll()
		default:
			log.Warnf("[MT5] %s settled: the trade request %v of an unknown outcome was not executed, absent from "+
				"the account after its deadline", pair, unsettled.request)
		}
		b.pairsMtx.Lock()
		delete(b.unsettled, pair)
		b.pairsMtx.Unlock()
		return
	}
}

// lookupTrade looks for a trade request sent at sent in the account: the
// pending orders, the positions, and the deals and the orders of the history
// since then. The records of a deal, of a pending order or of a close-by carry
// the comment of their request (tagComment); a record of the bot on the symbol
// since then without the comment of a request, eg: cut by the broker, is
// ambiguous. A modification is found by the state it sets: the levels of the
// position (SLTP), the price and the levels of the pending order (MODIFY), or
// the pending order gone unfilled (REMOVE).
func (m *MT5) lookupTrade(ctx context.Context, request map[string]any, sent time.Time) (mt5Result, int, error) {
	var orders []mt5Order
	if err := m.get(ctx, "/orders", nil, &orders); err != nil {
		return mt5Result{}, 0, err
	}
	var positions []mt5Position
	if err := m.get(ctx, "/positions", nil, &positions); err != nil {
		return mt5Result{}, 0, err
	}
	since := sent.Add(-mt5HistoryOverlap)
	query := url.Values{
		"from": {strconv.FormatInt(since.Unix(), 10)},
		"to":   {strconv.FormatInt(time.Now().Add(mt5HistoryAhead).Unix(), 10)},
	}
	var deals []mt5Deal
	if err := m.get(ctx, "/deals", query, &deals); err != nil {
		return mt5Result{}, 0, err
	}
	var history []mt5Order
	if err := m.get(ctx, "/history_orders", query, &history); err != nil {
		return mt5Result{}, 0, err
	}

	done := mt5Result{Retcode: mt5CodeDone}
	action, _ := request["action"].(int)
	ticket, _ := request["order"].(int64)
	position, _ := request["position"].(int64)
	sl, _ := request["sl"].(float64)
	tp, _ := request["tp"].(float64)
	switch action {
	case mt5ActionSLTP:
		for _, open := range positions {
			if symbol := m.instrument(open.Symbol); open.id() == position && symbol.price(open.SL) == symbol.price(sl) &&
				symbol.price(open.TP) == symbol.price(tp) {
				return done, mt5TradeFound, nil
			}
		}
		return mt5Result{}, mt5TradeAbsent, nil
	case mt5ActionModify:
		price, _ := request["price"].(float64)
		for _, pending := range orders {
			if symbol := m.instrument(pending.Symbol); pending.Ticket == ticket &&
				symbol.price(pending.PriceOpen) == symbol.price(price) && symbol.price(pending.SL) == symbol.price(sl) &&
				symbol.price(pending.TP) == symbol.price(tp) {
				return done, mt5TradeFound, nil
			}
		}
		return mt5Result{}, mt5TradeAbsent, nil
	case mt5ActionRemove:
		filled := slices.ContainsFunc(positions, func(open mt5Position) bool { return open.id() == ticket }) ||
			slices.ContainsFunc(deals, func(deal mt5Deal) bool { return deal.Order == ticket })
		if filled || slices.ContainsFunc(orders, func(pending mt5Order) bool { return pending.Ticket == ticket }) {
			return mt5Result{}, mt5TradeAbsent, nil
		}
		done.Order = ticket
		return done, mt5TradeFound, nil
	}

	comment, _ := request["comment"].(string)
	symbol, _ := request["symbol"].(string)
	ours := func(magic int64, name, tag string) bool { return tag == comment && magic == m.magic && name == symbol }
	switch action {
	case mt5ActionDeal: // opening a position, or closing position
		var volume, value float64
		for _, deal := range deals {
			entry := deal.Entry == mt5EntryIn
			if position != 0 {
				entry = deal.Entry == mt5EntryOut && deal.PositionID == position
			}
			if ours(deal.Magic, deal.Symbol, deal.Comment) && entry {
				if done.Deal == 0 {
					done.Order, done.Deal = deal.Order, deal.Ticket
				}
				volume, value = volume+deal.Volume, value+deal.Volume*deal.Price
			}
		}
		if volume > 0 {
			done.Volume, done.Price = volume, value/volume
			return done, mt5TradeFound, nil
		}
		for _, open := range positions {
			if position == 0 && ours(open.Magic, open.Symbol, open.Comment) {
				done.Order, done.Volume, done.Price = open.id(), open.Volume, open.PriceOpen
				return done, mt5TradeFound, nil
			}
		}
		for _, order := range history {
			if ours(order.Magic, order.Symbol, order.Comment) && order.State == mt5StateFilled &&
				(position == 0 || order.PositionID == position) {
				done.Order, done.Volume, done.Price = order.Ticket, order.VolumeInitial, order.PriceOpen
				return done, mt5TradeFound, nil
			}
		}
	case mt5ActionPending:
		for _, pending := range orders {
			if ours(pending.Magic, pending.Symbol, pending.Comment) {
				done.Order, done.Volume, done.Price = pending.Ticket, pending.VolumeInitial, pending.PriceOpen
				return done, mt5TradeFound, nil
			}
		}
		for _, order := range history { // placed, and filled or canceled since
			if ours(order.Magic, order.Symbol, order.Comment) && order.State != mt5StateRejected {
				done.Order, done.Volume, done.Price = order.Ticket, order.VolumeInitial, order.PriceOpen
				return done, mt5TradeFound, nil
			}
		}
	case mt5ActionCloseBy:
		for _, deal := range deals {
			if ours(deal.Magic, deal.Symbol, deal.Comment) && deal.Entry == mt5EntryOutBy {
				done.Order, done.Deal, done.Volume = deal.Order, deal.Ticket, deal.Volume
				return done, mt5TradeFound, nil
			}
		}
		for _, order := range history {
			if ours(order.Magic, order.Symbol, order.Comment) && order.Type == mt5TypeCloseBy {
				done.Order, done.Volume = order.Ticket, order.VolumeInitial
				return done, mt5TradeFound, nil
			}
		}
	}

	// a record of the bot without the comment of a request: maybe this one
	untagged := func(magic int64, name, tag string, at time.Time) bool {
		return magic == m.magic && name == symbol && !at.Before(since) && !mt5Tagged(tag)
	}
	for _, deal := range deals {
		if untagged(deal.Magic, deal.Symbol, deal.Comment, deal.at()) && deal.Reason != mt5ReasonSL &&
			deal.Reason != mt5ReasonTP && deal.Reason != mt5ReasonSO {
			return mt5Result{}, mt5TradeAmbiguous, nil
		}
	}
	for _, open := range positions {
		if untagged(open.Magic, open.Symbol, open.Comment, open.opened()) {
			return mt5Result{}, mt5TradeAmbiguous, nil
		}
	}
	for _, order := range slices.Concat(orders, history) {
		if untagged(order.Magic, order.Symbol, order.Comment, order.setup()) && order.Reason != mt5ReasonSL &&
			order.Reason != mt5ReasonTP && order.Reason != mt5ReasonSO {
			return mt5Result{}, mt5TradeAmbiguous, nil
		}
	}
	return mt5Result{}, mt5TradeAbsent, nil
}

// marketRequest returns the trade request of a market order of lots on side at
// the price of tick, closing position when not 0.
func (m *MT5) marketRequest(pair string, side model.SideType, lots float64, tick mt5Tick,
	position int64) map[string]any {
	symbol := m.instruments[pair]
	price := tick.Ask
	if side == model.SideTypeSell {
		price = tick.Bid
	}
	request := map[string]any{
		"action":       mt5ActionDeal,
		"symbol":       pair,
		"volume":       lots,
		"type":         mt5MarketType(side),
		"price":        symbol.price(price),
		"deviation":    m.deviation,
		"magic":        m.magic,
		"comment":      m.comment,
		"type_filling": symbol.filling(),
		"type_time":    mt5TimeGTC,
	}
	if position != 0 {
		request["position"] = position
	}
	return request
}

// executed returns the price and the lots of a market deal from its answer:
// the price of the answer, or else the one of tick on side, never 0, and the
// volume of the answer, or else the lots requested.
func (m *MT5) executed(pair string, side model.SideType, lots float64, result mt5Result,
	tick mt5Tick) (price, volume float64) {
	symbol := m.instruments[pair]
	price = symbol.price(result.Price)
	if price <= 0 {
		price = tick.Ask
		if side == model.SideTypeSell {
			price = tick.Bid
		}
		if price <= 0 {
			price = mid(tick.Bid, tick.Ask)
		}
		price = symbol.price(price)
	}
	volume = lots
	if result.Volume > 0 {
		volume = symbol.lotsOf(symbol.steps(result.Volume))
	}
	return price, volume
}

// openPosition opens a position of lots on side at the market with the levels
// stop and profit (0 for none), and applies it to the book with its entry deal.
// It returns the ticket of the position, its price and its lots.
func (m *MT5) openPosition(ctx context.Context, pair string, side model.SideType, lots, stop, profit float64,
	tick mt5Tick) (int64, float64, float64, error) {
	request := m.marketRequest(pair, side, lots, tick, 0)
	request["sl"], request["tp"] = stop, profit
	result, err := m.trade(ctx, request)
	if err != nil {
		return 0, 0, 0, err
	}
	if result.Order == 0 {
		m.requestPoll()
		return 0, 0, 0, errors.New("mt5: POST /order: no order ticket in the answer")
	}
	price, volume := m.executed(pair, side, lots, result, tick)

	b := m.book
	b.mtx.Lock()
	defer b.mtx.Unlock()
	now := time.Now()
	id := result.Order
	if _, open := b.positions[id]; !open {
		b.positions[id] = mt5Position{
			Ticket:     id,
			Identifier: id,
			Time:       now.Unix(),
			TimeMsc:    now.UnixMilli(),
			Type:       mt5MarketType(side),
			Magic:      m.magic,
			Reason:     mt5ReasonExpert,
			Volume:     volume,
			PriceOpen:  price,
			SL:         stop,
			TP:         profit,
			Symbol:     pair,
			Comment:    m.comment,
		}
	}
	if result.Deal != 0 {
		m.recordDeal(mt5Deal{Ticket: result.Deal, Order: id, Time: now.Unix(), TimeMsc: now.UnixMilli(),
			Type: mt5MarketType(side), Entry: mt5EntryIn, Magic: m.magic, PositionID: id, Reason: mt5ReasonExpert,
			Volume: volume, Price: price, Symbol: pair, Comment: m.comment, provisional: true})
	}
	b.touched[id] = now
	b.accountAt = time.Time{}
	return id, price, volume, nil
}

// closePosition closes lots of position at the market (TRADE_ACTION_DEAL with
// the position) and applies it to the book with its exit deal. It returns the
// ticket of the closing order, the price and the lots closed.
func (m *MT5) closePosition(ctx context.Context, position mt5Position, lots float64, tick mt5Tick) (int64, float64,
	float64, error) {
	pair := position.Symbol
	side := mt5Opposite(mt5Side(position.Type))
	result, err := m.trade(ctx, m.marketRequest(pair, side, lots, tick, position.id()))
	if err != nil {
		return 0, 0, 0, err
	}
	price, volume := m.executed(pair, side, lots, result, tick)
	symbol := m.instruments[pair]

	b := m.book
	b.mtx.Lock()
	defer b.mtx.Unlock()
	now := time.Now()
	m.applyClose(position, symbol.steps(volume), now)
	if result.Deal != 0 {
		m.recordDeal(mt5Deal{Ticket: result.Deal, Order: result.Order, Time: now.Unix(), TimeMsc: now.UnixMilli(),
			Type: mt5MarketType(side), Entry: mt5EntryOut, Magic: m.magic, PositionID: position.id(),
			Reason: mt5ReasonExpert, Volume: volume, Price: price, Symbol: pair, Comment: m.comment,
			provisional: true})
	} else if _, open := b.positions[position.id()]; !open {
		b.gone[position.id()] = mt5Gone{at: now} // its exit comes with the history
	}
	b.accountAt = time.Time{}
	return result.Order, price, volume, nil
}

// placePending places a pending order (TRADE_ACTION_PENDING, good till
// canceled, filling return) of lots at level with the levels stop and profit
// (0 for none), and applies it to the book. It returns its ticket.
func (m *MT5) placePending(ctx context.Context, pair string, kind int, lots, level, stop, profit float64) (int64,
	error) {
	request := map[string]any{
		"action":       mt5ActionPending,
		"symbol":       pair,
		"volume":       lots,
		"type":         kind,
		"price":        level,
		"sl":           stop,
		"tp":           profit,
		"magic":        m.magic,
		"comment":      m.comment,
		"type_filling": mt5FillReturn,
		"type_time":    mt5TimeGTC,
	}
	result, err := m.trade(ctx, request)
	if err != nil {
		return 0, err
	}
	if result.Order == 0 {
		m.requestPoll()
		return 0, errors.New("mt5: POST /order: no order ticket in the answer")
	}

	b := m.book
	b.mtx.Lock()
	defer b.mtx.Unlock()
	now := time.Now()
	if _, pending := b.orders[result.Order]; !pending {
		b.orders[result.Order] = mt5Order{
			Ticket:        result.Order,
			TimeSetup:     now.Unix(),
			TimeSetupMsc:  now.UnixMilli(),
			Type:          kind,
			TypeTime:      mt5TimeGTC,
			TypeFilling:   mt5FillReturn,
			State:         mt5StatePlaced,
			Magic:         m.magic,
			VolumeInitial: lots,
			VolumeCurrent: lots,
			PriceOpen:     level,
			SL:            stop,
			TP:            profit,
			Symbol:        pair,
			Comment:       m.comment,
		}
	}
	b.touched[result.Order] = now
	b.accountAt = time.Time{}
	return result.Order, nil
}

// modifyPending moves a pending order (TRADE_ACTION_MODIFY with its price, both
// levels and its expiry) and applies it to the book.
func (m *MT5) modifyPending(ctx context.Context, order mt5Order, price, stop, profit float64) error {
	request := map[string]any{
		"action":     mt5ActionModify,
		"order":      order.Ticket,
		"symbol":     order.Symbol,
		"price":      price,
		"sl":         stop,
		"tp":         profit,
		"type_time":  order.TypeTime,
		"expiration": order.TimeExpiration,
	}
	if _, err := m.trade(ctx, request); err != nil {
		return err
	}
	b := m.book
	b.mtx.Lock()
	defer b.mtx.Unlock()
	if pending, ok := b.orders[order.Ticket]; ok {
		pending.PriceOpen, pending.SL, pending.TP = price, stop, profit
		b.orders[order.Ticket] = pending
	}
	b.touched[order.Ticket] = time.Now()
	return nil
}

// setLevels sets both levels of a position (TRADE_ACTION_SLTP: MT5 takes both,
// 0 removing one) and applies them to the book.
func (m *MT5) setLevels(ctx context.Context, position mt5Position, stop, profit float64) error {
	request := map[string]any{
		"action":   mt5ActionSLTP,
		"symbol":   position.Symbol,
		"position": position.id(),
		"sl":       stop,
		"tp":       profit,
		"magic":    m.magic,
	}
	if _, err := m.trade(ctx, request); err != nil {
		return err
	}
	b := m.book
	b.mtx.Lock()
	defer b.mtx.Unlock()
	if current, open := b.positions[position.id()]; open {
		current.SL, current.TP = stop, profit
		b.positions[position.id()] = current
	}
	b.touched[position.id()] = time.Now()
	return nil
}

// removePending removes a pending order (TRADE_ACTION_REMOVE) and moves it out
// of the book as canceled. A request refused as invalid is ErrOrderNotOpen: the
// order was filled or canceled meanwhile.
func (m *MT5) removePending(ctx context.Context, order mt5Order) error {
	request := map[string]any{"action": mt5ActionRemove, "order": order.Ticket, "symbol": order.Symbol}
	if _, err := m.trade(ctx, request); err != nil {
		var mt5Err *MT5Error
		if errors.As(err, &mt5Err) && mt5Err.Retcode == mt5CodeInvalid {
			m.requestPoll()
			return fmt.Errorf("%w: %v", ErrOrderNotOpen, err)
		}
		return err
	}
	b := m.book
	b.mtx.Lock()
	defer b.mtx.Unlock()
	now := time.Now()
	delete(b.orders, order.Ticket)
	b.finals[order.Ticket] = mt5Final{order: order, state: mt5StateCanceled, at: now}
	b.touched[order.Ticket] = now
	b.accountAt = time.Time{}
	return nil
}

// mt5Wallet is the account as xtrader sees it, in the account currency.
type mt5Wallet struct {
	free      float64            // cash available for new positions
	committed float64            // held by the open positions: their value, or their margin with leverage
	sizes     map[string]float64 // net units of the bot on each traded symbol, negative when short
}

// pollAccount reads the account for the poll and checks it, see checkAccount.
func (m *MT5) pollAccount(ctx context.Context) error {
	var account mt5Account
	if err := m.get(ctx, "/account", nil, &account); err != nil {
		return fmt.Errorf("reading the account: %w", err)
	}
	return m.checkAccount(account)
}

// accountInfo returns the account of the book, read again (GET /account) when
// older than the poll interval, changed by a trade, or of another account
// last time: then ErrMT5AccountChanged, see checkAccount.
func (m *MT5) accountInfo(ctx context.Context) (mt5Account, error) {
	b := m.book
	b.mtx.RLock()
	account, at, switched := b.account, b.accountAt, b.switched
	b.mtx.RUnlock()
	if switched == nil && !at.IsZero() && time.Since(at) < m.pollInterval {
		return account, nil
	}
	if err := m.get(ctx, "/account", nil, &account); err != nil {
		return mt5Account{}, fmt.Errorf("mt5: reading the account: %w", err)
	}
	if err := m.checkAccount(account); err != nil {
		return mt5Account{}, err
	}
	b.mtx.Lock()
	b.account, b.accountAt = account, time.Now()
	b.mtx.Unlock()
	return account, nil
}

// checkAccount compares an account read with the one NewMT5 connected to.
// Another login, eg: the account switched by hand in the terminal, or the
// account turned real or demo, is ErrMT5AccountChanged: until an account read
// is the bot's again, no trade request is sent (see trade), the poll applies
// no snapshot and the account is not reported, logged once each way.
func (m *MT5) checkAccount(account mt5Account) error {
	var err error
	if real := account.TradeMode == mt5AccountReal; account.Login != m.login || real == m.demo {
		mode, want := "demo", "real"
		if real {
			mode = "real"
		}
		if m.demo {
			want = "demo"
		}
		err = fmt.Errorf("%w: it is logged in the %s account %d, the bot trades the %s account %d",
			ErrMT5AccountChanged, mode, account.Login, want, m.login)
	}
	b := m.book
	b.mtx.Lock()
	previous := b.switched
	b.switched = err
	b.mtx.Unlock()
	switch {
	case err != nil && (previous == nil || previous.Error() != err.Error()):
		log.Errorf("[MT5] %v: no trade request is sent until it is back", err)
	case err == nil && previous != nil:
		log.Infof("[MT5] the terminal is logged in the account %d again: trading resumes", m.login)
	}
	return err
}

// accountSwitched returns the error of the last account read when it was not
// the bot's, see checkAccount.
func (m *MT5) accountSwitched() error {
	b := m.book
	b.mtx.RLock()
	defer b.mtx.RUnlock()
	return b.switched
}

// wallet returns the wallet: the cash is the account's, shared by the bots,
// and sizes counts the bot's positions only.
//   - with leverage: free = max(margin_free, 0), committed = margin;
//   - without: committed = the value of every open position of the account at
//     its open price (units x price_open x the rate of its quote currency into
//     the account currency), free = max(balance - committed, 0), the balance
//     not including the floating profits.
func (m *MT5) wallet(ctx context.Context) (mt5Wallet, error) {
	account, err := m.accountInfo(ctx)
	if err != nil {
		return mt5Wallet{}, err
	}

	b := m.book
	b.mtx.RLock()
	positions := make([]mt5Position, 0, len(b.positions))
	for _, position := range b.positions {
		positions = append(positions, position)
	}
	b.mtx.RUnlock()

	steps := make(map[string]int64) // in volume steps, so that opposite positions net to 0 exactly
	for _, position := range positions {
		if !m.ours(position.Magic, position.Symbol) {
			continue
		}
		n := m.instruments[position.Symbol].steps(position.Volume)
		if position.Type == mt5TypeSell {
			n = -n
		}
		steps[position.Symbol] += n
	}
	wallet := mt5Wallet{sizes: make(map[string]float64)}
	for name, n := range steps {
		if n == 0 {
			continue
		}
		symbol := m.instruments[name]
		units := symbol.units(symbol.lotsOf(max(n, -n)))
		if n < 0 {
			units = -units
		}
		wallet.sizes[name] = units
	}

	if m.leverage {
		wallet.free = math.Max(account.MarginFree, 0)
		wallet.committed = math.Max(account.Margin, 0)
		return wallet, nil
	}
	rates := make(map[string]float64)
	for _, position := range positions {
		value, err := m.value(ctx, position, rates)
		if err != nil {
			return mt5Wallet{}, err
		}
		wallet.committed += value
	}
	wallet.free = math.Max(account.Balance-wallet.committed, 0)
	return wallet, nil
}

// value returns the value of a position in the account currency, at its open
// price, the rates of the quote currencies cached in rates. A position the bot
// can't value, eg: a manual trade on a symbol without conversion into the
// account currency, counts for 0 rather than failing the wallet of every bot
// on the account; the traded symbols always have one (NewMT5).
func (m *MT5) value(ctx context.Context, position mt5Position, rates map[string]float64) (float64, error) {
	symbol, traded := m.instruments[position.Symbol]
	if !traded {
		found, err := m.symbol(ctx, position.Symbol)
		if err != nil || found == nil {
			log.Debugf("[MT5] no symbol %s to value the position %d, counted for 0", position.Symbol,
				position.id())
			return 0, nil
		}
		symbol = *found
	}
	quote := symbol.CurrencyProfit
	rate, ok := rates[quote]
	if !ok {
		var err error
		if rate, err = m.rate(ctx, quote, m.currency); err != nil {
			if !traded {
				log.Debugf("[MT5] the position %d of %s counted for 0: %v", position.id(), position.Symbol, err)
				return 0, nil
			}
			return 0, err
		}
		rates[quote] = rate
	}
	return symbol.units(position.Volume) * position.PriceOpen * rate, nil
}

// Account returns the cash available for new positions as a free balance and
// the amount the positions commit as locked, and the net size of the bot on
// each traded symbol as a balance of its base asset (the symbols sharing a base
// summed), with the leverage of the account when trading with leverage. See
// wallet. The cash is in the quote currency the traded symbols share, eg: USD
// for a BRL account trading BTCUSDm, converted at the market as Position does,
// as the consumers of the account value the pairs in their quote (the Telegram
// /balance, the balance of the dashboard); in the account currency when the
// symbols have several.
func (m *MT5) Account() (model.Account, error) {
	if m.dataOnly {
		return model.Account{}, m.errDataOnly("Account")
	}
	wallet, err := m.wallet(m.ctx)
	if err != nil {
		return model.Account{}, err
	}
	currency := m.cashCurrency()
	rate, err := m.rate(m.ctx, m.currency, currency)
	if err != nil {
		return model.Account{}, err
	}
	balances := []model.Balance{{Asset: currency, Free: wallet.free * rate, Lock: wallet.committed * rate}}
	assets := make(map[string]int)
	for _, name := range m.symbols {
		size := wallet.sizes[name]
		if size == 0 {
			continue
		}
		asset := m.assetsInfo[name].BaseAsset
		if i, ok := assets[asset]; ok {
			balances[i].Free += size
			continue
		}
		balance := model.Balance{Asset: asset, Free: size}
		if m.leverage {
			balance.Leverage = m.Leverage(name)
		}
		assets[asset] = len(balances)
		balances = append(balances, balance)
	}
	return model.Account{Balances: balances}, nil
}

// cashCurrency returns the quote currency of the traded symbols when they all
// share one, or else the account currency.
func (m *MT5) cashCurrency() string {
	currency := ""
	for _, name := range m.symbols {
		quote := m.assetsInfo[name].QuoteAsset
		switch {
		case quote == "":
			return m.currency
		case currency == "":
			currency = quote
		case quote != currency:
			return m.currency
		}
	}
	if currency == "" {
		return m.currency
	}
	return currency
}

// Position returns the net size of the bot's positions of pair in units,
// negative when short, and the cash available for new positions converted into
// the quote currency of the pair.
func (m *MT5) Position(pair string) (asset, quote float64, err error) {
	if m.dataOnly {
		return 0, 0, m.errDataOnly("Position")
	}
	wallet, err := m.wallet(m.ctx)
	if err != nil {
		return 0, 0, err
	}
	currency := m.currency
	if info, ok := m.assetsInfo[pair]; ok && info.QuoteAsset != "" {
		currency = info.QuoteAsset
	}
	rate, err := m.rate(m.ctx, m.currency, currency)
	if err != nil {
		return 0, 0, err
	}
	return wallet.sizes[pair], wallet.free * rate, nil
}

// MT5OpenPosition is the net position of the bot on a symbol as the terminal
// reports it: its side, its size in units of the asset, the average open price
// of its positions on that side, and their profit in the account currency,
// the swap included, at the price they close at (the bid of a long, the ask
// of a short), as the terminal shows it.
type MT5OpenPosition struct {
	Side     model.SideType
	Quantity float64
	Entry    float64
	Profit   float64
	Currency string
}

// OpenPosition returns the net position of the bot's positions of pair, as
// last polled from the terminal (no request), false when flat.
func (m *MT5) OpenPosition(pair string) (MT5OpenPosition, bool) {
	symbol := m.instrument(pair)
	b := m.book
	b.mtx.RLock()
	defer b.mtx.RUnlock()

	var net, profit, longUnits, longCost, shortUnits, shortCost float64
	for _, position := range b.positions {
		if position.Symbol != pair || position.Magic != m.magic {
			continue
		}
		units := symbol.units(position.Volume)
		if position.Type == mt5TypeBuy {
			net += units
			longUnits, longCost = longUnits+units, longCost+units*position.PriceOpen
		} else {
			net -= units
			shortUnits, shortCost = shortUnits+units, shortCost+units*position.PriceOpen
		}
		profit += position.Profit + position.Swap
	}
	switch {
	case net > 1e-12 && longUnits > 0:
		return MT5OpenPosition{Side: model.SideTypeBuy, Quantity: net, Entry: longCost / longUnits, Profit: profit,
			Currency: m.currency}, true
	case net < -1e-12 && shortUnits > 0:
		return MT5OpenPosition{Side: model.SideTypeSell, Quantity: -net, Entry: shortCost / shortUnits,
			Profit: profit, Currency: m.currency}, true
	}
	return MT5OpenPosition{}, false
}

// AccountSummary returns the account as the terminal reports it, in its
// currency (the one read within the poll interval, else read again). It
// implements service.AccountReporter.
func (m *MT5) AccountSummary() (service.AccountSummary, bool) {
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	account, err := m.accountInfo(ctx)
	if err != nil {
		return service.AccountSummary{}, false
	}
	return service.AccountSummary{Currency: account.Currency, Balance: account.Balance, Equity: account.Equity,
		Profit: account.Profit, Margin: account.Margin, FreeMargin: account.MarginFree}, true
}

// ClosedProfit returns the profit the account booked for close, a fill of the
// bot closing the position entry opened, from the deals of the history kept
// (mt5HistoryLookback at the start): the exits of the position by the stop
// loss for a stop loss leg, by the take profit for a take profit leg, else the
// ones of the closing order (or, without its ticket, the other exits within
// seconds of the fill). Their profit, swap, commission and fee, in the account
// currency; unknown while one of them is only known from the answer of the
// close, not from the history yet. It implements service.AccountReporter; it
// reads the book only.
func (m *MT5) ClosedProfit(close, entry model.Order) (float64, bool) {
	position := entry.ExchangeID
	if !mt5IsTicket(position) {
		return 0, false
	}
	var reason int
	switch close.Type {
	case model.OrderTypeStopLoss, model.OrderTypeStopLossLimit:
		reason = mt5ReasonSL
	case model.OrderTypeTakeProfit, model.OrderTypeTakeProfitLimit, model.OrderTypeLimitMaker:
		reason = mt5ReasonTP
	}
	if close.GroupID == nil && close.Type != model.OrderTypeStopLoss && close.Type != model.OrderTypeStopLossLimit {
		reason = 0 // a market or a limit order, not a leg
	}

	b := m.book
	b.mtx.RLock()
	defer b.mtx.RUnlock()
	exits := b.exits[position]
	matches := func(deal mt5Deal) bool {
		switch {
		case reason != 0:
			return deal.Reason == reason
		case deal.Order == close.ExchangeID:
			return true
		}
		for _, other := range exits {
			if other.Order == close.ExchangeID {
				return false // the ticket of the closing order is known: its exits only
			}
		}
		gap := deal.at().Sub(close.UpdatedAt)
		return deal.Reason != mt5ReasonSL && deal.Reason != mt5ReasonTP && gap > -10*time.Second &&
			gap < 10*time.Second
	}
	var profit float64
	found := false
	for _, deal := range exits {
		if matches(deal) {
			if deal.provisional {
				return 0, false // its profit comes with the history
			}
			profit += deal.Profit + deal.Swap + deal.Commission + deal.Fee
			found = true
		}
	}
	return profit, found
}

// readExits reads the history from since, once positions were closed at the
// market: the exit deals recorded from the answers, provisional, are replaced
// by the ones of the history, with their profit, before the order is handed
// back. A read failing is logged: the next polls read it, see unresolved.
func (m *MT5) readExits(ctx context.Context, since time.Time) {
	if err := m.readHistory(ctx, since.Add(-mt5HistoryOverlap), time.Now().Add(mt5HistoryAhead)); err != nil {
		log.Warnf("[MT5] reading the deals of the close: %v", err)
	}
}

// errDataOnly is the error of a trading call with WithMT5DataOnly.
func (m *MT5) errDataOnly(call string) error {
	return fmt.Errorf("%w: mt5: %s refused, the adapter reads the market data only (WithMT5DataOnly)",
		ErrNotSupported, call)
}

// CreateOrderMarket buys or sells size units of pair at the market, netted
// against the bot's positions: its opposite positions are closed first in,
// first out, and only the rest opens a position. The order returned is FILLED
// at the average price of its fills, with the ticket of the position opened,
// or else of the first closing order. When a step fails after others were done,
// the order returned is FILLED with the quantity done and the error is logged,
// so the order controller's position matches the broker's; when the first one
// fails, the error is returned.
func (m *MT5) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	if m.dataOnly {
		return model.Order{}, m.errDataOnly("CreateOrderMarket")
	}
	lots, _, err := m.dealSize(pair, size)
	if err != nil {
		return model.Order{}, err
	}
	unlock := m.lockPair(pair)
	defer unlock()
	// a fill against the net not netted yet: both sides would blur first in,
	// first out
	if err := m.settleLocked(m.ctx, pair); err != nil {
		log.Warnf("[MT5] %v", err)
	}
	return m.marketNet(m.ctx, side, pair, lots)
}

// ClosesProtected reports that a market order closes a protected position with
// its stop loss and take profit in place, see service.ProtectedCloser: it
// closes the bot's opposite positions by their tickets (marketNet), and the
// levels go with them, the legs reported CANCELED. A close refused leaves the
// position protected.
func (m *MT5) ClosesProtected() bool {
	return true
}

// marketNet is CreateOrderMarket of lots under the lock of the pair.
func (m *MT5) marketNet(ctx context.Context, side model.SideType, pair string, lots float64) (model.Order, error) {
	symbol := m.instruments[pair]
	requested := symbol.units(lots)
	remaining := symbol.steps(lots)
	tick, err := m.tick(ctx, pair)
	if err != nil {
		return model.Order{}, &OrderError{Err: err, Pair: pair, Quantity: requested}
	}

	started := time.Now()
	var done int64            // steps filled
	var filled, value float64 // units filled, and their value at the prices of the fills
	var prices []float64
	var ticket, closing, first int64
	fill := func(units, price float64) {
		filled, value = filled+units, value+units*price
		prices = append(prices, price)
	}
	finish := func(err error) (model.Order, error) {
		if first != 0 {
			m.readExits(ctx, started)
		}
		if done == 0 {
			return model.Order{}, err
		}
		quantity := symbol.units(symbol.lotsOf(done))
		if err != nil {
			log.Errorf("[MT5] the %s market order of %v %s filled %v only: %v", side, requested, pair, quantity, err)
		}
		price := symbol.price(value / filled)
		if slices.Min(prices) == slices.Max(prices) {
			price = prices[0]
		}
		now := time.Now().UTC()
		order := model.Order{
			ExchangeID: ticket,
			CreatedAt:  now,
			UpdatedAt:  now,
			Pair:       pair,
			Side:       side,
			Type:       model.OrderTypeMarket,
			Status:     model.OrderStatusTypeFilled,
			Price:      price,
			Quantity:   quantity,
		}
		if ticket == 0 {
			order.ExchangeID = closing
		}
		if order.ExchangeID == 0 {
			// no ticket in the answers: derived from the first position closed
			order.ExchangeID = mt5HashID(fmt.Sprintf("mt5:close:%d:%d", first, now.UnixMilli()))
		}
		m.remember(mt5Known{ticket: order.ExchangeID, order: order})
		return order, nil
	}

	// the bot's opposite positions, first in first out
	for _, position := range m.botPositions(pair) {
		if remaining == 0 {
			break
		}
		if mt5Side(position.Type) == side {
			continue
		}
		steps := min(remaining, symbol.steps(position.Volume))
		if steps <= 0 {
			continue
		}
		order, price, volume, err := m.closePosition(ctx, position, symbol.lotsOf(steps), tick)
		if err != nil {
			return finish(&OrderError{Err: err, Pair: pair, Quantity: requested})
		}
		if closing == 0 {
			closing = order
		}
		if first == 0 {
			first = position.id()
		}
		closed := min(symbol.steps(volume), steps)
		fill(symbol.units(symbol.lotsOf(closed)), price)
		done, remaining = done+closed, remaining-closed
		if closed < steps {
			return finish(fmt.Errorf("mt5: the close of the position %d filled %v lots of %v", position.id(),
				volume, symbol.lotsOf(steps)))
		}
	}

	// the rest opens a position
	if remaining > 0 {
		id, price, volume, err := m.openPosition(ctx, pair, side, symbol.lotsOf(remaining), 0, 0, tick)
		if err != nil {
			return finish(&OrderError{Err: err, Pair: pair, Quantity: requested})
		}
		ticket = id
		opened := min(symbol.steps(volume), remaining)
		fill(symbol.units(symbol.lotsOf(opened)), price)
		done += opened
	}
	return finish(nil)
}

// CreateOrderMarketQuote buys or sells the size worth quote at the current
// price, the ask for a buy and the bid for a sell.
func (m *MT5) CreateOrderMarketQuote(side model.SideType, pair string, quote float64) (model.Order, error) {
	if m.dataOnly {
		return model.Order{}, m.errDataOnly("CreateOrderMarketQuote")
	}
	tick, err := m.tick(m.ctx, pair)
	if err != nil {
		return model.Order{}, err
	}
	price := tick.Ask
	if side == model.SideTypeSell {
		price = tick.Bid
	}
	if price <= 0 {
		return model.Order{}, fmt.Errorf("mt5: no price for %s", pair)
	}
	return m.CreateOrderMarket(side, pair, quote/price)
}

// CreateOrderLimit places a buy limit or a sell limit of size units at limit.
// Its fill against the bot's net is netted with close-by (settlePair).
func (m *MT5) CreateOrderLimit(side model.SideType, pair string, size float64, limit float64) (model.Order,
	error) {
	if m.dataOnly {
		return model.Order{}, m.errDataOnly("CreateOrderLimit")
	}
	lots, units, err := m.dealSize(pair, size)
	if err != nil {
		return model.Order{}, err
	}
	order, _, err := m.place(side, pair, lots, units, limit, 0, 0, false)
	return order, err
}

// CreateOrderStop places a sell stop of size units at stop, which closes a long
// position once the price falls to it: its fill opens a short position, netted
// at once with the long ones by close-by (settlePair).
func (m *MT5) CreateOrderStop(pair string, size float64, stop float64) (model.Order, error) {
	if m.dataOnly {
		return model.Order{}, m.errDataOnly("CreateOrderStop")
	}
	lots, units, err := m.dealSize(pair, size)
	if err != nil {
		return model.Order{}, err
	}
	order, _, err := m.place(model.SideTypeSell, pair, lots, units, stop, 0, 0, true)
	return order, err
}

// place places a pending order of lots, units, at level, a limit or a stop, with
// the levels stop and profit (0 for none), and returns it remembered, with its
// ticket.
func (m *MT5) place(side model.SideType, pair string, lots, units, level, stop, profit float64,
	isStop bool) (model.Order, int64, error) {
	symbol := m.instruments[pair]
	level = symbol.price(level)
	kind := mt5PendingType(side, isStop)
	ticket, err := m.placePending(m.ctx, pair, kind, lots, level, stop, profit)
	if err != nil {
		return model.Order{}, 0, &OrderError{Err: err, Pair: pair, Quantity: units}
	}
	now := time.Now().UTC()
	order := model.Order{
		ExchangeID: ticket,
		CreatedAt:  now,
		UpdatedAt:  now,
		Pair:       pair,
		Side:       side,
		Type:       mt5OrderType(kind),
		Status:     model.OrderStatusTypeNew,
		Price:      level,
		Quantity:   units,
	}
	if isStop {
		trigger := level
		order.Stop = &trigger
	}
	m.remember(mt5Known{ticket: ticket, order: order})
	return order, ticket, nil
}

// checkEntry validates a protected entry at entry, 0 for the market: its levels
// (checkProtection) and that it opens or adds to the bot's net rather than
// reducing it, which its levels would not protect (ErrNotSupported).
func (m *MT5) checkEntry(side model.SideType, pair string, size, entry, stop, profit float64) error {
	if err := checkProtection(side, entry, stop, profit); err != nil {
		return &OrderError{Err: err, Pair: pair, Quantity: size}
	}
	net := m.netSteps(pair)
	if (side == model.SideTypeBuy && net < 0) || (side == model.SideTypeSell && net > 0) {
		held := model.SideTypeBuy
		if net < 0 {
			held = model.SideTypeSell
		}
		return &OrderError{
			Err: fmt.Errorf("mt5: a protected %s order would reduce the %s position of %s, close it first: %w",
				side, held, pair, ErrNotSupported),
			Pair:     pair,
			Quantity: size,
		}
	}
	return nil
}

// CreateOrderMarketProtected opens a position at the market with its stop loss
// and take profit in the same request, see service.ProtectionBroker: the entry
// FILLED, then the legs, anchored to the ticket of the position. Refused
// against the bot's net (checkEntry).
func (m *MT5) CreateOrderMarketProtected(side model.SideType, pair string, size, stop,
	profit float64) ([]model.Order, error) {
	if m.dataOnly {
		return nil, m.errDataOnly("CreateOrderMarketProtected")
	}
	lots, units, err := m.dealSize(pair, size)
	if err != nil {
		return nil, err
	}
	symbol := m.instruments[pair]
	stop, profit = symbol.price(stop), symbol.price(profit)

	unlock := m.lockPair(pair)
	defer unlock()
	// net a fill first, or the netting would close the new position by it
	if err := m.settleLocked(m.ctx, pair); err != nil {
		log.Warnf("[MT5] %v", err)
	}
	if err := m.checkEntry(side, pair, units, 0, stop, profit); err != nil {
		return nil, err
	}
	tick, err := m.tick(m.ctx, pair)
	if err != nil {
		return nil, &OrderError{Err: err, Pair: pair, Quantity: units}
	}
	ticket, price, volume, err := m.openPosition(m.ctx, pair, side, lots, stop, profit, tick)
	if err != nil {
		return nil, &OrderError{Err: err, Pair: pair, Quantity: units}
	}
	units = symbol.units(volume)
	now := time.Now().UTC()
	order := model.Order{
		ExchangeID: ticket,
		CreatedAt:  now,
		UpdatedAt:  now,
		Pair:       pair,
		Side:       side,
		Type:       model.OrderTypeMarket,
		Status:     model.OrderStatusTypeFilled,
		Price:      price,
		Quantity:   units,
	}
	m.remember(mt5Known{ticket: ticket, order: order})
	legs := m.protect(ticket, nil, side, pair, units, stop, profit)
	return append([]model.Order{order}, legs...), nil
}

// CreateOrderLimitProtected places a limit order at limit opening a position
// with its stop loss and take profit, see service.ProtectionBroker: the entry
// pending, then the legs, anchored to its ticket.
func (m *MT5) CreateOrderLimitProtected(side model.SideType, pair string, size, limit, stop,
	profit float64) ([]model.Order, error) {
	return m.placeProtected(side, pair, size, limit, stop, profit, false)
}

// CreateOrderStopProtected places a stop order triggered at level opening a
// position with its stop loss and take profit, see service.ProtectionBroker.
func (m *MT5) CreateOrderStopProtected(side model.SideType, pair string, size, level, stop,
	profit float64) ([]model.Order, error) {
	return m.placeProtected(side, pair, size, level, stop, profit, true)
}

// placeProtected places a protected pending entry, a limit or a stop, at level:
// the entry, then its legs, under the lock of the pair, netted first so that
// checkEntry sees the net the order controller expects.
func (m *MT5) placeProtected(side model.SideType, pair string, size, level, stop, profit float64,
	isStop bool) ([]model.Order, error) {
	if m.dataOnly {
		return nil, m.errDataOnly("protected pending orders")
	}
	lots, units, err := m.dealSize(pair, size)
	if err != nil {
		return nil, err
	}
	symbol := m.instruments[pair]
	level, stop, profit = symbol.price(level), symbol.price(stop), symbol.price(profit)

	unlock := m.lockPair(pair)
	defer unlock()
	if err := m.settleLocked(m.ctx, pair); err != nil {
		log.Warnf("[MT5] %v", err)
	}
	if err := m.checkEntry(side, pair, units, level, stop, profit); err != nil {
		return nil, err
	}
	order, ticket, err := m.place(side, pair, lots, units, level, stop, profit, isStop)
	if err != nil {
		return nil, err
	}
	// the position doesn't exist yet: the legs follow the order until it opens
	legs := m.protect(ticket, nil, side, pair, units, stop, profit)
	return append([]model.Order{order}, legs...), nil
}

// CreateOrderOCO protects the bot's open positions of pair with a take profit at
// price and a stop loss at stop, the levels MT5 attaches to each position
// (TRADE_ACTION_SLTP): once they close the positions the other leg goes with
// them, as the other leg of an OCO order. The bot's positions of pair must be
// on the side opposite to side and sum size units (within half a step), and
// have no level yet: otherwise ErrNotSupported or ErrInvalidQuantity. The
// legs are anchored to the oldest position. The stop loss closes at the
// market, so stopLimit is not used.
func (m *MT5) CreateOrderOCO(side model.SideType, pair string, size, price, stop,
	_ float64) ([]model.Order, error) {
	if m.dataOnly {
		return nil, m.errDataOnly("CreateOrderOCO")
	}
	lots, units, err := m.dealSize(pair, size)
	if err != nil {
		return nil, err
	}
	fail := func(err error) ([]model.Order, error) {
		return nil, &OrderError{Err: err, Pair: pair, Quantity: units}
	}
	symbol := m.instruments[pair]
	price, stop = symbol.price(price), symbol.price(stop)

	unlock := m.lockPair(pair)
	defer unlock()
	if err := m.settleLocked(m.ctx, pair); err != nil {
		log.Warnf("[MT5] %v", err)
	}

	held := m.botPositions(pair)
	if len(held) == 0 {
		return fail(fmt.Errorf("mt5: OCO orders only protect an open position, the bot holds none of %s: %w", pair,
			ErrNotSupported))
	}
	heldSide := mt5Side(held[0].Type)
	var steps int64
	for _, position := range held {
		steps += symbol.steps(position.Volume)
		switch {
		case mt5Side(position.Type) != heldSide:
			return fail(fmt.Errorf("mt5: the bot holds %s on both sides, not netted yet: %w", pair,
				ErrNotSupported))
		case position.SL > 0 || position.TP > 0:
			return fail(fmt.Errorf("mt5: the position %d of %s is protected already (stop loss %v, take profit %v), "+
				"move its levels with UpdateProtection or cancel them first: %w", position.id(), pair, position.SL,
				position.TP, ErrNotSupported))
		}
	}
	switch {
	case heldSide == side:
		return fail(fmt.Errorf("mt5: a %s OCO order would add to the %s position of %s, it can only close it: %w",
			side, heldSide, pair, ErrNotSupported))
	case steps != symbol.steps(lots):
		return fail(fmt.Errorf("mt5: the stop loss and the take profit close the whole position of %s, %v, not "+
			"%v: %w", pair, symbol.units(symbol.lotsOf(steps)), units, ErrInvalidQuantity))
	}
	if err := checkProtection(heldSide, 0, stop, price); err != nil {
		return fail(err)
	}

	tickets := make([]int64, 0, len(held))
	for i, position := range held {
		if err := m.setLevels(m.ctx, position, stop, price); err != nil {
			// the levels set on the positions before: removed, so that none is
			// protected without legs
			for _, done := range held[:i] {
				if undo := m.setLevels(m.ctx, done, 0, 0); undo != nil {
					log.Errorf("[MT5] removing the levels of the position %d: %v", done.id(), undo)
				}
			}
			return fail(err)
		}
		tickets = append(tickets, position.id())
	}
	anchor := tickets[0]
	m.reprotect(anchor)
	if len(tickets) == 1 {
		tickets = nil
	}
	return m.protect(anchor, tickets, heldSide, pair, units, stop, price), nil
}

// reprotect moves the position opened by ticket to its next protection when
// the adapter knows legs of the current one (canceled, or removed from the
// position), so that new legs never share their ExchangeIDs: the order
// controller may still follow the old ones, and would count a fill twice.
func (m *MT5) reprotect(ticket int64) {
	b := m.book
	b.knownMtx.Lock()
	defer b.knownMtx.Unlock()
	generation := b.generations[ticket]
	for _, leg := range []string{mt5TakeProfit, mt5StopLoss} {
		if _, ok := b.known[mt5LegID(leg, ticket, generation)]; ok {
			b.generations[ticket] = generation + 1
			return
		}
	}
}

// generation returns the protection in use of the position opened by ticket.
func (m *MT5) generation(ticket int64) int {
	b := m.book
	b.knownMtx.Lock()
	defer b.knownMtx.Unlock()
	return b.generations[ticket]
}

// seeGeneration records that the position opened by ticket had a protection
// number generation, eg: a leg of a previous run rebuilt.
func (m *MT5) seeGeneration(ticket int64, generation int) {
	b := m.book
	b.knownMtx.Lock()
	defer b.knownMtx.Unlock()
	if generation > b.generations[ticket] {
		b.generations[ticket] = generation
	}
}

// protect returns and remembers the legs of the levels protecting the position
// opened on side by ticket (and the other positions of tickets): the take
// profit first, then the stop loss, the ones above 0, of the opposite side,
// sized size units, at their level (Stop set on the stop loss).
func (m *MT5) protect(ticket int64, tickets []int64, side model.SideType, pair string, size, stop,
	profit float64) []model.Order {
	now := time.Now().UTC()
	generation := m.generation(ticket)
	var legs []model.Order
	for _, level := range []struct {
		leg   string
		price float64
	}{{mt5TakeProfit, profit}, {mt5StopLoss, stop}} {
		if level.price <= 0 {
			continue
		}
		order := mt5Leg(level.leg, ticket, generation, pair)
		order.CreatedAt, order.UpdatedAt = now, now
		order.Side, order.Price, order.Quantity = mt5Opposite(side), level.price, size
		if level.leg == mt5StopLoss {
			price := level.price
			order.Stop = &price
		}
		m.remember(mt5Known{ticket: ticket, tickets: tickets, leg: level.leg, generation: generation, order: order})
		legs = append(legs, order)
	}
	return legs
}

// mt5Leg returns the order of a leg of the protection number generation of the
// position opened by ticket, pending: its ExchangeID, GroupID and type
// (LIMIT_MAKER for the take profit, STOP_LOSS for the stop loss), without side,
// price or quantity.
func mt5Leg(leg string, ticket int64, generation int, pair string) model.Order {
	group := mt5GroupID(ticket, generation)
	order := model.Order{
		ExchangeID: mt5LegID(leg, ticket, generation),
		Pair:       pair,
		Type:       model.OrderTypeLimitMaker,
		Status:     model.OrderStatusTypeNew,
		GroupID:    &group,
	}
	if leg == mt5StopLoss {
		order.Type = model.OrderTypeStopLoss
	}
	return order
}

func (m *MT5) remember(known mt5Known) {
	b := m.book
	b.knownMtx.Lock()
	defer b.knownMtx.Unlock()
	b.known[known.order.ExchangeID] = known
}

// canceled records that Cancel deleted an order.
func (m *MT5) canceled(known mt5Known) {
	b := m.book
	b.knownMtx.Lock()
	defer b.knownMtx.Unlock()
	if stored, ok := b.known[known.order.ExchangeID]; ok {
		known = stored
	}
	known.canceled = true
	known.order.UpdatedAt = time.Now().UTC()
	b.known[known.order.ExchangeID] = known
}

func (m *MT5) known(id int64) (mt5Known, bool) {
	b := m.book
	b.knownMtx.Lock()
	defer b.knownMtx.Unlock()
	known, ok := b.known[id]
	return known, ok
}

// canceledLegs records both legs of the protection in use of the position
// opened by ticket as canceled, the ones the adapter doesn't know rebuilt from
// the entry: its type, volume (lots) and levels.
func (m *MT5) canceledLegs(ticket int64, tickets []int64, pair string, kind int, volume, sl, tp float64) {
	units := m.instrument(pair).units(volume)
	generation := m.generation(ticket)
	for _, level := range []struct {
		leg   string
		price float64
	}{{mt5TakeProfit, tp}, {mt5StopLoss, sl}} {
		known, ok := m.known(mt5LegID(level.leg, ticket, generation))
		if !ok {
			known = mt5Known{ticket: ticket, tickets: tickets, leg: level.leg, generation: generation,
				order: mt5Leg(level.leg, ticket, generation, pair)}
			mt5LegFrom(&known.order, level.leg, kind, units, level.price)
			m.remember(known)
		}
		m.canceled(known)
	}
}

// Order returns an order from the book, see OrderSince.
func (m *MT5) Order(pair string, id int64) (model.Order, error) {
	return m.OrderSince(pair, id, time.Time{})
}

// OrderSince returns an order created at since, from the book, see
// service.OrderHistory:
//  1. canceled by Cancel: CANCELED;
//  2. a leg (known, or rebuilt: a hashed id matching mt5LegID with a ticket of
//     the bot in the book, see findLeg): legOrder;
//  3. a market order known: FILLED as returned;
//  4. a ticket: pending while in the book (NEW or PARTIALLY_FILLED); FILLED
//     when a position of its ticket, or its deals, are known, at the average
//     price of its deals (or the open price of its position), UpdatedAt the
//     time of the last one; else CANCELED, EXPIRED or REJECTED as its state in
//     the orders of the history;
//  5. a ticket the book doesn't know: ensureHistory(since), one read at most,
//     then step 4 again; a pending order of the adapter gone from the book
//     stays NEW while its outcome is awaited (mt5ResolveWait), then CANCELED;
//     any other is ErrOrderNotFound.
//
// Orders rebuilt for a previous run are remembered. Only step 5 may send
// requests.
func (m *MT5) OrderSince(pair string, id int64, since time.Time) (model.Order, error) {
	if m.dataOnly {
		return model.Order{}, m.errDataOnly("Order")
	}
	known, isKnown := m.known(id)
	switch {
	case isKnown && known.canceled:
		order := known.order
		order.Status = model.OrderStatusTypeCanceled
		return order, nil
	case isKnown && known.leg != "":
		return m.legOrder(known, since)
	case isKnown && known.order.Type == model.OrderTypeMarket:
		return known.order, nil // market orders are FILLED on return
	case !mt5IsTicket(id):
		rebuilt, ok := m.findLeg(pair, id)
		if !ok {
			if err := m.ensureHistory(m.ctx, since); err != nil {
				return model.Order{}, err
			}
			if rebuilt, ok = m.findLeg(pair, id); !ok {
				return model.Order{}, ErrOrderNotFound
			}
		}
		m.seeGeneration(rebuilt.ticket, rebuilt.generation)
		m.remember(rebuilt)
		return m.legOrder(rebuilt, since)
	}

	order, found := m.ticketOrder(pair, id, known, isKnown)
	if !found {
		if err := m.ensureHistory(m.ctx, since); err != nil {
			return model.Order{}, err
		}
		order, found = m.ticketOrder(pair, id, known, isKnown)
	}
	if !found {
		if !isKnown {
			return model.Order{}, ErrOrderNotFound
		}
		b := m.book
		b.mtx.RLock()
		_, waiting := b.gone[id]
		b.mtx.RUnlock()
		order = known.order
		if !waiting {
			order.Status, order.UpdatedAt = model.OrderStatusTypeCanceled, time.Now().UTC()
		}
		return order, nil
	}
	if !isKnown && mt5OpenStatus(order.Status) {
		m.remember(mt5Known{ticket: id, order: order})
	}
	return order, nil
}

// ticketOrder is step 4 of OrderSince, from the book: the order of ticket id,
// known or else rebuilt. found is false when the book has no trace of it.
func (m *MT5) ticketOrder(pair string, id int64, known mt5Known, isKnown bool) (model.Order, bool) {
	b := m.book
	b.mtx.RLock()
	defer b.mtx.RUnlock()

	order := model.Order{ExchangeID: id, Pair: pair}
	if isKnown {
		order = known.order
	}

	if pending, ok := b.orders[id]; ok {
		book := m.pendingOrder(pending)
		if !isKnown {
			return book, true
		}
		order.Status, order.Price = book.Status, book.Price
		if book.Stop != nil {
			order.Stop = book.Stop
		}
		return order, true
	}

	history, inHistory := b.history[id]
	if !isKnown && inHistory {
		order = m.pendingOrder(history)
	}

	// filled: its deals, or the position it opened
	deals := b.byOrder[id]
	position, open := b.positions[id]
	if !open {
		position, open = b.closed[id]
	}
	entry, entered := b.entries[id]
	if len(deals) > 0 || open || entered {
		symbol := m.instrument(pair)
		var volume, value float64
		var last time.Time
		for _, deal := range deals {
			volume, value = volume+deal.Volume, value+deal.Volume*deal.Price
			if deal.at().After(last) {
				last = deal.at()
			}
		}
		switch {
		case volume > 0:
			order.Price = symbol.price(value / volume)
			order.UpdatedAt = last
		case entered:
			order.Price, order.UpdatedAt = symbol.price(entry.Price), entry.at()
		default:
			order.Price, order.UpdatedAt = symbol.price(position.PriceOpen), position.opened()
		}
		if !isKnown {
			switch {
			case len(deals) > 0:
				order.Side = mt5Side(deals[0].Type)
				order.Quantity = symbol.units(volume)
			case entered:
				order.Side, order.Quantity = mt5Side(entry.Type), symbol.units(entry.Volume)
			default:
				order.Side = mt5Side(position.Type)
				order.Quantity = symbol.units(m.openingVolume(position))
			}
			if order.Type == "" {
				order.Type = model.OrderTypeMarket
			}
		}
		order.Status = model.OrderStatusTypeFilled
		return order, true
	}

	if inHistory {
		order.UpdatedAt = history.done()
		order.Status = mt5Status(history.State)
		switch order.Status {
		case model.OrderStatusTypeFilled:
			if price := m.instrument(pair).price(history.PriceCurrent); price > 0 {
				order.Price = price
			}
		case model.OrderStatusTypeNew, model.OrderStatusTypePartiallyFilled:
			order.Status = model.OrderStatusTypeCanceled // out of the book without fill
		}
		return order, true
	}

	if final, ok := b.finals[id]; ok && final.state == mt5StateCanceled {
		if !isKnown {
			order = m.pendingOrder(final.order)
		}
		order.Status, order.UpdatedAt = model.OrderStatusTypeCanceled, final.at.UTC()
		return order, true
	}
	return model.Order{}, false
}

// openingVolume returns the lots a position opened with: its volume plus the
// ones of its exits. The caller holds the lock of the book.
func (m *MT5) openingVolume(position mt5Position) float64 {
	lots := position.Volume
	for _, exit := range m.book.exits[position.id()] {
		lots += exit.Volume
	}
	return lots
}

// pendingOrder converts a pending order into a xtrader one: its ticket, setup
// time, side and type (a stop is STOP_LOSS with Stop at its price), the status
// of its state, its price and its units.
func (m *MT5) pendingOrder(order mt5Order) model.Order {
	symbol := m.instrument(order.Symbol)
	volume := order.VolumeInitial
	if volume <= 0 {
		volume = order.VolumeCurrent
	}
	created := order.setup()
	converted := model.Order{
		ExchangeID: order.Ticket,
		CreatedAt:  created,
		UpdatedAt:  created,
		Pair:       order.Symbol,
		Side:       mt5Side(order.Type),
		Type:       mt5OrderType(order.Type),
		Status:     mt5Status(order.State),
		Price:      symbol.price(order.PriceOpen),
		Quantity:   symbol.units(volume),
	}
	if converted.Type == model.OrderTypeStopLoss {
		level := converted.Price
		converted.Stop = &level
	}
	return converted
}

// findLeg rebuilds a leg of a previous run: the leg whose ExchangeID is id, of a
// ticket of the bot in the book (pending, open, closed or in the history), or
// of a ticket the adapter knows. The legs of an OCO order over several
// positions protect the ones open of the same side with the same level.
func (m *MT5) findLeg(pair string, id int64) (mt5Known, bool) {
	b := m.book
	seen := make(map[int64]bool)
	var tickets []int64
	add := func(ticket int64) {
		if ticket > 0 && !seen[ticket] {
			seen[ticket] = true
			tickets = append(tickets, ticket)
		}
	}
	b.knownMtx.Lock()
	for _, known := range b.known {
		if known.leg == "" {
			add(known.ticket)
		}
	}
	b.knownMtx.Unlock()

	b.mtx.RLock()
	for ticket, order := range b.orders {
		if order.Magic == m.magic {
			add(ticket)
		}
	}
	for id, position := range b.positions {
		if position.Magic == m.magic {
			add(id)
		}
	}
	for id, position := range b.closed {
		if position.Magic == m.magic {
			add(id)
		}
	}
	for id, entry := range b.entries {
		if entry.Magic == m.magic {
			add(id)
		}
	}
	for ticket, order := range b.history {
		if order.Magic == m.magic && (mt5PendingKind(order.Type) || order.PositionID == ticket) {
			add(ticket)
		}
	}
	b.mtx.RUnlock()

	// the first protections first, the usual ones
	for generation := 0; generation < mt5MaxProtections; generation++ {
		for _, ticket := range tickets {
			for _, leg := range []string{mt5TakeProfit, mt5StopLoss} {
				if mt5LegID(leg, ticket, generation) != id {
					continue
				}
				known := mt5Known{ticket: ticket, leg: leg, generation: generation,
					order: mt5Leg(leg, ticket, generation, pair)}
				known.tickets = m.protectedWith(ticket, leg)
				return known, true
			}
		}
	}
	return mt5Known{}, false
}

// protectedWith returns the positions protected with the open position ticket
// by an OCO order, the open ones of the bot of the same symbol and side with
// the same level of leg, the anchor first; nil for the anchor alone.
func (m *MT5) protectedWith(ticket int64, leg string) []int64 {
	b := m.book
	b.mtx.RLock()
	defer b.mtx.RUnlock()
	anchor, open := b.positions[ticket]
	level := mt5Level(anchor.SL, anchor.TP, leg)
	if !open || level <= 0 {
		return nil
	}
	tickets := []int64{ticket}
	for id, position := range b.positions {
		if id != ticket && position.Magic == m.magic && position.Symbol == anchor.Symbol &&
			position.Type == anchor.Type && mt5Level(position.SL, position.TP, leg) == level {
			tickets = append(tickets, id)
		}
	}
	if len(tickets) == 1 {
		return nil
	}
	slices.Sort(tickets[1:])
	return tickets
}

// The outcomes of a leg read from the book, see legState.
const (
	mt5LegResolved = iota // its status is known
	mt5LegWaiting         // its positions left the book, their exits are awaited in the history
	mt5LegUnknown         // the book has no trace of its entry or of the exits of its positions
)

// legOrder returns a leg protecting the positions opened by known.protected()
// (CANCELED when of a protection replaced since, see reprotect), see legState;
// with no trace of its entry or of the exits of its positions, even after
// reading the history since since, it is FILLED by the exits by its level known,
// or else CANCELED.
func (m *MT5) legOrder(known mt5Known, since time.Time) (model.Order, error) {
	if known.generation < m.generation(known.ticket) {
		order := known.order
		order.Status, order.UpdatedAt = model.OrderStatusTypeCanceled, time.Now().UTC()
		return order, nil
	}
	order, state := m.legState(known, false)
	if state != mt5LegUnknown {
		return order, nil
	}
	if err := m.ensureHistory(m.ctx, since); err != nil {
		return model.Order{}, err
	}
	order, _ = m.legState(known, true)
	return order, nil
}

// legState returns a leg from the book:
//   - pending with its entry: NEW, CANCELED when the entry has no level for it
//     any more;
//   - with one of its positions open: NEW while one has its level, CANCELED
//     when none has (the protection removed);
//   - with its positions closed: FILLED at the average price of the exits by
//     its level (reason SL for the stop loss, TP for the take profit), their
//     volume, UpdatedAt the last one; CANCELED when they closed otherwise (a
//     market order, a close-by, a stop out, the other leg), or when its entry
//     left the book without fill.
//
// A position closed whose exits are not all in the history leaves the leg NEW
// while they are awaited, or unknown after, as is an entry without trace;
// final settles those, as legOrder does. A leg rebuilt without side or
// quantity takes them from its entry: the opposite side and its units.
func (m *MT5) legState(known mt5Known, final bool) (model.Order, int) {
	b := m.book
	b.mtx.RLock()
	defer b.mtx.RUnlock()

	order := known.order
	now := time.Now().UTC()
	pair := order.Pair

	// pending with its entry, or open with its positions, while its level is set
	if pending, ok := b.orders[known.ticket]; ok {
		level := mt5Level(pending.SL, pending.TP, known.leg)
		mt5LegFrom(&order, known.leg, pending.Type, m.instrument(pending.Symbol).units(pending.VolumeInitial), level)
		order.Status = model.OrderStatusTypeNew
		if level <= 0 {
			order.Status, order.UpdatedAt = model.OrderStatusTypeCanceled, now
		}
		return order, mt5LegResolved
	}
	var open []mt5Position
	for _, ticket := range known.protected() {
		if position, ok := b.positions[ticket]; ok {
			open = append(open, position)
		}
	}
	if len(open) > 0 {
		var units float64
		level := 0.0
		for _, position := range open {
			units += m.instrument(position.Symbol).units(position.Volume)
			level = math.Max(level, mt5Level(position.SL, position.TP, known.leg))
		}
		mt5LegFrom(&order, known.leg, open[0].Type, units, level)
		order.Status = model.OrderStatusTypeNew
		if level <= 0 {
			order.Status, order.UpdatedAt = model.OrderStatusTypeCanceled, now
		}
		return order, mt5LegResolved
	}

	// closed: by its level, or otherwise
	reason := mt5ReasonTP
	if known.leg == mt5StopLoss {
		reason = mt5ReasonSL
	}
	symbol := m.instrument(pair)
	state := mt5LegResolved
	var matched, volume, value float64
	var last, lastMatched time.Time
	kind := -1 // type of the positions, when known
	for _, ticket := range known.protected() {
		exits := b.exits[ticket]
		entry, entered := b.entries[ticket]
		position, closed := b.closed[ticket]
		switch {
		case entered:
			kind = entry.Type
		case closed:
			kind = position.Type
		}
		if !entered && !closed && len(exits) == 0 {
			// never opened: the pending entry out of the book, or no trace
			if history, ok := b.history[ticket]; ok && history.State != mt5StateFilled {
				kind = history.Type
				last = mt5Later(last, history.done())
				continue
			}
			if final, ok := b.finals[ticket]; ok && final.state == mt5StateCanceled {
				kind = final.order.Type
				last = mt5Later(last, final.at)
				continue
			}
			if _, waiting := b.gone[ticket]; waiting {
				state = max(state, mt5LegWaiting)
			} else {
				state = mt5LegUnknown
			}
			continue
		}
		if !m.covered(ticket) {
			if _, waiting := b.gone[ticket]; waiting {
				state = max(state, mt5LegWaiting)
			} else {
				state = mt5LegUnknown
			}
		}
		for _, exit := range exits {
			last = mt5Later(last, exit.at())
			volume += exit.Volume
			if exit.Reason == reason {
				matched += exit.Volume
				value += exit.Volume * exit.Price
				lastMatched = mt5Later(lastMatched, exit.at())
			}
		}
	}
	if kind >= 0 {
		mt5LegFrom(&order, known.leg, kind, symbol.units(volume), 0)
	}

	if state == mt5LegWaiting || (state == mt5LegUnknown && !final) {
		order.Status = model.OrderStatusTypeNew
		return order, state
	}
	if matched > 0 {
		order.Status = model.OrderStatusTypeFilled
		order.Price = symbol.price(value / matched)
		order.Quantity = symbol.units(matched)
		order.UpdatedAt = lastMatched
		return order, mt5LegResolved
	}
	order.Status = model.OrderStatusTypeCanceled
	order.UpdatedAt = last
	if last.IsZero() {
		order.UpdatedAt = now
	}
	return order, mt5LegResolved
}

func mt5Later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// mt5LegFrom completes a leg rebuilt without side, quantity or level, from the
// entry of type kind (a position or pending type) and units, and sets its level
// when above 0.
func mt5LegFrom(order *model.Order, leg string, kind int, units, level float64) {
	if order.Side == "" {
		order.Side = mt5Opposite(mt5Side(kind))
	}
	if order.Quantity == 0 {
		order.Quantity = units
	}
	if level > 0 {
		order.Price = level
		if leg == mt5StopLoss {
			stop := level
			order.Stop = &stop
		}
	}
}

// mt5Level returns the level of leg among the stop loss sl and the take profit
// tp.
func mt5Level(sl, tp float64, leg string) float64 {
	if leg == mt5StopLoss {
		return sl
	}
	return tp
}

// lookup rebuilds an order unknown to the adapter, eg: of a previous run, from
// the book: a pending ticket, or a leg of the protection in use of a ticket
// pending or open. ok is false when the book doesn't know it.
func (m *MT5) lookup(order model.Order) (mt5Known, bool) {
	id := order.ExchangeID
	b := m.book
	if mt5IsTicket(id) {
		b.mtx.RLock()
		pending, ok := b.orders[id]
		b.mtx.RUnlock()
		if !ok {
			return mt5Known{}, false
		}
		rebuilt := order
		mt5FillOrder(&rebuilt, m.pendingOrder(pending))
		return mt5Known{ticket: id, order: rebuilt}, true
	}

	known, ok := m.findLeg(order.Pair, id)
	if !ok || known.generation < m.generation(known.ticket) {
		return mt5Known{}, false
	}
	b.mtx.RLock()
	pending, isPending := b.orders[known.ticket]
	position, isOpen := b.positions[known.ticket]
	b.mtx.RUnlock()
	rebuilt := order
	mt5FillOrder(&rebuilt, known.order)
	switch {
	case isPending:
		mt5LegFrom(&rebuilt, known.leg, pending.Type, m.instrument(pending.Symbol).units(pending.VolumeInitial),
			0)
	case isOpen:
		mt5LegFrom(&rebuilt, known.leg, position.Type, m.instrument(position.Symbol).units(position.Volume), 0)
	default:
		return mt5Known{}, false
	}
	known.order = rebuilt
	m.seeGeneration(known.ticket, known.generation)
	return known, true
}

// Cancel removes a pending order and the legs protecting the position it would
// open, or removes the protection of the positions or of the pending order of
// a leg (both legs: SLTP with both levels 0 on each position, or MODIFY of the
// pending order keeping its price and expiry). A filled or canceled order is
// ErrOrderNotOpen, in an OrderError.
func (m *MT5) Cancel(order model.Order) error {
	if m.dataOnly {
		return m.errDataOnly("Cancel")
	}
	fail := func(err error) error {
		return &OrderError{Err: err, Pair: order.Pair, Quantity: order.Quantity}
	}
	known, ok := m.known(order.ExchangeID)
	if !ok {
		if known, ok = m.lookup(order); !ok {
			return fail(fmt.Errorf("mt5: order %d is not pending: %w", order.ExchangeID, ErrOrderNotOpen))
		}
	}
	mt5FillOrder(&known.order, order)
	switch {
	case known.canceled, known.leg != "" && known.generation < m.generation(known.ticket):
		return fail(fmt.Errorf("mt5: order %d is canceled already: %w", order.ExchangeID, ErrOrderNotOpen))
	case known.leg != "":
		return m.cancelProtection(known, fail)
	case known.order.Type == model.OrderTypeMarket:
		return fail(fmt.Errorf("mt5: market order %d is filled: %w", order.ExchangeID, ErrOrderNotOpen))
	}

	b := m.book
	b.mtx.RLock()
	pending, isPending := b.orders[known.ticket]
	b.mtx.RUnlock()
	if !isPending {
		return fail(fmt.Errorf("mt5: order %d is not pending: %w", order.ExchangeID, ErrOrderNotOpen))
	}
	if err := m.removePending(m.ctx, pending); err != nil {
		return fail(err)
	}
	m.remember(known)
	m.canceled(known)
	m.canceledLegs(known.ticket, nil, pending.Symbol, pending.Type, pending.VolumeInitial, pending.SL, pending.TP)
	return nil
}

// cancelProtection removes the stop loss and the take profit of the positions
// of the leg known, or of the pending order opening its position.
func (m *MT5) cancelProtection(known mt5Known, fail func(error) error) error {
	b := m.book
	b.mtx.RLock()
	pending, isPending := b.orders[known.ticket]
	var open []mt5Position
	for _, ticket := range known.protected() {
		if position, ok := b.positions[ticket]; ok {
			open = append(open, position)
		}
	}
	b.mtx.RUnlock()

	switch {
	case isPending:
		if err := m.modifyPending(m.ctx, pending, pending.PriceOpen, 0, 0); err != nil {
			return fail(err)
		}
		m.remember(known)
		m.canceledLegs(known.ticket, known.tickets, pending.Symbol, pending.Type, pending.VolumeInitial, pending.SL,
			pending.TP)
		return nil
	case len(open) == 0:
		return fail(fmt.Errorf("mt5: the position of order %d is closed: %w", known.order.ExchangeID,
			ErrOrderNotOpen))
	}
	for _, position := range open {
		if err := m.setLevels(m.ctx, position, 0, 0); err != nil {
			return fail(err)
		}
	}
	m.remember(known)
	first := open[0]
	m.canceledLegs(known.ticket, known.tickets, first.Symbol, first.Type, first.Volume, first.SL, first.TP)
	return nil
}

// UpdateProtection moves a leg to level: the stop loss or the take profit of
// the positions it protects, or of the pending order opening its position, see
// service.ProtectionBroker. MT5 sets both levels together, so the other one is
// sent again where it is; the levels are checked with checkProtection first. A
// leg whose level was removed, eg: by hand in the terminal, is reported
// CANCELED (legState) and is ErrOrderNotOpen here.
func (m *MT5) UpdateProtection(leg model.Order, level float64) (model.Order, error) {
	if m.dataOnly {
		return model.Order{}, m.errDataOnly("UpdateProtection")
	}
	fail := func(err error) (model.Order, error) {
		return model.Order{}, &OrderError{Err: err, Pair: leg.Pair, Quantity: leg.Quantity}
	}
	known, ok := m.known(leg.ExchangeID)
	if !ok {
		if known, ok = m.lookup(leg); !ok {
			if mt5IsTicket(leg.ExchangeID) {
				return fail(fmt.Errorf("mt5: order %d is not a stop loss or a take profit: %w", leg.ExchangeID,
					ErrNotSupported))
			}
			return fail(fmt.Errorf("mt5: the position of order %d is closed: %w", leg.ExchangeID, ErrOrderNotOpen))
		}
	}
	mt5FillOrder(&known.order, leg)
	switch {
	case known.leg == "":
		return fail(fmt.Errorf("mt5: order %d is not a stop loss or a take profit: %w", leg.ExchangeID,
			ErrNotSupported))
	case known.canceled, known.generation < m.generation(known.ticket):
		return fail(fmt.Errorf("mt5: order %d is canceled: %w", leg.ExchangeID, ErrOrderNotOpen))
	}

	b := m.book
	b.mtx.RLock()
	pending, isPending := b.orders[known.ticket]
	var open []mt5Position
	for _, ticket := range known.protected() {
		if position, ok := b.positions[ticket]; ok {
			open = append(open, position)
		}
	}
	b.mtx.RUnlock()

	var side model.SideType // of the position protected
	var entry, current, stop, profit float64
	var symbol mt5Symbol
	switch {
	case isPending:
		side, entry, symbol = mt5Side(pending.Type), pending.PriceOpen, m.instrument(pending.Symbol)
		current, stop, profit = mt5Level(pending.SL, pending.TP, known.leg), pending.SL, pending.TP
	case len(open) > 0:
		side, symbol = mt5Side(open[0].Type), m.instrument(open[0].Symbol)
		current, stop, profit = mt5Level(open[0].SL, open[0].TP, known.leg), open[0].SL, open[0].TP
	default:
		return fail(fmt.Errorf("mt5: the position of order %d is closed: %w", leg.ExchangeID, ErrOrderNotOpen))
	}
	if current <= 0 {
		// reported CANCELED already: the order controller no longer follows it,
		// so it is never set again; CreateOrderOCO protects anew
		return fail(fmt.Errorf("mt5: the level of order %d was removed: %w", leg.ExchangeID, ErrOrderNotOpen))
	}

	level = symbol.price(level)
	if known.leg == mt5StopLoss {
		stop = level
	} else {
		profit = level
	}
	if err := checkProtection(side, entry, stop, profit); err != nil {
		return fail(err)
	}

	if isPending {
		if err := m.modifyPending(m.ctx, pending, pending.PriceOpen, stop, profit); err != nil {
			return fail(err)
		}
	}
	for _, position := range open {
		positionStop, positionProfit := position.SL, position.TP
		if known.leg == mt5StopLoss {
			positionStop = level
		} else {
			positionProfit = level
		}
		if err := m.setLevels(m.ctx, position, positionStop, positionProfit); err != nil {
			return fail(err)
		}
	}

	moved := known.order
	moved.Price, moved.UpdatedAt = level, time.Now().UTC()
	if known.leg == mt5StopLoss {
		trigger := level
		moved.Stop = &trigger
	}
	known.order = moved
	m.remember(known)
	return moved, nil
}

// UpdateLimit moves a pending limit order to limit, see
// service.ProtectionBroker: TRADE_ACTION_MODIFY with its levels as they are,
// good till canceled. Only the limit orders: the others are ErrNotSupported.
func (m *MT5) UpdateLimit(order model.Order, limit float64) (model.Order, error) {
	if m.dataOnly {
		return model.Order{}, m.errDataOnly("UpdateLimit")
	}
	fail := func(err error) (model.Order, error) {
		return model.Order{}, &OrderError{Err: err, Pair: order.Pair, Quantity: order.Quantity}
	}
	known, ok := m.known(order.ExchangeID)
	if !ok {
		if known, ok = m.lookup(order); !ok {
			if !mt5IsTicket(order.ExchangeID) {
				return fail(fmt.Errorf("mt5: order %d is not a pending limit order: %w", order.ExchangeID,
					ErrNotSupported))
			}
			return fail(fmt.Errorf("mt5: order %d is not pending: %w", order.ExchangeID, ErrOrderNotOpen))
		}
	}
	mt5FillOrder(&known.order, order)
	switch {
	case known.canceled:
		return fail(fmt.Errorf("mt5: order %d is canceled: %w", order.ExchangeID, ErrOrderNotOpen))
	case known.leg != "" || known.order.Type == model.OrderTypeMarket:
		return fail(fmt.Errorf("mt5: order %d is not a pending limit order: %w", order.ExchangeID,
			ErrNotSupported))
	case limit <= 0:
		return fail(fmt.Errorf("%w: limit %v", ErrInvalidProtection, limit))
	}

	b := m.book
	b.mtx.RLock()
	pending, isPending := b.orders[known.ticket]
	b.mtx.RUnlock()
	switch {
	case !isPending:
		return fail(fmt.Errorf("mt5: order %d is not pending: %w", order.ExchangeID, ErrOrderNotOpen))
	case pending.Type != mt5TypeBuyLimit && pending.Type != mt5TypeSellLimit:
		return fail(fmt.Errorf("mt5: order %d is not a pending limit order: %w", order.ExchangeID,
			ErrNotSupported))
	}

	limit = m.instrument(pending.Symbol).price(limit)
	if pending.SL > 0 || pending.TP > 0 {
		if err := checkProtection(mt5Side(pending.Type), limit, pending.SL, pending.TP); err != nil {
			return fail(err)
		}
	}
	pending.TypeTime, pending.TimeExpiration = mt5TimeGTC, 0
	if err := m.modifyPending(m.ctx, pending, limit, pending.SL, pending.TP); err != nil {
		return fail(err)
	}

	moved := known.order
	moved.Price, moved.UpdatedAt = limit, time.Now().UTC()
	known.order = moved
	m.remember(known)
	return moved, nil
}

// mt5OpenStatus reports whether a status is the one of an order still open.
func mt5OpenStatus(status model.OrderStatusType) bool {
	switch status {
	case model.OrderStatusTypeNew, model.OrderStatusTypePartiallyFilled, model.OrderStatusTypePendingCancel:
		return true
	}
	return false
}

// mt5FillOrder sets the fields of order left empty from the ones of from.
func mt5FillOrder(order *model.Order, from model.Order) {
	if order.Pair == "" {
		order.Pair = from.Pair
	}
	if order.Side == "" {
		order.Side = from.Side
	}
	if order.Type == "" {
		order.Type = from.Type
	}
	if order.Price == 0 {
		order.Price = from.Price
	}
	if order.Quantity == 0 {
		order.Quantity = from.Quantity
	}
	if order.CreatedAt.IsZero() {
		order.CreatedAt = from.CreatedAt
	}
	if order.UpdatedAt.IsZero() {
		order.UpdatedAt = from.UpdatedAt
	}
	if order.Stop == nil {
		order.Stop = from.Stop
	}
	if order.GroupID == nil {
		order.GroupID = from.GroupID
	}
}
