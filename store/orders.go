package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm/clause"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/tools/log"
)

// orderRow is an order of an agent in its table: model.Order with its agent,
// the timestamps written as the agent set them.
type orderRow struct {
	AgentID     int64 `gorm:"primaryKey;autoIncrement:false"`
	ID          int64 `gorm:"primaryKey;autoIncrement:false"`
	ExchangeID  int64
	Pair        string
	Side        string
	Type        string
	Status      string
	Price       float64
	Quantity    float64
	Fee         float64
	Stop        *float64
	GroupID     *int64
	RefPrice    float64
	Profit      float64
	ProfitValue float64
	CreatedAt   time.Time `gorm:"autoCreateTime:false"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime:false"`
}

func (orderRow) TableName() string { return "orders" }

// orderColumns are the columns an order rewrites: all but its keys and
// creation.
var orderColumns = []string{"exchange_id", "pair", "side", "type", "status", "price", "quantity", "fee", "stop",
	"group_id", "ref_price", "profit", "profit_value", "updated_at"}

func rowOfOrder(agentID int64, o model.Order) orderRow {
	return orderRow{AgentID: agentID, ID: o.ID, ExchangeID: o.ExchangeID, Pair: o.Pair, Side: string(o.Side),
		Type: string(o.Type), Status: string(o.Status), Price: o.Price, Quantity: o.Quantity, Fee: o.Fee,
		Stop: o.Stop, GroupID: o.GroupID, RefPrice: o.RefPrice, Profit: o.Profit, ProfitValue: o.ProfitValue,
		CreatedAt: o.CreatedAt.UTC(), UpdatedAt: o.UpdatedAt.UTC()}
}

func (r orderRow) order() model.Order {
	return model.Order{ID: r.ID, ExchangeID: r.ExchangeID, Pair: r.Pair, Side: model.SideType(r.Side),
		Type: model.OrderType(r.Type), Status: model.OrderStatusType(r.Status), Price: r.Price,
		Quantity: r.Quantity, Fee: r.Fee, Stop: r.Stop, GroupID: r.GroupID, RefPrice: r.RefPrice,
		Profit: r.Profit, ProfitValue: r.ProfitValue, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// finalStatuses are the statuses that end an order, see
// model.OrderStatusType.Final.
var finalStatuses = []string{string(model.OrderStatusTypeFilled), string(model.OrderStatusTypeCanceled),
	string(model.OrderStatusTypeRejected), string(model.OrderStatusTypeExpired)}

// laterState is the condition of an order replacing the one of its ID, as
// model.Order.Supersedes tells: a final status never goes back to an open
// one, and replaces an open one whatever their times; else updated_at never
// goes back.
var laterState = clause.Where{Exprs: []clause.Expression{clause.Expr{
	SQL: "CASE WHEN (EXCLUDED.status IN ?) = (orders.status IN ?) THEN orders.updated_at <= EXCLUDED.updated_at " +
		"ELSE EXCLUDED.status IN ? END",
	Vars: []interface{}{finalStatuses, finalStatuses, finalStatuses},
}}}

// SaveOrders writes the orders of the agent of id: each one replacing the one
// of its ID when it is a later state of it (see model.Order.Supersedes: the
// copy of an order a bot wrote late never takes back a fill written before
// it, the same state written again changing nothing), or, unless overwrite,
// kept only when missing (an import never replaces what the agent wrote
// since).
func (s *Store) SaveOrders(ctx context.Context, id int64, orders []model.Order, overwrite bool) error {
	if len(orders) == 0 {
		return nil
	}
	rows := make([]orderRow, 0, len(orders))
	for _, o := range orders {
		rows = append(rows, rowOfOrder(id, o))
	}
	conflict := clause.OnConflict{Columns: []clause.Column{{Name: "agent_id"}, {Name: "id"}}, DoNothing: true}
	if overwrite {
		conflict = clause.OnConflict{Columns: conflict.Columns, DoUpdates: clause.AssignmentColumns(orderColumns),
			Where: laterState}
	}
	result := s.conn(ctx).Clauses(conflict).Create(&rows)
	if result.Error != nil {
		return result.Error
	}
	if kept := len(rows) - int(result.RowsAffected); overwrite && kept > 0 {
		log.Warnf("store: agent %d: %s: a later state written before", id, s.keptStates(ctx, id, rows, kept))
	}
	return nil
}

// keptStates tells the states of rows SaveOrders left out, kept of their
// orders as stored, kept of them: the ones whose stored state isn't theirs.
func (s *Store) keptStates(ctx context.Context, id int64, rows []orderRow, kept int) string {
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	var stored []orderRow
	if err := s.conn(ctx).Where("agent_id = ? AND id IN ?", id, ids).Find(&stored).Error; err != nil {
		return fmt.Sprintf("%d of the %d orders written kept as stored (%v)", kept, len(rows), err)
	}
	byID := make(map[int64]orderRow, len(stored))
	for _, row := range stored {
		byID[row.ID] = row
	}
	var states []string
	for _, row := range rows {
		was, ok := byID[row.ID]
		if !ok || (was.Status == row.Status && was.UpdatedAt.Sub(row.UpdatedAt).Abs() < time.Microsecond) {
			continue // written
		}
		states = append(states, fmt.Sprintf("the order %d, %s at %s, kept %s at %s", row.ID, row.Status,
			row.UpdatedAt.Format(time.RFC3339Nano), was.Status, was.UpdatedAt.UTC().Format(time.RFC3339Nano)))
	}
	return strings.Join(states, "; ")
}

// Orders returns at most limit orders of the agent of id after the order of
// ID after, by ID.
func (s *Store) Orders(ctx context.Context, id, after int64, limit int) ([]model.Order, error) {
	var rows []orderRow
	err := s.conn(ctx).Where("agent_id = ? AND id > ?", id, after).Order("id").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	orders := make([]model.Order, 0, len(rows))
	for _, row := range rows {
		orders = append(orders, row.order())
	}
	return orders, nil
}
