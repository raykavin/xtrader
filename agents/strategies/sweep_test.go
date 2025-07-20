package strategies

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newLiquiditySweep(t *testing.T, configure func(*LiquiditySweepConfig)) *LiquiditySweep {
	t.Helper()
	cfg := DefaultLiquiditySweepConfig()
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewLiquiditySweep(cfg)
	require.NoError(t, err)
	return s
}

func TestNewLiquiditySweep_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*LiquiditySweepConfig)
		err       string
	}{
		{"timeframe", func(c *LiquiditySweepConfig) { c.Timeframe = "" }, "timeframe"},
		{"swing period", func(c *LiquiditySweepConfig) { c.SwingLen = 0 }, "at least 1"},
		{"sweep bars", func(c *LiquiditySweepConfig) { c.SweepBars = 0 }, "at least 1"},
		{"lookback without a swing", func(c *LiquiditySweepConfig) { c.Lookback = 6 }, "lookback"},
		{"negative sweep", func(c *LiquiditySweepConfig) { c.MinSweepATR = -1 }, "sweep ATR"},
		{"negative confirmation", func(c *LiquiditySweepConfig) { c.ConfirmATR = -1 }, "sweep ATR"},
		{"sweep limits crossed", func(c *LiquiditySweepConfig) { c.MinSweepATR = 2 }, "min sweep"},
		{"timezone", func(c *LiquiditySweepConfig) { c.DayLevels, c.Timezone = true, "Moon" }, "invalid timezone"},
		{"the exits", func(c *LiquiditySweepConfig) { c.MaxBars = -1 }, "max bars"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultLiquiditySweepConfig()
			tt.configure(&cfg)
			_, err := NewLiquiditySweep(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}
}

func TestLiquiditySweep_WarmupPeriod(t *testing.T) {
	// the seed of the ATR, longer than the lookback
	require.Equal(t, seedWarmup(14)+1, newLiquiditySweep(t, nil).WarmupPeriod())
	require.Equal(t, 200+3+1, newLiquiditySweep(t, func(c *LiquiditySweepConfig) { c.Lookback = 200 }).WarmupPeriod())
	// two days and an hour of 5m candles for the previous day
	require.Equal(t, 49*12+3+1, newLiquiditySweep(t, func(c *LiquiditySweepConfig) { c.DayLevels = true }).WarmupPeriod())
}

// newLSRunner feeds a LiquiditySweep of swings of 1 candle, the ATR at 1.
func newLSRunner(t *testing.T, configure func(*LiquiditySweepConfig)) *ivRunner {
	t.Helper()
	r := newIVRunner(t, newLiquiditySweep(t, func(c *LiquiditySweepConfig) {
		c.SwingLen, c.Lookback, c.SweepBars = 1, 10, 2
		if configure != nil {
			configure(c)
		}
	}), 0)
	r.fixed["ls_atr"] = 1
	return r
}

// the structures of the tests: a swing low at 99 for a long, and a swing high
// at 101 for a short
var (
	lsLows = []ivCandle{
		{close: 101, high: 101.5, low: 100.5},
		{close: 100.5, high: 101, low: 99},
		{close: 101, high: 101.5, low: 100},
		{close: 101.5, high: 102, low: 100.8},
	}
	lsHighs = []ivCandle{
		{close: 99, high: 99.5, low: 98.5},
		{close: 99.5, high: 101, low: 99},
		{close: 99, high: 100, low: 98.5},
		{close: 98.5, high: 99.2, low: 98},
	}
)

// lsSweep is the swing low, then candles.
func lsSweep(candles ...ivCandle) []ivCandle {
	return append(lsLows[:4:4], candles...)
}

func TestLiquiditySweep_Entries(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*LiquiditySweepConfig)
		candles   []ivCandle
	}{
		{
			name:    "buy on the reclaim of a swing low swept by a wick",
			candles: lsSweep(ivCandle{open: 99.2, close: 100, low: 98.6, high: 100.1, position: 100}),
		},
		{
			name: "sell on the reclaim of a swing high swept by a wick",
			candles: append(lsHighs[:4:4],
				ivCandle{open: 100.8, close: 100, high: 101.4, low: 99.9, position: -100}),
		},
		{
			name: "buy on the close back above the swept low within the sweep bars",
			candles: lsSweep(
				ivCandle{open: 99.5, close: 98.8, low: 98.6, high: 99.6},
				ivCandle{open: 98.9, close: 100, low: 98.8, high: 100.1, position: 100},
			),
		},
		{
			name:      "no entry after the sweep bars",
			configure: func(c *LiquiditySweepConfig) { c.SweepBars = 1 },
			candles: lsSweep(
				ivCandle{open: 99.5, close: 98.8, low: 98.6, high: 99.6},
				ivCandle{open: 98.9, close: 100, low: 98.8, high: 100.1},
			),
		},
		{
			name:    "no entry on a wick too deep: a breakout",
			candles: lsSweep(ivCandle{open: 99.2, close: 100, low: 97, high: 100.1}),
		},
		{
			name:      "no limit to the depth at 0",
			configure: func(c *LiquiditySweepConfig) { c.MaxSweepATR = 0 },
			candles:   lsSweep(ivCandle{open: 99.2, close: 100, low: 97, high: 100.1, position: 100}),
		},
		{
			name:      "no entry on a wick too shallow",
			configure: func(c *LiquiditySweepConfig) { c.MinSweepATR = 0.5 }, // 0.4 below the low
			candles:   lsSweep(ivCandle{open: 99.2, close: 100, low: 98.6, high: 100.1}),
		},
		{
			name:    "no entry without the close back above the level",
			candles: lsSweep(ivCandle{open: 98.7, close: 98.9, low: 98.6, high: 99}),
		},
		{
			name:    "no entry on a candle down",
			candles: lsSweep(ivCandle{open: 100.2, close: 100, low: 98.6, high: 100.3}),
		},
		{
			name:      "no entry on a weak reversal",
			configure: func(c *LiquiditySweepConfig) { c.ConfirmATR = 2 }, // 1.4 from the extreme
			candles:   lsSweep(ivCandle{open: 99.2, close: 100, low: 98.6, high: 100.1}),
		},
		{
			name:      "no liquidity left at a level already taken",
			configure: func(c *LiquiditySweepConfig) { c.SweepBars = 1 }, // else the candle taking it is the sweep
			candles: append(lsLows[:3:3],
				ivCandle{close: 98.95, high: 100.8, low: 98.9}, // takes the swing low
				ivCandle{open: 99.2, close: 100, low: 98.6, high: 100.1},
			),
		},
		{
			name:      "no swing out of the lookback",
			configure: func(c *LiquiditySweepConfig) { c.Lookback = 3 },
			candles: lsSweep(
				ivCandle{close: 101.5, high: 102, low: 100.8},
				ivCandle{open: 99.2, close: 100, low: 98.6, high: 100.1},
			),
		},
		{
			name:      "the direction keeps the other side out",
			configure: func(c *LiquiditySweepConfig) { c.Direction = RKDirectionSellOnly },
			candles:   lsSweep(ivCandle{open: 99.2, close: 100, low: 98.6, high: 100.1}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newLSRunner(t, tt.configure).feed(tt.candles...)
		})
	}
}

func TestLiquiditySweep_DayLevels(t *testing.T) {
	// the low of the previous day, 99, is no swing: its lows only go down to
	// it, and the first candle of the day doesn't go below
	previous := make([]ivCandle, 24)
	for hour := range previous {
		low := 99 + float64(23-hour)*0.1
		previous[hour] = ivCandle{close: low + 0.3, high: low + 0.5, low: low}
	}
	previous[0].at = "00:00"
	today := []ivCandle{
		{at: "00:00", close: 100, high: 100.5, low: 99},
		{open: 99.2, close: 100, low: 98.6, high: 100.1},
	}

	for _, dayLevels := range []bool{false, true} {
		r := newLSRunner(t, func(c *LiquiditySweepConfig) { c.DayLevels, c.Timezone = dayLevels, "UTC" })
		r.step = time.Hour
		r.feed(previous...)
		r.day = r.day.AddDate(0, 0, 1)
		if dayLevels {
			today[1].position = 100
		}
		r.feed(today...)
	}
}

func TestLiquiditySweep_Exits(t *testing.T) {
	// no exit of its own: the bracket and the time
	r := newLSRunner(t, func(c *LiquiditySweepConfig) { c.MaxBars = 1 })
	r.feed(lsSweep(ivCandle{open: 99.2, close: 100, low: 98.6, high: 100.1, position: 100})...)
	r.feed(ivCandle{close: 100.5, position: 0})
	require.InDelta(t, 10000+100*0.5, r.quote(), 1e-9)
}
