package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

const (
	testUsername = "op-7c1f"
	testPassword = "s3cret-Pa55"
)

// fakeUsers are the users of the tests, their passwords in plain text.
type fakeUsers struct {
	mu        sync.Mutex
	users     []store.User
	passwords map[int64]string
}

// newFakeUsers returns the users of the pairs of username and password.
func newFakeUsers(credentials ...string) *fakeUsers {
	f := &fakeUsers{passwords: make(map[int64]string)}
	for i := 0; i+1 < len(credentials); i += 2 {
		id := int64(len(f.users) + 1)
		f.users = append(f.users, store.User{Model: store.Model{ID: id}, Username: credentials[i], Enabled: true})
		f.passwords[id] = credentials[i+1]
	}
	return f
}

func (f *fakeUsers) Authenticate(_ context.Context, username, password string) (store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if strings.EqualFold(u.Username, strings.TrimSpace(username)) && u.Enabled && f.passwords[u.ID] == password {
			return u, nil
		}
	}
	return store.User{}, store.ErrCredentials
}

func (f *fakeUsers) User(_ context.Context, id int64) (store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return store.User{}, fmt.Errorf("%w: user %d", store.ErrNotFound, id)
}

func (f *fakeUsers) CountUsers(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.users)), nil
}

// disable disables the user of id.
func (f *fakeUsers) disable(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.users {
		if f.users[i].ID == id {
			f.users[i].Enabled = false
		}
	}
}

// authServer serves the dashboard of an agent online behind its login, with
// the options given, as Handler does, the failed logins not delayed.
func authServer(t *testing.T, options ...DashboardOption) (*Dashboard, *httptest.Server) {
	t.Helper()
	_, bot := newAgent(t, nil)
	d, err := NewDashboard([]Agent{{Name: "bot", Address: bot.URL}}, options...)
	require.NoError(t, err)
	d.auth.failDelay = 0

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('ok')"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.css"), []byte("body{}"), 0o644))
	srv := httptest.NewServer(d.auth.protect(d.router(bundle{dir: dir, version: "v1.0.0"}, nil)))
	t.Cleanup(srv.Close)
	return d, srv
}

// do sends a request, with the cookies set, without following the
// redirects.
func do(t *testing.T, method, url, body string, cookies []*http.Cookie, headers ...string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	require.NoError(t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	for _, cookie := range cookies {
		if cookie != nil {
			req.AddCookie(cookie)
		}
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(data)
}

// cookieOf returns the cookie of name a response sets, nil for none.
func cookieOf(resp *http.Response, name string) *http.Cookie {
	for _, cookie := range resp.Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

// sessionOf returns the cookie of the session a response sets, as the
// cookies of the next requests, nil for none.
func sessionOf(resp *http.Response) []*http.Cookie {
	if session := cookieOf(resp, sessionCookie); session != nil {
		return []*http.Cookie{session}
	}
	return nil
}

func login(t *testing.T, srv *httptest.Server, username, password string, headers ...string) (*http.Response, string) {
	t.Helper()
	body, err := json.Marshal(ui.LoginRequest{Username: username, Password: password})
	require.NoError(t, err)
	return do(t, http.MethodPost, srv.URL+"/api/auth/login", string(body), nil, headers...)
}

// Without a session, the pages lead to the login and the API is refused; the
// files of the bundle and the login page are served.
func TestAuth_BlocksWithoutSession(t *testing.T) {
	_, srv := authServer(t, WithUsers(newFakeUsers(testUsername, testPassword)))

	resp, _ := do(t, http.MethodGet, srv.URL+"/", "", nil)
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/login?next=%2F", resp.Header.Get("Location"))
	resp, _ = do(t, http.MethodGet, srv.URL+"/?agent=llm&pair=XAUUSDm", "", nil)
	assert.Equal(t, "/login?next=%2F%3Fagent%3Dllm%26pair%3DXAUUSDm", resp.Header.Get("Location"), "back there")
	resp, _ = do(t, http.MethodGet, srv.URL+"/settings", "", nil)
	assert.Equal(t, "/login?next=%2Fsettings", resp.Header.Get("Location"))

	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/agents", ""},
		{http.MethodGet, "/api/agents/bot/pairs", ""},
		{http.MethodGet, "/api/agents/bot/events", ""},
		{http.MethodPost, "/api/agents/bot/controls/reanalyze", `{"pair":"BTCUSDm"}`},
		{http.MethodGet, "/api/prompts", ""},
		{http.MethodPost, "/", ""},
	} {
		resp, body := do(t, request.method, srv.URL+request.path, request.body, nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, request.path)
		assert.Equal(t, "authentication required", decodeError(t, body), request.path)
	}

	resp, body := do(t, http.MethodGet, srv.URL+"/ui/v1.0.0/app.js", "", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the bundle is public")
	assert.Equal(t, "console.log('ok')", body)
	resp, body = do(t, http.MethodGet, srv.URL+"/login", "", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, "/ui/v1.0.0/app.js", "the app shows its login")
	_, body = do(t, http.MethodGet, srv.URL+"/api/auth/session", "", nil)
	assert.JSONEq(t, `{"authenticated":false}`, body)
}

// The credentials open a session, in a cookie the browser keeps and doesn't
// hand to the scripts, with a CSRF token the scripts read; the logout, with
// the token, closes it.
func TestAuth_LoginAndLogout(t *testing.T) {
	d, srv := authServer(t, WithUsers(newFakeUsers(testUsername, testPassword)))

	resp, body := login(t, srv, strings.ToUpper(testUsername), testPassword)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"authenticated":true,"username":"`+testUsername+`"}`, body)
	session := cookieOf(resp, sessionCookie)
	require.NotNil(t, session)
	assert.True(t, session.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, session.SameSite)
	assert.Equal(t, "/", session.Path)
	assert.False(t, session.Secure, "over HTTP")
	assert.Equal(t, int(sessionTTL.Seconds()), session.MaxAge)
	assert.GreaterOrEqual(t, len(session.Value), 43, "32 random bytes")
	assert.NotContains(t, d.auth.sessions, session.Value, "kept by its hash only")
	csrf := cookieOf(resp, csrfCookie)
	require.NotNil(t, csrf)
	assert.False(t, csrf.HttpOnly, "read by the web app")
	assert.Equal(t, http.SameSiteStrictMode, csrf.SameSite)
	assert.NotEqual(t, session.Value, csrf.Value)
	cookies := []*http.Cookie{session}

	resp, body = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, `"name":"bot"`)
	resp, body = do(t, http.MethodGet, srv.URL+"/api/agents/bot/pairs", "", cookies)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the proxy of the agent")
	assert.JSONEq(t, `{"pairs":[]}`, body)
	resp, body = do(t, http.MethodGet, srv.URL+"/", "", cookies)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, "/ui/v1.0.0/app.js")
	resp, _ = do(t, http.MethodGet, srv.URL+"/settings", "", cookies)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the page of the settings")
	_, body = do(t, http.MethodGet, srv.URL+"/api/auth/session", "", cookies)
	assert.JSONEq(t, `{"authenticated":true,"username":"`+testUsername+`"}`, body)
	resp, _ = do(t, http.MethodGet, srv.URL+"/login?next=%2F%3Fagent%3Dbot", "", cookies)
	assert.Equal(t, http.StatusFound, resp.StatusCode, "logged in already")
	assert.Equal(t, "/?agent=bot", resp.Header.Get("Location"))

	resp, _ = do(t, http.MethodPost, srv.URL+"/api/auth/logout", "", cookies)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "the logout needs the CSRF token too")
	resp, body = do(t, http.MethodPost, srv.URL+"/api/auth/logout", "", cookies, csrfHeader, csrf.Value)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"authenticated":false}`, body)
	require.NotNil(t, cookieOf(resp, sessionCookie))
	assert.Less(t, cookieOf(resp, sessionCookie).MaxAge, 0, "the cookie cleared")
	assert.Less(t, cookieOf(resp, csrfCookie).MaxAge, 0)
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the session closed, its cookie kept or not")
	resp, _ = do(t, http.MethodGet, srv.URL+"/", "", cookies)
	assert.Equal(t, http.StatusFound, resp.StatusCode)
}

// A request changing something needs the CSRF token of its session, which a
// page of another site can't read: the reads don't.
func TestAuth_CSRF(t *testing.T) {
	_, srv := authServer(t, WithUsers(newFakeUsers(testUsername, testPassword)))
	resp, _ := login(t, srv, testUsername, testPassword)
	cookies, csrf := sessionOf(resp), cookieOf(resp, csrfCookie)
	require.NotNil(t, csrf)

	for _, token := range []string{"", "forged", csrf.Value + "x"} {
		resp, body := do(t, http.MethodPost, srv.URL+"/api/agents/bot/controls/start", "", cookies, csrfHeader, token)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, token)
		assert.Contains(t, decodeError(t, body), "CSRF", token)
	}
	resp, body := do(t, http.MethodPost, srv.URL+"/api/agents/bot/controls/start", "", cookies, csrfHeader, csrf.Value)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "through to the agent, whose controls are off")
	assert.Equal(t, "bot controls are not enabled", decodeError(t, body))
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents/bot/controls", "", cookies)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "a read needs no token")

	// the token of another session doesn't do
	resp, _ = login(t, srv, testUsername, testPassword)
	other := cookieOf(resp, csrfCookie)
	resp, _ = do(t, http.MethodPost, srv.URL+"/api/agents/bot/controls/start", "", cookies, csrfHeader, other.Value)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// Wrong credentials open no session, the answer telling neither which one is
// wrong; the failed logins are delayed.
func TestAuth_InvalidCredentials(t *testing.T) {
	d, srv := authServer(t, WithUsers(newFakeUsers(testUsername, testPassword)))
	for _, credentials := range [][2]string{
		{testUsername, "wrong"},
		{"someone", testPassword},
		{"", ""},
		{testUsername, testPassword + " "},
	} {
		resp, body := login(t, srv, credentials[0], credentials[1])
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, credentials)
		assert.Equal(t, "invalid username or password", decodeError(t, body), credentials)
		assert.Nil(t, sessionOf(resp), credentials)
	}
	assert.Empty(t, d.auth.sessions)

	resp, _ := do(t, http.MethodPost, srv.URL+"/api/auth/login", `{`, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, _ = do(t, http.MethodPost, srv.URL+"/api/auth/login", `{"username":"`+strings.Repeat("x", maxLoginBody)+`"}`,
		nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "too large")
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", []*http.Cookie{{Name: sessionCookie, Value: "forged"}})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a session never opened")

	d.auth.failDelay = 50 * time.Millisecond
	start := time.Now()
	login(t, srv, testUsername, "wrong")
	assert.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond)
}

// A session lasts sessionTTL, and its cookies are only sent over HTTPS when
// it came over HTTPS, eg: through ngrok.
func TestAuth_SessionExpiresAndSecureCookie(t *testing.T) {
	d, srv := authServer(t, WithUsers(newFakeUsers(testUsername, testPassword)))
	resp, _ := login(t, srv, testUsername, testPassword, "X-Forwarded-Proto", "https")
	cookies := sessionOf(resp)
	require.NotNil(t, cookies)
	assert.True(t, cookies[0].Secure)
	assert.True(t, cookieOf(resp, csrfCookie).Secure)

	now := time.Now()
	d.auth.now = func() time.Time { return now.Add(sessionTTL - time.Minute) }
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	d.auth.now = func() time.Time { return now.Add(sessionTTL + time.Minute) }
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "expired")

	// the expired ones dropped at the next login
	d.auth.now = time.Now
	login(t, srv, testUsername, testPassword)
	d.auth.now = func() time.Time { return now.Add(sessionTTL + time.Minute) }
	login(t, srv, testUsername, testPassword)
	assert.Len(t, d.auth.sessions, 1)
}

// A session ends with its user: disabled or deleted elsewhere, found within
// sessionCheck, or closed by the dashboard at once.
func TestAuth_SessionEndsWithItsUser(t *testing.T) {
	users := newFakeUsers(testUsername, testPassword, "other", "other-password")
	d, srv := authServer(t, WithUsers(users))
	resp, _ := login(t, srv, testUsername, testPassword)
	cookies := sessionOf(resp)
	resp, _ = login(t, srv, "other", "other-password")
	other := sessionOf(resp)

	users.disable(1)
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "checked again after sessionCheck")
	now := time.Now()
	d.auth.now = func() time.Time { return now.Add(sessionCheck + time.Second) }
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "disabled")
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", other)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the other user goes on")

	d.auth.closeUser(2)
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", other)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "its sessions closed, eg: a new password")
}

// Without users the dashboard is closed: nobody logs in, the API tells why,
// and the pages lead to the login, which shows it.
func TestAuth_WithoutUsers(t *testing.T) {
	for _, options := range [][]DashboardOption{nil, {WithUsers(newFakeUsers())}} {
		_, srv := authServer(t, options...)

		resp, body := login(t, srv, testUsername, testPassword)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		assert.Equal(t, errNoUsers, decodeError(t, body))
		assert.Nil(t, sessionOf(resp))

		resp, body = do(t, http.MethodGet, srv.URL+"/api/agents", "", nil)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		assert.Equal(t, errNoUsers, decodeError(t, body))
		resp, _ = do(t, http.MethodGet, srv.URL+"/api/auth/session", "", nil)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		resp, _ = do(t, http.MethodGet, srv.URL+"/", "", nil)
		assert.Equal(t, http.StatusFound, resp.StatusCode)
	}
}

// The passwords are never written to the logs, nor to the answers.
func TestAuth_PasswordsNotExposed(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	_, srv := authServer(t, WithUsers(newFakeUsers(testUsername, testPassword)))
	var answers []string
	for _, password := range []string{"wrong", testPassword} {
		resp, body := login(t, srv, testUsername, password, "X-Forwarded-For", "203.0.113.7, 10.0.0.1")
		answers = append(answers, body)
		cookies := sessionOf(resp)
		for _, path := range []string{"/api/auth/session", "/api/agents", "/login", "/"} {
			_, body := do(t, http.MethodGet, srv.URL+path, "", cookies)
			answers = append(answers, body)
		}
	}
	for _, text := range append(answers, logs.String()) {
		assert.NotContains(t, text, testPassword)
		assert.NotContains(t, text, "wrong")
	}
	assert.Contains(t, logs.String(), "failed login from 203.0.113.7")
	assert.Contains(t, logs.String(), "login of "+testUsername+" from 203.0.113.7")
}

func TestSafeNext(t *testing.T) {
	for next, want := range map[string]string{
		"":                         "/",
		"/":                        "/",
		"/?agent=llm&pair=XAUUSDm": "/?agent=llm&pair=XAUUSDm",
		"//evil.example":           "/",
		"/\\evil.example":          "/",
		"https://evil.example":     "/",
		"/login":                   "/",
		"/login?next=/":            "/",
	} {
		assert.Equal(t, want, safeNext(next), next)
	}
}
