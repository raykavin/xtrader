//go:generate go run github.com/vektra/mockery/v2 --all --with-expecter --output=../testdata/mocks

package service

import (
	"context"
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

type Notifier interface {
	Notify(string)
	OnOrder(order model.Order)
	OnError(err error)
}

type Telegram interface {
	Notifier
	Start()
}
