package llm

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The switch of the news: off, nothing asked to the server; on without the
// token, refused before any request; a token refused by the server, told
// apart from the server failing; the token replaced while it runs, the
// session of the former one dropped.
func TestNewsSwitch(t *testing.T) {
	f := newFakeMCP(t)
	calendar, err := NewMCPCalendar(f.url(), "")
	require.NoError(t, err)
	day := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	news := NewNewsSwitch(calendar, false, fakeMCPToken)
	read := func() error {
		_, err := news.Events(context.Background(), []string{"USD"}, day, day.Add(24*time.Hour))
		return err
	}

	require.ErrorIs(t, read(), ErrNewsDisabled)
	require.Equal(t, CalendarDisabled, calendarStatus(read()))
	require.Zero(t, f.opened, "nothing asked while off")

	switched, changed := news.Set(true, " ")
	require.True(t, switched)
	require.True(t, changed, "the token removed")
	require.ErrorIs(t, read(), ErrNewsNoToken)
	require.Equal(t, CalendarMissingToken, calendarStatus(read()))
	require.Zero(t, f.opened, "nothing asked without a token")

	news.Set(true, "wrong-token")
	err = read()
	require.ErrorIs(t, err, ErrMCPUnauthorized)
	require.Equal(t, CalendarUnauthorized, calendarStatus(err))
	require.NotContains(t, err.Error(), "wrong-token", "never the token in the error")

	switched, changed = news.Set(true, fakeMCPToken)
	require.False(t, switched)
	require.True(t, changed)
	require.NoError(t, read(), "the new token at once")
	opened := f.opened
	require.NoError(t, read())
	require.Equal(t, opened, f.opened, "its session kept")
	_, changed = news.Set(true, fakeMCPToken)
	require.False(t, changed, "the same token")

	news.Set(false, fakeMCPToken)
	require.ErrorIs(t, read(), ErrNewsDisabled, "off again: nothing read")

	require.Equal(t, CalendarUnavailable, calendarStatus(errors.New("terminal closed")))
}

// The test of the settings of the news: the server answering, refusing the
// token, unreachable, and no token at all.
func TestProbeNews(t *testing.T) {
	f := newFakeMCP(t)
	ctx := context.Background()
	require.NoError(t, ProbeNews(ctx, f.url(), fakeMCPToken))
	require.ErrorIs(t, ProbeNews(ctx, f.url(), "wrong"), ErrMCPUnauthorized)
	require.ErrorIs(t, ProbeNews(ctx, f.url(), ""), ErrNewsNoToken)

	closed := httptest.NewServer(nil)
	url := closed.URL + "/mcp"
	closed.Close()
	err := ProbeNews(ctx, url, fakeMCPToken)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrMCPUnauthorized)
	require.Equal(t, CalendarUnavailable, calendarStatus(err))
}

// The context of the model tells why the calendar wasn't read: the news off,
// the token missing or refused, the server unavailable; never an available
// calendar without any release, and no entry blocked by news.
func TestTrader_CalendarStatus(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{ErrNewsDisabled, CalendarDisabled},
		{ErrNewsNoToken, CalendarMissingToken},
		{&mcpOpenError{err: &mcpHTTPError{Status: 401}}, CalendarUnauthorized},
		{&mcpHTTPError{Status: 503}, CalendarUnavailable},
	} {
		t.Run(c.want, func(t *testing.T) {
			r := calendarRunner(t, &fakeCalendar{err: c.err}, 0)
			r.step(100, enterShort(100))
			in := r.provider.last()
			require.NotNil(t, in.Calendar)
			require.False(t, in.Calendar.Available)
			require.Equal(t, c.want, in.Calendar.Status)
			require.Empty(t, in.Calendar.Events)
			require.Empty(t, in.Rules.EntriesBlockedBy)
			require.Equal(t, -10.0, r.position(), "the entry went through, as without a calendar")
		})
	}

	r := calendarRunner(t, &fakeCalendar{}, 0)
	r.step(100, decide(ActionWait, 0.5, nil, nil, nil))
	require.True(t, r.provider.last().Calendar.Available)
	require.Empty(t, r.provider.last().Calendar.Status, "read: no reason to tell")
}

// The trader switches the news of its calendar when it is a NewsSwitch, and
// nothing else.
func TestTrader_SetNews(t *testing.T) {
	f := newFakeMCP(t)
	calendar, err := NewMCPCalendar(f.url(), fakeMCPToken)
	require.NoError(t, err)
	news := NewNewsSwitch(calendar, true, fakeMCPToken)
	r := calendarRunner(t, &fakeCalendar{}, 0)
	switched, changed := r.trader.SetNews(false, "")
	require.False(t, switched || changed, "not a switch")

	r = newTraderRunner(t, func(c *Config) { c.Calendar, c.CalendarCurrencies = news, []string{"USD"} }, 0)
	switched, _ = r.trader.SetNews(false, fakeMCPToken)
	require.True(t, switched)
	require.False(t, news.Enabled())
	r.step(100, decide(ActionWait, 0.5, nil, nil, nil))
	require.Equal(t, CalendarDisabled, r.provider.last().Calendar.Status)
	require.Zero(t, f.opened, "nothing asked while off")
}
