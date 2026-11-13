//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/ui"
)

// fakeNewsCalendar is a calendarSource of the tests: its events, or its error,
// and what it was asked.
type fakeNewsCalendar struct {
	events []llm.Event
	err    error
	reads  int
	config llm.NewsConfig
}

func (f *fakeNewsCalendar) Set(config llm.NewsConfig) (bool, bool) {
	changed := f.config != config
	f.config = config
	return false, changed
}

func (f *fakeNewsCalendar) Events(context.Context, []string, time.Time, time.Time) ([]llm.Event, error) {
	f.reads++
	return f.events, f.err
}

// The releases of the period as the card shows them: the ones timed within
// it, the ones of a day overlapping it, oldest first, the values the calendar
// lacks absent; the ones of the importance of the window blocking the entries
// while the news block them; a read kept for a minute, read again past it or
// as the settings change; why it wasn't read told, the token never in it.
func TestNewsCalendar_Read(t *testing.T) {
	day := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	from, to := day.Add(6*time.Hour), day.Add(30*time.Hour) // from 06:00 to 06:00 of the next day
	now := day.Add(7 * time.Hour)
	source := &fakeNewsCalendar{events: []llm.Event{
		{Time: day.Add(5 * time.Hour), Currency: "USD", Name: "before", Importance: llm.ImportanceHigh},
		{Time: day.Add(13*time.Hour + 30*time.Minute), Currency: "USD", Name: "Nonfarm Payrolls",
			Code: "nonfarm-payrolls", Importance: llm.ImportanceHigh, Unit: "K", Actual: math.NaN(), Forecast: 150,
			Previous: 120},
		{Time: day.Add(9 * time.Hour), Currency: "EUR", Name: "ZEW", Importance: llm.ImportanceMedium,
			Actual: 1.5, Forecast: math.NaN(), Previous: math.NaN()},
		{Time: day, TimeMode: llm.TimeAllDay, Currency: "JPY", Name: "Holiday", Importance: llm.ImportanceNone,
			Actual: math.NaN(), Forecast: math.NaN(), Previous: math.NaN()},
		{Time: to, Currency: "GBP", Name: "at the end", Importance: llm.ImportanceLow},
	}}
	n := &newsCalendar{source: source, now: func() time.Time { return now }, cache: map[string]cachedCalendar{}}
	config := llm.NewsConfig{Enabled: true, URL: "http://mt5:22347/mcp", Token: "the-token",
		Before: 10 * time.Minute, After: 15 * time.Minute, Importance: llm.ImportanceHigh}
	ctx := context.Background()

	got := n.read(ctx, from, to, []string{"USD", "EUR"}, config)
	assert.Equal(t, "ok", got.Status)
	assert.Equal(t, &ui.NewsWindow{Before: 10, After: 15, Importance: "high"}, got.Window)
	assert.Equal(t, now.Unix(), got.At)
	require.Len(t, got.Events, 3)
	assert.Equal(t, ui.NewsEvent{At: day.Unix(), Currency: "JPY", Name: "Holiday", Importance: "none"},
		got.Events[0], "a day overlapping the period")
	assert.Equal(t, "ZEW", got.Events[1].Name)
	assert.InDelta(t, 1.5, *got.Events[1].Actual, 1e-9)
	assert.Nil(t, got.Events[1].Forecast)
	assert.False(t, got.Events[1].Blocks, "medium, under the importance of the window")
	payrolls := got.Events[2]
	assert.True(t, payrolls.Timed)
	assert.True(t, payrolls.Blocks)
	assert.Nil(t, payrolls.Actual, "not published yet")
	assert.InDelta(t, 150, *payrolls.Forecast, 1e-9)
	assert.Equal(t, "K", payrolls.Unit)

	n.read(ctx, from, to, []string{"USD", "EUR"}, config)
	assert.Equal(t, 1, source.reads, "kept for a minute")
	now = now.Add(newsCalendarTTL)
	n.read(ctx, from, to, []string{"USD", "EUR"}, config)
	assert.Equal(t, 2, source.reads, "read again past it")
	off := config
	off.Before, off.After = 0, 0
	got = n.read(ctx, from, to, []string{"USD", "EUR"}, off)
	assert.Equal(t, 3, source.reads, "the settings changed")
	assert.Nil(t, got.Window, "no window, no block")
	for _, e := range got.Events {
		assert.False(t, e.Blocks, e.Name)
	}

	source.err = fmt.Errorf("llm: refused with the token the-token: %w", llm.ErrMCPUnauthorized)
	got = n.read(ctx, from, to, []string{"GBP"}, off)
	assert.Equal(t, llm.CalendarUnauthorized, got.Status)
	assert.NotContains(t, got.Detail, "the-token")
	assert.Empty(t, got.Events)
	for err, status := range map[error]string{llm.ErrNewsDisabled: llm.CalendarDisabled,
		llm.ErrNewsNoServer: llm.CalendarMissingServer, llm.ErrNewsNoToken: llm.CalendarMissingToken,
		errors.New("down"): llm.CalendarUnavailable} {
		assert.Equal(t, status, calendarStatusOf(err), err.Error())
	}
}

// The calendar through the API: the period and the currencies checked, 400
// for ones refused; with the news off, their status, nothing asked to a
// server.
func TestNewsCalendar_ThroughTheAPI(t *testing.T) {
	c := newManaged(t)
	call, _ := settingsSession(t, c)
	day := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC).Unix()
	path := func(from, to int64, currencies string) string {
		return fmt.Sprintf("/api/news/calendar?from=%d&to=%d&currencies=%s", from, to, currencies)
	}

	for name, refused := range map[string]string{
		"no period":        "/api/news/calendar?currencies=USD",
		"to before from":   path(day, day-1, "USD"),
		"too long":         path(day, day+int64(ui.MaxNewsCalendarDays+1)*86400, "USD"),
		"no currency":      path(day, day+86400, ""),
		"a currency wrong": path(day, day+86400, "USD,EURO"),
	} {
		resp, body := call(http.MethodGet, refused, nil)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "%s: %s", name, body)
	}

	resp, body := call(http.MethodGet, path(day, day+86400, "usd,%20EUR,USD"), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var got ui.NewsCalendarResponse
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, []string{"USD", "EUR"}, got.Currencies, "upper-cased, each once")
	assert.Equal(t, llm.CalendarDisabled, got.Status, "the news off by the seeds")
	assert.Empty(t, got.Events)
	assert.Nil(t, got.Window)
}
