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

// The rules of the news: the server and the window of the seeds; changed
// with the news, checked, who changed them told.
func TestNews_Rules(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	rules, found, err := s.NewsRules(ctx)
	require.NoError(t, err)
	require.True(t, found, "seeded")
	require.Equal(t, "http://mt5:22347/mcp", *rules.MCPURL)
	require.Equal(t, [3]any{15, 15, "high"}, [3]any{rules.BlockBefore, rules.BlockAfter, rules.BlockImportance})
	require.Empty(t, rules.UpdatedBy)

	before, after, importance := 30, 5, "medium"
	_, err = s.SetNews(ctx, NewsUpdate{URL: secret(" http://127.0.0.1:22346/mcp "), BlockBefore: &before,
		BlockAfter: &after, BlockImportance: &importance, By: "admin"})
	require.NoError(t, err)
	rules, _, err = s.NewsRules(ctx)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:22346/mcp", *rules.MCPURL)
	require.Equal(t, [3]any{30, 5, "medium"}, [3]any{rules.BlockBefore, rules.BlockAfter, rules.BlockImportance})
	require.Equal(t, "admin", rules.UpdatedBy)

	tooLong, negative, urgent := 1441, -1, "urgent"
	for _, refused := range []NewsUpdate{
		{BlockBefore: &tooLong}, {BlockAfter: &negative}, {BlockImportance: &urgent},
		{URL: secret("ftp://127.0.0.1/mcp")}, {URL: secret("http://u:p@127.0.0.1/mcp")},
	} {
		_, err := s.SetNews(ctx, refused)
		require.ErrorIs(t, err, ErrInvalid)
	}
	rules, _, err = s.NewsRules(ctx)
	require.NoError(t, err)
	require.Equal(t, 30, rules.BlockBefore, "nothing changed")

	_, err = s.SetNews(ctx, NewsUpdate{URL: secret(""), By: "admin"})
	require.NoError(t, err)
	rules, _, err = s.NewsRules(ctx)
	require.NoError(t, err)
	require.Nil(t, rules.MCPURL, "removed")
}

// legacyURL sets the server of the calendar the agent a had, and its params.
func legacyURL(t *testing.T, s *Store, a Agent, url string, params string) {
	t.Helper()
	require.NoError(t, s.db.Exec("UPDATE agent_llm_settings SET calendar_url = ?, params = params || ?::jsonb "+
		"WHERE agent_id = ?", url, params, a.ID).Error)
}

// The servers of the calendar the LLM agents had: all the same, the server of
// the news, their column dropped; their windows of the news block removed.
func TestNews_MigratesTheServerOfTheAgents(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	first, second := llmAgent(t, s, "first"), llmAgent(t, s, "second")
	legacyURL(t, s, first, "http://127.0.0.1:22346/mcp", `{"news-block-before": "30m", "calendar-ahead": "4h"}`)
	legacyURL(t, s, second, " http://127.0.0.1:22346/mcp ", `{"news-block-importance": "medium"}`)
	llmAgent(t, s, "third") // without one

	result, err := s.MigrateNewsURL(ctx)
	require.NoError(t, err)
	require.Equal(t, NewsURLMigration{URLs: 2, Taken: "http://127.0.0.1:22346/mcp", Params: 2}, result)
	rules, _, err := s.NewsRules(ctx)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:22346/mcp", *rules.MCPURL)
	found, err := s.legacyCalendarURLs(ctx)
	require.NoError(t, err)
	require.False(t, found, "the former column dropped")
	var params []string
	require.NoError(t, s.db.Raw("SELECT params::text FROM agent_llm_settings ORDER BY agent_id").Scan(&params).Error)
	for _, p := range params {
		require.NotContains(t, p, "news-block", "the windows of the agents removed")
	}
	require.Contains(t, params[0], "calendar-ahead", "the window of the context kept")

	result, err = s.MigrateNewsURL(ctx)
	require.NoError(t, err)
	require.Equal(t, NewsURLMigration{}, result, "run again harmlessly")
}

// Different servers: none chosen, the server of the news as it was, the column
// kept and told until the server is set on the dashboard; the next run drops
// it then. A server set on the dashboard is never replaced.
func TestNews_TellsTheConflictOfTheServersOfTheAgents(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	alpha, beta := llmAgent(t, s, "alpha"), llmAgent(t, s, "beta")
	legacyURL(t, s, alpha, "http://127.0.0.1:22346/mcp", "{}")
	legacyURL(t, s, beta, "http://127.0.0.1:22347/mcp", "{}")

	result, err := s.MigrateNewsURL(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "beta"}, result.Conflict)
	require.Empty(t, result.Taken)
	rules, _, err := s.NewsRules(ctx)
	require.NoError(t, err)
	require.Equal(t, "http://mt5:22347/mcp", *rules.MCPURL, "the one of the seeds, none chosen")
	conflict, err := s.NewsURLConflict(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "beta"}, conflict)

	_, err = s.SetNews(ctx, NewsUpdate{URL: secret("http://127.0.0.1:22347/mcp"), By: "admin"})
	require.NoError(t, err)
	conflict, err = s.NewsURLConflict(ctx)
	require.NoError(t, err)
	require.Empty(t, conflict, "ended by the server set")
	result, err = s.MigrateNewsURL(ctx)
	require.NoError(t, err)
	require.Empty(t, result.Conflict)
	rules, _, err = s.NewsRules(ctx)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:22347/mcp", *rules.MCPURL, "the one of the dashboard kept")
	found, err := s.legacyCalendarURLs(ctx)
	require.NoError(t, err)
	require.False(t, found, "dropped by the next run")
}
