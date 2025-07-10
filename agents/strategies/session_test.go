package strategies

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

func TestParseDayTime(t *testing.T) {
	d, err := parseDayTime("09:30")
	require.NoError(t, err)
	require.Equal(t, dayTime(9*time.Hour+30*time.Minute), d)

	d, err = parseDayTime("00:00")
	require.NoError(t, err)
	require.Zero(t, d)

	for _, value := range []string{"", "9:30am", "24:00", "12:60", "12"} {
		_, err := parseDayTime(value)
		require.ErrorContains(t, err, "HH:MM", value)
	}
}

func TestSession_Bounds(t *testing.T) {
	day := func(d, hour, minute int) time.Time { return time.Date(2024, 1, d, hour, minute, 0, 0, time.UTC) }

	s, err := newSession("22:00", "02:00", "UTC")
	require.NoError(t, err)
	require.Equal(t, 4*time.Hour, s.length())

	// over midnight: the session started the day before
	start, end := s.bounds(day(15, 1, 0))
	require.Equal(t, day(14, 22, 0), start)
	require.Equal(t, day(15, 2, 0), end)

	// at its start, and after its end, the last one started
	start, _ = s.bounds(day(15, 22, 0))
	require.Equal(t, day(15, 22, 0), start)
	start, end = s.bounds(day(15, 10, 0))
	require.Equal(t, day(14, 22, 0), start)
	require.True(t, end.Before(day(15, 10, 0)))

	// a whole day
	s, err = newSession("00:00", "00:00", "UTC")
	require.NoError(t, err)
	require.Equal(t, rkDay, s.length())
}

func TestSession_DaylightSavingTime(t *testing.T) {
	// 9:30 in New York is 14:30 UTC in winter and 13:30 UTC in summer
	s, err := newSession("09:30", "12:00", "America/New_York")
	require.NoError(t, err)

	start, end := s.bounds(time.Date(2024, 1, 15, 15, 0, 0, 0, time.UTC))
	require.Equal(t, time.Date(2024, 1, 15, 14, 30, 0, 0, time.UTC), start.UTC())
	require.Equal(t, time.Date(2024, 1, 15, 17, 0, 0, 0, time.UTC), end.UTC())

	start, _ = s.bounds(time.Date(2024, 7, 15, 15, 0, 0, 0, time.UTC))
	require.Equal(t, time.Date(2024, 7, 15, 13, 30, 0, 0, time.UTC), start.UTC())
}

func TestRangeBreakout_Sessions(t *testing.T) {
	at := func(hour, minute int) time.Time { return time.Date(2024, 1, 15, hour, minute, 0, 0, time.UTC) }
	rng, err := newSession("10:00", "10:15", "UTC")
	require.NoError(t, err)
	trade, err := newSession("10:15", "11:00", "UTC")
	require.NoError(t, err)
	layout := rangeBreakout{rng: rng, trade: trade}

	rangeStart, rangeEnd, tradeStart, tradeEnd := layout.sessions(at(10, 30))
	require.Equal(t, []time.Time{at(10, 0), at(10, 15), at(10, 15), at(11, 0)},
		[]time.Time{rangeStart, rangeEnd, tradeStart, tradeEnd})

	// the candles traded close before the end of the session
	require.True(t, layout.trading(at(10, 15), 5*time.Minute))
	require.True(t, layout.trading(at(10, 50), 5*time.Minute))
	require.False(t, layout.trading(at(10, 55), 5*time.Minute), "closing at the end of the session")
	require.False(t, layout.trading(at(11, 0), 5*time.Minute))
	require.False(t, layout.trading(at(10, 5), 5*time.Minute), "in the range")

	// from the start of the range to the end of the trade session, and an hour
	require.Equal(t, 2*time.Hour, layout.span())
}

func TestAnchoredVWAP(t *testing.T) {
	loc := time.UTC
	df := &model.Dataframe{
		Time: []time.Time{
			time.Date(2024, 1, 14, 23, 0, 0, 0, loc),
			time.Date(2024, 1, 15, 0, 0, 0, 0, loc),
			time.Date(2024, 1, 15, 1, 0, 0, 0, loc),
			time.Date(2024, 1, 15, 2, 0, 0, 0, loc),
		},
		High:   model.Series[float64]{12, 12, 15, 9},
		Low:    model.Series[float64]{9, 9, 12, 6},
		Close:  model.Series[float64]{9, 9, 12, 6}, // typical prices 10, 10, 13 and 7
		Volume: model.Series[float64]{5, 1, 2, 3},
	}

	// the day before reaches back out of the window, then the volume weights
	// the typical prices from midnight
	requireSeries(t, []float64{na, 10, (10 + 13*2) / 3.0, (10 + 13*2 + 7*3) / 6.0},
		anchoredVWAP(df, 0, loc), 1e-12)

	// without volume, the candles weigh the same
	df.Volume = model.Series[float64]{0, 0, 0, 0}
	requireSeries(t, []float64{na, 10, 11.5, 10}, anchoredVWAP(df, 0, loc), 1e-12)

	// the anchor starts it over
	df.Volume = model.Series[float64]{1, 1, 1, 1}
	requireSeries(t, []float64{na, na, 13, 10}, anchoredVWAP(df, dayTime(time.Hour), loc), 1e-12)
}

func TestTimeframeCandles(t *testing.T) {
	candles, tf, err := timeframeCandles("5m", time.Hour+time.Minute)
	require.NoError(t, err)
	require.Equal(t, 13, candles, "rounded up")
	require.Equal(t, 5*time.Minute, tf)

	_, _, err = timeframeCandles("five", time.Hour)
	require.ErrorContains(t, err, "invalid timeframe")
}
