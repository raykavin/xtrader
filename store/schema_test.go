package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// migration is a migration of sql/migrations: its number, name and up and down
// statements, without their comments.
type migration struct {
	number   int
	name     string
	up, down []string
}

// sqlComment matches a comment of a line of SQL.
var sqlComment = regexp.MustCompile(`--[^\n]*`)

// statements returns the statements of the SQL of file, without their
// comments.
func statements(t *testing.T, file string) []string {
	t.Helper()
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	var list []string
	for _, statement := range strings.Split(sqlComment.ReplaceAllString(string(data), ""), ";") {
		if statement = strings.Join(strings.Fields(statement), " "); statement != "" {
			list = append(list, statement)
		}
	}
	return list
}

// migrations returns the migrations of sql/migrations, in their order.
func migrations(t *testing.T) []migration {
	t.Helper()
	ups, err := filepath.Glob(filepath.Join("..", "sql", "migrations", "*.up.sql"))
	require.NoError(t, err)
	var list []migration
	for _, up := range ups {
		var m migration
		base := filepath.Base(up)
		_, err := fmt.Sscanf(base, "%06d_", &m.number)
		require.NoError(t, err, base)
		m.name = strings.TrimSuffix(base[7:], ".up.sql")
		m.up = statements(t, up)
		m.down = statements(t, strings.TrimSuffix(up, ".up.sql")+".down.sql")
		list = append(list, m)
	}
	return list
}

var (
	createTable = regexp.MustCompile(`^CREATE TABLE IF NOT EXISTS ([a-z0-9_]+) \(`)
	createIndex = regexp.MustCompile(`^CREATE (UNIQUE )?INDEX IF NOT EXISTS [a-z0-9_]+ ON ([a-z0-9_]+) `)
	references  = regexp.MustCompile(`REFERENCES ([a-z0-9_]+) \(`)
)

// The schema of a new database: each migration creates a table whole, its
// columns, keys and checks in its CREATE TABLE and its indexes after it, never
// altering one, nor inserting a row (the seeds do); in the order of their
// dependencies, a table referenced created before; numbered from 1 to
// SchemaVersion, each down dropping its table.
func TestMigrations_EachCreatesATableWhole(t *testing.T) {
	list := migrations(t)
	require.Len(t, list, SchemaVersion, "the migrations of the version the store needs")
	created := map[string]bool{}
	for i, m := range list {
		require.Equal(t, i+1, m.number, "numbered in their order: %s", m.name)
		require.NotEmpty(t, m.up, m.name)
		table := createTable.FindStringSubmatch(m.up[0])
		require.NotNil(t, table, "%s: a CREATE TABLE first: %s", m.name, m.up[0])
		require.Equal(t, "create_"+table[1], m.name, "named after its table")
		for _, statement := range m.up[1:] {
			index := createIndex.FindStringSubmatch(statement)
			require.NotNil(t, index, "%s: only the indexes of its table after it: %s", m.name, statement)
			require.Equal(t, table[1], index[2], m.name)
		}
		for _, referenced := range references.FindAllStringSubmatch(m.up[0], -1) {
			require.True(t, created[referenced[1]] || referenced[1] == table[1],
				"%s: %s created before it", m.name, referenced[1])
		}
		created[table[1]] = true
		require.Equal(t, []string{"DROP TABLE IF EXISTS " + table[1]}, m.down, m.name)
	}
}

// The migrations down drop the whole schema, the seeds' rows with it, and up
// again create it, the tables the store needs.
func TestMigrations_DownAndUp(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	tables := func() []string {
		var names []string
		require.NoError(t, s.db.Raw("SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' "+
			"AND table_name <> 'schema_migrations' ORDER BY table_name").Scan(&names).Error)
		return names
	}
	before := tables()
	require.Len(t, before, SchemaVersion)

	// the files as the migrator sends them, a query each, never parsed by gorm
	sqlDB, err := s.db.DB()
	require.NoError(t, err)
	run := func(m migration, direction string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "sql", "migrations",
			fmt.Sprintf("%06d_%s.%s.sql", m.number, m.name, direction)))
		require.NoError(t, err)
		_, err = sqlDB.ExecContext(ctx, string(data))
		require.NoError(t, err, "%s %s", m.name, direction)
	}
	list := migrations(t)
	for i := len(list) - 1; i >= 0; i-- {
		run(list[i], "down")
	}
	require.Empty(t, tables(), "every table dropped")
	for _, m := range list {
		run(m, "up")
	}
	require.Equal(t, before, tables())
	require.NoError(t, s.CheckSchema(ctx))
	agents, err := s.Agents(ctx)
	require.NoError(t, err)
	require.Empty(t, agents)
}
