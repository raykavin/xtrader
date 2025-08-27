package llm

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

func TestSession(t *testing.T) {
	trader, err := New(DefaultConfig(), &fakeProvider{t: t})
	require.NoError(t, err)

	// a Monday of September: London on summer time (08:00 there is 07:00 UTC),
	// New York too (08:00 there is 12:00 UTC)
	monday := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	df := &model.Dataframe{Pair: testPair, Metadata: map[string]model.Series[float64]{}}
	for i := 0; i < 48; i++ { // the 15m candles of the day
		at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * 15 * time.Minute)
		if !at.Before(monday) {
			break
		}
		df.Time = append(df.Time, at)
		df.Close = append(df.Close, 100)
		df.High = append(df.High, 100+float64(i%7))
		df.Low = append(df.Low, 99-float64(i%5))
	}
	session := trader.session(df, monday)
	require.Equal(t, "Monday", session.Weekday)
	require.Equal(t, 10, session.HourUTC)
	require.Equal(t, []string{"London"}, session.Open)
	require.Equal(t, "New York in 2 h", session.NextOpen)
	require.False(t, session.Weekend)
	require.Equal(t, number(106), session.AsiaHigh, "00:00 to 07:00 UTC")
	require.Equal(t, number(95), session.AsiaLow)

	// a Saturday: every session closed, the next one Sydney, on Monday there
	saturday := time.Date(2026, 9, 26, 15, 30, 0, 0, time.UTC)
	session = trader.session(&model.Dataframe{Pair: testPair}, saturday)
	require.True(t, session.Weekend)
	require.Empty(t, session.Open)
	require.Equal(t, "Sydney in 30 h 30 min", session.NextOpen, "08:00 of Monday in Sydney, 22:00 UTC on Sunday")
	require.True(t, math.IsNaN(float64(session.AsiaHigh)), "no candle of the day")

	// winter: New York opens at 13:00 UTC
	winter := time.Date(2026, 12, 7, 12, 0, 0, 0, time.UTC)
	require.Equal(t, "New York in 1 h", trader.session(&model.Dataframe{}, winter).NextOpen)
}

func TestDescribeWait(t *testing.T) {
	require.Equal(t, "45 min", describeWait(45*time.Minute))
	require.Equal(t, "2 h", describeWait(2*time.Hour))
	require.Equal(t, "2 h 15 min", describeWait(135*time.Minute))
}
