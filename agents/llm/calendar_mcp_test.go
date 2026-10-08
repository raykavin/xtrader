package llm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMCPCalendar_TruncatedReadInHalves(t *testing.T) {
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	from, to := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC)

	whole := newFakeMCP(t)
	want, err := newTestCalendar(t, whole, now).Events(context.Background(), []string{"USD"}, from, to)
	require.NoError(t, err)
	require.Len(t, want, 3)

	f := newFakeMCP(t)
	f.page = 2 // 5 releases on the 25th
	events, err := newTestCalendar(t, f, now).Events(context.Background(), []string{"USD"}, from, to)
	require.NoError(t, err)
	require.Equal(t, releases(want), releases(events), "every release, none left out by the truncation")
	require.Equal(t, []string{"00:00-00:00", "00:00-12:00", "12:00-00:00", "12:00-18:00", "12:00-15:00",
		"12:00-13:30", "13:30-15:00", "15:00-18:00", "18:00-00:00"}, f.ranges, "the truncated ranges in halves")
}

// releases returns the names and times of events, comparable (NaN isn't).
func releases(events []Event) []string {
	var named []string
	for _, event := range events {
		named = append(named, event.Name+" "+event.Time.Format(time.RFC3339))
	}
	return named
}

func TestMCPCalendar_TruncatedFails(t *testing.T) {
	f := newFakeMCP(t)
	f.page = 1
	f.values["2026-09-25"] = append(f.values["2026-09-25"],
		mcpValue{EventID: "840020014", Time: "2026-09-25T12:30:00"}) // two releases at 12:30
	calendar := newTestCalendar(t, f, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC))

	_, err := calendar.Events(context.Background(), []string{"USD"},
		time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC), time.Date(2026, 9, 25, 13, 0, 0, 0, time.UTC))
	require.ErrorContains(t, err, "truncated", "unavailable rather than missing a release")
	require.Empty(t, calendar.days, "nothing cached")
}

func TestMCPCalendar_InclusiveEndNotDuplicated(t *testing.T) {
	f := newFakeMCP(t)
	f.inclusive = true
	f.values["2026-09-26"] = append(f.values["2026-09-26"],
		mcpValue{EventID: "840020013", Time: "2026-09-26T00:00:00", Forecast: ptr(0.1)})
	calendar := newTestCalendar(t, f, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC))

	events, err := calendar.Events(context.Background(), []string{"USD"},
		time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC), time.Date(2026, 9, 26, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, events, 2, "the release at midnight once, the one of 00:30 too")
	require.Equal(t, time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC), events[0].Time)
	require.Equal(t, time.Date(2026, 9, 26, 0, 30, 0, 0, time.UTC), events[1].Time)
}

func TestMCPCalendar_DayOverExpires(t *testing.T) {
	f := newFakeMCP(t)
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	calendar := newTestCalendar(t, f, now)
	read := func(day int) {
		t.Helper()
		_, err := calendar.Events(context.Background(), []string{"USD"},
			time.Date(2026, 9, day, 1, 0, 0, 0, time.UTC), time.Date(2026, 9, day, 2, 0, 0, 0, time.UTC))
		require.NoError(t, err)
	}

	read(25)
	calendar.now = func() time.Time { return now.Add(mcpDayOverTTL - time.Second) }
	read(25)
	require.Equal(t, 1, f.asked("economic_calendar_list_values:2026-09-25"), "a day over cached")

	calendar.now = func() time.Time { return now.Add(mcpDayOverTTL) }
	read(25)
	require.Equal(t, 2, f.asked("economic_calendar_list_values:2026-09-25"), "read again a day later")

	calendar.now = func() time.Time { return now.Add(2 * mcpDayOverTTL) }
	read(24)
	require.Len(t, calendar.days, 1, "the day read a day ago left the cache")
	require.Contains(t, calendar.days, "2026-09-24")
}

func TestMCPCalendar_TimeMode(t *testing.T) {
	f := newFakeMCP(t)
	f.moreUSD = []string{
		`{"id": "840030001", "importance": "High importance", "unit": "None", "name": "Feriado", "event_code": "holiday", "time_mode": "Event takes all day"}`,
		`{"id": "840030002", "importance": "High importance", "unit": "None", "name": "Reunião", "event_code": "meeting", "time_mode": "Source publishes a day of an event rather than its exact time. The time is specified upon the occurrence of the event."}`,
		`{"id": "840030003", "importance": "High importance", "unit": "None", "name": "Discurso", "event_code": "speech", "time_mode": "Source publishes no time for an event"}`,
	}
	f.values["2026-09-26"] = append(f.values["2026-09-26"],
		mcpValue{EventID: "840030001", Time: "2026-09-26T00:00:00"},
		mcpValue{EventID: "840030002", Time: "2026-09-26T00:00:00"},
		mcpValue{EventID: "840030003", Time: "2026-09-26T00:00:00", Actual: ptr(1)})
	calendar := newTestCalendar(t, f, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC))
	read := func(from, to time.Time) []Event {
		t.Helper()
		events, err := calendar.Events(context.Background(), []string{"USD"}, from, to)
		require.NoError(t, err)
		return events
	}

	// at 23:50, the releases of a day at 00:00 block nothing
	at := time.Date(2026, 9, 25, 23, 50, 0, 0, time.UTC)
	events := read(at.Add(-15*time.Minute), at.Add(15*time.Minute))
	require.Len(t, events, 3)
	for _, event := range events {
		require.False(t, event.Timed(), event.Name)
	}
	_, blocked := newsBlock(events, at, 15*time.Minute, 15*time.Minute, ImportanceHigh)
	require.False(t, blocked, "no time: not taken for a release at midnight")
	_, blocked = newsBlock(events, at.Add(20*time.Minute), 15*time.Minute, 15*time.Minute, ImportanceHigh)
	require.False(t, blocked)

	// in the middle of their day, still in the context, with their mode, the
	// published value hidden until the day is over
	at = time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	events = read(at.Add(-time.Hour), at.Add(time.Hour))
	require.Len(t, events, 3)
	cc := calendarContext(events, at, ImportanceLow, 0)
	data, err := json.Marshal(cc.Events)
	require.NoError(t, err)
	require.JSONEq(t, `[
		{"time":"2026-09-26T00:00:00Z","minutes_from_now":-600,"time_mode":"all_day","currency":"USD","event":"Feriado","importance":"high","actual":null,"forecast":null,"previous":null},
		{"time":"2026-09-26T00:00:00Z","minutes_from_now":-600,"time_mode":"tentative","currency":"USD","event":"Reunião","importance":"high","actual":null,"forecast":null,"previous":null},
		{"time":"2026-09-26T00:00:00Z","minutes_from_now":-600,"time_mode":"no_time","currency":"USD","event":"Discurso","importance":"high","actual":null,"forecast":null,"previous":null}
	]`, string(data))
	cc = calendarContext(events, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC), ImportanceLow, 0)
	require.Equal(t, 1.0, float64(cc.Events[2].Actual), "the day over")

	// a tentative release whose time is given blocks, as the exact ones do
	f.values["2026-09-26"][2].Time = "2026-09-26T13:00:00"
	calendar.days = map[string]mcpDay{}
	at = time.Date(2026, 9, 26, 12, 50, 0, 0, time.UTC)
	events = read(at.Add(-15*time.Minute), at.Add(15*time.Minute))
	event, blocked := newsBlock(events, at, 15*time.Minute, 15*time.Minute, ImportanceHigh)
	require.True(t, blocked)
	require.Equal(t, "Reunião", event.Name)

	// an exact release at midnight blocks, as before
	require.True(t, (Event{Time: at}).Timed())
	require.Equal(t, TimeExact, timeModeOf("Source publishes an exact time of an event"))
	require.Equal(t, TimeExact, timeModeOf(""))
	require.Equal(t, TimeAllDay, timeModeOf("CALENDAR_TIMEMODE_DATE"))
	require.Equal(t, TimeNone, timeModeOf("CALENDAR_TIMEMODE_NOTIME"))
	require.Equal(t, TimeTentative, timeModeOf("CALENDAR_TIMEMODE_TENTATIVE"))
}
