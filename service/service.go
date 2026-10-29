//go:generate go run github.com/vektra/mockery/v2 --all --with-expecter --output=../testdata/mocks

package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/raykavin/xtrader/model"
)

type Exchange interface {
	Broker
	Feeder
}

type Feeder interface {
	AssetsInfo(pair string) model.AssetInfo
	LastQuote(ctx context.Context, pair string) (float64, error)
	CandlesByPeriod(ctx context.Context, pair, period string, start, end time.Time) ([]model.Candle, error)
	CandlesByLimit(ctx context.Context, pair, period string, limit int) ([]model.Candle, error)
	CandlesSubscription(ctx context.Context, pair, timeframe string) (chan model.Candle, chan error)
}

type Broker interface {
	Account() (model.Account, error)
	Position(pair string) (asset, quote float64, err error)
	Order(pair string, id int64) (model.Order, error)
	CreateOrderOCO(side model.SideType, pair string, size, price, stop, stopLimit float64) ([]model.Order, error)
	CreateOrderLimit(side model.SideType, pair string, size float64, limit float64) (model.Order, error)
	CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error)
	CreateOrderMarketQuote(side model.SideType, pair string, quote float64) (model.Order, error)
	CreateOrderStop(pair string, quantity float64, limit float64) (model.Order, error)
	Cancel(model.Order) error
}

// OrderHistory is implemented by the brokers that look the orders out of the
// book up in a history read by periods, eg: MT5, from the deals of a period. The
// order controller passes since, the creation of the order, so the search
// covers its whole life, eg: an order filled while the bot was off for days.
type OrderHistory interface {
	OrderSince(pair string, id int64, since time.Time) (model.Order, error)
}

// ProtectionBroker is implemented by the brokers that open a position with its
// stop loss and take profit in the same request, protecting it from its
// opening, eg: MT5. It is also implemented by the paper wallet, which
// simulates it, and by the order controller, which stores the orders. stop or
// profit 0 leaves that level out, and the market entry, the limit entry at limit
// and the stop entry triggered at level may be on either side.
//
// The first order returned is the entry, followed by the legs of the levels,
// sized for the position: they are canceled with the entry, and once the
// position is open, when one of them closes it the other is canceled, as the
// legs of an OCO order. Canceling a leg removes the protection, both legs.
//
// UpdateProtection moves a leg to level, eg: a stop loss trailing the price,
// and returns it moved. The other leg stays where it is.
//
// UpdateLimit moves a pending limit order, protected or not, to limit and
// returns it moved. The levels of its legs stay where they are.
// AccountSummary is the account as the broker reports it, in its currency:
// its balance, its equity, the profit of its open positions, the margin they
// hold and the margin free.
type AccountSummary struct {
	Currency   string
	Balance    float64
	Equity     float64
	Profit     float64
	Margin     float64
	FreeMargin float64
}

// AccountReporter is an exchange reporting its account and the profits of its
// trades in the currency of the account, as the broker books them, eg: MT5 on
// a BRL account trading BTCUSDm. For the display only: the core computes in
// the quote of the pairs.
type AccountReporter interface {
	// AccountSummary returns the account, false when it can't be read.
	AccountSummary() (AccountSummary, bool)
	// ClosedProfit returns the profit the account booked for close, a fill
	// closing (a part of) the position that entry opened: the profit, the swap,
	// the commission and the fee of its exits, in the account currency; false
	// when unknown, eg: not in the history the broker keeps (yet). It doesn't
	// block.
	ClosedProfit(close, entry model.Order) (float64, bool)
}

type ProtectionBroker interface {
	CreateOrderMarketProtected(side model.SideType, pair string, size, stop, profit float64) ([]model.Order, error)
	CreateOrderLimitProtected(side model.SideType, pair string, size, limit, stop,
		profit float64) ([]model.Order, error)
	CreateOrderStopProtected(side model.SideType, pair string, size, level, stop,
		profit float64) ([]model.Order, error)
	UpdateProtection(leg model.Order, level float64) (model.Order, error)
	UpdateLimit(order model.Order, limit float64) (model.Order, error)
}

// ProtectedCloser is implemented by the brokers whose market order closes a
// protected position with its legs in place, its stop loss and take profit
// going with it, eg: MT5, which closes the positions held by their tickets.
// Elsewhere, eg: the paper wallet, the legs reserve the position: they are
// canceled before it is closed, or the order would open the other side.
// ClosesProtected reports which: the order controller implements it for its
// exchange.
type ProtectedCloser interface {
	ClosesProtected() bool
}

// ReduceOnlyBroker is implemented by the brokers that close a position at the
// market without ever opening the other side, eg: MT5 and the paper wallet:
// CreateOrderMarketReduce closes up to size units of the positions held
// against side on pair, the size cut to what they hold as the order executes,
// a stop loss filled meanwhile included, and their stop loss and take profit
// go with them. It fails with exchange.ErrNothingToReduce when nothing is held
// against side. The order controller sends its market orders through it while
// the entries of the bot are blocked, see order.Controller.BlockEntries.
type ReduceOnlyBroker interface {
	CreateOrderMarketReduce(side model.SideType, pair string, size float64) (model.Order, error)
}

// PositionProtector is implemented by the brokers that protect a position
// already held, eg: placing again the legs canceled for a close that failed.
// Protect places a stop loss at stop and a take profit at profit, 0 for none,
// on the size units held on side of pair, and returns the legs, the take
// profit first, as ProtectionBroker does.
type PositionProtector interface {
	Protect(side model.SideType, pair string, size, stop, profit float64) ([]model.Order, error)
}

// PositionLegs is implemented by the brokers that tell how a position held is
// protected, eg: the order controller, from the orders it stores and follows,
// a restart included: a strategy that finds a position it doesn't follow, eg:
// opened before a restart, follows it with its legs, which it moves or
// cancels, rather than without them. HeldLegs returns the open legs (the stop
// loss and the take profit) of the position held on side of pair, and when it
// opened, zero when unknown.
type PositionLegs interface {
	HeldLegs(pair string, side model.SideType) (legs []model.Order, opened time.Time, err error)
}

// PositionOpenings is implemented by the brokers that tell how many positions
// the bot opened on a pair since a time before a restart, eg: the order
// controller, from the fills of the storage it replays at the start: a
// strategy limiting its entries per session, eg: ORB, counts its own entries
// of the run and, after a restart, the ones of the session opened before it.
// PositionsReplayed returns the fills of pair of the previous runs that opened
// a position, or reversed one, at or after since; the storage doesn't tell
// what placed them (a strategy, the trade panel, Telegram).
type PositionOpenings interface {
	PositionsReplayed(pair string, since time.Time) int
}

// PositionReporter is implemented by the exchanges that report the position the
// bot holds on a pair apart from the rest of the account, eg: MT5, from the
// positions of its magic number: the order controller checks the positions it
// replays from the storage after a restart against it, and a position whose
// orders on the other side, eg: its legs, end without a fill, as a position
// closed outside the bot, eg: by hand in the terminal or by a stop out, leaves no
// fill in the storage. HeldPosition returns the side and the size of the net
// position held on pair, 0 when flat; false when unknown, eg: a pair not
// traded. It must not block: a state polled already, not a request.
type PositionReporter interface {
	HeldPosition(pair string) (side model.SideType, quantity float64, ok bool)
}

// ProtectionReader is implemented by the brokers that tell the level a leg
// is at now and whether it protects an open position, eg: MT5, from its book,
// and the paper wallet. LiveProtection returns leg as it is now: its level,
// for a stop loss over several positions the most protective one; and true
// when it protects a position held, false for a leg of a pending entry, which
// moves with its entry. It must not block: a state polled already.
type ProtectionReader interface {
	LiveProtection(leg model.Order) (live model.Order, protectsPosition bool, err error)
}

// StopSetter is implemented by the brokers that put a stop loss on a position
// held without one, eg: MT5, on the positions of its magic number, their take
// profit kept, and the paper wallet. SetStop places it at stop on the size
// units held on side of pair, and returns the legs it placed, the stop loss
// among them (MT5 places the take profit anew too, its old leg ending
// CANCELED).
type StopSetter interface {
	SetStop(side model.SideType, pair string, size, stop float64) ([]model.Order, error)
}

// ProtectionLimits are the constraints of the protective levels of a pair at
// the broker: Tick the step of a price, StopsLevel the least distance of a
// stop loss or a take profit from the market and FreezeLevel the distance
// within which a level can't change, both in price, 0 for none.
type ProtectionLimits struct {
	Tick        float64
	StopsLevel  float64
	FreezeLevel float64
}

// ProtectionLimiter is implemented by the brokers that tell the constraints of
// the protective levels of a pair, eg: MT5, from its symbol.
type ProtectionLimiter interface {
	ProtectionLimits(pair string) (ProtectionLimits, bool)
}

// PositionCosts are the costs known of a position held, as a distance of its
// price in the quote currency: what moving its exit by that much makes up,
// eg: the commission of its entry, the one of its exit taken as the same, and
// its swap when negative. Known is false when the broker can't tell them.
type PositionCosts struct {
	Price float64
	Known bool
}

// PositionCostReader is implemented by the brokers that tell the costs of a
// position held, eg: MT5, from its deals and its swap.
type PositionCostReader interface {
	PositionCosts(pair string) PositionCosts
}

// PointValuer is implemented by the brokers whose cash is in the currency of
// the account rather than in the quote of the pairs, eg: MT5, a BRL account
// trading XAUUSDm. PointValue returns the value in the account currency of a
// move of 1 of the price of pair for 1 unit of its asset, as the broker tells
// it (its tick value): no rate of exchange is read by the bot. The cash of
// Position, the amount of CreateOrderMarketQuote and the money of the
// strategies (their stops in money, their minimum position, their risk) are in
// that currency. The order controller implements it for its exchange.
type PointValuer interface {
	PointValue(pair string) (float64, error)
}

// PointValue returns the point value of pair at broker, see PointValuer: 1
// when the broker values the pairs in their quote, eg: the paper wallet.
func PointValue(broker any, pair string) (float64, error) {
	if valuer, ok := broker.(PointValuer); ok {
		return valuer.PointValue(pair)
	}
	return 1, nil
}

// ErrStopNotTighter is the error of a stop loss of an open position moved to
// a level that protects it less than the one it is at, or as much, refused
// while the stop guard of its pair is on, see StopNotTighterError.
var ErrStopNotTighter = errors.New("the stop loss is at a more protective level already")

// StopNotTighterError is ErrStopNotTighter with the leg as it is now, Current
// its level, and the level Requested: the mover adopts Leg.
type StopNotTighterError struct {
	Leg       model.Order
	Current   float64
	Requested float64
}

func (e *StopNotTighterError) Error() string {
	return fmt.Sprintf("%v: %s at %g, %g asked", ErrStopNotTighter, e.Leg.Pair, e.Current, e.Requested)
}

func (e *StopNotTighterError) Unwrap() error { return ErrStopNotTighter }

type Notifier interface {
	Notify(string)
	OnOrder(order model.Order)
	OnError(err error)
}

type Telegram interface {
	Notifier
	Start()
}
