package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// B9: a client reading slowly the answer of a cancel never holds the chart:
// its candles and its orders go on as the client reads, the answer being the
// order as the cancel left it.
func TestChart_SlowCancelClientNeverHoldsTheChart(t *testing.T) {
	c := newTestChart(t)
	controller := &fakeController{}
	c.SetOrderController(controller)
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	c.OnCandle(candleAt("BTCUSDT", base, 40000))
	pending := model.Order{ID: 1, Pair: "BTCUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeLimit,
		Status: model.OrderStatusTypeNew, Price: 38000, Quantity: 0.1, CreatedAt: base, UpdatedAt: base}
	c.OnOrder(pending)

	client := newSlowClient(t)
	served := make(chan struct{})
	go func() {
		defer close(served)
		req := httptest.NewRequest(http.MethodPost, "http://localhost/api/controls/orders/1/cancel", nil)
		req.Header.Set("Content-Type", "application/json")
		c.Handler().ServeHTTP(client, req)
	}()
	waitFor(t, client.writing, "the answer of the cancel written")

	canceled := pending
	canceled.Status, canceled.UpdatedAt = model.OrderStatusTypeCanceled, base.Add(time.Minute)
	within(t, 2*time.Second, "the chart fed while the client reads", func() {
		c.OnCandle(candleAt("BTCUSDT", base.Add(time.Hour), 40100))
		partial := candleAt("BTCUSDT", base.Add(2*time.Hour), 40200)
		partial.Complete = false
		c.OnCandle(partial)
		c.OnOrder(canceled)
		c.OnOrder(model.Order{ID: 2, Pair: "BTCUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
			Status: model.OrderStatusTypeFilled, Price: 40100, Quantity: 0.1, CreatedAt: base, UpdatedAt: base})
	})
	select {
	case <-served:
		t.Fatal("the client was still reading")
	default:
	}
	c.mu.Lock()
	status := c.orderByID[1].Status
	_, filled := c.orderByID[2]
	c.mu.Unlock()
	assert.Equal(t, model.OrderStatusTypeCanceled, status, "the cancel confirmed as the client reads")
	assert.True(t, filled, "the next order taken as the client reads")

	client.unblock()
	waitFor(t, served, "the cancel answered")
	require.Equal(t, []int64{1}, controller.canceled)
	client.mu.Lock()
	defer client.mu.Unlock()
	var answer Order
	require.NoError(t, json.Unmarshal(client.body.Bytes(), &answer))
	assert.Equal(t, string(model.OrderStatusTypePendingCancel), answer.Status, "the order as the cancel left it")
}
