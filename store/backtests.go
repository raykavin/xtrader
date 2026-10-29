package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The statuses of a run of a backtest, see BacktestRun.
const (
	BacktestQueued          = "queued"
	BacktestRunning         = "running"
	BacktestSucceeded       = "succeeded"
	BacktestFailed          = "failed"
	BacktestCanceled        = "canceled"
	BacktestInterrupted     = "interrupted"
	BacktestBudgetExhausted = "budget_exhausted"
)

// The segments of the period of a batch a run trades, see BacktestRun.
const (
	SegmentFit        = "fit"
	SegmentValidation = "validation"
)

// ErrBacktestBusy is the error of a batch deleted while some of its runs are
// queued or running.
var ErrBacktestBusy = errors.New("store: the batch has runs queued or running")

// BacktestBatch is a batch of backtests asked on the dashboard: its period
// (From until before To), the share of it kept for the validation, its
// assumptions, who asked and when. See backtest_batches.
type BacktestBatch struct {
	ID              int64           `json:"id"`
	Name            string          `json:"name"`
	From            time.Time       `json:"from"`
	To              time.Time       `json:"to"`
	ValidationShare float64         `json:"validation_share"`
	Assumptions     json.RawMessage `json:"assumptions"`
	CreatedBy       string          `json:"created_by"`
	CreatedAt       time.Time       `json:"created_at"`
}

// BacktestRun is a run of a batch: an agent on a segment of the period, its
// status, progress and error, the config it ran with (copied), its summary
// and its result. See backtest_runs.
type BacktestRun struct {
	ID         int64           `json:"id"`
	BatchID    int64           `json:"batch_id"`
	Agent      string          `json:"agent"`
	Kind       string          `json:"kind"`
	Segment    string          `json:"segment"`
	From       time.Time       `json:"from"`
	To         time.Time       `json:"to"`
	Status     string          `json:"status"`
	Processed  int64           `json:"processed"`
	Total      int64           `json:"total"`
	Error      string          `json:"error"`
	Config     json.RawMessage `json:"config"`
	Summary    json.RawMessage `json:"summary,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

// Final reports whether the run ended, whatever its outcome.
func (r BacktestRun) Final() bool {
	return r.Status != BacktestQueued && r.Status != BacktestRunning
}

type batchRow struct {
	ID              int64 `gorm:"primaryKey"`
	Name            string
	PeriodFrom      time.Time
	PeriodTo        time.Time
	ValidationShare float64
	Assumptions     jsonb
	CreatedBy       string
	CreatedAt       time.Time
}

func (batchRow) TableName() string { return "backtest_batches" }

func (r batchRow) batch() BacktestBatch {
	return BacktestBatch{ID: r.ID, Name: r.Name, From: r.PeriodFrom, To: r.PeriodTo, ValidationShare: r.ValidationShare,
		Assumptions: json.RawMessage(r.Assumptions), CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt}
}

type backtestRunRow struct {
	ID         int64 `gorm:"primaryKey"`
	BatchID    int64
	Agent      string
	Kind       string
	Segment    string
	PeriodFrom time.Time
	PeriodTo   time.Time
	Status     string
	Processed  int64
	Total      int64
	Error      string
	Config     jsonb
	Summary    jsonb
	Result     jsonb
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
}

func (backtestRunRow) TableName() string { return "backtest_runs" }

func (r backtestRunRow) run() BacktestRun {
	return BacktestRun{ID: r.ID, BatchID: r.BatchID, Agent: r.Agent, Kind: r.Kind, Segment: r.Segment,
		From: r.PeriodFrom, To: r.PeriodTo, Status: r.Status, Processed: r.Processed, Total: r.Total, Error: r.Error,
		Config: json.RawMessage(r.Config), Summary: json.RawMessage(r.Summary), Result: json.RawMessage(r.Result),
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt}
}

// CreateBacktestBatch records batch and its runs, queued, at once.
func (s *Store) CreateBacktestBatch(ctx context.Context, batch BacktestBatch, runs []BacktestRun) (BacktestBatch,
	[]BacktestRun, error) {
	if len(runs) == 0 {
		return BacktestBatch{}, nil, fmt.Errorf("%w: a batch without runs", ErrInvalid)
	}
	row := batchRow{Name: batch.Name, PeriodFrom: batch.From, PeriodTo: batch.To,
		ValidationShare: batch.ValidationShare, Assumptions: jsonb(batch.Assumptions), CreatedBy: batch.CreatedBy,
		CreatedAt: time.Now()}
	var created []BacktestRun
	err := s.Transaction(ctx, func(ctx context.Context) error {
		if err := s.conn(ctx).Create(&row).Error; err != nil {
			return notFound(err, "the batch of backtests")
		}
		for _, run := range runs {
			r := backtestRunRow{BatchID: row.ID, Agent: run.Agent, Kind: run.Kind, Segment: run.Segment,
				PeriodFrom: run.From, PeriodTo: run.To, Status: BacktestQueued, Config: jsonb(run.Config),
				CreatedAt: row.CreatedAt}
			if err := s.conn(ctx).Create(&r).Error; err != nil {
				return notFound(err, "a run of backtest")
			}
			created = append(created, r.run())
		}
		return nil
	})
	if err != nil {
		return BacktestBatch{}, nil, err
	}
	return row.batch(), created, nil
}

// BacktestBatches returns the last limit batches, the newest first.
func (s *Store) BacktestBatches(ctx context.Context, limit int) ([]BacktestBatch, error) {
	var rows []batchRow
	if err := s.conn(ctx).Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	batches := make([]BacktestBatch, 0, len(rows))
	for _, row := range rows {
		batches = append(batches, row.batch())
	}
	return batches, nil
}

// BacktestBatch returns the batch of id, ErrNotFound without one.
func (s *Store) BacktestBatch(ctx context.Context, id int64) (BacktestBatch, error) {
	var row batchRow
	if err := s.conn(ctx).Take(&row, id).Error; err != nil {
		return BacktestBatch{}, notFound(err, "the batch of backtests")
	}
	return row.batch(), nil
}

// BacktestRuns returns the runs of the batches of ids, by batch and ID, their
// result left out unless withResult.
func (s *Store) BacktestRuns(ctx context.Context, ids []int64, withResult bool) ([]BacktestRun, error) {
	var rows []backtestRunRow
	q := s.conn(ctx).Where("batch_id IN ?", ids).Order("batch_id DESC, id")
	if !withResult {
		q = q.Omit("result")
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	runs := make([]BacktestRun, 0, len(rows))
	for _, row := range rows {
		runs = append(runs, row.run())
	}
	return runs, nil
}

// BacktestRun returns the run of id with its result, ErrNotFound without one.
func (s *Store) BacktestRun(ctx context.Context, id int64) (BacktestRun, error) {
	var row backtestRunRow
	if err := s.conn(ctx).Take(&row, id).Error; err != nil {
		return BacktestRun{}, notFound(err, "the run of backtest")
	}
	return row.run(), nil
}

// ClaimBacktestRun takes the oldest run queued, running from now, false
// without one; a run is claimed once whatever the controllers.
func (s *Store) ClaimBacktestRun(ctx context.Context) (BacktestRun, bool, error) {
	var claimed BacktestRun
	found := false
	err := s.Transaction(ctx, func(ctx context.Context) error {
		var row backtestRunRow
		err := s.conn(ctx).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Omit("result").
			Where("status = ?", BacktestQueued).Order("id").Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		now := time.Now()
		if err := s.conn(ctx).Model(&backtestRunRow{}).Where("id = ?", row.ID).
			Updates(map[string]any{"status": BacktestRunning, "started_at": now}).Error; err != nil {
			return err
		}
		row.Status, row.StartedAt = BacktestRunning, &now
		claimed, found = row.run(), true
		return nil
	})
	return claimed, found, err
}

// ProgressBacktestRun records the progress of the run of id, running.
func (s *Store) ProgressBacktestRun(ctx context.Context, id, processed, total int64) error {
	return s.conn(ctx).Model(&backtestRunRow{}).Where("id = ? AND status = ?", id, BacktestRunning).
		Updates(map[string]any{"processed": processed, "total": total}).Error
}

// FinishBacktestRun records how the run of id ended: its status, error,
// summary and result.
func (s *Store) FinishBacktestRun(ctx context.Context, id int64, status, why string, summary,
	result json.RawMessage) error {
	return s.conn(ctx).Model(&backtestRunRow{}).Where("id = ?", id).Updates(map[string]any{"status": status,
		"error": why, "summary": jsonb(summary), "result": jsonb(result), "finished_at": time.Now()}).Error
}

// CancelQueuedBacktestRun cancels the run of id while queued, false when it
// isn't (running, or ended).
func (s *Store) CancelQueuedBacktestRun(ctx context.Context, id int64) (bool, error) {
	result := s.conn(ctx).Model(&backtestRunRow{}).Where("id = ? AND status = ?", id, BacktestQueued).
		Updates(map[string]any{"status": BacktestCanceled, "finished_at": time.Now(),
			"error": "cancelado antes de começar"})
	return result.RowsAffected > 0, result.Error
}

// InterruptRunningBacktestRuns ends the runs left running, interrupted for
// why: the ones of a controller stopped in their middle.
func (s *Store) InterruptRunningBacktestRuns(ctx context.Context, why string) (int64, error) {
	result := s.conn(ctx).Model(&backtestRunRow{}).Where("status = ?", BacktestRunning).
		Updates(map[string]any{"status": BacktestInterrupted, "finished_at": time.Now(), "error": why})
	return result.RowsAffected, result.Error
}

// DeleteBacktestBatch deletes the batch of id and its runs, ErrBacktestBusy
// while some are queued or running, ErrNotFound without it.
func (s *Store) DeleteBacktestBatch(ctx context.Context, id int64) error {
	return s.Transaction(ctx, func(ctx context.Context) error {
		var row batchRow
		if err := s.conn(ctx).Clauses(lockForUpdate()).Take(&row, id).Error; err != nil {
			return notFound(err, "the batch of backtests")
		}
		var busy int64
		if err := s.conn(ctx).Model(&backtestRunRow{}).Where("batch_id = ? AND status IN ?", id,
			[]string{BacktestQueued, BacktestRunning}).Count(&busy).Error; err != nil {
			return err
		}
		if busy > 0 {
			return ErrBacktestBusy
		}
		return s.conn(ctx).Delete(&batchRow{}, id).Error
	})
}
