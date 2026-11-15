//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// newMT5Supervised returns a supervisor of the tests whose accounts of MT5
// tell the agents running by its running map: stopped while held, see
// mt5Accounts.hold, as the manager stops them.
func newMT5Supervised(t *testing.T) *supervised {
	t.Helper()
	s := newSupervised(t)
	mt5 := s.dashboard.settings.mt5
	running := func(name string) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.running[name] && !mt5.isHeld(name)
	}
	mt5.stateOf = func(name string) AgentState {
		var state AgentState
		state.Running = running(name)
		return state
	}
	s.sup.stateOf = func(name string) AgentState {
		state := s.manager.State(name)
		state.Running = running(name)
		return state
	}
	return s
}

func listMT5Accounts(t *testing.T, call func(string, string, any) (*http.Response, string)) ui.MT5AccountsResponse {
	t.Helper()
	resp, body := call(http.MethodGet, "/api/settings/mt5-accounts", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var accounts ui.MT5AccountsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &accounts))
	return accounts
}

// switchPreview returns what activating (active) or deactivating the account
// of id does on its terminal.
func switchPreview(t *testing.T, call func(string, string, any) (*http.Response, string), id int64,
	active bool) ui.MT5SwitchPreview {
	t.Helper()
	resp, body := call(http.MethodGet, fmt.Sprintf("/api/settings/mt5-accounts/%d/switch?active=%t", id, active), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var preview ui.MT5SwitchPreview
	require.NoError(t, json.Unmarshal([]byte(body), &preview))
	return preview
}

// createMT5Account creates an account of the tests, inactive, on the bridge
// at url reached with token.
func createMT5Account(t *testing.T, call func(string, string, any) (*http.Response, string), name string,
	login int64, url, token string) ui.MT5AccountInfo {
	t.Helper()
	resp, body := call(http.MethodPost, "/api/settings/mt5-accounts", ui.MT5AccountRequest{Name: secret(name),
		Login: ptrTo(login), Server: secret("Exness-MT5Trial11"), BridgeURL: &url, BridgeToken: secret(token),
		Password: secret(name + "-pass")})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var account ui.MT5AccountInfo
	require.NoError(t, json.Unmarshal([]byte(body), &account))
	return account
}

// The accounts of the bridges imported at the start of the controller, once:
// the account of the terminal (its login, server, kind and currency, as the
// bridge tells them; the token its agents reach it with), active, its agents
// linked; its legacy session made the controller's; its secrets never
// answered nor logged; kept as the controller starts again, its changes never
// overwritten.
func TestMT5Accounts_ImportAndSettings(t *testing.T) {
	var logs syncBuffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	s := newMT5Supervised(t)
	ctx := context.Background()
	s.liveAgent(t, "trend", 101, false)
	s.liveAgent(t, "orb", 102, false)
	s.bridge.mu.Lock()
	s.bridge.tradeMode = 2 // a real account, served: allowed by the terminal
	s.bridge.mu.Unlock()
	call, _ := settingsSession(t, s.managed)

	s.dashboard.PrepareMT5Accounts(ctx)
	got := listMT5Accounts(t, call)
	require.Len(t, got.Accounts, 1)
	account := got.Accounts[0]
	assert.Equal(t, "Conta 10203040", account.Name)
	assert.Equal(t, int64(10203040), account.Login)
	assert.Equal(t, s.bridge.URL, account.BridgeURL)
	assert.True(t, account.Active)
	assert.True(t, account.AllowReal, "served real already")
	assert.Equal(t, "real", account.Kind)
	assert.Equal(t, "BRL", account.Currency)
	assert.True(t, account.TokenSet)
	assert.False(t, account.PasswordSet, "the password isn't on the bridge")
	assert.Equal(t, []string{"trend", "orb"}, account.Agents)
	require.NotNil(t, account.Session)
	assert.True(t, account.Session.Current)
	sessions := s.bridge.sessionsGiven()
	require.Len(t, sessions, 1, "the legacy session made the controller's")
	assert.Equal(t, true, sessions[0]["allow_real"])

	// the agents take the bridge and the token of their account
	trend, err := s.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	cfg, err := configOf(ctx, s.store, trend)
	require.NoError(t, err)
	assert.Equal(t, s.bridge.URL, cfg.Account.BridgeURL)
	assert.Equal(t, "bridge-token", cfg.Account.BridgeToken)

	// once: started again, nothing duplicated, a change kept
	resp, body := call(http.MethodPatch, fmt.Sprintf("/api/settings/mt5-accounts/%d", account.ID),
		ui.MT5AccountRequest{Name: secret("Real Exness"), Password: secret("pass-W0rd!"), Reason: "a senha"})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.NotContains(t, body, "pass-W0rd!", "never the password")
	s.dashboard.PrepareMT5Accounts(ctx)
	restarted := newMT5Accounts(s.store, s.manager, newOpenPositions(s.store, s.manager))
	accounts, err := s.store.MT5Accounts(ctx)
	require.NoError(t, err)
	require.Len(t, accounts, 1, "imported once")
	assert.Equal(t, "Real Exness", accounts[0].Name, "never overwritten")
	assert.True(t, restarted.infoOf(accounts[0], nil).PasswordSet)
	password, token, err := s.store.MT5AccountSecrets(accounts[0])
	require.NoError(t, err)
	assert.Equal(t, [2]string{"pass-W0rd!", "bridge-token"}, [2]string{password, token})

	resp, body = call(http.MethodGet, "/api/settings/mt5-accounts", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, body, "pass-W0rd!")
	assert.NotContains(t, body, "bridge-token")
	resp, body = call(http.MethodGet, fmt.Sprintf("/api/settings/mt5-accounts/%d/events", account.ID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, `"kind":"imported"`)
	assert.Contains(t, body, `"kind":"linked"`)
	assert.Contains(t, body, `"password_set":true`)
	assert.NotContains(t, body, "pass-W0rd!", "never in the record")
	assert.NotContains(t, logs.String(), "pass-W0rd!", "never logged")
	assert.Contains(t, logs.String(), "its password replaced")

	// refused out of their rules, and without the required fields
	for _, refused := range []ui.MT5AccountRequest{
		{Name: secret("x"), Login: ptrTo(int64(0)), Server: secret("s"), BridgeURL: secret("http://mt5:18812")},
		{Name: secret("x"), Server: secret("s"), BridgeURL: secret("http://mt5:18812")},
		{Name: secret("x"), Login: ptrTo(int64(7)), Server: secret("s"), BridgeURL: secret("mt5:18812")},
	} {
		resp, body := call(http.MethodPost, "/api/settings/mt5-accounts", refused)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
	}
}

// The terminal of the account active restarted, logged in none and asking for
// its password, is given it again under its agents running: logging in the
// same account again isn't switching it; without the password, told so.
func TestMT5Accounts_PasswordAgainUnderAgents(t *testing.T) {
	s := newMT5Supervised(t)
	ctx := context.Background()
	s.liveAgent(t, "trend", 101, true)
	call, _ := settingsSession(t, s.managed)
	s.dashboard.PrepareMT5Accounts(ctx)
	given := len(s.bridge.sessionsGiven())
	restarted := func() {
		s.bridge.mu.Lock()
		s.bridge.legacy, s.bridge.waiting = false, "password"
		s.bridge.mu.Unlock()
	}

	// without the password: nothing given, told so
	restarted()
	s.dashboard.settings.mt5.reconcile(ctx)
	assert.Len(t, s.bridge.sessionsGiven(), given)
	account := listMT5Accounts(t, call).Accounts[0]
	require.NotNil(t, account.Session)
	assert.Contains(t, account.Session.Error, "senha")

	resp, body := call(http.MethodPatch, fmt.Sprintf("/api/settings/mt5-accounts/%d", account.ID),
		ui.MT5AccountRequest{Password: secret("pass-W0rd!")})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	s.dashboard.settings.mt5.reconcile(ctx)
	sessions := s.bridge.sessionsGiven()
	require.Len(t, sessions, given+1, "given again under the agent running")
	assert.Equal(t, float64(account.Login), sessions[given]["login"])
	assert.Equal(t, "pass-W0rd!", sessions[given]["password"])
	account = listMT5Accounts(t, call).Accounts[0]
	assert.True(t, account.Session.Current)

	// another account on the terminal: never switched under the agent
	s.bridge.mu.Lock()
	s.bridge.sessionLogin, s.bridge.waiting = 5, "password"
	s.bridge.mu.Unlock()
	s.dashboard.settings.mt5.reconcile(ctx)
	assert.Len(t, s.bridge.sessionsGiven(), given+1)
}

// The account of a terminal in use is never switched under its agents: the
// change of its login, its deactivation, or another account activated on its
// terminal refused while an agent runs on it, or holds positions or pending
// orders there; then the terminal logged in the new account; an agent
// linked to another account only stopped and flat, its bridge never set by
// hand once linked; an agent of an account inactive not launched.
func TestMT5Accounts_InUse(t *testing.T) {
	s := newMT5Supervised(t)
	ctx := context.Background()
	s.liveAgent(t, "trend", 101, true)
	call, _ := settingsSession(t, s.managed)
	s.dashboard.PrepareMT5Accounts(ctx)
	accounts := listMT5Accounts(t, call).Accounts
	require.Len(t, accounts, 1)
	current := accounts[0]

	resp, body := call(http.MethodPost, "/api/settings/mt5-accounts", ui.MT5AccountRequest{Name: secret("Demo"),
		Login: ptrTo(int64(87654321)), Server: secret("Exness-MT5Trial11"), BridgeURL: &s.bridge.URL,
		BridgeToken: secret("bridge-token")})
	require.Equal(t, http.StatusCreated, resp.StatusCode, body)
	var demo ui.MT5AccountInfo
	require.NoError(t, json.Unmarshal([]byte(body), &demo))
	assert.False(t, demo.Active, "created inactive")
	activate := fmt.Sprintf("/api/settings/mt5-accounts/%d/activate", demo.ID)

	resp, body = call(http.MethodPost, activate, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
	assert.Contains(t, body, "senha", "the terminal needs its password")
	resp, body = call(http.MethodPatch, fmt.Sprintf("/api/settings/mt5-accounts/%d", demo.ID),
		ui.MT5AccountRequest{Password: secret("demo-pass")})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)

	// an agent running on the terminal: told, stopped by the switch
	preview := switchPreview(t, call, demo.ID, true)
	assert.Equal(t, []string{current.Name}, preview.Current)
	assert.Equal(t, []string{"trend"}, preview.Running)
	assert.Empty(t, preview.Exposure)
	resp, body = call(http.MethodPatch, fmt.Sprintf("/api/settings/mt5-accounts/%d", current.ID),
		ui.MT5AccountRequest{Login: ptrTo(int64(5))})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)
	resp, body = call(http.MethodPatch, fmt.Sprintf("/api/settings/mt5-accounts/%d", current.ID),
		ui.MT5AccountRequest{Active: boolPtr(false)})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)

	// a position and an order of it open: asked first, nothing stopped
	s.bridge.set(protected(1, 101), position(9, 0, "EURUSDm", false, 0.1, 5, 0))
	s.bridge.mu.Lock()
	s.bridge.orders = []bridgeOrder{{Ticket: 31, Type: 2, Magic: 101, Symbol: "XAUUSDm", PriceOpen: 4100,
		VolumeCurrent: 0.01}}
	s.bridge.mu.Unlock()
	preview = switchPreview(t, call, demo.ID, true)
	assert.Len(t, preview.Exposure, 2)
	resp, body = call(http.MethodPost, activate, nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)
	var refusal ui.MT5SwitchRefusal
	require.NoError(t, json.Unmarshal([]byte(body), &refusal))
	assert.True(t, refusal.Confirm)
	assert.Contains(t, refusal.Error, "Existem posições abertas ou ordens pendentes na conta "+current.Name)
	assert.Contains(t, strings.Join(refusal.Pending, " "), "#1")
	assert.Contains(t, strings.Join(refusal.Pending, " "), "#31")
	assert.NotContains(t, body, "#9", "a position by hand isn't the agents'")
	assert.True(t, s.dashboard.settings.mt5.isRunning("trend"), "nothing stopped")
	assert.Empty(t, s.bridge.tradeRequests(), "nothing closed")

	// confirmed: the agent stopped, its position closed and its order canceled,
	// then the terminal logged in the account activated
	resp, body = call(http.MethodPost, activate, ui.MT5SwitchRequest{ClosePositions: true})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &demo))
	assert.True(t, demo.Active)
	assert.Equal(t, []bridgePosition{position(9, 0, "EURUSDm", false, 0.1, 5, 0)}, s.bridge.held(),
		"the one by hand left")
	s.bridge.mu.Lock()
	assert.Empty(t, s.bridge.orders, "its order canceled")
	s.bridge.mu.Unlock()
	sessions := s.bridge.sessionsGiven()
	last := sessions[len(sessions)-1]
	assert.Equal(t, float64(87654321), last["login"])
	assert.Equal(t, "demo-pass", last["password"], "given to the terminal")
	former, err := s.store.MT5Account(ctx, current.ID)
	require.NoError(t, err)
	assert.False(t, former.Active, "the former one deactivated")
	actions, err := s.store.SupervisorActions(ctx, 5)
	require.NoError(t, err)
	require.NotEmpty(t, actions)
	assert.Equal(t, store.ActionClosePositions, actions[0].Action, "recorded")
	assert.Contains(t, actions[0].Reason, "troca da conta MT5")

	// the agent of the former account kept stopped, told why
	trend, err := s.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	assert.False(t, s.dashboard.settings.mt5.isHeld("trend"), "released")
	assert.Contains(t, s.dashboard.mt5Hold(trend), "está inativa")
	err = s.manager.start(trend)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "não é a ativa do terminal")

	// linked to the demo: refused while running, its bridge never by hand
	s.mu.Lock()
	s.running["trend"] = true
	s.mu.Unlock()
	agentPath := "/api/settings/agents/trend"
	resp, body = call(http.MethodPatch, agentPath, ui.AgentSettingsRequest{MT5AccountID: &demo.ID})
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)
	assert.Contains(t, body, "Pare o agente antes de trocar a conta MT5 dele.")
	s.mu.Lock()
	s.running["trend"] = false
	s.mu.Unlock()
	resp, body = call(http.MethodPatch, agentPath, ui.AgentSettingsRequest{MT5AccountID: &demo.ID})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var settings ui.AgentSettings
	require.NoError(t, json.Unmarshal([]byte(body), &settings))
	assert.Equal(t, demo.ID, settings.MT5AccountID)
	resp, body = call(http.MethodPatch, agentPath, ui.AgentSettingsRequest{
		Account: &ui.AgentAccountRequest{BridgeURL: secret("http://other:18812")}})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
	assert.Contains(t, body, "Contas MT5")

	// the test of the account active: as the bridge confirms it, no credential
	resp, body = call(http.MethodPost, fmt.Sprintf("/api/settings/mt5-accounts/%d/test", demo.ID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var test ui.MT5AccountTest
	require.NoError(t, json.Unmarshal([]byte(body), &test))
	assert.True(t, test.Reachable && test.TokenOK && test.Current, body)
	assert.Equal(t, "demo", test.Kind)
	assert.Equal(t, "BRL", test.Currency)
	assert.False(t, strings.Contains(body, "demo-pass") || strings.Contains(body, "bridge-token"))

	// the former, inactive: its test never switches the terminal
	given := len(s.bridge.sessionsGiven())
	resp, body = call(http.MethodPost, fmt.Sprintf("/api/settings/mt5-accounts/%d/test", current.ID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &test))
	assert.False(t, test.Current)
	assert.Contains(t, test.Detail, "exigiria trocar")
	assert.Len(t, s.bridge.sessionsGiven(), given, "nothing given to the terminal")

	// deleted: only inactive and without agents
	resp, _ = call(http.MethodDelete, fmt.Sprintf("/api/settings/mt5-accounts/%d", demo.ID), nil)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	resp, _ = call(http.MethodDelete, fmt.Sprintf("/api/settings/mt5-accounts/%d", current.ID), nil)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	_ = store.MT5KindDemo
}

// A switch of the account of a terminal refused leaves the accounts as they
// were: what stays open after the closes told, the agents of the account
// started again to watch it; the account it left active again when the
// terminal can't log in the new one. The accounts and the agents of another
// terminal are never touched, on the same host too; two URLs of one terminal
// are told the same by its instance, the agents of the account left moved to
// the one activated when asked (the ones of its inactive accounts, with none
// active on it); a deactivation stops the agents too.
func TestMT5Accounts_SwitchCoordinated(t *testing.T) {
	s := newMT5Supervised(t)
	ctx := context.Background()
	s.liveAgent(t, "trend", 101, true)
	s.bridge.mu.Lock()
	s.bridge.instance = "terminal-1"
	s.bridge.mu.Unlock()
	call, _ := settingsSession(t, s.managed)
	s.dashboard.PrepareMT5Accounts(ctx)
	current := listMT5Accounts(t, call).Accounts[0]
	demo := createMT5Account(t, call, "Demo", 87654321, s.bridge.URL, "bridge-token")
	activate := func(id int64) (*http.Response, string) {
		return call(http.MethodPost, fmt.Sprintf("/api/settings/mt5-accounts/%d/activate", id),
			ui.MT5SwitchRequest{ClosePositions: true})
	}
	active := func(id int64) bool {
		account, err := s.store.MT5Account(ctx, id)
		require.NoError(t, err)
		return account.Active
	}

	// another terminal: its account active, its agent running
	other := newFakeBridge(t, "other-token", 55555555, "USD")
	other.mu.Lock()
	other.instance, other.legacy, other.sessionLogin = "terminal-2", false, 0
	other.mu.Unlock()
	second := createMT5Account(t, call, "Segunda", 55555555, other.URL, "other-token")
	resp, body := activate(second.ID)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	orb := s.agent(t, "orb", "trend", true, onBridge(other.URL, 202, "XAUUSDm"))
	require.NoError(t, s.store.SetAgentMT5Account(ctx, orb.ID, &second.ID, store.MT5Change{By: "test"}))
	s.mu.Lock()
	s.running["orb"] = true
	s.mu.Unlock()
	preview := switchPreview(t, call, demo.ID, true)
	assert.Equal(t, []string{"trend"}, preview.Running, "the agents of its terminal only")

	// a close refused by the terminal: told, nothing switched
	s.bridge.set(protected(1, 101))
	s.bridge.mu.Lock()
	s.bridge.tradeAnswer = func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{"retcode": 10018, "comment": "Market closed"}
	}
	s.bridge.mu.Unlock()
	resp, body = activate(demo.ID)
	assert.Equal(t, http.StatusConflict, resp.StatusCode, body)
	var refusal ui.MT5SwitchRefusal
	require.NoError(t, json.Unmarshal([]byte(body), &refusal))
	assert.Contains(t, refusal.Error, "nem tudo foi encerrado")
	assert.Contains(t, strings.Join(refusal.Pending, " "), "#1")
	assert.True(t, active(current.ID), "nothing switched")
	assert.False(t, active(demo.ID))
	assert.False(t, s.dashboard.settings.mt5.isHeld("trend"), "started again to watch it")
	assert.True(t, active(second.ID), "the other terminal untouched")
	assert.Len(t, other.sessionsGiven(), 1, "nothing given to the other terminal")

	// the terminal refusing the new account: the account it left active again
	s.bridge.mu.Lock()
	s.bridge.tradeAnswer = nil
	s.bridge.sessionStatus, s.bridge.sessionOnce = http.StatusBadGateway, true
	s.bridge.mu.Unlock()
	resp, body = activate(demo.ID)
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode, body)
	assert.Contains(t, body, "voltou a ser a ativa")
	assert.True(t, active(current.ID))
	assert.False(t, active(demo.ID))
	assert.Empty(t, s.bridge.held(), "its position closed, confirmed")
	sessions := s.bridge.sessionsGiven()
	assert.Equal(t, float64(current.Login), sessions[len(sessions)-1]["login"], "logged in it again")

	// another URL of the same terminal, told by its instance: the account
	// active on it left, the other terminal untouched
	alias := newFakeBridge(t, "alias-token", 77777777, "BRL")
	alias.mu.Lock()
	alias.instance = "terminal-1"
	alias.mu.Unlock()
	third := createMT5Account(t, call, "Outra URL", 77777777, alias.URL, "alias-token")
	preview = switchPreview(t, call, third.ID, true)
	assert.Equal(t, []string{current.Name}, preview.Current)
	assert.Equal(t, []string{"trend"}, preview.Linked)
	assert.Equal(t, []string{"trend"}, preview.Running)
	resp, body = call(http.MethodPost, fmt.Sprintf("/api/settings/mt5-accounts/%d/activate", third.ID),
		ui.MT5SwitchRequest{ClosePositions: true, MoveAgents: true})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.True(t, active(third.ID))
	assert.False(t, active(current.ID), "left by the terminal")
	assert.True(t, active(second.ID))
	trend, err := s.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	require.NotNil(t, trend.MT5AccountOf())
	assert.Equal(t, third.ID, trend.MT5AccountOf().ID, "moved with the switch")
	assert.Equal(t, alias.URL, trend.Account.BridgeURL)
	assert.Empty(t, s.dashboard.mt5Hold(trend), "started on it")
	orb, err = s.store.AgentByName(ctx, "orb")
	require.NoError(t, err)
	assert.Equal(t, second.ID, orb.MT5AccountOf().ID, "the agent of another terminal never moved")

	// deactivated: its agents stopped, nothing left active on the terminal
	resp, body = call(http.MethodPost, fmt.Sprintf("/api/settings/mt5-accounts/%d/deactivate", second.ID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.False(t, active(second.ID))
	orb, err = s.store.AgentByName(ctx, "orb")
	require.NoError(t, err)
	assert.Contains(t, s.dashboard.mt5Hold(orb), "está inativa", "a deactivation moves none")

	// no account active on the terminal (as a switch failed leaves it): the
	// agents of its inactive accounts follow the account activated, the ones
	// of another terminal never
	resp, body = call(http.MethodPost, fmt.Sprintf("/api/settings/mt5-accounts/%d/deactivate", third.ID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	preview = switchPreview(t, call, demo.ID, true)
	assert.Empty(t, preview.Current)
	assert.Equal(t, []string{third.Name}, preview.From)
	assert.Equal(t, []string{"trend"}, preview.Linked)
	resp, body = call(http.MethodPost, fmt.Sprintf("/api/settings/mt5-accounts/%d/activate", demo.ID),
		ui.MT5SwitchRequest{MoveAgents: true})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.True(t, active(demo.ID))
	trend, err = s.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	assert.Equal(t, demo.ID, trend.MT5AccountOf().ID, "moved from the inactive account of the terminal")
	assert.Empty(t, s.dashboard.mt5Hold(trend), "started on it")
	orb, err = s.store.AgentByName(ctx, "orb")
	require.NoError(t, err)
	assert.Equal(t, second.ID, orb.MT5AccountOf().ID, "the agent of another terminal never moved")
}
