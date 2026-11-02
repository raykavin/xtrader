package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestCreateUser_HashesThePasswordWithBcrypt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	user, err := s.CreateUser(ctx, "xtrader", "a strong password")
	require.NoError(t, err)
	require.True(t, user.Enabled)

	var hash string
	require.NoError(t, s.db.Raw("SELECT password_hash FROM users WHERE id = ?", user.ID).Scan(&hash).Error)
	require.NotContains(t, hash, "a strong password", "never in plain text")
	cost, err := bcrypt.Cost([]byte(hash))
	require.NoError(t, err)
	require.Equal(t, bcryptCost, cost)

	for _, tc := range []struct{ name, username, password, want string }{
		{"taken, whatever the case", "XTRADER", "another password", "o usuário XTRADER já existe"},
		{"invalid username", "no spaces", "a strong password", "nome de usuário"},
		{"short password", "other", "short", "ao menos 8 caracteres"},
		{"long password", "other", strings.Repeat("x", 73), "até 72 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateUser(ctx, tc.username, tc.password)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestAuthenticate_RefusesWithoutTellingWhy(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user, err := s.CreateUser(ctx, "xtrader", "a strong password")
	require.NoError(t, err)
	other, err := s.CreateUser(ctx, "other", "another password")
	require.NoError(t, err)

	logged, err := s.Authenticate(ctx, " XTrader ", "a strong password")
	require.NoError(t, err, "the username, whatever the case and the spaces")
	require.Equal(t, user.ID, logged.ID)
	require.NotNil(t, logged.LastLoginAt)

	_, err = s.Authenticate(ctx, "xtrader", "a wrong password")
	require.ErrorIs(t, err, ErrCredentials)
	_, err = s.Authenticate(ctx, "nobody", "a strong password")
	require.ErrorIs(t, err, ErrCredentials)

	require.NoError(t, s.SetUserEnabled(ctx, other.ID, false))
	_, err = s.Authenticate(ctx, "other", "another password")
	require.ErrorIs(t, err, ErrCredentials, "disabled")

	require.NoError(t, s.SetPassword(ctx, user.ID, "a new password"))
	_, err = s.Authenticate(ctx, "xtrader", "a strong password")
	require.ErrorIs(t, err, ErrCredentials)
	_, err = s.Authenticate(ctx, "xtrader", "a new password")
	require.NoError(t, err)
}

func TestUsers_KeepOneEnabled(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	first, err := s.CreateUser(ctx, "first", "password-1")
	require.NoError(t, err)

	require.ErrorIs(t, s.SetUserEnabled(ctx, first.ID, false), ErrInvalid, "the last one enabled")
	require.ErrorIs(t, s.DeleteUser(ctx, first.ID), ErrInvalid)

	second, err := s.CreateUser(ctx, "second", "password-2")
	require.NoError(t, err)
	require.NoError(t, s.SetUserEnabled(ctx, first.ID, false))
	require.ErrorIs(t, s.DeleteUser(ctx, second.ID), ErrInvalid, "now the last one enabled")
	require.NoError(t, s.DeleteUser(ctx, first.ID))
	require.ErrorIs(t, s.DeleteUser(ctx, first.ID), ErrNotFound)

	users, err := s.Users(ctx)
	require.NoError(t, err)
	require.Len(t, users, 1)
	_, err = s.CreateUser(ctx, "first", "password-1")
	require.NoError(t, err, "the name of a deleted user is free again")
	require.ErrorIs(t, s.SetPassword(ctx, 999999, "password-3"), ErrNotFound)
}

func TestBootstrap_OnlyOnAnEmptyDatabase(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	created, err := s.Bootstrap(ctx, "admin", "first password")
	require.NoError(t, err)
	require.True(t, created)
	created, err = s.Bootstrap(ctx, "admin2", "second password")
	require.NoError(t, err)
	require.False(t, created, "a user exists")
	_, err = s.Authenticate(ctx, "admin", "first password")
	require.NoError(t, err)
}
