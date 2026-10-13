package order

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/testdata/mocks"
)

// newTestController wires a controller to a mocked exchange and an in-memory
// storage.
func newTestController(t *testing.T) (*Controller, *mocks.Exchange) {
	t.Helper()

	memory, err := storage.FromMemory()
	require.NoError(t, err)

	exchange := &mocks.Exchange{}
	controller := NewController(context.Background(), exchange, memory, NewOrderFeed())

	return controller, exchange
}

// filledOrder is the exchange reply for a market order.
func filledOrder(id int64, side model.SideType, price, quantity float64) model.Order {
	return model.Order{
		ExchangeID: id,
		Pair:       "BTCUSDT",
		Side:       side,
		Type:       model.OrderTypeMarket,
		Status:     model.OrderStatusTypeFilled,
		Price:      price,
		Quantity:   quantity,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
}

func TestControllerStatus(t *testing.T) {
	controller, _ := newTestController(t)

	require.Equal(t, Status(""), controller.Status(), "a fresh controller has no status yet")

	controller.Start()
	require.Equal(t, StatusRunning, controller.Status())

	controller.Start() // starting twice is a no-op
	require.Equal(t, StatusRunning, controller.Status())

	controller.Stop()
	require.Equal(t, StatusStopped, controller.Status())

	controller.Stop() // stopping twice is a no-op
	require.Equal(t, StatusStopped, controller.Status())
}

func TestControllerExchangeDelegation(t *testing.T) {
	controller, exchange := newTestController(t)

	account := model.Account{Balances: []model.Balance{{Asset: "USDT", Free: 1000}}}
	exchange.On("Account").Return(account, nil)
	exchange.On("LastQuote", mock.Anything, "BTCUSDT").Return(100.0, nil)
	exchange.On("Position", "BTCUSDT").Return(2.0, 1000.0, nil)
	exchange.On("Order", "BTCUSDT", int64(1)).Return(filledOrder(1, model.SideTypeBuy, 100, 1), nil)

	t.Run("Account", func(t *testing.T) {
		result, err := controller.Account()

		require.NoError(t, err)
		require.Equal(t, account, result)
	})

	t.Run("LastQuote", func(t *testing.T) {
		quote, err := controller.LastQuote("BTCUSDT")

		require.NoError(t, err)
		require.Equal(t, 100.0, quote)
	})

	t.Run("Order", func(t *testing.T) {
		order, err := controller.Order("BTCUSDT", 1)

		require.NoError(t, err)
		require.Equal(t, int64(1), order.ExchangeID)
	})

	t.Run("PositionValue reports an exchange failure", func(t *testing.T) {
		failing, exchange := newTestController(t)
		exchange.On("Position", "ETHUSDT").Return(0.0, 0.0, errors.New("exchange down"))

		_, err := failing.PositionValue("ETHUSDT")

		require.ErrorContains(t, err, "exchange down")
	})
}

func TestControllerSetNotifier(t *testing.T) {
	controller, _ := newTestController(t)
	notifier := &mocks.Notifier{}
	notifier.On("Notify", mock.Anything).Return()
	notifier.On("OnError", mock.Anything).Return()

	controller.SetNotifier(notifier)

	controller.notify("hello")
	controller.notifyError(errors.New("boom"))

	notifier.AssertCalled(t, "Notify", "hello")
	notifier.AssertNumberOfCalls(t, "OnError", 1)
}

func TestControllerCreateOrderFailures(t *testing.T) {
	failure := errors.New("exchange rejected the order")

	tests := []struct {
		name   string
		expect func(*mocks.Exchange)
		run    func(*Controller) error
	}{
		{
			name: "OCO",
			expect: func(e *mocks.Exchange) {
				e.On("CreateOrderOCO", model.SideTypeSell, "BTCUSDT", 1.0, 120.0, 90.0, 89.0).
					Return([]model.Order{}, failure)
			},
			run: func(c *Controller) error {
				_, err := c.CreateOrderOCO(model.SideTypeSell, "BTCUSDT", 1, 120, 90, 89)
				return err
			},
		},
		{
			name: "limit",
			expect: func(e *mocks.Exchange) {
				e.On("CreateOrderLimit", model.SideTypeBuy, "BTCUSDT", 1.0, 100.0).
					Return(model.Order{}, failure)
			},
			run: func(c *Controller) error {
				_, err := c.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 100)
				return err
			},
		},
		{
			name: "market",
			expect: func(e *mocks.Exchange) {
				e.On("CreateOrderMarket", model.SideTypeBuy, "BTCUSDT", 1.0).
					Return(model.Order{}, failure)
			},
			run: func(c *Controller) error {
				_, err := c.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
				return err
			},
		},
		{
			name: "market quote",
			expect: func(e *mocks.Exchange) {
				e.On("CreateOrderMarketQuote", model.SideTypeBuy, "BTCUSDT", 100.0).
					Return(model.Order{}, failure)
			},
			run: func(c *Controller) error {
				_, err := c.CreateOrderMarketQuote(model.SideTypeBuy, "BTCUSDT", 100)
				return err
			},
		},
		{
			name: "stop",
			expect: func(e *mocks.Exchange) {
				e.On("CreateOrderStop", "BTCUSDT", 1.0, 90.0).Return(model.Order{}, failure)
			},
			run: func(c *Controller) error {
				_, err := c.CreateOrderStop("BTCUSDT", 1, 90)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller, exchange := newTestController(t)
			notifier := &mocks.Notifier{}
			notifier.On("OnError", mock.Anything).Return()
			controller.SetNotifier(notifier)
			tt.expect(exchange)

			require.ErrorIs(t, tt.run(controller), failure)
			notifier.AssertNumberOfCalls(t, "OnError", 1)
		})
	}
}

func TestControllerCreateOrderSuccess(t *testing.T) {
	t.Run("OCO tracks both legs", func(t *testing.T) {
		controller, exchange := newTestController(t)
		group := int64(7)
		orders := []model.Order{
			{ExchangeID: 1, Pair: "BTCUSDT", Side: model.SideTypeSell, Type: model.OrderTypeLimitMaker,
				Status: model.OrderStatusTypeNew, Price: 120, Quantity: 1, GroupID: &group},
			{ExchangeID: 2, Pair: "BTCUSDT", Side: model.SideTypeSell, Type: model.OrderTypeStopLossLimit,
				Status: model.OrderStatusTypeNew, Price: 89, Quantity: 1, GroupID: &group},
		}
		exchange.On("CreateOrderOCO", model.SideTypeSell, "BTCUSDT", 1.0, 120.0, 90.0, 89.0).Return(orders, nil)

		created, err := controller.CreateOrderOCO(model.SideTypeSell, "BTCUSDT", 1, 120, 90, 89)

		require.NoError(t, err)
		require.Len(t, created, 2)
		require.Len(t, controller.pending, 2, "open orders are tracked until the exchange settles them")
	})

	t.Run("stop order is tracked", func(t *testing.T) {
		controller, exchange := newTestController(t)
		exchange.On("CreateOrderStop", "BTCUSDT", 1.0, 90.0).Return(model.Order{
			ExchangeID: 3, Pair: "BTCUSDT", Side: model.SideTypeSell, Type: model.OrderTypeStopLoss,
			Status: model.OrderStatusTypeNew, Price: 90, Quantity: 1,
		}, nil)

		order, err := controller.CreateOrderStop("BTCUSDT", 1, 90)

		require.NoError(t, err)
		require.Equal(t, model.OrderTypeStopLoss, order.Type)
		require.Len(t, controller.pending, 1)
	})

	t.Run("market quote order settles the trade", func(t *testing.T) {
		controller, exchange := newTestController(t)
		exchange.On("CreateOrderMarketQuote", model.SideTypeBuy, "BTCUSDT", 100.0).
			Return(filledOrder(4, model.SideTypeBuy, 100, 1), nil)

		order, err := controller.CreateOrderMarketQuote(model.SideTypeBuy, "BTCUSDT", 100)

		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeFilled, order.Status)
		require.Equal(t, 1.0, controller.position["BTCUSDT"].Quantity)
		require.Empty(t, controller.pending, "a filled order is not pending")
	})
}

func TestControllerCancel(t *testing.T) {
	t.Run("marks the order as pending cancel", func(t *testing.T) {
		controller, exchange := newTestController(t)
		open := model.Order{ExchangeID: 5, Pair: "BTCUSDT", Side: model.SideTypeBuy,
			Type: model.OrderTypeLimit, Status: model.OrderStatusTypeNew, Price: 100, Quantity: 1}
		exchange.On("CreateOrderLimit", model.SideTypeBuy, "BTCUSDT", 1.0, 100.0).Return(open, nil)
		exchange.On("Cancel", mock.Anything).Return(nil)

		created, err := controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 100)
		require.NoError(t, err)

		require.NoError(t, controller.Cancel(created))
		require.Equal(t, model.OrderStatusTypePendingCancel, controller.pending[created.ID].Status)
	})

	t.Run("propagates an exchange failure", func(t *testing.T) {
		controller, exchange := newTestController(t)
		exchange.On("Cancel", mock.Anything).Return(errors.New("order already filled"))

		err := controller.Cancel(model.Order{ID: 1, Pair: "BTCUSDT"})

		require.ErrorContains(t, err, "order already filled")
	})
}

// historyExchange reads its history by periods, see service.OrderHistory.
type historyExchange struct {
	*mocks.Exchange
	since time.Time
	order model.Order
}

func (e *historyExchange) OrderSince(_ string, _ int64, since time.Time) (model.Order, error) {
	e.since = since
	return e.order, nil
}

// A leg reported by the exchange without its pair, side, price or size, eg:
// of a pending entry canceled before its fill, keeps the ones stored: the
// controller refuses an order without a side.
func TestControllerUpdateOrdersKeepsWhatTheExchangeLeavesOut(t *testing.T) {
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	exchange := &historyExchange{Exchange: &mocks.Exchange{}}
	controller := NewController(context.Background(), exchange, memory, NewOrderFeed())

	created := time.Now().Add(-time.Minute)
	group, stop := int64(7), 4240.0
	stored := model.Order{ExchangeID: 12, Pair: "XAUUSDm", Side: model.SideTypeSell, Type: model.OrderTypeStopLoss,
		Status: model.OrderStatusTypeNew, Price: 4240, Quantity: 2, Stop: &stop, GroupID: &group,
		CreatedAt: created, UpdatedAt: created}
	require.NoError(t, memory.CreateOrder(&stored))
	controller.loadPendingOrders()

	canceledAt := time.Now()
	exchange.order = model.Order{ExchangeID: 12, Status: model.OrderStatusTypeCanceled, UpdatedAt: canceledAt}
	controller.UpdateOrders()

	orders, err := memory.Orders()
	require.NoError(t, err)
	require.Len(t, orders, 1)
	leg := *orders[0]
	require.Equal(t, model.OrderStatusTypeCanceled, leg.Status)
	require.Equal(t, "XAUUSDm", leg.Pair)
	require.Equal(t, model.SideTypeSell, leg.Side)
	require.Equal(t, model.OrderTypeStopLoss, leg.Type)
	require.Equal(t, 4240.0, leg.Price)
	require.Equal(t, 2.0, leg.Quantity)
	require.Equal(t, &stop, leg.Stop)
	require.Equal(t, &group, leg.GroupID)
	require.WithinDuration(t, canceledAt, leg.UpdatedAt, time.Millisecond, "the time of the exchange")

	// what the exchange tells wins, eg: the price and the size of a fill
	filled := model.Order{ExchangeID: 13, Pair: "XAUUSDm", Side: model.SideTypeBuy, Type: model.OrderTypeLimit,
		Status: model.OrderStatusTypeNew, Price: 4250, Quantity: 2, CreatedAt: created, UpdatedAt: created}
	require.NoError(t, memory.CreateOrder(&filled))
	controller.loadPendingOrders()
	exchange.order = model.Order{ExchangeID: 13, Pair: "XAUUSDm", Side: model.SideTypeBuy, Type: model.OrderTypeLimit,
		Status: model.OrderStatusTypeFilled, Price: 4249.5, Quantity: 1, UpdatedAt: time.Now()}
	controller.UpdateOrders()
	orders, err = memory.Orders(storage.WithStatus(model.OrderStatusTypeFilled))
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, 4249.5, orders[0].Price)
	require.Equal(t, 1.0, orders[0].Quantity)
}

func TestControllerUpdateOrdersSinceCreation(t *testing.T) {
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	exchange := &historyExchange{Exchange: &mocks.Exchange{}}
	controller := NewController(context.Background(), exchange, memory, NewOrderFeed())

	// an order left open by a previous run, 5 days ago
	created := time.Now().Add(-5 * 24 * time.Hour)
	stored := model.Order{ExchangeID: 11, Pair: "BTCUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeLimit,
		Status: model.OrderStatusTypeNew, Price: 100, Quantity: 1, CreatedAt: created, UpdatedAt: created}
	require.NoError(t, memory.CreateOrder(&stored))
	controller.loadPendingOrders()

	exchange.order = stored
	exchange.order.Status = model.OrderStatusTypeFilled
	controller.UpdateOrders()

	require.True(t, created.Equal(exchange.since), "searched from the creation of the order")
	exchange.AssertNotCalled(t, "Order", mock.Anything, mock.Anything)
	require.Empty(t, controller.pending)
	require.Equal(t, 1.0, controller.position["BTCUSDT"].Quantity)
}

func TestControllerUpdateOrders(t *testing.T) {
	t.Run("does nothing without pending orders", func(t *testing.T) {
		controller, exchange := newTestController(t)

		controller.UpdateOrders()

		exchange.AssertNotCalled(t, "Order", mock.Anything, mock.Anything)
	})

	t.Run("settles a fill and stops tracking the order", func(t *testing.T) {
		controller, exchange := newTestController(t)
		open := model.Order{ExchangeID: 6, Pair: "BTCUSDT", Side: model.SideTypeBuy,
			Type: model.OrderTypeLimit, Status: model.OrderStatusTypeNew, Price: 100, Quantity: 1}
		exchange.On("CreateOrderLimit", model.SideTypeBuy, "BTCUSDT", 1.0, 100.0).Return(open, nil)

		created, err := controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 100)
		require.NoError(t, err)

		filled := created
		filled.Status = model.OrderStatusTypeFilled
		exchange.On("Order", "BTCUSDT", int64(6)).Return(filled, nil)

		controller.UpdateOrders()

		require.Empty(t, controller.pending)
		require.Equal(t, 1.0, controller.position["BTCUSDT"].Quantity)
	})

	t.Run("keeps the fields the exchange does not report", func(t *testing.T) {
		controller, exchange := newTestController(t)
		stop := 90.0
		created := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
		open := model.Order{ExchangeID: 10, Pair: "BTCUSDT", Side: model.SideTypeSell,
			Type: model.OrderTypeStopLoss, Status: model.OrderStatusTypeNew, Price: stop, Stop: &stop, Quantity: 1,
			CreatedAt: created, UpdatedAt: created}
		exchange.On("CreateOrderStop", "BTCUSDT", 1.0, stop).Return(open, nil)

		_, err := controller.CreateOrderStop("BTCUSDT", 1, stop)
		require.NoError(t, err)

		// eg: an order of a previous run, rebuilt from a history without them
		exchange.On("Order", "BTCUSDT", int64(10)).Return(model.Order{ExchangeID: 10, Pair: "BTCUSDT",
			Side: model.SideTypeSell, Status: model.OrderStatusTypeFilled, Price: stop, Quantity: 1,
			UpdatedAt: created.Add(time.Hour)}, nil)

		controller.UpdateOrders()

		orders, err := controller.storage.Orders()
		require.NoError(t, err)
		require.Len(t, orders, 1)
		require.Equal(t, model.OrderStatusTypeFilled, orders[0].Status)
		require.Equal(t, model.OrderTypeStopLoss, orders[0].Type)
		require.Equal(t, &stop, orders[0].Stop)
		require.True(t, created.Equal(orders[0].CreatedAt))
		require.True(t, created.Add(time.Hour).Equal(orders[0].UpdatedAt))
	})

	t.Run("keeps tracking a partially filled order", func(t *testing.T) {
		controller, exchange := newTestController(t)
		open := model.Order{ExchangeID: 7, Pair: "BTCUSDT", Side: model.SideTypeBuy,
			Type: model.OrderTypeLimit, Status: model.OrderStatusTypeNew, Price: 100, Quantity: 2}
		exchange.On("CreateOrderLimit", model.SideTypeBuy, "BTCUSDT", 2.0, 100.0).Return(open, nil)

		created, err := controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 2, 100)
		require.NoError(t, err)

		partial := created
		partial.Status = model.OrderStatusTypePartiallyFilled
		exchange.On("Order", "BTCUSDT", int64(7)).Return(partial, nil)

		controller.UpdateOrders()

		require.Len(t, controller.pending, 1)
		require.Equal(t, model.OrderStatusTypePartiallyFilled, controller.pending[created.ID].Status)
	})

	t.Run("skips orders the exchange cannot report", func(t *testing.T) {
		controller, exchange := newTestController(t)
		open := model.Order{ExchangeID: 8, Pair: "BTCUSDT", Side: model.SideTypeBuy,
			Type: model.OrderTypeLimit, Status: model.OrderStatusTypeNew, Price: 100, Quantity: 1}
		exchange.On("CreateOrderLimit", model.SideTypeBuy, "BTCUSDT", 1.0, 100.0).Return(open, nil)

		created, err := controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 100)
		require.NoError(t, err)

		exchange.On("Order", "BTCUSDT", int64(8)).Return(model.Order{}, errors.New("timeout"))

		controller.UpdateOrders()

		require.Len(t, controller.pending, 1, "the order stays pending for the next round")
		require.Equal(t, model.OrderStatusTypeNew, controller.pending[created.ID].Status)
	})

	t.Run("ignores an unchanged status", func(t *testing.T) {
		controller, exchange := newTestController(t)
		open := model.Order{ExchangeID: 9, Pair: "BTCUSDT", Side: model.SideTypeBuy,
			Type: model.OrderTypeLimit, Status: model.OrderStatusTypeNew, Price: 100, Quantity: 1}
		exchange.On("CreateOrderLimit", model.SideTypeBuy, "BTCUSDT", 1.0, 100.0).Return(open, nil)

		created, err := controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 100)
		require.NoError(t, err)

		exchange.On("Order", "BTCUSDT", int64(9)).Return(created, nil)

		controller.UpdateOrders()

		require.Len(t, controller.pending, 1)
	})
}

func TestControllerLoadPendingOrders(t *testing.T) {
	t.Run("recovers the open orders left in storage", func(t *testing.T) {
		memory, err := storage.FromMemory()
		require.NoError(t, err)

		open := &model.Order{ExchangeID: 10, Pair: "BTCUSDT", Side: model.SideTypeBuy,
			Type: model.OrderTypeLimit, Status: model.OrderStatusTypeNew, Price: 100, Quantity: 1,
			CreatedAt: time.Now(), UpdatedAt: time.Now()}
		require.NoError(t, memory.CreateOrder(open))

		exchange := &mocks.Exchange{}
		exchange.On("Order", "BTCUSDT", int64(10)).Return(*open, nil).Maybe()

		controller := NewController(context.Background(), exchange, memory, NewOrderFeed())
		controller.Start()
		defer controller.Stop()

		require.Len(t, controller.pending, 1)
	})

	t.Run("reports a storage failure", func(t *testing.T) {
		controller, _ := newTestController(t)
		notifier := &mocks.Notifier{}
		notifier.On("OnError", mock.Anything).Return()
		controller.SetNotifier(notifier)
		controller.storage = failingStorage{}

		controller.loadPendingOrders()

		notifier.AssertNumberOfCalls(t, "OnError", 1)
		require.Empty(t, controller.pending)
	})
}

// failingStorage rejects every write and read, to exercise the recovery paths.
type failingStorage struct{}

var errStorageUnavailable = errors.New("storage unavailable")

func (failingStorage) CreateOrder(*model.Order) error { return errStorageUnavailable }

func (failingStorage) UpdateOrder(*model.Order) error { return errStorageUnavailable }

func (failingStorage) Orders(...storage.OrderFilter) ([]*model.Order, error) {
	return nil, errStorageUnavailable
}

// An order accepted by the exchange but not persisted is reported, never an
// error: it exists on the exchange, followed all the same, the storage failing
// to number it followed under a provisional id of its own.
func TestControllerStorageFailures(t *testing.T) {
	open := model.Order{ExchangeID: 20, Pair: "BTCUSDT", Side: model.SideTypeBuy,
		Type: model.OrderTypeLimit, Status: model.OrderStatusTypeNew, Price: 100, Quantity: 1}

	tests := []struct {
		name     string
		expect   func(*mocks.Exchange)
		run      func(*Controller) ([]model.Order, error)
		followed func(*testing.T, *Controller, []model.Order)
	}{
		{
			name: "OCO",
			expect: func(e *mocks.Exchange) {
				e.On("CreateOrderOCO", model.SideTypeSell, "BTCUSDT", 1.0, 120.0, 90.0, 89.0).
					Return([]model.Order{open, open}, nil)
			},
			run: func(c *Controller) ([]model.Order, error) {
				return c.CreateOrderOCO(model.SideTypeSell, "BTCUSDT", 1, 120, 90, 89)
			},
			followed: func(t *testing.T, c *Controller, orders []model.Order) {
				require.Len(t, orders, 2)
				require.Equal(t, []int64{-1, -2}, []int64{orders[0].ID, orders[1].ID}, "an id of their own")
				require.Len(t, c.pending, 2)
			},
		},
		{
			name: "limit",
			expect: func(e *mocks.Exchange) {
				e.On("CreateOrderLimit", model.SideTypeBuy, "BTCUSDT", 1.0, 100.0).Return(open, nil)
			},
			run: func(c *Controller) ([]model.Order, error) {
				order, err := c.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 100)
				return []model.Order{order}, err
			},
			followed: func(t *testing.T, c *Controller, orders []model.Order) {
				require.Equal(t, int64(20), orders[0].ExchangeID)
				require.Contains(t, c.pending, orders[0].ID)
			},
		},
		{
			name: "market",
			expect: func(e *mocks.Exchange) {
				e.On("CreateOrderMarket", model.SideTypeBuy, "BTCUSDT", 1.0).
					Return(filledOrder(21, model.SideTypeBuy, 100, 1), nil)
			},
			run: func(c *Controller) ([]model.Order, error) {
				order, err := c.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
				return []model.Order{order}, err
			},
			followed: func(t *testing.T, c *Controller, orders []model.Order) {
				require.Equal(t, int64(21), orders[0].ExchangeID)
				require.Equal(t, 1.0, c.position["BTCUSDT"].Quantity, "the fill settled")
			},
		},
		{
			name: "market quote",
			expect: func(e *mocks.Exchange) {
				e.On("CreateOrderMarketQuote", model.SideTypeBuy, "BTCUSDT", 100.0).
					Return(filledOrder(22, model.SideTypeBuy, 100, 1), nil)
			},
			run: func(c *Controller) ([]model.Order, error) {
				order, err := c.CreateOrderMarketQuote(model.SideTypeBuy, "BTCUSDT", 100)
				return []model.Order{order}, err
			},
			followed: func(t *testing.T, c *Controller, orders []model.Order) {
				require.Equal(t, int64(22), orders[0].ExchangeID)
				require.Equal(t, 1.0, c.position["BTCUSDT"].Quantity, "the fill settled")
			},
		},
		{
			name: "stop",
			expect: func(e *mocks.Exchange) {
				e.On("CreateOrderStop", "BTCUSDT", 1.0, 90.0).Return(open, nil)
			},
			run: func(c *Controller) ([]model.Order, error) {
				order, err := c.CreateOrderStop("BTCUSDT", 1, 90)
				return []model.Order{order}, err
			},
			followed: func(t *testing.T, c *Controller, orders []model.Order) {
				require.Equal(t, int64(20), orders[0].ExchangeID)
				require.Contains(t, c.pending, orders[0].ID)
			},
		},
		{
			name: "cancel",
			expect: func(e *mocks.Exchange) {
				e.On("Cancel", mock.Anything).Return(nil)
			},
			run: func(c *Controller) ([]model.Order, error) {
				canceled := open
				canceled.ID = 7
				c.pending[7] = canceled
				return nil, c.Cancel(canceled)
			},
			followed: func(t *testing.T, c *Controller, _ []model.Order) {
				require.Equal(t, model.OrderStatusTypePendingCancel, c.pending[7].Status)
				require.Equal(t, model.OrderStatusTypePendingCancel, c.unstored[7].Status, "stored again")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exchange := &mocks.Exchange{}
			tt.expect(exchange)

			notifier := &mocks.Notifier{}
			notifier.On("OnError", mock.Anything).Return()

			controller := NewController(context.Background(), exchange, failingStorage{}, NewOrderFeed())
			controller.SetNotifier(notifier)

			orders, err := tt.run(controller)
			require.NoError(t, err, "executed on the exchange")
			tt.followed(t, controller, orders)
			notifier.AssertCalled(t, "OnError", mock.MatchedBy(func(err error) bool {
				return errors.Is(err, errStorageUnavailable)
			}))
		})
	}
}

// UpdateOrders settles a fill the storage fails to write, the fill done on
// the exchange: notified once, written again on the next rounds.
func TestControllerUpdateOrdersStorageFailure(t *testing.T) {
	exch := &mocks.Exchange{}
	filled := filledOrder(23, model.SideTypeBuy, 100, 1)
	exch.On("Order", "BTCUSDT", int64(23)).Return(filled, nil)

	notifier := &mocks.Notifier{}
	notifier.On("OnError", mock.Anything).Return()

	controller := NewController(context.Background(), exch, failingStorage{}, NewOrderFeed())
	controller.SetNotifier(notifier)
	controller.pending[1] = model.Order{ID: 1, ExchangeID: 23, Pair: "BTCUSDT",
		Status: model.OrderStatusTypeNew, Side: model.SideTypeBuy, Quantity: 1, Price: 100}

	controller.UpdateOrders()
	controller.UpdateOrders()

	notifier.AssertNumberOfCalls(t, "OnError", 1)
	require.Empty(t, controller.pending, "the fill settled")
	require.Equal(t, 1.0, controller.position["BTCUSDT"].Quantity)
	require.Equal(t, model.OrderStatusTypeFilled, controller.unstored[1].Status, "written again")
}

// flakyStorage is a storage in memory failing its writes while failing is set,
// the orders created numbered all the same, as the storages number them before
// they write them.
type flakyStorage struct {
	storage.Storage
	mu      sync.Mutex
	failing bool
	next    int64
}

func newFlakyStorage(t *testing.T) *flakyStorage {
	t.Helper()
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	return &flakyStorage{Storage: memory, next: 1000}
}

func (f *flakyStorage) fail(failing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing = failing
}

func (f *flakyStorage) CreateOrder(order *model.Order) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		f.next++
		order.ID = f.next
		return errStorageUnavailable
	}
	return f.Storage.CreateOrder(order)
}

func (f *flakyStorage) UpdateOrder(order *model.Order) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing {
		return errStorageUnavailable
	}
	return f.Storage.UpdateOrder(order)
}

// A protected entry the storage fails to write is followed all the same: the
// entry settled, published, its legs followed until the stop loss closes the
// position, the profit counted; the orders stored in their last state once the
// storage writes, the failure and the recovery notified once.
func TestController_OrdersExecutedFollowedWhenTheStorageFails(t *testing.T) {
	ctx := context.Background()
	flaky := newFlakyStorage(t)
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 10000))
	feed := NewOrderFeed()
	var mu sync.Mutex
	var published []model.Order
	feed.Subscribe("BTCUSDT", func(order model.Order) {
		mu.Lock()
		defer mu.Unlock()
		published = append(published, order)
	}, false)
	feed.Start()
	controller := NewController(ctx, wallet, flaky, feed)
	notifier := &mocks.Notifier{}
	notifier.On("OnError", mock.Anything).Return()
	notifier.On("Notify", mock.Anything).Return()
	controller.SetNotifier(notifier)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: start, Low: 100, High: 100, Close: 100, Complete: true})

	flaky.fail(true)
	orders, err := controller.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 10, 90, 120)
	require.NoError(t, err, "executed on the exchange")
	require.Len(t, orders, 3)
	require.Equal(t, []int64{1001, 1002, 1003}, []int64{orders[0].ID, orders[1].ID, orders[2].ID})
	require.Equal(t, 10.0, controller.position["BTCUSDT"].Quantity, "the entry settled")
	require.Len(t, controller.pending, 2, "the legs followed")
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(published) == 3
	}, time.Second, time.Millisecond, "published")

	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: start.Add(time.Hour), Low: 85, High: 101, Close: 88,
		Complete: true})
	controller.UpdateOrders()
	require.Empty(t, controller.pending)
	require.Nil(t, controller.position["BTCUSDT"], "closed by the stop loss")
	require.Len(t, controller.Results["BTCUSDT"].LoseLong, 1)
	notifier.AssertNumberOfCalls(t, "OnError", 1)
	stored, err := flaky.Orders()
	require.NoError(t, err)
	require.Empty(t, stored)

	flaky.fail(false)
	controller.UpdateOrders()
	require.Empty(t, controller.unstored)
	statuses := map[model.OrderType]model.OrderStatusType{}
	stored, err = flaky.Orders()
	require.NoError(t, err)
	require.Len(t, stored, 3)
	for _, order := range stored {
		statuses[order.Type] = order.Status
		if order.Type == model.OrderTypeStopLoss {
			require.InDelta(t, -100, order.ProfitValue, 1e-9, "with its profit")
		}
	}
	require.Equal(t, map[model.OrderType]model.OrderStatusType{model.OrderTypeMarket: model.OrderStatusTypeFilled,
		model.OrderTypeStopLoss: model.OrderStatusTypeFilled, model.OrderTypeLimitMaker: model.OrderStatusTypeCanceled},
		statuses, "their last state")
	recovered := 0
	for _, call := range notifier.Calls {
		if call.Method == "Notify" && strings.Contains(call.Arguments.String(0), "stored at last") {
			recovered++
		}
	}
	require.Equal(t, 1, recovered, "the recovery notified")
	notifier.AssertNumberOfCalls(t, "OnError", 1)
}

// The orders the storage fails to number are followed apart, each under a
// provisional id of its own, never written under it.
func TestController_OrdersNotNumberedFollowedApart(t *testing.T) {
	ctx := context.Background()
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 10000))
	controller := NewController(ctx, wallet, failingStorage{}, NewOrderFeed())
	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: time.Now(), Low: 100, High: 100, Close: 100,
		Complete: true})

	first, err := controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 95)
	require.NoError(t, err)
	second, err := controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 2, 94)
	require.NoError(t, err)
	require.Equal(t, []int64{-1, -2}, []int64{first.ID, second.ID})
	require.Len(t, controller.pending, 2, "both followed")

	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: time.Now(), Low: 90, High: 100, Close: 92,
		Complete: true})
	controller.UpdateOrders()
	require.Empty(t, controller.pending)
	require.InDelta(t, 3, controller.position["BTCUSDT"].Quantity, 1e-9, "both settled")
	require.Empty(t, controller.unstored, "never written under a provisional id")
}

func TestSummarySaveReturns(t *testing.T) {
	result := &summary{
		Pair:            "BTCUSDT",
		WinLong:         []float64{100},
		WinLongPercent:  []float64{0.1},
		LoseLong:        []float64{-50},
		LoseLongPercent: []float64{-0.05},
	}

	t.Run("writes one return per line", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "returns.csv")

		require.NoError(t, result.SaveReturns(path))

		content, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, []string{"0.1000", "-0.0500"}, strings.Fields(string(content)))
	})

	t.Run("fails when the file cannot be created", func(t *testing.T) {
		err := result.SaveReturns(filepath.Join(t.TempDir(), "missing", "returns.csv"))

		require.Error(t, err)
	})
}

func TestSummaryString(t *testing.T) {
	result := &summary{
		Pair:            "BTCUSDT",
		WinLong:         []float64{100, 200},
		WinLongPercent:  []float64{0.1, 0.2},
		LoseLong:        []float64{-50},
		LoseLongPercent: []float64{-0.05},
		Volume:          1000,
	}

	table := result.String()

	require.Contains(t, table, "BTCUSDT")
	require.Contains(t, table, "Trades")
	require.Contains(t, table, "Volume")
	require.Contains(t, table, "250.0000 USDT", "profit is the sum of wins and losses")
}

func TestControllerCreateOrderMarketProtected(t *testing.T) {
	t.Run("stored and followed until the stop loss closes the position", func(t *testing.T) {
		memory, err := storage.FromMemory()
		require.NoError(t, err)
		ctx := context.Background()
		wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 10000))
		controller := NewController(ctx, wallet, memory, NewOrderFeed())
		start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: start, Low: 100, High: 100, Close: 100, Complete: true})

		orders, err := controller.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 10, 90, 120)
		require.NoError(t, err)
		require.Len(t, orders, 3)
		require.Equal(t, 10.0, controller.position["BTCUSDT"].Quantity, "the entry is settled")
		require.Len(t, controller.pending, 2, "the legs are followed")

		stored, err := memory.Orders()
		require.NoError(t, err)
		require.Len(t, stored, 3)

		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: start.Add(time.Hour), Low: 85, High: 101, Close: 88,
			Complete: true})
		controller.UpdateOrders()

		require.Empty(t, controller.pending)
		require.Nil(t, controller.position["BTCUSDT"], "closed by the stop loss")
		require.Len(t, controller.Results["BTCUSDT"].LoseLong, 1)
		require.InDelta(t, -100, controller.Results["BTCUSDT"].LoseLong[0], 1e-9)

		statuses := map[model.OrderType]model.OrderStatusType{}
		stored, err = memory.Orders()
		require.NoError(t, err)
		for _, order := range stored {
			statuses[order.Type] = order.Status
		}
		require.Equal(t, model.OrderStatusTypeFilled, statuses[model.OrderTypeStopLoss])
		require.Equal(t, model.OrderStatusTypeCanceled, statuses[model.OrderTypeLimitMaker])
	})

	t.Run("not supported by the exchange", func(t *testing.T) {
		controller, exchangeMock := newTestController(t)

		_, err := controller.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 1, 90, 0)
		require.ErrorIs(t, err, exchange.ErrNotSupported)
		exchangeMock.AssertNotCalled(t, "CreateOrderMarket", mock.Anything, mock.Anything, mock.Anything)
	})
}

func TestControllerUpdateProtection(t *testing.T) {
	t.Run("stored moved", func(t *testing.T) {
		memory, err := storage.FromMemory()
		require.NoError(t, err)
		ctx := context.Background()
		wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 10000))
		controller := NewController(ctx, wallet, memory, NewOrderFeed())
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: time.Now(), Low: 100, High: 100, Close: 100,
			Complete: true})

		orders, err := controller.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 10, 90, 0)
		require.NoError(t, err)
		moved, err := controller.UpdateProtection(orders[1], 95)
		require.NoError(t, err)
		require.Equal(t, orders[1].ID, moved.ID)
		require.Equal(t, 95.0, *controller.pending[moved.ID].Stop)

		stored, err := memory.Orders(storage.WithStatus(model.OrderStatusTypeNew))
		require.NoError(t, err)
		require.Len(t, stored, 1)
		require.Equal(t, 95.0, *stored[0].Stop)
		require.Equal(t, 95.0, stored[0].Price)
	})

	t.Run("not supported by the exchange", func(t *testing.T) {
		controller, _ := newTestController(t)
		_, err := controller.UpdateProtection(model.Order{ID: 1, ExchangeID: 2, Pair: "BTCUSDT"}, 95)
		require.ErrorIs(t, err, exchange.ErrNotSupported)
	})
}

// Protect places the legs of a position held, stored and followed as the ones
// of a protected entry, even with the bot stopped: it only protects.
func TestControllerProtect(t *testing.T) {
	setup := func(t *testing.T) (*Controller, *exchange.PaperWallet, storage.Storage) {
		t.Helper()
		memory, err := storage.FromMemory()
		require.NoError(t, err)
		ctx := context.Background()
		wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 10000),
			exchange.WithPaperAsset("BTC", 10))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), Low: 100,
			High: 100, Close: 100, Complete: true})
		return NewController(ctx, wallet, memory, NewOrderFeed()), wallet, memory
	}

	t.Run("stored and followed until the stop loss closes the position", func(t *testing.T) {
		controller, wallet, memory := setup(t)
		legs, err := controller.Protect(model.SideTypeBuy, "BTCUSDT", 10, 90, 120)
		require.NoError(t, err)
		require.Len(t, legs, 2)
		require.Len(t, controller.pending, 2, "the legs are followed")
		stored, err := memory.Orders(storage.WithStatus(model.OrderStatusTypeNew))
		require.NoError(t, err)
		require.Len(t, stored, 2)

		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC), Low: 85,
			High: 101, Close: 88, Complete: true})
		controller.UpdateOrders()
		require.Empty(t, controller.pending)
		statuses := map[model.OrderType]model.OrderStatusType{}
		stored, err = memory.Orders()
		require.NoError(t, err)
		for _, order := range stored {
			statuses[order.Type] = order.Status
		}
		require.Equal(t, model.OrderStatusTypeFilled, statuses[model.OrderTypeStopLoss])
		require.Equal(t, model.OrderStatusTypeCanceled, statuses[model.OrderTypeLimitMaker])
	})

	t.Run("with the bot stopped", func(t *testing.T) {
		controller, _, _ := setup(t)
		controller.Start()
		controller.Stop()
		_, err := controller.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 10)
		require.ErrorIs(t, err, ErrBotStopped)
		legs, err := controller.Protect(model.SideTypeBuy, "BTCUSDT", 10, 90, 120)
		require.NoError(t, err)
		require.Len(t, legs, 2)
	})

	t.Run("refused by the exchange", func(t *testing.T) {
		controller, _, memory := setup(t)
		_, err := controller.Protect(model.SideTypeSell, "BTCUSDT", 10, 110, 80)
		require.ErrorIs(t, err, exchange.ErrNotSupported, "no short held")
		stored, err := memory.Orders()
		require.NoError(t, err)
		require.Empty(t, stored)
	})

	t.Run("not supported by the exchange", func(t *testing.T) {
		controller, _ := newTestController(t)
		_, err := controller.Protect(model.SideTypeBuy, "BTCUSDT", 10, 90, 120)
		require.ErrorIs(t, err, exchange.ErrNotSupported)
	})
}

// closerExchange is an exchange closing a protected position with its legs in
// place, eg: MT5.
type closerExchange struct {
	*mocks.Exchange
}

func (closerExchange) ClosesProtected() bool { return true }

// ClosesProtected tells what the exchange does.
func TestControllerClosesProtected(t *testing.T) {
	controller, _ := newTestController(t)
	require.False(t, controller.ClosesProtected(), "not told")

	memory, err := storage.FromMemory()
	require.NoError(t, err)
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
	require.False(t, NewController(context.Background(), wallet, memory, NewOrderFeed()).ClosesProtected(),
		"the legs reserve the position in the paper wallet")

	closer := NewController(context.Background(), closerExchange{&mocks.Exchange{}}, memory, NewOrderFeed())
	require.True(t, closer.ClosesProtected())
}

func TestControllerUpdateLimit(t *testing.T) {
	setup := func(t *testing.T) (*Controller, storage.Storage) {
		t.Helper()
		memory, err := storage.FromMemory()
		require.NoError(t, err)
		ctx := context.Background()
		wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 10000))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: time.Now(), Low: 100, High: 100, Close: 100,
			Complete: true})
		return NewController(ctx, wallet, memory, NewOrderFeed()), memory
	}

	t.Run("stored moved", func(t *testing.T) {
		controller, memory := setup(t)
		order, err := controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 10, 90)
		require.NoError(t, err)

		moved, err := controller.UpdateLimit(order, 95)
		require.NoError(t, err)
		require.Equal(t, order.ID, moved.ID)
		require.Equal(t, 95.0, controller.pending[moved.ID].Price)

		stored, err := memory.Orders(storage.WithStatus(model.OrderStatusTypeNew))
		require.NoError(t, err)
		require.Len(t, stored, 1)
		require.Equal(t, 95.0, stored[0].Price)
	})

	t.Run("refused with the bot stopped", func(t *testing.T) {
		controller, _ := setup(t)
		order, err := controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 10, 90)
		require.NoError(t, err)

		controller.Start()
		controller.Stop()
		_, err = controller.UpdateLimit(order, 95)
		require.ErrorIs(t, err, ErrBotStopped)
	})

	t.Run("not supported by the exchange", func(t *testing.T) {
		controller, _ := newTestController(t)
		_, err := controller.UpdateLimit(model.Order{ID: 1, ExchangeID: 2, Pair: "BTCUSDT"}, 95)
		require.ErrorIs(t, err, exchange.ErrNotSupported)
	})
}

// within runs fn, failing the test unless it returns within wait.
func within(t *testing.T, wait time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatalf("%s: still waiting after %s", what, wait)
	}
}

// R1: a slow call to the exchange, eg: an MT5 trade request whose answer was
// lost, looked for in the account, holds the orders of its pair only:
// UpdateOrders, the reads and the orders of the other pairs go on, and the
// next order of the pair waits for it.
func TestController_SlowOrderHoldsItsPairOnly(t *testing.T) {
	controller, exch := newTestController(t)
	entered, release := make(chan struct{}), make(chan struct{})
	slow := filledOrder(1, model.SideTypeBuy, 100, 1)
	exch.On("CreateOrderMarket", model.SideTypeBuy, "BTCUSDT", 1.0).Run(func(mock.Arguments) {
		close(entered)
		<-release
	}).Return(slow, nil).Once()
	exch.On("CreateOrderMarket", model.SideTypeBuy, "BTCUSDT", 2.0).Return(filledOrder(2, model.SideTypeBuy, 100,
		2), nil).Once()
	other := filledOrder(3, model.SideTypeBuy, 10, 1)
	other.Pair = "ETHUSDT"
	exch.On("CreateOrderMarket", model.SideTypeBuy, "ETHUSDT", 1.0).Return(other, nil).Once()
	exch.On("Position", "ETHUSDT").Return(1.0, 0.0, nil)
	pendingOrder := model.Order{ID: 99, ExchangeID: 99, Pair: "ETHUSDT", Side: model.SideTypeSell,
		Type: model.OrderTypeLimit, Status: model.OrderStatusTypeNew, Price: 20, Quantity: 1}
	exch.On("Order", "ETHUSDT", int64(99)).Return(pendingOrder, nil)
	controller.pending[99] = pendingOrder

	first := make(chan error, 1)
	go func() {
		_, err := controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		first <- err
	}()
	<-entered

	within(t, time.Second, "another pair", func() {
		order, err := controller.CreateOrderMarket(model.SideTypeBuy, "ETHUSDT", 1)
		require.NoError(t, err)
		require.Equal(t, int64(3), order.ExchangeID)
	})
	within(t, time.Second, "UpdateOrders", controller.UpdateOrders)
	within(t, time.Second, "the status", func() { require.Equal(t, Status(""), controller.Status()) })
	within(t, time.Second, "the value of a position", func() {
		_, err := controller.PositionValue("ETHUSDT")
		require.NoError(t, err)
	})

	second := make(chan error, 1)
	go func() {
		_, err := controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 2)
		second <- err
	}()
	select {
	case err := <-second:
		t.Fatalf("the next order of the pair went before the slow one: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-first)
	require.NoError(t, <-second)
	require.InDelta(t, 3, controller.position["BTCUSDT"].Quantity, 1e-9, "both settled, in their order")
	exch.AssertExpectations(t)
}

// Stop waits for the orders begun to be recorded, and refuses the new ones
// meanwhile.
func TestController_StopWaitsForTheOrdersBegun(t *testing.T) {
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
	within(t, time.Second, "a new order", func() {
		_, err := controller.CreateOrderMarket(model.SideTypeSell, "ETHUSDT", 1)
		require.ErrorIs(t, err, ErrBotStopped)
	})
	select {
	case <-stopped:
		t.Fatal("stopped before the order begun was recorded")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-created)
	<-stopped
	orders, err := controller.storage.Orders()
	require.NoError(t, err)
	require.Len(t, orders, 1, "recorded before Stop returned")
	require.Equal(t, 1.0, controller.position["BTCUSDT"].Quantity)
}

// unstoredStorage is a storage in memory refusing for good the orders of
// pairs set, eg: the NaN of an order a JSON storage can't write, and while
// failing is set, the others.
type unstoredStorage struct {
	*flakyStorage
	refused map[string]bool
}

func (u *unstoredStorage) CreateOrder(order *model.Order) error {
	if u.refused[order.Pair] {
		u.mu.Lock()
		u.next++
		order.ID = u.next
		u.mu.Unlock()
		return errors.New("json: unsupported value: NaN")
	}
	return u.flakyStorage.CreateOrder(order)
}

func (u *unstoredStorage) UpdateOrder(order *model.Order) error {
	if u.refused[order.Pair] {
		return errors.New("json: unsupported value: NaN")
	}
	return u.flakyStorage.UpdateOrder(order)
}

// B5: an order the storage refuses for good never hides the failures of the
// next ones: each new failure is notified, unstoredNotifyEvery apart at most,
// the ones meanwhile together with the next notice, and so are the orders
// stored at last while the one refused stays.
func TestController_NewStorageFailuresNotifiedWithAnOrderRefusedForGood(t *testing.T) {
	every := unstoredNotifyEvery
	unstoredNotifyEvery = 200 * time.Millisecond
	t.Cleanup(func() { unstoredNotifyEvery = every })

	ctx := context.Background()
	st := &unstoredStorage{flakyStorage: newFlakyStorage(t), refused: map[string]bool{"ETHUSDT": true}}
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 100000))
	controller := NewController(ctx, wallet, st, NewOrderFeed())
	var mu sync.Mutex
	var errs, notices []string
	notifier := &mocks.Notifier{}
	notifier.On("OnError", mock.Anything).Run(func(args mock.Arguments) {
		mu.Lock()
		defer mu.Unlock()
		errs = append(errs, args.Error(0).Error())
	}).Return()
	notifier.On("Notify", mock.Anything).Run(func(args mock.Arguments) {
		mu.Lock()
		defer mu.Unlock()
		notices = append(notices, args.String(0))
	}).Return()
	controller.SetNotifier(notifier)
	for _, pair := range []string{"BTCUSDT", "ETHUSDT"} {
		wallet.OnCandle(model.Candle{Pair: pair, Time: time.Now(), Low: 100, High: 100, Close: 100, Complete: true})
	}
	counts := func() (int, int) {
		mu.Lock()
		defer mu.Unlock()
		return len(errs), len(notices)
	}

	_, err := controller.CreateOrderMarket(model.SideTypeBuy, "ETHUSDT", 1) // refused for good
	require.NoError(t, err)
	failures, _ := counts()
	require.Equal(t, 1, failures)

	st.fail(true)
	_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
	require.NoError(t, err)
	_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 2)
	require.NoError(t, err)
	failures, _ = counts()
	require.Equal(t, 1, failures, "within the notice of the first one")
	require.Eventually(t, func() bool {
		controller.UpdateOrders()
		failures, _ := counts()
		return failures == 2
	}, 2*time.Second, 10*time.Millisecond, "the new failures notified, though one order is refused for good")
	mu.Lock()
	require.Contains(t, errs[1], "and 1 other orders since the last notice, 3 not stored yet in all")
	mu.Unlock()

	st.fail(false)
	require.Eventually(t, func() bool {
		controller.UpdateOrders()
		_, stored := counts()
		return stored == 1
	}, 2*time.Second, 10*time.Millisecond, "the orders stored at last notified, though one is refused for good")
	mu.Lock()
	require.Contains(t, notices[0], "2 orders the storage failed to write are stored at last, 1 not yet")
	mu.Unlock()
	require.Len(t, controller.unstored, 1)
	failures, _ = counts()
	require.Equal(t, 2, failures)
}
