package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The accounts of MT5: created with their secrets encrypted, sealed for their
// place, never in their events; kept on a change leaving them out, removed
// with ""; refused out of their rules, and twice the same login on a server.
func TestMT5Accounts_Secrets(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	by := MT5Change{By: "admin", Reason: "a conta real"}
	login, name, server, bridge := int64(50607080), "Real", "Exness-MT5Real11", "http://mt5:18812"
	account, err := s.CreateMT5Account(ctx, MT5AccountInput{Name: &name, Login: &login, Server: &server,
		BridgeURL: &bridge, AllowReal: ptrBool(true), Password: secret("s3cret!"), BridgeToken: secret("tok")},
		true, MT5KindReal, by)
	require.NoError(t, err)
	assert.Equal(t, "mt5:18812", account.BridgeKey)
	assert.NotContains(t, string(account.Password), "s3cret!", "encrypted")
	password, token, err := s.MT5AccountSecrets(account)
	require.NoError(t, err)
	assert.Equal(t, [2]string{"s3cret!", "tok"}, [2]string{password, token})

	// another account's password copied here doesn't open
	var other MT5Account
	require.NoError(t, s.db.Take(&other, account.ID).Error)
	other.ID = account.ID + 1
	_, _, err = s.MT5AccountSecrets(other)
	assert.Error(t, err, "sealed for its place")

	// kept, then replaced, then removed
	renamed := "Conta real"
	_, next, err := s.UpdateMT5Account(ctx, account.ID, MT5AccountInput{Name: &renamed}, by)
	require.NoError(t, err)
	password, token, err = s.MT5AccountSecrets(next)
	require.NoError(t, err)
	assert.Equal(t, [2]string{"s3cret!", "tok"}, [2]string{password, token}, "kept")
	_, next, err = s.UpdateMT5Account(ctx, account.ID, MT5AccountInput{Password: secret("n3w"),
		BridgeToken: secret("")}, by)
	require.NoError(t, err)
	password, token, err = s.MT5AccountSecrets(next)
	require.NoError(t, err)
	assert.Equal(t, [2]string{"n3w", ""}, [2]string{password, token})

	events, err := s.MT5AccountEvents(ctx, account.ID, 10)
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, []string{MT5Changed, MT5Changed, MT5Created}, []string{events[0].Kind, events[1].Kind,
		events[2].Kind})
	for _, e := range events {
		for _, text := range []string{"s3cret!", "n3w", "tok\""} {
			assert.NotContains(t, string(e.Previous)+string(e.Next), text, "never a secret in the record")
		}
	}
	assert.Contains(t, string(events[0].Next), `"password_set": true`)
	assert.Contains(t, string(events[0].Next), `"token_set": false`)

	for _, refused := range []MT5AccountInput{
		{Name: secret(""), Login: &login, Server: &server, BridgeURL: &bridge},
		{Name: secret("x"), Login: ptrInt64(0), Server: &server, BridgeURL: &bridge},
		{Name: secret("x"), Login: ptrInt64(5), Server: secret(" "), BridgeURL: &bridge},
		{Name: secret("x"), Login: ptrInt64(5), Server: &server, BridgeURL: secret("http://u:p@mt5:18812")},
		{Name: secret("x"), Login: ptrInt64(5), Server: &server, BridgeURL: secret("mt5:18812")},
	} {
		_, err := s.CreateMT5Account(ctx, refused, false, "", by)
		assert.ErrorIs(t, err, ErrInvalid)
	}
	_, err = s.CreateMT5Account(ctx, MT5AccountInput{Name: secret("dup"), Login: &login,
		Server: secret("exness-mt5real11"), BridgeURL: &bridge}, false, "", by)
	assert.ErrorIs(t, err, ErrConflict, "the same login on the same server")
	found, ok, err := s.MT5AccountByLogin(ctx, login, " EXNESS-MT5Real11 ")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, account.ID, found.ID)
}

// A terminal serves one account at a time: activating another deactivates the
// former; an active account or one with agents isn't deleted.
func TestMT5Accounts_ActiveAndLinks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	by := MT5Change{By: "admin"}
	bridge := "http://mt5:18812"
	create := func(name string, login int64, active bool) MT5Account {
		t.Helper()
		a, err := s.CreateMT5Account(ctx, MT5AccountInput{Name: &name, Login: &login, Server: secret("Exness"),
			BridgeURL: &bridge, BridgeToken: secret("tok-" + name)}, active, "", by)
		require.NoError(t, err)
		return a
	}
	real := create("real", 1, true)
	demo := create("demo", 2, false)
	_, err := s.CreateMT5Account(ctx, MT5AccountInput{Name: secret("other"), Login: ptrInt64(3),
		Server: secret("Exness"), BridgeURL: secret("http://MT5:18812/")}, true, "", by)
	assert.ErrorIs(t, err, ErrConflict, "one active account a terminal")

	deactivated, err := s.ActivateMT5Account(ctx, demo.ID, true, by)
	require.NoError(t, err)
	require.Len(t, deactivated, 1)
	assert.Equal(t, real.ID, deactivated[0].ID)
	got, err := s.MT5Account(ctx, real.ID)
	require.NoError(t, err)
	assert.False(t, got.Active)

	// an agent linked: its bridge the account's, its token the account's
	trend, err := s.CreateAgent(ctx, Agent{Name: "trend", Kind: "trend", Mode: "live", APIAddress: "localhost:5570",
		Launched: true, Params: Params{"strategy": "matrend"},
		Account: &Account{BridgeURL: "http://127.0.0.1:18812"}}, AgentSecrets{BridgeToken: secret("agent-token")})
	require.NoError(t, err)
	require.NoError(t, s.SetAgentMT5Account(ctx, trend.ID, &demo.ID, by))
	trend, err = s.Agent(ctx, trend.ID)
	require.NoError(t, err)
	require.NotNil(t, trend.MT5AccountOf())
	assert.Equal(t, demo.ID, trend.MT5AccountOf().ID)
	assert.Equal(t, bridge, trend.Account.BridgeURL, "the bridge of the account")
	secrets, err := s.SecretsOf(trend)
	require.NoError(t, err)
	assert.Equal(t, "tok-demo", secrets.BridgeToken, "the token of the account, not the agent's")
	agents, err := s.MT5AccountAgents(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"trend"}, agents[demo.ID])

	// the bridge of the account moved: its agents with it
	moved := "http://mt5-2:18812"
	_, _, err = s.UpdateMT5Account(ctx, demo.ID, MT5AccountInput{BridgeURL: &moved}, by)
	require.NoError(t, err)
	trend, err = s.Agent(ctx, trend.ID)
	require.NoError(t, err)
	assert.Equal(t, moved, trend.Account.BridgeURL)

	assert.ErrorIs(t, s.DeleteMT5Account(ctx, demo.ID, by), ErrConflict, "active")
	_, err = s.ActivateMT5Account(ctx, demo.ID, false, by)
	require.NoError(t, err)
	assert.ErrorIs(t, s.DeleteMT5Account(ctx, demo.ID, by), ErrConflict, "with an agent")
	require.NoError(t, s.SetAgentMT5Account(ctx, trend.ID, nil, by))
	trend, err = s.Agent(ctx, trend.ID)
	require.NoError(t, err)
	assert.Nil(t, trend.MT5AccountOf())
	secrets, err = s.SecretsOf(trend)
	require.NoError(t, err)
	assert.Equal(t, "agent-token", secrets.BridgeToken, "its own again, unlinked")
	require.NoError(t, s.DeleteMT5Account(ctx, demo.ID, by))
	_, err = s.MT5Account(ctx, demo.ID)
	assert.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, s.SetMT5AccountCheck(ctx, real.ID, MT5KindReal, "BRL", "Exness", "", time.Now(), by))
	got, err = s.MT5Account(ctx, real.ID)
	require.NoError(t, err)
	assert.Equal(t, [3]string{"real", "BRL", "Exness"}, [3]string{got.Kind, got.Currency, got.Company})
	require.NotNil(t, got.CheckedAt)
}

func ptrBool(v bool) *bool    { return &v }
func ptrInt64(v int64) *int64 { return &v }
