package model

import (
	"context"
	"sync"
)

// PriorityQueue is a heap of items, the least first (see Item.Less), safe for
// concurrent use. PopLock hands its items to a consumer as they are pushed.
type PriorityQueue struct {
	sync.Mutex
	length int
	data   []Item
	ready  chan struct{} // signaled by Push for the consumer of PopLock, nil until PopLock
	items  chan Item     // see PopLock, nil until PopLock
}

type Item interface {
	Less(Item) bool
}

func NewPriorityQueue(data []Item) *PriorityQueue {
	q := &PriorityQueue{}
	q.data = data
	q.length = len(data)
	if q.length > 0 {
		i := q.length >> 1
		for ; i >= 0; i-- {
			q.down(i)
		}
	}
	return q
}

func (q *PriorityQueue) Push(item Item) {
	q.Lock()
	q.push(item)
	ready := q.ready
	q.Unlock()

	if ready != nil {
		select {
		case ready <- struct{}{}:
		default: // signaled already
		}
	}
}

// push adds item to the heap. The caller holds the lock.
func (q *PriorityQueue) push(item Item) {
	q.data = append(q.data, item)
	q.length++
	q.up(q.length - 1)
}

// PopLock returns a channel handing the items of the queue, the ones pushed
// before included, as they come, until ctx is done, when it is closed. A
// single goroutine pops them, one at a time, as the consumer takes the
// previous one: two items queued together come out in their order, eg: the
// close of a candle before the next candle in progress, never the other way
// around, as when each push popped an item in a goroutine of its own racing
// the others to the channel. The item popped and not taken by the time ctx is
// done goes back to the queue. Called again, it returns the same channel: the
// items have one consumer.
func (q *PriorityQueue) PopLock(ctx context.Context) <-chan Item {
	q.Lock()
	defer q.Unlock()

	if q.items != nil {
		return q.items
	}
	q.ready = make(chan struct{}, 1)
	q.items = make(chan Item)
	if q.length > 0 {
		q.ready <- struct{}{}
	}
	go q.hand(ctx, q.ready, q.items)
	return q.items
}

// hand hands the items of the queue to items, in their order, see PopLock.
func (q *PriorityQueue) hand(ctx context.Context, ready <-chan struct{}, items chan<- Item) {
	defer close(items)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ready:
		}
		for item := q.Pop(); item != nil; item = q.Pop() {
			select {
			case items <- item:
			case <-ctx.Done():
				q.Lock()
				q.push(item) // not taken: left in the queue
				q.Unlock()
				return
			}
		}
	}
}

func (q *PriorityQueue) Pop() Item {
	q.Lock()
	defer q.Unlock()

	if q.length == 0 {
		return nil
	}
	top := q.data[0]
	q.length--
	if q.length > 0 {
		q.data[0] = q.data[q.length]
		q.down(0)
	}
	q.data = q.data[:len(q.data)-1]
	return top
}

func (q *PriorityQueue) Peek() Item {
	q.Lock()
	defer q.Unlock()

	if q.length == 0 {
		return nil
	}
	return q.data[0]
}

func (q *PriorityQueue) Len() int {
	q.Lock()
	defer q.Unlock()

	return q.length
}
func (q *PriorityQueue) down(pos int) {
	data := q.data
	halfLength := q.length >> 1
	item := data[pos]
	for pos < halfLength {
		left := (pos << 1) + 1
		right := left + 1
		best := data[left]
		if right < q.length && data[right].Less(best) {
			left = right
			best = data[right]
		}
		if !best.Less(item) {
			break
		}
		data[pos] = best
		pos = left
	}
	data[pos] = item
}

func (q *PriorityQueue) up(pos int) {
	data := q.data
	item := data[pos]
	for pos > 0 {
		parent := (pos - 1) >> 1
		current := data[parent]
		if !item.Less(current) {
			break
		}
		data[pos] = current
		pos = parent
	}
	data[pos] = item
}
