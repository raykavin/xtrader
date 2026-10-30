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

// The news of all the agents on the dashboard: off from the seeds, with their
// server and window; set, the token never answered nor logged, kept when a
// change leaves it out; the rules checked; applied to every agent, LLM or
// not, through its runtime, a new version each change, the server, the token
// and the window only while on.
func TestNewsAPI_Settings(t *testing.T) {
	var logs syncBuffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	s := newSupervised(t)
	ctx := context.Background()
	gold := s.agent(t, "gold", "llm", false)
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
	assert.Equal(t, "http://mt5:22347/mcp", got.URL, "of the seeds")
	assert.Equal(t, [3]any{15, 15, "high"}, [3]any{got.BlockBefore, got.BlockAfter, got.BlockImportance})
	assert.Empty(t, got.Conflict)
	assert.Empty(t, got.URLConflict)
	assert.Equal(t, []ui.NewsAgent{{Agent: "gold", Kind: "llm"}, {Agent: "trend", Kind: "trend"}}, got.Agents,
		"every agent")
	assert.Equal(t, &agentapi.News{}, runtime(gold).News)
	assert.Equal(t, &agentapi.News{}, runtime(trend).News, "LLM or not")
	before := runtime(trend).Version

	resp, body = call(http.MethodPut, "/api/settings/news", ui.NewsSettingsRequest{Enabled: boolPtr(true),
		Token: secret("mcp-secret-token")})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	got = news(body)
	assert.True(t, got.Enabled)
	assert.True(t, got.TokenSet)
	assert.Equal(t, "admin", got.UpdatedBy)
	assert.Positive(t, got.UpdatedAt)
	assert.NotContains(t, body, "mcp-secret-token", "never the token")
	want := &agentapi.News{Enabled: true, URL: "http://mt5:22347/mcp", Token: "mcp-secret-token", BlockBefore: 15,
		BlockAfter: 15, BlockImportance: "high"}
	doc := runtime(trend)
	assert.Equal(t, want, doc.News, "applied while it runs")
	assert.Greater(t, doc.Version, before, "a new version")
	assert.Equal(t, want, runtime(gold).News, "shared by the agents")

	// the server and the window
	resp, body = call(http.MethodPut, "/api/settings/news", ui.NewsSettingsRequest{
		URL: secret(" http://127.0.0.1:22346/mcp "), BlockBefore: ptrTo(30), BlockAfter: ptrTo(5),
		BlockImportance: secret("medium")})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	got = news(body)
	assert.Equal(t, "http://127.0.0.1:22346/mcp", got.URL)
	assert.Equal(t, [3]any{30, 5, "medium"}, [3]any{got.BlockBefore, got.BlockAfter, got.BlockImportance})
	assert.True(t, got.TokenSet, "the token kept")
	assert.Equal(t, &agentapi.News{Enabled: true, URL: "http://127.0.0.1:22346/mcp", Token: "mcp-secret-token",
		BlockBefore: 30, BlockAfter: 5, BlockImportance: "medium"}, runtime(trend).News)

	for _, refused := range []ui.NewsSettingsRequest{
		{BlockBefore: ptrTo(-1)},
		{BlockAfter: ptrTo(1441)},
		{BlockImportance: secret("urgent")},
		{URL: secret("ftp://127.0.0.1/mcp")},
		{URL: secret("http://user:pass@127.0.0.1/mcp")},
	} {
		resp, body = call(http.MethodPut, "/api/settings/news", refused)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
	}
	assert.Equal(t, 30, runtime(trend).News.BlockBefore, "nothing changed")

	// off, the token left out: kept, nothing sent to the agents while off
	resp, body = call(http.MethodPut, "/api/settings/news", ui.NewsSettingsRequest{Enabled: boolPtr(false)})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	got = news(body)
	assert.False(t, got.Enabled)
	assert.True(t, got.TokenSet, "kept")
	assert.Equal(t, &agentapi.News{}, runtime(trend).News)
	token, err := s.store.NewsToken(ctx)
	require.NoError(t, err)
	assert.Equal(t, "mcp-secret-token", token)

	// on again, the token removed: told as missing by the agents
	resp, body = call(http.MethodPut, "/api/settings/news", ui.NewsSettingsRequest{Enabled: boolPtr(true),
		Token: secret("")})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.False(t, news(body).TokenSet)
	assert.Empty(t, runtime(gold).News.Token)
	assert.True(t, runtime(gold).News.Enabled)

	// a new agent gets them at its start
	cfg, err := configOf(ctx, s.store, trend)
	require.NoError(t, err)
	assert.True(t, cfg.News.Enabled)
	assert.Equal(t, "http://127.0.0.1:22346/mcp", cfg.News.URL)
	assert.Contains(t, logs.String(), "ui: the news changed by admin")
	assert.NotContains(t, logs.String(), "mcp-secret-token", "never logged")
}

// The test of the news: the MCP server of the news, once, with its token:
// without a server or a token, nothing asked; a token refused told apart from
// a server unreachable; never the token in the answer.
func TestNewsAPI_Test(t *testing.T) {
	s := newSupervised(t)
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(refusing.Close)
	closed := httptest.NewServer(nil)
	unreachable := closed.URL + "/mcp"
	closed.Close()
	s.agent(t, "gold", "llm", true)
	s.agent(t, "trend", "trend", true)
	s.agent(t, "dormant", "trend", false)
	call, _ := settingsSession(t, s.managed)
	ctx := context.Background()
	test := func() ui.NewsTestResult {
		t.Helper()
		resp, body := call(http.MethodPost, "/api/settings/news/test", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		assert.NotContains(t, body, "mcp-test-token", "never the token")
		var response ui.NewsTestResponse
		require.NoError(t, json.Unmarshal([]byte(body), &response))
		require.Len(t, response.Results, 1, "the server of the news")
		return response.Results[0]
	}
	set := func(update store.NewsUpdate) {
		t.Helper()
		update.By = "test"
		_, err := s.store.SetNews(ctx, update)
		require.NoError(t, err)
	}

	got := test()
	assert.Equal(t, llm.CalendarMissingToken, got.Status, "the server of the seeds, no token")
	assert.Equal(t, []string{"gold", "trend"}, got.Agents, "the agents enabled")

	set(store.NewsUpdate{URL: secret(refusing.URL + "/mcp"), Token: secret("mcp-test-token")})
	got = test()
	assert.Equal(t, llm.CalendarUnauthorized, got.Status)
	assert.Equal(t, refusing.URL+"/mcp", got.URL)

	set(store.NewsUpdate{URL: secret(unreachable)})
	got = test()
	assert.Equal(t, llm.CalendarUnavailable, got.Status)
	assert.NotEmpty(t, got.Detail)

	set(store.NewsUpdate{URL: secret("")})
	assert.Equal(t, llm.CalendarMissingServer, test().Status)
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
