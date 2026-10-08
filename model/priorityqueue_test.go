package model

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPriorityQueue(t *testing.T) {
	now := time.Now()
	pq := NewPriorityQueue(nil)
	require.Nil(t, pq.Pop())

	pq.Push(Candle{Time: now, Close: 2})
	pq.Push(Candle{Time: now.Add(time.Minute), Close: 6, Pair: "D"})
	pq.Push(Candle{Time: now.Add(time.Minute), Close: 5, Pair: "C"})
	pq.Push(Candle{Time: now.Add(time.Minute), Close: 4, Pair: "B"})
	pq.Push(Candle{Time: now.Add(time.Minute), Close: 3, Pair: "A"})
	pq.Push(Candle{Time: now.Add(-time.Minute), Close: 1})

	require.Equal(t, 1.0, pq.Pop().(Candle).Close)
	require.Equal(t, 2.0, pq.Pop().(Candle).Close)
	require.Equal(t, 3.0, pq.Pop().(Candle).Close)
	require.Equal(t, 4.0, pq.Pop().(Candle).Close)
	require.Equal(t, 5.0, pq.Pop().(Candle).Close)
	require.Equal(t, 6.0, pq.Pop().(Candle).Close)
}

func TestPriorityQueue_Peek(t *testing.T) {
	pq := &PriorityQueue{}
	require.Nil(t, pq.Peek())

	pq = NewPriorityQueue([]Item{Candle{Pair: "A"}})
	require.Equal(t, "A", pq.Peek().(Candle).Pair)
}

func TestPriorityQueue_Len(t *testing.T) {
	pq := &PriorityQueue{}
	require.Zero(t, pq.Len())

	pq = NewPriorityQueue([]Item{Candle{Pair: "A"}})
	require.Equal(t, 1, pq.Len())
}

func TestPriorityQueue_PopLock(t *testing.T) {
	now := time.Now()
	queue := NewPriorityQueue(nil)
	queue.Push(Candle{Pair: "C", Time: now.Add(2 * time.Minute)}) // pushed before PopLock: handed too

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	items := queue.PopLock(ctx)
	require.Equal(t, items, queue.PopLock(ctx), "a single consumer")
	require.Equal(t, "C", (<-items).(Candle).Pair)

	queue.Push(Candle{Pair: "B", Time: now.Add(time.Minute)})
	require.Equal(t, "B", (<-items).(Candle).Pair)
	queue.Push(Candle{Pair: "A", Time: now})
	require.Equal(t, "A", (<-items).(Candle).Pair)
	require.Zero(t, queue.Len())
}

// B27: the items come out in their order, one consumer popping them: the close
// of a candle is never handed after the next candle in progress, which made
// the strategy drop the close as late (strategy.Controller.OnCandle), eg: no
// analysis of the LLM on it.
func TestPriorityQueue_PopLockNeverInvertsACloseAndTheNextPartial(t *testing.T) {
	queue := NewPriorityQueue(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	items := queue.PopLock(ctx)

	const n = 20000
	base := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	pushed := make([]Candle, 0, 2*n)
	for i := 0; i < n; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		pushed = append(pushed,
			Candle{Pair: "A", Time: at, UpdatedAt: at.Add(time.Minute - time.Second), Complete: true},
			Candle{Pair: "A", Time: at.Add(time.Minute), UpdatedAt: at.Add(time.Minute)})
	}
	go func() {
		for _, candle := range pushed {
			queue.Push(candle)
		}
	}()

	inverted := 0
	for i := range pushed {
		got := (<-items).(Candle)
		if !got.Time.Equal(pushed[i].Time) || got.Complete != pushed[i].Complete {
			inverted++
		}
	}
	require.Zero(t, inverted, "candles handed out of their order, of %d", len(pushed))
}

// The channel of PopLock closes once ctx is done, the item popped and not
// taken back in the queue: no goroutine left blocked on it, nor item lost.
func TestPriorityQueue_PopLockEndsWithItsContext(t *testing.T) {
	now := time.Now()
	queue := NewPriorityQueue(nil)
	ctx, cancel := context.WithCancel(context.Background())
	items := queue.PopLock(ctx)

	queue.Push(Candle{Pair: "A", Time: now})
	require.Eventually(t, func() bool { return queue.Len() == 0 }, time.Second, time.Millisecond,
		"popped, waiting for the consumer")
	cancel()

	select {
	case _, open := <-items:
		if open { // taken as ctx was done: either way, never lost
			_, open = <-items
		}
		require.False(t, open)
	case <-time.After(time.Second):
		t.Fatal("the channel of PopLock never closed")
	}
	if queue.Len() == 1 {
		require.Equal(t, "A", queue.Pop().(Candle).Pair, "back in the queue")
	}
}
