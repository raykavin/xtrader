//go:build unix

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/internal/pgtest"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// testKey is the secrets key of the tests.
var testKey = []byte(strings.Repeat("k", 32))

// managed is a controller of the tests: its store on a database of its own,
// migrated, its dashboard, its manager and the output of its agents, fake
// processes of the test binary telling their environment (llm) or running
// (trend).
type managed struct {
	store     *store.Store
	dashboard *Dashboard
	manager   *Manager
	stdout    *lockedBuffer
	dsn       string // of its database
}

func newManaged(t *testing.T) *managed {
	t.Helper()
	dsn := pgtest.Migrated(t, "..")
	st, err := store.Open(config.Database{DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 4, ConnMaxLifetime: time.Minute,
		LogLevel: "silent"}, testKey)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	dashboard, err := NewDashboard(nil, WithUsers(st))
	require.NoError(t, err)
	dashboard.auth.failDelay = 0
	sup, stdout, _ := testSupervisor(t, 5*time.Second)
	agents := config.Agents{API: "127.0.0.1:8090", Dir: os.TempDir(),
		Commands: map[string]string{"llm": os.Args[0] + " " + fakeBotPrefix + "env",
			"trend": os.Args[0] + " " + fakeBotPrefix + "bot"},
		Env: config.Env{"GOCACHE=/tmp/go-cache"}}
	m := NewManager(st, agents, sup, dashboard, nil)
	dashboard.ServeSettings(st, m, nil)
	return &managed{store: st, dashboard: dashboard, manager: m, stdout: stdout, dsn: dsn}
}

// secret returns a pointer to value, a secret to write.
func secret(value string) *string { return &value }

// agent creates an agent of the tests: an LLM one with its secrets, or one of
// the runner, changed by edit, if any.
func (c *managed) agent(t *testing.T, name, kind string, enabled bool, edit ...func(a *store.Agent)) store.Agent {
	t.Helper()
	a := store.Agent{Name: name, Kind: kind, Enabled: enabled, Mode: "live",
		APIAddress: fmt.Sprintf("localhost:%d", 15500+len(name)),
		Launched:   true, Params: store.Params{"pair": "XAUUSDm", "lot": "0.01"},
		Account: &store.Account{BridgeURL: "http://127.0.0.1:18812"}}
	secrets := store.AgentSecrets{BridgeToken: secret("bridge-" + name)}
	if kind == "llm" {
		a.LLM = &store.LLMSettings{Provider: "openai", Model: "gpt-5", Params: store.Params{"llm-min-stop-atr": "1"}}
		secrets.LLMAPIKey = secret("sk-" + name)
	}
	for _, change := range edit {
		change(&a)
	}
	created, err := c.store.CreateAgent(context.Background(), a, secrets)
	require.NoError(t, err)
	return created
}

func TestManager_LaunchesTheAgentsEnabledAndStopsTheOthers(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	llm := c.agent(t, "llm", "llm", true)
	c.agent(t, "matrend", "trend", false)
	t.Setenv("XTRADER_SECRETS_KEY", "the-secrets-key")

	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return strings.Contains(c.stdout.String(), "[llm] XTRADER_AGENT_TOKEN=") },
		5*time.Second, 10*time.Millisecond)
	output := c.stdout.String()
	assert.Contains(t, output, "[llm] XTRADER_AGENT=llm\n")
	assert.Contains(t, output, "[llm] XTRADER_CONTROLLER_URL=http://127.0.0.1:8090\n")
	assert.Contains(t, output, "[llm] GOCACHE=/tmp/go-cache\n")
	assert.NotContains(t, output, "the-secrets-key", "the environment of the controller stays out")
	token := regexp.MustCompile(`XTRADER_AGENT_TOKEN=(\S+)`).FindStringSubmatch(output)[1]
	id, ok := c.manager.tokens.verify(token)
	require.True(t, ok)
	assert.Equal(t, llm.ID, id, "the token of its start")
	assert.NotContains(t, output, "[matrend]", "disabled")
	names := func() []string {
		var list []string
		for _, a := range c.dashboard.agents.list() {
			list = append(list, a.Name)
		}
		return list
	}
	assert.Equal(t, []string{"llm"}, names(), "the dashboard serves the agents enabled")

	on, off := true, false
	matrend, err := c.store.AgentByName(ctx, "matrend")
	require.NoError(t, err)
	_, err = c.store.UpdateAgent(ctx, matrend.ID, store.AgentUpdate{Enabled: &on})
	require.NoError(t, err)
	_, err = c.store.UpdateAgent(ctx, llm.ID, store.AgentUpdate{Enabled: &off})
	require.NoError(t, err)
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return strings.Contains(c.stdout.String(), "[matrend] started") },
		5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return !c.manager.supervisor.Running("llm") }, 10*time.Second,
		10*time.Millisecond, "disabled: stopped")
	_, ok = c.manager.tokens.verify(token)
	assert.False(t, ok, "its token revoked once it exited")
	assert.Equal(t, []string{"matrend"}, names())

	// a change of its settings waits for a restart
	assert.False(t, c.manager.State("matrend").RestartPending)
	require.NoError(t, c.store.SetAgentSecrets(ctx, matrend.ID, store.AgentSecrets{BridgeToken: secret("new")}))
	require.NoError(t, c.manager.Reconcile(ctx))
	assert.True(t, c.manager.State("matrend").RestartPending)
	require.NoError(t, c.manager.Restart(ctx, "matrend"))
	assert.False(t, c.manager.State("matrend").RestartPending)
	require.Eventually(t, func() bool { return strings.Count(c.stdout.String(), "[matrend] started") == 2 },
		5*time.Second, 10*time.Millisecond)
}

// The dashboard serves an agent running at the address it was launched with:
// a new one waits for its restart, as the process listens at the old one.
func TestManager_ServesTheAddressAgentsListenAtUntilTheirRestart(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	trend := c.agent(t, "matrend", "trend", true)
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return strings.Contains(c.stdout.String(), "[matrend] started") },
		5*time.Second, 10*time.Millisecond)
	served := func() string {
		a, ok := c.dashboard.agents.get("matrend")
		require.True(t, ok)
		return a.Address
	}
	launched := served()
	assert.Equal(t, trend.APIAddress, launched)

	moved := "localhost:15599"
	_, err := c.store.UpdateAgent(ctx, trend.ID, store.AgentUpdate{APIAddress: &moved})
	require.NoError(t, err)
	require.NoError(t, c.manager.Reconcile(ctx))
	assert.Equal(t, launched, served(), "the process listens at the old address")
	assert.True(t, c.manager.State("matrend").RestartPending)

	require.NoError(t, c.manager.Restart(ctx, "matrend"))
	require.Eventually(t, func() bool { return strings.Count(c.stdout.String(), "[matrend] started") == 2 },
		5*time.Second, 10*time.Millisecond)
	require.NoError(t, c.manager.Reconcile(ctx))
	assert.Equal(t, moved, served(), "restarted at the new address")
	assert.False(t, c.manager.State("matrend").RestartPending)

	// stopped, the dashboard shows the address of the database
	off := false
	_, err = c.store.UpdateAgent(ctx, trend.ID, store.AgentUpdate{Enabled: &off})
	require.NoError(t, err)
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return !c.manager.supervisor.Running("matrend") }, 10*time.Second,
		10*time.Millisecond)
}

// internalCall calls the internal API of srv with the token, decoding its
// answer into out when set.
func internalCall(t *testing.T, srv *httptest.Server, token, method, path string, body, out interface{},
	headers ...string) *http.Response {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+agentapi.Prefix+path, reader)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode == http.StatusOK {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
	}
	return resp
}

// record returns a record of the outbox of kind with payload.
func record(t *testing.T, seq uint64, kind, key string, payload interface{}) agentapi.Record {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	return agentapi.Record{Seq: seq, Kind: kind, Key: key, Payload: data}
}

func TestInternalAPI_OnlyTheAgentOfTheToken(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	llm := c.agent(t, "llm", "llm", true)
	other := c.agent(t, "other", "llm", true, func(a *store.Agent) { a.Account.Comment = secret("nb:other") })
	prompt, err := c.store.PromptByName(ctx, "trend_following")
	require.NoError(t, err)
	require.NoError(t, c.store.SetAgentPrompt(ctx, llm.ID, &prompt.ID))
	token, err := c.manager.tokens.issue(llm.ID)
	require.NoError(t, err)
	otherToken, err := c.manager.tokens.issue(other.ID)
	require.NoError(t, err)
	srv := httptest.NewServer(c.manager.InternalHandler())
	t.Cleanup(srv.Close)

	for _, forged := range []string{"", "garbage", "xta_" + fmt.Sprint(llm.ID) + "_forged"} {
		resp := internalCall(t, srv, forged, http.MethodGet, "/config", nil, nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, forged)
	}

	var cfg agentapi.Config
	require.Equal(t, http.StatusOK, internalCall(t, srv, token, http.MethodGet, "/config", nil, &cfg).StatusCode)
	assert.Equal(t, "llm", cfg.Name)
	assert.Equal(t, "live", cfg.Mode)
	assert.Equal(t, map[string]string{"pair": "XAUUSDm", "lot": "0.01"}, cfg.Params)
	assert.Equal(t, "bridge-llm", cfg.Account.BridgeToken)
	assert.Equal(t, "sk-llm", cfg.LLM.APIKey, "its own secrets")
	assert.Equal(t, &agentapi.News{}, cfg.News, "the news off from the seeds")
	assert.Equal(t, map[string]string{"llm-min-stop-atr": "1"}, cfg.LLM.Params)
	require.NotNil(t, cfg.Prompt)
	assert.Equal(t, "trend_following", cfg.Prompt.Name)
	assert.Contains(t, cfg.Prompt.Content, "# Take profit: none")
	var otherCfg agentapi.Config
	internalCall(t, srv, otherToken, http.MethodGet, "/config", nil, &otherCfg)
	assert.Equal(t, "sk-other", otherCfg.LLM.APIKey, "never the secrets of another")
	assert.Nil(t, otherCfg.Prompt, "its default prompt")
	resp := internalCall(t, srv, otherToken, http.MethodGet, "/prompt", nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// the runtime, and a change of it waited for
	require.NoError(t, c.manager.Reconcile(ctx))
	var runtime agentapi.Runtime
	internalCall(t, srv, token, http.MethodGet, "/runtime", nil, &runtime)
	require.NotNil(t, runtime.Prompt)
	assert.Equal(t, 1, runtime.Prompt.Version)
	version := fmt.Sprint(runtime.Version)
	start := time.Now()
	resp = internalCall(t, srv, token, http.MethodGet, "/runtime?wait=100ms", nil, nil, "If-None-Match", version)
	assert.Equal(t, http.StatusNotModified, resp.StatusCode)
	assert.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond)
	go func() {
		time.Sleep(50 * time.Millisecond)
		content := "# Take profit: none\n\nSetup: v2"
		_, _ = c.store.UpdatePrompt(ctx, prompt.ID, store.PromptEdit{Content: &content}, "test")
		_ = c.manager.Reconcile(ctx)
	}()
	resp = internalCall(t, srv, token, http.MethodGet, "/runtime?wait=5s", nil, &runtime, "If-None-Match", version)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "woken by the edit")
	assert.Equal(t, 2, runtime.Prompt.Version)

	// the order IDs, and the records of the outbox
	var ids agentapi.OrderIDs
	internalCall(t, srv, token, http.MethodPost, "/order-ids", agentapi.OrderIDsRequest{Count: 100}, &ids)
	assert.Equal(t, agentapi.OrderIDs{First: 1, Last: 100}, ids)
	resp = internalCall(t, srv, token, http.MethodPost, "/order-ids", agentapi.OrderIDsRequest{Count: 0}, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	at := time.Date(2026, 9, 28, 12, 30, 0, 0, time.UTC)
	order := model.Order{ID: 1, ExchangeID: 99, Pair: "XAUUSDm", Side: model.SideTypeSell, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Price: 4154.2, Quantity: 1, CreatedAt: at, UpdatedAt: at}
	decision := ui.Decision{ID: "20260928T123000.000000-XAUUSDm", Pair: "XAUUSDm", Timeframe: "30m",
		Candle: at.Unix() - 1800, DecidedAt: at.Unix(), Model: "openai gpt-5", Action: "ENTER_SHORT",
		Outcome: "applied", OrderIDs: []int64{1}, Prompt: &ui.DecisionPrompt{ID: prompt.ID, Name: prompt.Name,
			Version: 2}}
	journal := agentapi.JournalRecord{ID: decision.ID, At: at, Pair: "XAUUSDm", Model: "openai gpt-5",
		Input:    json.RawMessage(`{"symbol":"XAUUSDm","time":"2026-09-28T12:00:00Z"}`),
		Decision: json.RawMessage(`{"action":"ENTER_SHORT","confidence":0.7,"reason":"the trend"}`)}
	status := agentapi.PromptStatus{PromptID: prompt.ID, Name: prompt.Name, Version: 2, Applied: true, At: at}
	var acked agentapi.OutboxResponse
	resp = internalCall(t, srv, token, http.MethodPost, "/outbox", agentapi.OutboxRequest{Records: []agentapi.Record{
		record(t, 1, agentapi.KindOrder, "1", order),
		record(t, 2, agentapi.KindDecision, decision.ID, agentapi.DecisionRecord{Decision: decision,
			Attachments: []agentapi.Attachment{{MediaType: "image/png", Data: []byte("png")}}}),
		record(t, 3, agentapi.KindJournal, decision.ID, journal),
		record(t, 4, agentapi.KindPromptStatus, "prompt", status),
	}}, &acked)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.EqualValues(t, 4, acked.Acked)

	var orders []model.Order
	internalCall(t, srv, token, http.MethodGet, "/orders", nil, &orders)
	require.Len(t, orders, 1)
	assert.Equal(t, int64(99), orders[0].ExchangeID)
	internalCall(t, srv, otherToken, http.MethodGet, "/orders", nil, &orders)
	assert.Empty(t, orders, "never the orders of another")
	var decisions []ui.Decision
	internalCall(t, srv, token, http.MethodGet, "/decisions?per_pair=10", nil, &decisions)
	require.Len(t, decisions, 1)
	assert.Equal(t, []ui.DecisionAttachment{{Index: 0, MediaType: "image/png", Size: 3}}, decisions[0].Attachments)
	var links []agentapi.DecisionLink
	internalCall(t, srv, token, http.MethodGet, "/decision-links", nil, &links)
	assert.Equal(t, []agentapi.DecisionLink{{OrderID: 1, DecisionID: decision.ID}}, links)
	var memory []agentapi.Remembered
	internalCall(t, srv, token, http.MethodGet, "/journal/memory?per_pair=20", nil, &memory)
	require.Len(t, memory, 1)
	assert.Equal(t, "the trend", memory[0].Reason)
	assert.True(t, memory[0].At.Equal(at), "when it was taken")
	resp = internalCall(t, srv, otherToken, http.MethodGet, "/decisions/"+decision.ID, nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "never the decisions of another")
	stored, err := c.store.Agent(ctx, llm.ID)
	require.NoError(t, err)
	assert.True(t, stored.LLM.PromptStatus.Applied)

	// a record refused: named, none of the batch written
	bad := order
	bad.ID, bad.Side = 2, "HOLD"
	var refused agentapi.OutboxError
	req, _ := json.Marshal(agentapi.OutboxRequest{Records: []agentapi.Record{
		record(t, 5, agentapi.KindOrder, "3", model.Order{ID: 3, Pair: "XAUUSDm", Side: model.SideTypeBuy,
			Type: model.OrderTypeMarket, Status: model.OrderStatusTypeNew, CreatedAt: at, UpdatedAt: at}),
		record(t, 6, agentapi.KindOrder, "2", bad)}})
	httpReq, _ := http.NewRequest(http.MethodPost, srv.URL+agentapi.Prefix+"/outbox", bytes.NewReader(req))
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpResp, err := http.DefaultClient.Do(httpReq)
	require.NoError(t, err)
	defer func() { _ = httpResp.Body.Close() }()
	require.Equal(t, http.StatusUnprocessableEntity, httpResp.StatusCode)
	require.NoError(t, json.NewDecoder(httpResp.Body).Decode(&refused))
	assert.Equal(t, 1, refused.Index)
	internalCall(t, srv, token, http.MethodGet, "/orders", nil, &orders)
	assert.Len(t, orders, 1, "the whole batch rolled back")
}

// settingsSession logs a user of the tests in the dashboard of c, and
// returns a caller of its API with the session and its CSRF token.
func settingsSession(t *testing.T, c *managed) (func(method, path string, body interface{}) (*http.Response, string),
	*httptest.Server) {
	t.Helper()
	return settingsSessionIn(t, c, nil)
}

// settingsSessionIn is settingsSession, the requests of the dashboard in base
// when set, as the controller serves them in the context of its signals.
func settingsSessionIn(t *testing.T, c *managed, base context.Context) (
	func(method, path string, body interface{}) (*http.Response, string), *httptest.Server) {
	t.Helper()
	createAdmin(t, c.store, "admin", "admin-password")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.js"), []byte("app"), 0o644))
	srv := httptest.NewUnstartedServer(c.dashboard.auth.protect(c.dashboard.router(bundle{dir: dir, version: "v1"},
		nil)))
	if base != nil {
		srv.Config.BaseContext = func(net.Listener) context.Context { return base }
	}
	srv.Start()
	t.Cleanup(srv.Close)
	resp, _ := login(t, srv, "admin", "admin-password")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cookies, csrf := sessionOf(resp), cookieOf(resp, csrfCookie).Value
	return func(method, path string, body interface{}) (*http.Response, string) {
		payload := ""
		if body != nil {
			data, err := json.Marshal(body)
			require.NoError(t, err)
			payload = string(data)
		}
		return do(t, method, srv.URL+path, payload, cookies, csrfHeader, csrf)
	}, srv
}

func TestSettingsAPI_TheAgents(t *testing.T) {
	c := newManaged(t)
	llm := c.agent(t, "llm", "llm", false)
	c.agent(t, "matrend", "trend", false)
	call, srv := settingsSession(t, c)

	resp, body := call(http.MethodGet, "/api/settings/agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var agents ui.AgentSettingsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &agents))
	require.Len(t, agents.Agents, 2)
	assert.Equal(t, ui.AgentSecretsPresent{LLMAPIKey: true, BridgeToken: true}, agents.Agents[0].Secrets)
	assert.Equal(t, "openai gpt-5", agents.Agents[0].Model)
	for _, secret := range []string{"sk-llm", "bridge-llm"} {
		assert.NotContains(t, body, secret, "never a secret")
	}

	prompt, err := c.store.PromptByName(context.Background(), "smc_ict")
	require.NoError(t, err)
	resp, body = call(http.MethodPatch, "/api/settings/agents/llm", ui.AgentSettingsRequest{Enabled: boolPtr(true),
		TelegramControl: boolPtr(true), PromptID: &prompt.ID, LLMAPIKey: secret("sk-new")})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var updated ui.AgentSettings
	require.NoError(t, json.Unmarshal([]byte(body), &updated))
	assert.True(t, updated.Enabled)
	assert.True(t, updated.TelegramControl)
	assert.Equal(t, prompt.ID, *updated.PromptID)
	assert.NotContains(t, body, "sk-new")
	secrets, err := c.store.AgentSecrets(context.Background(), llm.ID)
	require.NoError(t, err)
	assert.Equal(t, "sk-new", secrets.LLMAPIKey)
	require.Eventually(t, func() bool { return c.manager.supervisor.Running("llm") }, 5*time.Second,
		10*time.Millisecond, "enabled: launched at once")

	resp, body = call(http.MethodPatch, "/api/settings/agents/llm", ui.AgentSettingsRequest{DefaultPrompt: true})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var defaulted ui.AgentSettings
	require.NoError(t, json.Unmarshal([]byte(body), &defaulted))
	assert.Nil(t, defaulted.PromptID, "its default prompt")
	resp, _ = call(http.MethodPost, "/api/settings/agents/llm/restart", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	resp, _ = call(http.MethodPatch, "/api/settings/agents/matrend", ui.AgentSettingsRequest{PromptID: &prompt.ID})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "not an LLM agent")
	resp, _ = call(http.MethodPatch, "/api/settings/agents/nobody", ui.AgentSettingsRequest{})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = do(t, http.MethodPatch, srv.URL+"/api/settings/agents/llm", `{`, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "without a session")
}

func boolPtr(value bool) *bool { return &value }

// ptrTo returns a pointer to value, a field to set.
func ptrTo[T any](value T) *T { return &value }

// agentSettingsOf decodes the settings of an agent answered in body.
func agentSettingsOf(t *testing.T, body string) ui.AgentSettings {
	t.Helper()
	var settings ui.AgentSettings
	require.NoError(t, json.Unmarshal([]byte(body), &settings), body)
	return settings
}

// agentNames returns the names of the agents of the settings, in their order,
// with call, see settingsSession.
func agentNames(t *testing.T, call func(method, path string, body interface{}) (*http.Response, string)) []string {
	t.Helper()
	resp, body := call(http.MethodGet, "/api/settings/agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var agents ui.AgentSettingsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &agents))
	names := make([]string, 0, len(agents.Agents))
	for _, a := range agents.Agents {
		names = append(names, a.Name)
	}
	return names
}

// assertProtected asserts that a request of method to path of srv, with body,
// is refused without a session (401) and without the CSRF token of its session
// (403), see settingsSession.
func assertProtected(t *testing.T, srv *httptest.Server, method, path, body string) {
	t.Helper()
	resp, _ := do(t, method, srv.URL+path, body, nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s %s without a session", method, path)
	resp, _ = login(t, srv, "admin", "admin-password")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = do(t, method, srv.URL+path, body, sessionOf(resp))
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "%s %s without the CSRF token", method, path)
}

func TestSettingsAPI_CreateAnAgent(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	c.agent(t, "llm", "llm", false)
	call, srv := settingsSession(t, c)
	prompt, err := c.store.PromptByName(ctx, "trend_following")
	require.NoError(t, err)
	secrets := []string{"sk-created-key", "bridge-created-token"}
	gold := func() ui.AgentCreateRequest {
		return ui.AgentCreateRequest{Name: "gold", Kind: "llm", Mode: "live", APIAddress: "localhost:15600",
			TradePanel: true, Params: map[string]string{"pair": "XAUUSDm", "llm-min-stop-atr": "1"},
			Account: ui.AgentAccountSettings{BridgeURL: "http://127.0.0.1:18812", Magic: ptrTo(int64(4242)),
				Comment: secret("nb:gold")},
			LLM:      &ui.AgentLLMSettings{Provider: "openai", Model: "gpt-5", ReasoningEffort: secret("low")},
			PromptID: &prompt.ID, LLMAPIKey: secret(secrets[0]), BridgeToken: secret(secrets[1])}
	}

	for _, tc := range []struct {
		name   string
		edit   func(r *ui.AgentCreateRequest)
		status int
		want   string
	}{
		{"mode", func(r *ui.AgentCreateRequest) { r.Mode = "backtest" }, http.StatusBadRequest, "use live ou paper"},
		{"no mode", func(r *ui.AgentCreateRequest) { r.Mode = "" }, http.StatusBadRequest, `modo \"\"`},
		{"address", func(r *ui.AgentCreateRequest) { r.APIAddress = "localhost" }, http.StatusBadRequest,
			"host:porta"},
		{"param name", func(r *ui.AgentCreateRequest) { r.Params = map[string]string{"Lot": "0.01"} },
			http.StatusBadRequest, `parâmetro \"Lot\"`},
		{"param value", func(r *ui.AgentCreateRequest) { r.Params = map[string]string{"pair": "XAUUSDm\n-mode=paper"} },
			http.StatusBadRequest, "deve ficar numa linha só"},
		{"param of a field", func(r *ui.AgentCreateRequest) { r.Params = map[string]string{"mt5-comment": "nb:x"} },
			http.StatusBadRequest, "parâmetro mt5-comment do agente gold tem campo próprio"},
		{"param of a language model", func(r *ui.AgentCreateRequest) {
			r.Kind, r.LLM, r.PromptID, r.LLMAPIKey = "trend", nil, nil, nil
		}, http.StatusBadRequest, "só para agentes LLM"},
		{"name taken", func(r *ui.AgentCreateRequest) { r.Name = "llm" }, http.StatusConflict, "agente llm já existe"},
		{"address taken", func(r *ui.AgentCreateRequest) { r.APIAddress = "localhost:15503" }, http.StatusConflict,
			"já é do agente llm"},
		{"llm without its language model", func(r *ui.AgentCreateRequest) { r.LLM, r.PromptID = nil, nil },
			http.StatusBadRequest, "um agente LLM precisa dele"},
		{"llm without model", func(r *ui.AgentCreateRequest) { r.LLM.Model = "" }, http.StatusBadRequest,
			"modelo do LLM do agente"},
		{"kind", func(r *ui.AgentCreateRequest) { r.Kind = "grid" }, http.StatusBadRequest, `tipo \"grid\"`},
		{"name", func(r *ui.AgentCreateRequest) { r.Name = "../gold" }, http.StatusBadRequest, `agente \"../gold\" inválido`},
		{"bridge URL", func(r *ui.AgentCreateRequest) { r.Account.BridgeURL = "" }, http.StatusBadRequest,
			"a URL da bridge do agente gold"},
		{"magic", func(r *ui.AgentCreateRequest) { r.Account.Magic = ptrTo(int64(-1)) }, http.StatusBadRequest,
			"magic number -1"},
		{"prompt missing", func(r *ui.AgentCreateRequest) { r.PromptID = ptrTo(int64(999999)) },
			http.StatusBadRequest, "prompt 999999 do agente gold não existe"},
		{"prompt without a language model", func(r *ui.AgentCreateRequest) {
			r.Kind, r.LLM, r.Params, r.LLMAPIKey = "trend", nil, nil, nil
		}, http.StatusBadRequest, "Só agentes LLM têm prompt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := gold()
			tc.edit(&request)
			resp, body := call(http.MethodPost, "/api/settings/agents", request)
			assert.Equal(t, tc.status, resp.StatusCode, body)
			assert.Contains(t, body, tc.want)
			for _, value := range secrets {
				assert.NotContains(t, body, value, "never a secret")
			}
		})
	}
	assert.Equal(t, []string{"llm"}, agentNames(t, call), "none created")

	// an LLM agent, its params merged, its secrets written and never told
	resp, body := call(http.MethodPost, "/api/settings/agents", gold())
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	for _, value := range secrets {
		assert.NotContains(t, body, value, "never a secret")
	}
	assert.Equal(t, ui.AgentSettings{Name: "gold", Kind: "llm", Mode: "live", APIAddress: "localhost:15600",
		Launched: true, Restart: true, TradePanel: true,
		Params: map[string]string{"pair": "XAUUSDm", "llm-min-stop-atr": "1"},
		Account: &ui.AgentAccountSettings{BridgeURL: "http://127.0.0.1:18812", Magic: ptrTo(int64(4242)),
			Comment: secret("nb:gold")},
		LLM:   &ui.AgentLLMSettings{Provider: "openai", Model: "gpt-5", ReasoningEffort: secret("low")},
		Model: "openai gpt-5", PromptID: &prompt.ID,
		Secrets:    ui.AgentSecretsPresent{LLMAPIKey: true, BridgeToken: true},
		Protection: ui.ProtectionSettings{Unit: ui.ProtectionUnitATR}},
		agentSettingsOf(t, body))
	stored, err := c.store.AgentByName(ctx, "gold")
	require.NoError(t, err)
	assert.Equal(t, store.Params{"pair": "XAUUSDm"}, stored.Params)
	assert.Equal(t, store.Params{"llm-min-stop-atr": "1"}, stored.LLM.Params, "kept with its language model")
	values, err := c.store.AgentSecrets(ctx, stored.ID)
	require.NoError(t, err)
	assert.Equal(t, store.AgentSecretValues{LLMAPIKey: secrets[0], BridgeToken: secrets[1]}, values)
	assert.False(t, c.manager.supervisor.Running("gold"), "disabled: not launched")

	// one enabled, launched at once, last on the dashboard
	resp, body = call(http.MethodPost, "/api/settings/agents", ui.AgentCreateRequest{Name: "matrend", Kind: "trend",
		Mode: "paper", APIAddress: "localhost:15601", Enabled: true, Restart: boolPtr(false),
		Params:  map[string]string{"strategy": "matrend"},
		Account: ui.AgentAccountSettings{BridgeURL: "http://127.0.0.1:18812"}})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	matrend := agentSettingsOf(t, body)
	assert.True(t, matrend.Enabled)
	assert.True(t, matrend.Launched)
	assert.False(t, matrend.Restart)
	assert.Nil(t, matrend.LLM)
	assert.Equal(t, ui.AgentSecretsPresent{}, matrend.Secrets)
	require.Eventually(t, func() bool { return strings.Contains(c.stdout.String(), "[matrend] started") },
		5*time.Second, 10*time.Millisecond, "launched at once")
	assert.Equal(t, []string{"llm", "gold", "matrend"}, agentNames(t, call))

	assertProtected(t, srv, http.MethodPost, "/api/settings/agents", `{"name":"x","kind":"trend"}`)
	assert.Len(t, agentNames(t, call), 3)
}

func TestSettingsAPI_EditAnAgent(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	c.agent(t, "llm", "llm", false)
	c.agent(t, "matrend", "trend", true)
	call, srv := settingsSession(t, c)
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return strings.Contains(c.stdout.String(), "[matrend] started") },
		5*time.Second, 10*time.Millisecond)
	secrets := []string{"sk-llm", "bridge-llm", "bridge-matrend", "bridge-rotated"}
	patch := func(name string, request interface{}) (int, ui.AgentSettings, string) {
		t.Helper()
		resp, body := call(http.MethodPatch, "/api/settings/agents/"+name, request)
		for _, value := range secrets {
			assert.NotContains(t, body, value, "never a secret")
		}
		var settings ui.AgentSettings
		if resp.StatusCode == http.StatusOK {
			settings = agentSettingsOf(t, body)
		}
		return resp.StatusCode, settings, body
	}

	// its settings
	status, llm, body := patch("llm", ui.AgentSettingsRequest{Mode: secret("paper"),
		APIAddress: secret("localhost:15700"), Launched: boolPtr(false), Restart: boolPtr(false)})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "paper", llm.Mode)
	assert.Equal(t, "localhost:15700", llm.APIAddress)
	assert.False(t, llm.Launched)
	assert.False(t, llm.Restart)
	assert.Equal(t, "llm", llm.Name)
	assert.Equal(t, "llm", llm.Kind)

	// its params, merged, replaced
	status, llm, body = patch("llm", ui.AgentSettingsRequest{Params: map[string]string{"pair": "BTCUSDm",
		"llm-candles": "60"}})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, map[string]string{"pair": "BTCUSDm", "llm-candles": "60"}, llm.Params)
	stored, err := c.store.AgentByName(ctx, "llm")
	require.NoError(t, err)
	assert.Equal(t, store.Params{"pair": "BTCUSDm"}, stored.Params)
	assert.Equal(t, store.Params{"llm-candles": "60"}, stored.LLM.Params, "kept with its language model")
	_, llm, _ = patch("llm", ui.AgentSettingsRequest{TradePanel: boolPtr(true)})
	assert.Equal(t, map[string]string{"pair": "BTCUSDm", "llm-candles": "60"}, llm.Params, "left as they are")
	status, llm, _ = patch("llm", map[string]interface{}{"params": map[string]string{}})
	require.Equal(t, http.StatusOK, status)
	assert.Empty(t, llm.Params, "{} removes them")

	// its account
	status, llm, body = patch("llm", ui.AgentSettingsRequest{Account: &ui.AgentAccountRequest{
		BridgeURL: secret("http://127.0.0.1:18813"), Magic: ptrTo(int64(4242)), Comment: secret("nb:edited"),
		RealAllowed: boolPtr(true)}})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, &ui.AgentAccountSettings{BridgeURL: "http://127.0.0.1:18813", Magic: ptrTo(int64(4242)),
		Comment: secret("nb:edited"), RealAllowed: true}, llm.Account)
	_, llm, _ = patch("llm", ui.AgentSettingsRequest{Account: &ui.AgentAccountRequest{Magic: ptrTo(int64(0)),
		Comment: secret("")}})
	assert.Equal(t, &ui.AgentAccountSettings{BridgeURL: "http://127.0.0.1:18813", RealAllowed: true}, llm.Account,
		"their defaults")

	// its language model
	status, llm, body = patch("llm", ui.AgentSettingsRequest{LLM: &ui.AgentLLMRequest{Provider: secret("anthropic"),
		Model: secret("claude-sonnet-4-5"), BaseURL: secret("https://api.anthropic.com"),
		ReasoningEffort: secret("medium"), MaxTokens: ptrTo(2048)}})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, &ui.AgentLLMSettings{Provider: "anthropic", Model: "claude-sonnet-4-5",
		BaseURL: secret("https://api.anthropic.com"), ReasoningEffort: secret("medium"), MaxTokens: ptrTo(2048)},
		llm.LLM)
	assert.Equal(t, "anthropic claude-sonnet-4-5", llm.Model)
	_, llm, _ = patch("llm", ui.AgentSettingsRequest{LLM: &ui.AgentLLMRequest{BaseURL: secret(""),
		ReasoningEffort: secret(""), MaxTokens: ptrTo(0)}})
	assert.Equal(t, &ui.AgentLLMSettings{Provider: "anthropic", Model: "claude-sonnet-4-5"}, llm.LLM,
		"their defaults")
	assert.Equal(t, ui.AgentSecretsPresent{LLMAPIKey: true, BridgeToken: true}, llm.Secrets, "the secrets kept")

	// refused, nothing changed
	before, err := c.store.AgentByName(ctx, "llm")
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		agent   string
		request ui.AgentSettingsRequest
		status  int
		want    string
	}{
		{"mode", "llm", ui.AgentSettingsRequest{Mode: secret("backtest")}, http.StatusBadRequest, "use live ou paper"},
		{"address", "llm", ui.AgentSettingsRequest{APIAddress: secret("localhost:0")}, http.StatusBadRequest,
			"host:porta"},
		{"address taken", "llm", ui.AgentSettingsRequest{APIAddress: secret("localhost:15507")}, http.StatusConflict,
			"já é do agente matrend"},
		{"param of a field", "llm", ui.AgentSettingsRequest{Params: map[string]string{"api": "localhost:1"}},
			http.StatusBadRequest, "parâmetro api do agente llm tem campo próprio"},
		{"bridge URL", "llm", ui.AgentSettingsRequest{Account: &ui.AgentAccountRequest{
			BridgeURL: secret("ftp://127.0.0.1")}}, http.StatusBadRequest, "URL da bridge do agente llm deve ser http(s)"},
		{"comment", "llm", ui.AgentSettingsRequest{Account: &ui.AgentAccountRequest{Comment: secret("[tp 1]")}},
			http.StatusBadRequest, "sem começar com ["},
		{"model removed", "llm", ui.AgentSettingsRequest{LLM: &ui.AgentLLMRequest{Model: secret("")}},
			http.StatusBadRequest, "modelo do LLM do agente"},
		{"reasoning effort", "llm", ui.AgentSettingsRequest{LLM: &ui.AgentLLMRequest{
			ReasoningEffort: secret("max")}}, http.StatusBadRequest, `esforço de raciocínio \"max\"`},
		{"language model of a trend agent", "matrend", ui.AgentSettingsRequest{LLM: &ui.AgentLLMRequest{
			Provider: secret("openai"), Model: secret("gpt-5")}}, http.StatusBadRequest,
			"agente matrend é do tipo trend, que não usa LLM"},
		{"param of a language model", "matrend", ui.AgentSettingsRequest{Params: map[string]string{
			"news-block-before": "30m"}}, http.StatusBadRequest, "só para agentes LLM"},
		{"unknown", "nobody", ui.AgentSettingsRequest{Mode: secret("paper")}, http.StatusNotFound,
			"agente nobody não existe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, body := patch(tc.agent, tc.request)
			assert.Equal(t, tc.status, status, body)
			assert.Contains(t, body, tc.want)
		})
	}
	after, err := c.store.AgentByName(ctx, "llm")
	require.NoError(t, err)
	assert.Equal(t, before, after, "nothing changed")

	// a change of the settings of an agent running waits for its restart, not
	// a change of its switches
	status, matrend, body := patch("matrend", ui.AgentSettingsRequest{TelegramControl: boolPtr(true)})
	require.Equal(t, http.StatusOK, status, body)
	assert.True(t, matrend.Running)
	assert.False(t, matrend.RestartPending, "applied while it runs")
	for _, request := range []ui.AgentSettingsRequest{
		{Mode: secret("paper")},
		{APIAddress: secret("localhost:15701")},
		{Restart: boolPtr(true)},
		{Params: map[string]string{"strategy": "emacross"}},
		{Account: &ui.AgentAccountRequest{Comment: secret("nb:emacross")}},
		{BridgeToken: secret("bridge-rotated")},
	} {
		status, matrend, body = patch("matrend", request)
		require.Equal(t, http.StatusOK, status, body)
		assert.True(t, matrend.RestartPending, body)
		resp, body := call(http.MethodPost, "/api/settings/agents/matrend/restart", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		assert.False(t, agentSettingsOf(t, body).RestartPending, "restarted with it")
	}

	// and the one of the language model of an LLM agent running
	status, llm, body = patch("llm", ui.AgentSettingsRequest{Enabled: boolPtr(true), Launched: boolPtr(true)})
	require.Equal(t, http.StatusOK, status, body)
	assert.True(t, llm.Running, "launched at once")
	assert.False(t, llm.RestartPending)
	_, llm, _ = patch("llm", ui.AgentSettingsRequest{LLM: &ui.AgentLLMRequest{Model: secret("claude-opus-4-1")}})
	assert.True(t, llm.RestartPending)

	assertProtected(t, srv, http.MethodPatch, "/api/settings/agents/llm", `{"mode":"live"}`)
	stored, err = c.store.AgentByName(ctx, "llm")
	require.NoError(t, err)
	assert.Equal(t, "paper", stored.Mode, "left as it was")
}

func TestSettingsAPI_DeleteAnAgent(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	// an agent ignoring the interrupts, killed at the stop timeout
	c.manager.agents.Commands["momentum"] = os.Args[0] + " " + fakeBotPrefix + "stubborn"
	c.agent(t, "llm", "llm", true)
	adxema := c.agent(t, "adxema", "momentum", true)
	call, srv := settingsSession(t, c)
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool {
		return strings.Contains(c.stdout.String(), "[llm] started") &&
			strings.Contains(c.stdout.String(), "[adxema] started") // ignoring the interrupts then
	}, 5*time.Second, 10*time.Millisecond)

	resp, body := call(http.MethodDelete, "/api/settings/agents/adxema", nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "enabled")
	assert.Contains(t, body, "agente adxema antes")
	resp, _ = call(http.MethodDelete, "/api/settings/agents/nobody", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// disabled, deleted once stopped
	resp, body = call(http.MethodPatch, "/api/settings/agents/adxema", ui.AgentSettingsRequest{
		Enabled: boolPtr(false)})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	resp, body = call(http.MethodDelete, "/api/settings/agents/adxema", nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)
	assert.Contains(t, body, "ainda está parando")
	require.Eventually(t, func() bool { return !c.manager.State("adxema").Running }, 10*time.Second,
		50*time.Millisecond)
	resp, body = call(http.MethodDelete, "/api/settings/agents/adxema", nil)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode, body)
	assert.Empty(t, body)

	assert.Equal(t, []string{"llm"}, agentNames(t, call), "gone from the dashboard")
	var served []string
	for _, a := range c.dashboard.agents.list() {
		served = append(served, a.Name)
	}
	assert.Equal(t, []string{"llm"}, served)
	resp, _ = call(http.MethodDelete, "/api/settings/agents/adxema", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "deleted already")
	resp, _ = call(http.MethodPatch, "/api/settings/agents/adxema", ui.AgentSettingsRequest{})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// its name and its address free for a new agent
	resp, body = call(http.MethodPost, "/api/settings/agents", ui.AgentCreateRequest{Name: "adxema",
		Kind: "momentum", Mode: "live", APIAddress: adxema.APIAddress,
		Account: ui.AgentAccountSettings{BridgeURL: "http://127.0.0.1:18812"}})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	again, err := c.store.AgentByName(ctx, "adxema")
	require.NoError(t, err)
	assert.NotEqual(t, adxema.ID, again.ID, "an agent of its own")
	assert.Equal(t, []string{"llm", "adxema"}, agentNames(t, call))

	assertProtected(t, srv, http.MethodDelete, "/api/settings/agents/adxema", "")
	assert.Equal(t, []string{"llm", "adxema"}, agentNames(t, call), "left as it was")
}

func TestSettingsAPI_ThePrompts(t *testing.T) {
	c := newManaged(t)
	c.agent(t, "llm", "llm", false)
	call, _ := settingsSession(t, c)

	resp, body := call(http.MethodGet, "/api/prompts", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var prompts ui.PromptsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &prompts))
	assert.Len(t, prompts.Prompts, 4, "the ones only a language model trades and the one created in the dashboard")

	resp, body = call(http.MethodPost, "/api/prompts", ui.PromptRequest{Name: "breakout_v2",
		Description: secret("the breakouts"), Content: secret("# Indicators: atr\n\nSetup: v1")})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var created ui.PromptDetail
	require.NoError(t, json.Unmarshal([]byte(body), &created))
	assert.Equal(t, 1, created.Version)
	path := fmt.Sprintf("/api/prompts/%d", created.ID)

	resp, _ = call(http.MethodPost, "/api/prompts", ui.PromptRequest{Name: "breakout_v2", Content: secret("x")})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	resp, _ = call(http.MethodPost, "/api/prompts", ui.PromptRequest{Name: "no_content"})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp, body = call(http.MethodPut, path, ui.PromptRequest{Content: secret("# Indicators: atr\n\nSetup: v2"),
		Version: 1})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var edited ui.PromptDetail
	require.NoError(t, json.Unmarshal([]byte(body), &edited))
	assert.Equal(t, 2, edited.Version)
	resp, _ = call(http.MethodPut, path, ui.PromptRequest{Content: secret("Setup: v3"), Version: 1})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "edited on version 1, now 2")
	version, err := c.store.PromptVersion(context.Background(), created.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, "web:admin", version.CreatedBy, "who edited it")

	llm, err := c.store.AgentByName(context.Background(), "llm")
	require.NoError(t, err)
	require.NoError(t, c.store.SetAgentPrompt(context.Background(), llm.ID, &created.ID))
	_, body = call(http.MethodGet, path, nil)
	require.NoError(t, json.Unmarshal([]byte(body), &edited))
	assert.Equal(t, []string{"llm"}, edited.UsedBy)
	resp, _ = call(http.MethodDelete, path, nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "used by llm")
	require.NoError(t, c.store.SetAgentPrompt(context.Background(), llm.ID, nil))
	resp, _ = call(http.MethodDelete, path, nil)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp, _ = call(http.MethodGet, path, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = call(http.MethodGet, "/api/prompts/x", nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestSettingsAPI_TelegramAndUsers(t *testing.T) {
	c := newManaged(t)
	call, srv := settingsSession(t, c)

	resp, body := call(http.MethodGet, "/api/settings/telegram", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"enabled":false,"has_token":false,"running":false,"users":[]}`, body)
	resp, body = call(http.MethodPatch, "/api/settings/telegram", ui.TelegramSettingsRequest{Enabled: boolPtr(true),
		Token: secret("123:the-telegram-token")})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, body, "the-telegram-token")
	resp, _ = call(http.MethodPost, "/api/settings/telegram/users", ui.TelegramUserRequest{TelegramID: 111,
		Name: "Ray"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	call(http.MethodPost, "/api/settings/telegram/users", ui.TelegramUserRequest{TelegramID: 222})
	resp, _ = call(http.MethodPost, "/api/settings/telegram/users", ui.TelegramUserRequest{TelegramID: 111})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	var telegram ui.TelegramSettingsResponse
	_, body = call(http.MethodGet, "/api/settings/telegram", nil)
	require.NoError(t, json.Unmarshal([]byte(body), &telegram))
	assert.True(t, telegram.Enabled)
	assert.True(t, telegram.HasToken)
	require.Len(t, telegram.Users, 2)
	resp, _ = call(http.MethodDelete, fmt.Sprintf("/api/settings/telegram/users/%d", telegram.Users[0].ID), nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var admin store.User
	adminRole, err := c.store.AdminRole(context.Background())
	require.NoError(t, err)
	resp, body = call(http.MethodPost, "/api/users", ui.UserRequest{Username: "second", Password: secret("second-pw1"),
		RoleID: &adminRole.ID})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var second ui.UserInfo
	require.NoError(t, json.Unmarshal([]byte(body), &second))
	require.NotNil(t, second.Role)
	assert.Equal(t, "Administrador", second.Role.Name)
	resp, _ = call(http.MethodPost, "/api/users", ui.UserRequest{Username: "third", Password: secret("short"),
		RoleID: &adminRole.ID})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, body = call(http.MethodPost, "/api/users", ui.UserRequest{Username: "fourth", Password: secret("fourth-pw1")})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "a role needed")
	assert.Contains(t, body, "papel")
	resp, body = call(http.MethodGet, "/api/users", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, body, "password")

	secondLogin, _ := login(t, srv, "second", "second-pw1")
	secondCookies := sessionOf(secondLogin)
	resp, _ = call(http.MethodPatch, fmt.Sprintf("/api/users/%d", second.ID), ui.UserRequest{Password: secret("new-pw-2")})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", secondCookies)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "its sessions closed by the new password")

	admin, err = c.store.Authenticate(context.Background(), "admin", "admin-password")
	require.NoError(t, err)
	resp, _ = call(http.MethodPatch, fmt.Sprintf("/api/users/%d", second.ID), ui.UserRequest{Enabled: boolPtr(false)})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp, body = call(http.MethodPatch, fmt.Sprintf("/api/users/%d", admin.ID), ui.UserRequest{Enabled: boolPtr(false)})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "the last one enabled")
	assert.Contains(t, body, "último usuário ativo")
	resp, _ = call(http.MethodDelete, fmt.Sprintf("/api/users/%d", second.ID), nil)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp, _ = call(http.MethodDelete, "/api/users/999999", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
