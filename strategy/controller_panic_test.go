package strategy

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/internal/recovery"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/testdata/mocks"
)

// panicky is a high frequency strategy panicking on the candles closing at
// panicAt, in its indicators or in its call, as an index out of range of an
// indicator on the data of the day does.
type panicky struct {
	fakeHighFrequencyStrategy
	panicAt    float64
	indicators bool // panics in Indicators, else in OnCandle / OnPartialCandle
}

func (p *panicky) Indicators(df *model.Dataframe) []ChartIndicator {
	if p.indicators && df.Close.Last(0) == p.panicAt {
		var values []float64
		_ = values[len(df.Close)]
	}
	return p.fakeHighFrequencyStrategy.Indicators(df)
}

func (p *panicky) OnCandle(df *model.Dataframe, broker service.Broker) {
	if !p.indicators && df.Close.Last(0) == p.panicAt {
		panic("boom")
	}
	p.fakeHighFrequencyStrategy.OnCandle(df, broker)
}

func (p *panicky) OnPartialCandle(df *model.Dataframe, broker service.Broker) {
	if !p.indicators && df.Close.Last(0) == p.panicAt {
		panic("boom")
	}
	p.fakeHighFrequencyStrategy.OnPartialCandle(df, broker)
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

// R10: a panic of the strategy fails the candle it was called on only:
// recovered, notified and returned, the next candles run as before, rather
// than the panic taking the whole agent down.
func TestController_PanicOfTheStrategySkipsTheCandle(t *testing.T) {
	for _, indicators := range []bool{false, true} {
		name := "OnCandle"
		if indicators {
			name = "Indicators"
		}
		t.Run(name, func(t *testing.T) {
			strategy := &panicky{fakeHighFrequencyStrategy: fakeHighFrequencyStrategy{
				fakeStrategy: fakeStrategy{warmupPeriod: 1}}, panicAt: 101, indicators: indicators}
			controller := NewStrategyController("BTCUSDT", strategy, &mocks.Broker{})
			notifier := &notices{}
			controller.SetNotifier(notifier)
			controller.Start()

			require.NoError(t, controller.OnCandle(candle(0, 100)))
			err := controller.OnCandle(candle(1, 101))
			var recovered *recovery.Panic
			require.ErrorAs(t, err, &recovered)
			require.Contains(t, err.Error(), name+" of the strategy on BTCUSDT panicked on the candle of "+
				"2023-01-01 01:00:00")
			require.NotEmpty(t, recovered.Stack)
			require.NoError(t, controller.OnCandle(candle(2, 102)), "the next candle")

			closes := make([]float64, 0, len(strategy.onCandleCalls))
			for _, df := range strategy.onCandleCalls {
				closes = append(closes, df.Close.Last(0))
			}
			require.Equal(t, []float64{100, 102}, closes, "the candle that panicked skipped only")
			require.Equal(t, []float64{100, 101, 102}, []float64(controller.dataframe.Close), "kept")
			require.Len(t, notifier.errors, 1)
			require.ErrorIs(t, notifier.errors[0], recovered, "the panic returned")
			require.Contains(t, notifier.errors[0].Error(), "the strategy skipped the candle")
		})
	}
}

// R10: the same for a candle in progress of a high frequency strategy, the
// notices of the panics of every tick sent once a while.
func TestController_PanicOnACandleInProgressSkipsIt(t *testing.T) {
	strategy := &panicky{fakeHighFrequencyStrategy: fakeHighFrequencyStrategy{
		fakeStrategy: fakeStrategy{warmupPeriod: 1}}, panicAt: 101}
	controller := NewStrategyController("BTCUSDT", strategy, &mocks.Broker{})
	notifier := &notices{}
	controller.SetNotifier(notifier)
	controller.Start()

	require.NoError(t, controller.OnCandle(candle(0, 100)))
	partial := candle(1, 101)
	partial.Complete = false
	require.Error(t, controller.OnPartialCandle(partial))
	require.Error(t, controller.OnPartialCandle(partial), "every tick")
	partial.Close = 100.5
	require.NoError(t, controller.OnPartialCandle(partial))
	require.Len(t, strategy.onPartialCalls, 1)
	require.Len(t, notifier.errors, 1, "notified once a while")

	require.NoError(t, controller.OnCandle(candle(1, 103)), "its close")
	require.Len(t, strategy.onCandleCalls, 2)
}
