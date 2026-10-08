package store

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Each reset starts a period of the agent, the earlier ones kept in their
// order, the current one first, apart from the periods of the other agents.
func TestStartPeriod_KeepsTheEarlierOnes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	llm := llmAgent(t, s, "llm")
	llmAgent(t, s, "other")

	periods, err := s.Periods(ctx, "llm")
	require.NoError(t, err)
	require.Empty(t, periods, "none before the first reset")
	current, err := s.CurrentPeriods(ctx)
	require.NoError(t, err)
	require.Empty(t, current)

	before := time.Now().Add(-time.Second)
	first, err := s.StartPeriod(ctx, "llm", 1000.005, " brl ", "admin")
	require.NoError(t, err)
	require.Equal(t, llm.ID, first.AgentID)
	require.InDelta(t, 1000.01, first.Deposit, 1e-9, "rounded to cents")
	require.Equal(t, "BRL", first.Currency)
	require.Equal(t, "admin", first.CreatedBy)
	require.True(t, first.StartedAt.After(before))

	second, err := s.StartPeriod(ctx, "llm", 0, "USD", "ana")
	require.NoError(t, err)
	require.True(t, second.StartedAt.After(first.StartedAt))
	_, err = s.StartPeriod(ctx, "other", 50, "BRL", "admin")
	require.NoError(t, err)

	periods, err = s.Periods(ctx, "llm")
	require.NoError(t, err)
	require.Len(t, periods, 2)
	require.Equal(t, second.ID, periods[0].ID, "the current one first")
	require.Equal(t, first.ID, periods[1].ID)
	require.Equal(t, first.StartedAt.UTC(), periods[1].StartedAt.UTC())
	require.Equal(t, "ana", periods[0].CreatedBy)

	current, err = s.CurrentPeriods(ctx)
	require.NoError(t, err)
	require.Len(t, current, 2)
	require.Equal(t, second.ID, current["llm"].ID)
	require.Equal(t, "USD", current["llm"].Currency)
	require.InDelta(t, 50, current["other"].Deposit, 1e-9)
}

// A period starts after the one before it, even with the clock behind it.
func TestStartPeriod_AfterTheOneBefore(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	llmAgent(t, s, "llm")
	first, err := s.StartPeriod(ctx, "llm", 10, "BRL", "admin")
	require.NoError(t, err)
	ahead := first.StartedAt.Add(time.Hour)
	require.NoError(t, s.db.Model(&Period{}).Where("id = ?", first.ID).Update("started_at", ahead).Error)

	second, err := s.StartPeriod(ctx, "llm", 10, "BRL", "admin")
	require.NoError(t, err)
	require.Equal(t, ahead.Add(time.Microsecond).UTC(), second.StartedAt.UTC())
}

// The deposit and the currency are checked, and the agent must exist.
func TestStartPeriod_Refused(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	llmAgent(t, s, "llm")
	for _, tc := range []struct {
		deposit  float64
		currency string
		want     error
	}{
		{-1, "BRL", ErrInvalid},
		{math.NaN(), "BRL", ErrInvalid},
		{math.Inf(1), "BRL", ErrInvalid},
		{2e12, "BRL", ErrInvalid},
		{10, "", ErrInvalid},
		{10, "REAL", ErrInvalid},
		{10, "R$", ErrInvalid},
	} {
		_, err := s.StartPeriod(ctx, "llm", tc.deposit, tc.currency, "admin")
		require.ErrorIs(t, err, tc.want, "%v %q", tc.deposit, tc.currency)
	}
	_, err := s.StartPeriod(ctx, "nobody", 10, "BRL", "admin")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.Periods(ctx, "nobody")
	require.ErrorIs(t, err, ErrNotFound)

	periods, err := s.Periods(ctx, "llm")
	require.NoError(t, err)
	require.Empty(t, periods, "nothing kept of the refusals")
}

// The periods of a deleted agent are kept, out of the current ones.
func TestCurrentPeriods_LeavesTheDeletedAgentsOut(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	llm := llmAgent(t, s, "llm")
	_, err := s.StartPeriod(ctx, "llm", 10, "BRL", "admin")
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&Agent{}).Where("id = ?", llm.ID).Update("enabled", false).Error)
	require.NoError(t, s.DeleteAgent(ctx, llm.ID))

	current, err := s.CurrentPeriods(ctx)
	require.NoError(t, err)
	require.Empty(t, current)
	var kept int64
	require.NoError(t, s.db.Model(&Period{}).Where("agent_id = ?", llm.ID).Count(&kept).Error)
	require.EqualValues(t, 1, kept)
}
