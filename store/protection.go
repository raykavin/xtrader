package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm/clause"

	"github.com/raykavin/xtrader/ui"
)

// ProtectionMaxDistance bounds every distance of the protection of the
// positions, in its unit (see ui.ProtectionUnitATR), as the database checks.
const ProtectionMaxDistance = 50

// AgentProtection is the protection of the open positions of an agent, see
// ui.ProtectionSettings: a row once set, none for an agent without one.
type AgentProtection struct {
	AgentID             int64     `gorm:"primaryKey" json:"agent_id"`
	Unit                string    `json:"unit"`
	BreakevenEnabled    bool      `json:"breakeven_enabled"`
	BreakevenSupervisor bool      `json:"breakeven_supervisor"`
	BreakevenTrigger    float64   `json:"breakeven_trigger"`
	BreakevenMargin     float64   `json:"breakeven_margin"`
	TrailingEnabled     bool      `json:"trailing_enabled"`
	TrailingSupervisor  bool      `json:"trailing_supervisor"`
	TrailingTrigger     float64   `json:"trailing_trigger"`
	TrailingDistance    float64   `json:"trailing_distance"`
	TrailingStep        float64   `json:"trailing_step"`
	UpdatedBy           string    `json:"updated_by"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (AgentProtection) TableName() string { return "agent_protection" }

// Settings returns the protection as the agents and the dashboard take it.
func (p AgentProtection) Settings() ui.ProtectionSettings {
	unit := p.Unit
	if unit == "" {
		unit = ui.ProtectionUnitATR
	}
	return ui.ProtectionSettings{Unit: unit,
		Breakeven: ui.BreakevenSettings{Enabled: p.BreakevenEnabled, Supervisor: p.BreakevenSupervisor,
			Trigger: p.BreakevenTrigger, Margin: p.BreakevenMargin},
		Trailing: ui.TrailingSettings{Enabled: p.TrailingEnabled, Supervisor: p.TrailingSupervisor,
			Trigger: p.TrailingTrigger, Distance: p.TrailingDistance, Step: p.TrailingStep}}
}

// ProtectionOf returns the protection of the positions of a, all off without
// its row, see ui.ProtectionSettings.
func (a Agent) ProtectionOf() ui.ProtectionSettings {
	if a.Protection == nil {
		return AgentProtection{}.Settings()
	}
	return a.Protection.Settings()
}

// validateProtection returns why settings are refused, as the database
// checks them: its unit atr; every distance from 0 to ProtectionMaxDistance;
// the breakeven, enabled or allowed to the supervisor, with a trigger above
// its margin; the trailing stop with a trigger, a distance, and a step up to
// the distance.
func validateProtection(settings ui.ProtectionSettings) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: the protection of the positions: %s", ErrInvalid, fmt.Sprintf(format, args...))
	}
	if settings.Unit != ui.ProtectionUnitATR {
		return invalid("the unit %q: atr", settings.Unit)
	}
	b, t := settings.Breakeven, settings.Trailing
	for name, value := range map[string]float64{"the trigger of the breakeven": b.Trigger,
		"the margin of the breakeven": b.Margin, "the trigger of the trailing stop": t.Trigger,
		"the distance of the trailing stop": t.Distance, "the step of the trailing stop": t.Step} {
		if math.IsNaN(value) || value < 0 || value > ProtectionMaxDistance {
			return invalid("%s: from 0 to %d ATR", name, ProtectionMaxDistance)
		}
	}
	if (b.Enabled || b.Supervisor) && (b.Trigger <= 0 || b.Margin >= b.Trigger) {
		return invalid("the breakeven needs a trigger above 0 and above its margin")
	}
	if (t.Enabled || t.Supervisor) && (t.Trigger <= 0 || t.Distance <= 0 || t.Step <= 0 || t.Step > t.Distance) {
		return invalid("the trailing stop needs a trigger, a distance and a step above 0, the step up to the distance")
	}
	return nil
}

// SetAgentProtection sets the protection of the positions of the agent of id,
// by by, validated (see validateProtection), and returns it.
func (s *Store) SetAgentProtection(ctx context.Context, id int64, settings ui.ProtectionSettings,
	by string) (AgentProtection, error) {
	if settings.Unit == "" {
		settings.Unit = ui.ProtectionUnitATR
	}
	if err := validateProtection(settings); err != nil {
		return AgentProtection{}, err
	}
	row := AgentProtection{AgentID: id, Unit: settings.Unit, BreakevenEnabled: settings.Breakeven.Enabled,
		BreakevenSupervisor: settings.Breakeven.Supervisor, BreakevenTrigger: settings.Breakeven.Trigger,
		BreakevenMargin: settings.Breakeven.Margin, TrailingEnabled: settings.Trailing.Enabled,
		TrailingSupervisor: settings.Trailing.Supervisor, TrailingTrigger: settings.Trailing.Trigger,
		TrailingDistance: settings.Trailing.Distance, TrailingStep: settings.Trailing.Step, UpdatedBy: by,
		UpdatedAt: time.Now()}
	err := s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "agent_id"}}, UpdateAll: true}).
		Create(&row).Error
	if err != nil {
		return AgentProtection{}, notFound(err, fmt.Sprintf("the protection of the agent %d", id))
	}
	return row, nil
}

// The statuses of an arm of the protection of a position, see
// PositionProtection.
const (
	ArmRequested = "requested" // sent to the agent, its answer awaited
	ArmArmed     = "armed"     // waiting for its trigger
	ArmActive    = "active"    // applied
	ArmRefused   = "refused"   // not armed by the agent
	ArmEnded     = "ended"     // its position closed
)

// The outcomes of the last move of an arm, see PositionProtection.
const (
	MoveConfirmed = "confirmed"
	MoveRejected  = "rejected"
	MoveUnknown   = "unknown"
)

// maxProtectionReason bounds the reason of an arm.
const maxProtectionReason = 1000

// PositionProtection is a protection the XTrader AI armed on an open position
// of an agent (position_protections): the breakeven or the trailing stop of
// the position PositionRef of Pair, asked by the analysis RunID or by the
// user UserID confirming a proposal, for Reason resting on Evidence (a JSON
// array); its status (see ArmRequested) and its last move of the stop loss
// (Move, see MoveConfirmed): Previous, Requested and Confirmed, 0 for none.
type PositionProtection struct {
	ID          int64           `gorm:"primaryKey" json:"id"`
	AgentID     int64           `json:"agent_id"`
	RunID       *int64          `json:"run_id,omitempty"`
	UserID      *int64          `json:"user_id,omitempty"`
	Pair        string          `json:"pair"`
	PositionRef string          `json:"position_ref"`
	Ticket      int64           `json:"ticket"`
	Side        string          `json:"side"`
	Feature     string          `json:"feature"`
	Reason      string          `json:"reason"`
	Category    *string         `json:"category,omitempty"`
	Evidence    json.RawMessage `gorm:"type:jsonb" json:"evidence"`
	RequestedBy string          `json:"requested_by"`
	Status      string          `json:"status"`
	Move        string          `json:"move"`
	Previous    float64         `json:"previous"`
	Requested   float64         `json:"requested"`
	Confirmed   float64         `json:"confirmed"`
	Detail      string          `json:"detail"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	EndedAt     *time.Time      `json:"ended_at,omitempty"`
}

func (PositionProtection) TableName() string { return "position_protections" }

// Arm returns the arm as the agents take it.
func (p PositionProtection) Arm() ui.ProtectionArm {
	return ui.ProtectionArm{ID: p.ID, Pair: p.Pair, PositionRef: p.PositionRef, Feature: p.Feature,
		Reason: p.Reason, ArmedAt: p.CreatedAt.Unix()}
}

// ArmProtection records the arm a, requested: ErrConflict when one is in force
// already on its position and feature, ErrInvalid for one out of the rules.
func (s *Store) ArmProtection(ctx context.Context, a PositionProtection) (PositionProtection, error) {
	a.Reason = strings.TrimSpace(a.Reason)
	switch {
	case a.Feature != ui.ProtectionBreakeven && a.Feature != ui.ProtectionTrailing:
		return PositionProtection{}, fmt.Errorf("%w: the feature %q: breakeven or trailing", ErrInvalid, a.Feature)
	case a.Reason == "" || utf8.RuneCountInString(a.Reason) > maxProtectionReason:
		return PositionProtection{}, fmt.Errorf("%w: the reason of an arm: 1 to %d characters", ErrInvalid,
			maxProtectionReason)
	case a.Pair == "" || a.PositionRef == "":
		return PositionProtection{}, fmt.Errorf("%w: an arm needs its pair and its position", ErrInvalid)
	}
	if len(a.Evidence) == 0 {
		a.Evidence = json.RawMessage(`[]`)
	}
	now := time.Now()
	a.ID, a.Status, a.CreatedAt, a.UpdatedAt = 0, ArmRequested, now, now
	if err := s.conn(ctx).Create(&a).Error; err != nil {
		return PositionProtection{}, notFound(err, fmt.Sprintf("the %s of the position %s", a.Feature,
			a.PositionRef))
	}
	return a, nil
}

// ProtectionMove is a move of the stop loss an arm made, see
// PositionProtection.
type ProtectionMove struct {
	Move      string
	Previous  float64
	Requested float64
	Confirmed float64
	Detail    string
}

// SetArmStatus sets the status of the arm of id, and its last move when move
// is set; an arm ended stays ended.
func (s *Store) SetArmStatus(ctx context.Context, id int64, status string, move *ProtectionMove) error {
	values := map[string]any{"status": status, "updated_at": time.Now()}
	if status == ArmEnded {
		values["ended_at"] = time.Now()
	}
	if move != nil {
		values["move"], values["previous"], values["requested"] = move.Move, move.Previous, move.Requested
		values["confirmed"], values["detail"] = move.Confirmed, move.Detail
	}
	result := s.conn(ctx).Model(&PositionProtection{}).Where("id = ? AND status <> ?", id, ArmEnded).
		Updates(values)
	if result.Error != nil {
		return notFound(result.Error, fmt.Sprintf("the arm %d", id))
	}
	return nil
}

// ProtectionArmsInForce returns the arms in force of the agent of id
// (requested, armed or active), the oldest first.
func (s *Store) ProtectionArmsInForce(ctx context.Context, id int64) ([]PositionProtection, error) {
	var arms []PositionProtection
	err := s.conn(ctx).Where("agent_id = ? AND status IN ?", id, []string{ArmRequested, ArmArmed, ArmActive}).
		Order("id").Find(&arms).Error
	return arms, err
}

// ProtectionArms returns the last limit arms of every agent, the newest first.
func (s *Store) ProtectionArms(ctx context.Context, limit int) ([]PositionProtection, error) {
	var arms []PositionProtection
	err := s.conn(ctx).Order("id DESC").Limit(limit).Find(&arms).Error
	return arms, err
}

// ProtectionArmsOfRun returns the arms an analysis asked for.
func (s *Store) ProtectionArmsOfRun(ctx context.Context, runID int64) ([]PositionProtection, error) {
	var arms []PositionProtection
	err := s.conn(ctx).Where("run_id = ?", runID).Order("id").Find(&arms).Error
	return arms, err
}

// EndProtectionArms ends the arms in force of the agent of id whose positions
// aren't among held (by their references), eg: closed; it returns how many.
func (s *Store) EndProtectionArms(ctx context.Context, id int64, held []string) (int64, error) {
	query := s.conn(ctx).Model(&PositionProtection{}).
		Where("agent_id = ? AND status IN ?", id, []string{ArmRequested, ArmArmed, ArmActive})
	if len(held) > 0 {
		query = query.Where("position_ref NOT IN ?", held)
	}
	now := time.Now()
	result := query.Updates(map[string]any{"status": ArmEnded, "ended_at": now, "updated_at": now})
	return result.RowsAffected, result.Error
}
