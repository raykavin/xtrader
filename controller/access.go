package controller

import (
	"context"
	"net/http"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The access of the users of the dashboard: each route of its API declares
// the permissions it needs (need, needByMethod), any one of them enough, and a
// request whose user holds none of them is refused, 403, before its handler;
// the permissions are the ones of the role of the user, the implied ones
// included (see ui.Effective), kept by its session and read again with its
// user (see auth.session). A request without a session never reaches a route.

// errForbidden is the error of a request refused for the permissions of its
// user.
const errForbidden = "Você não tem permissão para esta ação."

// errNoRole is the error of a login refused for a user without a role.
const errNoRole = "Seu usuário não tem um papel atribuído: peça acesso a um administrador."

// access is what the user of a session may see and do: its role and the
// permissions of it, the implied ones included. Never changed once made: a
// session takes another one when its role changes.
type access struct {
	roleID int64
	role   string
	perms  []string
	set    map[string]bool
}

// newAccess returns the access of role, nil for none.
func newAccess(role *store.Role) *access {
	if role == nil {
		return nil
	}
	perms := role.Effective()
	set := make(map[string]bool, len(perms))
	for _, p := range perms {
		set[p] = true
	}
	return &access{roleID: role.ID, role: role.Name, perms: perms, set: set}
}

// can reports whether a grants any of perms: never without an access.
func (a *access) can(perms ...string) bool {
	if a == nil {
		return false
	}
	for _, p := range perms {
		if a.set[p] {
			return true
		}
	}
	return false
}

// accessKey is the key of the access of the user of the request in its
// context.
type accessKey struct{}

// withAccess returns ctx carrying the access a, see accessOf.
func withAccess(ctx context.Context, a *access) context.Context {
	return context.WithValue(ctx, accessKey{}, a)
}

// accessOf returns the access of the user of the request, nil without one:
// nothing allowed.
func accessOf(ctx context.Context) *access {
	a, _ := ctx.Value(accessKey{}).(*access)
	return a
}

// need refuses (403) the requests whose user holds none of perms.
func need(perms ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !accessOf(r.Context()).can(perms...) {
				writeJSONError(w, http.StatusForbidden, errForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// needByMethod is need of read for the requests that change nothing (see
// safeMethod), of change for the other ones: a route serving both, eg: the
// proxy to the API of an agent.
func needByMethod(read, change []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			perms := change
			if safeMethod(r.Method) {
				perms = read
			}
			if !accessOf(r.Context()).can(perms...) {
				writeJSONError(w, http.StatusForbidden, errForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// toolPermissions are the permissions the tools of the chat of XTrader AI
// acting at once need of the user asking: the other ones read, or propose
// what a user allowed to operate confirms.
var toolPermissions = map[string]string{
	"execute_action":   ui.PermTradingOperate,
	"protect_position": ui.PermTradingOperate,
	"set_daily_limits": ui.PermLimitsManage,
}

// mayCall reports whether the user of a may have the chat call the tool of
// name.
func (a *access) mayCall(name string) bool {
	p, acts := toolPermissions[name]
	return !acts || a.can(p)
}
