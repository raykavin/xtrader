package remote

import (
	"context"
	"encoding/json"
	"path/filepath"
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
