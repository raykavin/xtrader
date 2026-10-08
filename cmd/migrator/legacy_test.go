package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// Two agents of the previous version trading the same positions, of the
// default comment of their kind on the same pair and bridge: the second one is
// imported disabled, as the store refuses two agents trading them, told.
func TestImport_TheSecondAgentOfTheSamePositionsDisabled(t *testing.T) {
	cfg := testConfig(t)
	ctx := context.Background()
	dir := t.TempDir()
	agents := `
agents:
  - name: gold
    exec: go run ./agents/trendagent
    args: [-mode live -exchange mt5 -pair XAUUSDm -api localhost:5555]
  - name: gold-15m
    exec: go run ./agents/trendagent
    args: [-mode live -exchange mt5 -pair XAUUSDm -timeframe 15m -api localhost:5556]
  - name: gold-own
    exec: go run ./agents/trendagent
    args: [-mode live -exchange mt5 -pair XAUUSDm -mt5-comment nb:own -api localhost:5557]
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents.yml"), []byte(agents), 0o600))
	var out bytes.Buffer
	require.NoError(t, runImport(ctx, cfg, []string{"-agents", filepath.Join(dir, "agents.yml")}, &out))
	require.Contains(t, out.String(), "agent gold-15m imported disabled: store: already exists: agent gold-15m: "+
		"the agent gold, enabled in live mode, trades XAUUSDm")

	s := testStore(t, cfg)
	list, err := s.Agents(ctx)
	require.NoError(t, err)
	enabled := map[string]bool{}
	for _, a := range list {
		enabled[a.Name] = a.Enabled
	}
	require.Equal(t, map[string]bool{"gold": true, "gold-15m": false, "gold-own": true}, enabled)
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

// importArgs returns the args of the import of the previous version of dir.
func importArgs(dir string, flags ...string) []string {
	return append([]string{"-agents", filepath.Join(dir, "agents.yml"), "-env", filepath.Join(dir, "automator.env")},
		flags...)
}

// panelAgent creates on the dashboard an LLM agent named name, of a pair of
// its own, before any import.
func panelAgent(t *testing.T, s *store.Store, name string) store.Agent {
	t.Helper()
	agent, err := s.CreateAgent(context.Background(), store.Agent{Name: name, Kind: "llm", Mode: "live",
		APIAddress: "localhost:5590", Launched: true, Params: store.Params{"pair": "EURUSDm"},
		Account: &store.Account{BridgeURL: "http://127.0.0.1:18812"},
		LLM:     &store.LLMSettings{Provider: "openai", Model: "gpt-5"}}, store.AgentSecrets{})
	require.NoError(t, err)
	return agent
}

// deleteAgent disables and deletes the agent of name, as the dashboard does.
func deleteAgent(t *testing.T, s *store.Store, name string) {
	t.Helper()
	ctx := context.Background()
	agent, err := s.AgentByName(ctx, name)
	require.NoError(t, err)
	off := false
	_, err = s.UpdateAgent(ctx, agent.ID, store.AgentUpdate{Enabled: &off})
	require.NoError(t, err)
	require.NoError(t, s.DeleteAgent(ctx, agent.ID))
}

// deleteUser deletes the user of the dashboard named username.
func deleteUser(t *testing.T, s *store.Store, username string) {
	t.Helper()
	ctx := context.Background()
	users, err := s.Users(ctx)
	require.NoError(t, err)
	for _, u := range users {
		if u.Username == username {
			require.NoError(t, s.DeleteUser(ctx, u.ID))
			return
		}
	}
	t.Fatalf("no user %s", username)
}

// telegramIDs returns the Telegram users authorized.
func telegramIDs(t *testing.T, s *store.Store) []int64 {
	t.Helper()
	users, err := s.TelegramUsers(context.Background())
	require.NoError(t, err)
	ids := make([]int64, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.TelegramID)
	}
	return ids
}

// orderSummary is an order by what tells it apart.
type orderSummary struct {
	ID, ExchangeID int64
	Pair           string
	Side           model.SideType
}

// ordersOf returns the orders of the agent of id, by ID.
func ordersOf(t *testing.T, s *store.Store, id int64) []orderSummary {
	t.Helper()
	orders, err := s.Orders(context.Background(), id, 0, 1000)
	require.NoError(t, err)
	list := make([]orderSummary, 0, len(orders))
	for _, o := range orders {
		list = append(list, orderSummary{ID: o.ID, ExchangeID: o.ExchangeID, Pair: o.Pair, Side: o.Side})
	}
	return list
}

// What the user deleted or changed on the dashboard after an import stays as
// it is when the import runs again, with -force too: the agent deleted isn't
// created again, the user deleted doesn't come back with its old password,
// the Telegram user revoked isn't authorized again, the bot stays off.
func TestImport_AgainNeverBringsBackWhatWasDeleted(t *testing.T) {
	cfg := testConfig(t)
	dir := legacyDir(t)
	ctx := context.Background()
	s := testStore(t, cfg)
	require.NoError(t, runImport(ctx, cfg, importArgs(dir), io.Discard))
	llmAgent, err := s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	for source, details := range map[string]store.Params{"agent:llm": {"agent_id": strconv.FormatInt(llmAgent.ID, 10)},
		"user:xtrader": nil, "env:TELEGRAM": {"users": "2", "enabled": "true"}} {
		imported, err := s.LegacyImportOf(ctx, source)
		require.NoError(t, err, "kept as imported: %s", source)
		for name, value := range details {
			require.Equal(t, value, imported.Details[name], source)
		}
	}

	deleteAgent(t, s, "llm")
	_, err = s.CreateUser(ctx, "admin", "another strong password")
	require.NoError(t, err)
	deleteUser(t, s, "xtrader")
	users, err := s.TelegramUsers(ctx)
	require.NoError(t, err)
	require.Len(t, users, 2)
	require.NoError(t, s.DeleteTelegramUser(ctx, users[0].ID), "111 revoked")
	off := false
	require.NoError(t, s.SetTelegram(ctx, &off, nil))

	for _, flags := range [][]string{nil, {"-force"}} {
		var out bytes.Buffer
		require.NoError(t, runImport(ctx, cfg, importArgs(dir, flags...), &out), flags)
		report := out.String()
		for _, want := range []string{"agent llm skipped: deleted on the dashboard", "dashboard: user xtrader " +
			"skipped: deleted on the dashboard", "telegram: left as it is"} {
			require.Contains(t, report, want, flags)
		}

		_, err = s.AgentByName(ctx, "llm")
		require.ErrorIs(t, err, store.ErrNotFound, "never created again")
		agents, err := s.Agents(ctx)
		require.NoError(t, err)
		require.Len(t, agents, 1)
		require.Equal(t, "matrend-gold", agents[0].Name)
		_, err = s.Authenticate(ctx, "xtrader", "a strong password")
		require.Error(t, err, "the user deleted stays deleted")
		require.Equal(t, []int64{222}, telegramIDs(t, s), "111 stays revoked")
		settings, hasToken, err := s.Telegram(ctx)
		require.NoError(t, err)
		require.False(t, settings.Enabled, "the bot stays off")
		require.True(t, hasToken)
	}
}

// What the dashboard configured before the first import stays as it is: its
// Telegram (token, users), and the agent and the user it deleted.
func TestImport_KeepsWhatTheDashboardConfiguredBefore(t *testing.T) {
	cfg := testConfig(t)
	dir := legacyDir(t)
	ctx := context.Background()
	s := testStore(t, cfg)
	on, token := true, "999:panel-token"
	require.NoError(t, s.SetTelegram(ctx, &on, &token))
	_, err := s.AddTelegramUser(ctx, 333, "panel")
	require.NoError(t, err)
	_, err = s.CreateUser(ctx, "admin", "another strong password")
	require.NoError(t, err)
	_, err = s.CreateUser(ctx, "XTrader", "the password of the panel")
	require.NoError(t, err)
	deleteUser(t, s, "XTrader")
	panelAgent(t, s, "llm")
	deleteAgent(t, s, "llm")

	var out bytes.Buffer
	require.NoError(t, runImport(ctx, cfg, importArgs(dir), &out))
	require.Contains(t, out.String(), "agent llm skipped: deleted on the dashboard")
	require.Contains(t, out.String(), "telegram: left as it is")

	_, err = s.AgentByName(ctx, "llm")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = s.AgentByName(ctx, "matrend-gold")
	require.NoError(t, err)
	_, err = s.Authenticate(ctx, "xtrader", "a strong password")
	require.Error(t, err)
	got, err := s.TelegramToken(ctx)
	require.NoError(t, err)
	require.Equal(t, token, got, "the token of the dashboard kept")
	require.Equal(t, []int64{333}, telegramIDs(t, s))
}

// An agent created on the dashboard that ordered already: the orders of the
// previous version would take the IDs of its own (one of them dropped, the
// decisions linked to its order). Refused, nothing written; with -force, the
// IDs of the orders imported are moved past the ones of the agent, the links
// of their decisions with them, once.
func TestImport_IntoAnAgentWithOrdersRefusedUnlessForced(t *testing.T) {
	cfg := testConfig(t)
	dir := legacyDir(t)
	ctx := context.Background()
	s := testStore(t, cfg)
	agent := panelAgent(t, s, "llm")
	first, _, err := s.LeaseOrderIDs(ctx, agent.ID, 1)
	require.NoError(t, err)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{{ID: first, ExchangeID: 77, Pair: "EURUSDm",
		Side: model.SideTypeBuy, Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 1.17,
		Quantity: 0.1, CreatedAt: at, UpdatedAt: at}}, true))
	own := orderSummary{ID: 1, ExchangeID: 77, Pair: "EURUSDm", Side: model.SideTypeBuy}

	var out bytes.Buffer
	for _, flags := range [][]string{{"-dry-run"}, nil} {
		out.Reset()
		err = runImport(ctx, cfg, importArgs(dir, flags...), &out)
		require.ErrorContains(t, err, "1 conflict, nothing imported", flags)
		require.Contains(t, out.String(), "Nothing imported, because of the conflicts below", flags)
		require.Contains(t, out.String(), "agent llm: it has orders already", flags)
		require.Contains(t, out.String(), "-force", flags)
	}
	agents, err := s.Agents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 1, "nothing written")
	count, err := s.CountUsers(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
	require.Equal(t, []orderSummary{own}, ordersOf(t, s, agent.ID))
	decisions, err := s.Decisions(ctx, agent.ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, decisions)

	// -force: past the order of the agent
	for range 2 {
		out.Reset()
		require.NoError(t, runImport(ctx, cfg, importArgs(dir, "-force"), &out))
		require.Equal(t, []orderSummary{own,
			{ID: 2, ExchangeID: 6144628423280988838, Pair: "XAUUSDm", Side: model.SideTypeSell},
			{ID: 3, ExchangeID: 2, Pair: "XAUUSDm", Side: model.SideTypeBuy},
			{ID: 4, ExchangeID: 3, Pair: "BTCUSDm", Side: model.SideTypeBuy}}, ordersOf(t, s, agent.ID))
		links, err := s.DecisionLinks(ctx, agent.ID)
		require.NoError(t, err)
		require.Equal(t, map[int64]string{2: "20260928T123000.000000-XAUUSDm", 3: "20260928T123000.000000-XAUUSDm"},
			links, "the decision linked to the orders it placed, never to the one of the agent")
		moved, err := s.Decision(ctx, agent.ID, "20260928T130000.000000-XAUUSDm")
		require.NoError(t, err)
		require.Equal(t, []int64{3}, moved.OrderIDs)
	}
	require.Contains(t, out.String(), "0 written, 3 kept already", "the second time")
	next, _, err := s.LeaseOrderIDs(ctx, agent.ID, 1)
	require.NoError(t, err)
	require.EqualValues(t, 5, next, "after the orders imported")
}

// The orders of a file imported before, imported again with -force after the
// agent took the next IDs: an order added to the file since, at an ID the
// agent used, is a conflict, never dropped, nothing written.
func TestImport_ForcedAgainRefusesTheIDsOfOtherOrders(t *testing.T) {
	cfg := testConfig(t)
	dir := legacyDir(t)
	ctx := context.Background()
	s := testStore(t, cfg)
	require.NoError(t, runImport(ctx, cfg, importArgs(dir), io.Discard))
	agent, err := s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	first, _, err := s.LeaseOrderIDs(ctx, agent.ID, 1)
	require.NoError(t, err)
	require.EqualValues(t, 4, first)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{{ID: first, ExchangeID: 77, Pair: "EURUSDm",
		Side: model.SideTypeBuy, Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 1.17,
		Quantity: 0.1, CreatedAt: at, UpdatedAt: at}}, true))
	before := ordersOf(t, s, agent.ID)

	legacy, err := storage.FromFile(filepath.Join(dir, "data", "nb-llm.db"))
	require.NoError(t, err)
	require.NoError(t, legacy.CreateOrder(&model.Order{ExchangeID: 4, Pair: "XAUUSDm", Side: model.SideTypeBuy,
		Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 4160, Quantity: 1, CreatedAt: at,
		UpdatedAt: at}))

	var out bytes.Buffer
	err = runImport(ctx, cfg, importArgs(dir, "-force"), &out)
	require.ErrorContains(t, err, "1 conflict")
	require.Contains(t, out.String(), "agent llm: the orders 4 of")
	require.Equal(t, before, ordersOf(t, s, agent.ID), "nothing written")
}

// A database imported by the versions that kept the files imported without
// their agent ID nor the offset of their order IDs: imported again with
// -force into the same agent, which ordered since, its orders and decisions
// are kept as they are, never imported twice; into an agent of the same name
// created after that import, they are moved past its own orders.
func TestImport_ForcedAgainAfterAnImportOfAnOlderVersion(t *testing.T) {
	cfg := testConfig(t)
	dir := legacyDir(t)
	ctx := context.Background()
	s := testStore(t, cfg)
	require.NoError(t, runImport(ctx, cfg, importArgs(dir), io.Discard))
	data := filepath.Join(dir, "data")
	older := map[string]store.Params{
		"nb-llm.db":              {"agent": "llm", "orders": "3"},
		"nb-llm.decisions.db":    {"agent": "llm", "decisions": "2", "attachments": "1"},
		"nb-llm.decisions.jsonl": {"agent": "llm", "entries": "2"},
		"nb-matrend.db":          {"agent": "matrend-gold", "orders": "1"},
	}
	for file, details := range older {
		require.NoError(t, s.MarkLegacyImported(ctx, filepath.Join(data, file), details))
	}
	agent, err := s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	first, _, err := s.LeaseOrderIDs(ctx, agent.ID, 1)
	require.NoError(t, err)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	require.NoError(t, s.SaveOrders(ctx, agent.ID, []model.Order{{ID: first, ExchangeID: 77, Pair: "EURUSDm",
		Side: model.SideTypeBuy, Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 1.17,
		Quantity: 0.1, CreatedAt: at, UpdatedAt: at}}, true))
	before := ordersOf(t, s, agent.ID)
	require.Len(t, before, 4)
	links, err := s.DecisionLinks(ctx, agent.ID)
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runImport(ctx, cfg, importArgs(dir, "-force"), &out))
	require.Contains(t, out.String(), "3 orders of "+filepath.Join(data, "nb-llm.db")+": 0 written, 3 kept already")
	require.NotContains(t, out.String(), "moved by")
	require.Equal(t, before, ordersOf(t, s, agent.ID), "never twice")
	again, err := s.DecisionLinks(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, links, again)

	// the agent deleted, another one of its name created on the dashboard:
	// past its orders
	for file, details := range older {
		require.NoError(t, s.MarkLegacyImported(ctx, filepath.Join(data, file), details))
	}
	deleteAgent(t, s, "llm")
	other := panelAgent(t, s, "llm")
	first, _, err = s.LeaseOrderIDs(ctx, other.ID, 1)
	require.NoError(t, err)
	require.NoError(t, s.SaveOrders(ctx, other.ID, []model.Order{{ID: first, ExchangeID: 77, Pair: "EURUSDm",
		Side: model.SideTypeBuy, Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 1.17,
		Quantity: 0.1, CreatedAt: at, UpdatedAt: at}}, true))
	out.Reset()
	require.NoError(t, runImport(ctx, cfg, importArgs(dir, "-force"), &out))
	require.Contains(t, out.String(), "moved by 1")
	require.Equal(t, []orderSummary{{ID: 1, ExchangeID: 77, Pair: "EURUSDm", Side: model.SideTypeBuy},
		{ID: 2, ExchangeID: 6144628423280988838, Pair: "XAUUSDm", Side: model.SideTypeSell},
		{ID: 3, ExchangeID: 2, Pair: "XAUUSDm", Side: model.SideTypeBuy},
		{ID: 4, ExchangeID: 3, Pair: "BTCUSDm", Side: model.SideTypeBuy}}, ordersOf(t, s, other.ID))
}

// The decisions of an agent imported without the file of their orders: the
// IDs of the orders they link to are never the ones of the next orders of the
// agent.
func TestImport_DecisionsWithoutTheirOrdersKeepTheirIDs(t *testing.T) {
	cfg := testConfig(t)
	dir := legacyDir(t)
	require.NoError(t, os.Remove(filepath.Join(dir, "data", "nb-llm.db")))
	ctx := context.Background()
	s := testStore(t, cfg)
	require.NoError(t, runImport(ctx, cfg, importArgs(dir), io.Discard))
	agent, err := s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	require.Empty(t, ordersOf(t, s, agent.ID))
	first, _, err := s.LeaseOrderIDs(ctx, agent.ID, 1)
	require.NoError(t, err)
	require.EqualValues(t, 3, first, "past the orders 1 and 2 of the decisions")
}

// B13: a database imported by a version before the import of Telegram was
// kept (no env:TELEGRAM), with TELEGRAM_TOKEN alone: its token cleared on the
// dashboard since, the bot off and no user, is never written again.
func TestImport_AgainAfterAnOlderVersionLeavesTheTelegramCleared(t *testing.T) {
	cfg := testConfig(t)
	dir := legacyDir(t)
	env := strings.Replace(legacyEnv, "TELEGRAM_USER=111,222\n", "", 1)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "automator.env"), []byte(env), 0o600))
	ctx := context.Background()
	s := testStore(t, cfg)
	// what that version left: the token, the bot off without users, and the
	// records of the data files, without their agent ID
	off, token := false, "123:telegram-token"
	require.NoError(t, s.SetTelegram(ctx, &off, &token))
	require.NoError(t, s.MarkLegacyImported(ctx, filepath.Join(dir, "data", "nb-llm.decisions.jsonl"),
		store.Params{"agent": "llm", "entries": "2"}))
	cleared := ""
	require.NoError(t, s.SetTelegram(ctx, nil, &cleared), "the token cleared on the dashboard")

	for _, flags := range [][]string{nil, {"-force"}} {
		var out bytes.Buffer
		require.NoError(t, runImport(ctx, cfg, importArgs(dir, flags...), &out), flags)
		require.Contains(t, out.String(), "telegram: left as it is, imported by a version before", flags)
		settings, hasToken, err := s.Telegram(ctx)
		require.NoError(t, err)
		require.False(t, hasToken, "never written again")
		require.False(t, settings.Enabled)
		require.Empty(t, telegramIDs(t, s))
	}
}

// addLegacyOrder adds to the orders file of llm of dir an order of the
// previous version placed after its import, at its next ID.
func addLegacyOrder(t *testing.T, dir string) {
	t.Helper()
	legacy, err := storage.FromFile(filepath.Join(dir, "data", "nb-llm.db"))
	require.NoError(t, err)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	require.NoError(t, legacy.CreateOrder(&model.Order{ExchangeID: 4, Pair: "XAUUSDm", Side: model.SideTypeBuy,
		Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 4160, Quantity: 1, CreatedAt: at,
		UpdatedAt: at}))
}

// B18: the orders of a file imported before, imported again with -force after
// the agent leased the next IDs, its orders at them not written yet (eg: in
// its outbox): an order added to the file since, at one of them, is a
// conflict, nothing written, -dry-run and the records of the versions before
// too; into an agent that leased none since, it is written.
func TestImport_ForcedAgainRefusesTheIDsLeasedToTheAgent(t *testing.T) {
	cfg := testConfig(t)
	dir := legacyDir(t)
	ctx := context.Background()
	s := testStore(t, cfg)
	require.NoError(t, runImport(ctx, cfg, importArgs(dir), io.Discard))
	agent, err := s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	first, last, err := s.LeaseOrderIDs(ctx, agent.ID, 10)
	require.NoError(t, err)
	require.EqualValues(t, 4, first)
	before := ordersOf(t, s, agent.ID)
	require.Len(t, before, 3, "none of the agent written yet")
	addLegacyOrder(t, dir)

	data := filepath.Join(dir, "data")
	for _, flags := range [][]string{{"-force", "-dry-run"}, {"-force"}, {"-force", "older"}} {
		if flags[len(flags)-1] == "older" {
			// the records of the versions that kept neither the agent nor the
			// last ID of an import
			flags = flags[:len(flags)-1]
			for file, details := range map[string]store.Params{"nb-llm.db": {"agent": "llm", "orders": "3"},
				"nb-llm.decisions.db":    {"agent": "llm", "decisions": "2", "attachments": "1"},
				"nb-llm.decisions.jsonl": {"agent": "llm", "entries": "2"}} {
				require.NoError(t, s.MarkLegacyImported(ctx, filepath.Join(data, file), details))
			}
		}
		var out bytes.Buffer
		err = runImport(ctx, cfg, importArgs(dir, flags...), &out)
		require.ErrorContains(t, err, "1 conflict, nothing imported", flags)
		require.Contains(t, out.String(), fmt.Sprintf("agent llm: the orders 4 of %s would take IDs leased to the "+
			"agent since the import of the file (4 to %d)", filepath.Join(data, "nb-llm.db"), last), flags)
		require.Equal(t, before, ordersOf(t, s, agent.ID), "nothing written", flags)
	}
	ids, err := s.LockOrderIDs(ctx, agent.ID)
	require.NoError(t, err)
	require.EqualValues(t, last+1, ids.Next, "the lease of the agent as it was")
}

// B18: into an agent that leased no ID since the import of its data, an order
// added to the file since is written past them, and so is a decision added
// linking to it; a decision added linking to an ID leased to the agent since
// is a conflict.
func TestImport_ForcedAgainPastTheIDsOfTheAgent(t *testing.T) {
	cfg := testConfig(t)
	dir := legacyDir(t)
	ctx := context.Background()
	s := testStore(t, cfg)
	require.NoError(t, runImport(ctx, cfg, importArgs(dir), io.Discard))
	agent, err := s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	addLegacyOrder(t, dir)
	data := filepath.Join(dir, "data")
	addDecision := func(id string, orders ...int64) {
		db, err := buntdb.Open(filepath.Join(data, "nb-llm.decisions.db"))
		require.NoError(t, err)
		d := legacyDecision{Decision: ui.Decision{ID: id, Pair: "XAUUSDm", Timeframe: "30m", Trigger: "candle",
			Model: "openai gpt-5", Action: "ENTER_LONG", Outcome: "applied", OrderIDs: orders}}
		value, err := json.Marshal(d)
		require.NoError(t, err)
		require.NoError(t, db.Update(func(tx *buntdb.Tx) error {
			_, _, err := tx.Set("decision:"+d.ID, string(value), nil)
			return err
		}))
		require.NoError(t, db.Close())
	}
	addDecision("20261001T090000.000000-XAUUSDm", 4)

	var out bytes.Buffer
	require.NoError(t, runImport(ctx, cfg, importArgs(dir, "-force"), &out))
	require.Contains(t, out.String(), "4 orders of "+filepath.Join(data, "nb-llm.db")+": 1 written, 3 kept already")
	require.Contains(t, out.String(), "3 decisions of "+filepath.Join(data, "nb-llm.decisions.db")+": 1 written")
	require.Len(t, ordersOf(t, s, agent.ID), 4)
	links, err := s.DecisionLinks(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, "20261001T090000.000000-XAUUSDm", links[4])

	// the agent leases the next IDs; a decision added linking to one of them
	first, last, err := s.LeaseOrderIDs(ctx, agent.ID, 10)
	require.NoError(t, err)
	require.EqualValues(t, 5, first)
	addDecision("20261001T100000.000000-XAUUSDm", 6)
	out.Reset()
	err = runImport(ctx, cfg, importArgs(dir, "-force"), &out)
	require.ErrorContains(t, err, "1 conflict")
	require.Contains(t, out.String(), fmt.Sprintf("the decisions of %s would link to IDs leased to the agent since "+
		"the import of its data (5 to %d), to its orders: 20261001T100000.000000-XAUUSDm to the order 6",
		filepath.Join(data, "nb-llm.decisions.db"), last))
	_, err = s.Decision(ctx, agent.ID, "20261001T100000.000000-XAUUSDm")
	require.ErrorIs(t, err, store.ErrNotFound, "nothing written")
}
