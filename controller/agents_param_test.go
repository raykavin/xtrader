//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The global parametrizer of the agents: a param changed on the agents
// holding it, enabled or not, each agent's other params kept, the ones
// without it left; added to them too when asked; the agents enabled restarted
// when asked, the disabled ones left stopped; the LLM agents only, the
// enabled ones only, or both, the others untouched and never restarted; a key
// the params of an agent refuse refused before anything changes; the edition
// of an agent alone unchanged.
func TestSettings_AgentsParam(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	c.agent(t, "gold", "trend", true)
	c.agent(t, "silver", "trend", false)
	c.agent(t, "llm", "llm", true, func(a *store.Agent) { delete(a.Params, "lot") })
	t.Setenv("XTRADER_SECRETS_KEY", "the-secrets-key")
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool {
		return c.manager.supervisor.Running("gold") && c.manager.supervisor.Running("llm")
	}, 10*time.Second, 50*time.Millisecond)
	call, _ := settingsSession(t, c)
	set := func(request ui.AgentsParamRequest) (int, ui.AgentsParamResponse, string) {
		t.Helper()
		resp, body := call(http.MethodPost, "/api/settings/agents/params", request)
		var response ui.AgentsParamResponse
		if resp.StatusCode == http.StatusOK {
			require.NoError(t, json.Unmarshal([]byte(body), &response))
		}
		return resp.StatusCode, response, body
	}
	byAgent := func(response ui.AgentsParamResponse) map[string]ui.AgentsParamResult {
		results := make(map[string]ui.AgentsParamResult)
		for _, result := range response.Results {
			results[result.Agent] = result
		}
		return results
	}
	params := func(name string) store.Params {
		t.Helper()
		a, err := c.store.AgentByName(ctx, name)
		require.NoError(t, err)
		return a.AllParams()
	}

	// the agents holding it only, the disabled one too
	status, response, body := set(ui.AgentsParamRequest{Key: " lot ", Value: " 0.02 "})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "lot", response.Key)
	assert.Equal(t, "0.02", response.Value, "trimmed")
	assert.Equal(t, map[string]ui.AgentsParamResult{
		"gold":   {Agent: "gold", Enabled: true, Status: ui.ParamUpdated, Previous: "0.01"},
		"silver": {Agent: "silver", Status: ui.ParamUpdated, Previous: "0.01"},
		"llm":    {Agent: "llm", Enabled: true, Status: ui.ParamSkipped, Detail: "sem o parâmetro"},
	}, byAgent(response))
	assert.Equal(t, store.Params{"pair": "XAUUSDm", "lot": "0.02"}, params("gold"), "its other params kept")
	assert.NotContains(t, params("llm"), "lot")
	assert.True(t, c.manager.State("gold").RestartPending, "applied at its restart")
	assert.False(t, c.manager.supervisor.Running("silver"))

	// the same value again: nothing written
	_, response, _ = set(ui.AgentsParamRequest{Key: "lot", Value: "0.02"})
	assert.Equal(t, ui.ParamUnchanged, byAgent(response)["gold"].Status)

	// added where missing, then every agent enabled restarted, the disabled one
	// left stopped
	status, response, body = set(ui.AgentsParamRequest{Key: "lot", Value: "0.03", AddMissing: true,
		RestartActive: true})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, map[string]ui.AgentsParamResult{
		"gold":   {Agent: "gold", Enabled: true, Status: ui.ParamUpdated, Previous: "0.02", Restart: ui.ParamRestarted},
		"silver": {Agent: "silver", Status: ui.ParamUpdated, Previous: "0.02"},
		"llm":    {Agent: "llm", Enabled: true, Status: ui.ParamAdded, Restart: ui.ParamRestarted},
	}, byAgent(response))
	assert.Equal(t, "0.03", params("llm")["lot"])
	assert.Equal(t, "1", params("llm")["llm-min-stop-atr"], "the params of its model kept")
	require.Eventually(t, func() bool {
		return c.manager.supervisor.Running("gold") && c.manager.supervisor.Running("llm")
	}, 10*time.Second, 50*time.Millisecond)
	assert.False(t, c.manager.State("gold").RestartPending, "restarted with it")
	assert.False(t, c.manager.State("llm").RestartPending)
	assert.False(t, c.manager.supervisor.Running("silver"), "disabled: left stopped")

	// a param of a language model added to the LLM agent only
	_, response, _ = set(ui.AgentsParamRequest{Key: "llm-consult-interval", Value: "15m", AddMissing: true})
	results := byAgent(response)
	assert.Equal(t, ui.ParamAdded, results["llm"].Status)
	assert.Equal(t, ui.AgentsParamResult{Agent: "gold", Enabled: true, Status: ui.ParamSkipped,
		Detail: "parâmetro só para agentes LLM"}, results["gold"])

	// the LLM agents only: the others untouched, never restarted
	gold := c.manager.State("gold").PID
	status, response, body = set(ui.AgentsParamRequest{Key: "timeframe", Value: "30m", AddMissing: true,
		RestartActive: true, LLMOnly: true})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, map[string]ui.AgentsParamResult{
		"gold":   {Agent: "gold", Enabled: true, Status: ui.ParamExcluded},
		"silver": {Agent: "silver", Status: ui.ParamExcluded},
		"llm":    {Agent: "llm", Enabled: true, Status: ui.ParamAdded, Restart: ui.ParamRestarted},
	}, byAgent(response))
	assert.NotContains(t, params("gold"), "timeframe")
	assert.Equal(t, gold, c.manager.State("gold").PID, "out of the filter: not restarted")

	// the enabled agents only: the disabled one untouched
	status, response, body = set(ui.AgentsParamRequest{Key: "lot", Value: "0.04", EnabledOnly: true})
	require.Equal(t, http.StatusOK, status, body)
	results = byAgent(response)
	assert.Equal(t, ui.ParamUpdated, results["gold"].Status)
	assert.Equal(t, ui.ParamUpdated, results["llm"].Status)
	assert.Equal(t, ui.ParamExcluded, results["silver"].Status)
	assert.Equal(t, "0.03", params("silver")["lot"], "disabled: untouched")

	// both: the LLM agents enabled
	_, response, _ = set(ui.AgentsParamRequest{Key: "lot", Value: "0.05", LLMOnly: true, EnabledOnly: true})
	results = byAgent(response)
	assert.Equal(t, []string{ui.ParamExcluded, ui.ParamUpdated, ui.ParamExcluded},
		[]string{results["gold"].Status, results["llm"].Status, results["silver"].Status})

	// refused before anything changes
	for _, refused := range []ui.AgentsParamRequest{
		{Key: "", Value: "1"},
		{Key: "Lot", Value: "1"},
		{Key: "mode", Value: "paper"},
		{Key: "lot", Value: "lot=0.04"},
	} {
		status, _, body := set(refused)
		assert.Equal(t, http.StatusBadRequest, status, body)
	}
	_, body = call(http.MethodPost, "/api/settings/agents/params", ui.AgentsParamRequest{Key: "mode", Value: "paper"})
	assert.Contains(t, body, "O parâmetro mode tem campo próprio")
	assert.Equal(t, "0.04", params("gold")["lot"], "nothing changed by a refusal")

	// an agent alone, as before
	resp, body := call(http.MethodPatch, "/api/settings/agents/gold",
		ui.AgentSettingsRequest{Params: map[string]string{"pair": "XAUUSDm", "lot": "0.05"}})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Equal(t, store.Params{"pair": "XAUUSDm", "lot": "0.05"}, params("gold"))
}
