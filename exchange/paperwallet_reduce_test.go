package exchange

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// A reduce-only market order closes the position, its legs canceled with it as
// a position closed on MT5, and never opens the other side.
func TestPaperWallet_CreateOrderMarketReduce(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candle := model.Candle{Pair: "BTCUSDT", Time: start, Low: 100, High: 100, Close: 100, Complete: true}

	t.Run("a protected long, closed with its legs, the size cut to it", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 10000))
		wallet.OnCandle(candle)
		orders, err := wallet.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 50, 90, 120)
		require.NoError(t, err)

		reduced, err := wallet.CreateOrderMarketReduce(model.SideTypeSell, "BTCUSDT", 80)
		require.NoError(t, err)
		require.Equal(t, 50.0, reduced.Quantity)
		require.Equal(t, model.OrderStatusTypeFilled, reduced.Status)
		for _, leg := range orders[1:] {
			got, err := wallet.Order("BTCUSDT", leg.ExchangeID)
			require.NoError(t, err)
			require.Equal(t, model.OrderStatusTypeCanceled, got.Status, leg.Type)
		}
		asset, quote, err := wallet.Position("BTCUSDT")
		require.NoError(t, err)
		require.Zero(t, asset, "flat, no short")
		require.InDelta(t, 10000, quote, 1e-9)
	})

	t.Run("a short reduced by a buy", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 10000))
		wallet.OnCandle(candle)
		_, err := wallet.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 10)
		require.NoError(t, err)

		reduced, err := wallet.CreateOrderMarketReduce(model.SideTypeBuy, "BTCUSDT", 4)
		require.NoError(t, err)
		require.Equal(t, 4.0, reduced.Quantity)
		asset, _, err := wallet.Position("BTCUSDT")
		require.NoError(t, err)
		require.InDelta(t, -6, asset, 1e-9)
	})

	t.Run("nothing held against the side", func(t *testing.T) {
		wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 10000))
		wallet.OnCandle(candle)
		_, err := wallet.CreateOrderMarketReduce(model.SideTypeSell, "BTCUSDT", 1)
		require.ErrorIs(t, err, ErrNothingToReduce, "flat")

		_, err = wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 2)
		require.NoError(t, err)
		_, err = wallet.CreateOrderMarketReduce(model.SideTypeBuy, "BTCUSDT", 1)
		require.ErrorIs(t, err, ErrNothingToReduce, "held on the side of the order")
		asset, _, err := wallet.Position("BTCUSDT")
		require.NoError(t, err)
		require.Equal(t, 2.0, asset)
	})
}
