package llm

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

// windowProvider answers by the count of its requests, keeping each context
// it is asked with as written to the model: enters now and then, exits a
// position held a while.
type windowProvider struct {
	mu     sync.Mutex
	inputs []string
}

func (p *windowProvider) Analyze(_ context.Context, in Input) (Decision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	written, err := json.Marshal(in)
	if err != nil {
		return Decision{}, err
	}
	p.inputs = append(p.inputs, string(written))
	switch n := len(p.inputs); {
	case in.Position == nil && n%9 == 0:
		price := float64(in.Price)
		return Decision{Action: ActionEnterLong, Confidence: 0.9, Entry: ptr(price), StopLoss: ptr(price - 3),
			TakeProfit: ptr(price + 6), Reason: "window"}, nil
	case in.Position != nil && n%5 == 0:
		return Decision{Action: ActionExit, Confidence: 0.9, Reason: "window"}, nil
	}
	return Decision{Action: ActionWait, Confidence: 0.5, Reason: "window"}, nil
}

// wholeHistory runs a Trader as the strategy controller did before it kept a
// few warmups of candles only: on the whole history, its warmup handed at the
// closes, all of it on the ticks.
type wholeHistory struct {
	trader  *Trader
	broker  service.Broker
	df      *model.Dataframe
	started bool
}

func (w *wholeHistory) update(c model.Candle) {
	df := w.df
	if n := len(df.Time); n > 0 && c.Time.Equal(df.Time[n-1]) {
		df.Close[n-1], df.Open[n-1], df.High[n-1], df.Low[n-1], df.Volume[n-1] = c.Close, c.Open, c.High, c.Low,
			c.Volume
		return
	}
	df.Close, df.Open, df.High = append(df.Close, c.Close), append(df.Open, c.Open), append(df.High, c.High)
	df.Low, df.Volume, df.Time = append(df.Low, c.Low), append(df.Volume, c.Volume), append(df.Time, c.Time)
	df.LastUpdate = c.Time
}

func (w *wholeHistory) OnCandle(c model.Candle) {
	w.update(c)
	if warmup := w.trader.WarmupPeriod(); len(w.df.Close) >= warmup {
		sample := w.df.Sample(warmup)
		w.trader.Indicators(&sample)
		if w.started {
			w.trader.OnCandle(&sample, w.broker)
		}
	}
}

func (w *wholeHistory) OnPartialCandle(c model.Candle) {
	if len(w.df.Close) >= w.trader.WarmupPeriod() {
		w.update(c)
		w.trader.Indicators(w.df)
		w.trader.OnPartialCandle(w.df, w.broker)
	}
}

// sameNumbers reports whether a and b, contexts written to the model, are the
// same but for the last digit written of their numbers.
func sameNumbers(a, b any) bool {
	switch a := a.(type) {
	case map[string]any:
		b, ok := b.(map[string]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for key, value := range a {
			if !sameNumbers(value, b[key]) {
				return false
			}
		}
		return true
	case []any:
		b, ok := b.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !sameNumbers(a[i], b[i]) {
				return false
			}
		}
		return true
	case float64:
		b, ok := b.(float64)
		return ok && math.Abs(a-b) <= 1e-6*math.Max(math.Max(math.Abs(a), math.Abs(b)), 1)
	}
	return a == b
}

// M7: the strategy controller keeps a few warmups of candles (see
// strategy.Controller): the Trader, run by it on the closes and the ticks of a
// long run, its context read anew at each analysis of a candle in progress
// (ConsultInterval), is asked what it would be asked on the whole history:
// the same contexts, but for the last digit written of a few numbers (the sums
// of talib in another order), and so the same decisions and orders.
func TestTrader_ControllerAsksAsOnTheWholeHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("long: left out by -short")
	}
	ctx := context.Background()
	var now time.Time
	newTrader := func() (*Trader, *windowProvider, *exchange.PaperWallet) {
		cfg := DefaultConfig()
		cfg.Live = true
		cfg.ConsultInterval = 5 * time.Minute
		cfg.PositionSize = map[string]float64{testPair: 1}
		cfg.MaxEntryATR, cfg.MaxLevelATR = 0, 0
		cfg.Candles, cfg.StructureCandles = 50, 60
		provider := &windowProvider{}
		trader, err := New(cfg, provider)
		require.NoError(t, err)
		trader.now = func() time.Time { return now }
		return trader, provider, exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 100000))
	}
	trader, provider, wallet := newTrader()
	controller := strategy.NewStrategyController(testPair, trader, wallet)
	refTrader, refProvider, refWallet := newTrader()
	reference := &wholeHistory{trader: refTrader, broker: refWallet,
		df: &model.Dataframe{Pair: testPair, Metadata: map[string]model.Series[float64]{}}}
	warmup := trader.WarmupPeriod()

	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	price := func(x float64) float64 { return 100 + 6*math.Sin(x/17) + 2*math.Sin(x/5) + x*0.003 }
	candle := func(i int, until float64, complete bool) model.Candle {
		open, close := price(float64(i)), price(float64(i)+until)
		return model.Candle{Pair: testPair, Time: base.Add(time.Duration(i) * 15 * time.Minute), Open: open,
			Close: close, High: math.Max(open, close) + 0.3, Low: math.Min(open, close) - 0.3,
			Volume: 100 + float64(i%13), Complete: complete}
	}
	closeCandle := func(c model.Candle) {
		now = c.Time.Add(15*time.Minute + time.Second)
		wallet.OnCandle(c)
		refWallet.OnCandle(c)
		require.NoError(t, controller.OnCandle(c))
		reference.OnCandle(c)
	}
	for i := range warmup {
		closeCandle(candle(i, 1, true))
	}
	controller.Start()
	reference.started = true

	candles := 5 * warmup // far past the candles the controller keeps
	for i := warmup; i < candles; i++ {
		for _, minute := range []int{1, 6, 11} { // the slots of the consult interval
			c := candle(i, float64(minute)/15, false)
			now = c.Time.Add(time.Duration(minute)*time.Minute + time.Second)
			wallet.OnCandle(c)
			refWallet.OnCandle(c)
			require.NoError(t, controller.OnPartialCandle(c))
			reference.OnPartialCandle(c)
			for _, tr := range []*Trader{trader, refTrader} {
				if s, ok := tr.starting[testPair]; ok {
					<-s.done // the decision of the start, applied on the next tick
				}
			}
		}
		closeCandle(candle(i, 1, true))
	}

	require.Len(t, provider.inputs, len(refProvider.inputs))
	// the 3 slots of each candle, the one of its open on its first tick, but
	// for the one the decision of the start is applied in, taken by it
	require.GreaterOrEqual(t, len(provider.inputs), 3*(candles-warmup)-1, "the slots asked")
	identical := 0
	for i := range provider.inputs {
		if provider.inputs[i] == refProvider.inputs[i] {
			identical++
			continue
		}
		var got, want any
		require.NoError(t, json.Unmarshal([]byte(provider.inputs[i]), &got))
		require.NoError(t, json.Unmarshal([]byte(refProvider.inputs[i]), &want))
		require.True(t, sameNumbers(got, want), "request %d:\n%s\n%s", i, provider.inputs[i], refProvider.inputs[i])
	}
	require.Greater(t, identical, len(provider.inputs)*99/100, "the contexts written the same")

	ordersOf := func(wallet *exchange.PaperWallet) []model.Order {
		var orders []model.Order
		for id := int64(1); ; id++ {
			order, err := wallet.Order(testPair, id)
			if err != nil {
				return orders
			}
			order.CreatedAt, order.UpdatedAt = time.Time{}, time.Time{}
			orders = append(orders, order)
		}
	}
	orders := ordersOf(wallet)
	require.NotEmpty(t, orders, "the trades decided")
	require.Equal(t, ordersOf(refWallet), orders)
}
