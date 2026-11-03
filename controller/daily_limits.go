package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The cadence of the daily limits.
const (
	dailyLimitEvery      = 5 * time.Second  // between two evaluations
	dailyConfirmReads    = 2                // reads in a row a limit is reached in, or the data incomplete, to trip
	dailyFlattenAttempts = 3                // closes of the positions of a guard, at most
	dailyFlattenGap      = 15 * time.Second // between two closes of a guard, at least
	dailyOwnersEvery     = 10 * time.Minute // between two reads of the entries of the positions, at least
)

// dailyBy is who the daily limits act as, in the records.
const dailyBy = "limite diário"

// dailyScope is a scope of the daily limits: all the agents together (key
// empty, agentID nil, its results one by currency of the accounts, the limits
// applying to each apart) or an agent (in the currency of its account); its
// limits, its agents.
type dailyScope struct {
	key     string
	agentID *int64
	limits  ui.DailyLimitSettings
	names   []string         // its agents, paper ones included (blocked and closed with all the agents)
	results []ui.DailyResult // of all the agents, by currency
}

// dailyLimiter applies the daily limits: every dailyLimitEvery, and at once
// on a change of the limits, it reads the result of the day of the agents
// (realized plus open, see dailyMetrics), trips the guard of a scope reaching
// a limit in dailyConfirmReads reads in a row (its positions closed, its
// pending entries canceled, its entries blocked until the next day) or whose
// result stays unknown as long (its entries blocked), releases them at the
// next day (midnight of the zone of the controller, America/Belem, see
// llm.LocalZone), and tells how they stand (see Status).
type dailyLimiter struct {
	s     *Supervision
	every time.Duration
	zone  *time.Location
	wake  chan struct{}

	mu        sync.Mutex
	day       string
	owners    map[string]dailyOwner
	ownersAt  time.Time
	carry     map[string]dailyCarry
	breaches  map[string]int        // the reads in a row a scope reached a limit in
	failures  map[string]int        // the reads in a row the result of a scope was unknown in
	held      map[int64]int         // the reads in a row the scope of a guard closed held a position in
	flattenAt map[int64]time.Time   // the closes of the guards in progress, since when
	triedAt   map[int64]time.Time   // the last close of each guard
	status    ui.DailyLimitsStatus  // as last evaluated
	hours     ui.TradingHoursStatus // as last evaluated, see evaluateHours

	hoursMu     sync.Mutex  // an evaluation of the trading hours at a time
	hoursEnding atomic.Bool // the actions at the end of a window running, see endHours
}

func newDailyLimiter(s *Supervision) *dailyLimiter {
	return &dailyLimiter{s: s, every: dailyLimitEvery, zone: llm.LocalZone(),
		wake: make(chan struct{}, 1), owners: make(map[string]dailyOwner), carry: make(map[string]dailyCarry),
		breaches: make(map[string]int), failures: make(map[string]int), held: make(map[int64]int),
		flattenAt: make(map[int64]time.Time), triedAt: make(map[int64]time.Time)}
}

// Run evaluates the daily limits until ctx is done, see dailyLimiter.
func (l *dailyLimiter) Run(ctx context.Context) {
	ticker := time.NewTicker(l.every)
	defer ticker.Stop()
	for {
		if err := l.evaluate(ctx); err != nil && ctx.Err() == nil {
			log.Errorf("daily limits: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-l.wake:
		}
	}
}

// Reevaluate makes the limiter evaluate the limits now, eg: they changed.
func (l *dailyLimiter) Reevaluate() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Status returns how the daily limits stand, as last evaluated.
func (l *dailyLimiter) Status() ui.DailyLimitsStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	status := l.status
	status.Agents = slices.Clone(status.Agents)
	if status.Day == "" {
		status.Day, status.Zone = l.dayOf(l.s.now()), l.zone.String()
		status.Global.State = ui.DailyOff
	}
	status.Hours = l.hours
	if status.Hours.Zone == "" { // not evaluated yet
		status.Hours.Zone, status.Hours.Open = l.zone.String(), true
	}
	return status
}

// dayOf returns the day of t, eg: 2026-10-29, in the zone of the limits.
func (l *dailyLimiter) dayOf(t time.Time) string { return t.In(l.zone).Format(time.DateOnly) }

// midnight returns the start of the day of t in the zone of the limits.
func (l *dailyLimiter) midnight(t time.Time) time.Time {
	local := t.In(l.zone)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, l.zone)
}

// dailyMeasure is the result of the day read: the agents, the limits, the
// accounts and the metric, the result of each scope and the ones of all the
// agents by currency (see globalResults).
type dailyMeasure struct {
	at       time.Time
	day      string
	agents   []store.Agent
	global   ui.DailyLimitSettings
	accounts []liveAccount
	reads    []dailyRead
	metric   dailyMetric
	results  map[string]*ui.DailyResult // by scope key: of all the agents, the one their limits weigh
	globals  []ui.DailyResult           // of all the agents, by currency
}

// measure reads the result of the day of the agents of the accounts trading
// live: their account, deals of the day and positions, each account once; the
// entries of the positions read at most every dailyOwnersEvery for 30 days
// back, telling the agent of an exit without its magic.
func (l *dailyLimiter) measure(ctx context.Context) (*dailyMeasure, error) {
	now := l.s.now()
	m := &dailyMeasure{at: now, day: l.dayOf(now), results: make(map[string]*ui.DailyResult)}
	agents, err := l.s.store.Agents(ctx)
	if err != nil {
		return nil, err
	}
	m.agents = agents
	global, _, err := l.s.store.GlobalDailyLimit(ctx)
	if err != nil {
		return nil, err
	}
	m.global = global.Settings()
	window := closedRange{from: l.midnight(now), to: now}
	m.accounts = liveAccounts(agents)
	m.reads = make([]dailyRead, len(m.accounts))
	l.mu.Lock()
	lookback := now.Sub(l.ownersAt) > dailyOwnersEvery
	if lookback {
		l.ownersAt = now
	}
	l.mu.Unlock()
	readCtx, cancel := context.WithTimeout(ctx, 3*bridgeTimeout+time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, account := range m.accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.reads[i] = l.readDaily(readCtx, account, window, lookback)
		}()
	}
	wg.Wait()

	l.mu.Lock()
	m.metric = dailyMetrics(window, m.accounts, m.reads, l.owners, l.carry)
	l.carry = m.metric.carry
	l.mu.Unlock()

	for _, a := range agents {
		if a.Mode == "live" {
			m.results[a.Name] = m.agentResult(a.Name)
		}
	}
	m.globals = m.globalResults()
	m.results[""] = weighed(m.globals, m.global, m.metric.missing)
	return m, nil
}

// readDaily reads an account for the daily limits: its account, its deals of
// window (from 30 days before it with lookback, for the entries of the
// positions) and its positions, the deals before the positions so that a
// position closed between the two isn't counted twice.
func (l *dailyLimiter) readDaily(ctx context.Context, account liveAccount, window closedRange,
	lookback bool) dailyRead {
	var read dailyRead
	p := l.s.positions
	read.gap, _ = p.readBridge(ctx, account, "the daily limits", func(token string) error {
		read = dailyRead{}
		if err := p.get(ctx, account.bridge, "/account", token, &read.account); err != nil {
			return err
		}
		from := window.from.Add(-time.Minute)
		if lookback {
			from = window.from.Add(-closedLookback)
		}
		path := fmt.Sprintf("/deals?from=%d&to=%d", from.Unix(), window.to.Add(time.Minute).Unix())
		if err := p.get(ctx, account.bridge, path, token, &read.deals); err != nil {
			return err
		}
		return p.get(ctx, account.bridge, "/positions", token, &read.positions)
	})
	return read
}

// agentResult returns the result of the day of the agent of name, in the
// currency of its account.
func (m *dailyMeasure) agentResult(name string) *ui.DailyResult {
	result := &ui.DailyResult{Complete: true}
	for i, account := range m.accounts {
		if slices.ContainsFunc(account.agents, func(a bookedAgent) bool { return a.agent.Name == name }) &&
			m.reads[i].gap == "" {
			result.Currency = m.reads[i].account.currency()
		}
	}
	if m.metric.unread[name] {
		result.Complete = false
		result.Missing = append(result.Missing, "a conta do agente não pôde ser lida")
	}
	for currency, amounts := range m.metric.byAgent[name] {
		if result.Currency == "" {
			result.Currency = currency
		}
		if currency != result.Currency {
			continue // an agent trades one account, in one currency
		}
		result.Realized += amounts.realized
		result.Open += amounts.open
	}
	result.Total = result.Realized + result.Open
	return result
}

// globalResults returns the results of the day of all the agents together,
// one by currency of their accounts, none converted, by currency: each
// incomplete (Missing telling why) with an account not read.
func (m *dailyMeasure) globalResults() []ui.DailyResult {
	byCurrency := make(map[string]*ui.DailyResult)
	add := func(currency string) *ui.DailyResult {
		if byCurrency[currency] == nil {
			byCurrency[currency] = &ui.DailyResult{Currency: currency}
		}
		return byCurrency[currency]
	}
	for i := range m.accounts {
		if currency := m.reads[i].account.currency(); m.reads[i].gap == "" && currency != "" {
			add(currency) // read, its result 0 without any trade
		}
	}
	for _, amounts := range m.metric.byAgent {
		for currency, a := range amounts {
			result := add(currency)
			result.Realized += a.realized
			result.Open += a.open
		}
	}
	results := make([]ui.DailyResult, 0, len(byCurrency))
	for _, result := range byCurrency {
		result.Total = result.Realized + result.Open
		result.Complete, result.Missing = len(m.metric.missing) == 0, slices.Clone(m.metric.missing)
		results = append(results, *result)
	}
	slices.SortFunc(results, func(a, b ui.DailyResult) int { return strings.Compare(a.Currency, b.Currency) })
	return results
}

// weighed returns the result of results the limits weigh, of all the agents
// together, their amounts applying in each currency apart: the first one
// reaching a limit, else the one closest to one; without any (no account
// read), one of no currency, incomplete with missing.
func weighed(results []ui.DailyResult, limits ui.DailyLimitSettings, missing []string) *ui.DailyResult {
	if len(results) == 0 {
		return &ui.DailyResult{Complete: len(missing) == 0, Missing: slices.Clone(missing)}
	}
	closeness := func(r ui.DailyResult) float64 {
		near := math.Inf(-1)
		if limits.MaxLoss != nil {
			near = max(near, -r.Total / *limits.MaxLoss)
		}
		if limits.MaxGain != nil {
			near = max(near, r.Total / *limits.MaxGain)
		}
		return near
	}
	best := 0
	for i, r := range results {
		if trigger, _ := breach(r.Total, limits); trigger != "" {
			best = i
			break
		}
		if closeness(r) > closeness(results[best]) {
			best = i
		}
	}
	result := results[best]
	return &result
}

// scopes returns the scopes of the daily limits of m: all the agents together
// (limits enabled), and each agent live with its limits enabled.
func (m *dailyMeasure) scopes() []dailyScope {
	var scopes []dailyScope
	all := make([]string, 0, len(m.agents))
	for _, a := range m.agents {
		all = append(all, a.Name)
	}
	if m.global.Enabled {
		scopes = append(scopes, dailyScope{limits: m.global, names: all, results: m.globals})
	}
	for _, a := range m.agents {
		if limits := a.DailyLimitOf(); limits.Enabled && a.Mode == "live" {
			id := a.ID
			scopes = append(scopes, dailyScope{key: a.Name, agentID: &id, limits: limits, names: []string{a.Name}})
		}
	}
	return scopes
}

// breach returns the side of limits total reaches (TriggerLoss, TriggerGain),
// and the limit, "" for none; by the cents of total.
func breach(total float64, limits ui.DailyLimitSettings) (string, float64) {
	cents := math.Round(total*100) / 100
	switch {
	case limits.MaxLoss != nil && cents <= -*limits.MaxLoss:
		return store.TriggerLoss, *limits.MaxLoss
	case limits.MaxGain != nil && cents >= *limits.MaxGain:
		return store.TriggerGain, *limits.MaxGain
	}
	return "", 0
}

// money returns an amount as the dashboard writes it, eg: −152,30 BRL.
func money(value float64, currency string) string {
	sign := ""
	if value < 0 {
		sign, value = "−", -value
	}
	return strings.TrimSpace(sign + strings.Replace(fmt.Sprintf("%.2f", value), ".", ",", 1) + " " + currency)
}

// scopeName returns the name of a scope in the words of the dashboard.
func scopeName(key string) string {
	if key == "" {
		return "todos os agentes"
	}
	return key
}

// evaluate evaluates the daily limits once, see dailyLimiter.
func (l *dailyLimiter) evaluate(ctx context.Context) error {
	l.evaluateHours(ctx)
	now := l.s.now()
	day := l.dayOf(now)
	released, err := l.s.store.RolloverDailyGuards(ctx, day)
	if err != nil {
		return err
	}
	l.mu.Lock()
	if day != l.day {
		l.day = day
		l.breaches, l.failures = make(map[string]int), make(map[string]int)
	}
	l.mu.Unlock()
	if len(released) > 0 {
		log.Infof("daily limits: %d guards of the days before released", len(released))
		l.publish(ctx, nil, false)
	}

	tripped, err := l.s.store.TrippedDailyGuards(ctx)
	if err != nil {
		return err
	}
	global, _, err := l.s.store.GlobalDailyLimit(ctx)
	if err != nil {
		return err
	}
	agents, err := l.s.store.Agents(ctx)
	if err != nil {
		return err
	}
	active := global.Enabled || len(tripped) > 0 || slices.ContainsFunc(agents, func(a store.Agent) bool {
		return a.DailyLimitOf().Enabled && a.Mode == "live"
	})
	if !active {
		l.setStatus(&dailyMeasure{at: now, day: day, agents: agents, global: global.Settings(),
			results: map[string]*ui.DailyResult{}}, nil)
		return nil
	}

	m, err := l.measure(ctx)
	if err != nil {
		return err
	}
	guardOf := func(key string, kind string) *store.DailyGuard {
		for i, g := range tripped {
			if g.Kind == kind && scopeKeyOf(g, m.agents) == key {
				return &tripped[i]
			}
		}
		return nil
	}
	globalTripped := guardOf("", store.GuardLimit) != nil
	for _, scope := range m.scopes() {
		if scope.key != "" && globalTripped {
			continue // every agent closed and blocked with all the agents
		}
		result := m.results[scope.key]
		if result == nil {
			continue
		}
		l.decide(ctx, m, scope, result, guardOf(scope.key, store.GuardLimit), guardOf(scope.key, store.GuardIncomplete))
	}
	// the guards of a limit tripped: their scope flat, else closed again
	if tripped, err = l.s.store.TrippedDailyGuards(ctx); err != nil {
		return err
	}
	for _, g := range tripped {
		if g.Kind == store.GuardLimit && g.DayOf() == day {
			l.watchFlat(ctx, m, g)
		}
	}
	l.setStatus(m, tripped)
	return nil
}

// scopeKeyOf returns the scope key of the guard g: its agent, empty for all.
func scopeKeyOf(g store.DailyGuard, agents []store.Agent) string {
	if g.AgentID == nil {
		return ""
	}
	for _, a := range agents {
		if a.ID == *g.AgentID {
			return a.Name
		}
	}
	return fmt.Sprintf("agente %d", *g.AgentID)
}

// decide trips or releases the guards of scope after its result: incomplete
// in dailyConfirmReads reads in a row, its incomplete guard tripped (entries
// blocked); complete again and under its limits, released; a limit reached in
// dailyConfirmReads reads in a row, its limit guard tripped and its positions
// closed.
func (l *dailyLimiter) decide(ctx context.Context, m *dailyMeasure, scope dailyScope, result *ui.DailyResult,
	limit, incomplete *store.DailyGuard) {
	l.mu.Lock()
	if !result.Complete {
		l.failures[scope.key]++
		l.breaches[scope.key] = 0
	} else {
		l.failures[scope.key] = 0
	}
	failures := l.failures[scope.key]
	l.mu.Unlock()
	metric := map[string]any{"scope": scopeName(scope.key), "result": result, "limits": scope.limits}
	if scope.key == "" {
		metric["results"] = scope.results // every currency, the result above the one weighed
	}

	if !result.Complete {
		if failures < dailyConfirmReads || incomplete != nil || limit != nil {
			return
		}
		reason := fmt.Sprintf("resultado do dia de %s incompleto (%s): entradas bloqueadas até a leitura voltar",
			scopeName(scope.key), strings.Join(result.Missing, "; "))
		_, newly, err := l.s.store.TripDailyGuard(ctx, store.GuardTrip{Day: m.day, AgentID: scope.agentID,
			Kind: store.GuardIncomplete, Currency: result.Currency, Reason: reason, Metric: metric})
		if err != nil {
			log.Errorf("daily limits: %v", err)
			return
		}
		if newly {
			log.Warnf("daily limits: %s", reason)
			l.publish(ctx, scope.names, true)
		}
		return
	}

	trigger, amount := breach(result.Total, scope.limits)
	if incomplete != nil && trigger == "" {
		_, ok, err := l.s.store.ReleaseDailyGuard(ctx, incomplete.ID, store.DailyChange{Day: m.day, Via: store.ViaSystem,
			By: "system", Reason: "resultado do dia lido de novo, abaixo dos limites"}, metric)
		if err != nil {
			log.Errorf("daily limits: %v", err)
		} else if ok {
			l.publish(ctx, scope.names, false)
		}
	}
	l.mu.Lock()
	if trigger == "" || limit != nil {
		l.breaches[scope.key] = 0
	} else {
		l.breaches[scope.key]++
	}
	breaches := l.breaches[scope.key]
	l.mu.Unlock()
	if trigger == "" || limit != nil || breaches < dailyConfirmReads {
		return
	}
	what := "Perda diária de %s atingida"
	if trigger == store.TriggerGain {
		what = "Ganho diário de %s atingido"
	}
	reason := fmt.Sprintf(what+": %s (limite %s); posições zeradas e entradas bloqueadas até 00:00",
		scopeName(scope.key), money(result.Total, result.Currency), money(amount, result.Currency))
	guard, newly, err := l.s.store.TripDailyGuard(ctx, store.GuardTrip{Day: m.day, AgentID: scope.agentID,
		Kind: store.GuardLimit, Trigger: trigger, Result: result.Total, Limit: amount, Currency: result.Currency,
		Reason: reason, Metric: metric})
	if err != nil {
		log.Errorf("daily limits: %v", err)
		return
	}
	if !newly {
		return
	}
	log.Warnf("daily limits: %s", reason)
	l.publish(ctx, scope.names, true)
	l.flatten(ctx, guard, scope.names, metric)
}

// watchFlat closes again the positions of the scope of the guard g, tripped
// that day, when it still holds some in dailyConfirmReads reads in a row
// (eg: a pending entry filled meanwhile, a close failed), dailyFlattenGap
// after its last close, dailyFlattenAttempts closes at most.
func (l *dailyLimiter) watchFlat(ctx context.Context, m *dailyMeasure, g store.DailyGuard) {
	key := scopeKeyOf(g, m.agents)
	var names []string
	held := false
	for _, position := range m.metric.positions {
		if key == "" || position.agent == key {
			held = true
		}
	}
	if key == "" {
		for _, a := range m.agents {
			names = append(names, a.Name)
		}
	} else {
		names = []string{key}
	}
	l.mu.Lock()
	_, running := l.flattenAt[g.ID]
	if held && !running {
		l.held[g.ID]++
	} else if !held {
		l.held[g.ID] = 0
	}
	due := held && !running && l.held[g.ID] >= dailyConfirmReads && g.FlattenAttempts < dailyFlattenAttempts &&
		m.at.Sub(l.triedAt[g.ID]) >= dailyFlattenGap
	l.mu.Unlock()
	if !held || running {
		return
	}
	if g.FlattenAttempts >= dailyFlattenAttempts && g.FlattenStatus != store.FlattenFailed {
		if err := l.s.store.SetGuardFlatten(ctx, g.ID, store.FlattenFailed, g.FlattenActionID,
			g.FlattenAttempts); err != nil {
			log.Errorf("daily limits: %v", err)
		}
		log.Errorf("daily limits: the positions of %s still open after %d closes: check the accounts",
			scopeName(key), g.FlattenAttempts)
		return
	}
	if due {
		log.Warnf("daily limits: %s still holds positions: closing them again", scopeName(key))
		l.flatten(ctx, g, names, map[string]any{"scope": scopeName(key), "reason": "posições ainda abertas"})
	}
}

// publish publishes the entries of the agents of names (all of them when
// nil) at once (see Manager.RefreshRuntime) and, drain, blocks them on the
// agents running, waiting for the orders being sent to end (see
// ui.SupervisionEntriesRequest.Drain).
func (l *dailyLimiter) publish(ctx context.Context, names []string, drain bool) {
	agents, err := l.s.store.Agents(ctx)
	if err != nil {
		log.Errorf("daily limits: %v", err)
		return
	}
	var wg sync.WaitGroup
	for _, a := range agents {
		if names != nil && !slices.Contains(names, a.Name) {
			continue
		}
		if err := l.s.manager.RefreshRuntime(ctx, a.Name); err != nil {
			log.Warnf("daily limits: the runtime of the agent %s: %v", a.Name, err)
		}
		if !drain || !l.s.running(a.Name) {
			continue
		}
		entries, err := entriesOf(ctx, l.s.store, a)
		if err != nil {
			log.Warnf("daily limits: the entries of the agent %s: %v", a.Name, err)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			var state ui.SupervisionState
			err := l.s.callAgent(ctx, a.Name, "POST", "supervision/entries", ui.SupervisionEntriesRequest{
				Blocked: entries.EntriesBlocked, Revision: entries.EntriesRevision, Reason: entries.EntriesReason,
				Drain: true}, &state)
			if err != nil {
				log.Warnf("daily limits: the entries of the agent %s, applied at its next runtime: %v", a.Name, err)
			}
		}()
	}
	wg.Wait()
}

// flatten closes the positions of the agents names of the guard g and cancels
// their pending entries, in the background, through a close_positions of the
// XTrader AI (see Supervision.execute): every position of theirs read then
// (see ui.SupervisorOptions.Whole), checked on the accounts; recorded on g.
func (l *dailyLimiter) flatten(ctx context.Context, g store.DailyGuard, names []string, metric any) {
	now := l.s.now()
	l.mu.Lock()
	if _, running := l.flattenAt[g.ID]; running {
		l.mu.Unlock()
		return
	}
	l.flattenAt[g.ID], l.triedAt[g.ID], l.held[g.ID] = now, now, 0
	l.mu.Unlock()
	attempts := g.FlattenAttempts + 1
	if err := l.s.store.SetGuardFlatten(ctx, g.ID, store.FlattenRunning, g.FlattenActionID, attempts); err != nil {
		log.Errorf("daily limits: %v", err)
	}
	ctx = context.WithoutCancel(ctx)
	go func() {
		defer func() {
			l.mu.Lock()
			delete(l.flattenAt, g.ID)
			l.mu.Unlock()
			l.Reevaluate()
		}()
		action := store.SupervisorAction{Source: store.SourceSupervisor, Action: store.ActionClosePositions,
			Reason: g.Reason, Category: store.CategoryRisk, Status: store.ActionExecuting, RequestedBy: dailyBy,
			ConfirmedBy: dailyBy}
		action.Scope, _ = json.Marshal(ui.SupervisorScope{Agents: names})
		action.Options, _ = json.Marshal(ui.SupervisorOptions{CancelPending: true, Whole: true})
		evidence, _ := json.Marshal(metric)
		action.Evidence, _ = json.Marshal([]string{string(evidence)})
		created, err := l.s.store.CreateSupervisorAction(ctx, action)
		if err != nil {
			log.Errorf("daily limits: recording the close of the positions: %v", err)
			_ = l.s.store.SetGuardFlatten(ctx, g.ID, store.FlattenFailed, nil, attempts)
			return
		}
		status, _ := l.s.execute(ctx, created)
		flatten := store.FlattenDone
		switch status {
		case store.ActionPartial, store.ActionUnknown:
			flatten = store.FlattenPartial
		case store.ActionFailed:
			flatten = store.FlattenFailed
		}
		if err := l.s.store.SetGuardFlatten(ctx, g.ID, flatten, &created.ID, attempts); err != nil {
			log.Errorf("daily limits: %v", err)
		}
		log.Warnf("daily limits: the positions of %s closed (action %d): %s", scopeName(scopeKeyOf(g, nil)),
			created.ID, status)
	}()
}

// setStatus records how the limits stand after m and the guards tripped.
func (l *dailyLimiter) setStatus(m *dailyMeasure, tripped []store.DailyGuard) {
	status := ui.DailyLimitsStatus{Day: m.day, Zone: l.zone.String(), EvaluatedAt: m.at.Unix(),
		Agents: []ui.DailyScopeStatus{}}
	stateOf := func(key string, limits ui.DailyLimitSettings) ui.DailyScopeStatus {
		scope := ui.DailyScopeStatus{Agent: key, Limits: limits, Result: m.results[key], State: ui.DailyOff}
		if key == "" {
			scope.Results = m.globals
		}
		if limits.Enabled {
			scope.State = ui.DailyFree
		}
		for _, g := range tripped {
			if scopeKeyOf(g, m.agents) != key {
				continue
			}
			info := g.Info()
			switch {
			case g.Kind == store.GuardLimit:
				scope.State, scope.Guard = ui.DailyBlocked, &info
			case scope.State != ui.DailyBlocked:
				scope.State, scope.Guard = ui.DailyIncomplete, &info
			}
		}
		return scope
	}
	status.Global = stateOf("", m.global)
	for _, a := range m.agents {
		if a.Mode != "live" {
			status.Paper = append(status.Paper, a.Name)
		}
		scope := stateOf(a.Name, a.DailyLimitOf())
		if scope.State != ui.DailyOff || a.DailyLimitOf().Enabled {
			status.Agents = append(status.Agents, scope)
		}
	}
	l.mu.Lock()
	l.status = status
	l.mu.Unlock()
}

// preview returns what limits of the agent a (all the agents together when
// nil) would do now against its result of the day read now (see
// ui.DailyLimitPreview): trip its guard (a limit reached), release it (its
// limit off, or no longer reached), or nothing; with the guard of the limit
// of the scope tripped today, if any.
func (l *dailyLimiter) preview(ctx context.Context, a *store.Agent,
	limits ui.DailyLimitSettings) (ui.DailyLimitPreview, *store.DailyGuard, error) {
	key := ""
	if a != nil {
		key = a.Name
	}
	day := l.dayOf(l.s.now())
	tripped, err := l.s.store.TrippedDailyGuards(ctx)
	if err != nil {
		return ui.DailyLimitPreview{}, nil, err
	}
	agents, err := l.s.store.Agents(ctx)
	if err != nil {
		return ui.DailyLimitPreview{}, nil, err
	}
	var guard *store.DailyGuard
	for i, g := range tripped {
		if g.Kind == store.GuardLimit && g.DayOf() == day && scopeKeyOf(g, agents) == key {
			guard = &tripped[i]
		}
	}
	preview := ui.DailyLimitPreview{Effect: ui.DailyEffectNone, Message: "Sem efeito imediato."}
	if !limits.Enabled && guard == nil {
		return preview, nil, nil
	}
	if a == nil || a.Mode == "live" {
		m, err := l.measure(ctx)
		if err != nil {
			return ui.DailyLimitPreview{}, nil, err
		}
		if a == nil {
			preview.Result, preview.Results = weighed(m.globals, limits, m.metric.missing), m.globals
		} else {
			preview.Result = m.results[key]
		}
	}
	result := preview.Result
	known := result != nil && result.Complete
	trigger := ""
	if known {
		trigger, _ = breach(result.Total, limits)
	}
	switch {
	case guard != nil && (!limits.Enabled || (known && trigger == "")):
		preview.Effect = ui.DailyEffectRelease
		preview.Message = fmt.Sprintf("Com esses limites, a trava de hoje de %s é liberada e as entradas voltam a "+
			"ser permitidas agora; as posições zeradas não são reabertas.", scopeName(key))
	case guard == nil && limits.Enabled && known && trigger != "":
		preview.Effect = ui.DailyEffectTrip
		preview.Message = fmt.Sprintf("O resultado de hoje de %s (%s) já atinge esse limite: as posições serão "+
			"zeradas, as ordens pendentes de entrada canceladas e as entradas bloqueadas até 00:00.",
			scopeName(key), money(result.Total, result.Currency))
	case limits.Enabled && !known:
		preview.Message = "O resultado de hoje ainda não pôde ser lido: o limite vale assim que for lido."
	}
	return preview, guard, nil
}

// change sets limits of the agent a (all the agents together when nil) for
// change, see preview: refused (applied false) when it would trip or release
// the guard now unless confirm; saved and recorded; a release applied at
// once, its agents' entries published; a trip left to the next evaluation,
// which closes and blocks as usual (now, woken).
func (l *dailyLimiter) change(ctx context.Context, a *store.Agent, limits ui.DailyLimitSettings,
	change store.DailyChange, confirm bool) (ui.DailyLimitPreview, bool, error) {
	change.Day = l.dayOf(l.s.now())
	preview, guard, err := l.preview(ctx, a, limits)
	if err != nil {
		return preview, false, err
	}
	if preview.Effect != ui.DailyEffectNone && !confirm {
		return preview, false, nil
	}
	if a == nil {
		_, _, err = l.s.store.SetGlobalDailyLimit(ctx, limits, change)
	} else {
		_, _, err = l.s.store.SetAgentDailyLimit(ctx, a.ID, limits, change)
	}
	if err != nil {
		return preview, false, err
	}
	key := ""
	var names []string
	if a != nil {
		key, names = a.Name, []string{a.Name}
	}
	l.mu.Lock()
	l.breaches[key] = 0
	l.mu.Unlock()
	if preview.Effect == ui.DailyEffectRelease && guard != nil {
		release := change
		release.Reason = "limites alterados"
		if change.Reason != "" {
			release.Reason += ": " + change.Reason
		}
		if _, ok, err := l.s.store.ReleaseDailyGuard(ctx, guard.ID, release, preview.Result); err != nil {
			return preview, true, err
		} else if ok {
			l.publish(ctx, names, false)
		}
	}
	log.Infof("daily limits: the limits of %s changed by %s (%s): %s", scopeName(key), change.By, change.Via,
		preview.Effect)
	l.Reevaluate()
	return preview, true, nil
}
