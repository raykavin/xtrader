package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
)

// slowClient is the ResponseWriter of a client reading slowly, eg: a phone
// downloading a large snapshot through ngrok: its Write blocks until released.
type slowClient struct {
	header  http.Header
	writing chan struct{} // closed by the first Write
	release chan struct{}
	once    sync.Once

	mu     sync.Mutex
	status int
	body   bytes.Buffer
}

func newSlowClient(t *testing.T) *slowClient {
	w := &slowClient{header: make(http.Header), writing: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(w.unblock)
	return w
}

func (w *slowClient) Header() http.Header { return w.header }

func (w *slowClient) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status = status
}

func (w *slowClient) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.writing) })
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}

func (w *slowClient) unblock() {
	select {
	case <-w.release:
	default:
		close(w.release)
	}
}

// serve serves the request of path to w in the background, returning when it
// was answered.
func serve(c *Chart, w http.ResponseWriter, path string) <-chan struct{} {
	served := make(chan struct{})
	go func() {
		defer close(served)
		c.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil))
	}()
	return served
}

// within fails the test unless fn returns within wait. fn must not call t: it
// may return after the test failed.
func within(t *testing.T, wait time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatalf("%s: still waiting after %s", what, wait)
	}
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: still waiting", what)
	}
}

// B9: a client downloading a snapshot slowly never holds the chart: its
// candles, its orders and the other requests go on as the client reads, the
// snapshot being the one of the time it was asked.
func TestChart_SlowSnapshotClientNeverHoldsTheChart(t *testing.T) {
	c := newTestChart(t)
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	c.OnCandle(candleAt("ETHUSDT", base, 3000))

	client := newSlowClient(t)
	served := serve(c, client, "/api/ETHUSDT/snapshot")
	waitFor(t, client.writing, "the snapshot written")

	pairs := httptest.NewRecorder()
	within(t, 2*time.Second, "the chart fed while the client reads", func() {
		c.OnCandle(candleAt("ETHUSDT", base.Add(time.Hour), 3010))
		partial := candleAt("ETHUSDT", base.Add(2*time.Hour), 3020)
		partial.Complete = false
		c.OnCandle(partial)
		c.OnOrder(model.Order{ID: 1, Pair: "ETHUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
			Status: model.OrderStatusTypeFilled, Price: 3010, Quantity: 1, CreatedAt: base, UpdatedAt: base})
		c.Handler().ServeHTTP(pairs, httptest.NewRequest(http.MethodGet, "http://localhost/api/pairs", nil))
	})
	assert.Equal(t, http.StatusOK, pairs.Code)

	client.unblock()
	waitFor(t, served, "the snapshot answered")
	client.mu.Lock()
	defer client.mu.Unlock()
	var snapshot Snapshot
	require.NoError(t, json.Unmarshal(client.body.Bytes(), &snapshot))
	assert.Len(t, snapshot.Candles, 1, "the snapshot of the time it was asked")
	assert.Empty(t, snapshot.Orders)
}

// B9: the order controller and the chart go on while a client downloads a
// snapshot slowly: the legs of a bracket filled and canceled reach the chart
// as the client reads, and the bot keeps trading.
func TestChart_SlowSnapshotClientNeverHoldsTheOrderController(t *testing.T) {
	ctx := context.Background()
	pair := "BTCUSDT"
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 100000))
	memory, err := storage.FromMemory()
	require.NoError(t, err)

	c := newTestChart(t)
	feed := order.NewOrderFeed()
	feed.Subscribe(pair, c.OnOrder, false)
	feed.Start()
	controller := order.NewController(ctx, wallet, memory, feed)

	candle := candleAt(pair, base, 1000)
	wallet.OnCandle(candle)
	c.OnCandle(candle)
	bracket, err := controller.CreateOrderMarketProtected(model.SideTypeBuy, pair, 1, 900, 1100)
	require.NoError(t, err)
	require.Len(t, bracket, 3)
	ids := []int64{bracket[0].ID, bracket[1].ID, bracket[2].ID}
	require.Eventually(t, func() bool { return chartHas(c, ids...) }, 5*time.Second, time.Millisecond)

	client := newSlowClient(t)
	served := serve(c, client, "/api/"+pair+"/snapshot")
	waitFor(t, client.writing, "the snapshot written")

	// the take profit fills, the stop loss is canceled with it
	wallet.OnCandle(model.Candle{Pair: pair, Time: base.Add(time.Minute), Open: 1000, High: 1150, Low: 990,
		Close: 1120, Complete: true})
	var exit model.Order
	within(t, 2*time.Second, "the order controller as the client reads", func() {
		controller.UpdateOrders()
		exit, err = controller.CreateOrderMarket(model.SideTypeSell, pair, 0.5)
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		ended := 0
		for _, leg := range bracket[1:] {
			if c.orderByID[leg.ID].Status.Final() {
				ended++
			}
		}
		return ended == 2 && c.orderByID[exit.ID].Status == model.OrderStatusTypeFilled
	}, 2*time.Second, time.Millisecond, "the orders reach the chart as the client reads")
	select {
	case <-served:
		t.Fatal("the client was still reading")
	default:
	}

	client.unblock()
	waitFor(t, served, "the snapshot answered")
	client.mu.Lock()
	defer client.mu.Unlock()
	var snapshot Snapshot
	require.NoError(t, json.Unmarshal(client.body.Bytes(), &snapshot))
	require.Len(t, snapshot.Orders, 3, "the snapshot of the time it was asked")
	for _, o := range snapshot.Orders[1:] {
		assert.Equal(t, string(model.OrderStatusTypeNew), o.Status, "the legs as they were")
	}
}

// chartHas reports whether the chart received the orders of ids.
func chartHas(c *Chart, ids ...int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		if _, ok := c.orderByID[id]; !ok {
			return false
		}
	}
	return true
}

// slowReporter is a broker whose account is read on the network, slowly: its
// AccountSummary waits for release.
type slowReporter struct {
	fakeReporter
	asking  chan struct{} // closed by the first AccountSummary
	release chan struct{}
	once    sync.Once
	summary atomic.Int32
}

func newSlowReporter(t *testing.T) *slowReporter {
	r := &slowReporter{fakeReporter: fakeReporter{booked: map[int64]float64{100: 12.5}},
		asking: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-r.release:
		default:
			close(r.release)
		}
	})
	return r
}

func (r *slowReporter) AccountSummary() (service.AccountSummary, bool) {
	r.summary.Add(1)
	r.once.Do(func() { close(r.asking) })
	<-r.release
	return r.fakeReporter.AccountSummary()
}

// B9: the currency of the account, read on the network, is never read holding
// the chart: the orders and candles of the bot go on while it is asked, and
// the snapshot shows it once read.
func TestChart_AccountCurrencyNeverReadHoldingTheChart(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	reporter := newSlowReporter(t)
	c := newTestChart(t, WithAccountReporter(reporter))
	c.OnCandle(candleAt("BTCUSDm", base, 84000))

	within(t, 2*time.Second, "the orders while the account is read", func() {
		c.OnOrder(model.Order{ID: 1, ExchangeID: 100, Pair: "BTCUSDm", Side: model.SideTypeBuy,
			Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Quantity: 0.1, Price: 84000,
			CreatedAt: base, UpdatedAt: base})
		at := base.Add(time.Minute)
		c.OnOrder(model.Order{ID: 2, ExchangeID: 101, Pair: "BTCUSDm", Side: model.SideTypeSell,
			Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Quantity: 0.1, Price: 84100,
			CreatedAt: at, UpdatedAt: at, ProfitValue: 10, Profit: 10 / 8400.0})
	})
	waitFor(t, reporter.asking, "the account asked") // in the background
	served := serve(c, httptest.NewRecorder(), "/api/BTCUSDm/snapshot")
	within(t, 2*time.Second, "the chart while the account is read", func() {
		c.OnCandle(candleAt("BTCUSDm", base.Add(time.Hour), 84100))
		pairs := httptest.NewRecorder()
		c.Handler().ServeHTTP(pairs, httptest.NewRequest(http.MethodGet, "http://localhost/api/pairs", nil))
	})

	close(reporter.release)
	waitFor(t, served, "the snapshot answered")
	snapshot := getSnapshot(t, c, "BTCUSDm")
	assert.Equal(t, "BRL", snapshot.AccountCurrency)
	require.Len(t, snapshot.Orders, 2)
	require.NotNil(t, snapshot.Orders[1].AccountProfit)
	assert.Equal(t, 12.5, *snapshot.Orders[1].AccountProfit)
	assert.Equal(t, "BRL", snapshot.Orders[1].AccountCurrency)
}

// connectedReporter is a broker that knows the currency of its account since
// it connected, eg: MT5: AccountSummary is never asked for it.
type connectedReporter struct {
	fakeReporter
	summary atomic.Int32
}

func (r *connectedReporter) AccountCurrency() string { return "BRL" }

func (r *connectedReporter) AccountSummary() (service.AccountSummary, bool) {
	r.summary.Add(1)
	return service.AccountSummary{}, false
}

func TestChart_AccountCurrencyOfTheConnection(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	reporter := &connectedReporter{fakeReporter: fakeReporter{booked: map[int64]float64{100: 12.5}}}
	c := newTestChart(t, WithAccountReporter(reporter))
	c.OnCandle(candleAt("BTCUSDm", base, 84000))
	events := c.events.subscribe()
	defer c.events.unsubscribe(events)

	c.OnOrder(model.Order{ID: 1, ExchangeID: 100, Pair: "BTCUSDm", Side: model.SideTypeBuy,
		Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Quantity: 0.1, Price: 84000,
		CreatedAt: base, UpdatedAt: base})
	c.OnOrder(model.Order{ID: 2, ExchangeID: 101, Pair: "BTCUSDm", Side: model.SideTypeSell,
		Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Quantity: 0.1, Price: 84100,
		CreatedAt: base, UpdatedAt: base.Add(time.Minute), ProfitValue: 10, Profit: 10 / 8400.0})
	<-events
	var event OrderEvent
	require.NoError(t, json.Unmarshal((<-events).data, &event))
	require.NotNil(t, event.Order.AccountProfit)
	assert.Equal(t, "BRL", event.Order.AccountCurrency, "known at once")

	assert.Equal(t, "BRL", getSnapshot(t, c, "BTCUSDm").AccountCurrency)
	assert.Zero(t, reporter.summary.Load(), "never asked to the broker")
}

// getSnapshot gets the snapshot of pair from the API.
func getSnapshot(t *testing.T, c *Chart, pair string) Snapshot {
	t.Helper()
	recorder := httptest.NewRecorder()
	c.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://localhost/api/"+pair+"/snapshot", nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var snapshot Snapshot
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &snapshot))
	return snapshot
}

// nanReporter books a profit that isn't a number.
type nanReporter struct{ fakeReporter }

func (r *nanReporter) ClosedProfit(model.Order, model.Order) (float64, bool) { return math.NaN(), true }

// R12: an order whose numbers aren't finite, eg: the profit of a fill without
// a price, is sent with them as 0: the snapshot of its pair and its events
// never fail, nor do the ones of a position or a level that aren't finite.
func TestChart_NumbersNotFiniteNeverBreakThePair(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	nan, inf := math.NaN(), math.Inf(1)
	c := newTestChart(t, WithAccountReporter(&nanReporter{}),
		WithBrokerPositions(func(string) (BrokerPosition, bool) {
			return BrokerPosition{Side: "BUY", Quantity: 1, Entry: nan, Profit: inf}, true
		}))
	c.strategy = levelsStrategy{levels: []Level{{Kind: "support", Price: nan}, {Kind: "resistance", Price: 3100}}}
	c.OnCandle(candleAt("ETHUSDT", base, 3000))
	events := c.events.subscribe()
	defer c.events.unsubscribe(events)

	stop := nan
	c.OnOrder(model.Order{ID: 1, ExchangeID: 10, Pair: "ETHUSDT", Side: model.SideTypeBuy,
		Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 0, Quantity: 1,
		CreatedAt: base, UpdatedAt: base})
	c.OnOrder(model.Order{ID: 2, ExchangeID: 11, Pair: "ETHUSDT", Side: model.SideTypeSell,
		Type: model.OrderTypeStopLoss, Status: model.OrderStatusTypeFilled, Price: 3000, Quantity: 1, Stop: &stop,
		CreatedAt: base, UpdatedAt: base.Add(time.Minute), Profit: inf, ProfitValue: nan, RefPrice: -inf})

	for range 2 {
		select {
		case event := <-events:
			require.Equal(t, "order", event.name)
		case <-time.After(time.Second):
			t.Fatal("an order event was dropped")
		}
	}
	c.OnCandle(candleAt("ETHUSDT", base.Add(time.Hour), 3010))
	select {
	case event := <-events:
		require.Equal(t, "candle", event.name)
		var candle CandleEvent
		require.NoError(t, json.Unmarshal(event.data, &candle))
		assert.Equal(t, []Level{{Kind: "resistance", Price: 3100}}, candle.Levels)
	case <-time.After(time.Second):
		t.Fatal("the candle event was dropped")
	}

	snapshot := getSnapshot(t, c, "ETHUSDT")
	require.Len(t, snapshot.Orders, 2)
	closed := snapshot.Orders[1]
	assert.Zero(t, closed.Profit)
	assert.Zero(t, closed.ProfitValue)
	assert.Zero(t, closed.RefPrice)
	require.NotNil(t, closed.Stop)
	assert.Zero(t, *closed.Stop)
	assert.Nil(t, closed.AccountProfit, "a booked profit that isn't a number is unknown")
	assert.Equal(t, 3000.0, closed.Price)
	require.NotNil(t, snapshot.Position)
	assert.Zero(t, snapshot.Position.Entry)
	assert.Zero(t, snapshot.Position.Profit)
	assert.Equal(t, 1.0, snapshot.Position.Quantity)
}

// levelsStrategy draws levels, see LevelsSource.
type levelsStrategy struct {
	gapStrategy
	levels []Level
}

func (s levelsStrategy) ChartLevels(string) []Level { return append([]Level(nil), s.levels...) }
