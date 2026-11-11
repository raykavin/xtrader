package controller

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// roleOf returns a role of the tests granting perms.
func roleOf(id int64, name string, perms ...string) *store.Role {
	role := &store.Role{ID: id, Name: name}
	for _, p := range perms {
		role.Permissions = append(role.Permissions, store.RolePermission{RoleID: id, Permission: p})
	}
	return role
}

// A user without a role is refused at the login, its password right: no
// session; one with a role is told its permissions.
func TestAccess_LoginWithoutARole(t *testing.T) {
	users := newFakeUsers(testUsername, testPassword, "norole", "norole-pass")
	users.setRole(2, nil)
	_, srv := authServer(t, WithUsers(users))

	resp, body := login(t, srv, "norole", "norole-pass")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, body, errNoRole)
	assert.Nil(t, sessionOf(resp), "no session")
	assert.Nil(t, cookieOf(resp, csrfCookie))

	resp, _ = login(t, srv, "norole", "wrong")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a wrong password tells nothing more")

	resp, body = login(t, srv, testUsername, testPassword)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var session ui.SessionResponse
	require.NoError(t, json.Unmarshal([]byte(body), &session))
	require.NotNil(t, session.Role)
	assert.Equal(t, "Administrador", session.Role.Name)
	assert.Equal(t, ui.AllPermissions(), session.Permissions)
}

// The access of a session follows the role of its user: read again every
// sessionCheck (a change made out of the dashboard), at once when the
// dashboard changed it (refreshUsers); the session closed once the user has no
// role.
func TestAccess_FollowsTheRoleOfTheUser(t *testing.T) {
	users := newFakeUsers(testUsername, testPassword)
	users.setRole(1, roleOf(5, "Desempenho", ui.PermPerformanceView))
	d, srv := authServer(t, WithUsers(users))
	clock := newClock(d)
	resp, body := login(t, srv, testUsername, testPassword)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cookies := sessionOf(resp)
	sessionPerms := func() []string {
		_, body := do(t, http.MethodGet, srv.URL+"/api/auth/session", "", cookies)
		var session ui.SessionResponse
		require.NoError(t, json.Unmarshal([]byte(body), &session))
		return session.Permissions
	}
	assert.Contains(t, body, `"permissions":["performance:view"]`)

	resp, body = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, body, errForbidden)

	// changed out of the dashboard: taken at the next check
	users.setRole(1, roleOf(5, "Desempenho", ui.PermPerformanceView, ui.PermDashboardView))
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "until the next check")
	clock.advance(sessionCheck)
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{ui.PermDashboardView, ui.PermPerformanceView}, sessionPerms())

	// changed on the dashboard: at once
	users.setRole(1, roleOf(5, "Desempenho", ui.PermPerformanceView))
	d.auth.refreshUsers(1)
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "at once")

	// its role taken away: the session closed
	users.setRole(1, nil)
	d.auth.refreshUsers()
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp, _ = login(t, srv, testUsername, testPassword)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}
