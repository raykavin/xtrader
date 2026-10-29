package exchange

import (
	"fmt"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
)

// This file tells how the positions of the bot are protected now, and puts a
// stop loss on one without, see service.ProtectionReader, service.StopSetter,
// service.ProtectionLimiter and service.PositionCostReader.

// mt5Protective returns the level of leg protecting positions the most: for a
// stop loss the highest of a long position, the lowest of a short one; for a
// take profit the highest; 0 when none has it.
func mt5Protective(positions []mt5Position, leg string) float64 {
	best := 0.0
	for _, position := range positions {
		level := mt5Level(position.SL, position.TP, leg)
		switch {
		case level <= 0:
		case leg == mt5StopLoss && mt5Side(position.Type) == model.SideTypeSell:
			if best == 0 || level < best {
				best = level
			}
		case level > best:
			best = level
		}
	}
	return best
}

// LiveProtection returns leg as the book holds it now, see
// service.ProtectionReader: at the level of its pending entry, false; or at
// the most protective level of its open positions (see mt5Protective), true.
// A leg canceled, of an older protection, or of positions closed is
// ErrOrderNotOpen. It sends no request.
func (m *MT5) LiveProtection(leg model.Order) (model.Order, bool, error) {
	if m.dataOnly || m.book == nil {
		return model.Order{}, false, m.errDataOnly("LiveProtection")
	}
	known, ok := m.known(leg.ExchangeID)
	if !ok {
		if known, ok = m.lookup(leg); !ok {
			return model.Order{}, false, fmt.Errorf("mt5: order %d is not a leg of an open position: %w",
				leg.ExchangeID, ErrOrderNotOpen)
		}
	}
	switch {
	case known.leg == "":
		return model.Order{}, false, fmt.Errorf("mt5: order %d is not a stop loss or a take profit: %w",
			leg.ExchangeID, ErrNotSupported)
	case known.canceled, known.generation < m.generation(known.ticket):
		return model.Order{}, false, fmt.Errorf("mt5: order %d is canceled: %w", leg.ExchangeID, ErrOrderNotOpen)
	}

	b := m.book
	b.mtx.RLock()
	pending, isPending := b.orders[known.ticket]
	var open []mt5Position
	for _, ticket := range known.protected() {
		if position, ok := b.positions[ticket]; ok {
			open = append(open, position)
		}
	}
	b.mtx.RUnlock()

	live := known.order
	mt5FillOrder(&live, leg)
	switch {
	case isPending:
		level := mt5Level(pending.SL, pending.TP, known.leg)
		mt5LegFrom(&live, known.leg, pending.Type, m.instrument(pending.Symbol).units(pending.VolumeInitial), level)
		return live, false, nil
	case len(open) > 0:
		var units float64
		for _, position := range open {
			units += m.instrument(position.Symbol).units(position.Volume)
		}
		mt5LegFrom(&live, known.leg, open[0].Type, units, mt5Protective(open, known.leg))
		return live, true, nil
	}
	return model.Order{}, false, fmt.Errorf("mt5: the position of order %d is closed: %w", leg.ExchangeID,
		ErrOrderNotOpen)
}

// ProtectionLimits returns the constraints of the levels of pair, see
// service.ProtectionLimiter: the tick of its symbol, and its stops and freeze
// levels in price, as read at the start.
func (m *MT5) ProtectionLimits(pair string) (service.ProtectionLimits, bool) {
	symbol, ok := m.instruments[pair]
	if !ok {
		return service.ProtectionLimits{}, false
	}
	return service.ProtectionLimits{Tick: symbol.tick(), StopsLevel: float64(symbol.StopsLevel) * symbol.Point,
		FreezeLevel: float64(symbol.FreezeLevel) * symbol.Point}, true
}

// PositionCosts returns the costs known of the position of the bot on pair,
// see service.PositionCostReader: its negative swap, and the commission and
// the fee of its entry deals twice (the exit taken as the same), in the
// account currency, turned into a distance of the price by the units held and
// the point value of the symbol. Known is false when an entry deal isn't in
// the history read yet (its commission unknown); the swap counts all the same.
// Nothing when the point value can't be read.
func (m *MT5) PositionCosts(pair string) service.PositionCosts {
	if m.dataOnly || m.book == nil {
		return service.PositionCosts{}
	}
	symbol, ok := m.instruments[pair]
	if !ok {
		return service.PositionCosts{}
	}
	held := m.botPositions(pair)
	if len(held) == 0 {
		return service.PositionCosts{}
	}
	known := true
	var units, money float64
	b := m.book
	b.mtx.RLock()
	for _, position := range held {
		units += symbol.units(position.Volume)
		if position.Swap < 0 {
			money -= position.Swap
		}
		entry, ok := b.entries[position.id()]
		if !ok || entry.provisional {
			known = false
			continue
		}
		if cost := -(entry.Commission + entry.Fee); cost > 0 {
			money += 2 * cost
		}
	}
	b.mtx.RUnlock()
	if units <= 0 {
		return service.PositionCosts{}
	}
	value, err := m.pointValue(m.ctx, symbol.Name)
	if err != nil {
		return service.PositionCosts{}
	}
	return service.PositionCosts{Price: money / (units * value), Known: known}
}

// SetStop puts a stop loss at stop on the positions of the bot held on side of
// pair, none with one, their take profit kept, see service.StopSetter: one
// TRADE_ACTION_SLTP each, the levels set before removed on a failure (see
// changeLevels). The protection moves to a new generation when it has legs
// already (eg: a take profit, or a stop loss removed by hand), so that the old
// ones end CANCELED, and the legs of both levels are returned anew, the take
// profit first, as CreateOrderOCO returns them.
func (m *MT5) SetStop(side model.SideType, pair string, size, stop float64) ([]model.Order, error) {
	if m.dataOnly {
		return nil, m.errDataOnly("SetStop")
	}
	fail := func(err error) ([]model.Order, error) {
		return nil, &OrderError{Err: err, Pair: pair, Quantity: size}
	}
	symbol, ok := m.instruments[pair]
	if !ok {
		return fail(fmt.Errorf("mt5: %s is not traded: %w", pair, ErrNotSupported))
	}
	stop = symbol.price(stop)

	unlock := m.lockPair(pair)
	defer unlock()
	held := m.botPositions(pair)
	if len(held) == 0 {
		return fail(fmt.Errorf("mt5: the bot holds no position of %s: %w", pair, ErrOrderNotOpen))
	}
	var units, profit float64
	for _, position := range held {
		switch {
		case mt5Side(position.Type) != side:
			return fail(fmt.Errorf("mt5: the bot holds %s on the %s side, not %s: %w", pair,
				mt5Side(position.Type), side, ErrNotSupported))
		case position.SL > 0:
			return fail(fmt.Errorf("mt5: the position %d of %s has a stop loss at %v already, move it with "+
				"UpdateProtection: %w", position.id(), pair, position.SL, ErrNotSupported))
		}
		if err := checkProtection(side, 0, stop, position.TP); err != nil {
			return fail(err)
		}
		units += symbol.units(position.Volume)
		if profit == 0 {
			profit = position.TP
		}
	}

	// a failure removes the stop losses set before: the positions stay as they were
	if err := m.changeLevels(m.ctx, held, func(position mt5Position) (float64, float64) {
		return stop, position.TP
	}); err != nil {
		return fail(err)
	}
	tickets := make([]int64, 0, len(held))
	for _, position := range held {
		tickets = append(tickets, position.id())
	}
	anchor := tickets[0]
	m.reprotect(anchor)
	if len(tickets) == 1 {
		tickets = nil
	}
	return m.protect(anchor, tickets, side, pair, units, stop, profit), nil
}
