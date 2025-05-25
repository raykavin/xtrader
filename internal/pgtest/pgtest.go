// Package pgtest gives the tests a PostgreSQL database of their own, on the
// server of XTRADER_TEST_DATABASE_URL, eg: a container of postgres:18.6: the
// tests are skipped without it.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/raykavin/gobox/database/migrate"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	glog "gorm.io/gorm/logger"
)

// Env is the variable of the URL of the server of the tests, of a user
// allowed to create databases.
const Env = "XTRADER_TEST_DATABASE_URL"

// Database creates an empty database for the test, dropped at its end, and
// returns its URL.
func Database(t *testing.T) string {
	t.Helper()
	admin := os.Getenv(Env)
	if admin == "" {
		t.Skip(Env + ": no PostgreSQL to test against")
	}
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	name := "xtrader_test_" + hex.EncodeToString(suffix)

	server, err := gorm.Open(postgres.Open(admin), &gorm.Config{Logger: glog.Discard})
	require.NoError(t, err)
	require.NoError(t, server.Exec("CREATE DATABASE "+name).Error)
	t.Cleanup(func() {
		// the migrator of gobox keeps its connections open: forced out
		_ = server.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)").Error
		if db, err := server.DB(); err == nil {
			_ = db.Close()
		}
	})
	return DSN(t, name)
}

// DSN returns the URL of the database name on the server of the tests.
func DSN(t *testing.T, name string) string {
	t.Helper()
	u, err := url.Parse(os.Getenv(Env))
	require.NoError(t, err)
	u.Path = "/" + name
	return u.String()
}

// Migrated creates a database for the test with the migrations and the seeds
// of the sql directory of the repository, root its path from the test, eg:
// "..", as cmd/migrator does, and returns its URL.
func Migrated(t *testing.T, root string) string {
	t.Helper()
	dsn := Database(t)
	Migrate(t, dsn, root)
	return dsn
}

// Migrate applies the migrations and the seeds of the repository at root to
// the database of dsn.
func Migrate(t *testing.T, dsn, root string) {
	t.Helper()
	m, err := migrate.New(migrate.MigrateConfig{DSN: dsn, Dialector: "postgres",
		MigrationsPath: filepath.Join(root, "sql", "migrations"), PopulationPath: filepath.Join(root, "sql", "seeds")})
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, m.Migrate(ctx))
	require.NoError(t, m.Populate(ctx))
}
