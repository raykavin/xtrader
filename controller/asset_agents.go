package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// assetReason returns why the agent can't open positions on excluded, by the
// rules of the agents per asset, eg: "XAUUSDm só para os agentes orb, trend".
func assetReason(rules store.AssetAgents, excluded []string) string {
	parts := make([]string, 0, len(excluded))
	for _, pair := range excluded {
		agents, _ := rules.AgentsOf(pair)
		who := "o agente"
		if len(agents) > 1 {
			who = "os agentes"
		}
		parts = append(parts, fmt.Sprintf("%s só para %s %s", pair, who, strings.Join(agents, ", ")))
	}
	return "Agentes por ativo: " + strings.Join(parts, "; ")
}

// assetEntries narrows the entries of the agent a to the assets the rules of
// the agents per asset allow it (rules, see store.AssetAgents.Allowed), within
// the ones of the window of the trading hours open, if any (their
// EntriesPairs); none of its assets allowed, its entries are blocked, as an
// empty list of assets would allow them all.
func assetEntries(entries agentapi.Entries, rules store.AssetAgents, a store.Agent) agentapi.Entries {
	entries.EntriesRevision += rules.Revision
	allowed, excluded := rules.Allowed(a.Name, agentPairs(a.Params))
	if len(excluded) == 0 {
		return entries
	}
	reason := assetReason(rules, excluded)
	if len(entries.EntriesPairs) > 0 {
		allowed = slices.DeleteFunc(allowed, func(p string) bool { return !slices.Contains(entries.EntriesPairs, p) })
		reason = entries.PairsReason + "; " + reason
	}
	if len(allowed) == 0 {
		entries.EntriesBlocked = true
		entries.EntriesReason = joinReasons(entries.EntriesReason, "Nenhum ativo permitido. "+reason)
		entries.EntriesPairs, entries.PairsReason = nil, ""
		return entries
	}
	entries.EntriesPairs, entries.PairsReason = allowed, reason
	return entries
}

// joinReasons joins the reasons of a block, the blank ones left out.
func joinReasons(reasons ...string) string {
	return strings.Join(slices.DeleteFunc(reasons, func(r string) bool { return strings.TrimSpace(r) == "" }), "; ")
}

// handleAssetAgents answers the agents allowed on assets, see
// ui.AssetAgentsResponse.
func (s *settings) handleAssetAgents(w http.ResponseWriter, r *http.Request) {
	rules, _, err := s.store.AssetAgents(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, ui.AssetAgentsResponse{Settings: rules.Settings()})
}

// handleSetAssetAgents sets the agents allowed on assets, by the user of the
// session, and tells every agent the assets it may open positions on (their
// runtime), at once: no restart.
func (s *settings) handleSetAssetAgents(w http.ResponseWriter, r *http.Request) {
	var request ui.AssetAgentsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Pedido inválido.")
		return
	}
	for i, rule := range request.Settings.Rules {
		for j, pair := range rule.Pairs {
			request.Settings.Rules[i].Pairs[j] = strings.TrimSpace(pair)
		}
		for j, agent := range rule.Agents {
			request.Settings.Rules[i].Agents[j] = strings.TrimSpace(agent)
		}
	}
	by := userOf(r.Context())
	rules, err := s.store.SetAssetAgents(r.Context(), request.Settings, by)
	if err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the agents per asset set by %s: %v", by, rules.Settings().Rules)
	agents, err := s.store.Agents(r.Context())
	if err != nil {
		log.Warnf("ui: the agents per asset not sent to the agents: %v", err)
	}
	for _, a := range agents {
		if err := s.manager.RefreshRuntime(r.Context(), a.Name); err != nil {
			log.Warnf("ui: the agents per asset not sent to the agent %s: %v", a.Name, err)
		}
	}
	writeJSON(w, ui.AssetAgentsResponse{Settings: rules.Settings()})
}
