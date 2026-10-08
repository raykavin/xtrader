package order

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
)

// R12: a fill or a position without a price, eg: reported by the exchange
// without it, has no profit: never a NaN or an infinity, which JSON can't
// write, nor a loss of the whole position made of a price of 0. The position
// is followed all the same.
func TestPosition_UpdateWithoutPriceHasNoProfit(t *testing.T) {
	base := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	fill := func(side model.SideType, quantity, price, fee float64) *model.Order {
		return &model.Order{Pair: "BTCUSDT", Side: side, Type: model.OrderTypeMarket, Quantity: quantity,
			Price: price, Fee: fee, CreatedAt: base.Add(time.Hour)}
	}
	for _, tc := range []struct {
		name     string
		position Position
		order    *model.Order
		finished bool
	}{
		{"entry without a price", Position{Side: model.SideTypeBuy, Quantity: 1, CreatedAt: base},
			fill(model.SideTypeSell, 1, 1100, 0), true},
		{"entry without a price, fees", Position{Side: model.SideTypeBuy, Quantity: 1, Fee: 1, CreatedAt: base},
			fill(model.SideTypeSell, 1, 1100, 1), true},
		{"entry without a price, nothing", Position{Side: model.SideTypeSell, Quantity: 1, CreatedAt: base},
			fill(model.SideTypeBuy, 1, 0, 0), true},
		{"close without a price", Position{Side: model.SideTypeBuy, AvgPrice: 1000, Quantity: 1, CreatedAt: base},
			fill(model.SideTypeSell, 1, 0, 0), true},
		{"close without a price, part", Position{Side: model.SideTypeSell, AvgPrice: 1000, Quantity: 2,
			CreatedAt: base}, fill(model.SideTypeBuy, 1, 0, 0.5), false},
		{"price not a number", Position{Side: model.SideTypeBuy, AvgPrice: 1000, Quantity: 1, CreatedAt: base},
			fill(model.SideTypeSell, 1, math.NaN(), 0), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			position := tc.position
			result, finished := position.Update(tc.order)

			require.NotNil(t, result)
			assert.Equal(t, tc.finished, finished)
			assert.Zero(t, tc.order.Profit)
			assert.Zero(t, tc.order.ProfitValue)
			assert.Zero(t, result.ProfitPercent)
			assert.Zero(t, result.ProfitValue)
			_, fields := tc.order.Finite()
			assert.NotContains(t, fields, "profit")
			assert.NotContains(t, fields, "profit_value")
			assert.False(t, math.IsNaN(position.AvgPrice) || math.IsInf(position.AvgPrice, 0))
		})
	}

	t.Run("added without a price", func(t *testing.T) {
		position := Position{Side: model.SideTypeBuy, AvgPrice: 1000, Quantity: 1, CreatedAt: base}
		_, finished := position.Update(fill(model.SideTypeBuy, 1, 0, 0))
		require.False(t, finished)
		assert.Equal(t, 2.0, position.Quantity)
		assert.Zero(t, position.AvgPrice, "unknown: no average made of a price of 0")

		closing := fill(model.SideTypeSell, 2, 1100, 0)
		result, finished := position.Update(closing)
		require.True(t, finished)
		assert.Zero(t, result.ProfitValue)
		assert.Zero(t, closing.Profit)
	})

	t.Run("priced", func(t *testing.T) {
		position := Position{Side: model.SideTypeBuy, AvgPrice: 1000, Quantity: 1, CreatedAt: base}
		closing := fill(model.SideTypeSell, 1, 1100, 0)
		result, finished := position.Update(closing)
		require.True(t, finished)
		assert.InDelta(t, 0.1, result.ProfitPercent, 1e-9)
		assert.InDelta(t, 0.1, closing.Profit, 1e-9)
		assert.InDelta(t, 100, closing.ProfitValue, 1e-9)
	})
}

// R12: the order controller settles a fill without a price as a trade of an
// unknown profit: stored and published with finite numbers, its position
// closed, and left out of the results rather than counted as a win of 0.
func TestController_FillWithoutPriceHasNoProfit(t *testing.T) {
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	ctx := context.Background()
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 3000))
	feed := NewOrderFeed()
	handed := &handedOrders{}
	feed.Subscribe("BTCUSDT", handed.add, false)
	feed.Start()
	controller := NewController(ctx, wallet, memory, feed)
	notified := &notices{}
	controller.SetNotifier(notified)

	base := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	entry := model.Order{ExchangeID: 1, Pair: "BTCUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Quantity: 1, Price: 0, CreatedAt: base, UpdatedAt: base}
	closing := model.Order{ExchangeID: 2, Pair: "BTCUSDT", Side: model.SideTypeSell,
		Type: model.OrderTypeStopLoss, Status: model.OrderStatusTypeFilled, Quantity: 1, Price: 900,
		CreatedAt: base, UpdatedAt: base.Add(time.Minute)}
	controller.mtx.Lock()
	for _, o := range []*model.Order{&entry, &closing} {
		controller.create(o)
		controller.processTrade(o)
		controller.publish(*o, true)
	}
	controller.mtx.Unlock()

	assert.Nil(t, controller.position["BTCUSDT"], "closed")
	assert.Zero(t, closing.Profit)
	assert.Zero(t, closing.ProfitValue)
	summary := controller.Results["BTCUSDT"]
	require.NotNil(t, summary)
	assert.Empty(t, summary.Win())
	assert.Empty(t, summary.Lose())
	require.Len(t, notified.messages, 1)
	assert.Contains(t, notified.messages[0], "[PROFIT] unknown USDT")

	stored, err := memory.Orders(storage.WithPair("BTCUSDT"))
	require.NoError(t, err)
	require.Len(t, stored, 2)
	require.Eventually(t, func() bool {
		handed.mtx.Lock()
		defer handed.mtx.Unlock()
		return len(handed.orders) == 2
	}, 5*time.Second, time.Millisecond)
	handed.mtx.Lock()
	defer handed.mtx.Unlock()
	for _, o := range handed.orders {
		_, err := json.Marshal(o)
		require.NoError(t, err)
	}
}

// R12: a position opened by a fill whose price isn't a number has its price
// unknown, 0, never a NaN: its close has no profit.
func TestController_PositionOpenedWithoutPrice(t *testing.T) {
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	ctx := context.Background()
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 3000))
	controller := NewController(ctx, wallet, memory, nil)
	base := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	entry := model.Order{ID: 1, Pair: "BTCUSDT", Side: model.SideTypeSell, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Quantity: 1, Price: math.NaN(), CreatedAt: base, UpdatedAt: base}
	closing := model.Order{ID: 2, Pair: "BTCUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Quantity: 1, Price: 900, CreatedAt: base,
		UpdatedAt: base.Add(time.Minute)}

	controller.mtx.Lock()
	defer controller.mtx.Unlock()
	controller.processTrade(&entry)
	require.NotNil(t, controller.position["BTCUSDT"])
	assert.Zero(t, controller.position["BTCUSDT"].AvgPrice)
	controller.processTrade(&closing)
	assert.Nil(t, controller.position["BTCUSDT"])
	assert.Zero(t, closing.Profit)
	assert.Zero(t, closing.ProfitValue)
	assert.Empty(t, controller.Results["BTCUSDT"].Win())
}

// notices keeps the messages of the controller.
type notices struct{ messages []string }

func (n *notices) Notify(message string) { n.messages = append(n.messages, message) }
func (n *notices) OnOrder(model.Order)   {}
func (n *notices) OnError(error)         {}
