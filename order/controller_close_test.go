package order

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// isClosed reports whether the controller is closed, see Close.
func (c *Controller) isClosed() bool {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	return c.closed
}

// R5: Close, as the bot exits, waits for the calls begun to the exchange, the
// cancels and the protections included, to be recorded, updates the pending
// orders a last time, and refuses every call after at once, without reaching
// the exchange: nothing is written to the storage once it returns, eg: an
// outbox closed then.
func TestController_CloseStopsEveryCallToTheExchange(t *testing.T) {
	controller, exch := newTestController(t)
	limit := model.Order{Pair: "BTCUSDT", ExchangeID: 7, Side: model.SideTypeBuy, Type: model.OrderTypeLimit,
		Status: model.OrderStatusTypeNew, Price: 90, Quantity: 1, CreatedAt: time.Now()}
	require.NoError(t, controller.storage.CreateOrder(&limit))
	other := model.Order{Pair: "BTCUSDT", ExchangeID: 8, Side: model.SideTypeSell, Type: model.OrderTypeLimit,
		Status: model.OrderStatusTypeNew, Price: 110, Quantity: 1, CreatedAt: time.Now()}
	require.NoError(t, controller.storage.CreateOrder(&other))
	controller.mtx.Lock()
	controller.pending[limit.ID], controller.pending[other.ID] = limit, other
	controller.mtx.Unlock()

	entered, release := make(chan struct{}), make(chan struct{})
	exch.On("Cancel", limit).Run(func(mock.Arguments) {
		close(entered)
		<-release
	}).Return(nil).Once()
	canceled := limit
	canceled.Status = model.OrderStatusTypeCanceled
	exch.On("Order", "BTCUSDT", int64(7)).Return(canceled, nil).Once()
	exch.On("Order", "BTCUSDT", int64(8)).Return(other, nil).Once()

	canceling := make(chan error, 1)
	go func() { canceling <- controller.Cancel(limit) }()
	<-entered
	closing := make(chan struct{})
	go func() {
		controller.Close()
		close(closing)
	}()
	require.Eventually(t, controller.isClosed, time.Second, time.Millisecond)

	// refused at once, the pair busy with the cancel begun
	within(t, time.Second, "the calls after Close", func() {
		_, err := controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		require.ErrorIs(t, err, ErrBotStopped)
		_, err = controller.Protect(model.SideTypeBuy, "BTCUSDT", 1, 90, 120)
		require.ErrorIs(t, err, ErrBotStopped)
		_, err = controller.UpdateProtection(other, 105)
		require.ErrorIs(t, err, ErrBotStopped)
		require.ErrorIs(t, controller.Cancel(other), ErrBotStopped)
	})
	select {
	case <-closing:
		t.Fatal("closed before the cancel begun was recorded")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-canceling)
	within(t, time.Second, "Close", func() { <-closing })
	stored, err := controller.storage.Orders()
	require.NoError(t, err)
	statuses := map[int64]model.OrderStatusType{}
	for _, order := range stored {
		statuses[order.ExchangeID] = order.Status
	}
	require.Equal(t, model.OrderStatusTypeCanceled, statuses[7], "updated a last time")
	require.Equal(t, model.OrderStatusTypeNew, statuses[8])

	controller.UpdateOrders() // never after Close
	controller.Start()        // never started again
	require.Equal(t, StatusStopped, controller.Status())
	_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
	require.ErrorIs(t, err, ErrBotStopped)
	exch.AssertNumberOfCalls(t, "Order", 2)
	exch.AssertNumberOfCalls(t, "Cancel", 1)
	exch.AssertNotCalled(t, "CreateOrderMarket", mock.Anything, mock.Anything, mock.Anything)
	exch.AssertExpectations(t)
}

// An order asked on a stopped bot is refused at once, even while an order of
// its pair begun before is still at the exchange.
func TestController_StopRefusesAtOnceAnOrderOfABusyPair(t *testing.T) {
	controller, exch := newTestController(t)
	entered, release := make(chan struct{}), make(chan struct{})
	exch.On("CreateOrderMarket", model.SideTypeBuy, "BTCUSDT", 1.0).Run(func(mock.Arguments) {
		close(entered)
		<-release
	}).Return(filledOrder(1, model.SideTypeBuy, 100, 1), nil).Once()
	controller.Start()

	created := make(chan error, 1)
	go func() {
		_, err := controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		created <- err
	}()
	<-entered
	stopped := make(chan struct{})
	go func() {
		controller.Stop()
		close(stopped)
	}()
	require.Eventually(t, func() bool { return controller.Status() == StatusStopped }, time.Second,
		time.Millisecond)
	within(t, time.Second, "an order of the pair", func() {
		_, err := controller.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 1)
		require.ErrorIs(t, err, ErrBotStopped)
	})

	close(release)
	require.NoError(t, <-created)
	<-stopped
	exch.AssertExpectations(t)
}
