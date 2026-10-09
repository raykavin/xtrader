package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/tools/log"
)

const testPair = "BTCUSDT"

// answer is an answer of the fakeProvider: a decision, or an error.
type answer struct {
	decision Decision
	err      error
}

// fakeProvider answers its answers in order, without validating them, as a
// provider plugged in may not, and records the contexts it is asked.
type fakeProvider struct {
	t        *testing.T
	answers  []answer
	inputs   []Input
	systems  []string      // the instructions of each request, empty for its own
	deadline time.Duration // left to the deadline of the last context
	asked    func()        // called with each request, if set
}

func (f *fakeProvider) Analyze(ctx context.Context, in Input) (Decision, error) {
	return f.AnalyzeWith(ctx, "", in)
}

func (f *fakeProvider) AnalyzeWith(ctx context.Context, system string, in Input) (Decision, error) {
	f.inputs = append(f.inputs, in)
	f.systems = append(f.systems, system)
	if f.asked != nil {
		f.asked()
	}
	if deadline, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(deadline)
	}
	if len(f.answers) == 0 {
		f.t.Errorf("decision of %s asked, without answer", in.Symbol)
		return Decision{}, errors.New("no answer")
	}
	next := f.answers[0]
	f.answers = f.answers[1:]
	return next.decision, next.err
}

func (f *fakeProvider) String() string {
	return "fake test-model"
}

// last returns the last context asked.
func (f *fakeProvider) last() Input {
	f.t.Helper()
	require.NotEmpty(f.t, f.inputs, "no decision asked")
	return f.inputs[len(f.inputs)-1]
}

// traderRunner feeds candles to a Trader trading the paper wallet, its
// indicators computed as the controller does, the warmup candles first.
type traderRunner struct {
	t        *testing.T
	trader   *Trader
	provider *fakeProvider
	wallet   *exchange.PaperWallet
	broker   service.Broker // the wallet, unless a test wraps it
	df       *model.Dataframe
	meta     map[string]float64 // Metadata of the next candles
}

// newTraderRunner creates a Trader of 10 BTC positions, 50 candles in the
// context, and its runner, the wallet holding 10000 USDT and holding BTC.
func newTraderRunner(t *testing.T, configure func(*Config), holding float64) *traderRunner {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Candles = 50
	cfg.PositionSize = map[string]float64{testPair: 10}
	if configure != nil {
		configure(&cfg)
	}
	if cfg.Live && cfg.ConsultInterval == 0 {
		cfg.ConsultInterval = 15 * time.Minute // required live: a slot at each close of the candles of the tests
	}
	provider := &fakeProvider{t: t}
	trader, err := New(cfg, provider)
	require.NoError(t, err)

	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000),
		exchange.WithPaperAsset("BTC", holding))
	r := &traderRunner{t: t, trader: trader, provider: provider, wallet: wallet, broker: wallet,
		df: &model.Dataframe{Pair: testPair, Metadata: map[string]model.Series[float64]{}}}
	for i := 0; i < trader.WarmupPeriod(); i++ {
		r.add(100 + 2*math.Sin(float64(i)/8))
	}
	return r
}

// add adds a candle closing at close, its open the previous close.
func (r *traderRunner) add(close float64) {
	df := r.df
	open := close
	if len(df.Close) > 0 {
		open = df.Close.Last(0)
	}
	at := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC).Add(time.Duration(len(df.Close)) * 15 * time.Minute)
	candle := model.Candle{Pair: testPair, Time: at, Open: open, Close: close, High: math.Max(open, close) + 0.2,
		Low: math.Min(open, close) - 0.2, Volume: 100 + float64(len(df.Close)%10), Complete: true}
	r.wallet.OnCandle(candle)

	df.Time = append(df.Time, at)
	df.Open = append(df.Open, candle.Open)
	df.Close = append(df.Close, candle.Close)
	df.High = append(df.High, candle.High)
	df.Low = append(df.Low, candle.Low)
	df.Volume = append(df.Volume, candle.Volume)
	for key, value := range r.meta {
		series := df.Metadata[key]
		for len(series) < len(df.Close)-1 {
			series = append(series, math.NaN())
		}
		df.Metadata[key] = append(series, value)
	}
}

// step closes a candle at close, the provider answering answers, all of them
// asked: in a backtest at its close; live, where the model is never asked at
// a close, on the first tick of the next candle, see slotTick.
func (r *traderRunner) step(close float64, answers ...answer) {
	r.t.Helper()
	r.add(close)
	r.provider.answers = answers
	r.trader.Indicators(r.df)
	r.trader.OnCandle(r.df, r.broker)
	if r.trader.cfg.Live {
		r.slotTick()
	}
	require.Empty(r.t, r.provider.answers, "answers not asked")
}

// slotTick hands to OnPartialCandle the first tick of the candle after the
// last one closed, a second after its open, at the close: live, the slot of
// the consult interval on that close is asked on it. The candle in progress
// is taken off after, the clock of the Trader as it was.
func (r *traderRunner) slotTick() {
	r.t.Helper()
	df := r.df
	n := len(df.Close)
	at := df.Time[n-1].Add(15 * time.Minute)
	price := df.Close.Last(0)
	now := r.trader.now
	r.trader.now = func() time.Time { return at.Add(time.Second) }
	defer func() { r.trader.now = now }()
	df.Time = append(df.Time, at)
	df.Open = append(df.Open, price)
	df.Close = append(df.Close, price)
	df.High = append(df.High, price)
	df.Low = append(df.Low, price)
	df.Volume = append(df.Volume, 1)
	r.trader.Indicators(df)
	r.trader.OnPartialCandle(df, r.broker)
	df.Time, df.Open, df.Close = df.Time[:n], df.Open[:n], df.Close[:n]
	df.High, df.Low, df.Volume = df.High[:n], df.Low[:n], df.Volume[:n]
	for key, series := range df.Metadata {
		if len(series) > n {
			df.Metadata[key] = series[:n]
		}
	}
}

// startAsked waits for the model asked about the start of pair, apart (see
// startApart): its decision is applied on the next candle of the pair.
func (r *traderRunner) startAsked(pair string) {
	r.t.Helper()
	s, ok := r.trader.starting[pair]
	require.True(r.t, ok, "the start of %s not asked apart", pair)
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		r.t.Fatalf("the model still asked about the start of %s", pair)
	}
}

// position returns the asset held.
func (r *traderRunner) position() float64 {
	r.t.Helper()
	asset, _, err := r.wallet.Position(testPair)
	require.NoError(r.t, err)
	return asset
}

// legs returns the legs of the position followed, as the wallet holds them:
// the take profit, then the stop loss.
func (r *traderRunner) legs() (profit, stop model.Order) {
	r.t.Helper()
	tr, ok := r.trader.trades[testPair]
	require.True(r.t, ok, "no position followed")
	require.Len(r.t, tr.legs, 2)
	var err error
	profit, err = r.wallet.Order(testPair, tr.legs[tr.leg(false)].ExchangeID)
	require.NoError(r.t, err)
	stop, err = r.wallet.Order(testPair, tr.legs[tr.leg(true)].ExchangeID)
	require.NoError(r.t, err)
	return profit, stop
}

func decide(action Action, confidence float64, entry, stop, profit *float64) answer {
	return answer{decision: Decision{Action: action, Confidence: confidence, Entry: entry, StopLoss: stop,
		TakeProfit: profit, Reason: "test"}}
}

func enterLong(price float64) answer {
	return decide(ActionEnterLong, 0.8, ptr(price), ptr(price-2), ptr(price+4))
}

func enterShort(price float64) answer {
	return decide(ActionEnterShort, 0.8, ptr(price), ptr(price+2), ptr(price-4))
}

func TestNew_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Config)
		want      string
	}{
		{"no timeframe", func(c *Config) { c.Timeframe = "" }, "timeframe is required"},
		{"no candles", func(c *Config) { c.Candles = 0 }, "the candles of the context"},
		{"warmup", func(c *Config) { c.Warmup = -1 }, "the warmup can't be negative"},
		{"no history", func(c *Config) { c.History = 0 }, "the history of the indicators"},
		{"history over the candles", func(c *Config) { c.Candles, c.History = 3, 4 }, "at most the candles"},
		{"period", func(c *Config) { c.RSILen = 0 }, "periods must be at least 1"},
		{"EMAs", func(c *Config) { c.EMAFastLen = 21 }, "the fast EMA must be shorter"},
		{"MACD", func(c *Config) { c.MACDFast = 26 }, "the fast MACD average must be shorter"},
		{"bands", func(c *Config) { c.BBDev = 0 }, "deviation must be positive"},
		{"confidence", func(c *Config) { c.MinConfidence = 1.1 }, "min confidence must be from 0 to 1"},
		{"distances", func(c *Config) { c.MaxLevelATR = -1 }, "can't be negative"},
		{"language", func(c *Config) { c.Language = " " }, "the language of the reasons is required"},
		{"timeout", func(c *Config) { c.Timeout = 0 }, "timeout must be positive"},
		{"min value", func(c *Config) { c.MinPositionValue = 0 }, "min position value must be positive"},
		{"fraction", func(c *Config) { c.PositionFraction = 1.5 }, "position fraction"},
		{"leverage", func(c *Config) { c.Leverage = 0.5 }, "leverage must be at least 1"},
		{"whole balance leveraged", func(c *Config) { c.Leverage = 20 }, "the position fraction must be below 1"},
		{"size", func(c *Config) { c.PositionSize = map[string]float64{testPair: -1} }, "must be positive"},
		{"direction", func(c *Config) { c.Direction = "up" }, `invalid direction: "up"`},
		{"take profit optional and none", func(c *Config) { c.TakeProfitOptional, c.NoTakeProfit = true, true },
			"both optional and none"},
		{"risk:reward without take profit", func(c *Config) { c.NoTakeProfit, c.MinRiskReward = true, 1.5 },
			"a min risk:reward needs a take profit"},
		{"higher trend without timeframes", func(c *Config) {
			c.HigherTrendRequired, c.HigherTimeframes = true, []string{}
		}, "the trend of the higher timeframes is required for an entry, without any above 15m"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.configure(&cfg)
			_, err := New(cfg, &fakeProvider{t: t})
			require.ErrorContains(t, err, tc.want)
		})
	}

	_, err := New(DefaultConfig(), nil)
	require.ErrorContains(t, err, "a provider is required")
}

func TestTrader_WarmupPeriod(t *testing.T) {
	trader, err := New(DefaultConfig(), &fakeProvider{t: t})
	require.NoError(t, err)
	require.Equal(t, "15m", trader.Timeframe())
	// the 201 candles of the SMA 200, then the last 5 values of the indicators
	require.Equal(t, 201, trader.state.Load().lookback)
	require.Equal(t, 206, trader.WarmupPeriod())

	cfg := DefaultConfig()
	cfg.Candles = 500
	trader, err = New(cfg, &fakeProvider{t: t})
	require.NoError(t, err)
	require.Equal(t, 500, trader.WarmupPeriod(), "the window of the model")

	cfg.Warmup = 800
	trader, err = New(cfg, &fakeProvider{t: t})
	require.NoError(t, err)
	require.Equal(t, 800, trader.WarmupPeriod(), "the room of the prompts")
	cfg.Warmup = 100
	trader, err = New(cfg, &fakeProvider{t: t})
	require.NoError(t, err)
	require.Equal(t, 500, trader.WarmupPeriod(), "what its prompt needs at least")
}

func TestTrader_Indicators(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	r.trader.Indicators(r.df)
	for _, s := range r.trader.series {
		values := r.df.Metadata[s.key]
		require.Len(t, values, len(r.df.Close), s.name)
		require.False(t, math.IsNaN(values.Last(0)), s.name)
	}
	charts := r.trader.Indicators(r.df)
	require.Len(t, charts, 6, "all the indicators sent")
	require.True(t, charts[0].Overlay)
	require.Len(t, charts[0].Metrics, 6, "the EMAs, the SMA and the bands over the prices")

	// the chart may hold fewer candles than the warmup, which talib can't take
	lookback := r.trader.state.Load().lookback
	for _, n := range []int{0, 1, lookback - 1, lookback} {
		df := &model.Dataframe{Pair: testPair, Metadata: map[string]model.Series[float64]{},
			Close: r.df.Close[:n], Open: r.df.Open[:n], High: r.df.High[:n], Low: r.df.Low[:n],
			Volume: r.df.Volume[:n], Time: r.df.Time[:n]}
		require.NotPanics(t, func() { r.trader.Indicators(df) }, "%d candles", n)
		require.Len(t, df.Metadata["llm_sma"], n)
	}
}

func TestTrader_Wait(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	r.step(100, decide(ActionWait, 0.42, nil, nil, nil))
	require.Zero(t, r.position())
	require.Empty(t, r.trader.trades)

	// the context of a flat pair
	in := r.provider.last()
	require.Equal(t, testPair, in.Symbol)
	require.Equal(t, "15m", in.Timeframe)
	require.Equal(t, r.df.Time[len(r.df.Time)-1], in.Time)
	require.Equal(t, number(100), in.Price)
	require.Nil(t, in.Position)
	require.Equal(t, number(10), in.EntrySize)
	require.Equal(t, number(10000), in.Balance)
	require.Equal(t, []Action{ActionWait, ActionEnterLong, ActionEnterShort}, in.Rules.Actions)
	require.Equal(t, number(0.6), in.Rules.MinConfidence)
	require.Equal(t, "pt-BR", in.Rules.ReasonLanguage)
	require.False(t, in.Rules.StopCanWiden)

	// the last 50 candles, the last one just closed
	require.Len(t, in.Candles, 50)
	require.Equal(t, r.df.Time[len(r.df.Time)-50], in.Candles[0].Time)
	require.Equal(t, 100.0, in.Candles[49].Close)
	require.Equal(t, r.df.High[len(r.df.High)-1], in.Candles[49].High)

	// the last 5 values of each indicator, the distances in ATR of the rules
	require.Len(t, in.Indicators, len(r.trader.series))
	for _, name := range []string{"ema_9", "ema_21", "sma_200", "rsi_14", "atr_14", "adx_14", "plus_di_14",
		"minus_di_14", "macd_12_26", "macd_signal_9", "macd_histogram", "bb_upper_20_2", "bb_middle_20",
		"bb_lower_20_2", "volume_sma_20"} {
		require.Len(t, in.Indicators[name], 5, name)
	}
	atr := float64(in.Indicators["atr_14"][4])
	require.Equal(t, r.df.Metadata["llm_atr"].Last(0), atr)
	require.InDelta(t, 0.5*atr, float64(in.Rules.MaxEntryDistance), 1e-9)
	require.InDelta(t, 10*atr, float64(in.Rules.MaxLevelDistance), 1e-9)
	require.InDelta(t, atr, float64(in.Summary.ATRPct), 1e-9, "the ATR in % of 100")
	require.Contains(t, []string{"up", "down", "mixed"}, in.Summary.Trend)

	// without bid nor ask from the exchange
	require.True(t, math.IsNaN(float64(in.Bid)))
	require.True(t, math.IsNaN(float64(in.Ask)))
	require.True(t, math.IsNaN(float64(in.Spread)))

	_, err := json.Marshal(in)
	require.NoError(t, err)
	require.LessOrEqual(t, r.provider.deadline, time.Minute, "bounded by the timeout")
	require.Positive(t, r.provider.deadline)
}

func TestTrader_EnterLong(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	r.step(100, enterLong(100))
	require.InDelta(t, 10, r.position(), 1e-9)

	// the stop loss and take profit of the decision, at the broker
	profit, stop := r.legs()
	require.Equal(t, model.OrderTypeLimitMaker, profit.Type)
	require.InDelta(t, 104, profit.Price, 1e-9)
	require.Equal(t, model.OrderTypeStopLoss, stop.Type)
	require.InDelta(t, 98, *stop.Stop, 1e-9)
	for _, leg := range []model.Order{profit, stop} {
		require.Equal(t, model.SideTypeSell, leg.Side)
		require.Equal(t, model.OrderStatusTypeNew, leg.Status)
		require.InDelta(t, 10, leg.Quantity, 1e-9)
	}
	require.InDelta(t, 100, r.trader.trades[testPair].entry, 1e-9)
}

func TestTrader_EnterShort(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	r.step(100, enterShort(100))
	require.InDelta(t, -10, r.position(), 1e-9)

	profit, stop := r.legs()
	require.InDelta(t, 96, profit.Price, 1e-9)
	require.InDelta(t, 102, *stop.Stop, 1e-9)
	require.Equal(t, model.SideTypeBuy, profit.Side)
	require.Equal(t, model.SideTypeBuy, stop.Side)
}

func TestTrader_Hold(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	r.step(100, enterLong(100))
	legs := append([]model.Order(nil), r.trader.trades[testPair].legs...)

	r.step(101, decide(ActionHold, 0.76, nil, ptr(98), ptr(104)))
	require.InDelta(t, 10, r.position(), 1e-9)
	require.Equal(t, legs, r.trader.trades[testPair].legs, "the legs left where they are")
	profit, stop := r.legs()
	require.InDelta(t, 104, profit.Price, 1e-9)
	require.InDelta(t, 98, *stop.Stop, 1e-9)

	// the context of the position held
	in := r.provider.last()
	require.Equal(t, &Position{Side: model.SideTypeBuy, Size: 10, Entry: 100, StopLoss: 98, TakeProfit: 104,
		PnL: 10, Candles: 1, InitialStop: 98, RMultiple: 0.5}, in.Position, "1 of the 2 risked: 0.5 R")
	require.Equal(t, []Action{ActionHold, ActionExit, ActionModifySL, ActionModifyTP}, in.Rules.Actions)
	require.True(t, math.IsNaN(float64(in.EntrySize)))

	r.step(100.5, decide(ActionHold, 0.7, nil, nil, nil))
	require.Equal(t, 2, r.provider.last().Position.Candles)
	require.Equal(t, number(5), r.provider.last().Position.PnL)
}

func TestTrader_Exit(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	r.step(100, enterLong(100))
	legs := r.trader.trades[testPair].legs

	r.step(101, decide(ActionExit, 0.9, nil, nil, nil))
	require.Zero(t, r.position())
	require.Empty(t, r.trader.trades)
	for _, leg := range legs {
		got, err := r.wallet.Order(testPair, leg.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, got.Status, leg.Type)
	}

	// flat again: the next candle may open the next position
	r.step(101, enterShort(101))
	require.InDelta(t, -10, r.position(), 1e-9)
}

func TestTrader_ModifySL(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	r.step(100, enterLong(100))

	r.step(101, decide(ActionModifySL, 0.8, nil, ptr(99.5), nil))
	profit, stop := r.legs()
	require.InDelta(t, 99.5, *stop.Stop, 1e-9, "tightened")
	require.InDelta(t, 104, profit.Price, 1e-9, "the take profit left where it is")
	require.InDelta(t, 99.5, legLevel(r.trader.trades[testPair].legs[r.trader.trades[testPair].leg(true)]), 1e-9)
	require.Equal(t, number(99.5), func() number {
		r.step(101, decide(ActionHold, 0.8, nil, nil, nil))
		return r.provider.last().Position.StopLoss
	}(), "the next context holds the stop loss moved")

	// widening it adds risk: refused
	r.step(101, decide(ActionModifySL, 0.8, nil, ptr(98.5), nil))
	_, stop = r.legs()
	require.InDelta(t, 99.5, *stop.Stop, 1e-9)

	// unless allowed
	r = newTraderRunner(t, func(c *Config) { c.WidenStop = true }, 0)
	r.step(100, enterLong(100))
	r.step(100, decide(ActionModifySL, 0.8, nil, ptr(97), nil))
	_, stop = r.legs()
	require.InDelta(t, 97, *stop.Stop, 1e-9)
}

func TestTrader_ModifyTP(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	r.step(100, enterShort(100))

	r.step(99, decide(ActionModifyTP, 0.8, nil, nil, ptr(95)))
	profit, stop := r.legs()
	require.InDelta(t, 95, profit.Price, 1e-9)
	require.InDelta(t, 102, *stop.Stop, 1e-9, "the stop loss left where it is")

	// a take profit on the wrong side is refused
	r.step(99, decide(ActionModifyTP, 0.8, nil, nil, ptr(99.5)))
	profit, _ = r.legs()
	require.InDelta(t, 95, profit.Price, 1e-9)
}

func TestTrader_LowConfidence(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	low := enterLong(100)
	low.decision.Confidence = 0.59
	r.step(100, low)
	require.Zero(t, r.position(), "under the min confidence")

	r.step(100, enterLong(100))
	exit := decide(ActionExit, 0.3, nil, nil, nil)
	r.step(100, exit)
	require.InDelta(t, 10, r.position(), 1e-9, "an exit under the min confidence is ignored too")
}

func TestTrader_InvalidDecisions(t *testing.T) {
	// whatever the provider answers, the Trader validates it
	tests := []struct {
		name     string
		holding  float64
		decision answer
	}{
		{"stop loss above the price of a long", 0, decide(ActionEnterLong, 0.9, ptr(100), ptr(101), ptr(104))},
		{"take profit below the price of a long", 0, decide(ActionEnterLong, 0.9, ptr(100), ptr(98), ptr(99))},
		{"entry without stop loss", 0, decide(ActionEnterShort, 0.9, ptr(100), nil, ptr(96))},
		{"entry far from the price", 0, decide(ActionEnterLong, 0.9, ptr(150), ptr(98), ptr(160))},
		{"stop loss beyond the max distance", 0, decide(ActionEnterLong, 0.9, ptr(100), ptr(1), ptr(104))},
		{"action unknown", 0, decide("BUY", 0.9, ptr(100), ptr(98), ptr(104))},
		{"exit when flat", 0, decide(ActionExit, 0.9, nil, nil, nil)},
		{"entry with a position", 10, decide(ActionEnterShort, 0.9, ptr(100), ptr(102), ptr(96))},
		{"move the stop loss of a position without legs", 10, decide(ActionModifySL, 0.9, nil, ptr(99), nil)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTraderRunner(t, nil, tc.holding)
			r.step(100, tc.decision)
			require.InDelta(t, tc.holding, r.position(), 1e-9, "no order")
			if tr, ok := r.trader.trades[testPair]; ok {
				require.Empty(t, tr.legs)
				require.False(t, tr.exiting)
			}
		})
	}
}

func TestTrader_Direction(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.Direction = strategies.RKDirectionBuyOnly }, 0)
	r.step(100, enterShort(100))
	require.Zero(t, r.position(), "buy only")
	require.Equal(t, []Action{ActionWait, ActionEnterLong}, r.provider.last().Rules.Actions)
}

func TestTrader_ProviderErrors(t *testing.T) {
	for _, err := range []error{
		ErrTimeout,
		&APIError{Provider: "openai", Status: 500},
		&APIError{Provider: "anthropic", Status: 401, Type: "authentication_error"},
		ErrInvalidResponse,
		ErrUnavailable,
	} {
		t.Run(err.Error(), func(t *testing.T) {
			r := newTraderRunner(t, nil, 0)
			failure := enterLong(100)
			failure.err = err // a decision along with the error is never traded
			r.step(100, failure)
			require.Zero(t, r.position())
			require.Empty(t, r.trader.trades)
		})
	}
}

func TestTrader_RateLimit(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	r.trader.now = func() time.Time { return now }

	r.step(100, answer{err: &APIError{Provider: "anthropic", Status: 429, RetryAfter: 30 * time.Second}})
	asked := len(r.provider.inputs)

	// no request until the Retry-After has passed
	now = now.Add(20 * time.Second)
	r.step(100)
	require.Len(t, r.provider.inputs, asked)

	now = now.Add(11 * time.Second)
	r.step(100, enterLong(100))
	require.InDelta(t, 10, r.position(), 1e-9)

	// a rate limit without Retry-After pauses nothing: the next candle asks
	r.step(100, answer{err: &APIError{Provider: "openai", Status: 429}})
	r.step(100, decide(ActionHold, 0.8, nil, nil, nil))
}

func TestTrader_NothingToAsk(t *testing.T) {
	t.Run("no size for the pair", func(t *testing.T) {
		r := newTraderRunner(t, func(c *Config) { c.PositionSize = map[string]float64{"ETHUSDT": 1} }, 0)
		r.step(100) // no answer: the fake fails when asked
		require.Empty(t, r.provider.inputs)
	})

	t.Run("a balance too low", func(t *testing.T) {
		r := newTraderRunner(t, func(c *Config) { c.PositionSize = map[string]float64{testPair: 1000} }, 0)
		r.step(100)
		require.Empty(t, r.provider.inputs)
	})

	t.Run("a broker without stop loss and take profit", func(t *testing.T) {
		r := newTraderRunner(t, nil, 0)
		r.broker = plainBroker{r.wallet}
		r.step(100)
		require.Empty(t, r.provider.inputs)
	})
}

// plainBroker is a broker that can't place the stop loss and take profit with
// the order: service.ProtectionBroker is hidden.
type plainBroker struct {
	service.Broker
}

func TestTrader_PositionOfSomeoneElse(t *testing.T) {
	// held before a restart, or opened by someone else: followed without its
	// entry nor its legs
	r := newTraderRunner(t, nil, 10)
	r.step(100, decide(ActionHold, 0.8, nil, nil, nil))
	in := r.provider.last()
	require.Equal(t, model.SideTypeBuy, in.Position.Side)
	require.Equal(t, number(10), in.Position.Size)
	for _, unknown := range []number{in.Position.Entry, in.Position.StopLoss, in.Position.TakeProfit, in.Position.PnL} {
		require.True(t, math.IsNaN(float64(unknown)))
	}
	require.Equal(t, []Action{ActionHold, ActionExit}, in.Rules.Actions, "no leg to move")

	r.step(100, decide(ActionExit, 0.8, nil, nil, nil))
	require.Zero(t, r.position())
}

// failingExits rejects the next orders at the market, eg: the market closed.
type failingExits struct {
	*exchange.PaperWallet
	fails int
}

func (b *failingExits) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	if b.fails > 0 {
		b.fails--
		return model.Order{}, errors.New("market closed")
	}
	return b.PaperWallet.CreateOrderMarket(side, pair, size)
}

func TestTrader_FailedExitIsRetried(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	r.step(100, enterLong(100))

	r.broker = &failingExits{PaperWallet: r.wallet, fails: 2}
	r.step(100, decide(ActionExit, 0.9, nil, nil, nil))
	require.InDelta(t, 10, r.position(), 1e-9)

	// retried on the next candles without asking the model
	r.step(100)
	require.InDelta(t, 10, r.position(), 1e-9)
	r.step(100)
	require.Zero(t, r.position())
	require.Empty(t, r.trader.trades)
}

func TestTrader_Quotes(t *testing.T) {
	// Exness: bid candles, with the ask and the spread in their Metadata
	r := newTraderRunner(t, nil, 0)
	r.meta = map[string]float64{"ask": 100.3, "spread": 0.3, "funding": 0.01}
	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	in := r.provider.last()
	require.InDelta(t, 100, float64(in.Bid), 1e-9)
	require.Equal(t, number(100.3), in.Ask)
	require.Equal(t, number(0.3), in.Spread)
	require.Equal(t, map[string]number{"funding": 0.01}, in.Market, "the other data of the exchange")

	// the spread from the bid and the ask
	r.meta = map[string]float64{"bid": 99.9, "ask": 100.1}
	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	in = r.provider.last()
	require.InDelta(t, 0.2, float64(in.Spread), 1e-9)
}

// scriptedProvider decides with a function of the context.
type scriptedProvider func(Input) Decision

func (f scriptedProvider) Analyze(_ context.Context, in Input) (Decision, error) {
	return f(in), nil
}

func TestTrader_Backtest(t *testing.T) {
	// the bot runs the Trader as any strategy: entries with their stop loss
	// and take profit at the broker, exits at the market
	ctx := context.Background()
	entries := 0
	script := scriptedProvider(func(in Input) Decision {
		price, atr := float64(in.Price), float64(in.Indicators["atr_14"][len(in.Indicators["atr_14"])-1])
		switch {
		case in.Position == nil:
			entries++
			action, sign := ActionEnterLong, 1.0
			if entries%2 == 0 {
				action, sign = ActionEnterShort, -1
			}
			return Decision{Action: action, Confidence: 0.8, Entry: ptr(price), StopLoss: ptr(price - sign*2*atr),
				TakeProfit: ptr(price + sign*3*atr), Reason: "scripted"}
		case in.Position.Candles >= 6:
			return Decision{Action: ActionExit, Confidence: 0.8, Reason: "scripted"}
		}
		return Decision{Action: ActionHold, Confidence: 0.8, Reason: "scripted"}
	})

	cfg := DefaultConfig()
	cfg.Timeframe = "1h"
	cfg.PositionFraction = 0.5
	trader, err := New(cfg, script)
	require.NoError(t, err)

	csvFeed, err := exchange.NewCSVFeed(trader.Timeframe(), exchange.PairFeed{
		Pair:      testPair,
		File:      "../../testdata/btc-1h.csv",
		Timeframe: "1h",
	})
	require.NoError(t, err)
	store, err := storage.FromMemory()
	require.NoError(t, err)
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 10000),
		exchange.WithPaperFee(0.001, 0.001), exchange.WithDataFeed(csvFeed))

	bot, err := xtrader.NewBot(ctx, xtrader.Settings{Pairs: []string{testPair}}, wallet, trader,
		xtrader.WithBacktest(wallet), xtrader.WithStorage(store), xtrader.WithLogLevel(log.ErrorLevel))
	require.NoError(t, err)
	require.NoError(t, bot.Run(ctx))

	orders, err := store.Orders()
	require.NoError(t, err)
	var market, legs, legsFilled int
	for _, order := range orders {
		switch order.Type {
		case model.OrderTypeMarket:
			market++
		case model.OrderTypeLimitMaker, model.OrderTypeStopLoss:
			legs++
			if order.Status == model.OrderStatusTypeFilled {
				legsFilled++
			}
		}
	}

	// each trade: a market entry with its two legs, then one exit, a leg
	// filled or a market order on EXIT, the other leg canceled
	require.Zero(t, legs%2)
	trades := legs / 2
	exitsByMarket := market - trades
	open := 0
	if asset, _, err := wallet.Position(testPair); err == nil && math.Abs(asset) > 1e-9 {
		open = 1
	}
	require.Positive(t, trades)
	require.Equal(t, entries, trades, "every entry decided was placed")
	require.Positive(t, legsFilled, "trades ended at their stop loss or take profit")
	require.Positive(t, exitsByMarket, "trades ended on EXIT")
	require.Equal(t, trades, legsFilled+exitsByMarket+open, "one exit per trade, the last one may be open")
}
