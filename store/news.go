package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
)

// NewsSettings are the news of the agents, a single record set on the
// dashboard: the feature on or off for all the agents, and the token of the
// MCP server of the economic calendar, shared by the LLM agents (each reads
// the server at its calendar_url), encrypted, never answered.
type NewsSettings struct {
	ID        int16     `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Enabled   bool      `json:"enabled"`
	MCPToken  []byte    `gorm:"column:mcp_token" json:"-"` // encrypted
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (NewsSettings) TableName() string { return "news_settings" }

// News returns the news of the agents, and whether they exist: false before
// the seeds are applied, the news off then.
func (s *Store) News(ctx context.Context) (NewsSettings, bool, error) {
	var settings NewsSettings
	err := s.conn(ctx).First(&settings, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return NewsSettings{}, false, nil
	}
	if err != nil {
		return NewsSettings{}, false, err
	}
	return settings, true, nil
}

// NewsToken returns the token of the MCP server of the news, decrypted: ""
// for none.
func (s *Store) NewsToken(ctx context.Context) (string, error) {
	settings, found, err := s.News(ctx)
	if err != nil || !found {
		return "", err
	}
	return s.open(newsToken(), settings.MCPToken)
}

// NewsUpdate is a change of the news, of the fields set: Token is a Secret (nil
// keeps the one kept); By is who changes them.
type NewsUpdate struct {
	Enabled *bool
	Token   Secret
	By      string
}

// SetNews changes the news, the row locked from its read to its write:
// ErrNotFound before the seeds are applied. A token set ends a conflict of the
// former tokens of the agents (see NewsConflict): their column, unused, is
// dropped by the next run of cmd/migrator (see MigrateNewsToken), never by the
// controller, whose prepared queries of the table would change under it.
func (s *Store) SetNews(ctx context.Context, update NewsUpdate) (NewsSettings, error) {
	var settings NewsSettings
	err := s.Transaction(ctx, func(ctx context.Context) error {
		err := s.conn(ctx).Clauses(lockForUpdate()).First(&settings, 1).Error
		if err != nil {
			return notFound(err, "the settings of the news: run cmd/migrator up, which seeds them")
		}
		if update.Enabled != nil {
			settings.Enabled = *update.Enabled
		}
		if update.Token != nil {
			if err := s.sealInto(&settings.MCPToken, newsToken(), update.Token); err != nil {
				return err
			}
		}
		settings.UpdatedBy, settings.UpdatedAt = update.By, time.Now()
		return s.conn(ctx).Select("*").Save(&settings).Error
	})
	if err != nil {
		return NewsSettings{}, err
	}
	return settings, nil
}

// legacyCalendarTokens reports whether agent_llm_settings still has the
// column of the tokens of the calendar the agents kept each, before the news
// of all the agents (dropped by MigrateNewsToken, or kept by a conflict).
func (s *Store) legacyCalendarTokens(ctx context.Context) (bool, error) {
	var found bool
	err := s.conn(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'agent_llm_settings'
		AND column_name = 'calendar_token')`).Scan(&found).Error
	return found, err
}

// dropLegacyCalendarTokens drops the column of the tokens of the calendar the
// agents kept each, once carried to the news or replaced by its token.
func (s *Store) dropLegacyCalendarTokens(ctx context.Context) error {
	return s.conn(ctx).Exec("ALTER TABLE agent_llm_settings DROP COLUMN IF EXISTS calendar_token").Error
}

// legacyCalendarToken is a token of the calendar an LLM agent kept, encrypted
// for its former place, see MigrateNewsToken.
type legacyCalendarToken struct {
	AgentID   int64
	AgentName string
	Token     []byte
}

// legacyCalendarTokenRows returns the tokens of the calendar the agents kept
// each, none once their column is dropped.
func (s *Store) legacyCalendarTokenRows(ctx context.Context) ([]legacyCalendarToken, bool, error) {
	found, err := s.legacyCalendarTokens(ctx)
	if err != nil || !found {
		return nil, false, err
	}
	var rows []legacyCalendarToken
	err = s.conn(ctx).Raw(`SELECT l.agent_id, a.name AS agent_name, l.calendar_token AS token
		FROM agent_llm_settings l JOIN agents a ON a.id = l.agent_id
		WHERE l.calendar_token IS NOT NULL ORDER BY a.name`).Scan(&rows).Error
	return rows, true, err
}

// NewsConflict returns the agents whose former tokens of the calendar wait for
// the token of the news to be set (see MigrateNewsToken): they held different
// ones, or one that can't be opened, and none was chosen. Empty without a
// conflict.
func (s *Store) NewsConflict(ctx context.Context) ([]string, error) {
	settings, _, err := s.News(ctx)
	if err != nil || len(settings.MCPToken) > 0 {
		return nil, err
	}
	rows, _, err := s.legacyCalendarTokenRows(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.AgentName)
	}
	return names, nil
}

// NewsMigration is how the former tokens of the calendar of the agents were
// carried to the news, see MigrateNewsToken.
type NewsMigration struct {
	// Tokens is how many agents held one.
	Tokens int
	// Taken tells the token of the news was set from theirs, all the same.
	Taken bool
	// Conflict names the agents of a conflict: different tokens, or one
	// that can't be opened, none chosen.
	Conflict []string
}

// MigrateNewsToken carries the tokens of the calendar the LLM agents kept each
// (the column agent_llm_settings.calendar_token, encrypted for their place)
// to the news of all the agents, the column dropped after: none, nothing to
// carry; all the same, the token of the news, the news on (as the agents read
// their calendar); a token of the news already set, kept. With different ones,
// or one that can't be opened, none is chosen: the column is kept, its tokens
// as they were, told on the dashboard (NewsConflict) until a token is set
// there; the next run drops it then. Run by cmd/migrator after the migrations
// and the seeds, before the controller starts, harmless once the column is
// dropped.
func (s *Store) MigrateNewsToken(ctx context.Context) (NewsMigration, error) {
	var result NewsMigration
	err := s.Transaction(ctx, func(ctx context.Context) error {
		rows, found, err := s.legacyCalendarTokenRows(ctx)
		if err != nil || !found {
			return err
		}
		result.Tokens = len(rows)
		var settings NewsSettings
		err = s.conn(ctx).Clauses(lockForUpdate()).First(&settings, 1).Error
		if err != nil {
			return notFound(err, "the settings of the news: run cmd/migrator up, which seeds them")
		}
		if len(rows) == 0 || len(settings.MCPToken) > 0 {
			return s.dropLegacyCalendarTokens(ctx)
		}
		var tokens []string
		unreadable := false
		for _, row := range rows {
			token, err := s.open(legacyCalendarTokenOf(row.AgentID), row.Token)
			if err != nil || strings.TrimSpace(token) == "" {
				unreadable = true
				continue
			}
			if !slices.Contains(tokens, token) {
				tokens = append(tokens, token)
			}
		}
		if unreadable || len(tokens) != 1 {
			for _, row := range rows {
				result.Conflict = append(result.Conflict, row.AgentName)
			}
			return nil
		}
		if err := s.sealInto(&settings.MCPToken, newsToken(), &tokens[0]); err != nil {
			return err
		}
		settings.Enabled, settings.UpdatedBy, settings.UpdatedAt = true, "migração", time.Now()
		if err := s.conn(ctx).Select("*").Save(&settings).Error; err != nil {
			return err
		}
		result.Taken = true
		return s.dropLegacyCalendarTokens(ctx)
	})
	if err != nil {
		return NewsMigration{}, fmt.Errorf("store: the tokens of the calendar of the agents: %w", err)
	}
	return result, nil
}
