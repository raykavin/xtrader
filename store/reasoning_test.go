package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// An LLM agent takes the reasoning efforts of the current models, none and
// xhigh among them, and no other.
func TestSchema_TheReasoningEffortsOfTheCurrentModels(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	setEffort := func(effort string) error {
		return s.db.Exec("UPDATE agent_llm_settings SET reasoning_effort = ? WHERE agent_id = ?", effort,
			agent.ID).Error
	}
	for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh"} {
		require.NoError(t, setEffort(effort), effort)
		updated, err := s.UpdateAgent(ctx, agent.ID, AgentUpdate{LLM: &LLMUpdate{ReasoningEffort: &effort}})
		require.NoError(t, err, effort)
		require.Equal(t, effort, *updated.LLM.ReasoningEffort)
	}
	require.ErrorIs(t, setEffort("max"), gorm.ErrCheckConstraintViolated)
}
