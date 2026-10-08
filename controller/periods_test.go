package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// periodOf decodes the period answered in body.
func periodOf(t *testing.T, body string) ui.AgentPeriod {
	t.Helper()
	var period ui.AgentPeriod
	require.NoError(t, json.Unmarshal([]byte(body), &period), body)
	return period
}

// The reset of the results of an agent starts a period, the earlier ones kept,
// each ending as the next one starts; the dashboard lists the agent with its
// current one.
func TestSettingsAPI_ResetTheResults(t *testing.T) {
	c := newManaged(t)
	llm := c.agent(t, "llm", "llm", false)
	c.agent(t, "matrend", "trend", false)
	call, srv := settingsSession(t, c)

	resp, body := call(http.MethodGet, "/api/settings/agents/llm/periods", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.JSONEq(t, `{"periods": []}`, body, "none before the first reset")

	before := time.Now().Add(-time.Second).Unix()
	resp, body = call(http.MethodPost, "/api/settings/agents/llm/periods",
		ui.AgentPeriodRequest{Deposit: 1000, Currency: "brl"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	first := periodOf(t, body)
	assert.InDelta(t, 1000, first.Deposit, 1e-9)
	assert.Equal(t, "BRL", first.Currency)
	assert.Equal(t, "admin", first.CreatedBy)
	assert.GreaterOrEqual(t, first.StartedAt, before)
	assert.Nil(t, first.EndedAt, "the current one")

	resp, body = call(http.MethodPost, "/api/settings/agents/llm/periods",
		ui.AgentPeriodRequest{Deposit: 250.5, Currency: "USD"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	second := periodOf(t, body)

	resp, body = call(http.MethodGet, "/api/settings/agents/llm/periods", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var periods ui.AgentPeriodsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &periods))
	require.Len(t, periods.Periods, 2, "the earlier one kept")
	assert.Equal(t, second.ID, periods.Periods[0].ID, "the current one first")
	assert.Nil(t, periods.Periods[0].EndedAt)
	assert.Equal(t, first.ID, periods.Periods[1].ID)
	require.NotNil(t, periods.Periods[1].EndedAt)
	assert.Equal(t, second.StartedAt, *periods.Periods[1].EndedAt, "ended as the next one started")
	resp, body = call(http.MethodGet, "/api/settings/agents/matrend/periods", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"periods": []}`, body, "the periods of the agent only")

	// the dashboard counts the trades closed since the current one
	require.NoError(t, c.dashboard.SetAgents([]Agent{{Name: "llm", Address: llm.APIAddress},
		{Name: "matrend", Address: "localhost:15507"}}))
	resp, body = call(http.MethodGet, "/api/agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var agents ui.AgentsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &agents))
	require.Len(t, agents.Agents, 2)
	require.NotNil(t, agents.Agents[0].Period)
	assert.Equal(t, second, *agents.Agents[0].Period)
	assert.Nil(t, agents.Agents[1].Period, "never reset: all its trades count")
	since, ok := c.dashboard.currentPeriod(context.Background(), "llm") // the start of the /profit of Telegram
	assert.True(t, ok)
	assert.Equal(t, second.StartedAt, since.Unix())
	_, ok = c.dashboard.currentPeriod(context.Background(), "matrend")
	assert.False(t, ok)

	for _, tc := range []struct {
		name string
		body interface{}
		want int
	}{
		{"a negative deposit", ui.AgentPeriodRequest{Deposit: -1, Currency: "BRL"}, http.StatusBadRequest},
		{"no currency", ui.AgentPeriodRequest{Deposit: 10}, http.StatusBadRequest},
		{"not a currency", ui.AgentPeriodRequest{Deposit: 10, Currency: "REAL"}, http.StatusBadRequest},
		{"not JSON", "{", http.StatusBadRequest},
	} {
		resp, body = call(http.MethodPost, "/api/settings/agents/llm/periods", tc.body)
		assert.Equal(t, tc.want, resp.StatusCode, "%s: %s", tc.name, body)
	}
	resp, _ = call(http.MethodPost, "/api/settings/agents/nobody/periods", ui.AgentPeriodRequest{Deposit: 10,
		Currency: "BRL"})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = call(http.MethodGet, "/api/settings/agents/nobody/periods", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	resp, body = call(http.MethodGet, "/api/settings/agents/llm/periods", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.Unmarshal([]byte(body), &periods))
	assert.Len(t, periods.Periods, 2, "nothing kept of the refusals")

	assertProtected(t, srv, http.MethodPost, "/api/settings/agents/llm/periods", `{"deposit":1,"currency":"BRL"}`)
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/settings/agents/llm/periods", "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the periods without a session")
}
