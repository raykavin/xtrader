package ui

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

func newTestChart(t *testing.T, options ...Option) *Chart {
	t.Helper()
	c, err := New(options...)
	require.NoError(t, err)
	return c
}

func candleAt(pair string, ts time.Time, close float64) model.Candle {
	return model.Candle{
		Pair:     pair,
		Time:     ts,
		Open:     close - 1,
		Close:    close,
		High:     close + 2,
		Low:      close - 2,
		Volume:   100,
		Complete: true,
	}
}

func TestChart_OnCandle(t *testing.T) {
	c := newTestChart(t)
	base := time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC)

	c.OnCandle(candleAt("ETHUSDT", base, 3000))
	c.OnCandle(model.Candle{Pair: "ETHUSDT", Time: base.Add(time.Hour), Close: 1, Complete: false})
	c.OnCandle(candleAt("ETHUSDT", base, 3000)) // duplicated
	c.OnCandle(candleAt("ETHUSDT", base.Add(time.Hour), 3010))
	c.OnCandle(candleAt("BTCUSDT", base, 40000))

	require.Len(t, c.candles["ETHUSDT"], 2)
	require.Len(t, c.candles["BTCUSDT"], 1)
	assert.Equal(t, Candle{Time: base.Unix(), Open: 2999, High: 3002, Low: 2998, Close: 3000, Volume: 100}, c.candles["ETHUSDT"][0])
	assert.Equal(t, []string{"BTCUSDT", "ETHUSDT"}, c.pairs())
	assert.Equal(t, []float64{3000, 3010}, []float64(c.dataframe["ETHUSDT"].Close))
	assert.WithinDuration(t, time.Now(), c.lastUpdate, time.Minute)
}

func TestChart_OnOrderAndSnapshot(t *testing.T) {
	c := newTestChart(t)
	base := time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC)

	c.OnCandle(candleAt("ETHUSDT", base, 3000))
	c.OnCandle(candleAt("ETHUSDT", base.Add(time.Hour), 3010))
	c.OnCandle(candleAt("ETHUSDT", base.Add(2*time.Hour), 3020))

	stop := 2900.0
	c.OnOrder(model.Order{
		ID: 1, Pair: "ETHUSDT", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Price: 3000, Quantity: 1,
		CreatedAt: base, UpdatedAt: base.Add(30 * time.Minute),
	})
	c.OnOrder(model.Order{
		ID: 2, Pair: "ETHUSDT", Side: model.SideTypeSell, Type: model.OrderTypeStopLoss,
		Status: model.OrderStatusTypeFilled, Price: 3020, Quantity: 1, Stop: &stop,
		CreatedAt: base.Add(time.Hour), UpdatedAt: base.Add(5 * time.Hour), // after last candle
		Profit: 0.1, ProfitValue: 20, Fee: 3.02, RefPrice: 3000,
	})
	// order for a pair without candles must not panic
	c.OnOrder(model.Order{ID: 3, Pair: "SOLUSDT", UpdatedAt: base})

	snap := c.snapshot("ETHUSDT")
	assert.Equal(t, "ETHUSDT", snap.Pair)
	assert.Equal(t, "ETH", snap.Asset)
	assert.Equal(t, "USDT", snap.Quote)
	require.Len(t, snap.Candles, 3)
	require.Len(t, snap.Orders, 2)
	assert.Nil(t, snap.MaxDrawdown)
	assert.Empty(t, snap.EquityValues)
	assert.NotNil(t, snap.Indicators)

	first, second := snap.Orders[0], snap.Orders[1]
	assert.Equal(t, int64(1), first.ID)
	assert.Equal(t, "BUY", first.Side)
	assert.Equal(t, base.Unix(), first.CandleTime, "order inside first candle")
	assert.Equal(t, int64(2), second.ID)
	assert.Equal(t, "STOP_LOSS", second.Type)
	assert.Equal(t, base.Add(2*time.Hour).Unix(), second.CandleTime, "order after last candle snaps to it")
	require.NotNil(t, second.Stop)
	assert.Equal(t, stop, *second.Stop)
	assert.Equal(t, 0.1, second.Profit)

	rows := c.orderRowsByPair("ETHUSDT")
	require.Len(t, rows, 2)
	assert.Equal(t, []string{base.Format(time.RFC3339), "FILLED", "BUY", "1", "MARKET", "1.000000",
		"3000.000000", "3000.00", "0.0000", ""}, rows[0])
	assert.Equal(t, "3.0200", rows[1][8])
	assert.Equal(t, "0.10", rows[1][9])

	assert.Empty(t, c.orderRowsByPair("UNKNOWN"))
	assert.Empty(t, c.ordersByPair("UNKNOWN"))
}

func TestCandleTimeFor(t *testing.T) {
	candles := []Candle{{Time: 100}, {Time: 200}, {Time: 300}}
	assert.Equal(t, int64(0), candleTimeFor(nil, time.Unix(150, 0)))
	assert.Equal(t, int64(100), candleTimeFor(candles, time.Unix(50, 0)))
	assert.Equal(t, int64(100), candleTimeFor(candles, time.Unix(100, 0)))
	assert.Equal(t, int64(100), candleTimeFor(candles, time.Unix(199, 0)))
	assert.Equal(t, int64(200), candleTimeFor(candles, time.Unix(200, 0)))
	assert.Equal(t, int64(300), candleTimeFor(candles, time.Unix(999, 0)))
}

type fakeIndicator struct {
	loaded bool
}

func (f *fakeIndicator) Name() string  { return "FAKE" }
func (f *fakeIndicator) Overlay() bool { return true }
func (f *fakeIndicator) Warmup() int   { return 0 }
func (f *fakeIndicator) Load(df *model.Dataframe) {
	f.loaded = true
}
func (f *fakeIndicator) Metrics() []IndicatorMetric {
	return []IndicatorMetric{{
		Name:   "fake",
		Color:  "red",
		Style:  "line",
		Values: []float64{1, 2},
		Time:   []time.Time{time.Unix(10, 0), time.Unix(20, 0)},
	}}
}

type fakeStrategy struct{}

func (fakeStrategy) Timeframe() string                         { return "1h" }
func (fakeStrategy) WarmupPeriod() int                         { return 1 }
func (fakeStrategy) OnCandle(*model.Dataframe, service.Broker) {}
func (fakeStrategy) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	return []strategy.ChartIndicator{
		{
			GroupName: "Close",
			Overlay:   true,
			Warmup:    1,
			Time:      df.Time,
			Metrics: []strategy.IndicatorMetric{
				{Name: "close", Color: "blue", Style: strategy.StyleLine, Values: df.Close},
				{Name: "short", Color: "blue", Style: strategy.StyleLine, Values: nil}, // below warmup, skipped
			},
		},
	}
}

func TestChart_Indicators(t *testing.T) {
	fake := &fakeIndicator{}
	c := newTestChart(t, WithCustomIndicators(fake), WithStrategyIndicators(fakeStrategy{}))
	base := time.Unix(1_700_000_000, 0).UTC()

	assert.Empty(t, c.indicatorsByPair("ETHUSDT"), "no dataframe yet")

	c.OnCandle(candleAt("ETHUSDT", base, 10))
	c.OnCandle(candleAt("ETHUSDT", base.Add(time.Hour), 11))

	indicators := c.indicatorsByPair("ETHUSDT")
	require.Len(t, indicators, 2)
	assert.True(t, fake.loaded)

	assert.Equal(t, "FAKE", indicators[0].Name)
	assert.Equal(t, []Point{{Time: 10, Value: 1}, {Time: 20, Value: 2}}, indicators[0].Metrics[0].Points)

	assert.Equal(t, "Close", indicators[1].Name)
	require.Len(t, indicators[1].Metrics, 1, "metric below warmup is skipped")
	assert.Equal(t, []Point{{Time: base.Add(time.Hour).Unix(), Value: 11}}, indicators[1].Metrics[0].Points)

	event := c.candleEvent("ETHUSDT", c.candles["ETHUSDT"][1])
	require.Len(t, event.Indicators, 2)
	assert.Equal(t, Point{Time: 20, Value: 2}, event.Indicators[0].Metrics[0].Point)
	assert.Nil(t, event.Equity)
}

// warmupStrategy needs more candles than its indicators do.
type warmupStrategy struct{ fakeStrategy }

func (warmupStrategy) WarmupPeriod() int { return 5 }
func (warmupStrategy) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	return []strategy.ChartIndicator{
		{
			GroupName: "declared",
			Warmup:    1,
			Time:      df.Time,
			Metrics:   []strategy.IndicatorMetric{{Name: "close", Values: df.Close}},
		},
		{
			GroupName: "undeclared",
			Time:      df.Time,
			Metrics:   []strategy.IndicatorMetric{{Name: "close", Values: df.Close}},
		},
	}
}

func TestChart_IndicatorWarmup(t *testing.T) {
	c := newTestChart(t, WithStrategyIndicators(warmupStrategy{}))
	base := time.Unix(1_700_000_000, 0).UTC()
	c.OnCandle(candleAt("ETHUSDT", base, 10))
	c.OnCandle(candleAt("ETHUSDT", base.Add(time.Hour), 11))

	indicators := c.indicatorsByPair("ETHUSDT")
	require.Len(t, indicators, 2)

	// the warmup of an indicator is trusted, even below the strategy's
	require.Len(t, indicators[0].Metrics, 1)
	assert.Equal(t, []Point{{Time: base.Add(time.Hour).Unix(), Value: 11}}, indicators[0].Metrics[0].Points)

	// without one, the indicator waits for the warmup of the strategy
	assert.Empty(t, indicators[1].Metrics)
}

func TestChart_WithPaperWallet(t *testing.T) {
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 1000))
	c := newTestChart(t, WithPaperWallet(wallet), WithPort(9999))
	assert.Equal(t, 9999, c.port)

	base := time.Unix(1_700_000_000, 0).UTC()
	c.OnCandle(candleAt("ETHUSDT", base, 10))
	wallet.OnCandle(candleAt("ETHUSDT", base, 10))
	wallet.OnCandle(candleAt("ETHUSDT", base.Add(time.Hour), 12))

	snap := c.snapshot("ETHUSDT")
	require.NotEmpty(t, snap.EquityValues)
	assert.Equal(t, 1000.0, snap.EquityValues[0].Value)
	assert.NotNil(t, snap.MaxDrawdown)

	event := c.candleEvent("ETHUSDT", c.candles["ETHUSDT"][0])
	require.NotNil(t, event.Equity)
}

func TestToPoints(t *testing.T) {
	points := toPoints([]time.Time{time.Unix(1, 0), time.Unix(2, 0)}, []float64{1})
	assert.Equal(t, []Point{{Time: 1, Value: 1}}, points)
	assert.Empty(t, toPoints(nil, []float64{1, 2}))

	times := []time.Time{time.Unix(1, 0), time.Unix(2, 0), time.Unix(3, 0), time.Unix(4, 0), time.Unix(5, 0)}
	points = toPoints(times, []float64{math.NaN(), 2, math.Inf(1), math.Inf(-1), 5})
	assert.Equal(t, []Point{{Time: 2, Value: 2}, {Time: 5, Value: 5}}, points, "the gaps have no point")
}

// gapStrategy has an indicator with gaps, NaN as the na of Pine.
type gapStrategy struct{ fakeStrategy }

func (gapStrategy) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	values := make([]float64, len(df.Close))
	for i := range values {
		values[i] = math.NaN()
		if i%2 == 0 {
			values[i] = df.Close[i]
		}
	}
	return []strategy.ChartIndicator{{
		GroupName: "Range",
		Overlay:   true,
		Warmup:    1,
		Time:      df.Time,
		Metrics:   []strategy.IndicatorMetric{{Name: "Range high", Values: values}},
	}}
}

func TestChart_IndicatorGaps(t *testing.T) {
	c := newTestChart(t, WithStrategyIndicators(gapStrategy{}))
	base := time.Unix(1_700_000_000, 0).UTC()
	events := c.events.subscribe()
	defer c.events.unsubscribe(events)
	for i := range 5 {
		c.OnCandle(candleAt("ETHUSDT", base.Add(time.Duration(i)*time.Hour), float64(10+i)))
	}

	// the candles with a gap are still sent, with the last point of the indicator
	for i := range 5 {
		event := <-events
		assert.Equal(t, "candle", event.name, i)
		assert.NotContains(t, string(event.data), "NaN")
	}

	srv := newTestServer(t, c)
	resp, body := get(t, srv.URL+"/api/ETHUSDT/snapshot")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var snapshot Snapshot
	require.NoError(t, json.Unmarshal([]byte(body), &snapshot))
	// after the warmup, the first candle
	assert.Equal(t, []Point{{Time: base.Add(2 * time.Hour).Unix(), Value: 12},
		{Time: base.Add(4 * time.Hour).Unix(), Value: 14}}, snapshot.Indicators[0].Metrics[0].Points)
}

func TestChart_Notifications(t *testing.T) {
	c := newTestChart(t)
	events := c.events.subscribe()
	defer c.events.unsubscribe(events)

	c.Notify("[PROFIT] 12 USDT")
	event := <-events
	assert.Equal(t, "notification", event.name)
	assert.JSONEq(t, `{"message":"[PROFIT] 12 USDT","error":false}`, string(event.data))

	c.OnError(&exchange.OrderError{Err: errors.New("insufficient funds"), Pair: "BTCUSDT", Quantity: 0.5})
	event = <-events
	var notification NotificationEvent
	require.NoError(t, json.Unmarshal(event.data, &notification))
	assert.True(t, notification.Error)
	assert.Equal(t, "Pair: BTCUSDT\nQuantity: 0.5000\n-----\ninsufficient funds", notification.Message)

	c.OnError(errors.New("boom"))
	event = <-events
	require.NoError(t, json.Unmarshal(event.data, &notification))
	assert.Equal(t, NotificationEvent{Message: "boom", Error: true}, notification)
}

// The candle in progress is kept and sent as it changes, so the chart follows
// the market between the closes; its close replaces it.
func TestChart_PartialCandle(t *testing.T) {
	c := newTestChart(t)
	base := time.Unix(1_700_000_000, 0).UTC()
	c.OnCandle(candleAt("ETHUSDT", base, 3000))
	events := c.events.subscribe()
	defer c.events.unsubscribe(events)

	partial := candleAt("ETHUSDT", base.Add(time.Minute), 3005)
	partial.Complete = false
	c.OnCandle(partial)
	c.OnCandle(partial) // unchanged: not sent again
	partial.Close = 3007
	c.OnCandle(partial)

	for _, want := range []float64{3005, 3007} {
		event := <-events
		require.Equal(t, "candle", event.name)
		var got CandleEvent
		require.NoError(t, json.Unmarshal(event.data, &got))
		assert.True(t, got.Partial)
		assert.Equal(t, want, got.Candle.Close)
	}
	require.Empty(t, events)
	require.Len(t, c.candles["ETHUSDT"], 1, "not a candle of the history")

	snapshot := c.snapshot("ETHUSDT")
	require.Len(t, snapshot.Candles, 2)
	assert.Equal(t, 3007.0, snapshot.Candles[1].Close, "the candle in progress, last")

	closed := candleAt("ETHUSDT", base.Add(time.Minute), 3008)
	c.OnCandle(closed)
	event := <-events
	var got CandleEvent
	require.NoError(t, json.Unmarshal(event.data, &got))
	assert.False(t, got.Partial, "the close")
	snapshot = c.snapshot("ETHUSDT")
	require.Len(t, snapshot.Candles, 2)
	assert.Equal(t, 3008.0, snapshot.Candles[1].Close)

	stale := candleAt("ETHUSDT", base, 1) // before the last close
	stale.Complete = false
	c.OnCandle(stale)
	require.Empty(t, events)
	require.Len(t, c.snapshot("ETHUSDT").Candles, 2)
}

// levelStrategy draws the levels of its analysis, see LevelsSource.
type levelStrategy struct {
	gapStrategy
	levels []Level
}

func (s levelStrategy) ChartLevels(string) []Level { return s.levels }

func TestChart_Levels(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	c := newTestChart(t, WithStrategyIndicators(levelStrategy{levels: []Level{{Kind: "support", Price: 9},
		{Kind: "resistance", Price: 15}}}))
	events := c.events.subscribe()
	defer c.events.unsubscribe(events)
	c.OnCandle(candleAt("ETHUSDT", base, 10))

	var event CandleEvent
	require.NoError(t, json.Unmarshal((<-events).data, &event))
	assert.Equal(t, []Level{{Kind: "support", Price: 9}, {Kind: "resistance", Price: 15}}, event.Levels)
	assert.Equal(t, event.Levels, c.snapshot("ETHUSDT").Levels)

	// none drawn: an empty list, which clears the lines
	c = newTestChart(t, WithStrategyIndicators(levelStrategy{}))
	c.OnCandle(candleAt("ETHUSDT", base, 10))
	data, err := json.Marshal(c.snapshot("ETHUSDT"))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"levels":[]`)

	// a strategy without levels: null, the lines left to the others
	c = newTestChart(t, WithStrategyIndicators(gapStrategy{}))
	c.OnCandle(candleAt("ETHUSDT", base, 10))
	data, err = json.Marshal(c.snapshot("ETHUSDT"))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"levels":null`)
}

// The open position as the broker reports it goes with the snapshot and every
// candle, the one in progress too: its profit follows the market.
func TestChart_BrokerPosition(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	open := true
	c := newTestChart(t, WithBrokerPositions(func(pair string) (BrokerPosition, bool) {
		return BrokerPosition{Side: "BUY", Quantity: 0.1, Entry: 84001.49, Profit: 4.3, Currency: "BRL"}, open
	}))
	c.OnCandle(candleAt("BTCUSDm", base, 84000))
	events := c.events.subscribe()
	defer c.events.unsubscribe(events)

	partial := candleAt("BTCUSDm", base.Add(time.Minute), 84010)
	partial.Complete = false
	c.OnCandle(partial)
	var event CandleEvent
	require.NoError(t, json.Unmarshal((<-events).data, &event))
	assert.Equal(t, &BrokerPosition{Open: true, Side: "BUY", Quantity: 0.1, Entry: 84001.49, Profit: 4.3,
		Currency: "BRL"}, event.Position)

	open = false
	assert.Equal(t, &BrokerPosition{}, c.snapshot("BTCUSDm").Position, "flat")

	// without a broker: absent, the dashboard computes it
	c = newTestChart(t)
	c.OnCandle(candleAt("BTCUSDm", base, 84000))
	data, err := json.Marshal(c.snapshot("BTCUSDm"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), `"position"`)
}

// fakeReporter books the profits of fakeReporter.booked, by the entry of the
// position closed, in BRL.
type fakeReporter struct {
	booked map[int64]float64 // by the ExchangeID of the entry
	asked  []int64           // the entries asked
}

func (r *fakeReporter) AccountSummary() (service.AccountSummary, bool) {
	return service.AccountSummary{Currency: "BRL", Balance: 990.55, Equity: 1004.1}, true
}

func (r *fakeReporter) ClosedProfit(close, entry model.Order) (float64, bool) {
	r.asked = append(r.asked, entry.ExchangeID)
	profit, ok := r.booked[entry.ExchangeID]
	return profit, ok
}

// The profits of the closes are shown as the account books them, in its
// currency: the core's, in the quote, left aside.
func TestChart_AccountReporter(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	reporter := &fakeReporter{booked: map[int64]float64{200: 77.26}}
	c := newTestChart(t, WithAccountReporter(reporter))
	c.OnCandle(candleAt("BTCUSDm", base, 84000))
	fill := func(id, exchangeID int64, side model.SideType, minute int, profit float64) {
		at := base.Add(time.Duration(minute) * time.Minute)
		c.OnOrder(model.Order{ID: id, ExchangeID: exchangeID, Pair: "BTCUSDm", Side: side, Type: model.OrderTypeMarket,
			Status: model.OrderStatusTypeFilled, Quantity: 0.1, Price: 84000, CreatedAt: at, UpdatedAt: at,
			ProfitValue: profit, Profit: profit / 8400})
	}
	fill(1, 100, model.SideTypeSell, 0, 0)       // a short
	fill(2, 101, model.SideTypeBuy, 10, -14.49)  // its stop: not booked
	fill(3, 200, model.SideTypeBuy, 20, 0)       // a long
	fill(4, 201, model.SideTypeSell, 30, 14.906) // its take profit: booked

	snapshot := getSnapshot(t, c, "BTCUSDm") // the account read first, never holding the chart
	assert.Equal(t, "BRL", snapshot.AccountCurrency)
	byID := map[int64]Order{}
	for _, order := range snapshot.Orders {
		byID[order.ID] = order
	}
	require.NotNil(t, byID[4].AccountProfit)
	assert.Equal(t, 77.26, *byID[4].AccountProfit)
	assert.Equal(t, "BRL", byID[4].AccountCurrency)
	assert.Nil(t, byID[2].AccountProfit, "not in the history: the one of the core only")
	assert.Nil(t, byID[1].AccountProfit, "an entry")
	assert.Nil(t, byID[3].AccountProfit, "an entry")
	assert.ElementsMatch(t, []int64{100, 200}, dedupe(reporter.asked), "each close asked with the entry it closes")
}

func dedupe(values []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// fillOrder notifies c of a fill of pair, at minute of base, the profit the
// core settled it with (0 for an opening).
func fillOrder(c *Chart, base time.Time, pair string, id, exchangeID int64, side model.SideType,
	quantity float64, minute int, profitValue float64) {
	at := base.Add(time.Duration(minute) * time.Minute)
	c.OnOrder(model.Order{ID: id, ExchangeID: exchangeID, Pair: pair, Side: side, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Quantity: quantity, Price: 4160, CreatedAt: at, UpdatedAt: at,
		ProfitValue: profitValue, Profit: profitValue / 12480})
}

// accountProfits returns the account profits of the orders of the snapshot of
// pair, by ID.
func accountProfits(t *testing.T, c *Chart, pair string) map[int64]*float64 {
	t.Helper()
	profits := map[int64]*float64{}
	for _, order := range getSnapshot(t, c, pair).Orders {
		profits[order.ID] = order.AccountProfit
	}
	return profits
}

// A position closed outside the bot (by hand, by a stop out) leaves no fill:
// the core discards it, and its fills mustn't take the closes of the next
// positions, which the account books against their own entry: their profits
// shown as the account books them, in its currency, as the other ones (eg:
// the XAUUSDm of the Fibonacci agent, its stop against the second entry).
func TestChart_AccountProfitsAfterAPositionGoneOutsideTheBot(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()

	// a long gone, then a long and its stop loss
	reporter := &fakeReporter{booked: map[int64]float64{700: -251.67}}
	c := newTestChart(t, WithAccountReporter(reporter))
	c.OnCandle(candleAt("XAUUSDm", base, 4160))
	fillOrder(c, base, "XAUUSDm", 6001, 600, model.SideTypeBuy, 3, 0, 0)       // gone: closed outside the bot
	fillOrder(c, base, "XAUUSDm", 7004, 700, model.SideTypeBuy, 3, 75, 0)      // a long anew
	fillOrder(c, base, "XAUUSDm", 7006, 706, model.SideTypeSell, 3, 82, -50.0) // its stop loss
	profits := accountProfits(t, c, "XAUUSDm")
	require.NotNil(t, profits[7006], "booked against its own entry")
	assert.Equal(t, -251.67, *profits[7006])

	// a short gone, then a short closed at the market
	reporter = &fakeReporter{booked: map[int64]float64{420: -42.34}}
	c = newTestChart(t, WithAccountReporter(reporter))
	c.OnCandle(candleAt("XAUUSDm", base, 4160))
	fillOrder(c, base, "XAUUSDm", 41001, 410, model.SideTypeSell, 1, 0, 0)
	fillOrder(c, base, "XAUUSDm", 42001, 420, model.SideTypeSell, 1, 240, 0)
	fillOrder(c, base, "XAUUSDm", 42003, 423, model.SideTypeBuy, 1, 255, -8.4)
	profits = accountProfits(t, c, "XAUUSDm")
	require.NotNil(t, profits[42003])
	assert.Equal(t, -42.34, *profits[42003])

	// a long gone, then a short: an opening for the core (no profit), its
	// close booked against it, not taken for an opening
	reporter = &fakeReporter{booked: map[int64]float64{800: 12.5, 900: -3.1}}
	c = newTestChart(t, WithAccountReporter(reporter))
	c.OnCandle(candleAt("XAUUSDm", base, 4160))
	fillOrder(c, base, "XAUUSDm", 6001, 600, model.SideTypeBuy, 3, 0, 0)       // gone
	fillOrder(c, base, "XAUUSDm", 8000, 800, model.SideTypeSell, 2, 30, 0)     // a short
	fillOrder(c, base, "XAUUSDm", 8002, 802, model.SideTypeBuy, 2, 40, 2.4)    // its close
	fillOrder(c, base, "XAUUSDm", 9000, 900, model.SideTypeBuy, 1, 50, 0)      // a long
	fillOrder(c, base, "XAUUSDm", 9001, 901, model.SideTypeSell, 1, 60, -0.62) // its close
	profits = accountProfits(t, c, "XAUUSDm")
	require.NotNil(t, profits[8002])
	assert.Equal(t, 12.5, *profits[8002])
	require.NotNil(t, profits[9001])
	assert.Equal(t, -3.1, *profits[9001])
	assert.Nil(t, profits[8000], "an entry")
}

// The closes are paired with the fills that opened their position as the core
// settles them: from the oldest without a booking that tells otherwise, a
// partial close leaving the rest open, a close beyond the position reversing
// it, the next close against the reversal.
func TestChart_CloseEntries(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	c := newTestChart(t)
	c.OnCandle(candleAt("BTCUSDm", base, 84000))
	fillOrder(c, base, "BTCUSDm", 1, 10, model.SideTypeBuy, 2, 0, 0)   // A long 2
	fillOrder(c, base, "BTCUSDm", 2, 20, model.SideTypeBuy, 1, 1, 0)   // B adds 1
	fillOrder(c, base, "BTCUSDm", 3, 30, model.SideTypeSell, 1, 2, 5)  // closes 1 of A
	fillOrder(c, base, "BTCUSDm", 4, 40, model.SideTypeSell, 4, 3, 7)  // closes A and B, reverses 2
	fillOrder(c, base, "BTCUSDm", 5, 50, model.SideTypeBuy, 2, 4, -1)  // closes the reversal
	fillOrder(c, base, "BTCUSDm", 6, 60, model.SideTypeSell, 1, 5, 0)  // a short anew
	fillOrder(c, base, "BTCUSDm", 7, 70, model.SideTypeBuy, 1, 6, 0.5) // its close

	c.mu.Lock()
	entries := c.closeEntries("BTCUSDm")
	c.mu.Unlock()
	ids := map[int64]int64{}
	for close, entry := range entries {
		ids[close] = entry.ID
	}
	assert.Equal(t, map[int64]int64{3: 1, 4: 1, 5: 4, 7: 6}, ids)
}

// pointedReporter is the reporter of an account valuing a move of 1 of the
// price of a unit at point in its currency, as MT5 on a BRL account.
type pointedReporter struct {
	fakeReporter
	point float64
}

func (r *pointedReporter) PointValue(string) (float64, error) { return r.point, nil }

// The snapshot of a pair tells the money of the account: its currency and the
// point value of the pair; a paper wallet tells its money simulated.
func TestChart_SnapshotMoney(t *testing.T) {
	base := time.Date(2021, 9, 26, 20, 0, 0, 0, time.UTC)
	c := newTestChart(t, WithAccountReporter(&pointedReporter{point: 5.18}))
	c.OnCandle(candleAt("BTCUSDT", base, 20000))
	snapshot, ok := c.snapshotOf("BTCUSDT")
	require.True(t, ok)
	require.Equal(t, "BRL", snapshot.AccountCurrency)
	require.Equal(t, 5.18, snapshot.PointValue)
	require.False(t, snapshot.Simulated)

	paper := newTestChart(t, WithPaperWallet(exchange.NewPaperWallet(context.Background(), "USDT",
		exchange.WithPaperAsset("USDT", 1000))))
	paper.OnCandle(candleAt("BTCUSDT", base, 20000))
	snapshot, ok = paper.snapshotOf("BTCUSDT")
	require.True(t, ok)
	require.True(t, snapshot.Simulated)
	require.Zero(t, snapshot.PointValue)
	require.Empty(t, snapshot.AccountCurrency)
}
