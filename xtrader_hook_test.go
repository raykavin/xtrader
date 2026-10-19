package xtrader

import (
	"context"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/strategy"
)

// orderedStrategy records its closed candles in calls, panicking when told.
type orderedStrategy struct {
	calls *[]string
	panic bool
}

func (s orderedStrategy) Timeframe() string { return "1m" }
func (s orderedStrategy) WarmupPeriod() int { return 1 }
func (s orderedStrategy) Indicators(*Dataframe) []strategy.ChartIndicator {
	return nil
}
func (s orderedStrategy) OnCandle(*Dataframe, service.Broker) {
	*s.calls = append(*s.calls, "strategy")
	if s.panic {
		panic("the strategy failed")
	}
}

// recordingHook records the candles it is told in calls, panicking when told.
type recordingHook struct {
	calls *[]string
	panic bool
}

func (h recordingHook) OnCandle(candle model.Candle) {
	*h.calls = append(*h.calls, map[bool]string{true: "hook closed", false: "hook partial"}[candle.Complete])
	if h.panic {
		panic("the hook failed")
	}
}

// The hook of the candles runs live after the strategy, on the candles in
// progress and the closed ones, its panic and the strategy's recovered; never
// before Run lets it (the preload, a backtest).
func TestCandleHook(t *testing.T) {
	newBot := func(t *testing.T, strategyPanics, hookPanics bool) (*XTrader, *[]string) {
		t.Helper()
		var calls []string
		ctx := context.Background()
		st, err := storage.FromMemory()
		require.NoError(t, err)
		wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 10000))
		strat := orderedStrategy{calls: &calls, panic: strategyPanics}
		bot, err := NewBot(ctx, Settings{Pairs: []string{"BTCUSDT"}}, wallet, strat, WithStorage(st),
			WithPaperWallet(wallet), WithLogLevel(log.ErrorLevel),
			WithCandleHook(recordingHook{calls: &calls, panic: hookPanics}))
		require.NoError(t, err)
		bot.strategiesControllers["BTCUSDT"] = strategy.NewStrategyController("BTCUSDT", strat, bot.orderController)
		bot.strategiesControllers["BTCUSDT"].Start()
		return bot, &calls
	}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	closed := model.Candle{Pair: "BTCUSDT", Time: start, Open: 100, Close: 100, High: 100, Low: 100, Complete: true}
	partial := model.Candle{Pair: "BTCUSDT", Time: start.Add(time.Minute), Open: 100, Close: 101, High: 101,
		Low: 100}

	t.Run("not before Run lets it: the preload", func(t *testing.T) {
		bot, calls := newBot(t, false, false)
		_ = bot.processCandle(closed)
		require.Equal(t, []string{"strategy"}, *calls)
	})

	t.Run("live, after the strategy", func(t *testing.T) {
		bot, calls := newBot(t, false, false)
		bot.hooked = true
		_ = bot.processCandle(closed)
		_ = bot.processCandle(partial)
		require.Equal(t, []string{"strategy", "hook closed", "hook partial"}, *calls)
	})

	t.Run("the panics recovered, the hook told all the same", func(t *testing.T) {
		bot, calls := newBot(t, true, true)
		bot.hooked = true
		err := bot.processCandle(closed)
		require.Error(t, err, "the panic of the strategy reported")
		require.Equal(t, []string{"strategy", "hook closed"}, *calls)
		require.NotPanics(t, func() { _ = bot.processCandle(closed) })
	})
}
