package strategies

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func newVWAPReclaim(t *testing.T, configure func(*VWAPReclaimConfig)) *VWAPReclaim {
	t.Helper()
	cfg := DefaultVWAPReclaimConfig()
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewVWAPReclaim(cfg)
	require.NoError(t, err)
	return s
}

func TestNewVWAPReclaim_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*VWAPReclaimConfig)
		err       string
	}{
		{"timeframe", func(c *VWAPReclaimConfig) { c.Timeframe = "" }, "timeframe"},
		{"invalid timeframe", func(c *VWAPReclaimConfig) { c.Timeframe = "5x" }, "invalid timeframe"},
		{"timezone", func(c *VWAPReclaimConfig) { c.Timezone = "Atlantis" }, "invalid timezone"},
		{"anchor", func(c *VWAPReclaimConfig) { c.Anchor = "midnight" }, "HH:MM"},
		{"no setup", func(c *VWAPReclaimConfig) { c.Reclaim, c.Reject = false, false }, "no setup"},
		{"confirmation bars", func(c *VWAPReclaimConfig) { c.ConfirmBars = 0 }, "confirmation bars"},
		{"negative reclaim", func(c *VWAPReclaimConfig) { c.ReclaimATR = -1 }, "reclaim and reject"},
		{"negative reject", func(c *VWAPReclaimConfig) { c.RejectATR = -1 }, "reclaim and reject"},
		{"reject wick", func(c *VWAPReclaimConfig) { c.RejectWick = 1.5 }, "reject wick"},
		{"negative RSI period", func(c *VWAPReclaimConfig) { c.RSILen = -1 }, "RSI period"},
		{"the exits", func(c *VWAPReclaimConfig) { c.PositionFraction = 0 }, "position fraction"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultVWAPReclaimConfig()
			tt.configure(&cfg)
			_, err := NewVWAPReclaim(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}
}

func TestVWAPReclaim_WarmupPeriod(t *testing.T) {
	// a day and an hour of 5m candles, to reach back to the anchor, and the
	// closes before the signal
	require.Equal(t, 25*12+3+1, newVWAPReclaim(t, nil).WarmupPeriod())
}

// newVWRunner feeds a VWAPReclaim without the momentum filter, with the VWAP
// at vwap and the ATR at 1.
func newVWRunner(t *testing.T, vwap float64, configure func(*VWAPReclaimConfig)) *ivRunner {
	t.Helper()
	r := newIVRunner(t, newVWAPReclaim(t, func(c *VWAPReclaimConfig) {
		c.RSILen = 0
		if configure != nil {
			configure(c)
		}
	}), 0)
	r.fixed["vw_vwap"], r.fixed["vw_atr"] = vwap, 1
	return r
}

// vwCloses are candles closing at price.
func vwCloses(price float64, count int) []ivCandle {
	candles := make([]ivCandle, count)
	for i := range candles {
		candles[i] = ivCandle{close: price}
	}
	return candles
}

func TestVWAPReclaim_Entries(t *testing.T) {
	// the reclaims cross a VWAP at 99.5 up and at 100.5 down, and the rejects
	// come back to them from the other side
	reclaimLong := ivCandle{open: 99.2, close: 100, position: 100}
	rejectLong := ivCandle{open: 99.8, close: 100, high: 100.1, low: 99.4, position: 100}
	tests := []struct {
		name      string
		vwap      float64
		configure func(*VWAPReclaimConfig)
		candles   []ivCandle
	}{
		{
			name:    "buy on the reclaim of the VWAP",
			vwap:    99.5,
			candles: append(vwCloses(99, 3), reclaimLong),
		},
		{
			name:    "sell on the loss of the VWAP",
			vwap:    100.5,
			candles: append(vwCloses(101, 3), ivCandle{open: 100.8, close: 100, position: -100}),
		},
		{
			name:    "buy on the reject of the VWAP from above",
			vwap:    99.5,
			candles: append(vwCloses(100.5, 3), rejectLong),
		},
		{
			name:    "sell on the reject of the VWAP from below",
			vwap:    100.5,
			candles: append(vwCloses(99.5, 3), ivCandle{open: 100.2, close: 100, high: 100.6, low: 99.9, position: -100}),
		},
		{
			name:    "no reclaim without the closes on the other side before",
			vwap:    99.5,
			candles: append([]ivCandle{{close: 100.2}, {close: 99}, {close: 99}}, ivCandle{open: 99.2, close: 100}),
		},
		{
			name:      "the confirmation bars are configurable",
			vwap:      99.5,
			configure: func(c *VWAPReclaimConfig) { c.ConfirmBars = 2 },
			candles:   append([]ivCandle{{close: 100.2}, {close: 99}, {close: 99}}, reclaimLong),
		},
		{
			name:      "no reclaim within its threshold",
			vwap:      99.5,
			configure: func(c *VWAPReclaimConfig) { c.ReclaimATR = 1 }, // closing 0.5 above
			candles:   append(vwCloses(99, 3), ivCandle{open: 99.2, close: 100}),
		},
		{
			name:    "no reclaim on a candle down",
			vwap:    99.5,
			candles: append(vwCloses(99, 3), ivCandle{open: 100.2, close: 100}),
		},
		{
			name:    "no reject without the wick reaching the VWAP",
			vwap:    99.5,
			candles: append(vwCloses(100.5, 3), ivCandle{open: 99.8, close: 100, high: 100.1, low: 99.7}),
		},
		{
			name:      "the reject reach is configurable",
			vwap:      99.5,
			configure: func(c *VWAPReclaimConfig) { c.RejectATR = 0.3 },
			candles:   append(vwCloses(100.5, 3), ivCandle{open: 99.9, close: 100, high: 100.1, low: 99.7, position: 100}),
		},
		{
			name:      "no reject with a small wick",
			vwap:      99.5,
			configure: func(c *VWAPReclaimConfig) { c.RejectWick = 0.8 }, // 0.4 of a 0.7 range
			candles:   append(vwCloses(100.5, 3), ivCandle{open: 99.8, close: 100, high: 100.1, low: 99.4}),
		},
		{
			name:    "no reject closing beyond the VWAP",
			vwap:    99.5,
			candles: append(vwCloses(100.5, 3), ivCandle{open: 99.45, close: 99.48, high: 99.6, low: 99.0}),
		},
		{
			name:      "only the reclaims",
			vwap:      99.5,
			configure: func(c *VWAPReclaimConfig) { c.Reject = false },
			candles:   append(vwCloses(100.5, 3), ivCandle{open: 99.8, close: 100, high: 100.1, low: 99.4}),
		},
		{
			name:      "only the rejects",
			vwap:      99.5,
			configure: func(c *VWAPReclaimConfig) { c.Reclaim = false },
			candles:   append(vwCloses(99, 3), ivCandle{open: 99.2, close: 100}),
		},
		{
			name:      "the momentum keeps a weak RSI out",
			vwap:      99.5,
			configure: func(c *VWAPReclaimConfig) { c.RSILen = 14 },
			candles:   append(vwCloses(99, 3), ivCandle{open: 99.2, close: 100, meta: map[string]float64{"vw_rsi": 45}}),
		},
		{
			name:      "the momentum lets a strong RSI in",
			vwap:      99.5,
			configure: func(c *VWAPReclaimConfig) { c.RSILen = 14 },
			candles: append(vwCloses(99, 3),
				ivCandle{open: 99.2, close: 100, meta: map[string]float64{"vw_rsi": 55}, position: 100}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newVWRunner(t, tt.vwap, tt.configure).feed(tt.candles...)
		})
	}
}

func TestVWAPReclaim_Exits(t *testing.T) {
	t.Run("the close back across the VWAP closes the long", func(t *testing.T) {
		r := newVWRunner(t, 99.5, nil)
		r.feed(vwCloses(99, 3)...)
		r.feed(ivCandle{open: 99.2, close: 100, position: 100}, ivCandle{close: 99.4, position: 0})
		require.InDelta(t, 10000-100*0.6, r.quote(), 1e-9)
	})

	t.Run("a close above the VWAP keeps it", func(t *testing.T) {
		r := newVWRunner(t, 99.5, nil)
		r.feed(vwCloses(99, 3)...)
		r.feed(ivCandle{open: 99.2, close: 100, position: 100}, ivCandle{close: 99.6, position: 100})
	})
}

func TestVWAPReclaim_Indicators(t *testing.T) {
	// the VWAP of the day, from midnight UTC
	s := newVWAPReclaim(t, nil)
	r := newIVRunner(t, s, 0)
	r.feed(ivCandle{at: "23:50", close: 90}, ivCandle{close: 90})
	r.day = r.day.AddDate(0, 0, 1)
	r.feed(ivCandle{at: "00:00", close: 100, volume: 1}, ivCandle{close: 103, volume: 2})
	requireSeries(t, []float64{na, na, 100, (100 + 103*2) / 3.0}, r.df.Metadata["vw_vwap"], 1e-12)
}
