package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

var (
	// closeCheckFor bounds how long the positions closed are looked for gone
	// from their account, read again every closeCheckEvery.
	closeCheckFor   = 15 * time.Second
	closeCheckEvery = 1500 * time.Millisecond
	// flatFor is how long an agent stopped once flat must be seen flat, by two
	// reads at least, before it is disabled.
	flatFor = 10 * time.Second
)

// The statuses of the items of the result of an action, see
// ui.SupervisorResultItem.
const (
	itemDone    = "done"
	itemAlready = "already"
	itemPending = "pending"
	itemFailed  = "failed"
	itemUnknown = "unknown"
)

// maxReasonLength bounds the reason of an action.
const maxReasonLength = 1000

// statusOf returns the status of an action of items: succeeded when every item
// is done, already or pending, unknown when one is unknown and none failed,
// failed when none was done, partial else.
func statusOf(items []ui.SupervisorResultItem) string {
	var done, failed, unknown int
	for _, item := range items {
		switch item.Status {
		case itemDone, itemAlready, itemPending:
			done++
		case itemFailed:
			failed++
		case itemUnknown:
			unknown++
		}
	}
	switch {
	case len(items) == 0:
		return store.ActionFailed
	case failed == 0 && unknown == 0:
		return store.ActionSucceeded
	case failed == 0:
		return store.ActionUnknown
	case done == 0 && unknown == 0:
		return store.ActionFailed
	}
	return store.ActionPartial
}

// scopeOf decodes the scope of the action a.
func scopeOf(a store.SupervisorAction) (ui.SupervisorScope, ui.SupervisorOptions) {
	var scope ui.SupervisorScope
	var options ui.SupervisorOptions
	_ = json.Unmarshal(a.Scope, &scope)
	_ = json.Unmarshal(a.Options, &options)
	return scope, options
}

// execute executes the action a, claimed (executing), and records its result,
// checked: the actions on several agents one at a time, an action on an agent
// at a time, the locks of the agents in the order of their names.
func (s *Supervision) execute(ctx context.Context, a store.SupervisorAction) (string, ui.SupervisorResult) {
	ctx = context.WithoutCancel(ctx) // an action begun ends, whatever the request
	scope, options := scopeOf(a)
	agents := slices.Clone(scope.Agents)
	slices.Sort(agents)
	agents = slices.Compact(agents)
	if len(agents) > 1 || a.Action == store.ActionClosePositions {
		s.bulk.Lock()
		defer s.bulk.Unlock()
	}
	for _, name := range agents {
		defer s.lockAgent(name)()
	}

	by := a.RequestedBy
	if a.ConfirmedBy != "" {
		by = a.ConfirmedBy
	}
	var items []ui.SupervisorResultItem
	if a.Action == store.ActionClosePositions {
		items = s.closePositions(ctx, a, scope, options, by)
	} else {
		for _, name := range agents {
			agent, err := s.store.AgentByName(ctx, name)
			if err != nil {
				items = append(items, ui.SupervisorResultItem{Agent: name, Kind: "agent", Status: itemFailed,
					Detail: err.Error()})
				continue
			}
			switch a.Action {
			case store.ActionBlockEntries:
				items = append(items, s.setEntries(ctx, agent, true, a.Reason, by, &a.ID))
			case store.ActionAllowEntries:
				items = append(items, s.setEntries(ctx, agent, false, a.Reason, by, &a.ID))
			case store.ActionStopAgent:
				items = append(items, s.stopAgent(ctx, agent, a, by)...)
			case store.ActionCancelPending:
				items = append(items, s.cancelPending(ctx, agent, scope.Pairs)...)
			}
		}
	}
	result := ui.SupervisorResult{Items: items, CheckedAt: s.now().Unix()}
	status := statusOf(items)
	data, _ := json.Marshal(result)
	if err := s.store.FinishSupervisorAction(ctx, a.ID, status, data); err != nil {
		log.Errorf("supervisor: recording the result of the action %d: %v", a.ID, err)
	}
	log.Infof("supervisor: action %d (%s of %s) by %s: %s", a.ID, a.Action, strings.Join(agents, ", "), by,
		status)
	return status, result
}

// running reports whether the agent of name runs, its process up, not waiting
// to be started again.
func (s *Supervision) running(name string) bool {
	state := s.stateOf(name)
	return state.Running && !state.Restarting
}

// setEntries blocks the entries of the agent a, for reason, by by, of the
// action of actionID, or allows them again, and publishes them: confirmed by
// the agent when it runs, applied at its start else (pending).
func (s *Supervision) setEntries(ctx context.Context, a store.Agent, blocked bool, reason, by string,
	actionID *int64) ui.SupervisorResultItem {
	item := ui.SupervisorResultItem{Agent: a.Name, Kind: "entries"}
	supervision, err := s.store.SetAgentEntries(ctx, a.ID, blocked, reason, by, actionID)
	if err != nil {
		item.Status, item.Detail = itemFailed, err.Error()
		return item
	}
	// what the agent applies: its supervision and its daily guards together
	entries, err := combinedEntries(ctx, s.store, a.ID, supervision)
	if err != nil {
		item.Status, item.Detail = itemFailed, "o limite diário não pôde ser lido: "+err.Error()
		return item
	}
	daily := !blocked && entries.EntriesBlocked // allowed, a daily guard still blocking
	if err := s.manager.RefreshRuntime(ctx, a.Name); err != nil {
		log.Warnf("supervisor: the runtime of the agent %s: %v", a.Name, err)
	}
	if !s.running(a.Name) {
		item.Status, item.Detail = itemPending, "gravado: o agente aplica ao iniciar"
		if daily {
			item.Detail += dailyStill
		}
		return item
	}
	var state ui.SupervisionState
	err = s.callAgent(ctx, a.Name, "POST", "supervision/entries", ui.SupervisionEntriesRequest{
		Blocked: entries.EntriesBlocked, Revision: entries.EntriesRevision, Reason: entries.EntriesReason}, &state)
	var refused *apiError
	switch {
	case errors.As(err, &refused) && refused.Status == 404:
		item.Status, item.Detail = itemUnknown, "o agente não tem o canal do XTrader AI (versão anterior): "+
			"gravado, aplicado quando ele for reiniciado"
	case err != nil:
		item.Status, item.Detail = itemPending, "gravado: o agente aplica ao ler o runtime ("+err.Error()+")"
	case state.EntriesRevision < entries.EntriesRevision:
		item.Status, item.Detail = itemUnknown, "o agente não confirmou a revisão"
	case entries.EntriesBlocked && !state.EntriesBlocked:
		item.Status, item.Detail = itemUnknown, "o agente não confirmou o bloqueio"
	default:
		item.Status = itemDone
		switch {
		case blocked:
			item.Detail = "entradas bloqueadas"
		case daily:
			item.Detail = "entradas liberadas" + dailyStill
		default:
			item.Detail = "entradas liberadas"
		}
	}
	return item
}

// dailyStill tells entries allowed in the supervision of an agent still
// blocked by a daily guard.
const dailyStill = "; continuam bloqueadas pelo limite diário até 00:00 (Belém)"

// stopAgent stops the agent a as the action act asks: when flat, its entries
// blocked and its pending entries canceled, disabled once it holds nothing
// (see watchStops); now, only when every position it holds has its stop loss
// at the broker, never liquidated.
func (s *Supervision) stopAgent(ctx context.Context, a store.Agent, act store.SupervisorAction,
	by string) []ui.SupervisorResultItem {
	if !a.Enabled {
		return []ui.SupervisorResultItem{{Agent: a.Name, Kind: "stop", Status: itemAlready,
			Detail: "o agente já está desligado"}}
	}
	reason := "parada pelo XTrader AI: " + act.Reason
	if act.Mode == store.ModeNow {
		v, err := s.read(ctx)
		if err != nil {
			return []ui.SupervisorResultItem{{Agent: a.Name, Kind: "stop", Status: itemFailed, Detail: err.Error()}}
		}
		if gap := v.agentGap(a.Name); gap != "" && a.Mode != "paper" {
			return []ui.SupervisorResultItem{{Agent: a.Name, Kind: "stop", Status: itemFailed,
				Detail: "dados incompletos (" + gap + "): parada recusada"}}
		}
		for _, held := range v.positions[a.Name] {
			if held.position.SL == 0 {
				return []ui.SupervisorResultItem{{Agent: a.Name, Kind: "stop", Status: itemFailed,
					Ticket: held.position.Ticket, Pair: held.position.Symbol,
					Detail: "posição sem stop loss na corretora: proteja ou feche antes de parar o agente"}}
			}
		}
	}

	items := []ui.SupervisorResultItem{s.setEntries(ctx, a, true, reason, by, &act.ID)}
	items = append(items, s.cancelPending(ctx, a, nil)...)
	if act.Mode == store.ModeNow {
		if _, err := s.store.MarkAgentStop(ctx, a.ID, store.StopStopped, act.Reason, act.Category, by,
			&act.ID); err != nil {
			return append(items, ui.SupervisorResultItem{Agent: a.Name, Kind: "stop", Status: itemFailed,
				Detail: err.Error()})
		}
		enabled := false
		if _, err := s.store.UpdateAgent(ctx, a.ID, store.AgentUpdate{Enabled: &enabled}); err != nil {
			return append(items, ui.SupervisorResultItem{Agent: a.Name, Kind: "stop", Status: itemFailed,
				Detail: err.Error()})
		}
		s.manager.Trigger()
		return append(items, ui.SupervisorResultItem{Agent: a.Name, Kind: "stop", Status: itemDone,
			Detail: "agente desligado; as posições ficam com o stop loss e o take profit na corretora"})
	}
	if _, err := s.store.MarkAgentStop(ctx, a.ID, store.StopPendingFlat, act.Reason, act.Category, by,
		&act.ID); err != nil {
		return append(items, ui.SupervisorResultItem{Agent: a.Name, Kind: "stop", Status: itemFailed,
			Detail: err.Error()})
	}
	s.mu.Lock()
	s.stopping[a.ID] = stopTarget{action: act.ID, name: a.Name}
	s.mu.Unlock()
	return append(items, ui.SupervisorResultItem{Agent: a.Name, Kind: "stop", Status: itemPending,
		Detail: "desligado assim que estiver sem posição e sem ordem pendente; até lá ele gerencia o que tem"})
}

// watchStops disables the agents stopped once flat that hold nothing, seen so
// twice, flatFor apart at least: their entries blocked as the agent told,
// nothing held on their account (read whole) nor told by them, their outbox
// sent. The agents busy with an action are left to the next time.
func (s *Supervision) watchStops(ctx context.Context) {
	agents, err := s.store.Agents(ctx)
	if err != nil {
		log.Warnf("supervisor: the agents stopped once flat: %v", err)
		return
	}
	var waiting []store.Agent
	for _, a := range agents {
		if a.SupervisionOf().StopState == store.StopPendingFlat {
			waiting = append(waiting, a)
		}
	}
	if len(waiting) == 0 {
		return
	}
	accounts, reads, err := s.positions.readFresh(ctx)
	if err != nil {
		log.Warnf("supervisor: the accounts of the agents stopped once flat: %v", err)
		return
	}
	for _, a := range waiting {
		unlock, ok := s.tryLockAgent(a.Name)
		if !ok {
			continue
		}
		s.watchStop(ctx, a, accounts, reads)
		unlock()
	}
}

// tryLockAgent takes the lock of the actions on the agent of name when free,
// see lockAgent.
func (s *Supervision) tryLockAgent(name string) (func(), bool) {
	s.locksMu.Lock()
	lock, ok := s.locks[name]
	if !ok {
		lock = new(sync.Mutex)
		s.locks[name] = lock
	}
	s.locksMu.Unlock()
	if !lock.TryLock() {
		return nil, false
	}
	return lock.Unlock, true
}

// watchStop disables the agent a, stopped once flat, when it holds nothing,
// see watchStops.
func (s *Supervision) watchStop(ctx context.Context, a store.Agent, accounts []liveAccount, reads []accountRead) {
	supervision := a.SupervisionOf()
	if supervision.StopActionID == nil {
		return
	}
	if a.Enabled {
		why := s.notFlat(ctx, a, supervision, accounts, reads)
		s.mu.Lock()
		since, seen := s.flat[a.ID]
		switch {
		case why != "":
			delete(s.flat, a.ID)
		case !seen:
			s.flat[a.ID] = s.now()
		}
		s.mu.Unlock()
		if why != "" {
			log.Debugf("supervisor: the agent %s, stopped once flat, isn't: %s", a.Name, why)
			return
		}
		if !seen || s.now().Sub(since) < flatFor {
			return // seen flat again later before it is stopped
		}
	}
	// disabled meanwhile by a user, StopAgentFlat clears its stop
	stopped, err := s.store.StopAgentFlat(ctx, a.ID, *supervision.StopActionID)
	if err != nil {
		log.Errorf("supervisor: stopping the agent %s: %v", a.Name, err)
		return
	}
	s.mu.Lock()
	delete(s.flat, a.ID)
	delete(s.stopping, a.ID)
	s.mu.Unlock()
	if !stopped {
		return
	}
	s.manager.Trigger()
	log.Warnf("supervisor: the agent %s stopped, flat, as the action %d asked: %s", a.Name,
		*supervision.StopActionID, supervision.StopReason)
	s.noteStopped(ctx, *supervision.StopActionID, a.Name)
}

// noteStopped adds to the result of the stop of id that its agent is disabled
// now.
func (s *Supervision) noteStopped(ctx context.Context, id int64, name string) {
	action, err := s.store.SupervisorAction(ctx, id)
	if err != nil {
		return
	}
	var result ui.SupervisorResult
	_ = json.Unmarshal(action.Result, &result)
	for i := range result.Items {
		if result.Items[i].Agent == name && result.Items[i].Kind == "stop" {
			result.Items[i].Status, result.Items[i].Detail = itemDone, "agente desligado: sem posição nem ordem pendente"
		}
	}
	result.CheckedAt = s.now().Unix()
	data, _ := json.Marshal(result)
	_ = s.store.FinishSupervisorAction(ctx, id, statusOf(result.Items), data)
}

// notFlat returns why the agent a, stopped once flat, isn't, "" when it holds
// nothing: its account read whole without a position nor a pending order of
// it, its entries blocked as the agent told, it holding nothing as it tells,
// its outbox sent.
func (s *Supervision) notFlat(ctx context.Context, a store.Agent, supervision store.AgentSupervision,
	accounts []liveAccount, reads []accountRead) string {
	if a.Mode == "live" {
		for i, account := range accounts {
			if !slices.ContainsFunc(account.agents, func(b bookedAgent) bool { return b.agent.ID == a.ID }) {
				continue
			}
			read := reads[i]
			if read.gap != "" {
				return "its account wasn't read: " + read.gap
			}
			for _, position := range read.positions {
				if owner, ok := ownerOf(account.agents, position.Magic, position.Symbol); ok && owner.ID == a.ID {
					return fmt.Sprintf("its position %d", position.Ticket)
				}
			}
			for _, order := range read.orders {
				if owner, ok := ownerOf(account.agents, order.Magic, order.Symbol); ok && owner.ID == a.ID {
					return fmt.Sprintf("its pending order %d", order.Ticket)
				}
			}
		}
	}
	if outbox := s.manager.Outbox(a.ID); outbox != nil && (outbox.Pending > 0 || outbox.Failing) {
		return "its outbox isn't sent"
	}
	if !s.running(a.Name) {
		return "" // nothing told: its account read whole, or in paper its positions gone with its process
	}
	state, err := s.supervision(ctx, a.Name)
	switch {
	case err != nil:
		return "its state can't be read: " + err.Error()
	case !state.EntriesBlocked || state.EntriesRevision < supervision.EntriesRevision:
		return "its entries aren't blocked yet"
	case len(state.Positions) > 0:
		return "it holds a position"
	case len(state.Pending) > 0:
		return "it has a pending order"
	}
	return ""
}

// cancelPending cancels the pending entries of the agent a on pairs, all of
// them without any: through its API when it runs, on its bridge else, its
// launch fenced meanwhile.
func (s *Supervision) cancelPending(ctx context.Context, a store.Agent, pairs []string) []ui.SupervisorResultItem {
	if s.running(a.Name) {
		var response ui.SupervisionCancelResponse
		err := s.callAgent(ctx, a.Name, "POST", "supervision/cancel", ui.SupervisionCancelRequest{Pairs: pairs},
			&response)
		if err != nil {
			return []ui.SupervisorResultItem{{Agent: a.Name, Kind: "orders", Status: itemFailed, Detail: err.Error()}}
		}
		if len(response.Results) == 0 {
			return []ui.SupervisorResultItem{{Agent: a.Name, Kind: "orders", Status: itemAlready,
				Detail: "nenhuma ordem pendente de entrada"}}
		}
		var items []ui.SupervisorResultItem
		for _, result := range response.Results {
			item := ui.SupervisorResultItem{Agent: a.Name, Kind: "order", Pair: result.Pair, Ticket: result.ID,
				Detail: result.Error}
			switch result.Status {
			case ui.SupervisionCanceled:
				item.Status = itemDone
			case ui.SupervisionGone:
				item.Status, item.Detail = itemAlready, "a ordem já tinha terminado"
			default:
				item.Status = itemFailed
			}
			items = append(items, item)
		}
		return items
	}
	if a.Mode != "live" {
		return []ui.SupervisorResultItem{{Agent: a.Name, Kind: "orders", Status: itemAlready,
			Detail: "agente em paper parado: as ordens simuladas não existem mais"}}
	}
	return s.onBridge(ctx, a, func(v *view, token string) []ui.SupervisorResultItem {
		var items []ui.SupervisorResultItem
		for _, held := range v.orders[a.Name] {
			if len(pairs) > 0 && !slices.ContainsFunc(pairs, func(p string) bool {
				return strings.EqualFold(p, held.order.Symbol)
			}) {
				continue
			}
			item := ui.SupervisorResultItem{Agent: a.Name, Kind: "order", Pair: held.order.Symbol,
				Ticket: held.order.Ticket}
			item.Status, item.Detail = s.bridges.remove(ctx, held.bridge, token, held.order, bridgeAgentOf(a))
			items = append(items, item)
		}
		if len(items) == 0 {
			return []ui.SupervisorResultItem{{Agent: a.Name, Kind: "orders", Status: itemAlready,
				Detail: "nenhuma ordem pendente na conta"}}
		}
		return items
	})
}

// onBridge runs act on the bridge of the agent a, not running, with its token
// and a view read now: its launch fenced meanwhile (see Manager.Fence), never
// while its process runs.
func (s *Supervision) onBridge(ctx context.Context, a store.Agent,
	act func(v *view, token string) []ui.SupervisorResultItem) []ui.SupervisorResultItem {
	release := s.manager.Fence(a.Name)
	defer release()
	if state := s.stateOf(a.Name); state.Running && !state.Restarting {
		return []ui.SupervisorResultItem{{Agent: a.Name, Kind: "agent", Status: itemFailed,
			Detail: "o agente voltou a rodar: tente de novo"}}
	}
	secrets, err := s.store.SecretsOf(a)
	if err != nil {
		return []ui.SupervisorResultItem{{Agent: a.Name, Kind: "agent", Status: itemFailed, Detail: err.Error()}}
	}
	v, err := s.read(ctx)
	if err != nil {
		return []ui.SupervisorResultItem{{Agent: a.Name, Kind: "agent", Status: itemFailed, Detail: err.Error()}}
	}
	if gap := v.agentGap(a.Name); gap != "" {
		return []ui.SupervisorResultItem{{Agent: a.Name, Kind: "agent", Status: itemFailed,
			Detail: "a conta não pôde ser lida: " + gap}}
	}
	return act(v, secrets.BridgeToken)
}

// holdOf returns how long the entries of the agent a are held after a close
// that keeps them open: a candle of its timeframe, a minute at least.
func holdOf(a store.Agent) int {
	timeframe := a.AllParams()["timeframe"]
	duration, err := time.ParseDuration(timeframe)
	if err != nil || duration < time.Minute {
		return 60
	}
	return int(min(duration, 24*time.Hour) / time.Second)
}

// closePositions closes the positions of the scope of the action a, the ones
// confirmed only: their agents' entries blocked first (kept blocked or held a
// candle, as options say) and their pending entries canceled when asked; the
// positions of an agent running closed through its API, a pair at a time,
// refused when it holds a position on it the confirmation didn't list; the
// ones of an agent not running closed on its bridge, its launch fenced; then
// each position looked for on its account, closed, still open or unknown.
func (s *Supervision) closePositions(ctx context.Context, a store.SupervisorAction, scope ui.SupervisorScope,
	options ui.SupervisorOptions, by string) []ui.SupervisorResultItem {
	before, err := s.read(ctx)
	if err != nil {
		return []ui.SupervisorResultItem{{Kind: "positions", Status: itemFailed,
			Detail: "as contas não puderam ser lidas: " + err.Error()}}
	}
	if options.Whole { // every position of the agents now, the ones opened since included
		scope.Positions = wholePositions(before, scope.Agents)
	}
	byAgent := make(map[string][]ui.SupervisorScopePosition)
	for _, position := range scope.Positions {
		byAgent[position.Agent] = append(byAgent[position.Agent], position)
	}

	var items []ui.SupervisorResultItem
	var unknownPairs = make(map[string]map[string]bool) // the pairs closed with an outcome unknown, by agent
	names := slices.Sorted(maps.Keys(byAgent))
	if options.Whole { // the agents shown without a position too: their pending entries canceled
		for _, agent := range before.snapshot.Agents {
			if slices.Contains(scope.Agents, agent.Name) && !slices.Contains(names, agent.Name) {
				names = append(names, agent.Name)
			}
		}
		slices.Sort(names)
	}
	for _, name := range names {
		agent, err := s.store.AgentByName(ctx, name)
		if err != nil {
			items = append(items, ui.SupervisorResultItem{Agent: name, Kind: "agent", Status: itemFailed,
				Detail: err.Error()})
			continue
		}
		if options.CancelPending {
			items = append(items, s.cancelPending(ctx, agent, nil)...)
		}
		revision := int64(0)
		if options.KeepBlocked {
			item := s.setEntries(ctx, agent, true, "encerramento de posições: "+a.Reason, by, &a.ID)
			items = append(items, item)
			if sup, err := s.store.AgentByName(ctx, name); err == nil {
				if entries, err := entriesOf(ctx, s.store, sup); err == nil {
					revision = entries.EntriesRevision
				}
			}
		}
		confirmed := byAgent[name]
		switch {
		case s.running(name):
			closed, unknown := s.closeThroughAgent(ctx, agent, confirmed, before, options, revision, a.Reason)
			items = append(items, closed...)
			if len(unknown) > 0 {
				unknownPairs[name] = unknown
			}
		case agent.Mode != "live":
			for _, position := range confirmed {
				items = append(items, ui.SupervisorResultItem{Agent: name, Kind: "position", Pair: position.Pair,
					Status: itemAlready, Detail: "agente em paper parado: a posição simulada não existe mais"})
			}
		default:
			items = append(items, s.onBridge(ctx, agent, func(v *view, token string) []ui.SupervisorResultItem {
				return s.closeOnBridge(ctx, agent, confirmed, v, token)
			})...)
		}
	}
	return s.checkClosed(ctx, items, scope.Positions, unknownPairs)
}

// closeThroughAgent closes the positions confirmed of the agent a, running,
// through its API, a pair at a time: a pair where it holds a position the
// confirmation didn't list is left, to confirm again. It returns the items of
// the pairs refused or failed (the closed ones are checked on the account
// after), and the pairs whose outcome is unknown.
func (s *Supervision) closeThroughAgent(ctx context.Context, a store.Agent, confirmed []ui.SupervisorScopePosition,
	before *view, options ui.SupervisorOptions, revision int64, reason string) ([]ui.SupervisorResultItem,
	map[string]bool) {
	tickets := make(map[int64]bool)
	pairs := []string{}
	for _, position := range confirmed {
		tickets[position.Ticket] = true
		if !slices.Contains(pairs, position.Pair) {
			pairs = append(pairs, position.Pair)
		}
	}
	var items []ui.SupervisorResultItem
	var allowed []string
	for _, pair := range pairs {
		unlisted := false
		for _, held := range before.positions[a.Name] {
			if held.position.Symbol == pair && !tickets[held.position.Ticket] && !options.Whole {
				unlisted = true
			}
		}
		if unlisted {
			items = append(items, ui.SupervisorResultItem{Agent: a.Name, Kind: "position", Pair: pair,
				Status: itemFailed, Detail: "o agente abriu outra posição neste par depois da confirmação: " +
					"confirme de novo"})
			continue
		}
		allowed = append(allowed, pair)
	}
	if len(allowed) == 0 {
		return items, nil
	}
	request := ui.SupervisionCloseRequest{Pairs: allowed, Block: options.KeepBlocked, Revision: revision,
		Reason: "encerramento de posições: " + reason}
	if !options.KeepBlocked {
		request.HoldSeconds = holdOf(a)
	}
	var response ui.SupervisionCloseResponse
	unknown := make(map[string]bool)
	if err := s.callAgent(ctx, a.Name, "POST", "supervision/close", request, &response); err != nil {
		var refused *apiError
		if errors.As(err, &refused) && refused.Status != 0 && refused.Status < 500 {
			for _, pair := range allowed {
				items = append(items, ui.SupervisorResultItem{Agent: a.Name, Kind: "position", Pair: pair,
					Status: itemFailed, Detail: err.Error()})
			}
			return items, nil
		}
		for _, pair := range allowed { // maybe closed: checked on the account, never sent again
			unknown[pair] = true
		}
		return items, unknown
	}
	for _, result := range response.Results {
		switch result.Status {
		case ui.SupervisionUnknown:
			unknown[result.Pair] = true
		case ui.SupervisionFailed:
			items = append(items, ui.SupervisorResultItem{Agent: a.Name, Kind: "position", Pair: result.Pair,
				Status: itemFailed, Detail: result.Error})
		}
	}
	return items, unknown
}

// closeOnBridge closes the positions confirmed of the agent a, not running, on
// its bridge, by their tickets, the ones still open in v.
func (s *Supervision) closeOnBridge(ctx context.Context, a store.Agent, confirmed []ui.SupervisorScopePosition,
	v *view, token string) []ui.SupervisorResultItem {
	open := make(map[int64]heldPosition)
	for _, held := range v.positions[a.Name] {
		open[held.position.Ticket] = held
	}
	var items []ui.SupervisorResultItem
	for _, position := range confirmed {
		held, ok := open[position.Ticket]
		if !ok {
			continue // closed meanwhile: told by the check
		}
		item := ui.SupervisorResultItem{Agent: a.Name, Kind: "position", Pair: position.Pair, Ticket: position.Ticket}
		item.Status, item.Detail = s.bridges.close(ctx, held.bridge, token, held.position, bridgeAgentOf(a))
		if item.Status != itemDone {
			items = append(items, item) // the done ones checked on the account
		}
	}
	return items
}

// checkClosed looks for the positions confirmed on their account, read again
// until they are gone or closeCheckFor passed: each one closed (done), gone
// before (already), still open (failed, or unknown when its close was), or
// unknown when its account isn't read; the items of the failures before kept.
func (s *Supervision) checkClosed(ctx context.Context, items []ui.SupervisorResultItem,
	confirmed []ui.SupervisorScopePosition, unknownPairs map[string]map[string]bool) []ui.SupervisorResultItem {
	failed := make(map[string]bool) // the pairs of each agent refused or failed before
	for _, item := range items {
		if item.Kind == "position" && item.Status == itemFailed {
			failed[item.Agent+"/"+item.Pair] = true
		}
	}
	live := []ui.SupervisorScopePosition{}
	for _, position := range confirmed {
		if !position.Paper && position.Ticket != 0 {
			live = append(live, position)
		}
	}
	var last *view
	deadline := s.now().Add(closeCheckFor)
	for {
		v, err := s.read(ctx)
		if err == nil {
			last = v
			if allGone(v, live, failed) {
				break
			}
		}
		if !s.now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(closeCheckEvery):
		}
		if ctx.Err() != nil {
			break
		}
	}
	for _, position := range confirmed {
		key := position.Agent + "/" + position.Pair
		if failed[key] {
			continue
		}
		item := ui.SupervisorResultItem{Agent: position.Agent, Kind: "position", Pair: position.Pair,
			Ticket: position.Ticket}
		switch {
		case position.Paper:
			item.Status, item.Detail = itemDone, "fechamento simulado pedido ao agente em paper"
			if unknownPairs[position.Agent][position.Pair] {
				item.Status, item.Detail = itemUnknown, "o agente não respondeu: confira no painel"
			}
		case last == nil || last.agentGap(position.Agent) != "":
			item.Status, item.Detail = itemUnknown, "a conta não pôde ser lida para conferir"
		case stillOpen(last, position):
			item.Status, item.Detail = itemFailed, "a posição continua aberta"
			if unknownPairs[position.Agent][position.Pair] {
				item.Status, item.Detail = itemUnknown, "resultado desconhecido e a posição ainda aparece aberta: "+
					"confira na conta, nada é reenviado"
			}
		default:
			item.Status, item.Detail = itemDone, "posição fechada, conferida na conta"
		}
		items = append(items, item)
	}
	return items
}

// allGone reports whether none of the positions confirmed is open in v, but
// the ones of the pairs failed.
func allGone(v *view, positions []ui.SupervisorScopePosition, failed map[string]bool) bool {
	for _, position := range positions {
		if !failed[position.Agent+"/"+position.Pair] && stillOpen(v, position) {
			return false
		}
	}
	return true
}

// stillOpen reports whether the position confirmed is open in v, by its ticket.
func stillOpen(v *view, position ui.SupervisorScopePosition) bool {
	for _, held := range v.positions[position.Agent] {
		if held.position.Ticket == position.Ticket {
			return true
		}
	}
	return false
}

// analysisTools returns the tools of the model in an analysis: its report, and
// the actions it asks for, executed within autonomy, proposed beyond it.
func analysisTools(autonomy store.Autonomy) []llm.ToolSpec {
	action := func(extra string) json.RawMessage {
		return json.RawMessage(`{"type":"object","properties":{` +
			`"agent":{"type":"string","description":"o nome exato do agente, como no retrato"},` +
			`"reason":{"type":"string","description":"o motivo, curto"},` +
			`"category":{"type":"string","enum":["operational","risk","performance"]},` +
			`"evidence":{"type":"string","description":"os números do retrato que justificam, com a moeda, a janela e o horário"}` +
			extra + `},"required":["agent","reason","category","evidence"]}`)
	}
	alone := func(allowed bool) string {
		if allowed {
			return " Você pode executá-la sozinho."
		}
		return " Fora da sua autonomia: vira uma proposta para o usuário confirmar."
	}
	return []llm.ToolSpec{
		{Name: "report", Description: "O resultado da análise: um resumo e os achados.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"summary":{"type":"string"},` +
				`"findings":{"type":"array","items":{"type":"object","properties":{` +
				`"agent":{"type":"string","description":"o agente, vazio para o conjunto"},` +
				`"category":{"type":"string","enum":["operational","risk","performance"]},` +
				`"severity":{"type":"string","enum":["info","warning","critical"]},` +
				`"summary":{"type":"string"},` +
				`"evidence":{"type":"string"},` +
				`"recommendation":{"type":"string","enum":["none","watch","block_entries","stop","cancel_pending","close_positions"]}` +
				`},"required":["category","severity","summary"]}}},"required":["summary","findings"]}`)},
		{Name: store.ActionBlockEntries, Description: "Bloqueia novas entradas do agente: ele não abre nem aumenta " +
			"posição, e continua gerenciando as que tem." + alone(autonomy.BlockEntries), Parameters: action("")},
		{Name: store.ActionStopAgent, Description: "Para o agente assim que ele estiver sem posição e sem ordem " +
			"pendente: bloqueia as entradas e cancela as pendentes de entrada agora; nunca liquida posições. Só " +
			"acontece se a análise anterior também pediu a parada do mesmo agente." + alone(autonomy.StopWhenFlat),
			Parameters: action("")},
		{Name: store.ActionCancelPending, Description: "Cancela as ordens pendentes de entrada do agente, nunca as " +
			"proteções das posições." + alone(autonomy.CancelPending),
			Parameters: action(`,"pairs":{"type":"array","items":{"type":"string"},"description":"os pares, todos sem nenhum"}`)},
		protectTool(),
	}
}

// act runs a call of an action of the analysis: validated against the view,
// executed within the autonomy of the settings and its count, proposed beyond
// them; refused for an agent of incomplete data. A stop runs when the
// analysis before asked for it too, else it waits for the next one.
func (a *analysis) act(ctx context.Context, call llm.ToolCall) string {
	var args struct {
		Agent    string   `json:"agent"`
		Reason   string   `json:"reason"`
		Category string   `json:"category"`
		Evidence string   `json:"evidence"`
		Pairs    []string `json:"pairs"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return toolError("invalid arguments: " + err.Error())
	}
	args.Reason = strings.TrimSpace(args.Reason)
	agent, ok := a.view.agents[args.Agent]
	switch {
	case !ok:
		return toolError(fmt.Sprintf("no agent %q: use the exact name of the snapshot", args.Agent))
	case args.Reason == "" || len([]rune(args.Reason)) > maxReasonLength:
		return toolError(fmt.Sprintf("the reason is required, %d characters at most", maxReasonLength))
	case args.Category != store.CategoryOperational && args.Category != store.CategoryRisk &&
		args.Category != store.CategoryPerformance:
		return toolError("category: operational, risk or performance")
	}
	if gap := a.view.agentGap(agent.Name); gap != "" {
		return toolError("the data of the agent is incomplete (" + gap + "): nothing done; report the finding")
	}
	autonomy := a.settings.Autonomy
	allowed := map[string]bool{store.ActionBlockEntries: autonomy.BlockEntries,
		store.ActionStopAgent: autonomy.StopWhenFlat, store.ActionCancelPending: autonomy.CancelPending}[call.Name]
	action := store.SupervisorAction{Source: store.SourceSupervisor, RunID: &a.run.ID, Action: call.Name,
		Reason: args.Reason, Category: args.Category, RequestedBy: "supervisor"}
	action.Scope, _ = json.Marshal(ui.SupervisorScope{Agents: []string{agent.Name}, Pairs: args.Pairs})
	action.Evidence, _ = json.Marshal([]string{args.Evidence})
	if call.Name == store.ActionStopAgent {
		action.Mode = store.ModeWhenFlat
		a.findings = append(a.findings, ui.SupervisorFinding{Agent: agent.Name, Category: args.Category,
			Severity: "critical", Summary: args.Reason, Evidence: args.Evidence, Recommendation: "stop"})
		if !a.stopConfirmed(ctx, agent.Name) {
			return toolResult(map[string]any{"deferred": true, "message": "a parada fica registrada e só " +
				"acontece se a próxima análise também a pedir; bloqueie as entradas agora se o risco exigir"})
		}
	}
	if !allowed || a.actions >= autonomy.MaxActionsPerRun {
		why := "outside your autonomy"
		if allowed {
			why = "the limit of actions alone in an analysis"
		}
		return a.propose(ctx, action, why)
	}
	a.actions++
	action.Status = store.ActionExecuting
	created, err := a.s.store.CreateSupervisorAction(ctx, action)
	if err != nil {
		return notRecorded(err)
	}
	status, result := a.s.execute(ctx, created)
	return toolResult(map[string]any{"action": created.ID, "status": status, "items": result.Items})
}

// propose records action as a proposal of the analysis, for a user to confirm
// within an interval of the analyses.
func (a *analysis) propose(ctx context.Context, action store.SupervisorAction, why string) string {
	expires := a.s.now().Add(time.Duration(a.settings.IntervalSeconds) * time.Second)
	action.Status, action.ExpiresAt = store.ActionProposed, &expires
	created, err := a.s.store.CreateSupervisorAction(ctx, action)
	if err != nil {
		return notRecorded(err)
	}
	return toolResult(map[string]any{"proposed": created.ID, "message": "proposed to the users (" + why + ")"})
}

// notRecorded returns the refusal of a call of a tool the database didn't
// record, nothing done: the error logged, never told to the model.
func notRecorded(err error) string {
	log.Errorf("supervisor: recording a call of a tool: %v", err)
	return toolError("the database didn't record it: nothing done")
}

// stopConfirmed reports whether the analysis before this one asked for the
// stop of the agent of name too: a stop alone takes two analyses in a row.
func (a *analysis) stopConfirmed(ctx context.Context, name string) bool {
	previous, found, err := a.s.store.PreviousSupervisorRun(ctx, a.run.ID)
	if err != nil || !found {
		return false
	}
	var findings []ui.SupervisorFinding
	if json.Unmarshal(previous.Findings, &findings) != nil {
		return false
	}
	return slices.ContainsFunc(findings, func(f ui.SupervisorFinding) bool {
		return f.Agent == name && f.Recommendation == "stop"
	})
}

// wholePositions returns every position of the agents names in v, live and
// paper, as a scope to close (see ui.SupervisorOptions.Whole).
func wholePositions(v *view, names []string) []ui.SupervisorScopePosition {
	positions := []ui.SupervisorScopePosition{}
	for _, agent := range v.snapshot.Agents {
		if !slices.Contains(names, agent.Name) {
			continue
		}
		for _, position := range agent.Positions {
			positions = append(positions, ui.SupervisorScopePosition{Agent: agent.Name, SupervisorPosition: position,
				Account: agent.Account, Running: agent.State == "running"})
		}
	}
	return positions
}
