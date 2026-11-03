package agentapi

import (
	"slices"
	"sync"
)

// PairGate tells the assets an agent may open positions on now: all of them,
// or only the ones of the window of the trading hours open (see
// Entries.EntriesPairs), set by the controller (or the backtest) and asked by
// the strategy of the agent before each entry. Safe for concurrent use; the
// zero value allows every asset.
type PairGate struct {
	mu       sync.Mutex
	revision int64
	pairs    []string // nil for all of them
	reason   string
}

// Set sets the assets of the gate at revision (nil for all of them) and why,
// an older revision ignored: whether it was taken.
func (g *PairGate) Set(revision int64, pairs []string, reason string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if revision < g.revision {
		return false
	}
	g.revision, g.pairs, g.reason = revision, slices.Clone(pairs), reason
	return true
}

// Allows reports whether an entry on pair is allowed now, and why not.
func (g *PairGate) Allows(pair string) (bool, string) {
	if g == nil {
		return true, ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.pairs) == 0 || slices.Contains(g.pairs, pair) {
		return true, ""
	}
	return false, g.reason
}
