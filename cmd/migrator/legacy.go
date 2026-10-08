package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/tidwall/buntdb"
	"gopkg.in/yaml.v3"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

var (
	// errDryRun rolls back an import run with -dry-run.
	errDryRun = errors.New("migrator: dry run")
	// errConflicts rolls back an import with conflicts, see report.conflict.
	errConflicts = errors.New("migrator: conflicts")
)

// telegramImport is the record of the import of Telegram, see
// importer.telegram.
const telegramImport = "env:TELEGRAM"

// legacyConfig is the agents.yml of the previous version.
type legacyConfig struct {
	Services []legacyService `yaml:"services"`
	Agents   []legacyAgent   `yaml:"agents"`
}

// legacyAgent is an agent of agents.yml: its bot run by the controller with
// Exec and Args, from the directory of the file, with its environment and Env.
type legacyAgent struct {
	Name    string            `yaml:"name"`
	Address string            `yaml:"address"`
	Enabled *bool             `yaml:"enabled"`
	Exec    string            `yaml:"exec"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
	Restart bool              `yaml:"restart"`
}

// legacyService is a service of agents.yml, eg: the bridge of MT5.
type legacyService struct {
	Name    string            `yaml:"name"`
	Exec    string            `yaml:"exec"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
	Restart bool              `yaml:"restart"`
	Health  string            `yaml:"health"`
	Wait    string            `yaml:"wait"`
}

// legacyDecision is a decision of a decisions file of an LLM agent, with the
// files of its attachments.
type legacyDecision struct {
	ui.Decision
	Files []string `json:"files,omitempty"`
}

// boolFlags are the flags of the agents that take no value.
var boolFlags = map[string]bool{"trade-panel": true, "telegram-control": true, "mt5-live": true,
	"capital-live": true, "llm-widen-stop": true, "llm-take-profit-optional": true, "llm-no-take-profit": true,
	"llm-higher-trend-filter": true, "llm-higher-trend-required": true, "broker-stop": true, "vwap": true,
	"day-levels": true}

// defaultStrategies are the default strategies of the agents of the runner,
// which name their comments on MT5.
var defaultStrategies = map[string]string{"trend": "trendconfluence", "momentum": "adxema",
	"breakout": "atrbreakout", "intraday": "orb"}

// numberRegexp matches a negative number, a value rather than a flag.
var numberRegexp = regexp.MustCompile(`^-\d+(\.\d+)?$`)

// importer imports the settings and the data of the previous version.
type importer struct {
	store    *store.Store
	env      map[string]string
	dir      string // the working directory of the agents, of their data files
	bridge   string // the bridge of every agent created, of -bridge: "" for the one of each
	calendar string // the MCP server of the calendars, of -calendar: "" for the one of each
	force    bool
	report   *report
}

// runImport runs migrator import with args, printing what it does to out.
func runImport(ctx context.Context, cfg *config.Config, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	agentsFile := fs.String("agents", "agents.yml", "agents.yml of the previous version")
	envFile := fs.String("env", "", "environment file of the previous version, eg: ~/.config/automator/automator.env "+
		"(the login of the dashboard, Telegram, the keys of the language models, the tokens of the bridge and "+
		"of the MCP server)")
	dir := fs.String("dir", "", "working directory of the agents of the previous version, where their data/ is "+
		"(default: the directory of -agents)")
	bridge := fs.String("bridge", "", "bridge of MT5 of every agent imported, in place of its -mt5-bridge and of the "+
		"default, eg: http://mt5:18812 in the containers (default: the one of each agent)")
	calendar := fs.String("calendar", "", "MCP server of the terminal of the agents with a calendar, in place of "+
		"their -calendar-mcp, eg: http://mt5:22347/mcp in the containers (default: the one of each agent)")
	force := fs.Bool("force", false, "import again the data files imported already (their records kept are never "+
		"replaced), and the data of an agent that has orders already, the IDs of the orders imported moved past its "+
		"own; never brings back an agent or a user deleted, nor changes Telegram")
	dryRun := fs.Bool("dry-run", false, "tell what would be imported, writing nothing")
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return err
	}
	// the URLs checked before the files and the database
	bridgeURL, bridgeErr := checkURL("bridge", *bridge, "http://mt5:18812")
	calendarURL, calendarErr := checkURL("calendar", *calendar, "http://mt5:22347/mcp")
	if err := errors.Join(bridgeErr, calendarErr); err != nil {
		return err
	}

	legacy, err := readAgents(*agentsFile)
	if err != nil {
		return err
	}
	env := map[string]string{}
	if *envFile != "" {
		if env, err = readEnv(*envFile); err != nil {
			return err
		}
	}
	if *dir == "" {
		*dir = filepath.Dir(*agentsFile)
	}

	key, err := cfg.Key()
	if err != nil {
		return err
	}
	s, err := store.Open(cfg.Database, key)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	if err := s.CheckSchema(ctx); err != nil {
		return err
	}

	imp := &importer{store: s, env: env, dir: *dir, bridge: bridgeURL, calendar: calendarURL, force: *force,
		report: &report{}}
	err = s.Transaction(ctx, func(ctx context.Context) error {
		if err := imp.run(ctx, legacy); err != nil {
			return err
		}
		switch {
		case len(imp.report.conflicts) > 0:
			return errConflicts
		case *dryRun:
			return errDryRun
		}
		return nil
	})
	if err != nil && !errors.Is(err, errDryRun) && !errors.Is(err, errConflicts) {
		return err
	}
	imp.report.print(out, *dryRun)
	if n := len(imp.report.conflicts); n > 0 {
		return fmt.Errorf("migrator: %d %s, nothing imported: see the report", n,
			map[bool]string{true: "conflict", false: "conflicts"}[n == 1])
	}
	return nil
}

// readAgents reads the agents.yml of file.
func readAgents(file string) (legacyConfig, error) {
	var cfg legacyConfig
	data, err := os.ReadFile(file)
	if err != nil {
		return cfg, fmt.Errorf("migrator: %w", err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("migrator: %s: %w", file, err)
	}
	return cfg, nil
}

// readEnv reads the variables of an environment file: NAME=value lines, with
// an optional export and quotes, # starting a comment line.
func readEnv(file string) (map[string]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("migrator: %w", err)
	}
	defer func() { _ = f.Close() }()
	env := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		env[strings.TrimSpace(name)] = value
	}
	return env, scanner.Err()
}

// run imports the users, Telegram, the agents with their data, and tells what
// to do with the services.
func (imp *importer) run(ctx context.Context, legacy legacyConfig) error {
	if err := imp.users(ctx); err != nil {
		return err
	}
	if err := imp.telegram(ctx); err != nil {
		return err
	}
	for i, a := range legacy.Agents {
		if err := imp.agent(ctx, i, a); err != nil {
			return fmt.Errorf("migrator: agent %s: %w", a.Name, err)
		}
	}
	for _, service := range legacy.Services {
		if imp.bridge != "" {
			imp.report.note("service %s (%s): a process of the host, left out: the agents imported use the bridge "+
				"of -bridge", service.Name, service.Exec)
			continue
		}
		imp.report.note("service %s (%s): a process of the host, add it to the services of config.yml",
			service.Name, service.Exec)
	}
	return nil
}

// users imports the login of the dashboard, its password hashed, once: a
// user that exists is kept as it is (its password too), and one deleted on the
// dashboard is never created again.
func (imp *importer) users(ctx context.Context) error {
	username, password := strings.TrimSpace(imp.env["XTRADER_UI_USERNAME"]), imp.env["XTRADER_UI_PASSWORD"]
	if username == "" || password == "" {
		imp.report.note("dashboard: no XTRADER_UI_USERNAME and XTRADER_UI_PASSWORD, no user imported")
		return nil
	}
	users, err := imp.store.Users(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if strings.EqualFold(u.Username, username) {
			imp.report.add("dashboard: user %s kept, it exists", username)
			return nil
		}
	}
	key := "user:" + strings.ToLower(username)
	deleted, err := imp.store.UserDeleted(ctx, username)
	if err != nil {
		return err
	}
	imported, err := imp.store.LegacyImported(ctx, key)
	if err != nil {
		return err
	}
	switch {
	case deleted:
		imp.report.note("dashboard: user %s skipped: deleted on the dashboard, never created again", username)
		return nil
	case imported:
		imp.report.note("dashboard: user %s skipped: imported already, gone since", username)
		return nil
	}
	user, err := imp.store.CreateUser(ctx, username, password)
	if err != nil {
		if errors.Is(err, store.ErrInvalid) {
			imp.report.note("dashboard: user %s not imported: %v", username, err)
			return nil
		}
		return err
	}
	if err := imp.store.MarkLegacyImported(ctx, key, store.Params{"user_id": strconv.FormatInt(user.ID, 10)}); err != nil {
		return err
	}
	imp.report.add("dashboard: user %s, its password hashed with bcrypt", username)
	return nil
}

// telegram imports the token of the bot and its users, several allowed:
// TELEGRAM_USER a list of IDs. Once, and only into a Telegram never
// configured nor imported by a version before: the token, the bot and the
// users of the dashboard, the ones revoked too, are kept as they are.
func (imp *importer) telegram(ctx context.Context) error {
	token := strings.TrimSpace(imp.env["TELEGRAM_TOKEN"])
	ids := strings.FieldsFunc(imp.env["TELEGRAM_USER"], func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	if token == "" && len(ids) == 0 {
		return nil
	}
	imported, err := imp.store.LegacyImported(ctx, telegramImport)
	if err != nil {
		return err
	}
	configured, err := imp.store.TelegramConfigured(ctx)
	if err != nil {
		return err
	}
	// an import of a version before the record of Telegram wrote it, its
	// token maybe cleared on the dashboard since
	older := false
	if !imported && !configured {
		if older, err = imp.store.ImportedByAnOlderVersion(ctx); err != nil {
			return err
		}
	}
	if imported || configured || older {
		why := "configured on the dashboard"
		switch {
		case imported:
			why = "imported already"
		case older:
			why = "imported by a version before (which kept no record of it), changed on the dashboard since " +
				"maybe; set it there"
		}
		imp.report.add("telegram: left as it is, %s: TELEGRAM_TOKEN and TELEGRAM_USER not imported", why)
		return nil
	}
	known := make(map[int64]bool, len(ids))
	authorized := 0
	for _, value := range ids {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			imp.report.note("telegram: user %q is not a numeric ID, left out", value)
			continue
		}
		if !known[id] {
			if _, err := imp.store.AddTelegramUser(ctx, id, ""); err != nil {
				return err
			}
			known[id] = true
		}
		authorized++
	}
	var secret store.Secret
	if token != "" {
		secret = &token
	}
	enabled := token != "" && authorized > 0
	if err := imp.store.SetTelegram(ctx, &enabled, secret); err != nil {
		return err
	}
	if err := imp.store.MarkLegacyImported(ctx, telegramImport, store.Params{"users": strconv.Itoa(authorized),
		"enabled": strconv.FormatBool(enabled)}); err != nil {
		return err
	}
	imp.report.add("telegram: token %s, %d users authorized, the bot %s", describeSecret(token), authorized,
		map[bool]string{true: "on", false: "off"}[enabled])
	return nil
}

// agent imports the agent a at position of the dashboard, and its data.
func (imp *importer) agent(ctx context.Context, position int, a legacyAgent) error {
	kind := kindOf(a.Exec)
	if kind == "" {
		imp.report.note("agent %s skipped: its exec %q isn't one of the agents", a.Name, a.Exec)
		return nil
	}
	flags := parseArgs(a.Args)
	if exchange := flagOr(flags, "exchange", "binance"); exchange != "mt5" {
		imp.report.note("agent %s skipped: the exchange %s was removed, only mt5 remains", a.Name, exchange)
		return nil
	}
	mode := flagOr(flags, "mode", "backtest")
	if mode != "live" && mode != "paper" {
		imp.report.note("agent %s skipped: mode %s, the controller runs live and paper agents only", a.Name, mode)
		return nil
	}
	comment := flagOr(flags, "mt5-comment", defaultComment(kind, flags))

	agent, err := imp.store.AgentByName(ctx, a.Name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		gone, err := imp.gone(ctx, a.Name)
		if err != nil || gone {
			return err
		}
		if agent, err = imp.createAgent(ctx, position, a, kind, mode, flags); err != nil {
			return err
		}
		err = imp.store.MarkLegacyImported(ctx, "agent:"+a.Name, store.Params{"agent_id": strconv.FormatInt(agent.ID,
			10)})
		if err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		imp.report.add("agent %s kept, it exists", a.Name)
		imp.kept(agent)
	}
	return imp.data(ctx, agent, kind, comment, flags)
}

// gone reports whether the agent name of the previous version, none of that
// name on the dashboard, is to leave out, told: deleted there (or imported
// before, gone since), as an import never brings back what the user deleted.
func (imp *importer) gone(ctx context.Context, name string) (bool, error) {
	deleted, err := imp.store.AgentDeleted(ctx, name)
	if err != nil {
		return false, err
	}
	imported, err := imp.store.LegacyImported(ctx, "agent:"+name)
	if err != nil {
		return false, err
	}
	switch {
	case deleted:
		imp.report.note("agent %s skipped: deleted on the dashboard, never created again, nor its data imported "+
			"(to import it, create it there, or give it another name in agents.yml)", name)
	case imported:
		imp.report.note("agent %s skipped: imported already, gone since", name)
	}
	return deleted || imported, nil
}

// kept notes the bridge and the calendar of the agent kept as it exists that
// differ from the ones of -bridge and -calendar, which it didn't take.
func (imp *importer) kept(agent store.Agent) {
	if acc := agent.Account; acc != nil && imp.bridge != "" && acc.BridgeURL != imp.bridge {
		imp.report.note("agent %s kept as it is: its bridge at %s, not %s of -bridge", agent.Name, acc.BridgeURL,
			imp.bridge)
	}
	if l := agent.LLM; l != nil && l.CalendarURL != nil && imp.calendar != "" && *l.CalendarURL != imp.calendar {
		imp.report.note("agent %s kept as it is: its calendar at %s, not %s of -calendar", agent.Name,
			*l.CalendarURL, imp.calendar)
	}
}

// createAgent creates the agent a of kind with its flags.
func (imp *importer) createAgent(ctx context.Context, position int, a legacyAgent, kind, mode string,
	flags map[string]string) (store.Agent, error) {
	env := func(name string) string {
		if value, ok := a.Env[name]; ok {
			return value
		}
		return imp.env[name]
	}
	agent := store.Agent{Name: a.Name, Kind: kind, Enabled: a.Enabled == nil || *a.Enabled, Mode: mode,
		APIAddress: flagOr(flags, "api", strings.TrimSpace(a.Address)), Launched: true, Restart: a.Restart,
		TradePanel: flags["trade-panel"] == "true", TelegramControl: flags["telegram-control"] == "true",
		Params: store.Params{}, Position: position}
	bridge := flagOr(flags, "mt5-bridge", "http://127.0.0.1:18812")
	if imp.bridge != "" {
		bridge = imp.bridge
	}
	account := &store.Account{Exchange: "mt5", BridgeURL: bridge, RealAllowed: flags["mt5-live"] == "true"}
	if value, ok := flags["mt5-magic"]; ok {
		magic, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return store.Agent{}, fmt.Errorf("-mt5-magic %q: %w", value, err)
		}
		account.Magic = &magic
	}
	if value, ok := flags["mt5-comment"]; ok {
		account.Comment = &value
	}
	agent.Account = account
	secrets := store.AgentSecrets{BridgeToken: optional(flagOr(flags, "mt5-token", env("MT5_BRIDGE_TOKEN")))}

	var llmParams store.Params
	if kind == "llm" {
		calendar := optional(flags["calendar-mcp"])
		if calendar != nil && imp.calendar != "" {
			calendar = optional(imp.calendar)
		}
		settings := &store.LLMSettings{Provider: flags["llm-provider"], Model: flags["llm-model"],
			BaseURL: optional(flags["llm-base-url"]), ReasoningEffort: optional(flags["llm-reasoning"]),
			CalendarURL: calendar, Params: store.Params{}}
		if value, ok := flags["llm-max-tokens"]; ok {
			tokens, err := strconv.Atoi(value)
			if err != nil {
				return store.Agent{}, fmt.Errorf("-llm-max-tokens %q: %w", value, err)
			}
			settings.MaxTokens = &tokens
		}
		if path := flags["prompt-path"]; path != "" {
			id, err := imp.prompt(ctx, path)
			if err != nil {
				return store.Agent{}, err
			}
			settings.PromptID = &id
		}
		secrets.LLMAPIKey = optional(env(llm.KeyEnv(settings.Provider)))
		if settings.CalendarURL != nil {
			secrets.CalendarToken = optional(env("MT5_MCP_TOKEN"))
		}
		agent.LLM, llmParams = settings, settings.Params
	}
	for name, value := range flags {
		switch {
		case store.ColumnFlag(name): // a column, read above, or a file the database replaces
		case llmParams != nil && store.LLMParam(name):
			llmParams[name] = value
		default:
			agent.Params[name] = value
		}
	}

	created, err := imp.store.CreateAgent(ctx, agent, secrets)
	if refusal := err; errors.Is(refusal, store.ErrConflict) && agent.Enabled {
		// two agents of the previous version trading the same positions: the
		// second one disabled, until it gets a comment or a magic number of its own
		agent.Enabled = false
		if created, err = imp.store.CreateAgent(ctx, agent, secrets); err == nil {
			imp.report.note("agent %s imported disabled: %v", agent.Name, refusal)
		}
	}
	if err != nil {
		return store.Agent{}, err
	}
	state := map[bool]string{true: "enabled", false: "disabled"}[created.Enabled]
	line := fmt.Sprintf("agent %s: %s %s, %s, API at %s", created.Name, kind, mode, state, created.APIAddress)
	if created.LLM != nil {
		line += fmt.Sprintf(", %s %s with its API key %s", created.LLM.Provider, created.LLM.Model,
			describeSecret(deref(secrets.LLMAPIKey)))
	}
	imp.report.add("%s", line)
	if acc := created.Account; acc != nil {
		imp.report.add("  bridge at %s%s, its token %s", acc.BridgeURL,
			map[bool]string{true: " of -bridge"}[imp.bridge != ""], describeSecret(deref(secrets.BridgeToken)))
	}
	if l := created.LLM; l != nil && l.CalendarURL != nil {
		imp.report.add("  calendar at %s%s, its token %s", *l.CalendarURL,
			map[bool]string{true: " of -calendar"}[imp.calendar != ""], describeSecret(deref(secrets.CalendarToken)))
	}
	return created, nil
}

// prompt returns the ID of the prompt of the file path of -prompt-path: the
// one of its name, eg: trend_following, else a new one of its content.
func (imp *importer) prompt(ctx context.Context, path string) (int64, error) {
	name := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	prompt, err := imp.store.PromptByName(ctx, name)
	if err == nil {
		return prompt.ID, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	file := path
	if !filepath.IsAbs(file) {
		file = filepath.Join(imp.dir, path)
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return 0, fmt.Errorf("the prompt %s: %w", path, err)
	}
	prompt, err = imp.store.CreatePrompt(ctx, name, "imported from "+path, string(content), "import")
	if err != nil {
		return 0, err
	}
	imp.report.add("prompt %s: created of %s", name, path)
	return prompt.ID, nil
}

// dataFiles are the data files of an agent of the previous version: its
// orders, and the decisions and the journal of an LLM agent, "" for none.
type dataFiles struct {
	orders, decisions, journal string
}

// all returns the files, the ones set.
func (f dataFiles) all() []string {
	var files []string
	for _, file := range []string{f.orders, f.decisions, f.journal} {
		if file != "" {
			files = append(files, file)
		}
	}
	return files
}

// data imports the data files of the agent: its orders, and the decisions and
// journal of an LLM agent, where the agent of the previous version kept them.
// Their order IDs are the ones of the agent: into an agent that used some
// already, they are moved past its own with -force (see offset), else its data
// is a conflict, left out.
func (imp *importer) data(ctx context.Context, agent store.Agent, kind, comment string,
	flags map[string]string) error {
	files := dataFiles{orders: imp.path(flagOr(flags, "db", ""), comment, ".db")}
	if kind == "llm" {
		files.decisions = imp.path(flagOr(flags, "llm-decisions", ""), comment, ".decisions.db")
		if journal := flagOr(flags, "llm-journal", ""); journal != "none" {
			files.journal = imp.path(journal, comment, ".decisions.jsonl")
		}
	}
	pending := make(map[string]bool)
	for _, file := range files.all() {
		ok, err := imp.pending(ctx, file)
		if err != nil {
			return err
		}
		if ok {
			pending[file] = true
		}
	}
	if len(pending) == 0 {
		return nil
	}
	move, ok, err := imp.offset(ctx, agent, files, pending)
	if err != nil || !ok {
		return err
	}
	if move.offset > 0 {
		imp.report.add("  the IDs of its orders imported moved by %d, past the ones of the agent", move.offset)
	}

	var orders []model.Order
	if pending[files.orders] {
		if orders, ok, err = imp.orders(ctx, agent, files.orders, &move); err != nil || !ok {
			return err
		}
	}
	if files.decisions == "" {
		return nil
	}
	if pending[files.decisions] {
		if ok, err := imp.decisions(ctx, agent, files.decisions, move); err != nil || !ok {
			return err
		}
	} else if _, statErr := os.Stat(files.decisions); statErr != nil && pending[files.journal] {
		if err := imp.journalDecisions(ctx, agent, files.journal, orders); err != nil {
			return err
		}
	}
	if pending[files.journal] {
		return imp.journal(ctx, agent, files.journal, move.offset)
	}
	return nil
}

// idMove is how the order IDs of the data of an agent move, see
// importer.offset: by offset; and, its data imported before, the IDs leased
// to the agent since, past the last one of that import, which an order or a
// link of a decision imported takes only as a conflict: the order of the
// agent at that ID may be on its way, eg: in its outbox.
type idMove struct {
	offset int64
	leased store.LeasedIDs
}

// path returns the data file of a flag, else the default one of the comment
// of the agent with ext, as the agents of the previous version named them,
// eg: data/nb-llm.db.
func (imp *importer) path(file, comment, ext string) string {
	if file == "" {
		name := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
				return r
			}
			return '-'
		}, comment)
		if name == "" {
			name = "llm"
		}
		file = filepath.Join("data", name+ext)
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(imp.dir, file)
	}
	return file
}

// pending reports whether file is to import: it exists, and wasn't imported
// yet unless forced.
func (imp *importer) pending(ctx context.Context, file string) (bool, error) {
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	imported, err := imp.store.LegacyImported(ctx, file)
	if err != nil {
		return false, err
	}
	if imported && !imp.force {
		imp.report.add("  %s imported already", file)
		return false, nil
	}
	return true, nil
}

// offset returns how far the IDs of the orders of the data of the agent move,
// and of the links of its decisions: as far as when its data was imported
// before (the files of pending imported again, or the ones missing then), the
// IDs leased to the agent since never taken (see idMove); else none into an
// agent that used no order ID, or, with -force, past the IDs it used. Without
// -force, the data of an agent that used some is a conflict, told: false.
// The agent is locked meanwhile, leasing no order ID.
func (imp *importer) offset(ctx context.Context, agent store.Agent, files dataFiles,
	pending map[string]bool) (idMove, bool, error) {
	ids, err := imp.store.LockOrderIDs(ctx, agent.ID)
	if err != nil {
		return idMove{}, false, err
	}
	var move idMove
	before := false
	for _, file := range files.all() {
		record, err := imp.store.LegacyImportOf(ctx, file)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return idMove{}, false, err
		}
		offset, ok := importedOffset(record, agent)
		if !ok {
			continue
		}
		if !before {
			move.offset, before = offset, true
		}
		// the last ID it took, unknown to the versions before: then the
		// orders it kept tell it, see store.ImportOrders
		last, _ := strconv.ParseInt(record.Details["order_last"], 10, 64)
		move.leased.After = max(move.leased.After, last)
	}
	if before {
		move.leased.After = max(move.leased.After, move.offset)
		move.leased.Next = ids.Next
		return move, true, nil
	}
	switch {
	case !ids.Used():
		return idMove{}, true, nil
	case imp.force:
		return idMove{offset: max(ids.Next-1, ids.Max)}, true, nil
	}
	var names []string
	for _, file := range files.all() {
		if pending[file] {
			names = append(names, file)
		}
	}
	used := fmt.Sprintf("orders already (%d, its next order ID %d)", ids.Orders, ids.Next)
	if ids.Orders == 0 {
		used = fmt.Sprintf("leased order IDs already (its next one %d), its orders on their way", ids.Next)
	}
	imp.report.conflict("agent %s: it has %s: the orders imported would take their IDs, and the decisions "+
		"imported would link to its orders, so its data is left out (%s). Take the agent out of agents.yml, or "+
		"import with -force, which moves the IDs of the orders imported past %d, the links of their decisions "+
		"with them", agent.Name, used, strings.Join(names, ", "), max(ids.Next-1, ids.Max))
	return idMove{}, false, nil
}

// importedOffset returns the offset of the order IDs of a data file imported
// into the agent before, of its record: false for a file of another agent.
func importedOffset(record store.LegacyImport, agent store.Agent) (int64, bool) {
	details := record.Details
	switch details["agent_id"] {
	case strconv.FormatInt(agent.ID, 10):
		offset, err := strconv.ParseInt(details["order_offset"], 10, 64)
		return offset, err == nil && offset >= 0
	case "":
		// a record of the versions that never moved the IDs, of the agent
		// of its name then: never one created after it
		return 0, details["agent"] == agent.Name && !agent.CreatedAt.After(record.ImportedAt)
	}
	return 0, false
}

// importDetails are the details of the record of a data file imported into
// the agent, its order IDs moved by offset, with counts.
func importDetails(agent store.Agent, offset int64, counts ...string) store.Params {
	details := store.Params{"agent": agent.Name, "agent_id": strconv.FormatInt(agent.ID, 10),
		"order_offset": strconv.FormatInt(offset, 10)}
	for i := 0; i+1 < len(counts); i += 2 {
		details[counts[i]] = counts[i+1]
	}
	return details
}

// orders imports the orders file of the agent, their IDs moved as move tells,
// and returns its orders: false for orders at the IDs of other orders of the
// agent, or at IDs leased to it, a conflict, told, nothing written. The
// orders kept already were imported then: past their IDs, move leases.
func (imp *importer) orders(ctx context.Context, agent store.Agent, file string, move *idMove) ([]model.Order,
	bool, error) {
	offset := move.offset
	copied, cleanup, err := copyFile(file)
	if err != nil {
		return nil, false, err
	}
	defer cleanup()
	orders, err := storage.FromFile(copied)
	if err != nil {
		return nil, false, fmt.Errorf("the orders %s: %w", file, err)
	}
	stored, err := orders.Orders()
	if err != nil {
		return nil, false, fmt.Errorf("the orders %s: %w", file, err)
	}
	list := make([]model.Order, 0, len(stored))
	var last int64
	for _, order := range stored {
		o := *order
		o.ID += offset
		list = append(list, o)
		last = max(last, o.ID)
	}
	result, err := imp.store.ImportOrders(ctx, agent.ID, list, move.leased)
	if err != nil {
		return nil, false, err
	}
	move.leased.After = max(move.leased.After, result.KeptLast)
	if len(result.Conflicts) > 0 || len(result.Leased) > 0 {
		legacy := func(ids []int64) string {
			list := make([]string, 0, len(ids))
			for _, id := range ids {
				list = append(list, strconv.FormatInt(id-offset, 10))
			}
			return strings.Join(list, ", ")
		}
		moved := ""
		if offset > 0 {
			moved = fmt.Sprintf(" (their IDs moved by %d)", offset)
		}
		if len(result.Conflicts) > 0 {
			imp.report.conflict("agent %s: the orders %s of %s%s would take the IDs of other orders of the agent, "+
				"which it placed since the import of the file: its data is left out", agent.Name,
				legacy(result.Conflicts), file, moved)
		}
		if len(result.Leased) > 0 {
			imp.report.conflict("agent %s: the orders %s of %s%s would take IDs leased to the agent since the import "+
				"of the file (%d to %d), its orders at them maybe on their way, eg: in its outbox: its data is left "+
				"out", agent.Name, legacy(result.Leased), file, moved, move.leased.After+1, move.leased.Next-1)
		}
		return nil, false, nil
	}
	if err := imp.store.ReserveOrderIDs(ctx, agent.ID, last); err != nil {
		return nil, false, err
	}
	err = imp.store.MarkLegacyImported(ctx, file, importDetails(agent, offset, "orders", strconv.Itoa(len(list)),
		"written", strconv.FormatInt(result.Written, 10), "order_last", strconv.FormatInt(last, 10)))
	if err != nil {
		return nil, false, err
	}
	imp.report.add("  %d orders of %s: %d written, %d kept already, the next ID of the agent after %d", len(list),
		file, result.Written, result.Kept, last)
	return list, true, nil
}

// decisions imports the decisions file of the LLM agent with its attachments,
// oldest first, so that an order keeps the decision that placed it, the IDs
// of their orders moved as move tells: false for a decision written linking
// to an ID leased to the agent, a conflict, told.
func (imp *importer) decisions(ctx context.Context, agent store.Agent, file string, move idMove) (bool, error) {
	offset := move.offset
	copied, cleanup, err := copyFile(file)
	if err != nil {
		return false, err
	}
	defer cleanup()
	db, err := buntdb.Open(copied)
	if err != nil {
		return false, fmt.Errorf("the decisions %s: %w", file, err)
	}
	defer func() { _ = db.Close() }()
	var decisions []legacyDecision
	err = db.View(func(tx *buntdb.Tx) error {
		return tx.AscendKeys("decision:*", func(key, value string) bool {
			var d legacyDecision
			if err := json.Unmarshal([]byte(value), &d); err != nil {
				imp.report.note("  %s: %s unreadable, left out: %v", file, key, err)
				return true
			}
			decisions = append(decisions, d)
			return true
		})
	})
	if err != nil {
		return false, fmt.Errorf("the decisions %s: %w", file, err)
	}
	sort.Slice(decisions, func(i, j int) bool {
		if decisions[i].DecidedAt != decisions[j].DecidedAt {
			return decisions[i].DecidedAt < decisions[j].DecidedAt
		}
		return decisions[i].ID < decisions[j].ID
	})

	dir := strings.TrimSuffix(file, filepath.Ext(file)) + ".attachments"
	var written, links, files, last int64
	var leased []string
	for _, d := range decisions {
		var attachments []store.Attachment
		for i, name := range d.Files {
			data, err := os.ReadFile(filepath.Join(dir, filepath.Base(name)))
			if err != nil || i >= len(d.Attachments) {
				imp.report.note("  the attachment %s of the decision %s is missing, left out", name, d.ID)
				continue
			}
			attachments = append(attachments, store.Attachment{MediaType: d.Attachments[i].MediaType, Data: data})
		}
		ids := make([]int64, 0, len(d.OrderIDs))
		for _, id := range d.OrderIDs {
			ids = append(ids, id+offset)
			last = max(last, id+offset)
		}
		d.OrderIDs = ids
		result, err := imp.store.ImportDecision(ctx, agent.ID, d.Decision, attachments)
		if err != nil {
			return false, fmt.Errorf("the decision %s: %w", d.ID, err)
		}
		if result.Written {
			written++
			for _, id := range ids {
				if move.leased.Holds(id) {
					leased = append(leased, fmt.Sprintf("%s to the order %d", d.ID, id-offset))
				}
			}
		}
		links += result.Links
		files += result.Attachments
	}
	if len(leased) > 0 {
		imp.report.conflict("agent %s: the decisions of %s would link to IDs leased to the agent since the import of "+
			"its data (%d to %d), to its orders: %s; its data is left out", agent.Name, file, move.leased.After+1,
			move.leased.Next-1, strings.Join(leased, ", "))
		return false, nil
	}
	// the orders linked missing from the orders file: their IDs never the
	// ones of orders of the agent
	if err := imp.store.ReserveOrderIDs(ctx, agent.ID, last); err != nil {
		return false, err
	}
	err = imp.store.MarkLegacyImported(ctx, file, importDetails(agent, offset,
		"decisions", strconv.Itoa(len(decisions)), "written", strconv.FormatInt(written, 10),
		"attachments", strconv.FormatInt(files, 10), "order_last", strconv.FormatInt(last, 10)))
	if err != nil {
		return false, err
	}
	imp.report.add("  %d decisions of %s: %d written, %d kept already, %d links to their orders, %d attachments",
		len(decisions), file, written, int64(len(decisions))-written, links, files)
	return true, nil
}

// journalDecisions imports the decisions of the journal of an LLM agent that
// kept none apart, linked to its orders imported by their time.
func (imp *importer) journalDecisions(ctx context.Context, agent store.Agent, journal string,
	orders []model.Order) error {
	data, err := os.ReadFile(journal)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	byPair := func(pair string) []model.Order {
		var list []model.Order
		for _, order := range orders {
			if order.Pair == pair {
				list = append(list, order)
			}
		}
		return list
	}
	decisions := llm.JournalDecisions(data, byPair)
	var written int64
	for _, d := range decisions {
		result, err := imp.store.ImportDecision(ctx, agent.ID, d, nil)
		if err != nil {
			return fmt.Errorf("the decision %s: %w", d.ID, err)
		}
		if result.Written {
			written++
		}
	}
	imp.report.add("  %d decisions of the journal %s, without a decisions file: %d written, %d kept already",
		len(decisions), journal, written, int64(len(decisions))-written)
	return nil
}

// journal imports the journal of the LLM agent, a line of JSON per call to
// the model.
func (imp *importer) journal(ctx context.Context, agent store.Agent, file string, offset int64) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var entries []store.JournalEntry
	skipped := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	for scanner.Scan() {
		entry, ok := journalEntry(scanner.Bytes())
		if !ok {
			if strings.TrimSpace(scanner.Text()) != "" {
				skipped++
			}
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("the journal %s: %w", file, err)
	}
	written, err := imp.store.ImportJournal(ctx, agent.ID, entries)
	if err != nil {
		return err
	}
	err = imp.store.MarkLegacyImported(ctx, file, importDetails(agent, offset, "entries", strconv.Itoa(len(entries)),
		"written", strconv.FormatInt(written, 10)))
	if err != nil {
		return err
	}
	line := fmt.Sprintf("  %d entries of the journal %s: %d written, %d kept already", len(entries), file, written,
		int64(len(entries))-written)
	if skipped > 0 {
		line += fmt.Sprintf(", %d unreadable lines left out", skipped)
	}
	imp.report.add("%s", line)
	return nil
}

// journalEntry returns the entry of a line of a journal, false for a line of
// another version or broken: the ID of the entries without one, of the
// versions before the decisions were kept, is their time and pair.
func journalEntry(line []byte) (store.JournalEntry, bool) {
	var entry struct {
		At          time.Time       `json:"at"`
		ID          string          `json:"id"`
		Trigger     string          `json:"trigger"`
		RequestedBy string          `json:"requested_by"`
		Model       string          `json:"model"`
		LatencyMS   int64           `json:"latency_ms"`
		Input       json.RawMessage `json:"input"`
		Decision    json.RawMessage `json:"decision"`
		Error       string          `json:"error"`
	}
	if json.Unmarshal(line, &entry) != nil || entry.At.IsZero() || len(entry.Input) == 0 {
		return store.JournalEntry{}, false
	}
	var input struct {
		Symbol string `json:"symbol"`
	}
	if json.Unmarshal(entry.Input, &input) != nil || input.Symbol == "" {
		return store.JournalEntry{}, false
	}
	id := entry.ID
	if id == "" {
		clean := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return r
			}
			return '-'
		}, input.Symbol)
		id = entry.At.UTC().Format("20060102T150405.000000") + "-" + clean
	}
	return store.JournalEntry{ID: id, At: entry.At, Pair: input.Symbol, Trigger: entry.Trigger,
		RequestedBy: entry.RequestedBy, Model: entry.Model, LatencyMS: entry.LatencyMS, Input: entry.Input,
		Decision: entry.Decision, Error: entry.Error}, true
}

// kindOf returns the kind of the agent of exec, eg: llm for
// go run ./agents/llm/llmagent, "" for none.
func kindOf(exec string) string {
	for _, kind := range config.Kinds {
		if strings.Contains(exec, kind+"agent") {
			return kind
		}
	}
	return ""
}

// defaultComment returns the comment of the orders of an agent of kind
// without -mt5-comment on MT5, as the agents of the previous version set it.
func defaultComment(kind string, flags map[string]string) string {
	if kind == "llm" {
		return "nb:llm"
	}
	return "nb:" + flagOr(flags, "strategy", defaultStrategies[kind])
}

// parseArgs returns the flags of args, the args of an agent of agents.yml
// split at their spaces, by name: -name value, -name=value, or -name alone for
// a flag without a value.
func parseArgs(args []string) map[string]string {
	var tokens []string
	for _, arg := range args {
		tokens = append(tokens, strings.Fields(arg)...)
	}
	flags := make(map[string]string)
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if !strings.HasPrefix(token, "-") || numberRegexp.MatchString(token) {
			continue
		}
		name := strings.TrimLeft(token, "-")
		if key, value, ok := strings.Cut(name, "="); ok {
			flags[key] = value
			continue
		}
		next := ""
		if i+1 < len(tokens) {
			next = tokens[i+1]
		}
		switch {
		case boolFlags[name]:
			if next == "true" || next == "false" {
				flags[name] = next
				i++
			} else {
				flags[name] = "true"
			}
		case next != "" && (!strings.HasPrefix(next, "-") || numberRegexp.MatchString(next)):
			flags[name] = next
			i++
		default:
			flags[name] = "true"
		}
	}
	return flags
}

// flagOr returns the flag name of flags, else value.
func flagOr(flags map[string]string, name, value string) string {
	if v, ok := flags[name]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return value
}

// checkURL returns the value of the flag name trimmed, "" for none: an http(s)
// URL with its host, eg: http://mt5:18812, without a user or a password, which
// the database would keep in the clear (the tokens of the bridge and of the
// MCP server are secrets of the agents, apart). Its errors show example, never
// the value, which may hold a password.
func checkURL(name, value, example string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	u, err := url.Parse(value)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "":
		return "", fmt.Errorf("migrator: -%s: an http(s) URL is required, eg: %s", name, example)
	case u.User != nil:
		return "", fmt.Errorf("migrator: -%s: a URL without a user or a password is required, eg: %s", name, example)
	}
	return value, nil
}

// optional returns a pointer to value, nil for none.
func optional(value string) *string {
	if value = strings.TrimSpace(value); value == "" {
		return nil
	}
	return &value
}

// deref returns the value of a pointer, "" for nil.
func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// describeSecret tells whether a secret is set, never its value.
func describeSecret(secret string) string {
	if strings.TrimSpace(secret) == "" {
		return "not set"
	}
	return "set, encrypted"
}

// copyFile copies file to a temporary directory, as buntdb opens its files to
// write: the files of the previous version are only read. cleanup removes the
// copy.
func copyFile(file string) (copied string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "xtrader-import-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	data, err := os.ReadFile(file)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	copied = filepath.Join(dir, filepath.Base(file))
	if err := os.WriteFile(copied, data, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return copied, cleanup, nil
}

// report is what an import did, what it left to do, and its conflicts, which
// leave out the whole import.
type report struct {
	lines     []string
	notes     []string
	conflicts []string
}

func (r *report) add(format string, args ...interface{}) {
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *report) note(format string, args ...interface{}) {
	r.notes = append(r.notes, fmt.Sprintf(format, args...))
}

func (r *report) conflict(format string, args ...interface{}) {
	r.conflicts = append(r.conflicts, fmt.Sprintf(format, args...))
}

// print writes the report to out, as a dry run when dry.
func (r *report) print(out io.Writer, dry bool) {
	title := "Imported:"
	switch {
	case len(r.conflicts) > 0:
		title = "Nothing imported, because of the conflicts below. Without them, it would import:"
	case dry:
		title = "Would import (dry run, nothing written):"
	}
	_, _ = fmt.Fprintln(out, title)
	if len(r.lines) == 0 {
		_, _ = fmt.Fprintln(out, "  nothing")
	}
	for _, line := range r.lines {
		_, _ = fmt.Fprintln(out, "  "+line)
	}
	if len(r.notes) > 0 {
		_, _ = fmt.Fprintln(out, "Notes:")
		for _, note := range r.notes {
			_, _ = fmt.Fprintln(out, "  "+note)
		}
	}
	if len(r.conflicts) > 0 {
		_, _ = fmt.Fprintln(out, "Conflicts:")
		for _, conflict := range r.conflicts {
			_, _ = fmt.Fprintln(out, "  "+conflict)
		}
	}
}
