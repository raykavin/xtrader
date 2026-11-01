package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSeasonWindows(t *testing.T) {
	windows, err := parseSeasonWindows("10:30/60@Europe/London; 09:30/90@America/New_York")
	require.NoError(t, err)
	require.Len(t, windows, 2)
	assert.Equal(t, 10*time.Hour+30*time.Minute, windows[0].start)
	assert.Equal(t, time.Hour, windows[0].hold)
	assert.Equal(t, "America/New_York", windows[1].loc.String())
	assert.Equal(t, 90*time.Minute, windows[1].hold)

	for _, invalid := range []string{"", "10:30@UTC", "10:30/60", "25:00/60@UTC", "10:30/0@UTC", "10:30/60@Mars/Base"} {
		_, err := parseSeasonWindows(invalid)
		assert.Error(t, err, invalid)
	}
}

// The return of a window is taken on the days it traded whole, from the open
// of its first candle to the close of its last one, in its own zone: London in
// summer is UTC+1.
func TestWindowReturns(t *testing.T) {
	windows, err := parseSeasonWindows("10:30/60@Europe/London")
	require.NoError(t, err)
	w := windows[0]
	var candles []seasonCandle
	day := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC) // a Monday, BST
	for d := 0; d < 3; d++ {
		for m := 0; m < 24*60; m += 15 {
			at := day.AddDate(0, 0, d).Add(time.Duration(m) * time.Minute)
			price := 100.0
			// 09:30 to 10:30 UTC is 10:30 to 11:30 in London: +1 on day 0, -2 on day 1
			if at.Hour() == 9 && at.Minute() >= 30 || at.Hour() == 10 && at.Minute() < 30 {
				price += []float64{0.25, -0.5, 0}[d] * float64((at.Minute()+30)%60/15+1)
			}
			candles = append(candles, seasonCandle{at: at, open: 100, high: price + 1, low: price - 1, close: price})
		}
	}
	// the third day misses the last candle of the window: not whole
	cut := time.Date(2026, 7, 8, 10, 15, 0, 0, time.UTC)
	for i, c := range candles {
		if c.at.Equal(cut) {
			candles = append(candles[:i], candles[i+1:]...)
			break
		}
	}
	returns := windowReturns(candles, w)
	require.Len(t, returns, 2)
	assert.InDelta(t, 1.0, returns[0], 1e-9)
	assert.InDelta(t, -2.0, returns[1], 1e-9)
}

func TestSeasonOf(t *testing.T) {
	up := seasonOf("w", []float64{0.1, 0.12, 0.09, 0.11, 0.1}, 2, 3)
	assert.Equal(t, "alta", up.Bias)
	assert.Greater(t, up.T, 2.0)
	assert.Equal(t, 1.0, up.Up)
	assert.Equal(t, "poucos dias", seasonOf("w", []float64{0.1, 0.12}, 2, 3).Bias)
	assert.Equal(t, "-", seasonOf("w", []float64{0.1, -0.1, 0.1, -0.1}, 2, 3).Bias)
	assert.Equal(t, "baixa", seasonOf("w", []float64{-0.1, -0.12, -0.09, -0.11}, 2, 3).Bias)
}

func TestReadSeasonCandles(t *testing.T) {
	file := filepath.Join(t.TempDir(), "x.csv")
	var b strings.Builder
	b.WriteString("time,open,close,low,high,volume,spread\n")
	start := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	for i := 2; i >= 0; i-- { // out of order: sorted when read
		fmt.Fprintf(&b, "%d,1,2,0.5,3,10,20\n", start.Add(time.Duration(i)*time.Hour).Unix())
	}
	require.NoError(t, os.WriteFile(file, []byte(b.String()), 0o600))
	candles, err := readSeasonCandles(file, time.Time{}, start.Add(2*time.Hour))
	require.NoError(t, err)
	require.Len(t, candles, 2)
	assert.True(t, candles[0].at.Before(candles[1].at))
	assert.Equal(t, 3.0, candles[0].high)
}
