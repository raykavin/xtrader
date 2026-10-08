package exchange

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// The dashboard and the Telegram bot read the paper wallet (Account, Position,
// the equity) while the bot feeds it candles and orders: every accessor takes
// the lock, none deadlocks with the methods holding it. Run with -race.
func TestPaperWallet_ReadersWhileTrading(t *testing.T) {
	wallet := NewPaperWallet(context.Background(), "USDT", WithPaperAsset("USDT", 1e9))
	pairs := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT", "XRPUSDT"}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, pair := range pairs[:1] {
		wallet.OnCandle(model.Candle{Pair: pair, Time: start, Open: 100, High: 101, Low: 99, Close: 100,
			Complete: true})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 16)

	// the bot: candles, filling the limit orders, and new pairs, which add
	// assets to the wallet
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 400 && ctx.Err() == nil; i++ {
			pair := pairs[i%len(pairs)]
			at := start.Add(time.Duration(i+1) * time.Minute)
			price := 100 + float64(i%7)
			wallet.OnCandle(model.Candle{Pair: pair, Time: at, Open: price, High: price + 2, Low: price - 2,
				Close: price, Complete: true})
			if _, err := wallet.CreateOrderMarket(model.SideTypeBuy, pair, 1); err != nil {
				errs <- fmt.Errorf("market buy %d: %w", i, err)
				return
			}
			limit, err := wallet.CreateOrderLimit(model.SideTypeSell, pair, 1, price+1)
			if err != nil {
				errs <- fmt.Errorf("limit %d: %w", i, err)
				return
			}
			if i%3 == 0 {
				_ = wallet.Cancel(limit) // filled already by a candle: ErrOrderNotOpen
			}
			// a pair never traded before
			asset := fmt.Sprintf("ZZRACE%d", i)
			fresh := asset + "USDT"
			RegisterPair(fresh, asset, "USDT")
			wallet.OnCandle(model.Candle{Pair: fresh, Time: at, Open: 10, High: 10, Low: 10, Close: 10,
				Complete: true})
			if _, err := wallet.CreateOrderMarket(model.SideTypeBuy, fresh, 1); err != nil {
				errs <- fmt.Errorf("fresh buy %d: %w", i, err)
				return
			}
		}
	}()

	// the readers
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				account, err := wallet.Account()
				if err != nil {
					errs <- err
					return
				}
				_ = account.Balances
				_ = wallet.Pairs()
				_ = wallet.AssetValues("BTC")
				_ = wallet.EquityValues()
				_, _, _ = wallet.MaxDrawdown()
				if _, _, err := wallet.Position("BTCUSDT"); err != nil {
					errs <- err
					return
				}
				_ = wallet.ID()
				_, _ = wallet.Order("BTCUSDT", 1)
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	go func() {
		// the bot is done: let the readers go
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: the readers or the bot never returned")
	}
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	// the copies are the wallet's values, not views of them
	equity := wallet.EquityValues()
	require.NotEmpty(t, equity)
	equity[0].Value = -1
	require.NotEqual(t, -1.0, wallet.EquityValues()[0].Value)
}
