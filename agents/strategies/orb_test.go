package strategies

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newORB creates an ORB on the 5m timeframe for the session from 10:00 to 11:00
// UTC, its range over the first 15 minutes, without filters.
func newORB(t *testing.T, configure func(*ORBConfig)) *ORB {
	t.Helper()
	cfg := DefaultORBConfig()
	cfg.Timezone, cfg.SessionStart, cfg.OpenRange, cfg.SessionEnd = "UTC", "10:00", 15*time.Minute, "11:00"
	cfg.VWAP = false
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewORB(cfg)
	require.NoError(t, err)
	return s
}

// newORBRunner feeds an ORB with the ATR at 1.
func newORBRunner(t *testing.T, configure func(*ORBConfig)) (*ivRunner, *ORB) {
	t.Helper()
	s := newORB(t, configure)
	r := newIVRunner(t, s, 0)
	r.fixed["orb_atr"] = 1
	return r, s
}

func TestNewORB_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ORBConfig)
		err       string
	}{
		{"timeframe", func(c *ORBConfig) { c.Timeframe = "" }, "timeframe"},
		{"invalid timeframe", func(c *ORBConfig) { c.Timeframe = "fast" }, "invalid timeframe"},
		{"timezone", func(c *ORBConfig) { c.Timezone = "Mars/Olympus" }, "invalid timezone"},
		{"session start", func(c *ORBConfig) { c.SessionStart = "9h30" }, "HH:MM"},
		{"session end", func(c *ORBConfig) { c.SessionEnd = "25:00" }, "HH:MM"},
		{"no opening range", func(c *ORBConfig) { c.OpenRange = 0 }, "opening range"},
		{"opening range in seconds", func(c *ORBConfig) { c.OpenRange = 90 * time.Second }, "opening range"},
		{"opening range longer than the session", func(c *ORBConfig) { c.OpenRange = 3 * time.Hour },
			"end before the session"},
		{"opening range shorter than a candle", func(c *ORBConfig) { c.OpenRange = time.Minute }, "hold a candle"},
		{"negative buffer", func(c *ORBConfig) { c.BufferATR = -1 }, "buffer"},
		{"no trade", func(c *ORBConfig) { c.MaxTrades = 0 }, "max trades"},
		{"negative max range", func(c *ORBConfig) { c.MaxRangeATR = -1 }, "max range"},
		{"negative trend period", func(c *ORBConfig) { c.TrendLen = -1 }, "trend EMA"},
		{"the exits", func(c *ORBConfig) { c.StopATR = 0 }, "stop loss"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultORBConfig()
			tt.configure(&cfg)
			_, err := NewORB(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}
}

func TestORB_WarmupPeriod(t *testing.T) {
	// the session from its start, an hour of daylight saving time included:
	// 2h of 5m candles, less than the seed of the ATR
	require.Equal(t, seedWarmup(14)+2, newORB(t, nil).WarmupPeriod())

	// the default session from 9:30 to 12:00 in New York, on the 1m timeframe
	s, err := NewORB(func() ORBConfig {
		cfg := DefaultORBConfig()
		cfg.Timeframe = "1m"
		return cfg
	}())
	require.NoError(t, err)
	require.Equal(t, 150+60+2, s.WarmupPeriod())
}

// the candles of the ORB tests, their range from 97 to 99 for a long and from
// 101 to 103 for a short, broken by a close at 100
var (
	orbLongRange = []ivCandle{
		{at: "09:50", close: 98},
		{close: 98},
		{at: "10:00", open: 98, close: 98.5, high: 99, low: 97},
		{close: 98, high: 98.8, low: 97.5},
		{close: 98.2, high: 98.6, low: 97.8},
	}
	orbLong       = append(orbLongRange[:len(orbLongRange):len(orbLongRange)], ivCandle{open: 98.5, close: 100, position: 100})
	orbShortRange = []ivCandle{
		{at: "09:50", close: 102},
		{close: 102},
		{at: "10:00", open: 102, close: 101.5, high: 103, low: 101},
		{close: 102, high: 102.5, low: 101.2},
		{close: 101.8, high: 102.2, low: 101.4},
	}
	orbShort = append(orbShortRange[:len(orbShortRange):len(orbShortRange)], ivCandle{open: 101.5, close: 100, position: -100})
)

// orbBreakout is a close at price above the long range, its entry holding
// position.
func orbBreakout(price float64, meta map[string]float64, position float64) []ivCandle {
	return append(orbLongRange[:len(orbLongRange):len(orbLongRange)],
		ivCandle{open: 98.5, close: price, meta: meta, position: position})
}

func TestORB_Entries(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ORBConfig)
		candles   []ivCandle
	}{
		{name: "buy on the first close above the opening range", candles: orbLong},
		{name: "sell on the first close below the opening range", candles: orbShort},
		{
			name: "no entry while the range is built",
			candles: append(orbLongRange[:4:4],
				ivCandle{open: 98.5, close: 100, high: 100, low: 98.4}, // 10:10, the last candle of the range
				ivCandle{close: 99.9}, // under its new high
			),
		},
		{name: "no entry within the buffer", candles: orbBreakout(99.05, nil, 0)},
		{
			name:      "the buffer is configurable",
			configure: func(c *ORBConfig) { c.BufferATR = 0 },
			candles:   orbBreakout(99.05, nil, 10000/99.05),
		},
		{
			name:      "the buffer in ATR",
			configure: func(c *ORBConfig) { c.BufferATR = 2 },
			candles:   orbBreakout(100, nil, 0), // 1 above the range, 2 needed
		},
		{
			name: "no entry on the candle closing the session",
			candles: append(orbLongRange[:5:5],
				ivCandle{close: 98}, ivCandle{close: 98}, ivCandle{close: 98}, ivCandle{close: 98},
				ivCandle{close: 98}, ivCandle{close: 98}, ivCandle{close: 98}, ivCandle{close: 98},
				ivCandle{at: "10:55", close: 100},
			),
		},
		{
			name:    "no entry after the session",
			candles: append(orbLongRange[:5:5], ivCandle{at: "11:05", close: 100}),
		},
		{
			name: "no range without the start of the session in the window",
			candles: []ivCandle{
				{at: "10:05", close: 98, high: 98.8, low: 97.5},
				{close: 98.2, high: 98.6, low: 97.8},
				{open: 98.5, close: 100},
			},
		},
		{
			name:      "the EMA filter keeps a long below it out",
			configure: func(c *ORBConfig) { c.TrendLen = 10 },
			candles:   orbBreakout(100, map[string]float64{"orb_ema": 100.5}, 0),
		},
		{
			name:      "the EMA filter lets a long above it in",
			configure: func(c *ORBConfig) { c.TrendLen = 10 },
			candles:   orbBreakout(100, map[string]float64{"orb_ema": 99.5}, 100),
		},
		{
			name:      "the VWAP filter keeps a long below it out",
			configure: func(c *ORBConfig) { c.VWAP = true },
			candles:   orbBreakout(100, map[string]float64{"orb_vwap": 100.5}, 0),
		},
		{
			name:      "the VWAP filter lets a long above the session VWAP in",
			configure: func(c *ORBConfig) { c.VWAP = true },
			candles:   orbLong,
		},
		{
			name:      "the max range filter skips a wide range",
			configure: func(c *ORBConfig) { c.MaxRangeATR = 1.5 }, // a range of 2 ATR
			candles:   orbBreakout(100, nil, 0),
		},
		{
			name:      "the max range filter takes a narrow one",
			configure: func(c *ORBConfig) { c.MaxRangeATR = 2.5 },
			candles:   orbLong,
		},
		{
			name:      "the direction keeps the other side out",
			configure: func(c *ORBConfig) { c.Direction = RKDirectionBuyOnly },
			candles:   append(orbShortRange[:5:5], ivCandle{open: 101.5, close: 100}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newORBRunner(t, tt.configure)
			r.feed(tt.candles...)
		})
	}
}

func TestORB_Exits(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ORBConfig)
		candles   []ivCandle
		quote     float64 // after the trade, 10000 before
	}{
		{
			name: "the end of the session closes the position",
			candles: []ivCandle{
				{close: 100.5, position: 100}, {close: 100.5, position: 100}, {close: 100.5, position: 100},
				{close: 100.5, position: 100}, {close: 100.5, position: 100}, {close: 100.5, position: 100},
				{close: 100.5, position: 100},
				{at: "10:55", close: 100.5, position: 0},
			},
			quote: 10000 + 100*0.5,
		},
		{
			name:      "the close back past the middle of the range closes it",
			configure: func(c *ORBConfig) { c.StopATR = 3 },
			candles:   []ivCandle{{close: 97.9, high: 99, low: 97.9, position: 0}},
			quote:     10000 - 100*2.1,
		},
		{
			name:      "a close back in the range above its middle keeps it",
			configure: func(c *ORBConfig) { c.StopATR = 3 },
			candles:   []ivCandle{{close: 98.5, position: 100}},
		},
		{
			name:    "the take profit closes it",
			candles: []ivCandle{{close: 101, high: 102.5, low: 100.5, position: 0}},
			quote:   10000 + 100*2,
		},
		{
			name:    "the stop loss closes it",
			candles: []ivCandle{{close: 99.5, high: 100, low: 98.9, position: 0}},
			quote:   10000 - 100*1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newORBRunner(t, tt.configure)
			r.feed(orbLong...)
			r.feed(tt.candles...)
			if tt.quote > 0 {
				require.InDelta(t, tt.quote, r.quote(), 1e-9)
			}
		})
	}
}

func TestORB_TradesPerSession(t *testing.T) {
	// a failed breakout, closed at 97.9, then a second close above the range
	failed := ivCandle{close: 97.9, high: 99, low: 97.9, position: 0}
	quote := 10000 - 100*2.1

	t.Run("one per session", func(t *testing.T) {
		r, _ := newORBRunner(t, func(c *ORBConfig) { c.StopATR = 3 })
		r.feed(orbLong...)
		r.feed(failed, ivCandle{close: 100, position: 0})
	})

	t.Run("more with max trades", func(t *testing.T) {
		r, _ := newORBRunner(t, func(c *ORBConfig) { c.StopATR, c.MaxTrades = 3, 2 })
		r.feed(orbLong...)
		r.feed(failed, ivCandle{close: 100, position: quote / 100})
	})

	t.Run("again in the next session", func(t *testing.T) {
		r, _ := newORBRunner(t, func(c *ORBConfig) { c.StopATR = 3 })
		r.feed(orbLong...)
		r.feed(failed)
		r.day = r.day.Add(24 * time.Hour)
		next := append(orbLongRange[:5:5], ivCandle{open: 98.5, close: 100, position: quote / 100})
		r.feed(next...)
	})
}

func TestORB_Levels(t *testing.T) {
	// the range of the session on the candles of its breakouts only
	r, s := newORBRunner(t, nil)
	r.feed(orbLong...)
	r.feed(ivCandle{close: 100.2, position: 100})

	high, low := r.df.Metadata["orb_high"], r.df.Metadata["orb_low"]
	for i := range r.df.Time {
		if i < 5 {
			require.Truef(t, isNa(high[i]) && isNa(low[i]), "candle %d is not in the trade session", i)
			continue
		}
		require.InDelta(t, 99, high[i], 1e-9)
		require.InDelta(t, 97, low[i], 1e-9)
	}
	require.Len(t, s.Indicators(r.df), 1)
}
