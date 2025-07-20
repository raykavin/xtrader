package strategies

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newAsiaLondon creates an AsiaLondon on the 1h timeframe with its sessions in
// UTC: Asia from 00:00 to 07:00, London from 07:00 to 10:00.
func newAsiaLondon(t *testing.T, configure func(*AsiaLondonConfig)) *AsiaLondon {
	t.Helper()
	cfg := DefaultAsiaLondonConfig()
	cfg.Timeframe, cfg.Timezone = "1h", "UTC"
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewAsiaLondon(cfg)
	require.NoError(t, err)
	return s
}

// newALRunner feeds an AsiaLondon hourly candles, with the ATR at 1.
func newALRunner(t *testing.T, configure func(*AsiaLondonConfig)) (*ivRunner, *AsiaLondon) {
	t.Helper()
	s := newAsiaLondon(t, configure)
	r := newIVRunner(t, s, 0)
	r.step = time.Hour
	r.fixed["al_atr"] = 1
	return r, s
}

func TestNewAsiaLondon_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*AsiaLondonConfig)
		err       string
	}{
		{"timeframe", func(c *AsiaLondonConfig) { c.Timeframe = "" }, "timeframe"},
		{"timezone", func(c *AsiaLondonConfig) { c.Timezone = "Nowhere" }, "invalid timezone"},
		{"asia start", func(c *AsiaLondonConfig) { c.AsiaStart = "0" }, "HH:MM"},
		{"london end", func(c *AsiaLondonConfig) { c.LondonEnd = "10:60" }, "HH:MM"},
		{"london overlapping asia", func(c *AsiaLondonConfig) { c.LondonStart = "06:00" }, "London session"},
		{"london over the next asia", func(c *AsiaLondonConfig) { c.LondonEnd = "01:00" }, "London session"},
		{"asia shorter than a candle", func(c *AsiaLondonConfig) { c.Timeframe, c.AsiaEnd = "4h", "02:00" },
			"hold a candle"},
		{"negative buffer", func(c *AsiaLondonConfig) { c.BufferATR = -0.1 }, "buffer"},
		{"no trade", func(c *AsiaLondonConfig) { c.MaxTrades = 0 }, "max trades"},
		{"negative range", func(c *AsiaLondonConfig) { c.MinRangeATR = -1 }, "range ATR"},
		{"range limits crossed", func(c *AsiaLondonConfig) { c.MinRangeATR, c.MaxRangeATR = 3, 2 }, "min range"},
		{"negative body", func(c *AsiaLondonConfig) { c.BodyATR = -1 }, "body"},
		{"negative trend period", func(c *AsiaLondonConfig) { c.TrendLen = -1 }, "trend EMA"},
		{"volume without multiple", func(c *AsiaLondonConfig) { c.VolumeLen = 20 }, "volume multiple"},
		{"negative volume", func(c *AsiaLondonConfig) { c.VolumeMult = -1 }, "volume"},
		{"the exits", func(c *AsiaLondonConfig) { c.Direction = "up" }, "invalid direction"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultAsiaLondonConfig()
			tt.configure(&cfg)
			_, err := NewAsiaLondon(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}
}

func TestAsiaLondon_WarmupPeriod(t *testing.T) {
	// from the start of Asia to the end of London, 10h and an hour of daylight
	// saving time, in 1m candles
	require.Equal(t, 11*60+2, newAsiaLondon(t, func(c *AsiaLondonConfig) { c.Timeframe = "1m" }).WarmupPeriod())

	// the seed of the ATR, longer than the sessions in 1h candles
	require.Equal(t, seedWarmup(14)+2, newAsiaLondon(t, nil).WarmupPeriod())
}

// the Asia session of the tests: its range from 97 to 99 for a long, and from
// 101 to 103 for a short
var (
	alLongAsia = []ivCandle{
		{at: "00:00", open: 98, close: 98, high: 99, low: 97},
		{close: 98}, {close: 98.2}, {close: 97.8}, {close: 98}, {close: 98.4}, {close: 98.5},
	}
	alShortAsia = []ivCandle{
		{at: "00:00", open: 102, close: 102, high: 103, low: 101},
		{close: 102}, {close: 101.8}, {close: 102.2}, {close: 102}, {close: 101.6}, {close: 101.5},
	}
)

// alBreakout is the first London candle closing at 100 from open, with meta,
// its entry holding position.
func alBreakout(open float64, meta map[string]float64, volume, position float64) []ivCandle {
	return append(alLongAsia[:7:7], ivCandle{at: "07:00", open: open, close: 100, meta: meta, volume: volume,
		position: position})
}

func TestAsiaLondon_Entries(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*AsiaLondonConfig)
		candles   []ivCandle
	}{
		{name: "buy on the London close above Asia High + buffer", candles: alBreakout(98.5, nil, 0, 100)},
		{
			name:    "sell on the London close below Asia Low - buffer",
			candles: append(alShortAsia[:7:7], ivCandle{at: "07:00", open: 101.5, close: 100, position: -100}),
		},
		{
			name: "no entry in the Asia session",
			candles: []ivCandle{
				{at: "00:00", open: 98, close: 98, high: 99, low: 97},
				{open: 98, close: 100}, {close: 101},
			},
		},
		{
			name:      "no entry before London",
			configure: func(c *AsiaLondonConfig) { c.LondonStart = "08:00" },
			candles:   alBreakout(98.5, nil, 0, 0),
		},
		{
			name:      "an entry once London started",
			configure: func(c *AsiaLondonConfig) { c.LondonStart = "08:00" },
			candles: append(alLongAsia[:7:7], ivCandle{at: "07:00", close: 98.5},
				ivCandle{open: 98.5, close: 100, position: 100}),
		},
		{
			name:      "no entry within the buffer",
			configure: func(c *AsiaLondonConfig) { c.BufferATR = 1.5 },
			candles:   alBreakout(98.5, nil, 0, 0),
		},
		{
			name: "no entry on the candle closing London",
			candles: append(alLongAsia[:7:7], ivCandle{at: "07:00", close: 98.5}, ivCandle{close: 98.5},
				ivCandle{open: 98.5, close: 100}),
		},
		{name: "the breakout candle needs a body", candles: alBreakout(99.8, nil, 0, 0)},
		{
			name:      "the body filter disabled",
			configure: func(c *AsiaLondonConfig) { c.BodyATR = 0 },
			candles:   alBreakout(99.8, nil, 0, 100),
		},
		{
			name:      "the max range filter skips a wide Asia range",
			configure: func(c *AsiaLondonConfig) { c.MaxRangeATR = 1.5 },
			candles:   alBreakout(98.5, nil, 0, 0),
		},
		{
			name:      "the min range filter skips a tight Asia range",
			configure: func(c *AsiaLondonConfig) { c.MinRangeATR = 2.5 },
			candles:   alBreakout(98.5, nil, 0, 0),
		},
		{
			name:      "the range filters take a range between them",
			configure: func(c *AsiaLondonConfig) { c.MinRangeATR, c.MaxRangeATR = 1.5, 2.5 },
			candles:   alBreakout(98.5, nil, 0, 100),
		},
		{
			name:      "the volume filter keeps an ordinary volume out",
			configure: func(c *AsiaLondonConfig) { c.VolumeLen, c.VolumeMult = 3, 2 },
			candles:   alBreakout(98.5, nil, 1, 0),
		},
		{
			name:      "the volume filter lets a high volume in",
			configure: func(c *AsiaLondonConfig) { c.VolumeLen, c.VolumeMult = 3, 2 },
			candles:   alBreakout(98.5, nil, 3, 100),
		},
		{
			name:      "the EMA filter",
			configure: func(c *AsiaLondonConfig) { c.TrendLen = 5 },
			candles:   alBreakout(98.5, map[string]float64{"al_ema": 100.2}, 0, 0),
		},
		{
			name:      "the VWAP filter",
			configure: func(c *AsiaLondonConfig) { c.VWAP = true },
			candles:   alBreakout(98.5, map[string]float64{"al_vwap": 100.2}, 0, 0),
		},
		{
			name:      "the VWAP from the start of Asia",
			configure: func(c *AsiaLondonConfig) { c.VWAP = true },
			candles:   alBreakout(98.5, nil, 0, 100),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newALRunner(t, tt.configure)
			r.feed(tt.candles...)
		})
	}
}

func TestAsiaLondon_Exits(t *testing.T) {
	t.Run("the end of London closes the position", func(t *testing.T) {
		r, _ := newALRunner(t, nil)
		r.feed(alBreakout(98.5, nil, 0, 100)...)
		r.feed(ivCandle{close: 100.5, position: 100}, ivCandle{close: 100.8, position: 0}) // 09:00, closing at 10:00
		require.InDelta(t, 10000+100*0.8, r.quote(), 1e-9)
	})

	t.Run("the close back past the middle of the Asia range closes it", func(t *testing.T) {
		r, _ := newALRunner(t, func(c *AsiaLondonConfig) { c.StopATR = 3 })
		r.feed(alBreakout(98.5, nil, 0, 100)...)
		r.feed(ivCandle{open: 100, close: 97.9, position: 0})
		require.InDelta(t, 10000-100*2.1, r.quote(), 1e-9)
	})

	t.Run("one trade per London session", func(t *testing.T) {
		r, _ := newALRunner(t, func(c *AsiaLondonConfig) { c.StopATR, c.LondonEnd = 3, "12:00" })
		r.feed(alBreakout(98.5, nil, 0, 100)...)
		r.feed(ivCandle{open: 100, close: 97.9, position: 0}, ivCandle{open: 97.9, close: 100, position: 0})
	})
}

func TestAsiaLondon_DaylightSavingTime(t *testing.T) {
	// the sessions follow the clock of London: in summer, its midnight is at
	// 23:00 UTC the day before
	s := newAsiaLondon(t, func(c *AsiaLondonConfig) { c.Timezone = "Europe/London" })
	at := time.Date(2024, 7, 15, 6, 30, 0, 0, time.UTC) // 07:30 in London
	rangeStart, rangeEnd, tradeStart, tradeEnd := s.rules.layout.sessions(at)
	require.Equal(t, time.Date(2024, 7, 14, 23, 0, 0, 0, time.UTC), rangeStart.UTC())
	require.Equal(t, time.Date(2024, 7, 15, 6, 0, 0, 0, time.UTC), rangeEnd.UTC())
	require.Equal(t, time.Date(2024, 7, 15, 6, 0, 0, 0, time.UTC), tradeStart.UTC())
	require.Equal(t, time.Date(2024, 7, 15, 9, 0, 0, 0, time.UTC), tradeEnd.UTC())

	// and in winter, it is UTC
	_, _, tradeStart, _ = s.rules.layout.sessions(time.Date(2024, 1, 15, 7, 30, 0, 0, time.UTC))
	require.Equal(t, time.Date(2024, 1, 15, 7, 0, 0, 0, time.UTC), tradeStart.UTC())
}
