package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// The roles: created with the permissions of the catalog only, a name once;
// changed and deleted, but not the fixed one of the seeds, nor one held.
func TestRoles_CreateChangeAndDelete(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	admin, err := s.AdminRole(ctx)
	require.NoError(t, err, "seeded")
	assert.True(t, admin.Fixed())
	assert.Equal(t, "Administrador", admin.Name)
	assert.Equal(t, ui.AllPermissions(), admin.Granted(), "every permission, none kept")
	assert.Empty(t, admin.Permissions)

	viewer, err := s.CreateRole(ctx, RoleChange{Name: ptr("  Observador "), Description: ptr("Só olha."),
		Permissions: &[]string{ui.PermPerformanceView, ui.PermAgentsManage, ui.PermPerformanceView}, By: "tester"})
	require.NoError(t, err)
	assert.Equal(t, "Observador", viewer.Name)
	assert.Equal(t, "Só olha.", viewer.Description)
	assert.Equal(t, "tester", viewer.UpdatedBy)
	assert.Equal(t, []string{ui.PermPerformanceView, ui.PermAgentsManage}, viewer.Granted(), "once each")
	assert.Equal(t, []string{ui.PermPerformanceView, ui.PermAgentsView, ui.PermAgentsManage}, viewer.Effective())

	_, err = s.CreateRole(ctx, RoleChange{Name: ptr("observador")})
	require.ErrorIs(t, err, ErrConflict, "a name once, whatever its case")
	_, err = s.CreateRole(ctx, RoleChange{Name: ptr("Outro"), Permissions: &[]string{"agents:delete"}})
	require.ErrorIs(t, err, ErrInvalid, "a permission of the catalog only")
	_, err = s.CreateRole(ctx, RoleChange{Name: ptr(" ")})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = s.CreateRole(ctx, RoleChange{Name: ptr(strings.Repeat("é", 65))})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = s.CreateRole(ctx, RoleChange{})
	require.ErrorIs(t, err, ErrInvalid)

	changed, err := s.UpdateRole(ctx, viewer.ID, RoleChange{Name: ptr("Leitura"),
		Permissions: &[]string{ui.PermDashboardView}, By: "other"})
	require.NoError(t, err)
	assert.Equal(t, "Leitura", changed.Name)
	assert.Equal(t, "Só olha.", changed.Description, "kept")
	assert.Equal(t, []string{ui.PermDashboardView}, changed.Granted())
	assert.Equal(t, "other", changed.UpdatedBy)
	empty, err := s.UpdateRole(ctx, viewer.ID, RoleChange{Permissions: &[]string{}})
	require.NoError(t, err)
	assert.Empty(t, empty.Granted(), "a role may grant nothing")

	_, err = s.UpdateRole(ctx, admin.ID, RoleChange{Name: ptr("Chefe")})
	require.ErrorIs(t, err, ErrInvalid, "the fixed role")
	require.ErrorIs(t, s.DeleteRole(ctx, admin.ID), ErrInvalid)
	_, err = s.UpdateRole(ctx, 999999, RoleChange{Name: ptr("X")})
	require.ErrorIs(t, err, ErrNotFound)

	_, err = s.CreateUser(ctx, "jose", "password-2")
	require.NoError(t, err)
	user, err := s.CreateUser(ctx, "maria", "password-1")
	require.NoError(t, err)
	require.NoError(t, s.SetUserRole(ctx, user.ID, viewer.ID, "tester"))
	require.ErrorIs(t, s.DeleteRole(ctx, viewer.ID), ErrConflict, "held")
	roles, err := s.Roles(ctx)
	require.NoError(t, err)
	require.Len(t, roles, 2)
	assert.True(t, roles[0].Fixed(), "the fixed one first")
	assert.Equal(t, int64(1), roles[1].Users)

	require.NoError(t, s.DeleteUser(ctx, user.ID))
	require.NoError(t, s.DeleteRole(ctx, viewer.ID), "its user deleted")
	_, err = s.Role(ctx, viewer.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

// The role of a user, read with it (at the login too); taken away, the user
// has none.
func TestUsers_TheirRoles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	trader, err := s.CreateRole(ctx, RoleChange{Name: ptr("Operador"),
		Permissions: &[]string{ui.PermTradingOperate}})
	require.NoError(t, err)
	user, err := s.CreateUser(ctx, "joao", "password-1")
	require.NoError(t, err)

	got, err := s.User(ctx, user.ID)
	require.NoError(t, err)
	assert.Nil(t, got.RoleOf(), "none")

	require.NoError(t, s.SetUserRole(ctx, user.ID, trader.ID, "tester"))
	logged, err := s.Authenticate(ctx, "joao", "password-1")
	require.NoError(t, err)
	require.NotNil(t, logged.RoleOf())
	assert.Equal(t, "Operador", logged.RoleOf().Name)
	assert.Equal(t, []string{ui.PermDashboardView, ui.PermTradingOperate}, logged.RoleOf().Effective())
	users, err := s.Users(ctx)
	require.NoError(t, err)
	require.NotNil(t, users[0].RoleOf())
	assert.Equal(t, trader.ID, users[0].RoleOf().ID)

	require.NoError(t, s.SetUserRole(ctx, user.ID, 0, "tester"))
	got, err = s.User(ctx, user.ID)
	require.NoError(t, err)
	assert.Nil(t, got.RoleOf(), "taken away")
	require.ErrorIs(t, s.SetUserRole(ctx, user.ID, 999999, "tester"), ErrNotFound)
	require.ErrorIs(t, s.SetUserRole(ctx, 999999, trader.ID, "tester"), ErrNotFound)
}

// A user enabled able to manage the users and the roles is always kept: by the
// fixed role or one granting users:manage; never disabled, deleted, given
// another role nor its role losing the permission when it is the last one.
func TestRoles_KeepAnAdministrator(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	admin, err := s.AdminRole(ctx)
	require.NoError(t, err)
	viewer, err := s.CreateRole(ctx, RoleChange{Name: ptr("Observador"),
		Permissions: &[]string{ui.PermDashboardView}})
	require.NoError(t, err)
	first, err := s.CreateUser(ctx, "first", "password-1")
	require.NoError(t, err)
	second, err := s.CreateUser(ctx, "second", "password-2")
	require.NoError(t, err)
	require.NoError(t, s.SetUserRole(ctx, first.ID, admin.ID, "tester"))
	require.NoError(t, s.SetUserRole(ctx, second.ID, viewer.ID, "tester"))

	require.ErrorIs(t, s.SetUserRole(ctx, first.ID, viewer.ID, "tester"), ErrConflict, "the last administrator")
	require.ErrorIs(t, s.SetUserRole(ctx, first.ID, 0, "tester"), ErrConflict)
	require.ErrorIs(t, s.SetUserEnabled(ctx, first.ID, false), ErrConflict)
	require.ErrorIs(t, s.DeleteUser(ctx, first.ID), ErrConflict)

	// a role managing the users does
	managers, err := s.UpdateRole(ctx, viewer.ID, RoleChange{Permissions: &[]string{ui.PermUsersManage}})
	require.NoError(t, err)
	require.NoError(t, s.SetUserRole(ctx, first.ID, 0, "tester"), "the second one manages")
	_, err = s.UpdateRole(ctx, managers.ID, RoleChange{Permissions: &[]string{ui.PermUsersView}})
	require.ErrorIs(t, err, ErrConflict, "its last role managing the users")
	require.ErrorIs(t, s.SetUserEnabled(ctx, second.ID, false), ErrConflict)

	got, err := s.User(ctx, first.ID)
	require.NoError(t, err)
	assert.Nil(t, got.RoleOf())
	got, err = s.User(ctx, second.ID)
	require.NoError(t, err)
	assert.True(t, got.Enabled, "nothing changed by the refusals")
	assert.Equal(t, []string{ui.PermUsersManage}, got.RoleOf().Granted())
}

// The users of a database without roles get the fixed role once, the access
// they had kept; afterwards nothing is given.
func TestMigrateUserRoles_Once(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	assigned, err := s.MigrateUserRoles(ctx)
	require.NoError(t, err)
	assert.Zero(t, assigned, "no users")

	first, err := s.CreateUser(ctx, "first", "password-1")
	require.NoError(t, err)
	gone, err := s.CreateUser(ctx, "gone", "password-2")
	require.NoError(t, err)
	_, err = s.CreateUser(ctx, "second", "password-3")
	require.NoError(t, err)
	require.NoError(t, s.DeleteUser(ctx, gone.ID))

	assigned, err = s.MigrateUserRoles(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), assigned, "the users left")
	got, err := s.User(ctx, first.ID)
	require.NoError(t, err)
	require.NotNil(t, got.RoleOf())
	assert.True(t, got.RoleOf().Fixed())

	late, err := s.CreateUser(ctx, "late", "password-4")
	require.NoError(t, err)
	assigned, err = s.MigrateUserRoles(ctx)
	require.NoError(t, err)
	assert.Zero(t, assigned, "once")
	got, err = s.User(ctx, late.ID)
	require.NoError(t, err)
	assert.Nil(t, got.RoleOf(), "a new user gets none by itself")
}
