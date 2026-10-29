package controller

import (
	"context"
	"sync"
	"time"
)

// rangeReads shares the reads of ranges of the deals of the accounts between
// the requests of the dashboards, eg: the closed profit, the performance: a
// read of a range at a time, its answer kept for a while; the reads of the
// other ranges, older than a minute, dropped.
type rangeReads[T any] struct {
	mu    sync.Mutex
	reads map[closedRange]*rangeRead[T]
}

// rangeRead is a read of a range, see rangeReads.
type rangeRead[T any] struct {
	response *T
	at       time.Time
	reading  chan struct{} // closed when the read in progress ends, nil without one
}

// read returns the answer of window, read with readAll once the last read of
// it is ttl old, a read of it at a time for all the requests; a read goes on
// for the others when the request that started it ends, bounded by
// 2*bridgeTimeout+1s.
func (c *rangeReads[T]) read(ctx context.Context, window closedRange, ttl time.Duration,
	readAll func(context.Context, closedRange) (T, error)) (T, error) {
	var zero T
	for {
		c.mu.Lock()
		if c.reads == nil {
			c.reads = make(map[closedRange]*rangeRead[T])
		}
		for key, read := range c.reads {
			if read.reading == nil && time.Since(read.at) > time.Minute {
				delete(c.reads, key)
			}
		}
		read := c.reads[window]
		if read == nil {
			read = &rangeRead[T]{}
			c.reads[window] = read
		}
		if read.response != nil && time.Since(read.at) < ttl {
			response := *read.response
			c.mu.Unlock()
			return response, nil
		}
		if wait := read.reading; wait != nil {
			c.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return zero, ctx.Err()
			}
		}
		done := make(chan struct{})
		read.reading = done
		c.mu.Unlock()

		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*bridgeTimeout+time.Second)
		response, err := readAll(readCtx, window)
		cancel()
		c.mu.Lock()
		if err == nil {
			read.response, read.at = &response, time.Now()
		}
		read.reading = nil
		close(done)
		c.mu.Unlock()
		return response, err
	}
}
