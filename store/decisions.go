package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm/clause"

	"github.com/raykavin/xtrader/ui"
)

// Attachment is a file of a decision to keep, eg: an image the model answered
// with.
type Attachment struct {
	MediaType string
	Data      []byte
}

// decisionRow is a decision of an agent in its table.
type decisionRow struct {
	AgentID       int64  `gorm:"primaryKey;autoIncrement:false"`
	ID            string `gorm:"primaryKey"`
	Pair          string
	Timeframe     string
	CandleAt      time.Time
	DecidedAt     time.Time
	Trigger       string
	RequestedBy   string
	RequestNote   string
	Model         string
	PromptID      *int64
	PromptName    string
	PromptVersion *int
	LatencyMS     int64 `gorm:"column:latency_ms"`
	Price         float64
	Position      string
	Action        string
	Confidence    float64
	Reason        string
	Entry         *float64
	StopLoss      *float64
	TakeProfit    *float64
	Levels        []ui.Level        `gorm:"serializer:json"`
	Notes         []ui.DecisionNote `gorm:"serializer:json"`
	Outcome       string
	Error         string
	CreatedAt     time.Time
}

func (decisionRow) TableName() string { return "decisions" }

// decisionOrderRow is an order of a decision: placed by it, or moved.
type decisionOrderRow struct {
	AgentID    int64
	DecisionID string
	OrderID    int64
	Placed     bool
}

func (decisionOrderRow) TableName() string { return "decision_orders" }

// attachmentRow is an attachment of a decision.
type attachmentRow struct {
	AgentID    int64
	DecisionID string
	Number     int
	MediaType  string
	Data       []byte
}

func (attachmentRow) TableName() string { return "decision_attachments" }

// decisionColumns are the columns a decision rewrites: all but its keys.
var decisionColumns = []string{"pair", "timeframe", "candle_at", "decided_at", "trigger", "requested_by",
	"request_note", "model", "prompt_id", "prompt_name", "prompt_version", "latency_ms", "price", "position",
	"action", "confidence", "reason", "entry", "stop_loss", "take_profit", "levels", "notes", "outcome", "error"}

func rowOfDecision(agentID int64, d ui.Decision) decisionRow {
	row := decisionRow{AgentID: agentID, ID: d.ID, Pair: d.Pair, Timeframe: d.Timeframe,
		CandleAt: time.Unix(d.Candle, 0).UTC(), DecidedAt: time.Unix(d.DecidedAt, 0).UTC(), Trigger: d.Trigger,
		RequestedBy: d.RequestedBy, RequestNote: d.RequestNote, Model: d.Model, LatencyMS: d.LatencyMS,
		Price: d.Price, Position: d.Position, Action: d.Action, Confidence: d.Confidence, Reason: d.Reason,
		Entry: d.Entry, StopLoss: d.StopLoss, TakeProfit: d.TakeProfit, Levels: d.Levels, Notes: d.Notes,
		Outcome: d.Outcome, Error: d.Error}
	if row.Levels == nil {
		row.Levels = []ui.Level{}
	}
	if row.Notes == nil {
		row.Notes = []ui.DecisionNote{}
	}
	if p := d.Prompt; p != nil {
		row.PromptName = p.Name
		if p.ID > 0 {
			id := p.ID
			row.PromptID = &id
		}
		if p.Version > 0 {
			version := p.Version
			row.PromptVersion = &version
		}
	}
	return row
}

func (r decisionRow) decision() ui.Decision {
	d := ui.Decision{ID: r.ID, Pair: r.Pair, Timeframe: r.Timeframe, Candle: r.CandleAt.Unix(),
		DecidedAt: r.DecidedAt.Unix(), Trigger: r.Trigger, RequestedBy: r.RequestedBy, RequestNote: r.RequestNote,
		Model: r.Model, LatencyMS: r.LatencyMS, Price: r.Price, Position: r.Position, Action: r.Action,
		Confidence: r.Confidence, Reason: r.Reason, Entry: r.Entry, StopLoss: r.StopLoss, TakeProfit: r.TakeProfit,
		Levels: r.Levels, Notes: r.Notes, Attachments: []ui.DecisionAttachment{}, Outcome: r.Outcome,
		Error: r.Error, OrderIDs: []int64{}}
	if d.Levels == nil {
		d.Levels = []ui.Level{}
	}
	if d.Notes == nil {
		d.Notes = []ui.DecisionNote{}
	}
	if r.PromptName != "" || r.PromptID != nil {
		d.Prompt = &ui.DecisionPrompt{Name: r.PromptName}
		if r.PromptID != nil {
			d.Prompt.ID = *r.PromptID
		}
		if r.PromptVersion != nil {
			d.Prompt.Version = *r.PromptVersion
		}
	}
	return d
}

// SaveDecision keeps a decision of the agent of id with its attachments and
// the links of its orders: an order placed by the first decision listing it,
// moved by the others. It replaces the decision of its ID, or, unless
// overwrite, keeps it only when missing.
func (s *Store) SaveDecision(ctx context.Context, id int64, d ui.Decision, attachments []Attachment,
	overwrite bool) error {
	row := rowOfDecision(id, d)
	return s.Transaction(ctx, func(ctx context.Context) error {
		keys := []clause.Column{{Name: "agent_id"}, {Name: "id"}}
		conflict := clause.OnConflict{Columns: keys, DoNothing: true}
		if overwrite {
			conflict = clause.OnConflict{Columns: keys, DoUpdates: clause.AssignmentColumns(decisionColumns)}
		}
		if err := s.conn(ctx).Clauses(conflict).Create(&row).Error; err != nil {
			return err
		}
		for _, order := range d.OrderIDs {
			// a link already kept, or a decision placing an order another
			// placed meanwhile: left as it is
			err := s.conn(ctx).Exec(`INSERT INTO decision_orders (agent_id, decision_id, order_id, placed)
				VALUES (?, ?, ?, NOT EXISTS (SELECT 1 FROM decision_orders
					WHERE agent_id = ? AND order_id = ? AND placed))
				ON CONFLICT DO NOTHING`, id, d.ID, order, id, order).Error
			if err != nil {
				return err
			}
		}
		for i, attachment := range attachments {
			err := s.conn(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&attachmentRow{AgentID: id,
				DecisionID: d.ID, Number: i, MediaType: attachment.MediaType, Data: attachment.Data}).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// Decisions returns the last decisions of the agent of id, of pair when set,
// limit at most, newest first.
func (s *Store) Decisions(ctx context.Context, id int64, pair string, limit int) ([]ui.Decision, error) {
	query := s.conn(ctx).Where("agent_id = ?", id)
	if pair != "" {
		query = query.Where("pair = ?", pair)
	}
	var rows []decisionRow
	if err := query.Order("decided_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return s.decisions(ctx, id, rows)
}

// DecisionsPerPair returns the last perPair decisions of each pair of the
// agent of id, newest first.
func (s *Store) DecisionsPerPair(ctx context.Context, id int64, perPair int) ([]ui.Decision, error) {
	var rows []decisionRow
	err := s.conn(ctx).Raw(`SELECT * FROM (
			SELECT *, row_number() OVER (PARTITION BY pair ORDER BY decided_at DESC, id DESC) AS rank
			FROM decisions WHERE agent_id = ?
		) ranked WHERE rank <= ? ORDER BY decided_at DESC, id DESC`, id, perPair).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return s.decisions(ctx, id, rows)
}

// Decision returns the decision of the agent of id of decisionID.
func (s *Store) Decision(ctx context.Context, id int64, decisionID string) (ui.Decision, error) {
	var rows []decisionRow
	if err := s.conn(ctx).Where("agent_id = ? AND id = ?", id, decisionID).Limit(1).Find(&rows).Error; err != nil {
		return ui.Decision{}, err
	}
	if len(rows) == 0 {
		return ui.Decision{}, fmt.Errorf("%w: decision %s", ErrNotFound, decisionID)
	}
	decisions, err := s.decisions(ctx, id, rows)
	if err != nil {
		return ui.Decision{}, err
	}
	return decisions[0], nil
}

// decisions returns the decisions of rows with their orders and the
// description of their attachments.
func (s *Store) decisions(ctx context.Context, id int64, rows []decisionRow) ([]ui.Decision, error) {
	decisions := make([]ui.Decision, 0, len(rows))
	if len(rows) == 0 {
		return decisions, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	var links []decisionOrderRow
	err := s.conn(ctx).Where("agent_id = ? AND decision_id IN ?", id, ids).Order("order_id").Find(&links).Error
	if err != nil {
		return nil, err
	}
	var files []struct {
		DecisionID string
		Number     int
		MediaType  string
		Size       int
	}
	err = s.conn(ctx).Model(&attachmentRow{}).Select("decision_id, number, media_type, length(data) AS size").
		Where("agent_id = ? AND decision_id IN ?", id, ids).Order("number").Scan(&files).Error
	if err != nil {
		return nil, err
	}
	orders := make(map[string][]int64)
	for _, link := range links {
		orders[link.DecisionID] = append(orders[link.DecisionID], link.OrderID)
	}
	attachments := make(map[string][]ui.DecisionAttachment)
	for _, f := range files {
		attachments[f.DecisionID] = append(attachments[f.DecisionID],
			ui.DecisionAttachment{Index: f.Number, MediaType: f.MediaType, Size: f.Size})
	}
	for _, row := range rows {
		d := row.decision()
		if list := orders[row.ID]; list != nil {
			d.OrderIDs = list
		}
		if list := attachments[row.ID]; list != nil {
			d.Attachments = list
		}
		decisions = append(decisions, d)
	}
	return decisions, nil
}

// DecisionAttachment returns the attachment number of the decision decisionID
// of the agent of id: its media type and its content.
func (s *Store) DecisionAttachment(ctx context.Context, id int64, decisionID string, number int) (string, []byte,
	error) {
	var rows []attachmentRow
	err := s.conn(ctx).Where("agent_id = ? AND decision_id = ? AND number = ?", id, decisionID, number).
		Limit(1).Find(&rows).Error
	if err != nil {
		return "", nil, err
	}
	if len(rows) == 0 {
		return "", nil, fmt.Errorf("%w: attachment %d of the decision %s", ErrNotFound, number, decisionID)
	}
	return rows[0].MediaType, rows[0].Data, nil
}

// DecisionLinks returns the decision that placed each order of the agent of
// id, by the ID of the order.
func (s *Store) DecisionLinks(ctx context.Context, id int64) (map[int64]string, error) {
	var links []decisionOrderRow
	if err := s.conn(ctx).Where("agent_id = ? AND placed", id).Find(&links).Error; err != nil {
		return nil, err
	}
	placed := make(map[int64]string, len(links))
	for _, link := range links {
		placed[link.OrderID] = link.DecisionID
	}
	return placed, nil
}
