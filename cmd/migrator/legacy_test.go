package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/buntdb"

	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/internal/pgtest"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// testKey is the secrets key of the tests.
var testKey = strings.Repeat("k", 32)

const legacyAgents = `
services:
  - name: mt5bridge
    exec: tools/mt5bridge/run.sh
    health: http://127.0.0.1:18812/health
    restart: true
agents:
  - name: llm
    enabled: true
    address: localhost:5580
    exec: go run ./agents/llm/llmagent
    args:
      - -llm-provider openai -llm-model gpt-5 -llm-reasoning low -llm-candles 60
      - -prompt-path agents/llm/prompts/trend_following.txt
      - -llm-min-stop-atr 1 -llm-higher-trend-required
      - >-
        -mode live -exchange mt5 -pair BTCUSDm,XAUUSDm -timeframe 30m
        -lot 0.03,0.01 -leverage 400
        -calendar-mcp http://127.0.0.1:22346/mcp -calendar-importance high
        -trade-panel -telegram-control
      - -api localhost:5580
    restart: true
  - name: matrend-gold
    enabled: false
    address: localhost:5555
    exec: go run ./agents/trendagent
    args:
      - -mode live -exchange mt5 -pair XAUUSDm -lot 0.01 -strategy matrend -max-distance 0.5 -min-atr-pct -2
      - -api localhost:5555
    restart: true
  - name: capital-gold
    exec: go run ./agents/trendagent
    args: [-mode live -exchange capital -pair GOLD]
  - name: binance
    exec: go run ./agents/trendagent
    args: [-mode paper]
  - name: research
    exec: go run ./agents/trendagent
    args: [-exchange mt5 -csv x.csv]
`

const legacyEnv = `# the environment of the previous version
export XTRADER_UI_USERNAME=xtrader
XTRADER_UI_PASSWORD="a strong password"
TELEGRAM_TOKEN=123:telegram-token
TELEGRAM_USER=111,222
OPENAI_API_KEY=sk-test-key
MT5_BRIDGE_TOKEN=bridge-token
MT5_MCP_TOKEN=mcp-token
`

// hostAgents is an agents.yml of the host whose agents move into the
// containers: the bridge of llm and matrend the default one, the one of
// llm-paper its own, and the calendar of llm on the host, llm-paper without
// one.
const hostAgents = `
services:
  - name: mt5bridge
    exec: tools/mt5bridge/run.sh
    health: http://127.0.0.1:18812/health
agents:
  - name: llm
    address: localhost:5580
    exec: go run ./agents/llm/llmagent
    args:
      - -llm-provider openai -llm-model gpt-5 -mode live -exchange mt5 -pair XAUUSDm
      - -calendar-mcp http://127.0.0.1:22346/mcp -calendar-importance high -api localhost:5580
  - name: llm-paper
    address: localhost:5581
    exec: go run ./agents/llm/llmagent
    args: [-llm-provider openai -llm-model gpt-5 -mode paper -exchange mt5 -mt5-bridge http://127.0.0.1:18813]
  - name: matrend
    address: localhost:5555
    exec: go run ./agents/trendagent
    args: [-mode live -exchange mt5 -pair XAUUSDm -strategy matrend]
`

// legacyDir writes the files of a previous version to a directory of its own,
// and returns it.
func legacyDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents.yml"), []byte(legacyAgents), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "automator.env"), []byte(legacyEnv), 0o600))
	data := filepath.Join(dir, "data")
	require.NoError(t, os.MkdirAll(filepath.Join(data, "nb-llm.decisions.attachments"), 0o755))

	at := time.Date(2026, 9, 28, 12, 30, 29, 106770558, time.UTC)
	group := int64(8174584131579702652)
	stop := 4173.0
	orders, err := storage.FromFile(filepath.Join(data, "nb-llm.db"))
	require.NoError(t, err)
	for _, o := range []model.Order{
		{ExchangeID: 6144628423280988838, Pair: "XAUUSDm", Side: model.SideTypeSell, Type: model.OrderTypeMarket,
			Status: model.OrderStatusTypeFilled, Price: 4153.064, Quantity: 1, CreatedAt: at, UpdatedAt: at},
		{ExchangeID: 2, Pair: "XAUUSDm", Side: model.SideTypeBuy, Type: model.OrderTypeStopLoss,
			Status: model.OrderStatusTypeFilled, Price: 4173, Quantity: 1, Stop: &stop, GroupID: &group,
			CreatedAt: at, UpdatedAt: at.Add(time.Hour), Profit: -0.0048, ProfitValue: -19.94},
		{ExchangeID: 3, Pair: "BTCUSDm", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
			Status: model.OrderStatusTypeFilled, Price: 84000, Quantity: 0.03, CreatedAt: at, UpdatedAt: at},
	} {
		order := o
		require.NoError(t, orders.CreateOrder(&order))
	}
	matrend, err := storage.FromFile(filepath.Join(data, "nb-matrend.db"))
	require.NoError(t, err)
	require.NoError(t, matrend.CreateOrder(&model.Order{Pair: "XAUUSDm", Side: model.SideTypeBuy,
		Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 4100, Quantity: 1,
		CreatedAt: at, UpdatedAt: at}))

	db, err := buntdb.Open(filepath.Join(data, "nb-llm.decisions.db"))
	require.NoError(t, err)
	first := legacyDecision{Decision: ui.Decision{ID: "20260928T123000.000000-XAUUSDm", Pair: "XAUUSDm",
		Timeframe: "30m", Candle: at.Unix() - 1800, DecidedAt: at.Unix(), Trigger: "candle", Model: "openai gpt-5",
		Action: "ENTER_SHORT", Confidence: 0.67, Outcome: "applied", OrderIDs: []int64{1, 2},
		Levels: []ui.Level{{Kind: "resistance", Price: 4173}}, Notes: []ui.DecisionNote{},
		Attachments: []ui.DecisionAttachment{{Index: 0, MediaType: "image/png", Size: 3}}},
		Files: []string{"20260928T123000.000000-XAUUSDm-0.png"}}
	moved := legacyDecision{Decision: ui.Decision{ID: "20260928T130000.000000-XAUUSDm", Pair: "XAUUSDm",
		Timeframe: "30m", Candle: at.Unix(), DecidedAt: at.Unix() + 1800, Trigger: "candle", Model: "openai gpt-5",
		Action: "MODIFY_SL", Outcome: "applied", OrderIDs: []int64{2}}}
	require.NoError(t, db.Update(func(tx *buntdb.Tx) error {
		for _, d := range []legacyDecision{moved, first} {
			value, _ := json.Marshal(d)
			if _, _, err := tx.Set("decision:"+d.ID, string(value), nil); err != nil {
				return err
			}
		}
		_, _, err := tx.Set("order:1", first.ID, nil)
		return err
	}))
	require.NoError(t, db.Close())
	require.NoError(t, os.WriteFile(filepath.Join(data, "nb-llm.decisions.attachments",
		"20260928T123000.000000-XAUUSDm-0.png"), []byte("png"), 0o600))

	journal := strings.Join([]string{
		`{"at":"2026-09-28T12:30:58Z","id":"20260928T123000.000000-XAUUSDm","trigger":"candle","model":"openai gpt-5",` +
			`"latency_ms":27974,"input":{"symbol":"XAUUSDm","time":"2026-09-28T12:00:00Z","price":4154.246},` +
			`"decision":{"action":"ENTER_SHORT","confidence":0.67,"reason":"the trend"}}`,
		`{"at":"2026-09-27T16:02:04Z","model":"openai gpt-4o","latency_ms":900,` +
			`"input":{"symbol":"BTCUSDm","time":"2026-09-27T15:30:00Z"},"decision":{"action":"WAIT","confidence":0.4}}`,
		`{"broken`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(data, "nb-llm.decisions.jsonl"), []byte(journal), 0o600))
	return dir
}

// hostArgs writes hostAgents and legacyEnv to a directory of their own, and
// returns the args of their import.
func hostArgs(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents.yml"), []byte(hostAgents), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "automator.env"), []byte(legacyEnv), 0o600))
	return []string{"-agents", filepath.Join(dir, "agents.yml"), "-env", filepath.Join(dir, "automator.env")}
}

// testConfig returns the configuration of a database of the test, migrated.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{Database: config.Database{DSN: pgtest.Migrated(t, filepath.Join("..", "..")),
		MaxOpenConns: 4, LogLevel: "silent"}, SecretsKey: base64.StdEncoding.EncodeToString([]byte(testKey))}
}

// testStore opens the store of cfg.
func testStore(t *testing.T, cfg *config.Config) *store.Store {
	t.Helper()
	key, err := cfg.Key()
	require.NoError(t, err)
	s, err := store.Open(cfg.Database, key)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestImport_TheSettingsAndTheDataOfThePreviousVersion(t *testing.T) {
	cfg := testConfig(t)
	dir := legacyDir(t)
	ctx := context.Background()
	args := []string{"-agents", filepath.Join(dir, "agents.yml"), "-env", filepath.Join(dir, "automator.env")}

	var out bytes.Buffer
	require.NoError(t, runImport(ctx, cfg, args, &out))
	report := out.String()
	for _, want := range []string{"agent capital-gold skipped: the exchange capital was removed",
		"agent binance skipped: the exchange binance was removed", "agent research skipped: mode backtest",
		"service mt5bridge", "user xtrader, its password hashed with bcrypt", "3 orders of", "2 decisions of",
		"2 entries of the journal", "1 unreadable lines left out", "bridge at http://127.0.0.1:18812, its token set",
		"calendar at http://127.0.0.1:22346/mcp, its token set"} {
		require.Contains(t, report, want)
	}
	require.NotContains(t, report, "of -bridge", "without -bridge")
	require.NotContains(t, report, "of -calendar", "without -calendar")
	for _, secret := range []string{"sk-test-key", "bridge-token", "mcp-token", "telegram-token", "a strong password"} {
		require.NotContains(t, report, secret, "never printed")
	}

	s := testStore(t, cfg)
	agents, err := s.Agents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 2)
	llmAgent, matrend := agents[0], agents[1]
	require.Equal(t, "llm", llmAgent.Name)
	require.Equal(t, "llm", llmAgent.Kind)
	require.True(t, llmAgent.Enabled)
	require.Equal(t, "live", llmAgent.Mode)
	require.Equal(t, "localhost:5580", llmAgent.APIAddress)
	require.True(t, llmAgent.TradePanel)
	require.True(t, llmAgent.TelegramControl)
	require.True(t, llmAgent.Restart)
	require.Equal(t, store.Params{"pair": "BTCUSDm,XAUUSDm", "timeframe": "30m", "lot": "0.03,0.01",
		"leverage": "400"}, llmAgent.Params)
	require.Equal(t, "openai", llmAgent.LLM.Provider)
	require.Equal(t, "gpt-5", llmAgent.LLM.Model)
	require.Equal(t, "low", *llmAgent.LLM.ReasoningEffort)
	require.Equal(t, "http://127.0.0.1:22346/mcp", *llmAgent.LLM.CalendarURL)
	require.Equal(t, store.Params{"llm-candles": "60", "llm-min-stop-atr": "1", "llm-higher-trend-required": "true",
		"calendar-importance": "high"}, llmAgent.LLM.Params)
	prompt, err := s.PromptByName(ctx, "trend_following")
	require.NoError(t, err)
	require.Equal(t, prompt.ID, *llmAgent.LLM.PromptID, "the prompt of -prompt-path")
	require.Equal(t, "http://127.0.0.1:18812", llmAgent.Account.BridgeURL)
	require.Nil(t, llmAgent.Account.Comment, "the default one, nb:llm")

	secrets, err := s.AgentSecrets(ctx, llmAgent.ID)
	require.NoError(t, err)
	require.Equal(t, store.AgentSecretValues{LLMAPIKey: "sk-test-key", BridgeToken: "bridge-token",
		CalendarToken: "mcp-token"}, secrets)

	require.Equal(t, "matrend-gold", matrend.Name)
	require.False(t, matrend.Enabled)
	require.Equal(t, "trend", matrend.Kind)
	require.Nil(t, matrend.LLM)
	require.Equal(t, store.Params{"pair": "XAUUSDm", "lot": "0.01", "strategy": "matrend", "max-distance": "0.5",
		"min-atr-pct": "-2"}, matrend.Params, "a negative value")

	_, err = s.Authenticate(ctx, "xtrader", "a strong password")
	require.NoError(t, err)
	settings, hasToken, err := s.Telegram(ctx)
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.True(t, hasToken)
	users, err := s.TelegramUsers(ctx)
	require.NoError(t, err)
	require.Len(t, users, 2, "several authorized")

	orders, err := s.Orders(ctx, llmAgent.ID, 0, 100)
	require.NoError(t, err)
	require.Len(t, orders, 3)
	require.Equal(t, int64(6144628423280988838), orders[0].ExchangeID)
	require.Equal(t, -19.94, orders[1].ProfitValue, "the profit kept")
	first, _, err := s.LeaseOrderIDs(ctx, llmAgent.ID, 1)
	require.NoError(t, err)
	require.EqualValues(t, 4, first, "after the orders imported")
	gold, err := s.Orders(ctx, matrend.ID, 0, 100)
	require.NoError(t, err)
	require.Len(t, gold, 1)

	links, err := s.DecisionLinks(ctx, llmAgent.ID)
	require.NoError(t, err)
	require.Equal(t, map[int64]string{1: "20260928T123000.000000-XAUUSDm", 2: "20260928T123000.000000-XAUUSDm"},
		links, "each order kept by the decision that placed it")
	mediaType, data, err := s.DecisionAttachment(ctx, llmAgent.ID, "20260928T123000.000000-XAUUSDm", 0)
	require.NoError(t, err)
	require.Equal(t, "image/png", mediaType)
	require.Equal(t, []byte("png"), data)
	memory, err := s.JournalMemory(ctx, llmAgent.ID, 20)
	require.NoError(t, err)
	require.Len(t, memory, 2)
	require.Equal(t, "BTCUSDm", memory[0].Pair, "the line without an ID, of an older version")

	// again: nothing twice
	out.Reset()
	require.NoError(t, runImport(ctx, cfg, args, &out))
	require.Contains(t, out.String(), "agent llm kept, it exists")
	require.Contains(t, out.String(), "imported already")
	require.NotContains(t, out.String(), "kept as it is", "no -bridge nor -calendar to tell")
	orders, err = s.Orders(ctx, llmAgent.ID, 0, 100)
	require.NoError(t, err)
	require.Len(t, orders, 3)
	users, err = s.TelegramUsers(ctx)
	require.NoError(t, err)
	require.Len(t, users, 2)
}

func TestImport_DryRunWritesNothing(t *testing.T) {
	cfg := testConfig(t)
	dir := legacyDir(t)
	ctx := context.Background()
	var out bytes.Buffer
	require.NoError(t, runImport(ctx, cfg, []string{"-agents", filepath.Join(dir, "agents.yml"), "-env",
		filepath.Join(dir, "automator.env"), "-dry-run"}, &out))
	require.Contains(t, out.String(), "Would import (dry run, nothing written):")
	require.Contains(t, out.String(), "3 orders of")

	s := testStore(t, cfg)
	agents, err := s.Agents(ctx)
	require.NoError(t, err)
	require.Empty(t, agents)
	count, err := s.CountUsers(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestImport_TheDecisionsOfAJournalWithoutThem(t *testing.T) {
	cfg := testConfig(t)
	dir := legacyDir(t)
	require.NoError(t, os.Remove(filepath.Join(dir, "data", "nb-llm.decisions.db")))
	ctx := context.Background()
	var out bytes.Buffer
	require.NoError(t, runImport(ctx, cfg, []string{"-agents", filepath.Join(dir, "agents.yml")}, &out))
	require.Contains(t, out.String(), "2 decisions of the journal")
	require.Contains(t, out.String(), "no XTRADER_UI_USERNAME and XTRADER_UI_PASSWORD")

	s := testStore(t, cfg)
	agent, err := s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	decisions, err := s.Decisions(ctx, agent.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, decisions, 2)
	secrets, err := s.AgentSecrets(ctx, agent.ID)
	require.NoError(t, err)
	require.Empty(t, secrets.LLMAPIKey, "no environment file")
}

func TestImport_TheBridgeAndTheCalendarOfEachAgentOrOfTheFlags(t *testing.T) {
	const (
		bridge, calendar         = "http://mt5:18812", "http://mt5:22347/mcp"
		defaultBridge, ownBridge = "http://127.0.0.1:18812", "http://127.0.0.1:18813"
		ownCalendar              = "http://127.0.0.1:22346/mcp"
	)
	for _, tc := range []struct {
		name     string
		flags    []string
		bridges  []string // of llm, llm-paper and matrend
		calendar string   // of llm
		report   []string
		absent   []string
	}{
		{name: "without them, the ones of each agent", bridges: []string{defaultBridge, ownBridge, defaultBridge},
			calendar: ownCalendar,
			report: []string{"bridge at http://127.0.0.1:18812, its token set", "bridge at http://127.0.0.1:18813, its " +
				"token set", "calendar at http://127.0.0.1:22346/mcp, its token set",
				"service mt5bridge (tools/mt5bridge/run.sh): a process of the host, add it to the services of config.yml"},
			absent: []string{"of -bridge", "of -calendar"}},
		{name: "-bridge", flags: []string{"-bridge", bridge}, bridges: []string{bridge, bridge, bridge},
			calendar: ownCalendar,
			report: []string{"bridge at http://mt5:18812 of -bridge, its token set",
				"calendar at http://127.0.0.1:22346/mcp, its token set",
				"service mt5bridge (tools/mt5bridge/run.sh): a process of the host, left out: the agents imported use " +
					"the bridge of -bridge"},
			absent: []string{"127.0.0.1:18812", "127.0.0.1:18813", "of -calendar", "add it to the services"}},
		{name: "-calendar", flags: []string{"-calendar", calendar}, bridges: []string{defaultBridge, ownBridge,
			defaultBridge}, calendar: calendar,
			report: []string{"bridge at http://127.0.0.1:18813, its token set",
				"calendar at http://mt5:22347/mcp of -calendar, its token set"},
			absent: []string{"127.0.0.1:22346", "of -bridge"}},
		{name: "both, into the containers", flags: []string{"-bridge", " http://mt5:18812 ", "-calendar", calendar},
			bridges: []string{bridge, bridge, bridge}, calendar: calendar,
			report: []string{"bridge at http://mt5:18812 of -bridge, its token set",
				"calendar at http://mt5:22347/mcp of -calendar, its token set"},
			absent: []string{"127.0.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			ctx := context.Background()
			var out bytes.Buffer
			require.NoError(t, runImport(ctx, cfg, append(hostArgs(t), tc.flags...), &out))
			report := out.String()
			for _, want := range tc.report {
				require.Contains(t, report, want)
			}
			for _, unwanted := range tc.absent {
				require.NotContains(t, report, unwanted)
			}
			for _, secret := range []string{"sk-test-key", "bridge-token", "mcp-token"} {
				require.NotContains(t, report, secret, "never printed")
			}

			s := testStore(t, cfg)
			agents, err := s.Agents(ctx)
			require.NoError(t, err)
			require.Len(t, agents, 3)
			for i, a := range agents {
				require.NotNil(t, a.Account, a.Name)
				require.Equal(t, tc.bridges[i], a.Account.BridgeURL, a.Name)
			}
			llmAgent, paper, matrend := agents[0], agents[1], agents[2]
			require.Equal(t, "llm", llmAgent.Name)
			require.NotNil(t, llmAgent.LLM.CalendarURL)
			require.Equal(t, tc.calendar, *llmAgent.LLM.CalendarURL)
			require.Equal(t, store.Params{"calendar-importance": "high"}, llmAgent.LLM.Params)
			require.Equal(t, "llm-paper", paper.Name)
			require.Nil(t, paper.LLM.CalendarURL, "without a calendar, none of -calendar either")
			require.Equal(t, "matrend", matrend.Name)
			require.Nil(t, matrend.LLM)

			secrets, err := s.AgentSecrets(ctx, llmAgent.ID)
			require.NoError(t, err)
			require.Equal(t, store.AgentSecretValues{LLMAPIKey: "sk-test-key", BridgeToken: "bridge-token",
				CalendarToken: "mcp-token"}, secrets)
			secrets, err = s.AgentSecrets(ctx, paper.ID)
			require.NoError(t, err)
			require.Empty(t, secrets.CalendarToken, "without a calendar")
		})
	}
}

func TestImport_IntoTheContainersOnceWithTheFlags(t *testing.T) {
	cfg := testConfig(t)
	ctx := context.Background()
	s := testStore(t, cfg)
	host := hostArgs(t)
	args := append(slices.Clone(host), "-bridge", "http://mt5:18812", "-calendar", "http://mt5:22347/mcp")

	// a dry run tells them, writing nothing
	var out bytes.Buffer
	require.NoError(t, runImport(ctx, cfg, append(slices.Clone(args), "-dry-run"), &out))
	require.Contains(t, out.String(), "Would import (dry run, nothing written):")
	require.Contains(t, out.String(), "bridge at http://mt5:18812 of -bridge")
	require.Contains(t, out.String(), "calendar at http://mt5:22347/mcp of -calendar")
	agents, err := s.Agents(ctx)
	require.NoError(t, err)
	require.Empty(t, agents)

	// once, then again: the agents kept, with the same URLs
	require.NoError(t, runImport(ctx, cfg, args, &out))
	out.Reset()
	require.NoError(t, runImport(ctx, cfg, args, &out))
	require.Contains(t, out.String(), "agent llm kept, it exists")
	require.NotContains(t, out.String(), "kept as it is", "the URLs of the flags")
	agents, err = s.Agents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 3)

	// other URLs: the agents kept as they are, told
	out.Reset()
	require.NoError(t, runImport(ctx, cfg, append(slices.Clone(host), "-bridge", "https://mt5.lan:18812",
		"-calendar", "https://mt5.lan/mcp"), &out))
	for _, want := range []string{
		"agent llm kept as it is: its bridge at http://mt5:18812, not https://mt5.lan:18812 of -bridge",
		"agent llm kept as it is: its calendar at http://mt5:22347/mcp, not https://mt5.lan/mcp of -calendar",
		"agent llm-paper kept as it is: its bridge at http://mt5:18812",
		"agent matrend kept as it is: its bridge at http://mt5:18812",
	} {
		require.Contains(t, out.String(), want)
	}
	require.NotContains(t, out.String(), "agent llm-paper kept as it is: its calendar", "without a calendar")
	agent, err := s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	require.Equal(t, "http://mt5:18812", agent.Account.BridgeURL)
	require.NotNil(t, agent.LLM.CalendarURL)
	require.Equal(t, "http://mt5:22347/mcp", *agent.LLM.CalendarURL)
}

func TestImport_InvalidURLsFailBeforeTheDatabase(t *testing.T) {
	ctx := context.Background()
	host := hostArgs(t)
	invalid := []struct{ flag, value, want string }{
		{"-bridge", "mt5:18812", "an http(s) URL is required, eg: http://mt5:18812"},
		{"-bridge", "ftp://mt5:18812", "an http(s) URL is required"},
		{"-bridge", "http://", "an http(s) URL is required"},
		{"-bridge", "http://:18812", "an http(s) URL is required"},
		{"-bridge", "http://xtrader:secret-password@mt5:18812", "a URL without a user or a password is required"},
		{"-calendar", "mt5:22347/mcp", "an http(s) URL is required, eg: http://mt5:22347/mcp"},
		{"-calendar", "http://mt5 22347/mcp", "an http(s) URL is required"},
		{"-calendar", "https://secret-token@mt5:22347/mcp", "a URL without a user or a password is required"},
	}
	// neither a database nor a key: the flags fail first
	for _, tc := range invalid {
		err := runImport(ctx, &config.Config{}, append(slices.Clone(host), tc.flag, tc.value), io.Discard)
		require.ErrorContains(t, err, "migrator: "+tc.flag+": "+tc.want, tc.value)
		require.NotContains(t, err.Error(), "secret", "the URL never told")
	}
	err := runImport(ctx, &config.Config{}, append(slices.Clone(host), "-bridge", "mt5", "-calendar", "mt5"),
		io.Discard)
	require.ErrorContains(t, err, "migrator: -bridge: ")
	require.ErrorContains(t, err, "migrator: -calendar: ", "both told")

	// a database: nothing written
	cfg := testConfig(t)
	for _, tc := range invalid {
		require.ErrorContains(t, runImport(ctx, cfg, append(slices.Clone(host), tc.flag, tc.value), io.Discard),
			tc.want, tc.value)
	}
	s := testStore(t, cfg)
	agents, err := s.Agents(ctx)
	require.NoError(t, err)
	require.Empty(t, agents, "nothing written")
	count, err := s.CountUsers(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestParseArgs_SplitsTheFlagsAsTheAgentsRead(t *testing.T) {
	flags := parseArgs([]string{"-mode live -pair A,B", "-trade-panel -telegram-control", "-entry-z -2 -vwap false",
		"-llm-indicators ema+20+50", "-lot=0.01", "-broker-stop"})
	require.Equal(t, map[string]string{"mode": "live", "pair": "A,B", "trade-panel": "true",
		"telegram-control": "true", "entry-z": "-2", "vwap": "false", "llm-indicators": "ema+20+50", "lot": "0.01",
		"broker-stop": "true"}, flags)
	require.Equal(t, "nb:matrend", defaultComment("trend", map[string]string{"strategy": "matrend"}))
	require.Equal(t, "nb:trendconfluence", defaultComment("trend", nil))
	require.Equal(t, "nb:llm", defaultComment("llm", nil))
	require.Equal(t, "llm", kindOf("/app/bin/llmagent"))
	require.Equal(t, "intraday", kindOf("go run ./agents/intradayagent"))
	require.Empty(t, kindOf("tools/mt5bridge/run.sh"))
}

func TestReadEnv_TheFormatsOfAnEnvironmentFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "env")
	require.NoError(t, os.WriteFile(file, []byte("# comment\n\nexport A=1\nB=\"two words\"\nC='x'\nD\n E = spaced \n"),
		0o600))
	env, err := readEnv(file)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"A": "1", "B": "two words", "C": "x", "E": "spaced"}, env)
}
