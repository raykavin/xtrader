package entryfilter

import (
	"fmt"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/tools/log"
)

// The broker the filter hands the strategy: the one of the bot, the entries
// asked to the filter first (see filter.entry). It embeds the order controller
// of the bot, so every optional interface the strategies look for stays
// (service.ProtectionBroker, PositionLegs, PositionProtector, ProtectedCloser,
// PositionOpenings, PointValuer...), and overrides the calls that open a
// position only: CreateOrderMarket (an order on the other side of the position
// held, at most its size, is an exit and always passes; beyond it, a reversal,
// its entry asked, never its exit: refused, the exit alone is sent, as the
// order controller reduces a position only while the entries are blocked, and
// the refusal returned with it), CreateOrderMarketQuote,
// CreateOrderMarketProtected, CreateOrderLimit and CreateOrderStop (a limit or
// stop order on the other side of the position, at most its size, protects it
// and passes), CreateOrderLimitProtected and CreateOrderStopProtected. The
// position held is the one the exchange holds, read as the strategies read it
// to size their exits, and the one the order controller follows, whichever
// makes an order an exit (see holdings): the followed one may lag the
// exchange. The protections (CreateOrderOCO,
// UpdateProtection), the moves of a pending order (UpdateLimit) and the cancels
// always pass. A broker other than the order controller (eg: a paper wallet in
// the tests) is wrapped as a service.Broker, with service.ProtectionBroker when
// it is one; its other interfaces are hidden.

// quantityEpsilon is the relative tolerance of an exit sized as the position,
// as the order controller has it.
const quantityEpsilon = 1e-9

// gatedController is the order controller of the bot, its entries filtered.
type gatedController struct {
	*order.Controller
	f *filter
}

func (g gatedController) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	return g.f.market(g.Controller, side, pair, size)
}

func (g gatedController) CreateOrderMarketQuote(side model.SideType, pair string, quote float64) (model.Order,
	error) {
	return g.f.marketQuote(g.Controller, side, pair, quote)
}

func (g gatedController) CreateOrderLimit(side model.SideType, pair string, size, limit float64) (model.Order,
	error) {
	return g.f.limit(g.Controller, side, pair, size, limit)
}

func (g gatedController) CreateOrderStop(pair string, size, limit float64) (model.Order, error) {
	return g.f.stop(g.Controller, pair, size, limit)
}

func (g gatedController) CreateOrderMarketProtected(side model.SideType, pair string, size, stop,
	profit float64) ([]model.Order, error) {
	return g.f.protected(g.Controller, kindMarket, side, pair, size, 0, stop, profit)
}

func (g gatedController) CreateOrderLimitProtected(side model.SideType, pair string, size, limit, stop,
	profit float64) ([]model.Order, error) {
	return g.f.protected(g.Controller, kindLimit, side, pair, size, limit, stop, profit)
}

func (g gatedController) CreateOrderStopProtected(side model.SideType, pair string, size, level, stop,
	profit float64) ([]model.Order, error) {
	return g.f.protected(g.Controller, kindStop, side, pair, size, level, stop, profit)
}

// gatedBroker is a broker other than the order controller, its entries
// filtered.
type gatedBroker struct {
	service.Broker
	f *filter
}

func (g gatedBroker) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	return g.f.market(g.Broker, side, pair, size)
}

func (g gatedBroker) CreateOrderMarketQuote(side model.SideType, pair string, quote float64) (model.Order,
	error) {
	return g.f.marketQuote(g.Broker, side, pair, quote)
}

func (g gatedBroker) CreateOrderLimit(side model.SideType, pair string, size, limit float64) (model.Order,
	error) {
	return g.f.limit(g.Broker, side, pair, size, limit)
}

func (g gatedBroker) CreateOrderStop(pair string, size, limit float64) (model.Order, error) {
	return g.f.stop(g.Broker, pair, size, limit)
}

// gatedProtection is a broker other than the order controller placing the
// protected orders, its entries filtered.
type gatedProtection struct {
	gatedBroker
	protection service.ProtectionBroker
}

func (g gatedProtection) CreateOrderMarketProtected(side model.SideType, pair string, size, stop,
	profit float64) ([]model.Order, error) {
	return g.f.protected(g.protection, kindMarket, side, pair, size, 0, stop, profit)
}

func (g gatedProtection) CreateOrderLimitProtected(side model.SideType, pair string, size, limit, stop,
	profit float64) ([]model.Order, error) {
	return g.f.protected(g.protection, kindLimit, side, pair, size, limit, stop, profit)
}

func (g gatedProtection) CreateOrderStopProtected(side model.SideType, pair string, size, level, stop,
	profit float64) ([]model.Order, error) {
	return g.f.protected(g.protection, kindStop, side, pair, size, level, stop, profit)
}

func (g gatedProtection) UpdateProtection(leg model.Order, level float64) (model.Order, error) {
	return g.protection.UpdateProtection(leg, level)
}

func (g gatedProtection) UpdateLimit(o model.Order, limit float64) (model.Order, error) {
	return g.protection.UpdateLimit(o, limit)
}

// gate returns broker, the entries filtered, see gatedController.
func (f *filter) gate(broker service.Broker) service.Broker {
	switch b := broker.(type) {
	case *order.Controller:
		return gatedController{Controller: b, f: f}
	case service.ProtectionBroker:
		return gatedProtection{gatedBroker: gatedBroker{Broker: broker, f: f}, protection: b}
	}
	return gatedBroker{Broker: broker, f: f}
}

// position is a position held on a pair: its side and its size, 0 when flat.
type position struct {
	side     model.SideType
	quantity float64
}

// reduced returns how much of the position an order on side reduces: all of
// it on its other side, nothing on its side or flat.
func (p position) reduced(side model.SideType) float64 {
	if p.quantity > 0 && p.side != side {
		return p.quantity
	}
	return 0
}

// exitedBy reports whether an order of size on side only reduces the
// position: on its other side, of its size at most.
func (p position) exitedBy(side model.SideType, size float64) bool {
	quantity := p.reduced(side)
	return quantity > 0 && size <= quantity*(1+quantityEpsilon)
}

func (p position) String() string {
	if p.quantity <= 0 {
		return "flat"
	}
	return fmt.Sprintf("%s %v", p.side, p.quantity)
}

// holdings are the two views of the position the bot holds on a pair: the one
// the exchange holds, read with Position (the one the strategies size their
// exits from, and the order controller its reduces and the protections of a
// position), and, with the order controller, the one it follows, rebuilt from
// the fills it stored. They part when a fill is dropped, the stored orders
// wiped, or the reconcile at a start leaves them apart: the followed one then
// missing or smaller. An order is judged against both, so that an exit is
// never taken for an entry whichever of them lags: an order on the other side
// of either, of its size at most, is an exit (see exits); beyond the size the
// exchange holds, a reversal, only the part beyond it is asked (see
// exitPart). read is false, err why, when the exchange can't be read: the
// followed one judges alone then.
type holdings struct {
	exchange, followed position
	read               bool
	err                error
}

// holdingsOf returns the holdings of pair at broker, see holdings: the
// followed one flat for a broker other than the order controller.
func holdingsOf(broker service.Broker, pair string) holdings {
	var h holdings
	switch asset, _, err := broker.Position(pair); {
	case err != nil:
		h.err = err
	case asset > 0:
		h.read, h.exchange = true, position{side: model.SideTypeBuy, quantity: asset}
	case asset < 0:
		h.read, h.exchange = true, position{side: model.SideTypeSell, quantity: -asset}
	default:
		h.read = true
	}
	if ctl, ok := broker.(*order.Controller); ok {
		if followed, ok := ctl.HeldPosition(pair); ok {
			h.followed = position{side: followed.Side, quantity: followed.Quantity}
		}
	}
	return h
}

// exits reports whether an order of size on side only reduces the position:
// on the other side of the one the exchange holds, or of the one the order
// controller follows, of its size at most.
func (h holdings) exits(side model.SideType, size float64) bool {
	return h.exchange.exitedBy(side, size) || h.followed.exitedBy(side, size)
}

// exitPart returns the part of an order on side that closes the position, the
// rest of it an entry: the size the exchange holds on its other side, the
// size the strategies size their exits from (the followed one when the
// exchange can't be read), 0 for none.
func (h holdings) exitPart(side model.SideType) float64 {
	if !h.read {
		return h.followed.reduced(side)
	}
	return h.exchange.reduced(side)
}

// holds reports whether the bot holds a position of pair on side, as the
// exchange or the order controller has it.
func (h holdings) holds(side model.SideType) bool {
	return h.exchange.quantity > 0 && h.exchange.side == side || h.followed.quantity > 0 && h.followed.side == side
}

// tellExit tells an order of what on side, of size, taken for an exit when the
// two views of the position disagree on it (see holdings), or when the
// exchange couldn't be read: an exit the filter let through on the word of
// one of them only, never an entry refused.
func tellExit(pair, what string, side model.SideType, size float64, h holdings) {
	switch {
	case !h.read:
		log.Warnf("[FILTER] %s: a %s %s of %v let through as an exit of the position the order controller "+
			"follows (%s), the exchange not read: %v", pair, what, side, size, h.followed, h.err)
	case h.exchange.exitedBy(side, size) != h.followed.exitedBy(side, size):
		log.Warnf("[FILTER] %s: a %s %s of %v let through as an exit: the exchange holds %s, the order controller "+
			"follows %s", pair, what, side, size, h.exchange, h.followed)
	}
}

// market sends a market order of size on side of pair to broker: an exit at
// once (see holdings.exits); else an entry, or a reversal (the part closing
// the position the exchange holds and an entry beyond it), its entry asked
// first, the exit part never asked. A reversal whose entry is refused sends
// its exit part alone, as the order controller reduces a position only while
// the entries are blocked, and returns the order of that exit WITH the
// refusal (order.ErrEntriesBlocked wrapped): done in part only, so that the
// strategy reads the position again rather than take the reversal for done,
// eg: cancel the stop loss and the take profit of what is still held.
func (f *filter) market(broker service.Broker, side model.SideType, pair string, size float64) (model.Order,
	error) {
	h := holdingsOf(broker, pair)
	if h.exits(side, size) {
		tellExit(pair, "market order", side, size, h)
		return broker.CreateOrderMarket(side, pair, size)
	}
	exit := h.exitPart(side)
	var placed model.Order
	refused, err := f.entry(pair, side, kindMarket, 0, 0, 0, func() error {
		var err error
		placed, err = broker.CreateOrderMarket(side, pair, size)
		return err
	})
	if !refused || exit <= 0 {
		return placed, err
	}
	log.Warnf("[FILTER] %s: a reversal %s of %v cut to its exit, the %v held closed alone, its entry refused (%v)",
		pair, side, size, exit, err)
	closed, exitErr := broker.CreateOrderMarket(side, pair, exit)
	if exitErr != nil {
		log.Errorf("[FILTER] %s: the exit of %v of the reversal failed: %v", pair, exit, exitErr)
		return closed, exitErr
	}
	return closed, fmt.Errorf("%w; the reversal cut to its exit, %v of %v sent: read the position again", err, exit,
		size)
}

// marketQuote sends a market order of an amount of the quote: an entry, as the
// order controller has it, never compared with the position.
func (f *filter) marketQuote(broker service.Broker, side model.SideType, pair string, quote float64) (model.Order,
	error) {
	var placed model.Order
	_, err := f.entry(pair, side, kindMarket, 0, 0, 0, func() error {
		var err error
		placed, err = broker.CreateOrderMarketQuote(side, pair, quote)
		return err
	})
	return placed, err
}

// limit places a limit order: protecting the position at once (on the other
// side of the one the exchange holds or the order controller follows, of its
// size at most, see holdings), else an entry asked first.
func (f *filter) limit(broker service.Broker, side model.SideType, pair string, size, limit float64) (model.Order,
	error) {
	if h := holdingsOf(broker, pair); h.exits(side, size) {
		tellExit(pair, "limit order", side, size, h)
		return broker.CreateOrderLimit(side, pair, size, limit)
	}
	var placed model.Order
	_, err := f.entry(pair, side, kindLimit, limit, 0, 0, func() error {
		var err error
		placed, err = broker.CreateOrderLimit(side, pair, size, limit)
		return err
	})
	return placed, err
}

// stop places a sell stop order: protecting a long position at once (held at
// the exchange or followed by the order controller, of its size at most, see
// holdings), else an entry asked first.
func (f *filter) stop(broker service.Broker, pair string, size, limit float64) (model.Order, error) {
	if h := holdingsOf(broker, pair); h.exits(model.SideTypeSell, size) {
		tellExit(pair, "stop order", model.SideTypeSell, size, h)
		return broker.CreateOrderStop(pair, size, limit)
	}
	var placed model.Order
	_, err := f.entry(pair, model.SideTypeSell, kindStop, limit, 0, 0, func() error {
		var err error
		placed, err = broker.CreateOrderStop(pair, size, limit)
		return err
	})
	return placed, err
}

// protected places a protected entry of kind, its level for a limit or a stop
// one: an entry asked first, always.
func (f *filter) protected(broker service.ProtectionBroker, kind entryKind, side model.SideType, pair string,
	size, level, stop, profit float64) ([]model.Order, error) {
	var placed []model.Order
	_, err := f.entry(pair, side, kind, level, stop, profit, func() error {
		var err error
		switch kind {
		case kindLimit:
			placed, err = broker.CreateOrderLimitProtected(side, pair, size, level, stop, profit)
		case kindStop:
			placed, err = broker.CreateOrderStopProtected(side, pair, size, level, stop, profit)
		default:
			placed, err = broker.CreateOrderMarketProtected(side, pair, size, stop, profit)
		}
		return err
	})
	return placed, err
}
