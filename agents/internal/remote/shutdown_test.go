package remote

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/testdata/mocks"
)

// idleStrategy is a strategy placing no order of its own.
type idleStrategy struct{}

func (idleStrategy) Timeframe() string                                     { return "1m" }
func (idleStrategy) WarmupPeriod() int                                     { return 1 }
func (idleStrategy) Indicators(*model.Dataframe) []strategy.ChartIndicator { return nil }
func (idleStrategy) OnCandle(*model.Dataframe, service.Broker)             {}

// R5: as the agent is signaled to stop, its bot stops trading before the
// outbox is sent and closed: an order begun before the signal is recorded in
// the outbox, and sent, before Serve returns; one asked after it is refused
// without reaching the exchange.
func TestAgent_ServeStopsTheBotBeforeTheOutboxCloses(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.cfg = testConfig()
		f.cfg.LLM, f.cfg.Prompt = nil, nil
		f.runtime = agentapi.Runtime{Version: 1}
	})
	a, err := connect(context.Background(), f.client(), "")
	require.NoError(t, err)
	require.NoError(t, a.Open(t.TempDir()))
	orders, err := a.Orders(context.Background())
	require.NoError(t, err)

	const pair = "BTCUSDT"
	exch := &mocks.Exchange{}
	warmup := []model.Candle{{Pair: pair, Time: time.Now().Add(-time.Minute), Open: 100, Close: 100, High: 100,
		Low: 100, Complete: true, Metadata: map[string]float64{}}}
	exch.On("CandlesByLimit", mock.Anything, pair, "1m", 1).Return(warmup, nil)
	exch.On("CandlesSubscription", mock.Anything, pair, "1m").Return(make(chan model.Candle), make(chan error))
	entered, release := make(chan struct{}), make(chan struct{})
	exch.On("CreateOrderMarket", model.SideTypeBuy, pair, 1.0).Run(func(mock.Arguments) {
		close(entered)
		<-release
	}).Return(model.Order{ExchangeID: 701, Pair: pair, Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Price: 100, Quantity: 1, CreatedAt: time.Now()}, nil).Once()
	exch.On("CreateOrderMarket", model.SideTypeSell, pair, 2.0).Return(model.Order{ExchangeID: 702, Pair: pair,
		Status: model.OrderStatusTypeFilled}, nil).Maybe()

	bot, err := xtrader.NewBot(context.Background(), model.Settings{Pairs: []string{pair}}, exch, idleStrategy{},
		xtrader.WithStorage(orders))
	require.NoError(t, err)
	controller := bot.Controller()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- a.Serve(ctx, nil, bot.Run) }()
	require.Eventually(t, func() bool { return controller.Status() == order.StatusRunning }, 5*time.Second,
		time.Millisecond, "the bot never started")

	// an order of the bot at the exchange as the signal comes
	created := make(chan error, 1)
	go func() {
		_, err := controller.CreateOrderMarket(model.SideTypeBuy, pair, 1)
		created <- err
	}()
	<-entered
	cancel() // a signal

	require.Eventually(t, func() bool { return controller.Status() == order.StatusStopped }, 5*time.Second,
		time.Millisecond, "the bot trades on after the signal")
	_, err = controller.CreateOrderMarket(model.SideTypeSell, pair, 2)
	require.ErrorIs(t, err, order.ErrBotStopped, "an order asked after the signal")
	exch.AssertNotCalled(t, "CreateOrderMarket", model.SideTypeSell, pair, 2.0)

	select {
	case err := <-served:
		t.Fatalf("Serve returned with an order still at the exchange: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-created)
	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("still serving once the order was recorded")
	}

	require.NoError(t, a.Close())
	var keys []string
	for _, record := range f.written() {
		if record.Kind == agentapi.KindOrder {
			keys = append(keys, record.Key)
		}
	}
	require.NotEmpty(t, keys, "the order executed, recorded and sent")
	stored, err := orders.Orders()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, int64(701), stored[0].ExchangeID)
	assert.Contains(t, keys, strconv.FormatInt(stored[0].ID, 10))
	assert.Empty(t, orders.unwrittenIDs(), "nothing left out of the outbox")

	// the bot exited: nothing reaches the exchange any more, the cancels and
	// the protections included
	err = controller.Cancel(*stored[0])
	require.ErrorIs(t, err, order.ErrBotStopped)
	exch.AssertNotCalled(t, "Cancel", mock.Anything)
}
