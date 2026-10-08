package storage

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/tidwall/buntdb"

	"github.com/raykavin/xtrader/model"
)

type Bunt struct {
	lastID int64
	db     *buntdb.DB
}

func FromMemory() (Storage, error) {
	return newBunt(":memory:")
}

func FromFile(file string) (Storage, error) {
	return newBunt(file)
}

func newBunt(sourceFile string) (Storage, error) {
	db, err := buntdb.Open(sourceFile)
	if err != nil {
		return nil, err
	}

	err = db.CreateIndex("update_index", "*", buntdb.IndexJSON("updated_at"))
	if err != nil {
		return nil, err
	}

	// the ids go on from the last one of the file: from 1 again, the orders
	// of a restart would overwrite the ones of the previous runs
	var lastID int64
	err = db.View(func(tx *buntdb.Tx) error {
		return tx.AscendKeys("*", func(key, _ string) bool {
			if id, err := strconv.ParseInt(key, 10, 64); err == nil && id > lastID {
				lastID = id
			}
			return true
		})
	})
	if err != nil {
		return nil, err
	}

	return &Bunt{
		lastID: lastID,
		db:     db,
	}, nil
}

func (b *Bunt) getID() int64 {
	return atomic.AddInt64(&b.lastID, 1)
}

func (b *Bunt) CreateOrder(order *model.Order) error {
	return b.db.Update(func(tx *buntdb.Tx) error {
		order.ID = b.getID()
		content, err := orderJSON(*order)
		if err != nil {
			return err
		}

		_, _, err = tx.Set(strconv.FormatInt(order.ID, 10), string(content), nil)
		return err
	})
}

// UpdateOrder writes order under its id, unless the order stored ended (see
// model.OrderStatusType.Final) and order is open: an end is never undone, eg:
// by a move the exchange answered as the order filled, logged and not written.
// Unlike the database of the agents, the times aren't compared: the order
// controller writes the states of an order in their order.
func (b *Bunt) UpdateOrder(order *model.Order) error {
	return b.db.Update(func(tx *buntdb.Tx) error {
		id := strconv.FormatInt(order.ID, 10)
		if value, err := tx.Get(id); err == nil && !order.Status.Final() {
			var stored model.Order
			if json.Unmarshal([]byte(value), &stored) == nil && stored.Status.Final() {
				log.Printf("storage: the order %d (%s %s of %s) %s, never %s again: not written", order.ID,
					order.Type, order.Side, order.Pair, stored.Status, order.Status)
				return nil
			}
		}

		content, err := orderJSON(*order)
		if err != nil {
			return err
		}

		_, _, err = tx.Set(id, string(content), nil)
		return err
	})
}

// orderJSON returns the JSON of order, its numbers that aren't finite, eg: the
// profit of a fill without a price, written as 0: JSON can't write them, and
// the order executed would never be stored.
func orderJSON(order model.Order) ([]byte, error) {
	stored, fields := order.Finite()
	if len(fields) > 0 {
		log.Printf("storage: the order %d (%s %s of %s): %s not finite, stored as 0", order.ID, order.Type,
			order.Side, order.Pair, strings.Join(fields, ", "))
	}
	return json.Marshal(stored)
}

func (b *Bunt) Orders(filters ...OrderFilter) ([]*model.Order, error) {
	orders := make([]*model.Order, 0)
	err := b.db.View(func(tx *buntdb.Tx) error {
		err := tx.Ascend("update_index", func(_, value string) bool {
			var order model.Order
			err := json.Unmarshal([]byte(value), &order)
			if err != nil {
				log.Println(err)
				return true
			}

			for _, filter := range filters {
				if ok := filter(order); !ok {
					return true
				}
			}

			orders = append(orders, &order)

			return true
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return orders, nil
}
