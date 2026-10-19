package exchange

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// paperAt returns a wallet funded with USDT whose last candle of BTCUSDT closed
// at 100.
func paperAt(t *testing.T, start time.Time) *PaperWallet {
	t.Helper()
	wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 100000))
	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: start, Open: 100, Low: 100, High: 100, Close: 100,
		Complete: true})
	return wallet
}

// stopLeg returns the stop loss among orders.
func stopLeg(t *testing.T, orders []model.Order) model.Order {
	t.Helper()
	for _, order := range orders {
		if order.Type == model.OrderTypeStopLoss {
			return order
		}
	}
	t.Fatal("no stop loss")
	return model.Order{}
}

// A stop loss moved within a candle never fills on the extremes the candle
// had before the move: only on a close beyond it or a new extreme; the next
// candle fills it as any other.
func TestPaperWallet_StopMovedWithinTheCandle(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	next := start.Add(time.Minute)
	partial := func(low, high, close float64) model.Candle {
		return model.Candle{Pair: "BTCUSDT", Time: next, Open: 100, Low: low, High: high, Close: close}
	}
	status := func(t *testing.T, wallet *PaperWallet, leg model.Order) model.OrderStatusType {
		t.Helper()
		got, err := wallet.Order("BTCUSDT", leg.ExchangeID)
		require.NoError(t, err)
		return got.Status
	}

	for _, tc := range []struct {
		name   string
		after  model.Candle
		filled bool
	}{
		{"the old low again, the close above: kept", partial(95, 106, 103), false},
		{"a close at the stop: filled", partial(95, 106, 100), true},
		{"a new low below the one of the move: filled", partial(94, 106, 101), true},
	} {
		t.Run("a long, "+tc.name, func(t *testing.T) {
			wallet := paperAt(t, start)
			orders, err := wallet.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 1, 90, 130)
			require.NoError(t, err)
			wallet.OnCandle(partial(95, 105, 104)) // the low of the candle before the move
			leg := stopLeg(t, orders[1:])
			_, err = wallet.UpdateProtection(leg, 100)
			require.NoError(t, err)

			wallet.OnCandle(tc.after)
			require.Equal(t, tc.filled, status(t, wallet, leg) == model.OrderStatusTypeFilled)
		})
	}

	t.Run("a short mirrored: the old high doesn't fill it, a new one does", func(t *testing.T) {
		wallet := paperAt(t, start)
		orders, err := wallet.CreateOrderMarketProtected(model.SideTypeSell, "BTCUSDT", 1, 110, 70)
		require.NoError(t, err)
		wallet.OnCandle(partial(96, 105, 97))
		leg := stopLeg(t, orders[1:])
		_, err = wallet.UpdateProtection(leg, 100)
		require.NoError(t, err)

		wallet.OnCandle(partial(96, 105, 98))
		require.Equal(t, model.OrderStatusTypeNew, status(t, wallet, leg))
		wallet.OnCandle(partial(96, 106, 99))
		require.Equal(t, model.OrderStatusTypeFilled, status(t, wallet, leg))
	})

	t.Run("the next candle fills it on its extreme", func(t *testing.T) {
		wallet := paperAt(t, start)
		orders, err := wallet.CreateOrderMarketProtected(model.SideTypeBuy, "BTCUSDT", 1, 90, 130)
		require.NoError(t, err)
		wallet.OnCandle(partial(95, 105, 104))
		leg := stopLeg(t, orders[1:])
		_, err = wallet.UpdateProtection(leg, 100)
		require.NoError(t, err)

		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: next.Add(time.Minute), Open: 104, Low: 99, High: 104,
			Close: 103})
		require.Equal(t, model.OrderStatusTypeFilled, status(t, wallet, leg))
	})
}

// The legs of a pending entry wait for it, false; once it fills they protect
// the position, true.
func TestPaperWallet_LiveProtection(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	wallet := paperAt(t, start)
	orders, err := wallet.CreateOrderLimitProtected(model.SideTypeBuy, "BTCUSDT", 1, 95, 90, 120)
	require.NoError(t, err)
	leg := stopLeg(t, orders[1:])

	live, protects, err := wallet.LiveProtection(leg)
	require.NoError(t, err)
	require.False(t, protects, "waiting for its entry")
	require.Equal(t, 90.0, live.Price)

	wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: start.Add(time.Minute), Open: 100, Low: 94, High: 100,
		Close: 96, Complete: true})
	_, protects, err = wallet.LiveProtection(leg)
	require.NoError(t, err)
	require.True(t, protects, "the entry filled")
}

// A position without a stop loss gets one: alone without legs; joining its
// take profit in its group, filled first when a candle touches both.
func TestPaperWallet_SetStop(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("without legs", func(t *testing.T) {
		wallet := paperAt(t, start)
		_, err := wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		require.NoError(t, err)
		legs, err := wallet.SetStop(model.SideTypeBuy, "BTCUSDT", 1, 92)
		require.NoError(t, err)
		require.Len(t, legs, 1)
		require.Equal(t, model.OrderTypeStopLoss, legs[0].Type)
		require.Equal(t, 92.0, *legs[0].Stop)

		_, err = wallet.SetStop(model.SideTypeBuy, "BTCUSDT", 1, 93)
		require.ErrorIs(t, err, ErrNotSupported, "a stop loss already")
	})

	t.Run("with a take profit", func(t *testing.T) {
		wallet := paperAt(t, start)
		_, err := wallet.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		require.NoError(t, err)
		tp, err := wallet.Protect(model.SideTypeBuy, "BTCUSDT", 1, 0, 110)
		require.NoError(t, err)
		require.Len(t, tp, 1)

		legs, err := wallet.SetStop(model.SideTypeBuy, "BTCUSDT", 1, 92)
		require.NoError(t, err)
		require.Len(t, legs, 1)
		require.Equal(t, *tp[0].GroupID, *legs[0].GroupID, "in the group of the take profit")

		// a candle touching both: the stop loss fills, the worst assumed
		wallet.OnCandle(model.Candle{Pair: "BTCUSDT", Time: start.Add(time.Minute), Open: 100, Low: 91, High: 111,
			Close: 100, Complete: true})
		stop, err := wallet.Order("BTCUSDT", legs[0].ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeFilled, stop.Status)
		profit, err := wallet.Order("BTCUSDT", tp[0].ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, profit.Status)
		asset, _, err := wallet.Position("BTCUSDT")
		require.NoError(t, err)
		require.Zero(t, asset, "closed once, by the stop loss")
	})
}
