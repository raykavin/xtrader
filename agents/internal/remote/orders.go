package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/tools/log"
)

// The leases of order IDs: how many at a time, and how few left lease the
// next ones.
const (
	leaseSize  = 1000
	leaseAgain = 200
)

// ordersPage are the orders read from the controller at a time.
const ordersPage = 1000

// orderIDs leases order IDs, see Client.LeaseOrderIDs.
type orderIDs interface {
	LeaseOrderIDs(ctx context.Context, count int64) (agentapi.OrderIDs, error)
}

// recorder writes the records of the orders, see Outbox.Add.
type recorder interface {
	Add(kind, key string, payload any) error
}

// Notifier is told of the orders the outbox fails to write and of their write
// at last, eg: the chart of the bot, which the controller relays to Telegram.
type Notifier interface {
	Notify(message string)
	OnError(err error)
}

// Orders is the storage of the orders of an agent launched by the controller
// (storage.Storage): in memory, each change written through the outbox, the
// orders numbered with IDs leased from the controller ahead of their need, so
// CreateOrder never waits for it: the order is at the broker already when it
// is called. The orders are handed as copies. A state of an order older than
// the one kept is neither kept nor written, as the controller would refuse it
// (see model.Order.Supersedes), eg: the copy of a move written after a fill.
//
// The order at the broker already, a failure of the outbox, eg: a full disk, is
// never an error of CreateOrder nor of UpdateOrder: the order kept in memory is
// followed by the bot all the same, the failure logged and notified, and its
// last state written again until the outbox takes it, see Close.
type Orders struct {
	outbox recorder
	ids    orderIDs
	// the context of the leases, see lease: done as the agent stops
	leaseCtx    context.Context
	stopLeasing context.CancelFunc

	// write serializes the writes of the orders, so that the last record of
	// an order is its last state, whichever writes it
	write sync.Mutex

	mu        sync.Mutex
	orders    map[int64]model.Order
	next      int64 // the ID of the next order
	last      int64 // the last ID leased
	leasing   bool
	warned    bool
	notifier  Notifier
	unwritten map[int64]bool // the orders whose last state the outbox failed to write
	rewriting bool           // the orders unwritten are written again, see rewrite
	closed    chan struct{}  // closed by Close
	stopped   bool
	// the notices of the orders unwritten, see add: the last one, and the new
	// failures since with the last of them
	notified   time.Time
	unnotified int
	lastFailed error
}

// ordersNotifyEvery bounds how often the orders the outbox fails to write, and
// the ones it writes at last, are notified: the new failures meanwhile are
// notified together, that late at most.
var ordersNotifyEvery = time.Minute

// NewOrders returns the orders of the agent: the ones of the controller, and
// the ones of the outbox not sent yet, newer, with the IDs leased to it. The
// next IDs are leased until ctx is done or Close, see lease.
func NewOrders(ctx context.Context, client *Client, outbox *Outbox) (_ *Orders, err error) {
	o := &Orders{outbox: outbox, ids: client, orders: make(map[int64]model.Order),
		unwritten: make(map[int64]bool), closed: make(chan struct{})}
	o.leaseCtx, o.stopLeasing = context.WithCancel(ctx)
	defer func() {
		if err != nil {
			o.stopLeasing()
		}
	}()
	for after := int64(0); ; {
		page, err := client.Orders(ctx, after, ordersPage)
		if err != nil {
			return nil, fmt.Errorf("remote: the orders: %w", err)
		}
		for _, order := range page {
			o.orders[order.ID] = order
			after = max(after, order.ID)
		}
		if len(page) < ordersPage {
			break
		}
	}
	pending, err := outbox.Pending(agentapi.KindOrder)
	if err != nil {
		return nil, err
	}
	for _, record := range pending {
		var order model.Order
		if err := json.Unmarshal(record.Payload, &order); err != nil {
			return nil, fmt.Errorf("remote: the order %s of the outbox: %w", record.Key, err)
		}
		if known, ok := o.orders[order.ID]; ok && !order.Supersedes(known) {
			continue // older than the one kept, as the controller takes it
		}
		o.orders[order.ID] = order
	}

	ids, err := client.LeaseOrderIDs(ctx, leaseSize)
	if err != nil {
		return nil, fmt.Errorf("remote: the order IDs: %w", err)
	}
	o.next, o.last = ids.First, ids.Last
	for id := range o.orders {
		// an order of the outbox numbered past its lease, the controller
		// out of reach, keeps its ID
		o.next = max(o.next, id+1)
	}
	log.Infof("[SETUP] orders of the controller: %d, the next numbered from %d", len(o.orders), o.next)
	return o, nil
}

var _ storage.Storage = (*Orders)(nil)

// SetNotifier sets who is told of the orders the outbox fails to write.
func (o *Orders) SetNotifier(notifier Notifier) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.notifier = notifier
}

// CreateOrder numbers the order and keeps it. It never fails: the order is at
// the broker already, see Orders.
func (o *Orders) CreateOrder(order *model.Order) error {
	o.write.Lock()
	defer o.write.Unlock()
	o.mu.Lock()
	order.ID = o.next
	o.next++
	if order.ID > o.last && !o.warned {
		o.warned = true
		log.Warnf("remote: the order IDs leased are spent, the controller out of reach: numbered past them")
	}
	if !o.leasing && o.last-order.ID < leaseAgain {
		o.leasing = true
		go o.lease()
	}
	kept, _ := o.keep(*order)
	o.mu.Unlock()
	o.add(kept)
	return nil
}

// lease leases the next order IDs, retried while the controller is out of
// reach, warned every warnEvery while it lasts, until the agent stops (the
// context of NewOrders done, or Close) or the controller refuses it (401),
// which stops it: the next orders are numbered past the IDs leased then, see
// CreateOrder, never leased again.
func (o *Orders) lease() {
	delay := retryDelay
	var warned time.Time
	for {
		ctx, cancel := context.WithTimeout(o.leaseCtx, requestTimeout)
		ids, err := o.ids.LeaseOrderIDs(ctx, leaseSize)
		cancel()
		switch {
		case err == nil:
			o.mu.Lock()
			if ids.Last >= o.next {
				o.next, o.last = max(o.next, ids.First), ids.Last
				o.warned = false
			}
			o.leasing = false
			o.mu.Unlock()
			return
		case errors.Is(err, ErrUnauthorized):
			log.Errorf("remote: the next order IDs: %v: asked no more", err)
			return
		case o.leaseCtx.Err() != nil:
			return
		}
		if time.Since(warned) >= warnEvery {
			warned = time.Now()
			log.Warnf("remote: the next order IDs: %v: asked again until the controller answers, warned every %s",
				err, warnEvery)
		}
		select {
		case <-time.After(delay):
		case <-o.leaseCtx.Done():
			return
		}
		delay = min(2*delay, retryMaxDelay)
	}
}

// UpdateOrder keeps the order as it is, its UpdatedAt the one of the order
// controller, unless older than the one kept (see Orders). It never fails,
// see CreateOrder.
func (o *Orders) UpdateOrder(order *model.Order) error {
	o.write.Lock()
	defer o.write.Unlock()
	o.mu.Lock()
	kept, ok := o.keep(*order)
	o.mu.Unlock()
	if ok {
		o.add(kept)
	}
	return nil
}

// keep keeps a copy of order, its numbers not finite, which JSON can't write,
// eg: the profit of a fill without a price, written as 0; false, and the one
// kept, for a state older than it (see Orders). Must be called while holding
// o.write and o.mu: the outbox takes the states in the order they are kept.
func (o *Orders) keep(order model.Order) (model.Order, bool) {
	kept, fields := clone(order).Finite()
	if len(fields) > 0 {
		log.Errorf("remote: the order %d (%s %s of %s): %s not finite, kept as 0", kept.ID, kept.Type, kept.Side,
			kept.Pair, strings.Join(fields, ", "))
	}
	if known, ok := o.orders[kept.ID]; ok && !kept.Supersedes(known) {
		log.Warnf("remote: the order %d (%s %s of %s): a state older than the one kept, %s at %s after %s at %s: "+
			"not written", kept.ID, kept.Type, kept.Side, kept.Pair, kept.Status,
			kept.UpdatedAt.Format(time.RFC3339Nano), known.Status, known.UpdatedAt.Format(time.RFC3339Nano))
		return known, false
	}
	o.orders[kept.ID] = kept
	return kept, true
}

// add writes the last state of an order to the outbox, else keeps it to write
// again, see rewrite. The failure of an order already kept is logged only; the
// ones of new orders are notified, ordersNotifyEvery apart at most, the ones
// meanwhile with the next notice (see notifyUnwritten), so that an order the
// outbox refuses for good never hides the failures of the others. Must be
// called while holding o.write.
func (o *Orders) add(order model.Order) {
	err := o.outbox.Add(agentapi.KindOrder, strconv.FormatInt(order.ID, 10), order)
	o.mu.Lock()
	if err == nil {
		delete(o.unwritten, order.ID)
		o.mu.Unlock()
		return
	}
	kept := o.unwritten[order.ID]
	o.unwritten[order.ID] = true
	stopped, notifier := o.stopped, o.notifier
	if !stopped && !o.rewriting {
		o.rewriting = true
		go o.rewrite()
	}

	what := fmt.Sprintf("the order %d (%s %s %s of %s, ticket %d)", order.ID, order.Status, order.Type, order.Side,
		order.Pair, order.ExchangeID)
	switch {
	case stopped:
		o.mu.Unlock()
		// eg: the bot trading on after the outbox closed, see Agent.Close
		err = fmt.Errorf("remote: %s executed as the agent stops: not written, the controller never gets it: %w",
			what, err)
		log.Error(err)
		if notifier != nil {
			notifier.OnError(err)
		}
		return
	case kept:
		o.mu.Unlock()
		log.Errorf("remote: %s not written yet: kept in memory, written again until it is: %v", what, err)
		return
	}
	o.unnotified++
	o.lastFailed = fmt.Errorf("remote: the orders can't be written, eg: the disk full: %s kept in memory and "+
		"followed by the bot, written again until it is, lost if the agent stops before: %w", what, err)
	if !o.notified.IsZero() && time.Since(o.notified) < ordersNotifyEvery {
		failure := o.lastFailed
		o.mu.Unlock()
		log.Error(failure)
		return
	}
	failure := o.takeUnnotified()
	o.mu.Unlock()
	o.notifyUnwritten(notifier, failure)
}

// takeUnnotified returns the error of the new failures since the last notice,
// the last one with how many others failed, as notified now. Must be called
// while holding o.mu.
func (o *Orders) takeUnnotified() error {
	err := o.lastFailed
	if others := o.unnotified - 1; others > 0 {
		err = fmt.Errorf("%w (and %d other orders since the last notice, %d not written yet in all)", err, others,
			len(o.unwritten))
	}
	o.notified, o.unnotified, o.lastFailed = time.Now(), 0, nil
	return err
}

// notifyUnwritten logs and notifies err, the failures of orders to be written.
func (o *Orders) notifyUnwritten(notifier Notifier, err error) {
	log.Error(err)
	if notifier != nil {
		notifier.OnError(err)
	}
}

// rewrite writes the orders unwritten again, retried after a wait, doubled up
// to the max, until they are or Close. The ones written at last are notified:
// at once when none is left, else ordersNotifyEvery apart at most; so are the
// failures of new orders waiting for their notice, see add.
func (o *Orders) rewrite() {
	delay := retryDelay
	since, warned, recovered := time.Now(), time.Now(), time.Now()
	written := 0 // at last, since the last notice of them
	for {
		select {
		case <-time.After(delay):
		case <-o.closed:
			return
		}
		o.mu.Lock()
		before := len(o.unwritten)
		o.mu.Unlock()
		left, err := o.writeAgain()
		o.mu.Lock()
		written += max(before-left, 0)
		notifier := o.notifier
		if len(o.unwritten) == 0 { // none failed since, which would wait for this loop
			o.rewriting = false
			// written, this notice tells of them; the next failure, of another
			// interruption, notified at once
			o.notified, o.unnotified, o.lastFailed = time.Time{}, 0, nil
			o.mu.Unlock()
			message := fmt.Sprintf("remote: the orders kept in memory are written at last, after %s",
				time.Since(since).Round(time.Second))
			log.Info(message)
			if notifier != nil {
				notifier.Notify("✅ " + message)
			}
			return
		}
		var failure error
		if o.unnotified > 0 && time.Since(o.notified) >= ordersNotifyEvery {
			failure = o.takeUnnotified()
		}
		var message string
		if written > 0 && time.Since(recovered) >= ordersNotifyEvery {
			message = fmt.Sprintf("remote: %d orders kept in memory are written at last, %d not yet: %v", written,
				len(o.unwritten), err)
			written, recovered = 0, time.Now()
		}
		o.mu.Unlock()
		if failure != nil {
			o.notifyUnwritten(notifier, failure)
		}
		if message != "" {
			log.Info(message)
			if notifier != nil {
				notifier.Notify("✅ " + message)
			}
		}
		if time.Since(warned) >= warnEvery {
			warned = time.Now()
			log.Errorf("remote: %d orders not written yet, for %s, kept in memory: %v", left,
				time.Since(since).Round(time.Second), err)
		}
		delay = min(2*delay, retryMaxDelay)
	}
}

// writeAgain writes the last state of the orders unwritten, and returns how
// many are left, with the error of the outbox.
func (o *Orders) writeAgain() (int, error) {
	o.write.Lock()
	defer o.write.Unlock()
	o.mu.Lock()
	ids := make([]int64, 0, len(o.unwritten))
	for id := range o.unwritten {
		ids = append(ids, id)
	}
	o.mu.Unlock()
	slices.Sort(ids)

	var failed error
	for _, id := range ids {
		o.mu.Lock()
		order := o.orders[id]
		o.mu.Unlock()
		err := o.outbox.Add(agentapi.KindOrder, strconv.FormatInt(id, 10), order)
		if err != nil {
			failed = err
			continue // one refused never holds the others
		}
		o.mu.Lock()
		delete(o.unwritten, id)
		o.mu.Unlock()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.unwritten), failed
}

// Close stops leasing the next order IDs and writing the orders unwritten
// again, after a last attempt, as the agent stops, before its outbox is
// flushed: it returns the error of the ones still unwritten, never sent to the
// controller.
func (o *Orders) Close() error {
	o.stopLeasing()
	o.mu.Lock()
	if !o.stopped {
		o.stopped = true
		close(o.closed)
	}
	o.mu.Unlock()

	left, err := o.writeAgain()
	if left == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	lost := make([]string, 0, len(o.unwritten))
	for id := range o.unwritten {
		order := o.orders[id]
		lost = append(lost, fmt.Sprintf("%d (%s %s %s %g of %s, ticket %d)", id, order.Status, order.Type,
			order.Side, order.Quantity, order.Pair, order.ExchangeID))
	}
	slices.Sort(lost)
	return fmt.Errorf("remote: the orders %s not written as the agent stops, never sent to the controller: %w",
		strings.Join(lost, ", "), errors.Join(errOrdersLost, err))
}

// errOrdersLost tells of orders the outbox couldn't write before the agent
// stopped.
var errOrdersLost = errors.New("orders lost")

// Orders returns copies of the orders the filters keep, by their update, as
// the storage of the files did.
func (o *Orders) Orders(filters ...storage.OrderFilter) ([]*model.Order, error) {
	o.mu.Lock()
	list := make([]*model.Order, 0, len(o.orders))
	for _, order := range o.orders {
		kept := true
		for _, filter := range filters {
			if !filter(order) {
				kept = false
				break
			}
		}
		if kept {
			order := clone(order)
			list = append(list, &order)
		}
	}
	o.mu.Unlock()
	sort.Slice(list, func(i, j int) bool {
		if a, b := list[i].UpdatedAt, list[j].UpdatedAt; !a.Equal(b) {
			return a.Before(b)
		}
		return list[i].ID < list[j].ID
	})
	return list, nil
}

// clone returns a copy of order sharing nothing with it, as the storage of the
// files read them back: without its candle, never kept.
func clone(order model.Order) model.Order {
	if order.Stop != nil {
		stop := *order.Stop
		order.Stop = &stop
	}
	if order.GroupID != nil {
		group := *order.GroupID
		order.GroupID = &group
	}
	order.Candle = model.Candle{}
	return order
}
