package strategies

import (
	"cmp"
	"context"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/tools/log"
)

// The days of the CMEGap tests, in January 2024, on the standard time of
// Chicago (UTC-6): the Friday close at 16:00 there is 22:00 UTC, the Sunday
// reopen at 17:00 there 23:00 UTC.
var (
	cgFriday = time.Date(2024, 1, 12, 0, 0, 0, 0, time.UTC)
	cgSunday = time.Date(2024, 1, 14, 0, 0, 0, 0, time.UTC)
	cgMonday = time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
)

// newCMEGap creates a CMEGap with its defaults, on the 1h timeframe.
func newCMEGap(t *testing.T, configure func(*CMEGapConfig)) *CMEGap {
	t.Helper()
	cfg := DefaultCMEGapConfig()
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewCMEGap(cfg)
	require.NoError(t, err)
	return s
}

// newCGRunner feeds a CMEGap hourly candles, with the ATR at 1.
func newCGRunner(t *testing.T, configure func(*CMEGapConfig)) (*ivRunner, *CMEGap) {
	t.Helper()
	s := newCMEGap(t, configure)
	r := newIVRunner(t, s, 0)
	r.step = time.Hour
	r.fixed["cg_atr"] = 1
	return r, s
}

// cgFeed feeds r the candles of day, their times HH:MM on it.
func cgFeed(r *ivRunner, day time.Time, candles ...ivCandle) {
	r.t.Helper()
	r.day = day
	r.feed(candles...)
}

// cgWeekClosed feeds r the last candles of the week before the Friday close,
// the reference of the gap the close of the one ending at it, at 21:00 UTC.
func cgWeekClosed(r *ivRunner, reference float64) {
	r.t.Helper()
	cgFeed(r, cgFriday, ivCandle{at: "19:00", close: 100}, ivCandle{close: 100}, ivCandle{close: reference})
}

// the sell of the tests: a gap up to 102 from the reference at 100, the stop
// loss at 103 and the take profit at 100
var cgShort = -10000 / 102.0

// cgSell feeds r the week closed at 100 and its reopen at 102: a sell.
func cgSell(r *ivRunner) {
	r.t.Helper()
	cgWeekClosed(r, 100)
	cgFeed(r, cgSunday, ivCandle{at: "23:00", close: 102, position: cgShort})
}

func TestNewCMEGap_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*CMEGapConfig)
		err       string
	}{
		{"timeframe", func(c *CMEGapConfig) { c.Timeframe = "" }, "timeframe"},
		{"invalid timeframe", func(c *CMEGapConfig) { c.Timeframe = "fast" }, "invalid timeframe"},
		{"timeframe longer than a day", func(c *CMEGapConfig) { c.Timeframe = "1w" }, "a day at most"},
		{"timezone", func(c *CMEGapConfig) { c.Timezone = "Mars/Olympus" }, "invalid timezone"},
		{"close", func(c *CMEGapConfig) { c.Close = "4pm" }, "HH:MM"},
		{"open", func(c *CMEGapConfig) { c.Open = "24:00" }, "HH:MM"},
		{"negative min gap", func(c *CMEGapConfig) { c.MinGapATR = -0.1 }, "min gap"},
		{"min gap not a number", func(c *CMEGapConfig) { c.MinGapATR = math.NaN() }, "min gap"},
		{"no expiry", func(c *CMEGapConfig) { c.ExpiryHours = 0 }, "expiry"},
		{"the stop loss", func(c *CMEGapConfig) { c.StopATR = 0 }, "stop loss"},
		{"the exits", func(c *CMEGapConfig) { c.Direction = "up" }, "invalid direction"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultCMEGapConfig()
			tt.configure(&cfg)
			_, err := NewCMEGap(cfg)
			require.ErrorContains(t, err, tt.err)
		})
	}
	_, err := NewCMEGap(DefaultCMEGapConfig())
	require.NoError(t, err)
}

func TestCMEGap_WarmupPeriod(t *testing.T) {
	// the weekend, from 16:00 on Friday to 17:00 on Sunday, an hour of daylight
	// saving time included, is 50h: 54 candles of 1h, less than the seed of
	// the ATR
	require.Equal(t, seedWarmup(14)+2, newCMEGap(t, nil).WarmupPeriod())

	for _, tt := range []struct {
		timeframe string
		warmup    int
	}{
		{"5m", 600 + 4 + 2},
		{"15m", 200 + 4 + 2},
		{"1m", 3000 + 4 + 2},
		{"1d", seedWarmup(14) + 2},
	} {
		s := newCMEGap(t, func(c *CMEGapConfig) { c.Timeframe = tt.timeframe })
		require.Equal(t, tt.warmup, s.WarmupPeriod(), tt.timeframe)
	}
}

func TestCMEWeek(t *testing.T) {
	s := newCMEGap(t, nil)
	week := s.week
	utc := func(month time.Month, day, hour int) time.Time {
		return time.Date(2024, month, day, hour, 0, 0, 0, time.UTC)
	}

	// the reopen at 17:00 in Chicago, the Friday close before it at 16:00
	require.Equal(t, utc(1, 14, 23), week.reopen(utc(1, 15, 0)).UTC(), "standard time")
	require.Equal(t, utc(1, 14, 23), week.reopen(utc(1, 14, 23)).UTC(), "at the reopen")
	require.Equal(t, utc(1, 7, 23), week.reopen(utc(1, 14, 22)).UTC(), "before the reopen of the Sunday")
	require.Equal(t, utc(1, 12, 22), week.closeBefore(week.reopen(utc(1, 15, 0))).UTC())
	require.Equal(t, utc(1, 19, 22), week.closeAfter(week.reopen(utc(1, 15, 0))).UTC())
	require.Equal(t, utc(7, 14, 22), week.reopen(utc(7, 15, 0)).UTC(), "daylight saving time")
	require.Equal(t, utc(7, 12, 21), week.closeBefore(utc(7, 14, 22)).UTC())

	// the weekends of the changes of the daylight saving time, on their Sunday
	require.Equal(t, utc(3, 10, 22), week.reopen(utc(3, 11, 0)).UTC(), "into daylight saving time")
	require.Equal(t, utc(3, 8, 22), week.closeBefore(utc(3, 10, 22)).UTC(), "the Friday before, standard")
	require.Equal(t, utc(11, 3, 23), week.reopen(utc(11, 4, 0)).UTC(), "back to standard time")
	require.Equal(t, utc(11, 1, 21), week.closeBefore(utc(11, 3, 23)).UTC(), "the Friday before, daylight")

	// the next Friday close: the one of the week, the next one over the
	// weekend and at the close itself
	require.Equal(t, utc(1, 19, 22), week.nextClose(utc(1, 15, 0)).UTC())
	require.Equal(t, utc(1, 19, 22), week.nextClose(utc(1, 13, 12)).UTC(), "on Saturday")
	require.Equal(t, utc(1, 26, 22), week.nextClose(utc(1, 19, 22)).UTC(), "at the close")
	require.Equal(t, utc(1, 19, 22), week.nextClose(utc(1, 19, 21)).UTC(), "before the close")

	require.Equal(t, 50*time.Hour, week.weekend())
}

func TestCMEGap_Entries(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*CMEGapConfig)
		feed      func(r *ivRunner)
	}{
		{name: "sell a gap up, toward the reference", feed: cgSell},
		{
			name: "buy a gap down",
			feed: func(r *ivRunner) {
				cgWeekClosed(r, 100)
				cgFeed(r, cgSunday, ivCandle{at: "23:00", close: 98, position: 10000 / 98.0})
			},
		},
		{
			name: "the reference is the close at the Friday close, not the ones of the weekend",
			feed: func(r *ivRunner) {
				cgWeekClosed(r, 100)
				r.feed(ivCandle{close: 104}, ivCandle{close: 104}) // 22:00 and 23:00, the CME closed
				cgFeed(r, cgSunday, ivCandle{at: "21:00", close: 104}, ivCandle{close: 104},
					ivCandle{close: 102, position: cgShort}) // 23:00: up 2 from 100, down 2 from 104
			},
		},
		{
			name: "no entry under the min gap",
			feed: func(r *ivRunner) {
				cgWeekClosed(r, 100)
				cgFeed(r, cgSunday, ivCandle{at: "23:00", close: 100.4})
			},
		},
		{
			name:      "the min gap is configurable",
			configure: func(c *CMEGapConfig) { c.MinGapATR = 0.3 },
			feed: func(r *ivRunner) {
				cgWeekClosed(r, 100)
				cgFeed(r, cgSunday, ivCandle{at: "23:00", close: 100.4, position: -10000 / 100.4})
			},
		},
		{
			name: "the min gap in ATR of the first candle of the week",
			feed: func(r *ivRunner) {
				cgWeekClosed(r, 100)
				cgFeed(r, cgSunday, ivCandle{at: "23:00", close: 102, meta: map[string]float64{"cg_atr": 5}})
			},
		},
		{
			name: "only the first candle of the week enters",
			feed: func(r *ivRunner) {
				cgWeekClosed(r, 100)
				cgFeed(r, cgSunday, ivCandle{at: "23:00", close: 100.2}, ivCandle{close: 103})
			},
		},
		{
			name: "a market closed at the weekend, reopening later: its first candle",
			feed: func(r *ivRunner) {
				cgWeekClosed(r, 100)
				cgFeed(r, cgMonday, ivCandle{at: "02:00", close: 102, position: cgShort})
			},
		},
		{
			name: "no entry without the reference in the window",
			feed: func(r *ivRunner) {
				cgFeed(r, cgFriday, ivCandle{at: "22:00", close: 100}, ivCandle{close: 100})
				cgFeed(r, cgSunday, ivCandle{at: "22:00", close: 100}, ivCandle{close: 102})
			},
		},
		{
			name: "no entry from a reference of the week before, a hole in the candles",
			feed: func(r *ivRunner) {
				cgFeed(r, cgFriday.AddDate(0, 0, -7), ivCandle{at: "20:00", close: 100}, ivCandle{close: 100})
				cgFeed(r, cgSunday, ivCandle{at: "23:00", close: 102})
			},
		},
		{
			name:      "the direction keeps the other side out",
			configure: func(c *CMEGapConfig) { c.Direction = RKDirectionBuyOnly },
			feed: func(r *ivRunner) {
				cgWeekClosed(r, 100)
				cgFeed(r, cgSunday, ivCandle{at: "23:00", close: 102})
			},
		},
		{
			name:      "the cost filter",
			configure: func(c *CMEGapConfig) { c.MinATRPct = 2 }, // an ATR of 1 is under 1% of 102
			feed: func(r *ivRunner) {
				cgWeekClosed(r, 100)
				cgFeed(r, cgSunday, ivCandle{at: "23:00", close: 102})
			},
		},
		{
			name: "the daylight saving time of Chicago: the close at 21:00 UTC, the reopen at 22:00 UTC",
			feed: func(r *ivRunner) {
				cgFeed(r, time.Date(2024, 7, 12, 0, 0, 0, 0, time.UTC),
					ivCandle{at: "19:00", close: 100}, ivCandle{close: 100}, // 20:00, ending at the close
					ivCandle{close: 104}) // 21:00, after it
				cgFeed(r, time.Date(2024, 7, 14, 0, 0, 0, 0, time.UTC),
					ivCandle{at: "21:00", close: 104}, ivCandle{close: 102, position: cgShort})
			},
		},
		{
			name: "the times of the week are configurable",
			configure: func(c *CMEGapConfig) {
				c.Timezone, c.Close, c.Open = "UTC", "20:00", "21:00"
			},
			feed: func(r *ivRunner) {
				cgFeed(r, cgFriday, ivCandle{at: "18:00", close: 100}, ivCandle{close: 100}, // ending at 20:00
					ivCandle{close: 104})
				cgFeed(r, cgSunday, ivCandle{at: "20:00", close: 104}, ivCandle{close: 102, position: cgShort})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newCGRunner(t, tt.configure)
			tt.feed(r)
		})
	}
}

func TestCMEGap_Exits(t *testing.T) {
	hold := ivCandle{close: 102, position: cgShort}
	flat := ivCandle{close: 102}

	t.Run("the take profit at the reference: the gap filled", func(t *testing.T) {
		r, _ := newCGRunner(t, nil)
		cgSell(r)
		r.feed(ivCandle{open: 102, close: 100.5, high: 102, low: 99.5})
		require.InDelta(t, 10000+2*10000/102.0, r.quote(), 1e-6)
	})

	t.Run("the stop loss stop-atr ATR from the entry, away from the reference", func(t *testing.T) {
		r, _ := newCGRunner(t, func(c *CMEGapConfig) { c.StopATR = 1.5 })
		cgSell(r)
		r.feed(ivCandle{open: 102, close: 103, high: 103.4, position: cgShort}) // under the stop loss at 103.5
		r.feed(ivCandle{open: 103, close: 103.2, high: 103.6})
		require.InDelta(t, 10000-1.5*10000/102.0, r.quote(), 1e-6)
	})

	t.Run("the expiry from the entry", func(t *testing.T) {
		r, _ := newCGRunner(t, func(c *CMEGapConfig) { c.ExpiryHours = 3 })
		cgSell(r)                             // closes at 00:00
		cgFeed(r, cgMonday, hold, hold, flat) // the candle of 02:00 closes 3h after the entry
		require.Equal(t, cgMonday.Add(2*time.Hour), r.df.Time[len(r.df.Time)-1])
		require.InDelta(t, 10000, r.quote(), 1e-6)
	})

	for _, expiry := range []int{120, 500} {
		t.Run("the last candle before the next Friday close", func(t *testing.T) {
			r, _ := newCGRunner(t, func(c *CMEGapConfig) { c.ExpiryHours = expiry })
			cgSell(r)
			// held until the candle of 19:00 on Friday; the one of 20:00
			// ends an hour before the close, at 22:00 UTC
			last := cgFriday.AddDate(0, 0, 7).Add(20 * time.Hour)
			cgFeed(r, cgMonday, ivCandle{at: "00:00", close: 102, position: cgShort})
			for r.df.Time[len(r.df.Time)-1].Add(r.step).Before(last) {
				r.feed(hold)
			}
			r.feed(flat)
			require.Equal(t, last, r.df.Time[len(r.df.Time)-1])
			require.InDelta(t, 10000, r.quote(), 1e-6)
		})
	}

	t.Run("max bars", func(t *testing.T) {
		r, _ := newCGRunner(t, func(c *CMEGapConfig) { c.MaxBars = 2 })
		cgSell(r)
		cgFeed(r, cgMonday, hold, flat)
	})
}

// A position found after a restart counts its expiry from its opening, when
// the broker tells it (the order controller, from the orders it stored), else
// from the first candle the strategy follows it on.
func TestCMEGap_ExpiryAfterARestart(t *testing.T) {
	configure := func(c *CMEGapConfig) { c.ExpiryHours = 3 }
	hold := ivCandle{close: 102, position: cgShort}
	flat := ivCandle{close: 102}

	t.Run("its opening told", func(t *testing.T) {
		r, _ := newCGRunner(t, configure)
		bot := newTradingBot(t, r.wallet, r.step)
		r.broker, r.settle = bot.broker(), bot.settle
		cgSell(r)
		cgFeed(r, cgMonday, hold)

		bot.restart()
		s := newCMEGap(t, configure)
		r.s, r.broker = s, bot.broker()
		r.feed(hold) // 01:00
		require.Equal(t, cgMonday, s.bracket.trades["BTCUSDT"].opened.UTC())
		r.feed(flat) // 02:00, closing 3h after the entry
	})

	t.Run("its opening told by MT5, a moment after the close of its entry", func(t *testing.T) {
		r, _ := newCGRunner(t, configure)
		bot := newMT5Bot(t, r.wallet, r.step)
		r.broker, r.settle = bot.broker(), bot.settle
		cgSell(r)
		cgFeed(r, cgMonday, hold)

		bot.restart()
		s := newCMEGap(t, configure)
		r.s, r.broker = s, bot.broker()
		r.feed(hold) // 01:00
		require.Equal(t, cgMonday.Add(2*time.Second), s.bracket.trades["BTCUSDT"].opened.UTC())
		r.feed(flat) // 02:00, closing 3h after the entry, not a candle later
	})

	t.Run("its opening unknown", func(t *testing.T) {
		r, _ := newCGRunner(t, configure)
		cgSell(r)

		s := newCMEGap(t, configure) // the wallet doesn't tell the openings
		r.s = s
		cgFeed(r, cgMonday, hold) // 00:00, followed from its close at 01:00
		require.Equal(t, cgMonday.Add(time.Hour), s.bracket.trades["BTCUSDT"].opened)
		r.feed(hold, hold, flat) // the candle of 03:00 closes 3h after 01:00
	})
}

func TestCMEGap_Entered(t *testing.T) {
	s := newCMEGap(t, nil)
	last := cgMonday.Add(5 * time.Hour) // the candle of 05:00
	for _, tt := range []struct {
		name   string
		opened time.Time
		want   time.Time
	}{
		{"at the close of its entry", cgMonday, cgMonday},
		{"a moment after it", cgMonday.Add(2 * time.Second), cgMonday},
		{"by hand within a candle: from its open", cgMonday.Add(30 * time.Minute), cgMonday},
		{"at the open of the last candle", last, last},
		{"within the last candle", last.Add(10 * time.Minute), last},
	} {
		require.Equal(t, tt.want, s.entered(tt.opened, last), tt.name)
	}
}

func TestCMEGap_Indicators(t *testing.T) {
	s := newCMEGap(t, nil)
	df := &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}
	add := func(at time.Time, closePrice float64) {
		df.Time = append(df.Time, at)
		df.Open, df.Close = append(df.Open, closePrice), append(df.Close, closePrice)
		df.High, df.Low = append(df.High, closePrice), append(df.Low, closePrice)
		df.Volume = append(df.Volume, 1)
	}
	add(cgFriday.Add(20*time.Hour), 101)
	add(cgFriday.Add(21*time.Hour), 102) // ending at the close: the reference
	add(cgFriday.Add(22*time.Hour), 103)
	add(cgSunday.Add(22*time.Hour), 104)
	add(cgSunday.Add(23*time.Hour), 105) // the reopen
	add(cgMonday, 106)

	chart := s.Indicators(df)
	require.Len(t, chart, 1)
	require.Equal(t, "CME close", chart[0].Metrics[0].Name)

	// the week before, its reference out of the window, and the weekend: none
	ref := df.Metadata["cg_ref"]
	for i := range 4 {
		require.True(t, isNa(ref[i]), "candle %d", i)
	}
	require.Equal(t, []float64{102, 102}, []float64(ref[4:]))
	require.Len(t, df.Metadata["cg_atr"], len(df.Close))
	require.Empty(t, s.bracket.trades, "the indicators don't change the strategy")
}

// On the hourly candles of the test data, the strategy enters on the first
// candle of each week of the CME only, and closes each position before the
// next Friday close.
func TestCMEGap_Backtest(t *testing.T) {
	checkBacktest(t, newCMEGap(t, nil))

	s := newCMEGap(t, nil)
	ctx := context.Background()
	feed, err := exchange.NewCSVFeed(s.Timeframe(), exchange.PairFeed{Pair: "BTCUSDT",
		File: "../../testdata/btc-1h.csv", Timeframe: "1h"})
	require.NoError(t, err)
	store, err := storage.FromMemory()
	require.NoError(t, err)
	wallet := exchange.NewPaperWallet(ctx, "USDT", exchange.WithPaperAsset("USDT", 10000),
		exchange.WithDataFeed(feed))
	bot, err := xtrader.NewBot(ctx, xtrader.Settings{Pairs: []string{"BTCUSDT"}}, wallet, s,
		xtrader.WithBacktest(wallet), xtrader.WithStorage(store), xtrader.WithLogLevel(log.ErrorLevel))
	require.NoError(t, err)
	require.NoError(t, bot.Run(ctx))

	orders, err := store.Orders()
	require.NoError(t, err)
	slices.SortFunc(orders, func(a, b *model.Order) int { return cmp.Compare(a.ID, b.ID) })

	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	var (
		position float64
		opened   time.Time
		entries  int
	)
	for _, order := range orders {
		if order.Status != model.OrderStatusTypeFilled {
			continue
		}
		quantity := order.Quantity
		if order.Side == model.SideTypeSell {
			quantity = -quantity
		}
		was := position
		if position += quantity; math.Abs(position) < 1e-6 {
			position = 0
		}
		switch {
		case was == 0 && position != 0:
			// stamped with the open of the candle whose close placed it: the
			// one opened at the reopen, 17:00 on Sunday in Chicago
			local := order.CreatedAt.In(chicago)
			require.Equal(t, time.Sunday, local.Weekday(), order.String())
			require.Equal(t, 17, local.Hour(), order.String())
			opened = order.CreatedAt
			entries++
		case was != 0 && position == 0:
			require.True(t, order.UpdatedAt.Before(s.week.nextClose(opened.Add(time.Hour))),
				"closed before the next Friday close: %s", order.String())
		}
	}
	require.Greater(t, entries, 10, "most weeks gap by half an ATR at least")
}
