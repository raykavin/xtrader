package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/raykavin/xtrader/config"
)

// R17i: from the migration 17 on, an LLM agent takes the reasoning efforts of
// the current models, none and xhigh among them; the ones kept before are
// kept, and the migration down leaves none and xhigh to the default of the
// model.
func TestMigration17_TheReasoningEffortsOfTheCurrentModels(t *testing.T) {
	dsn := testDatabase(t)
	migratedTo(t, dsn, 16)
	s, err := Open(config.Database{DSN: dsn, MaxOpenConns: 2, LogLevel: "silent"}, testKey)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	kept := llmAgent(t, s, "kept")
	newer := llmAgent(t, s, "newer")
	setEffort := func(agent Agent, effort string) error {
		return s.db.Exec("UPDATE agent_llm_settings SET reasoning_effort = ? WHERE agent_id = ?", effort,
			agent.ID).Error
	}
	require.NoError(t, setEffort(kept, "minimal"))
	require.ErrorIs(t, setEffort(newer, "xhigh"), gorm.ErrCheckConstraintViolated, "refused before")

	migrated(t, dsn)
	require.NoError(t, s.CheckSchema(ctx))
	require.NoError(t, setEffort(newer, "xhigh"))
	require.ErrorIs(t, setEffort(newer, "max"), gorm.ErrCheckConstraintViolated)
	for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh"} {
		agent, err := s.UpdateAgent(ctx, newer.ID, AgentUpdate{LLM: &LLMUpdate{ReasoningEffort: &effort}})
		require.NoError(t, err, effort)
		require.Equal(t, effort, *agent.LLM.ReasoningEffort)
	}
	agent, err := s.Agent(ctx, kept.ID)
	require.NoError(t, err)
	require.Equal(t, "minimal", *agent.LLM.ReasoningEffort, "kept")

	down, err := os.ReadFile(filepath.Join("..", "sql", "migrations",
		"000017_alter_agent_llm_settings_reasoning_effort.down.sql"))
	require.NoError(t, err)
	require.NoError(t, s.db.Transaction(func(tx *gorm.DB) error { // a statement at a time, as prepared
		for _, statement := range strings.Split(string(down), ";\n") {
			if strings.TrimSpace(statement) != "" {
				if err := tx.Exec(statement).Error; err != nil {
					return err
				}
			}
		}
		return nil
	}))
	agent, err = s.Agent(ctx, newer.ID)
	require.NoError(t, err)
	require.Nil(t, agent.LLM.ReasoningEffort, "xhigh left to the default of the model")
	agent, err = s.Agent(ctx, kept.ID)
	require.NoError(t, err)
	require.Equal(t, "minimal", *agent.LLM.ReasoningEffort)
	require.ErrorIs(t, setEffort(newer, "none"), gorm.ErrCheckConstraintViolated)
}
