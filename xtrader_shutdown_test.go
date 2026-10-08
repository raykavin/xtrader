package xtrader

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/testdata/mocks"
)

// slowStrategy holds its candle until released, eg: asking an LLM, then
// buys, telling how that went.
type slowStrategy struct {
	entered, release chan struct{}
	bought           chan error
}

func (slowStrategy) Timeframe() string                               { return "1d" }
func (slowStrategy) WarmupPeriod() int                               { return 1 }
func (slowStrategy) Indicators(*Dataframe) []strategy.ChartIndicator { return nil }

func (s slowStrategy) OnCandle(df *Dataframe, broker service.Broker) {
	close(s.entered)
	<-s.release
	_, err := broker.CreateOrderMarket(model.SideTypeBuy, df.Pair, 1)
	s.bought <- err
}

// R5: live, Run stops trading as its context is done, before it returns: the
// orders asked after, the one of the candle in progress included, are refused
// without reaching the exchange; Run returns once that candle ended, the order
// controller closed.
func TestBotRun_StopsTradingWithItsContext(t *testing.T) {
	start := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	warmup := []model.Candle{{Pair: "BTCUSDT", Time: start, Open: 100, Close: 100, High: 100, Low: 100,
		Complete: true, Metadata: map[string]float64{}}}
	exch := &mocks.Exchange{}
	exch.On("CandlesByLimit", mock.Anything, "BTCUSDT", "1d", 1).Return(warmup, nil)
	stream := make(chan model.Candle)
	exch.On("CandlesSubscription", mock.Anything, "BTCUSDT", "1d").Return(stream, make(chan error))

	slow := slowStrategy{entered: make(chan struct{}), release: make(chan struct{}), bought: make(chan error, 1)}
	memory := newTestBot(t, exch).storage
	bot, err := NewBot(context.Background(), model.Settings{Pairs: []string{"BTCUSDT"}}, exch, slow,
		WithStorage(memory))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- bot.Run(ctx) }()
	stream <- model.Candle{Pair: "BTCUSDT", Time: start.Add(24 * time.Hour), Open: 100, Close: 120, High: 125,
		Low: 95, Complete: true, Metadata: map[string]float64{}}
	<-slow.entered
	cancel() // a signal, the candle in progress

	require.Eventually(t, func() bool { return bot.Controller().Status() == order.StatusStopped }, time.Second,
		time.Millisecond, "the bot trades on after its context is done")
	select {
	case err := <-ran:
		t.Fatalf("Run returned during the candle in progress: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(slow.release)
	require.ErrorIs(t, <-slow.bought, order.ErrBotStopped, "the order of the candle in progress")
	select {
	case err := <-ran:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run never returned")
	}
	_, err = bot.Controller().Protect(model.SideTypeBuy, "BTCUSDT", 1, 90, 130)
	require.ErrorIs(t, err, order.ErrBotStopped, "closed: nothing reaches the exchange any more")
	exch.AssertNotCalled(t, "CreateOrderMarket", mock.Anything, mock.Anything, mock.Anything)
}
