package order

import (
	"context"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
)

func TestController_updatePosition(t *testing.T) {
	t.Run("market orders", func(t *testing.T) {
		storage, err := storage.FromMemory()
		require.NoError(t, err)
		ctx := context.Background()
		wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 3000))
		controller := NewController(ctx, wallet, storage, NewOrderFeed())

		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 1000})
		_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		require.NoError(t, err)

		require.Equal(t, 1000.0, controller.position["BTCUSDT"].AvgPrice)
		require.Equal(t, 1.0, controller.position["BTCUSDT"].Quantity)
		assert.Equal(t, model.SideTypeBuy, controller.position["BTCUSDT"].Side)

		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 2000})
		_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		require.NoError(t, err)

		require.Equal(t, 1500.0, controller.position["BTCUSDT"].AvgPrice)
		require.Equal(t, 2.0, controller.position["BTCUSDT"].Quantity)

		// close half position 1BTC with 100% of profit
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 3000})
		order, err := controller.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 1)
		require.NoError(t, err)

		assert.Equal(t, 1500.0, controller.position["BTCUSDT"].AvgPrice)
		assert.Equal(t, 1.0, controller.position["BTCUSDT"].Quantity)

		assert.Equal(t, 1500.0, order.ProfitValue)
		assert.Equal(t, 1.0, order.Profit)

		// sell remaining BTC, 50% of loss
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 750})
		order, err = controller.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 1)
		require.NoError(t, err)

		assert.Nil(t, controller.position["BTCUSDT"]) // close position
		assert.Equal(t, -750.0, order.ProfitValue)
		assert.Equal(t, -0.5, order.Profit)
	})

	t.Run("limit order", func(t *testing.T) {
		storage, err := storage.FromMemory()
		require.NoError(t, err)
		ctx := context.Background()
		wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 3000))
		controller := NewController(ctx, wallet, storage, NewOrderFeed())
		wallet.OnCandle(model.Candle{Time: time.Now(), Pair: "BTCUSDT", High: 1500, Close: 1500})

		_, err = controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 1000)
		require.NoError(t, err)

		// should execute previous order
		wallet.OnCandle(model.Candle{Time: time.Now(), Pair: "BTCUSDT", High: 1000, Close: 1000})
		controller.UpdateOrders()

		require.Equal(t, 1000.0, controller.position["BTCUSDT"].AvgPrice)
		require.Equal(t, 1.0, controller.position["BTCUSDT"].Quantity)

		_, err = controller.CreateOrderLimit(model.SideTypeSell, "BTCUSDT", 1, 2000)
		require.NoError(t, err)

		// should execute previous order
		wallet.OnCandle(model.Candle{Time: time.Now(), Pair: "BTCUSDT", High: 2000, Close: 2000})
		controller.UpdateOrders()

		require.Nil(t, controller.position["BTCUSDT"])
		require.Len(t, controller.Results["BTCUSDT"].WinLong, 1)
		require.Equal(t, 1000.0, controller.Results["BTCUSDT"].WinLong[0])
		require.Len(t, controller.Results["BTCUSDT"].WinLongPercent, 1)
		require.Equal(t, 1.0, controller.Results["BTCUSDT"].WinLongPercent[0])
	})

	t.Run("oco order limit maker", func(t *testing.T) {
		storage, err := storage.FromMemory()
		require.NoError(t, err)
		ctx := context.Background()
		wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 3000))
		controller := NewController(ctx, wallet, storage, NewOrderFeed())
		wallet.OnCandle(model.Candle{Time: time.Now(), Pair: "BTCUSDT", High: 1500, Close: 1500})

		_, err = controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 1000)
		require.NoError(t, err)

		// should execute previous order
		wallet.OnCandle(model.Candle{Time: time.Now(), Pair: "BTCUSDT", High: 1000, Close: 1000})
		controller.UpdateOrders()

		_, err = controller.CreateOrderOCO(model.SideTypeSell, "BTCUSDT", 1, 2000, 500, 500)
		require.NoError(t, err)

		// should execute previous order
		wallet.OnCandle(model.Candle{Time: time.Now(), Pair: "BTCUSDT", High: 2000, Close: 2000})
		controller.UpdateOrders()

		require.Nil(t, controller.position["BTCUSDT"])
		require.Len(t, controller.Results["BTCUSDT"].WinLong, 1)
		require.Equal(t, 1000.0, controller.Results["BTCUSDT"].WinLong[0])
		require.Len(t, controller.Results["BTCUSDT"].WinLongPercent, 1)
		require.Equal(t, 1.0, controller.Results["BTCUSDT"].WinLongPercent[0])
	})

	t.Run("oco stop sell", func(t *testing.T) {
		storage, err := storage.FromMemory()
		require.NoError(t, err)
		ctx := context.Background()
		wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 3000))
		controller := NewController(ctx, wallet, storage, NewOrderFeed())
		wallet.OnCandle(model.Candle{Time: time.Now(), Pair: "BTCUSDT", Close: 1500, Low: 1500})

		_, err = controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 0.5, 1000)
		require.NoError(t, err)

		_, err = controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1.5, 1000)
		require.NoError(t, err)

		// should execute previous order
		wallet.OnCandle(model.Candle{Time: time.Now(), Pair: "BTCUSDT", Close: 1000, Low: 1000})
		controller.UpdateOrders()

		assert.Equal(t, 1000.0, controller.position["BTCUSDT"].AvgPrice)
		assert.Equal(t, 2.0, controller.position["BTCUSDT"].Quantity)

		_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1.0)
		require.NoError(t, err)

		assert.Equal(t, 1000.0, controller.position["BTCUSDT"].AvgPrice)
		assert.Equal(t, 3.0, controller.position["BTCUSDT"].Quantity)

		_, err = controller.CreateOrderOCO(model.SideTypeSell, "BTCUSDT", 1, 2000, 500, 500)
		require.NoError(t, err)

		// should execute previous order
		wallet.OnCandle(model.Candle{Time: time.Now(), Pair: "BTCUSDT", Close: 400, Low: 400})
		controller.UpdateOrders()

		assert.Equal(t, 1000.0, controller.position["BTCUSDT"].AvgPrice)
		assert.Equal(t, 2.0, controller.position["BTCUSDT"].Quantity)

		require.Len(t, controller.Results["BTCUSDT"].LoseLong, 1)
		require.Equal(t, -500.0, controller.Results["BTCUSDT"].LoseLong[0])
		require.Len(t, controller.Results["BTCUSDT"].LoseLongPercent, 1)
		require.Equal(t, -0.5, controller.Results["BTCUSDT"].LoseLongPercent[0])
	})

	t.Run("short market", func(t *testing.T) {
		storage, err := storage.FromMemory()
		require.NoError(t, err)
		ctx := context.Background()

		wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 0),
			exchange.WithPaperAsset("BTC", 2))
		controller := NewController(ctx, wallet, storage, NewOrderFeed())
		wallet.OnCandle(model.Candle{Time: time.Now(), Pair: "BTCUSDT", Close: 1500, Low: 1500})

		_, err = controller.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 1)
		require.NoError(t, err)

		assert.Equal(t, model.SideTypeSell, controller.position["BTCUSDT"].Side)
		assert.Equal(t, 1500.0, controller.position["BTCUSDT"].AvgPrice)
		assert.Equal(t, 1.0, controller.position["BTCUSDT"].Quantity)
	})
}

func TestController_PositionValue(t *testing.T) {
	storage, err := storage.FromMemory()
	require.NoError(t, err)
	ctx := context.Background()
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 3000))
	controller := NewController(ctx, wallet, storage, NewOrderFeed())

	lastCandle := model.Candle{Time: time.Now(), Pair: "BTCUSDT", Close: 1500, Low: 1500}

	// update wallet and controller
	wallet.OnCandle(lastCandle)
	controller.OnCandle(lastCandle)

	_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1.0)
	require.NoError(t, err)

	value, err := controller.PositionValue("BTCUSDT")
	require.NoError(t, err)
	assert.Equal(t, 1500.0, value)
}

func TestController_Position(t *testing.T) {
	storage, err := storage.FromMemory()
	require.NoError(t, err)
	ctx := context.Background()
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 3000))
	controller := NewController(ctx, wallet, storage, NewOrderFeed())

	lastCandle := model.Candle{Time: time.Now(), Pair: "BTCUSDT", Close: 1500, Low: 1500}

	// update wallet and controller
	wallet.OnCandle(lastCandle)
	controller.OnCandle(lastCandle)

	_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1.0)
	require.NoError(t, err)

	asset, quote, err := controller.Position("BTCUSDT")
	require.NoError(t, err)
	assert.Equal(t, 1.0, asset)
	assert.Equal(t, 1500.0, quote)
}

func TestController_StopGatesCreateOrder(t *testing.T) {
	storage, err := storage.FromMemory()
	require.NoError(t, err)
	ctx := context.Background()
	wallet := exchange.NewPaperWallet(ctx, "USDT",
		exchange.WithPaperAsset("USDT", 3000),
		exchange.WithPaperAsset("BTC", 1),
	)
	controller := NewController(ctx, wallet, storage, NewOrderFeed())
	defer controller.Stop()

	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 1000})

	// Zero-value status (never Start()'d) must still accept orders — public API.
	_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 0.1)
	require.NoError(t, err)

	controller.Start()
	pending, err := controller.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 0.1, 900)
	require.NoError(t, err)

	controller.Stop()

	_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 0.1)
	require.ErrorIs(t, err, ErrBotStopped)

	_, err = controller.CreateOrderMarketQuote(model.SideTypeBuy, "BTCUSDT", 100)
	require.ErrorIs(t, err, ErrBotStopped)

	_, err = controller.CreateOrderLimit(model.SideTypeSell, "BTCUSDT", 0.1, 1100)
	require.ErrorIs(t, err, ErrBotStopped)

	_, err = controller.CreateOrderStop("BTCUSDT", 0.1, 800)
	require.ErrorIs(t, err, ErrBotStopped)

	_, err = controller.CreateOrderOCO(model.SideTypeSell, "BTCUSDT", 0.1, 1100, 900, 890)
	require.ErrorIs(t, err, ErrBotStopped)

	// Cancel stays available while stopped so open orders can be cleaned up.
	require.NoError(t, controller.Cancel(pending))

	canceled, err := controller.Order("BTCUSDT", pending.ExchangeID)
	require.NoError(t, err)
	assert.Equal(t, model.OrderStatusTypeCanceled, canceled.Status)

	controller.Start()
	_, err = controller.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 0.1)
	require.NoError(t, err)
}

func TestPosition_Update(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	newOrder := func(side model.SideType, quantity, price float64) *model.Order {
		return &model.Order{
			Pair:      "BTCUSDT",
			Side:      side,
			Type:      model.OrderTypeMarket,
			Quantity:  quantity,
			Price:     price,
			CreatedAt: base.Add(time.Hour),
		}
	}

	t.Run("short closed in profit", func(t *testing.T) {
		position := &Position{Side: model.SideTypeSell, AvgPrice: 1000, Quantity: 2, CreatedAt: base}
		order := newOrder(model.SideTypeBuy, 2, 800)

		result, finished := position.Update(order)

		require.NotNil(t, result)
		assert.True(t, finished)
		assert.Equal(t, model.SideTypeSell, result.Side)
		assert.InDelta(t, 0.2, order.Profit, 1e-9)
		assert.InDelta(t, 400.0, order.ProfitValue, 1e-9)
	})

	t.Run("short closed in loss", func(t *testing.T) {
		position := &Position{Side: model.SideTypeSell, AvgPrice: 1000, Quantity: 1, CreatedAt: base}
		order := newOrder(model.SideTypeBuy, 1, 1200)

		result, finished := position.Update(order)

		require.NotNil(t, result)
		assert.True(t, finished)
		assert.Equal(t, model.SideTypeSell, result.Side)
		assert.InDelta(t, -0.2, order.Profit, 1e-9)
		assert.InDelta(t, -200.0, order.ProfitValue, 1e-9)
	})

	t.Run("short partially closed", func(t *testing.T) {
		position := &Position{Side: model.SideTypeSell, AvgPrice: 1000, Quantity: 2, CreatedAt: base}
		order := newOrder(model.SideTypeBuy, 1, 900)

		result, finished := position.Update(order)

		require.NotNil(t, result)
		assert.False(t, finished)
		assert.InDelta(t, 0.1, order.Profit, 1e-9)
		assert.InDelta(t, 100.0, order.ProfitValue, 1e-9)
		assert.Equal(t, model.SideTypeSell, position.Side)
		assert.Equal(t, 1.0, position.Quantity)
		assert.Equal(t, 1000.0, position.AvgPrice)
	})

	t.Run("long flipped to short", func(t *testing.T) {
		position := &Position{Side: model.SideTypeBuy, AvgPrice: 1000, Quantity: 1, CreatedAt: base}
		order := newOrder(model.SideTypeSell, 3, 1100)

		result, finished := position.Update(order)

		require.NotNil(t, result)
		assert.False(t, finished)

		// the closed leg is the old long: 1 unit bought at 1000, sold at 1100
		assert.Equal(t, model.SideTypeBuy, result.Side)
		assert.InDelta(t, 0.1, order.Profit, 1e-9)
		assert.InDelta(t, 100.0, order.ProfitValue, 1e-9)

		// the remainder opens a short at the order price
		assert.Equal(t, model.SideTypeSell, position.Side)
		assert.Equal(t, 2.0, position.Quantity)
		assert.Equal(t, 1100.0, position.AvgPrice)
		assert.Equal(t, order.CreatedAt, position.CreatedAt)
	})

	t.Run("short flipped to long", func(t *testing.T) {
		position := &Position{Side: model.SideTypeSell, AvgPrice: 1000, Quantity: 1, CreatedAt: base}
		order := newOrder(model.SideTypeBuy, 2, 900)

		result, finished := position.Update(order)

		require.NotNil(t, result)
		assert.False(t, finished)

		// the closed leg is the old short: sold at 1000, bought back at 900
		assert.Equal(t, model.SideTypeSell, result.Side)
		assert.InDelta(t, 0.1, order.Profit, 1e-9)
		assert.InDelta(t, 100.0, order.ProfitValue, 1e-9)

		// the remainder opens a long at the order price
		assert.Equal(t, model.SideTypeBuy, position.Side)
		assert.Equal(t, 1.0, position.Quantity)
		assert.Equal(t, 900.0, position.AvgPrice)
	})

	t.Run("fees are deducted from the profit", func(t *testing.T) {
		position := &Position{Side: model.SideTypeBuy, AvgPrice: 1000, Quantity: 1, Fee: 1, CreatedAt: base}
		order := newOrder(model.SideTypeSell, 1, 1100)
		order.Fee = 1.1

		result, finished := position.Update(order)

		require.NotNil(t, result)
		assert.True(t, finished)
		assert.InDelta(t, 97.9, order.ProfitValue, 1e-9) // 100 - 1 - 1.1
		assert.InDelta(t, 0.0979, order.Profit, 1e-9)
		assert.InDelta(t, order.ProfitValue, result.ProfitValue, 1e-9)
	})

	t.Run("only the closed share of the fees is settled", func(t *testing.T) {
		position := &Position{Side: model.SideTypeBuy, AvgPrice: 1000, Quantity: 2, Fee: 2, CreatedAt: base}
		order := newOrder(model.SideTypeSell, 1, 1100)
		order.Fee = 1.1

		result, finished := position.Update(order)

		require.NotNil(t, result)
		assert.False(t, finished)
		assert.InDelta(t, 97.9, order.ProfitValue, 1e-9) // 100 - 1 (half the entry) - 1.1
		assert.Equal(t, 1.0, position.Quantity)
		assert.InDelta(t, 1.0, position.Fee, 1e-9) // the fee of the open half
	})

	t.Run("a flip carries the unused fee to the new position", func(t *testing.T) {
		position := &Position{Side: model.SideTypeBuy, AvgPrice: 1000, Quantity: 1, Fee: 1, CreatedAt: base}
		order := newOrder(model.SideTypeSell, 3, 1100)
		order.Fee = 3.3

		result, finished := position.Update(order)

		require.NotNil(t, result)
		assert.False(t, finished)
		assert.InDelta(t, 97.9, order.ProfitValue, 1e-9) // 100 - 1 - 1.1 (a third of the exit)
		assert.Equal(t, 2.0, position.Quantity)
		assert.InDelta(t, 2.2, position.Fee, 1e-9) // the remaining two thirds
	})

	t.Run("adding to a position accumulates the fees", func(t *testing.T) {
		position := &Position{Side: model.SideTypeBuy, AvgPrice: 1000, Quantity: 1, Fee: 1, CreatedAt: base}
		order := newOrder(model.SideTypeBuy, 1, 1200)
		order.Fee = 1.2

		result, finished := position.Update(order)

		require.Nil(t, result)
		assert.False(t, finished)
		assert.InDelta(t, 2.2, position.Fee, 1e-9)
	})

	t.Run("stop loss settles at the fill price reported by the exchange", func(t *testing.T) {
		// live exchanges report the executed price in Price and don't return
		// the stop; it must not be dereferenced
		position := &Position{Side: model.SideTypeSell, AvgPrice: 1000, Quantity: 1, CreatedAt: base}
		order := newOrder(model.SideTypeBuy, 1, 950)
		order.Type = model.OrderTypeStopLoss
		order.Stop = nil

		var result *Result
		var finished bool
		require.NotPanics(t, func() { result, finished = position.Update(order) })

		require.NotNil(t, result)
		assert.True(t, finished)
		assert.InDelta(t, 0.05, order.Profit, 1e-9)
		assert.InDelta(t, 50.0, order.ProfitValue, 1e-9)
	})

	t.Run("rounding noise does not leave a dust position open", func(t *testing.T) {
		position := &Position{Side: model.SideTypeBuy, AvgPrice: 1000, Quantity: 0.1, CreatedAt: base}
		_, finished := position.Update(newOrder(model.SideTypeBuy, 0.2, 1000))
		require.False(t, finished)
		require.NotEqual(t, 0.3, position.Quantity) // 0.1 + 0.2 != 0.3 in floating point

		result, finished := position.Update(newOrder(model.SideTypeSell, 0.3, 1100))
		require.NotNil(t, result)
		assert.True(t, finished)
		assert.InDelta(t, 30.0, result.ProfitValue, 1e-9)
	})
}

func TestSummary_Metrics(t *testing.T) {
	t.Run("empty summary has no NaN", func(t *testing.T) {
		s := summary{Pair: "BTCUSDT"}
		assert.Equal(t, 0.0, s.SQN())
		assert.Equal(t, 0.0, s.Payoff())
		assert.Equal(t, 0.0, s.ProfitFactor())
		assert.Equal(t, 0.0, s.WinPercentage())
		assert.NotContains(t, s.String(), "NaN")
	})

	t.Run("single trade has a zero SQN", func(t *testing.T) {
		s := summary{Pair: "BTCUSDT", WinLong: []float64{10}, WinLongPercent: []float64{0.1}}
		assert.Equal(t, 0.0, s.SQN())
	})

	t.Run("sqn, payoff and profit factor", func(t *testing.T) {
		s := summary{
			Pair:             "BTCUSDT",
			WinLong:          []float64{10, 20},
			WinLongPercent:   []float64{0.10, 0.20},
			LoseShort:        []float64{-5},
			LoseShortPercent: []float64{-0.05},
		}
		// mean = 25/3, population stddev of {10, 20, -5}
		mean := 25.0 / 3
		variance := ((10-mean)*(10-mean) + (20-mean)*(20-mean) + (-5-mean)*(-5-mean)) / 3
		assert.InDelta(t, math.Sqrt(3)*mean/math.Sqrt(variance), s.SQN(), 1e-9)
		assert.InDelta(t, 3.0, s.Payoff(), 1e-9)       // avg win 0.15 / avg loss 0.05
		assert.InDelta(t, 6.0, s.ProfitFactor(), 1e-9) // 0.30 / 0.05
		assert.InDelta(t, 25.0, s.Profit(), 1e-9)
		assert.InDelta(t, 200.0/3, s.WinPercentage(), 1e-9)

		// the report prints the profit factor, not the payoff twice
		out := s.String()
		assert.Contains(t, out, "3.000")
		assert.Contains(t, out, "6.000")
	})
}

// The positions and the results are rebuilt from the stored fills at the
// start: the legs filling after a restart close the positions they protect.
// The trades of the incident: a short stopped out, a short at its take profit,
// a long at its take profit.
func TestController_RestoresFromStorage(t *testing.T) {
	db, err := storage.FromMemory()
	require.NoError(t, err)
	base := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	group := int64(1)
	fill := func(minute int, side model.SideType, kind model.OrderType, price float64, grouped bool) *model.Order {
		at := base.Add(time.Duration(minute) * time.Minute)
		order := &model.Order{Pair: "BTCUSDm", Side: side, Type: kind, Status: model.OrderStatusTypeFilled,
			Price: price, Quantity: 0.1, CreatedAt: at, UpdatedAt: at}
		if grouped {
			order.GroupID = &group
		}
		require.NoError(t, db.CreateOrder(order))
		return order
	}
	fill(0, model.SideTypeSell, model.OrderTypeMarket, 84329.07, false)
	stopped := fill(14, model.SideTypeBuy, model.OrderTypeStopLoss, 84474, true)
	fill(270, model.SideTypeSell, model.OrderTypeMarket, 84447.38, false)
	target := fill(293, model.SideTypeBuy, model.OrderTypeLimitMaker, 84320, true)
	entry := fill(315, model.SideTypeBuy, model.OrderTypeMarket, 84299.94, false)
	open := &model.Order{Pair: "BTCUSDm", Side: model.SideTypeSell, Type: model.OrderTypeLimitMaker,
		Status: model.OrderStatusTypeNew, Price: 84449, Quantity: 0.1, GroupID: &group,
		CreatedAt: entry.CreatedAt, UpdatedAt: entry.CreatedAt}
	require.NoError(t, db.CreateOrder(open)) // the take profit of the long, pending at the restart

	ctx := context.Background()
	wallet := exchange.NewPaperWallet(ctx, "USD", exchange.WithPaperAsset("USD", 10000))
	c := NewController(ctx, wallet, db, NewOrderFeed())

	results := c.Results["BTCUSDm"]
	require.InDeltaSlice(t, []float64{12.738}, results.Win(), 1e-6, "the short at its take profit")
	require.InDeltaSlice(t, []float64{-14.493}, results.Lose(), 1e-6, "the short stopped out")
	require.Equal(t, model.SideTypeBuy, c.position["BTCUSDm"].Side, "the long left open")
	require.InDelta(t, 84299.94, c.position["BTCUSDm"].AvgPrice, 1e-9)

	// the profits are stored with the orders closing the positions
	for id, want := range map[int64]float64{stopped.ID: -14.493, target.ID: 12.738} {
		stored, err := db.Orders(storage.WithStatus(model.OrderStatusTypeFilled))
		require.NoError(t, err)
		for _, order := range stored {
			if order.ID == id {
				require.InDelta(t, want, order.ProfitValue, 1e-6, "order %d", id)
			}
		}
	}

	// the take profit filling after the restart closes the long: a win
	open.Status, open.UpdatedAt = model.OrderStatusTypeFilled, base.Add(350*time.Minute)
	c.processTrade(open)
	require.InDeltaSlice(t, []float64{12.738, 14.906}, sorted(c.Results["BTCUSDm"].Win()), 1e-6)
	require.NotContains(t, c.position, "BTCUSDm", "flat")

	// restored again, the same results: the stored profits recomputed alike
	again := NewController(ctx, wallet, db, NewOrderFeed())
	require.InDeltaSlice(t, []float64{12.738, 14.906}, sorted(again.Results["BTCUSDm"].Win()), 1e-6)
	require.Len(t, again.Results["BTCUSDm"].Lose(), 1)
}

// sorted returns a sorted copy of values: the results group the longs first.
func sorted(values []float64) []float64 {
	values = append([]float64(nil), values...)
	sort.Float64s(values)
	return values
}
