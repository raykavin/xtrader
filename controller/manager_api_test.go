//go:build unix

package controller

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/store"
)

// Each start of an agent gets a key of its API, apart from its token, which
// the dashboard sends it: dropped once it exited, another one for the next
// start.
func TestManager_GivesEachStartAKeyOfItsAPI(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	c.agent(t, "llm", "llm", true)

	keyRe := regexp.MustCompile(`\[llm\] XTRADER_AGENT_API_KEY=(\S+)`)
	keys := func() []string {
		var found []string
		for _, match := range keyRe.FindAllStringSubmatch(c.stdout.String(), -1) {
			found = append(found, match[1])
		}
		return found
	}
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return len(keys()) == 1 }, 5*time.Second, 10*time.Millisecond)
	first := keys()[0]
	assert.Len(t, first, 43, "32 random bytes")
	assert.Equal(t, first, c.dashboard.keys.get("llm"))
	token := regexp.MustCompile(`XTRADER_AGENT_TOKEN=(\S+)`).FindStringSubmatch(c.stdout.String())[1]
	assert.NotContains(t, token, first, "the agent never gets the token of the controller")

	require.NoError(t, c.manager.Restart(ctx, "llm"))
	require.Eventually(t, func() bool { return len(keys()) == 2 }, 5*time.Second, 10*time.Millisecond)
	second := keys()[1]
	assert.NotEqual(t, first, second)
	assert.Equal(t, second, c.dashboard.keys.get("llm"), "the one of the start running")

	off := false
	llm, err := c.store.AgentByName(ctx, "llm")
	require.NoError(t, err)
	_, err = c.store.UpdateAgent(ctx, llm.ID, store.AgentUpdate{Enabled: &off})
	require.NoError(t, err)
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return c.dashboard.keys.get("llm") == "" }, 10*time.Second,
		10*time.Millisecond, "dropped once it exited")
}

// An agent saved with the address of its API off the loopback, before the
// store refused it, is neither served, probed nor launched, its state telling
// why, the others served and launched.
func TestManager_AgentOffTheLoopback(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	elsewhere := c.agent(t, "elsewhere", "trend", true)
	apart := c.agent(t, "apart", "trend", true, func(a *store.Agent) { a.Launched, a.Mode = false, "paper" })
	c.agent(t, "llm", "llm", true)
	for id, address := range map[int64]string{elsewhere.ID: "169.254.169.254:80", apart.ID: "0.0.0.0:5580"} {
		c.exec(t, "UPDATE agents SET api_address = ? WHERE id = ?", address, id)
	}

	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return strings.Contains(c.stdout.String(), "[llm] started") },
		5*time.Second, 10*time.Millisecond)
	var names []string
	for _, a := range c.dashboard.agents.list() {
		names = append(names, a.Name)
	}
	assert.Equal(t, []string{"llm"}, names, "the others served")
	assert.False(t, c.manager.supervisor.Running("elsewhere"), "not launched")
	for _, name := range []string{"elsewhere", "apart"} {
		state := c.manager.State(name)
		assert.Contains(t, state.LaunchError, "isn't on the loopback", name)
		assert.Contains(t, state.LaunchError, "change it in its settings", name)
	}
	assert.Empty(t, c.manager.State("llm").LaunchError)
	assert.ErrorContains(t, c.manager.Restart(ctx, "elsewhere"), "isn't on the loopback")
	assert.False(t, c.manager.supervisor.Running("elsewhere"))

	// its settings changed to the loopback, it is served and launched
	address := "localhost:15599"
	_, err := c.store.UpdateAgent(ctx, elsewhere.ID, store.AgentUpdate{APIAddress: &address})
	require.NoError(t, err)
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return c.manager.supervisor.Running("elsewhere") }, 5*time.Second,
		10*time.Millisecond)
	assert.Empty(t, c.manager.State("elsewhere").LaunchError)
	_, ok := c.dashboard.agents.get("elsewhere")
	assert.True(t, ok)
}
