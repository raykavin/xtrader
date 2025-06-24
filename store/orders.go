package store

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"github.com/raykavin/xtrader/model"
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

// SaveOrders writes the orders of the agent of id: each one replacing the one
// of its ID, or, unless overwrite, kept only when missing (an import never
// replaces what the agent wrote since).
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
		conflict = clause.OnConflict{Columns: conflict.Columns, DoUpdates: clause.AssignmentColumns(orderColumns)}
	}
	return s.conn(ctx).Clauses(conflict).Create(&rows).Error
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
