package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// An agent disabled is renamed with all it has: its ID, its account (its
// comment and magic number at the broker), its language model and its
// secrets, its decisions; its former name kept, free for another agent.
func TestRenameAgent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	other := llmAgent(t, s, "llm-smc")
	d := decision("20261010T120000.000000-BTCUSDm", "BTCUSDm", 0)
	d.Prompt = nil
	require.NoError(t, s.SaveDecision(ctx, agent.ID, d, nil, true))

	_, err := s.RenameAgent(ctx, agent.ID, "llm-tendencia")
	require.ErrorIs(t, err, ErrConflict, "enabled: running")
	off := false
	_, err = s.UpdateAgent(ctx, agent.ID, AgentUpdate{Enabled: &off})
	require.NoError(t, err)

	for _, name := range []string{"", "-llm", "llm tendencia", "llm/tendencia"} {
		_, err = s.RenameAgent(ctx, agent.ID, name)
		require.ErrorIs(t, err, ErrInvalid, name)
	}
	_, err = s.RenameAgent(ctx, agent.ID, other.Name)
	require.ErrorIs(t, err, ErrConflict, "the name of another agent")

	renamed, err := s.RenameAgent(ctx, agent.ID, " llm-tendencia ")
	require.NoError(t, err)
	require.Equal(t, "llm-tendencia", renamed.Name)
	require.Equal(t, agent.ID, renamed.ID)
	require.Equal(t, agent.Account.Comment, renamed.Account.Comment, "the comment, so the magic number, kept")
	require.Equal(t, agent.LLM.Model, renamed.LLM.Model)
	secrets, err := s.AgentSecrets(ctx, renamed.ID)
	require.NoError(t, err)
	require.Equal(t, "sk-test-key", secrets.LLMAPIKey, "its secrets, sealed to its ID, still open")
	decisions, err := s.Decisions(ctx, renamed.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, decisions, 1, "its decisions kept")
	_, err = s.AgentByName(ctx, "llm")
	require.ErrorIs(t, err, ErrNotFound)

	var former []string
	require.NoError(t, s.db.Table("agent_former_names").Where("agent_id = ?", agent.ID).Pluck("name", &former).Error)
	require.Equal(t, []string{"llm"}, former)

	again, err := s.RenameAgent(ctx, agent.ID, "llm-tendencia")
	require.NoError(t, err, "the same name: nothing to do")
	require.Equal(t, renamed.Name, again.Name)
	_, err = s.RenameAgent(ctx, 999999, "x")
	require.ErrorIs(t, err, ErrNotFound)

	created := llmAgent(t, s, "llm")
	require.NotEqual(t, agent.ID, created.ID, "the former name free for another agent")
}
