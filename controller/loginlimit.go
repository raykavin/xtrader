package controller

import (
	"math"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// The logins are limited before their password is checked, bcrypt costing
// about 250 ms of a CPU (see store.Authenticate), so that passwords can't be
// guessed faster than the limits, nor the CPU of the controller, which runs
// the agents, be spent on the guesses:
//   - by the address of the client, strictly: one login at a time, the next
//     one failDelay after a failure, and after clientFreeFailures failures in
//     a row twice as long each time, from clientBackoff up to
//     clientMaxBackoff, forgotten loginForget after the last one;
//   - by the username, more loosely, since all the addresses share it and an
//     attacker shouldn't lock its user out: userBurst logins in a row, then
//     one every userRefill, refilled by a success;
//   - and maxConcurrentLogins passwords checked at once at most.
//
// A login refused gets 429 with Retry-After, its password unchecked, whether
// its user exists or not.
const (
	maxConcurrentLogins = 2
	clientFreeFailures  = 5
	clientBackoff       = 2 * time.Second
	clientMaxBackoff    = 5 * time.Minute
	loginForget         = time.Hour
	userBurst           = 10
	userRefill          = 30 * time.Second

	// maxLoginEntries bounds the addresses and the usernames followed, each:
	// beyond, a new one is refused until others are forgotten.
	maxLoginEntries = 4096
	// maxUsernameKey bounds the key of a username, longer than any username.
	maxUsernameKey = 64
)

// The reasons of the logins refused.
const (
	refusedFailures = "Muitas tentativas de login falharam: tente de novo mais tarde."
	refusedBusy     = "Muitos logins ao mesmo tempo: tente de novo em instantes."
)

// loginLimiter follows the logins of the dashboard, see above.
type loginLimiter struct {
	mu      sync.Mutex
	clients map[string]*clientLogins
	users   map[string]*userLogins
	active  int // passwords being checked
	entries int // maxLoginEntries, but in the tests
}

// clientLogins are the logins of an address.
type clientLogins struct {
	busy     bool      // one being checked
	failures int       // in a row
	last     time.Time // of the last failure
	until    time.Time // no login before
}

// userLogins are the logins of a username: its tokens, one spent by each
// login.
type userLogins struct {
	tokens float64
	at     time.Time // when counted
}

// available returns the tokens of u at now, a whole bucket for none.
func (u *userLogins) available(now time.Time) float64 {
	if u == nil {
		return userBurst
	}
	return min(userBurst, u.tokens+float64(now.Sub(u.at))/float64(userRefill))
}

// loginRefusal is a login refused before its password is checked: why, and
// when to try again.
type loginRefusal struct {
	reason string
	retry  time.Duration
}

// retryAfter returns the seconds of the Retry-After of r, 1 at least.
func (r *loginRefusal) retryAfter() int {
	return max(1, int(math.Ceil(r.retry.Seconds())))
}

// loginOutcome is how the check of a password ended.
type loginOutcome int

const (
	loginAborted   loginOutcome = iota // unchecked, eg: the database out of reach
	loginFailed                        // wrong credentials
	loginSucceeded                     // right ones
)

// loginTicket is a login admitted, its password being checked: finish ends it.
type loginTicket struct {
	limiter      *loginLimiter
	client, user string
	done         bool
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{clients: make(map[string]*clientLogins), users: make(map[string]*userLogins),
		entries: maxLoginEntries}
}

// admit admits a login of username from client, see clientKey, at now: else
// it tells why not. A login admitted is ended by the finish of its ticket.
func (l *loginLimiter) admit(client, username string, now time.Time) (*loginTicket, *loginRefusal) {
	user := usernameKey(username)
	l.mu.Lock()
	defer l.mu.Unlock()

	c := l.clients[client]
	if c != nil && !c.busy && !now.Before(c.until) && now.Sub(c.last) >= loginForget {
		c.failures = 0
	}
	switch {
	case c != nil && c.busy:
		return nil, &loginRefusal{refusedBusy, time.Second}
	case c != nil && now.Before(c.until):
		return nil, &loginRefusal{refusedFailures, c.until.Sub(now)}
	}
	u := l.users[user]
	tokens := u.available(now)
	switch {
	case tokens < 1:
		return nil, &loginRefusal{refusedFailures, time.Duration((1 - tokens) * float64(userRefill))}
	case l.active >= maxConcurrentLogins:
		return nil, &loginRefusal{refusedBusy, time.Second}
	}
	full := func() bool {
		return (c == nil && len(l.clients) >= l.entries) || (u == nil && len(l.users) >= l.entries)
	}
	if full() {
		l.prune(now)
		if full() {
			return nil, &loginRefusal{refusedFailures, time.Minute}
		}
	}

	if c == nil {
		c = &clientLogins{}
		l.clients[client] = c
	}
	if u == nil {
		u = &userLogins{}
		l.users[user] = u
	}
	c.busy = true
	u.tokens, u.at = tokens-1, now
	l.active++
	return &loginTicket{limiter: l, client: client, user: user}, nil
}

// prune forgets the addresses and the usernames whose limits are over.
func (l *loginLimiter) prune(now time.Time) {
	for key, c := range l.clients {
		if !c.busy && !now.Before(c.until) && (c.failures == 0 || now.Sub(c.last) >= loginForget) {
			delete(l.clients, key)
		}
	}
	for key, u := range l.users {
		if u.available(now) >= userBurst {
			delete(l.users, key)
		}
	}
}

// finish ends the login of t, with outcome at now, once: a failure delays
// the next login of its address by failDelay at least, a success refills the
// tokens of its username, a login unchecked gives its token back. It returns
// the failures in a row of the address and how long its next login waits.
func (t *loginTicket) finish(outcome loginOutcome, now time.Time, failDelay time.Duration) (int, time.Duration) {
	l := t.limiter
	l.mu.Lock()
	defer l.mu.Unlock()
	if t.done {
		return 0, 0
	}
	t.done = true
	l.active--

	c := l.clients[t.client] // kept while busy
	var wait time.Duration
	switch outcome {
	case loginFailed:
		c.failures++
		c.last = now
		wait = max(failDelay, backoff(c.failures))
		c.until = now.Add(wait)
	case loginSucceeded:
		delete(l.users, t.user)
	case loginAborted:
		if u := l.users[t.user]; u != nil {
			u.tokens = min(userBurst, u.tokens+1)
		}
	}
	c.busy = false
	if c.failures == 0 {
		delete(l.clients, t.client)
	}
	return c.failures, wait
}

// backoff is how long the next login of an address waits after failures in
// a row, failDelay aside.
func backoff(failures int) time.Duration {
	if failures < clientFreeFailures {
		return 0
	}
	wait := clientBackoff
	for i := clientFreeFailures; i < failures && wait < clientMaxBackoff; i++ {
		wait *= 2
	}
	return min(wait, clientMaxBackoff)
}

// clientKey is the key of the limits of the client at addr, see
// auth.clientAddress: an IPv6 address by its /64, which a client usually has
// whole.
func clientKey(addr string) string {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return addr
	}
	if ip = ip.Unmap(); ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().String()
	}
	return ip.String()
}

// usernameKey is the key of the limits of username, the one the store finds
// it by.
func usernameKey(username string) string {
	key := strings.ToLower(strings.TrimSpace(username))
	if len(key) > maxUsernameKey {
		key = key[:maxUsernameKey]
	}
	return key
}
