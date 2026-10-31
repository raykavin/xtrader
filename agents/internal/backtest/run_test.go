package backtest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/ui"
)

const testPair = "XAUUSDm"

// bar is a candle of the tests: its open, high, low and close, its spread in
// points (0 for none).
type bar struct{ o, h, l, c, spread float64 }

// flat returns n bars at price.
func flat(n int, price float64) []bar {
	bars := make([]bar, n)
	for i := range bars {
		bars[i] = bar{price, price, price, price, 0}
	}
	return bars
}

func init() {
	exchange.RegisterMT5Pair(testPair, "XAU", "USD") // as the runner does before a backtest
}

// start is the time of the first bar of the tests: 00:00 in Belém.
var start = time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)

// writeBars writes bars as a CSV of 1m candles from start, with their spread.
func writeBars(t *testing.T, bars []bar) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("time,open,close,low,high,volume,spread\n")
	for i, x := range bars {
		fmt.Fprintf(&b, "%d,%g,%g,%g,%g,1,%g\n", start.Add(time.Duration(i)*time.Minute).Unix(), x.o, x.c, x.l, x.h,
			x.spread)
	}
	path := filepath.Join(t.TempDir(), "candles.csv")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
	return path
}

// script is a strategy of the tests: on its candle i (from 0, the first of the
// period), it runs steps[i] with the broker; seen records the latest data
// any of its reads returned.
type script struct {
	timeframe string
	steps     map[int]func(df *model.Dataframe, broker service.Broker)
	calls     int
	feed      *exchange.BacktestFeed
	onCandle  func(df *model.Dataframe, broker service.Broker)

	mu   sync.Mutex
	errs []error
}

func (s *script) Timeframe() string                                     { return s.timeframe }
func (s *script) WarmupPeriod() int                                     { return 1 }
func (s *script) Indicators(*model.Dataframe) []strategy.ChartIndicator { return nil }
func (s *script) OnCandle(df *model.Dataframe, broker service.Broker) {
	if s.onCandle != nil {
		s.onCandle(df, broker)
	}
	if step, ok := s.steps[s.calls]; ok {
		step(df, broker)
	}
	s.calls++
}

func (s *script) fail(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.errs = append(s.errs, err)
	s.mu.Unlock()
}

// config returns the config of a backtest of the tests over bars of 1m from
// start, the report in a temp dir.
func config(t *testing.T, bars int) Config {
	t.Helper()
	return Config{From: start.Unix(), To: start.Add(time.Duration(bars) * time.Minute).Unix(), Balance: 10000,
		Leverage: 1, Report: filepath.Join(t.TempDir(), "report.json"),
		Symbols: map[string]Symbol{testPair: {Point: 0.01}}}
}

// runScript runs s over bars as cfg says and returns the report.
func runScript(t *testing.T, ctx context.Context, s *script, bars []bar, cfg Config, hooks Hooks) Report {
	t.Helper()
	if s.timeframe == "" {
		s.timeframe = "1m"
	}
	prepare := hooks.Prepare
	hooks.Prepare = func(feed *exchange.BacktestFeed) {
		s.feed = feed
		if prepare != nil {
			prepare(feed)
		}
	}
	err := Run(ctx, xtrader.Settings{Pairs: []string{testPair}}, s, []string{writeBars(t, bars)}, "1m", cfg, hooks)
	require.NoError(t, err)
	data, err := os.ReadFile(cfg.Report)
	require.NoError(t, err)
	var report Report
	require.NoError(t, json.Unmarshal(data, &report))
	return report
}

func buy(size float64) func(*model.Dataframe, service.Broker) {
	return func(_ *model.Dataframe, b service.Broker) {
		_, _ = b.CreateOrderMarket(model.SideTypeBuy, testPair, size)
	}
}

func sell(size float64) func(*model.Dataframe, service.Broker) {
	return func(_ *model.Dataframe, b service.Broker) {
		_, _ = b.CreateOrderMarket(model.SideTypeSell, testPair, size)
	}
}

// The costs of a round trip as by hand: the buy at the ask (the close plus
// the spread) plus the slippage, the sell at the bid less it, the fee on the
// notional of each fill.
func TestRun_Costs(t *testing.T) {
	bars := append(flat(3, 100), bar{110, 110, 110, 110, 0}, bar{110, 110, 110, 110, 0})
	cfg := config(t, len(bars))
	cfg.Fee, cfg.SpreadPoints, cfg.SlippagePoints = 0.001, 20, 10
	s := &script{steps: map[int]func(*model.Dataframe, service.Broker){0: buy(1), 3: sell(1)}}
	report := runScript(t, context.Background(), s, bars, cfg, Hooks{})

	assert.Equal(t, StatusSucceeded, report.Status)
	require.Len(t, report.Trades, 1)
	trade := report.Trades[0]
	entry, exit := 100+0.2+0.1, 110-0.1
	assert.InDelta(t, entry, trade.Entry, 1e-9)
	assert.InDelta(t, exit, trade.Exit, 1e-9)
	assert.InDelta(t, exit-entry, trade.Gross, 1e-9)
	assert.InDelta(t, 0.001*(entry+exit), trade.Fees, 1e-9)
	assert.InDelta(t, exit-entry-0.001*(entry+exit), trade.Net, 1e-9)
	assert.Equal(t, ReasonMarket, trade.EntryReason)
	assert.Equal(t, ReasonMarket, trade.ExitReason)
	assert.InDelta(t, trade.Net, report.Summary.Realized, 1e-9)
	assert.InDelta(t, 10000+trade.Net, report.Summary.FinalEquity, 1e-6)
	assert.Equal(t, "USD", report.Currency)
	assert.Empty(t, report.Open)
}

// A candle reaching both the stop loss and the take profit fills the stop
// loss: the worst is assumed.
func TestRun_StopBeforeTarget(t *testing.T) {
	bars := append(flat(2, 100), bar{100, 103, 97, 100, 0}, bar{100, 100, 100, 100, 0})
	cfg := config(t, len(bars))
	cfg.Conservative = true
	s := &script{steps: map[int]func(*model.Dataframe, service.Broker){0: func(_ *model.Dataframe, b service.Broker) {
		_, _ = b.(service.ProtectionBroker).CreateOrderMarketProtected(model.SideTypeBuy, testPair, 1, 98, 102)
	}}}
	report := runScript(t, context.Background(), s, bars, cfg, Hooks{})
	require.Len(t, report.Trades, 1)
	assert.Equal(t, ReasonStopLoss, report.Trades[0].ExitReason)
	assert.InDelta(t, 98, report.Trades[0].Exit, 1e-9)
}

// A stop on the book before a candle opening beyond it fills at the open.
func TestRun_StopGap(t *testing.T) {
	bars := append(flat(2, 100), bar{95, 96, 94, 95, 0}, bar{95, 95, 95, 95, 0})
	cfg := config(t, len(bars))
	cfg.GapFills, cfg.SlippagePoints = true, 10
	s := &script{steps: map[int]func(*model.Dataframe, service.Broker){0: func(_ *model.Dataframe, b service.Broker) {
		_, _ = b.(service.ProtectionBroker).CreateOrderMarketProtected(model.SideTypeBuy, testPair, 1, 98, 0)
	}}}
	report := runScript(t, context.Background(), s, bars, cfg, Hooks{})
	require.Len(t, report.Trades, 1)
	assert.InDelta(t, 95-0.1, report.Trades[0].Exit, 1e-9, "the open less the slippage, not the stop")
}

// A limit entry filled within a candle that crossed its stop loss too is
// stopped on it, conservative.
func TestRun_EntryAndStopInOneCandle(t *testing.T) {
	bars := append(flat(2, 100), bar{100, 100, 96, 98, 0}, bar{98, 98, 98, 98, 0})
	cfg := config(t, len(bars))
	cfg.Conservative = true
	s := &script{steps: map[int]func(*model.Dataframe, service.Broker){0: func(_ *model.Dataframe, b service.Broker) {
		_, _ = b.(service.ProtectionBroker).CreateOrderLimitProtected(model.SideTypeBuy, testPair, 1, 99, 97, 0)
	}}}
	report := runScript(t, context.Background(), s, bars, cfg, Hooks{})
	require.Len(t, report.Trades, 1)
	assert.Equal(t, ReasonLimit, report.Trades[0].EntryReason)
	assert.Equal(t, ReasonStopLoss, report.Trades[0].ExitReason)
	assert.InDelta(t, -2, report.Trades[0].Gross, 1e-9)
}

// A position open at the end is valued at the market apart from the
// realized result.
func TestRun_OpenAtTheEnd(t *testing.T) {
	bars := append(flat(2, 100), bar{105, 105, 105, 105, 0})
	cfg := config(t, len(bars))
	cfg.SlippagePoints = 10
	s := &script{steps: map[int]func(*model.Dataframe, service.Broker){0: buy(2)}}
	report := runScript(t, context.Background(), s, bars, cfg, Hooks{})
	assert.Empty(t, report.Trades)
	assert.Zero(t, report.Summary.Realized)
	require.Len(t, report.Open, 1)
	open := report.Open[0]
	assert.Equal(t, 2.0, open.Quantity)
	assert.InDelta(t, 105-0.1, open.Mark, 1e-9)
	assert.InDelta(t, 2*(105-0.1-(100+0.1)), open.Profit, 1e-9)
	assert.InDelta(t, open.Profit, report.Summary.OpenProfit, 1e-9)
}

// The strategy and whatever reads the feed never see a candle after the one
// being processed, whatever they ask for.
func TestRun_NoFutureData(t *testing.T) {
	bars := make([]bar, 60)
	for i := range bars {
		p := 100 + float64(i)
		bars[i] = bar{p, p, p, p, 0}
	}
	cfg := config(t, len(bars))
	s := &script{timeframe: "5m"}
	var latest, ahead time.Time
	s.steps = map[int]func(*model.Dataframe, service.Broker){}
	for i := range 12 {
		s.steps[i] = func(df *model.Dataframe, _ service.Broker) {
			now := df.Time[len(df.Time)-1].Add(5 * time.Minute) // the close of the candle processed
			for _, tf := range []string{"1m", "5m", "15m"} {
				candles, _ := s.feed.CandlesByPeriod(context.Background(), testPair, tf, start,
					start.Add(1000*time.Hour))
				if limit, err := s.feed.CandlesByLimit(context.Background(), testPair, tf, 1); err == nil {
					candles = append(candles, limit...)
				}
				for _, c := range candles {
					length, _ := time.ParseDuration(tf)
					if end := c.Time.Add(length); end.After(now) {
						ahead = end
					}
					if c.Time.After(latest) {
						latest = c.Time
					}
				}
			}
		}
	}
	report := runScript(t, context.Background(), s, bars, cfg, Hooks{})
	assert.Equal(t, StatusSucceeded, report.Status)
	assert.True(t, ahead.IsZero(), "a candle closing after the one processed: %v", ahead)
	assert.False(t, latest.IsZero(), "the reads returned candles")
}

// The daily limit closes the position and blocks the entries until the next
// day of Belém.
func TestRun_DailyLimit(t *testing.T) {
	// 00:00 to 00:04 of a day falling, then the next day
	bars := append([]bar{{100, 100, 100, 100, 0}, {100, 100, 100, 100, 0}}, bar{94, 94, 94, 94, 0},
		bar{94, 94, 94, 94, 0})
	path := writeBars(t, bars)
	nextDay := start.Add(24 * time.Hour)
	extra := fmt.Sprintf("%d,94,94,94,94,1,0\n%d,94,94,94,94,1,0\n", nextDay.Unix(), nextDay.Add(time.Minute).Unix())
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(extra)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	cfg := config(t, 0)
	cfg.To = nextDay.Add(2 * time.Minute).Unix()
	cfg.DailyLoss = 5
	var refused, after error
	s := &script{timeframe: "1m", steps: map[int]func(*model.Dataframe, service.Broker){
		0: buy(1),
		3: func(_ *model.Dataframe, b service.Broker) {
			_, refused = b.CreateOrderMarket(model.SideTypeBuy, testPair, 1)
		},
		4: func(_ *model.Dataframe, b service.Broker) {
			_, after = b.CreateOrderMarket(model.SideTypeBuy, testPair, 1)
		},
	}}
	require.NoError(t, Run(context.Background(), xtrader.Settings{Pairs: []string{testPair}}, s, []string{path}, "1m",
		cfg, Hooks{Prepare: func(feed *exchange.BacktestFeed) { s.feed = feed }}))
	var report Report
	data, err := os.ReadFile(cfg.Report)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &report))
	require.NotEmpty(t, report.Trades)
	assert.Equal(t, ReasonDailyLimit, report.Trades[0].ExitReason)
	assert.Error(t, refused, "blocked the rest of the day")
	assert.NoError(t, after, "allowed the next day")
}

// A canceled backtest writes its report, partial, canceled.
func TestRun_Canceled(t *testing.T) {
	bars := flat(50, 100)
	cfg := config(t, len(bars))
	ctx, cancel := context.WithCancel(context.Background())
	s := &script{steps: map[int]func(*model.Dataframe, service.Broker){10: func(*model.Dataframe, service.Broker) {
		cancel()
	}}}
	report := runScript(t, ctx, s, bars, cfg, Hooks{})
	assert.Equal(t, StatusCanceled, report.Status)
	assert.Less(t, report.Processed, len(bars))
}

// With a leverage, a position commits its notional over it: a notional above
// the balance is taken, the cash told the free margin, the equity the
// account's.
func TestRun_Leverage(t *testing.T) {
	bars := append(flat(2, 100), bar{101, 101, 101, 101, 0}, bar{101, 101, 101, 101, 0})
	cfg := config(t, len(bars))
	cfg.Balance, cfg.Leverage = 1000, 10
	var cash float64
	s := &script{steps: map[int]func(*model.Dataframe, service.Broker){
		0: buy(50), // a notional of 5000
		1: func(_ *model.Dataframe, b service.Broker) { _, cash, _ = b.Position(testPair) },
		2: sell(50),
	}}
	report := runScript(t, context.Background(), s, bars, cfg, Hooks{})
	require.Len(t, report.Trades, 1)
	assert.Equal(t, 50.0, report.Trades[0].Quantity)
	assert.InDelta(t, 1000-5000.0/10, cash, 1e-6, "the free margin: the balance less the margin")
	assert.InDelta(t, 1000+50, report.Summary.FinalEquity, 1e-6)
}

// The volumes floored to the volume step, under the least volume refused and
// counted.
func TestRun_VolumeSteps(t *testing.T) {
	bars := flat(5, 100)
	cfg := config(t, len(bars))
	cfg.Symbols = map[string]Symbol{testPair: {Point: 0.01, VolumeStep: 0.01, VolumeMin: 0.01}}
	s := &script{steps: map[int]func(*model.Dataframe, service.Broker){0: buy(0.015), 1: buy(0.005), 2: sell(0.01)}}
	report := runScript(t, context.Background(), s, bars, cfg, Hooks{})
	require.Len(t, report.Trades, 1)
	assert.InDelta(t, 0.01, report.Trades[0].Quantity, 1e-12, "floored to the step")
	assert.Equal(t, 1, report.Summary.Refused)
	assert.Contains(t, strings.Join(report.Warnings, " "), "under the least")
}

// No entry before the period: the candles before it warm up only.
func TestRun_WarmupHoldsTheEntries(t *testing.T) {
	bars := flat(6, 100)
	cfg := config(t, len(bars))
	cfg.From = start.Add(3 * time.Minute).Unix()
	var early, late error
	s := &script{steps: map[int]func(*model.Dataframe, service.Broker){
		0: func(_ *model.Dataframe, b service.Broker) {
			_, early = b.CreateOrderMarket(model.SideTypeBuy, testPair, 1)
		},
		4: func(_ *model.Dataframe, b service.Broker) {
			_, late = b.CreateOrderMarket(model.SideTypeBuy, testPair, 1)
		},
	}}
	runScript(t, context.Background(), s, bars, cfg, Hooks{})
	assert.Error(t, early, "in the warm-up")
	assert.NoError(t, late)
}

// The breakeven of the agent moves the stop loss on the candle clock.
func TestRun_Breakeven(t *testing.T) {
	bars := flat(20, 100)
	for i := 10; i < 20; i++ {
		bars[i] = bar{100 + float64(i-9), 100 + float64(i-9), 100 + float64(i-9) - 0.5, 100 + float64(i-9), 0}
	}
	bars = append(bars, bar{100, 100, 95, 96, 0}, bar{96, 96, 96, 96, 0})
	cfg := config(t, len(bars))
	cfg.From = start.Add(15 * time.Minute).Unix()
	cfg.Protection = &ui.ProtectionSettings{Unit: "atr", Breakeven: ui.BreakevenSettings{Enabled: true, Trigger: 1,
		Margin: 0.1}}
	s := &script{steps: map[int]func(*model.Dataframe, service.Broker){15: func(_ *model.Dataframe, b service.Broker) {
		_, _ = b.(service.ProtectionBroker).CreateOrderMarketProtected(model.SideTypeBuy, testPair, 1, 90, 0)
	}}}
	report := runScript(t, context.Background(), s, bars, cfg, Hooks{})
	require.Len(t, report.Trades, 1, "%v", report.Warnings)
	assert.Equal(t, ReasonStopLoss, report.Trades[0].ExitReason)
	assert.Greater(t, report.Trades[0].Exit, report.Trades[0].Entry, "stopped above the entry by the breakeven")
}

// The trading hours of a backtest on the clock of the candles (each closing
// a minute after 00:00 of Belém): no entry outside the window; at its end,
// after it was open, the position closed (its exit told) and the pending
// entry canceled, as checked; unchecked, both left as they are.
func TestRun_TradingHours(t *testing.T) {
	type outcome struct {
		before, inside, after error
		pending               model.Order
		report                Report
	}
	run := func(t *testing.T, flatten, cancel bool) outcome {
		t.Helper()
		var o outcome
		var limit model.Order
		cfg := config(t, 8)
		cfg.Hours = &ui.BacktestHours{Start: "00:02", End: "00:05", Flatten: flatten, CancelPending: cancel}
		s := &script{steps: map[int]func(*model.Dataframe, service.Broker){
			0: func(_ *model.Dataframe, b service.Broker) { // closes at 00:01
				_, o.before = b.CreateOrderMarket(model.SideTypeBuy, testPair, 1)
			},
			1: func(_ *model.Dataframe, b service.Broker) { // 00:02
				_, o.inside = b.CreateOrderMarket(model.SideTypeBuy, testPair, 1)
			},
			2: func(_ *model.Dataframe, b service.Broker) { // 00:03: an entry far below
				limit, _ = b.CreateOrderLimit(model.SideTypeBuy, testPair, 1, 50)
			},
			5: func(_ *model.Dataframe, b service.Broker) { // 00:06
				_, o.after = b.CreateOrderMarket(model.SideTypeBuy, testPair, 1)
				o.pending, _ = b.Order(testPair, limit.ID)
			},
		}}
		o.report = runScript(t, context.Background(), s, flat(8, 100), cfg, Hooks{})
		return o
	}

	t.Run("flatten and cancel", func(t *testing.T) {
		o := run(t, true, true)
		assert.Error(t, o.before, "before the start")
		assert.NoError(t, o.inside)
		assert.Error(t, o.after, "from the end")
		require.Len(t, o.report.Trades, 1)
		assert.Equal(t, ReasonHours, o.report.Trades[0].ExitReason)
		assert.Equal(t, start.Add(4*time.Minute).Unix(), o.report.Trades[0].ClosedAt,
			"on the candle closing at the end (the trades tell the time of their candle)")
		assert.Empty(t, o.report.Open)
		assert.Equal(t, model.OrderStatusTypeCanceled, o.pending.Status, "the pending entry canceled")
	})

	t.Run("left as they are", func(t *testing.T) {
		o := run(t, false, false)
		assert.Error(t, o.after, "the entries blocked all the same")
		assert.Empty(t, o.report.Trades)
		require.Len(t, o.report.Open, 1, "the position left open")
		assert.Equal(t, model.OrderStatusTypeNew, o.pending.Status, "the pending entry left")
	})

	t.Run("only the pending entries", func(t *testing.T) {
		o := run(t, false, true)
		assert.Empty(t, o.report.Trades)
		require.Len(t, o.report.Open, 1)
		assert.Equal(t, model.OrderStatusTypeCanceled, o.pending.Status)
	})
}

// A window of the trading hours crossing midnight: closed from its end, open
// again from its start, the entries allowed again.
func TestRun_TradingHoursAcrossMidnight(t *testing.T) {
	cfg := config(t, 4)
	cfg.Hours = &ui.BacktestHours{Start: "00:03", End: "00:01", Flatten: true}
	var closed, reopened error
	s := &script{steps: map[int]func(*model.Dataframe, service.Broker){
		1: func(_ *model.Dataframe, b service.Broker) { // 00:02
			_, closed = b.CreateOrderMarket(model.SideTypeBuy, testPair, 1)
		},
		2: func(_ *model.Dataframe, b service.Broker) { // 00:03
			_, reopened = b.CreateOrderMarket(model.SideTypeBuy, testPair, 1)
		},
	}}
	report := runScript(t, context.Background(), s, flat(4, 100), cfg, Hooks{})
	assert.Error(t, closed)
	assert.NoError(t, reopened, "open again from its start")
	assert.Empty(t, report.Trades, "no end acted on: the window wasn't open before")
	require.Len(t, report.Open, 1)
}
