package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The exposure limit of the agents (see ui.ExposureSettings) is weighed by the
// controller as each agent claims an entry before it sends it (see
// agentapi.ExposureClaim): the agents holding a position or a pending entry on
// the asset in the direction, as their accounts tell them, and the ones the
// controller let in a moment ago whose order the accounts don't show yet (the
// claims), one claim at a time, so that agents entering at the same second
// never pass the limit together.
const (
	// claimHeld is how long an entry granted holds its place before its order
	// shows on the account: long enough for an order sent and filled, short
	// enough that one never sent frees it
	claimHeld = 2 * time.Minute
	// exposureReadFor is how long a read of the accounts serves the claims, the
	// claims covering what it misses
	exposureReadFor = 2 * time.Second
)

// exposureKey is an asset in a direction (BUY or SELL).
type exposureKey struct{ pair, side string }

// exposureGuard weighs the claims of the entries of the agents, see the
// package doc above.
type exposureGuard struct {
	store     *store.Store
	positions *openPositions
	now       func() time.Time

	mu       sync.Mutex
	claims   map[exposureKey]map[int64]time.Time // the agents granted, by ID, when
	held     map[exposureKey]map[int64]string    // as last read: the agents on each, their names
	heldAt   time.Time
	heldGaps []string // the accounts the last read missed
}

func newExposureGuard(s *store.Store, positions *openPositions) *exposureGuard {
	return &exposureGuard{store: s, positions: positions, now: time.Now,
		claims: make(map[exposureKey]map[int64]time.Time)}
}

// claim weighs the entry of the agent of id on pair on side: granted when the
// limit is off for pair, the agent in paper (no position on an account, so
// none held for it either), the agent already on it, or fewer agents than the
// limit on it (its place then held, see claimHeld); refused else, with why,
// and when the accounts can't be read (nothing let in blind).
func (g *exposureGuard) claim(ctx context.Context, id int64, pair, side string) (bool, string, error) {
	limits, _, err := g.store.ExposureLimits(ctx)
	if err != nil {
		return false, "", err
	}
	limit := limits.LimitOf(pair)
	if limit <= 0 {
		return true, "", nil
	}
	a, err := g.store.Agent(ctx, id)
	if err != nil {
		return false, "", err
	}
	if a.Mode != "live" {
		return true, "", nil
	}
	key := exposureKey{pair: pair, side: strings.ToUpper(side)}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.read(ctx); err != nil {
		return false, "", err
	}
	if len(g.heldGaps) > 0 {
		return false, "Não foi possível conferir a exposição: sem leitura de " + strings.Join(g.heldGaps, "; "), nil
	}
	on := make(map[int64]string)
	for agent, name := range g.held[key] {
		on[agent] = name
	}
	now := g.now()
	for agent, at := range g.claims[key] {
		if now.Sub(at) > claimHeld {
			delete(g.claims[key], agent)
			continue
		}
		if _, found := on[agent]; !found {
			on[agent] = ""
		}
	}
	if _, found := on[id]; !found && len(on) >= limit {
		return false, exposureReason(ctx, g.store, pair, key.side, limit, on), nil
	}
	if g.claims[key] == nil {
		g.claims[key] = make(map[int64]time.Time)
	}
	g.claims[key][id] = now
	return true, "", nil
}

// read reads the agents on each asset in each direction from their accounts,
// unless the last read is younger than exposureReadFor: a position, or a
// pending order of an agent without a position on its asset (one with it
// protects or adds to it). Under mu.
func (g *exposureGuard) read(ctx context.Context) error {
	if g.held != nil && g.now().Sub(g.heldAt) < exposureReadFor {
		return nil
	}
	accounts, reads, err := g.positions.readFresh(ctx)
	if err != nil {
		return err
	}
	held := make(map[exposureKey]map[int64]string)
	add := func(key exposureKey, a store.Agent) {
		if held[key] == nil {
			held[key] = make(map[int64]string)
		}
		held[key][a.ID] = a.Name
	}
	var gaps []string
	for i, read := range reads {
		if read.err != nil {
			gaps = append(gaps, accounts[i].bridge)
			continue
		}
		holding := make(map[string]bool) // agent ID and pair holding a position
		for _, p := range read.positions {
			a, ok := ownerOf(accounts[i].agents, p.Magic, p.Symbol)
			if !ok {
				continue // by hand
			}
			side := "BUY"
			if p.Type == 1 {
				side = "SELL"
			}
			add(exposureKey{pair: p.Symbol, side: side}, a)
			holding[fmt.Sprint(a.ID, p.Symbol)] = true
		}
		for _, o := range read.orders {
			a, ok := ownerOf(accounts[i].agents, o.Magic, o.Symbol)
			if !ok || holding[fmt.Sprint(a.ID, o.Symbol)] {
				continue
			}
			add(exposureKey{pair: o.Symbol, side: o.side()}, a)
		}
	}
	g.held, g.heldAt, g.heldGaps = held, g.now(), gaps
	return nil
}

// exposureReason returns why an entry on pair on side is refused: the limit
// reached, by the agents on it (their names, the ones granted a moment ago
// read from the store).
func exposureReason(ctx context.Context, s *store.Store, pair, side string, limit int, on map[int64]string) string {
	var names []string
	for id, name := range on {
		if name == "" {
			if a, err := s.Agent(ctx, id); err == nil {
				name = a.Name
			}
		}
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	held := "comprados"
	if side == "SELL" {
		held = "vendidos"
	}
	agents := "agente já está " + strings.TrimSuffix(held, "s")
	if limit != 1 {
		agents = "agentes já estão " + held
	}
	reason := fmt.Sprintf("Exposição máxima em %s: %d %s", pair, limit, agents)
	if len(names) > 0 {
		reason += " (" + strings.Join(names, ", ") + ")"
	}
	return reason
}

// exposureOn reports whether the exposure limit is on, an error read as off
// (logged): what the agents are told, so that they claim their entries.
func exposureOn(ctx context.Context, s *store.Store) bool {
	limits, _, err := s.ExposureLimits(ctx)
	if err != nil {
		log.Warnf("controller: the exposure limit: %v", err)
		return false
	}
	return limits.On()
}

// handleExposureClaim answers the claim of an entry of the agent of the
// request, see agentapi.ExposureClaim: granted while no limit weighs it.
func (m *Manager) handleExposureClaim(w http.ResponseWriter, r *http.Request) {
	var claim agentapi.ExposureClaim
	if err := json.NewDecoder(r.Body).Decode(&claim); err != nil || claim.Pair == "" ||
		(claim.Side != "BUY" && claim.Side != "SELL") {
		writeJSONError(w, http.StatusBadRequest, "claim: a pair and a side, BUY or SELL")
		return
	}
	if m.exposure == nil {
		writeJSON(w, agentapi.ExposureAnswer{Granted: true})
		return
	}
	granted, reason, err := m.exposure.claim(r.Context(), agentOf(r), claim.Pair, claim.Side)
	if err != nil {
		log.Errorf("controller: the exposure of %s %s: %v", claim.Side, claim.Pair, err)
		writeJSON(w, agentapi.ExposureAnswer{Reason: "Não foi possível conferir a exposição agora."})
		return
	}
	if !granted {
		log.Infof("controller: an entry of the agent %d refused: %s", agentOf(r), reason)
	}
	writeJSON(w, agentapi.ExposureAnswer{Granted: granted, Reason: reason})
}

// handleExposure answers the exposure limit of the agents, see
// ui.ExposureResponse.
func (s *settings) handleExposure(w http.ResponseWriter, r *http.Request) {
	limits, _, err := s.store.ExposureLimits(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, ui.ExposureResponse{Settings: limits.Settings()})
}

// handleSetExposure sets the exposure limit of the agents, by the user of the
// session, and tells every agent whether to claim its entries (their
// runtime).
func (s *settings) handleSetExposure(w http.ResponseWriter, r *http.Request) {
	var request ui.ExposureRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Pedido inválido.")
		return
	}
	pairs := make(map[string]int, len(request.Settings.Pairs))
	for pair, limit := range request.Settings.Pairs {
		pairs[strings.TrimSpace(pair)] = limit
	}
	request.Settings.Pairs = pairs
	by := userOf(r.Context())
	limits, err := s.store.SetExposureLimits(r.Context(), request.Settings, by)
	if err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the exposure limit set by %s: %v", by, limits.Settings().Pairs)
	agents, err := s.store.Agents(r.Context())
	if err != nil {
		log.Warnf("ui: the exposure limit not sent to the agents: %v", err)
	}
	for _, a := range agents {
		if err := s.manager.RefreshRuntime(r.Context(), a.Name); err != nil {
			log.Warnf("ui: the exposure limit not sent to the agent %s: %v", a.Name, err)
		}
	}
	writeJSON(w, ui.ExposureResponse{Settings: limits.Settings()})
}
