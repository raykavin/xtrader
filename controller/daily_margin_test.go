//go:build unix

package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The case of 2026-11-17: a loss limit of 100 whose stop left the day at
// −99,99, so short of the limit, the entries still allowed with a cent left.
// With the least margin (10% by default), the entries of the agents are
// blocked in two reads in a row, waiting for the orders being sent, nothing
// closed; recorded as a guard of the margin, told on the dashboard; the next
// day releasing it.
func TestDailyLimiter_MarginBlocksTheEntries(t *testing.T) {
	s, clock, trend := newDailySupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	setGlobal(t, s, 100, 0)
	day := time.Date(2026, 10, 28, 0, 0, 0, 0, belem(t))
	s.bridge.mu.Lock()
	s.bridge.deals = []bridgeDeal{deal(1, 9, 101, dealEntryIn, day.Add(9*time.Hour), 0, 0, 0, 0),
		deal(2, 9, 101, dealEntryOut, day.Add(10*time.Hour), -99.99, 0, 0, 0)}
	s.bridge.mu.Unlock()

	require.NoError(t, l.evaluate(ctx))
	assert.Equal(t, ui.DailyFree, l.Status().Global.State, "a read alone doesn't block")
	clock.add(5 * time.Second)
	require.NoError(t, l.evaluate(ctx))
	status := l.Status()
	require.Equal(t, ui.DailyMargin, status.Global.State)
	require.NotNil(t, status.Global.Guard)
	assert.Equal(t, store.GuardMargin, status.Global.Guard.Kind)
	assert.Contains(t, status.Global.Guard.Reason, "faltam 0,01 BRL para a perda máxima (mínimo 10,00 BRL)")

	a, err := s.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	entries, err := entriesOf(ctx, s.store, a)
	require.NoError(t, err)
	assert.True(t, entries.EntriesBlocked, "no new entry")
	trend.mu.Lock()
	assert.Positive(t, trend.drains, "the orders being sent waited for")
	assert.Empty(t, trend.closes, "nothing closed")
	trend.mu.Unlock()
	limits, err := s.store.TrippedDailyGuards(ctx)
	require.NoError(t, err)
	assert.Empty(t, limits, "no limit reached: nothing closed")
	margins, err := s.store.TrippedMarginGuards(ctx)
	require.NoError(t, err)
	require.Len(t, margins, 1)
	assert.InDelta(t, 0.01, margins[0].Margin, 1e-9)
	assert.InDelta(t, 10, margins[0].Least, 1e-9)
	events, err := s.store.DailyLimitEvents(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, store.EventTripped, events[0].Kind)
	assert.Contains(t, string(events[0].Metric), `"guard": "margin"`)

	// the next day: released, the entries back
	clock.set(time.Date(2026, 10, 29, 0, 0, 30, 0, belem(t)))
	s.bridge.mu.Lock()
	s.bridge.deals = nil
	s.bridge.mu.Unlock()
	require.NoError(t, l.evaluate(ctx))
	assert.Equal(t, ui.DailyFree, l.Status().Global.State)
	entries, err = entriesOf(ctx, s.store, a)
	require.NoError(t, err)
	assert.False(t, entries.EntriesBlocked)
}

// The margin weighs the worse of the realized and the result with the
// positions open, so that a profit still open never lends margin: an open
// loss eating it, blocked; the position recovering with the realized far from
// the limit, released in two reads; the least margin off, released at once
// and never blocking; the limit off, released.
func TestDailyLimiter_MarginReleased(t *testing.T) {
	s, clock, _ := newDailySupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	day := time.Date(2026, 10, 28, 0, 0, 0, 0, belem(t))
	loss, pct := 100.0, 20.0
	set := func(settings ui.DailyLimitSettings) {
		t.Helper()
		_, _, err := s.store.SetGlobalDailyLimit(ctx, settings, store.DailyChange{Day: "2026-10-28",
			Via: store.ViaSettings, By: "admin"})
		require.NoError(t, err)
	}
	set(ui.DailyLimitSettings{Enabled: true, MaxLoss: &loss, MinMarginPct: &pct}) // 20 of 100
	s.bridge.mu.Lock()
	s.bridge.deals = []bridgeDeal{deal(1, 9, 101, dealEntryIn, day.Add(9*time.Hour), 0, 0, 0, 0),
		deal(2, 9, 101, dealEntryOut, day.Add(10*time.Hour), -50, 0, 0, 0)}
	s.bridge.mu.Unlock()
	evaluate := func(times int) {
		t.Helper()
		for range times {
			require.NoError(t, l.evaluate(ctx))
			clock.add(5 * time.Second)
		}
	}

	// -50 realized, -35 open: 15 left of 100, under 20
	s.bridge.set(position(10, 101, "XAUUSDm", false, 0.01, -35, 0))
	evaluate(2)
	require.Equal(t, ui.DailyMargin, l.Status().Global.State)

	// open in profit: the realized decides, 50 left, over 20
	s.bridge.set(position(10, 101, "XAUUSDm", false, 0.01, 30, 0))
	evaluate(1)
	assert.Equal(t, ui.DailyMargin, l.Status().Global.State, "a read alone doesn't release")
	evaluate(1)
	assert.Equal(t, ui.DailyFree, l.Status().Global.State)

	// a profit open never lends margin: -95 realized, +60 open, 5 left
	s.bridge.mu.Lock()
	s.bridge.deals[1].Profit = -95
	s.bridge.mu.Unlock()
	evaluate(2)
	require.Equal(t, ui.DailyMargin, l.Status().Global.State, "the realized under the least")

	// the least margin off: released at once, never blocking
	zero := 0.0
	set(ui.DailyLimitSettings{Enabled: true, MaxLoss: &loss, MinMarginPct: &zero})
	evaluate(1)
	assert.Equal(t, ui.DailyFree, l.Status().Global.State)
	evaluate(3)
	assert.Equal(t, ui.DailyFree, l.Status().Global.State)

	// on again, then the limit off: released
	set(ui.DailyLimitSettings{Enabled: true, MaxLoss: &loss, MinMarginPct: &pct})
	evaluate(2)
	require.Equal(t, ui.DailyMargin, l.Status().Global.State)
	set(ui.DailyLimitSettings{})
	evaluate(1)
	margins, err := s.store.TrippedMarginGuards(ctx)
	require.NoError(t, err)
	assert.Empty(t, margins)
}

// A change of the limits putting the margin under its least tells it in its
// preview, nothing to confirm: the entries blocked by the next reads.
func TestDailyLimits_MarginPreview(t *testing.T) {
	s, _, _ := newDailySupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	day := time.Date(2026, 10, 28, 0, 0, 0, 0, belem(t))
	s.bridge.mu.Lock()
	s.bridge.deals = []bridgeDeal{deal(1, 9, 101, dealEntryIn, day.Add(9*time.Hour), 0, 0, 0, 0),
		deal(2, 9, 101, dealEntryOut, day.Add(10*time.Hour), -95, 0, 0, 0)}
	s.bridge.mu.Unlock()
	loss := 100.0
	preview, _, err := l.preview(ctx, nil, ui.DailyLimitSettings{Enabled: true, MaxLoss: &loss})
	require.NoError(t, err)
	assert.Equal(t, ui.DailyEffectNone, preview.Effect)
	assert.Contains(t, preview.Message, "Faltam 5,00 BRL para a perda máxima, abaixo da margem mínima de 10,00 BRL")
}
