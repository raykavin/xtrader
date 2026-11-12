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

// Several params set on an agent at once, under one lock: each told as it
// went (changed, added, the value it had already, skipped), its other params
// kept, written once; one refused refuses them all, nothing changed on it.
func TestSetAgentParams(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	trend, err := s.CreateAgent(ctx, Agent{Name: "trend", Kind: "trend", Enabled: true, Mode: "paper",
		APIAddress: "localhost:5590", Launched: true,
		Params: Params{"pair": "XAUUSDm", "lot": "0.01", "timeframe": "15m"}}, AgentSecrets{})
	require.NoError(t, err)
	params := func() Params {
		t.Helper()
		got, err := s.Agent(ctx, trend.ID)
		require.NoError(t, err)
		return got.AllParams()
	}

	sets, err := s.SetAgentParams(ctx, trend.ID, []ParamChange{
		{Name: "pair", Value: "XAUUSDm;BTCUSDm"},
		{Name: "lot", Value: "XAUUSDm=0.01;BTCUSDm=0.02"},
		{Name: "timeframe", Value: "15m"},
		{Name: "stop-atr", Value: "2"},
		{Name: "llm-min-stop-atr", Value: "1"},
	}, true)
	require.NoError(t, err)
	assert.Equal(t, []ParamSet{
		{Status: ParamUpdated, Had: "XAUUSDm"},
		{Status: ParamUpdated, Had: "0.01"},
		{Status: ParamUnchanged, Had: "15m"},
		{Status: ParamAdded},
		{Status: ParamSkipped, Detail: "parâmetro só para agentes LLM"},
	}, sets)
	assert.Equal(t, Params{"pair": "XAUUSDm;BTCUSDm", "lot": "XAUUSDm=0.01;BTCUSDm=0.02", "timeframe": "15m",
		"stop-atr": "2"}, params())

	sets, err = s.SetAgentParams(ctx, trend.ID, []ParamChange{{Name: "pair", Value: "XAUUSDm;BTCUSDm"},
		{Name: "lot", Value: "XAUUSDm=0.01;BTCUSDm=0.02"}}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{ParamUnchanged, ParamUnchanged}, []string{sets[0].Status, sets[1].Status},
		"nothing to write")

	_, err = s.SetAgentParams(ctx, trend.ID, []ParamChange{{Name: "lot", Value: "0.05"},
		{Name: "mode", Value: "live"}}, true)
	assert.ErrorIs(t, err, ErrInvalid)
	assert.Equal(t, "XAUUSDm=0.01;BTCUSDm=0.02", params()["lot"], "one refused: none changed")
}

// Params removed from an agent at once, by their names: with any value, or
// only where it holds the value asked; skipped on the agent without them or
// holding another value; the ones of its language model too; its other params
// and settings kept; nothing written when none is removed; a name the params
// of an agent refuse refused before anything changes.
func TestRemoveAgentParams(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	llm := llmAgent(t, s, "llm")
	trend, err := s.CreateAgent(ctx, Agent{Name: "trend", Kind: "trend", Enabled: true, Mode: "paper",
		APIAddress: "localhost:5590", Launched: true,
		Params: Params{"pair": "XAUUSDm", "lot": "0.01", "timeframe": "30m", "stop-atr": "2"}}, AgentSecrets{})
	require.NoError(t, err)
	params := func(a Agent) Params {
		t.Helper()
		got, err := s.Agent(ctx, a.ID)
		require.NoError(t, err)
		return got.AllParams()
	}

	sets, err := s.RemoveAgentParams(ctx, trend.ID, []ParamChange{
		{Name: "timeframe", Value: "15m"},
		{Name: "stop-atr"},
		{Name: "take-profit-atr"},
	})
	require.NoError(t, err)
	assert.Equal(t, []ParamSet{
		{Status: ParamSkipped, Had: "30m", Detail: "com outro valor (30m)"},
		{Status: ParamRemoved, Had: "2"},
		{Status: ParamSkipped, Detail: "sem o parâmetro"},
	}, sets)
	assert.Equal(t, Params{"pair": "XAUUSDm", "lot": "0.01", "timeframe": "30m"}, params(trend), "the others kept")

	set, err := s.RemoveAgentParams(ctx, trend.ID, []ParamChange{{Name: "timeframe", Value: "30m"}})
	require.NoError(t, err)
	assert.Equal(t, []ParamSet{{Status: ParamRemoved, Had: "30m"}}, set, "the value asked")
	assert.Equal(t, Params{"pair": "XAUUSDm", "lot": "0.01"}, params(trend))

	sets, err = s.RemoveAgentParams(ctx, llm.ID, []ParamChange{{Name: "llm-min-stop-atr"}, {Name: "timeframe"}})
	require.NoError(t, err)
	assert.Equal(t, []string{ParamRemoved, ParamRemoved}, []string{sets[0].Status, sets[1].Status})
	got, err := s.Agent(ctx, llm.ID)
	require.NoError(t, err)
	assert.Empty(t, got.LLM.Params, "the param of its language model")
	assert.Equal(t, Params{"pair": "BTCUSDm,XAUUSDm"}, got.Params)
	assert.Equal(t, "gpt-5", got.LLM.Model, "its settings kept")

	sets, err = s.RemoveAgentParams(ctx, trend.ID, []ParamChange{{Name: "timeframe"}})
	require.NoError(t, err)
	assert.Equal(t, ParamSkipped, sets[0].Status, "nothing to write")

	_, err = s.RemoveAgentParams(ctx, trend.ID, []ParamChange{{Name: "lot"}, {Name: "mode"}})
	assert.ErrorIs(t, err, ErrInvalid)
	assert.Equal(t, "0.01", params(trend)["lot"], "one refused: none removed")
}
