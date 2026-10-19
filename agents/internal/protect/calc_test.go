package protect

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// The levels round toward the protection of the position, or away from the
// market, on the grid of its tick, never short of what they keep.
func TestRounding(t *testing.T) {
	buy, sell := model.SideTypeBuy, model.SideTypeSell
	require.Equal(t, 100.11, roundProtective(buy, 100.101, 0.01), "a long: up")
	require.Equal(t, 100.1, roundProtective(sell, 100.109, 0.01), "a short: down")
	require.Equal(t, 100.1, roundProtective(buy, 100.1, 0.01), "on the grid already")
	require.Equal(t, 100.25, roundProtective(buy, 100.01, 0.25), "a coarse tick")
	require.Equal(t, 100.0, roundAway(buy, 100.24, 0.25), "a long: down, away from the price above")
	require.Equal(t, 100.25, roundAway(sell, 100.01, 0.25), "a short: up")
	require.Equal(t, 1.2345, roundAway(buy, 1.2345, 0), "no tick: as it is")
}

// The breakeven is the entry beyond by the margin and the costs; the trailing
// stop the distance behind the price; on the losing side of the price only.
func TestLevels(t *testing.T) {
	buy, sell := model.SideTypeBuy, model.SideTypeSell
	require.Equal(t, 100.16, breakevenLevel(buy, 100, 0.1, 1.5, 0.01, 0.01))
	require.Equal(t, 99.84, breakevenLevel(sell, 100, 0.1, 1.5, 0.01, 0.01))
	require.Equal(t, 101.25, trailingLevel(buy, 102, 0.5, 1.5, 0.01))
	require.Equal(t, 98.75, trailingLevel(sell, 98, 0.5, 1.5, 0.01))

	require.True(t, tighter(buy, 99, 98, 0.01))
	require.False(t, tighter(buy, 98.005, 98, 0.01), "less than a tick")
	require.True(t, tighter(sell, 101, 102, 0.01))
	require.False(t, tighter(sell, 103, 102, 0.01))
	require.True(t, tighter(buy, 99, 0, 0.01), "any, without a stop loss")

	require.True(t, onLosingSide(buy, 99, 100, 0))
	require.False(t, onLosingSide(buy, 100, 100, 0), "at the price")
	require.False(t, onLosingSide(buy, 99.5, 100, 1), "within the stops level")
	require.True(t, onLosingSide(sell, 101, 100, 0))

	require.Equal(t, 2.0, favorable(buy, 100, 103, 1.5))
	require.Equal(t, 0.0, favorable(sell, 100, 103, 1.5), "never against it")
}

// A long exits at the bid, the close; a short at the ask.
func TestExitPrice(t *testing.T) {
	candle := model.Candle{Close: 100, Low: 99, High: 101, Metadata: map[string]float64{"ask": 100.2, "spread": 0.2}}
	require.Equal(t, 100.0, exitPrice(model.SideTypeBuy, candle))
	require.Equal(t, 100.2, exitPrice(model.SideTypeSell, candle))
	require.Equal(t, 99.2, bestExit(model.SideTypeSell, candle), "its low plus the spread")
	require.Equal(t, 101.0, bestExit(model.SideTypeBuy, candle))
	require.Equal(t, 100.0, exitPrice(model.SideTypeSell, model.Candle{Close: 100}), "without a spread")
}
