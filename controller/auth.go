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
	"net/netip"
	"net/url"
	"slices"
	"strconv"
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
	// enabled, with the same password, eg: changed by another way than the
	// dashboard.
	sessionCheck = time.Minute
	// sessionGrace is how long a session goes on, since its user was last
	// checked, while the database can't tell: a restart of PostgreSQL doesn't
	// log everybody out, but a user disabled or with a new password meanwhile
	// can't go on for long. Beyond, the session is refused until the database
	// answers again.
	sessionGrace = 5 * time.Minute
	// failedLoginDelay delays the answer to a failed login, and the next login
	// of its address, see loginLimiter.
	failedLoginDelay = time.Second
	// maxLoginBody is the size of the payload of a login at most.
	maxLoginBody = 4 << 10
)

// errNoUsers is the error of the dashboard without users.
const errNoUsers = "O painel não tem usuários: defina bootstrap no config.yml ou importe a versão anterior com o " +
	"cmd/migrator."

// Users are the users of the dashboard, see store.Store.
type Users interface {
	Authenticate(ctx context.Context, username, password string) (store.User, error)
	User(ctx context.Context, id int64) (store.User, error)
	CountUsers(ctx context.Context) (int64, error)
}

// session is a session open: its user, the mark of its password (see
// passwordMark), its CSRF token, when it expires, when its user was last found
// enabled with the same password and a role, and what the role lets it do
// (access), read again with its user; stale once the access changed on the
// dashboard, read again at its next request.
type session struct {
	userID   int64
	username string
	password string
	csrf     string
	expires  time.Time
	checked  time.Time
	stale    bool
	access   atomic.Pointer[access]
}

// auth is the login of the dashboard: its users, the limits of the logins,
// the proxies in front of it and the sessions open.
type auth struct {
	users Users

	now       func() time.Time
	failDelay time.Duration
	limiter   *loginLimiter
	proxies   []netip.Prefix // trusted with X-Forwarded-For and X-Forwarded-Proto
	hasUsers  atomic.Bool

	mu       sync.Mutex
	sessions map[string]*session // by the hash of their ID
}

// loopback is the proxies trusted by default: eg, ngrok on the host.
var loopback = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}

func newAuth(users Users) *auth {
	return &auth{users: users, now: time.Now, failDelay: failedLoginDelay, limiter: newLoginLimiter(),
		proxies: loopback, sessions: make(map[string]*session)}
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

// userIDKey is the key of the ID of the user of the request in its context.
type userIDKey struct{}

// userIDOf returns the ID of the user of the request, 0 without a session.
func userIDOf(ctx context.Context) int64 {
	id, _ := ctx.Value(userIDKey{}).(int64)
	return id
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
			writeJSONError(w, http.StatusForbidden, "Sessão desatualizada: recarregue a página.")
		case s != nil:
			ctx := context.WithValue(context.WithValue(r.Context(), userKey{}, s.username), userIDKey{}, s.userID)
			next.ServeHTTP(w, r.WithContext(withAccess(ctx, s.access.Load())))
		case strings.HasPrefix(r.URL.Path, "/api/") && !a.configured(r.Context()):
			writeJSONError(w, http.StatusServiceUnavailable, errNoUsers)
		case strings.HasPrefix(r.URL.Path, "/api/") || !safeMethod(r.Method):
			writeJSONError(w, http.StatusUnauthorized, "Sessão expirada: entre de novo.")
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
// cookies, within the limits of the logins (see loginLimiter): beyond, 429
// with Retry-After, the password unchecked. The answer to wrong credentials
// doesn't tell which one is wrong, and comes after failDelay, or as soon as
// the request ends. A user without a role is refused (403), its password
// right: nothing is granted to it.
func (a *auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !a.configured(r.Context()) {
		writeJSONError(w, http.StatusServiceUnavailable, errNoUsers)
		return
	}
	var request ui.LoginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBody)).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Pedido inválido.")
		return
	}
	client := a.clientAddress(r)
	ticket, refusal := a.limiter.admit(clientKey(client), request.Username, a.now())
	if refusal != nil {
		log.Debugf("ui: login from %s refused: %s", client, refusal.reason)
		w.Header().Set("Retry-After", strconv.Itoa(refusal.retryAfter()))
		writeJSONError(w, http.StatusTooManyRequests, refusal.reason)
		return
	}
	defer ticket.finish(loginAborted, a.now(), 0) // a panic of the store
	user, err := a.users.Authenticate(r.Context(), request.Username, request.Password)
	if errors.Is(err, store.ErrCredentials) {
		failures, wait := ticket.finish(loginFailed, a.now(), a.failDelay)
		log.Warnf("ui: failed login from %s (%d in a row, the next one in %v)", client, failures, wait)
		pause(r.Context(), a.failDelay)
		writeJSONError(w, http.StatusUnauthorized, "Usuário ou senha inválidos.")
		return
	}
	if err != nil {
		ticket.finish(loginAborted, a.now(), 0)
		log.Errorf("ui: login: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "Não foi possível entrar. Tente de novo.")
		return
	}
	ticket.finish(loginSucceeded, a.now(), 0)
	if user.RoleOf() == nil {
		log.Warnf("ui: login of %s from %s refused: the user has no role", user.Username, client)
		writeJSONError(w, http.StatusForbidden, errNoRole)
		return
	}

	id, s, err := a.open(user)
	if err != nil {
		log.Error(err)
		writeJSONError(w, http.StatusInternalServerError, "Não foi possível abrir a sessão. Tente de novo.")
		return
	}
	a.setCookies(w, r, id, s)
	log.Infof("ui: login of %s from %s", user.Username, client)
	writeJSON(w, sessionResponse(s))
}

// sessionResponse tells the session s: its user, its role and what it may do.
func sessionResponse(s *session) ui.SessionResponse {
	response := ui.SessionResponse{Authenticated: true, Username: s.username, Permissions: []string{}}
	if a := s.access.Load(); a != nil {
		response.Role = &ui.RoleRef{ID: a.roleID, Name: a.role}
		response.Permissions = a.perms
	}
	return response
}

// pause waits for d, or less if ctx ends first.
func pause(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// setCookies sets the cookies of the session s of id: the session, HttpOnly,
// and its CSRF token, for the web app to read.
func (a *auth) setCookies(w http.ResponseWriter, r *http.Request, id string, s *session) {
	maxAge := int(sessionTTL.Seconds())
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: id, Path: "/", Expires: s.expires, MaxAge: maxAge,
		HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: s.csrf, Path: "/", Expires: s.expires, MaxAge: maxAge,
		Secure: a.secure(r), SameSite: http.SameSiteStrictMode})
}

// handleLogout closes the session of the request, if any, and clears its
// cookies.
func (a *auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s := a.session(r); s != nil && !validCSRF(r, s) {
		writeJSONError(w, http.StatusForbidden, "Sessão desatualizada: recarregue a página.")
		return
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, sessionKey(cookie.Value))
		a.mu.Unlock()
	}
	for _, name := range []string{sessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: name == sessionCookie,
			Secure: a.secure(r), SameSite: http.SameSiteLaxMode})
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
	writeJSON(w, sessionResponse(s))
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
	s := &session{userID: user.ID, username: user.Username, password: passwordMark(user), csrf: csrf,
		expires: now.Add(sessionTTL), checked: now}
	s.access.Store(newAccess(user.RoleOf()))
	a.sessions[sessionKey(id)] = s
	return id, s, nil
}

// session returns the session of the request, open and not expired, nil for
// none: its user checked again every sessionCheck, and at once when its access
// changed on the dashboard (stale, see refreshUsers), the session closed once
// the user is disabled, deleted, has another password or no role, its access
// taken again from its role. While the database can't tell, the session goes
// on for sessionGrace since the last check, then is refused, kept for when the
// database answers again.
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
	case now.Sub(s.checked) < sessionCheck && !s.stale:
		a.mu.Unlock()
		return s
	}
	a.mu.Unlock()

	user, err := a.users.User(r.Context(), s.userID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.mu.Lock()
		unchecked := now.Sub(s.checked)
		a.mu.Unlock()
		if unchecked < sessionGrace {
			log.Warnf("ui: the user of a session: %v: the session goes on meanwhile", err)
			return s
		}
		log.Warnf("ui: the user of a session: %v: unchecked for %v, the session refused", err,
			unchecked.Round(time.Second))
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessions[key] != s {
		return nil // closed meanwhile, eg: a new password
	}
	if err != nil || !user.Enabled || subtle.ConstantTimeCompare([]byte(passwordMark(user)), []byte(s.password)) != 1 ||
		user.RoleOf() == nil {
		delete(a.sessions, key)
		return nil
	}
	s.access.Store(newAccess(user.RoleOf()))
	s.checked, s.stale = now, false
	return s
}

// passwordMark is the mark of the password of user its sessions keep, to end
// once it changes: a prefix of the SHA-256 of its hash, the hash itself not
// kept.
func passwordMark(user store.User) string {
	sum := sha256.Sum256([]byte(user.PasswordHash))
	return hex.EncodeToString(sum[:8])
}

// refreshUsers has the sessions of the users of ids read their access again at
// their next request, eg: their role changed; of every user without ids, eg:
// the permissions of a role changed.
func (a *auth) refreshUsers(ids ...int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.sessions {
		if len(ids) == 0 || slices.Contains(ids, s.userID) {
			s.stale = true
		}
	}
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
// trusted proxy (eg: ngrok): its cookies are then only sent over HTTPS.
func (a *auth) secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !a.trusted(remoteIP(r)) {
		return false
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// safeNext returns the page to go to after the login: next when it is a path
// of the dashboard, else its root, never another site nor the login. The
// browsers drop the tabs and the newlines of a URL and read a backslash as a
// slash, so that "/\t/x" goes to the site x: none of them, nor a space, is
// taken. Nor is an empty segment of the path: the browsers remove the dot
// segments, so that "/.//x" (or "/%2e//x") is "//x", the site x.
func safeNext(next string) string {
	if strings.ContainsFunc(next, func(c rune) bool { return c <= ' ' || c == 0x7f || c == '\\' }) ||
		!strings.HasPrefix(next, "/") || strings.HasPrefix(next, "/login") {
		return "/"
	}
	pathOf := next
	if i := strings.IndexAny(next, "?#"); i >= 0 {
		pathOf = next[:i]
	}
	if strings.Contains(pathOf, "//") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" ||
		!strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "/login") {
		return "/"
	}
	return next
}

// clientAddress is the address of the client of the request, for the logs and
// the limits of the logins: from a trusted proxy, the last address of
// X-Forwarded-For before the trusted ones (the ones before it are the word
// of the client), else the remote one.
func (a *auth) clientAddress(r *http.Request) string {
	remote := r.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	if !a.trusted(remoteIP(r)) {
		return remote
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	client := remote
	for i := len(hops) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(hops[i])
		if hop == "" && len(hops) == 1 {
			break // no header
		}
		ip, err := netip.ParseAddr(hop)
		if err != nil {
			if addrPort, portErr := netip.ParseAddrPort(hop); portErr == nil {
				ip, err = addrPort.Addr(), nil
			}
		}
		if err != nil {
			break // a proxy wouldn't: the last trusted one is the client
		}
		client = ip.Unmap().WithZone("").String()
		if !a.trusted(ip) {
			break
		}
	}
	return client
}

// remoteIP returns the IP of the remote address of the request, invalid for
// none, eg: a test.
func remoteIP(r *http.Request) netip.Addr {
	if addrPort, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return addrPort.Addr()
	}
	ip, _ := netip.ParseAddr(r.RemoteAddr)
	return ip
}

// trusted reports whether ip is the one of a trusted proxy.
func (a *auth) trusted(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap().WithZone("")
	for _, proxy := range a.proxies {
		if proxy.Contains(ip) {
			return true
		}
	}
	return false
}
