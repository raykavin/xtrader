package llm

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

// Who placed a pending order, see PendingOrder.PlacedBy: the model itself, or
// someone else, eg: the trader from the dashboard, the order found on the pair.
const (
	PlacedByModel  = "model"
	PlacedByTrader = "trader"
)

// legsWindow is how long after its entry the legs of a pending order found
// among the stored orders may have been created, see adoptPending: the broker
// places them with it.
const legsWindow = 5 * time.Second

// pendingOrder is the pending entry of the Trader on a pair, see
// Config.PendingOrders: the action placing one of its kind (eg: BUY_LIMIT), its
// entry order and its legs at the broker, the stop loss and the take profit of
// the position it opens once filled, when it was placed (or found), who placed
// it, and the start of the next window of the news block, if one is known (see
// guardPending), when it is canceled. canceling tells its cancel was sent, its
// outcome not known yet.
type pendingOrder struct {
	action    Action
	entry     model.Order
	legs      []model.Order
	since     time.Time
	placedBy  string
	blockAt   time.Time
	canceling bool
}

// pendingKindOf returns the type of the pending entry order, limit or stop,
// none for an order that isn't a pending entry: a leg, of a group, or an order
// at the market.
func pendingKindOf(order model.Order) string {
	if order.GroupID != nil {
		return ""
	}
	switch order.Type {
	case model.OrderTypeLimit:
		return PendingLimit
	case model.OrderTypeStopLoss, model.OrderTypeStopLossLimit:
		return PendingStop
	}
	return ""
}

// price returns the entry of the pending order: the price of a limit, the
// trigger of a stop.
func (p *pendingOrder) price() float64 {
	if p.action.pendingKind() == PendingStop && p.entry.Stop != nil {
		return *p.entry.Stop
	}
	return p.entry.Price
}

// levels returns the stop loss and the take profit of the pending order, 0 for
// none.
func (p *pendingOrder) levels() protection {
	return (&trade{legs: p.legs}).protection()
}

// describe names the pending order for the logs and the notes, eg: the
// BUY_LIMIT of 0.01 at 2650.5 (#12).
func (p *pendingOrder) describe() string {
	return fmt.Sprintf("the %s of %g at %g (#%d)", p.action, p.entry.Quantity, p.price(), p.entry.ID)
}

// context returns the pending order for the context of an analysis of the
// closed candles of df at at, see Input.PendingOrder.
func (p *pendingOrder) context(t *Trader, df *model.Dataframe, at time.Time) *PendingOrder {
	nan := number(math.NaN())
	levels := p.levels()
	po := &PendingOrder{Type: p.action, Entry: number(p.price()), StopLoss: nan, TakeProfit: nan,
		Size: number(p.entry.Quantity), Candles: t.heldCandles(df, p.since, at), PlacedBy: p.placedBy}
	if levels.stop > 0 {
		po.StopLoss = number(levels.stop)
	}
	if levels.profit > 0 {
		po.TakeProfit = number(levels.profit)
	}
	return po
}

// pendingOf returns the pending order followed on pair, nil without one.
func (t *Trader) pendingOf(pair string) *pendingOrder {
	return t.pendings[pair]
}

// pendingEvent is what happened to the pending order of a pair, see
// refreshPending: told in the note of the record of the analysis, with the
// orders of it canceled by the Trader.
type pendingEvent struct {
	notes  []string
	orders []model.Order
}

func (e *pendingEvent) note(format string, args ...any) {
	e.notes = append(e.notes, fmt.Sprintf(format, args...))
}

// refreshPending reads again at the broker the pending order of the pair of
// df, with held the side of the position held on it (none when flat), at at
// (see dayClock): filled, it is the position held, followed with its legs;
// ended otherwise (canceled, expired or rejected), it is dropped; still
// waiting while a position is held on the pair, which it would add to, it is
// canceled. Without one followed, a pending entry found open on the pair while
// flat is followed, see adoptPending. What happened is told in ev.
func (t *Trader) refreshPending(df *model.Dataframe, broker service.Broker, st *promptState, held model.SideType,
	at time.Time, ev *pendingEvent) {

	pair := df.Pair
	p := t.pendings[pair]
	if p == nil {
		if held == "" {
			t.adoptPending(pair, st, at, ev)
		}
		return
	}
	t.readPending(pair, broker, p)
	switch status := p.entry.Status; {
	case status == model.OrderStatusTypeFilled || status == model.OrderStatusTypePartiallyFilled &&
		held == p.action.side():
		delete(t.pendings, pair)
		if held != p.action.side() {
			ev.note("%s filled at %g, its position closed since", p.describe(), p.entry.Price)
			return
		}
		tr := t.filled(p, at)
		ev.note("%s filled at %g: the %s position followed with its %s", p.describe(), tr.entry,
			describeSide(tr.side), tr.protection())
		if status == model.OrderStatusTypePartiallyFilled {
			// one position at a time: the rest isn't added to it
			if err := broker.Cancel(p.entry); err != nil {
				ev.note("the rest of %s not canceled: %v", p.describe(), err)
			} else {
				ev.note("the rest of %s canceled: one position at a time on the pair", p.describe())
			}
		}
	case status.Final():
		delete(t.pendings, pair)
		ev.note("%s ended %s at the broker", p.describe(), status)
	case held != "":
		t.cancelPending(pair, broker, p, fmt.Sprintf("a %s position is held on the pair, which it would add to",
			describeSide(held)), ev)
	}
}

// readPending reads the pending order p of pair and its legs at the broker,
// keeping their IDs: one that can't be read is kept as it was.
func (t *Trader) readPending(pair string, broker service.Broker, p *pendingOrder) {
	read := func(order model.Order) model.Order {
		current, err := broker.Order(pair, order.ExchangeID)
		if err != nil {
			log.Warnf("[LLM] %s %s | the pending order %d at the broker: %v", pair, t.cfg.Timeframe, order.ID, err)
			return order
		}
		current.ID = order.ID
		if current.CreatedAt.IsZero() {
			current.CreatedAt = order.CreatedAt
		}
		return current
	}
	p.entry = read(p.entry)
	if p.entry.Status == model.OrderStatusTypePendingCancel {
		p.canceling = true
	}
	for i := range p.legs {
		p.legs[i] = read(p.legs[i])
	}
}

// filled follows the position the pending order p opened, filled at its
// price, from its fill (at when unknown), with its legs still open, and
// returns it.
func (t *Trader) filled(p *pendingOrder, at time.Time) *trade {
	tr := &trade{side: p.action.side(), entry: p.entry.Price, since: at}
	if tr.entry <= 0 {
		tr.entry = p.price()
	}
	if !p.entry.UpdatedAt.IsZero() && p.entry.UpdatedAt.Before(at) {
		tr.since = p.entry.UpdatedAt
	}
	for _, leg := range p.legs {
		if leg.Status == model.OrderStatusTypeNew || leg.Status == model.OrderStatusTypePartiallyFilled {
			tr.legs = append(tr.legs, leg)
		}
	}
	if i := tr.leg(true); i >= 0 {
		tr.stop = legLevel(tr.legs[i])
	}
	t.trades[p.entry.Pair] = tr
	return tr
}

// adoptPending follows the pending entry found open on pair, flat, among the
// stored orders (see SetOrderSource), eg: placed before a restart, or by the
// trader from the dashboard, as positions are: the last one of a type the
// prompt st places, with its legs, placed with it; the model then keeps it,
// moves it or cancels it. Never with a prompt that places none: the pending
// orders of the trader are left to them.
func (t *Trader) adoptPending(pair string, st *promptState, at time.Time, ev *pendingEvent) {
	if t.orders == nil || len(st.pendingOrders) == 0 {
		return
	}
	orders, err := t.orders(pair)
	if err != nil {
		log.Warnf("[LLM] %s | the stored orders, for a pending order: %v", pair, err)
		return
	}
	var entry *model.Order
	for i := range orders {
		order := &orders[i]
		if order.Status != model.OrderStatusTypeNew && order.Status != model.OrderStatusTypePartiallyFilled {
			continue
		}
		if kind := pendingKindOf(*order); kind != "" && st.pends(kind) && (entry == nil || order.ID > entry.ID) {
			entry = order
		}
	}
	if entry == nil {
		return
	}
	p := &pendingOrder{action: pendingAction(entry.Side, pendingKindOf(*entry)), entry: *entry, since: at,
		placedBy: PlacedByTrader}
	if !entry.CreatedAt.IsZero() {
		p.since = entry.CreatedAt
	}
	if t.store().OrderDecision(entry.ID) != "" {
		p.placedBy = PlacedByModel
	}
	for _, order := range orders {
		if order.GroupID == nil || order.Side == entry.Side || order.Status != model.OrderStatusTypeNew &&
			order.Status != model.OrderStatusTypePartiallyFilled || order.CreatedAt.Before(entry.CreatedAt.Add(
			-time.Second)) || order.CreatedAt.After(entry.CreatedAt.Add(legsWindow)) {
			continue
		}
		p.legs = append(p.legs, order)
	}
	slices.SortFunc(p.legs, func(a, b model.Order) int { // the take profit first, as the broker returns them
		return isStopLeg(a) - isStopLeg(b)
	})
	p.blockAt = t.nextNewsBlock(pair, at)
	t.pendings[pair] = p
	log.Infof("[LLM] %s %s | %s found open, placed by the %s, followed with %d legs", pair, t.cfg.Timeframe,
		p.describe(), p.placedBy, len(p.legs))
	ev.note("%s found open on the pair, placed by the %s: followed", p.describe(), p.placedBy)
}

// isStopLeg is 1 for a stop loss leg, 0 for a take profit.
func isStopLeg(leg model.Order) int {
	if leg.Type == model.OrderTypeStopLoss || leg.Type == model.OrderTypeStopLossLimit {
		return 1
	}
	return 0
}

// cancelPending cancels the pending order p of pair, why told in ev, and
// returns whether it was canceled: its legs go with it. Failed, eg: filled
// meanwhile, it stays followed, read again at the next analysis.
func (t *Trader) cancelPending(pair string, broker service.Broker, p *pendingOrder, why string,
	ev *pendingEvent) bool {

	log.Infof("[LLM] %s %s | %s canceled: %s", pair, t.cfg.Timeframe, p.describe(), why)
	if err := broker.Cancel(p.entry); err != nil {
		log.Warnf("[LLM] %s %s | the cancel of %s: %v", pair, t.cfg.Timeframe, p.describe(), err)
		ev.note("%s not canceled (%s): %v", p.describe(), why, err)
		return false
	}
	delete(t.pendings, pair)
	ev.orders = append(ev.orders, p.entry)
	ev.orders = append(ev.orders, p.legs...)
	ev.note("%s canceled: %s", p.describe(), why)
	return true
}

// place places a pending entry of action (BUY_LIMIT, SELL_LIMIT, BUY_STOP or
// SELL_STOP) on the pair of df, of size at entry with its stop loss and take
// profit at the broker, profit 0 for none, follows it, and returns the orders
// placed: the entry, then its legs.
func (t *Trader) place(df *model.Dataframe, broker service.Broker, action Action, size, entry, stop,
	profit float64) ([]model.Order, error) {

	protected, ok := broker.(service.ProtectionBroker)
	if !ok {
		return nil, errors.New("the broker can't place the stop loss and take profit with the order")
	}
	side, kind := action.side(), action.pendingKind()
	log.Infof("[LLM] %s %s | %s | entry %f | size %g | stop %f | take profit %f | close %f", df.Pair,
		t.cfg.Timeframe, action, entry, size, stop, profit, df.Close.Last(0))
	var orders []model.Order
	var err error
	if kind == PendingLimit {
		orders, err = protected.CreateOrderLimitProtected(side, df.Pair, size, entry, stop, profit)
	} else {
		orders, err = protected.CreateOrderStopProtected(side, df.Pair, size, entry, stop, profit)
	}
	if err != nil {
		log.Error(err)
	}
	if len(orders) > 0 && pendingKindOf(orders[0]) == kind && !orders[0].Status.Final() {
		at := t.dayClock(df)
		// the window of the news block it may be filled in, known from now on
		t.pendings[df.Pair] = &pendingOrder{action: action, entry: orders[0], legs: slices.Clone(orders[1:]),
			since: at, placedBy: PlacedByModel, blockAt: t.nextNewsBlock(df.Pair, at)}
	}
	return orders, err
}

// modifyPending moves the pending order p of the pair of df to entry, stop and
// profit (0 for none, the one it has), and returns the orders moved, or placed
// in its place. A limit and its legs are moved where they are, in the order
// that keeps them valid at each step: the leg on the side the entry moves to
// first, then the entry, then the other leg. The entry of a stop, which the
// brokers can't move, is replaced: canceled, and placed again at its new
// levels. A step failing ends the move there, the levels moved kept.
func (t *Trader) modifyPending(df *model.Dataframe, broker service.Broker, p *pendingOrder, entry, stop,
	profit float64) ([]model.Order, error) {

	protected, ok := broker.(service.ProtectionBroker)
	if !ok {
		return nil, errors.New("the broker can't move the levels of a pending order")
	}
	current := p.levels()
	if entry == p.price() && stop == current.stop && profit == current.profit {
		return nil, nil // unchanged: no request
	}
	log.Infof("[LLM] %s %s | MODIFY_PENDING | %s | entry %f -> %f | stop %f -> %f | take profit %f -> %f", df.Pair,
		t.cfg.Timeframe, p.describe(), p.price(), entry, current.stop, stop, current.profit, profit)

	if p.action.pendingKind() == PendingStop && entry != p.price() {
		ev := &pendingEvent{}
		if !t.cancelPending(df.Pair, broker, p, "replaced at its new levels", ev) {
			return nil, fmt.Errorf("the stop order, whose entry the broker can't move, not canceled to be placed "+
				"again: %s", ev.notes[len(ev.notes)-1])
		}
		placed, err := t.place(df, broker, p.action, p.entry.Quantity, entry, stop, profit)
		return append(ev.orders, placed...), err
	}

	var moved []model.Order
	moveLeg := func(stopLeg bool, level float64) error {
		i := (&trade{legs: p.legs}).leg(stopLeg)
		if i < 0 || legLevel(p.legs[i]) == level {
			return nil
		}
		leg, err := protected.UpdateProtection(p.legs[i], level)
		if err != nil {
			return err
		}
		p.legs[i] = leg
		moved = append(moved, leg)
		return nil
	}
	moveEntry := func() error {
		if entry == p.price() {
			return nil
		}
		order, err := protected.UpdateLimit(p.entry, entry)
		if err != nil {
			return err
		}
		p.entry = order
		moved = append(moved, order)
		return nil
	}
	// the entry moving toward the stop loss moves the stop loss first, toward
	// the take profit the take profit first: each level stays on its side
	steps := []func() error{func() error { return moveLeg(false, profit) }, moveEntry,
		func() error { return moveLeg(true, stop) }}
	if sideSign(p.action.side())*(entry-p.price()) < 0 {
		slices.Reverse(steps)
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return moved, err
		}
	}
	return moved, nil
}

// guardPending checks the pending order of the pair of df on a tick, as the
// price of now goes on: filled since, it is the position held; a position held
// on the pair otherwise, or the window of a release of the news block reached
// (see pendingOrder.blockAt), cancels it, notified. The other rules of the
// entries are checked at the analyses, see checkPending.
func (t *Trader) guardPending(df *model.Dataframe, broker service.Broker) {
	p := t.pendings[df.Pair]
	if p == nil || p.canceling {
		return
	}
	asset, _, err := broker.Position(df.Pair)
	if err != nil {
		return // read again on the next tick
	}
	held := heldSide(worth(broker, df.Pair, asset, df.Close.Last(0)), t.cfg.MinPositionValue)
	ev := &pendingEvent{}
	t.refreshPending(df, broker, t.state.Load(), held, t.dayClock(df), ev)
	// the clock of the candles: in a backtest, never the wall clock
	if p = t.pendings[df.Pair]; p != nil && !p.blockAt.IsZero() && !t.dayClock(df).Before(p.blockAt) {
		blocked := t.newsBlocked(df.Pair, t.dayClock(df))
		p.blockAt = time.Time{}
		if blocked != "" {
			t.cancelPending(df.Pair, broker, p, "no entry around the release "+blocked, ev)
		}
	}
	for _, note := range ev.notes {
		t.tell(fmt.Sprintf("%s %s: %s", df.Pair, t.cfg.Timeframe, note))
	}
}

// checkPending checks the pending order p of the pair of df, flat, at the
// analysis of the context in: it is canceled, why returned, once it waited
// Config.PendingExpiry candles, or when an entry of its type isn't among the
// actions allowed now (in.Rules.Actions, as if flat without it), that being
// why when known (eg: a daily limit, the news block, the spread). Kept, it is
// the pending order of the context, the model asked to keep it (WAIT), move it
// or cancel it, and the start of the next window of the news block, if any,
// is kept to cancel it on the ticks (see guardPending).
func (t *Trader) checkPending(df *model.Dataframe, broker service.Broker, p *pendingOrder, in *Input,
	why skip, at time.Time, ev *pendingEvent) bool {

	reason := ""
	switch {
	case p.canceling:
		return true // its cancel sent, the next analysis tells what came of it
	case t.cfg.PendingExpiry > 0 && t.heldCandles(df, p.since, at) >= t.cfg.PendingExpiry:
		reason = fmt.Sprintf("not filled after %d candles, its expiry (llm-pending-expiry)", t.cfg.PendingExpiry)
	case why.code != "":
		reason = "an entry of its type isn't allowed now: " + why.detail
	case !slices.Contains(in.Rules.Actions, p.action):
		reason = fmt.Sprintf("an entry of its type isn't allowed now, %v only", in.Rules.Actions)
	}
	if reason != "" {
		return !t.cancelPending(df.Pair, broker, p, reason, ev)
	}
	p.blockAt = t.nextNewsBlock(df.Pair, at)
	return true
}

// pendingRecord adds to the record of an analysis what happened to the pending
// order of its pair, see pendingEvent: its notes, and the orders canceled.
func pendingRecord(record *ui.Decision, ev *pendingEvent) {
	if ev == nil {
		return
	}
	for _, note := range ev.notes {
		record.Notes = append(record.Notes, ui.DecisionNote{Kind: "text", Text: note})
	}
	for _, order := range ev.orders {
		if order.ID > 0 && !slices.Contains(record.OrderIDs, order.ID) {
			record.OrderIDs = append(record.OrderIDs, order.ID)
		}
	}
}
