package order

import (
	"errors"
	"fmt"
	"math"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/tools/log"
)

// This file guards the stop losses of the open positions: while the stop guard
// of a pair is on (see SetStopGuard), the strategies, the language model and
// the protection of the positions (breakeven, trailing stop) only tighten
// them, whoever moves first, see TightenProtection.

// SetStopGuard sets the stop guard: guarded reports whether the stop losses of
// the open positions of a pair only tighten (see UpdateProtection), nil for
// none. It is called under the lock of the pair, so it must not wait: an
// atomic flag, not a lock.
func (c *Controller) SetStopGuard(guarded func(pair string) bool) {
	if guarded == nil {
		c.stopGuard.Store(nil)
		return
	}
	c.stopGuard.Store(&guarded)
}

// StopGuarded reports whether the stop guard of pair is on, see SetStopGuard.
func (c *Controller) StopGuarded(pair string) bool {
	guarded := c.stopGuard.Load()
	return guarded != nil && (*guarded)(pair)
}

// isStopLeg reports whether order is a stop loss leg.
func isStopLeg(order model.Order) bool {
	return order.GroupID != nil && (order.Type == model.OrderTypeStopLoss ||
		order.Type == model.OrderTypeStopLossLimit)
}

// StopLevel returns the level of a stop loss leg: its stop, else its price.
func StopLevel(leg model.Order) float64 {
	if leg.Stop != nil && *leg.Stop > 0 {
		return *leg.Stop
	}
	return leg.Price
}

// levelMoved reports whether the open leg was moved at the exchange to the
// level of current, see updateOrders.
func levelMoved(followed, current model.Order) bool {
	if followed.GroupID == nil || !isOpen(current.Status) {
		return false
	}
	return current.Price > 0 && math.Abs(current.Price-followed.Price) > 1e-12 ||
		isStopLeg(current) && current.Stop != nil && math.Abs(StopLevel(current)-StopLevel(followed)) > 1e-12
}

// Tick returns the step of the prices of pair at the exchange, 0 when unknown.
func (c *Controller) Tick(pair string) float64 {
	if limiter, ok := c.exchange.(service.ProtectionLimiter); ok {
		if limits, ok := limiter.ProtectionLimits(pair); ok && limits.Tick > 0 {
			return limits.Tick
		}
	}
	return c.exchange.AssetsInfo(pair).TickSize
}

// tightens reports whether moving the stop loss leg to level protects its
// position more than it does now, by more than half a tick, see
// TightenProtection: the leg as it is now (the live level of the exchange,
// see service.ProtectionReader) and nil when it is as tight; a
// *service.StopNotTighterError when it is tighter. A leg of a pending entry,
// one the exchange doesn't tell, or one that ended, moves as asked: the
// exchange refuses what it must.
func (c *Controller) tightens(leg model.Order, level float64) (model.Order, bool, error) {
	reader, ok := c.exchange.(service.ProtectionReader)
	if !ok {
		return leg, true, nil
	}
	live, protects, err := reader.LiveProtection(leg)
	if err != nil || !protects {
		return leg, true, nil
	}
	keepStored(&live, leg)
	current := StopLevel(live)
	if current <= 0 {
		return live, true, nil // without a stop loss now, any protects more
	}
	gain := level - current // how much tighter, for a long: its stop loss below the price
	if live.Side == model.SideTypeBuy {
		gain = current - level // a short, its stop loss above the price
	}
	half := c.Tick(leg.Pair) / 2
	if half <= 0 {
		half = math.Abs(current) * 1e-9
	}
	switch {
	case gain > half:
		return live, true, nil
	case gain >= -half:
		return live, false, nil
	}
	return live, false, &service.StopNotTighterError{Leg: live, Current: current, Requested: level}
}

// HeldPosition returns the position held on pair as the controller follows it,
// false when flat.
func (c *Controller) HeldPosition(pair string) (Position, bool) {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	position, ok := c.position[pair]
	if !ok || position == nil || position.Quantity <= 0 {
		return Position{}, false
	}
	return *position, true
}

// PositionStop returns the position held on pair and its stop loss leg as it
// is now (see service.ProtectionReader), from the legs the controller follows,
// false for the leg when it has none, eg: removed by hand, or never placed.
// It doesn't read the storage: it may be called on every price.
func (c *Controller) PositionStop(pair string) (Position, model.Order, bool) {
	c.mtx.RLock()
	var held Position
	if position, ok := c.position[pair]; ok && position != nil && position.Quantity > 0 {
		held = *position
	}
	var candidates []model.Order
	if held.Quantity > 0 {
		for _, order := range c.pending {
			if order.Pair == pair && isStopLeg(order) && order.Side != held.Side && isOpen(order.Status) {
				candidates = append(candidates, order)
			}
		}
	}
	c.mtx.RUnlock()
	if held.Quantity <= 0 {
		return Position{}, model.Order{}, false
	}
	reader, readable := c.exchange.(service.ProtectionReader)
	for _, leg := range candidates {
		if !c.legOf(leg, held.Entry) {
			continue
		}
		if !readable {
			return held, leg, true
		}
		live, protects, err := reader.LiveProtection(leg)
		if err != nil || !protects {
			continue
		}
		keepStored(&live, leg)
		return held, live, true
	}
	return held, model.Order{}, false
}

// ErrNoPosition is the error of a stop loss set on a position not held.
var ErrNoPosition = errors.New("no position held")

// SetPositionStop puts a stop loss at level on the position held on side of
// pair, which has none, see service.StopSetter, and stores the legs placed,
// followed as any other order. Like UpdateProtection, it works on a stopped
// bot too; not once the controller is closed.
func (c *Controller) SetPositionStop(pair string, side model.SideType, level float64) ([]model.Order, error) {
	end, err := c.beginProtection(pair)
	if err != nil {
		return nil, err
	}
	defer end()

	setter, ok := c.exchange.(service.StopSetter)
	if !ok {
		return nil, fmt.Errorf("setting a stop loss on a position held: %w", exchange.ErrNotSupported)
	}
	position, held := c.HeldPosition(pair)
	if !held || position.Side != side {
		return nil, fmt.Errorf("%w: %s %s", ErrNoPosition, side, pair)
	}
	log.Infof("[ORDER] Setting a stop loss at %f on the %s position of %s", level, side, pair)
	legs, err := setter.SetStop(side, pair, position.Quantity, level)
	c.keepCreated(legs)
	if err != nil {
		c.notifyError(err)
		return legs, err
	}
	return legs, nil
}
