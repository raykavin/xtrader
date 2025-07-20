package strategies

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func newEMAPullback(t *testing.T, configure func(*EMAPullbackConfig)) *EMAPullback {
	t.Helper()
	cfg := DefaultEMAPullbackConfig()
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewEMAPullback(cfg)
	require.NoError(t, err)
	return s
}

func TestNewEMAPullback_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*EMAPullbackConfig)
		err       string
	}{
		{"timeframe", func(c *EMAPullbackConfig) { c.Timeframe = "" }, "timeframe"},
		{"zero period", func(c *EMAPullbackConfig) { c.FastLen = 0 }, "at least 1"},
		{"trend bars", func(c *EMAPullbackConfig) { c.TrendBars = 0 }, "at least 1"},
		{"pullback bars", func(c *EMAPullbackConfig) { c.PullbackBars = 0 }, "at least 1"},
		{"EMAs crossed", func(c *EMAPullbackConfig) { c.FastLen = 50 }, "strictly shorter"},
		{"negative pullback", func(c *EMAPullbackConfig) { c.PullbackATR = -1 }, "pullback ATR"},
		{"negative RSI period", func(c *EMAPullbackConfig) { c.RSILen = -1 }, "RSI period"},
		{"momentum level", func(c *EMAPullbackConfig) { c.Momentum = 100 }, "momentum"},
		{"the exits", func(c *EMAPullbackConfig) { c.MinPositionValue = -1 }, "min position value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultEMAPullbackConfig()
			tt.configure(&cfg)
			_, err := NewEMAPullback(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}
}

func TestEMAPullback_WarmupPeriod(t *testing.T) {
	// the slow EMA seeded, then the candles of the trend and the pullback
	require.Equal(t, seedWarmup(50)+5+1, newEMAPullback(t, nil).WarmupPeriod())
}

// newEPRunner feeds an EMAPullback over 2 candles of trend and of pullback, with
// the ATR at 1.
func newEPRunner(t *testing.T, configure func(*EMAPullbackConfig)) *ivRunner {
	t.Helper()
	r := newIVRunner(t, newEMAPullback(t, func(c *EMAPullbackConfig) {
		c.TrendBars, c.PullbackBars = 2, 2
		if configure != nil {
			configure(c)
		}
	}), 0)
	r.fixed["ep_atr"] = 1
	return r
}

// ep is a candle of an EMAPullback test and the values of its EMAs and RSI.
func ep(candle ivCandle, fast, slow, rsi float64) ivCandle {
	candle.meta = map[string]float64{"ep_fast": fast, "ep_slow": slow, "ep_rsi": rsi}
	return candle
}

// the setups of the tests: a trend, a pullback to the fast EMA, and a candle
// closing at 100 beyond the previous one
var (
	epLong = []ivCandle{
		ep(ivCandle{close: 100}, 99, 97, 50),
		ep(ivCandle{close: 99.3, high: 99.5, low: 99.0}, 99.1, 97.1, 45),
		ep(ivCandle{open: 99.4, close: 100, position: 100}, 99.2, 97.2, 55),
	}
	epShort = []ivCandle{
		ep(ivCandle{close: 100}, 101, 103, 50),
		ep(ivCandle{close: 100.7, high: 101.0, low: 100.5}, 100.9, 102.9, 55),
		ep(ivCandle{open: 100.6, close: 100, position: -100}, 100.8, 102.8, 45),
	}
)

// epVariant is the long setup with its candle i replaced by candle, and no
// entry.
func epVariant(i int, candle ivCandle) []ivCandle {
	candles := append([]ivCandle(nil), epLong...)
	candles[i] = candle
	candles[2].position = 0
	return candles
}

func TestEMAPullback_Entries(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*EMAPullbackConfig)
		candles   []ivCandle
	}{
		{name: "buy on a pullback to the fast EMA in an uptrend", candles: epLong},
		{name: "sell on a pullback to the fast EMA in a downtrend", candles: epShort},
		{
			name:    "no entry without the fast EMA above the slow one",
			candles: epVariant(1, ep(ivCandle{close: 99.3, high: 99.5, low: 99.0}, 99.1, 99.2, 45)),
		},
		{
			name:    "no entry with the fast EMA falling",
			candles: epVariant(2, ep(ivCandle{open: 99.4, close: 100}, 98.9, 97.2, 55)),
		},
		{
			name:    "no entry without a pullback",
			candles: epVariant(1, ep(ivCandle{close: 99.4, high: 99.5, low: 99.4}, 99.1, 97.1, 45)),
		},
		{
			name:      "the pullback distance is configurable",
			configure: func(c *EMAPullbackConfig) { c.PullbackATR = 0.4 },
			candles: []ivCandle{
				epLong[0],
				ep(ivCandle{close: 99.4, high: 99.5, low: 99.4}, 99.1, 97.1, 45), // 0.3 above the fast EMA
				epLong[2],
			},
		},
		{
			name:    "no entry on a pullback closing beyond the slow EMA",
			candles: epVariant(1, ep(ivCandle{open: 99.3, close: 97.0, high: 99.3, low: 97.0}, 99.1, 97.1, 45)),
		},
		{
			name:    "no entry without a close above the previous high",
			candles: epVariant(2, ep(ivCandle{open: 99.3, close: 99.45}, 99.2, 97.2, 55)),
		},
		{
			name:    "no entry on a candle down",
			candles: epVariant(2, ep(ivCandle{open: 100.2, close: 100}, 99.2, 97.2, 55)),
		},
		{
			name:    "no entry with a weak momentum",
			candles: epVariant(2, ep(ivCandle{open: 99.4, close: 100}, 99.2, 97.2, 49)),
		},
		{
			name: "no entry with a falling momentum",
			candles: []ivCandle{
				epLong[0],
				ep(ivCandle{close: 99.3, high: 99.5, low: 99.0}, 99.1, 97.1, 60),
				ep(ivCandle{open: 99.4, close: 100}, 99.2, 97.2, 55),
			},
		},
		{
			name:      "the momentum level is configurable",
			configure: func(c *EMAPullbackConfig) { c.Momentum = 60 },
			candles:   epVariant(2, ep(ivCandle{open: 99.4, close: 100}, 99.2, 97.2, 55)),
		},
		{
			name:      "no momentum filter with RSILen at 0",
			configure: func(c *EMAPullbackConfig) { c.RSILen = 0 },
			candles: []ivCandle{
				epLong[0], epLong[1],
				ep(ivCandle{open: 99.4, close: 100, position: 100}, 99.2, 97.2, 10),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newEPRunner(t, tt.configure).feed(tt.candles...)
		})
	}
}

func TestEMAPullback_Exits(t *testing.T) {
	t.Run("the close back across the slow EMA closes the long", func(t *testing.T) {
		r := newEPRunner(t, nil)
		r.feed(epLong...)
		r.feed(ep(ivCandle{close: 99.3, high: 99.8, low: 99.2, position: 0}, 99.5, 99.4, 40))
		require.InDelta(t, 10000-100*0.7, r.quote(), 1e-9)
	})

	t.Run("the close back across the slow EMA closes the short", func(t *testing.T) {
		r := newEPRunner(t, nil)
		r.feed(epShort...)
		r.feed(ep(ivCandle{close: 100.7, high: 100.8, low: 100.2, position: 0}, 100.5, 100.6, 60))
		require.InDelta(t, 10000-100*0.7, r.quote(), 1e-9)
	})

	t.Run("a close above the slow EMA keeps the long", func(t *testing.T) {
		r := newEPRunner(t, nil)
		r.feed(epLong...)
		r.feed(ep(ivCandle{close: 99.5, position: 100}, 99.3, 97.3, 45))
	})
}
