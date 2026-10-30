package llm

import (
	"context"
	"errors"
	"math"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
)

// The switch of the news: off, nothing asked to the server; on without the
// server or the token, refused before any request; a token refused by the
// server, told apart from the server failing; the token, the server and the
// window replaced while it runs, the session of the former token dropped.
func TestNewsSwitch(t *testing.T) {
	f := newFakeMCP(t)
	day := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	config := NewsConfig{URL: f.url(), Token: fakeMCPToken, Before: 15 * time.Minute, After: 30 * time.Minute,
		Importance: ImportanceHigh}
	news := NewNewsSwitch(config)
	read := func() error {
		_, err := news.Events(context.Background(), []string{"USD"}, day, day.Add(24*time.Hour))
		return err
	}

	require.ErrorIs(t, read(), ErrNewsDisabled)
	require.Equal(t, CalendarDisabled, calendarStatus(read()))
	require.Zero(t, f.opened, "nothing asked while off")

	config.Enabled, config.URL = true, ""
	switched, changed := news.Set(config)
	require.True(t, switched)
	require.True(t, changed)
	require.ErrorIs(t, read(), ErrNewsNoServer)
	require.Equal(t, CalendarMissingServer, calendarStatus(read()))

	config.URL, config.Token = f.url(), " "
	news.Set(config)
	require.ErrorIs(t, read(), ErrNewsNoToken)
	require.Equal(t, CalendarMissingToken, calendarStatus(read()))
	require.Zero(t, f.opened, "nothing asked without a server or a token")

	config.Token = "wrong-token"
	news.Set(config)
	err := read()
	require.ErrorIs(t, err, ErrMCPUnauthorized)
	require.Equal(t, CalendarUnauthorized, calendarStatus(err))
	require.NotContains(t, err.Error(), "wrong-token", "never the token in the error")

	config.Token = fakeMCPToken
	switched, changed = news.Set(config)
	require.False(t, switched)
	require.True(t, changed)
	require.NoError(t, read(), "the new token at once")
	opened := f.opened
	require.NoError(t, read())
	require.Equal(t, opened, f.opened, "its session kept")
	_, changed = news.Set(config)
	require.False(t, changed, "the same news")

	before, after, importance := news.Window()
	require.Equal(t, [3]any{15 * time.Minute, 30 * time.Minute, ImportanceHigh}, [3]any{before, after, importance})
	config.Before, config.Importance = 5*time.Minute, ImportanceMedium
	news.Set(config)
	before, _, importance = news.Window()
	require.Equal(t, 5*time.Minute, before, "the window replaced")
	require.Equal(t, ImportanceMedium, importance)

	other := newFakeMCP(t)
	config.URL = other.url()
	news.Set(config)
	require.NoError(t, read())
	require.Positive(t, other.opened, "another server read")

	config.Enabled = false
	news.Set(config)
	require.ErrorIs(t, read(), ErrNewsDisabled, "off again: nothing read")
	require.Equal(t, CalendarUnavailable, calendarStatus(errors.New("terminal closed")))
}

// The release of a calendar blocking the entries in the window of the switch,
// none with the window at 0; the currencies of the calendar of a pair.
func TestNewsSwitch_Blocking(t *testing.T) {
	at := time.Date(2026, 9, 25, 12, 20, 0, 0, time.UTC)
	nfp := release(time.Date(2026, 9, 25, 12, 30, 0, 0, time.UTC), "nfp", ImportanceHigh, math.NaN())
	minor := release(time.Date(2026, 9, 25, 12, 25, 0, 0, time.UTC), "minor", ImportanceLow, math.NaN())
	news := NewNewsSwitch(NewsConfig{Enabled: true, Before: 15 * time.Minute, After: 15 * time.Minute,
		Importance: ImportanceHigh})
	event, ok := news.Blocking([]Event{minor, nfp}, at)
	require.True(t, ok)
	require.Equal(t, "nfp", event.Name)
	_, ok = news.Blocking([]Event{nfp}, at.Add(-10*time.Minute))
	require.False(t, ok, "before the window")
	_, ok = news.Blocking([]Event{minor}, at)
	require.False(t, ok, "below the importance")
	news.Set(NewsConfig{Enabled: true, Importance: ImportanceHigh})
	_, ok = news.Blocking([]Event{nfp}, at)
	require.False(t, ok, "no window")

	exchange.RegisterMT5Pair("XAUUSDm", "XAU", "USD")
	exchange.RegisterMT5Pair("EURUSDm", "EUR", "USD")
	exchange.RegisterMT5Pair("BTCUSDTm", "BTC", "USDT")
	require.Equal(t, []string{"USD"}, CalendarCurrencies("XAUUSDm"))
	require.Equal(t, []string{"USD", "EUR"}, CalendarCurrencies("EURUSDm"))
	require.Empty(t, CalendarCurrencies("BTCUSDTm"), "no currency of the calendar")
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
// nothing else; the window of the news block is the one of the switch.
func TestTrader_SetNews(t *testing.T) {
	f := newFakeMCP(t)
	news := NewNewsSwitch(NewsConfig{Enabled: true, URL: f.url(), Token: fakeMCPToken})
	r := calendarRunner(t, &fakeCalendar{}, 0)
	switched, changed := r.trader.SetNews(NewsConfig{})
	require.False(t, switched || changed, "not a switch")
	require.Nil(t, r.trader.News())

	r = newTraderRunner(t, func(c *Config) { c.Calendar, c.CalendarCurrencies = news, []string{"USD"} }, 0)
	require.Same(t, news, r.trader.News())
	switched, _ = r.trader.SetNews(NewsConfig{URL: f.url(), Token: fakeMCPToken})
	require.True(t, switched)
	require.False(t, news.Enabled())
	r.step(100, decide(ActionWait, 0.5, nil, nil, nil))
	require.Equal(t, CalendarDisabled, r.provider.last().Calendar.Status)
	require.Zero(t, f.opened, "nothing asked while off")

	r.trader.SetNews(NewsConfig{Enabled: true, URL: f.url(), Token: fakeMCPToken, Before: 20 * time.Minute,
		After: 10 * time.Minute, Importance: ImportanceMedium})
	before, after, importance := r.trader.newsWindow()
	require.Equal(t, [3]any{20 * time.Minute, 10 * time.Minute, ImportanceMedium}, [3]any{before, after, importance})
}
