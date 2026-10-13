package strategies

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

func newFibonacci(t *testing.T, configure func(*FibonacciConfig)) *Fibonacci {
	t.Helper()
	cfg := DefaultFibonacciConfig()
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewFibonacci(cfg)
	require.NoError(t, err)
	return s
}

func TestNewFibonacci_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*FibonacciConfig)
		err       string
	}{
		{"timeframe", func(c *FibonacciConfig) { c.Timeframe = "" }, "timeframe"},
		{"swing", func(c *FibonacciConfig) { c.Swing = "zigzag" }, "invalid swing"},
		{"pivot period", func(c *FibonacciConfig) { c.PivotLen = 0 }, "at least 1"},
		{"lookback without a swing", func(c *FibonacciConfig) { c.Lookback = 11 }, "lookback"},
		{"lookback of a range", func(c *FibonacciConfig) { c.Swing, c.Lookback = FibSwingRange, 2 }, "lookback"},
		{"negative impulse", func(c *FibonacciConfig) { c.MinImpulseATR = -1 }, "ATR multiples"},
		{"negative stop buffer", func(c *FibonacciConfig) { c.StopBufferATR = -1 }, "ATR multiples"},
		{"negative trailing", func(c *FibonacciConfig) { c.TrailingATR = -1 }, "trailing"},
		{"negative trend EMA", func(c *FibonacciConfig) { c.TrendEMA = -1 }, "trend EMA"},
		{"zone crossed", func(c *FibonacciConfig) { c.EntryLow = 0.7 }, "levels"},
		{"zone at the extreme", func(c *FibonacciConfig) { c.EntryLow = 0 }, "levels"},
		{"stop within the zone", func(c *FibonacciConfig) { c.StopLevel = 0.6 }, "levels"},
		{"stop beyond the origin", func(c *FibonacciConfig) { c.StopLevel = 1.1 }, "levels"},
		{"trigger", func(c *FibonacciConfig) { c.Trigger = "touch" }, "invalid trigger"},
		{"limit out of the zone", func(c *FibonacciConfig) { c.Trigger, c.LimitLevel = FibTriggerLimit, 0.7 },
			"limit level"},
		{"negative pending bars", func(c *FibonacciConfig) { c.PendingBars = -1 }, "pending bars"},
		{"target", func(c *FibonacciConfig) { c.Target = "far" }, "invalid target"},
		{"extension within the impulse", func(c *FibonacciConfig) { c.Target, c.TargetLevel = FibTargetExtension, 1 },
			"extension"},
		{"reward to risk", func(c *FibonacciConfig) { c.Target, c.RewardRisk = FibTargetRR, 0 }, "reward to risk"},
		{"no target without trailing", func(c *FibonacciConfig) { c.Target = FibTargetNone }, "trailing stop"},
		{"the exits", func(c *FibonacciConfig) { c.MaxBars = -1 }, "max bars"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultFibonacciConfig()
			tt.configure(&cfg)
			_, err := NewFibonacci(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}

	// the ATR multiples of the bracket aren't used
	newFibonacci(t, func(c *FibonacciConfig) { c.StopATR, c.TakeProfitATR = 0, 0 })
	newFibonacci(t, func(c *FibonacciConfig) { c.Target, c.TrailingATR = FibTargetNone, 2 })
}

func TestFibonacci_WarmupPeriod(t *testing.T) {
	// the lookback, with the ATR at its first candle seeded
	require.Equal(t, 300+seedWarmup(14)+1, newFibonacci(t, nil).WarmupPeriod())
	require.Equal(t, 50+seedWarmup(14)+1, newFibonacci(t, func(c *FibonacciConfig) { c.Lookback = 50 }).WarmupPeriod())
	// the trend EMA seeded, longer
	require.Equal(t, seedWarmup(200)+1, newFibonacci(t, func(c *FibonacciConfig) { c.TrendEMA = 200 }).WarmupPeriod())
}

// fibUp is an impulse up of 10 from the swing low at 98 (candle 2) to the
// swing high at 108 (candle 6), known on its last candle with swings of 1
// candle: its 50% at 103, its 61.8% at 101.82, its 78.6% at 100.14.
var fibUp = []ivCandle{
	{open: 101, close: 100.5, high: 101, low: 100},
	{open: 100.5, close: 100, high: 100.5, low: 99.5},
	{open: 99.5, close: 98.5, high: 99.6, low: 98},
	{open: 98.5, close: 100, high: 100.5, low: 98.4},
	{open: 100, close: 102, high: 102.5, low: 99.8},
	{open: 102, close: 104, high: 104.5, low: 101.8},
	{open: 104, close: 107, high: 108, low: 103.8},
	{open: 107, close: 106, high: 107, low: 105.5},
}

// fibReject is a candle rejecting the zone of fibUp: its low at 102.5, below
// 50%, closing up at 103.5, above 61.8%, a BUY of 10.
var fibReject = ivCandle{open: 103.2, close: 103.5, high: 103.8, low: 102.5, position: 10}

// fibMirror returns candles mirrored around 100, the impulses up as impulses
// down, the positions the other side.
func fibMirror(candles ...ivCandle) []ivCandle {
	mirrored := make([]ivCandle, len(candles))
	for i, c := range candles {
		mirrored[i] = ivCandle{open: 200 - c.open, close: 200 - c.close, high: 200 - c.low, low: 200 - c.high,
			position: -c.position}
	}
	return mirrored
}

// newFibRunner feeds a Fibonacci of swings of 1 candle, the ATR at 1, trading
// positions of 10.
func newFibRunner(t *testing.T, configure func(*FibonacciConfig)) (*ivRunner, *Fibonacci) {
	t.Helper()
	s := newFibonacci(t, func(c *FibonacciConfig) {
		c.PivotLen = 1
		c.PositionSize = map[string]float64{"BTCUSDT": 10}
		if configure != nil {
			configure(c)
		}
	})
	r := newIVRunner(t, s, 0)
	r.fixed["fib_atr"] = 1
	return r, s
}

// fibCandles is fibUp, then candles.
func fibCandles(candles ...ivCandle) []ivCandle {
	return append(fibUp[:len(fibUp):len(fibUp)], candles...)
}

func TestFibonacci_Entries(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*FibonacciConfig)
		candles   []ivCandle
	}{
		{name: "buy on the rejection of the zone of an impulse up", candles: fibCandles(fibReject)},
		{name: "sell on the rejection of the zone of an impulse down", candles: fibMirror(fibCandles(fibReject)...)},
		{
			name: "no entry on a close beyond the zone",
			candles: fibCandles(ivCandle{open: 101, close: 101.5, high: 102, low: 100.5},
				ivCandle{open: 101.5, close: 101.7, high: 101.9, low: 101.2}),
		},
		{
			name:    "no entry without the wick reaching the zone",
			candles: fibCandles(ivCandle{open: 103.4, close: 104, high: 104.2, low: 103.2}),
		},
		{
			name:    "no entry on a candle closing against the impulse",
			candles: fibCandles(ivCandle{open: 103.8, close: 103.5, high: 104, low: 102.5}),
		},
		{
			name:      "no entry on an impulse too short",
			configure: func(c *FibonacciConfig) { c.MinImpulseATR = 11 },
			candles:   fibCandles(ivCandle{open: 103.2, close: 103.5, high: 103.8, low: 102.5}),
		},
		{
			name:      "no entry on the side the direction keeps out",
			configure: func(c *FibonacciConfig) { c.Direction = RKDirectionSellOnly },
			candles:   fibCandles(ivCandle{open: 103.2, close: 103.5, high: 103.8, low: 102.5}),
		},
		{
			name:      "no entry while the ATR is too small for the costs",
			configure: func(c *FibonacciConfig) { c.MinATRPct = 2 },
			candles:   fibCandles(ivCandle{open: 103.2, close: 103.5, high: 103.8, low: 102.5}),
		},
		{
			name: "no entry once a close went beyond the origin",
			candles: fibCandles(ivCandle{open: 105, close: 97.5, high: 105, low: 97},
				ivCandle{open: 103.2, close: 103.5, high: 103.8, low: 102.5}),
		},
		{
			name:      "the impulse of the range of the lookback",
			configure: func(c *FibonacciConfig) { c.Swing, c.Lookback = FibSwingRange, 8 },
			candles:   fibCandles(fibReject),
		},
		{
			name:      "a wider zone",
			configure: func(c *FibonacciConfig) { c.EntryLow, c.EntryHigh = 0.382, 0.5 },
			// its 38.2% at 104.18, its 50% at 103
			candles: fibCandles(ivCandle{open: 103.8, close: 104.2, high: 104.4, low: 104, position: 10}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newFibRunner(t, tt.configure)
			r.feed(tt.candles...)
		})
	}
}

func TestFibonacci_TheSwingIsKnownLater(t *testing.T) {
	// with swings of 2 candles, the high at 108 is known 2 candles later: the
	// first rejection comes before, the second one is entered
	r, _ := newFibRunner(t, func(c *FibonacciConfig) { c.PivotLen = 2 })
	r.feed(fibUp[:7]...)
	r.feed(ivCandle{open: 103.2, close: 103.5, high: 103.8, low: 102.5},
		ivCandle{open: 103.3, close: 103.6, high: 103.9, low: 102.6, position: 10})
}

func TestFibonacci_TrendEMA(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ema      float64
		position float64
	}{
		{"the close above the EMA", 100, 10},
		{"the close below the EMA", 110, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newFibRunner(t, func(c *FibonacciConfig) { c.TrendEMA = 50 })
			r.fixed["fib_ema"] = tc.ema
			reject := fibReject
			reject.position = tc.position
			r.feed(fibCandles(reject)...)
		})
	}
}

func TestFibonacci_Exits(t *testing.T) {
	for _, tc := range []struct {
		name         string
		configure    func(*FibonacciConfig)
		mirror       bool
		stop, profit float64
	}{
		// beyond 78.6% by 0.25 ATR, at the extreme
		{"long", nil, false, 100.14 - 0.25, 108},
		{"short", nil, true, 200 - 100.14 + 0.25, 92},
		{"the stop loss at the origin", func(c *FibonacciConfig) { c.StopLevel, c.StopBufferATR = 1, 0.5 }, false,
			97.5, 108},
		// 1.272 times the impulse from its origin
		{"at an extension", func(c *FibonacciConfig) { c.Target = FibTargetExtension }, false, 99.89, 110.72},
		{"at an extension, short", func(c *FibonacciConfig) { c.Target = FibTargetExtension }, true, 100.11,
			200 - 110.72},
		// 1.5 times the 3.61 risked
		{"at a reward to risk", func(c *FibonacciConfig) { c.Target, c.RewardRisk = FibTargetRR, 1.5 }, false, 99.89,
			103.5 + 1.5*3.61},
		// a loss of 20 and a profit of 30 on 10
		{"at fixed values", func(c *FibonacciConfig) { c.StopValue, c.TakeProfitValue = 20, 30 }, false, 101.5,
			106.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := newFibRunner(t, tc.configure)
			candles := fibCandles(fibReject)
			if tc.mirror {
				candles = fibMirror(candles...)
			}
			r.feed(candles...)
			legs := s.bracket.trades["BTCUSDT"].legs
			require.Len(t, legs, 2)
			takeProfit, stopLoss := legs[0], legs[1]
			require.Equal(t, model.OrderTypeLimitMaker, takeProfit.Type)
			require.InDelta(t, tc.profit, takeProfit.Price, 1e-9)
			require.Equal(t, model.OrderTypeStopLoss, stopLoss.Type)
			require.InDelta(t, tc.stop, *stopLoss.Stop, 1e-9)
		})
	}

	t.Run("no take profit, the stop loss trailing", func(t *testing.T) {
		r, s := newFibRunner(t, func(c *FibonacciConfig) { c.Target, c.TrailingATR = FibTargetNone, 1 })
		r.feed(fibCandles(fibReject)...)
		legs := s.bracket.trades["BTCUSDT"].legs
		require.Len(t, legs, 1)
		require.Equal(t, model.OrderTypeStopLoss, legs[0].Type)

		// the best price at 106.5: the stop loss 1 ATR behind it
		r.feed(ivCandle{open: 103.5, close: 106, high: 106.5, low: 103.4, position: 10})
		require.InDelta(t, 105.5, *s.bracket.trades["BTCUSDT"].legs[0].Stop, 1e-9)
	})
}

func TestFibonacci_TheTakeProfitAndTheStopLoss(t *testing.T) {
	t.Run("the take profit at the extreme", func(t *testing.T) {
		r, _ := newFibRunner(t, nil)
		r.feed(fibCandles(fibReject, ivCandle{open: 104, close: 106, high: 108.2, low: 103.9})...)
		require.InDelta(t, 10000+10*(108-103.5), r.quote(), 1e-9)
	})

	t.Run("the stop loss, the impulse entered once", func(t *testing.T) {
		r, s := newFibRunner(t, nil)
		r.feed(fibCandles(fibReject, ivCandle{open: 103, close: 100.5, high: 103.2, low: 99.5})...)
		require.InDelta(t, 10000-10*(103.5-99.89), r.quote(), 1e-9)
		// another rejection of the zone of the same impulse
		r.feed(ivCandle{open: 102.6, close: 103.4, high: 103.6, low: 102.4})
		require.Empty(t, s.bracket.trades)
	})
}

func TestFibonacci_Limit(t *testing.T) {
	limit := func(c *FibonacciConfig) { c.Trigger = FibTriggerLimit }

	t.Run("placed once the impulse is known, filled in the zone", func(t *testing.T) {
		r, s := newFibRunner(t, limit)
		r.feed(fibUp...)
		p := s.pending["BTCUSDT"]
		require.NotNil(t, p)
		require.Equal(t, model.OrderTypeLimit, p.entry.Type)
		require.InDelta(t, 101.82, p.entry.Price, 1e-9)

		r.feed(ivCandle{open: 104, close: 102.5, high: 104.5, low: 101.5, position: 10})
		require.Empty(t, s.pending)
		legs := s.bracket.trades["BTCUSDT"].legs
		require.Len(t, legs, 2)
		require.InDelta(t, 108, legs[0].Price, 1e-9)
		require.InDelta(t, 99.89, *legs[1].Stop, 1e-9)

		// the take profit closes it
		r.feed(ivCandle{open: 103, close: 107.5, high: 108.5, low: 102.8})
		require.InDelta(t, 10000+10*(108-101.82), r.quote(), 1e-9)
	})

	t.Run("short", func(t *testing.T) {
		r, s := newFibRunner(t, limit)
		r.feed(fibMirror(fibCandles(ivCandle{open: 104, close: 102.5, high: 104.5, low: 101.5, position: 10})...)...)
		legs := s.bracket.trades["BTCUSDT"].legs
		require.Len(t, legs, 2)
		require.InDelta(t, 92, legs[0].Price, 1e-9)
		require.InDelta(t, 100.11, *legs[1].Stop, 1e-9)
	})

	for _, tc := range []struct {
		name      string
		configure func(*FibonacciConfig)
		candles   []ivCandle
		next      float64 // the limit placed on the next candle, 0 for none
	}{
		// the next candle makes the high at 108.5 a swing: the impulse to it is
		// another one, its 61.8% at 102.011
		{"canceled on a price beyond the extreme", nil,
			[]ivCandle{{open: 106, close: 107.5, high: 108.5, low: 105.8}}, 102.011},
		{"canceled after the pending bars", func(c *FibonacciConfig) { c.PendingBars = 2 },
			[]ivCandle{{open: 106, close: 105, high: 106.2, low: 104.5}, {open: 105, close: 104.8, high: 105.5,
				low: 104.2}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := newFibRunner(t, func(c *FibonacciConfig) {
				limit(c)
				if tc.configure != nil {
					tc.configure(c)
				}
			})
			r.feed(fibUp...)
			entry := s.pending["BTCUSDT"].entry
			r.feed(tc.candles...)
			require.Empty(t, s.pending)
			got, err := r.wallet.Order("BTCUSDT", entry.ExchangeID)
			require.NoError(t, err)
			require.Equal(t, model.OrderStatusTypeCanceled, got.Status)

			// the impulse entered once: no other limit for it
			r.feed(ivCandle{open: 104.8, close: 105, high: 105.2, low: 104.6})
			if tc.next == 0 {
				require.Empty(t, s.pending)
				return
			}
			require.NotNil(t, s.pending["BTCUSDT"])
			require.InDelta(t, tc.next, s.pending["BTCUSDT"].entry.Price, 1e-9)
		})
	}

	t.Run("not placed with the price already in the zone", func(t *testing.T) {
		r, s := newFibRunner(t, func(c *FibonacciConfig) {
			limit(c)
			c.LimitLevel = 0.5
		})
		r.feed(fibUp[:7]...)
		r.feed(ivCandle{open: 107, close: 102.8, high: 107, low: 102.6})
		require.Empty(t, s.pending)
	})
}

func TestFibonacci_Legs(t *testing.T) {
	s := newFibonacci(t, func(c *FibonacciConfig) { c.PivotLen = 1 })
	r := newIVRunner(t, s, 0)
	r.fixed["fib_atr"] = 1
	r.feed(fibUp...)

	legs := s.legs(r.df)
	require.Equal(t, []fibLeg{{side: model.SideTypeBuy, origin: 98, extreme: 108, from: 2, to: 6}}, legs)
	leg := legs[0]
	require.InDelta(t, 103, leg.level(0.5), 1e-9)
	require.InDelta(t, 101.82, leg.level(0.618), 1e-9)
	require.InDelta(t, 110.72, leg.level(1-1.272), 1e-9)
	require.True(t, s.holds(r.df, leg, len(r.df.Close)-1))
	require.True(t, s.long(r.df, leg))

	// a price beyond the extreme ends it, as a close beyond the origin does
	r.feed(ivCandle{open: 106, close: 106.5, high: 108.5, low: 105.8})
	require.False(t, s.holds(r.df, leg, len(r.df.Close)-1))
	require.True(t, s.holds(r.df, leg, len(r.df.Close)-2))
}

func TestFibonacci_Indicators(t *testing.T) {
	s := newFibonacci(t, func(c *FibonacciConfig) {
		c.PivotLen, c.ATRLen, c.MinImpulseATR, c.Target = 1, 2, 1, FibTargetExtension
	})
	r := newIVRunner(t, s, 0)
	r.feed(fibUp...)

	values := map[string][]float64{}
	for _, indicator := range s.Indicators(r.df) {
		for _, metric := range indicator.Metrics {
			values[metric.Name] = metric.Values
		}
	}
	last := len(r.df.Close) - 1
	for name, want := range map[string]float64{"Fib 0": 108, "Fib 0.5": 103, "Fib 0.618": 101.82,
		"Fib 0.786": 100.14, "Fib 1": 98, "Fib target 1.272": 110.72} {
		require.Len(t, values[name], len(r.df.Close), name)
		require.InDelta(t, want, values[name][last], 1e-9, name)
		require.InDelta(t, want, values[name][2], 1e-9, "%s: from the origin", name)
		require.True(t, math.IsNaN(values[name][1]), "%s: not before the origin", name)
	}
	require.InDelta(t, 108, values["Swing high"][6], 1e-9)
	require.InDelta(t, 98, values["Swing low"][2], 1e-9)
	require.Contains(t, values, "ATR")

	// no impulse long enough: no levels
	s.cfg.MinImpulseATR = 100
	for _, indicator := range s.Indicators(r.df) {
		for _, metric := range indicator.Metrics {
			if metric.Name == "Fib 0" {
				require.True(t, math.IsNaN(metric.Values[last]))
			}
		}
	}
}

func TestFibonacci_ForgetsTheImpulsesOutOfTheWindow(t *testing.T) {
	s := newFibonacci(t, nil)
	at := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	s.traded["BTCUSDT"] = map[fibKey]bool{
		{side: model.SideTypeBuy, from: at, to: at.Add(time.Hour)}:         true,
		{side: model.SideTypeBuy, from: at, to: at.Add(3 * time.Hour)}:     true,
		{side: model.SideTypeSell, from: at, to: at.Add(30 * time.Minute)}: true,
	}
	s.forget(&model.Dataframe{Pair: "BTCUSDT", Time: []time.Time{at.Add(time.Hour), at.Add(2 * time.Hour)}})
	require.Len(t, s.traded["BTCUSDT"], 2)
}

// TestFibonacci_Backtest runs Fibonacci on the 1h candles of the test data:
// each trade a market entry on a rejection, placed with its stop loss and take
// profit, then a single exit.
func TestFibonacci_Backtest(t *testing.T) {
	if testing.Short() {
		t.Skip("long: left out by -short")
	}
	s := newFibonacci(t, func(c *FibonacciConfig) { c.Timeframe = "1h" })
	checkBacktest(t, s)
}
