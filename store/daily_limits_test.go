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
	require.Equal(t, ui.DailyLimitSettings{}, a.DailyLimitOf())

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
