//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// rulesOf returns the agents per asset of rules, at revision.
func rulesOf(t *testing.T, revision int64, rules ...ui.AssetAgentRule) store.AssetAgents {
	t.Helper()
	data, err := json.Marshal(rules)
	require.NoError(t, err)
	return store.AssetAgents{Rules: data, Revision: revision}
}

// The entries of an agent narrowed by the rules of the agents per asset: its
// assets of a rule without it left out, with why; within the assets of the
// window open; none of its assets left, its entries blocked, never an empty
// list (all of them); the revision of the rules added.
func TestAssetEntries(t *testing.T) {
	trend := store.Agent{Name: "trend", Params: store.Params{"pair": "XAUUSDm;BTCUSDm"}}
	gold := rulesOf(t, 3, ui.AssetAgentRule{Pairs: []string{"XAUUSDm"}, Agents: []string{"orb", "gold"}})

	got := assetEntries(agentapi.Entries{EntriesRevision: 10}, store.AssetAgents{}, trend)
	assert.Equal(t, agentapi.Entries{EntriesRevision: 10}, got, "no rule: as it was")

	got = assetEntries(agentapi.Entries{EntriesRevision: 10}, gold, trend)
	assert.Equal(t, []string{"BTCUSDm"}, got.EntriesPairs, "the asset of the rule left out")
	assert.Equal(t, "Agentes por ativo: XAUUSDm só para os agentes orb, gold", got.PairsReason)
	assert.EqualValues(t, 13, got.EntriesRevision, "the revision of the rules added")
	assert.False(t, got.EntriesBlocked)

	window := agentapi.Entries{EntriesPairs: []string{"XAUUSDm", "BTCUSDm"}, PairsReason: "Janela 08:00–12:00 só para os ativos XAUUSDm, BTCUSDm"}
	got = assetEntries(window, gold, trend)
	assert.Equal(t, []string{"BTCUSDm"}, got.EntriesPairs, "within the window")
	assert.Equal(t, "Janela 08:00–12:00 só para os ativos XAUUSDm, BTCUSDm; Agentes por ativo: XAUUSDm só para os agentes orb, gold",
		got.PairsReason)

	onlyGold := agentapi.Entries{EntriesPairs: []string{"XAUUSDm"}, PairsReason: "Janela 08:00–12:00 só para o ativo XAUUSDm"}
	got = assetEntries(onlyGold, gold, trend)
	assert.True(t, got.EntriesBlocked, "the window's asset not allowed: none left")
	assert.Nil(t, got.EntriesPairs, "never an empty list, which allows them all")
	assert.Contains(t, got.EntriesReason, "Nenhum ativo permitido")

	both := rulesOf(t, 1, ui.AssetAgentRule{Pairs: []string{"XAUUSDm", "BTCUSDm"}, Agents: []string{"orb"}})
	got = assetEntries(agentapi.Entries{EntriesBlocked: true, EntriesReason: "Bloqueado por admin"}, both, trend)
	assert.True(t, got.EntriesBlocked)
	assert.Equal(t, "Bloqueado por admin; Nenhum ativo permitido. Agentes por ativo: XAUUSDm só para o agente orb; "+
		"BTCUSDm só para o agente orb", got.EntriesReason)

	orb := store.Agent{Name: "orb", Params: store.Params{"pair": "XAUUSDm"}}
	got = assetEntries(agentapi.Entries{}, both, orb)
	assert.Empty(t, got.EntriesPairs, "allowed on all of its assets")
	assert.False(t, got.EntriesBlocked)
}

// The agents per asset through the API: read and set by the users allowed
// (see routePermissions), 400 for rules refused; set, the runtime of each
// agent tells the assets it may open positions on at once.
func TestAssetAgents_ThroughTheAPI(t *testing.T) {
	c := newManaged(t)
	c.agent(t, "trend", "trend", false, func(a *store.Agent) { a.Params["pair"] = "XAUUSDm;BTCUSDm" })
	c.agent(t, "orb", "trend", false, func(a *store.Agent) { a.Params["pair"] = "XAUUSDm" })
	call, _ := settingsSession(t, c)
	ctx := context.Background()

	resp, body := call(http.MethodGet, "/api/settings/asset-agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var got ui.AssetAgentsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, []ui.AssetAgentRule{}, got.Settings.Rules, "none while never set")

	resp, body = call(http.MethodPut, "/api/settings/asset-agents", ui.AssetAgentsRequest{Settings: ui.AssetAgentsSettings{
		Rules: []ui.AssetAgentRule{{Pairs: []string{"XAUUSDm"}, Agents: []string{"ninguem"}}}}})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
	assert.Contains(t, body, "o agente ninguem não existe")

	resp, body = call(http.MethodPut, "/api/settings/asset-agents", ui.AssetAgentsRequest{Settings: ui.AssetAgentsSettings{
		Rules: []ui.AssetAgentRule{{Pairs: []string{" XAUUSDm "}, Agents: []string{"orb "}}}}})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &got))
	assert.Equal(t, []ui.AssetAgentRule{{Pairs: []string{"XAUUSDm"}, Agents: []string{"orb"}}}, got.Settings.Rules,
		"trimmed")
	assert.Equal(t, "admin", got.Settings.UpdatedBy)

	trend, err := c.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	entries, err := entriesOf(ctx, c.store, trend)
	require.NoError(t, err)
	assert.Equal(t, []string{"BTCUSDm"}, entries.EntriesPairs, "trend only on BTCUSDm")
	assert.Contains(t, entries.PairsReason, "XAUUSDm só para o agente orb")
	orb, err := c.store.AgentByName(ctx, "orb")
	require.NoError(t, err)
	entries, err = entriesOf(ctx, c.store, orb)
	require.NoError(t, err)
	assert.Empty(t, entries.EntriesPairs, "orb on all of its assets")
	assert.False(t, entries.EntriesBlocked)

	resp, body = call(http.MethodPut, "/api/settings/asset-agents", ui.AssetAgentsRequest{Settings: ui.AssetAgentsSettings{
		Rules: []ui.AssetAgentRule{{Pairs: []string{"XAUUSDm", "BTCUSDm"}, Agents: []string{"orb"}}}}})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	entries, err = entriesOf(ctx, c.store, trend)
	require.NoError(t, err)
	assert.True(t, entries.EntriesBlocked, "trend allowed on none of its assets")
	assert.Contains(t, entries.EntriesReason, "Nenhum ativo permitido")
}
