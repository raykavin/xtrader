package strategies

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// London and New York open from 08:00 until 17:00 in the time of each city, on
// its weekdays, their daylight saving times followed, as the context of the
// LLM agent tells it.
func TestMajorSessionOpen(t *testing.T) {
	at := func(value string) time.Time {
		v, err := time.Parse(time.RFC3339, value)
		require.NoError(t, err)
		return v
	}
	for value, open := range map[string]bool{
		"2024-01-15T07:59:00Z": false, // a Monday of winter, London on UTC
		"2024-01-15T08:00:00Z": true,
		"2024-01-15T16:59:00Z": true,
		"2024-01-15T17:00:00Z": true, // London closed, 12:00 in New York
		"2024-01-15T21:59:00Z": true,
		"2024-01-15T22:00:00Z": false, // 17:00 in New York
		"2024-01-20T10:00:00Z": false, // a Saturday
		"2024-07-15T07:00:00Z": true,  // 08:00 of the summer time of London
		"2024-07-15T06:59:00Z": false,
	} {
		require.Equal(t, open, majorSessionOpen(at(value)), value)
	}
}

// The last swing low is the low of a candle under the ones on each side of it,
// pivot of them, confirmed before the last candle; the mirror for a high.
func TestLastSwing(t *testing.T) {
	df := &model.Dataframe{
		Low:  []float64{10, 9, 8, 9, 10, 11, 10, 11},
		High: []float64{20, 21, 22, 21, 20, 19, 20, 19},
	}
	df.Close = make([]float64, len(df.Low))
	require.Equal(t, 8.0, lastSwing(df, model.SideTypeBuy, 2))
	require.Equal(t, 22.0, lastSwing(df, model.SideTypeSell, 2))
	require.True(t, isNa(lastSwing(df, model.SideTypeBuy, 4)), "none confirmed")
}

// The R multiple of a trade is its profit at a price in units of its initial
// risk, unknown without one; the time stop ends a trade held long enough
// without reaching an R.
func TestTimeR(t *testing.T) {
	df := &model.Dataframe{Close: []float64{101}}
	trade := &scalpTrade{side: model.SideTypeBuy, entry: 100, risk: 2, bars: 8}
	r, ok := trade.rMultiple(101)
	require.True(t, ok)
	require.InDelta(t, 0.5, r, 1e-9)
	sell := &scalpTrade{side: model.SideTypeSell, entry: 100, risk: 2}
	r, _ = sell.rMultiple(101)
	require.InDelta(t, -0.5, r, 1e-9)
	_, ok = (&scalpTrade{side: model.SideTypeBuy}).rMultiple(101)
	require.False(t, ok, "the risk of a position adopted after a restart")

	require.Empty(t, timeR(df, trade, 8, 0.5, false), "0.5 R reached")
	require.Equal(t, "TIME", timeR(df, trade, 8, 0.6, false))
	require.Empty(t, timeR(df, trade, 9, 0.6, false), "not held long enough")
	trade.bestR = 0.7
	require.Empty(t, timeR(df, trade, 8, 0.6, true), "its best R reached it")
	require.Equal(t, "TIME", timeR(df, &scalpTrade{side: model.SideTypeBuy, bars: 8}, 8, 0.6, true),
		"its risk unknown: the time only")
	require.Empty(t, timeR(df, nil, 8, 0.6, true))
}
