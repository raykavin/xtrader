package controller

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// This file has the daily loss limits move the stop losses of the open
// positions at the broker within the loss left of the day, so that the
// protection stays at the broker whatever becomes of the application: every
// minute (the candle of the stops, at once on a change of the limits), for
// each scope whose loss limit and stops are on (see ui.DailyLimitSettings.
// BrokerStops), the loss left is the limit plus the result of the day of the
// scope (realized and open, see dailyMetrics) less the exit costs of its
// positions (their entry commission and fee); it is shared among the open
// positions of the scope in proportion to the loss to their stop losses now
// (a position without one weighed by its exposure), the positions whose stop
// is tighter than their share keeping it and leaving the rest to the others;
// each share is a stop loss level, by the value of a tick of the symbol. A
// stop only tightens, never loosens; one asked stays in force, the one of the
// tighter scope winning. The agent running moves it (see ui.DailyStop, its
// stop guard on meanwhile); the controller moves the one of a stopped agent
// on its bridge. Each stop asked and its last move are recorded (see
// store.DailyStop). The legs of the strategy pairs, protecting each other,
// get none. Hit, the stops leave the day about at the limit: never exactly,
// with the slippage and the gaps; the close of the limit stays behind them.

// The cadence and the thresholds of the stops of the daily loss limits.
const (
	dailyStopsEvery = time.Minute      // the candle of the stops: computed once a minute, at once on a change
	dailyStopStep   = 0.02             // a stop moved when it tightens by more than this share of the distance to it
	dailySymbolFor  = 10 * time.Minute // a symbol read kept for the stops
	dailyStopsWait  = 30 * time.Second // a round of moves, at most
)

// dailySymbol is what the stops need of a symbol of an account, of GET
// /symbol of its bridge: the value of a tick of a lot lost (else of the one
// won) in the currency of the account, the step of the prices, and the least
// distances of the broker in points.
type dailySymbol struct {
	Digits      int     `json:"digits"`
	Point       float64 `json:"point"`
	TickSize    float64 `json:"trade_tick_size"`
	TickValue   float64 `json:"trade_tick_value"`
	TickLoss    float64 `json:"trade_tick_value_loss"`
	StopsLevel  int     `json:"trade_stops_level"`
	FreezeLevel int     `json:"trade_freeze_level"`
}

// tick returns the step of the prices of the symbol.
func (s dailySymbol) tick() float64 {
	if s.TickSize > 0 {
		return s.TickSize
	}
	return s.Point
}

// perUnit returns the value of a move of 1 in the price of a lot, in the
// currency of the account, 0 when unknown.
func (s dailySymbol) perUnit() float64 {
	value := s.TickLoss
	if value <= 0 {
		value = s.TickValue
	}
	if value <= 0 || s.tick() <= 0 {
		return 0
	}
	return value / s.tick()
}

// stops returns the least distance of a stop loss from the market, in price.
func (s dailySymbol) stops() float64 { return float64(max(s.StopsLevel, 0)) * s.Point }

// dailyHolding is the open position of an agent on a symbol of an account as
// the stops weigh it: its tickets (moving together under the stop of the
// agent, see order.Controller), side, lots, the price it exits at now (the bid
// of a long, the ask of a short), its stop loss now (the least protective of
// its tickets, 0 when one has none), its profit open, its exit costs (about
// the commission and fee of its entries) and its symbol; why it gets no stop
// (excluded).
type dailyHolding struct {
	agent    store.Agent
	account  string // the key of its account, see bridgeAccount.key
	bridge   string
	pair     string
	sell     bool
	tickets  []bridgePosition
	volume   float64
	price    float64
	stop     float64
	open     float64
	costs    float64
	currency string
	symbol   dailySymbol
	excluded string
}

// dir returns the direction of the holding: 1 for a long, -1 for a short.
func (h dailyHolding) dir() float64 {
	if h.sell {
		return -1
	}
	return 1
}

// side returns the side of the holding, BUY or SELL.
func (h dailyHolding) side() string {
	if h.sell {
		return "SELL"
	}
	return "BUY"
}

// value returns the value of a move of 1 in its price, in the currency of its
// account.
func (h dailyHolding) value() float64 { return h.volume * h.symbol.perUnit() }

// risk returns what the holding loses from now to its stop loss, false without
// one.
func (h dailyHolding) risk() (float64, bool) {
	if h.stop <= 0 {
		return 0, false
	}
	return math.Max(0, (h.price-h.stop)*h.dir()*h.value()), true
}

// key returns what tells the holding apart: its agent, account and symbol.
func (h dailyHolding) key() string { return h.agent.Name + "|" + h.account + "|" + h.pair }

// tighter reports whether level protects the holding more than other: any
// level against none.
func (h dailyHolding) tighter(level, other float64) bool {
	if other <= 0 {
		return level > 0
	}
	return h.dir()*(level-other) > 0
}

// onLosingSide reports whether level is a stop loss the broker takes now:
// beyond the least distance from the price, on its losing side.
func (h dailyHolding) onLosingSide(level float64) bool {
	return level > 0 && h.dir()*(h.price-level) > h.symbol.stops()
}

// moves reports whether moving the stop loss of the holding to level is worth
// it: tighter than its stop now by more than dailyStopStep of the distance to
// it, a tick at least; always without a stop.
func (h dailyHolding) moves(level float64) bool {
	if h.stop <= 0 {
		return level > 0
	}
	step := math.Max(h.symbol.tick(), dailyStopStep*math.Abs(h.price-h.stop))
	return h.dir()*(level-h.stop) > step-1e-12
}

// dailyTarget is the stop loss the loss left of a scope asks of a holding (by
// its index): its level, the share of the loss left, the loss to the stop
// before (nil without one), and whether the least distance of the broker made
// it looser than the share.
type dailyTarget struct {
	holding int
	level   float64
	share   float64
	risk    *float64
	clamped bool
}

// dailyStopTargets returns the stop losses of the holdings of a scope within
// its loss left, see the file: limit its loss limit, total its result of the
// day (realized plus open), holdings its open positions (the excluded ones
// weighed in the loss left, never given a stop); also the exit costs set aside
// and the loss left. None when nothing is left (the close of the limit sees to
// it) or the stops now fit in it.
func dailyStopTargets(limit, total float64, holdings []dailyHolding) ([]dailyTarget, float64, float64) {
	costs := 0.0
	for _, h := range holdings {
		costs += math.Max(0, -h.costs)
	}
	headroom := limit + total - costs
	if headroom <= 0 {
		return nil, costs, headroom
	}
	type weighed struct {
		index   int
		risk    float64
		bounded bool
		weight  float64
	}
	var eligible []weighed
	riskSum, notionalSum := 0.0, 0.0
	for i, h := range holdings {
		if h.excluded != "" || h.value() <= 0 || h.price <= 0 {
			continue
		}
		risk, bounded := h.risk()
		eligible = append(eligible, weighed{index: i, risk: risk, bounded: bounded})
		if bounded {
			riskSum += risk
			notionalSum += h.price * h.value()
		}
	}
	// the weights: the loss to the stop now; without one, the exposure, as if
	// at the mean distance of the others (or the exposure alone)
	perNotional := 1.0
	if notionalSum > 0 && riskSum > 0 {
		perNotional = riskSum / notionalSum
	}
	for i := range eligible {
		w := &eligible[i]
		if w.bounded {
			w.weight = w.risk
		} else {
			h := holdings[w.index]
			w.weight = perNotional * h.price * h.value()
		}
	}
	// the shares, the stops tighter than theirs kept, their rest to the others
	active, left := eligible, headroom
	shares := make(map[int]float64)
	for len(active) > 0 {
		total := 0.0
		for _, w := range active {
			total += w.weight
		}
		var next []weighed
		for _, w := range active {
			share := left * w.weight / total
			if total <= 0 {
				share = left / float64(len(active))
			}
			if w.bounded && w.risk <= share {
				left -= w.risk // fits: kept
				continue
			}
			next = append(next, w)
		}
		if len(next) == len(active) {
			for _, w := range active {
				if total > 0 {
					shares[w.index] = left * w.weight / total
				} else {
					shares[w.index] = left / float64(len(active))
				}
			}
			break
		}
		active = next
	}
	var targets []dailyTarget
	for _, w := range eligible {
		share, ok := shares[w.index]
		if !ok {
			continue
		}
		h := holdings[w.index]
		target := dailyTarget{holding: w.index, share: share}
		if w.bounded {
			risk := w.risk
			target.risk = &risk
		}
		level := h.price - h.dir()*share/h.value()
		if level <= 0 {
			continue // beyond any price a long reaches: no stop needed for its share
		}
		target.level, target.clamped = dailyLevel(h, level)
		if target.level <= 0 || !h.moves(target.level) {
			continue
		}
		targets = append(targets, target)
	}
	return targets, costs, headroom
}

// dailyLevel returns level as a stop loss of the holding the broker takes:
// rounded toward the price to its tick (never looser than the share), and
// beyond the least distance of the broker from the price by a tick (then
// looser than the share: clamped).
func dailyLevel(h dailyHolding, level float64) (float64, bool) {
	tick := h.symbol.tick()
	round := func(value float64, up bool) float64 {
		if tick <= 0 {
			return value
		}
		steps := value / tick
		if up {
			steps = math.Ceil(steps - 1e-9)
		} else {
			steps = math.Floor(steps + 1e-9)
		}
		digits := max(h.symbol.Digits, 0)
		scale := math.Pow10(digits)
		return math.Round(steps*tick*scale) / scale
	}
	limit := h.price - h.dir()*(h.symbol.stops()+tick) // the nearest level the broker takes
	if h.sell {
		level, limit = round(level, false), round(limit, true)
		if level < limit {
			return limit, true
		}
		return level, false
	}
	level, limit = round(level, true), round(limit, false)
	if level > limit {
		return limit, true
	}
	return level, false
}

// dailyStopPlan is a round of the stops: the holdings read, the stop of each
// one asked (recorded, by the key of its holding), the agents whose stops in
// force changed (their runtime published) and the ones to move now.
type dailyStopPlan struct {
	holdings []dailyHolding
	stops    map[string]store.DailyStop // by holding key, the ones in force
	changed  map[string]bool            // the agents whose stops in force changed
	moves    map[string][]int           // by agent, the holdings whose stop to move now
}

// stopsStep runs a round of the stops of the daily loss limits once a minute,
// or at once when due (a change of the limits), see the file: planned now,
// the moves sent in the background, one round at a time.
func (l *dailyLimiter) stopsStep(ctx context.Context, m *dailyMeasure, tripped []store.DailyGuard) {
	minute := m.at.Truncate(dailyStopsEvery)
	l.mu.Lock()
	due := l.stopsDue || !minute.Equal(l.stopsMinute)
	if !due || l.stopsBusy {
		l.mu.Unlock()
		return
	}
	l.stopsDue, l.stopsMinute, l.stopsBusy = false, minute, true
	l.mu.Unlock()
	plan, err := l.planStops(ctx, m, tripped)
	if err != nil {
		log.Errorf("daily limits: the stops at the broker: %v", err)
		l.mu.Lock()
		l.stopsBusy = false
		l.mu.Unlock()
		return
	}
	run := func() {
		defer func() {
			l.mu.Lock()
			l.stopsBusy = false
			l.mu.Unlock()
		}()
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dailyStopsWait)
		defer cancel()
		l.applyStops(runCtx, plan)
	}
	if l.stopsSync {
		run()
		return
	}
	go run()
}

// endStops ends the stops of the daily loss limits in force, the limits off,
// their agents' runtime published.
func (l *dailyLimiter) endStops(ctx context.Context) {
	l.mu.Lock()
	inForce := l.stopsInForce
	l.mu.Unlock()
	if !inForce {
		return
	}
	stops, err := l.s.store.DailyStopsInForce(ctx, 0)
	if err != nil {
		log.Errorf("daily limits: the stops at the broker: %v", err)
		return
	}
	if _, err := l.s.store.EndDailyStops(ctx, nil); err != nil {
		log.Errorf("daily limits: the stops at the broker: %v", err)
		return
	}
	agents, _ := l.s.store.Agents(ctx)
	names := make(map[int64]string, len(agents))
	for _, a := range agents {
		names[a.ID] = a.Name
	}
	refreshed := map[int64]bool{}
	for _, stop := range stops {
		if !refreshed[stop.AgentID] {
			refreshed[stop.AgentID] = true
			if err := l.s.manager.RefreshRuntime(ctx, names[stop.AgentID]); err != nil {
				log.Warnf("daily limits: the runtime of the agent %s: %v", names[stop.AgentID], err)
			}
		}
	}
	l.mu.Lock()
	l.stopsInForce, l.stopInfos = false, nil
	l.mu.Unlock()
}

// holdingsOf returns the open positions of the agents of the accounts read of
// m, by agent and symbol of each account, with the symbols read on their
// bridges (kept dailySymbolFor), the costs of their entries of owners.
func (l *dailyLimiter) holdingsOf(ctx context.Context, m *dailyMeasure) []dailyHolding {
	var holdings []dailyHolding
	for i, account := range m.accounts {
		read := m.reads[i]
		if read.gap != "" {
			continue
		}
		key := read.account.key(account.bridge)
		byKey := make(map[string]int)
		for _, p := range read.positions {
			agent, ok := ownerOf(account.agents, p.Magic, p.Symbol)
			if !ok {
				continue
			}
			k := agent.Name + "|" + p.Symbol
			index, ok := byKey[k]
			if !ok {
				holdings = append(holdings, dailyHolding{agent: agent, account: key, bridge: account.bridge,
					pair: p.Symbol, sell: p.Type == 1, currency: read.account.currency(), stop: p.SL})
				index = len(holdings) - 1
				byKey[k] = index
				if strings.EqualFold(agent.AllParams()["strategy"], "pairs") {
					holdings[index].excluded = "as pernas da estratégia pairs se protegem uma à outra: a posição " +
						"fica com a zeragem do limite"
				}
			}
			h := &holdings[index]
			if (p.Type == 1) != h.sell {
				h.excluded = "posições compradas e vendidas no mesmo ativo: fica com a zeragem do limite"
			}
			h.tickets = append(h.tickets, p)
			h.volume += p.Volume
			h.price = p.PriceCurrent
			h.open += p.Profit + p.Swap
			if p.SL <= 0 || h.stop <= 0 {
				h.stop = 0
			} else if h.dir()*(p.SL-h.stop) < 0 { // the least protective of its tickets
				h.stop = p.SL
			}
			l.mu.Lock()
			owner := l.owners[key+"/"+strconv.FormatInt(p.Ticket, 10)]
			l.mu.Unlock()
			h.costs += owner.entryCosts
		}
		for j := range holdings {
			h := &holdings[j]
			if h.account != key || h.symbol.perUnit() > 0 {
				continue
			}
			symbol, err := l.symbolOf(ctx, account, h.pair)
			if err != nil {
				h.excluded = "o símbolo não pôde ser lido na bridge: " + err.Error()
				continue
			}
			h.symbol = symbol
		}
	}
	return holdings
}

// symbolOf returns the symbol of account, read on its bridge, kept
// dailySymbolFor.
func (l *dailyLimiter) symbolOf(ctx context.Context, account liveAccount, pair string) (dailySymbol, error) {
	key := account.bridge + "|" + pair
	l.mu.Lock()
	cached, ok := l.symbols[key]
	l.mu.Unlock()
	if ok && l.s.now().Sub(cached.at) < dailySymbolFor {
		return cached.symbol, nil
	}
	var symbol dailySymbol
	p := l.s.positions
	gap, err := p.readBridge(ctx, account, "the symbol of the stops of the daily limits", func(token string) error {
		return p.get(ctx, account.bridge, "/symbol?"+url.Values{"symbol": {pair}}.Encode(), token, &symbol)
	})
	if gap != "" || err != nil {
		return dailySymbol{}, fmt.Errorf("%s", gapText(gap))
	}
	if symbol.perUnit() <= 0 {
		return dailySymbol{}, fmt.Errorf("sem o valor do tick de %s", pair)
	}
	l.mu.Lock()
	l.symbols[key] = cachedSymbol{symbol: symbol, at: l.s.now()}
	l.mu.Unlock()
	return symbol, nil
}

// cachedSymbol is a symbol read for the stops, and when.
type cachedSymbol struct {
	symbol dailySymbol
	at     time.Time
}

// planStops plans a round of the stops of m, see the file: the stops of the
// scopes whose loss limit and stops are on (none tripped, its result whole),
// the tightest of each holding, a stop in force kept unless a tighter one; the
// ones of the holdings out of these scopes, closed, or of the days before
// ended; all recorded.
func (l *dailyLimiter) planStops(ctx context.Context, m *dailyMeasure, tripped []store.DailyGuard) (*dailyStopPlan,
	error) {
	plan := &dailyStopPlan{stops: make(map[string]store.DailyStop), changed: make(map[string]bool),
		moves: make(map[string][]int)}
	trippedScope := func(key string) bool {
		return slices.ContainsFunc(tripped, func(g store.DailyGuard) bool {
			return g.DayOf() == m.day && (scopeKeyOf(g, m.agents) == key || g.AgentID == nil && g.Kind == store.GuardLimit)
		})
	}
	var scopes []dailyScope
	for _, scope := range m.scopes() {
		if scope.limits.StopsOn() {
			scopes = append(scopes, scope)
		}
	}
	before, err := l.s.store.DailyStopsInForce(ctx, 0)
	if err != nil {
		return nil, err
	}
	if len(scopes) == 0 && len(before) == 0 {
		return plan, nil
	}
	plan.holdings = l.holdingsOf(ctx, m)
	covered := make(map[int]bool) // the holdings of a scope whose stops are on
	type candidate struct {
		target dailyTarget
		scope  string
		budget store.DailyStopBudget
	}
	best := make(map[int]candidate)
	for _, scope := range scopes {
		var members []int
		for i, h := range plan.holdings {
			if scope.key == "" || h.agent.Name == scope.key {
				members = append(members, i)
				covered[i] = true
			}
		}
		if trippedScope(scope.key) || len(members) == 0 {
			continue // the close of the limit sees to it, or nothing to stop
		}
		// all the agents: each currency of the accounts apart, as the limit
		groups := map[string]ui.DailyResult{}
		if scope.key == "" {
			for _, result := range m.globals {
				groups[result.Currency] = result
			}
		} else if result := m.results[scope.key]; result != nil {
			groups[result.Currency] = *result
		}
		for currency, result := range groups {
			if !result.Complete {
				continue
			}
			var group []dailyHolding
			var indexes []int
			for _, i := range members {
				if plan.holdings[i].currency == currency {
					group = append(group, plan.holdings[i])
					indexes = append(indexes, i)
				}
			}
			targets, costs, headroom := dailyStopTargets(*scope.limits.MaxLoss, result.Total, group)
			for _, target := range targets {
				i := indexes[target.holding]
				h := plan.holdings[i]
				if current, ok := best[i]; ok && !h.tighter(target.level, current.target.level) {
					continue
				}
				target.holding = i
				best[i] = candidate{target: target, scope: scope.key, budget: store.DailyStopBudget{
					Limit: *scope.limits.MaxLoss, Realized: result.Realized, Open: result.Open, Costs: costs,
					Headroom: headroom, Share: target.share, Risk: target.risk}}
			}
		}
	}

	previous := make(map[string]store.DailyStop, len(before))
	for _, stop := range before {
		previous[fmt.Sprintf("%d|%s|%s|%s", stop.AgentID, stop.Account, stop.Pair, stop.Side)] = stop
	}
	var keep []int64
	inForce := map[int64]bool{}
	for i, h := range plan.holdings {
		if !covered[i] || h.excluded != "" {
			continue
		}
		prev, had := previous[fmt.Sprintf("%d|%s|%s|%s", h.agent.ID, h.account, h.pair, h.side())]
		if had && prev.DayOf() != m.day {
			had = false // of a day before: the loss left of today decides
		}
		c, has := best[i]
		level := 0.0
		target := store.DailyStopTarget{Day: m.day, AgentID: h.agent.ID, Account: h.account, Pair: h.pair,
			Side: h.side(), Currency: h.currency}
		for _, t := range h.tickets {
			target.Tickets = append(target.Tickets, t.Ticket)
		}
		switch {
		case has && had && h.tighter(prev.Level, c.target.level) && h.onLosingSide(prev.Level):
			level = prev.Level // asked before, tighter: kept
			target.Scope, target.Budget = prev.Scope, prev.BudgetOf()
		case has:
			level = c.target.level
			target.Scope, target.Budget = c.scope, c.budget
		case had && h.onLosingSide(prev.Level):
			level = prev.Level
			target.Scope, target.Budget = prev.Scope, prev.BudgetOf()
		default:
			continue
		}
		target.Level = level
		if had && prev.Level == level && sameTickets(prev.TicketsOf(), target.Tickets) {
			plan.stops[h.key()] = prev // unchanged: not written again
		} else {
			stop, err := l.s.store.SetDailyStop(ctx, target)
			if err != nil {
				log.Errorf("daily limits: recording the stop of %s on %s: %v", h.agent.Name, h.pair, err)
				continue
			}
			plan.stops[h.key()] = stop
			plan.changed[h.agent.Name] = true
		}
		stop := plan.stops[h.key()]
		keep = append(keep, stop.ID)
		inForce[stop.ID] = true
		if h.moves(level) {
			plan.moves[h.agent.Name] = append(plan.moves[h.agent.Name], i)
		}
	}
	// the stops no longer in force: their agents' runtime published
	names := make(map[int64]string, len(m.agents))
	for _, a := range m.agents {
		names[a.ID] = a.Name
	}
	for _, stop := range before {
		if !inForce[stop.ID] {
			plan.changed[names[stop.AgentID]] = true
		}
	}
	if _, err := l.s.store.EndDailyStops(ctx, keep); err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.stopsInForce = len(keep) > 0
	l.mu.Unlock()
	return plan, nil
}

// sameTickets reports whether a and b hold the same tickets, in any order.
func sameTickets(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// applyStops sends the moves of plan: the stops of an agent running asked of
// it, all of them, each answered by the broker; the ones of an agent stopped
// (launched by the controller) moved on its bridge, its launch fenced, each
// ticket checked on the account; the ones of an agent run apart skipped. The
// moves recorded, the runtime of the agents whose stops changed published,
// and the stops in force told on the dashboard.
func (l *dailyLimiter) applyStops(ctx context.Context, plan *dailyStopPlan) {
	var wg sync.WaitGroup
	for name, indexes := range plan.moves {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := plan.holdings[indexes[0]].agent
			switch {
			case l.s.running(name):
				l.moveThroughAgent(ctx, plan, name)
			case !a.Launched:
				for _, i := range indexes {
					h := plan.holdings[i]
					l.recordMove(ctx, plan.stops[h.key()], store.DailyStopMove{Via: "", Move: ui.DailyStopSkipped,
						Previous: h.stop, Requested: plan.stops[h.key()].Level,
						Detail: "o agente roda fora do controller: ajuste o stop onde ele roda"})
				}
			default:
				l.moveOnBridge(ctx, plan, a, indexes)
			}
		}()
	}
	wg.Wait()
	for name := range plan.changed {
		if name == "" {
			continue
		}
		if err := l.s.manager.RefreshRuntime(ctx, name); err != nil {
			log.Warnf("daily limits: the runtime of the agent %s: %v", name, err)
		}
	}
	l.refreshStopInfos(ctx)
}

// moveThroughAgent asks the agent name running for all its stops in force,
// each moved at once, and records how each went.
func (l *dailyLimiter) moveThroughAgent(ctx context.Context, plan *dailyStopPlan, name string) {
	var request ui.SupervisionDailyStopsRequest
	var sent []dailyHolding
	for _, h := range plan.holdings {
		if stop, ok := plan.stops[h.key()]; ok && h.agent.Name == name {
			request.Stops = append(request.Stops, stop.Request())
			sent = append(sent, h)
		}
	}
	var response ui.SupervisionDailyStopsResponse
	err := l.s.callAgent(ctx, name, "POST", "supervision/daily-stops", request, &response)
	for i, h := range sent {
		stop := plan.stops[h.key()]
		move := store.DailyStopMove{Via: store.StopViaAgent, Previous: h.stop, Requested: stop.Level}
		switch {
		case err != nil:
			move.Move, move.Detail = ui.DailyStopUnknown, "o agente não respondeu: "+err.Error()+
				"; o runtime leva o stop, aplicado no próximo preço"
		case i >= len(response.Results):
			move.Move, move.Detail = ui.DailyStopUnknown, "o agente não respondeu por este stop"
		default:
			result := response.Results[i]
			move.Detail, move.Sent = result.Detail, true
			if result.Previous > 0 {
				move.Previous = result.Previous
			}
			if result.Requested > 0 {
				move.Requested = result.Requested
			}
			switch result.Status {
			case ui.ProtectApplied:
				move.Move, move.Confirmed = ui.DailyStopConfirmed, result.Confirmed
			case ui.ProtectArmed:
				move.Move, move.Sent = ui.DailyStopRequested, false
			case ui.ProtectUnknown:
				move.Move = ui.DailyStopUnknown
			case ui.ProtectRefused:
				move.Move, move.Sent = ui.DailyStopRejected, false
			default:
				move.Move = ui.DailyStopRejected
			}
		}
		l.recordMove(ctx, stop, move)
	}
}

// moveOnBridge moves the stops of the holdings of indexes of the agent a, not
// running, on its bridge, ticket by ticket, its launch fenced meanwhile.
func (l *dailyLimiter) moveOnBridge(ctx context.Context, plan *dailyStopPlan, a store.Agent, indexes []int) {
	release := l.s.manager.Fence(a.Name)
	defer release()
	skip := func(detail string) {
		for _, i := range indexes {
			h := plan.holdings[i]
			stop := plan.stops[h.key()]
			l.recordMove(ctx, stop, store.DailyStopMove{Via: store.StopViaBridge, Move: ui.DailyStopSkipped,
				Previous: h.stop, Requested: stop.Level, Detail: detail})
		}
	}
	if l.s.running(a.Name) {
		skip("o agente voltou a rodar: o stop vai na próxima rodada")
		return
	}
	secrets, err := l.s.store.SecretsOf(a)
	if err != nil {
		skip("o token da bridge não pôde ser lido: " + err.Error())
		return
	}
	for _, i := range indexes {
		h := plan.holdings[i]
		stop := plan.stops[h.key()]
		move := store.DailyStopMove{Via: store.StopViaBridge, Previous: h.stop, Requested: stop.Level,
			Move: ui.DailyStopConfirmed, Confirmed: stop.Level}
		for _, ticket := range h.tickets {
			if ticket.SL > 0 && !h.tighter(stop.Level, ticket.SL) {
				continue // as tight already
			}
			status, detail, confirmed := l.s.bridges.setStop(ctx, h.bridge, secrets.BridgeToken, ticket,
				stop.Level, h.sell, bridgeAgentOf(a))
			if status != ui.DailyStopSkipped {
				move.Sent = true
			}
			if status != ui.DailyStopConfirmed {
				move.Move, move.Confirmed = status, 0
				move.Detail = fmt.Sprintf("ticket %d: %s", ticket.Ticket, detail)
				break
			}
			move.Confirmed = confirmed
		}
		l.recordMove(ctx, stop, move)
	}
}

// recordMove records the move of the stop stop, logged as the broker
// answered it.
func (l *dailyLimiter) recordMove(ctx context.Context, stop store.DailyStop, move store.DailyStopMove) {
	if stop.ID == 0 {
		return
	}
	if err := l.s.store.SetDailyStopMove(ctx, stop.ID, move); err != nil {
		log.Errorf("daily limits: recording the move of the stop %d: %v", stop.ID, err)
	}
	switch move.Move {
	case ui.DailyStopConfirmed:
		log.Infof("daily limits: the stop of %s on %s at %g (was %g), confirmed at %g (%s)", scopeName(stop.Scope),
			stop.Pair, move.Requested, move.Previous, move.Confirmed, move.Via)
	case ui.DailyStopRequested:
		log.Infof("daily limits: the stop of %s on %s at %g asked: %s", scopeName(stop.Scope), stop.Pair,
			move.Requested, move.Detail)
	default:
		log.Warnf("daily limits: the stop of %s on %s at %g %s (%s): %s", scopeName(stop.Scope), stop.Pair,
			move.Requested, move.Move, move.Via, move.Detail)
	}
}

// refreshStopInfos reads the stops in force for the dashboard, see Status.
func (l *dailyLimiter) refreshStopInfos(ctx context.Context) {
	stops, err := l.s.store.DailyStopsInForce(ctx, 0)
	if err != nil {
		log.Warnf("daily limits: the stops at the broker: %v", err)
		return
	}
	agents, err := l.s.store.Agents(ctx)
	if err != nil {
		return
	}
	names := make(map[int64]string, len(agents))
	for _, a := range agents {
		names[a.ID] = a.Name
	}
	infos := make([]ui.DailyStopInfo, 0, len(stops))
	for _, stop := range stops {
		infos = append(infos, stop.Info(names[stop.AgentID]))
	}
	l.mu.Lock()
	l.stopInfos = infos
	l.mu.Unlock()
}
