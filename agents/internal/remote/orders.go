package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/tools/log"
)

// The leases of order IDs: how many at a time, and how few left lease the
// next ones.
const (
	leaseSize  = 1000
	leaseAgain = 200
)

// ordersPage are the orders read from the controller at a time.
const ordersPage = 1000

// orderIDs leases order IDs, see Client.LeaseOrderIDs.
type orderIDs interface {
	LeaseOrderIDs(ctx context.Context, count int64) (agentapi.OrderIDs, error)
}

// Orders is the storage of the orders of an agent launched by the controller
// (storage.Storage): in memory, each change written through the outbox, the
// orders numbered with IDs leased from the controller ahead of their need, so
// CreateOrder never waits for it: the order is at the broker already when it
// is called. The orders are handed as copies.
type Orders struct {
	outbox *Outbox
	ids    orderIDs

	mu      sync.Mutex
	orders  map[int64]model.Order
	next    int64 // the ID of the next order
	last    int64 // the last ID leased
	leasing bool
	warned  bool
}

// NewOrders returns the orders of the agent: the ones of the controller, and
// the ones of the outbox not sent yet, newer, with the IDs leased to it.
func NewOrders(ctx context.Context, client *Client, outbox *Outbox) (*Orders, error) {
	o := &Orders{outbox: outbox, ids: client, orders: make(map[int64]model.Order)}
	for after := int64(0); ; {
		page, err := client.Orders(ctx, after, ordersPage)
		if err != nil {
			return nil, fmt.Errorf("remote: the orders: %w", err)
		}
		for _, order := range page {
			o.orders[order.ID] = order
			after = max(after, order.ID)
		}
		if len(page) < ordersPage {
			break
		}
	}
	pending, err := outbox.Pending(agentapi.KindOrder)
	if err != nil {
		return nil, err
	}
	for _, record := range pending {
		var order model.Order
		if err := json.Unmarshal(record.Payload, &order); err != nil {
			return nil, fmt.Errorf("remote: the order %s of the outbox: %w", record.Key, err)
		}
		o.orders[order.ID] = order
	}

	ids, err := client.LeaseOrderIDs(ctx, leaseSize)
	if err != nil {
		return nil, fmt.Errorf("remote: the order IDs: %w", err)
	}
	o.next, o.last = ids.First, ids.Last
	for id := range o.orders {
		// an order of the outbox numbered past its lease, the controller
		// out of reach, keeps its ID
		o.next = max(o.next, id+1)
	}
	log.Infof("[SETUP] orders of the controller: %d, the next numbered from %d", len(o.orders), o.next)
	return o, nil
}

var _ storage.Storage = (*Orders)(nil)

// CreateOrder numbers the order and keeps it.
func (o *Orders) CreateOrder(order *model.Order) error {
	o.mu.Lock()
	order.ID = o.next
	o.next++
	if order.ID > o.last && !o.warned {
		o.warned = true
		log.Warnf("remote: the order IDs leased are spent, the controller out of reach: numbered past them")
	}
	if !o.leasing && o.last-order.ID < leaseAgain {
		o.leasing = true
		go o.lease()
	}
	o.orders[order.ID] = clone(*order)
	o.mu.Unlock()
	return o.outbox.Add(agentapi.KindOrder, strconv.FormatInt(order.ID, 10), order)
}

// lease leases the next order IDs, retried while the controller is out of
// reach.
func (o *Orders) lease() {
	delay := retryDelay
	for {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		ids, err := o.ids.LeaseOrderIDs(ctx, leaseSize)
		cancel()
		if err == nil {
			o.mu.Lock()
			if ids.Last >= o.next {
				o.next, o.last = max(o.next, ids.First), ids.Last
				o.warned = false
			}
			o.leasing = false
			o.mu.Unlock()
			return
		}
		log.Warnf("remote: the next order IDs: %v, again in %s", err, delay)
		time.Sleep(delay)
		delay = min(2*delay, retryMaxDelay)
	}
}

// UpdateOrder keeps the order as it is, its UpdatedAt the one of the order
// controller.
func (o *Orders) UpdateOrder(order *model.Order) error {
	o.mu.Lock()
	o.orders[order.ID] = clone(*order)
	o.mu.Unlock()
	return o.outbox.Add(agentapi.KindOrder, strconv.FormatInt(order.ID, 10), order)
}

// Orders returns copies of the orders the filters keep, by their update, as
// the storage of the files did.
func (o *Orders) Orders(filters ...storage.OrderFilter) ([]*model.Order, error) {
	o.mu.Lock()
	list := make([]*model.Order, 0, len(o.orders))
	for _, order := range o.orders {
		kept := true
		for _, filter := range filters {
			if !filter(order) {
				kept = false
				break
			}
		}
		if kept {
			order := clone(order)
			list = append(list, &order)
		}
	}
	o.mu.Unlock()
	sort.Slice(list, func(i, j int) bool {
		if a, b := list[i].UpdatedAt, list[j].UpdatedAt; !a.Equal(b) {
			return a.Before(b)
		}
		return list[i].ID < list[j].ID
	})
	return list, nil
}

// clone returns a copy of order sharing nothing with it, as the storage of the
// files read them back: without its candle, never kept.
func clone(order model.Order) model.Order {
	if order.Stop != nil {
		stop := *order.Stop
		order.Stop = &stop
	}
	if order.GroupID != nil {
		group := *order.GroupID
		order.GroupID = &group
	}
	order.Candle = model.Candle{}
	return order
}
