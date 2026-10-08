package remote

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/model"
)

// leaseRequests returns how many leases of order IDs the fake controller was
// asked for.
func leaseRequests(f *fakeController) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, request := range f.requests {
		if strings.HasSuffix(request, "/order-ids") {
			n++
		}
	}
	return n
}

// ordersLeasingAgain returns the orders of an agent whose lease is nearly
// spent, the fake controller answering the next ones as fail sets it, and an
// order created: the next lease asked.
func ordersLeasingAgain(t *testing.T, fail func(f *fakeController)) (*fakeController, *Orders) {
	t.Helper()
	f := newFakeController(t)
	f.set(func(f *fakeController) { f.leases = []agentapi.OrderIDs{{First: 1, Last: 5}} })
	orders, err := NewOrders(context.Background(), f.client(), openOutbox(t, f))
	require.NoError(t, err)
	t.Cleanup(func() { _ = orders.Close() })
	f.set(fail)
	require.NoError(t, orders.CreateOrder(&model.Order{Pair: "XAUUSDm", Side: model.SideTypeBuy,
		Status: model.OrderStatusTypeFilled}))
	return f, orders
}

// M10: the controller refusing the agent (401), the next order IDs are asked
// no more: the agent stops.
func TestOrders_LeasingStopsOnceTheControllerRefusesTheAgent(t *testing.T) {
	f, _ := ordersLeasingAgain(t, func(f *fakeController) { f.refuse = true })
	require.Eventually(t, func() bool { return leaseRequests(f) >= 2 }, 5*time.Second, time.Millisecond)
	time.Sleep(20 * retryMaxDelay)
	assert.Equal(t, 2, leaseRequests(f), "the one refused, never again")
}

// M10: the lease of the next order IDs, retried while the controller is out of
// reach, ends with the orders as the agent stops, its failures warned once
// meanwhile.
func TestOrders_LeasingEndsAsTheAgentStops(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	f, orders := ordersLeasingAgain(t, func(f *fakeController) { f.down = true })
	require.Eventually(t, func() bool { return leaseRequests(f) >= 8 }, 5*time.Second, time.Millisecond)
	require.NoError(t, orders.Close())
	asked := leaseRequests(f)
	time.Sleep(10 * retryMaxDelay)
	assert.LessOrEqual(t, leaseRequests(f), asked+1, "none after Close but the one under way")

	warned := 0
	for _, entry := range hook.AllEntries() {
		// the ones of this controller, down, not of another test's left behind
		if entry.Level == logrus.WarnLevel && strings.Contains(entry.Message, "order IDs") &&
			strings.Contains(entry.Message, "HTTP 503") {
			warned++
		}
	}
	assert.Equal(t, 1, warned, "warned once while it lasts")
}
