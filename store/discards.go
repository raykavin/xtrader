package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm/clause"

	"github.com/raykavin/xtrader/model"
)

// discardRow is a position of an agent its order controller discarded, in
// its table: model.Discard with its agent.
type discardRow struct {
	AgentID  int64  `gorm:"primaryKey;autoIncrement:false"`
	Pair     string `gorm:"primaryKey"`
	AfterID  int64  `gorm:"primaryKey;autoIncrement:false"`
	Side     string
	Quantity float64
	At       time.Time
}

func (discardRow) TableName() string { return "position_discards" }

// SaveDiscard keeps a position the order controller of the agent of id
// discarded: once, the one of the same pair after the same fill kept as it
// is, eg: sent again by the outbox.
func (s *Store) SaveDiscard(ctx context.Context, id int64, discard model.Discard) error {
	if discard.Pair == "" || discard.AfterID < 1 || discard.Quantity <= 0 ||
		(discard.Side != model.SideTypeBuy && discard.Side != model.SideTypeSell) {
		return fmt.Errorf("%w: the discard of a position: pair, side, quantity or fill after", ErrInvalid)
	}
	row := discardRow{AgentID: id, Pair: discard.Pair, AfterID: discard.AfterID, Side: string(discard.Side),
		Quantity: discard.Quantity, At: discard.At.UTC()}
	return s.conn(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

// Discards returns the positions the order controller of the agent of id
// discarded, by the fill after which each one was.
func (s *Store) Discards(ctx context.Context, id int64) ([]model.Discard, error) {
	var rows []discardRow
	if err := s.conn(ctx).Where("agent_id = ?", id).Order("after_id, pair").Find(&rows).Error; err != nil {
		return nil, err
	}
	discards := make([]model.Discard, 0, len(rows))
	for _, row := range rows {
		discards = append(discards, model.Discard{Pair: row.Pair, Side: model.SideType(row.Side),
			Quantity: row.Quantity, AfterID: row.AfterID, At: row.At})
	}
	return discards, nil
}
