package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/raykavin/xtrader/ui"
)

// The ways a stop loss of the daily loss limits reaches the broker, see
// DailyStop.
const (
	StopViaAgent  = "agent"  // the agent running moved it, see ui.SupervisionDailyStopsRequest
	StopViaBridge = "bridge" // the controller moved it on the bridge of the agent stopped
)

// DailyStop is a stop loss the daily loss limits ask of an open position
// (daily_stops): the position of an agent on Pair of an account (its tickets
// moving together under one stop), on Side, a day; the level within the loss
// left of the limit of Scope (empty for all the agents) in Currency, the loss
// left and the share of the position then (Budget); its last move at the
// broker (see ui.DailyStopConfirmed): the stop before, the one asked, the one
// confirmed, why it wasn't moved, through the agent or the bridge; Attempts,
// the moves sent; ended once its position closed, its limit turned off or its
// day over.
type DailyStop struct {
	ID        int64           `gorm:"primaryKey" json:"id"`
	Day       time.Time       `gorm:"type:date" json:"day"`
	AgentID   int64           `json:"agent_id"`
	Account   string          `json:"account"`
	Pair      string          `json:"pair"`
	Side      string          `json:"side"`
	Tickets   json.RawMessage `gorm:"type:jsonb" json:"tickets"`
	Scope     string          `json:"scope"`
	Currency  string          `json:"currency"`
	Level     float64         `json:"level"`
	Via       string          `json:"via"`
	Move      string          `json:"move"`
	Previous  float64         `json:"previous"`
	Requested float64         `json:"requested"`
	Confirmed float64         `json:"confirmed"`
	Detail    string          `json:"detail"`
	Budget    json.RawMessage `gorm:"type:jsonb" json:"budget"`
	Attempts  int             `json:"attempts"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	EndedAt   *time.Time      `json:"ended_at,omitempty"`
}

func (DailyStop) TableName() string { return "daily_stops" }

// DailyStopBudget is the loss left of the limit of a stop and the share of its
// position, in the currency of the stop: the limit, the result of the day of
// its scope (realized and open), the exit costs set aside, the loss left
// (Headroom) and the share of the position of it; the loss to its stop
// before (Risk, absent for none).
type DailyStopBudget struct {
	Limit    float64  `json:"limit"`
	Realized float64  `json:"realized"`
	Open     float64  `json:"open"`
	Costs    float64  `json:"costs"`
	Headroom float64  `json:"headroom"`
	Share    float64  `json:"share"`
	Risk     *float64 `json:"risk,omitempty"`
}

// TicketsOf returns the tickets of the stop.
func (d DailyStop) TicketsOf() []int64 {
	var tickets []int64
	_ = json.Unmarshal(d.Tickets, &tickets)
	return tickets
}

// BudgetOf returns the budget of the stop, zero when unreadable.
func (d DailyStop) BudgetOf() DailyStopBudget {
	var budget DailyStopBudget
	_ = json.Unmarshal(d.Budget, &budget)
	return budget
}

// Info returns the stop as the dashboard takes it, its agent named name.
func (d DailyStop) Info(name string) ui.DailyStopInfo {
	budget := d.BudgetOf()
	tickets := d.TicketsOf()
	if tickets == nil {
		tickets = []int64{}
	}
	return ui.DailyStopInfo{Agent: name, Pair: d.Pair, Side: d.Side, Tickets: tickets, Scope: d.Scope,
		Currency: d.Currency, Level: d.Level, Headroom: budget.Headroom, Share: budget.Share, Via: d.Via,
		Move: d.Move, Previous: d.Previous, Requested: d.Requested, Confirmed: d.Confirmed, Detail: d.Detail,
		UpdatedAt: d.UpdatedAt.Unix()}
}

// DailyStopTarget is a stop loss the daily loss limits ask now, see
// SetDailyStop.
type DailyStopTarget struct {
	Day      string // eg: 2026-11-16, of the zone of the limits
	AgentID  int64
	Account  string
	Pair     string
	Side     string // BUY or SELL
	Tickets  []int64
	Scope    string
	Currency string
	Level    float64
	Budget   DailyStopBudget
}

// SetDailyStop records the stop loss of target in force: the one of its
// position of the day updated (its level, side, tickets, scope and budget;
// its last move kept), else added; a position whose side changed starts
// again. It returns the stop as recorded.
func (s *Store) SetDailyStop(ctx context.Context, target DailyStopTarget) (DailyStop, error) {
	if target.Level <= 0 || target.Pair == "" || target.Account == "" ||
		(target.Side != "BUY" && target.Side != "SELL") {
		return DailyStop{}, fmt.Errorf("%w: stop do limite diário: nível, ativo, conta e lado são obrigatórios",
			ErrInvalid)
	}
	tickets, _ := json.Marshal(target.Tickets)
	budget, _ := json.Marshal(target.Budget)
	var stop DailyStop
	err := s.Transaction(ctx, func(ctx context.Context) error {
		var rows []DailyStop
		err := s.conn(ctx).Clauses(lockForUpdate()).Where("day = ? AND agent_id = ? AND account = ? AND pair = ? "+
			"AND ended_at IS NULL", target.Day, target.AgentID, target.Account, target.Pair).Find(&rows).Error
		if err != nil {
			return err
		}
		now := time.Now()
		if len(rows) > 0 && rows[0].Side == target.Side {
			stop = rows[0]
			stop.Tickets, stop.Scope, stop.Currency, stop.Level, stop.Budget, stop.UpdatedAt = tickets,
				target.Scope, target.Currency, target.Level, budget, now
			return s.conn(ctx).Model(&DailyStop{ID: stop.ID}).Updates(map[string]any{"tickets": tickets,
				"scope": target.Scope, "currency": target.Currency, "level": target.Level, "budget": budget,
				"updated_at": now}).Error
		}
		if len(rows) > 0 { // another position, on the other side
			if err := s.conn(ctx).Model(&DailyStop{ID: rows[0].ID}).Update("ended_at", now).Error; err != nil {
				return err
			}
		}
		day, err := time.Parse(time.DateOnly, target.Day)
		if err != nil {
			return fmt.Errorf("%w: dia do stop do limite diário: %v", ErrInvalid, err)
		}
		stop = DailyStop{Day: day, AgentID: target.AgentID, Account: target.Account, Pair: target.Pair,
			Side: target.Side, Tickets: tickets, Scope: target.Scope, Currency: target.Currency, Level: target.Level,
			Budget: budget, CreatedAt: now, UpdatedAt: now}
		return s.conn(ctx).Create(&stop).Error
	})
	return stop, err
}

// DailyStopMove is a move of a stop loss of the daily loss limits at the
// broker, see SetDailyStopMove.
type DailyStopMove struct {
	Via       string // see StopViaAgent
	Move      string // see ui.DailyStopConfirmed
	Previous  float64
	Requested float64
	Confirmed float64
	Detail    string
	Sent      bool // sent to the broker, counted in Attempts
}

// SetDailyStopMove records the last move of the stop loss of id.
func (s *Store) SetDailyStopMove(ctx context.Context, id int64, move DailyStopMove) error {
	values := map[string]any{"via": move.Via, "move": move.Move, "previous": move.Previous,
		"requested": move.Requested, "confirmed": move.Confirmed, "detail": move.Detail, "updated_at": time.Now()}
	if move.Sent {
		values["attempts"] = gorm.Expr("attempts + 1")
	}
	return s.conn(ctx).Model(&DailyStop{ID: id}).Updates(values).Error
}

// EndDailyStops ends the stop losses in force but the ones of keep: their
// positions closed, their limits turned off, their day over. It returns how
// many ended.
func (s *Store) EndDailyStops(ctx context.Context, keep []int64) (int64, error) {
	query := s.conn(ctx).Model(&DailyStop{}).Where("ended_at IS NULL")
	if len(keep) > 0 {
		query = query.Where("id NOT IN ?", keep)
	}
	result := query.Update("ended_at", time.Now())
	return result.RowsAffected, result.Error
}

// DailyStopsInForce returns the stop losses of the daily loss limits in force,
// of the agent of id (all of them for 0), by agent, asset and account.
func (s *Store) DailyStopsInForce(ctx context.Context, id int64) ([]DailyStop, error) {
	var stops []DailyStop
	query := s.conn(ctx).Where("ended_at IS NULL")
	if id != 0 {
		query = query.Where("agent_id = ?", id)
	}
	err := query.Order("agent_id, pair, account").Find(&stops).Error
	return stops, err
}

// Request returns the stop as its agent takes it, see ui.DailyStop.
func (d DailyStop) Request() ui.DailyStop {
	scope := "todos os agentes"
	if d.Scope != "" {
		scope = d.Scope
	}
	return ui.DailyStop{Pair: d.Pair, Side: d.Side, Level: d.Level,
		Reason: fmt.Sprintf("limite de perda diária de %s", scope)}
}
