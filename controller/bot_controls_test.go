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

	"github.com/raykavin/xtrader/ui"
)

// The bot of an agent stopped from the dashboard kept: recorded once the agent
// took it, with who and when, told to the agent at its launch so that it
// starts stopped whatever restarts it, and shown in its settings; started
// again, cleared; refused by the agent, nothing recorded.
func TestDashboard_BotStopKept(t *testing.T) {
	s := newSupervised(t)
	ctx := context.Background()
	orb := s.liveAgent(t, "orb", 102, true)
	call, _ := settingsSession(t, s.managed)
	stopped := func() (bool, string) {
		t.Helper()
		a, err := s.store.AgentByName(ctx, "orb")
		require.NoError(t, err)
		cfg, err := configOf(ctx, s.store, a)
		require.NoError(t, err)
		on, by, at := a.BotStopped()
		assert.Equal(t, on, cfg.BotStopped, "told to the agent at its launch")
		if on {
			assert.WithinDuration(t, time.Now(), at, time.Minute)
		}
		return on, by
	}
	settingsOf := func() ui.AgentSettings {
		t.Helper()
		resp, body := call(http.MethodGet, "/api/settings/agents", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		var list ui.AgentSettingsResponse
		require.NoError(t, json.Unmarshal([]byte(body), &list))
		for _, a := range list.Agents {
			if a.Name == "orb" {
				return a
			}
		}
		t.Fatal("orb not listed")
		return ui.AgentSettings{}
	}

	resp, body := call(http.MethodPost, "/api/agents/orb/controls/stop", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.JSONEq(t, `{"status":"stopped"}`, body, "the answer of the agent, as before")
	on, by := stopped()
	assert.True(t, on)
	assert.Equal(t, "admin", by)
	listed := settingsOf()
	assert.True(t, listed.BotStopped)
	assert.Equal(t, "admin", listed.BotStoppedBy)
	assert.NotZero(t, listed.BotStoppedAt)

	resp, body = call(http.MethodPost, "/api/agents/orb/controls/start", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	on, _ = stopped()
	assert.False(t, on, "started again: cleared")
	assert.False(t, settingsOf().BotStopped)

	orb.mu.Lock()
	orb.refuseBot = true
	orb.mu.Unlock()
	resp, _ = call(http.MethodPost, "/api/agents/orb/controls/stop", nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	on, _ = stopped()
	assert.False(t, on, "refused by the agent: nothing recorded")
}
