package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/raykavin/xtrader/config"
)

// The migration 21 gives each LLM agent the switch of its reanalysis, the one
// it had: on in paper, whose control panel is on in every channel, and in live
// with its trade panel or its Telegram control on; the agents of the other
// kinds get none. The migration down drops the switches only, and up again
// gives them back from the switches of the channels.
func TestMigration21_TheReanalysisTheAgentsHad(t *testing.T) {
	dsn := testDatabase(t)
	migratedTo(t, dsn, 20)
	s, err := Open(config.Database{DSN: dsn, MaxOpenConns: 2, LogLevel: "silent"}, testKey)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	// the agents of before, written as the store of version 20 wrote them
	before := []struct {
		name, kind, mode  string
		panel, telegram   bool
		allowed, switched bool // the switch after the migration, and whether the agent has one
	}{
		{"llm-panel", "llm", "live", true, false, true, true},
		{"llm-telegram", "llm", "live", false, true, true, true},
		{"llm-both", "llm", "live", true, true, true, true},
		{"llm-none", "llm", "live", false, false, false, true},
		{"llm-paper", "llm", "paper", false, false, true, true},
		{"trend-both", "trend", "live", true, true, false, false},
		{"breakout-paper", "breakout", "paper", false, false, false, false},
	}
	for i, a := range before {
		require.NoError(t, s.db.Exec("INSERT INTO agents (name, kind, mode, api_address, trade_panel, "+
			"telegram_control) VALUES (?, ?, ?, ?, ?, ?)", a.name, a.kind, a.mode, fmt.Sprintf("localhost:%d", 5600+i),
			a.panel, a.telegram).Error)
	}

	check := func(when string) {
		t.Helper()
		for _, want := range before {
			agent, err := s.AgentByName(ctx, want.name)
			require.NoError(t, err)
			require.Equal(t, want.allowed, agent.ReanalysisAllowed(), "%s: %s", when, want.name)
			require.Equal(t, want.switched, agent.Reanalysis != nil, "%s: %s has a switch", when, want.name)
			require.Equal(t, want.panel, agent.TradePanel, "%s: %s: its trade panel kept", when, want.name)
			require.Equal(t, want.telegram, agent.TelegramControl, "%s: %s: its Telegram control kept", when,
				want.name)
		}
	}
	migrated(t, dsn)
	require.NoError(t, s.CheckSchema(ctx))
	check("migrated")

	run := func(name string) {
		data, err := os.ReadFile(filepath.Join("..", "sql", "migrations", "000021_create_agent_reanalysis."+name+
			".sql"))
		require.NoError(t, err)
		require.NoError(t, s.db.Transaction(func(tx *gorm.DB) error { // a statement at a time, as prepared
			for _, statement := range strings.Split(string(data), ";\n") {
				if strings.TrimSpace(statement) != "" {
					if err := tx.Exec(statement).Error; err != nil {
						return err
					}
				}
			}
			return nil
		}), name)
	}
	run("down")
	var agents, tables int64
	require.NoError(t, s.db.Table("agents").Count(&agents).Error)
	require.EqualValues(t, len(before), agents, "the agents kept")
	require.NoError(t, s.db.Raw("SELECT count(*) FROM information_schema.tables WHERE table_name = "+
		"'agent_reanalysis'").Scan(&tables).Error)
	require.Zero(t, tables, "the switches dropped")
	run("up")
	check("up again")
}

// An LLM agent is created with the switch of its reanalysis, off unless asked
// on; one of another kind gets none, refused on.
func TestCreateAgent_TheReanalysisOfAnLLMAgentOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	llm := func(name string, port int, reanalysis *AgentReanalysis) (Agent, error) {
		return s.CreateAgent(ctx, Agent{Name: name, Kind: "llm", Mode: "live", APIAddress: fmt.Sprintf("localhost:%d",
			port), Launched: true, Reanalysis: reanalysis, Account: &Account{BridgeURL: "http://127.0.0.1:18812",
			Comment: ptr("nb:" + name)}, LLM: &LLMSettings{Provider: "openai", Model: "gpt-5"}}, AgentSecrets{})
	}
	trend := func(name string, port int, reanalysis *AgentReanalysis) (Agent, error) {
		return s.CreateAgent(ctx, Agent{Name: name, Kind: "trend", Mode: "live", APIAddress: fmt.Sprintf(
			"localhost:%d", port), Launched: true, Reanalysis: reanalysis, Params: Params{"strategy": "matrend"},
			Account: &Account{BridgeURL: "http://127.0.0.1:18812", Comment: ptr("nb:" + name)}}, AgentSecrets{})
	}
	switches := func(agent Agent) int64 {
		var count int64
		require.NoError(t, s.db.Table("agent_reanalysis").Where("agent_id = ?", agent.ID).Count(&count).Error)
		return count
	}

	on, err := llm("llm-on", 5601, &AgentReanalysis{Allowed: true})
	require.NoError(t, err)
	require.True(t, on.ReanalysisAllowed())
	require.False(t, on.TradePanel, "apart from its trade panel")
	require.False(t, on.TelegramControl, "and from its Telegram control")

	off, err := llm("llm-off", 5602, nil)
	require.NoError(t, err)
	require.False(t, off.ReanalysisAllowed(), "off unless asked on")
	require.NotNil(t, off.Reanalysis)
	require.EqualValues(t, 1, switches(off), "its switch written, off")

	_, err = trend("trend-on", 5603, &AgentReanalysis{Allowed: true})
	require.ErrorIs(t, err, ErrInvalid)
	require.ErrorContains(t, err, "an agent of kind trend has none")
	plain, err := trend("trend-off", 5604, &AgentReanalysis{})
	require.NoError(t, err)
	require.False(t, plain.ReanalysisAllowed())
	require.Nil(t, plain.Reanalysis)
	require.Zero(t, switches(plain), "no switch for an agent without a language model")
}

// The reanalysis of an LLM agent switches apart from its trade panel and its
// Telegram control, while it runs (a switch: the other settings unchecked),
// kept as set; the one of an agent of another kind can't be switched on.
func TestUpdateAgent_TheReanalysisApartFromTheChannels(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm") // its trade panel on, its reanalysis off
	require.True(t, agent.TradePanel)
	require.False(t, agent.ReanalysisAllowed())

	for _, tc := range []struct {
		panel, reanalysis bool
	}{{true, true}, {false, true}, {false, false}, {true, false}} {
		updated, err := s.UpdateAgent(ctx, agent.ID, AgentUpdate{TradePanel: ptr(tc.panel)})
		require.NoError(t, err)
		require.Equal(t, tc.panel, updated.TradePanel)
		updated, err = s.UpdateAgent(ctx, agent.ID, AgentUpdate{Reanalysis: ptr(tc.reanalysis)})
		require.NoError(t, err)
		require.Equal(t, tc.reanalysis, updated.ReanalysisAllowed(), "panel %v", tc.panel)
		require.Equal(t, tc.panel, updated.TradePanel, "the trade panel left as it was")

		// as the controller reads it again, eg: after a restart
		for _, got := range []func() (Agent, error){
			func() (Agent, error) { return s.Agent(ctx, agent.ID) },
			func() (Agent, error) { return s.AgentByName(ctx, "llm") },
			func() (Agent, error) {
				agents, err := s.Agents(ctx)
				if err != nil || len(agents) != 1 {
					return Agent{}, fmt.Errorf("agents %d: %w", len(agents), err)
				}
				return agents[0], nil
			},
		} {
			read, err := got()
			require.NoError(t, err)
			require.Equal(t, tc.reanalysis, read.ReanalysisAllowed(), "kept: panel %v", tc.panel)
			require.Equal(t, tc.panel, read.TradePanel, "kept: panel %v", tc.panel)
		}
	}

	// the reanalysis set with the other switches, and with a change of the
	// settings, checked then
	updated, err := s.UpdateAgent(ctx, agent.ID, AgentUpdate{Reanalysis: ptr(true), TelegramControl: ptr(true),
		Params: Params{"pair": "XAUUSDm"}})
	require.NoError(t, err)
	require.True(t, updated.ReanalysisAllowed())
	require.True(t, updated.TelegramControl)
	require.Equal(t, "XAUUSDm", updated.Params["pair"])
	updated, err = s.UpdateAgent(ctx, agent.ID, AgentUpdate{Params: Params{"pair": "BTCUSDm"}})
	require.NoError(t, err)
	require.True(t, updated.ReanalysisAllowed(), "left as it was")

	// an LLM agent without its switch, eg: written by hand, gets it
	require.NoError(t, s.db.Exec("DELETE FROM agent_reanalysis WHERE agent_id = ?", agent.ID).Error)
	updated, err = s.Agent(ctx, agent.ID)
	require.NoError(t, err)
	require.False(t, updated.ReanalysisAllowed(), "none: not allowed")
	updated, err = s.UpdateAgent(ctx, agent.ID, AgentUpdate{Reanalysis: ptr(true)})
	require.NoError(t, err)
	require.True(t, updated.ReanalysisAllowed())

	trend, err := s.CreateAgent(ctx, Agent{Name: "matrend", Kind: "trend", Mode: "live", APIAddress: "localhost:5570",
		Launched: true, Params: Params{"strategy": "matrend"}}, AgentSecrets{})
	require.NoError(t, err)
	_, err = s.UpdateAgent(ctx, trend.ID, AgentUpdate{Reanalysis: ptr(true)})
	require.ErrorIs(t, err, ErrInvalid)
	updated, err = s.UpdateAgent(ctx, trend.ID, AgentUpdate{Reanalysis: ptr(false), TelegramControl: ptr(true)})
	require.NoError(t, err, "off: nothing to switch off")
	require.True(t, updated.TelegramControl)
	require.Nil(t, updated.Reanalysis)
	var count int64
	require.NoError(t, s.db.Table("agent_reanalysis").Where("agent_id = ?", trend.ID).Count(&count).Error)
	require.Zero(t, count)

	// a switch of another kind written by hand never lets it reanalyze, nor
	// keeps its settings from changing
	require.NoError(t, s.db.Exec("INSERT INTO agent_reanalysis (agent_id, allowed) VALUES (?, TRUE)", trend.ID).Error)
	updated, err = s.Agent(ctx, trend.ID)
	require.NoError(t, err)
	require.False(t, updated.ReanalysisAllowed())
	updated, err = s.UpdateAgent(ctx, trend.ID, AgentUpdate{Params: Params{"strategy": "matrend", "pair": "XAUUSDm"}})
	require.NoError(t, err)
	require.Equal(t, "XAUUSDm", updated.Params["pair"])
}

// Reanalysis is a setting of its own, never a param: the flag of the agent
// comes from its switch.
func TestCreateAgent_TheReanalysisIsNoParam(t *testing.T) {
	s := testStore(t)
	_, err := s.CreateAgent(context.Background(), Agent{Name: "llm", Kind: "llm", Mode: "paper",
		APIAddress: "localhost:5580", Launched: true, Params: Params{"reanalysis": "true"},
		LLM: &LLMSettings{Provider: "openai", Model: "gpt-5"}}, AgentSecrets{})
	require.ErrorIs(t, err, ErrInvalid)
	require.ErrorContains(t, err, "param reanalysis is a setting of its own")
}
