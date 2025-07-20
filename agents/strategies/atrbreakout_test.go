package strategies

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func newATRBreakout(t *testing.T, configure func(*ATRBreakoutConfig)) *ATRBreakout {
	t.Helper()
	cfg := DefaultATRBreakoutConfig()
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewATRBreakout(cfg)
	require.NoError(t, err)
	return s
}

func TestNewATRBreakout_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ATRBreakoutConfig)
		err       string
	}{
		{"timeframe", func(c *ATRBreakoutConfig) { c.Timeframe = "" }, "timeframe"},
		{"baseline period", func(c *ATRBreakoutConfig) { c.BaselineLen = 0 }, "at least 1"},
		{"breakout period", func(c *ATRBreakoutConfig) { c.BreakoutLen = 0 }, "at least 1"},
		{"expansion", func(c *ATRBreakoutConfig) { c.Expansion = 0 }, "expansion"},
		{"negative buffer", func(c *ATRBreakoutConfig) { c.BufferATR = -1 }, "buffer and body"},
		{"negative body", func(c *ATRBreakoutConfig) { c.BodyATR = -1 }, "buffer and body"},
		{"the exits", func(c *ATRBreakoutConfig) { c.ATRLen = 0 }, "at least 1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultATRBreakoutConfig()
			tt.configure(&cfg)
			_, err := NewATRBreakout(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}
}

func TestATRBreakout_WarmupPeriod(t *testing.T) {
	// the average of the seeded ATR, and the previous candle
	require.Equal(t, seedWarmup(14)+50+1, newATRBreakout(t, nil).WarmupPeriod())
}

// newABRunner feeds an ATRBreakout of a 3 candles channel, the ATR at 1 and its
// average at 0.8.
func newABRunner(t *testing.T, configure func(*ATRBreakoutConfig)) *ivRunner {
	t.Helper()
	r := newIVRunner(t, newATRBreakout(t, func(c *ATRBreakoutConfig) {
		c.BreakoutLen = 3
		if configure != nil {
			configure(c)
		}
	}), 0)
	r.fixed["ab_atr"], r.fixed["ab_atr_avg"] = 1, 0.8
	return r
}

// the channels of the tests, from 98.5 to 99.5 for a long and from 100.5 to
// 101.5 for a short
var (
	abLongChannel = []ivCandle{
		{close: 99, high: 99.5, low: 98.5}, {close: 99, high: 99.5, low: 98.5}, {close: 99, high: 99.5, low: 98.5},
	}
	abShortChannel = []ivCandle{
		{close: 101, high: 101.5, low: 100.5}, {close: 101, high: 101.5, low: 100.5},
		{close: 101, high: 101.5, low: 100.5},
	}
)

// abBreakout is the long channel, then a candle from open to closePrice.
func abBreakout(open, closePrice float64, meta map[string]float64, position float64) []ivCandle {
	return append(abLongChannel[:3:3], ivCandle{open: open, close: closePrice, meta: meta, position: position})
}

func TestATRBreakout_Entries(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*ATRBreakoutConfig)
		candles   []ivCandle
	}{
		{name: "buy on an expansion closing above the channel", candles: abBreakout(99, 100, nil, 100)},
		{
			name:    "sell on an expansion closing below the channel",
			candles: append(abShortChannel[:3:3], ivCandle{open: 101, close: 100, position: -100}),
		},
		{
			name: "no entry without an expansion of the volatility",
			candles: []ivCandle{
				abLongChannel[0], abLongChannel[1],
				// the average before the breakout: 1.2 x 0.9 above the ATR
				{close: 99, high: 99.5, low: 98.5, meta: map[string]float64{"ab_atr_avg": 0.9}},
				{open: 99, close: 100},
			},
		},
		{
			name:      "the expansion is configurable",
			configure: func(c *ATRBreakoutConfig) { c.Expansion = 1.3 },
			candles:   abBreakout(99, 100, nil, 0),
		},
		{name: "no entry inside the channel", candles: abBreakout(98.5, 99.4, nil, 0)},
		{name: "no entry on a small body", candles: abBreakout(99.5, 100, nil, 0)},
		{
			name:      "the body is configurable",
			configure: func(c *ATRBreakoutConfig) { c.BodyATR = 0.4 },
			candles:   abBreakout(99.5, 100, nil, 100),
		},
		{name: "no entry on a candle against the breakout", candles: abBreakout(100.5, 100, nil, 0)},
		{
			name:      "no entry within the buffer",
			configure: func(c *ATRBreakoutConfig) { c.BufferATR = 0.6 },
			candles:   abBreakout(99, 100, nil, 0),
		},
		{
			name:      "the direction keeps the other side out",
			configure: func(c *ATRBreakoutConfig) { c.Direction = RKDirectionSellOnly },
			candles:   abBreakout(99, 100, nil, 0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newABRunner(t, tt.configure).feed(tt.candles...)
		})
	}
}

func TestATRBreakout_Exits(t *testing.T) {
	// no exit of its own: the bracket and the time
	r := newABRunner(t, func(c *ATRBreakoutConfig) { c.MaxBars = 2 })
	r.feed(abBreakout(99, 100, nil, 100)...)
	r.feed(ivCandle{close: 99.2, position: 100}, ivCandle{close: 99.4, position: 0})
	require.InDelta(t, 10000-100*0.6, r.quote(), 1e-9)
}

func TestATRBreakout_Indicators(t *testing.T) {
	r := newABRunner(t, nil)
	r.feed(abBreakout(99, 100, nil, 100)...)
	requireSeries(t, []float64{na, na, 99.5, 100}, r.df.Metadata["ab_high"], 1e-12)
	requireSeries(t, []float64{na, na, 98.5, 98.5}, r.df.Metadata["ab_low"], 1e-12)
}
