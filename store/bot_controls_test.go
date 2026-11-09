package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bot of an agent stopped by a user kept with the agent, by whom and when,
// read with it whichever way it is read; started again, cleared.
func TestSetAgentBotStopped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	stopped, _, _ := agent.BotStopped()
	assert.False(t, stopped, "never stopped")

	require.NoError(t, s.SetAgentBotStopped(ctx, agent.ID, true, "admin"))
	got, err := s.Agent(ctx, agent.ID)
	require.NoError(t, err)
	stopped, by, at := got.BotStopped()
	assert.True(t, stopped)
	assert.Equal(t, "admin", by)
	assert.WithinDuration(t, time.Now(), at, time.Minute)
	byName, err := s.AgentByName(ctx, "llm")
	require.NoError(t, err)
	stopped, _, _ = byName.BotStopped()
	assert.True(t, stopped)
	all, err := s.Agents(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	stopped, _, _ = all[0].BotStopped()
	assert.True(t, stopped)

	require.NoError(t, s.SetAgentBotStopped(ctx, agent.ID, false, "telegram:42"))
	got, err = s.Agent(ctx, agent.ID)
	require.NoError(t, err)
	stopped, by, _ = got.BotStopped()
	assert.False(t, stopped)
	assert.Empty(t, by, "started: nothing told")
	assert.Equal(t, "telegram:42", got.BotControl.ChangedBy, "who started it kept")

	_, err = s.UpdateAgent(ctx, agent.ID, AgentUpdate{Params: Params{"pair": "XAUUSDm", "llm-min-stop-atr": "1"}})
	require.NoError(t, err, "an agent with its bot control still updated")
}
