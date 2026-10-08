package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stuckStream is the ResponseWriter of an events client that stopped reading:
// its first Write, the ": connected" of the stream, goes through, the next
// ones block until released.
type stuckStream struct {
	header  http.Header
	writes  atomic.Int32
	writing chan struct{} // closed by the first Write that blocks
	release chan struct{}
	once    sync.Once
	closing sync.Once
}

func newStuckStream(t *testing.T) *stuckStream {
	w := &stuckStream{header: make(http.Header), writing: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(w.unblock)
	return w
}

func (w *stuckStream) Header() http.Header { return w.header }

func (w *stuckStream) WriteHeader(int) {}

func (w *stuckStream) Write(p []byte) (int, error) {
	if w.writes.Add(1) > 1 {
		w.once.Do(func() { close(w.writing) })
		<-w.release
	}
	return len(p), nil
}

func (w *stuckStream) Flush() {}

func (w *stuckStream) unblock() { w.closing.Do(func() { close(w.release) }) }

// B32: a client too slow for the events loses some, so its stream is ended:
// the browser reconnects and loads the dashboard again, rather than staying
// out of date for good with a stream that looks alive.
func TestEvents_ClientBehindHasItsStreamEnded(t *testing.T) {
	c := newTestChart(t)
	client := newStuckStream(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // the stream of the code without the fix never ends
	served := make(chan struct{})
	go func() {
		defer close(served)
		req := httptest.NewRequest(http.MethodGet, "http://localhost/api/events", nil).WithContext(ctx)
		c.Handler().ServeHTTP(client, req)
	}()
	require.Eventually(t, c.events.hasSubscribers, 2*time.Second, 5*time.Millisecond)

	c.events.publish("notification", NotificationEvent{Message: "first"})
	waitFor(t, client.writing, "the first event written")
	within(t, 2*time.Second, "publishing to a client behind", func() {
		for i := 0; i < 300; i++ {
			c.events.publish("notification", NotificationEvent{Message: "more"})
		}
	})

	client.unblock()
	waitFor(t, served, "the stream of a client behind ended")
	assert.False(t, c.events.hasSubscribers(), "unsubscribed")
}

// A subscriber that never reads holds no publisher, even many at once, and is
// told once that it is behind; its events channel stays open, since the
// publishers may still be sending to it.
func TestBroker_PublishNeverWaitsForASubscriberBehind(t *testing.T) {
	b := newBroker()
	ch, behind := b.listen()
	other, otherBehind := b.listen()

	within(t, 2*time.Second, "publishing", func() {
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range cap(ch) {
					b.publish("candle", i)
				}
			}()
		}
		wg.Wait()
	})

	waitFor(t, behind, "told behind")
	waitFor(t, otherBehind, "the other told behind")
	assert.Len(t, ch, cap(ch))

	// once unsubscribed, the publishers leave it
	b.unsubscribe(other)
	<-other
	within(t, time.Second, "publishing after unsubscribing", func() { b.publish("candle", 0) })
	assert.Len(t, other, cap(other)-1)
	b.unsubscribe(ch)
	assert.False(t, b.hasSubscribers())
}

// A subscriber keeping up is never told behind.
func TestBroker_SubscriberKeepingUpIsNotBehind(t *testing.T) {
	b := newBroker()
	ch, behind := b.listen()
	defer b.unsubscribe(ch)

	for i := range 3 * cap(ch) {
		b.publish("candle", i)
		<-ch
	}
	select {
	case <-behind:
		t.Fatal("told behind while keeping up")
	default:
	}
}
