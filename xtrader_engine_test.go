package xtrader

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/internal/recovery"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/testdata/mocks"
	"github.com/raykavin/xtrader/tools/log"
)

// tickStrategy is a high frequency strategy, as the LLM trader, recording the
// closes it ran on, panicking on the candles closing at panicAt (0: never), in
// its indicators with indicators, else in OnCandle.
type tickStrategy struct {
	mtx        sync.Mutex
	closes     []time.Time
	panicAt    float64
	indicators bool
}

func (s *tickStrategy) Timeframe() string { return "1d" }
func (s *tickStrategy) WarmupPeriod() int { return 1 }

func (s *tickStrategy) Indicators(df *Dataframe) []strategy.ChartIndicator {
	if s.indicators && s.panicAt != 0 && df.Close.Last(0) == s.panicAt {
		var values []float64
		_ = values[len(df.Close)] // eg: an indicator on the data of the day
	}
	return nil
}

func (s *tickStrategy) OnCandle(df *Dataframe, _ service.Broker) {
	if !s.indicators && s.panicAt != 0 && df.Close.Last(0) == s.panicAt {
		panic("boom")
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.closes = append(s.closes, df.Time[len(df.Time)-1])
}

func (s *tickStrategy) OnPartialCandle(*Dataframe, service.Broker) {}

func (s *tickStrategy) closed() []time.Time {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return append([]time.Time(nil), s.closes...)
}

// notices is a notifier keeping the errors it is told of.
type notices struct {
	mtx    sync.Mutex
	errors []error
}

func (n *notices) Notify(string)       {}
func (n *notices) OnOrder(model.Order) {}
func (n *notices) OnError(err error) {
	n.mtx.Lock()
	defer n.mtx.Unlock()
	n.errors = append(n.errors, err)
}

func (n *notices) errs() []error {
	n.mtx.Lock()
	defer n.mtx.Unlock()
	return append([]error(nil), n.errors...)
}

// liveBot runs a bot on BTCUSDT 1d with str, its warmup the candle at start,
// returning the stream of its live candles and the end of Run.
func liveBot(t *testing.T, ctx context.Context, str strategy.Strategy, start time.Time,
	options ...Option) (chan<- model.Candle, <-chan error) {
	t.Helper()
	exch := &mocks.Exchange{}
	warmup := []model.Candle{{Pair: "BTCUSDT", Time: start, Open: 100, Close: 100, High: 100, Low: 100,
		Complete: true, Metadata: map[string]float64{}}}
	exch.On("CandlesByLimit", mock.Anything, "BTCUSDT", "1d", 1).Return(warmup, nil)
	stream := make(chan model.Candle)
	exch.On("CandlesSubscription", mock.Anything, "BTCUSDT", "1d").Return(stream, make(chan error))
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	bot, err := NewBot(context.Background(), model.Settings{Pairs: []string{"BTCUSDT"}}, exch, str,
		append([]Option{WithStorage(memory), WithLogLevel(log.ErrorLevel)}, options...)...)
	require.NoError(t, err)

	ran := make(chan error, 1)
	go func() { ran <- bot.Run(ctx) }()
	return stream, ran
}

// B27: live, the close of a candle reaches the strategy before the next
// candle in progress, however fast they come: one of them handed first, the
// strategy took the close for a late candle and dropped it, eg: no analysis of
// the LLM on it.
func TestBotRun_EveryCloseReachesTheStrategy(t *testing.T) {
	start := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := &tickStrategy{}
	stream, ran := liveBot(t, ctx, ticks, start)

	const days = 2000
	for day := 1; day <= days; day++ {
		at := start.Add(time.Duration(day) * 24 * time.Hour)
		stream <- model.Candle{Pair: "BTCUSDT", Time: at, Open: 100, Close: 101, High: 102, Low: 99,
			Complete: true, UpdatedAt: at.Add(24*time.Hour - time.Second), Metadata: map[string]float64{}}
		next := at.Add(24 * time.Hour)
		stream <- model.Candle{Pair: "BTCUSDT", Time: next, Open: 101, Close: 101, High: 101, Low: 101,
			UpdatedAt: next, Metadata: map[string]float64{}}
	}

	ok := assertEventually(func() bool { return len(ticks.closed()) == days }, 10*time.Second)
	closes := ticks.closed()
	require.True(t, ok, "%d closes of %d reached the strategy", len(closes), days)
	for i, at := range closes {
		require.Equal(t, start.Add(time.Duration(i+1)*24*time.Hour), at, "in their order")
	}
	cancel()
	require.NoError(t, <-ran)
}

// assertEventually reports whether condition holds within wait.
func assertEventually(condition func() bool, wait time.Duration) bool {
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if condition() {
			return true
		}
	}
	return condition()
}

// R10: live, a panic of the strategy skips the candle it panicked on, notified,
// rather than taking the agent down: the bot goes on with the next candles.
func TestBotRun_PanicOfTheStrategySkipsTheCandleOnly(t *testing.T) {
	start := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := &tickStrategy{panicAt: 666}
	notifier := &notices{}
	stream, ran := liveBot(t, ctx, ticks, start, WithNotifier(notifier))

	day := func(n int, close float64) model.Candle {
		return model.Candle{Pair: "BTCUSDT", Time: start.Add(time.Duration(n) * 24 * time.Hour), Open: close,
			Close: close, High: close, Low: close, Complete: true, Metadata: map[string]float64{}}
	}
	stream <- day(1, 101)
	stream <- day(2, 666) // panics
	stream <- day(3, 103)

	require.True(t, assertEventually(func() bool { return len(ticks.closed()) == 2 }, 5*time.Second),
		"the candles after the panic")
	require.Equal(t, []time.Time{day(1, 0).Time, day(3, 0).Time}, ticks.closed())
	require.Len(t, notifier.errs(), 1)
	var recovered *recovery.Panic
	require.ErrorAs(t, notifier.errs()[0], &recovered)
	require.Contains(t, recovered.Error(), "OnCandle of the strategy on BTCUSDT panicked on the candle of "+
		"2023-01-03 00:00:00: boom")

	cancel()
	require.NoError(t, <-ran)
}

// R10: a panic in the warmup would come back at every start: the bot doesn't
// start, Run telling why, rather than the agent crashing in a loop with a
// stack only.
func TestBotRun_PanicInTheWarmupFailsTheStart(t *testing.T) {
	start := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	ticks := &tickStrategy{panicAt: 100, indicators: true} // the close of the warmup candle
	_, ran := liveBot(t, context.Background(), ticks, start)

	select {
	case err := <-ran:
		require.ErrorContains(t, err, "preloading BTCUSDT: Indicators of the strategy on BTCUSDT panicked on the "+
			"candle of 2023-01-01 00:00:00: runtime error: index out of range")
	case <-time.After(5 * time.Second):
		t.Fatal("Run went on")
	}
}

// R10: a panic of the strategy in a backtest ends it, returned: its results
// would be wrong.
func TestBotRun_PanicInABacktestEndsIt(t *testing.T) {
	ctx := context.Background()
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	csvFeed, err := exchange.NewCSVFeed("1h", exchange.PairFeed{Pair: "BTCUSDT", File: "testdata/btc-1h.csv",
		Timeframe: "1h"})
	require.NoError(t, err)
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 10000),
		exchange.WithDataFeed(csvFeed))
	bot, err := NewBot(ctx, Settings{Pairs: []string{"BTCUSDT"}}, wallet, panickingHourly{}, WithStorage(memory),
		WithBacktest(wallet), WithLogLevel(log.ErrorLevel))
	require.NoError(t, err)

	require.ErrorContains(t, bot.Run(ctx), "backtest: OnCandle of the strategy on BTCUSDT panicked")
}

// panickingHourly panics on its first candle.
type panickingHourly struct{}

func (panickingHourly) Timeframe() string                               { return "1h" }
func (panickingHourly) WarmupPeriod() int                               { return 1 }
func (panickingHourly) Indicators(*Dataframe) []strategy.ChartIndicator { return nil }
func (panickingHourly) OnCandle(*Dataframe, service.Broker)             { panic("boom") }
