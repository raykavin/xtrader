package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A param set on one agent at a time, by its name: changed on an agent
// holding it, its other params and settings kept; left on one without it,
// added when asked; nothing written when the agent has the value already; a
// param of a language model never added to an agent of another kind; a name
// or a value the params of an agent refuse refused before anything changes.
func TestSetAgentParam(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	llm := llmAgent(t, s, "llm")
	trend, err := s.CreateAgent(ctx, Agent{Name: "trend", Kind: "trend", Enabled: true, Mode: "paper",
		APIAddress: "localhost:5590", Launched: true, Params: Params{"pair": "XAUUSDm", "lot": "0.01"}},
		AgentSecrets{})
	require.NoError(t, err)
	params := func(a Agent) Params {
		t.Helper()
		got, err := s.Agent(ctx, a.ID)
		require.NoError(t, err)
		return got.AllParams()
	}

	set, err := s.SetAgentParam(ctx, trend.ID, "lot", "0.02", false)
	require.NoError(t, err)
	assert.Equal(t, ParamSet{Status: ParamUpdated, Had: "0.01"}, set)
	assert.Equal(t, Params{"pair": "XAUUSDm", "lot": "0.02"}, params(trend), "the others kept")

	set, err = s.SetAgentParam(ctx, llm.ID, "lot", "0.02", false)
	require.NoError(t, err)
	assert.Equal(t, ParamSet{Status: ParamSkipped, Detail: "sem o parâmetro"}, set)
	assert.NotContains(t, params(llm), "lot")

	set, err = s.SetAgentParam(ctx, llm.ID, "lot", "0.02", true)
	require.NoError(t, err)
	assert.Equal(t, ParamSet{Status: ParamAdded}, set)
	assert.Equal(t, Params{"pair": "BTCUSDm,XAUUSDm", "timeframe": "30m", "llm-min-stop-atr": "1", "lot": "0.02"},
		params(llm))
	got, err := s.Agent(ctx, llm.ID)
	require.NoError(t, err)
	assert.Equal(t, "gpt-5", got.LLM.Model, "its settings kept")
	assert.Equal(t, Params{"llm-min-stop-atr": "1"}, got.LLM.Params, "the ones of its model kept with it")

	set, err = s.SetAgentParam(ctx, trend.ID, "lot", "0.02", true)
	require.NoError(t, err)
	assert.Equal(t, ParamSet{Status: ParamUnchanged, Had: "0.02"}, set)

	set, err = s.SetAgentParam(ctx, trend.ID, "llm-min-stop-atr", "2", true)
	require.NoError(t, err)
	assert.Equal(t, ParamSet{Status: ParamSkipped, Detail: "parâmetro só para agentes LLM"}, set)
	assert.NotContains(t, params(trend), "llm-min-stop-atr")

	set, err = s.SetAgentParam(ctx, llm.ID, "llm-min-stop-atr", "2", false)
	require.NoError(t, err)
	assert.Equal(t, ParamSet{Status: ParamUpdated, Had: "1"}, set)
	got, err = s.Agent(ctx, llm.ID)
	require.NoError(t, err)
	assert.Equal(t, Params{"llm-min-stop-atr": "2"}, got.LLM.Params, "kept with its model")

	for _, refused := range []struct{ name, value string }{
		{"Lot", "1"},
		{"-lot", "1"},
		{"", "1"},
		{"mode", "live"},
		{"lot", "0.01\n0.02"},
		{"lot", "lot = 0.03"},
		{"lot", string(make([]byte, maxParamValue+1))},
	} {
		_, err := s.SetAgentParam(ctx, trend.ID, refused.name, refused.value, true)
		assert.ErrorIs(t, err, ErrInvalid, "%q=%q", refused.name, refused.value)
		assert.ErrorIs(t, CheckParam(refused.name, refused.value), ErrInvalid)
	}
	assert.Equal(t, Params{"pair": "XAUUSDm", "lot": "0.02"}, params(trend), "nothing changed by a refusal")
	assert.ErrorContains(t, CheckParam("mode", "live"), "o parâmetro mode tem campo próprio")
	assert.NoError(t, CheckParam("lot", ""), "an empty value, as in the form of an agent")
}
