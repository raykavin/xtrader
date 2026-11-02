//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The reanalysis of an LLM agent is a setting of its own on the dashboard:
// switched apart from its trade panel, each combination kept, applied while
// the agent runs (its runtime, never a restart), refused for an agent of
// another kind and for one that doesn't exist, and only to a user logged in
// with the CSRF token.
func TestSettingsAPI_TheReanalysisOfAnAgent(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	llm := c.agent(t, "llm", "llm", true)
	c.agent(t, "matrend", "trend", true)
	call, srv := settingsSession(t, c)
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return c.manager.State("llm").Running }, 5*time.Second,
		10*time.Millisecond)
	token, err := c.manager.tokens.issue(llm.ID)
	require.NoError(t, err)
	internal := httptest.NewServer(c.manager.InternalHandler())
	t.Cleanup(internal.Close)
	runtime := func() agentapi.Runtime {
		t.Helper()
		var doc agentapi.Runtime
		resp := internalCall(t, internal, token, http.MethodGet, "/runtime", nil, &doc)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		return doc
	}
	patch := func(name string, request any) (int, ui.AgentSettings, string) {
		t.Helper()
		resp, body := call(http.MethodPatch, "/api/settings/agents/"+name, request)
		var settings ui.AgentSettings
		if resp.StatusCode == http.StatusOK {
			settings = agentSettingsOf(t, body)
		}
		return resp.StatusCode, settings, body
	}
	listed := func(name string) ui.AgentSettings {
		t.Helper()
		resp, body := call(http.MethodGet, "/api/settings/agents", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		var list ui.AgentSettingsResponse
		require.NoError(t, json.Unmarshal([]byte(body), &list))
		for _, a := range list.Agents {
			if a.Name == name {
				return a
			}
		}
		t.Fatalf("no agent %s", name)
		return ui.AgentSettings{}
	}
	assert.False(t, listed("llm").Reanalysis, "created off")
	assert.False(t, runtime().Reanalysis)

	// without a session, and without the CSRF token: nothing changed
	assertProtected(t, srv, http.MethodPatch, "/api/settings/agents/llm", `{"reanalysis":true}`)
	assert.False(t, listed("llm").Reanalysis)

	// each combination of the trade panel and the reanalysis, kept as set and
	// applied while the agent runs
	for _, tc := range []struct{ panel, reanalysis bool }{{true, true}, {false, true}, {true, false}, {false, false}} {
		name := fmt.Sprintf("panel %v, reanalysis %v", tc.panel, tc.reanalysis)
		before := runtime().Version
		status, _, body := patch("llm", ui.AgentSettingsRequest{TradePanel: boolPtr(tc.panel)})
		require.Equal(t, http.StatusOK, status, body)
		status, settings, body := patch("llm", ui.AgentSettingsRequest{Reanalysis: boolPtr(tc.reanalysis)})
		require.Equal(t, http.StatusOK, status, body)
		assert.Equal(t, tc.reanalysis, settings.Reanalysis, name)
		assert.Equal(t, tc.panel, settings.TradePanel, "%s: the trade panel left as it was", name)
		assert.True(t, settings.Running, name)
		assert.False(t, settings.RestartPending, "%s: applied while it runs", name)

		stored, err := c.store.AgentByName(ctx, "llm")
		require.NoError(t, err)
		assert.Equal(t, tc.reanalysis, stored.ReanalysisAllowed(), "%s: kept", name)
		assert.Equal(t, tc.panel, stored.TradePanel, "%s: kept", name)
		listedLLM := listed("llm")
		assert.Equal(t, tc.reanalysis, listedLLM.Reanalysis, name)
		assert.Equal(t, tc.panel, listedLLM.TradePanel, name)

		doc := runtime()
		assert.Equal(t, tc.reanalysis, doc.Reanalysis, "%s: its runtime", name)
		assert.Equal(t, tc.panel, doc.TradePanel, "%s: its runtime", name)
		assert.Greater(t, doc.Version, before, "%s: a change the agent waiting is woken by", name)
	}

	// its launch: the flag of its settings
	_, _, _ = patch("llm", ui.AgentSettingsRequest{Reanalysis: boolPtr(true)})
	var cfg agentapi.Config
	require.Equal(t, http.StatusOK, internalCall(t, internal, token, http.MethodGet, "/config", nil, &cfg).StatusCode)
	assert.True(t, cfg.Reanalysis)
	assert.False(t, cfg.TradePanel, "apart from the trade panel")

	// refused: an agent of another kind, and one that doesn't exist
	status, _, body := patch("matrend", ui.AgentSettingsRequest{Reanalysis: boolPtr(true)})
	assert.Equal(t, http.StatusBadRequest, status, body)
	assert.Contains(t, body, "é do tipo trend e não tem LLM")
	assert.False(t, listed("matrend").Reanalysis)
	status, matrend, body := patch("matrend", ui.AgentSettingsRequest{Reanalysis: boolPtr(false)})
	assert.Equal(t, http.StatusOK, status, "off: nothing to switch off: %s", body)
	assert.False(t, matrend.Reanalysis)
	status, _, body = patch("nobody", ui.AgentSettingsRequest{Reanalysis: boolPtr(true)})
	assert.Equal(t, http.StatusNotFound, status, body)
	assert.Contains(t, body, "agente nobody não existe")

	// created with it, an LLM agent only
	resp, body := call(http.MethodPost, "/api/settings/agents", ui.AgentCreateRequest{Name: "gold", Kind: "llm",
		Mode: "paper", APIAddress: "localhost:15610", Reanalysis: true,
		Account: ui.AgentAccountSettings{BridgeURL: "http://127.0.0.1:18812", Comment: secret("nb:gold")},
		LLM:     &ui.AgentLLMSettings{Provider: "openai", Model: "gpt-5"}})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	gold := agentSettingsOf(t, body)
	assert.True(t, gold.Reanalysis)
	assert.False(t, gold.TradePanel)
	resp, body = call(http.MethodPost, "/api/settings/agents", ui.AgentCreateRequest{Name: "silver", Kind: "llm",
		Mode: "paper", APIAddress: "localhost:15611",
		Account: ui.AgentAccountSettings{BridgeURL: "http://127.0.0.1:18812", Comment: secret("nb:silver")},
		LLM:     &ui.AgentLLMSettings{Provider: "openai", Model: "gpt-5"}})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	assert.False(t, agentSettingsOf(t, body).Reanalysis, "off unless asked on")
	resp, body = call(http.MethodPost, "/api/settings/agents", ui.AgentCreateRequest{Name: "emas", Kind: "trend",
		Mode: "paper", APIAddress: "localhost:15612", Reanalysis: true, Params: map[string]string{"strategy": "emacross"},
		Account: ui.AgentAccountSettings{BridgeURL: "http://127.0.0.1:18812", Comment: secret("nb:emas")}})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
	assert.Contains(t, body, "é do tipo trend e não tem LLM")
	assert.Len(t, agentNames(t, call), 4, "the trend agent not created")
	assertProtected(t, srv, http.MethodPost, "/api/settings/agents", `{"name":"x","kind":"llm","reanalysis":true}`)
}

// The list of the agents of the dashboard tells the kind of each one, so that
// it lays out the cards of an LLM agent as it loads.
func TestDashboard_TheKindOfTheAgents(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	c.agent(t, "llm", "llm", true)
	c.agent(t, "matrend", "trend", true)
	call, _ := settingsSession(t, c)
	require.NoError(t, c.manager.Reconcile(ctx))

	resp, body := call(http.MethodGet, "/api/agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var list ui.AgentsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	kinds := map[string]string{}
	for _, a := range list.Agents {
		kinds[a.Name] = a.Kind
	}
	assert.Equal(t, map[string]string{"llm": "llm", "matrend": "trend"}, kinds)
}

// The list of the agents of the dashboard tells the pairs of each one, as its
// settings tell them (the default pair without any), so that the dashboard
// links to an agent on one of them.
func TestDashboard_ThePairsOfTheAgents(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	c.agent(t, "highlow", "breakout", true, func(a *store.Agent) { a.Params["pair"] = "XAUUSDm; BTCUSDm" })
	c.agent(t, "mtfema", "trend", true, func(a *store.Agent) { delete(a.Params, "pair") })
	call, _ := settingsSession(t, c)
	require.NoError(t, c.manager.Reconcile(ctx))

	resp, body := call(http.MethodGet, "/api/agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var list ui.AgentsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	pairs := map[string][]string{}
	for _, a := range list.Agents {
		pairs[a.Name] = a.Pairs
	}
	assert.Equal(t, map[string][]string{"highlow": {"XAUUSDm", "BTCUSDm"}, "mtfema": {agentapi.DefaultPair}}, pairs)
}
