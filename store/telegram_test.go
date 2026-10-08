package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetTelegram_ConcurrentChangesKeepBoth(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	on, off := true, false
	for round := range 30 {
		require.NoError(t, s.SetTelegram(ctx, &off, secret("123:old")))
		token := fmt.Sprintf("123:new-%d", round)
		start := make(chan struct{})
		errs := make(chan error, 2)
		go func() { <-start; errs <- s.SetTelegram(ctx, &on, nil) }()    // the switch alone
		go func() { <-start; errs <- s.SetTelegram(ctx, nil, &token) }() // the token alone
		close(start)
		require.NoError(t, <-errs)
		require.NoError(t, <-errs)

		settings, _, err := s.Telegram(ctx)
		require.NoError(t, err)
		require.True(t, settings.Enabled, "round %d: the switch kept", round)
		got, err := s.TelegramToken(ctx)
		require.NoError(t, err)
		require.Equal(t, token, got, "round %d: the token kept", round)
	}
}

func TestSetTelegram_WithoutTheRow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.db.Exec("DELETE FROM telegram_settings").Error)
	require.NoError(t, s.SetTelegram(ctx, nil, secret("123:token")))
	settings, hasToken, err := s.Telegram(ctx)
	require.NoError(t, err)
	require.False(t, settings.Enabled, "the default")
	require.True(t, hasToken)

	on := true
	require.NoError(t, s.SetTelegram(ctx, &on, nil))
	token, err := s.TelegramToken(ctx)
	require.NoError(t, err)
	require.Equal(t, "123:token", token)
	settings, _, err = s.Telegram(ctx)
	require.NoError(t, err)
	require.True(t, settings.Enabled)
}
