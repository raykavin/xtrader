package exchange

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

func TestPaperWallet_LockAndFill(t *testing.T) {
	t.Run("simple lock limit", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		err := wallet.lockOrder(1, model.SideTypeBuy, "BTCUSDT", 1, 100, 0)
		require.NoError(t, err)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 100.0, wallet.assets["USDT"].Lock)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)

		// releasing gives everything back, twice is harmless
		wallet.release(1, "BTCUSDT")
		wallet.release(1, "BTCUSDT")
		require.Equal(t, 100.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
	})

	t.Run("simple buy market", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		wallet.lastCandle["BTCUSDT"] = model.Candle{Pair: "BTCUSDT", Close: 100}
		_, _, err := wallet.fillOrder(model.SideTypeBuy, "BTCUSDT", 1, 100, 0)
		require.NoError(t, err)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
		require.Equal(t, 1.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
	})

	t.Run("simple short market", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		wallet.lastCandle["BTCUSDT"] = model.Candle{Pair: "BTCUSDT", Close: 100}
		_, _, err := wallet.fillOrder(model.SideTypeSell, "BTCUSDT", 1, 100, 0)
		require.NoError(t, err)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
		require.Equal(t, -1.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
	})

	t.Run("simple short limit", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		err := wallet.lockOrder(1, model.SideTypeSell, "BTCUSDT", 1, 100, 0)
		require.NoError(t, err)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 100.0, wallet.assets["USDT"].Lock)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
	})

	t.Run("sell locks the long and collateralizes the rest", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("BTC", 1), WithPaperAsset("USDT", 100))
		err := wallet.lockOrder(1, model.SideTypeSell, "BTCUSDT", 2, 100, 0)
		require.NoError(t, err)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.Equal(t, 1.0, wallet.assets["BTC"].Lock)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 100.0, wallet.assets["USDT"].Lock)
	})

	t.Run("buy covering a short reserves only the excess", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("BTC", -1), WithPaperAsset("USDT", 100))
		wallet.avgShortPrice["BTCUSDT"] = 100
		err := wallet.lockOrder(1, model.SideTypeBuy, "BTCUSDT", 2, 50, 0)
		require.NoError(t, err)
		require.Equal(t, -1.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
		require.Equal(t, 50.0, wallet.assets["USDT"].Free)
		require.Equal(t, 50.0, wallet.assets["USDT"].Lock)
	})

	t.Run("invert position long to short", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("BTC", 1), WithPaperAsset("USDT", 100))
		wallet.avgLongPrice["BTCUSDT"] = 100

		// 1 BTC is sold out of the long (+100 USDT), 1 BTC is shorted (-100 USDT collateral)
		_, _, err := wallet.fillOrder(model.SideTypeSell, "BTCUSDT", 2, 100, 0)
		require.NoError(t, err)
		require.Equal(t, 100.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
		require.Equal(t, -1.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
		require.Equal(t, 100.0, wallet.avgShortPrice["BTCUSDT"])
	})

	t.Run("invert position short to long", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("BTC", -1), WithPaperAsset("USDT", 100))
		wallet.avgShortPrice["BTCUSDT"] = 100

		// the short is covered at 150 (-50 USDT over the 100 collateral), 1 BTC is bought at 150
		_, _, err := wallet.fillOrder(model.SideTypeBuy, "BTCUSDT", 2, 150, 0)
		require.NoError(t, err)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
		require.Equal(t, 1.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
		require.Equal(t, 150.0, wallet.avgLongPrice["BTCUSDT"])
	})

	t.Run("short entry without collateral is rejected", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 50))
		err := wallet.lockOrder(1, model.SideTypeSell, "BTCUSDT", 1, 100, 0)
		require.ErrorIs(t, err, ErrInsufficientFunds)
		_, _, err = wallet.fillOrder(model.SideTypeSell, "BTCUSDT", 1, 100, 0)
		require.ErrorIs(t, err, ErrInsufficientFunds)
		require.Equal(t, 50.0, wallet.assets["USDT"].Free)
	})
}

func TestPaperWallet_OrderLimit(t *testing.T) {
	t.Run("normal order", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		order, err := wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 100)
		require.NoError(t, err)

		// create order and lock values
		require.Len(t, wallet.orders, 1)
		require.Equal(t, 1.0, order.Quantity)
		require.Equal(t, 100.0, order.Price)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 100.0, wallet.assets["USDT"].Lock)

		// a new candle should execute order and unlock values
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100})
		require.Equal(t, model.OrderStatusTypeFilled, wallet.orders[0].Status)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
		require.Equal(t, 1.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
		require.Equal(t, 100.0, wallet.avgLongPrice["BTCUSDT"])

		// try to buy again without funds
		order, err = wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 100)
		require.Empty(t, order)
		require.Equal(t, &OrderError{
			Err:      ErrInsufficientFunds,
			Pair:     "BTCUSDT",
			Quantity: 1,
		}, err)

		// try to sell and profit 100 USDT
		order, err = wallet.CreateOrderLimit(model.SideTypeSell, "BTCUSDT", 1, 200)
		require.NoError(t, err)
		require.Len(t, wallet.orders, 2)
		require.Equal(t, 1.0, order.Quantity)
		require.Equal(t, 200.0, order.Price)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.Equal(t, 1.0, wallet.assets["BTC"].Lock)

		// a new candle should execute order and unlock values
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 200, High: 200})
		require.Equal(t, model.OrderStatusTypeFilled, wallet.orders[1].Status)
		require.Equal(t, 200.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
	})

	t.Run("multiple pending orders", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		wallet.lastCandle["BTCUSDT"] = model.Candle{Close: 10}

		order, err := wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 10)
		require.NoError(t, err)
		require.NotEmpty(t, order)

		require.Equal(t, 90.0, wallet.assets["USDT"].Free)
		require.Equal(t, 10.0, wallet.assets["USDT"].Lock)

		order, err = wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 20)
		require.NoError(t, err)
		require.NotEmpty(t, order)

		require.Equal(t, 70.0, wallet.assets["USDT"].Free)
		require.Equal(t, 30.0, wallet.assets["USDT"].Lock)

		order, err = wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 50)
		require.NoError(t, err)
		require.NotEmpty(t, order)

		require.Equal(t, 20.0, wallet.assets["USDT"].Free)
		require.Equal(t, 80.0, wallet.assets["USDT"].Lock)

		// should execute two orders and keep one pending
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 15, High: 15, Low: 15})
		require.Equal(t, 20.0, wallet.assets["USDT"].Free)
		require.Equal(t, 10.0, wallet.assets["USDT"].Lock)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
		require.Equal(t, 2.0, wallet.assets["BTC"].Free)
		require.Equal(t, 35.0, wallet.avgLongPrice["BTCUSDT"])
		require.Equal(t, model.OrderStatusTypeNew, wallet.orders[0].Status)
		require.Equal(t, model.OrderStatusTypeFilled, wallet.orders[1].Status)
		require.Equal(t, model.OrderStatusTypeFilled, wallet.orders[2].Status)

		// sell all bitcoin position
		order, err = wallet.CreateOrderLimit(model.SideTypeSell, "BTCUSDT", 2, 40)
		require.NoError(t, err)
		require.NotEmpty(t, order)

		require.Equal(t, 20.0, wallet.assets["USDT"].Free)
		require.Equal(t, 10.0, wallet.assets["USDT"].Lock)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.Equal(t, 2.0, wallet.assets["BTC"].Lock)

		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 50, High: 50, Low: 50})
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
		require.Equal(t, 100.0, wallet.assets["USDT"].Free)
		require.Equal(t, 10.0, wallet.assets["USDT"].Lock)

		// execute old buy position
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 9, High: 9, Low: 9})
		require.Equal(t, 1.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
		require.Equal(t, 100.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
		require.Equal(t, 10.0, wallet.avgLongPrice["BTCUSDT"])
	})

	t.Run("cancel buy order before executing", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		order, err := wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 100)
		require.NoError(t, err)

		// create order and lock values
		require.Len(t, wallet.orders, 1)
		require.Equal(t, 1.0, order.Quantity)
		require.Equal(t, 100.0, order.Price)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 100.0, wallet.assets["USDT"].Lock)

		// cancel limit order and it should unlock funds
		err = wallet.Cancel(order)
		require.NoError(t, err)

		require.Equal(t, model.OrderStatusTypeCanceled, wallet.orders[0].Status)
		require.Equal(t, 100.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
	})

	t.Run("cancel sell order before executing", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		order, err := wallet.CreateOrderLimit(model.SideTypeSell, "BTCUSDT", 1, 100)
		require.NoError(t, err)

		// create order and lock values
		require.Len(t, wallet.orders, 1)
		require.Equal(t, 1.0, order.Quantity)
		require.Equal(t, 100.0, order.Price)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 100.0, wallet.assets["USDT"].Lock)

		// cancel limit order and it should unlock funds
		err = wallet.Cancel(order)
		require.NoError(t, err)

		require.Equal(t, model.OrderStatusTypeCanceled, wallet.orders[0].Status)
		require.Equal(t, 100.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
	})
}

func TestPaperWallet_OrderMarket(t *testing.T) {
	wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 50})
	order, err := wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
	require.NoError(t, err)

	// create buy order
	require.Len(t, wallet.orders, 1)
	require.Equal(t, model.OrderStatusTypeFilled, order.Status)
	require.Equal(t, 1.0, order.Quantity)
	require.Equal(t, 50.0, order.Price)
	require.Equal(t, 50.0, wallet.assets["USDT"].Free)
	require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
	require.Equal(t, 1.0, wallet.assets["BTC"].Free)
	require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
	require.Equal(t, 50.0, wallet.avgLongPrice["BTCUSDT"])

	// insufficient funds
	order, err = wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 100)
	require.Equal(t, &OrderError{
		Err:      ErrInsufficientFunds,
		Pair:     "BTCUSDT",
		Quantity: 100}, err)
	require.Empty(t, order)

	// sell
	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100})
	order, err = wallet.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 1)
	require.NoError(t, err)
	require.Equal(t, 1.0, order.Quantity)
	require.Equal(t, 100.0, order.Price)
	require.Equal(t, 150.0, wallet.assets["USDT"].Free)
	require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
	require.Equal(t, 0.0, wallet.assets["BTC"].Free)
	require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
	require.Equal(t, 50.0, wallet.avgLongPrice["BTCUSDT"])
}

func TestPaperWallet_OrderOCO(t *testing.T) {
	wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 50))
	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 50})
	_, err := wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
	require.NoError(t, err)

	orders, err := wallet.CreateOrderOCO(model.SideTypeSell, "BTCUSDT", 1, 100, 40, 39)
	require.NoError(t, err)

	// create buy order
	require.Len(t, wallet.orders, 3)
	require.Equal(t, model.OrderStatusTypeNew, orders[0].Status)
	require.Equal(t, model.OrderStatusTypeNew, orders[1].Status)
	require.Equal(t, 1.0, orders[0].Quantity)
	require.Equal(t, 1.0, orders[1].Quantity)

	require.Equal(t, 0.0, wallet.assets["USDT"].Free)
	require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
	require.Equal(t, 0.0, wallet.assets["BTC"].Free)
	require.Equal(t, 1.0, wallet.assets["BTC"].Lock)

	// insufficient funds
	orders, err = wallet.CreateOrderOCO(model.SideTypeSell, "BTCUSDT", 1, 100, 40, 39)
	require.Equal(t, &OrderError{
		Err:      ErrInsufficientFunds,
		Pair:     "BTCUSDT",
		Quantity: 1}, err)
	require.Nil(t, orders)

	// execute stop and cancel target
	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 30})
	require.Equal(t, 40.0, wallet.assets["USDT"].Free)
	require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
	require.Equal(t, 0.0, wallet.assets["BTC"].Free)
	require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
	require.Equal(t, wallet.orders[1].Status, model.OrderStatusTypeCanceled)
	require.Equal(t, wallet.orders[2].Status, model.OrderStatusTypeFilled)
}

func TestPaperWallet_Order(t *testing.T) {
	wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
	expectOrder, err := wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), expectOrder.ExchangeID)

	order, err := wallet.Order("BTCUSDT", expectOrder.ExchangeID)
	require.NoError(t, err)
	require.Equal(t, expectOrder, order)
}

func TestPaperWallet_MaxDrawndown(t *testing.T) {
	tt := []struct {
		name   string
		values []AssetValue
		result float64
		start  time.Time
		end    time.Time
	}{
		{
			name: "down only",
			values: []AssetValue{
				{Time: time.Date(2019, time.January, 1, 0, 0, 0, 0, time.UTC), Value: 10},
				{Time: time.Date(2019, time.January, 2, 0, 0, 0, 0, time.UTC), Value: 5},
			},
			result: -0.5,
			start:  time.Date(2019, time.January, 1, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2019, time.January, 2, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "up and down",
			values: []AssetValue{
				{Time: time.Date(2019, time.January, 1, 0, 0, 0, 0, time.UTC), Value: 1},
				{Time: time.Date(2019, time.January, 2, 0, 0, 0, 0, time.UTC), Value: 10},
				{Time: time.Date(2019, time.January, 3, 0, 0, 0, 0, time.UTC), Value: 5},
			},
			result: -0.5,
			start:  time.Date(2019, time.January, 2, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2019, time.January, 3, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "down and up",
			values: []AssetValue{
				{Time: time.Date(2019, time.January, 1, 0, 0, 0, 0, time.UTC), Value: 4},
				{Time: time.Date(2019, time.January, 2, 0, 0, 0, 0, time.UTC), Value: 5},
				{Time: time.Date(2019, time.January, 3, 0, 0, 0, 0, time.UTC), Value: 4},
				{Time: time.Date(2019, time.January, 4, 0, 0, 0, 0, time.UTC), Value: 3},
				{Time: time.Date(2019, time.January, 5, 0, 0, 0, 0, time.UTC), Value: 4},
				{Time: time.Date(2019, time.January, 6, 0, 0, 0, 0, time.UTC), Value: 5},
				{Time: time.Date(2019, time.January, 7, 0, 0, 0, 0, time.UTC), Value: 6},
				{Time: time.Date(2019, time.January, 8, 0, 0, 0, 0, time.UTC), Value: 7},
				{Time: time.Date(2019, time.January, 9, 0, 0, 0, 0, time.UTC), Value: 6},
			},
			result: -0.4,
			start:  time.Date(2019, time.January, 2, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2019, time.January, 4, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "two drawn downs",
			values: []AssetValue{
				{Time: time.Date(2019, time.January, 1, 0, 0, 0, 0, time.UTC), Value: 1},
				{Time: time.Date(2019, time.January, 2, 0, 0, 0, 0, time.UTC), Value: 5},
				{Time: time.Date(2019, time.January, 3, 0, 0, 0, 0, time.UTC), Value: 4},
				{Time: time.Date(2019, time.January, 4, 0, 0, 0, 0, time.UTC), Value: 7},
				{Time: time.Date(2019, time.January, 5, 0, 0, 0, 0, time.UTC), Value: 8},
				{Time: time.Date(2019, time.January, 6, 0, 0, 0, 0, time.UTC), Value: 4},
				{Time: time.Date(2019, time.January, 7, 0, 0, 0, 0, time.UTC), Value: 5},
				{Time: time.Date(2019, time.January, 8, 0, 0, 0, 0, time.UTC), Value: 2},
				{Time: time.Date(2019, time.January, 9, 0, 0, 0, 0, time.UTC), Value: 3},
			},
			result: -0.75,
			start:  time.Date(2019, time.January, 5, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2019, time.January, 8, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			wallet := PaperWallet{
				equityValues: tc.values,
			}

			max, start, end := wallet.MaxDrawdown()
			require.Equal(t, tc.result, max)
			require.Equal(t, tc.start, start)
			require.Equal(t, tc.end, end)
		})
	}
}

func TestPaperWallet_AssetsInfo(t *testing.T) {
	wallet := PaperWallet{}
	info := wallet.AssetsInfo("BTCUSDT")
	require.Equal(t, info.QuotePrecision, 8)
	require.Equal(t, info.BaseAsset, "BTC")
	require.Equal(t, info.QuoteAsset, "USDT")
}

func TestPaperWallet_CreateOrderStop(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100})
		_, err := wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		require.NoError(t, err)

		order, err := wallet.CreateOrderStop("BTCUSDT", 1, 50)
		require.NoError(t, err)

		// create order and lock values
		require.Len(t, wallet.orders, 2)
		require.Equal(t, 1.0, order.Quantity)
		require.Equal(t, 50.0, order.Price)
		require.Equal(t, 50.0, *order.Stop)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.Equal(t, 1.0, wallet.assets["BTC"].Lock)

		// a new candle should execute order and unlock values
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 40})
		require.Equal(t, model.OrderStatusTypeFilled, wallet.orders[1].Status)
		require.Equal(t, 50.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
		require.Equal(t, 100.0, wallet.avgLongPrice["BTCUSDT"])
	})
}

func TestUpdateAveragePrice(t *testing.T) {
	t.Run("long", func(t *testing.T) {
		wallet := NewPaperWallet(
			context.Background(),
			"USDT",
			WithPaperAsset("BTC", 0),
			WithPaperAsset("USDT", 100),
		)

		tt := []struct {
			name     string
			quantity float64
			price    float64
			avgPrice float64
		}{
			{
				name:     "first order",
				quantity: 1,
				price:    100,
				avgPrice: 100,
			},
			{
				name:     "second order",
				quantity: 1,
				price:    50,
				avgPrice: 75,
			},
			{
				name:     "third order",
				quantity: 2,
				price:    101,
				avgPrice: 88,
			},
		}

		for _, tc := range tt {
			t.Run(tc.name, func(t *testing.T) {
				wallet.updateAveragePrice(model.SideTypeBuy, "BTCUSDT", tc.quantity, tc.price)
				require.Equal(t, tc.avgPrice, wallet.avgLongPrice["BTCUSDT"])
				wallet.assets["BTC"].Free += tc.quantity
			})
		}
	})

	t.Run("short", func(t *testing.T) {
		wallet := NewPaperWallet(
			context.Background(),
			"USDT",
			WithPaperAsset("BTC", 0),
			WithPaperAsset("USDT", 100),
		)

		tt := []struct {
			name     string
			quantity float64
			price    float64
			avgPrice float64
		}{
			{
				name:     "first order",
				quantity: 1,
				price:    100,
				avgPrice: 100,
			},
			{
				name:     "second order",
				quantity: 1,
				price:    50,
				avgPrice: 75,
			},
			{
				name:     "third order",
				quantity: 2,
				price:    101,
				avgPrice: 88,
			},
		}

		for _, tc := range tt {
			t.Run(tc.name, func(t *testing.T) {
				wallet.updateAveragePrice(model.SideTypeSell, "BTCUSDT", tc.quantity, tc.price)
				require.Equal(t, tc.avgPrice, wallet.avgShortPrice["BTCUSDT"])
				wallet.assets["BTC"].Free -= tc.quantity
			})
		}
	})

	t.Run("mixed order", func(t *testing.T) {
		wallet := NewPaperWallet(
			context.Background(),
			"USDT",
			WithPaperAsset("BTC", 0),
			WithPaperAsset("USDT", 100),
		)

		tt := []struct {
			name          string
			side          model.SideType
			quantity      float64
			price         float64
			avgLongPrice  float64
			avgShortPrice float64
		}{
			{
				name:         "first buy order",
				side:         model.SideTypeBuy,
				quantity:     1,
				price:        100,
				avgLongPrice: 100,
			},
			{
				name:         "second buy order",
				side:         model.SideTypeBuy,
				quantity:     1,
				price:        50,
				avgLongPrice: 75,
			},
			{
				name:         "sell half",
				side:         model.SideTypeSell,
				quantity:     1,
				price:        50,
				avgLongPrice: 75,
			},
			{
				name:          "long to short",
				side:          model.SideTypeSell,
				quantity:      2,
				price:         100,
				avgLongPrice:  75,
				avgShortPrice: 100,
			},
			{
				name:          "back to long",
				side:          model.SideTypeBuy,
				quantity:      2,
				price:         50,
				avgLongPrice:  50,
				avgShortPrice: 100,
			},
		}

		for _, tc := range tt {
			t.Run(tc.name, func(t *testing.T) {
				wallet.updateAveragePrice(tc.side, "BTCUSDT", tc.quantity, tc.price)
				require.Equal(t, tc.avgLongPrice, wallet.avgLongPrice["BTCUSDT"])
				require.Equal(t, tc.avgShortPrice, wallet.avgShortPrice["BTCUSDT"])
				if tc.side == model.SideTypeBuy {
					wallet.assets["BTC"].Free += tc.quantity
				} else {
					wallet.assets["BTC"].Free -= tc.quantity
				}
			})
		}
	})

}

func TestPaperWallet_Fees(t *testing.T) {
	t.Run("market order charges taker fee", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT",
			WithPaperAsset("USDT", 100), WithPaperFee(0.001, 0.002))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 50})

		order, err := wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		require.NoError(t, err)
		require.Equal(t, 1.0, order.Quantity)
		require.InDelta(t, 0.1, order.Fee, 1e-9) // 50 * 0.002
		require.InDelta(t, 49.9, wallet.assets["USDT"].Free, 1e-9)
		require.Equal(t, 1.0, wallet.assets["BTC"].Free)

		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100})
		order, err = wallet.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 1)
		require.NoError(t, err)
		require.InDelta(t, 0.2, order.Fee, 1e-9) // 100 * 0.002
		require.InDelta(t, 149.7, wallet.assets["USDT"].Free, 1e-9)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.InDelta(t, 0.3, wallet.feesPaid, 1e-9)
	})

	t.Run("limit order charges maker fee", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT",
			WithPaperAsset("USDT", 100), WithPaperFee(0.001, 0.002))

		_, err := wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 0.5, 100)
		require.NoError(t, err)

		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100})
		require.Equal(t, model.OrderStatusTypeFilled, wallet.orders[0].Status)
		require.InDelta(t, 0.05, wallet.orders[0].Fee, 1e-9) // 50 * 0.001
		require.InDelta(t, 49.95, wallet.assets["USDT"].Free, 1e-9)
		require.Equal(t, 0.5, wallet.assets["BTC"].Free)
		require.InDelta(t, 0.05, wallet.feesPaid, 1e-9)
	})

	t.Run("stop loss charges taker fee", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT",
			WithPaperAsset("USDT", 100), WithPaperFee(0.001, 0.002))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100})

		_, err := wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 0.5)
		require.NoError(t, err)

		_, err = wallet.CreateOrderStop("BTCUSDT", 0.5, 80)
		require.NoError(t, err)

		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 70, Low: 70})
		require.Equal(t, model.OrderStatusTypeFilled, wallet.orders[1].Status)
		require.InDelta(t, 0.08, wallet.orders[1].Fee, 1e-9) // 0.5 * 80 * 0.002
		require.InDelta(t, 89.82, wallet.assets["USDT"].Free, 1e-9)
	})

	t.Run("all-in market order is trimmed to fit the fee", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT",
			WithPaperAsset("USDT", 100), WithPaperFee(0.001, 0.002))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 50})

		// a strategy sizing the order with the whole balance leaves no room
		// for the fee, so the wallet fills a slightly smaller quantity
		order, err := wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 2)
		require.NoError(t, err)
		require.InDelta(t, 1.996008, order.Quantity, 1e-6)
		require.InDelta(t, order.Quantity, wallet.assets["BTC"].Free, 1e-9)
		require.InDelta(t, 0, wallet.assets["USDT"].Free, 1e-9)
		require.InDelta(t, 100, order.Quantity*order.Price+order.Fee, 1e-9)
	})

	t.Run("selling the position of an all-in buy is not trimmed", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT",
			WithPaperAsset("USDT", 10000), WithPaperFee(0.001, 0.001))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 8615})

		bought, err := wallet.CreateOrderMarketQuote(model.SideTypeBuy, "BTCUSDT", 10000)
		require.NoError(t, err)

		// the trimmed buy leaves a rounding error on the quote balance, which
		// must not shrink the sale: closing a long pays its fee from the proceeds
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 9155})
		sold, err := wallet.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", bought.Quantity)
		require.NoError(t, err)
		require.Equal(t, bought.Quantity, sold.Quantity)
		require.Zero(t, wallet.assets["BTC"].Free)
	})

	t.Run("selling a rounding error more than the position closes it", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT",
			WithPaperAsset("USDT", 10000), WithPaperFee(0.001, 0.001))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 8615})

		bought, err := wallet.CreateOrderMarketQuote(model.SideTypeBuy, "BTCUSDT", 10000)
		require.NoError(t, err)

		// a "sell 100%" sized as amount*asset/100 may exceed the position by an ulp
		sold, err := wallet.CreateOrderMarket(model.SideTypeSell, "BTCUSDT",
			math.Nextafter(bought.Quantity, math.Inf(1)))
		require.NoError(t, err)
		require.Equal(t, bought.Quantity, sold.Quantity)
		require.Zero(t, wallet.assets["BTC"].Free)
	})

	t.Run("underfunded order is still rejected", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT",
			WithPaperAsset("USDT", 100), WithPaperFee(0.001, 0.002))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 50})

		order, err := wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 100)
		require.Equal(t, &OrderError{
			Err:      ErrInsufficientFunds,
			Pair:     "BTCUSDT",
			Quantity: 100,
		}, err)
		require.Empty(t, order)
	})

	t.Run("short entry pays the fee upfront", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT",
			WithPaperAsset("USDT", 100), WithPaperFee(0.001, 0.002))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100})

		// no proceeds are credited on a short entry, the fee comes out of the
		// free balance and the size is trimmed to make room for it
		order, err := wallet.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 1)
		require.NoError(t, err)
		require.InDelta(t, 0.998004, order.Quantity, 1e-6)
		require.InDelta(t, -order.Quantity, wallet.assets["BTC"].Free, 1e-9)
		require.InDelta(t, 0, wallet.assets["USDT"].Free, 1e-9)
	})

	t.Run("wallet without fees is unchanged", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 50})

		order, err := wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 2)
		require.NoError(t, err)
		require.Equal(t, 2.0, order.Quantity)
		require.Equal(t, 0.0, order.Fee)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.feesPaid)
	})
}

// equity returns the wallet value at the last candle: quote balance plus the
// value of every position, shorts at their liquidation value.
func equity(t *testing.T, w *PaperWallet) float64 {
	t.Helper()
	w.updateEquityValues(model.Candle{Complete: true})
	return w.equityValues[len(w.equityValues)-1].Value
}

func TestPaperWallet_ShortPositions(t *testing.T) {
	t.Run("market sell crossing from long to short", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT",
			WithPaperAsset("BTC", 1), WithPaperAsset("USDT", 100))
		wallet.avgLongPrice["BTCUSDT"] = 100
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100, Complete: true})
		before := equity(t, wallet)

		order, err := wallet.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 2)
		require.NoError(t, err)
		require.Equal(t, 2.0, order.Quantity)

		// 1 BTC sold out of the long, 1 BTC shorted with 100 USDT of collateral
		require.Equal(t, -1.0, wallet.assets["BTC"].Free)
		require.Equal(t, 100.0, wallet.assets["USDT"].Free)
		require.Equal(t, 100.0, wallet.avgShortPrice["BTCUSDT"])
		require.InDelta(t, before, equity(t, wallet), 1e-9)

		// cover at 50: 50 USDT of profit on the short
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 50, Complete: true})
		_, err = wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		require.NoError(t, err)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.Equal(t, 250.0, wallet.assets["USDT"].Free)
	})

	t.Run("partial short cover returns only the covered collateral", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 200))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100, Complete: true})
		_, err := wallet.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 2)
		require.NoError(t, err)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free) // 200 USDT of collateral
		before := equity(t, wallet)

		_, err = wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		require.NoError(t, err)
		require.Equal(t, -1.0, wallet.assets["BTC"].Free)
		require.Equal(t, 100.0, wallet.assets["USDT"].Free)
		require.InDelta(t, before, equity(t, wallet), 1e-9)

		_, err = wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		require.NoError(t, err)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.Equal(t, 200.0, wallet.assets["USDT"].Free)
	})

	t.Run("short entry through a limit order keeps the equity", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100, High: 100, Low: 100, Complete: true})
		before := equity(t, wallet)

		_, err := wallet.CreateOrderLimit(model.SideTypeSell, "BTCUSDT", 1, 100)
		require.NoError(t, err)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 100.0, wallet.assets["USDT"].Lock)
		require.InDelta(t, before, equity(t, wallet), 1e-9)

		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100, High: 100, Low: 100, Complete: true})
		require.Equal(t, model.OrderStatusTypeFilled, wallet.orders[0].Status)
		require.Equal(t, -1.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
		require.InDelta(t, before, equity(t, wallet), 1e-9)

		// price falls to 80: 20 USDT of open profit
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 80, High: 80, Low: 80, Complete: true})
		require.InDelta(t, 120.0, equity(t, wallet), 1e-9)
	})

	t.Run("limit buy covering a short", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100, High: 100, Low: 100, Complete: true})
		_, err := wallet.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 1)
		require.NoError(t, err)

		// covering needs no extra quote, the collateral pays for it
		_, err = wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 80)
		require.NoError(t, err)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
		require.Equal(t, -1.0, wallet.assets["BTC"].Free)

		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 80, High: 80, Low: 80, Complete: true})
		require.Equal(t, model.OrderStatusTypeFilled, wallet.orders[1].Status)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
		require.InDelta(t, 120.0, wallet.assets["USDT"].Free, 1e-9)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
	})

	t.Run("oco buy closes a short by target or stop", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			candle model.Candle
			filled int // index of the filled leg: 1 limit maker, 2 stop
			usdt   float64
		}{
			{name: "target", candle: model.Candle{Close: 70, High: 70, Low: 70}, filled: 1, usdt: 130},
			{name: "stop", candle: model.Candle{Close: 130, High: 130, Low: 130}, filled: 2, usdt: 80},
		} {
			t.Run(tc.name, func(t *testing.T) {
				wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
				wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100, High: 100, Low: 100, Complete: true})
				_, err := wallet.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 1)
				require.NoError(t, err)

				orders, err := wallet.CreateOrderOCO(model.SideTypeBuy, "BTCUSDT", 1, 70, 120, 121)
				require.NoError(t, err)
				require.Len(t, orders, 2)

				// a candle between target and stop fills nothing
				wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100, High: 110, Low: 90, Complete: true})
				require.Equal(t, model.OrderStatusTypeNew, wallet.orders[1].Status)
				require.Equal(t, model.OrderStatusTypeNew, wallet.orders[2].Status)

				tc.candle.Pair = "BTCUSDT"
				tc.candle.Complete = true
				wallet.OnCandle(tc.candle)
				require.Equal(t, model.OrderStatusTypeFilled, wallet.orders[tc.filled].Status)
				require.Equal(t, model.OrderStatusTypeCanceled, wallet.orders[3-tc.filled].Status)
				require.Equal(t, 0.0, wallet.assets["BTC"].Free)
				require.Equal(t, 0.0, wallet.assets["BTC"].Lock)
				require.InDelta(t, tc.usdt, wallet.assets["USDT"].Free, 1e-9)
				require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
			})
		}
	})
}

func TestPaperWallet_Fills(t *testing.T) {
	t.Run("buy limit fills when the candle trades through it", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		_, err := wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 100)
		require.NoError(t, err)

		// closes above the limit but dips below it within the candle
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Open: 110, High: 120, Low: 95, Close: 115})
		require.Equal(t, model.OrderStatusTypeFilled, wallet.orders[0].Status)
		require.Equal(t, 100.0, wallet.orders[0].Price)
		require.Equal(t, 1.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
	})

	t.Run("buy limit does not fill above the limit", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		_, err := wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 100)
		require.NoError(t, err)

		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Open: 110, High: 120, Low: 101, Close: 105})
		require.Equal(t, model.OrderStatusTypeNew, wallet.orders[0].Status)
		require.Equal(t, 100.0, wallet.assets["USDT"].Lock)
	})

	t.Run("stop order records the fill price and volume", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100, High: 100, Low: 100})
		_, err := wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		require.NoError(t, err)

		orders, err := wallet.CreateOrderOCO(model.SideTypeSell, "BTCUSDT", 1, 200, 80, 79)
		require.NoError(t, err)
		require.Equal(t, 79.0, orders[1].Price) // stop limit
		require.Equal(t, 80.0, *orders[1].Stop)

		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 70, High: 90, Low: 70})
		filled, err := wallet.Order("BTCUSDT", orders[1].ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeFilled, filled.Status)
		require.Equal(t, 80.0, filled.Price) // filled at the stop price
		require.Equal(t, 80.0, wallet.assets["USDT"].Free)
		require.Equal(t, 180.0, wallet.volume["BTCUSDT"])
	})
}

func TestPaperWallet_Cancel(t *testing.T) {
	t.Run("buy order with a pending sell on the same pair", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT",
			WithPaperAsset("BTC", 1), WithPaperAsset("USDT", 100))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100, High: 100, Low: 100})

		_, err := wallet.CreateOrderLimit(model.SideTypeSell, "BTCUSDT", 1, 200)
		require.NoError(t, err)
		buy, err := wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 50)
		require.NoError(t, err)
		require.Equal(t, 50.0, wallet.assets["USDT"].Free)
		require.Equal(t, 50.0, wallet.assets["USDT"].Lock)

		require.NoError(t, wallet.Cancel(buy))
		require.Equal(t, 100.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)
		require.Equal(t, 0.0, wallet.assets["BTC"].Free) // the sell stays locked
		require.Equal(t, 1.0, wallet.assets["BTC"].Lock)
	})

	t.Run("filled or canceled orders can't be canceled again", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		buy, err := wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 50)
		require.NoError(t, err)

		require.NoError(t, wallet.Cancel(buy))
		require.ErrorIs(t, wallet.Cancel(buy), ErrOrderNotOpen)
		require.Equal(t, 100.0, wallet.assets["USDT"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Lock)

		buy, err = wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 50)
		require.NoError(t, err)
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 50, High: 50, Low: 50})
		require.ErrorIs(t, wallet.Cancel(buy), ErrOrderNotOpen)
		require.Equal(t, 50.0, wallet.assets["USDT"].Free)
		require.Equal(t, 1.0, wallet.assets["BTC"].Free)

		require.ErrorIs(t, wallet.Cancel(model.Order{ExchangeID: 999}), ErrOrderNotFound)
	})

	t.Run("cancelling an oco leg cancels the group", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100))
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 100, High: 100, Low: 100})
		_, err := wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		require.NoError(t, err)

		orders, err := wallet.CreateOrderOCO(model.SideTypeSell, "BTCUSDT", 1, 200, 80, 79)
		require.NoError(t, err)
		require.Equal(t, 1.0, wallet.assets["BTC"].Lock)

		require.NoError(t, wallet.Cancel(orders[0]))
		require.Equal(t, model.OrderStatusTypeCanceled, wallet.orders[1].Status)
		require.Equal(t, model.OrderStatusTypeCanceled, wallet.orders[2].Status)
		require.Equal(t, 1.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["BTC"].Lock)

		// the stop leg no longer fills
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Close: 70, High: 70, Low: 70})
		require.Equal(t, 1.0, wallet.assets["BTC"].Free)
		require.Equal(t, 0.0, wallet.assets["USDT"].Free)
	})
}

func TestPaperWallet_EquityOfPairWithoutQuoteSuffix(t *testing.T) {
	// eg: the XAUUSDm symbol of MT5, quoted in USD
	RegisterPair("TESTGOLD", "TESTGOLD", "USD")
	wallet := NewPaperWallet(context.Background(), "USD", WithPaperAsset("USD", 10000))
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	wallet.OnCandle(model.Candle{Pair: "TESTGOLD", Time: start, Close: 2000, High: 2000, Low: 2000, Complete: true})
	_, err := wallet.CreateOrderMarket(model.SideTypeBuy, "TESTGOLD", 4)
	require.NoError(t, err)
	wallet.OnCandle(model.Candle{Pair: "TESTGOLD", Time: start.Add(time.Hour), Close: 2000, High: 2000, Low: 2000,
		Complete: true})

	equity := wallet.EquityValues()
	require.NotEmpty(t, equity)
	require.InDelta(t, 10000, equity[len(equity)-1].Value, 1e-6, "the position is worth its price")
}

func TestPaperWallet_MarketProtected(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candle := func(hour int, low, high, close float64) model.Candle {
		return model.Candle{Pair: "BTCUSDT", Time: start.Add(time.Duration(hour) * time.Hour), Low: low, High: high,
			Close: close, Complete: true}
	}

	t.Run("the stop loss closes the long, the take profit goes with it", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 10000))
		wallet.OnCandle(candle(0, 100, 100, 100))

		orders, err := wallet.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 50, 90, 120)
		require.NoError(t, err)
		require.Len(t, orders, 3)
		require.Equal(t, model.OrderStatusTypeFilled, orders[0].Status)
		takeProfit, stopLoss := orders[1], orders[2]
		require.Equal(t, model.OrderTypeLimitMaker, takeProfit.Type)
		require.Equal(t, model.OrderTypeStopLoss, stopLoss.Type)
		require.Equal(t, *takeProfit.GroupID, *stopLoss.GroupID)
		for _, leg := range orders[1:] {
			require.Equal(t, model.SideTypeSell, leg.Side)
			require.Equal(t, 50.0, leg.Quantity)
		}

		// the position is reserved by its protection
		asset, _, err := wallet.Position("BTCUSDT")
		require.NoError(t, err)
		require.Equal(t, 50.0, asset)
		_, err = wallet.CreateOrderLimit(model.SideTypeSell, "BTCUSDT", 50, 130)
		require.ErrorIs(t, err, ErrInsufficientFunds)

		wallet.OnCandle(candle(1, 89, 101, 95))
		got, err := wallet.Order("BTCUSDT", stopLoss.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeFilled, got.Status)
		require.Equal(t, 90.0, got.Price)
		got, err = wallet.Order("BTCUSDT", takeProfit.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, got.Status)

		asset, quote, err := wallet.Position("BTCUSDT")
		require.NoError(t, err)
		require.Zero(t, asset)
		require.InDelta(t, 10000-50*10, quote, 1e-9)
	})

	t.Run("a short all in, protected however far its stop loss", func(t *testing.T) {
		// covering at 250 costs more than the short left: an entry that far
		// would be refused, a stop closing the position is always taken
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 10000),
			WithPaperFee(0.001, 0.001))
		wallet.OnCandle(candle(0, 100, 100, 100))

		orders, err := wallet.CreateOrderMarketProtected(model.SideTypeSell, "BTCUSDT", 99.9, 250, 0)
		require.NoError(t, err)
		require.Len(t, orders, 2)
		require.Equal(t, model.SideTypeBuy, orders[1].Side)
		got, err := wallet.Order("BTCUSDT", orders[1].ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeNew, got.Status)

		wallet.OnCandle(candle(1, 99, 251, 240))
		got, err = wallet.Order("BTCUSDT", orders[1].ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeFilled, got.Status)
		asset, _, err := wallet.Position("BTCUSDT")
		require.NoError(t, err)
		require.InDelta(t, 0, asset, 1e-9)
	})

	t.Run("a candle touching both levels fills the stop loss, the worst case", func(t *testing.T) {
		for _, side := range []model.SideType{model.SideTypeBuy, model.SideTypeSell} {
			wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 10000))
			wallet.OnCandle(candle(0, 100, 100, 100))
			stop, profit := 95.0, 105.0
			if side == model.SideTypeSell {
				stop, profit = 105, 95
			}
			orders, err := wallet.CreateOrderMarketProtected(side, "BTCUSDT", 50, stop, profit)
			require.NoError(t, err)

			wallet.OnCandle(candle(1, 94, 106, 100))
			got, err := wallet.Order("BTCUSDT", orders[2].ExchangeID)
			require.NoError(t, err)
			require.Equal(t, model.OrderStatusTypeFilled, got.Status, side)
			require.Equal(t, stop, got.Price, side)
			got, err = wallet.Order("BTCUSDT", orders[1].ExchangeID)
			require.NoError(t, err)
			require.Equal(t, model.OrderStatusTypeCanceled, got.Status, side)
		}
	})

	t.Run("canceled before the close, the position is free again", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 10000))
		wallet.OnCandle(candle(0, 100, 100, 100))
		orders, err := wallet.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 50, 90, 0)
		require.NoError(t, err)

		require.NoError(t, wallet.Cancel(orders[1]))
		_, err = wallet.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 50)
		require.NoError(t, err)
		asset, _, err := wallet.Position("BTCUSDT")
		require.NoError(t, err)
		require.Zero(t, asset, "closed, not reversed")
	})

	t.Run("levels on the wrong side", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 10000))
		wallet.OnCandle(candle(0, 100, 100, 100))
		_, err := wallet.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 50, 105, 0)
		require.ErrorIs(t, err, ErrInvalidProtection)
		asset, _, err := wallet.Position("BTCUSDT")
		require.NoError(t, err)
		require.Zero(t, asset, "nothing filled")
	})
}

func TestPaperWallet_EntryProtected(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candle := func(hour int, low, high, close float64) model.Candle {
		return model.Candle{Pair: "BTCUSDT", Time: start.Add(time.Duration(hour) * time.Hour), Low: low, High: high,
			Close: close, Complete: true}
	}

	t.Run("the legs wait for the limit entry", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 10000))
		wallet.OnCandle(candle(0, 100, 100, 100))

		orders, err := wallet.CreateOrderLimitProtected(model.SideTypeBuy, "BTCUSDT", 50, 95, 90, 120)
		require.NoError(t, err)
		require.Len(t, orders, 3)
		entry, takeProfit, stopLoss := orders[0], orders[1], orders[2]

		// the stop level is touched before the entry fills: nothing happens
		wallet.OnCandle(candle(1, 96, 121, 110))
		got, err := wallet.Order("BTCUSDT", takeProfit.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeNew, got.Status)

		// the entry fills, its protection takes over from the next candle
		wallet.OnCandle(candle(2, 89, 100, 92))
		got, err = wallet.Order("BTCUSDT", entry.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeFilled, got.Status)
		got, err = wallet.Order("BTCUSDT", stopLoss.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeNew, got.Status)

		wallet.OnCandle(candle(3, 88, 93, 89))
		got, err = wallet.Order("BTCUSDT", stopLoss.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeFilled, got.Status)
		require.Equal(t, 90.0, got.Price)
		got, err = wallet.Order("BTCUSDT", takeProfit.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, got.Status)
	})

	t.Run("a stop entry canceled with its legs", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 10000))
		wallet.OnCandle(candle(0, 100, 100, 100))

		orders, err := wallet.CreateOrderStopProtected(model.SideTypeBuy, "BTCUSDT", 50, 105, 98, 0)
		require.NoError(t, err)
		require.Len(t, orders, 2)
		require.Equal(t, model.OrderTypeStopLoss, orders[0].Type)

		require.NoError(t, wallet.Cancel(orders[0]))
		for _, order := range orders {
			got, err := wallet.Order("BTCUSDT", order.ExchangeID)
			require.NoError(t, err)
			require.Equal(t, model.OrderStatusTypeCanceled, got.Status)
		}

		// nothing left to fill
		wallet.OnCandle(candle(1, 90, 110, 100))
		asset, quote, err := wallet.Position("BTCUSDT")
		require.NoError(t, err)
		require.Zero(t, asset)
		require.Equal(t, 10000.0, quote)
	})
}

func TestPaperWallet_UpdateProtection(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candle := func(hour int, low, high, close float64) model.Candle {
		return model.Candle{Pair: "BTCUSDT", Time: start.Add(time.Duration(hour) * time.Hour), Low: low, High: high,
			Close: close, Complete: true}
	}
	wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 10000))
	wallet.OnCandle(candle(0, 100, 100, 100))
	orders, err := wallet.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 50, 90, 120)
	require.NoError(t, err)
	stopLoss := orders[2]

	wallet.OnCandle(candle(1, 99, 110, 108))
	moved, err := wallet.UpdateProtection(stopLoss, 104)
	require.NoError(t, err)
	require.Equal(t, 104.0, *moved.Stop)

	for _, level := range []float64{108, 125} { // at the price, beyond the take profit
		_, err = wallet.UpdateProtection(stopLoss, level)
		require.ErrorIs(t, err, ErrInvalidProtection)
	}
	_, err = wallet.UpdateProtection(orders[0], 104)
	require.ErrorIs(t, err, ErrOrderNotOpen, "the entry is filled")
	plain, err := wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 80)
	require.NoError(t, err)
	_, err = wallet.UpdateProtection(plain, 85)
	require.ErrorIs(t, err, ErrNotSupported, "not a leg")

	// filled at the level moved
	wallet.OnCandle(candle(2, 103, 109, 105))
	got, err := wallet.Order("BTCUSDT", stopLoss.ExchangeID)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeFilled, got.Status)
	require.Equal(t, 104.0, got.Price)
}

func TestPaperWallet_UpdateLimit(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candle := func(hour int, low, high, close float64) model.Candle {
		return model.Candle{Pair: "BTCUSDT", Time: start.Add(time.Duration(hour) * time.Hour), Low: low, High: high,
			Close: close, Complete: true}
	}
	quote := func(t *testing.T, wallet *PaperWallet) (free, locked float64) {
		t.Helper()
		account, err := wallet.Account()
		require.NoError(t, err)
		_, balance := account.Balance("BTC", "USDT")
		return balance.Free, balance.Lock
	}

	t.Run("the funds reserved follow the limit", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 10000))
		wallet.OnCandle(candle(0, 100, 100, 100))
		order, err := wallet.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 10, 90)
		require.NoError(t, err)

		moved, err := wallet.UpdateLimit(order, 95)
		require.NoError(t, err)
		require.Equal(t, order.ExchangeID, moved.ExchangeID)
		require.Equal(t, 95.0, moved.Price)
		free, locked := quote(t, wallet)
		require.InDelta(t, 950, locked, 1e-9)
		require.InDelta(t, 9050, free, 1e-9)

		// not enough for the new limit: kept where it was
		_, err = wallet.UpdateLimit(moved, 2000)
		require.ErrorIs(t, err, ErrInsufficientFunds)
		free, locked = quote(t, wallet)
		require.InDelta(t, 950, locked, 1e-9)
		require.InDelta(t, 9050, free, 1e-9)

		// filled at the limit moved
		wallet.OnCandle(candle(1, 94, 101, 96))
		got, err := wallet.Order("BTCUSDT", order.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeFilled, got.Status)
		require.Equal(t, 95.0, got.Price)
		_, err = wallet.UpdateLimit(got, 96)
		require.ErrorIs(t, err, ErrOrderNotOpen)
	})

	t.Run("its legs stay where they are", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 10000))
		wallet.OnCandle(candle(0, 100, 100, 100))
		orders, err := wallet.CreateOrderLimitProtected(model.SideTypeBuy, "BTCUSDT", 10, 90, 80, 120)
		require.NoError(t, err)

		_, err = wallet.UpdateLimit(orders[0], 95)
		require.NoError(t, err)
		for _, leg := range orders[1:] {
			got, err := wallet.Order("BTCUSDT", leg.ExchangeID)
			require.NoError(t, err)
			require.Equal(t, leg.Price, got.Price)
		}
		for _, limit := range []float64{80, 125} { // at the stop loss, beyond the take profit
			_, err = wallet.UpdateLimit(orders[0], limit)
			require.ErrorIs(t, err, ErrInvalidProtection)
		}
		_, err = wallet.UpdateLimit(orders[2], 85)
		require.ErrorIs(t, err, ErrNotSupported, "a leg")
	})
}
