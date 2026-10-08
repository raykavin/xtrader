package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// fakeCalendar returns its events, or its error, and records the reads.
type fakeCalendar struct {
	events     []Event
	err        error
	currencies []string
	from, to   time.Time
	reads      int
}

func (f *fakeCalendar) Events(_ context.Context, currencies []string, from, to time.Time) ([]Event, error) {
	f.reads++
	f.currencies, f.from, f.to = currencies, from, to
	return f.events, f.err
}

// nextClose returns the close of the next candle of the runner: the time the
// decision of the next step is made at.
func (r *traderRunner) nextClose() time.Time {
	return time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC).Add(time.Duration(len(r.df.Close)+1) * 15 * time.Minute)
}

func release(at time.Time, name string, importance Importance, actual float64) Event {
	return Event{Time: at, Currency: "USD", Name: name, Code: name, Importance: importance, Unit: "%",
		Actual: actual, Forecast: 0.2, Previous: 0.3}
}

func calendarRunner(t *testing.T, calendar *fakeCalendar, holding float64) *traderRunner {
	return newTraderRunner(t, func(c *Config) {
		c.Calendar = calendar
		c.CalendarCurrencies = []string{"USD"}
	}, holding)
}

func TestTrader_Calendar(t *testing.T) {
	t.Run("the releases around the close in the context", func(t *testing.T) {
		calendar := &fakeCalendar{}
		r := calendarRunner(t, calendar, 0)
		at := r.nextClose()
		calendar.events = []Event{
			release(at.Add(-3*time.Hour), "too old", ImportanceHigh, 1),
			release(at.Add(-90*time.Minute), "past cpi", ImportanceHigh, 0.4),
			release(at.Add(-time.Hour), "minor", ImportanceLow, 1),
			release(at.Add(2*time.Hour), "payroll", ImportanceHigh, 99), // a backtest knows it: hidden
			release(at.Add(5*time.Hour), "too far", ImportanceHigh, math.NaN()),
		}
		r.step(100, decide(ActionWait, 0.5, nil, nil, nil))

		require.Equal(t, []string{"USD"}, calendar.currencies)
		require.Equal(t, at.Add(-2*time.Hour), calendar.from, "the context back")
		require.Equal(t, at.Add(4*time.Hour), calendar.to, "the context ahead")

		in := r.provider.last()
		require.NotNil(t, in.Calendar)
		require.True(t, in.Calendar.Available)
		require.Len(t, in.Calendar.Events, 2, "medium importance at least, within the window")
		past, next := in.Calendar.Events[0], in.Calendar.Events[1]
		require.Equal(t, "past cpi", past.Name)
		require.Equal(t, -90, past.Minutes)
		require.Equal(t, number(0.4), past.Actual)
		require.Equal(t, "high", past.Importance)
		require.Equal(t, "payroll", next.Name)
		require.Equal(t, 120, next.Minutes)
		require.True(t, math.IsNaN(float64(next.Actual)), "not published yet at the close")
		require.Equal(t, number(0.2), next.Forecast)
		require.Empty(t, in.Rules.EntriesBlockedBy)
		require.Equal(t, []Action{ActionWait, ActionEnterLong, ActionEnterShort}, in.Rules.Actions)

		data, err := json.Marshal(in)
		require.NoError(t, err)
		require.Contains(t, string(data), `"economic_calendar":{"available":true`)
		require.Contains(t, string(data), `"actual":null`)
	})

	t.Run("no entry asked around a release of high importance", func(t *testing.T) {
		for _, offset := range []time.Duration{15 * time.Minute, 0, -15 * time.Minute} {
			calendar := &fakeCalendar{}
			r := calendarRunner(t, calendar, 0)
			calendar.events = []Event{release(r.nextClose().Add(offset), "cpi", ImportanceHigh, math.NaN())}
			r.step(100) // no answer: the fake fails when asked
			require.Empty(t, r.provider.inputs, "release %s from the close", offset)
			require.Zero(t, r.position())
		}
	})

	t.Run("entries asked out of the window, or for a lesser release", func(t *testing.T) {
		for _, event := range []func(time.Time) Event{
			func(at time.Time) Event { return release(at.Add(16*time.Minute), "later", ImportanceHigh, math.NaN()) },
			func(at time.Time) Event { return release(at.Add(-16*time.Minute), "over", ImportanceHigh, 1) },
			func(at time.Time) Event { return release(at.Add(5*time.Minute), "medium", ImportanceMedium, 1) },
		} {
			calendar := &fakeCalendar{}
			r := calendarRunner(t, calendar, 0)
			calendar.events = []Event{event(r.nextClose())}
			r.step(100, enterLong(100))
			require.Equal(t, 10.0, r.position())
		}
	})

	t.Run("a position held is still managed during the block", func(t *testing.T) {
		calendar := &fakeCalendar{}
		r := calendarRunner(t, calendar, 10)
		calendar.events = []Event{release(r.nextClose().Add(5*time.Minute), "fomc", ImportanceHigh, math.NaN())}
		r.step(100, decide(ActionExit, 0.9, nil, nil, nil))

		in := r.provider.last()
		require.Contains(t, in.Rules.EntriesBlockedBy, "USD fomc (high importance) at 2026-")
		require.Equal(t, []Action{ActionHold, ActionExit}, in.Rules.Actions)
		require.Zero(t, r.position(), "the exit went through")
	})

	t.Run("a calendar that fails blocks nothing, and says so", func(t *testing.T) {
		calendar := &fakeCalendar{err: errors.New("terminal closed")}
		r := calendarRunner(t, calendar, 0)
		r.step(100, enterShort(100))
		in := r.provider.last()
		require.NotNil(t, in.Calendar)
		require.False(t, in.Calendar.Available)
		require.Empty(t, in.Calendar.Events)
		require.Equal(t, -10.0, r.position())
	})

	t.Run("no block with the windows at 0", func(t *testing.T) {
		calendar := &fakeCalendar{}
		r := newTraderRunner(t, func(c *Config) {
			c.Calendar, c.CalendarCurrencies = calendar, []string{"USD"}
			c.NewsBlockBefore, c.NewsBlockAfter = 0, 0
		}, 0)
		calendar.events = []Event{release(r.nextClose(), "cpi", ImportanceHigh, math.NaN())}
		r.step(100, enterLong(100))
		require.Equal(t, 10.0, r.position())
	})

	t.Run("a pair without currency of the calendar reads none", func(t *testing.T) {
		calendar := &fakeCalendar{}
		r := newTraderRunner(t, func(c *Config) { c.Calendar = calendar }, 0) // BTCUSDT: BTC and USDT
		r.step(100, decide(ActionWait, 0.5, nil, nil, nil))
		require.Zero(t, calendar.reads)
		require.Nil(t, r.provider.last().Calendar)
	})

	t.Run("no calendar: nothing changes", func(t *testing.T) {
		r := newTraderRunner(t, nil, 0)
		r.step(100, decide(ActionWait, 0.5, nil, nil, nil))
		data, err := json.Marshal(r.provider.last())
		require.NoError(t, err)
		require.NotContains(t, string(data), "economic_calendar")
		require.NotContains(t, string(data), "entries_blocked_by_news")
	})
}

func TestConfig_CalendarValidation(t *testing.T) {
	for name, configure := range map[string]func(*Config){
		"negative window":     func(c *Config) { c.NewsBlockBefore = -time.Minute },
		"negative context":    func(c *Config) { c.CalendarBack = -time.Hour },
		"invalid importance":  func(c *Config) { c.NewsBlockImportance = ImportanceNone },
		"timeframe unknown":   func(c *Config) { c.Calendar, c.Timeframe = &fakeCalendar{}, "15x" },
		"importance too high": func(c *Config) { c.CalendarImportance = ImportanceHigh + 1 },
	} {
		cfg := DefaultConfig()
		configure(&cfg)
		_, err := New(cfg, &fakeProvider{t: t})
		require.Error(t, err, name)
	}
}

func TestNewsBlock(t *testing.T) {
	at := time.Date(2026, 9, 25, 12, 30, 0, 0, time.UTC)
	events := []Event{
		{Time: at.Add(20 * time.Minute), Importance: ImportanceHigh, Name: "later"},
		{Time: at.Add(10 * time.Minute), Importance: ImportanceMedium, Name: "medium"},
	}
	_, blocked := newsBlock(events, at, 15*time.Minute, 15*time.Minute, ImportanceHigh)
	require.False(t, blocked, "20 minutes ahead, the medium one ignored")

	event, blocked := newsBlock(events, at, 15*time.Minute, 15*time.Minute, ImportanceMedium)
	require.True(t, blocked)
	require.Equal(t, "medium", event.Name)

	event, blocked = newsBlock(events, at.Add(35*time.Minute), 15*time.Minute, 15*time.Minute, ImportanceHigh)
	require.True(t, blocked, "15 minutes after it, the end included")
	require.Equal(t, "later", event.Name)
}

func TestCalendarContext_Limit(t *testing.T) {
	at := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	var events []Event
	for i := -30; i <= 30; i++ {
		events = append(events, Event{Time: at.Add(time.Duration(i) * time.Minute), Importance: ImportanceHigh})
	}
	cc := calendarContext(events, at, ImportanceLow, 20)
	require.Len(t, cc.Events, 20, "the closest ones")
	require.Equal(t, -10, cc.Events[0].Minutes, "oldest first")
	require.Equal(t, 9, cc.Events[19].Minutes)
}

func TestCalendarCurrencies(t *testing.T) {
	require.Equal(t, []string{"USD", "EUR"}, calendarCurrencies("EUR", "USD"))
	require.Equal(t, []string{"USD"}, calendarCurrencies("XAU", "USD"))
	require.Equal(t, []string{"USD"}, calendarCurrencies("BTC", "USD"))
	require.Nil(t, calendarCurrencies("BTC", "USDT"))
	require.Nil(t, calendarCurrencies("", ""))
}

func TestParseImportance(t *testing.T) {
	for value, want := range map[string]Importance{"low": ImportanceLow, " Medium ": ImportanceMedium,
		"HIGH": ImportanceHigh} {
		got, err := ParseImportance(value)
		require.NoError(t, err)
		require.Equal(t, want, got)
		require.Equal(t, want, mustParse(t, got.String()))
	}
	_, err := ParseImportance("urgent")
	require.ErrorContains(t, err, "low, medium or high")
}

func mustParse(t *testing.T, value string) Importance {
	t.Helper()
	importance, err := ParseImportance(value)
	require.NoError(t, err)
	return importance
}

func TestTimeframeDuration(t *testing.T) {
	for timeframe, want := range map[string]time.Duration{"1m": time.Minute, "15m": 15 * time.Minute,
		"4h": 4 * time.Hour, "1d": 24 * time.Hour, "1w": 7 * 24 * time.Hour} {
		got, err := timeframeDuration(timeframe)
		require.NoError(t, err)
		require.Equal(t, want, got, timeframe)
	}
	for _, invalid := range []string{"", "m", "0m", "-5m", "5x", "1M"} {
		_, err := timeframeDuration(invalid)
		require.Error(t, err, invalid)
	}
}

func TestMCPUnit(t *testing.T) {
	for value, want := range map[string]string{"Percentage": "%", "None": "", "": "",
		"Measurement unit is not set": "", "Unit is not set": "", "Drilling rigs": "drilling rigs"} {
		require.Equal(t, want, mcpUnit(value), value)
	}
}

// liveCalendarRunner is a liveRunner reading calendar, a candle in progress
// added, the clock 10 minutes after its open.
func liveCalendarRunner(t *testing.T, calendar *fakeCalendar) (*traderRunner, time.Time) {
	t.Helper()
	r, _ := liveRunner(t, func(c *Config) {
		c.Calendar, c.CalendarCurrencies = calendar, []string{"USD"}
	}, 0)
	r.inProgress(101.2)
	now := r.df.Time[len(r.df.Time)-1].Add(10 * time.Minute)
	r.trader.now = func() time.Time { return now }
	return r, now
}

// Asked on the candle in progress, by the consult interval, a reanalysis or
// at the start of the agent, the news block is at the time of the analysis,
// not at the close of the candle before: at 12:40 on the candle of 12:30, a
// release at 12:50 refuses the entries.
func TestTrader_NewsBlockAtTheTimeOfTheAnalysis(t *testing.T) {
	for _, trigger := range []string{TriggerInterval, TriggerReanalysis, TriggerStart} {
		t.Run(trigger, func(t *testing.T) {
			calendar := &fakeCalendar{}
			var r *traderRunner
			var now time.Time
			if trigger == TriggerStart {
				r = startRunner(t, func(c *Config) { c.Calendar, c.CalendarCurrencies = calendar, []string{"USD"} })
				now = r.df.Time[len(r.df.Time)-1].Add(10 * time.Minute)
				r.trader.now = func() time.Time { return now }
			} else {
				r, now = liveCalendarRunner(t, calendar)
			}
			calendar.events = []Event{release(now.Add(10*time.Minute), "cpi", ImportanceHigh, math.NaN())}
			switch trigger {
			case TriggerInterval:
				r.trader.cfg.ConsultInterval = 5 * time.Minute
			case TriggerReanalysis:
				_, err := r.trader.Reanalyze(testPair, ui.ChannelWeb, "")
				require.NoError(t, err)
			}
			asked := len(r.provider.inputs)

			r.trader.OnPartialCandle(r.df, r.broker) // no answer: the fake fails when asked
			require.Len(t, r.provider.inputs, asked, "no entry around the release: not asked")
			d := r.lastDecision()
			require.Equal(t, trigger, d.Trigger)
			require.Equal(t, OutcomeSkipped, d.Outcome)
			require.Equal(t, SkipNews, d.Reason)
			require.Equal(t, now.Add(-2*time.Hour), calendar.from, "the window around now")
			require.Equal(t, now.Add(4*time.Hour), calendar.to)
		})
	}
}

// On the candle in progress, the releases are told to the model from now: a
// release since the close comes with its value.
func TestTrader_CalendarAtTheTimeOfTheAnalysis(t *testing.T) {
	calendar := &fakeCalendar{}
	r, now := liveCalendarRunner(t, calendar)
	calendar.events = []Event{
		release(now.Add(-5*time.Minute), "pmi", ImportanceMedium, 0.4), // after the close, before now
		release(now.Add(10*time.Minute), "sales", ImportanceMedium, math.NaN()),
	}
	r.trader.cfg.ConsultInterval = 5 * time.Minute
	r.provider.answers = []answer{decide(ActionWait, 0.5, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)

	in := r.provider.last()
	require.True(t, in.Scheduled)
	require.Len(t, in.Calendar.Events, 2)
	pmi, sales := in.Calendar.Events[0], in.Calendar.Events[1]
	require.Equal(t, -5, pmi.Minutes)
	require.Equal(t, number(0.4), pmi.Actual, "published")
	require.Equal(t, 10, sales.Minutes)
}

// The news block is checked again when an entry is applied, at the time of
// now: the model answering late, or the decision of the start applied on a
// later tick, inside the window of a release.
func TestTrader_NewsBlockCheckedAtTheEntry(t *testing.T) {
	t.Run("the model answering late", func(t *testing.T) {
		calendar := &fakeCalendar{}
		r, _ := liveRunner(t, func(c *Config) {
			c.Calendar, c.CalendarCurrencies = calendar, []string{"USD"}
		}, 0)
		closeAt := r.nextClose() // the clock 1s after it
		calendar.events = []Event{release(closeAt.Add(15*time.Minute+30*time.Second), "cpi", ImportanceHigh,
			math.NaN())}
		r.provider.asked = func() { r.trader.now = func() time.Time { return closeAt.Add(time.Minute) } }

		r.step(100, enterLong(100))
		require.Contains(t, r.provider.last().Rules.Actions, ActionEnterLong, "no block when asked")
		require.Equal(t, OutcomeRefused, r.lastDecision().Outcome)
		require.Zero(t, r.position())
	})

	t.Run("the decision of the start applied later", func(t *testing.T) {
		calendar := &fakeCalendar{}
		r := startRunner(t, func(c *Config) { c.Calendar, c.CalendarCurrencies = calendar, []string{"USD"} })
		opened := r.df.Time[len(r.df.Time)-1] // the clock 1 minute after it
		calendar.events = []Event{release(opened.Add(20*time.Minute), "cpi", ImportanceHigh, math.NaN())}
		price := r.df.Close[len(r.df.Close)-2]
		r.provider.answers = []answer{enterLong(price)}

		r.trader.OnPartialCandle(r.df, r.broker)
		r.startAsked(testPair)
		require.Contains(t, r.provider.last().Rules.Actions, ActionEnterLong, "no block when asked")

		r.trader.now = func() time.Time { return opened.Add(6 * time.Minute) }
		r.trader.OnPartialCandle(r.df, r.broker)
		d := r.lastDecision()
		require.Equal(t, TriggerStart, d.Trigger)
		require.Equal(t, OutcomeRefused, d.Outcome)
		require.Zero(t, r.position())
	})
}
