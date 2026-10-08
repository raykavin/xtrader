package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// sseEvent is one message of the Server-Sent Events stream.
type sseEvent struct {
	name string
	data []byte
}

// broker fans out events to every connected SSE client. Slow clients that
// cannot keep up are dropped instead of blocking the bot.
type broker struct {
	mu          sync.RWMutex
	subscribers map[chan sseEvent]*subscriber
}

// subscriber tells its stream that an event was dropped for it: the stream is
// ended, so that the browser reconnects and loads the dashboard again.
type subscriber struct {
	behind chan struct{} // closed once an event was dropped
	once   sync.Once
}

func newBroker() *broker {
	return &broker{subscribers: make(map[chan sseEvent]*subscriber)}
}

func (b *broker) subscribe() chan sseEvent {
	ch, _ := b.listen()
	return ch
}

// listen subscribes, returning the events and a channel closed once an event
// was dropped because the subscriber was behind. The events channel is never
// closed: publish may be sending to it.
func (b *broker) listen() (chan sseEvent, <-chan struct{}) {
	ch := make(chan sseEvent, 256)
	sub := &subscriber{behind: make(chan struct{})}
	b.mu.Lock()
	b.subscribers[ch] = sub
	b.mu.Unlock()
	return ch, sub.behind
}

func (b *broker) unsubscribe(ch chan sseEvent) {
	b.mu.Lock()
	delete(b.subscribers, ch)
	b.mu.Unlock()
}

func (b *broker) hasSubscribers() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers) > 0
}

// publish sends the event to the subscribers without waiting for any: the
// chart publishes holding c.mu, so that the events of a pair go in the order
// they happen, and never waits for a client, see handleEvents.
func (b *broker) publish(name string, payload interface{}) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.subscribers) == 0 {
		return
	}

	data, err := json.Marshal(payload)
	if err != nil {
		log.Errorf("ui: failed to encode %s event: %v", name, err)
		return
	}

	event := sseEvent{name: name, data: data}
	for ch, sub := range b.subscribers {
		select {
		case ch <- event:
		default:
			// the client is too slow: the event is lost, its stream ended
			sub.once.Do(func() {
				log.Warnf("ui: an events client fell behind (%d events queued), ending its stream", len(ch))
				close(sub.behind)
			})
		}
	}
}

// handleEvents streams candle and order events to the browser.
func (c *Chart) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	ch, behind := c.events.listen()
	defer c.events.unsubscribe(ch)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-behind:
			// an event was lost: the browser reconnects and loads again
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event := <-ch:
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, event.data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
