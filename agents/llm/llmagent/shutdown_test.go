package main

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/testdata/mocks"
)

// waitingProvider answers once its context is done only, telling it was asked.
type waitingProvider struct{ asked chan struct{} }

func (p waitingProvider) Analyze(ctx context.Context, _ llm.Input) (llm.Decision, error) {
	p.asked <- struct{}{}
	<-ctx.Done()
	return llm.Decision{}, ctx.Err()
}

// R5: the bot of the agent stops at once as its context is done, the model
// asked about the candle in progress: the request is ended (see
// llm.Trader.Stop), rather than waited for until its timeout, and no order is
// placed.
func TestRunBot_TheBotStopsWithoutTheReplyOfTheModel(t *testing.T) {
	const pair = "BTCUSDT"
	cfg := llm.DefaultConfig()
	cfg.Timeout = time.Hour
	cfg.PositionSize = map[string]float64{pair: 1}
	provider := waitingProvider{asked: make(chan struct{}, 1)}
	trader, err := llm.New(cfg, provider)
	require.NoError(t, err)

	start := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	candle := func(i int) model.Candle {
		price := 100 + 2*math.Sin(float64(i)/8)
		return model.Candle{Pair: pair, Time: start.Add(time.Duration(i) * 15 * time.Minute), Open: price,
			Close: price, High: price + 0.2, Low: price - 0.2, Volume: 100, Complete: true,
			Metadata: map[string]float64{}}
	}
	warmup := make([]model.Candle, trader.WarmupPeriod())
	for i := range warmup {
		warmup[i] = candle(i)
	}
	exch := &mocks.Exchange{}
	exch.On("CandlesByLimit", mock.Anything, pair, cfg.Timeframe, trader.WarmupPeriod()).Return(warmup, nil)
	stream := make(chan model.Candle)
	exch.On("CandlesSubscription", mock.Anything, pair, cfg.Timeframe).Return(stream, make(chan error))
	exch.On("Position", pair).Return(0.0, 10000.0, nil).Maybe()
	exch.On("LastQuote", mock.Anything, pair).Return(warmup[len(warmup)-1].Close, nil).Maybe()

	memory, err := storage.FromMemory()
	require.NoError(t, err)
	bot, err := xtrader.NewBot(context.Background(), xtrader.Settings{Pairs: []string{pair}}, exch, trader,
		xtrader.WithStorage(memory))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- runBot(bot, trader)(ctx) }()
	stream <- candle(len(warmup))
	select {
	case <-provider.asked:
	case <-time.After(10 * time.Second):
		t.Fatal("the model never asked")
	}
	cancel() // a signal
	select {
	case err := <-ran:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the bot waits for the reply of the model")
	}
	require.Equal(t, order.StatusStopped, bot.Controller().Status())
	exch.AssertNotCalled(t, "CreateOrderMarket", mock.Anything, mock.Anything, mock.Anything)
}
