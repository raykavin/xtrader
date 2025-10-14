package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
)

func TestTokens_OneAgentEachRevocable(t *testing.T) {
	tokens := newTokens()
	first, err := tokens.issue(7)
	require.NoError(t, err)
	second, err := tokens.issue(7)
	require.NoError(t, err)
	other, err := tokens.issue(8)
	require.NoError(t, err)
	assert.Regexp(t, `^xta_7_[A-Za-z0-9_-]{43}$`, first)
	assert.NotEqual(t, first, second, "one per start")

	for token, want := range map[string]int64{first: 7, second: 7, other: 8} {
		id, ok := tokens.verify(token)
		assert.True(t, ok)
		assert.Equal(t, want, id)
	}

	tokens.revoke(first)
	_, ok := tokens.verify(first)
	assert.False(t, ok, "revoked")
	_, ok = tokens.verify(second)
	assert.True(t, ok, "the other start of the agent goes on")

	for _, forged := range []string{"", "xta_", "xta_7", "xta_7_", "xta_x_abc", "xta_-1_abc",
		"xta_8" + second[5:], // the secret of an agent with the ID of another
		second + "x", "Bearer " + second} {
		_, ok := tokens.verify(forged)
		assert.False(t, ok, forged)
	}
	tokens.revoke("garbage") // nothing to revoke
	tokens.revoke(second)
	tokens.revoke(other)
	assert.Empty(t, tokens.hashes)
}

func TestRuntimes_AVersionPerChange(t *testing.T) {
	r := newRuntimes()
	_, changed, ok := r.get(1)
	assert.False(t, ok)

	r.set(1, agentapi.Runtime{TradePanel: true})
	doc, _, ok := r.get(1)
	require.True(t, ok)
	first := doc.Version
	assert.Positive(t, first)
	select {
	case <-changed:
	default:
		t.Fatal("the waiters of the first runtime not woken")
	}

	_, changed, _ = r.get(1)
	r.set(1, agentapi.Runtime{TradePanel: true})
	doc, _, _ = r.get(1)
	assert.Equal(t, first, doc.Version, "the same runtime, the same version")
	select {
	case <-changed:
		t.Fatal("woken without a change")
	case <-time.After(10 * time.Millisecond):
	}

	r.set(1, agentapi.Runtime{TradePanel: true, Prompt: &agentapi.PromptRef{ID: 3, Name: "trend_following", Version: 1}})
	doc, _, _ = r.get(1)
	assert.Greater(t, doc.Version, first)
	<-changed

	next := doc.Version
	r.set(1, agentapi.Runtime{TradePanel: true, Prompt: &agentapi.PromptRef{ID: 3, Name: "trend_following", Version: 2}})
	doc, _, _ = r.get(1)
	assert.Greater(t, doc.Version, next, "a new version of the prompt")
	r.set(2, agentapi.Runtime{})
	other, _, _ := r.get(2)
	assert.NotEqual(t, doc.Version, other.Version)
}
