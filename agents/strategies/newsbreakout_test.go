package strategies

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raykavin/xtrader"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/tools/log"
)

// fakeReleases is a source of releases of a list, recording the reads.
type fakeReleases struct {
	mu       sync.Mutex
	releases []Release
	err      error
	reads    []fakeRead
}

// fakeRead is a read of fakeReleases: its pair, its span and the time its
// context left it.
type fakeRead struct {
	pair     string
	from, to time.Time
	deadline time.Duration
}

func (f *fakeReleases) Releases(ctx context.Context, pair string, from, to time.Time) ([]Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	read := fakeRead{pair: pair, from: from, to: to}
	if deadline, ok := ctx.Deadline(); ok {
		read.deadline = time.Until(deadline)
	}
	f.reads = append(f.reads, read)
	if f.err != nil {
		return nil, f.err
	}
	var out []Release
	for _, release := range f.releases {
		if !release.Time.Before(from) && !release.Time.After(to) {
			out = append(out, release)
		}
	}
	return out, nil
}

func (f *fakeReleases) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reads)
}

func (f *fakeReleases) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// nbAt is the time of day clock, HH:MM, on the day of the runner.
func nbAt(t *testing.T, clock string) time.Time {
	t.Helper()
	at, err := time.Parse("15:04", clock)
	require.NoError(t, err)
	return time.Date(2024, 1, 15, at.Hour(), at.Minute(), 0, 0, time.UTC)
}

// nbRelease is a timed release of high importance at clock, the payrolls.
func nbRelease(t *testing.T, clock string) Release {
	t.Helper()
	return Release{Time: nbAt(t, clock), Currency: "USD", Code: "nonfarm-payrolls", Name: "Nonfarm Payrolls",
		Importance: ReleaseHigh, Timed: true}
}

// newNewsBreakout creates a NewsBreakout on the 5m timeframe with the
// defaults.
func newNewsBreakout(t *testing.T, configure func(*NewsBreakoutConfig)) *NewsBreakout {
	t.Helper()
	cfg := DefaultNewsBreakoutConfig()
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewNewsBreakout(cfg)
	require.NoError(t, err)
	return s
}

// newNBRunner feeds a NewsBreakout with the ATR at 1, its releases from a
// fake source holding releases.
func newNBRunner(t *testing.T, configure func(*NewsBreakoutConfig), releases ...Release) (*ivRunner,
	*NewsBreakout, *fakeReleases) {
	t.Helper()
	s := newNewsBreakout(t, configure)
	source := &fakeReleases{releases: releases}
	s.SetReleases(source)
	r := newIVRunner(t, s, 0)
	r.fixed["nb_atr"] = 1
	return r, s, source
}

// nbOneMinute runs a NewsBreakout on the 1m timeframe, its candles shorter
// than newsRefresh.
func nbOneMinute(c *NewsBreakoutConfig) { c.Timeframe = "1m" }

// set replaces the releases of the source.
func (f *fakeReleases) set(releases ...Release) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = releases
}

// the candles of the news tests, a release at 10:00: its range from 97 to 99
// for a long and from 101 to 103 for a short, over the 15 minutes after it,
// broken by a close at 100 at 10:15
var (
	nbLongRange = []ivCandle{
		{at: "09:50", close: 98},
		{close: 98},
		{at: "10:00", open: 98, close: 98.5, high: 99, low: 97},
		{close: 98, high: 98.8, low: 97.5},
		{close: 98.2, high: 98.6, low: 97.8},
	}
	nbLong       = append(nbLongRange[:len(nbLongRange):len(nbLongRange)], ivCandle{open: 98.5, close: 100, position: 100})
	nbShortRange = []ivCandle{
		{at: "09:50", close: 102},
		{close: 102},
		{at: "10:00", open: 102, close: 101.5, high: 103, low: 101},
		{close: 102, high: 102.5, low: 101.2},
		{close: 101.8, high: 102.2, low: 101.4},
	}
	nbShort = append(nbShortRange[:len(nbShortRange):len(nbShortRange)], ivCandle{open: 101.5, close: 100, position: -100})
)

// nbBreakout is a close at price above the long range, its entry holding
// position.
func nbBreakout(price, position float64) []ivCandle {
	return append(nbLongRange[:len(nbLongRange):len(nbLongRange)],
		ivCandle{open: 98.5, close: price, position: position})
}

// nbFlat returns n candles closing at price, holding position.
func nbFlat(n int, price, position float64) []ivCandle {
	candles := make([]ivCandle, n)
	for i := range candles {
		candles[i] = ivCandle{close: price, position: position}
	}
	return candles
}

func TestNewNewsBreakout_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*NewsBreakoutConfig)
		err       string
	}{
		{"timeframe", func(c *NewsBreakoutConfig) { c.Timeframe = "" }, "timeframe"},
		{"invalid timeframe", func(c *NewsBreakoutConfig) { c.Timeframe = "fast" }, "invalid timeframe"},
		{"no importance", func(c *NewsBreakoutConfig) { c.Importance = ReleaseNone }, "importance"},
		{"importance out of range", func(c *NewsBreakoutConfig) { c.Importance = ReleaseHigh + 1 }, "importance"},
		{"an empty code", func(c *NewsBreakoutConfig) { c.Codes = []string{"cpi", " "} }, "event code"},
		{"no wait", func(c *NewsBreakoutConfig) { c.Wait = 0 }, "wait"},
		{"wait in seconds", func(c *NewsBreakoutConfig) { c.Wait = 90 * time.Second }, "wait"},
		{"wait shorter than a candle", func(c *NewsBreakoutConfig) { c.Timeframe = "30m" }, "hold a candle"},
		{"no expiry", func(c *NewsBreakoutConfig) { c.Expiry = 0 }, "expiry"},
		{"expiry in seconds", func(c *NewsBreakoutConfig) { c.Expiry = 30*time.Minute + 30*time.Second }, "expiry"},
		{"expiry of a candle", func(c *NewsBreakoutConfig) { c.Expiry = 5 * time.Minute }, "longer than a candle"},
		{"negative buffer", func(c *NewsBreakoutConfig) { c.BufferATR = -1 }, "buffer"},
		{"negative max range", func(c *NewsBreakoutConfig) { c.MaxRangeATR = -1 }, "max range"},
		{"the exits", func(c *NewsBreakoutConfig) { c.StopATR = 0 }, "stop loss"},
		{"the sizing", func(c *NewsBreakoutConfig) { c.PositionFraction = 2 }, "position fraction"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultNewsBreakoutConfig()
			tt.configure(&cfg)
			_, err := NewNewsBreakout(cfg)
			require.ErrorContains(t, err, tt.err)
			require.ErrorContains(t, err, "newsbreakout: ")
		})
	}

	_, err := NewNewsBreakout(DefaultNewsBreakoutConfig())
	require.NoError(t, err)
}

func TestNewsBreakout_Defaults(t *testing.T) {
	cfg := DefaultNewsBreakoutConfig()
	require.Equal(t, "5m", cfg.Timeframe)
	require.Equal(t, ReleaseHigh, cfg.Importance)
	require.Empty(t, cfg.Codes)
	require.Equal(t, 15*time.Minute, cfg.Wait, "the window of the news guard after a release, by default")
	require.Equal(t, 60*time.Minute, cfg.Expiry)

	// any close beyond the range, no range too wide; the stop loss and the
	// take profit of ORB
	require.Zero(t, cfg.BufferATR)
	require.Zero(t, cfg.MaxRangeATR)
	require.Equal(t, DefaultORBConfig().BracketConfig, cfg.BracketConfig)
}

func TestNewsBreakout_WarmupPeriod(t *testing.T) {
	// the 75 minutes from the release to the end of the breakouts, 16 candles
	// of 5m with one more, under the seed of the ATR
	s := newNewsBreakout(t, nil)
	require.Equal(t, seedWarmup(14)+2, s.WarmupPeriod())

	// 2h of range and 4h of breakouts on the 1m timeframe: 360 candles, one
	// more for a release off the open of a candle
	s = newNewsBreakout(t, func(c *NewsBreakoutConfig) {
		c.Timeframe, c.Wait, c.Expiry = "1m", 2*time.Hour, 4*time.Hour
	})
	require.Equal(t, 360+1+2, s.WarmupPeriod())
}

func TestParseReleaseImportance(t *testing.T) {
	for value, want := range map[string]ReleaseImportance{"low": ReleaseLow, "medium": ReleaseMedium,
		"high": ReleaseHigh, " HIGH ": ReleaseHigh} {
		got, err := ParseReleaseImportance(value)
		require.NoError(t, err, value)
		require.Equal(t, want, got, value)
	}
	for _, value := range []string{"", "none", "critical"} {
		_, err := ParseReleaseImportance(value)
		require.ErrorContains(t, err, "low, medium or high", value)
	}
}

func TestParseReleaseCodes(t *testing.T) {
	require.Empty(t, ParseReleaseCodes(""))
	require.Empty(t, ParseReleaseCodes(" , "))
	require.Equal(t, []string{"cpi", "nonfarm-payrolls"}, ParseReleaseCodes(" cpi,, nonfarm-payrolls "))
	require.Empty(t, ParseReleaseCodes(" ; ;, "))
	require.Equal(t, []string{"cpi", "nonfarm-payrolls", "fomc"}, ParseReleaseCodes("cpi; nonfarm-payrolls;;fomc"),
		"; as the other lists of the agents")
	require.Equal(t, []string{"cpi", "nonfarm-payrolls", "fomc"}, ParseReleaseCodes("cpi,nonfarm-payrolls; fomc"))
}

func TestNewsBreakout_Entries(t *testing.T) {
	medium := func(t *testing.T) Release {
		release := nbRelease(t, "10:00")
		release.Importance = ReleaseMedium
		return release
	}
	untimed := func(t *testing.T) Release {
		release := nbRelease(t, "10:00")
		release.Timed = false
		return release
	}

	tests := []struct {
		name      string
		configure func(*NewsBreakoutConfig)
		releases  func(t *testing.T) []Release // the release at 10:00 when nil
		noSource  bool
		candles   []ivCandle
	}{
		{name: "buy on the first close above the range after the wait", candles: nbLong},
		{name: "sell on the first close below the range after the wait", candles: nbShort},
		{
			name: "no entry while the range is built",
			candles: append(nbLongRange[:4:4],
				ivCandle{open: 98.5, close: 100, high: 100, low: 98.4}, // 10:10, the last candle of the range
				ivCandle{close: 99.9}, // under its new high
			),
		},
		{name: "any close beyond the range by default", candles: nbBreakout(99.05, 10000/99.05)},
		{
			name:      "no entry within the buffer",
			configure: func(c *NewsBreakoutConfig) { c.BufferATR = 0.1 },
			candles:   nbBreakout(99.05, 0),
		},
		{
			// the first close beyond the range skipped, its ATR too small for
			// the costs: the next one isn't a breakout
			name:      "only the first close beyond the range",
			configure: func(c *NewsBreakoutConfig) { c.MinATRPct = 0.5 },
			candles: append(nbLongRange[:5:5],
				ivCandle{open: 98.5, close: 99.5, meta: map[string]float64{"nb_atr": 0.1}},
				ivCandle{close: 100}),
		},
		{
			name:      "the buffer in ATR",
			configure: func(c *NewsBreakoutConfig) { c.BufferATR = 2 },
			candles:   nbBreakout(100, 0), // 1 above the range, 2 needed
		},
		{
			name: "the candles before the release are left out of the range",
			candles: []ivCandle{
				{at: "09:50", close: 98},
				{close: 98, high: 105, low: 96}, // 09:55, before the release
				{at: "10:00", open: 98, close: 98.5, high: 99, low: 97},
				{close: 98, high: 98.8, low: 97.5},
				{close: 98.2, high: 98.6, low: 97.8},
				{open: 98.5, close: 100, position: 100},
			},
		},
		{
			name:     "the candle holding a release off its open is left out of the range",
			releases: func(t *testing.T) []Release { return []Release{nbRelease(t, "10:02")} },
			candles: []ivCandle{
				{at: "09:50", close: 98},
				{close: 98},
				{at: "10:00", open: 98, close: 98.5, high: 103, low: 97}, // holds the release
				{close: 98, high: 98.8, low: 97.5},                       // 10:05, the range to 10:17
				{close: 98.2, high: 98.6, low: 97.8},
				{close: 98.5}, // 10:15, neither the range nor a breakout
				{open: 98.5, close: 100, position: 100},
			},
		},
		{
			name: "the last candle closing before the expiry enters",
			candles: append(append(nbLongRange[:5:5], nbFlat(10, 98, 0)...), // 10:15 to 11:00
				ivCandle{at: "11:05", close: 100, position: 100}),
		},
		{
			name: "no entry on the candle closing at the expiry",
			candles: append(append(nbLongRange[:5:5], nbFlat(11, 98, 0)...), // 10:15 to 11:05
				ivCandle{at: "11:10", close: 100}),
		},
		{
			name:    "no entry after the expiry",
			candles: append(nbLongRange[:5:5], ivCandle{at: "11:20", close: 100}),
		},
		{
			name:      "the expiry is configurable",
			configure: func(c *NewsBreakoutConfig) { c.Expiry = 90 * time.Minute },
			candles:   append(nbLongRange[:5:5], ivCandle{at: "11:20", close: 100, position: 100}),
		},
		{
			name:      "the wait is configurable",
			configure: func(c *NewsBreakoutConfig) { c.Wait = 30 * time.Minute },
			candles: append(nbLongRange[:5:5],
				ivCandle{open: 98.5, close: 100}, // 10:15, in the range now
				ivCandle{close: 99}, ivCandle{close: 99},
				ivCandle{close: 101, position: 10000 / 101.0}, // 10:30
			),
		},
		{
			name:     "no entry without a release",
			releases: func(*testing.T) []Release { return nil },
			candles:  nbBreakout(100, 0),
		},
		{name: "no entry without a source of the releases", noSource: true, candles: nbBreakout(100, 0)},
		{
			name:     "a release of a lower importance is left out",
			releases: func(t *testing.T) []Release { return []Release{medium(t)} },
			candles:  nbBreakout(100, 0),
		},
		{
			name:      "the importance is configurable",
			configure: func(c *NewsBreakoutConfig) { c.Importance = ReleaseMedium },
			releases:  func(t *testing.T) []Release { return []Release{medium(t)} },
			candles:   nbLong,
		},
		{
			name:     "a release without a time is left out",
			releases: func(t *testing.T) []Release { return []Release{untimed(t)} },
			candles:  nbBreakout(100, 0),
		},
		{
			name:      "the codes leave the other events out",
			configure: func(c *NewsBreakoutConfig) { c.Codes = []string{"cpi"} },
			candles:   nbBreakout(100, 0),
		},
		{
			name:      "the codes take theirs, whatever the case",
			configure: func(c *NewsBreakoutConfig) { c.Codes = []string{"cpi", "NonFarm-Payrolls"} },
			candles:   nbLong,
		},
		{
			name: "no range without the release in the window",
			candles: []ivCandle{
				{at: "10:05", close: 98, high: 98.8, low: 97.5},
				{close: 98.2, high: 98.6, low: 97.8},
				{open: 98.5, close: 100},
			},
		},
		{
			name: "the next release ends the breakouts of the one before",
			releases: func(t *testing.T) []Release {
				return []Release{nbRelease(t, "10:00"), nbRelease(t, "10:25")}
			},
			candles: append(nbLongRange[:5:5],
				ivCandle{close: 98.4}, // 10:15, the last breakout of 10:00
				ivCandle{close: 100},  // 10:20, closing at 10:25
			),
		},
		{
			name:      "the max range filter skips a wide range",
			configure: func(c *NewsBreakoutConfig) { c.MaxRangeATR = 1.5 }, // a range of 2 ATR
			candles:   nbBreakout(100, 0),
		},
		{
			name:      "the max range filter takes a narrow one",
			configure: func(c *NewsBreakoutConfig) { c.MaxRangeATR = 2.5 },
			candles:   nbLong,
		},
		{
			name:      "the direction keeps the other side out",
			configure: func(c *NewsBreakoutConfig) { c.Direction = RKDirectionBuyOnly },
			candles:   append(nbShortRange[:5:5], ivCandle{open: 101.5, close: 100}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			releases := []Release{nbRelease(t, "10:00")}
			if tt.releases != nil {
				releases = tt.releases(t)
			}
			r, s, _ := newNBRunner(t, tt.configure, releases...)
			if tt.noSource {
				s.SetReleases(nil)
			}
			r.feed(tt.candles...)
		})
	}
}

func TestNewsBreakout_Exits(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*NewsBreakoutConfig)
		releases  func(t *testing.T) []Release // the release at 10:00 when nil
		candles   []ivCandle
		quote     float64 // after the trade, 10000 before
	}{
		{
			name: "the expiry closes the position",
			candles: append(nbFlat(10, 100.5, 100), // 10:20 to 11:05
				ivCandle{at: "11:10", close: 100.5, position: 0}),
			quote: 10000 + 100*0.5,
		},
		{
			name: "the next release closes it",
			releases: func(t *testing.T) []Release {
				return []Release{nbRelease(t, "10:00"), nbRelease(t, "10:40")}
			},
			candles: append(nbFlat(3, 100.5, 100), // 10:20 to 10:30
				ivCandle{close: 100.5, position: 0}), // 10:35, closing at 10:40
			quote: 10000 + 100*0.5,
		},
		{
			name:      "the close back past the middle of the range closes it",
			configure: func(c *NewsBreakoutConfig) { c.StopATR = 3 },
			candles:   []ivCandle{{close: 97.9, high: 99, low: 97.9, position: 0}},
			quote:     10000 - 100*2.1,
		},
		{
			name:      "a close back in the range above its middle keeps it",
			configure: func(c *NewsBreakoutConfig) { c.StopATR = 3 },
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
		{
			name:      "max bars closes it",
			configure: func(c *NewsBreakoutConfig) { c.MaxBars = 2 },
			candles:   []ivCandle{{close: 100.5, position: 100}, {close: 100.4, position: 0}},
			quote:     10000 + 100*0.4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			releases := []Release{nbRelease(t, "10:00")}
			if tt.releases != nil {
				releases = tt.releases(t)
			}
			r, _, _ := newNBRunner(t, tt.configure, releases...)
			r.feed(nbLong...)
			r.feed(tt.candles...)
			if tt.quote > 0 {
				require.InDelta(t, tt.quote, r.quote(), 1e-9)
			}
		})
	}
}

func TestNewsBreakout_PositionFound(t *testing.T) {
	// a position found (opened before a restart, or by hand): closed out of
	// the breakouts of a release once the releases tell it, as ORB closes it
	// out of its session; kept while they are unknown
	for _, tt := range []struct {
		name     string
		source   bool
		position float64
	}{
		{name: "out of the breakouts of a release: closed", source: true, position: 0},
		{name: "the releases unknown: kept", position: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newNewsBreakout(t, nil)
			if tt.source {
				s.SetReleases(&fakeReleases{})
			}
			r := newIVRunner(t, s, 1)
			r.fixed["nb_atr"] = 1
			r.feed(ivCandle{at: "09:00", close: 50, position: tt.position})
		})
	}
}

func TestNewsBreakout_TradesPerRelease(t *testing.T) {
	// a failed breakout, closed at 97.9, then a second close above the range
	failed := ivCandle{close: 97.9, high: 99, low: 97.9, position: 0}
	quote := 10000 - 100*2.1
	configure := func(c *NewsBreakoutConfig) { c.StopATR = 3 }

	t.Run("one per release", func(t *testing.T) {
		r, _, _ := newNBRunner(t, configure, nbRelease(t, "10:00"))
		r.feed(nbLong...)
		r.feed(failed, ivCandle{close: 100, position: 0})
	})

	t.Run("the releases at the same time are one", func(t *testing.T) {
		rate := nbRelease(t, "10:00")
		rate.Code, rate.Name = "unemployment-rate", "Unemployment Rate"
		r, _, _ := newNBRunner(t, configure, nbRelease(t, "10:00"), rate)
		r.feed(nbLong...)
		r.feed(failed, ivCandle{close: 100, position: 0})
	})

	t.Run("again on the next release", func(t *testing.T) {
		r, _, _ := newNBRunner(t, configure, nbRelease(t, "10:00"), nbRelease(t, "13:00"))
		r.feed(nbLong...)
		r.feed(failed)
		next := append([]ivCandle{
			{at: "12:50", close: 98}, {close: 98},
			{at: "13:00", open: 98, close: 98.5, high: 99, low: 97},
			{close: 98, high: 98.8, low: 97.5}, {close: 98.2, high: 98.6, low: 97.8},
		}, ivCandle{open: 98.5, close: 100, position: quote / 100})
		r.feed(next...)
	})
}

// The position of a release opened before a restart counts against its one
// trade, told by the order controller from the orders it stored; the next
// release trades again.
func TestNewsBreakout_TradesAfterARestart(t *testing.T) {
	failed := ivCandle{close: 97.9, high: 99, low: 97.9, position: 0}
	quote := 10000 - 100*2.1
	configure := func(c *NewsBreakoutConfig) { c.StopATR = 3 }

	restarted := func(t *testing.T, r *ivRunner, bot *tradingBot, source Releases) {
		t.Helper()
		bot.restart()
		s := newNewsBreakout(t, configure)
		s.SetReleases(source)
		r.s, r.broker = s, bot.broker()
	}

	t.Run("the trade of the release before the restart counted", func(t *testing.T) {
		r, _, source := newNBRunner(t, configure, nbRelease(t, "10:00"))
		bot := newTradingBot(t, r.wallet, r.step)
		r.broker, r.settle = bot.broker(), bot.settle
		r.feed(nbLong...)
		r.feed(failed)

		restarted(t, r, bot, source)
		r.feed(ivCandle{close: 100, position: 0})
	})

	t.Run("the next release", func(t *testing.T) {
		r, _, source := newNBRunner(t, configure, nbRelease(t, "10:00"), nbRelease(t, "13:00"))
		bot := newTradingBot(t, r.wallet, r.step)
		r.broker, r.settle = bot.broker(), bot.settle
		r.feed(nbLong...)
		r.feed(failed)

		restarted(t, r, bot, source)
		r.feed(ivCandle{at: "12:50", close: 98}, ivCandle{close: 98},
			ivCandle{at: "13:00", open: 98, close: 98.5, high: 99, low: 97},
			ivCandle{close: 98, high: 98.8, low: 97.5}, ivCandle{close: 98.2, high: 98.6, low: 97.8},
			ivCandle{open: 98.5, close: 100, position: quote / 100})
	})
}

func TestNewsBreakout_Reads(t *testing.T) {
	t.Run("once every few minutes of the candles", func(t *testing.T) {
		// the candles of a minute fed at once, as a backtest feeds them: the
		// clock of the reads is the close of the candles, not the wall clock
		r, _, source := newNBRunner(t, nbOneMinute, nbRelease(t, "10:00"))
		r.step = time.Minute
		r.feed(ivCandle{at: "09:50", close: 98}) // closed at 09:51
		require.Equal(t, 1, source.count())
		r.feed(nbFlat(4, 98, 0)...) // closed at 09:52 to 09:55
		require.Equal(t, 1, source.count(), "the releases read 4 minutes before")
		r.feed(ivCandle{close: 98}) // closed at 09:56
		require.Equal(t, 2, source.count(), "read again once newsRefresh passed")
		r.feed(nbFlat(4, 98, 0)...)
		require.Equal(t, 2, source.count())
		r.feed(ivCandle{close: 98})
		require.Equal(t, 3, source.count())

		// the default 5m candles: each one closes newsRefresh after the one
		// before
		r, _, source = newNBRunner(t, nil, nbRelease(t, "10:00"))
		r.feed(nbLong...)
		require.Equal(t, len(nbLong), source.count())
	})

	t.Run("at once when the releases read no longer cover the candle", func(t *testing.T) {
		// a backtest: the candles go faster than the wall clock
		r, _, source := newNBRunner(t, nil, nbRelease(t, "10:00"))
		r.feed(nbLongRange[0])
		require.Equal(t, 1, source.count())
		r.day = r.day.Add(newsAhead)
		r.feed(nbLongRange[0])
		require.Equal(t, 2, source.count())
	})

	t.Run("around the candle, with a short context", func(t *testing.T) {
		r, s, source := newNBRunner(t, nil, nbRelease(t, "10:00"))
		r.feed(nbLongRange[0])
		require.Len(t, source.reads, 1)
		read, at := source.reads[0], nbAt(t, "09:50")
		require.Equal(t, "BTCUSDT", read.pair)
		reach := s.cfg.Wait + s.cfg.Expiry + 5*time.Minute
		require.False(t, read.from.After(at.Add(-reach)), "back to the releases whose breakouts hold the candle")
		require.False(t, read.from.After(at.Add(-time.Duration(s.WarmupPeriod())*5*time.Minute)),
			"back to the window of the candles, for the chart")
		require.False(t, read.to.Before(at.Add(reach)), "ahead to the release that may end them")
		require.Positive(t, read.deadline)
		require.LessOrEqual(t, read.deadline, newsReadTimeout)
	})

	t.Run("a failed read keeps the releases read before it", func(t *testing.T) {
		r, _, source := newNBRunner(t, nbOneMinute, nbRelease(t, "10:00"))
		r.step = time.Minute
		r.feed(ivCandle{at: "09:58", close: 98}) // read, closed at 09:59
		source.fail(errors.New("calendar down"))
		r.feed(nbFlat(4, 98, 0)...) // closed at 10:00 to 10:03
		require.Equal(t, 1, source.count())
		r.feed(ivCandle{close: 98}) // closed at 10:04
		require.Equal(t, 2, source.count(), "tried again once newsRefresh passed")
		r.feed(nbFlat(4, 98, 0)...)
		require.Equal(t, 2, source.count(), "not on every candle")

		// the range of 10:00 to 10:15 broken at 10:15, on the releases read
		// before the failures, tried every 5 minutes meanwhile (10:09, 10:14)
		r.feed(ivCandle{at: "10:00", open: 98, close: 98.5, high: 99, low: 97})
		r.feed(nbFlat(14, 98, 0)...)
		r.feed(ivCandle{open: 98.5, close: 100, position: 100})
		require.Equal(t, 4, source.count())
	})

	t.Run("a failed first read: no entry, tried again on the next candles", func(t *testing.T) {
		r, _, source := newNBRunner(t, nil, nbRelease(t, "10:00"))
		source.fail(errors.New("news disabled"))
		r.feed(nbBreakout(100, 0)...)
		require.Equal(t, len(nbLongRange)+1, source.count(), "each candle newsRefresh after the one before")

		source.fail(nil)
		r.feed(ivCandle{close: 98.5}) // 10:20, back in the range, read
		require.Equal(t, len(nbLongRange)+2, source.count())
		r.feed(ivCandle{close: 100, position: 100}) // 10:25, broken again
	})

	t.Run("a failed first read tried again after a few minutes of the candles", func(t *testing.T) {
		r, _, source := newNBRunner(t, nbOneMinute, nbRelease(t, "10:00"))
		r.step = time.Minute
		source.fail(errors.New("news disabled"))
		r.feed(ivCandle{at: "09:50", close: 98})
		r.feed(nbFlat(4, 98, 0)...)
		require.Equal(t, 1, source.count())
		r.feed(ivCandle{close: 98})
		require.Equal(t, 2, source.count())
	})

	t.Run("another source drops the releases read", func(t *testing.T) {
		r, s, source := newNBRunner(t, nbOneMinute, nbRelease(t, "10:00"))
		r.step = time.Minute
		r.feed(ivCandle{at: "09:50", close: 98})
		other := &fakeReleases{}
		s.SetReleases(other)
		r.feed(ivCandle{close: 98}) // a minute later: read at once from the other one
		require.Equal(t, 1, source.count())
		require.Equal(t, 1, other.count())
	})

	t.Run("each pair its own", func(t *testing.T) {
		s := newNewsBreakout(t, nil)
		source := &fakeReleases{}
		s.SetReleases(source)
		for _, pair := range []string{"XAUUSDm", "BTCUSDm", "XAUUSDm"} {
			df := &model.Dataframe{Pair: pair, Time: []time.Time{nbAt(t, "10:00")}, Close: model.Series[float64]{1},
				Open: model.Series[float64]{1}, High: model.Series[float64]{1}, Low: model.Series[float64]{1},
				Volume: model.Series[float64]{1}, Metadata: map[string]model.Series[float64]{}}
			s.refresh(df.Pair, df.Time[0])
		}
		require.Len(t, source.reads, 2)
		require.Equal(t, "XAUUSDm", source.reads[0].pair)
		require.Equal(t, "BTCUSDm", source.reads[1].pair)
	})
}

func TestNewsBreakout_Levels(t *testing.T) {
	// the range of the release on the candles of its breakouts only
	r, s, source := newNBRunner(t, nil, nbRelease(t, "10:00"))
	r.feed(nbLong...)
	r.feed(ivCandle{close: 100.2, position: 100})

	high, low := r.df.Metadata["nb_high"], r.df.Metadata["nb_low"]
	for i := range r.df.Time {
		if i < 5 {
			require.Truef(t, isNa(high[i]) && isNa(low[i]), "candle %d is not a breakout", i)
			continue
		}
		require.InDelta(t, 99, high[i], 1e-9)
		require.InDelta(t, 97, low[i], 1e-9)
	}

	// drawn from the releases read: Indicators reads none
	reads := source.count()
	require.Len(t, s.Indicators(r.df), 1)
	other := &model.Dataframe{Pair: "ETHUSDT", Time: r.df.Time, Open: r.df.Open, Close: r.df.Close, High: r.df.High,
		Low: r.df.Low, Volume: r.df.Volume, Metadata: map[string]model.Series[float64]{}}
	s.Indicators(other)
	require.Equal(t, reads, source.count())
	for i := range other.Time {
		require.True(t, isNa(other.Metadata["nb_high"][i]), "no release read for the pair")
	}
}

// A release tentative when read first (no time to trade around, see
// Release.Timed), its time told later, as the calendar tells it live, is
// traded once a read on the clock of the candles sees it timed: on the candles
// fed at once, as a backtest feeds them, whatever the wall clock.
func TestNewsBreakout_ReleaseTimedLater(t *testing.T) {
	timed := nbRelease(t, "10:00")
	tentative := timed
	tentative.Timed = false

	t.Run("on the next read", func(t *testing.T) {
		r, _, source := newNBRunner(t, nil, tentative)
		r.feed(nbLongRange[:4]...) // to 10:05
		require.Equal(t, 4, source.count())
		source.set(timed)
		r.feed(nbLongRange[4], ivCandle{open: 98.5, close: 100, position: 100}) // read again at 10:15
	})

	t.Run("once newsRefresh of the candles passed", func(t *testing.T) {
		r, _, source := newNBRunner(t, nbOneMinute, tentative)
		r.step = time.Minute
		r.feed(ivCandle{at: "09:50", close: 98})
		source.set(timed)
		r.feed(nbFlat(3, 98, 0)...)
		require.Equal(t, 1, source.count(), "the release told tentative still")
		r.feed(ivCandle{at: "10:00", open: 98, close: 98.5, high: 99, low: 97}) // closed at 09:55: read again
		require.Equal(t, 2, source.count())
		r.feed(nbFlat(14, 98, 0)...)
		r.feed(ivCandle{open: 98.5, close: 100, position: 100}) // 10:15, the range broken
	})

	t.Run("never while tentative", func(t *testing.T) {
		r, _, _ := newNBRunner(t, nil, tentative)
		r.feed(nbBreakout(100, 0)...)
	})
}

// The chart calls Indicators at any time, while the candles run OnCandle: run
// with -race.
func TestNewsBreakout_IndicatorsWhileTrading(t *testing.T) {
	r, s, _ := newNBRunner(t, nil, nbRelease(t, "10:00"))
	chart := &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}
	for i := range 30 {
		at := nbAt(t, "09:30").Add(time.Duration(i) * 5 * time.Minute)
		chart.Time = append(chart.Time, at)
		for _, series := range []*model.Series[float64]{&chart.Open, &chart.Close, &chart.High, &chart.Low,
			&chart.Volume} {
			*series = append(*series, 100)
		}
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-done:
				return
			default:
				df := *chart
				df.Metadata = map[string]model.Series[float64]{}
				s.Indicators(&df)
			}
		}
	})
	for range 3 {
		r.feed(nbLongRange[0])
		r.day = r.day.Add(newsAhead) // read again
	}
	close(done)
	wg.Wait()
}

// nbReleasesOf returns a release at 14:00 UTC on each weekday from the first
// candle of the test data to its last.
func nbReleasesOf() []Release {
	var releases []Release
	from, to := time.Unix(1605571200, 0).UTC(), time.Unix(1621123200, 0).UTC()
	for day := from.Truncate(24 * time.Hour); !day.After(to); day = day.Add(24 * time.Hour) {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		releases = append(releases, Release{Time: day.Add(14 * time.Hour), Currency: "USD", Code: "test",
			Name: "Test", Importance: ReleaseHigh, Timed: true})
	}
	return releases
}

// newNBBacktest creates a NewsBreakout on the 1h candles of the test data,
// the releases of nbReleasesOf, the range of the hour after each broken in
// the 6 hours after it.
func newNBBacktest(t *testing.T) *NewsBreakout {
	t.Helper()
	s := newNewsBreakout(t, func(c *NewsBreakoutConfig) {
		c.Timeframe, c.Wait, c.Expiry = "1h", time.Hour, 6*time.Hour
	})
	s.SetReleases(&fakeReleases{releases: nbReleasesOf()})
	return s
}

func TestNewsBreakout_Backtest(t *testing.T) {
	if testing.Short() {
		t.Skip("long: left out by -short")
	}
	checkBacktest(t, newNBBacktest(t))
}

// No lookahead: the entries of a backtest of the test data cut in half are
// the ones of the whole test data up to the cut.
func TestNewsBreakout_NoLookahead(t *testing.T) {
	if testing.Short() {
		t.Skip("long: left out by -short")
	}

	file, err := os.Open("../../testdata/btc-1h.csv")
	require.NoError(t, err)
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	require.NoError(t, scanner.Err())
	half := filepath.Join(t.TempDir(), "btc-1h-half.csv")
	cut := len(lines) / 2
	require.NoError(t, os.WriteFile(half, []byte(strings.Join(lines[:cut], "\n")+"\n"), 0o600))

	whole := nbEntries(t, newNBBacktest(t), "../../testdata/btc-1h.csv")
	part := nbEntries(t, newNBBacktest(t), half)
	require.NotEmpty(t, part)

	last := part[len(part)-1].time
	var before []nbEntry
	for _, entry := range whole {
		if !entry.time.After(last) {
			before = append(before, entry)
		}
	}
	require.Equal(t, before, part)
}

// nbEntry is an entry of a backtest: its time, side and price.
type nbEntry struct {
	time  time.Time
	side  model.SideType
	price float64
}

// nbEntries runs s on the 1h candles of file and returns its entries, see
// checkBacktest.
func nbEntries(t *testing.T, s *NewsBreakout, file string) []nbEntry {
	t.Helper()
	ctx := context.Background()
	csvFeed, err := exchange.NewCSVFeed(s.Timeframe(), exchange.PairFeed{Pair: "BTCUSDT", File: file,
		Timeframe: "1h"})
	require.NoError(t, err)
	store, err := storage.FromMemory()
	require.NoError(t, err)
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 10000),
		exchange.WithPaperFee(0.001, 0.001), exchange.WithDataFeed(csvFeed))
	bot, err := xtrader.NewBot(ctx, xtrader.Settings{Pairs: []string{"BTCUSDT"}}, wallet, s,
		xtrader.WithBacktest(wallet), xtrader.WithStorage(store), xtrader.WithLogLevel(log.ErrorLevel))
	require.NoError(t, err)
	require.NoError(t, bot.Run(ctx))

	orders, err := store.Orders()
	require.NoError(t, err)
	slices.SortFunc(orders, func(a, b *model.Order) int { return cmp.Compare(a.ID, b.ID) })
	var entries []nbEntry
	position := 0.0
	for _, order := range orders {
		if order.Status != model.OrderStatusTypeFilled {
			continue
		}
		if order.Type == model.OrderTypeMarket && position == 0 {
			entries = append(entries, nbEntry{time: order.CreatedAt.UTC(), side: order.Side, price: order.Price})
		}
		quantity := order.Quantity
		if order.Side == model.SideTypeSell {
			quantity = -quantity
		}
		if position += quantity; position < 1e-6 && position > -1e-6 {
			position = 0
		}
	}
	return entries
}
