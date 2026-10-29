package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The batches of backtests and their runs: created queued, claimed once each
// in their order, their progress and their end recorded; a queued run
// canceled, the running ones interrupted by a restart; a batch deleted once
// none of its runs is queued or running.
func TestBacktests(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(30 * 24 * time.Hour)
	config := json.RawMessage(`{"params":{"pair":"XAUUSDm"}}`)
	batch, runs, err := s.CreateBacktestBatch(ctx, BacktestBatch{Name: "teste", From: from, To: to,
		ValidationShare: 0.3, Assumptions: json.RawMessage(`{"balance":10000}`), CreatedBy: "admin"},
		[]BacktestRun{
			{Agent: "matrend", Kind: "trend", Segment: SegmentFit, From: from, To: to.Add(-9 * 24 * time.Hour),
				Config: config},
			{Agent: "matrend", Kind: "trend", Segment: SegmentValidation, From: to.Add(-9 * 24 * time.Hour), To: to,
				Config: config},
		})
	require.NoError(t, err)
	require.Len(t, runs, 2)
	require.Equal(t, BacktestQueued, runs[0].Status)

	first, ok, err := s.ClaimBacktestRun(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, runs[0].ID, first.ID, "the oldest first")
	require.Equal(t, BacktestRunning, first.Status)
	require.NoError(t, s.ProgressBacktestRun(ctx, first.ID, 50, 100))

	// the second canceled while queued: nothing left to claim
	canceled, err := s.CancelQueuedBacktestRun(ctx, runs[1].ID)
	require.NoError(t, err)
	require.True(t, canceled)
	_, ok, err = s.ClaimBacktestRun(ctx)
	require.NoError(t, err)
	require.False(t, ok)
	canceled, err = s.CancelQueuedBacktestRun(ctx, first.ID)
	require.NoError(t, err)
	require.False(t, canceled, "running: the controller stops its process")

	require.ErrorIs(t, s.DeleteBacktestBatch(ctx, batch.ID), ErrBacktestBusy)
	n, err := s.InterruptRunningBacktestRuns(ctx, "o controller parou")
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	got, err := s.BacktestRun(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, BacktestInterrupted, got.Status)
	require.Equal(t, int64(50), got.Processed)
	require.NotNil(t, got.FinishedAt)

	require.NoError(t, s.FinishBacktestRun(ctx, first.ID, BacktestSucceeded, "", json.RawMessage(`{"trades":3}`),
		json.RawMessage(`{"trades":[]}`)))
	listed, err := s.BacktestRuns(ctx, []int64{batch.ID}, false)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Nil(t, listed[0].Result, "the results left out of the list")
	require.JSONEq(t, `{"trades":3}`, string(listed[0].Summary))
	batches, err := s.BacktestBatches(ctx, 10)
	require.NoError(t, err)
	require.Len(t, batches, 1)
	require.Equal(t, 0.3, batches[0].ValidationShare)

	require.NoError(t, s.DeleteBacktestBatch(ctx, batch.ID))
	_, err = s.BacktestRun(ctx, first.ID)
	require.ErrorIs(t, err, ErrNotFound, "its runs gone with it")

	// refused as the database checks them
	_, _, err = s.CreateBacktestBatch(ctx, BacktestBatch{From: to, To: from, Assumptions: json.RawMessage(`{}`)},
		[]BacktestRun{{Agent: "a", Kind: "trend", Segment: SegmentFit, From: from, To: to, Config: config}})
	require.ErrorIs(t, err, ErrInvalid)
}
