package controller

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/store"
)

// adminRole is the fixed role of the administrators, as the tests read it.
func adminRole() *store.Role {
	return &store.Role{ID: 1, Name: "Administrador", System: store.RoleAdmin}
}

// adminCtx returns ctx of a user of the fixed role: every permission.
func adminCtx(ctx context.Context) context.Context {
	return withAccess(ctx, newAccess(adminRole()))
}

// asAdmin serves next to a user of the fixed role, as auth.protect would: the
// routes of the dashboard served without a session.
func asAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(adminCtx(r.Context())))
	})
}

// createAdmin creates a user of the fixed role in st.
func createAdmin(t *testing.T, st *store.Store, username, password string) store.User {
	t.Helper()
	ctx := context.Background()
	user, err := st.CreateUser(ctx, username, password)
	require.NoError(t, err)
	admin, err := st.AdminRole(ctx)
	require.NoError(t, err)
	require.NoError(t, st.SetUserRole(ctx, user.ID, admin.ID, "tests"))
	return user
}
