package strategies

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gonum.org/v1/gonum/stat"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

// the legs of the pairs tests: A, then B, alphabetical as the backtest hands
// the candles of a time
const (
	legA = "BTCUSDT"
	legB = "ETHUSDT"
)

// pairsRunner trades a Pairs strategy on two legs through the order controller
// of a tradingBot over the paper wallet, as the agents do: each candle goes to
// the wallet, the fills are settled, then the strategy gets it through a
// strategy.Controller of its pair, as the bot hands it (the sample of the
// warmup, its indicators first), and the order controller last.
type pairsRunner struct {
	t      *testing.T
	cfg    PairsConfig
	s      *Pairs
	wallet *exchange.PaperWallet
	bot    *tradingBot
	broker service.Broker
	wrap   func(liveStamps) service.Broker // the broker of the strategy, the bot's when nil
	ctls   map[string]*strategy.Controller
	fed    map[string][]model.Candle // the candles fed, by pair, preloaded after a restart
	start  time.Time
	step   time.Duration
}

// newPairsRunner trades legs A and B on 1h candles with a window of 20, the
// spread the log of the ratio of their prices (the default hedge), leg A sized
// 1 and leg B 12 (the value of a unit of A, see pairsHedged).
func newPairsRunner(t *testing.T, configure func(*PairsConfig)) *pairsRunner {
	t.Helper()
	cfg := DefaultPairsConfig()
	cfg.Pairs = []string{legA, legB}
	cfg.Len = 20
	cfg.PositionSize = map[string]float64{legA: 1, legB: 12}
	if configure != nil {
		configure(&cfg)
	}
	wallet := exchange.NewPaperWallet(context.Background(), "USDT", exchange.WithPaperAsset("USDT", 10000))
	r := &pairsRunner{t: t, cfg: cfg, wallet: wallet, start: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
		step: time.Hour}
	r.bot = newTradingBot(t, wallet, r.step)
	r.fed = map[string][]model.Candle{}
	r.begin()
	return r
}

// begin starts a strategy over the bot, its controllers preloaded with the
// candles fed so far (indicators only, as the live preload).
func (r *pairsRunner) begin() {
	r.t.Helper()
	s, err := NewPairs(r.cfg)
	require.NoError(r.t, err)
	r.s = s
	stamps := r.bot.broker().(liveStamps)
	r.broker = stamps
	if r.wrap != nil {
		r.broker = r.wrap(stamps)
	}
	r.ctls = map[string]*strategy.Controller{}
	for pair, candles := range r.fed {
		ctl := r.controller(pair)
		for _, candle := range candles {
			require.NoError(r.t, ctl.OnCandle(candle))
		}
	}
	for _, ctl := range r.ctls {
		ctl.Start()
	}
}

func (r *pairsRunner) controller(pair string) *strategy.Controller {
	ctl, ok := r.ctls[pair]
	if !ok {
		ctl = strategy.NewStrategyController(pair, r.s, r.broker)
		r.ctls[pair] = ctl
	}
	return ctl
}

// restart restarts the bot and the strategy, as after a restart of the agent.
func (r *pairsRunner) restart() {
	r.bot.restart()
	r.begin()
}

// candle feeds the candle of pair at the bar i closing at price.
func (r *pairsRunner) candle(pair string, i int, price float64) {
	r.t.Helper()
	candle := model.Candle{Pair: pair, Time: r.start.Add(time.Duration(i) * r.step), Open: price, Close: price,
		High: price, Low: price, Volume: 1, Complete: true}
	r.wallet.OnCandle(candle)
	r.bot.settle()
	ctl, ok := r.ctls[pair]
	if !ok {
		ctl = r.controller(pair)
		ctl.Start()
	}
	require.NoError(r.t, ctl.OnCandle(candle))
	r.bot.ctl.OnCandle(candle)
	r.fed[pair] = append(r.fed[pair], candle)
}

// bars feeds the candles of both legs from the bar from to the bar to, A then B.
func (r *pairsRunner) bars(from, to int, a, b []float64) {
	r.t.Helper()
	for i := from; i <= to; i++ {
		r.candle(legA, i, a[i])
		r.candle(legB, i, b[i])
	}
}

// requireHeld checks the positions of the legs.
func (r *pairsRunner) requireHeld(a, b float64, msgAndArgs ...any) {
	r.t.Helper()
	heldA, _, err := r.wallet.Position(legA)
	require.NoError(r.t, err)
	heldB, _, err := r.wallet.Position(legB)
	require.NoError(r.t, err)
	require.InDelta(r.t, a, heldA, 1e-9, msgAndArgs...)
	require.InDelta(r.t, b, heldB, 1e-9, msgAndArgs...)
}

// orders returns the orders of the bot filled, by their creation.
func (r *pairsRunner) orders() []*model.Order {
	r.t.Helper()
	orders, err := r.bot.store.Orders(storage.WithStatusIn(model.OrderStatusTypeFilled))
	require.NoError(r.t, err)
	slices.SortFunc(orders, func(a, b *model.Order) int { return cmp.Compare(a.ID, b.ID) })
	return orders
}

// pairsPrices returns the closes of legs A and B on n candles, the log of
// their ratio stationary, see pairsHedged.
func pairsPrices(n int, eps map[int]float64) (a, b []float64) {
	return pairsHedged(n, 1, eps)
}

// pairsHedged returns the closes of legs A and B on n candles: B swinging
// around 50, A cointegrated with it, ln A = ln 600 + beta (ln B − ln 50) + ε,
// A worth 600, 12 units of B, while B is at 50, ε an alternating noise of
// 0.001, replaced on the candles of eps.
func pairsHedged(n int, beta float64, eps map[int]float64) (a, b []float64) {
	a, b = make([]float64, n), make([]float64, n)
	for i := range n {
		b[i] = 50 * math.Exp(0.02*math.Sin(float64(i)/3))
		noise := 0.001
		if i%2 == 1 {
			noise = -noise
		}
		if e, ok := eps[i]; ok {
			noise = e
		}
		a[i] = 600 * math.Exp(beta*math.Log(b[i]/50)+noise)
	}
	return a, b
}

// refZ is the Z-Score of the spread of the closes a and b on the candle i
// over length candles with the default hedge, by gonum: the log of the ratio
// of the closes minus its mean over its population deviation.
func refZ(a, b []float64, i, length int) float64 {
	spread := make([]float64, length)
	for k := range length {
		spread[k] = math.Log(a[i-length+1+k]) - math.Log(b[i-length+1+k])
	}
	mean, deviation := stat.PopMeanStdDev(spread, nil)
	return (spread[length-1] - mean) / deviation
}

// refBeta is the hedge ratio of the closes a and b on the candle i over
// length candles with PairsHedgeOLS, by gonum: the slope of the OLS regression
// of ln a on ln b.
func refBeta(a, b []float64, i, length int) float64 {
	x, y := make([]float64, length), make([]float64, length)
	for k := range length {
		x[k], y[k] = math.Log(b[i-length+1+k]), math.Log(a[i-length+1+k])
	}
	_, beta := stat.LinearRegression(x, y, nil, false)
	return beta
}

// refOLSZ is the Z-Score of the spread of the closes a and b on the candle i
// over length candles with PairsHedgeOLS, by gonum: the residual of the OLS
// regression of ln a on ln b over the population deviation of the residuals.
func refOLSZ(a, b []float64, i, length int) float64 {
	x, y := make([]float64, length), make([]float64, length)
	for k := range length {
		x[k], y[k] = math.Log(b[i-length+1+k]), math.Log(a[i-length+1+k])
	}
	alpha, beta := stat.LinearRegression(x, y, nil, false)
	residuals := make([]float64, length)
	for k := range length {
		residuals[k] = y[k] - alpha - beta*x[k]
	}
	_, deviation := stat.PopMeanStdDev(residuals, nil)
	return residuals[length-1] / deviation
}

func TestNewPairs_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*PairsConfig)
		err       string
	}{
		{"timeframe", func(c *PairsConfig) { c.Timeframe = "" }, "invalid timeframe"},
		{"bad timeframe", func(c *PairsConfig) { c.Timeframe = "1x" }, "invalid timeframe"},
		{"window", func(c *PairsConfig) { c.Len = 2 }, "at least 3 candles"},
		{"entry", func(c *PairsConfig) { c.EntryZ = 0 }, "entry Z-Score must be positive"},
		{"entry out of reach", func(c *PairsConfig) { c.Len, c.EntryZ = 5, 2 }, "the most a Z-Score of 5"},
		{"exit beyond the entry", func(c *PairsConfig) { c.ExitZ = 2 }, "exit Z-Score"},
		{"exit beyond the other entry", func(c *PairsConfig) { c.ExitZ = -2.5 }, "exit Z-Score"},
		{"negative stop", func(c *PairsConfig) { c.StopZ = -1 }, "stop Z-Score"},
		{"stop inside the entry", func(c *PairsConfig) { c.StopZ = 2 }, "stop Z-Score"},
		{"negative max bars", func(c *PairsConfig) { c.MaxHoldBars = -1 }, "max bars"},
		{"fixed stop", func(c *PairsConfig) { c.StopValue = 5 }, "no stop loss nor take profit"},
		{"fixed take profit", func(c *PairsConfig) { c.TakeProfitValue = 5 }, "no stop loss nor take profit"},
		{"max risk", func(c *PairsConfig) { c.MaxRiskPct = 1 }, "max risk"},
		{"min ATR", func(c *PairsConfig) { c.MinATRPct = 0.1 }, "min ATR"},
		{"whole balance", func(c *PairsConfig) { c.PositionFraction = 1 }, "share the position fraction"},
		{"one pair", func(c *PairsConfig) { c.Pairs = []string{"XAUUSDm"} }, "not 1 pairs: XAUUSDm"},
		{"three pairs", func(c *PairsConfig) { c.Pairs = []string{"XAUUSDm", "XAGUSDm", "BTCUSDm"} },
			"not 3 pairs"},
		{"the same pair", func(c *PairsConfig) { c.Pairs = []string{"XAUUSDm", "XAUUSDm"} }, "the same pair"},
		{"no hedge", func(c *PairsConfig) { c.Hedge = "" }, `the hedge must be ratio or ols, not ""`},
		{"another hedge", func(c *PairsConfig) { c.Hedge = "tls" }, `not "tls"`},
		{"no pair", func(c *PairsConfig) { c.Pairs = []string{"XAUUSDm", ""} }, "without a pair"},
		{"the sizing", func(c *PairsConfig) { c.Leverage = 0.5 }, "leverage"},
		{"the direction", func(c *PairsConfig) { c.Direction = "long" }, "invalid direction"},
		{"a size", func(c *PairsConfig) { c.PositionSize = map[string]float64{"XAUUSDm": 0} }, "position size"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultPairsConfig()
			tt.configure(&cfg)
			_, err := NewPairs(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}

	require.NoError(t, DefaultPairsConfig().validate())
	valid := []func(*PairsConfig){
		func(c *PairsConfig) { c.Pairs = []string{"XAUUSDm", "XAGUSDm"} },
		func(c *PairsConfig) { c.PositionFraction, c.PositionSize = 1, map[string]float64{"XAUUSDm": 0.01} },
		func(c *PairsConfig) { c.StopZ, c.ExitZ, c.MaxHoldBars = 3.5, -0.5, 0 },
		func(c *PairsConfig) { c.StopATR, c.TakeProfitATR = 0, 0 }, // not read
		func(c *PairsConfig) { c.Hedge = PairsHedgeOLS },
	}
	require.Equal(t, PairsHedgeRatio, DefaultPairsConfig().Hedge, "the log of the ratio by default")
	for _, configure := range valid {
		cfg := DefaultPairsConfig()
		configure(&cfg)
		_, err := NewPairs(cfg)
		require.NoError(t, err)
	}
}

func TestPairs_WarmupPeriod(t *testing.T) {
	// the window, the candle before it, and a tenth more for the candles of
	// one leg alone
	s, err := NewPairs(DefaultPairsConfig())
	require.NoError(t, err)
	require.Equal(t, 250+1+25, s.WarmupPeriod())
	require.Equal(t, "1h", s.Timeframe())

	cfg := DefaultPairsConfig()
	cfg.Len = 20
	s, err = NewPairs(cfg)
	require.NoError(t, err)
	require.Equal(t, 23, s.WarmupPeriod())
}

func TestPairsZ(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	a, b := make([]float64, 300), make([]float64, 300)
	price := 100.0
	for i := range a {
		price *= math.Exp(0.01 * rng.NormFloat64())
		b[i] = price
		a[i] = math.Exp(1 + 0.8*math.Log(price) + 0.005*rng.NormFloat64())
	}
	for _, length := range []int{3, 20, 250} {
		for i := length - 1; i < len(a); i += 7 {
			lnA, lnB := make([]float64, length), make([]float64, length)
			for k := range length {
				lnA[k], lnB[k] = math.Log(a[i-length+1+k]), math.Log(b[i-length+1+k])
			}
			z, beta, ok := pairsZ(lnA, lnB, true)
			require.True(t, ok)
			_, wantBeta := stat.LinearRegression(lnB, lnA, nil, false)
			require.InDelta(t, wantBeta, beta, 1e-9)
			require.InDelta(t, refBeta(a, b, i, length), beta, 1e-9)
			require.InDelta(t, refOLSZ(a, b, i, length), z, 1e-9)
			require.Less(t, math.Abs(z), math.Sqrt(float64(length-1))+1e-9, "never beyond √(n-1)")

			// the log of the ratio: β 1, the Z-Score of ln A − ln B, apart
			// from the one of the residual (β 0.8 here)
			z, beta, ok = pairsZ(lnA, lnB, false)
			require.True(t, ok)
			require.Equal(t, 1.0, beta)
			require.InDelta(t, refZ(a, b, i, length), z, 1e-9)
			require.Less(t, math.Abs(z), math.Sqrt(float64(length-1))+1e-9, "never beyond √(n-1)")
			if length == 250 {
				require.Greater(t, math.Abs(z-refOLSZ(a, b, i, length)), 1e-3, "another spread")
			}
		}
	}

	for _, ols := range []bool{false, true} {
		_, _, ok := pairsZ([]float64{1, 2}, []float64{1, 2}, ols)
		require.False(t, ok, "too few candles")
	}
	_, _, ok := pairsZ([]float64{1, 2, 3}, []float64{4, 4, 4}, true)
	require.False(t, ok, "leg B flat, no regression")
	z, _, ok := pairsZ([]float64{1, 2, 3}, []float64{4, 4, 4}, false)
	require.True(t, ok, "leg B flat, the ratio moving")
	require.InDelta(t, math.Sqrt(1.5), z, 1e-12)
	_, _, ok = pairsZ([]float64{1, 2, 3}, []float64{2, 3, 4}, true)
	require.False(t, ok, "the residual flat")
	_, _, ok = pairsZ([]float64{1, 2, 3}, []float64{3, 4, 5}, false)
	require.False(t, ok, "the ratio flat")
}

// the scenario of the entries: the noise of the spread alternating, a
// divergence of A under B on the candle 30, back above its mean on 31
const pairsDiverge = 30

func TestPairs_LongSpread(t *testing.T) {
	a, b := pairsPrices(40, map[int]float64{pairsDiverge: -0.01, pairsDiverge + 1: 0.003})
	for i := 22; i < pairsDiverge; i++ {
		require.Greater(t, refZ(a, b, i, 20), -2.0, "candle %d", i)
		require.Less(t, refZ(a, b, i, 20), 2.0, "candle %d", i)
	}
	require.LessOrEqual(t, refZ(a, b, pairsDiverge, 20), -2.0)
	require.GreaterOrEqual(t, refZ(a, b, pairsDiverge+1, 20), 0.0)

	t.Run("leg A first", func(t *testing.T) {
		r := newPairsRunner(t, nil)
		r.bars(0, pairsDiverge-1, a, b)
		r.requireHeld(0, 0)

		r.candle(legA, pairsDiverge, a[pairsDiverge])
		r.requireHeld(0, 0, "the candle of leg B awaited")
		r.candle(legB, pairsDiverge, b[pairsDiverge])
		r.requireHeld(1, -12, "A bought, B sold")

		r.candle(legA, pairsDiverge+1, a[pairsDiverge+1])
		r.requireHeld(1, -12)
		r.candle(legB, pairsDiverge+1, b[pairsDiverge+1])
		r.requireHeld(0, 0, "back at its mean")

		orders := r.orders()
		require.Len(t, orders, 4)
		for i, want := range []struct {
			pair string
			side model.SideType
			at   int
		}{
			{legA, model.SideTypeBuy, pairsDiverge}, {legB, model.SideTypeSell, pairsDiverge},
			{legA, model.SideTypeSell, pairsDiverge + 1}, {legB, model.SideTypeBuy, pairsDiverge + 1},
		} {
			require.Equal(t, want.pair, orders[i].Pair, "order %d", i)
			require.Equal(t, want.side, orders[i].Side, "order %d", i)
			require.Equal(t, model.OrderTypeMarket, orders[i].Type, "order %d", i)
			require.Equal(t, r.start.Add(time.Duration(want.at)*r.step), orders[i].CreatedAt, "order %d", i)
		}
		require.InDelta(t, a[pairsDiverge], orders[0].Price, 1e-9, "at the close of the decision")
		require.InDelta(t, b[pairsDiverge], orders[1].Price, 1e-9, "at the close of the decision")
	})

	t.Run("leg B first", func(t *testing.T) {
		r := newPairsRunner(t, nil)
		for i := 0; i <= pairsDiverge+1; i++ {
			r.candle(legB, i, b[i])
			if i == pairsDiverge {
				r.requireHeld(0, 0, "the candle of leg A awaited")
			}
			r.candle(legA, i, a[i])
			switch i {
			case pairsDiverge:
				r.requireHeld(1, -12, "decided on the candle of leg A")
			case pairsDiverge + 1:
				r.requireHeld(0, 0)
			}
		}
		orders := r.orders()
		require.Len(t, orders, 4)
		require.Equal(t, legA, orders[0].Pair, "leg A placed first")
		require.Equal(t, legB, orders[1].Pair)
	})
}

func TestPairs_ShortSpread(t *testing.T) {
	a, b := pairsPrices(40, map[int]float64{pairsDiverge: 0.01, pairsDiverge + 1: -0.003})
	require.GreaterOrEqual(t, refZ(a, b, pairsDiverge, 20), 2.0)
	require.Less(t, refZ(a, b, pairsDiverge-1, 20), 2.0)
	require.LessOrEqual(t, refZ(a, b, pairsDiverge+1, 20), 0.0)

	r := newPairsRunner(t, nil)
	r.bars(0, pairsDiverge-1, a, b)
	r.requireHeld(0, 0)
	r.bars(pairsDiverge, pairsDiverge, a, b)
	r.requireHeld(-1, 12, "A sold, B bought")
	r.bars(pairsDiverge+1, pairsDiverge+1, a, b)
	r.requireHeld(0, 0, "back at its mean")
}

// A candle of a leg already past the time of the other leg's late candle
// isn't decided on: the orders would go at the prices of the next candle.
func TestPairs_StaleCandleNotDecided(t *testing.T) {
	a, b := pairsPrices(40, map[int]float64{pairsDiverge: -0.01, pairsDiverge + 1: -0.01})
	require.LessOrEqual(t, refZ(a, b, pairsDiverge, 20), -2.0, "the cross on 30")
	require.LessOrEqual(t, refZ(a, b, pairsDiverge+1, 20), -2.0, "under the entry still on 31, crossed before")

	t.Run("in time", func(t *testing.T) {
		r := newPairsRunner(t, nil)
		r.bars(0, pairsDiverge, a, b)
		r.requireHeld(1, -12)
	})

	t.Run("leg B late", func(t *testing.T) {
		r := newPairsRunner(t, nil)
		r.bars(0, pairsDiverge-1, a, b)
		r.candle(legA, pairsDiverge, a[pairsDiverge])
		r.candle(legA, pairsDiverge+1, a[pairsDiverge+1])
		r.candle(legB, pairsDiverge, b[pairsDiverge])
		r.requireHeld(0, 0, "30 not decided: leg A is on 31")
		r.candle(legB, pairsDiverge+1, b[pairsDiverge+1])
		r.requireHeld(0, 0, "31 decided: no cross on it")
		require.Empty(t, r.orders())
	})
}

func TestPairs_MaxBars(t *testing.T) {
	eps := map[int]float64{}
	for i := pairsDiverge; i < 40; i++ {
		eps[i] = -0.01 // the divergence lasting
	}
	a, b := pairsPrices(40, eps)
	for i := pairsDiverge + 1; i <= pairsDiverge+4; i++ {
		require.Less(t, refZ(a, b, i, 20), 0.0, "short of its mean on %d", i)
	}

	t.Run("after 3 candles", func(t *testing.T) {
		r := newPairsRunner(t, func(c *PairsConfig) { c.MaxHoldBars = 3 })
		r.bars(0, pairsDiverge+2, a, b)
		r.requireHeld(1, -12)
		r.bars(pairsDiverge+3, pairsDiverge+3, a, b)
		r.requireHeld(0, 0, "closed on the third candle after the entry")
	})

	t.Run("candles of one leg alone not counted", func(t *testing.T) {
		r := newPairsRunner(t, func(c *PairsConfig) { c.MaxHoldBars = 3 })
		r.bars(0, pairsDiverge+1, a, b)
		r.candle(legA, pairsDiverge+2, a[pairsDiverge+2]) // leg B misses the candle 32
		r.bars(pairsDiverge+3, pairsDiverge+3, a, b)
		r.requireHeld(1, -12, "two candles of both legs since the entry")
		r.bars(pairsDiverge+4, pairsDiverge+4, a, b)
		r.requireHeld(0, 0)
	})

	t.Run("never", func(t *testing.T) {
		r := newPairsRunner(t, func(c *PairsConfig) { c.MaxHoldBars = 0 })
		r.bars(0, pairsDiverge+4, a, b)
		r.requireHeld(1, -12)
	})
}

func TestPairs_StopZ(t *testing.T) {
	t.Run("the divergence widening", func(t *testing.T) {
		a, b := pairsPrices(40, map[int]float64{pairsDiverge: -0.003, pairsDiverge + 1: -0.02})
		z := refZ(a, b, pairsDiverge, 20)
		require.LessOrEqual(t, z, -2.0)
		require.Greater(t, z, -2.5, "inside the stop on the entry")
		require.LessOrEqual(t, refZ(a, b, pairsDiverge+1, 20), -2.5)

		r := newPairsRunner(t, func(c *PairsConfig) { c.StopZ = 2.5 })
		r.bars(0, pairsDiverge, a, b)
		r.requireHeld(1, -12)
		r.bars(pairsDiverge+1, pairsDiverge+1, a, b)
		r.requireHeld(0, 0, "stopped")

		off := newPairsRunner(t, nil)
		off.bars(0, pairsDiverge+1, a, b)
		off.requireHeld(1, -12, "no stop by default")
	})

	t.Run("an entry beyond the stop already", func(t *testing.T) {
		a, b := pairsPrices(40, map[int]float64{pairsDiverge: -0.01})
		require.Less(t, refZ(a, b, pairsDiverge, 20), -3.5)
		r := newPairsRunner(t, func(c *PairsConfig) { c.StopZ = 3.5 })
		r.bars(0, pairsDiverge+2, a, b)
		r.requireHeld(0, 0)
	})
}

func TestPairs_Direction(t *testing.T) {
	long, longB := pairsPrices(40, map[int]float64{pairsDiverge: -0.01})
	short, shortB := pairsPrices(40, map[int]float64{pairsDiverge: 0.01})
	tests := []struct {
		direction RKDirection
		a, b      []float64
		heldA     float64
	}{
		{RKDirectionBuyOnly, long, longB, 1},
		{RKDirectionBuyOnly, short, shortB, 0},
		{RKDirectionSellOnly, long, longB, 0},
		{RKDirectionSellOnly, short, shortB, -1},
	}
	for _, tt := range tests {
		r := newPairsRunner(t, func(c *PairsConfig) { c.Direction = tt.direction })
		r.bars(0, pairsDiverge, tt.a, tt.b)
		r.requireHeld(tt.heldA, -12*tt.heldA, "%s", tt.direction)
	}
}

// failingLeg is the broker of the strategy refusing the market orders of a
// pair.
type failingLeg struct {
	liveStamps
	pair string
}

var errLegRefused = errors.New("the leg refused")

func (b failingLeg) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	if pair == b.pair {
		return model.Order{}, errLegRefused
	}
	return b.liveStamps.CreateOrderMarket(side, pair, size)
}

func (b failingLeg) CreateOrderMarketQuote(side model.SideType, pair string, quote float64) (model.Order,
	error) {
	if pair == b.pair {
		return model.Order{}, errLegRefused
	}
	return b.liveStamps.CreateOrderMarketQuote(side, pair, quote)
}

func TestPairs_LegBFailingClosesLegA(t *testing.T) {
	a, b := pairsPrices(40, map[int]float64{pairsDiverge: -0.01})
	r := newPairsRunner(t, nil)
	r.wrap = func(stamps liveStamps) service.Broker { return failingLeg{liveStamps: stamps, pair: legB} }
	r.begin()
	r.bars(0, pairsDiverge, a, b)
	r.requireHeld(0, 0, "leg A closed at once")

	orders := r.orders()
	require.Len(t, orders, 2)
	require.Equal(t, legA, orders[0].Pair)
	require.Equal(t, model.SideTypeBuy, orders[0].Side)
	require.Equal(t, legA, orders[1].Pair)
	require.Equal(t, model.SideTypeSell, orders[1].Side)
	require.Equal(t, orders[0].CreatedAt, orders[1].CreatedAt, "on the candle of the entry")

	// leg A refused: nothing to undo
	r = newPairsRunner(t, nil)
	r.wrap = func(stamps liveStamps) service.Broker { return failingLeg{liveStamps: stamps, pair: legA} }
	r.begin()
	r.bars(0, pairsDiverge, a, b)
	r.requireHeld(0, 0)
	require.Empty(t, r.orders())
}

func TestPairs_LoneLegClosed(t *testing.T) {
	a, b := pairsPrices(40, map[int]float64{pairsDiverge: -0.01})
	for _, gone := range []string{legA, legB} {
		t.Run(gone+" gone", func(t *testing.T) {
			r := newPairsRunner(t, nil)
			r.bars(0, pairsDiverge, a, b)
			r.requireHeld(1, -12)

			// eg: the protection of the agent, or a hand, closing one leg
			_, err := r.bot.ctl.ClosePosition(gone)
			require.NoError(t, err)
			r.candle(legA, pairsDiverge+1, a[pairsDiverge+1])
			r.requireHeld(0, 0, "the other leg closed on the next candle of either leg")
		})
	}
}

func TestPairs_SameSideLegsClosed(t *testing.T) {
	a, b := pairsPrices(40, nil)
	r := newPairsRunner(t, nil)
	r.bars(0, 25, a, b)
	_, err := r.bot.ctl.CreateOrderMarket(model.SideTypeBuy, legA, 1)
	require.NoError(t, err)
	_, err = r.bot.ctl.CreateOrderMarket(model.SideTypeBuy, legB, 12)
	require.NoError(t, err)
	r.requireHeld(1, 12)
	r.candle(legB, 26, b[26])
	r.requireHeld(0, 0, "not a spread")
}

// Without the legs in the configuration, the strategy learns them from the
// candles: A the first in alphabetical order, and a third pair stops the
// entries.
func TestPairs_LegsLearned(t *testing.T) {
	a, b := pairsPrices(40, map[int]float64{pairsDiverge: -0.01, pairsDiverge + 1: 0.003})
	learned := func(c *PairsConfig) { c.Pairs = nil }

	t.Run("two pairs", func(t *testing.T) {
		r := newPairsRunner(t, learned)
		for i := 0; i <= pairsDiverge; i++ {
			r.candle(legB, i, b[i]) // B first: still the second leg
			r.candle(legA, i, a[i])
		}
		r.requireHeld(1, -12)
		r.bars(pairsDiverge+1, pairsDiverge+1, a, b)
		r.requireHeld(0, 0)
	})

	t.Run("a third pair", func(t *testing.T) {
		r := newPairsRunner(t, learned)
		for i := 0; i <= pairsDiverge+1; i++ {
			r.candle(legA, i, a[i])
			r.candle(legB, i, b[i])
			r.candle("XRPUSDT", i, 1)
		}
		r.requireHeld(0, 0, "no entry")
		require.Empty(t, r.orders())
	})
}

// With the legs in the configuration, the candles of another pair are left
// out.
func TestPairs_NotALeg(t *testing.T) {
	a, b := pairsPrices(40, map[int]float64{pairsDiverge: -0.01})
	r := newPairsRunner(t, nil)
	for i := 0; i <= pairsDiverge; i++ {
		r.candle("XRPUSDT", i, 1)
		r.candle(legA, i, a[i])
		r.candle(legB, i, b[i])
	}
	r.requireHeld(1, -12)
}

func TestPairs_AfterARestart(t *testing.T) {
	t.Run("the spread found closed at its mean", func(t *testing.T) {
		a, b := pairsPrices(40, map[int]float64{pairsDiverge: -0.01, pairsDiverge + 1: 0.003})
		r := newPairsRunner(t, nil)
		r.bars(0, pairsDiverge, a, b)
		r.requireHeld(1, -12)
		r.restart()
		r.candle(legA, pairsDiverge+1, a[pairsDiverge+1])
		r.requireHeld(1, -12, "the candle of leg B awaited")
		r.candle(legB, pairsDiverge+1, b[pairsDiverge+1])
		r.requireHeld(0, 0)
	})

	t.Run("its max bars counted from its entry", func(t *testing.T) {
		eps := map[int]float64{}
		for i := pairsDiverge; i < 40; i++ {
			eps[i] = -0.01
		}
		a, b := pairsPrices(40, eps)
		r := newPairsRunner(t, func(c *PairsConfig) { c.MaxHoldBars = 3 })
		r.bars(0, pairsDiverge+1, a, b)
		r.restart()
		r.bars(pairsDiverge+2, pairsDiverge+2, a, b)
		r.requireHeld(1, -12)
		r.bars(pairsDiverge+3, pairsDiverge+3, a, b)
		r.requireHeld(0, 0, "the third candle after the entry, not after the restart")
	})

	t.Run("a leg left alone by a close that failed", func(t *testing.T) {
		a, b := pairsPrices(40, map[int]float64{pairsDiverge: -0.01, pairsDiverge + 1: 0.003})
		r := newPairsRunner(t, nil)
		r.bars(0, pairsDiverge, a, b)
		r.wrap = func(stamps liveStamps) service.Broker { return failingLeg{liveStamps: stamps, pair: legA} }
		r.restart()
		r.bars(pairsDiverge+1, pairsDiverge+1, a, b)
		r.requireHeld(1, 0, "the close of leg A failed, leg B closed")
		r.wrap = nil
		r.restart()
		r.candle(legA, pairsDiverge+2, a[pairsDiverge+2])
		r.requireHeld(1, 0, "leg B awaited for its price")
		r.candle(legB, pairsDiverge+2, b[pairsDiverge+2])
		r.requireHeld(0, 0, "held alone, closed")
	})
}

// retrying is the broker of the strategy refusing every market order while
// refused.
type retrying struct {
	liveStamps
	refused *bool
}

func (b retrying) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	if *b.refused {
		return model.Order{}, errLegRefused
	}
	return b.liveStamps.CreateOrderMarket(side, pair, size)
}

func TestPairs_ExitRetried(t *testing.T) {
	a, b := pairsPrices(40, map[int]float64{pairsDiverge: -0.01, pairsDiverge + 1: 0.003})
	refused := false
	r := newPairsRunner(t, nil)
	r.wrap = func(stamps liveStamps) service.Broker { return retrying{liveStamps: stamps, refused: &refused} }
	r.begin()
	r.bars(0, pairsDiverge, a, b)
	r.requireHeld(1, -12)
	refused = true
	r.bars(pairsDiverge+1, pairsDiverge+1, a, b)
	r.requireHeld(1, -12, "both closes refused")
	refused = false
	r.candle(legA, pairsDiverge+2, a[pairsDiverge+2])
	r.requireHeld(0, 0, "retried on the next candle of either leg")
}

func TestPairs_FractionSizing(t *testing.T) {
	a, b := pairsPrices(40, map[int]float64{pairsDiverge: -0.01})
	r := newPairsRunner(t, func(c *PairsConfig) { c.PositionSize, c.PositionFraction = nil, 0.5 })
	r.bars(0, pairsDiverge, a, b)
	heldA, _, err := r.wallet.Position(legA)
	require.NoError(t, err)
	heldB, _, err := r.wallet.Position(legB)
	require.NoError(t, err)
	// half of the fraction of the free balance in each leg
	require.InDelta(t, 2500, heldA*a[pairsDiverge], 1e-3)
	require.InDelta(t, -2500, heldB*b[pairsDiverge], 1e-3)
}

// The hedge measures the spread the strategy decides on: on legs whose logs
// move 1.5 to 1, a divergence of the residual of the regression beyond -2 is
// no divergence of the log of the ratio, swinging with leg B: entered with
// PairsHedgeOLS only.
func TestPairs_TheSpreadOfTheHedge(t *testing.T) {
	a, b := pairsHedged(40, 1.5, map[int]float64{pairsDiverge: -0.006, pairsDiverge + 1: 0.003})
	for i := 21; i <= pairsDiverge+1; i++ {
		require.Less(t, math.Abs(refZ(a, b, i, 20)), 2.0, "the log of the ratio on %d", i)
		if i < pairsDiverge {
			require.Greater(t, refOLSZ(a, b, i, 20), -2.0, "the residual on %d", i)
		}
	}
	require.LessOrEqual(t, refOLSZ(a, b, pairsDiverge, 20), -2.0, "the residual diverging")
	require.GreaterOrEqual(t, refOLSZ(a, b, pairsDiverge+1, 20), 0.0, "back at its mean")
	require.InDelta(t, 1.5, refBeta(a, b, pairsDiverge, 20), 0.05)

	ratio := newPairsRunner(t, nil)
	ratio.bars(0, pairsDiverge+1, a, b)
	require.Empty(t, ratio.orders(), "the log of the ratio never at 2")

	ols := newPairsRunner(t, func(c *PairsConfig) { c.Hedge = PairsHedgeOLS })
	ols.bars(0, pairsDiverge, a, b)
	heldA, _, err := ols.wallet.Position(legA)
	require.NoError(t, err)
	heldB, _, err := ols.wallet.Position(legB)
	require.NoError(t, err)
	require.InDelta(t, 1, heldA, 1e-9, "A bought")
	require.Negative(t, heldB, "B sold")
	ols.bars(pairsDiverge+1, pairsDiverge+1, a, b)
	ols.requireHeld(0, 0, "back at its mean")
}

// With PairsHedgeOLS, leg B is worth β times leg A: the two legs sharing the
// fraction by value, or leg B rounded to the multiples of its size, its step,
// with sizes.
func TestPairs_OLSSizing(t *testing.T) {
	a, b := pairsHedged(40, 1.5, map[int]float64{pairsDiverge: -0.01})
	require.LessOrEqual(t, refOLSZ(a, b, pairsDiverge, 20), -2.0)
	beta := refBeta(a, b, pairsDiverge, 20)
	require.InDelta(t, 1.5, beta, 0.05)
	ols := func(sizes map[string]float64) func(*PairsConfig) {
		return func(c *PairsConfig) { c.Hedge, c.PositionSize = PairsHedgeOLS, sizes }
	}
	held := func(r *pairsRunner) (heldA, heldB float64) {
		heldA, _, err := r.wallet.Position(legA)
		require.NoError(t, err)
		heldB, _, err = r.wallet.Position(legB)
		require.NoError(t, err)
		return heldA, heldB
	}

	t.Run("by value", func(t *testing.T) {
		r := newPairsRunner(t, ols(nil))
		r.bars(0, pairsDiverge, a, b)
		heldA, heldB := held(r)
		// the fraction, half of the free balance, split 1 to β
		require.InDelta(t, 5000/(1+beta), heldA*a[pairsDiverge], 1e-3)
		require.InDelta(t, -5000*beta/(1+beta), heldB*b[pairsDiverge], 1e-3)
	})

	t.Run("leg B in steps of its size", func(t *testing.T) {
		r := newPairsRunner(t, ols(map[string]float64{legA: 1, legB: 0.5}))
		r.bars(0, pairsDiverge, a, b)
		want := math.Round(beta*a[pairsDiverge]/(b[pairsDiverge]*0.5)) * 0.5
		require.Greater(t, want, 10.0, "many steps of 0.5")
		r.requireHeld(1, -want, "A its size, B β times its value")
		require.InDelta(t, beta*a[pairsDiverge], want*b[pairsDiverge], 0.25*b[pairsDiverge], "within half a step")
	})

	t.Run("leg B rounding to nothing", func(t *testing.T) {
		r := newPairsRunner(t, ols(map[string]float64{legA: 1, legB: 1000}))
		r.bars(0, pairsDiverge, a, b)
		r.requireHeld(0, 0)
		require.Empty(t, r.orders())
	})

	t.Run("no size of leg B, its step", func(t *testing.T) {
		r := newPairsRunner(t, ols(map[string]float64{legA: 1}))
		r.bars(0, pairsDiverge, a, b)
		require.Empty(t, r.orders())
	})
}

// A hedge ratio that isn't positive, the legs moving apart, would hold both
// legs on the same side: no entry.
func TestPairs_OLSNegativeBeta(t *testing.T) {
	a, b := pairsHedged(40, -1, map[int]float64{pairsDiverge: -0.01})
	require.LessOrEqual(t, refOLSZ(a, b, pairsDiverge, 20), -2.0)
	require.Greater(t, refOLSZ(a, b, pairsDiverge-1, 20), -2.0)
	require.Negative(t, refBeta(a, b, pairsDiverge, 20))

	for _, sizes := range []map[string]float64{nil, {legA: 1, legB: 0.5}} {
		r := newPairsRunner(t, func(c *PairsConfig) { c.Hedge, c.PositionSize = PairsHedgeOLS, sizes })
		r.bars(0, pairsDiverge+2, a, b)
		require.Empty(t, r.orders(), "sizes %v", sizes)
	}
}

func TestPairs_Indicators(t *testing.T) {
	a, b := pairsPrices(40, map[int]float64{pairsDiverge: -0.01, pairsDiverge + 1: 0.003})
	r := newPairsRunner(t, nil)
	fresh, err := NewPairs(r.cfg)
	require.NoError(t, err)
	r.bars(0, 35, a, b)

	dataframe := func(pair string, closes []float64, step time.Duration) *model.Dataframe {
		df := &model.Dataframe{Pair: pair, Metadata: map[string]model.Series[float64]{}}
		for i := 0; i <= 35; i++ {
			df.Time = append(df.Time, r.start.Add(time.Duration(i)*step))
			df.Close = append(df.Close, closes[i])
		}
		return df
	}
	for _, leg := range []struct {
		pair   string
		closes []float64
	}{{legA, a}, {legB, b}} {
		df := dataframe(leg.pair, leg.closes, r.step)
		charts := r.s.Indicators(df)
		require.Len(t, charts, 1)
		z := df.Metadata["pr_z"]
		require.Len(t, z, 36)
		// the other leg holds its last 23 candles, 13 to 35: the windows of
		// 20 of them end on 32 to 35
		for i := range z {
			if i < 32 {
				require.True(t, isNa(z[i]), "%s %d", leg.pair, i)
				continue
			}
			require.InDelta(t, refZ(a, b, i, 20), z[i], 1e-9, "%s %d", leg.pair, i)
		}

		// another timeframe: none
		other := dataframe(leg.pair, leg.closes, 2*r.step)
		r.s.Indicators(other)
		for i, value := range other.Metadata["pr_z"] {
			require.True(t, isNa(value), "%s %d", leg.pair, i)
		}
		// no candle of the other leg yet: none
		df = dataframe(leg.pair, leg.closes, r.step)
		fresh.Indicators(df)
		for i, value := range df.Metadata["pr_z"] {
			require.True(t, isNa(value), "%s %d", leg.pair, i)
		}
	}
}

// Indicators, called by the chart while the strategy runs, only reads.
func TestPairs_IndicatorsWhileTrading(t *testing.T) {
	a, b := pairsPrices(60, map[int]float64{pairsDiverge: -0.01, pairsDiverge + 1: 0.003})
	r := newPairsRunner(t, nil)
	r.bars(0, 25, a, b)

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			df := &model.Dataframe{Pair: legA, Metadata: map[string]model.Series[float64]{}}
			for i := 0; i < 26; i++ {
				df.Time = append(df.Time, r.start.Add(time.Duration(i)*r.step))
				df.Close = append(df.Close, a[i])
			}
			r.s.Indicators(df)
		}
	}()
	r.bars(26, 59, a, b)
	close(done)
	wg.Wait()
	r.requireHeld(0, 0)
	require.Len(t, r.orders(), 4, "the spread opened and closed as without the chart")
}

// pairsRecorder is a Pairs recording the candles of each leg handed on each
// of its candles, by pair and time.
type pairsRecorder struct {
	*Pairs
	handed map[string]map[time.Time]pairsLeg
}

func (r *pairsRecorder) OnCandle(df *model.Dataframe, broker service.Broker) {
	if r.handed[df.Pair] == nil {
		r.handed[df.Pair] = map[time.Time]pairsLeg{}
	}
	r.handed[df.Pair][df.Time[len(df.Time)-1]] = pairsLeg{times: slices.Clone(df.Time),
		closes: slices.Clone(df.Close)}
	r.Pairs.OnCandle(df, broker)
}

// spreadAt returns the Z-Score of the spread of the candle at, and of the one
// before, and the hedge ratio, by gonum, from the candles the legs were
// handed on it, with the hedge of the strategy, see refZ and refOLSZ.
func (r *pairsRecorder) spreadAt(t *testing.T, at time.Time) (z, prev, beta float64) {
	t.Helper()
	a, okA := r.handed[legA][at]
	b, okB := r.handed[legB][at]
	require.True(t, okA && okB, "%s: the candles of both legs", at)
	joined := pairsJoin(a, b)
	closesA, closesB := make([]float64, len(joined.times)), make([]float64, len(joined.times))
	for k := range joined.times {
		closesA[k], closesB[k] = a.closes[joined.ia[k]], b.closes[joined.ib[k]]
	}
	n, length := len(joined.times), r.cfg.Len
	require.True(t, joined.times[n-1].Equal(at))
	require.Greater(t, n, length)
	if r.ols {
		return refOLSZ(closesA, closesB, n-1, length), refOLSZ(closesA, closesB, n-2, length),
			refBeta(closesA, closesB, n-1, length)
	}
	return refZ(closesA, closesB, n-1, length), refZ(closesA, closesB, n-2, length), 1
}

// pairsBacktest runs s on the 1h candles of files, by pair, through the bot as
// a backtest (the candles of a time in the alphabetical order of the pairs),
// and returns the orders filled, by their creation.
func pairsBacktest(t *testing.T, s strategy.Strategy, files map[string]string) []*model.Order {
	t.Helper()
	ctx := context.Background()
	csvFeed, err := exchange.NewCSVFeed(s.Timeframe(),
		exchange.PairFeed{Pair: legA, File: files[legA], Timeframe: "1h"},
		exchange.PairFeed{Pair: legB, File: files[legB], Timeframe: "1h"})
	require.NoError(t, err)

	store, err := storage.FromMemory()
	require.NoError(t, err)
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 10000),
		exchange.WithPaperFee(0.001, 0.001), exchange.WithDataFeed(csvFeed))

	bot, err := xtrader.NewBot(ctx, xtrader.Settings{Pairs: []string{legA, legB}}, wallet, s,
		xtrader.WithBacktest(wallet), xtrader.WithStorage(store), xtrader.WithLogLevel(log.ErrorLevel))
	require.NoError(t, err)
	require.NoError(t, bot.Run(ctx))

	orders, err := store.Orders(storage.WithStatusIn(model.OrderStatusTypeFilled))
	require.NoError(t, err)
	slices.SortFunc(orders, func(a, b *model.Order) int { return cmp.Compare(a.ID, b.ID) })
	return orders
}

// pairsTestData are the BTC and ETH candles of the tests, over the same dates
// (ETH a day later on both ends).
var pairsTestData = map[string]string{legA: "../../testdata/btc-1h.csv", legB: "../../testdata/eth-1h.csv"}

func newBTCETHPairs(t *testing.T, hedge string) *pairsRecorder {
	t.Helper()
	cfg := DefaultPairsConfig()
	cfg.Pairs, cfg.Hedge = []string{legA, legB}, hedge
	s, err := NewPairs(cfg)
	require.NoError(t, err)
	return &pairsRecorder{Pairs: s, handed: map[string]map[time.Time]pairsLeg{}}
}

// The defaults on half a year of BTC and ETH, with each hedge: the strategy
// trades, and after the orders of each candle the position is the spread or
// nothing: each leg opened and closed with the other, on the same candle, on
// the other side. Each spread opens on the cross of ±2 of the Z-Score of the
// spread of its hedge, as gonum computes it from the candles handed, its legs
// of equal value (the log of the ratio) or leg B worth β times leg A (the
// residual of the regression).
func TestPairs_Backtest(t *testing.T) {
	for _, hedge := range []string{PairsHedgeRatio, PairsHedgeOLS} {
		t.Run(hedge, func(t *testing.T) {
			s := newBTCETHPairs(t, hedge)
			orders := pairsBacktest(t, s, pairsTestData)

			held := map[string]float64{}
			entries, exits := 0, 0
			for i := 0; i < len(orders); {
				at := orders[i].CreatedAt
				before := map[string]float64{legA: held[legA], legB: held[legB]}
				values := map[string]float64{}
				var sideA model.SideType
				for ; i < len(orders) && orders[i].CreatedAt.Equal(at); i++ {
					require.Equal(t, model.OrderTypeMarket, orders[i].Type)
					quantity := orders[i].Quantity
					if orders[i].Side == model.SideTypeSell {
						quantity = -quantity
					}
					held[orders[i].Pair] += quantity
					values[orders[i].Pair] += orders[i].Quantity * orders[i].Price
					if orders[i].Pair == legA {
						sideA = orders[i].Side
					}
				}
				for _, pair := range []string{legA, legB} {
					if math.Abs(held[pair]) < 1e-9 {
						held[pair] = 0
					}
				}
				switch {
				case held[legA] == 0 && held[legB] == 0:
					require.NotZero(t, before[legA], "%s: an exit closes both legs", at)
					require.NotZero(t, before[legB], "%s: an exit closes both legs", at)
					exits++
				case held[legA]*held[legB] < 0:
					require.Zero(t, before[legA], "%s: one spread at a time", at)
					require.Zero(t, before[legB], "%s: one spread at a time", at)
					entries++

					z, prev, beta := s.spreadAt(t, at)
					if sideA == model.SideTypeBuy {
						require.LessOrEqual(t, z, -2.0, "%s: the long spread at -2", at)
						require.Greater(t, prev, -2.0, "%s: crossed", at)
					} else {
						require.GreaterOrEqual(t, z, 2.0, "%s: the short spread at 2", at)
						require.Less(t, prev, 2.0, "%s: crossed", at)
					}
					require.Positive(t, beta)
					require.InEpsilon(t, beta*values[legA], values[legB], 1e-5, "%s: leg B worth β times leg A",
						at)
				default:
					require.Failf(t, "not a spread", "%s: %s %g, %s %g", at, legA, held[legA], legB, held[legB])
				}
			}
			t.Logf("%s: %d spreads opened, %d closed", hedge, entries, exits)
			require.Positive(t, entries, "the strategy trades")
			require.LessOrEqual(t, entries-exits, 1, "the last spread may be open")
			require.GreaterOrEqual(t, entries-exits, 0)
		})
	}
}

// The orders of the backtest before a time are the same without the candles
// after it, with each hedge: no decision reads a candle after its own.
func TestPairs_BacktestNoLookahead(t *testing.T) {
	for _, hedge := range []string{PairsHedgeRatio, PairsHedgeOLS} {
		t.Run(hedge, func(t *testing.T) { requirePairsNoLookahead(t, hedge) })
	}
}

// requirePairsNoLookahead checks that the orders of the backtest of hedge
// before a time are the same without the candles after it.
func requirePairsNoLookahead(t *testing.T, hedge string) {
	t.Helper()
	full := pairsBacktest(t, newBTCETHPairs(t, hedge), pairsTestData)
	require.GreaterOrEqual(t, len(full), 8)

	for _, k := range []int{len(full) / 3, 2 * len(full) / 3} {
		// cut right after the candle of an order: its decision keeps the
		// candle it was decided on, nothing after
		cut := full[k].CreatedAt.Add(time.Hour)
		dir := t.TempDir()
		files := map[string]string{}
		for pair, file := range pairsTestData {
			files[pair] = filepath.Join(dir, filepath.Base(file))
			truncateCSV(t, file, files[pair], cut)
		}
		truncated := pairsBacktest(t, newBTCETHPairs(t, hedge), files)

		var want []*model.Order
		for _, order := range full {
			if order.CreatedAt.Before(cut) {
				want = append(want, order)
			}
		}
		require.Len(t, truncated, len(want), "cut at %s", cut)
		for i, order := range want {
			got := truncated[i]
			require.Equal(t, order.Pair, got.Pair, "order %d", i)
			require.Equal(t, order.Side, got.Side, "order %d", i)
			require.Equal(t, order.CreatedAt, got.CreatedAt, "order %d", i)
			require.InDelta(t, order.Quantity, got.Quantity, 1e-12, "order %d", i)
			require.InDelta(t, order.Price, got.Price, 1e-12, "order %d", i)
		}
	}
}

// truncateCSV copies the candles of the CSV file from before cut to the file
// to.
func truncateCSV(t *testing.T, from, to string, cut time.Time) {
	t.Helper()
	in, err := os.Open(from)
	require.NoError(t, err)
	defer in.Close()

	var kept strings.Builder
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		line := scanner.Text()
		at, err := strconv.ParseInt(strings.SplitN(line, ",", 2)[0], 10, 64)
		require.NoError(t, err)
		if time.Unix(at, 0).Before(cut) {
			kept.WriteString(line + "\n")
		}
	}
	require.NoError(t, scanner.Err())
	require.NoError(t, os.WriteFile(to, []byte(kept.String()), 0o600))
}
