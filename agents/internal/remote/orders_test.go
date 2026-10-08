package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
)

// openOutbox opens an outbox of the test, sent to the fake controller.
func openOutbox(t *testing.T, f *fakeController) *Outbox {
	t.Helper()
	o, err := OpenOutbox(filepath.Join(t.TempDir(), "agent.db"), f.client())
	require.NoError(t, err)
	t.Cleanup(func() { _ = o.Close() })
	return o
}

func TestOrders(t *testing.T) {
	f := newFakeController(t)
	at := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	stop, group := 99.5, int64(3)
	f.set(func(f *fakeController) {
		f.orders = []model.Order{
			{ID: 3, Pair: "XAUUSDm", Status: model.OrderStatusTypeNew, Stop: &stop, GroupID: &group, UpdatedAt: at},
			{ID: 7, Pair: "BTCUSDm", Status: model.OrderStatusTypeFilled, UpdatedAt: at.Add(-time.Hour)},
		}
		f.leases = []agentapi.OrderIDs{{First: 8, Last: 10}, {First: 50, Last: 1049}}
	})
	outbox := openOutbox(t, f)
	// an order of a run before, not sent: newer than the controller's
	require.NoError(t, outbox.Add(agentapi.KindOrder, "7", model.Order{ID: 7, Pair: "BTCUSDm",
		Status: model.OrderStatusTypeCanceled, UpdatedAt: at.Add(time.Minute)}))
	require.NoError(t, outbox.Add(agentapi.KindOrder, "9", model.Order{ID: 9, Pair: "BTCUSDm",
		Status: model.OrderStatusTypeNew, UpdatedAt: at.Add(2 * time.Minute)}))

	orders, err := NewOrders(context.Background(), f.client(), outbox)
	require.NoError(t, err)
	var _ storage.Storage = orders

	all, err := orders.Orders()
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, []int64{3, 7, 9}, []int64{all[0].ID, all[1].ID, all[2].ID}, "by their update")
	assert.Equal(t, model.OrderStatusTypeCanceled, all[1].Status, "the outbox is newer")
	*all[0].Stop = 1 // a copy
	again, _ := orders.Orders(storage.WithPair("XAUUSDm"))
	require.Len(t, again, 1)
	assert.Equal(t, 99.5, *again[0].Stop)

	// the controller out of reach: numbered from the lease at once, past the
	// orders of the outbox, the next lease asked meanwhile
	f.set(func(f *fakeController) { f.down = true })
	order := &model.Order{Pair: "XAUUSDm", Side: model.SideTypeBuy, Status: model.OrderStatusTypeFilled,
		UpdatedAt: at.Add(3 * time.Minute)}
	start := time.Now()
	require.NoError(t, orders.CreateOrder(order))
	assert.Less(t, time.Since(start), 50*time.Millisecond, "never waits for the controller")
	assert.Equal(t, int64(10), order.ID)
	next := &model.Order{Pair: "XAUUSDm"}
	require.NoError(t, orders.CreateOrder(next))
	assert.Equal(t, int64(11), next.ID, "past the lease, spent")

	f.set(func(f *fakeController) { f.down = false })
	require.Eventually(t, func() bool {
		orders.mu.Lock()
		defer orders.mu.Unlock()
		return orders.last == 1049
	}, 5*time.Second, time.Millisecond, "the next lease once the controller answers")
	third := &model.Order{Pair: "XAUUSDm"}
	require.NoError(t, orders.CreateOrder(third))
	assert.Equal(t, int64(50), third.ID)

	third.Status = model.OrderStatusTypeFilled
	third.UpdatedAt = at.Add(time.Hour)
	require.NoError(t, orders.UpdateOrder(third))
	filled, _ := orders.Orders(storage.WithStatus(model.OrderStatusTypeFilled))
	require.Len(t, filled, 2)
	assert.Equal(t, at.Add(time.Hour), filled[1].UpdatedAt, "the update of the order controller kept")

	pending, err := outbox.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	var keys []string
	for _, record := range pending {
		keys = append(keys, record.Key)
	}
	assert.Equal(t, []string{"7", "9", "10", "11", "50", "50"}, keys, "every change in the outbox, in order")
	var last model.Order
	require.NoError(t, json.Unmarshal(pending[len(pending)-1].Payload, &last))
	assert.Equal(t, model.OrderStatusTypeFilled, last.Status)
}

// pendingOrders returns the orders of the records of the outbox not sent yet,
// in order.
func pendingOrders(t *testing.T, outbox *Outbox) []model.Order {
	t.Helper()
	records, err := outbox.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	orders := make([]model.Order, 0, len(records))
	for _, record := range records {
		var order model.Order
		require.NoError(t, json.Unmarshal(record.Payload, &order))
		orders = append(orders, order)
	}
	return orders
}

// The order is at the broker when the order controller stores it: a failure
// of the outbox is never its error, the order kept in memory, its numbers not
// finite written as 0 (the audit: "json: unsupported value: NaN", "database
// closed").
func TestOrders_AnOrderAtTheBrokerIsKeptWhateverTheOutbox(t *testing.T) {
	f := newFakeController(t)
	outbox := openOutbox(t, f)
	orders, err := NewOrders(context.Background(), f.client(), outbox)
	require.NoError(t, err)

	stop := math.Inf(-1)
	nan := &model.Order{ExchangeID: 501, Pair: "XAUUSDm", Side: model.SideTypeSell, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Price: 2000, Quantity: 1, Profit: math.NaN(),
		ProfitValue: math.Inf(1), Stop: &stop}
	require.NoError(t, orders.CreateOrder(nan))
	assert.Equal(t, int64(1), nan.ID)
	written := pendingOrders(t, outbox)
	require.Len(t, written, 1, "written")
	assert.Equal(t, int64(501), written[0].ExchangeID)
	assert.Equal(t, 2000.0, written[0].Price)
	assert.Zero(t, written[0].Profit)
	assert.Zero(t, written[0].ProfitValue)
	assert.Zero(t, *written[0].Stop)
	assert.True(t, math.IsNaN(nan.Profit), "the order of the order controller untouched")

	require.NoError(t, outbox.Close()) // eg: the agent stopping, the bot trading on
	closed := &model.Order{ExchangeID: 502, Pair: "XAUUSDm", Side: model.SideTypeBuy, Type: model.OrderTypeLimit,
		Status: model.OrderStatusTypeNew, Price: 1990, Quantity: 1}
	require.NoError(t, orders.CreateOrder(closed))
	assert.Equal(t, int64(2), closed.ID)
	closed.Status = model.OrderStatusTypeFilled
	require.NoError(t, orders.UpdateOrder(closed))

	all, err := orders.Orders()
	require.NoError(t, err)
	require.Len(t, all, 2, "held in memory")
	assert.Zero(t, all[0].Profit)
	assert.Equal(t, model.OrderStatusTypeFilled, all[1].Status, "its last state")
}

// flakyOutbox fails its writes while failing is set, else writes them to the
// outbox.
type flakyOutbox struct {
	outbox  *Outbox
	mu      sync.Mutex
	failing bool
	adds    int
}

var errDiskFull = errors.New("no space left on device")

func (f *flakyOutbox) Add(kind, key string, payload any) error {
	f.mu.Lock()
	failing := f.failing
	f.adds++
	f.mu.Unlock()
	if failing {
		return errDiskFull
	}
	return f.outbox.Add(kind, key, payload)
}

func (f *flakyOutbox) fail(failing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing = failing
}

// recordingNotifier records the notifications.
type recordingNotifier struct {
	mu       sync.Mutex
	messages []string
	errors   []error
}

func (n *recordingNotifier) Notify(message string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, message)
}

func (n *recordingNotifier) OnError(err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.errors = append(n.errors, err)
}

func (n *recordingNotifier) counts() (messages, errors int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.messages), len(n.errors)
}

// unwrittenIDs returns the IDs of the orders not written.
func (o *Orders) unwrittenIDs() []int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	var ids []int64
	for id := range o.unwritten {
		ids = append(ids, id)
	}
	return ids
}

// The orders the outbox failed to write are written again, in their last
// state, once it writes: the failure notified once, and the write at last.
func TestOrders_WrittenAgainOnceTheOutboxWrites(t *testing.T) {
	f := newFakeController(t)
	outbox := openOutbox(t, f)
	orders, err := NewOrders(context.Background(), f.client(), outbox)
	require.NoError(t, err)
	flaky := &flakyOutbox{outbox: outbox, failing: true}
	orders.outbox = flaky
	notifier := &recordingNotifier{}
	orders.SetNotifier(notifier)

	entry := &model.Order{ExchangeID: 601, Pair: "XAUUSDm", Side: model.SideTypeBuy, Type: model.OrderTypeLimit,
		Status: model.OrderStatusTypeNew, Price: 2000, Quantity: 1}
	require.NoError(t, orders.CreateOrder(entry))
	entry.Status = model.OrderStatusTypeFilled
	require.NoError(t, orders.UpdateOrder(entry))
	other := &model.Order{ExchangeID: 602, Pair: "XAUUSDm", Side: model.SideTypeSell,
		Type: model.OrderTypeStopLoss, Status: model.OrderStatusTypeNew, Price: 1990, Quantity: 1}
	require.NoError(t, orders.CreateOrder(other))
	assert.ElementsMatch(t, []int64{1, 2}, orders.unwrittenIDs())
	messages, errs := notifier.counts()
	assert.Equal(t, 0, messages)
	assert.Equal(t, 1, errs, "the failure notified once")
	notifier.mu.Lock()
	assert.ErrorIs(t, notifier.errors[0], errDiskFull)
	notifier.mu.Unlock()
	assert.Empty(t, pendingOrders(t, outbox))

	flaky.fail(false)
	require.Eventually(t, func() bool { return len(orders.unwrittenIDs()) == 0 }, 5*time.Second,
		time.Millisecond, "written again")
	written := pendingOrders(t, outbox)
	require.Len(t, written, 2)
	assert.Equal(t, int64(1), written[0].ID)
	assert.Equal(t, model.OrderStatusTypeFilled, written[0].Status, "its last state")
	assert.Equal(t, int64(2), written[1].ID)
	require.Eventually(t, func() bool { messages, _ := notifier.counts(); return messages == 1 }, 5*time.Second,
		time.Millisecond, "the write at last notified")

	// written, a later failure is notified again
	flaky.fail(true)
	require.NoError(t, orders.UpdateOrder(other))
	_, errs = notifier.counts()
	assert.Equal(t, 2, errs)
	flaky.fail(false)
	require.Eventually(t, func() bool { return len(orders.unwrittenIDs()) == 0 }, 5*time.Second, time.Millisecond)
}

// refusingOutbox is a flaky outbox refusing for good the record of a key, eg:
// a record it can't write.
type refusingOutbox struct {
	*flakyOutbox
	refused string
}

func (r *refusingOutbox) Add(kind, key string, payload any) error {
	if key == r.refused {
		return errors.New("a record refused for good")
	}
	return r.flakyOutbox.Add(kind, key, payload)
}

// B5: an order the outbox refuses for good never hides the failures of the
// next ones: each new failure is notified, ordersNotifyEvery apart at most, the
// ones meanwhile with the next notice, and so are the orders written at last
// while the one refused stays.
func TestOrders_NewFailuresNotifiedWithAnOrderRefusedForGood(t *testing.T) {
	every := ordersNotifyEvery
	ordersNotifyEvery = 200 * time.Millisecond
	t.Cleanup(func() { ordersNotifyEvery = every })
	f := newFakeController(t)
	outbox := openOutbox(t, f)
	orders, err := NewOrders(context.Background(), f.client(), outbox)
	require.NoError(t, err)
	refusing := &refusingOutbox{flakyOutbox: &flakyOutbox{outbox: outbox}, refused: "1"}
	orders.outbox = refusing
	notifier := &recordingNotifier{}
	orders.SetNotifier(notifier)
	order := func(ticket int64) *model.Order {
		return &model.Order{ExchangeID: ticket, Pair: "XAUUSDm", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
			Status: model.OrderStatusTypeFilled, Price: 2000, Quantity: 1}
	}

	require.NoError(t, orders.CreateOrder(order(601))) // refused for good
	_, errs := notifier.counts()
	require.Equal(t, 1, errs)

	refusing.fail(true)
	require.NoError(t, orders.CreateOrder(order(602)))
	require.NoError(t, orders.CreateOrder(order(603)))
	_, errs = notifier.counts()
	require.Equal(t, 1, errs, "within the notice of the first one")
	require.Eventually(t, func() bool { _, errs := notifier.counts(); return errs == 2 }, 5*time.Second,
		10*time.Millisecond, "the new failures notified, though one order is refused for good")
	notifier.mu.Lock()
	require.ErrorContains(t, notifier.errors[1], "and 1 other orders since the last notice, 3 not written yet in all")
	notifier.mu.Unlock()

	refusing.fail(false)
	require.Eventually(t, func() bool { messages, _ := notifier.counts(); return messages == 1 }, 10*time.Second,
		10*time.Millisecond, "the orders written at last notified, though one is refused for good")
	notifier.mu.Lock()
	require.Contains(t, notifier.messages[0], "2 orders kept in memory are written at last, 1 not yet")
	notifier.mu.Unlock()
	require.Equal(t, []int64{1}, orders.unwrittenIDs())
	_, errs = notifier.counts()
	require.Equal(t, 2, errs)
}

// The last record of an order is its last state, the orders written again
// while the bot changes them.
func TestOrders_TheLastRecordOfAnOrderIsItsLastState(t *testing.T) {
	f := newFakeController(t)
	outbox := openOutbox(t, f)
	orders, err := NewOrders(context.Background(), f.client(), outbox)
	require.NoError(t, err)
	flaky := &flakyOutbox{outbox: outbox, failing: true}
	orders.outbox = flaky

	const count, changes = 4, 50
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			order := &model.Order{ExchangeID: int64(700 + i), Pair: "XAUUSDm", Status: model.OrderStatusTypeNew}
			_ = orders.CreateOrder(order)
			for change := range changes {
				order.Quantity = float64(change + 1)
				_ = orders.UpdateOrder(order)
			}
		}()
	}
	for range changes {
		flaky.fail(true)
		time.Sleep(100 * time.Microsecond)
		flaky.fail(false)
	}
	wg.Wait()
	flaky.fail(false)
	require.Eventually(t, func() bool { return len(orders.unwrittenIDs()) == 0 }, 5*time.Second, time.Millisecond)

	last := map[int64]model.Order{}
	for _, order := range pendingOrders(t, outbox) {
		last[order.ID] = order
	}
	require.Len(t, last, count)
	for id, order := range last {
		assert.Equal(t, float64(changes), order.Quantity, "the order %d", id)
	}
}

// gatedOutbox fails its writes while failing is set, and holds the next write
// once held is set, until released.
type gatedOutbox struct {
	flakyOutbox
	held     bool
	entered  chan struct{}
	released chan struct{}
}

func (g *gatedOutbox) Add(kind, key string, payload any) error {
	g.mu.Lock()
	held := g.held && !g.failing
	g.held = g.held && !held
	g.mu.Unlock()
	if held {
		close(g.entered)
		<-g.released
	}
	return g.flakyOutbox.Add(kind, key, payload)
}

// An order written again never overwrites a later state of it written
// meanwhile.
func TestOrders_WrittenAgainNeverOverwritesALaterState(t *testing.T) {
	f := newFakeController(t)
	outbox := openOutbox(t, f)
	orders, err := NewOrders(context.Background(), f.client(), outbox)
	require.NoError(t, err)
	gated := &gatedOutbox{flakyOutbox: flakyOutbox{outbox: outbox, failing: true},
		entered: make(chan struct{}), released: make(chan struct{})}
	orders.outbox = gated

	order := &model.Order{ExchangeID: 901, Pair: "XAUUSDm", Status: model.OrderStatusTypeNew, Quantity: 1}
	require.NoError(t, orders.CreateOrder(order))
	gated.mu.Lock()
	gated.failing, gated.held = false, true
	gated.mu.Unlock()
	select {
	case <-gated.entered: // written again, its state then
	case <-time.After(5 * time.Second):
		t.Fatal("never written again")
	}
	updated := make(chan struct{})
	go func() {
		defer close(updated)
		later := *order
		later.Status, later.Quantity = model.OrderStatusTypeFilled, 2
		_ = orders.UpdateOrder(&later)
	}()
	select {
	case <-updated:
	case <-time.After(50 * time.Millisecond): // waits for the write again, as it must
	}
	close(gated.released)
	<-updated
	require.Eventually(t, func() bool {
		orders.mu.Lock()
		defer orders.mu.Unlock()
		return !orders.rewriting && len(orders.unwritten) == 0
	}, 5*time.Second, time.Millisecond, "written again")

	written := pendingOrders(t, outbox)
	require.NotEmpty(t, written)
	last := written[len(written)-1]
	assert.Equal(t, model.OrderStatusTypeFilled, last.Status, "the last record is the last state")
	assert.Equal(t, 2.0, last.Quantity)
}

// As the agent stops, the orders the outbox failed to write are written a last
// time before it is sent, the ones it still can't told of.
func TestAgent_CloseWritesTheOrdersNotWritten(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.cfg = testConfig()
		f.runtime = agentapi.Runtime{Version: 1}
	})
	a, err := connect(context.Background(), f.client(), "llm")
	require.NoError(t, err)
	require.NoError(t, a.Open(t.TempDir()))
	orders, err := a.Orders(context.Background())
	require.NoError(t, err)
	flaky := &flakyOutbox{outbox: a.outbox, failing: true}
	orders.outbox = flaky
	orders.mu.Lock()
	orders.rewriting = true // no rewriting meanwhile: the one of Close
	orders.mu.Unlock()

	order := &model.Order{ExchangeID: 801, Pair: "XAUUSDm", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Price: 2000, Quantity: 1}
	require.NoError(t, orders.CreateOrder(order))
	require.Equal(t, []int64{order.ID}, orders.unwrittenIDs())
	f.set(func(f *fakeController) { f.down = true })
	a.Run(context.Background(), nil)
	flaky.fail(false)
	f.set(func(f *fakeController) { f.down = false })

	require.NoError(t, a.Close())
	require.Len(t, f.written(), 1, "written and sent")
	assert.Equal(t, strconv.FormatInt(order.ID, 10), f.written()[0].Key)

	// the outbox closed: the order of the bot trading on is told of as lost
	notifier := &recordingNotifier{}
	orders.SetNotifier(notifier)
	late := &model.Order{ExchangeID: 802, Pair: "XAUUSDm", Status: model.OrderStatusTypeFilled}
	require.NoError(t, orders.CreateOrder(late))
	_, errs := notifier.counts()
	require.Equal(t, 1, errs)
	notifier.mu.Lock()
	assert.ErrorContains(t, notifier.errors[0], fmt.Sprintf("the order %d", late.ID))
	assert.ErrorContains(t, notifier.errors[0], "executed as the agent stops")
	notifier.mu.Unlock()
	err = orders.Close()
	require.ErrorIs(t, err, errOrdersLost)
	assert.ErrorContains(t, err, "ticket 802")
}
