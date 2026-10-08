package tools

import (
	"testing"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockBroker is a mock implementation of the service.Broker interface
type MockBroker struct {
	mock.Mock
}

func (m *MockBroker) Account() (model.Account, error) {
	args := m.Called()
	return args.Get(0).(model.Account), args.Error(1)
}

func (m *MockBroker) Position(pair string) (asset, quote float64, err error) {
	args := m.Called(pair)
	return args.Get(0).(float64), args.Get(1).(float64), args.Error(2)
}

func (m *MockBroker) Order(pair string, id int64) (model.Order, error) {
	args := m.Called(pair, id)
	return args.Get(0).(model.Order), args.Error(1)
}

func (m *MockBroker) CreateOrderOCO(side model.SideType, pair string, size, price, stop, stopLimit float64) ([]model.Order, error) {
	args := m.Called(side, pair, size, price, stop, stopLimit)
	return args.Get(0).([]model.Order), args.Error(1)
}

func (m *MockBroker) CreateOrderLimit(side model.SideType, pair string, size float64, limit float64) (model.Order, error) {
	args := m.Called(side, pair, size, limit)
	return args.Get(0).(model.Order), args.Error(1)
}

func (m *MockBroker) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	args := m.Called(side, pair, size)
	return args.Get(0).(model.Order), args.Error(1)
}

func (m *MockBroker) CreateOrderMarketQuote(side model.SideType, pair string, quote float64) (model.Order, error) {
	args := m.Called(side, pair, quote)
	return args.Get(0).(model.Order), args.Error(1)
}

func (m *MockBroker) CreateOrderStop(pair string, quantity float64, limit float64) (model.Order, error) {
	args := m.Called(pair, quantity, limit)
	return args.Get(0).(model.Order), args.Error(1)
}

func (m *MockBroker) Cancel(order model.Order) error {
	args := m.Called(order)
	return args.Error(0)
}

// Ensure MockBroker implements service.Broker
var _ service.Broker = (*MockBroker)(nil)

func TestScheduler(t *testing.T) {
	pair := "BTCUSDT"
	scheduler := NewScheduler(pair)
	mockBroker := new(MockBroker)

	// Test BuyWhen
	scheduler.BuyWhen(1.0, func(df *xtrader.Dataframe) bool {
		return df.Close.Last(0) > 100
	})

	// Test SellWhen
	scheduler.SellWhen(0.5, func(df *xtrader.Dataframe) bool {
		return df.Close.Last(0) < 50
	})

	// Case 1: No condition met
	df := &xtrader.Dataframe{
		Pair:  pair,
		Close: xtrader.Series{75},
	}
	scheduler.Update(df, mockBroker)
	mockBroker.AssertNotCalled(t, "CreateOrderMarket")

	// Case 2: Buy condition met
	df.Close = xtrader.Series{150}
	mockBroker.On("CreateOrderMarket", xtrader.SideTypeBuy, pair, 1.0).Return(model.Order{}, nil)
	scheduler.Update(df, mockBroker)
	mockBroker.AssertExpectations(t)

	// Verify that the executed condition is removed
	assert.Len(t, scheduler.orderConditions, 1) // Only Sell condition remains

	// Case 3: Sell condition met
	df.Close = xtrader.Series{40}
	mockBroker.On("CreateOrderMarket", xtrader.SideTypeSell, pair, 0.5).Return(model.Order{}, nil)
	scheduler.Update(df, mockBroker)
	mockBroker.AssertExpectations(t)

	// Verify that all conditions are removed
	assert.Len(t, scheduler.orderConditions, 0)

	// Case 4: Broker error
	mockBrokerErr := new(MockBroker)
	scheduler.BuyWhen(1.0, func(df *xtrader.Dataframe) bool {
		return true
	})
	mockBrokerErr.On("CreateOrderMarket", xtrader.SideTypeBuy, pair, 1.0).Return(model.Order{}, assert.AnError)
	scheduler.Update(df, mockBrokerErr)
	mockBrokerErr.AssertExpectations(t)

	// Verify that the condition is NOT removed on error
	assert.Len(t, scheduler.orderConditions, 1)
}

// An order that always fails, eg: refused by the broker for good, is sent
// MaxOrderAttempts times at most, once per candle its condition is met, and
// the condition dropped, never sent again on each candle.
func TestScheduler_FailingOrderDroppedAfterItsAttempts(t *testing.T) {
	pair := "BTCUSDT"
	scheduler := NewScheduler(pair)
	broker := new(MockBroker)
	broker.On("CreateOrderMarket", xtrader.SideTypeSell, pair, 2.0).Return(model.Order{}, assert.AnError)
	broker.On("CreateOrderMarket", xtrader.SideTypeBuy, pair, 1.0).Return(model.Order{}, nil)

	scheduler.SellWhen(2.0, func(*xtrader.Dataframe) bool { return true })
	met := false
	scheduler.BuyWhen(1.0, func(*xtrader.Dataframe) bool { return met })

	df := &xtrader.Dataframe{Pair: pair, Close: xtrader.Series{75}}
	for range MaxOrderAttempts + 5 {
		scheduler.Update(df, broker)
	}
	broker.AssertNumberOfCalls(t, "CreateOrderMarket", MaxOrderAttempts)
	assert.Len(t, scheduler.orderConditions, 1, "the condition not met kept")

	met = true
	scheduler.Update(df, broker)
	broker.AssertCalled(t, "CreateOrderMarket", xtrader.SideTypeBuy, pair, 1.0)
	assert.Empty(t, scheduler.orderConditions)
}
