package storage

import (
	"time"

	"github.com/raykavin/xtrader/model"
)

type OrderFilter func(model.Order) bool

type Storage interface {
	CreateOrder(order *model.Order) error
	UpdateOrder(order *model.Order) error
	Orders(filters ...OrderFilter) ([]*model.Order, error)
}

// DiscardStorage is a Storage keeping the positions the order controller
// discarded, see model.Discard: CreateDiscard keeps one, once for its pair and
// its AfterID; Discards returns them all.
type DiscardStorage interface {
	CreateDiscard(discard model.Discard) error
	Discards() ([]model.Discard, error)
}

func WithStatusIn(status ...model.OrderStatusType) OrderFilter {
	return func(order model.Order) bool {
		for _, s := range status {
			if s == order.Status {
				return true
			}
		}
		return false
	}
}

func WithStatus(status model.OrderStatusType) OrderFilter {
	return func(order model.Order) bool {
		return order.Status == status
	}
}

func WithPair(pair string) OrderFilter {
	return func(order model.Order) bool {
		return order.Pair == pair
	}
}

func WithUpdateAtBeforeOrEqual(time time.Time) OrderFilter {
	return func(order model.Order) bool {
		return !order.UpdatedAt.After(time)
	}
}
