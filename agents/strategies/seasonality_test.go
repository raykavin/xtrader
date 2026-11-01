package strategies

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

func newSeasonality(t *testing.T, configure func(*SeasonalityConfig)) *Seasonality {
	t.Helper()
	cfg := DefaultSeasonalityConfig()
	if configure != nil {
		configure(&cfg)
	}
	s, err := NewSeasonality(cfg)
	require.NoError(t, err)
	return s
}

func TestNewSeasonality_InvalidConfig(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*SeasonalityConfig)
		err       string
	}{
		{"timeframe", func(c *SeasonalityConfig) { c.Timeframe = "" }, "timeframe"},
		{"timeframe unknown", func(c *SeasonalityConfig) { c.Timeframe = "soon" }, "invalid timeframe"},
		{"no window", func(c *SeasonalityConfig) { c.Windows = " ; " }, "no window"},
		{"no zone", func(c *SeasonalityConfig) { c.Windows = "10:30/60" }, "expected HH:MM/minutes@zone"},
		{"no minutes", func(c *SeasonalityConfig) { c.Windows = "10:30@Europe/London" }, "expected HH:MM/minutes@zone"},
		{"zero minutes", func(c *SeasonalityConfig) { c.Windows = "10:30/0@Europe/London" }, "expected HH:MM"},
		{"minutes not a number", func(c *SeasonalityConfig) { c.Windows = "10:30/1h@Europe/London" }, "expected HH:MM"},
		{"time of day", func(c *SeasonalityConfig) { c.Windows = "25:00/60@Europe/London" }, "invalid time of day"},
		{"zone", func(c *SeasonalityConfig) { c.Windows = "10:30/60@Mars/Olympus" }, "invalid timezone"},
		{"twice", func(c *SeasonalityConfig) { c.Windows = "10:30/60@Europe/London; 10:30/60@Europe/London" },
			"given twice"},
		{"twice written apart", func(c *SeasonalityConfig) {
			c.Windows = "9:30/60@America/New_York;09:30/60@America/New_York"
		}, "given twice"},
		{"not a multiple of the timeframe", func(c *SeasonalityConfig) { c.Windows = "10:30/50@Europe/London" },
			"multiple of the timeframe"},
		{"a day long", func(c *SeasonalityConfig) { c.Windows = "00:00/1440@UTC" }, "under 23 hours"},
		{"off the candles", func(c *SeasonalityConfig) { c.Windows = "10:40/60@Europe/London" },
			"not on the open of a candle of 15m"},
		{"off the candles of 1h", func(c *SeasonalityConfig) { c.Timeframe = "1h" },
			"10:30/60@Europe/London starts at 10:30 UTC on 2024-01-15, not on the open of a candle of 1h"},
		{"off the candles in a zone of half hours", func(c *SeasonalityConfig) {
			c.Timeframe, c.Windows = "1h", "10:00/60@Asia/Kolkata"
		}, "starts at 04:30 UTC"},
		{"min days", func(c *SeasonalityConfig) { c.MinDays = 1 }, "2 at least"},
		{"days under the min", func(c *SeasonalityConfig) { c.Days = 19 }, "at least its min days"},
		{"min t", func(c *SeasonalityConfig) { c.MinT = -1 }, "can't be negative"},
		{"min t not a number", func(c *SeasonalityConfig) { c.MinT = math.NaN() }, "can't be negative"},
		{"the min days beyond the window kept", func(c *SeasonalityConfig) { c.Timeframe = "1m" },
			"the 20 min days of a window on 1m need 29015 candles"},
		{"the exits", func(c *SeasonalityConfig) { c.TakeProfitATR = 0 }, "take profit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultSeasonalityConfig()
			tt.configure(&cfg)
			_, err := NewSeasonality(cfg)
			require.ErrorContains(t, err, tt.err)
			require.ErrorContains(t, err, "seasonality: ")
		})
	}

	s := newSeasonality(t, nil)
	require.Equal(t, "15m", s.Timeframe())
	require.Equal(t, []string{"10:30/60@Europe/London", "15:00/60@Europe/London", "09:30/60@America/New_York"},
		[]string{s.windows[0].name, s.windows[1].name, s.windows[2].name})
	require.Equal(t, 3.0, s.cfg.StopATR)
	require.Equal(t, 4.5, s.cfg.TakeProfitATR)
	require.GreaterOrEqual(t, s.cfg.TakeProfitATR, DefaultBracketConfig().TakeProfitATR, "a far target")
	require.Zero(t, s.cfg.MaxBars, "closed at the end of its window")

	// a window of the half hours on 30m, from the zone of its city
	newSeasonality(t, func(c *SeasonalityConfig) { c.Timeframe, c.Windows = "30m", "10:00/60@Asia/Kolkata" })
	// MinT 0 trades every window by the sign of its mean
	newSeasonality(t, func(c *SeasonalityConfig) { c.MinT = 0 })
}

// The window reaches back to the windows of the last Days days, as if the
// market traded every hour of every day, an hour more for a change of the
// daylight saving time, capped at seasonalityMaxWarmup candles.
func TestSeasonality_WarmupPeriod(t *testing.T) {
	for _, tt := range []struct {
		name      string
		configure func(*SeasonalityConfig)
		warmup    int
	}{
		{"the defaults: 60 days of 15m", nil, 60*96 + 4 + seedWarmup(14) + 1},
		{"5m", func(c *SeasonalityConfig) { c.Timeframe = "5m" }, 60*288 + 12 + seedWarmup(14) + 1},
		{"30m", func(c *SeasonalityConfig) { c.Timeframe = "30m" }, 60*48 + 2 + seedWarmup(14) + 1},
		{"10 days of 1h", func(c *SeasonalityConfig) {
			c.Timeframe, c.Windows, c.Days, c.MinDays = "1h", "10:00/60@Europe/London", 10, 5
		}, 10*24 + 1 + seedWarmup(14) + 1},
		{"the seed of another ATR", func(c *SeasonalityConfig) {
			c.Timeframe, c.Windows, c.Days, c.MinDays, c.ATRLen = "1h", "10:00/60@Europe/London", 2, 2, 20
		}, 2*24 + 1 + seedWarmup(20) + 1},
		{"capped", func(c *SeasonalityConfig) { c.Timeframe, c.Days = "5m", 100 }, seasonalityMaxWarmup},
		{"capped, with a huge number of days", func(c *SeasonalityConfig) { c.Days = math.MaxInt32 },
			seasonalityMaxWarmup},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.warmup, newSeasonality(t, tt.configure).WarmupPeriod())
		})
	}
}

// seasonDF returns a dataframe of n candles of step from from, each opening at
// the close of the one before, from 100, its return ret(t) at its open time t
// (0 flat), and the ones of skip(t) left out, if skip is set.
func seasonDF(from time.Time, step time.Duration, n int, ret func(t time.Time) float64,
	skip func(t time.Time) bool) *model.Dataframe {
	df := &model.Dataframe{Pair: "BTCUSDT", Metadata: map[string]model.Series[float64]{}}
	price := 100.0
	for k := range n {
		at := from.Add(time.Duration(k) * step)
		closePrice := price * (1 + ret(at))
		if skip == nil || !skip(at) {
			df.Time = append(df.Time, at)
			df.Open = append(df.Open, price)
			df.Close = append(df.Close, closePrice)
			df.High = append(df.High, math.Max(price, closePrice))
			df.Low = append(df.Low, math.Min(price, closePrice))
			df.Volume = append(df.Volume, 1)
		}
		price = closePrice
	}
	return df
}

// The windows follow the daylight saving time of their cities: 10:30 in London
// is 10:30 UTC until the last Sunday of March, 09:30 UTC after it; 09:30 in New
// York is 14:30 UTC until the second Sunday of March, 13:30 UTC after it. The
// return of a window is the one of its candles, from the open of the first to
// the close of the last.
func TestSeasonality_WindowsAcrossTheDaylightSavingTime(t *testing.T) {
	s := newSeasonality(t, func(c *SeasonalityConfig) {
		c.Windows = "10:30/60@Europe/London;09:30/60@America/New_York"
	})
	london, newYork := s.windows[0], s.windows[1]
	from := time.Date(2024, 3, 5, 0, 0, 0, 0, time.UTC)
	const days = 32 // to the 5th of April

	// a return on the second candle of each window, its own on each day
	move := func(w seasonWindow, day int) float64 {
		if w.loc == london.loc {
			return 0.001 * float64(day+1)
		}
		return -0.002 * float64(day+1)
	}
	df := seasonDF(from, 15*time.Minute, days*96, func(at time.Time) float64 {
		day := int(at.Sub(from) / rkDay)
		for _, w := range s.windows {
			if at.Equal(w.start.on(at, w.loc, 0).Add(15 * time.Minute)) {
				return move(w, day)
			}
		}
		return 0
	}, nil)

	for _, w := range []seasonWindow{london, newYork} {
		traded := w.traded(df, s.tf)
		require.Len(t, traded, days, w.name)
		for day, got := range traded {
			date := from.AddDate(0, 0, day)
			clock := time.Duration(w.start)
			end := time.Date(date.Year(), date.Month(), date.Day(), int(clock/time.Hour),
				int(clock%time.Hour/time.Minute), 0, 0, w.loc).Add(time.Hour)
			require.True(t, end.Equal(got.end), "%s on %s: %s", w.name, date.Format("01-02"), got.end.UTC())
			require.InDelta(t, move(w, day), got.ret, 1e-12, "%s on %s", w.name, date.Format("01-02"))
		}
	}

	ended := func(w seasonWindow, day int) string { return w.traded(df, s.tf)[day].end.UTC().Format("01-02 15:04") }
	require.Equal(t, "03-05 11:30", ended(london, 0))
	require.Equal(t, "03-29 11:30", ended(london, 24), "Friday, before the change")
	require.Equal(t, "04-01 10:30", ended(london, 27), "Monday, after it")
	require.Equal(t, "03-08 15:30", ended(newYork, 3), "Friday, before the change")
	require.Equal(t, "03-11 14:30", ended(newYork, 6), "Monday, after it")
}

// A day on which the window didn't trade isn't measured: the weekend of a
// market closed then, the first or the last candle of the window missing; a
// candle missing inside it still leaves the return of its first open to its
// last close.
func TestSeasonality_DaysNotTraded(t *testing.T) {
	s := newSeasonality(t, func(c *SeasonalityConfig) { c.Windows = "10:30/60@Europe/London" })
	w := s.windows[0]
	from := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC) // a Monday, London on UTC
	clock := func(at time.Time) string { return at.Format("15:04") }
	df := seasonDF(from, 15*time.Minute, 14*96, func(at time.Time) float64 {
		if clock(at) == "10:45" || clock(at) == "11:00" {
			return 0.001
		}
		return 0
	}, func(at time.Time) bool {
		day := at.Format("01-02")
		switch {
		case at.Weekday() == time.Saturday || at.Weekday() == time.Sunday:
			return true // the weekend, a market closed
		case day == "01-16" && clock(at) == "10:30":
			return true // the first candle of the window missing
		case day == "01-17" && clock(at) == "11:15":
			return true // its last one
		case day == "01-18" && clock(at) == "10:45":
			return true // one inside it
		}
		return false
	})

	traded := w.traded(df, s.tf)
	var dates []string
	for _, day := range traded {
		dates = append(dates, day.end.Format("01-02"))
	}
	require.Equal(t, []string{"01-15", "01-18", "01-19", "01-22", "01-23", "01-24", "01-25", "01-26"}, dates)
	require.InDelta(t, 1.001*1.001-1, traded[0].ret, 1e-12)
	require.InDelta(t, 1.001*1.001-1, traded[1].ret, 1e-12, "the 10:45 candle missing, its move still in")
}

// The t statistic of a window: the mean of its returns over the sample
// deviation of their mean, on the last Days days ended before its start, the
// day starting then never among them.
func TestSeasonality_Bias(t *testing.T) {
	s := newSeasonality(t, func(c *SeasonalityConfig) { c.Days, c.MinDays = 5, 3 })
	from := time.Date(2024, 1, 15, 11, 30, 0, 0, time.UTC)
	days := func(returns ...float64) []seasonDay {
		list := make([]seasonDay, len(returns))
		for i, ret := range returns {
			list[i] = seasonDay{end: from.AddDate(0, 0, i), ret: ret / 100}
		}
		return list
	}
	start := func(day int) time.Time { return from.AddDate(0, 0, day).Add(-time.Hour) } // 10:30 of day

	sample := days(0.4, 0.6, 0.5, 0.7, 0.3, 0.9)
	b, ok := s.bias(sample, start(6))
	require.True(t, ok)
	require.Equal(t, 5, b.days, "the last 5")
	require.InDelta(t, 0.006, b.mean, 1e-12)
	require.InDelta(t, 6, b.t, 1e-9, "0.6 / (√(0.2/4) / √5)")

	b, ok = s.bias(sample, start(5))
	require.True(t, ok)
	require.Equal(t, 5, b.days, "the day starting then not ended: left out")
	require.InDelta(t, 0.005, b.mean, 1e-12)
	require.InDelta(t, 0.5/math.Sqrt(0.025/5), b.t, 1e-9)

	b, ok = s.bias(sample, start(2))
	require.False(t, ok, "2 days, under the min of 3")
	require.Equal(t, 2, b.days)
	require.InDelta(t, 0.005, b.mean, 1e-12)

	b, ok = s.bias(days(0.4, -0.5, 0.3), start(3))
	require.True(t, ok)
	mean := (0.4 - 0.5 + 0.3) / 3
	variance := (math.Pow(0.4-mean, 2) + math.Pow(-0.5-mean, 2) + math.Pow(0.3-mean, 2)) / 2
	require.InDelta(t, mean/math.Sqrt(variance/3), b.t, 1e-9, "the sample deviation, n - 1")

	_, ok = s.bias(days(0, 0, 0, 0), start(4))
	require.False(t, ok, "no deviation")

	_, ok = s.bias(nil, start(4))
	require.False(t, ok)
}

// No lookahead: the bias of a window on the candle closing at its start is the
// same on the candles up to it and on the whole history, whatever the candles
// after it, the day traded among them.
func TestSeasonality_NoLookahead(t *testing.T) {
	s := newSeasonality(t, func(c *SeasonalityConfig) {
		c.Windows = "10:30/60@Europe/London;15:00/60@Europe/London;09:30/60@America/New_York;" +
			"10:30/30@Europe/London"
		c.Days, c.MinDays, c.MinT = 6, 3, 0
	})
	random := rand.New(rand.NewPCG(1, 2))
	from := time.Date(2024, 3, 20, 0, 0, 0, 0, time.UTC) // over the change of time in London
	df := seasonDF(from, 15*time.Minute, 16*96, func(time.Time) float64 { return random.NormFloat64() / 1000 },
		nil)
	s.Indicators(df)

	upTo := func(df *model.Dataframe, last int) *model.Dataframe {
		return &model.Dataframe{Pair: df.Pair, Time: df.Time[:last+1], Open: df.Open[:last+1],
			Close: df.Close[:last+1], High: df.High[:last+1], Low: df.Low[:last+1], Volume: df.Volume[:last+1],
			Metadata: map[string]model.Series[float64]{}}
	}
	known := 0
	for k := range s.windows {
		key := seasonKey("t", k)
		for i, value := range df.Metadata[key] {
			if isNa(value) {
				continue
			}
			known++
			start := df.Time[i].Add(s.tf)
			require.True(t, s.windows[k].start.last(start, s.windows[k].loc).Equal(start),
				"on the candle closing at its start")

			past := upTo(df, i)
			s.Indicators(past)
			require.Equal(t, value, past.Metadata[key][i], "%s at %s", s.windows[k].name, start)

			// the future, the window of the day included, changed
			changed := upTo(df, len(df.Time)-1)
			changed.Open, changed.Close = append([]float64(nil), df.Open...), append([]float64(nil), df.Close...)
			for j := i + 1; j < len(changed.Close); j++ {
				changed.Open[j], changed.Close[j] = 50, 200
			}
			s.Indicators(changed)
			require.Equal(t, value, changed.Metadata[key][i], "%s at %s", s.windows[k].name, start)
		}
	}
	require.Greater(t, known, 30, "the bias of each window from its 3rd day")
}

// seasonCandles returns the candles of days days of 15 minutes from 00:00 UTC,
// the ones of an ivRunner, each opening at the close before it, from 100: the
// return of ret(day, candle) on the candle of index candle of the day (0 at
// 00:00, 41 at 10:15), 0 flat, and the position of position(day, candle)
// after it.
func seasonCandles(days int, ret func(day, candle int) float64, position func(day, candle int) float64) []ivCandle {
	candles := make([]ivCandle, 0, days*96)
	price := 100.0
	for day := range days {
		for candle := range 96 {
			closePrice := price * (1 + ret(day, candle))
			candles = append(candles, ivCandle{open: price, close: closePrice, position: position(day, candle)})
			price = closePrice
		}
	}
	return candles
}

// the window of the London fix AM, 10:30 to 11:30 UTC in January: its entry on
// the close of the candle of 10:15, of index 41, the candles of 10:30 to 11:15
// in it, the last one, of index 45, closing it
const (
	seasonEntryCandle = 41
	seasonLastCandle  = 45
)

// seasonConfig configures a Seasonality of the window of the London fix AM,
// measured on the last 5 days it traded, 3 at least, a position of 1, then as
// configure does, if set.
func seasonConfig(configure func(*SeasonalityConfig)) func(*SeasonalityConfig) {
	return func(c *SeasonalityConfig) {
		c.Windows, c.Days, c.MinDays = "10:30/60@Europe/London", 5, 3
		c.PositionSize = map[string]float64{"BTCUSDT": 1}
		if configure != nil {
			configure(c)
		}
	}
}

// newSeasonRunner feeds a Seasonality of seasonConfig, its ATR at 1, the
// candles of 15 minutes from Monday, the 15th of January 2024.
func newSeasonRunner(t *testing.T, configure func(*SeasonalityConfig)) (*ivRunner, *Seasonality) {
	t.Helper()
	s := newSeasonality(t, seasonConfig(configure))
	r := newIVRunner(t, s, 0)
	r.step = 15 * time.Minute
	r.fixed["sb_atr"] = 1
	return r, s
}

// seasonInWindow returns the position held over the window on each day from
// the day from, its entry to its last candle excluded: held from the candle of
// its entry until the one closing it.
func seasonInWindow(from, position float64) func(day, candle int) float64 {
	return func(day, candle int) float64 {
		if day >= int(from) && candle >= seasonEntryCandle && candle < seasonLastCandle {
			return position
		}
		return 0
	}
}

// seasonPlanted returns the returns of the window of the London fix AM on
// each day, in %, on its candle of 10:45.
func seasonPlanted(returns ...float64) func(day, candle int) float64 {
	return func(day, candle int) float64 {
		if candle == seasonEntryCandle+2 && day < len(returns) {
			return returns[day] / 100
		}
		return 0
	}
}

// A BUY at the start of the window on a positive bias, a SELL on a negative
// one, once the min days are measured, held to the end of the window; none on
// a bias not significant.
func TestSeasonality_Entries(t *testing.T) {
	for _, tt := range []struct {
		name     string
		returns  []float64 // of the window on each day, in %
		from     float64   // the first day traded, out of range for none
		position float64
	}{
		{name: "a positive bias", returns: []float64{0.4, 0.6, 0.5, 0.2, -0.3}, from: 3, position: 1},
		{name: "a negative bias", returns: []float64{-0.4, -0.6, -0.5, -0.2, 0.3}, from: 3, position: -1},
		{name: "a bias not significant", returns: []float64{0.5, -0.5, 0.4, 0.5, -0.5}, from: 99},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newSeasonRunner(t, nil)
			r.feed(seasonCandles(len(tt.returns), seasonPlanted(tt.returns...),
				seasonInWindow(tt.from, tt.position))...)
		})
	}
}

// The entry: its stop loss and take profit, a safety, 3 and 4.5 ATR away from
// the close; closed at the market at the end of its window, its legs canceled.
func TestSeasonality_ClosedAtTheEndOfItsWindow(t *testing.T) {
	r, s := newSeasonRunner(t, nil)
	candles := seasonCandles(4, seasonPlanted(0.4, 0.6, 0.5, 0.5), seasonInWindow(3, 1))
	r.feed(candles[:3*96+seasonEntryCandle+1]...)
	trade := s.bracket.trades["BTCUSDT"]
	require.NotNil(t, trade)
	entry := r.df.Close.Last(0)
	require.Len(t, trade.legs, 2)
	require.InDelta(t, entry+4.5, trade.legs[0].Price, 1e-9, "the take profit")
	require.InDelta(t, entry-3, *trade.legs[1].Stop, 1e-9, "the stop loss")
	legs := trade.legs
	quote := r.quote()

	r.feed(candles[3*96+seasonEntryCandle+1:]...)
	exit := r.df.Close[3*96+seasonLastCandle]
	require.InDelta(t, quote+exit, r.quote(), 1e-9, "sold on the close of 11:15, the end of the window")
	require.InDelta(t, entry*1.005, exit, 1e-9)
	for _, leg := range legs {
		got, err := r.wallet.Order("BTCUSDT", leg.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, got.Status, leg.Type)
	}
}

// The stop loss of the safety closes the position before the end of its
// window, which isn't entered again.
func TestSeasonality_Stopped(t *testing.T) {
	r, _ := newSeasonRunner(t, nil)
	candles := seasonCandles(4, seasonPlanted(0.4, 0.6, 0.5), seasonInWindow(3, 1))
	crash := 3*96 + seasonEntryCandle + 2 // 10:45
	candles[crash].low = candles[crash].open - 3.5
	for i := crash; i < 3*96+seasonLastCandle; i++ {
		candles[i].position = 0
	}
	r.feed(candles...)
}

// With MaxBars, the position is closed after that many candles, before the end
// of its window.
func TestSeasonality_MaxBars(t *testing.T) {
	r, _ := newSeasonRunner(t, func(c *SeasonalityConfig) { c.MaxBars = 2 })
	r.feed(seasonCandles(4, seasonPlanted(0.4, 0.6, 0.5), func(day, candle int) float64 {
		if day == 3 && candle >= seasonEntryCandle && candle < seasonEntryCandle+2 {
			return 1
		}
		return 0
	})...)
}

// One position at a time: a window starting while the one of another is held
// isn't traded, nor entered after its start; a window starting at the end of
// another is entered on the candle closing the other.
func TestSeasonality_OverlappingWindows(t *testing.T) {
	// the window of 10:30 moving on its candle of 10:45, the other on its
	// candle of 11:45, out of the first one
	both := func(first, second float64) func(day, candle int) float64 {
		return func(day, candle int) float64 {
			switch candle {
			case seasonEntryCandle + 2:
				return first / 100 * float64(1+day%2)
			case seasonEntryCandle + 6:
				return second / 100 * float64(1+day%2)
			}
			return 0
		}
	}

	t.Run("overlapping", func(t *testing.T) {
		r, _ := newSeasonRunner(t, func(c *SeasonalityConfig) {
			c.Windows = "10:30/60@Europe/London;11:00/60@Europe/London"
		})
		r.feed(seasonCandles(4, both(0.3, 0.3), seasonInWindow(3, 1))...)
	})

	t.Run("one after the other", func(t *testing.T) {
		r, _ := newSeasonRunner(t, func(c *SeasonalityConfig) {
			c.Windows = "10:30/60@Europe/London;11:30/60@Europe/London"
		})
		r.feed(seasonCandles(4, both(0.3, -0.3), func(day, candle int) float64 {
			switch {
			case day < 3:
				return 0
			case candle >= seasonEntryCandle && candle < seasonLastCandle:
				return 1 // bought for the first window
			case candle >= seasonLastCandle && candle < seasonLastCandle+4:
				return -1 // sold on its close for the second one, until 12:30
			}
			return 0
		})...)
	})
}

// Of windows starting together, the one of the largest |t| is traded, closed
// at its own end; a |t| under MinT isn't traded.
func TestSeasonality_WindowsStartingTogether(t *testing.T) {
	bias := func(t0, t1 float64) kv {
		return kv{"sb_t0": t0, "sb_days0": 20, "sb_mean0": t0 / 1000, "sb_t1": t1, "sb_days1": 20,
			"sb_mean1": t1 / 1000}
	}
	for _, tt := range []struct {
		name      string
		bias      kv
		positions []float64 // from the entry candle
	}{
		{name: "the shorter one, its |t| the largest", bias: bias(2.5, -3), positions: []float64{-1, -1, 0}},
		{name: "the longer one, its |t| the largest", bias: bias(3, -2.5), positions: []float64{1, 1, 1, 1, 0}},
		{name: "the first on a tie", bias: bias(-3, 3), positions: []float64{-1, -1, -1, -1, 0}},
		{name: "at MinT", bias: bias(math.NaN(), 2), positions: []float64{1, 1, 0}},
		{name: "under MinT", bias: bias(1.99, -1.99), positions: []float64{0, 0, 0, 0, 0}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newSeasonRunner(t, func(c *SeasonalityConfig) {
				c.Windows = "10:30/60@Europe/London;10:30/30@Europe/London"
			})
			candles := make([]ivCandle, seasonEntryCandle+len(tt.positions))
			for i := range candles {
				candles[i] = ivCandle{close: 100}
				if i >= seasonEntryCandle {
					candles[i].position = tt.positions[i-seasonEntryCandle]
				}
			}
			candles[seasonEntryCandle].meta = tt.bias
			r.feed(candles...)
		})
	}
}

// The direction of the bracket keeps the side of the bias it doesn't trade.
func TestSeasonality_Direction(t *testing.T) {
	r, _ := newSeasonRunner(t, func(c *SeasonalityConfig) { c.Direction = RKDirectionBuyOnly })
	r.feed(seasonCandles(4, seasonPlanted(-0.4, -0.6, -0.5), seasonInWindow(99, 0))...)
}

// seasonIdle trades nothing: an agent down.
type seasonIdle struct{}

func (seasonIdle) Timeframe() string                                     { return "15m" }
func (seasonIdle) WarmupPeriod() int                                     { return 1 }
func (seasonIdle) Indicators(*model.Dataframe) []strategy.ChartIndicator { return nil }
func (seasonIdle) OnCandle(*model.Dataframe, service.Broker)             {}

// After a restart, the position found is closed at the end of the window it
// was opened in, told by its opening (the order controller, from the orders it
// stores); at once when that window is over, eg: the agent down until after
// it.
func TestSeasonality_AfterARestart(t *testing.T) {
	// the second window starts while the position of the first one is held,
	// not traded: opened in the first one, the position is closed at its end,
	// 11:30, not at the one of the second, 11:45
	configure := func(c *SeasonalityConfig) { c.Windows = "10:30/60@Europe/London;10:45/60@Europe/London" }
	restarted := func(t *testing.T, r *ivRunner, bot *tradingBot) *Seasonality {
		bot.restart()
		s := newSeasonality(t, seasonConfig(configure))
		r.s, r.broker = s, bot.broker()
		return s
	}
	start := func(t *testing.T) (*ivRunner, *tradingBot, []ivCandle) {
		r, _ := newSeasonRunner(t, configure)
		bot := newTradingBot(t, r.wallet, r.step)
		r.broker, r.settle = bot.broker(), bot.settle
		candles := seasonCandles(4, seasonPlanted(0.4, 0.6, 0.5, 0.5), seasonInWindow(3, 1))
		r.feed(candles[:3*96+seasonEntryCandle+2]...) // entered on the close of 10:15, held over 10:30
		return r, bot, candles[3*96+seasonEntryCandle+2:]
	}

	t.Run("at the end of its window", func(t *testing.T) {
		r, bot, rest := start(t)
		s := restarted(t, r, bot)
		r.feed(rest[:2]...) // 10:45 and 11:00, held
		require.Contains(t, s.held, "BTCUSDT")
		require.Equal(t, "11:30", s.held["BTCUSDT"].end.UTC().Format("15:04"), "not 11:45")
		r.feed(rest[2:]...) // closed on the close of 11:15
	})

	t.Run("at once when its window is over", func(t *testing.T) {
		r, bot, rest := start(t)
		r.s = seasonIdle{}
		down := seasonLastCandle + 3 - (seasonEntryCandle + 2) // until the candle of 11:45, its windows over
		for i := range down {
			rest[i].position = 1
		}
		r.feed(rest[:down]...)

		restarted(t, r, bot)
		r.feed(rest[down:]...) // closed on the close of the candle of 12:00, the first one seen
	})
}

// The chart: the t statistic of each window on the candles closing at its
// start, the thresholds of MinT; drawn on candles of another timeframe, or
// none, without a value rather than wrong.
func TestSeasonality_Indicators(t *testing.T) {
	s := newSeasonality(t, func(c *SeasonalityConfig) { c.Days, c.MinDays = 5, 3 })
	from := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	random := rand.New(rand.NewPCG(3, 4))
	df := seasonDF(from, 15*time.Minute, 8*96, func(time.Time) float64 { return random.NormFloat64() / 1000 }, nil)
	charts := s.Indicators(df)
	require.Len(t, charts, 1)
	require.Equal(t, "Seasonality", charts[0].GroupName)
	require.False(t, charts[0].Overlay)
	require.Equal(t, 1, charts[0].Warmup, "drawn where known, not after the whole warmup")
	require.Len(t, charts[0].Metrics, 3+2)
	known := 0
	for k := range s.windows {
		require.Equal(t, strategy.MetricStyle(strategy.StyleScatter), charts[0].Metrics[k].Style)
		for _, key := range []string{seasonKey("t", k), seasonKey("days", k), seasonKey("mean", k)} {
			require.Len(t, df.Metadata[key], len(df.Time), key)
		}
		for _, value := range df.Metadata[seasonKey("t", k)] {
			if !isNa(value) {
				known++
			}
		}
	}
	require.Equal(t, 3*5, known, "each window from its 4th day, on 8 days")
	require.Len(t, df.Metadata["sb_atr"], len(df.Time))

	hourly := seasonDF(from, time.Hour, 8*24, func(time.Time) float64 { return 0.001 }, nil)
	s.Indicators(hourly)
	for k := range s.windows {
		for _, value := range hourly.Metadata[seasonKey("t", k)] {
			require.True(t, isNa(value))
		}
	}

	empty := &model.Dataframe{Metadata: map[string]model.Series[float64]{}}
	require.NotPanics(t, func() { s.Indicators(empty) })
	require.Empty(t, empty.Metadata[seasonKey("t", 0)])
}

// Over the candles of the test data, on windows of whole hours of 1h, the
// strategy trades with the statistic of its defaults: each trade a market
// entry, then a single exit.
func TestSeasonality_Backtest(t *testing.T) {
	checkBacktest(t, newSeasonality(t, func(c *SeasonalityConfig) {
		c.Timeframe = "1h"
		c.Windows = "10:00/60@Europe/London;15:00/60@Europe/London;09:00/60@America/New_York"
	}))
}
