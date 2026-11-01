package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// switchRefusal is a switch of the account of a terminal refused, nothing
// switched: answered with its status, see ui.MT5SwitchRefusal.
type switchRefusal struct {
	status int
	answer ui.MT5SwitchRefusal
}

func (r *switchRefusal) Error() string { return r.answer.Error }

func refuseSwitch(status int, format string, args ...any) *switchRefusal {
	return &switchRefusal{status: status, answer: ui.MT5SwitchRefusal{Error: fmt.Sprintf(format, args...)}}
}

func writeJSONStatus(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// switchPlan is what a switch of the account of a terminal touches: the
// accounts active leaving it, and the agents trading on it, by any of their
// bridges.
type switchPlan struct {
	m       *mt5Accounts
	leaving []store.MT5Account
	agents  []string
}

// running returns the agents of the plan running.
func (p switchPlan) running() []string {
	var running []string
	for _, name := range p.agents {
		if p.m.isRunning(name) {
			running = append(running, name)
		}
	}
	return running
}

// planSwitch returns what activating target (active) or deactivating it
// touches on its terminal. The terminal of an account is the one its bridge
// reaches: the same for two accounts of the same host and port, or whose
// bridges tell the same instance (two URLs of one terminal), never for two
// terminals of one host. Activating needs the bridge of target reached.
func (m *mt5Accounts) planSwitch(ctx context.Context, target store.MT5Account, active bool) (switchPlan, error) {
	plan := switchPlan{m: m}
	accounts, err := m.store.MT5Accounts(ctx)
	if err != nil {
		return plan, err
	}
	if active {
		instance, err := m.instanceOf(ctx, target)
		if err != nil {
			return plan, fmt.Errorf("o terminal da conta %s não respondeu (%s): nada foi trocado", target.Name,
				bridgeError(err))
		}
		for _, other := range accounts {
			if other.ID == target.ID || !other.Active {
				continue
			}
			same := other.BridgeKey == target.BridgeKey
			if !same && instance != "" {
				theirs, err := m.instanceOf(ctx, other)
				same = err == nil && theirs == instance
			}
			if same {
				plan.leaving = append(plan.leaving, other)
			}
		}
	} else if target.Active {
		plan.leaving = []store.MT5Account{target}
	}
	bridges := []string{target.BridgeURL}
	for _, leaving := range plan.leaving {
		bridges = append(bridges, leaving.BridgeURL)
	}
	agents, err := m.store.Agents(ctx)
	if err != nil {
		return plan, err
	}
	for _, a := range agents {
		if a.Account == nil || a.Account.BridgeURL == "" {
			continue
		}
		if slices.ContainsFunc(bridges, func(b string) bool { return agentapi.SameMT5Bridge(b, a.Account.BridgeURL) }) {
			plan.agents = append(plan.agents, a.Name)
		}
	}
	return plan, nil
}

// exposureOf returns the positions and the pending orders of the agents open
// on the accounts leaving their terminal, as read now, see exposure.
func (m *mt5Accounts) exposureOf(ctx context.Context, leaving []store.MT5Account) ([]string, error) {
	var open []string
	for _, account := range leaving {
		more, err := m.exposure(ctx, account.BridgeURL, nil)
		if err != nil {
			return nil, err
		}
		open = append(open, more...)
	}
	return open, nil
}

// switchAccount activates target on its terminal (active), or deactivates it,
// coordinated, by change:
//
//  1. the agents of the terminal kept from launching and stopped, waited for;
//  2. the positions and the pending orders of the agents on the accounts
//     leaving it closed and canceled, only confirmed (closeConfirmed), then
//     checked on the account: anything still open refuses the switch;
//  3. target made the account of the terminal, and the terminal logged in
//     it: the account it left made active again when it can't log in;
//  4. the agents released: the ones of the account active started again.
//
// A refusal (switchRefusal) leaves the accounts as they were, the agents of
// the account active started again to watch what it holds. The accounts of
// another terminal are never touched, on the same host too.
func (m *mt5Accounts) switchAccount(ctx context.Context, target store.MT5Account, active, closeConfirmed bool,
	change store.MT5Change) error {
	ctx = context.WithoutCancel(ctx) // a switch begun ends, whatever the request
	m.switching.Lock()
	defer m.switching.Unlock()
	plan, err := m.planSwitch(ctx, target, active)
	if err != nil {
		return refuseSwitch(http.StatusConflict, "%s", err.Error())
	}
	open, err := m.exposureOf(ctx, plan.leaving)
	if err != nil {
		return refuseSwitch(http.StatusConflict, "%s", err.Error())
	}
	if refusal := m.unconfirmed(plan, open, closeConfirmed); refusal != nil {
		return refusal
	}

	release := m.hold(plan.agents)
	defer release() // the agents of the account active launched again, the switch done or refused
	if stuck := m.waitStopped(ctx, plan.agents); len(stuck) > 0 {
		return refuseSwitch(http.StatusConflict, "os agentes %s não pararam a tempo: nada foi trocado",
			strings.Join(stuck, ", "))
	}
	// what they hold once stopped: an entry filled meanwhile too
	if open, err = m.exposureOf(ctx, plan.leaving); err != nil {
		return refuseSwitch(http.StatusConflict, "%s", err.Error())
	}
	if refusal := m.unconfirmed(plan, open, closeConfirmed); refusal != nil {
		return refusal
	}
	if len(open) > 0 {
		reason := fmt.Sprintf("troca da conta MT5 do terminal: %s", target.Name)
		if !active {
			reason = fmt.Sprintf("conta MT5 %s desativada", target.Name)
		}
		if change.Reason != "" {
			reason += ": " + change.Reason
		}
		action, status, err := m.supervision.flattenAgents(ctx, plan.agents, reason, change.By, change.UserID)
		if err != nil {
			return refuseSwitch(http.StatusConflict, "o encerramento das posições falhou (%v): nada foi trocado", err)
		}
		log.Warnf("mt5 accounts: the agents of %s flattened (action %d) by %s: %s", namesOf(plan.leaving),
			action.ID, change.By, status)
		pending, err := m.exposureOf(ctx, plan.leaving)
		if err != nil {
			return refuseSwitch(http.StatusConflict, "%s", err.Error())
		}
		if len(pending) > 0 {
			refusal := refuseSwitch(http.StatusConflict, "nem tudo foi encerrado na conta %s: a troca não foi "+
				"feita e os agentes dela voltam a acompanhar o que ficou aberto", namesOf(plan.leaving))
			refusal.answer.Pending = pending
			return refusal
		}
	}

	if !active {
		_, err := m.store.ActivateMT5Account(ctx, target.ID, false, change)
		return err
	}
	for _, leaving := range plan.leaving {
		if leaving.BridgeKey != target.BridgeKey { // another URL of the terminal, not left by the index
			if _, err := m.store.ActivateMT5Account(ctx, leaving.ID, false, change); err != nil {
				return err
			}
		}
	}
	if _, err := m.store.ActivateMT5Account(ctx, target.ID, true, change); err != nil {
		return err
	}
	_, err = m.postSession(ctx, target)
	failure := bridgeError(err)
	if err := m.store.RecordMT5Session(ctx, target.ID, "a conta ativa do terminal", failure); err != nil {
		log.Warnf("mt5 accounts: %v", err)
	}
	if failure == "" {
		log.Infof("mt5 accounts: the terminal of %s serves %d on %s", target.BridgeURL, target.Login, target.Server)
		m.status(ctx, target)
		return nil
	}
	// the account it left made active again, its agents launched again
	log.Warnf("mt5 accounts: the terminal of %s not logged in %s (%d): %s", target.BridgeURL, target.Name,
		target.Login, failure)
	if _, err := m.store.ActivateMT5Account(ctx, target.ID, false, change); err != nil {
		return err
	}
	back := "nenhuma conta ficou ativa no terminal"
	if len(plan.leaving) > 0 {
		former := plan.leaving[0]
		if _, err := m.store.ActivateMT5Account(ctx, former.ID, true, change); err != nil {
			return err
		}
		_, err := m.postSession(ctx, former)
		if again := bridgeError(err); again != "" {
			back = fmt.Sprintf("a conta %s voltou a ser a ativa, mas o terminal não logou nela: %s", former.Name, again)
		} else {
			back = fmt.Sprintf("a conta %s voltou a ser a ativa", former.Name)
		}
		m.status(ctx, former)
	}
	return refuseSwitch(http.StatusBadGateway, "o terminal não logou na conta %s (%s): %s", target.Name, failure,
		back)
}

// unconfirmed refuses a switch whose accounts leaving the terminal hold the
// positions or the pending orders open of their agents, not confirmed: the
// question asked again with them.
func (m *mt5Accounts) unconfirmed(plan switchPlan, open []string, confirmed bool) *switchRefusal {
	switch {
	case len(open) == 0:
		return nil
	case !confirmed:
		refusal := refuseSwitch(http.StatusConflict, "Existem posições abertas ou ordens pendentes na conta %s. "+
			"Ao trocar de conta, todas as posições serão encerradas e as ordens pendentes serão canceladas. "+
			"Deseja continuar?", namesOf(plan.leaving))
		refusal.answer.Confirm, refusal.answer.Pending = true, open
		return refusal
	case m.supervision == nil:
		return refuseSwitch(http.StatusConflict, "as posições não podem ser encerradas agora: nada foi trocado")
	}
	return nil
}

// namesOf returns the names of accounts, joined.
func namesOf(accounts []store.MT5Account) string {
	names := make([]string, 0, len(accounts))
	for _, a := range accounts {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}

// hold keeps the agents names stopped, see holdOf, and returns its release.
func (m *mt5Accounts) hold(names []string) (release func()) {
	var fences []func()
	if m.manager != nil {
		for _, name := range names {
			fences = append(fences, m.manager.Fence(name))
		}
	}
	m.mu.Lock()
	for _, name := range names {
		m.held[name]++
	}
	m.mu.Unlock()
	m.trigger()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			for _, name := range names {
				if m.held[name]--; m.held[name] <= 0 {
					delete(m.held, name)
				}
			}
			m.mu.Unlock()
			for _, release := range fences {
				release()
			}
			m.trigger()
		})
	}
}

func (m *mt5Accounts) trigger() {
	if m.manager != nil {
		m.manager.Trigger()
	}
}

// isHeld reports whether the agent of name is kept stopped, see hold.
func (m *mt5Accounts) isHeld(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.held[name] > 0
}

// waitStopped waits for the agents names to stop, mt5StopTimeout at most,
// and returns the ones still running.
func (m *mt5Accounts) waitStopped(ctx context.Context, names []string) []string {
	deadline := m.now().Add(mt5StopTimeout)
	for {
		var running []string
		for _, name := range names {
			if m.isRunning(name) {
				running = append(running, name)
			}
		}
		if len(running) == 0 || !m.now().Before(deadline) {
			return running
		}
		m.trigger() // an agent busy at the last Reconcile stopped by the next one
		select {
		case <-ctx.Done():
			return running
		case <-time.After(mt5StopPoll):
		}
	}
}

// holdOf returns why the agent a, enabled, stays stopped by its account of
// MT5: its terminal switching account (see hold), or its account inactive,
// the terminal logged in another one; "" when it may run.
func (m *mt5Accounts) holdOf(a store.Agent) string {
	if m.isHeld(a.Name) {
		return "o terminal MT5 dele está trocando de conta: volta ao fim da troca"
	}
	if account := a.MT5AccountOf(); account != nil && !account.Active {
		return fmt.Sprintf("a conta MT5 %s está inativa: ative-a ou associe outra conta ao agente em "+
			"Configurações › Contas MT5", account.Name)
	}
	return ""
}

// mt5Hold returns why the agent a stays stopped by its account of MT5, see
// mt5Accounts.holdOf.
func (d *Dashboard) mt5Hold(a store.Agent) string {
	if d == nil || d.settings == nil || d.settings.mt5 == nil {
		return ""
	}
	return d.settings.mt5.holdOf(a)
}
