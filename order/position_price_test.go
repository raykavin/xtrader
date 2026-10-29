package order

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
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

// bookingWallet is a paper wallet reporting an account in BRL whose profits
// are booked once booked is set.
type bookingWallet struct {
	*exchange.PaperWallet
	mtx    sync.Mutex
	booked *float64
}

func (w *bookingWallet) AccountSummary() (service.AccountSummary, bool) {
	return service.AccountSummary{Currency: "BRL"}, true
}

func (w *bookingWallet) ClosedProfit(model.Order, model.Order) (float64, bool) {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	if w.booked == nil {
		return 0, false
	}
	return *w.booked, true
}

// The profit of a trade closed on an account is notified as the account books
// it, in its currency, and as awaited when it isn't booked in time: never the
// one computed from the prices, in the quote.
func TestController_ProfitInTheAccountCurrency(t *testing.T) {
	wait, poll := profitWait, profitPoll
	profitWait, profitPoll = 50*time.Millisecond, time.Millisecond
	t.Cleanup(func() { profitWait, profitPoll = wait, poll })

	profit := 51.8
	for name, booked := range map[string]*float64{"booked": &profit, "awaited": nil} {
		t.Run(name, func(t *testing.T) {
			memory, err := storage.FromMemory()
			require.NoError(t, err)
			ctx := context.Background()
			wallet := &bookingWallet{PaperWallet: exchange.NewPaperWallet(ctx, "USDT",
				exchange.WithPaperAsset("USDT", 3000)), booked: booked}
			controller := NewController(ctx, wallet, memory, NewOrderFeed())
			notified := &lockedNotices{}
			controller.SetNotifier(notified)

			base := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
			entry := model.Order{ExchangeID: 1, Pair: "BTCUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
				Status: model.OrderStatusTypeFilled, Quantity: 1, Price: 1000, CreatedAt: base, UpdatedAt: base}
			closing := model.Order{ExchangeID: 2, Pair: "BTCUSDT", Side: model.SideTypeSell,
				Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Quantity: 1, Price: 1010,
				CreatedAt: base, UpdatedAt: base.Add(time.Minute)}
			controller.mtx.Lock()
			for _, o := range []*model.Order{&entry, &closing} {
				controller.create(o)
				controller.processTrade(o)
			}
			controller.mtx.Unlock()

			require.Eventually(t, func() bool { return len(notified.all()) == 1 }, time.Second, time.Millisecond)
			message := notified.all()[0]
			assert.NotContains(t, message, "10.000000 USDT", "never in the quote")
			if booked != nil {
				assert.Contains(t, message, "[PROFIT] 51.80 BRL (1.0000 %)")
			} else {
				assert.Contains(t, message, "[PROFIT] awaiting the broker: the result in BRL isn't booked yet")
			}
			assert.Contains(t, message, "BTCUSDT | trades 1 | win 1 | loss 0")
		})
	}
}

// lockedNotices records the notifications sent from any goroutine.
type lockedNotices struct {
	mtx      sync.Mutex
	messages []string
}

func (n *lockedNotices) Notify(message string) {
	n.mtx.Lock()
	defer n.mtx.Unlock()
	n.messages = append(n.messages, message)
}

func (n *lockedNotices) OnOrder(model.Order) {}
func (n *lockedNotices) OnError(error)       {}

func (n *lockedNotices) all() []string {
	n.mtx.Lock()
	defer n.mtx.Unlock()
	return append([]string(nil), n.messages...)
}
