//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// exec runs statement on the database of the controller, eg: settings saved
// before the store refused them.
func (c *managed) exec(t *testing.T, statement string, args ...interface{}) {
	t.Helper()
	db, err := gorm.Open(postgres.Open(c.dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	conn, err := db.DB()
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, db.Exec(statement, args...).Error)
}

// sharesGold makes the agent of id trade the positions of the agents of the
// default comment of its kind, as saved before the store refused it.
func (c *managed) sharesGold(t *testing.T, id int64) {
	t.Helper()
	c.exec(t, "UPDATE agent_accounts SET comment = NULL WHERE agent_id = ?", id)
}

// Two agents enabled in live mode of the same positions on MT5, saved before
// the store refused them: the first one is launched, the second isn't while
// the first runs, its state telling why; a paper agent of the same positions
// is, as it reads the market data only.
func TestManager_DoesNotLaunchTwoAgentsOfTheSamePositions(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	c.agent(t, "gold", "trend", true)
	second := c.agent(t, "gold-two", "trend", true, func(a *store.Agent) { a.Account.Comment = secret("nb:two") })
	c.sharesGold(t, second.ID)
	c.agent(t, "paper", "trend", true, func(a *store.Agent) { a.Mode = "paper" })

	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool {
		output := c.stdout.String()
		return strings.Contains(output, "[gold] started") && strings.Contains(output, "[paper] started")
	}, 5*time.Second, 10*time.Millisecond)
	assert.False(t, c.manager.supervisor.Running("gold-two"))
	state := c.manager.State("gold-two")
	assert.Contains(t, state.LaunchError, "O agente gold, em execução no modo live, opera XAUUSDm com "+
		"o magic 980556666 (comentário \"nb:trendconfluence\")")
	assert.Empty(t, c.manager.State("gold").LaunchError)
	assert.Empty(t, c.manager.State("paper").LaunchError)
	require.NoError(t, c.manager.Reconcile(ctx))
	assert.False(t, c.manager.supervisor.Running("gold-two"), "never while the other runs")

	// the settings tell it
	call, _ := settingsSession(t, c)
	resp, body := call(http.MethodGet, "/api/settings/agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var list ui.AgentSettingsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	require.Len(t, list.Agents, 3)
	for _, a := range list.Agents {
		if a.Name == "gold-two" {
			assert.Equal(t, state.LaunchError, a.LaunchError)
		} else {
			assert.Empty(t, a.LaunchError, a.Name)
		}
	}

	// the first one disabled and stopped, the second one launches
	gold, err := c.store.AgentByName(ctx, "gold")
	require.NoError(t, err)
	_, err = c.store.UpdateAgent(ctx, gold.ID, store.AgentUpdate{Enabled: ptrTo(false)})
	require.NoError(t, err)
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool {
		_ = c.manager.Reconcile(ctx)
		return c.manager.supervisor.Running("gold-two")
	}, 10*time.Second, 50*time.Millisecond)
	assert.False(t, c.manager.supervisor.Running("gold"))
	assert.Empty(t, c.manager.State("gold-two").LaunchError)

	// a restart of the first one, enabled again, is refused in turn
	_, err = c.store.UpdateAgent(ctx, gold.ID, store.AgentUpdate{Enabled: ptrTo(true)})
	require.ErrorIs(t, err, store.ErrConflict, "the store refuses it now")
	c.exec(t, "UPDATE agents SET enabled = TRUE WHERE id = ?", gold.ID)
	err = c.manager.Restart(ctx, "gold")
	require.ErrorContains(t, err, "O agente gold-two, em execução no modo live")
	assert.False(t, c.manager.supervisor.Running("gold"))
	assert.Equal(t, err.Error(), c.manager.State("gold").LaunchError)
}

// An agent started again by the supervisor reads the settings of its launch:
// what it trades, changed meanwhile, waits for its restart, never the
// positions of another agent running. The settings read anew, of an agent
// the controller didn't launch, are refused when it would trade the positions
// of another agent running, and taken for a paper agent.
func TestInternalAPI_RefusesTheSettingsOfTheSamePositions(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	c.agent(t, "gold", "trend", true)
	second := c.agent(t, "gold-two", "trend", true, func(a *store.Agent) { a.Account.Comment = secret("nb:two") })
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool {
		return c.manager.supervisor.Running("gold") && c.manager.supervisor.Running("gold-two")
	}, 5*time.Second, 10*time.Millisecond)
	srv := httptest.NewServer(c.manager.InternalHandler())
	t.Cleanup(srv.Close)
	token, err := c.manager.tokens.issue(second.ID)
	require.NoError(t, err)

	var cfg agentapi.Config
	require.Equal(t, http.StatusOK, internalCall(t, srv, token, http.MethodGet, "/config", nil, &cfg).StatusCode)
	assert.Equal(t, "nb:two", cfg.MT5Book().Comment)

	c.sharesGold(t, second.ID) // changed meanwhile, as saved before the store refused it
	require.Equal(t, http.StatusOK, internalCall(t, srv, token, http.MethodGet, "/config", nil, &cfg).StatusCode)
	assert.Equal(t, "nb:two", cfg.MT5Book().Comment, "the settings of its launch")
	assert.Empty(t, c.manager.State("gold-two").LaunchError)

	// read anew once it stopped
	c.manager.stop("gold-two")
	token, err = c.manager.tokens.issue(second.ID)
	require.NoError(t, err)
	resp := internalCall(t, srv, token, http.MethodGet, "/config", nil, nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Contains(t, c.manager.State("gold-two").LaunchError, "O agente gold, em execução no modo live, opera XAUUSDm")

	c.exec(t, "UPDATE agents SET mode = 'paper' WHERE id = ?", second.ID)
	require.Equal(t, http.StatusOK, internalCall(t, srv, token, http.MethodGet, "/config", nil, &cfg).StatusCode,
		"a paper agent reads the market data only")
	assert.Empty(t, c.manager.State("gold-two").LaunchError)

	// what it trades is the one of the settings it runs with, of its launch:
	// launched in paper, changed to live meanwhile, it still reads paper, and
	// the agent of the same positions launches
	paper, err := c.store.AgentByName(ctx, "gold-two")
	require.NoError(t, err)
	require.NoError(t, c.manager.start(paper))
	require.Eventually(t, func() bool { return c.manager.supervisor.Running("gold-two") }, 5*time.Second,
		10*time.Millisecond)
	c.exec(t, "UPDATE agents SET mode = 'live' WHERE id = ?", second.ID)
	token, err = c.manager.tokens.issue(second.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, internalCall(t, srv, token, http.MethodGet, "/config", nil, &cfg).StatusCode,
		"its start reads the settings of its launch")
	assert.Equal(t, "paper", cfg.Mode)
	assert.Empty(t, c.manager.State("gold-two").LaunchError)
	c.manager.stop("gold")
	gold, err := c.store.AgentByName(ctx, "gold")
	require.NoError(t, err)
	require.NoError(t, c.manager.start(gold), "the other one trades in paper")
}

// The settings refuse an agent trading the positions of another, created,
// enabled or restarted.
func TestSettingsAPI_RefusesTheSamePositions(t *testing.T) {
	c := newManaged(t)
	c.agent(t, "gold", "llm", true)
	c.agent(t, "gold-two", "llm", false)
	running := c.agent(t, "trend", "trend", true)
	restarted := c.agent(t, "trend-two", "trend", false)
	call, _ := settingsSession(t, c)
	require.NoError(t, c.manager.Reconcile(context.Background()))
	require.Eventually(t, func() bool { return c.manager.supervisor.Running(running.Name) }, 5*time.Second,
		10*time.Millisecond)
	c.exec(t, "UPDATE agents SET enabled = TRUE WHERE id = ?", restarted.ID) // saved before the store refused it
	resp, body := call(http.MethodPost, "/api/settings/agents/trend-two/restart", nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)
	assert.Contains(t, body, "O agente trend, em execução no modo live, opera XAUUSDm")
	assert.False(t, c.manager.supervisor.Running(restarted.Name))

	resp, body = call(http.MethodPost, "/api/settings/agents", ui.AgentCreateRequest{Name: "gold-three",
		Kind: "llm", Mode: "live", APIAddress: "localhost:15900", Enabled: true,
		Params:  map[string]string{"pair": "BTCUSDm;XAUUSDm"},
		Account: ui.AgentAccountSettings{BridgeURL: "http://localhost:18812"},
		LLM:     &ui.AgentLLMSettings{Provider: "openai", Model: "gpt-5"}})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)
	assert.Contains(t, body, "agent gold-three: the agent gold, enabled in live mode, trades XAUUSDm")

	resp, body = call(http.MethodPatch, "/api/settings/agents/gold-two", ui.AgentSettingsRequest{
		Enabled: boolPtr(true)})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)
	assert.Contains(t, body, "agent gold-two: the agent gold, enabled in live mode, trades XAUUSDm")

	resp, body = call(http.MethodPatch, "/api/settings/agents/gold-two", ui.AgentSettingsRequest{
		Enabled: boolPtr(true), Account: &ui.AgentAccountRequest{Comment: secret("nb:two")}})
	assert.Equal(t, http.StatusOK, resp.StatusCode, body)
}
