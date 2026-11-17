package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

func amount(v float64) *float64 { return &v }

// The daily limits, of all the agents and of an agent: none while never set,
// set and read back, each change recorded with the previous and the next
// settings, refused out of the rules as the database checks them.
func TestDailyLimits(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := llmAgent(t, s, "llm")
	change := DailyChange{Day: "2026-10-29", Via: ViaSettings, By: "admin", Reason: "conter a perda do dia"}

	_, found, err := s.GlobalDailyLimit(ctx)
	require.NoError(t, err)
	require.False(t, found, "no limit while never set")
	on, margin := true, ui.DefaultMinMarginPct
	require.Equal(t, ui.DailyLimitSettings{BrokerStops: &on, MinMarginPct: &margin}, a.DailyLimitOf(),
		"the stops at the broker on, the least margin by default")

	global := ui.DailyLimitSettings{Enabled: true, MaxLoss: amount(150), MaxGain: amount(300)}
	previous, next, err := s.SetGlobalDailyLimit(ctx, global, change)
	require.NoError(t, err)
	require.Equal(t, DailyLimit{}, previous)
	require.Equal(t, NoCurrency, next.Currency, "in the currency of each account: none of its own")
	require.Empty(t, next.Settings().Currency)
	got, found, err := s.GlobalDailyLimit(ctx)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 150.0, *got.MaxLoss)
	require.Equal(t, "admin", got.UpdatedBy)

	// changed: the record holds both
	global.MaxLoss = amount(100)
	previous, _, err = s.SetGlobalDailyLimit(ctx, global, change)
	require.NoError(t, err)
	require.Equal(t, 150.0, *previous.MaxLoss)
	events, err := s.DailyLimitEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, events, 2)
	var before, after ui.DailyLimitSettings
	require.NoError(t, json.Unmarshal(events[0].Previous, &before))
	require.NoError(t, json.Unmarshal(events[0].Next, &after))
	require.Equal(t, 150.0, *before.MaxLoss)
	require.Equal(t, 100.0, *after.MaxLoss)
	require.Equal(t, EventLimitChanged, events[0].Kind)
	require.Equal(t, "conter a perda do dia", events[0].Reason)
	require.Nil(t, events[1].Previous, "none before the first")

	// of an agent, in the currency of its account
	_, _, err = s.SetAgentDailyLimit(ctx, a.ID, ui.DailyLimitSettings{Enabled: true, MaxLoss: amount(40)}, change)
	require.NoError(t, err)
	a, err = s.Agent(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 40.0, *a.DailyLimitOf().MaxLoss)
	require.True(t, a.DailyLimitOf().Enabled)

	for name, bad := range map[string]ui.DailyLimitSettings{
		"enabled without a limit": {Enabled: true},
		"a loss of 0":             {MaxLoss: amount(0)},
		"a negative gain":         {MaxGain: amount(-5)},
		"a currency":              {Enabled: true, MaxLoss: amount(10), Currency: "BRL"},
	} {
		_, _, err := s.SetGlobalDailyLimit(ctx, bad, change)
		require.ErrorIs(t, err, ErrInvalid, name)
	}
	_, _, err = s.SetAgentDailyLimit(ctx, a.ID, ui.DailyLimitSettings{Currency: "USD"}, change)
	require.ErrorIs(t, err, ErrInvalid, "the currency of its account")
	// off with its amounts kept: taken
	_, _, err = s.SetGlobalDailyLimit(ctx, ui.DailyLimitSettings{MaxLoss: amount(10)}, change)
	require.NoError(t, err)
}

// A daily guard trips once a day and scope (tripping it again is a no-op),
// trips again once released (counted), blocks the entries of its agents of
// any day until released, the revision of their entries only growing; the
// end of its day releases it.
func TestDailyGuards(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := llmAgent(t, s, "llm")
	b := llmAgent(t, s, "other")
	revision := func(id int64) int64 {
		t.Helper()
		_, _, rev, err := s.DailyEntries(ctx, id)
		require.NoError(t, err)
		return rev
	}
	blocked, _, rev0, err := s.DailyEntries(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, blocked)
	require.Zero(t, rev0)

	// of the agent a: only it blocked
	trip := GuardTrip{Day: "2026-10-28", AgentID: &a.ID, Kind: GuardLimit, Trigger: TriggerLoss, Result: -41,
		Limit: 40, Currency: "BRL", Reason: "perda diária de llm atingida", Metric: map[string]float64{"total": -41}}
	guard, newly, err := s.TripDailyGuard(ctx, trip)
	require.NoError(t, err)
	require.True(t, newly)
	require.Equal(t, "2026-10-28", guard.DayOf())
	require.Equal(t, TriggerLoss, guard.TriggerOf())
	blocked, reason, rev1, err := s.DailyEntries(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, blocked)
	require.Equal(t, "perda diária de llm atingida", reason)
	require.Greater(t, rev1, rev0)
	blocked, _, _, err = s.DailyEntries(ctx, b.ID)
	require.NoError(t, err)
	require.False(t, blocked, "another agent untouched")

	// tripped again the same day: nothing new
	again, newly, err := s.TripDailyGuard(ctx, trip)
	require.NoError(t, err)
	require.False(t, newly)
	require.Equal(t, guard.ID, again.ID)
	require.Equal(t, rev1, revision(a.ID), "no event")

	// released by a change of the limits, tripped again: counted
	_, released, err := s.ReleaseDailyGuard(ctx, guard.ID, DailyChange{Day: "2026-10-28", Via: ViaChat, By: "admin",
		Reason: "limite elevado"}, nil)
	require.NoError(t, err)
	require.True(t, released)
	blocked, _, rev2, err := s.DailyEntries(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, blocked)
	require.Greater(t, rev2, rev1)
	again, newly, err = s.TripDailyGuard(ctx, trip)
	require.NoError(t, err)
	require.True(t, newly)
	require.Equal(t, 2, again.Trips)

	// of all the agents: both blocked; the flatten recorded
	all, _, err := s.TripDailyGuard(ctx, GuardTrip{Day: "2026-10-28", Kind: GuardLimit, Trigger: TriggerGain,
		Result: 310, Limit: 300, Currency: "BRL", Reason: "ganho diário atingido"})
	require.NoError(t, err)
	blocked, reason, _, err = s.DailyEntries(ctx, b.ID)
	require.NoError(t, err)
	require.True(t, blocked)
	require.Equal(t, "ganho diário atingido", reason)
	_, reason, _, err = s.DailyEntries(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "ganho diário atingido", reason, "the one of all the agents first")
	require.NoError(t, s.SetGuardFlatten(ctx, all.ID, FlattenDone, nil, 1))
	tripped, err := s.TrippedDailyGuards(ctx)
	require.NoError(t, err)
	require.Len(t, tripped, 2)
	require.Equal(t, FlattenDone, tripped[1].FlattenStatus)

	// an incomplete guard of the same scope and day apart from the limit
	_, newly, err = s.TripDailyGuard(ctx, GuardTrip{Day: "2026-10-28", Kind: GuardIncomplete,
		Reason: "conta 1 sem leitura"})
	require.NoError(t, err)
	require.True(t, newly)

	// the next day: every guard of the day before released, the revisions grown
	before := revision(b.ID)
	gone, err := s.RolloverDailyGuards(ctx, "2026-10-29")
	require.NoError(t, err)
	require.Len(t, gone, 3)
	for _, id := range []int64{a.ID, b.ID} {
		blocked, _, _, err := s.DailyEntries(ctx, id)
		require.NoError(t, err)
		require.False(t, blocked)
	}
	require.Greater(t, revision(b.ID), before)
	gone, err = s.RolloverDailyGuards(ctx, "2026-10-29")
	require.NoError(t, err)
	require.Empty(t, gone, "once")

	events, err := s.DailyLimitEvents(ctx, 50)
	require.NoError(t, err)
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Kind]++
	}
	require.Equal(t, map[string]int{EventTripped: 3, EventReleased: 1, EventFlatten: 1, EventIncomplete: 1,
		EventDayRollover: 3}, kinds)
}

// The switch of the stops at the broker of a loss limit, of all the agents
// and of an agent: on while never set, kept as it is when a change doesn't
// send it, set apart for each scope, recorded with the limits.
func TestDailyLimits_BrokerStops(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := llmAgent(t, s, "llm")
	b := llmAgent(t, s, "other")
	change := DailyChange{Day: "2026-11-16", Via: ViaSettings, By: "admin"}
	on, off := true, false

	global, _, err := s.GlobalDailyLimit(ctx)
	require.NoError(t, err)
	require.True(t, *global.Settings().BrokerStops, "on while never set")

	_, next, err := s.SetGlobalDailyLimit(ctx, ui.DailyLimitSettings{Enabled: true, MaxLoss: amount(100),
		BrokerStops: &off}, change)
	require.NoError(t, err)
	require.False(t, *next.Settings().BrokerStops)
	global, _, err = s.GlobalDailyLimit(ctx)
	require.NoError(t, err)
	require.False(t, *global.Settings().BrokerStops)
	require.False(t, global.Settings().StopsOn())

	// a change without it: kept
	_, _, err = s.SetGlobalDailyLimit(ctx, ui.DailyLimitSettings{Enabled: true, MaxLoss: amount(90)}, change)
	require.NoError(t, err)
	global, _, err = s.GlobalDailyLimit(ctx)
	require.NoError(t, err)
	require.False(t, *global.Settings().BrokerStops, "kept")

	// of an agent, apart from all the agents and from the other agents
	_, _, err = s.SetAgentDailyLimit(ctx, a.ID, ui.DailyLimitSettings{Enabled: true, MaxLoss: amount(40),
		BrokerStops: &off}, change)
	require.NoError(t, err)
	_, _, err = s.SetAgentDailyLimit(ctx, b.ID, ui.DailyLimitSettings{Enabled: true, MaxLoss: amount(30)}, change)
	require.NoError(t, err)
	agent, err := s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	require.False(t, *agent.DailyLimitOf().BrokerStops)
	other, err := s.AgentByName(ctx, "other")
	require.NoError(t, err)
	require.True(t, other.DailyLimitOf().StopsOn(), "on, never set")
	_, _, err = s.SetAgentDailyLimit(ctx, a.ID, ui.DailyLimitSettings{Enabled: true, MaxLoss: amount(40),
		BrokerStops: &on}, change)
	require.NoError(t, err)
	agent, err = s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	require.True(t, agent.DailyLimitOf().StopsOn())
	global, _, err = s.GlobalDailyLimit(ctx)
	require.NoError(t, err)
	require.False(t, *global.Settings().BrokerStops, "all the agents untouched")

	// recorded with the limits
	events, err := s.DailyLimitEvents(ctx, 1)
	require.NoError(t, err)
	var before, after ui.DailyLimitSettings
	require.NoError(t, json.Unmarshal(events[0].Previous, &before))
	require.NoError(t, json.Unmarshal(events[0].Next, &after))
	require.False(t, *before.BrokerStops)
	require.True(t, *after.BrokerStops)

	// a loss limit only: the stops on with a loss limit enabled
	require.False(t, ui.DailyLimitSettings{Enabled: true, MaxGain: amount(10)}.StopsOn(), "no loss limit")
	require.False(t, ui.DailyLimitSettings{MaxLoss: amount(10)}.StopsOn(), "off")
}

// The least margin of a loss limit for new entries, of all the agents and of
// an agent: 10% while never set, kept as it is when a change doesn't send it,
// 0 turning it off, refused out of 0 to under 100; the margin in money by the
// loss limit; the guard of the margin tripped and released as the others, its
// trip recorded as an event of its own.
func TestDailyLimits_MinMargin(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := llmAgent(t, s, "llm")
	change := DailyChange{Day: "2026-11-17", Via: ViaSettings, By: "admin"}

	global, _, err := s.GlobalDailyLimit(ctx)
	require.NoError(t, err)
	require.InDelta(t, 10, *global.Settings().MinMarginPct, 1e-9, "10% while never set")

	_, next, err := s.SetGlobalDailyLimit(ctx, ui.DailyLimitSettings{Enabled: true, MaxLoss: amount(200),
		MinMarginPct: amount(25)}, change)
	require.NoError(t, err)
	require.InDelta(t, 25, *next.Settings().MinMarginPct, 1e-9)
	require.InDelta(t, 50, next.Settings().MinMargin(), 1e-9, "25% of 200")
	_, _, err = s.SetGlobalDailyLimit(ctx, ui.DailyLimitSettings{Enabled: true, MaxLoss: amount(100)}, change)
	require.NoError(t, err)
	global, _, err = s.GlobalDailyLimit(ctx)
	require.NoError(t, err)
	require.InDelta(t, 25, *global.Settings().MinMarginPct, 1e-9, "kept")
	require.InDelta(t, 25, global.Settings().MinMargin(), 1e-9, "following the loss limit")

	zero := 0.0
	_, _, err = s.SetAgentDailyLimit(ctx, a.ID, ui.DailyLimitSettings{Enabled: true, MaxLoss: amount(40),
		MinMarginPct: &zero}, change)
	require.NoError(t, err)
	agent, err := s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	require.Zero(t, agent.DailyLimitOf().MinMargin(), "0: off")
	global, _, err = s.GlobalDailyLimit(ctx)
	require.NoError(t, err)
	require.InDelta(t, 25, *global.Settings().MinMarginPct, 1e-9, "all the agents untouched")

	for _, refused := range []float64{-1, 100, 150} {
		_, _, err = s.SetGlobalDailyLimit(ctx, ui.DailyLimitSettings{Enabled: true, MaxLoss: amount(100),
			MinMarginPct: &refused}, change)
		require.ErrorIs(t, err, ErrInvalid, "%v", refused)
	}
	require.Zero(t, ui.DailyLimitSettings{Enabled: true, MaxGain: amount(50)}.MinMargin(), "no loss limit")

	// the guard of the margin, on its own table: tripped once, the entries
	// blocked; released; tripped again; each recorded; ended with its day
	guard, newly, err := s.TripMarginGuard(ctx, MarginTrip{Day: "2026-11-17", Margin: 0.01, Least: 10,
		Result: -99.99, Currency: "BRL", Reason: "margem abaixo do mínimo", Metric: map[string]any{"total": -99.99}})
	require.NoError(t, err)
	require.True(t, newly)
	require.Equal(t, GuardMargin, guard.Guard().Kind)
	_, newly, err = s.TripMarginGuard(ctx, MarginTrip{Day: "2026-11-17", Least: 10, Reason: "de novo"})
	require.NoError(t, err)
	require.False(t, newly, "tripped already")
	blocked, reason, revision, err := s.DailyEntries(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, blocked)
	require.Equal(t, "margem abaixo do mínimo", reason)
	events, err := s.DailyLimitEvents(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, EventTripped, events[0].Kind)
	require.Nil(t, events[0].GuardID)
	require.Contains(t, string(events[0].Metric), `"guard": "margin"`)
	require.Equal(t, events[0].ID, revision, "the entries of the agents at its revision")
	tripped, err := s.TrippedMarginGuards(ctx)
	require.NoError(t, err)
	require.Len(t, tripped, 1)

	ok, err := s.ReleaseMarginGuard(ctx, guard.ID, DailyChange{Day: "2026-11-17", Via: ViaSystem, By: "system",
		Reason: "margem de volta"}, nil)
	require.NoError(t, err)
	require.True(t, ok)
	blocked, _, _, err = s.DailyEntries(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, blocked)
	ok, err = s.ReleaseMarginGuard(ctx, guard.ID, DailyChange{Day: "2026-11-17"}, nil)
	require.NoError(t, err)
	require.False(t, ok, "released already")

	again, newly, err := s.TripMarginGuard(ctx, MarginTrip{Day: "2026-11-17", Least: 10, Currency: "BRL",
		Reason: "de novo"})
	require.NoError(t, err)
	require.True(t, newly)
	require.Equal(t, guard.ID, again.ID)
	require.Equal(t, 2, again.Trips)

	released, err := s.RolloverDailyGuards(ctx, "2026-11-18")
	require.NoError(t, err)
	require.Len(t, released, 1)
	require.Equal(t, GuardMargin, released[0].Kind)
	tripped, err = s.TrippedMarginGuards(ctx)
	require.NoError(t, err)
	require.Empty(t, tripped, "the day over")
}
