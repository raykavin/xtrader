//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raykavin/gobox/pagination"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The page of a list asked for: ?page= from 1, ?per_page= bounded, ?q=
// trimmed, at most ui.MaxSearch characters; the whole list without ?page=;
// 400 for one invalid.
func TestListQuery(t *testing.T) {
	parse := func(query string) (store.ListQuery, int) {
		w := httptest.NewRecorder()
		q, ok := listQuery(w, httptest.NewRequest(http.MethodGet, "/api/prompts?"+query, nil))
		if !ok {
			return q, w.Code
		}
		return q, http.StatusOK
	}
	q, code := parse("")
	assert.Equal(t, http.StatusOK, code)
	assert.False(t, q.Paged(), "the whole list")
	q, _ = parse("page=2&per_page=5&q=%20ouro%20xau%20")
	assert.Equal(t, store.ListQuery{Page: pagination.Page{Number: 2, PerPage: 5}, Search: "ouro xau"}, q)
	q, _ = parse("page=1")
	assert.Equal(t, ui.DefPerPage, q.Page.PerPage)
	q, _ = parse("page=1&per_page=1000")
	assert.Equal(t, ui.MaxPerPage, q.Page.PerPage, "bounded")
	q, _ = parse("q=ação")
	assert.Equal(t, "ação", q.Search)
	assert.False(t, q.Paged(), "searched, the whole list")
	for _, bad := range []string{"page=0", "page=-1", "page=x", "page=1&per_page=0", "per_page=dez",
		"q=" + strings.Repeat("a", ui.MaxSearch+1)} {
		_, code := parse(bad)
		assert.Equal(t, http.StatusBadRequest, code, bad)
	}
	_, code = parse("q=" + strings.Repeat("ç", ui.MaxSearch))
	assert.Equal(t, http.StatusOK, code, "characters, not bytes")
}

// A list kept in memory by pages: the last one for a page past it, all of it
// without a page.
func TestPageSlice(t *testing.T) {
	rows := []int{1, 2, 3, 4, 5}
	got, page := pageSlice(rows, store.ListQuery{})
	assert.Equal(t, rows, got)
	assert.Equal(t, ui.PageInfo{Total: 5, Page: 1, PerPage: 5, TotalPages: 1}, page)
	got, page = pageSlice(rows, store.ListQuery{Page: pagination.Page{Number: 2, PerPage: 2}})
	assert.Equal(t, []int{3, 4}, got)
	assert.Equal(t, ui.PageInfo{Total: 5, Page: 2, PerPage: 2, TotalPages: 3, HasNext: true, HasPrev: true}, page)
	got, page = pageSlice(rows, store.ListQuery{Page: pagination.Page{Number: 9, PerPage: 2}})
	assert.Equal(t, []int{5}, got, "the last page")
	assert.Equal(t, 3, page.Page)
	got, page = pageSlice([]int{}, store.ListQuery{Page: pagination.Page{Number: 3, PerPage: 2}})
	assert.Empty(t, got)
	assert.Equal(t, ui.PageInfo{Page: 1, PerPage: 2}, page, "no row, no page")
	_, page = pageSlice([]int{}, store.ListQuery{})
	assert.Equal(t, ui.PageInfo{Page: 1}, page)
}

// The lists of the settings by pages and searched through the API: the page
// asked for with its rows, the total of the search, the last page for one
// past it; the whole list without ?page=, as before; 400 for a page invalid.
func TestPagedLists_TheSettings(t *testing.T) {
	c := newManaged(t)
	call, _ := settingsSession(t, c)
	ctx := context.Background()
	for i := 1; i <= 7; i++ {
		_, err := c.store.AddTelegramUser(ctx, int64(500+i), fmt.Sprintf("Usuário %d", i))
		require.NoError(t, err)
	}
	_, err := c.store.AddTelegramUser(ctx, 999, "Gestão")
	require.NoError(t, err)

	telegram := func(query string) (ui.TelegramUsersResponse, int) {
		t.Helper()
		resp, body := call(http.MethodGet, "/api/settings/telegram/users"+query, nil)
		var response ui.TelegramUsersResponse
		if resp.StatusCode == http.StatusOK {
			require.NoError(t, json.Unmarshal([]byte(body), &response))
		}
		return response, resp.StatusCode
	}
	page, code := telegram("?page=2&per_page=3")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, ui.PageInfo{Total: 8, Page: 2, PerPage: 3, TotalPages: 3, HasNext: true, HasPrev: true},
		page.PageInfo)
	require.Len(t, page.Users, 3)
	assert.EqualValues(t, 504, page.Users[0].TelegramID, "by their ID")
	page, _ = telegram("?page=1&per_page=3&q=USUARIO")
	assert.Equal(t, 7, page.Total, "the total of the search")
	page, _ = telegram("?page=1&per_page=3&q=gestao")
	require.Len(t, page.Users, 1)
	assert.Equal(t, "Gestão", page.Users[0].Name)
	page, _ = telegram("?page=5&per_page=3")
	assert.Equal(t, 3, page.Page, "the last page")
	require.Len(t, page.Users, 2)
	page, _ = telegram("?page=1&q=ninguem")
	assert.Empty(t, page.Users)
	assert.Equal(t, 0, page.Total)
	assert.NotNil(t, page.Users, "a list, not null")
	page, _ = telegram("")
	assert.Len(t, page.Users, 8, "the whole list")
	assert.Equal(t, ui.PageInfo{Total: 8, Page: 1, PerPage: 8, TotalPages: 1}, page.PageInfo)
	_, code = telegram("?page=0")
	assert.Equal(t, http.StatusBadRequest, code)

	resp, body := call(http.MethodGet, "/api/settings/telegram", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var settings ui.TelegramSettingsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &settings))
	assert.Len(t, settings.Users, 8, "the settings keep all the users")

	resp, body = call(http.MethodGet, "/api/prompts?page=1&per_page=2", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var prompts ui.PromptsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &prompts))
	assert.Len(t, prompts.Prompts, 2)
	assert.Equal(t, 4, prompts.Total, "the seeds")
	assert.Equal(t, 2, prompts.TotalPages)
	resp, body = call(http.MethodGet, "/api/prompts?q=smc", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &prompts))
	require.Len(t, prompts.Prompts, 1, "searched, the whole list")
	assert.Equal(t, "smc_ict", prompts.Prompts[0].Name)

	resp, body = call(http.MethodGet, "/api/users?page=1&per_page=10&q=admin", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var users ui.UsersResponse
	require.NoError(t, json.Unmarshal([]byte(body), &users))
	require.Len(t, users.Users, 1)
	assert.Equal(t, "admin", users.Users[0].Username)
	resp, body = call(http.MethodGet, "/api/users?page=1&q=administrador", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &users))
	assert.Len(t, users.Users, 1, "by the role")

	resp, body = call(http.MethodGet, "/api/roles?page=1&per_page=5&q=nada", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var roles ui.RolesResponse
	require.NoError(t, json.Unmarshal([]byte(body), &roles))
	assert.Empty(t, roles.Roles)
	assert.Equal(t, ui.PermissionAreas(), roles.Areas, "the catalog all the same")

	resp, body = call(http.MethodGet, "/api/settings/mt5-accounts?page=1&per_page=5&q=nada", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var accounts ui.MT5AccountsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &accounts))
	assert.Empty(t, accounts.Accounts)
	for _, path := range []string{"/api/prompts", "/api/users", "/api/roles", "/api/settings/mt5-accounts"} {
		resp, _ := call(http.MethodGet, path+"?page=1&per_page=x", nil)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, path)
	}
}

// The lists of the XTrader AI by pages: the analyses with the actions and the
// protections each asked for, the actions; the last ones without ?page=.
func TestPagedLists_TheSupervisor(t *testing.T) {
	s := newSupervised(t)
	call, _ := settingsSession(t, s.managed)
	ctx := context.Background()
	var runs []store.SupervisorRun
	for range 3 {
		run, err := s.store.StartSupervisorRun(ctx, store.RunManual, store.RunSkipped, "admin", "")
		require.NoError(t, err)
		runs = append(runs, run)
	}
	action, err := s.store.CreateSupervisorAction(ctx, store.SupervisorAction{Source: store.SourceSupervisor,
		RunID: &runs[0].ID, Action: store.ActionBlockEntries, Scope: json.RawMessage(`{"agents":["orb"]}`),
		Reason: "risco", Status: store.ActionSucceeded, RequestedBy: "supervisor"})
	require.NoError(t, err)

	resp, body := call(http.MethodGet, "/api/supervisor/runs?page=2&per_page=2", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var page ui.SupervisorRunsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &page))
	assert.Equal(t, ui.PageInfo{Total: 3, Page: 2, PerPage: 2, TotalPages: 2, HasPrev: true}, page.PageInfo)
	require.Len(t, page.Runs, 1)
	assert.Equal(t, runs[0].ID, page.Runs[0].ID, "the oldest on the last page")
	assert.Equal(t, []int64{action.ID}, page.Runs[0].ActionIDs, "with its actions")

	resp, body = call(http.MethodGet, "/api/supervisor/runs", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &page))
	assert.Len(t, page.Runs, 3, "the last ones")
	assert.Equal(t, 3, page.Total)

	resp, body = call(http.MethodGet, "/api/supervisor/actions?page=1&per_page=10", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var actions ui.SupervisorActionsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &actions))
	require.Len(t, actions.Actions, 1)
	assert.Equal(t, 1, actions.Total)

	resp, body = call(http.MethodGet, "/api/supervisor/protections?page=1", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var protections ui.SupervisorProtectionsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &protections))
	assert.Empty(t, protections.Protections)
	assert.NotNil(t, protections.Protections)
	assert.Equal(t, ui.PageInfo{Page: 1, PerPage: ui.DefPerPage}, protections.PageInfo)
	for _, path := range []string{"/api/supervisor/runs", "/api/supervisor/actions", "/api/supervisor/protections"} {
		resp, _ := call(http.MethodGet, path+"?page=1&per_page=x", nil)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, path)
	}
}

// The agents with daily limits of their own by pages, in the order of the
// guard; 400 for a page invalid.
func TestPagedLists_TheDailyAgents(t *testing.T) {
	s, _, _ := newDailySupervised(t)
	s.liveAgent(t, "orb", 202, true)
	s.liveAgent(t, "zeta", 303, true)
	call, _ := settingsSession(t, s.managed)
	for _, name := range []string{"orb", "trend", "zeta"} {
		resp, body := call(http.MethodPut, "/api/settings/agents/"+name+"/daily-limit", ui.DailyLimitsRequest{
			Settings: ui.DailyLimitSettings{Enabled: true, MaxLoss: ptrTo(500.0)}, Confirm: true})
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
	}
	require.NoError(t, s.sup.daily.evaluate(context.Background()))
	all := s.sup.daily.Status().Agents
	require.Len(t, all, 3)

	resp, body := call(http.MethodGet, "/api/settings/daily-limits/agents?page=2&per_page=2", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var page ui.DailyAgentsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &page))
	assert.Equal(t, ui.PageInfo{Total: 3, Page: 2, PerPage: 2, TotalPages: 2, HasPrev: true}, page.PageInfo)
	require.Len(t, page.Agents, 1)
	assert.Equal(t, all[2].Agent, page.Agents[0].Agent, "in the order of the guard")
	assert.Equal(t, "America/Belem", page.Zone)

	resp, body = call(http.MethodGet, "/api/settings/daily-limits/agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &page))
	assert.Len(t, page.Agents, 3, "all of them without a page")
	resp, _ = call(http.MethodGet, "/api/settings/daily-limits/agents?page=x", nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
