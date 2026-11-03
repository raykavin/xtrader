package agentapi

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

func TestTradingWindows(t *testing.T) {
	zone, err := time.LoadLocation("America/Belem")
	require.NoError(t, err)
	at := func(clock string) time.Time {
		parsed, err := time.ParseInLocation("2006-01-02 15:04", "2026-11-03 "+clock, zone)
		require.NoError(t, err)
		return parsed
	}
	windows := []ui.TradingWindow{{Start: "12:00", End: "14:00"}, {Start: "08:00", End: "10:00"}}
	for _, c := range []struct {
		at             string
		open, upcoming int
		next           time.Time
	}{
		{"07:59", -1, 1, at("08:00")},
		{"08:00", 1, -1, at("10:00")},
		{"09:59", 1, -1, at("10:00")},
		{"10:00", -1, 0, at("12:00")}, // the end itself outside
		{"13:30", 0, -1, at("14:00")},
		{"14:00", -1, 1, at("08:00").AddDate(0, 0, 1)}, // the first one of tomorrow
	} {
		open, upcoming, next := TradingWindows(windows, at(c.at), zone)
		assert.Equal(t, c.open, open, c.at)
		assert.Equal(t, c.upcoming, upcoming, c.at)
		assert.True(t, c.next.Equal(next), "%s: next %s, want %s", c.at, next, c.next)
	}
	open, upcoming, _ := TradingWindows(nil, at("09:00"), zone)
	assert.Equal(t, []int{-1, -1}, []int{open, upcoming}, "no window: never open")
}

func TestWindowEnd(t *testing.T) {
	zone, err := time.LoadLocation("America/Belem")
	require.NoError(t, err)
	opened := time.Date(2026, 11, 3, 8, 5, 0, 0, zone)
	assert.Equal(t, time.Date(2026, 11, 3, 10, 0, 0, 0, zone), WindowEnd(ui.TradingWindow{Start: "08:00", End: "10:00"},
		opened, zone))
	night := time.Date(2026, 11, 3, 22, 30, 0, 0, zone)
	assert.Equal(t, time.Date(2026, 11, 4, 2, 0, 0, 0, zone), WindowEnd(ui.TradingWindow{Start: "22:00", End: "02:00"},
		night, zone), "a window of before crossing midnight ends the next day")
}

func TestWindowsProblem(t *testing.T) {
	w := func(start, end string) ui.TradingWindow { return ui.TradingWindow{Start: start, End: end} }
	assert.Empty(t, WindowsProblem(nil))
	assert.Empty(t, WindowsProblem([]ui.TradingWindow{w("12:00", "14:00"), w("08:00", "10:00")}), "in any order")
	assert.Contains(t, WindowsProblem([]ui.TradingWindow{w("8:00", "10:00")}), "o início da janela 1")
	assert.Contains(t, WindowsProblem([]ui.TradingWindow{w("08:00", "10:00"), w("12:00", "24:00")}),
		"o fim da janela 2")
	assert.Contains(t, WindowsProblem([]ui.TradingWindow{w("22:00", "02:00")}), "não atravessa a meia-noite")
	assert.Contains(t, WindowsProblem([]ui.TradingWindow{w("10:00", "10:00")}), "deve começar antes de terminar")
	assert.Contains(t, WindowsProblem([]ui.TradingWindow{w("08:00", "10:00"), w("09:00", "11:00")}),
		"as janelas 08:00–10:00 e 09:00–11:00 se sobrepõem ou se encostam")
	assert.Contains(t, WindowsProblem([]ui.TradingWindow{w("10:00", "12:00"), w("08:00", "10:00")}),
		"se encostam", "touching: one window then")
	many := make([]ui.TradingWindow, MaxTradingWindows+1)
	assert.Contains(t, WindowsProblem(many), "no máximo 24 janelas")
}

func TestHoursWindows(t *testing.T) {
	windows := []ui.TradingWindow{{Start: "08:00", End: "10:00", Flatten: true}}
	assert.Equal(t, windows, HoursWindows(windows, "07:00", "12:00", false, false), "the windows first")
	assert.Equal(t, []ui.TradingWindow{{Start: "07:00", End: "12:00", CancelPending: true}},
		HoursWindows(nil, "07:00", "12:00", false, true), "the single window of before")
	assert.Nil(t, HoursWindows(nil, "", "", true, true))
}
