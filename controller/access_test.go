//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// routePermissions are the permissions each route of the API needs, any one
// of them enough, by "METHOD pattern": every route of the router listed, see
// TestAccess_EveryRouteNeedsItsPermission. "*" stands for any method.
var routePermissions = map[string][]string{
	"GET /api/agents":                         {ui.PermDashboardView},
	"POST /api/agents/{agent}/controls/stop":  {ui.PermTradingOperate},
	"POST /api/agents/{agent}/controls/start": {ui.PermTradingOperate},
	"* /api/agents/{agent}/*":                 nil, // see proxyPermissions

	"GET /api/settings/exposure":                      {ui.PermLimitsView},
	"PUT /api/settings/exposure":                      {ui.PermLimitsManage},
	"GET /api/settings/agents":                        {ui.PermAgentsView, ui.PermLimitsView},
	"POST /api/settings/agents":                       {ui.PermAgentsManage},
	"POST /api/settings/agents/params":                {ui.PermAgentsManage},
	"PATCH /api/settings/agents/{name}":               {ui.PermAgentsManage},
	"DELETE /api/settings/agents/{name}":              {ui.PermAgentsManage},
	"POST /api/settings/agents/{name}/restart":        {ui.PermAgentsManage},
	"POST /api/settings/agents/{name}/rename":         {ui.PermAgentsManage},
	"GET /api/settings/agents/{name}/periods":         {ui.PermAgentsView},
	"POST /api/settings/agents/{name}/periods":        {ui.PermAgentsManage},
	"PUT /api/settings/agents/{name}/daily-limit":     {ui.PermLimitsManage},
	"GET /api/prompts":                                {ui.PermPromptsView, ui.PermAgentsManage},
	"POST /api/prompts":                               {ui.PermPromptsManage},
	"GET /api/prompts/{id}":                           {ui.PermPromptsView},
	"PUT /api/prompts/{id}":                           {ui.PermPromptsManage},
	"DELETE /api/prompts/{id}":                        {ui.PermPromptsManage},
	"GET /api/settings/telegram":                      {ui.PermTelegramView},
	"PATCH /api/settings/telegram":                    {ui.PermTelegramManage},
	"GET /api/settings/telegram/users":                {ui.PermTelegramView},
	"POST /api/settings/telegram/users":               {ui.PermTelegramManage},
	"DELETE /api/settings/telegram/users/{id}":        {ui.PermTelegramManage},
	"GET /api/positions/open":                         {ui.PermDashboardView},
	"GET /api/positions/closed":                       {ui.PermDashboardView},
	"GET /api/performance":                            {ui.PermPerformanceView},
	"GET /api/performance/excursions":                 {ui.PermPerformanceView},
	"GET /api/users":                                  {ui.PermUsersView},
	"POST /api/users":                                 {ui.PermUsersManage},
	"PATCH /api/users/{id}":                           {ui.PermUsersManage},
	"DELETE /api/users/{id}":                          {ui.PermUsersManage},
	"GET /api/config/catalog":                         configViewPermissions(),
	"POST /api/config/export":                         configViewPermissions(),
	"POST /api/config/import/preview":                 configManagePermissions(),
	"POST /api/config/import":                         configManagePermissions(),
	"GET /api/roles":                                  {ui.PermUsersView},
	"POST /api/roles":                                 {ui.PermUsersManage},
	"PATCH /api/roles/{id}":                           {ui.PermUsersManage},
	"DELETE /api/roles/{id}":                          {ui.PermUsersManage},
	"GET /api/settings/mt5-accounts":                  {ui.PermMT5AccountsView, ui.PermAgentsManage},
	"POST /api/settings/mt5-accounts":                 {ui.PermMT5AccountsManage},
	"PATCH /api/settings/mt5-accounts/{id}":           {ui.PermMT5AccountsManage},
	"DELETE /api/settings/mt5-accounts/{id}":          {ui.PermMT5AccountsManage},
	"GET /api/settings/mt5-accounts/{id}/switch":      {ui.PermMT5AccountsView},
	"POST /api/settings/mt5-accounts/{id}/activate":   {ui.PermMT5AccountsManage},
	"POST /api/settings/mt5-accounts/{id}/deactivate": {ui.PermMT5AccountsManage},
	"POST /api/settings/mt5-accounts/{id}/test":       {ui.PermMT5AccountsManage},
	"GET /api/settings/mt5-accounts/{id}/events":      {ui.PermMT5AccountsView},

	"GET /api/settings/supervisor":               {ui.PermSupervisorView, ui.PermSupervisorChat},
	"PATCH /api/settings/supervisor":             {ui.PermSupervisorManage},
	"GET /api/supervisor/overview":               {ui.PermSupervisorView},
	"POST /api/supervisor/run":                   {ui.PermSupervisorChat, ui.PermSupervisorManage},
	"GET /api/supervisor/runs":                   {ui.PermSupervisorView, ui.PermSupervisorChat},
	"GET /api/supervisor/runs/{id}":              {ui.PermSupervisorView, ui.PermSupervisorChat},
	"GET /api/supervisor/actions":                {ui.PermSupervisorView},
	"GET /api/supervisor/protections":            {ui.PermSupervisorView},
	"GET /api/supervisor/messages":               {ui.PermSupervisorChat},
	"POST /api/supervisor/messages":              {ui.PermSupervisorChat},
	"POST /api/supervisor/messages/stream":       {ui.PermSupervisorChat},
	"POST /api/supervisor/actions/{id}/confirm":  {ui.PermTradingOperate},
	"POST /api/supervisor/actions/{id}/reject":   {ui.PermTradingOperate},
	"POST /api/supervisor/agents/{name}/entries": {ui.PermTradingOperate},
	"POST /api/supervisor/agents/{name}/close":   {ui.PermTradingOperate},
	"POST /api/supervisor/close-all":             {ui.PermTradingOperate},
	"GET /api/settings/daily-limits":             {ui.PermLimitsView},
	"GET /api/settings/daily-limits/agents":      {ui.PermLimitsView},
	"GET /api/settings/asset-agents":             {ui.PermLimitsView, ui.PermBacktestsManage},
	"PUT /api/settings/asset-agents":             {ui.PermLimitsManage},
	"PUT /api/settings/daily-limits":             {ui.PermLimitsManage},
	"GET /api/daily-guard":                       {ui.PermDashboardView},
	"GET /api/settings/trading-hours":            {ui.PermLimitsView, ui.PermBacktestsManage},
	"PUT /api/settings/trading-hours":            {ui.PermLimitsManage},
	"GET /api/settings/news":                     {ui.PermNewsView},
	"PUT /api/settings/news":                     {ui.PermNewsManage},
	"POST /api/settings/news/test":               {ui.PermNewsManage},

	"GET /api/backtests":                   {ui.PermBacktestsView},
	"POST /api/backtests":                  {ui.PermBacktestsManage},
	"DELETE /api/backtests/{id}":           {ui.PermBacktestsManage},
	"GET /api/backtests/{id}/comparison":   {ui.PermBacktestsView},
	"GET /api/backtests/runs/{id}":         {ui.PermBacktestsView},
	"POST /api/backtests/runs/{id}/cancel": {ui.PermBacktestsManage},
}

// proxyPermissions are the ones of the proxy to the API of an agent: its
// charts read from the panel or the results of the agents, its controls
// operated.
var proxyPermissions = map[string][]string{
	http.MethodGet:  {ui.PermDashboardView, ui.PermAgentsView},
	http.MethodPost: {ui.PermTradingOperate},
}

// accessSessions logs in users of roles granting given permissions, one role
// and one user each, and returns a caller of the API as each of them.
type accessSessions struct {
	t     *testing.T
	store *store.Store
	srv   *httptest.Server
	by    map[string]func(method, path, body string) (*http.Response, string)
}

func (a *accessSessions) as(perms ...string) func(method, path, body string) (*http.Response, string) {
	t := a.t
	t.Helper()
	key := strings.Join(perms, ",")
	if call, ok := a.by[key]; ok {
		return call
	}
	ctx := context.Background()
	n := len(a.by) + 1
	role, err := a.store.CreateRole(ctx, store.RoleChange{Name: ptrTo(fmt.Sprintf("papel %d", n)),
		Permissions: &perms, By: "tests"})
	require.NoError(t, err)
	username, password := fmt.Sprintf("user%d", n), fmt.Sprintf("password-%d", n)
	user, err := a.store.CreateUser(ctx, username, password)
	require.NoError(t, err)
	require.NoError(t, a.store.SetUserRole(ctx, user.ID, role.ID, "tests"))
	resp, _ := login(t, a.srv, username, password)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cookies, csrf := sessionOf(resp), cookieOf(resp, csrfCookie).Value
	call := func(method, path, body string) (*http.Response, string) {
		return do(t, method, a.srv.URL+path, body, cookies, csrfHeader, csrf)
	}
	a.by[key] = call
	return call
}

// pathOf returns a path of pattern whose parameters name nothing: the route
// reached, its record not found.
func pathOf(pattern string) string {
	path := pattern
	for _, param := range []string{"{agent}", "{name}"} {
		path = strings.ReplaceAll(path, param, "nobody")
	}
	path = strings.ReplaceAll(path, "{id}", "999999")
	return strings.ReplaceAll(path, "/*", "/pairs")
}

// Every route of the API needs the permissions it declares: a user whose role
// grants none is refused (403) on each, whatever it asks, straight to the
// API; a user holding one of them gets past the check. A route missing from
// routePermissions, or listed and gone, fails the test.
func TestAccess_EveryRouteNeedsItsPermission(t *testing.T) {
	s := newSupervised(t)
	c := s.managed
	c.dashboard.ServeBacktests(c.store, config.Agents{}, config.Backtest{Concurrency: 1, Dir: t.TempDir(),
		MaxPeriodDays: 30, MaxRuns: 10, LLMMaxCalls: 10, LLMMaxCost: 1})
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.js"), []byte("app"), 0o644))
	router := c.dashboard.router(bundle{dir: dir, version: "v1"}, nil)
	srv := httptest.NewServer(c.dashboard.auth.protect(router))
	t.Cleanup(srv.Close)
	sessions := &accessSessions{t: t, store: c.store, srv: srv, by: map[string]func(string, string, string) (
		*http.Response, string){}}
	createAdmin(t, c.store, "admin", "admin-password") // the dashboard keeps one

	routes := map[string]bool{}
	require.NoError(t, chi.Walk(router.(chi.Routes), func(method, route string, _ http.Handler,
		_ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/") {
			return nil
		}
		if route == "/api/agents/{agent}/*" {
			routes["* "+route] = true
			return nil
		}
		routes[method+" "+route] = true
		return nil
	}))
	for key := range routes {
		_, listed := routePermissions[key]
		assert.True(t, listed, "the route %s declares no permission in routePermissions", key)
	}
	for key := range routePermissions {
		assert.True(t, routes[key], "the route %s is gone: drop it from routePermissions", key)
	}

	nothing := sessions.as()
	check := func(method, pattern string, perms []string) {
		path, body := pathOf(pattern), ""
		if !safeMethod(method) {
			body = "{}"
		}
		resp, text := nothing(method, path, body)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, "%s %s without permission: %s", method, path, text)
		assert.Contains(t, text, errForbidden, "%s %s", method, path)
		for _, p := range perms {
			resp, text = sessions.as(p)(method, path, body)
			assert.NotEqual(t, http.StatusForbidden, resp.StatusCode, "%s %s with %s: %s", method, path, p, text)
		}
	}
	for key, perms := range routePermissions {
		method, pattern, _ := strings.Cut(key, " ")
		if method == "*" {
			for method, perms := range proxyPermissions {
				check(method, pattern, perms)
			}
			continue
		}
		check(method, pattern, perms)
	}
}

// The roles and the users with them through the API: a role created, given to
// a user who logs in with its permissions; its permissions changed, taken at
// the next request; the role taken away, the session closed and the login
// refused. The fixed role never changes; a role held isn't deleted; the last
// administrator is kept.
func TestAccess_RolesThroughTheAPI(t *testing.T) {
	c := newManaged(t)
	call, srv := settingsSession(t, c)
	ctx := context.Background()

	resp, body := call(http.MethodGet, "/api/roles", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var roles ui.RolesResponse
	require.NoError(t, json.Unmarshal([]byte(body), &roles))
	require.Len(t, roles.Roles, 1)
	admin := roles.Roles[0]
	assert.True(t, admin.Fixed)
	assert.Equal(t, ui.AllPermissions(), admin.Permissions)
	assert.Equal(t, int64(1), admin.Users)
	assert.Equal(t, ui.PermissionAreas(), roles.Areas)

	resp, body = call(http.MethodPost, "/api/roles", ui.RoleRequest{Name: ptrTo("Desempenho"),
		Permissions: &[]string{ui.PermPerformanceView}})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var viewer ui.RoleInfo
	require.NoError(t, json.Unmarshal([]byte(body), &viewer))
	resp, _ = call(http.MethodPost, "/api/roles", ui.RoleRequest{Name: ptrTo("desempenho")})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "a name once")
	resp, _ = call(http.MethodPost, "/api/roles", ui.RoleRequest{Name: ptrTo("Outro"),
		Permissions: &[]string{"made:up"}})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "a permission of the catalog only")

	resp, body = call(http.MethodPost, "/api/users", ui.UserRequest{Username: "maria", Password: secret("maria-pass"),
		RoleID: &viewer.ID})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var maria ui.UserInfo
	require.NoError(t, json.Unmarshal([]byte(body), &maria))
	logged, body := login(t, srv, "maria", "maria-pass")
	require.Equal(t, http.StatusOK, logged.StatusCode, body)
	assert.Contains(t, body, `"permissions":["performance:view"]`)
	cookies, csrf := sessionOf(logged), cookieOf(logged, csrfCookie).Value
	asMaria := func(method, path string) int {
		resp, _ := do(t, method, srv.URL+path, "{}", cookies, csrfHeader, csrf)
		return resp.StatusCode
	}
	assert.NotEqual(t, http.StatusForbidden, asMaria(http.MethodGet, "/api/performance"), "allowed")
	assert.Equal(t, http.StatusForbidden, asMaria(http.MethodGet, "/api/agents"))
	assert.Equal(t, http.StatusForbidden, asMaria(http.MethodPost, "/api/roles"), "she can't give herself more")
	assert.Equal(t, http.StatusForbidden, asMaria(http.MethodPatch, fmt.Sprintf("/api/users/%d", maria.ID)))

	resp, body = call(http.MethodPatch, fmt.Sprintf("/api/roles/%d", viewer.ID), ui.RoleRequest{
		Permissions: &[]string{ui.PermPerformanceView, ui.PermDashboardView}})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Equal(t, http.StatusOK, asMaria(http.MethodGet, "/api/agents"), "taken at her next request")

	resp, body = call(http.MethodPatch, fmt.Sprintf("/api/roles/%d", admin.ID), ui.RoleRequest{Name: ptrTo("Chefe")})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "the fixed role: %s", body)
	resp, _ = call(http.MethodDelete, fmt.Sprintf("/api/roles/%d", admin.ID), nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, _ = call(http.MethodDelete, fmt.Sprintf("/api/roles/%d", viewer.ID), nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "held by maria")

	me, err := c.store.Authenticate(ctx, "admin", "admin-password")
	require.NoError(t, err)
	resp, body = call(http.MethodPatch, fmt.Sprintf("/api/users/%d", me.ID), ui.UserRequest{RoleID: &viewer.ID})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, "the last administrator: %s", body)
	assert.Contains(t, body, "administrá-lo")

	none := int64(0)
	resp, body = call(http.MethodPatch, fmt.Sprintf("/api/users/%d", maria.ID), ui.UserRequest{RoleID: &none})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.NotContains(t, body, `"role"`)
	assert.Equal(t, http.StatusUnauthorized, asMaria(http.MethodGet, "/api/performance"), "her session closed")
	resp, body = login(t, srv, "maria", "maria-pass")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, body, errNoRole)

	resp, _ = call(http.MethodDelete, fmt.Sprintf("/api/roles/%d", viewer.ID), nil)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode, "held by none")
}

// The chat of XTrader AI acts at once only for a user allowed to: without
// trading:operate the model is given neither execute_action nor
// protect_position (nor set_daily_limits without limits:manage), told why, and
// a call of one is refused all the same; allowed, it is given them.
func TestAccess_ChatToolsByPermission(t *testing.T) {
	s := newSupervised(t)
	s.liveAgent(t, "orb", 303, true)
	user := userOfTests(t, s)
	toolNames := func(tools []llm.ToolSpec) []string {
		var names []string
		for _, tool := range tools {
			names = append(names, tool.Name)
		}
		return names
	}

	var offered []string
	var system, refusal string
	s.chat.script(
		func(sys string, _ []llm.ChatMessage, tools []llm.ToolSpec) (llm.ChatAnswer, error) {
			offered, system = toolNames(tools), sys
			return llm.ChatAnswer{ToolCalls: []llm.ToolCall{tool("x1", "execute_action", map[string]any{
				"action": store.ActionBlockEntries, "agents": []string{"orb"}, "reason": "bloqueie o orb"})}}, nil
		},
		func(_ string, messages []llm.ChatMessage, _ []llm.ToolSpec) (llm.ChatAnswer, error) {
			refusal = messages[len(messages)-1].Content
			return llm.ChatAnswer{Content: "Você não tem permissão para operar."}, nil
		})
	chatOnly := withAccess(context.Background(), newAccess(roleOf(9, "Chat", ui.PermSupervisorChat)))
	_, err := s.sup.Ask(chatOnly, user, "maria", "bloqueie o orb")
	require.NoError(t, err)
	assert.NotContains(t, offered, "execute_action")
	assert.NotContains(t, offered, "protect_position")
	assert.NotContains(t, offered, "set_daily_limits")
	assert.Contains(t, offered, "propose_action", "a proposal a user allowed confirms")
	assert.Contains(t, offered, "get_daily_limits")
	assert.Contains(t, system, "não tem permissão para operar")
	assert.Contains(t, refusal, "refused")
	actions, err := s.store.SupervisorActions(context.Background(), 10)
	require.NoError(t, err)
	assert.Empty(t, actions, "nothing executed")

	s.chat.script(func(sys string, _ []llm.ChatMessage, tools []llm.ToolSpec) (llm.ChatAnswer, error) {
		offered, system = toolNames(tools), sys
		return llm.ChatAnswer{Content: "Pronto."}, nil
	})
	_, err = s.sup.Ask(adminCtx(context.Background()), user, "admin", "como estão os agentes?")
	require.NoError(t, err)
	for _, name := range []string{"execute_action", "protect_position", "set_daily_limits"} {
		assert.True(t, slices.Contains(offered, name), "%s given to an administrator", name)
	}
	assert.NotContains(t, system, "Permissões deste usuário")
}
