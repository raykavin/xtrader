package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	log "github.com/sirupsen/logrus"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The news of all the agents on the dashboard: off and without a token from
// the seeds; set, the token never answered nor logged, kept when a change
// leaves it out; applied to the LLM agents through their runtime, a new
// version each change, the token only while on; the other agents untouched.
func TestNewsAPI_Settings(t *testing.T) {
	var logs syncBuffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	s := newSupervised(t)
	ctx := context.Background()
	gold := s.agent(t, "gold", "llm", false, func(a *store.Agent) {
		a.LLM.CalendarURL = ptrTo("http://127.0.0.1:22346/mcp")
		a.LLM.Params["news-block-before"] = "15m"
	})
	silver := s.agent(t, "silver", "llm", false)
	trend := s.agent(t, "trend", "trend", false)
	call, _ := settingsSession(t, s.managed)
	news := func(body string) ui.NewsSettingsResponse {
		t.Helper()
		var response ui.NewsSettingsResponse
		require.NoError(t, json.Unmarshal([]byte(body), &response))
		return response
	}
	runtime := func(a store.Agent) agentapi.Runtime {
		t.Helper()
		require.NoError(t, s.manager.RefreshRuntime(ctx, a.Name))
		doc, _, ok := s.manager.runtimes.get(a.ID)
		require.True(t, ok)
		return doc
	}

	resp, body := call(http.MethodGet, "/api/settings/news", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	got := news(body)
	assert.False(t, got.Enabled)
	assert.False(t, got.TokenSet)
	assert.Empty(t, got.Conflict)
	assert.Equal(t, []ui.NewsAgent{
		{Agent: "gold", CalendarURL: "http://127.0.0.1:22346/mcp", BlockBefore: "15m"},
		{Agent: "silver"},
	}, got.Agents, "the LLM agents only")
	assert.Equal(t, &agentapi.News{}, runtime(gold).News)
	assert.Nil(t, runtime(trend).News, "not an LLM agent")
	before := runtime(gold).Version

	resp, body = call(http.MethodPut, "/api/settings/news", ui.NewsSettingsRequest{Enabled: boolPtr(true),
		Token: secret("mcp-secret-token")})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	got = news(body)
	assert.True(t, got.Enabled)
	assert.True(t, got.TokenSet)
	assert.Equal(t, "admin", got.UpdatedBy)
	assert.Positive(t, got.UpdatedAt)
	assert.NotContains(t, body, "mcp-secret-token", "never the token")
	doc := runtime(gold)
	assert.Equal(t, &agentapi.News{Enabled: true, Token: "mcp-secret-token"}, doc.News, "applied while it runs")
	assert.Greater(t, doc.Version, before, "a new version")
	assert.Equal(t, doc.News, runtime(silver).News, "shared by the agents")
	assert.Nil(t, runtime(trend).News)

	resp, body = call(http.MethodGet, "/api/settings/news", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, body, "mcp-secret-token")

	// off, the token left out: kept, never sent to the agents while off
	resp, body = call(http.MethodPut, "/api/settings/news", ui.NewsSettingsRequest{Enabled: boolPtr(false)})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	got = news(body)
	assert.False(t, got.Enabled)
	assert.True(t, got.TokenSet, "kept")
	assert.Equal(t, &agentapi.News{Enabled: false}, runtime(gold).News)
	token, err := s.store.NewsToken(ctx)
	require.NoError(t, err)
	assert.Equal(t, "mcp-secret-token", token)

	// the token replaced, then removed
	resp, _ = call(http.MethodPut, "/api/settings/news", ui.NewsSettingsRequest{Enabled: boolPtr(true),
		Token: secret("mcp-other")})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, &agentapi.News{Enabled: true, Token: "mcp-other"}, runtime(gold).News)
	resp, body = call(http.MethodPut, "/api/settings/news", ui.NewsSettingsRequest{Token: secret("")})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.False(t, news(body).TokenSet)
	assert.Equal(t, &agentapi.News{Enabled: true}, runtime(gold).News, "on without a token: told as missing")

	// a new agent gets them at its start
	cfg, err := configOf(ctx, s.store, silver)
	require.NoError(t, err)
	assert.Equal(t, &agentapi.News{Enabled: true}, cfg.News)
	assert.Contains(t, logs.String(), "ui: the news changed by admin")
	assert.NotContains(t, logs.String(), "mcp-secret-token", "never logged")
	assert.NotContains(t, logs.String(), "mcp-other")
}

// The test of the news: the MCP server of the calendar of each LLM agent,
// once, with the token of the news: without a token, nothing asked; a token
// refused told apart from a server unreachable; never the token in the
// answer.
func TestNewsAPI_Test(t *testing.T) {
	s := newSupervised(t)
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(refusing.Close)
	closed := httptest.NewServer(nil)
	unreachable := closed.URL + "/mcp"
	closed.Close()
	s.agent(t, "gold", "llm", false, func(a *store.Agent) { a.LLM.CalendarURL = ptrTo(refusing.URL + "/mcp") })
	s.agent(t, "silver", "llm", false, func(a *store.Agent) { a.LLM.CalendarURL = ptrTo(refusing.URL + "/mcp") })
	s.agent(t, "platinum", "llm", false, func(a *store.Agent) { a.LLM.CalendarURL = ptrTo(unreachable) })
	s.agent(t, "plain", "llm", false) // without a calendar
	call, _ := settingsSession(t, s.managed)
	test := func() ui.NewsTestResponse {
		t.Helper()
		resp, body := call(http.MethodPost, "/api/settings/news/test", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		assert.NotContains(t, body, "mcp-test-token", "never the token")
		var response ui.NewsTestResponse
		require.NoError(t, json.Unmarshal([]byte(body), &response))
		return response
	}

	got := test()
	require.Len(t, got.Results, 2, "a result by server")
	for _, result := range got.Results {
		assert.Equal(t, llm.CalendarMissingToken, result.Status, result.URL)
	}

	_, err := s.store.SetNews(context.Background(), store.NewsUpdate{Token: secret("mcp-test-token"), By: "test"})
	require.NoError(t, err)
	got = test()
	require.Len(t, got.Results, 2)
	byURL := map[string]ui.NewsTestResult{}
	for _, result := range got.Results {
		byURL[result.URL] = result
	}
	refused := byURL[refusing.URL+"/mcp"]
	assert.Equal(t, llm.CalendarUnauthorized, refused.Status)
	assert.Equal(t, []string{"gold", "silver"}, refused.Agents)
	assert.Equal(t, llm.CalendarUnavailable, byURL[unreachable].Status)
	assert.Equal(t, []string{"platinum"}, byURL[unreachable].Agents)
	assert.NotEmpty(t, byURL[unreachable].Detail)
}

// syncBuffer is a buffer of the logs written from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
