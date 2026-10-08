package exchange

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/testdata/mocks"
)

// B9: a consumer of the candles that is slow, eg: the chart of a bot serving a
// snapshot, holds itself only: the bot, subscribed after it, takes the
// candles as they come, and the slow one takes them all, in their order, once
// it goes on.
func TestDataFeedStart_SlowConsumerHoldsItselfOnly(t *testing.T) {
	exchange := &mocks.Exchange{}
	candles := make(chan model.Candle)
	errs := make(chan error)
	exchange.On("CandlesSubscription", mock.Anything, "BTCUSDT", "1h").Return(candles, errs)

	const sent = 20
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	slow := newCandleRecorder(sent)
	bot := newCandleRecorder(sent)
	feed := NewDataFeed(exchange)
	feed.Subscribe("BTCUSDT", "1h", func(candle model.Candle) {
		<-release
		slow.consume(candle)
	}, false)
	feed.Subscribe("BTCUSDT", "1h", bot.consume, false)
	feed.Start(false)

	go func() {
		for i := 1; i <= sent; i++ {
			candles <- testCandle(i, i%2 == 0)
		}
	}()
	taken := bot.wait(t)
	close(release)
	for _, got := range [][]model.Candle{taken, slow.wait(t)} {
		require.Len(t, got, sent)
		for i, candle := range got {
			require.Equal(t, testCandle(i+1, (i+1)%2 == 0), candle, "in their order")
		}
	}
}

// The candles of a backtest are taken by every consumer before Start(true)
// returns, in their order.
func TestDataFeedStart_SyncDeliversBeforeReturning(t *testing.T) {
	exchange := &mocks.Exchange{}
	candles := make(chan model.Candle, 10)
	errs := make(chan error)
	exchange.On("CandlesSubscription", mock.Anything, "BTCUSDT", "1h").Return(candles, errs)

	var first, second []model.Candle
	feed := NewDataFeed(exchange)
	feed.Subscribe("BTCUSDT", "1h", func(candle model.Candle) {
		time.Sleep(time.Millisecond)
		first = append(first, candle)
	}, false)
	feed.Subscribe("BTCUSDT", "1h", func(candle model.Candle) { second = append(second, candle) }, true)
	for i := 1; i <= 10; i++ {
		candles <- testCandle(i, true)
	}
	close(candles)
	feed.Start(true)

	require.Len(t, first, 10)
	require.Len(t, second, 10)
	for i := range first {
		require.Equal(t, testCandle(i+1, true), first[i])
		require.Equal(t, testCandle(i+1, true), second[i])
	}
}

// A feed closed live: its consumers take the candles queued for them, then
// their goroutines end.
func TestDataFeedStart_ClosedFeedDeliversTheQueued(t *testing.T) {
	exchange := &mocks.Exchange{}
	candles := make(chan model.Candle)
	errs := make(chan error)
	exchange.On("CandlesSubscription", mock.Anything, "BTCUSDT", "1h").Return(candles, errs)

	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	slow := newCandleRecorder(3)
	feed := NewDataFeed(exchange)
	feed.Subscribe("BTCUSDT", "1h", func(candle model.Candle) {
		<-release
		slow.consume(candle)
	}, false)
	feed.Start(false)
	sentAll := make(chan struct{})
	go func() {
		defer close(sentAll)
		for i := 1; i <= 3; i++ {
			candles <- testCandle(i, true)
		}
		close(candles)
	}()
	select {
	case <-sentAll:
	case <-time.After(time.Second):
		t.Fatal("the feed held by its consumer")
	}
	close(release)
	require.Equal(t, []model.Candle{testCandle(1, true), testCandle(2, true), testCandle(3, true)}, slow.wait(t))
}
