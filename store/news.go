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
// MCP server of the economic calendar (its address in NewsRules), shared by
// every agent, encrypted, never answered.
type NewsSettings struct {
	ID        int16     `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Enabled   bool      `json:"enabled"`
	MCPToken  []byte    `gorm:"column:mcp_token" json:"-"` // encrypted
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (NewsSettings) TableName() string { return "news_settings" }

// NewsRules are the rules of the news of all the agents, a single record set
// on the dashboard with NewsSettings: the address of the MCP server of the
// calendar, and the window of the news block in minutes (BlockBefore before a
// release of at least BlockImportance to BlockAfter after it, both 0 for
// none). UpdatedBy is who changed them last, empty for the seeds.
type NewsRules struct {
	ID              int16     `gorm:"primaryKey;autoIncrement:false" json:"-"`
	MCPURL          *string   `gorm:"column:mcp_url" json:"mcp_url,omitempty"`
	BlockBefore     int       `gorm:"column:block_before_minutes" json:"block_before"`
	BlockAfter      int       `gorm:"column:block_after_minutes" json:"block_after"`
	BlockImportance string    `json:"block_importance"`
	UpdatedBy       string    `json:"updated_by"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (NewsRules) TableName() string { return "news_rules" }

// newsImportances are the importances of a release of the window of the news.
var newsImportances = []string{"low", "medium", "high"}

// maxNewsWindow bounds the window of the news, in minutes: a day.
const maxNewsWindow = 1440

// NewsRules returns the rules of the news, and whether they exist: false before
// the seeds are applied, no server nor window then.
func (s *Store) NewsRules(ctx context.Context) (NewsRules, bool, error) {
	var rules NewsRules
	err := s.conn(ctx).First(&rules, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return NewsRules{}, false, nil
	}
	if err != nil {
		return NewsRules{}, false, err
	}
	return rules, true, nil
}

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
// keeps the one kept); URL the address of the MCP server ("" removes it), the
// window in minutes and its importance (see NewsRules); By is who changes them.
type NewsUpdate struct {
	Enabled         *bool
	Token           Secret
	URL             *string
	BlockBefore     *int
	BlockAfter      *int
	BlockImportance *string
	By              string
}

// rules reports whether u changes the rules of the news.
func (u NewsUpdate) rules() bool {
	return u.URL != nil || u.BlockBefore != nil || u.BlockAfter != nil || u.BlockImportance != nil
}

// apply applies the rules of u to rules, ErrInvalid out of their bounds.
func (u NewsUpdate) apply(rules *NewsRules) error {
	if u.URL != nil {
		url := strings.TrimSpace(*u.URL)
		switch {
		case url == "":
			rules.MCPURL = nil
		case !httpURL(url):
			return fmt.Errorf("%w: o servidor MCP deve ser uma URL http(s) sem credenciais", ErrInvalid)
		default:
			rules.MCPURL = &url
		}
	}
	for _, minutes := range []struct {
		value *int
		into  *int
		name  string
	}{{u.BlockBefore, &rules.BlockBefore, "antes"}, {u.BlockAfter, &rules.BlockAfter, "depois"}} {
		if minutes.value == nil {
			continue
		}
		if *minutes.value < 0 || *minutes.value > maxNewsWindow {
			return fmt.Errorf("%w: a janela das notícias %s de uma divulgação deve ficar entre 0 e %d minutos",
				ErrInvalid, minutes.name, maxNewsWindow)
		}
		*minutes.into = *minutes.value
	}
	if u.BlockImportance != nil {
		if !slices.Contains(newsImportances, *u.BlockImportance) {
			return fmt.Errorf("%w: a importância das divulgações deve ser low, medium ou high", ErrInvalid)
		}
		rules.BlockImportance = *u.BlockImportance
	}
	return nil
}

// SetNews changes the news and their rules, the rows locked from their read
// to their write: ErrNotFound before the seeds are applied, ErrInvalid for a
// rule out of its bounds. A token set ends a conflict of the
// former tokens of the agents (see NewsConflict): their column, unused, is
// dropped by the next run of cmd/migrator (see MigrateNewsToken), never by the
// controller, whose prepared queries of the table would change under it.
func (s *Store) SetNews(ctx context.Context, update NewsUpdate) (NewsSettings, error) {
	var settings NewsSettings
	err := s.Transaction(ctx, func(ctx context.Context) error {
		err := s.conn(ctx).Clauses(lockForUpdate()).First(&settings, 1).Error
		if err != nil {
			return notSeeded(err, "a configuração das notícias")
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
		if err := s.conn(ctx).Select("*").Save(&settings).Error; err != nil {
			return err
		}
		if !update.rules() {
			return nil
		}
		var rules NewsRules
		if err := s.conn(ctx).Clauses(lockForUpdate()).First(&rules, 1).Error; err != nil {
			return notSeeded(err, "a configuração das regras das notícias")
		}
		if err := update.apply(&rules); err != nil {
			return err
		}
		rules.UpdatedBy, rules.UpdatedAt = update.By, time.Now()
		return s.conn(ctx).Select("*").Save(&rules).Error
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
			return notSeeded(err, "a configuração das notícias")
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

// legacyCalendarURLs reports whether agent_llm_settings still has the column
// of the MCP server of the calendar each LLM agent had, before the one of the
// news of all the agents (dropped by MigrateNewsURL, or kept by a conflict).
func (s *Store) legacyCalendarURLs(ctx context.Context) (bool, error) {
	var found bool
	err := s.conn(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'agent_llm_settings'
		AND column_name = 'calendar_url')`).Scan(&found).Error
	return found, err
}

// legacyCalendarURL is the MCP server of the calendar an LLM agent had.
type legacyCalendarURL struct {
	AgentName string
	URL       string
}

// legacyCalendarURLRows returns the servers of the calendar the LLM agents had
// each, none once their column is dropped.
func (s *Store) legacyCalendarURLRows(ctx context.Context) ([]legacyCalendarURL, bool, error) {
	found, err := s.legacyCalendarURLs(ctx)
	if err != nil || !found {
		return nil, false, err
	}
	var rows []legacyCalendarURL
	err = s.conn(ctx).Raw(`SELECT a.name AS agent_name, trim(l.calendar_url) AS url
		FROM agent_llm_settings l JOIN agents a ON a.id = l.agent_id
		WHERE nullif(trim(l.calendar_url), '') IS NOT NULL AND a.deleted_at IS NULL
		ORDER BY a.name`).Scan(&rows).Error
	return rows, true, err
}

// NewsURLConflict returns the LLM agents whose former servers of the calendar
// differ, none chosen, until the address of the news is set on the dashboard
// (see MigrateNewsURL). Empty without a conflict.
func (s *Store) NewsURLConflict(ctx context.Context) ([]string, error) {
	rules, _, err := s.NewsRules(ctx)
	if err != nil || rules.UpdatedBy != "" {
		return nil, err
	}
	rows, _, err := s.legacyCalendarURLRows(ctx)
	if err != nil {
		return nil, err
	}
	var urls []string
	for _, row := range rows {
		if !slices.Contains(urls, row.URL) {
			urls = append(urls, row.URL)
		}
	}
	if len(urls) < 2 {
		return nil, nil
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.AgentName)
	}
	return names, nil
}

// legacyNewsParams are the params of the window of the news block each LLM
// agent had, now the window of the news of all the agents (see NewsRules).
var legacyNewsParams = []string{"news-block-before", "news-block-after", "news-block-importance"}

// NewsURLMigration is how the former servers of the calendar of the LLM agents
// and their windows of the news block were carried to the news, see
// MigrateNewsURL.
type NewsURLMigration struct {
	// URLs is how many agents had a server.
	URLs int
	// Taken is the address of the news set from theirs, all the same.
	Taken string
	// Conflict names the agents of different servers, none chosen.
	Conflict []string
	// Params is how many agents had params of the window, removed.
	Params int
}

// MigrateNewsURL carries the MCP server of the calendar each LLM agent had (the
// column agent_llm_settings.calendar_url) to the news of all the agents, the
// column dropped after: none, nothing to carry; all the same, the address of
// the news unless set on the dashboard. With different ones, none is chosen:
// the column is kept, told on the dashboard (NewsURLConflict) until the
// address is set there; the next run drops it then. The params of the window
// of the news block of the agents (news-block-*) are removed: the window is
// the one of the news of all the agents. Run by cmd/migrator after the
// migrations and the seeds, before the controller starts, harmless after.
func (s *Store) MigrateNewsURL(ctx context.Context) (NewsURLMigration, error) {
	var result NewsURLMigration
	err := s.Transaction(ctx, func(ctx context.Context) error {
		keys := "{" + strings.Join(legacyNewsParams, ",") + "}"
		params := s.conn(ctx).Exec(`UPDATE agent_llm_settings SET params = params - ?::text[]
			WHERE jsonb_exists_any(params, ?::text[])`, keys, keys)
		if params.Error != nil {
			return params.Error
		}
		result.Params = int(params.RowsAffected)

		rows, found, err := s.legacyCalendarURLRows(ctx)
		if err != nil || !found {
			return err
		}
		result.URLs = len(rows)
		var rules NewsRules
		if err := s.conn(ctx).Clauses(lockForUpdate()).First(&rules, 1).Error; err != nil {
			return notSeeded(err, "a configuração das regras das notícias")
		}
		var urls []string
		for _, row := range rows {
			if !slices.Contains(urls, row.URL) {
				urls = append(urls, row.URL)
			}
		}
		switch {
		case rules.UpdatedBy != "" || len(urls) == 0:
		case len(urls) > 1:
			for _, row := range rows {
				result.Conflict = append(result.Conflict, row.AgentName)
			}
			return nil
		case rules.MCPURL == nil || *rules.MCPURL != urls[0]:
			if !httpURL(urls[0]) {
				result.Conflict = []string{rows[0].AgentName}
				return nil
			}
			rules.MCPURL, rules.UpdatedAt, result.Taken = &urls[0], time.Now(), urls[0]
			if err := s.conn(ctx).Select("*").Save(&rules).Error; err != nil {
				return err
			}
		default:
			result.Taken = urls[0]
		}
		return s.conn(ctx).Exec("ALTER TABLE agent_llm_settings DROP COLUMN IF EXISTS calendar_url").Error
	})
	if err != nil {
		return NewsURLMigration{}, fmt.Errorf("store: the servers of the calendar of the agents: %w", err)
	}
	return result, nil
}
