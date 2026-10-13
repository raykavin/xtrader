package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raykavin/gobox/database/migrate"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/raykavin/xtrader/config"
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

// migratedTo applies the migrations of sql/migrations up to version to the
// database of dsn, as cmd/migrator did when that was the last one.
func migratedTo(t *testing.T, dsn string, version int) {
	t.Helper()
	dir := t.TempDir()
	names, err := filepath.Glob(filepath.Join("..", "sql", "migrations", "*.sql"))
	require.NoError(t, err)
	for _, name := range names {
		var number int
		_, err := fmt.Sscanf(filepath.Base(name), "%06d_", &number)
		require.NoError(t, err)
		if number > version {
			continue
		}
		data, err := os.ReadFile(name)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.Base(name)), data, 0o600))
	}
	m, err := migrate.New(migrate.MigrateConfig{DSN: dsn, Dialector: "postgres", MigrationsPath: dir})
	require.NoError(t, err)
	require.NoError(t, m.Migrate(context.Background()))
}

// The deposits of the periods in cents exactly from the migration 16 on: the
// ones kept before rounded to the cent, as numbers in JSON still; the
// migration down keeps them.
func TestMigration16_TheDepositsInCents(t *testing.T) {
	dsn := testDatabase(t)
	migratedTo(t, dsn, 15)
	s, err := Open(config.Database{DSN: dsn, MaxOpenConns: 2, LogLevel: "silent"}, testKey)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	llmAgentOfBefore(t, s, "llm")
	first, err := s.StartPeriod(ctx, "llm", 1000.005, "BRL", "admin")
	require.NoError(t, err)
	second, err := s.StartPeriod(ctx, "llm", 0.1+0.2, "BRL", "admin")
	require.NoError(t, err)
	most, err := s.StartPeriod(ctx, "llm", maxDeposit, "BRL", "admin")
	require.NoError(t, err)
	// one written otherwise, beyond the cent
	require.NoError(t, s.db.Exec("UPDATE agent_periods SET deposit = 12.345678 WHERE id = ?", second.ID).Error)
	type period struct {
		ID      int64
		Deposit string
	}
	var before []period
	require.NoError(t, s.db.Raw("SELECT id, deposit::text FROM agent_periods ORDER BY id").Scan(&before).Error)

	migrated(t, dsn)
	require.NoError(t, s.CheckSchema(ctx))
	var column struct {
		DataType         string
		NumericPrecision int
		NumericScale     int
	}
	require.NoError(t, s.db.Raw("SELECT data_type, numeric_precision, numeric_scale FROM information_schema.columns "+
		"WHERE table_name = 'agent_periods' AND column_name = 'deposit'").Scan(&column).Error)
	require.Equal(t, "numeric", column.DataType)
	require.Equal(t, 16, column.NumericPrecision)
	require.Equal(t, 2, column.NumericScale)

	periods, err := s.Periods(ctx, "llm")
	require.NoError(t, err)
	require.Len(t, periods, 3, "the periods kept: %v", before)
	deposits := map[int64]float64{}
	for _, p := range periods {
		deposits[p.ID] = p.Deposit
	}
	require.Equal(t, map[int64]float64{first.ID: 1000.01, second.ID: 12.35, most.ID: maxDeposit}, deposits)
	data, err := json.Marshal(periods[len(periods)-1])
	require.NoError(t, err)
	require.Contains(t, string(data), `"deposit":1000.01,`, "a number")
	current, err := s.CurrentPeriods(ctx)
	require.NoError(t, err)
	require.Equal(t, maxDeposit, current["llm"].Deposit)

	next, err := s.StartPeriod(ctx, "llm", 2500.555, "USD", "admin")
	require.NoError(t, err)
	periods, err = s.Periods(ctx, "llm")
	require.NoError(t, err)
	require.Equal(t, next.ID, periods[0].ID)
	require.Equal(t, 2500.56, periods[0].Deposit)
	err = s.db.Exec("UPDATE agent_periods SET deposit = -1 WHERE id = ?", next.ID).Error
	require.ErrorIs(t, err, gorm.ErrCheckConstraintViolated, "never below 0")

	down, err := os.ReadFile(filepath.Join("..", "sql", "migrations",
		"000016_alter_agent_periods_deposit_numeric.down.sql"))
	require.NoError(t, err)
	require.NoError(t, s.db.Exec(string(down)).Error)
	var after []period
	require.NoError(t, s.db.Raw("SELECT id, deposit::text FROM agent_periods ORDER BY id").Scan(&after).Error)
	require.Equal(t, []period{{first.ID, "1000.01"}, {second.ID, "12.35"}, {most.ID, "1000000000000"},
		{next.ID, "2500.56"}}, after, "down, the cents kept")
	require.True(t, strings.Contains(before[1].Deposit, "12.345678"), "beyond the cent before: %v", before)
}

// The deposits are rounded to the cent as written, half up.
func TestCents_AsWritten(t *testing.T) {
	for amount, want := range map[float64]float64{0: 0, 0.004: 0, 0.005: 0.01, 1000.005: 1000.01, 2500.555: 2500.56,
		1.115: 1.12, 0.1 + 0.2: 0.3, 12.345678: 12.35, 99.994999: 99.99, 1e-7: 0, 1e12: 1e12,
		999999999999.995: 1e12, 7: 7} {
		require.Equal(t, want, cents(amount), "%v", amount)
	}
}
