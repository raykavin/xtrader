package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The dashboard is served to the users of the database logged in (see
// WithUsers): a session ID in a cookie, random, which the controller keeps in
// memory for sessionTTL, so a restart logs the users out. A session carries a
// CSRF token too, echoed by the web app in csrfHeader on every request that
// changes something, which a page of another site can't read. Without users
// the dashboard is closed: its pages lead to the login, which tells so, and
// its API answers 503.
const (
	// sessionCookie is the cookie of the session ID.
	sessionCookie = "xtrader_session"
	// csrfCookie is the cookie of the CSRF token of the session, readable by
	// the web app: the names of gobox/httpserver/middlewares, whose middleware
	// is for Gin only.
	csrfCookie = "csrf_token"
	// csrfHeader is the header the web app echoes the CSRF token in.
	csrfHeader = "X-CSRF-Token"
	// sessionTTL is how long a session lasts from the login.
	sessionTTL = 7 * 24 * time.Hour
	// sessionCheck is how often a session checks that its user is still
	// enabled, eg: disabled by another way than the dashboard.
	sessionCheck = time.Minute
	// failedLoginDelay delays each failed login, one at a time: a password
	// can't be guessed faster than one try per delay.
	failedLoginDelay = time.Second
	// maxLoginBody is the size of the payload of a login at most.
	maxLoginBody = 4 << 10
)

// errNoUsers is the error of the dashboard without users.
const errNoUsers = "the dashboard has no user: set bootstrap in config.yml, or import the previous version with " +
	"cmd/migrator"

// Users are the users of the dashboard, see store.Store.
type Users interface {
	Authenticate(ctx context.Context, username, password string) (store.User, error)
	User(ctx context.Context, id int64) (store.User, error)
	CountUsers(ctx context.Context) (int64, error)
}

// session is a session open: its user, its CSRF token, when it expires and
// when its user was last found enabled.
type session struct {
	userID   int64
	username string
	csrf     string
	expires  time.Time
	checked  time.Time
}

// auth is the login of the dashboard: its users and the sessions open.
type auth struct {
	users Users

	now       func() time.Time
	failDelay time.Duration
	failMu    sync.Mutex // the failed logins, one at a time
	hasUsers  atomic.Bool

	mu       sync.Mutex
	sessions map[string]*session // by the hash of their ID
}

func newAuth(users Users) *auth {
	return &auth{users: users, now: time.Now, failDelay: failedLoginDelay, sessions: make(map[string]*session)}
}

// configured reports whether the dashboard has users to log in with.
func (a *auth) configured(ctx context.Context) bool {
	if a.users == nil {
		return false
	}
	if a.hasUsers.Load() {
		return true
	}
	count, err := a.users.CountUsers(ctx)
	if err != nil {
		log.Warnf("ui: the users: %v", err)
		return false
	}
	a.hasUsers.Store(count > 0)
	return count > 0
}

// userKey is the key of the user of the request in its context.
type userKey struct{}

// userOf returns the username of the user of the request, see protect.
func userOf(ctx context.Context) string {
	name, _ := ctx.Value(userKey{}).(string)
	return name
}

// protect serves the login and, to the requests with a session, the routes of
// next, their user in their context: without one, the pages lead to the login
// and the API answers 401 (503 without users). A request changing something
// needs the CSRF token of its session. The files of the bundle are public:
// the login page needs them.
func (a *auth) protect(next http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Post("/api/auth/login", a.handleLogin)
	r.Post("/api/auth/logout", a.handleLogout)
	r.Get("/api/auth/session", a.handleSession)
	r.Get("/login", func(w http.ResponseWriter, r *http.Request) {
		if a.session(r) != nil {
			http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
	r.Handle("/ui/*", next)
	r.HandleFunc("/*", func(w http.ResponseWriter, r *http.Request) {
		s := a.session(r)
		switch {
		case s != nil && !safeMethod(r.Method) && !validCSRF(r, s):
			writeJSONError(w, http.StatusForbidden, "invalid or missing CSRF token: reload the page")
		case s != nil:
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, s.username)))
		case strings.HasPrefix(r.URL.Path, "/api/") && !a.configured(r.Context()):
			writeJSONError(w, http.StatusServiceUnavailable, errNoUsers)
		case strings.HasPrefix(r.URL.Path, "/api/") || !safeMethod(r.Method):
			writeJSONError(w, http.StatusUnauthorized, "authentication required")
		default:
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		}
	})
	return r
}

// safeMethod reports whether method changes nothing, needing no CSRF token.
func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// validCSRF reports whether the request echoes the CSRF token of its session.
func validCSRF(r *http.Request, s *session) bool {
	token := r.Header.Get(csrfHeader)
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.csrf)) == 1
}

// handleLogin opens a session for the credentials of the request, in its
// cookies. The answer to wrong credentials doesn't tell which one is wrong,
// and comes after failDelay.
func (a *auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !a.configured(r.Context()) {
		writeJSONError(w, http.StatusServiceUnavailable, errNoUsers)
		return
	}
	var request ui.LoginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBody)).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	user, err := a.users.Authenticate(r.Context(), request.Username, request.Password)
	if errors.Is(err, store.ErrCredentials) {
		a.failMu.Lock()
		time.Sleep(a.failDelay)
		a.failMu.Unlock()
		log.Warnf("ui: failed login from %s", clientAddress(r))
		writeJSONError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err != nil {
		log.Errorf("ui: login: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "the login failed, try again")
		return
	}

	id, s, err := a.open(user)
	if err != nil {
		log.Error(err)
		writeJSONError(w, http.StatusInternalServerError, "the session couldn't be opened")
		return
	}
	a.setCookies(w, r, id, s)
	log.Infof("ui: login of %s from %s", user.Username, clientAddress(r))
	writeJSON(w, ui.SessionResponse{Authenticated: true, Username: user.Username})
}

// setCookies sets the cookies of the session s of id: the session, HttpOnly,
// and its CSRF token, for the web app to read.
func (a *auth) setCookies(w http.ResponseWriter, r *http.Request, id string, s *session) {
	maxAge := int(sessionTTL.Seconds())
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: id, Path: "/", Expires: s.expires, MaxAge: maxAge,
		HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: s.csrf, Path: "/", Expires: s.expires, MaxAge: maxAge,
		Secure: secure(r), SameSite: http.SameSiteStrictMode})
}

// handleLogout closes the session of the request, if any, and clears its
// cookies.
func (a *auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s := a.session(r); s != nil && !validCSRF(r, s) {
		writeJSONError(w, http.StatusForbidden, "invalid or missing CSRF token: reload the page")
		return
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, sessionKey(cookie.Value))
		a.mu.Unlock()
	}
	for _, name := range []string{sessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: name == sessionCookie,
			Secure: secure(r), SameSite: http.SameSiteLaxMode})
	}
	writeJSON(w, ui.SessionResponse{Authenticated: false})
}

// handleSession tells whether the request carries a session, and its user,
// 503 without users.
func (a *auth) handleSession(w http.ResponseWriter, r *http.Request) {
	if !a.configured(r.Context()) {
		writeJSONError(w, http.StatusServiceUnavailable, errNoUsers)
		return
	}
	s := a.session(r)
	if s == nil {
		writeJSON(w, ui.SessionResponse{})
		return
	}
	writeJSON(w, ui.SessionResponse{Authenticated: true, Username: s.username})
}

// open opens a session of user: its ID, random, and the session. The sessions
// expired are dropped.
func (a *auth) open(user store.User) (string, *session, error) {
	id, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", nil, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for key, s := range a.sessions {
		if !now.Before(s.expires) {
			delete(a.sessions, key)
		}
	}
	s := &session{userID: user.ID, username: user.Username, csrf: csrf, expires: now.Add(sessionTTL), checked: now}
	a.sessions[sessionKey(id)] = s
	return id, s, nil
}

// session returns the session of the request, open and not expired, nil for
// none: its user checked again every sessionCheck, the session closed once the
// user is disabled or deleted.
func (a *auth) session(r *http.Request) *session {
	if a.users == nil {
		return nil
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return nil
	}
	key := sessionKey(cookie.Value)
	a.mu.Lock()
	s, ok := a.sessions[key]
	now := a.now()
	switch {
	case !ok:
		a.mu.Unlock()
		return nil
	case !now.Before(s.expires):
		delete(a.sessions, key)
		a.mu.Unlock()
		return nil
	case now.Sub(s.checked) < sessionCheck:
		a.mu.Unlock()
		return s
	}
	a.mu.Unlock()

	user, err := a.users.User(r.Context(), s.userID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Warnf("ui: the user of a session: %v", err)
		return s // the database out of reach: the session goes on meanwhile
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil || !user.Enabled {
		delete(a.sessions, key)
		return nil
	}
	s.checked = now
	return s
}

// closeUser closes the sessions of the user of id, eg: disabled, or with a new
// password.
func (a *auth) closeUser(id int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, s := range a.sessions {
		if s.userID == id {
			delete(a.sessions, key)
		}
	}
}

// randomToken returns 32 random bytes in URL-safe base64.
func randomToken() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(secret), nil
}

// sessionKey is the key of a session: the hash of its ID, the ID itself kept
// by the browser only.
func sessionKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// secure reports whether the request came over HTTPS, directly or through a
// proxy (eg: ngrok): its cookies are then only sent over HTTPS.
func secure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// safeNext returns the page to go to after the login: next when it is a path
// of the dashboard, else its root, never another site.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") ||
		strings.HasPrefix(next, "/login") {
		return "/"
	}
	return next
}

// clientAddress is the address of the client of the request for the logs: the
// first one of X-Forwarded-For behind a proxy, else the remote one.
func clientAddress(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		return strings.TrimSpace(first)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
