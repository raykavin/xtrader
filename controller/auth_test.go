package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// fakeUsers are the users of the tests, their passwords in plain text, their
// hash a mark of it.
type fakeUsers struct {
	mu        sync.Mutex
	users     []store.User
	passwords map[int64]string
	lookup    error  // of User, eg: the database out of reach
	onLookup  func() // called by User, eg: to close the sessions meanwhile

	// delay is how long a password takes to check, as bcrypt does: calls
	// counts the checks, active and maxActive the ones at once.
	delay             time.Duration
	calls             atomic.Int64
	active, maxActive atomic.Int64
}

// newFakeUsers returns the users of the pairs of username and password.
func newFakeUsers(credentials ...string) *fakeUsers {
	f := &fakeUsers{passwords: make(map[int64]string)}
	for i := 0; i+1 < len(credentials); i += 2 {
		id := int64(len(f.users) + 1)
		f.users = append(f.users, store.User{Model: store.Model{ID: id}, Username: credentials[i],
			PasswordHash: "hash:" + credentials[i+1], Enabled: true,
			RoleLink: &store.UserRole{UserID: id, RoleID: 1, Role: adminRole()}})
		f.passwords[id] = credentials[i+1]
	}
	return f
}

func (f *fakeUsers) Authenticate(_ context.Context, username, password string) (store.User, error) {
	f.calls.Add(1)
	active := f.active.Add(1)
	for {
		top := f.maxActive.Load()
		if active <= top || f.maxActive.CompareAndSwap(top, active) {
			break
		}
	}
	time.Sleep(f.delay)
	f.active.Add(-1)

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
	if f.onLookup != nil {
		f.onLookup()
	}
	if f.lookup != nil {
		return store.User{}, f.lookup
	}
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

// setPassword replaces the password of the user of id, as SQL or cmd/migrator
// would, the dashboard not told.
func (f *fakeUsers) setPassword(id int64, password string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.users {
		if f.users[i].ID == id {
			f.users[i].PasswordHash = "hash:" + password
			f.passwords[id] = password
		}
	}
}

// failLookups makes User fail with err, nil for none.
func (f *fakeUsers) failLookups(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookup = err
}

// serve serves a request to h from the client at remote, eg: a proxy, with
// the headers given, and returns its answer.
func serve(h http.Handler, method, target, body, remote string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = remote
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// loginBody is the payload of a login of username with password.
func loginBody(t *testing.T, username, password string) string {
	t.Helper()
	body, err := json.Marshal(ui.LoginRequest{Username: username, Password: password})
	require.NoError(t, err)
	return string(body)
}

// retryAfter returns the seconds of the Retry-After of an answer, 0 for none.
func retryAfter(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	value := rec.Header().Get("Retry-After")
	if value == "" {
		return 0
	}
	seconds, err := strconv.Atoi(value)
	require.NoError(t, err, value)
	return seconds
}

// clock is the time of a test, moved by hand.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock(d *Dashboard) *clock {
	c := &clock{now: time.Now()}
	d.auth.now = c.Now
	return c
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
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
	resp, _ = do(t, http.MethodGet, srv.URL+"/performance", "", nil)
	assert.Equal(t, "/login?next=%2Fperformance", resp.Header.Get("Location"))

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
		assert.Equal(t, "Sessão expirada: entre de novo.", decodeError(t, body), request.path)
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

	everything, err := json.Marshal(ui.AllPermissions())
	require.NoError(t, err)
	logged := `{"authenticated":true,"username":"` + testUsername + `","role":{"id":1,"name":"Administrador"},` +
		`"permissions":` + string(everything) + `}`
	resp, body := login(t, srv, strings.ToUpper(testUsername), testPassword)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, logged, body)
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
	assert.JSONEq(t, logged, body)
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
		assert.Contains(t, decodeError(t, body), "Sessão desatualizada", token)
	}
	resp, body := do(t, http.MethodPost, srv.URL+"/api/agents/bot/controls/start", "", cookies, csrfHeader, csrf.Value)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "through to the agent, whose controls are off")
	assert.Equal(t, "Controles do agente desligados neste canal: ligue-os em "+
		"Configurações › Agentes.", decodeError(t, body))
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
		assert.Equal(t, "Usuário ou senha inválidos.", decodeError(t, body), credentials)
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
		// the client is the last address the proxy saw: the first one is the client's word
		resp, body := login(t, srv, testUsername, password, "X-Forwarded-For", "198.51.100.1, 203.0.113.7")
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
		"":                           "/",
		"/":                          "/",
		"/?agent=llm&pair=XAUUSDm":   "/?agent=llm&pair=XAUUSDm",
		"//evil.example":             "/",
		"/\\evil.example":            "/",
		"https://evil.example":       "/",
		"/login":                     "/",
		"/login?next=/":              "/",
		"/settings#prompts":          "/settings#prompts",
		"/?agent=llm&pair=XAU%2FUSD": "/?agent=llm&pair=XAU%2FUSD",
		// the browsers drop the tabs and the newlines of a URL, and read a
		// backslash as a slash: //evil.example
		"/\t/evil.example":     "/",
		"/\n/evil.example":     "/",
		"/\r/evil.example":     "/",
		"\t//evil.example":     "/",
		"/ /evil.example":      "/",
		"/\x00/evil.example":   "/",
		"/\x7f/evil.example":   "/",
		"/a\\b":                "/",
		"/\\\\evil.example":    "/",
		"///evil.example":      "/",
		"http:/evil.example":   "/",
		"https:evil.example":   "/",
		"//user@evil.example/": "/",
		"javascript:alert(1)":  "/",
		"/%2Flogin":            "/%2Flogin",
		"/%6Cogin":             "/",
		// the browsers remove the dot segments of a path: "//evil.example"
		"/.//evil.example":      "/",
		"/a/..//evil.example/x": "/",
		"/%2e//evil.example":    "/",
		"/%2E%2E//evil.example": "/",
		"/a//b":                 "/",
		"/?next=//evil.example": "/?next=//evil.example",
		"/settings#//evil":      "/settings#//evil",
	} {
		assert.Equal(t, want, safeNext(next), "%q", next)
	}
}

// A page of the login with a session goes on to next, a page of the dashboard
// only: never another site, whatever the browser drops of the URL.
func TestAuth_LoginRedirectStaysOnTheDashboard(t *testing.T) {
	_, srv := authServer(t, WithUsers(newFakeUsers(testUsername, testPassword)))
	resp, _ := login(t, srv, testUsername, testPassword)
	session := cookieOf(resp, sessionCookie)
	require.NotNil(t, session)
	for next, want := range map[string]string{
		"%2F%09%2Fevil.example":  "/",
		"%2F%0A%2Fevil.example":  "/",
		"%2F%0D%2Fevil.example":  "/",
		"%2F%5C%2Fevil.example":  "/",
		"https%3A%2F%2Fevil.exa": "/",
		"%2F.%2F%2Fevil.example": "/",
		"%2Fsettings%23users":    "/settings#users",
	} {
		// the Location as sent, which a client of Go refuses to follow
		req := httptest.NewRequest(http.MethodGet, "/login?next="+next, nil)
		req.AddCookie(session)
		rec := httptest.NewRecorder()
		srv.Config.Handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusFound, rec.Code, next)
		assert.Equal(t, want, rec.Header().Get("Location"), next)
	}
}

// 200 wrong passwords at once from an address, with the right one from
// another: the right one gets in, and few of the wrong ones are checked, the
// others refused before bcrypt with 429 and Retry-After.
func TestAuth_LoginBruteForce_ConcurrentGuesses(t *testing.T) {
	users := newFakeUsers(testUsername, testPassword)
	users.delay = 20 * time.Millisecond
	d, srv := authServer(t, WithUsers(users))
	d.auth.failDelay = 50 * time.Millisecond
	h := srv.Config.Handler

	const guesses = 200
	start := make(chan struct{})
	answers := make([]*httptest.ResponseRecorder, guesses+1)
	var wg sync.WaitGroup
	for i := range answers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, client := loginBody(t, testUsername, fmt.Sprintf("guess-%d", i)), "203.0.113.66"
			if i == guesses {
				body, client = loginBody(t, testUsername, testPassword), "198.51.100.7"
			}
			<-start
			answers[i] = serve(h, http.MethodPost, "/api/auth/login", body, "127.0.0.1:40000",
				"X-Forwarded-For", client)
		}()
	}
	began := time.Now()
	close(start)
	wg.Wait()

	assert.Equal(t, http.StatusOK, answers[guesses].Code, "the right password from elsewhere: %s",
		answers[guesses].Body.String())
	refused := 0
	for _, rec := range answers[:guesses] {
		switch rec.Code {
		case http.StatusUnauthorized:
		case http.StatusTooManyRequests:
			refused++
			assert.GreaterOrEqual(t, retryAfter(t, rec), 1, "429 tells when to try again")
		default:
			t.Errorf("a wrong password answered %d: %s", rec.Code, rec.Body.String())
		}
	}
	checked := users.calls.Load() - 1 // the right one
	t.Logf("%d wrong passwords checked, %d refused before bcrypt, in %v", checked, refused, time.Since(began))
	assert.LessOrEqual(t, checked, int64(clientFreeFailures+1), "the guesses of an address checked one at a time")
	assert.Equal(t, int64(guesses), checked+int64(refused))
	assert.LessOrEqual(t, users.maxActive.Load(), int64(maxConcurrentLogins))
}

// Logins of many addresses at once, eg: a password spraying, never check more
// than maxConcurrentLogins passwords at once: the others get 429 at once.
func TestAuth_LoginBruteForce_BcryptsAtOnce(t *testing.T) {
	users := newFakeUsers(testUsername, testPassword)
	users.delay = 50 * time.Millisecond
	_, srv := authServer(t, WithUsers(users))
	h := srv.Config.Handler

	const clients = 200
	start := make(chan struct{})
	codes := make([]int, clients)
	retries := make([]string, clients)
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := loginBody(t, fmt.Sprintf("user-%d", i), "wrong-password")
			<-start
			rec := serve(h, http.MethodPost, "/api/auth/login", body, "127.0.0.1:40000",
				"X-Forwarded-For", fmt.Sprintf("198.18.%d.%d", i/250, i%250+1))
			codes[i], retries[i] = rec.Code, rec.Header().Get("Retry-After")
		}()
	}
	close(start)
	wg.Wait()

	refused := 0
	for i, code := range codes {
		if code == http.StatusTooManyRequests {
			refused++
			assert.Equal(t, "1", retries[i])
		} else {
			assert.Equal(t, http.StatusUnauthorized, code)
		}
	}
	t.Logf("%d passwords checked, at most %d at once, %d refused", users.calls.Load(), users.maxActive.Load(), refused)
	assert.LessOrEqual(t, users.maxActive.Load(), int64(maxConcurrentLogins), "the bcrypts at once")
	assert.Positive(t, refused)
	assert.Equal(t, int64(clients), users.calls.Load()+int64(refused), "a login refused is never checked")
}

// The failed logins of an address in a row wait failDelay each, then twice as
// long each, up to clientMaxBackoff, the password not checked meanwhile, even
// the right one; another address goes on, and an hour without failures
// forgets them.
func TestAuth_LoginBruteForce_BackoffOfAnAddress(t *testing.T) {
	users := newFakeUsers(testUsername, testPassword)
	d, srv := authServer(t, WithUsers(users))
	d.auth.failDelay = 10 * time.Millisecond // the answers, the next login 1s later on the clock
	clock := newClock(d)
	h := srv.Config.Handler
	from := func(client, username, password string) *httptest.ResponseRecorder {
		return serve(h, http.MethodPost, "/api/auth/login", loginBody(t, username, password), "127.0.0.1:40000",
			"X-Forwarded-For", client)
	}
	const attacker = "203.0.113.66"
	guess := 0
	fail := func() {
		t.Helper()
		guess++
		rec := from(attacker, fmt.Sprintf("someone-%d", guess), "wrong")
		require.Equal(t, http.StatusUnauthorized, rec.Code, "guess %d: %s", guess, rec.Body.String())
	}

	for i := 1; i < clientFreeFailures; i++ {
		fail()
		rec := from(attacker, testUsername, testPassword)
		require.Equal(t, http.StatusTooManyRequests, rec.Code, "right after failure %d", i)
		assert.Equal(t, 1, retryAfter(t, rec))
		clock.advance(time.Second)
	}
	wait := clientBackoff
	for range 12 {
		fail()
		calls := users.calls.Load()
		rec := from(attacker, testUsername, testPassword)
		require.Equal(t, http.StatusTooManyRequests, rec.Code, "after %d failures", guess)
		assert.Equal(t, int(wait.Seconds()), retryAfter(t, rec), "after %d failures", guess)
		assert.Contains(t, decodeError(t, rec.Body.String()), "Muitas tentativas de login falharam")
		assert.Equal(t, calls, users.calls.Load(), "the password not checked, even the right one")

		clock.advance(wait - time.Second)
		require.Equal(t, http.StatusTooManyRequests, from(attacker, testUsername, testPassword).Code)
		rec = from("198.51.100.7", testUsername, testPassword)
		require.Equal(t, http.StatusOK, rec.Code, "another address: %s", rec.Body.String())
		clock.advance(time.Second)
		wait = min(2*wait, clientMaxBackoff)
	}
	assert.Equal(t, clientMaxBackoff, wait, "up to clientMaxBackoff")

	clock.advance(loginForget)
	for range clientFreeFailures - 1 {
		fail()
		clock.advance(time.Second)
	}
	assert.Equal(t, http.StatusOK, from(attacker, testUsername, testPassword).Code, "forgotten")
}

// The logins of a username, from any address, have a budget of userBurst in a
// row, then one every userRefill: a looser limit than the one of an address,
// which a success refills. A refused one doesn't tell whether the user exists.
func TestAuth_LoginBruteForce_BudgetOfAUser(t *testing.T) {
	users := newFakeUsers(testUsername, testPassword)
	d, srv := authServer(t, WithUsers(users))
	clock := newClock(d)
	h := srv.Config.Handler
	client := 0
	from := func(username, password string) *httptest.ResponseRecorder {
		client++
		return serve(h, http.MethodPost, "/api/auth/login", loginBody(t, username, password), "127.0.0.1:40000",
			"X-Forwarded-For", fmt.Sprintf("198.18.0.%d", client))
	}

	for _, username := range []string{testUsername, "nobody"} {
		for i := range userBurst {
			require.Equal(t, http.StatusUnauthorized, from(username, "wrong").Code, "%s: login %d", username, i)
		}
		calls := users.calls.Load()
		rec := from(strings.ToUpper(username)+" ", testPassword)
		require.Equal(t, http.StatusTooManyRequests, rec.Code, username)
		assert.Equal(t, int(userRefill.Seconds()), retryAfter(t, rec), username)
		assert.Equal(t, calls, users.calls.Load(), "not checked")
		assert.Equal(t, http.StatusUnauthorized, from("other", "wrong").Code, "another username goes on")
	}

	clock.advance(userRefill)
	require.Equal(t, http.StatusOK, from(testUsername, testPassword).Code, "one more every userRefill")
	for i := range userBurst {
		require.Equal(t, http.StatusUnauthorized, from(testUsername, "wrong").Code, "refilled: login %d", i)
	}
	assert.Equal(t, http.StatusTooManyRequests, from(testUsername, testPassword).Code)
}

// The delay of a failed login ends with its request.
func TestAuth_FailedLoginDelayEndsWithTheRequest(t *testing.T) {
	d, srv := authServer(t, WithUsers(newFakeUsers(testUsername, testPassword)))
	d.auth.failDelay = time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/auth/login",
		strings.NewReader(loginBody(t, testUsername, "wrong")))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.Config.Handler.ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-done:
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	case <-time.After(5 * time.Second):
		t.Fatal("the failed login still waits after its request ended")
	}
}

// X-Forwarded-For and X-Forwarded-Proto are only believed from a trusted
// proxy, the loopback by default: the address of the logs and of the limits
// of the logins is the one of the client otherwise, and its cookies are only
// Secure over HTTPS.
func TestAuth_ForwardedHeadersOnlyFromTrustedProxies(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	d, srv := authServer(t, WithUsers(newFakeUsers(testUsername, testPassword)))
	h := srv.Config.Handler
	body := loginBody(t, testUsername, testPassword)

	rec := serve(h, http.MethodPost, "/api/auth/login", body, "192.0.2.10:5555",
		"X-Forwarded-For", "203.0.113.7", "X-Forwarded-Proto", "https")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, logs.String(), "login of "+testUsername+" from 192.0.2.10")
	assert.NotContains(t, logs.String(), "203.0.113.7")
	for _, cookie := range rec.Result().Cookies() {
		assert.False(t, cookie.Secure, "%s: over HTTP, whatever the client says", cookie.Name)
	}

	for forwarded, client := range map[string]string{
		"203.0.113.7":                               "203.0.113.7",
		"198.51.100.1, 203.0.113.7":                 "203.0.113.7",
		"203.0.113.7, 127.0.0.1":                    "203.0.113.7",
		"2001:db8::1":                               "2001:db8::1",
		"not an address, 203.0.113.7":               "203.0.113.7",
		"203.0.113.7, not an address":               "127.0.0.1",
		"198.51.100.1, 203.0.113.7, ::1, 127.0.0.2": "203.0.113.7",
	} {
		logs.Reset()
		rec := serve(h, http.MethodPost, "/api/auth/login", body, "127.0.0.1:5555",
			"X-Forwarded-For", forwarded, "X-Forwarded-Proto", "https")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, logs.String(), "login of "+testUsername+" from "+client+"\"", forwarded)
		for _, cookie := range rec.Result().Cookies() {
			assert.True(t, cookie.Secure, "%s: through the proxy over HTTPS", cookie.Name)
		}
	}

	// the limits of the logins follow the client, not the headers it sends
	newClock(d)
	d.auth.failDelay = 10 * time.Millisecond
	wrong := loginBody(t, "someone", "wrong")
	require.Equal(t, http.StatusUnauthorized, serve(h, http.MethodPost, "/api/auth/login", wrong, "192.0.2.10:5555",
		"X-Forwarded-For", "203.0.113.1").Code)
	rec = serve(h, http.MethodPost, "/api/auth/login", wrong, "192.0.2.10:5556", "X-Forwarded-For", "203.0.113.2")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, "the same client")
}

// A session ends with the password of its user, changed elsewhere than on
// the dashboard, eg: by SQL or by cmd/migrator, found within sessionCheck.
func TestAuth_SessionEndsWithItsPassword(t *testing.T) {
	users := newFakeUsers(testUsername, testPassword, "other", "other-password")
	d, srv := authServer(t, WithUsers(users))
	clock := newClock(d)
	resp, _ := login(t, srv, testUsername, testPassword)
	cookies := sessionOf(resp)
	resp, _ = login(t, srv, "other", "other-password")
	other := sessionOf(resp)

	users.setPassword(1, "a-new-password")
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "checked again after sessionCheck")
	clock.advance(sessionCheck + time.Second)
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the password changed")
	users.setPassword(1, testPassword)
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "closed for good")
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", other)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the other user goes on")
	resp, _ = login(t, srv, testUsername, testPassword)
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", sessionOf(resp))
	assert.Equal(t, http.StatusOK, resp.StatusCode, "a new session")
}

// A session closed while its user is checked, eg: by a new password on the
// dashboard, isn't served by the check.
func TestAuth_SessionClosedWhileChecked(t *testing.T) {
	users := newFakeUsers(testUsername, testPassword)
	d, srv := authServer(t, WithUsers(users))
	clock := newClock(d)
	resp, _ := login(t, srv, testUsername, testPassword)
	cookies := sessionOf(resp)

	users.onLookup = func() { d.auth.closeUser(1) }
	clock.advance(sessionCheck + time.Second)
	resp, _ = do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Empty(t, d.auth.sessions)
}

// With the database out of reach, a session goes on for sessionGrace since its
// user was last checked, then is refused until the database answers again,
// when it goes on, unless its user changed meanwhile.
func TestAuth_SessionWhileTheDatabaseFails(t *testing.T) {
	users := newFakeUsers(testUsername, testPassword)
	d, srv := authServer(t, WithUsers(users))
	clock := newClock(d)
	resp, _ := login(t, srv, testUsername, testPassword)
	cookies := sessionOf(resp)
	get := func() int {
		resp, _ := do(t, http.MethodGet, srv.URL+"/api/agents", "", cookies)
		return resp.StatusCode
	}

	users.failLookups(errors.New("connection refused"))
	clock.advance(sessionCheck + time.Second)
	assert.Equal(t, http.StatusOK, get(), "a short outage")
	clock.advance(sessionGrace - sessionCheck - 2*time.Second)
	assert.Equal(t, http.StatusOK, get(), "within sessionGrace")
	clock.advance(time.Second)
	assert.Equal(t, http.StatusUnauthorized, get(), "unchecked for sessionGrace")
	_, body := do(t, http.MethodGet, srv.URL+"/api/auth/session", "", cookies)
	assert.JSONEq(t, `{"authenticated":false}`, body)

	users.failLookups(nil)
	assert.Equal(t, http.StatusOK, get(), "the database back, the session goes on")

	users.failLookups(errors.New("connection refused"))
	clock.advance(sessionGrace + time.Second)
	assert.Equal(t, http.StatusUnauthorized, get())
	users.setPassword(1, "a-new-password")
	users.failLookups(nil)
	assert.Equal(t, http.StatusUnauthorized, get(), "its password changed meanwhile")
}

// The proxies of ui.trusted_proxies are the ones trusted, eg: the gateway of
// the network of the compose, through which ngrok on the host reaches the
// controller in its container; the loopback isn't anymore.
func TestAuth_TrustedProxiesConfigured(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	_, srv := authServer(t, WithTrustedProxies([]netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}),
		WithUsers(newFakeUsers(testUsername, testPassword)))
	h := srv.Config.Handler
	body := loginBody(t, testUsername, testPassword)

	for remote, want := range map[string]struct {
		client string
		secure bool
	}{
		"172.18.0.1:40000": {"203.0.113.7", true},
		"127.0.0.1:40000":  {"127.0.0.1", false},
		"10.12.0.5:40000":  {"10.12.0.5", false},
	} {
		logs.Reset()
		rec := serve(h, http.MethodPost, "/api/auth/login", body, remote,
			"X-Forwarded-For", "203.0.113.7", "X-Forwarded-Proto", "https")
		require.Equal(t, http.StatusOK, rec.Code, remote)
		assert.Contains(t, logs.String(), "login of "+testUsername+" from "+want.client+"\"", remote)
		session := rec.Result().Cookies()[0]
		assert.Equal(t, want.secure, session.Secure, remote)
	}
}

// The addresses and the usernames followed are maxLoginEntries at most: a new
// one is refused while all of them are limited, and taken once some are over.
func TestLoginLimiter_Bounded(t *testing.T) {
	l := newLoginLimiter()
	l.entries = 64
	now := time.Now()
	fail := func(client, user string) {
		t.Helper()
		ticket, refusal := l.admit(client, user, now)
		require.Nil(t, refusal, "%s %s", client, user)
		ticket.finish(loginFailed, now, time.Second)
	}
	for i := range l.entries {
		fail(fmt.Sprintf("198.18.0.%d", i), fmt.Sprintf("user-%d", i))
	}
	require.Len(t, l.clients, l.entries)
	require.Len(t, l.users, l.entries)

	now = now.Add(time.Second)
	_, refusal := l.admit("198.18.1.1", "user-0", now)
	require.NotNil(t, refusal, "the addresses all limited")
	assert.Equal(t, refusedFailures, refusal.reason)
	_, refusal = l.admit("198.18.0.0", "someone", now)
	require.NotNil(t, refusal, "the usernames all limited")
	ticket, refusal := l.admit("198.18.0.0", "user-0", now)
	require.Nil(t, refusal, "the ones followed go on")
	ticket.finish(loginFailed, now, time.Second)

	now = now.Add(loginForget)
	ticket, refusal = l.admit("198.18.1.1", "someone", now)
	require.Nil(t, refusal, "the old ones forgotten")
	ticket.finish(loginSucceeded, now, time.Second)
	assert.LessOrEqual(t, len(l.clients), 2)
	assert.LessOrEqual(t, len(l.users), 1)
	assert.Zero(t, l.active)
}

// The limits of an address: an IPv6 one by its /64, an IPv4 one mapped to
// IPv6 as IPv4; of a username: as the store finds it, its length bounded.
func TestLoginLimiter_Keys(t *testing.T) {
	assert.Equal(t, "203.0.113.7", clientKey("203.0.113.7"))
	assert.Equal(t, "203.0.113.7", clientKey("::ffff:203.0.113.7"))
	assert.Equal(t, "2001:db8:1:2::/64", clientKey("2001:db8:1:2:aaaa:bbbb:cccc:dddd"))
	assert.Equal(t, "pipe", clientKey("pipe"))
	assert.Equal(t, "admin", usernameKey(" Admin "))
	assert.Len(t, usernameKey(strings.Repeat("x", maxLoginBody)), maxUsernameKey)
}

// setRole gives role to the user of id (nil takes it away), as SQL would, the
// dashboard not told.
func (f *fakeUsers) setRole(id int64, role *store.Role) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.users {
		if f.users[i].ID == id {
			f.users[i].RoleLink = nil
			if role != nil {
				f.users[i].RoleLink = &store.UserRole{UserID: id, RoleID: role.ID, Role: role}
			}
		}
	}
}
