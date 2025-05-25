package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raykavin/gobox/database/migrate"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/internal/pgtest"
)

// testKey is the secrets key of the tests.
var testKey = []byte(strings.Repeat("k", 32))

// testDatabase creates an empty database of its own for the test.
func testDatabase(t *testing.T) string { return pgtest.Database(t) }

// testDSN returns the URL of the database name on the PostgreSQL of the tests.
func testDSN(t *testing.T, name string) string { return pgtest.DSN(t, name) }

// migrated applies the migrations and the seeds of sql/ to the database of
// dsn, as cmd/migrator does.
func migrated(t *testing.T, dsn string) { pgtest.Migrate(t, dsn, "..") }

// testStore opens a store on a database of its own, migrated and seeded.
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := pgtest.Migrated(t, "..")
	s, err := Open(config.Database{DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 4, ConnMaxLifetime: time.Minute,
		LogLevel: "silent"}, testKey)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCheckSchema_TellsTheMigrationsMissing(t *testing.T) {
	dsn := testDatabase(t)
	s, err := Open(config.Database{DSN: dsn, MaxOpenConns: 2, LogLevel: "silent"}, testKey)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	err = s.CheckSchema(ctx)
	require.ErrorIs(t, err, ErrSchema)
	require.ErrorContains(t, err, "run cmd/migrator up")

	migrated(t, dsn)
	require.NoError(t, s.CheckSchema(ctx))

	require.NoError(t, s.db.Exec("UPDATE schema_migrations SET dirty = TRUE").Error)
	require.ErrorContains(t, s.CheckSchema(ctx), "stopped halfway")
}

func TestOpen_RefusesAKeyOfAnotherSize(t *testing.T) {
	_, err := Open(config.Database{DSN: "postgres://localhost/x"}, []byte("short"))
	require.Error(t, err)
}

func TestMigrations_ApplyAtomicallyAndTheSeedsRunAgain(t *testing.T) {
	dsn := testDatabase(t)
	migrated(t, dsn)
	migrated(t, dsn) // nothing pending, the seeds run again without a change

	s, err := Open(config.Database{DSN: dsn, MaxOpenConns: 2, LogLevel: "silent"}, testKey)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	var prompts, versions, telegram int64
	require.NoError(t, s.db.Table("prompts").Count(&prompts).Error)
	require.NoError(t, s.db.Table("prompt_versions").Count(&versions).Error)
	require.NoError(t, s.db.Table("telegram_settings").Count(&telegram).Error)
	require.EqualValues(t, 43, prompts, "the prompts seeded")
	require.Equal(t, prompts, versions)
	require.EqualValues(t, 1, telegram)

	// a migration failing halfway leaves nothing: one transaction
	dir := t.TempDir()
	names, err := filepath.Glob(filepath.Join("..", "sql", "migrations", "*.sql"))
	require.NoError(t, err)
	for _, name := range names {
		data, err := os.ReadFile(name)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.Base(name)), data, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("%06d_broken.up.sql", SchemaVersion+1)),
		[]byte("CREATE TABLE half_done (id INT);\nSELECT * FROM no_such_table;\n"), 0o600))
	m, err := migrate.New(migrate.MigrateConfig{DSN: dsn, Dialector: "postgres", MigrationsPath: dir})
	require.NoError(t, err)
	require.Error(t, m.Migrate(context.Background()))
	var tables int64
	require.NoError(t, s.db.Raw("SELECT count(*) FROM information_schema.tables WHERE table_name = 'half_done'").
		Scan(&tables).Error)
	require.Zero(t, tables, "rolled back")
}

func TestTransaction_RollsBackAndIsJoined(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	failure := errors.New("failure")
	err := s.Transaction(ctx, func(ctx context.Context) error {
		if _, err := s.CreateUser(ctx, "first", "password-1"); err != nil {
			return err
		}
		return s.Transaction(ctx, func(ctx context.Context) error { // joined
			if _, err := s.CreateUser(ctx, "second", "password-2"); err != nil {
				return err
			}
			return failure
		})
	})
	require.ErrorIs(t, err, failure)
	count, err := s.CountUsers(ctx)
	require.NoError(t, err)
	require.Zero(t, count, "both rolled back")
}
