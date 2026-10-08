package main

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLifecycle returns a lifecycle of the tests, of timeout, its context, and
// what it did on its deadline: the processes killed, then its exit code.
func testLifecycle(timeout time.Duration) (*lifecycle, context.Context, func() []string) {
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var done []string
	l := &lifecycle{cancel: cancel, timeout: timeout,
		kill: func() {
			mu.Lock()
			defer mu.Unlock()
			done = append(done, "killed")
		},
		exit: func(code int) {
			mu.Lock()
			defer mu.Unlock()
			done = append(done, "exit "+string(rune('0'+code)))
		}}
	return l, ctx, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), done...)
	}
}

// A server failing stops the controller as a signal does, and it exits with
// an error once stopped in order.
func TestLifecycle_AServerFailingStopsTheController(t *testing.T) {
	l, ctx, deadline := testLifecycle(time.Minute)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, listener.Close()) // its Serve fails at once
	l.serve("the dashboard", &http.Server{ReadHeaderTimeout: time.Second}, listener)

	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the controller doesn't stop")
	}
	stopped := false
	assert.Equal(t, 1, l.stop(func() { stopped = true }))
	assert.True(t, stopped, "stopped in order")
	assert.Empty(t, deadline())
}

// A server closed as the controller stops is no failure.
func TestLifecycle_AServerClosedIsNoFailure(t *testing.T) {
	l, ctx, deadline := testLifecycle(time.Minute)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{ReadHeaderTimeout: time.Second}
	l.serve("the dashboard", srv, listener)
	assert.Equal(t, 0, l.stop(func() { _ = srv.Close() }))
	assert.Never(t, func() bool { return ctx.Err() != nil }, 100*time.Millisecond, 10*time.Millisecond)
	assert.Empty(t, deadline())
}

// R9: a stop past the timeout kills the processes and exits with an error at
// once; one within it doesn't.
func TestLifecycle_TheStopPastItsTimeout(t *testing.T) {
	l, _, deadline := testLifecycle(100 * time.Millisecond)
	release := make(chan struct{})
	ended := make(chan int, 1)
	go func() { ended <- l.stop(func() { <-release }) }() // a process that never stops
	require.Eventually(t, func() bool { return len(deadline()) == 2 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"killed", "exit 1"}, deadline(), "killed, then exits")
	close(release)
	<-ended

	l, _, deadline = testLifecycle(200 * time.Millisecond)
	assert.Equal(t, 0, l.stop(func() { time.Sleep(50 * time.Millisecond) }))
	time.Sleep(300 * time.Millisecond)
	assert.Empty(t, deadline(), "the deadline stopped with the stop")
}
