package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// key is a secrets key of the tests, 32 bytes in base64.
var key = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))

// write writes content to a config.yml of its own and returns its path.
func write(t *testing.T, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(file, []byte(content), 0o600))
	return file
}

func TestLoad_ExpandsTheEnvironmentAndFillsTheDefaults(t *testing.T) {
	t.Setenv("TEST_DB_PASSWORD", "p@ss")
	t.Setenv("TEST_SECRETS_KEY", key)
	file := write(t, `
database:
  dsn: postgres://xtrader:${TEST_DB_PASSWORD}@localhost:5432/xtrader?sslmode=disable
secrets_key: ${TEST_SECRETS_KEY}
agents:
  commands:
    llm: go run ./agents/llm/llmagent
  env:
    - GOCACHE=/tmp/go-cache
    - http_proxy=http://proxy:3128
services:
  - name: mt5bridge
    exec: tools/mt5bridge/run.sh
    health: http://127.0.0.1:18812/health
    restart: true
    env:
      - MT5_BRIDGE_TOKEN=${TEST_DB_PASSWORD}
`)
	cfg, err := Load(file)
	require.NoError(t, err)
	require.Equal(t, "postgres://xtrader:p@ss@localhost:5432/xtrader?sslmode=disable", cfg.Database.DSN)
	require.Equal(t, "localhost:8080", cfg.Listen)
	require.Equal(t, 10, cfg.Database.MaxOpenConns)
	require.Equal(t, "warn", cfg.Database.LogLevel)
	require.Equal(t, "sql/migrations", cfg.Migrations.Path)
	require.Equal(t, "127.0.0.1:8090", cfg.Agents.API)
	require.Equal(t, 20*time.Second, cfg.Agents.StopTimeout)
	require.Equal(t, 3*time.Minute, cfg.Services[0].Wait)
	require.True(t, cfg.Services[0].Restart)
	require.Equal(t, "go run ./agents/llm/llmagent", cfg.Agents.Command("llm"))
	require.Equal(t, filepath.Join("bin", "trendagent"), cfg.Agents.Command("trend"), "the binary of the kind")
	require.Equal(t, map[string]string{"GOCACHE": "/tmp/go-cache", "http_proxy": "http://proxy:3128"},
		cfg.Agents.Env.Map(), "the names of the variables as they are")
	require.Equal(t, map[string]string{"MT5_BRIDGE_TOKEN": "p@ss"}, cfg.Services[0].Env.Map())

	secret, err := cfg.Key()
	require.NoError(t, err)
	require.Len(t, secret, 32)
}

func TestLoad_FailsWithoutTheFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yml"))
	require.Error(t, err)
}

func TestValidate_RefusesWhatCantBeUsed(t *testing.T) {
	valid := func() Config {
		c := Config{Database: Database{DSN: "postgres://localhost/xtrader"}, SecretsKey: key}
		c.defaults()
		return c
	}
	require.NoError(t, func() error { c := valid(); return c.Validate() }())

	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"no dsn", func(c *Config) { c.Database.DSN = "" }, "database.dsn"},
		{"not postgres", func(c *Config) { c.Database.DSN = "mysql://localhost/x" }, "database.dsn"},
		{"no secrets key", func(c *Config) { c.SecretsKey = "" }, "secrets_key: required"},
		{"short secrets key", func(c *Config) { c.SecretsKey = base64.StdEncoding.EncodeToString([]byte("short")) },
			"5 bytes, 32 required"},
		{"secrets key not base64", func(c *Config) { c.SecretsKey = "not base64!" }, "not standard base64"},
		{"internal api off the loopback", func(c *Config) { c.Agents.API = "0.0.0.0:8090" }, "loopback only"},
		{"unknown kind", func(c *Config) { c.Agents.Commands = map[string]string{"grid": "x"} }, `unknown kind "grid"`},
		{"reserved variable", func(c *Config) { c.Agents.Env = Env{"XTRADER_AGENT=x"} },
			"set by the controller"},
		{"variable without a value", func(c *Config) { c.Agents.Env = Env{"GOCACHE"} }, "isn't NAME=value"},
		{"variable of a service", func(c *Config) { c.Services = []Service{{Name: "x", Exec: "y", Env: Env{"a b=c"}}} },
			"service x: env"},
		{"service without exec", func(c *Config) { c.Services = []Service{{Name: "x"}} }, "name and exec"},
		{"service health", func(c *Config) { c.Services = []Service{{Name: "x", Exec: "y", Health: "tcp://z"}} },
			"not an http(s) URL"},
		{"bootstrap half", func(c *Config) { c.Bootstrap.Username = "admin" }, "go together"},
		{"log level", func(c *Config) { c.Database.LogLevel = "info" }, "silent, error or warn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mutate(&c)
			err := c.Validate()
			require.ErrorIs(t, err, ErrInvalid)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestValidate_KeepsTheSecretsOutOfTheErrors(t *testing.T) {
	c := Config{Database: Database{DSN: "::bad"}, SecretsKey: "c2VjcmV0"}
	c.defaults()
	err := c.Validate()
	require.Error(t, err)
	require.NotContains(t, err.Error(), "c2VjcmV0")
}

// The configurations shipped load and pass, their secrets from the
// environment: the example of the host, and the ones of the containers.
func TestLoad_TheConfigurationsShipped(t *testing.T) {
	t.Setenv("XTRADER_DB_PASSWORD", "0123abcd")
	t.Setenv("XTRADER_SECRETS_KEY", key)
	t.Setenv("XTRADER_BOOTSTRAP_USERNAME", "")
	t.Setenv("XTRADER_BOOTSTRAP_PASSWORD", "")
	t.Setenv("MT5_BRIDGE_TOKEN", "bridge")
	for _, file := range []string{
		filepath.Join("..", "configs", "config.example.yml"),
		filepath.Join("..", "deploy", "docker", "services", "controller", "config", "config.yml"),
		filepath.Join("..", "deploy", "docker", "services", "migrator", "config", "config.yml"),
	} {
		t.Run(file, func(t *testing.T) {
			cfg, err := Load(file)
			require.NoError(t, err)
			require.Contains(t, cfg.Database.DSN, ":0123abcd@")
			_, err = cfg.Key()
			require.NoError(t, err)
			for kind := range cfg.Agents.Commands {
				require.Contains(t, Kinds, kind)
			}
		})
	}
	cfg, err := Load(filepath.Join("..", "configs", "config.example.yml"))
	require.NoError(t, err)
	require.Len(t, cfg.Services, 1)
	require.Equal(t, "bridge", cfg.Services[0].Env.Map()["MT5_BRIDGE_TOKEN"], "the token of the bridge to its service")
	require.Len(t, cfg.Agents.Commands, len(Kinds), "a command of each kind")
}

// The ${VAR} of the configurations shipped take a value of any character but
// ', eg: a password starting with @, which the YAML refuses unquoted, or with
// " #", which it takes for a comment: the text is expanded before it is read.
func TestLoad_TheConfigurationsShippedTakeAnyValue(t *testing.T) {
	const password = "@p#ss w: \"x\" \\ %`&*!|>{[-"
	t.Setenv("XTRADER_DB_PASSWORD", "0123abcd")
	t.Setenv("XTRADER_SECRETS_KEY", key)
	t.Setenv("XTRADER_BOOTSTRAP_USERNAME", "`admin #1")
	t.Setenv("XTRADER_BOOTSTRAP_PASSWORD", password)
	t.Setenv("MT5_BRIDGE_TOKEN", "%tok #en")
	for _, file := range []string{
		filepath.Join("..", "configs", "config.example.yml"),
		filepath.Join("..", "deploy", "docker", "services", "controller", "config", "config.yml"),
	} {
		t.Run(file, func(t *testing.T) {
			cfg, err := Load(file)
			require.NoError(t, err)
			require.Equal(t, "`admin #1", cfg.Bootstrap.Username)
			require.Equal(t, password, cfg.Bootstrap.Password)
		})
	}
	cfg, err := Load(filepath.Join("..", "configs", "config.example.yml"))
	require.NoError(t, err)
	require.Equal(t, "%tok #en", cfg.Services[0].Env.Map()["MT5_BRIDGE_TOKEN"])
}
