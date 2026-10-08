// Package config is config.yml, the configuration of the controller
// (cmd/controller) and of the migrations (cmd/migrator): what they need to
// start and to reach PostgreSQL. Every other setting, the agents, the users,
// Telegram and the prompts, is in the database.
//
// The file is read once, at the start, with the ${VAR} of the environment
// expanded anywhere in it, eg: the password of the database and the key of the
// secrets, which stay out of the file (see configs/config.example.yml). The
// text is expanded before the YAML is read (gobox/config), so a ${VAR} stays
// between single quotes, eg: a password starting with @ or holding " #".
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	gobox "github.com/raykavin/gobox/config"
)

// Kinds are the kinds of agents, of the binaries the controller launches.
var Kinds = []string{"llm", "trend", "momentum", "breakout", "intraday"}

// ErrInvalid is the error of a configuration that can't be used.
var ErrInvalid = errors.New("config: invalid")

// Config is the configuration of config.yml.
type Config struct {
	// Listen is the address of the web dashboard, eg: 0.0.0.0:8080.
	Listen     string     `mapstructure:"listen"`
	Database   Database   `mapstructure:"database"`
	Migrations Migrations `mapstructure:"migrations"`

	// SecretsKey is the key of the secrets kept in the database, the API keys
	// and the tokens, encrypted with AES-256-GCM: 32 bytes in standard base64,
	// eg: from `head -c 32 /dev/urandom | base64`. Lost, the secrets have to be
	// set again.
	SecretsKey string `mapstructure:"secrets_key"`

	Bootstrap Bootstrap `mapstructure:"bootstrap"`
	UI        UI        `mapstructure:"ui"`
	Agents    Agents    `mapstructure:"agents"`
	Services  []Service `mapstructure:"services"`
	Log       Log       `mapstructure:"log"`

	// ShutdownTimeout is the whole stop of the controller, from the signal
	// to its exit: the agents stopped (agents.stop_timeout), then the
	// services (their stop_timeout); whatever still runs once it passed is
	// killed. Who stops the controller waits for it longer, eg: the
	// stop_grace_period of the compose, or tools/automator.sh. By default,
	// agents.stop_timeout plus the longest stop_timeout of the services plus
	// shutdownMargin; never under them plus shutdownSlack.
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`

	// Warnings are the ${VAR} of the file that weren't set, taken as empty,
	// by Load: the ones it can't do without fail it instead, see checkUnset.
	Warnings []string `mapstructure:"-"`
}

// Database is the connection to PostgreSQL.
type Database struct {
	// DSN is the URL of the database, eg:
	// postgres://xtrader:${XTRADER_DB_PASSWORD}@localhost:5432/xtrader?sslmode=disable
	DSN             string        `mapstructure:"dsn"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`

	// LogLevel is the one of the queries logged: silent, error or warn (the
	// slow ones), their values left out.
	LogLevel string `mapstructure:"log_level"`
}

// Migrations are the directories of the SQL files of cmd/migrator.
type Migrations struct {
	Path  string `mapstructure:"path"`
	Seeds string `mapstructure:"seeds"`
}

// Bootstrap is the first user of the dashboard, created by the controller
// while the database has none: its password hashed, then unused. Remove the
// variables of the password once in.
type Bootstrap struct {
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

// UI is the bundle of the web dashboard: a local build, eg: web/dist, or else
// a release, latest by default.
type UI struct {
	Dir     string `mapstructure:"dir"`
	Version string `mapstructure:"version"`

	// TrustedProxies are the reverse proxies in front of the dashboard, IPs or
	// CIDRs, eg: ngrok on the host: only the X-Forwarded-For and
	// X-Forwarded-Proto of a request coming from one of them are believed, for
	// the address of the client (the logs and the limits of the logins) and
	// its HTTPS. The loopback by default.
	TrustedProxies []string `mapstructure:"trusted_proxies"`
}

// defaultTrustedProxies are the proxies trusted by default: the loopback.
var defaultTrustedProxies = []string{"127.0.0.0/8", "::1/128"}

// Proxies returns the prefixes of TrustedProxies.
func (u UI) Proxies() ([]netip.Prefix, error) {
	proxies := make([]netip.Prefix, 0, len(u.TrustedProxies))
	for _, entry := range u.TrustedProxies {
		entry = strings.TrimSpace(entry)
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			addr, addrErr := netip.ParseAddr(entry)
			if addrErr != nil {
				return nil, fmt.Errorf("ui.trusted_proxies: %q is neither an IP nor a CIDR", entry)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		if prefix.Bits() == 0 {
			// anyone could tell its address and its HTTPS
			return nil, fmt.Errorf("ui.trusted_proxies: %q trusts every address", entry)
		}
		proxies = append(proxies, prefix.Masked())
	}
	return proxies, nil
}

// Agents is how the controller runs the agents of the database.
type Agents struct {
	// API is the address of the internal API the agents fetch their settings
	// from and persist through, on the loopback: never published.
	API string `mapstructure:"api"`

	// Dir is the working directory of the agents, eg: the root of the
	// repository for go run.
	Dir string `mapstructure:"dir"`

	// Commands is the command of each kind, eg: go run ./agents/llm/llmagent,
	// or /app/bin/llmagent in the container.
	Commands map[string]string `mapstructure:"commands"`

	// Env holds the variables of their environment besides PATH, HOME, USER,
	// LOGNAME, TZ, LANG, LC_ALL and TMPDIR, eg: GOCACHE=/tmp/go-cache for go
	// run: never a secret, they get theirs from the controller. The services
	// don't get it.
	Env Env `mapstructure:"env"`

	// StopTimeout is how long an agent stopped has before it is killed.
	StopTimeout time.Duration `mapstructure:"stop_timeout"`
}

// Service is a process the agents need, eg: tools/mt5bridge on the host,
// launched before them from Dir of Agents and stopped after them: once
// Health, a URL, answers, whatever its status, it is up, or after Wait at most
// (3 minutes by default). One answering already isn't launched, nor stopped.
// StopTimeout is how long it has to exit once interrupted before it is killed
// (10 seconds by default). Its environment is the base one of the agents
// (PATH, HOME, USER, LOGNAME, TZ, LANG, LC_ALL and TMPDIR) plus Env, never the
// Env of Agents.
type Service struct {
	Name        string        `mapstructure:"name"`
	Exec        string        `mapstructure:"exec"`
	Args        []string      `mapstructure:"args"`
	Env         Env           `mapstructure:"env"`
	Restart     bool          `mapstructure:"restart"`
	Health      string        `mapstructure:"health"`
	Wait        time.Duration `mapstructure:"wait"`
	StopTimeout time.Duration `mapstructure:"stop_timeout"`
}

// OutputWait is how long the controller waits for the output of a process
// once it exited, eg: held by a process out of its group, such as the X server
// a script of the bridge starts in a session of its own (see
// controller.Supervisor): each phase of the stop, the agents then the
// services, may take that long beyond its stop_timeout.
const OutputWait = time.Second

const (
	// shutdownMargin is what the default ShutdownTimeout gives beyond the
	// stops of the agents and of the services, and shutdownSlack what any
	// ShutdownTimeout does at least: OutputWait for each of the two phases,
	// and a second for the rest, eg: a process killed reaped, the records the
	// agents sent stored.
	shutdownMargin = 10 * time.Second
	shutdownSlack  = 2*OutputWait + time.Second
)

// StopTimeouts returns how long the stops of the agents and of the services
// take at most, one after the other: agents.stop_timeout plus the longest
// stop_timeout of the services.
func (c *Config) StopTimeouts() time.Duration {
	var services time.Duration
	for _, s := range c.Services {
		services = max(services, s.StopTimeout)
	}
	return c.Agents.StopTimeout + services
}

// Env is the environment of a process of config.yml, NAME=value each: a list,
// as the names of the variables keep their case there, not as the keys of a
// map.
type Env []string

// envNameRegexp is the rule of the names of the variables of an Env.
var envNameRegexp = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Map returns the variables of e by name, the last one of a name kept.
func (e Env) Map() map[string]string {
	vars := make(map[string]string, len(e))
	for _, entry := range e {
		name, value, _ := strings.Cut(entry, "=")
		vars[name] = value
	}
	return vars
}

// check fails on an entry that isn't NAME=value.
func (e Env) check(owner string) error {
	for _, entry := range e {
		if name, _, ok := strings.Cut(entry, "="); !ok || !envNameRegexp.MatchString(name) {
			return fmt.Errorf("%s: %q isn't NAME=value", owner, entry)
		}
	}
	return nil
}

// Log is the logging of the controller.
type Log struct {
	Level string `mapstructure:"level"`
}

// Load reads the configuration of file, fills its defaults and checks it.
func Load(file string) (*Config, error) {
	ext := strings.TrimPrefix(filepath.Ext(file), ".")
	if ext == "yml" {
		ext = "yaml"
	}
	loader := gobox.NewViper[Config](&gobox.LoaderOptions[Config]{ConfigType: ext})
	loader.GetViper().SetConfigFile(file) // this file: a missing one fails
	cfg, err := loader.Load()
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", file, err)
	}
	cfg.defaults()

	// gobox/config expands a ${VAR} without a value to nothing: the URL of the
	// database without its password, the bridge of MT5 without its token
	text, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", file, err)
	}
	found, err := unsetVars(string(text))
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", file, err)
	}
	errs, warnings := cfg.checkUnset(found)
	cfg.Warnings = warnings
	if len(errs) > 0 {
		errs = []error{fmt.Errorf("%w: %w", ErrInvalid, errors.Join(errs...))}
	}
	if err := cfg.Validate(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("config %s: %w", file, errors.Join(errs...))
	}
	return cfg, nil
}

// defaults fills the settings left out.
func (c *Config) defaults() {
	if c.Listen == "" {
		c.Listen = "localhost:8080"
	}
	if c.Database.MaxOpenConns == 0 {
		c.Database.MaxOpenConns = 10
	}
	if c.Database.MaxIdleConns == 0 {
		c.Database.MaxIdleConns = c.Database.MaxOpenConns
	}
	if c.Database.ConnMaxLifetime == 0 {
		c.Database.ConnMaxLifetime = time.Hour
	}
	if c.Database.LogLevel == "" {
		c.Database.LogLevel = "warn"
	}
	if c.Migrations.Path == "" {
		c.Migrations.Path = "sql/migrations"
	}
	if c.Agents.API == "" {
		c.Agents.API = "127.0.0.1:8090"
	}
	if c.Agents.Dir == "" {
		c.Agents.Dir = "."
	}
	if c.Agents.StopTimeout == 0 {
		c.Agents.StopTimeout = 20 * time.Second
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if len(c.UI.TrustedProxies) == 0 {
		c.UI.TrustedProxies = slices.Clone(defaultTrustedProxies)
	}
	for i := range c.Services {
		if c.Services[i].Wait == 0 {
			c.Services[i].Wait = 3 * time.Minute
		}
		if c.Services[i].StopTimeout == 0 {
			c.Services[i].StopTimeout = 10 * time.Second
		}
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = c.StopTimeouts() + shutdownMargin
	}
}

// Validate checks the configuration, the secrets key included.
func (c *Config) Validate() error {
	var errs []error
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		errs = append(errs, fmt.Errorf("listen %q: %w", c.Listen, err))
	}
	if dsn, err := url.Parse(c.Database.DSN); err != nil || (dsn.Scheme != "postgres" && dsn.Scheme != "postgresql") {
		errs = append(errs, errors.New("database.dsn: a postgres:// URL is required"))
	}
	if !slices.Contains([]string{"silent", "error", "warn"}, c.Database.LogLevel) {
		errs = append(errs, fmt.Errorf("database.log_level %q: silent, error or warn", c.Database.LogLevel))
	}
	if _, err := c.Key(); err != nil {
		errs = append(errs, err)
	}
	if host, _, err := net.SplitHostPort(c.Agents.API); err != nil {
		errs = append(errs, fmt.Errorf("agents.api %q: %w", c.Agents.API, err))
	} else if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		errs = append(errs, fmt.Errorf("agents.api %q: the internal API listens on the loopback only", c.Agents.API))
	}
	for kind := range c.Agents.Commands {
		if !slices.Contains(Kinds, kind) {
			errs = append(errs, fmt.Errorf("agents.commands: unknown kind %q, of %s", kind, strings.Join(Kinds, ", ")))
		}
	}
	if err := c.Agents.Env.check("agents.env"); err != nil {
		errs = append(errs, err)
	}
	for name := range c.Agents.Env.Map() {
		if strings.HasPrefix(name, "XTRADER_") {
			errs = append(errs, fmt.Errorf("agents.env: %s is set by the controller", name))
		}
	}
	for i, s := range c.Services {
		switch {
		case s.Name == "" || s.Exec == "":
			errs = append(errs, fmt.Errorf("services[%d]: name and exec are required", i))
		case s.Health != "" && !strings.HasPrefix(s.Health, "http://") && !strings.HasPrefix(s.Health, "https://"):
			errs = append(errs, fmt.Errorf("service %s: health %q is not an http(s) URL", s.Name, s.Health))
		case s.Wait < 0:
			errs = append(errs, fmt.Errorf("service %s: wait %v is negative", s.Name, s.Wait))
		case s.StopTimeout < 0:
			errs = append(errs, fmt.Errorf("service %s: stop_timeout %v is negative", s.Name, s.StopTimeout))
		}
		if err := s.Env.check("service " + s.Name + ": env"); err != nil {
			errs = append(errs, err)
		}
	}
	if c.Agents.StopTimeout < 0 {
		errs = append(errs, fmt.Errorf("agents.stop_timeout %v is negative", c.Agents.StopTimeout))
	}
	if least := c.StopTimeouts() + shutdownSlack; c.ShutdownTimeout < least {
		errs = append(errs, fmt.Errorf("shutdown_timeout %v: under the stops of the agents and of the services, "+
			"agents.stop_timeout plus the longest stop_timeout of the services, plus %v (%v): the controller would "+
			"kill them before their time", c.ShutdownTimeout, shutdownSlack, least))
	}
	if _, err := c.UI.Proxies(); err != nil {
		errs = append(errs, err)
	}
	if (c.Bootstrap.Username == "") != (c.Bootstrap.Password == "") {
		errs = append(errs, errors.New("bootstrap: the username and the password go together"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalid, errors.Join(errs...))
	}
	return nil
}

// Key returns the key of the secrets, its 32 bytes.
func (c *Config) Key() ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.SecretsKey))
	switch {
	case c.SecretsKey == "":
		return nil, errors.New("secrets_key: required, 32 bytes in base64 (eg: ${XTRADER_SECRETS_KEY})")
	case err != nil:
		return nil, errors.New("secrets_key: not standard base64")
	case len(key) != 32:
		return nil, fmt.Errorf("secrets_key: %d bytes, 32 required", len(key))
	}
	return key, nil
}

// Command returns the command of the agents of kind: the one of Commands, else
// the binary of the kind next to the controller's, eg: bin/llmagent.
func (a Agents) Command(kind string) string {
	if command := strings.TrimSpace(a.Commands[kind]); command != "" {
		return command
	}
	return filepath.Join("bin", kind+"agent")
}
