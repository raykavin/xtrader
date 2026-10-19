// Package protect protects the open positions of an agent: its breakeven and
// its trailing stop (see ui.ProtectionSettings), applied on every price the
// bot processes (see xtrader.CandleHook), and the ones the XTrader AI arms on
// a position, its trigger kept. The stop loss at the broker is its state: it
// only tightens it, through the stop guard of the order controller, which the
// strategies and the language model share (see order.Controller.
// TightenProtection), the most protective level winning, so that a restart
// goes on from it; the best price since the entry, its triggers, is rebuilt
// from the candles. A move never removes the stop loss: refused or lost at
// the broker, it is kept, and tried again after a while.
package protect

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/indicator"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

// Broker is the order controller as the protection moves the stop losses
// through it, see order.Controller.
type Broker interface {
	PositionStop(pair string) (order.Position, model.Order, bool)
	TightenProtection(leg model.Order, level float64) (model.Order, error)
	SetPositionStop(pair string, side model.SideType, level float64) ([]model.Order, error)
	Tick(pair string) float64
}

// Market is the exchange of the candles, for the ATR and the best price of a
// position adopted; and of the costs and the limits of the levels, when it
// tells them (service.PositionCostReader, service.ProtectionLimiter).
type Market interface {
	CandlesByLimit(ctx context.Context, pair, period string, limit int) ([]model.Candle, error)
	CandlesByPeriod(ctx context.Context, pair, period string, start, end time.Time) ([]model.Candle, error)
}

const (
	// ATRPeriod is the period of the ATR of the distances, Wilder's.
	ATRPeriod = 14
	// keptCandles are the closed candles kept for the ATR.
	keptCandles = 64
	// minInterval is the least time between two moves of a position.
	minInterval = 2 * time.Second
	// coolDown is the wait after a move refused or lost at the broker.
	coolDown = 10 * time.Second
)

// Manager protects the open positions of the pairs of an agent, see the
// package.
type Manager struct {
	ctx       context.Context
	broker    Broker
	market    Market
	timeframe string
	refPrefix string // of the references of the positions, see refOf
	now       func() time.Time

	guarded atomic.Bool // the stop guard of the order controller, see Guarded

	mu       sync.Mutex
	settings *ui.ProtectionSettings
	arms     map[string]map[string]ui.ProtectionArm // by position, by feature
	pairs    map[string]*pairState

	sends sync.WaitGroup
}

// pairState is what the manager follows of a pair: its closed candles and
// their ATR, its last candle, and its open position (its reference, side,
// best exit price since its entry), the moves of its features, and its state.
type pairState struct {
	candles   []model.Candle
	loading   bool // its closed candles being read, see bootstrap
	loaded    bool
	atr       float64
	last      model.Candle
	ref       string
	side      model.SideType
	best      float64
	rebuilt   bool // its best price read from the candles since its entry
	inflight  bool
	sentAt    time.Time
	coolUntil time.Time
	moves     map[string]*ui.ProtectionMove
	state     *ui.PositionProtection
}

// New returns the protection of the positions of an agent trading on
// timeframe through broker, its candles from market; refPrefix names its
// positions: mt5 for the ones of an MT5 account (by their identifier), paper
// and its run for the ones of the paper wallet (by their entry).
func New(ctx context.Context, broker Broker, market Market, timeframe, refPrefix string) *Manager {
	return &Manager{ctx: ctx, broker: broker, market: market, timeframe: timeframe, refPrefix: refPrefix,
		now: time.Now, arms: make(map[string]map[string]ui.ProtectionArm), pairs: make(map[string]*pairState)}
}

// Guarded reports whether the stop guard of the order controller is on: the
// protection runs, or may be armed, see order.Controller.SetStopGuard. It
// reads an atomic flag only.
func (m *Manager) Guarded(string) bool {
	return m.guarded.Load()
}

// Apply sets the settings of the protection, nil when off, and the arms of
// the XTrader AI in force, as the controller sends them (see
// agentapi.Protection).
func (m *Manager) Apply(settings *ui.ProtectionSettings, arms []ui.ProtectionArm) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if settings != nil {
		copied := *settings
		m.settings = &copied
	} else {
		m.settings = nil
	}
	m.arms = make(map[string]map[string]ui.ProtectionArm)
	for _, arm := range arms {
		m.armLocked(arm)
	}
	m.guarded.Store(m.settings != nil && m.settings.Active() || len(arms) > 0)
}

// armLocked adds arm. Must be called while holding m.mu.
func (m *Manager) armLocked(arm ui.ProtectionArm) {
	byFeature, ok := m.arms[arm.PositionRef]
	if !ok {
		byFeature = make(map[string]ui.ProtectionArm)
		m.arms[arm.PositionRef] = byFeature
	}
	byFeature[arm.Feature] = arm
}

// pair returns the state of pair. Must be called while holding m.mu.
func (m *Manager) pair(pair string) *pairState {
	ps, ok := m.pairs[pair]
	if !ok {
		ps = &pairState{moves: make(map[string]*ui.ProtectionMove)}
		m.pairs[pair] = ps
	}
	return ps
}

// OnCandle protects the position of the pair of candle at its price, a candle
// in progress or closed, see xtrader.CandleHook: the move, when one is due,
// sent apart, so that the candles never wait for the broker.
func (m *Manager) OnCandle(candle model.Candle) {
	m.mu.Lock()
	ps := m.pair(candle.Pair)
	ps.last = candle
	if candle.Complete {
		ps.addClosed(candle)
	}
	load := !ps.loaded && !ps.loading
	if load {
		ps.loading = true
	}
	p := m.evaluateLocked(candle.Pair, ps)
	m.mu.Unlock()
	if load {
		go m.bootstrap(candle.Pair)
	}
	if p != nil {
		m.sends.Add(1)
		go func() {
			defer m.sends.Done()
			m.apply(*p)
		}()
	}
}

// Wait waits for the moves sent to be answered: for the tests.
func (m *Manager) Wait() {
	m.sends.Wait()
}

// addClosed keeps candle, closed, for the ATR.
func (ps *pairState) addClosed(candle model.Candle) {
	if n := len(ps.candles); n > 0 && !candle.Time.After(ps.candles[n-1].Time) {
		if candle.Time.Equal(ps.candles[n-1].Time) {
			ps.candles[n-1] = candle
			ps.atr = atrOf(ps.candles)
		}
		return
	}
	ps.candles = append(ps.candles, candle)
	if len(ps.candles) > keptCandles {
		ps.candles = ps.candles[len(ps.candles)-keptCandles:]
	}
	ps.atr = atrOf(ps.candles)
}

// atrOf returns the ATR (see ATRPeriod) of the last of candles, 0 without
// enough of them.
func atrOf(candles []model.Candle) float64 {
	if len(candles) <= ATRPeriod {
		return 0
	}
	high, low, closes := make([]float64, len(candles)), make([]float64, len(candles)), make([]float64, len(candles))
	for i, c := range candles {
		high[i], low[i], closes[i] = c.High, c.Low, c.Close
	}
	atr := indicator.ATR(high, low, closes, ATRPeriod)
	last := atr[len(atr)-1]
	if math.IsNaN(last) || math.IsInf(last, 0) || last <= 0 {
		return 0
	}
	return last
}

// bootstrap reads the closed candles of pair for its ATR, once.
func (m *Manager) bootstrap(pair string) {
	candles, err := m.market.CandlesByLimit(m.ctx, pair, m.timeframe, keptCandles)
	m.mu.Lock()
	defer m.mu.Unlock()
	ps := m.pair(pair)
	ps.loading = false
	if err != nil {
		log.Warnf("[PROTECTION] %s: the candles of the ATR: %v: read from the next closes", pair, err)
		return
	}
	ps.loaded = true
	sort.Slice(candles, func(i, j int) bool { return candles[i].Time.Before(candles[j].Time) })
	for _, candle := range candles {
		if candle.Complete || candle.Time.Before(ps.last.Time) {
			ps.addClosed(candle)
		}
	}
}

// refOf returns the reference of position, stable across restarts on MT5:
// mt5:<identifier of the position> (the ticket of its entry); on the paper
// wallet, <prefix>:<entry>.
func (m *Manager) refOf(position order.Position) string {
	if m.refPrefix == "mt5" {
		return fmt.Sprintf("mt5:%d", position.Entry.ExchangeID)
	}
	return fmt.Sprintf("%s:%d", m.refPrefix, position.Entry.ID)
}

// plan is a move of the stop loss of the position of pair due: to level, by
// feature, from the leg as it is (none when hasStop is false).
type plan struct {
	pair     string
	side     model.SideType
	leg      model.Order
	hasStop  bool
	level    float64
	previous float64
	feature  string
}

// evaluateLocked works out the protection of the position of pair at its last
// price, sets its state, and returns the move due, nil for none: the most
// protective of the breakeven and the trailing stop, beyond the stop loss
// now, unless a move is in flight, was made less than minInterval ago, or
// failed less than coolDown ago. Must be called while holding m.mu.
func (m *Manager) evaluateLocked(pair string, ps *pairState) *plan {
	if m.settings == nil && len(m.arms) == 0 {
		ps.state = nil // off: the positions not even read
		return nil
	}
	position, leg, hasStop := m.broker.PositionStop(pair)
	if position.Quantity <= 0 {
		ps.ref, ps.best, ps.rebuilt, ps.state = "", 0, false, nil
		ps.moves = make(map[string]*ui.ProtectionMove)
		return nil
	}
	ref := m.refOf(position)
	if ref != ps.ref {
		ps.ref, ps.side, ps.best, ps.rebuilt = ref, position.Side, 0, false
		ps.moves = make(map[string]*ui.ProtectionMove)
		if !position.CreatedAt.IsZero() {
			go m.rebuild(pair, ref, position.Side, position.CreatedAt)
		}
	}
	candle := ps.last
	if candle.Close <= 0 {
		return nil // no price yet
	}
	side := position.Side
	exit := exitPrice(side, candle)
	for _, price := range []float64{exit, bestExit(side, candle)} {
		if better(side, price, ps.best) {
			ps.best = price
		}
	}
	current := 0.0
	if hasStop {
		current = order.StopLevel(leg)
	}
	state := &ui.PositionProtection{Pair: pair, PositionRef: ref, Side: string(side), Entry: position.AvgPrice,
		Price: exit, StopLoss: current, ATR: ps.atr, Spread: spreadOf(candle),
		Favorable: favorable(side, position.AvgPrice, ps.best, ps.atr)}
	settings := ui.ProtectionSettings{}
	if m.settings != nil {
		settings = *m.settings
	}
	arms := m.arms[ref]
	if m.settings == nil && len(arms) == 0 {
		ps.state = nil
		return nil
	}
	costs := m.costs(pair, position)
	state.Costs, state.CostsKnown = costs.Price, costs.Known
	tick := m.broker.Tick(pair)
	stops := m.stopsLevel(pair)

	var due *plan
	consider := func(feature string, level float64) {
		if due == nil || sign(side)*(level-due.level) > 0 {
			due = &plan{pair: pair, side: side, leg: leg, hasStop: hasStop, level: level, previous: current,
				feature: feature}
		}
	}
	state.Breakeven = m.breakeven(settings.Breakeven, arms, state, tick, stops, consider)
	state.Trailing = m.trailing(settings.Trailing, arms, state, tick, stops, consider)
	ps.state = state

	now := m.now()
	if due != nil && !ps.inflight && !now.Before(ps.sentAt.Add(minInterval)) && !now.Before(ps.coolUntil) {
		ps.inflight, ps.sentAt = true, now
		ps.moves[due.feature] = &ui.ProtectionMove{Status: ui.MoveRequested, Previous: due.previous,
			Requested: due.level, At: now.Unix()}
	} else {
		due = nil
	}
	ps.showMoves()
	return due
}

// showMoves puts the last move of each feature in the state of the position:
// a stop loss is shown at its level only once the broker confirmed it.
func (ps *pairState) showMoves() {
	if ps.state == nil {
		return
	}
	for feature, move := range ps.moves {
		copied := *move
		switch feature {
		case ui.ProtectionBreakeven:
			ps.state.Breakeven.Last = &copied
		case ui.ProtectionTrailing:
			ps.state.Trailing.Last = &copied
		}
		if move.Status == ui.MoveConfirmed && move.Confirmed > 0 &&
			(ps.state.StopLoss == 0 || sign(ps.side)*(move.Confirmed-ps.state.StopLoss) > 0) {
			ps.state.StopLoss = move.Confirmed
		}
	}
}

// featureOf returns the state of a feature, enabled or armed (and allowed to
// the supervisor), before its trigger: off, armed without the permission,
// unavailable without the ATR, waiting short of its trigger; false when it
// runs (its trigger reached).
func featureOf(enabled, supervisor bool, arm ui.ProtectionArm, armed bool, trigger float64,
	state *ui.PositionProtection) (ui.ProtectionFeature, bool) {
	feature := ui.ProtectionFeature{Enabled: enabled, Armed: armed}
	if armed {
		feature.ArmID, feature.ArmReason = arm.ID, arm.Reason
	}
	switch {
	case !enabled && (!armed || !supervisor):
		feature.State = ui.ProtectionOff
		if armed {
			feature.Detail = "armado pelo XTrader AI, sem permissão no agente"
		}
		return feature, false
	case state.ATR <= 0:
		feature.State, feature.Detail = ui.ProtectionUnavailable, fmt.Sprintf("aguardando o ATR(%d)", ATRPeriod)
		return feature, false
	case state.Favorable < trigger:
		feature.State, feature.Remaining = ui.ProtectionWaiting, trigger-state.Favorable
		return feature, false
	}
	feature.State = ui.ProtectionActive
	return feature, true
}

// breakeven returns the state of the breakeven of the position of state,
// telling consider its stop loss when it would tighten the one now.
func (m *Manager) breakeven(s ui.BreakevenSettings, arms map[string]ui.ProtectionArm, state *ui.PositionProtection,
	tick, stops float64, consider func(string, float64)) ui.ProtectionFeature {
	arm, armed := arms[ui.ProtectionBreakeven]
	feature, runs := featureOf(s.Enabled, s.Supervisor, arm, armed, s.Trigger, state)
	if !runs {
		return feature
	}
	side := model.SideType(state.Side)
	level := breakevenLevel(side, state.Entry, s.Margin, state.ATR, state.Costs, tick)
	feature.Level = level
	switch {
	case !onLosingSide(side, level, state.Price, stops):
		feature.State = ui.ProtectionWaiting
		feature.Detail = "o preço voltou para perto da entrada: o stop não fica além dele"
	case tighter(side, level, state.StopLoss, tick):
		consider(ui.ProtectionBreakeven, level)
	}
	return feature
}

// trailing returns the state of the trailing stop of the position of state,
// telling consider its stop loss when it would tighten the one now by its
// step.
func (m *Manager) trailing(s ui.TrailingSettings, arms map[string]ui.ProtectionArm, state *ui.PositionProtection,
	tick, stops float64, consider func(string, float64)) ui.ProtectionFeature {
	arm, armed := arms[ui.ProtectionTrailing]
	feature, runs := featureOf(s.Enabled, s.Supervisor, arm, armed, s.Trigger, state)
	if !runs {
		return feature
	}
	side := model.SideType(state.Side)
	level := trailingLevel(side, state.Price, s.Distance, state.ATR, tick)
	feature.Level = level
	if tighter(side, level, state.StopLoss, math.Max(s.Step*state.ATR, tick)) &&
		onLosingSide(side, level, state.Price, stops) {
		consider(ui.ProtectionTrailing, level)
	}
	if state.StopLoss > 0 && sign(side)*(state.StopLoss-level) > 0 {
		feature.Level = state.StopLoss // the stop loss is tighter already
	}
	return feature
}

// costs returns the costs of the position of pair as a distance of its price:
// as the exchange tells them, else, on the paper wallet, its fee and the one
// of its exit taken as the same.
func (m *Manager) costs(pair string, position order.Position) service.PositionCosts {
	if reader, ok := m.market.(service.PositionCostReader); ok {
		return reader.PositionCosts(pair)
	}
	if position.Quantity > 0 {
		return service.PositionCosts{Price: 2 * position.Fee / position.Quantity, Known: true}
	}
	return service.PositionCosts{}
}

// stopsLevel returns the least distance of a stop loss from the market at the
// exchange, 0 when unknown.
func (m *Manager) stopsLevel(pair string) float64 {
	if limiter, ok := m.market.(service.ProtectionLimiter); ok {
		if limits, ok := limiter.ProtectionLimits(pair); ok {
			return limits.StopsLevel
		}
	}
	return 0
}

// rebuild reads the best exit price of the position ref of pair, opened on
// side at opened, from the candles since: after a restart its triggers, from
// the best price since its entry, go on as before.
func (m *Manager) rebuild(pair, ref string, side model.SideType, opened time.Time) {
	candles, err := m.market.CandlesByPeriod(m.ctx, pair, m.timeframe, opened, m.now())
	if err != nil {
		log.Warnf("[PROTECTION] %s: the candles since the entry of %s: %v: the best price from now on", pair, ref,
			err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ps := m.pair(pair)
	if ps.ref != ref {
		return // another position meanwhile
	}
	for _, candle := range candles {
		if candle.Time.Before(opened) {
			continue
		}
		if price := bestExit(side, candle); better(side, price, ps.best) {
			ps.best = price
		}
	}
	ps.rebuilt = true
}

// apply sends the move p to the broker and records how it went, see
// ui.ProtectionMove: confirmed with the level the broker took, the stop loss
// kept when it was as tight already; rejected, or unknown when its answer was
// lost, the stop loss kept, the next move after coolDown.
func (m *Manager) apply(p plan) ui.ProtectionMove {
	move := ui.ProtectionMove{Status: ui.MoveRequested, Previous: p.previous, Requested: p.level, At: m.now().Unix()}
	var err error
	if p.hasStop {
		var moved model.Order
		if moved, err = m.broker.TightenProtection(p.leg, p.level); err == nil {
			move.Confirmed = order.StopLevel(moved)
		}
	} else {
		var legs []model.Order
		if legs, err = m.broker.SetPositionStop(p.pair, p.side, p.level); err == nil {
			for _, leg := range legs {
				if leg.Type == model.OrderTypeStopLoss {
					move.Confirmed = order.StopLevel(leg)
				}
			}
		}
	}

	var tighter *service.StopNotTighterError
	switch {
	case err == nil:
		move.Status = ui.MoveConfirmed
		log.Infof("[PROTECTION] %s | %s | stop loss %g -> %g", p.pair, p.feature, p.previous, move.Confirmed)
	case errors.As(err, &tighter):
		move.Status, move.Confirmed = ui.MoveConfirmed, tighter.Current
		move.Detail = "o stop já estava mais protetor"
	case errors.Is(err, exchange.ErrMT5OutcomeUnknown) || isProtectionError(err):
		move.Status, move.Detail = ui.MoveUnknown, err.Error()
		log.Warnf("[PROTECTION] %s | %s | the answer to the stop loss at %g lost: read again: %v", p.pair,
			p.feature, p.level, err)
	default:
		move.Status, move.Detail = ui.MoveRejected, err.Error()
		log.Warnf("[PROTECTION] %s | %s | the stop loss at %g refused, kept at %g: %v", p.pair, p.feature,
			p.level, p.previous, err)
	}

	m.mu.Lock()
	ps := m.pair(p.pair)
	ps.inflight = false
	if move.Status == ui.MoveRejected || move.Status == ui.MoveUnknown {
		ps.coolUntil = m.now().Add(coolDown)
	}
	recorded := move
	ps.moves[p.feature] = &recorded
	if ps.ref != "" {
		ps.showMoves()
	}
	m.mu.Unlock()
	return move
}

// isProtectionError reports whether err is a move of the levels of MT5 whose
// outcome is unknown on a position, see exchange.MT5ProtectionError.
func isProtectionError(err error) bool {
	var protection *exchange.MT5ProtectionError
	return errors.As(err, &protection)
}

// Protection returns the protection of the position of pair as it stands, nil
// without one, see ui.PositionProtection.
func (m *Manager) Protection(pair string) *ui.PositionProtection {
	m.mu.Lock()
	defer m.mu.Unlock()
	ps, ok := m.pairs[pair]
	if !ok || ps.state == nil {
		return nil
	}
	state := *ps.state
	return &state
}

// Protections returns the protection of the positions held, by pair.
func (m *Manager) Protections() []ui.PositionProtection {
	m.mu.Lock()
	defer m.mu.Unlock()
	var states []ui.PositionProtection
	for _, ps := range m.pairs {
		if ps.state != nil {
			states = append(states, *ps.state)
		}
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Pair < states[j].Pair })
	return states
}

// Arm arms a feature on the open position of request, by the XTrader AI, see
// ui.SupervisionProtectRequest: refused unless its feature is allowed to the
// supervisor and the position is the one held on its pair; applied at once
// when its trigger is reached, the answer of the broker awaited; else armed,
// applied once it is.
func (m *Manager) Arm(request ui.SupervisionProtectRequest) ui.SupervisionProtectResponse {
	refused := func(detail string) ui.SupervisionProtectResponse {
		return ui.SupervisionProtectResponse{Status: ui.ProtectRefused, Detail: detail}
	}
	m.mu.Lock()
	settings := m.settings
	switch {
	case request.Feature != ui.ProtectionBreakeven && request.Feature != ui.ProtectionTrailing:
		m.mu.Unlock()
		return refused(fmt.Sprintf("recurso desconhecido: %q", request.Feature))
	case settings == nil || request.Feature == ui.ProtectionBreakeven && !settings.Breakeven.Supervisor ||
		request.Feature == ui.ProtectionTrailing && !settings.Trailing.Supervisor:
		m.mu.Unlock()
		return refused("o XTrader AI não tem permissão para acionar esse recurso neste agente")
	}
	position, leg, hasStop := m.broker.PositionStop(request.Pair)
	if position.Quantity <= 0 {
		m.mu.Unlock()
		return refused(fmt.Sprintf("nenhuma posição aberta em %s", request.Pair))
	}
	if ref := m.refOf(position); ref != request.PositionRef {
		m.mu.Unlock()
		return refused(fmt.Sprintf("a posição aberta em %s é %s, não %s", request.Pair, ref, request.PositionRef))
	}
	previous := 0.0
	if hasStop {
		previous = order.StopLevel(leg)
	}
	m.armLocked(ui.ProtectionArm{ID: request.ArmID, Pair: request.Pair, PositionRef: request.PositionRef,
		Feature: request.Feature, Reason: request.Reason, ArmedAt: m.now().Unix()})
	m.guarded.Store(true)
	ps := m.pair(request.Pair)
	if ps.inflight {
		m.mu.Unlock()
		return ui.SupervisionProtectResponse{Status: ui.ProtectArmed, Previous: previous,
			Detail: "armado; um envio está em andamento, o próximo preço o avalia"}
	}
	ps.sentAt, ps.coolUntil = time.Time{}, time.Time{} // asked now: no wait
	p := m.evaluateLocked(request.Pair, ps)
	var feature ui.ProtectionFeature
	if ps.state != nil {
		feature = ps.state.Breakeven
		if request.Feature == ui.ProtectionTrailing {
			feature = ps.state.Trailing
		}
	}
	m.mu.Unlock()

	if p == nil {
		response := ui.SupervisionProtectResponse{Status: ui.ProtectArmed, Previous: previous,
			Protection: m.Protection(request.Pair)}
		switch feature.State {
		case ui.ProtectionWaiting:
			response.Detail = fmt.Sprintf("armado: aguardando o gatilho, faltam %.2f ATR", feature.Remaining)
			if feature.Detail != "" {
				response.Detail = "armado: " + feature.Detail
			}
		case ui.ProtectionActive:
			response.Detail = "armado: o stop já está tão protetor quanto o recurso pede"
		default:
			response.Detail = "armado: " + feature.Detail
		}
		return response
	}
	move := m.apply(*p)
	response := ui.SupervisionProtectResponse{Previous: move.Previous, Requested: move.Requested,
		Confirmed: move.Confirmed, Detail: move.Detail, Protection: m.Protection(request.Pair)}
	switch move.Status {
	case ui.MoveConfirmed:
		response.Status = ui.ProtectApplied
	case ui.MoveUnknown:
		response.Status = ui.ProtectUnknown
	default:
		response.Status = ui.ProtectFailed
	}
	return response
}
