package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm/clause"

	"github.com/raykavin/xtrader/ui"
)

// JournalEntry is a call to the model of an LLM agent with its whole context:
// Input and Decision are the JSON the agent sent, its ID the one of the
// decision kept of it.
type JournalEntry struct {
	ID          string             `json:"id"`
	At          time.Time          `json:"at"`
	Pair        string             `json:"pair"`
	Trigger     string             `json:"trigger,omitempty"`
	RequestedBy string             `json:"requested_by,omitempty"`
	Model       string             `json:"model"`
	Prompt      *ui.DecisionPrompt `json:"prompt,omitempty"`
	LatencyMS   int64              `json:"latency_ms"`
	Input       json.RawMessage    `json:"input"`
	Decision    json.RawMessage    `json:"decision,omitempty"`
	Error       string             `json:"error,omitempty"`
}

// Remembered is a decision of the journal of an LLM agent for the memory of
// its prompt: the candle it was taken on, when (the time of its entry), and
// its answer.
type Remembered struct {
	Pair       string    `json:"pair"`
	Candle     time.Time `json:"candle"`
	At         time.Time `json:"at"`
	Action     string    `json:"action"`
	Confidence float64   `json:"confidence"`
	Reason     string    `json:"reason"`
}

// journalRow is an entry of the journal of an agent in its table.
type journalRow struct {
	AgentID       int64  `gorm:"primaryKey;autoIncrement:false"`
	ID            string `gorm:"primaryKey"`
	At            time.Time
	Pair          string
	Trigger       string
	RequestedBy   string
	Model         string
	PromptID      *int64
	PromptName    string
	PromptVersion *int
	LatencyMS     int64 `gorm:"column:latency_ms"`
	Input         jsonb
	Decision      jsonb
	Error         string
}

func (journalRow) TableName() string { return "journal_entries" }

// jsonb is a JSON document of a jsonb column, written as text: []byte would be
// sent as bytea. Empty or null, it is NULL.
type jsonb json.RawMessage

func (j jsonb) Value() (driver.Value, error) {
	if len(j) == 0 || string(j) == "null" {
		return nil, nil
	}
	if !json.Valid(j) {
		return nil, fmt.Errorf("store: invalid JSON")
	}
	return string(j), nil
}

func (j *jsonb) Scan(src interface{}) error {
	switch v := src.(type) {
	case nil:
		*j = nil
	case []byte:
		*j = append(jsonb{}, v...)
	case string:
		*j = jsonb(v)
	default:
		return fmt.Errorf("store: jsonb of %T", src)
	}
	return nil
}

// AppendJournal appends the entries of the journal of the agent of id, the
// ones kept already left as they are.
func (s *Store) AppendJournal(ctx context.Context, id int64, entries []JournalEntry) error {
	_, err := s.appendJournal(ctx, id, entries)
	return err
}

// appendJournal appends the entries of the journal of the agent of id, the
// ones kept already left as they are, and returns how many it wrote.
func (s *Store) appendJournal(ctx context.Context, id int64, entries []JournalEntry) (int64, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	rows := make([]journalRow, 0, len(entries))
	for _, e := range entries {
		if len(e.Input) == 0 {
			return 0, fmt.Errorf("%w: o registro %s do diário não tem os dados de entrada", ErrInvalid, e.ID)
		}
		row := journalRow{AgentID: id, ID: e.ID, At: e.At.UTC(), Pair: e.Pair, Trigger: e.Trigger,
			RequestedBy: e.RequestedBy, Model: e.Model, LatencyMS: e.LatencyMS, Input: jsonb(e.Input),
			Decision: jsonb(e.Decision), Error: e.Error}
		if p := e.Prompt; p != nil {
			row.PromptName = p.Name
			if p.ID > 0 {
				promptID := p.ID
				row.PromptID = &promptID
			}
			if p.Version > 0 {
				version := p.Version
				row.PromptVersion = &version
			}
		}
		rows = append(rows, row)
	}
	result := s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "agent_id"}, {Name: "id"}},
		DoNothing: true}).CreateInBatches(&rows, 100)
	return result.RowsAffected, result.Error
}

// journalMemory reads the memory of an agent (@agent): the last @limit
// decisions of each pair of its journal, oldest first, read from the index of
// the agent, the pair and the time, newest first, a pair at a time (see
// pairsOf), the entries of a pair read until its last ones are found, however
// many the agent kept.
var journalMemory = `WITH RECURSIVE ` + pairsOf("journal_entries") + `
	SELECT m.pair, (m.input->>'time')::timestamptz AS candle, m.at AS at, m.decision->>'action' AS action,
		COALESCE((m.decision->>'confidence')::float8, 0) AS confidence,
		btrim(COALESCE(m.decision->>'reason', '')) AS reason
	FROM pairs CROSS JOIN LATERAL (
		SELECT * FROM journal_entries
		WHERE agent_id = @agent AND pair = pairs.pair AND error = '' AND decision IS NOT NULL
			AND decision->>'action' IS NOT NULL AND input->>'time' IS NOT NULL
		ORDER BY at DESC, id DESC LIMIT @limit
	) m
	ORDER BY m.at, m.id`

// JournalMemory returns the last perPair decisions of each pair of the
// journal of the agent of id, oldest first, the errors and the entries without
// a decision left out: the memory of its prompt, see agents/llm.
func (s *Store) JournalMemory(ctx context.Context, id int64, perPair int) ([]Remembered, error) {
	var remembered []Remembered
	err := s.conn(ctx).Raw(journalMemory, sql.Named("agent", id), sql.Named("limit", perPair)).
		Scan(&remembered).Error
	return remembered, err
}
