package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// legacyToken sets the token of the calendar the agent a kept, encrypted for
// its former place, as the versions before the news did; "garbage" for one
// that can't be opened.
func legacyToken(t *testing.T, s *Store, a Agent, token string) {
	t.Helper()
	sealed := []byte(token)
	if token != "garbage" {
		var err error
		sealed, err = s.seal(legacyCalendarTokenOf(a.ID), token)
		require.NoError(t, err)
	}
	require.NoError(t, s.db.Exec("UPDATE agent_llm_settings SET calendar_token = ? WHERE agent_id = ?", sealed,
		a.ID).Error)
}

// column reports whether agent_llm_settings still has the former tokens.
func column(t *testing.T, s *Store) bool {
	t.Helper()
	found, err := s.legacyCalendarTokens(context.Background())
	require.NoError(t, err)
	return found
}

// The news of the agents: off and without a token from the seeds; the token
// encrypted at rest for its place, kept when a change leaves it out, removed
// by an empty one; who changed them last.
func TestNews_SettingsAndToken(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	settings, found, err := s.News(ctx)
	require.NoError(t, err)
	require.True(t, found, "seeded")
	require.False(t, settings.Enabled)
	require.Empty(t, settings.MCPToken)
	token, err := s.NewsToken(ctx)
	require.NoError(t, err)
	require.Empty(t, token)

	on := true
	settings, err = s.SetNews(ctx, NewsUpdate{Enabled: &on, Token: secret(" mcp-token "), By: "admin"})
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, "admin", settings.UpdatedBy)
	require.NotContains(t, string(sealedSecret(t, s, "news_settings", "mcp_token", "id", 1)), "mcp-token",
		"encrypted at rest")
	require.Equal(t, "\x00xtrader-secret-v1\x00news_settings:mcp_token:1\x00mcp-token",
		string(rawSecret(t, s, "news_settings", "mcp_token", "id", 1)), "sealed for its place, trimmed")

	off := false
	_, err = s.SetNews(ctx, NewsUpdate{Enabled: &off, By: "other"})
	require.NoError(t, err)
	token, err = s.NewsToken(ctx)
	require.NoError(t, err)
	require.Equal(t, "mcp-token", token, "kept when left out")

	_, err = s.SetNews(ctx, NewsUpdate{Token: secret("mcp-new"), By: "admin"})
	require.NoError(t, err)
	token, err = s.NewsToken(ctx)
	require.NoError(t, err)
	require.Equal(t, "mcp-new", token, "replaced")

	_, err = s.SetNews(ctx, NewsUpdate{Token: secret(""), By: "admin"})
	require.NoError(t, err)
	settings, _, err = s.News(ctx)
	require.NoError(t, err)
	require.Empty(t, settings.MCPToken, "removed")
	require.False(t, settings.Enabled, "the switch left as it was")
}

// The tokens of the calendar the agents kept each: all the same, the token of
// the news, the news on, their column dropped; then nothing to do.
func TestNews_MigratesTheSameTokenOfTheAgents(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	first, second := llmAgent(t, s, "first"), llmAgent(t, s, "second")
	legacyToken(t, s, first, "mcp-shared")
	legacyToken(t, s, second, "mcp-shared")
	llmAgent(t, s, "third") // without one

	result, err := s.MigrateNewsToken(ctx)
	require.NoError(t, err)
	require.Equal(t, NewsMigration{Tokens: 2, Taken: true}, result)
	settings, _, err := s.News(ctx)
	require.NoError(t, err)
	require.True(t, settings.Enabled, "the news on, as the agents read their calendar")
	require.Equal(t, "migração", settings.UpdatedBy)
	token, err := s.NewsToken(ctx)
	require.NoError(t, err)
	require.Equal(t, "mcp-shared", token)
	require.False(t, column(t, s), "the former column dropped")
	conflict, err := s.NewsConflict(ctx)
	require.NoError(t, err)
	require.Empty(t, conflict)

	result, err = s.MigrateNewsToken(ctx)
	require.NoError(t, err)
	require.Equal(t, NewsMigration{}, result, "run again harmlessly")
}

// None held a token: the column dropped, the news as they were.
func TestNews_DropsTheColumnWithoutAToken(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	llmAgent(t, s, "llm")
	result, err := s.MigrateNewsToken(ctx)
	require.NoError(t, err)
	require.Equal(t, NewsMigration{}, result)
	require.False(t, column(t, s))
	settings, _, err := s.News(ctx)
	require.NoError(t, err)
	require.False(t, settings.Enabled)
}

// Different tokens, or one that can't be opened: none chosen, the token of the
// news left unset and the news off, the column kept as it was and told until
// a token is set on the dashboard; the next run of the migrator drops it.
func TestNews_TellsTheConflictOfTheTokensOfTheAgents(t *testing.T) {
	for _, c := range []struct {
		name   string
		tokens []string
	}{
		{"different tokens", []string{"mcp-one", "mcp-two", "mcp-one"}},
		{"one that can't be opened", []string{"mcp-one", "garbage", "mcp-one"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			for i, name := range []string{"beta", "alpha", "gamma"} {
				legacyToken(t, s, llmAgent(t, s, name), c.tokens[i])
			}

			result, err := s.MigrateNewsToken(ctx)
			require.NoError(t, err)
			require.Equal(t, NewsMigration{Tokens: 3, Conflict: []string{"alpha", "beta", "gamma"}}, result)
			settings, _, err := s.News(ctx)
			require.NoError(t, err)
			require.False(t, settings.Enabled)
			require.Empty(t, settings.MCPToken, "none chosen")
			require.True(t, column(t, s), "the tokens kept as they were")
			conflict, err := s.NewsConflict(ctx)
			require.NoError(t, err)
			require.Equal(t, []string{"alpha", "beta", "gamma"}, conflict)

			result, err = s.MigrateNewsToken(ctx)
			require.NoError(t, err)
			require.Len(t, result.Conflict, 3, "still told")

			_, err = s.SetNews(ctx, NewsUpdate{Token: secret("mcp-chosen"), By: "admin"})
			require.NoError(t, err)
			conflict, err = s.NewsConflict(ctx)
			require.NoError(t, err)
			require.Empty(t, conflict, "ended by the token set")
			require.True(t, column(t, s), "never dropped by the controller")

			result, err = s.MigrateNewsToken(ctx)
			require.NoError(t, err)
			require.Equal(t, NewsMigration{Tokens: 3}, result, "the token of the news kept")
			require.False(t, column(t, s), "dropped by the next run of the migrator")
			token, err := s.NewsToken(ctx)
			require.NoError(t, err)
			require.Equal(t, "mcp-chosen", token)
		})
	}
}
