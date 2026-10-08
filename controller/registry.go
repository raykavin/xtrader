package controller

import (
	"errors"
	"fmt"
	"sync"
)

// registry holds the agents the dashboard serves and Telegram relays, in the
// order of the dashboard: replaced as the agents of the database change, see
// Manager. An agent is never changed once in it, a new one taking its place,
// so the requests in flight keep theirs.
type registry struct {
	keys *agentKeys // of their API, see newAgentProxy

	mu     sync.RWMutex
	agents []*agent
	byName map[string]*agent
}

func newRegistry(keys *agentKeys) *registry {
	return &registry{keys: keys, byName: make(map[string]*agent)}
}

// replace makes the valid agents of list the agents, keeping the proxy of the
// ones whose address didn't change: the invalid ones are left out, told by
// the error, the others served anyway.
func (r *registry) replace(list []Agent) error {
	agents := make([]*agent, 0, len(list))
	byName := make(map[string]*agent, len(list))
	r.mu.RLock()
	previous := r.byName
	r.mu.RUnlock()
	var errs []error
	for _, a := range list {
		switch {
		case !agentNameRe.MatchString(a.Name):
			errs = append(errs, fmt.Errorf("ui: invalid agent name %q: letters, digits, '.', '_' and '-' only", a.Name))
			continue
		case byName[a.Name] != nil:
			errs = append(errs, fmt.Errorf("ui: agent %s is listed twice", a.Name))
			continue
		}
		if old := previous[a.Name]; old != nil && old.Address == a.Address {
			kept := &agent{Agent: a, api: old.api, proxy: old.proxy}
			agents = append(agents, kept)
			byName[a.Name] = kept
			continue
		}
		api, err := agentAPI(a.Address)
		if err != nil {
			errs = append(errs, fmt.Errorf("ui: agent %s: %w", a.Name, err))
			continue
		}
		created := &agent{Agent: a, api: api, proxy: newAgentProxy(a, api, r.keys)}
		agents = append(agents, created)
		byName[a.Name] = created
	}
	r.mu.Lock()
	r.agents, r.byName = agents, byName
	r.mu.Unlock()
	return errors.Join(errs...)
}

// get returns the agent of name.
func (r *registry) get(name string) (*agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.byName[name]
	return a, ok
}

// list returns the agents, in the order of the dashboard.
func (r *registry) list() []*agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]*agent(nil), r.agents...)
}
