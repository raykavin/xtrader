package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/raykavin/gobox/pagination"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// paged returns the query of the page number of size rows, of search.
func paged(number, size int, search string) ListQuery {
	return ListQuery{Page: pagination.Page{Number: number, PerPage: size}, Search: search}
}

// A search compares the words without their case and their accents, as the
// database does with foldSQL.
func TestFold_TheWordsOfASearch(t *testing.T) {
	assert.Equal(t, "acao rapida", fold("AÇÃO Rápida"))
	assert.Equal(t, []string{"acao", "e", "ultimo"}, searchWords("  Ação  É\tÚLTIMO "))
	assert.Empty(t, searchWords("   "))
	assert.Equal(t, len([]rune(accented)), len([]rune(plain)), "each letter folded to one")
}

// The pages of a list: in its order, none overlapping, the last one for a page
// past it; all the rows without a page; the search on every row, before the
// pages, its words in any field, the case, the accents and the wildcards of
// LIKE taken as they are.
func TestList_PagesAndSearch(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := 1; i <= 12; i++ {
		name := fmt.Sprintf("Usuário %02d", i)
		if i == 5 {
			name = "Operação_5%"
		}
		_, err := s.AddTelegramUser(ctx, int64(1000+i), name)
		require.NoError(t, err)
	}

	ids := func(listed Listed[TelegramUser]) []int64 {
		list := []int64{}
		for _, u := range listed.Rows {
			list = append(list, u.TelegramID)
		}
		return list
	}

	all, err := s.FindTelegramUsers(ctx, ListQuery{})
	require.NoError(t, err)
	assert.Len(t, all.Rows, 12, "all of them without a page")
	assert.EqualValues(t, 12, all.Total)

	first, err := s.FindTelegramUsers(ctx, paged(1, 5, ""))
	require.NoError(t, err)
	assert.Equal(t, []int64{1001, 1002, 1003, 1004, 1005}, ids(first), "by their ID")
	assert.EqualValues(t, 12, first.Total)
	assert.Equal(t, pagination.Page{Number: 1, PerPage: 5}, first.Page)
	third, err := s.FindTelegramUsers(ctx, paged(3, 5, ""))
	require.NoError(t, err)
	assert.Equal(t, []int64{1011, 1012}, ids(third))
	past, err := s.FindTelegramUsers(ctx, paged(9, 5, ""))
	require.NoError(t, err)
	assert.Equal(t, []int64{1011, 1012}, ids(past), "the last page for one past it")
	assert.Equal(t, 3, past.Page.Number)
	big, err := s.FindTelegramUsers(ctx, paged(1, 500, ""))
	require.NoError(t, err)
	assert.Equal(t, ui.MaxPerPage, big.Page.PerPage, "the size bounded")

	found, err := s.FindTelegramUsers(ctx, paged(1, 5, "usuario"))
	require.NoError(t, err)
	assert.Equal(t, []int64{1001, 1002, 1003, 1004, 1006}, ids(found), "case and accents ignored")
	assert.EqualValues(t, 11, found.Total, "the total of the search")
	found, err = s.FindTelegramUsers(ctx, paged(3, 5, "USUÁRIO"))
	require.NoError(t, err)
	assert.Equal(t, []int64{1012}, ids(found), "the pages of the search")
	found, err = s.FindTelegramUsers(ctx, paged(1, 5, "usuario 1"))
	require.NoError(t, err)
	assert.EqualValues(t, 11, found.Total, "1 in every ID on Telegram")
	found, err = s.FindTelegramUsers(ctx, paged(1, 5, "1007"))
	require.NoError(t, err)
	assert.Equal(t, []int64{1007}, ids(found), "by the ID on Telegram")
	found, err = s.FindTelegramUsers(ctx, paged(1, 5, "07 usuario"))
	require.NoError(t, err)
	assert.Equal(t, []int64{1007}, ids(found), "every word, each in a field")
	found, err = s.FindTelegramUsers(ctx, paged(1, 5, "_5%"))
	require.NoError(t, err)
	assert.Equal(t, []int64{1005}, ids(found), "the wildcards taken as they are")
	found, err = s.FindTelegramUsers(ctx, paged(1, 5, "%"))
	require.NoError(t, err)
	assert.Equal(t, []int64{1005}, ids(found))

	none, err := s.FindTelegramUsers(ctx, paged(3, 5, "ninguém"))
	require.NoError(t, err)
	assert.Empty(t, none.Rows)
	assert.Zero(t, none.Total)
	assert.Equal(t, 1, none.Page.Number, "the first page, of none")
}

// The prompts searched by their names, descriptions and the agents using them.
func TestFindPrompts_Search(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	trend, err := s.PromptByName(ctx, "trend_following")
	require.NoError(t, err)
	_, err = s.CreatePrompt(ctx, "reversao", "Reversão à média no ouro", trend.Content, "tester")
	require.NoError(t, err)
	agent := llmAgent(t, s, "analista-btc")
	require.NoError(t, s.SetAgentPrompt(ctx, agent.ID, &trend.ID))

	names := func(q ListQuery) []string {
		t.Helper()
		listed, err := s.FindPrompts(ctx, q)
		require.NoError(t, err)
		list := []string{}
		for _, p := range listed.Rows {
			list = append(list, p.Name)
			assert.Empty(t, p.Content, "the list leaves the content out")
		}
		return list
	}
	assert.Equal(t, []string{"reversao"}, names(paged(1, 10, "REVERSAO")), "by the name")
	assert.Equal(t, []string{"reversao"}, names(paged(1, 10, "media ouro")), "by the description")
	assert.Equal(t, []string{"trend_following"}, names(paged(1, 10, "analista")), "by an agent using it")
	assert.Empty(t, names(paged(1, 10, "nada-assim")))

	page, err := s.FindPrompts(ctx, paged(1, 10, "analista"))
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	assert.Equal(t, []string{"analista-btc"}, page.Rows[0].UsedBy, "with the agents using it")
	all, err := s.Prompts(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 5, "the seeds and the one created")
}

// The users searched by their usernames and their roles; the roles by their
// names, descriptions and the areas of their permissions, the fixed one in
// every area.
func TestFindUsersAndRoles_Search(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	analyst, err := s.CreateRole(ctx, RoleChange{Name: ptr("Analista"), Description: ptr("Acompanha os backtests."),
		Permissions: &[]string{ui.PermBacktestsView}, By: "tester"})
	require.NoError(t, err)
	_, err = s.CreateRole(ctx, RoleChange{Name: ptr("Operador"), Description: ptr("Opera o painel."),
		Permissions: &[]string{ui.PermTradingOperate}, By: "tester"})
	require.NoError(t, err)
	admin, err := s.AdminRole(ctx)
	require.NoError(t, err)
	for name, role := range map[string]int64{"maria": analyst.ID, "joao": admin.ID, "pedro": 0} {
		user, err := s.CreateUser(ctx, name, "password123")
		require.NoError(t, err)
		if role != 0 {
			require.NoError(t, s.SetUserRole(ctx, user.ID, role, "tester"))
		}
	}

	users := func(search string) []string {
		t.Helper()
		listed, err := s.FindUsers(ctx, paged(1, 10, search))
		require.NoError(t, err)
		list := []string{}
		for _, u := range listed.Rows {
			list = append(list, u.Username)
		}
		return list
	}
	assert.Equal(t, []string{"joao"}, users("JOÃO"), "by the username, accents ignored")
	assert.Equal(t, []string{"maria"}, users("analista"), "by the role")
	assert.Equal(t, []string{"joao"}, users("administrador"))
	assert.Equal(t, []string{"joao", "maria", "pedro"}, users(""), "by the username")
	assert.Empty(t, users("ninguem"))
	listed, err := s.FindUsers(ctx, paged(1, 10, "maria"))
	require.NoError(t, err)
	require.NotNil(t, listed.Rows[0].RoleOf(), "with the role")
	assert.Equal(t, "Analista", listed.Rows[0].RoleOf().Name)

	roles := func(search string) []string {
		t.Helper()
		listed, err := s.FindRoles(ctx, paged(1, 10, search))
		require.NoError(t, err)
		list := []string{}
		for _, r := range listed.Rows {
			list = append(list, r.Name)
		}
		return list
	}
	assert.Equal(t, []string{"Administrador", "Analista", "Operador"}, roles(""), "the fixed one first, then by name")
	assert.Equal(t, []string{"Operador"}, roles("OPERADOR"), "by the name")
	assert.Equal(t, []string{"Analista"}, roles("acompanha"), "by the description")
	assert.Equal(t, []string{"Administrador", "Operador"}, roles("opera painel"), "an area of a word, the fixed one in all")
	assert.Equal(t, []string{"Administrador", "Analista"}, roles("backtesting"), "by an area, the fixed one in all")
	assert.Equal(t, []string{"Administrador", "Operador"}, roles("operação"))
	assert.Empty(t, roles("inexistente"))
	page, err := s.FindRoles(ctx, paged(1, 10, "analista"))
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	assert.EqualValues(t, 1, page.Rows[0].Users, "with its users")
	assert.Equal(t, []string{ui.PermBacktestsView}, page.Rows[0].Granted(), "and its permissions")
}

// The accounts of MT5 searched by their names, logins, servers, bridges,
// kinds, currencies, companies and the agents trading them.
func TestFindMT5Accounts_Search(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	by := MT5Change{By: "admin"}
	create := func(name string, login int64, server, bridge string) MT5Account {
		t.Helper()
		a, err := s.CreateMT5Account(ctx, MT5AccountInput{Name: &name, Login: &login, Server: &server,
			BridgeURL: &bridge}, false, "", by)
		require.NoError(t, err)
		return a
	}
	create("Conta Demo", 90807060, "Exness-MT5Trial9", "http://mt5:18812")
	real := create("Principal", 12345678, "Exness-MT5Real", "http://mt5-real:18812")
	trend, err := s.CreateAgent(ctx, Agent{Name: "matrend-xau", Kind: "trend", Mode: "live",
		APIAddress: "localhost:5570", Launched: true, Params: Params{"strategy": "matrend"},
		Account: &Account{BridgeURL: "http://127.0.0.1:18812"}}, AgentSecrets{})
	require.NoError(t, err)
	require.NoError(t, s.SetAgentMT5Account(ctx, trend.ID, &real.ID, by))

	accounts := func(search string) []string {
		t.Helper()
		listed, err := s.FindMT5Accounts(ctx, paged(1, 10, search))
		require.NoError(t, err)
		list := []string{}
		for _, a := range listed.Rows {
			list = append(list, a.Name)
		}
		return list
	}
	assert.Equal(t, []string{"Conta Demo", "Principal"}, accounts(""), "by name")
	assert.Equal(t, []string{"Conta Demo"}, accounts("9080"), "by the login")
	assert.Equal(t, []string{"Principal"}, accounts("mt5real"), "by the server")
	assert.Equal(t, []string{"Principal"}, accounts("mt5-real:"), "by the bridge")
	assert.Equal(t, []string{"Principal"}, accounts("MATREND"), "by an agent trading it")
	assert.Equal(t, []string{"Conta Demo", "Principal"}, accounts("exness"))
	assert.Empty(t, accounts("exness outra"), "every word")
}

// The analyses, the actions and the protections of the XTrader AI by pages,
// the newest first, with the actions and the arms of the analyses of a page.
func TestFindSupervisorLists_Pages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var runs []SupervisorRun
	for range 3 {
		run, err := s.StartSupervisorRun(ctx, RunManual, RunSkipped, "admin", "")
		require.NoError(t, err)
		runs = append(runs, run)
	}
	var actions []int64
	for i := range 4 {
		action, err := s.CreateSupervisorAction(ctx, SupervisorAction{Source: SourceSupervisor, RunID: &runs[i%2].ID,
			Action: ActionBlockEntries, Scope: json.RawMessage(`{"agents":["orb"]}`), Reason: fmt.Sprintf("r%d", i),
			Status: ActionSucceeded, RequestedBy: "supervisor"})
		require.NoError(t, err)
		actions = append(actions, action.ID)
	}
	agent := llmAgent(t, s, "llm")
	arm, err := s.ArmProtection(ctx, PositionProtection{AgentID: agent.ID, RunID: &runs[2].ID, Pair: "XAUUSDm",
		PositionRef: "mt5:1", Ticket: 1, Side: "BUY", Feature: ui.ProtectionTrailing, Reason: "proteger",
		Evidence: json.RawMessage(`[]`), RequestedBy: "supervisor"})
	require.NoError(t, err)

	page, err := s.FindSupervisorRuns(ctx, paged(1, 2, ""))
	require.NoError(t, err)
	require.Len(t, page.Rows, 2)
	assert.EqualValues(t, 3, page.Total)
	assert.Equal(t, runs[2].ID, page.Rows[0].ID, "the newest first")
	page, err = s.FindSupervisorRuns(ctx, paged(2, 2, ""))
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	assert.Equal(t, runs[0].ID, page.Rows[0].ID)

	byRun, err := s.ActionIDsOfRuns(ctx, []int64{runs[0].ID, runs[1].ID, runs[2].ID})
	require.NoError(t, err)
	assert.Equal(t, []int64{actions[0], actions[2]}, byRun[runs[0].ID], "the oldest first")
	assert.Equal(t, []int64{actions[1], actions[3]}, byRun[runs[1].ID])
	assert.Empty(t, byRun[runs[2].ID])
	armed, err := s.ProtectionIDsOfRuns(ctx, []int64{runs[2].ID})
	require.NoError(t, err)
	assert.Equal(t, []int64{arm.ID}, armed[runs[2].ID])
	none, err := s.ActionIDsOfRuns(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, none)

	listed, err := s.FindSupervisorActions(ctx, paged(2, 3, ""))
	require.NoError(t, err)
	assert.EqualValues(t, 4, listed.Total)
	require.Len(t, listed.Rows, 1)
	assert.Equal(t, actions[0], listed.Rows[0].ID, "the oldest on the last page")
	arms, err := s.FindProtectionArms(ctx, paged(1, 10, ""))
	require.NoError(t, err)
	require.Len(t, arms.Rows, 1)
	assert.Equal(t, arm.ID, arms.Rows[0].ID)
}
