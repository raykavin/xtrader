package config

import (
	"encoding/base64"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
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
	require.Equal(t, 10*time.Second, cfg.Services[0].StopTimeout)
	require.Equal(t, 40*time.Second, cfg.ShutdownTimeout, "the agents, then the services, and a margin")
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
		{"trusted proxy", func(c *Config) { c.UI.TrustedProxies = []string{"127.0.0.1", "proxy.local"} },
			`"proxy.local" is neither an IP nor a CIDR`},
		{"every address trusted", func(c *Config) { c.UI.TrustedProxies = []string{"0.0.0.0/0"} },
			"trusts every address"},
		{"stop of a service", func(c *Config) { c.Services = []Service{{Name: "x", Exec: "y", StopTimeout: -1}} },
			"service x: stop_timeout -1ns is negative"},
		{"stop of the agents", func(c *Config) { c.Agents.StopTimeout = -time.Second },
			"agents.stop_timeout -1s is negative"},
		{"shutdown under the stops", func(c *Config) {
			c.Services = []Service{{Name: "x", Exec: "y", StopTimeout: 10 * time.Second}}
			c.ShutdownTimeout = 31 * time.Second
		}, "shutdown_timeout 31s: under the stops of the agents and of the services"},
		// R9: each phase, the agents then the services, may wait OutputWait for
		// the output of a process killed beyond its stop_timeout
		{"shutdown without the wait of the output", func(c *Config) {
			c.Services = []Service{{Name: "x", Exec: "y", StopTimeout: 10 * time.Second}}
			c.ShutdownTimeout = 32 * time.Second
		}, "shutdown_timeout 32s: under the stops of the agents and of the services, agents.stop_timeout plus " +
			"the longest stop_timeout of the services, plus 3s (33s)"},
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

// The whole stop of the controller fits the time of who stops it: the agents,
// then the services, each with its own stop timeout (R9).
func TestLoad_TheTimesOfTheStop(t *testing.T) {
	t.Setenv("TEST_SECRETS_KEY", key)
	base := `
database:
  dsn: postgres://localhost/xtrader
secrets_key: ${TEST_SECRETS_KEY}
`
	cfg, err := Load(write(t, base))
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, cfg.ShutdownTimeout, "the agents, without services, and a margin")

	cfg, err = Load(write(t, base+`
agents:
  stop_timeout: 30s
services:
  - name: mt5bridge
    exec: tools/mt5bridge/run.sh
    stop_timeout: 5s
  - name: other
    exec: other
    stop_timeout: 15s
shutdown_timeout: 50s
`))
	require.NoError(t, err)
	require.Equal(t, 5*time.Second, cfg.Services[0].StopTimeout)
	require.Equal(t, 45*time.Second, cfg.StopTimeouts(), "the agents, then the longest of the services")
	require.Equal(t, 50*time.Second, cfg.ShutdownTimeout)

	cfg, err = Load(write(t, base+`
agents:
  stop_timeout: 1m
`))
	require.NoError(t, err)
	require.Equal(t, 70*time.Second, cfg.ShutdownTimeout, "a longer stop of the agents, a longer one")

	_, err = Load(write(t, base+`
shutdown_timeout: 20s
`))
	require.ErrorIs(t, err, ErrInvalid)
	require.ErrorContains(t, err, "shutdown_timeout 20s: under the stops of the agents and of the services")

	// the least: the stops, and the wait of the output of each phase, plus a
	// second
	_, err = Load(write(t, base+`
shutdown_timeout: 22s
`))
	require.ErrorContains(t, err, "shutdown_timeout 22s: under the stops")
	cfg, err = Load(write(t, base+`
shutdown_timeout: 23s
`))
	require.NoError(t, err)
	require.Equal(t, 23*time.Second, cfg.ShutdownTimeout)
}

func TestValidate_KeepsTheSecretsOutOfTheErrors(t *testing.T) {
	c := Config{Database: Database{DSN: "::bad"}, SecretsKey: "c2VjcmV0"}
	c.defaults()
	err := c.Validate()
	require.Error(t, err)
	require.NotContains(t, err.Error(), "c2VjcmV0")
}

// The proxies trusted with X-Forwarded-For and X-Forwarded-Proto are the
// loopback by default, else the IPs and the CIDRs of ui.trusted_proxies.
func TestUI_TrustedProxies(t *testing.T) {
	c := Config{}
	c.defaults()
	proxies, err := c.UI.Proxies()
	require.NoError(t, err)
	require.Equal(t, []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}, proxies)

	proxies, err = UI{TrustedProxies: []string{" 172.16.0.0/12 ", "10.1.2.3", "2001:db8::1/64", "::1"}}.Proxies()
	require.NoError(t, err)
	require.Equal(t, []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("10.1.2.3/32"),
		netip.MustParsePrefix("2001:db8::/64"), netip.MustParsePrefix("::1/128")}, proxies)

	for _, entry := range []string{"", "localhost", "10.0.0.0/33", "::/0"} {
		_, err := UI{TrustedProxies: []string{entry}}.Proxies()
		require.Error(t, err, entry)
	}
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
	require.Equal(t, defaultTrustedProxies, cfg.UI.TrustedProxies, "ngrok on the loopback of the host")

	// in the container, the published port reaches the controller from the
	// gateway of the network of the compose: the private range of Docker
	cfg, err = Load(filepath.Join("..", "deploy", "docker", "services", "controller", "config", "config.yml"))
	require.NoError(t, err)
	proxies, err := cfg.UI.Proxies()
	require.NoError(t, err)
	gateway := netip.MustParseAddr("172.18.0.1")
	require.True(t, slices.ContainsFunc(proxies, func(p netip.Prefix) bool { return p.Contains(gateway) }), proxies)
	for _, lan := range []string{"10.12.0.5", "192.168.0.10", "203.0.113.7"} {
		addr := netip.MustParseAddr(lan)
		require.False(t, slices.ContainsFunc(proxies, func(p netip.Prefix) bool { return p.Contains(addr) }), lan)
	}
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

// unsetenv unsets name until the end of the test.
func unsetenv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	require.NoError(t, os.Unsetenv(name))
}

// A ${VAR} without a value would be expanded to nothing: the controller
// refuses to start without the ones it can't do without, the URL of the
// database, the key of the secrets and an entry of the env of a service left
// empty, eg: the bridge of MT5 without its token; it warns of any other unset.
func TestLoad_UnsetVariables(t *testing.T) {
	const config = `
database:
  dsn: 'postgres://xtrader:${TEST_DB_PASSWORD}@localhost:5432/xtrader?sslmode=disable'
secrets_key: '${TEST_SECRETS_KEY}'
bootstrap:
  username: '${TEST_BOOTSTRAP_USERNAME}'
  password: '${TEST_BOOTSTRAP_PASSWORD}'
ui:
  dir: '${TEST_UI_DIR}'
  # version: ${TEST_IN_A_COMMENT}
services:
  - name: mt5bridge
    exec: tools/mt5bridge/run.sh
    env:
      - 'MT5_BRIDGE_TOKEN=${TEST_BRIDGE_TOKEN}'
      - 'MT5_DIR=/opt/${TEST_BRIDGE_DIR}'
`
	set := func(t *testing.T) {
		t.Setenv("TEST_DB_PASSWORD", "p@ss")
		t.Setenv("TEST_SECRETS_KEY", key)
		t.Setenv("TEST_BRIDGE_TOKEN", "bridge")
		t.Setenv("TEST_BRIDGE_DIR", "mt5")
		t.Setenv("TEST_UI_DIR", "web/dist")
		for _, name := range []string{"TEST_BOOTSTRAP_USERNAME", "TEST_BOOTSTRAP_PASSWORD", "TEST_IN_A_COMMENT"} {
			unsetenv(t, name)
		}
	}

	t.Run("every one set", func(t *testing.T) {
		set(t)
		cfg, err := Load(write(t, config))
		require.NoError(t, err)
		require.Empty(t, cfg.Warnings, "the bootstrap removed once in, the comments none of the settings")
	})

	for _, tc := range []struct {
		name, want string
	}{
		{"TEST_DB_PASSWORD", "database.dsn: ${TEST_DB_PASSWORD} isn't set"},
		{"TEST_SECRETS_KEY", "secrets_key: ${TEST_SECRETS_KEY} isn't set"},
		{"TEST_BRIDGE_TOKEN", "service mt5bridge: env MT5_BRIDGE_TOKEN: ${TEST_BRIDGE_TOKEN} isn't set"},
	} {
		t.Run(tc.name+" unset", func(t *testing.T) {
			set(t)
			unsetenv(t, tc.name)
			_, err := Load(write(t, config))
			require.ErrorIs(t, err, ErrInvalid)
			require.ErrorContains(t, err, tc.want)
		})
		t.Run(tc.name+" empty", func(t *testing.T) {
			set(t)
			t.Setenv(tc.name, "")
			_, err := Load(write(t, config))
			require.ErrorIs(t, err, ErrInvalid)
			require.ErrorContains(t, err, strings.Replace(tc.want, "isn't set", "is empty", 1))
		})
	}

	t.Run("the optional ones unset", func(t *testing.T) {
		set(t)
		unsetenv(t, "TEST_BRIDGE_DIR")
		unsetenv(t, "TEST_UI_DIR")
		cfg, err := Load(write(t, config))
		require.NoError(t, err)
		require.Equal(t, []string{
			"service mt5bridge: env MT5_DIR: ${TEST_BRIDGE_DIR} isn't set, taken as empty",
			"ui.dir: ${TEST_UI_DIR} isn't set, taken as empty",
		}, cfg.Warnings)
		require.Equal(t, "/opt/", cfg.Services[0].Env.Map()["MT5_DIR"])
	})

	t.Run("the optional ones empty", func(t *testing.T) {
		set(t)
		t.Setenv("TEST_UI_DIR", "")
		cfg, err := Load(write(t, config))
		require.NoError(t, err)
		require.Empty(t, cfg.Warnings, "set on purpose")
	})

	t.Run("the secrets out of the error", func(t *testing.T) {
		set(t)
		unsetenv(t, "TEST_BRIDGE_TOKEN")
		_, err := Load(write(t, config))
		require.Error(t, err)
		require.NotContains(t, err.Error(), "p@ss")
		require.NotContains(t, err.Error(), key)
	})
}

// The token of the bridge of MT5 on the host is required by the example: the
// bridge accepts any process of the host without one.
func TestLoad_TheExampleRequiresTheTokenOfTheBridge(t *testing.T) {
	t.Setenv("XTRADER_DB_PASSWORD", "0123abcd")
	t.Setenv("XTRADER_SECRETS_KEY", key)
	unsetenv(t, "XTRADER_BOOTSTRAP_USERNAME")
	unsetenv(t, "XTRADER_BOOTSTRAP_PASSWORD")
	unsetenv(t, "MT5_BRIDGE_TOKEN")
	_, err := Load(filepath.Join("..", "configs", "config.example.yml"))
	require.ErrorContains(t, err, "service mt5bridge: env MT5_BRIDGE_TOKEN: ${MT5_BRIDGE_TOKEN} isn't set")

	// the containers have no service, their bootstrap removed once in
	for _, file := range []string{
		filepath.Join("..", "deploy", "docker", "services", "controller", "config", "config.yml"),
		filepath.Join("..", "deploy", "docker", "services", "migrator", "config", "config.yml"),
	} {
		cfg, err := Load(file)
		require.NoError(t, err, file)
		require.Empty(t, cfg.Warnings, file)
	}
}
