package controller

import (
	"reflect"
	"sync"

	"github.com/raykavin/xtrader/agentapi"
)

// runtimes are the runtime documents of the agents, what each one applies
// while it runs (see agentapi.Runtime), set by the Manager: a change gives the
// document a new version, and wakes the agents waiting for one.
type runtimes struct {
	mu      sync.Mutex
	version int64 // the last one given, of any agent
	docs    map[int64]agentapi.Runtime
	changed map[int64]chan struct{} // closed on the next change of the agent
}

func newRuntimes() *runtimes {
	return &runtimes{docs: make(map[int64]agentapi.Runtime), changed: make(map[int64]chan struct{})}
}

// set makes doc the runtime of the agent of id, a new version when it differs
// from the one it had.
func (r *runtimes) set(id int64, doc agentapi.Runtime) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.docs[id]; ok {
		doc.Version = old.Version
		if sameRuntime(old, doc) {
			return
		}
	}
	r.version++
	doc.Version = r.version
	r.docs[id] = doc
	if ch, ok := r.changed[id]; ok {
		close(ch)
	}
	r.changed[id] = make(chan struct{})
}

// get returns the runtime of the agent of id, false for none, and a channel
// closed on its next change.
func (r *runtimes) get(id int64) (agentapi.Runtime, <-chan struct{}, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	doc, ok := r.docs[id]
	ch, found := r.changed[id]
	if !found {
		ch = make(chan struct{})
		r.changed[id] = ch
	}
	return doc, ch, ok
}

// sameRuntime reports whether two runtimes tell the agent the same.
func sameRuntime(a, b agentapi.Runtime) bool {
	switch {
	case a.TradePanel != b.TradePanel || a.TelegramControl != b.TelegramControl || a.Reanalysis != b.Reanalysis,
		!reflect.DeepEqual(a.Entries, b.Entries), !reflect.DeepEqual(a.Protection, b.Protection),
		!reflect.DeepEqual(a.News, b.News):
		return false
	case a.Prompt == nil || b.Prompt == nil:
		return a.Prompt == b.Prompt
	}
	return *a.Prompt == *b.Prompt
}
