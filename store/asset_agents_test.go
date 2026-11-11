package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// The agents allowed on assets: none while never set; set with assets as MT5
// names them, each in one rule, and agents that exist, each once in a rule;
// every change grows its revision; a rename of an agent follows in its rules.
func TestAssetAgents_SetAndRename(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i, name := range []string{"orb", "trend", "gold"} {
		_, err := s.CreateAgent(ctx, Agent{Name: name, Kind: "trend", Mode: "live",
			APIAddress: fmt.Sprintf("localhost:%d", 5600+i), Params: Params{"pair": "XAUUSDm;BTCUSDm"}},
			AgentSecrets{})
		require.NoError(t, err)
	}

	none, found, err := s.AssetAgents(ctx)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, []ui.AssetAgentRule{}, none.Settings().Rules)
	allowed, excluded := none.Allowed("orb", []string{"XAUUSDm", "BTCUSDm"})
	assert.Equal(t, []string{"XAUUSDm", "BTCUSDm"}, allowed, "every asset without a rule")
	assert.Empty(t, excluded)

	for _, bad := range []ui.AssetAgentsSettings{
		{Rules: []ui.AssetAgentRule{{Agents: []string{"orb"}}}},
		{Rules: []ui.AssetAgentRule{{Pairs: []string{"XAUUSDm"}}}},
		{Rules: []ui.AssetAgentRule{{Pairs: []string{"XAU USD"}, Agents: []string{"orb"}}}},
		{Rules: []ui.AssetAgentRule{{Pairs: []string{"XAUUSDm"}, Agents: []string{"ninguem"}}}},
		{Rules: []ui.AssetAgentRule{{Pairs: []string{"XAUUSDm"}, Agents: []string{"orb", "orb"}}}},
		{Rules: []ui.AssetAgentRule{{Pairs: []string{"XAUUSDm"}, Agents: []string{"orb"}},
			{Pairs: []string{"XAUUSDm"}, Agents: []string{"trend"}}}},
	} {
		_, err := s.SetAssetAgents(ctx, bad, "admin")
		assert.ErrorIs(t, err, ErrInvalid, "%+v", bad)
	}

	saved, err := s.SetAssetAgents(ctx, ui.AssetAgentsSettings{Rules: []ui.AssetAgentRule{
		{Pairs: []string{"XAUUSDm", "XAGUSDm"}, Agents: []string{"orb", "gold"}}}}, "admin")
	require.NoError(t, err)
	assert.EqualValues(t, 1, saved.Revision)
	assert.Equal(t, "admin", saved.Settings().UpdatedBy)
	allowed, excluded = saved.Allowed("trend", []string{"XAUUSDm", "BTCUSDm"})
	assert.Equal(t, []string{"BTCUSDm"}, allowed, "the asset without a rule")
	assert.Equal(t, []string{"XAUUSDm"}, excluded, "the asset of a rule without it")
	allowed, excluded = saved.Allowed("orb", []string{"XAUUSDm", "BTCUSDm"})
	assert.Equal(t, []string{"XAUUSDm", "BTCUSDm"}, allowed)
	assert.Empty(t, excluded)
	agents, ruled := saved.AgentsOf("XAGUSDm")
	assert.True(t, ruled)
	assert.Equal(t, []string{"orb", "gold"}, agents)
	_, ruled = saved.AgentsOf("BTCUSDm")
	assert.False(t, ruled)

	gold, err := s.AgentByName(ctx, "gold")
	require.NoError(t, err)
	_, err = s.RenameAgent(ctx, gold.ID, "ouro")
	require.NoError(t, err)
	got, found, err := s.AssetAgents(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, []string{"orb", "ouro"}, got.Settings().Rules[0].Agents, "the rename followed")
	assert.EqualValues(t, 2, got.Revision, "a change the agents take")
	trend, err := s.AgentByName(ctx, "trend")
	require.NoError(t, err)
	_, err = s.RenameAgent(ctx, trend.ID, "tendencia")
	require.NoError(t, err)
	got, _, err = s.AssetAgents(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 2, got.Revision, "an agent of no rule: nothing changed")

	cleared, err := s.SetAssetAgents(ctx, ui.AssetAgentsSettings{}, "admin")
	require.NoError(t, err)
	assert.Equal(t, []ui.AssetAgentRule{}, cleared.Settings().Rules)
	assert.EqualValues(t, 3, cleared.Revision)
}
