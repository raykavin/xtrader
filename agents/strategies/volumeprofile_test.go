package strategies

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

func newVolumeProfile(t *testing.T, configure func(*VolumeProfileConfig)) *VolumeProfile {
	t.Helper()
	cfg := DefaultVolumeProfileConfig()
	cfg.Timezone, cfg.Anchor = "UTC", "00:00" // the sessions of the tests: UTC days
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewVolumeProfile(cfg)
	require.NoError(t, err)
	return s
}

// The default session is the trading day of the CME, from 17:00 to 17:00 in
// New York: the close of a Friday ends the session of the week.
func TestVolumeProfile_DefaultSessionIsTheCMETradingDay(t *testing.T) {
	cfg := DefaultVolumeProfileConfig()
	require.Equal(t, [2]string{"America/New_York", "17:00"}, [2]string{cfg.Timezone, cfg.Anchor})
	_, err := NewVolumeProfile(cfg)
	require.NoError(t, err)
}

func TestNewVolumeProfile_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*VolumeProfileConfig)
		err       string
	}{
		{"timeframe", func(c *VolumeProfileConfig) { c.Timeframe = "" }, "timeframe"},
		{"invalid timeframe", func(c *VolumeProfileConfig) { c.Timeframe = "30x" }, "invalid timeframe"},
		{"timezone", func(c *VolumeProfileConfig) { c.Timezone = "Atlantis" }, "invalid timezone"},
		{"anchor", func(c *VolumeProfileConfig) { c.Anchor = "midnight" }, "HH:MM"},
		{"one bin", func(c *VolumeProfileConfig) { c.Bins = 1 }, "2 bins"},
		{"no value area", func(c *VolumeProfileConfig) { c.ValueArea = 0 }, "value area"},
		{"value area above the volume", func(c *VolumeProfileConfig) { c.ValueArea = 1.2 }, "value area"},
		{"value area not a number", func(c *VolumeProfileConfig) { c.ValueArea = math.NaN() }, "value area"},
		{"no confirmation", func(c *VolumeProfileConfig) { c.Confirm = 0 }, "confirmation candles"},
		{"confirmation longer than a session", func(c *VolumeProfileConfig) { c.Timeframe, c.Confirm = "4h", 5 },
			"a session must hold"},
		{"daily candles", func(c *VolumeProfileConfig) { c.Timeframe, c.Confirm = "1d", 1 }, "a session must hold"},
		{
			// 23 hours, the shortest session: the candle of the confirmation
			// closes the positions of the session, see ending
			"no candle closing before the end after the confirmation",
			func(c *VolumeProfileConfig) { c.Timeframe, c.Confirm = "690m", 1 }, "a session must hold",
		},
		{"the buffer of the stop loss", func(c *VolumeProfileConfig) { c.StopATR = 0 }, "stop loss"},
		{"the exits", func(c *VolumeProfileConfig) { c.PositionFraction = 0 }, "position fraction"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultVolumeProfileConfig()
			tt.configure(&cfg)
			_, err := NewVolumeProfile(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}

	// the take profit at the edge of the value area: the one in ATR unused
	newVolumeProfile(t, func(c *VolumeProfileConfig) { c.TakeProfitATR = 0 })
	newVolumeProfile(t, func(c *VolumeProfileConfig) { c.ValueArea = 1 })
}

func TestVolumeProfile_WarmupPeriod(t *testing.T) {
	// two sessions of 25 hours and a candle: 101 candles of 30m, less than
	// the seed of the ATR
	require.Equal(t, seedWarmup(14), newVolumeProfile(t, nil).WarmupPeriod())

	// on the 5m timeframe, 600 candles and one more
	require.Equal(t, 601, newVolumeProfile(t, func(c *VolumeProfileConfig) { c.Timeframe = "5m" }).WarmupPeriod())
}

// vpFrame returns a dataframe of candles, each its low, its high and its
// volume, the open and the close in the middle.
func vpFrame(candles ...[3]float64) *model.Dataframe {
	df := &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}
	at := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	for i, c := range candles {
		middle := (c[0] + c[1]) / 2
		df.Time = append(df.Time, at.Add(time.Duration(i)*30*time.Minute))
		df.Open, df.Close = append(df.Open, middle), append(df.Close, middle)
		df.Low, df.High, df.Volume = append(df.Low, c[0]), append(df.High, c[1]), append(df.Volume, c[2])
	}
	return df
}

func TestVolumeProfile_Profile(t *testing.T) {
	// a range from 100 to 110 in 10 bins of 1: 10 spread over all of them, 8
	// over 104-106 and 6 over 105-107, the bins from 100 holding 1, 1, 1, 1,
	// 5, 8, 4, 1, 1, 1 of 24
	session := [][3]float64{{100, 110, 10}, {104, 106, 8}, {105, 107, 6}}
	tests := []struct {
		name      string
		candles   [][3]float64
		share     float64
		high, poc float64
		low       float64
	}{
		{
			// from the POC (105-106, 8), the two bins below (5 and 1, 6)
			// against the two above (4 and 1, 5): the ones below, 14 of 24;
			// then the two above (5) against the two below (1 and 1): 19 of
			// 24, 70% at least
			name: "the value area of 70%", candles: session, share: 0.7,
			high: 108, poc: 105.5, low: 103,
		},
		{
			// then the two bins on each side, 2 each: both pairs on the tie,
			// 23 of 24
			name: "both pairs on a tie", candles: session, share: 0.9,
			high: 110, poc: 105.5, low: 101,
		},
		{
			// then the one bin left below, none above
			name: "the whole range", candles: session, share: 1,
			high: 110, poc: 105.5, low: 100,
		},
		{
			// 1 in each bin, the POC 105-106 (10), 3 in 106-107 and 6 in
			// 103-104: the larger pair is the one below (1 and 6, 7), not
			// the one of the larger neighbour, above (3 and 1, 4): 17 of 26,
			// 60% at least
			name: "the larger pair, not the larger neighbour", share: 0.6,
			candles: [][3]float64{{100, 110, 10}, {105, 106, 9}, {106, 107, 2}, {103, 104, 5}},
			high:    106, poc: 105.5, low: 103,
		},
		{
			// 9 in the bin of 108.5, 10 there of 33: the POC; the one bin
			// left above (1) against the two below (1 and 4): the two below,
			// 15; then the two below again (8 and 5): 28, half of 33 at
			// least
			name: "a candle without range in its bin", candles: append(session[:3:3], [3]float64{108.5, 108.5, 9}),
			share: 0.5, high: 109, poc: 108.5, low: 104,
		},
		{
			// each candle weighing 1: 0.1 in each bin, 0.5 in 104-105 and
			// 105-106, 0.5 in 105-106 and 106-107, the POC 105-106 (1.1); the
			// two bins on each side tie (0.7): both pairs, 2.5 of 3
			name: "without volume, each candle the same", share: 0.7,
			candles: [][3]float64{{100, 110, 0}, {104, 106, 0}, {105, 107, 0}},
			high:    108, poc: 105.5, low: 103,
		},
		{
			// 104-105 as heavy as 100-101, closer to the middle; a candle
			// without volume weighing nothing in a session with volume
			name: "the POC closest to the middle on a tie", share: 0.5,
			candles: [][3]float64{{100, 101, 5}, {104, 105, 5}, {100, 110, 0}},
			high:    105, poc: 104.5, low: 104,
		},
		{
			name: "the lower POC when as close", share: 0.5,
			candles: [][3]float64{{104, 105, 5}, {105, 106, 5}, {100, 110, 0}},
			high:    105, poc: 104.5, low: 104,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			df := vpFrame(tt.candles...)
			profile, ok := volumeProfile(df, 0, len(df.Time), 10, tt.share)
			require.True(t, ok)
			require.InDelta(t, tt.high, profile.high, 1e-9, "value area high")
			require.InDelta(t, tt.poc, profile.poc, 1e-9, "POC")
			require.InDelta(t, tt.low, profile.low, 1e-9, "value area low")
		})
	}

	t.Run("no range", func(t *testing.T) {
		df := vpFrame([3]float64{100, 100, 1}, [3]float64{100, 100, 2})
		_, ok := volumeProfile(df, 0, len(df.Time), 10, 0.7)
		require.False(t, ok)
	})

	t.Run("the candles of the session only", func(t *testing.T) {
		df := vpFrame([3]float64{50, 200, 100}, [3]float64{100, 110, 10}, [3]float64{104, 106, 8},
			[3]float64{105, 107, 6}, [3]float64{0, 1000, 100})
		profile, ok := volumeProfile(df, 1, 4, 10, 0.7)
		require.True(t, ok)
		require.Equal(t, vpProfile{high: 108, poc: 105.5, low: 103}, profile)
	})
}

// vpDay is a session of the levels tests, in 4 bins of 2 from 98 to 106: 1
// spread over the range and 10 over 98-102, the POC the one of the two bins
// of 5.25 closest to the middle, 100-102, the value area from 98 to 102: the
// one bin left below it (5.25) larger than the two above it (0.5).
var vpDay = []ivCandle{
	{at: "00:00", open: 100, close: 100, high: 106, low: 98, volume: 1},
	{open: 100, close: 100, high: 102, low: 98, volume: 10},
}

func TestVolumeProfile_Levels(t *testing.T) {
	bins := func(c *VolumeProfileConfig) { c.Bins = 4 }
	requireLevels := func(t *testing.T, r *ivRunner, from int, high, poc, low float64) {
		t.Helper()
		for i := from; i < len(r.df.Time); i++ {
			require.InDeltaf(t, high, r.df.Metadata["vp_vah"][i], 1e-9, "value area high of candle %d", i)
			require.InDeltaf(t, poc, r.df.Metadata["vp_poc"][i], 1e-9, "POC of candle %d", i)
			require.InDeltaf(t, low, r.df.Metadata["vp_val"][i], 1e-9, "value area low of candle %d", i)
		}
	}
	requireNone := func(t *testing.T, r *ivRunner, to int) {
		t.Helper()
		for i := 0; i < to; i++ {
			require.Truef(t, isNa(r.df.Metadata["vp_vah"][i]) && isNa(r.df.Metadata["vp_poc"][i]) &&
				isNa(r.df.Metadata["vp_val"][i]), "candle %d has no previous session", i)
		}
	}

	t.Run("the value area of the previous session", func(t *testing.T) {
		r := newIVRunner(t, newVolumeProfile(t, bins), 0)
		r.step = 30 * time.Minute
		r.feed(vpDay...)
		r.day = r.day.AddDate(0, 0, 1)
		r.feed(ivCandle{at: "00:00", close: 120, high: 130, low: 110, volume: 50}, ivCandle{close: 121})
		requireNone(t, r, 2)
		requireLevels(t, r, 2, 102, 101, 98)

		// the next session, on the profile of this one: 110-130 in bins of
		// 5, 12.5 each and 1 more in 120-125, the POC; the two below it
		// (25) larger than the one left above it (12.5)
		r.day = r.day.AddDate(0, 0, 1)
		r.feed(ivCandle{at: "00:00", close: 100})
		require.InDelta(t, 125, r.df.Metadata["vp_vah"].Last(0), 1e-9)
		require.InDelta(t, 110, r.df.Metadata["vp_val"].Last(0), 1e-9)
	})

	t.Run("a day without candles skipped", func(t *testing.T) {
		r := newIVRunner(t, newVolumeProfile(t, bins), 0)
		r.step = 30 * time.Minute
		r.feed(vpDay...)
		r.day = r.day.AddDate(0, 0, 3)
		r.feed(ivCandle{at: "00:00", close: 100}, ivCandle{close: 101})
		requireLevels(t, r, 2, 102, 101, 98)
	})

	t.Run("none without the start of the previous session in the window", func(t *testing.T) {
		r := newIVRunner(t, newVolumeProfile(t, bins), 0)
		r.step = 30 * time.Minute
		r.feed(ivCandle{at: "00:30", open: 100, close: 100, high: 104, low: 96, volume: 1})
		r.day = r.day.AddDate(0, 0, 1)
		r.feed(ivCandle{at: "00:00", close: 100}, ivCandle{close: 101})
		requireNone(t, r, 3)
	})

	t.Run("the session from the anchor in its time zone", func(t *testing.T) {
		// noon in New York, 17:00 UTC in January: a candle before it in the
		// session of the day before, the one of 16:30 of the next day too,
		// its 1 at 100 in 100-102, the POC
		r := newIVRunner(t, newVolumeProfile(t, func(c *VolumeProfileConfig) {
			c.Bins, c.Anchor, c.Timezone = 4, "12:00", "America/New_York"
		}), 0)
		r.step = 30 * time.Minute
		r.feed(ivCandle{at: "16:30", close: 50, high: 60, low: 40, volume: 100}) // its start out of the window
		r.feed(ivCandle{at: "17:00", open: 100, close: 100, high: 106, low: 98, volume: 1},
			ivCandle{open: 100, close: 100, high: 102, low: 98, volume: 10})
		r.day = r.day.AddDate(0, 0, 1)
		r.feed(ivCandle{at: "16:30", close: 100}, ivCandle{at: "17:00", close: 100})
		requireNone(t, r, 4)
		requireLevels(t, r, 4, 102, 101, 98)
	})

	t.Run("pure: the state left as it was", func(t *testing.T) {
		s := newVolumeProfile(t, bins)
		r := newIVRunner(t, s, 0)
		r.step = 30 * time.Minute
		r.feed(vpDay...)
		for range 3 {
			require.Len(t, s.Indicators(r.df), 1)
		}
		require.Empty(t, s.traded)
		require.Empty(t, s.entries)
		require.Empty(t, s.bracket.trades)
	})
}

// TestVolumeProfile_NoLookahead checks that the levels of each candle are
// the ones of the candles up to it: the same on the window ending there as on
// the whole of it, each candle of the window placed by its open time.
func TestVolumeProfile_NoLookahead(t *testing.T) {
	s := newVolumeProfile(t, nil)
	rng := rand.New(rand.NewSource(1))
	df := &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}
	at, price := time.Date(2024, 1, 12, 7, 0, 0, 0, time.UTC), 100.0
	for i := 0; i < 5*48; i++ {
		if at.Weekday() == time.Saturday { // a weekend without candles
			at = at.Add(48 * time.Hour)
		}
		open := price
		price += rng.NormFloat64()
		df.Time = append(df.Time, at)
		df.Open, df.Close = append(df.Open, open), append(df.Close, price)
		df.High = append(df.High, math.Max(open, price)+rng.Float64())
		df.Low = append(df.Low, math.Min(open, price)-rng.Float64())
		df.Volume = append(df.Volume, 1+10*rng.Float64())
		at = at.Add(30 * time.Minute)
	}

	high, poc, low := s.levels(df)
	known := 0
	for i := range df.Time {
		window := &model.Dataframe{Pair: df.Pair, Time: df.Time[:i+1], Open: df.Open[:i+1], Close: df.Close[:i+1],
			High: df.High[:i+1], Low: df.Low[:i+1], Volume: df.Volume[:i+1]}
		h, p, l := s.levels(window)
		for _, pair := range [][2]float64{{high[i], h[i]}, {poc[i], p[i]}, {low[i], l[i]}} {
			if isNa(pair[0]) {
				require.Truef(t, isNa(pair[1]), "candle %d", i)
				continue
			}
			require.Equalf(t, pair[0], pair[1], "candle %d", i)
		}
		if !isNa(high[i]) {
			known++
		}
	}
	require.Greater(t, known, 3*48, "the sessions after the first one have levels")
}

// newVPRunner feeds a VolumeProfile on the 30m timeframe from 00:00 UTC, the
// value area of the previous session from 98 to 102, the ATR at 1.
func newVPRunner(t *testing.T, configure func(*VolumeProfileConfig)) (*ivRunner, *VolumeProfile) {
	t.Helper()
	s := newVolumeProfile(t, configure)
	r := newIVRunner(t, s, 0)
	r.step = 30 * time.Minute
	r.fixed["vp_vah"], r.fixed["vp_val"], r.fixed["vp_atr"] = 102, 98, 1
	return r, s
}

// the entries of the tests: the session opening above the value area, or
// below it, then two closes back inside it, the second at 100 holding 100,
// the stop loss an ATR beyond the extreme of the session (104.5, 95.5) and
// the take profit at the other edge (98, 102)
var (
	vpShort = []ivCandle{
		{at: "00:00", open: 103, close: 101, high: 103.5, low: 100.8},
		{close: 100, high: 101.2, low: 99.5, position: -100},
	}
	vpLong = []ivCandle{
		{at: "00:00", open: 97, close: 99, high: 99.2, low: 96.5},
		{close: 100, high: 100.5, low: 98.8, position: 100},
	}
)

func TestVolumeProfile_Entries(t *testing.T) {
	opening := ivCandle{at: "00:00", open: 103, close: 101, high: 103.5, low: 100.8} // above, back inside
	tests := []struct {
		name      string
		configure func(*VolumeProfileConfig)
		candles   []ivCandle
	}{
		{name: "sell after an open above the value area and two closes back inside", candles: vpShort},
		{name: "buy after an open below the value area and two closes back inside", candles: vpLong},
		{
			name:    "no entry after an open inside the value area",
			candles: []ivCandle{{at: "00:00", open: 101, close: 101}, {close: 100.5}, {close: 100}},
		},
		{
			name: "the closes inside consecutive",
			candles: []ivCandle{opening, {close: 102.5, high: 102.8, low: 100.9}, {close: 101},
				{close: 100, position: -100}},
		},
		{
			name:      "the confirmation candles are configurable",
			configure: func(c *VolumeProfileConfig) { c.Confirm = 3 },
			candles:   []ivCandle{opening, {close: 100.5}, {close: 100, position: -100}},
		},
		{
			name:      "no entry before them",
			configure: func(c *VolumeProfileConfig) { c.Confirm = 3 },
			candles:   []ivCandle{opening, {close: 100}},
		},
		{
			name:      "the opening candle counts closing inside",
			configure: func(c *VolumeProfileConfig) { c.Confirm = 1 },
			candles:   []ivCandle{{at: "00:00", open: 103, close: 100, high: 103.5, low: 99.8, position: -100}},
		},
		{
			name:    "no entry once the value area is filled",
			candles: []ivCandle{{at: "00:00", open: 103, close: 101, high: 103.5, low: 97.9}, {close: 100}},
		},
		{
			name:    "no entry on the candle filling it",
			candles: []ivCandle{opening, {close: 100, high: 101.2, low: 98}},
		},
		{
			name:    "no entry once the value area is filled, after an open below it",
			candles: []ivCandle{{at: "00:00", open: 97, close: 99, high: 102.1, low: 96.5}, {close: 100}},
		},
		{
			// 23:00, closing at 23:30: the next candle closes at the end of
			// the session, the position of 23:00 closed on its own close
			name:    "no entry on the last candle ending before the end of the session",
			candles: []ivCandle{{at: "00:00", open: 103, close: 102.5}, {at: "22:30", close: 101}, {close: 100}},
		},
		{
			name:    "no entry on the last candle of the session",
			candles: []ivCandle{{at: "00:00", open: 103, close: 102.5}, {at: "23:00", close: 101}, {close: 100}},
		},
		{
			name: "an entry on the candle before them",
			candles: []ivCandle{{at: "00:00", open: 103, close: 102.5}, {at: "22:00", close: 101},
				{close: 100, position: -100}},
		},
		{
			name:    "no entry without the open of the session in the window",
			candles: []ivCandle{{at: "00:30", open: 103, close: 101, high: 103.5}, {close: 100}},
		},
		{
			// the ATR too small for the costs on the second close inside only
			name:      "the entry on the close completing the confirmation only",
			configure: func(c *VolumeProfileConfig) { c.MinATRPct = 0.75 },
			candles:   []ivCandle{opening, {close: 100, meta: map[string]float64{"vp_atr": 0.5}}, {close: 100}},
		},
		{
			name:      "the direction keeps the other side out",
			configure: func(c *VolumeProfileConfig) { c.Direction = RKDirectionBuyOnly },
			candles:   []ivCandle{opening, {close: 100, high: 101.2, low: 99.5}},
		},
		{
			name:      "no entry while the ATR is too small for the costs",
			configure: func(c *VolumeProfileConfig) { c.MinATRPct = 2 },
			candles:   []ivCandle{opening, {close: 100}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newVPRunner(t, tt.configure)
			r.feed(tt.candles...)
		})
	}
}

func TestVolumeProfile_EntryOnTheValueAreaComputed(t *testing.T) {
	// the value area of the session before computed, 98 to 102: the session
	// opening above it, then closing back inside it twice
	r, _ := newVPRunner(t, func(c *VolumeProfileConfig) { c.Bins = 4 })
	delete(r.fixed, "vp_vah")
	delete(r.fixed, "vp_val")
	r.feed(vpDay...)
	r.day = r.day.AddDate(0, 0, 1)
	r.feed(vpShort...)
}

func TestVolumeProfile_Legs(t *testing.T) {
	for _, tc := range []struct {
		name         string
		candles      []ivCandle
		configure    func(*VolumeProfileConfig)
		stop, profit float64
	}{
		{name: "short", candles: vpShort, stop: 104.5, profit: 98},
		{name: "long", candles: vpLong, stop: 95.5, profit: 102},
		{name: "the buffer in ATR", candles: vpShort, configure: func(c *VolumeProfileConfig) { c.StopATR = 2 },
			stop: 105.5, profit: 98},
		{name: "the fixed values in their place", candles: vpShort, configure: func(c *VolumeProfileConfig) {
			c.StopValue, c.TakeProfitValue = 50, 150 // 0.5 and 1.5 away on 100
		}, stop: 100.5, profit: 98.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := newVPRunner(t, tc.configure)
			r.feed(tc.candles...)
			legs := s.bracket.trades["BTCUSDT"].legs
			require.Len(t, legs, 2)
			require.Equal(t, model.OrderTypeLimitMaker, legs[0].Type)
			require.InDelta(t, tc.profit, legs[0].Price, 1e-9)
			require.Equal(t, model.OrderTypeStopLoss, legs[1].Type)
			require.InDelta(t, tc.stop, *legs[1].Stop, 1e-9)
		})
	}
}

func TestVolumeProfile_Exits(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*VolumeProfileConfig)
		candles   []ivCandle
		quote     float64 // after the trade, 10000 before
	}{
		{
			name:    "the take profit at the other edge of the value area closes it",
			candles: []ivCandle{{close: 98.5, high: 100, low: 97.8, position: 0}},
			quote:   10000 + 100*2,
		},
		{
			name:    "the stop loss beyond the extreme of the session closes it",
			candles: []ivCandle{{close: 104, high: 104.6, low: 103, position: 0}},
			quote:   10000 - 100*4.5,
		},
		{
			// 23:00, closing at 23:30, the last candle ending before the end
			// of the session: the next one closes at 24:00, at the end, when
			// the market may be closed (17:00 in New York for the CME)
			name: "the end of the session closes it on the last candle ending before it",
			candles: []ivCandle{{at: "12:00", close: 100.5, position: -100},
				{at: "22:30", close: 100.5, position: -100}, {close: 100.5, position: 0}},
			quote: 10000 - 100*0.5,
		},
		{
			name: "the candle ending at the end of the session closes it, the one before it missing",
			candles: []ivCandle{{at: "12:00", close: 100.5, position: -100},
				{at: "22:30", close: 100.5, position: -100}, {at: "23:30", close: 100.5, position: 0}},
			quote: 10000 - 100*0.5,
		},
		{
			name: "a later session closes it at once, the last candle of its own missing",
			candles: []ivCandle{{at: "21:00", close: 100.5, position: -100},
				{at: "24:00", close: 100.5, position: 0}},
			quote: 10000 - 100*0.5,
		},
		{
			name:      "max bars closes it earlier",
			configure: func(c *VolumeProfileConfig) { c.MaxBars = 2 },
			candles:   []ivCandle{{close: 100.5, position: -100}, {close: 100.5, position: 0}},
			quote:     10000 - 100*0.5,
		},
		{
			name:    "a close back out of the value area keeps it",
			candles: []ivCandle{{close: 102.5, high: 103, low: 100, position: -100}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newVPRunner(t, tt.configure)
			r.feed(vpShort...)
			for _, candle := range tt.candles {
				if candle.at == "24:00" { // the next day, at midnight
					r.day, candle.at = r.day.AddDate(0, 0, 1), "00:00"
				}
				r.feed(candle)
			}
			if tt.quote > 0 {
				require.InDelta(t, tt.quote, r.quote(), 1e-9)
			}
		})
	}
}

func TestVolumeProfile_TradesPerSession(t *testing.T) {
	// stopped out, back above the value area, then two closes inside again
	stopped := ivCandle{close: 104, high: 104.6, low: 103, position: 0}
	quote := 10000 - 100*4.5

	t.Run("one per session", func(t *testing.T) {
		r, _ := newVPRunner(t, nil)
		r.feed(vpShort...)
		r.feed(stopped, ivCandle{close: 101}, ivCandle{close: 100, position: 0})
	})

	t.Run("again in the next session", func(t *testing.T) {
		r, _ := newVPRunner(t, nil)
		r.feed(vpShort...)
		r.feed(stopped)
		r.day = r.day.AddDate(0, 0, 1)
		next := append(vpShort[:1:1], ivCandle{close: 100, high: 101.2, low: 99.5, position: -quote / 100})
		r.feed(next...)
	})

	t.Run("by pair", func(t *testing.T) {
		r, s := newVPRunner(t, nil)
		r.feed(vpShort...)
		require.Contains(t, s.traded, "BTCUSDT")
		require.Equal(t, 1, s.traded["BTCUSDT"].trades)
		require.NotContains(t, s.traded, "ETHUSDT")
	})
}

// The positions of the session opened before a restart count, told by the
// order controller from the orders it stored; the position found after it is
// closed at the end of the session it was opened in.
func TestVolumeProfile_AfterARestart(t *testing.T) {
	stopped := ivCandle{close: 104, high: 104.6, low: 103, position: 0}

	t.Run("the trade of the session before the restart counted", func(t *testing.T) {
		r, _ := newVPRunner(t, nil)
		bot := newTradingBot(t, r.wallet, r.step)
		r.broker, r.settle = bot.broker(), bot.settle
		r.feed(vpShort...)
		r.feed(stopped)

		bot.restart()
		r.s, r.broker = newVolumeProfile(t, nil), bot.broker()
		r.feed(ivCandle{close: 101}, ivCandle{close: 100, position: 0})
	})

	t.Run("the position found closed at the end of its session", func(t *testing.T) {
		r, _ := newVPRunner(t, nil)
		bot := newTradingBot(t, r.wallet, r.step)
		r.broker, r.settle = bot.broker(), bot.settle
		r.feed(vpShort...)

		bot.restart()
		r.s, r.broker = newVolumeProfile(t, nil), bot.broker()
		r.feed(ivCandle{at: "12:00", close: 100.5, position: -100}, ivCandle{at: "22:30", close: 100.5, position: -100},
			ivCandle{at: "23:00", close: 100.5, position: 0})
	})

	t.Run("the position of an earlier session found closed at once", func(t *testing.T) {
		r, _ := newVPRunner(t, nil)
		bot := newTradingBot(t, r.wallet, r.step)
		r.broker, r.settle = bot.broker(), bot.settle
		r.feed(vpShort...)

		bot.restart()
		r.s, r.broker = newVolumeProfile(t, nil), bot.broker()
		r.day = r.day.AddDate(0, 0, 1)
		r.feed(ivCandle{at: "00:00", close: 100.5, position: 0})
	})
}

func TestVolumeProfile_Backtest(t *testing.T) {
	if testing.Short() {
		t.Skip("long: left out by -short")
	}
	checkBacktest(t, newVolumeProfile(t, func(c *VolumeProfileConfig) { c.Timeframe = "1h" }))
}
