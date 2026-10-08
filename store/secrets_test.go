package store

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// rawSecret returns the text the secret of column of table, of the row of
// key, is encrypted in.
func rawSecret(t *testing.T, s *Store, table, column, key string, id int64) []byte {
	t.Helper()
	sealed := sealedSecret(t, s, table, column, key, id)
	text, err := s.cipher.Decrypt(sealed)
	require.NoError(t, err)
	return text
}

// sealedSecret returns the encrypted secret of column of table, of the row of
// key.
func sealedSecret(t *testing.T, s *Store, table, column, key string, id int64) []byte {
	t.Helper()
	var sealed []byte
	require.NoError(t, s.db.Table(table).Select(column).Where(key+" = ?", id).Row().Scan(&sealed))
	return sealed
}

// copySecret copies the encrypted secret of a column of a row to another one.
func copySecret(t *testing.T, s *Store, fromTable, fromColumn, fromKey string, fromID int64, toTable, toColumn,
	toKey string, toID int64) {
	t.Helper()
	sealed := sealedSecret(t, s, fromTable, fromColumn, fromKey, fromID)
	require.NotEmpty(t, sealed)
	require.NoError(t, s.db.Table(toTable).Where(toKey+" = ?", toID).Update(toColumn, sealed).Error)
}

// A secret is sealed for its place, its table, column and row: copied to
// another one, eg: the key of a model as the token of a bridge another
// server gets, it doesn't open.
func TestSecrets_SealedForTheirPlace(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	first := llmAgent(t, s, "first")
	second := llmAgent(t, s, "second")
	on := true
	require.NoError(t, s.SetTelegram(ctx, &on, secret("123:telegram-token")))

	values, err := s.AgentSecrets(ctx, second.ID)
	require.NoError(t, err)
	require.Equal(t, AgentSecretValues{LLMAPIKey: "sk-test-key", BridgeToken: "bridge-token",
		CalendarToken: "mcp-token"}, values)
	for _, c := range []struct {
		name                                  string
		fromTable, fromColumn, fromKey        string
		fromID                                int64
		toTable, toColumn, toKey, wantInError string
		toID                                  int64
	}{
		{"of another agent", "agent_llm_settings", "api_key", "agent_id", first.ID,
			"agent_llm_settings", "api_key", "agent_id", "agent_llm_settings:api_key", second.ID},
		{"of another column", "agent_llm_settings", "api_key", "agent_id", second.ID,
			"agent_llm_settings", "calendar_token", "agent_id", "agent_llm_settings:calendar_token", second.ID},
		{"of another table", "telegram_settings", "token", "id", 1,
			"agent_accounts", "bridge_token", "agent_id", "agent_accounts:bridge_token", second.ID},
	} {
		copySecret(t, s, c.fromTable, c.fromColumn, c.fromKey, c.fromID, c.toTable, c.toColumn, c.toKey, c.toID)
		_, err := s.AgentSecrets(ctx, second.ID)
		require.ErrorContains(t, err, "sealed for another place", c.name)
		require.ErrorContains(t, err, c.wantInError, c.name)
		require.NoError(t, s.SetAgentSecrets(ctx, second.ID, AgentSecrets{LLMAPIKey: secret("sk-test-key"),
			BridgeToken: secret("bridge-token"), CalendarToken: secret("mcp-token")}), "set again")
	}

	copySecret(t, s, "agent_accounts", "bridge_token", "agent_id", first.ID, "telegram_settings", "token", "id", 1)
	_, err = s.TelegramToken(ctx)
	require.ErrorContains(t, err, "sealed for another place")

	text := rawSecret(t, s, "agent_llm_settings", "api_key", "agent_id", second.ID)
	require.Equal(t, fmt.Sprintf("\x00xtrader-secret-v1\x00agent_llm_settings:api_key:%d\x00sk-test-key", second.ID),
		string(text), "the text encrypted")
}

// The secrets sealed alone, by a version before the places, open as they did,
// and are sealed for their place once written again; a secret of a later
// version is refused.
func TestSecrets_TheOnesSealedBeforeOpenAndAreSealedAgain(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	agent := llmAgent(t, s, "llm")
	alone := func(secret string) []byte {
		sealed, err := s.cipher.Encrypt([]byte(secret))
		require.NoError(t, err)
		return sealed
	}
	require.NoError(t, s.db.Table("agent_llm_settings").Where("agent_id = ?", agent.ID).
		Updates(map[string]interface{}{"api_key": alone("sk-old"), "calendar_token": alone("mcp-old")}).Error)
	require.NoError(t, s.db.Table("agent_accounts").Where("agent_id = ?", agent.ID).
		Update("bridge_token", alone("bridge-old")).Error)
	require.NoError(t, s.db.Table("telegram_settings").Where("id = 1").Update("token", alone("123:old")).Error)

	values, err := s.AgentSecrets(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, AgentSecretValues{LLMAPIKey: "sk-old", BridgeToken: "bridge-old", CalendarToken: "mcp-old"},
		values)
	token, err := s.TelegramToken(ctx)
	require.NoError(t, err)
	require.Equal(t, "123:old", token)

	require.NoError(t, s.SetAgentSecrets(ctx, agent.ID, AgentSecrets{LLMAPIKey: secret("sk-new")}))
	require.True(t, bytes.HasPrefix(rawSecret(t, s, "agent_llm_settings", "api_key", "agent_id", agent.ID),
		[]byte("\x00xtrader-secret-v1\x00")), "written again: sealed for its place")
	require.Equal(t, []byte("mcp-old"), rawSecret(t, s, "agent_llm_settings", "calendar_token", "agent_id",
		agent.ID), "not written: as it was")
	values, err = s.AgentSecrets(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, AgentSecretValues{LLMAPIKey: "sk-new", BridgeToken: "bridge-old", CalendarToken: "mcp-old"},
		values)

	off := false
	require.NoError(t, s.SetTelegram(ctx, &off, nil))
	require.True(t, bytes.HasPrefix(rawSecret(t, s, "telegram_settings", "token", "id", 1),
		[]byte("\x00xtrader-secret-v1\x00telegram_settings:token:1\x00")), "the row written: sealed for its place")
	token, err = s.TelegramToken(ctx)
	require.NoError(t, err)
	require.Equal(t, "123:old", token, "kept")

	require.NoError(t, s.db.Table("agent_accounts").Where("agent_id = ?", agent.ID).
		Update("bridge_token", alone("\x00xtrader-secret-v2\x00agent_accounts:bridge_token:1\x00x")).Error)
	_, err = s.AgentSecrets(ctx, agent.ID)
	require.ErrorContains(t, err, "sealed by a later version")
}
