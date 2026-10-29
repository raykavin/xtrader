package backtest

import (
	"fmt"
	"math"
	"strconv"
	"sync"

	"github.com/shopspring/decimal"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
)

// wallet is the paper wallet of a backtest as the strategy trades it:
//   - the volumes of its orders floored to the volume step of their symbol,
//     as MT5 does, an order under the least volume refused (and counted);
//   - with a leverage L above 1, the margin of a position is its notional
//     over L: the paper wallet holds the balance times L, a position taking
//     its whole notional out of it (the buying power of the margin), and the
//     cash the strategy sees (Position) is the free margin of an account
//     holding the balance: the wallet's less the credit of the leverage
//     (balance x (L - 1)), plus what the positions don't commit (their
//     notional at the last close x (1 - 1/L)). The equity reported is the
//     wallet's less that credit.
type wallet struct {
	*exchange.PaperWallet
	balance, leverage float64
	pairs             []string
	symbols           map[string]Symbol

	mu      sync.Mutex
	refused []string // why the orders were refused, see refuse
}

// credit returns the credit of the leverage held by the paper wallet.
func (w *wallet) credit() float64 { return w.balance * (w.leverage - 1) }

// Position returns the units held on pair and the free margin, see wallet.
func (w *wallet) Position(pair string) (asset, quote float64, err error) {
	asset, quote, err = w.PaperWallet.Position(pair)
	if err != nil || w.leverage <= 1 {
		return asset, quote, err
	}
	committed := 0.0
	account, err := w.PaperWallet.Account()
	if err != nil {
		return 0, 0, err
	}
	for _, b := range account.Balances {
		if b.Free == 0 && b.Lock == 0 {
			continue
		}
		for _, p := range w.pairs {
			if base, _ := exchange.SplitAssetQuote(p); base == b.Asset {
				if price, ok := w.PaperWallet.LastClose(p); ok {
					committed += math.Abs(b.Free+b.Lock) * price
				}
			}
		}
	}
	return asset, quote - w.credit() + committed*(1-1/w.leverage), nil
}

// size returns the volume of an order of size units on pair floored to the
// volume step of its symbol, an error under its least volume.
func (w *wallet) size(pair string, size float64) (float64, error) {
	s, ok := w.symbols[pair]
	if !ok || (s.VolumeStep <= 0 && s.VolumeMin <= 0) {
		return size, nil
	}
	floored := size
	if s.VolumeStep > 0 {
		steps := decimal.NewFromFloat(size).Div(decimal.NewFromFloat(s.VolumeStep)).Add(decimal.NewFromFloat(1e-9)).
			Floor()
		floored, _ = strconv.ParseFloat(steps.Mul(decimal.NewFromFloat(s.VolumeStep)).String(), 64)
	}
	if floored <= 0 || (s.VolumeMin > 0 && floored < s.VolumeMin*(1-1e-9)) {
		err := fmt.Errorf("%w: %s, a volume of %g units under the least of the symbol, %g", exchange.ErrInvalidQuantity,
			pair, size, s.VolumeMin)
		w.refuse(err)
		return 0, err
	}
	return floored, nil
}

// refuse records why an order was refused.
func (w *wallet) refuse(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refused = append(w.refused, err.Error())
}

func (w *wallet) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	size, err := w.size(pair, size)
	if err != nil {
		return model.Order{}, err
	}
	return w.PaperWallet.CreateOrderMarket(side, pair, size)
}

func (w *wallet) CreateOrderMarketQuote(side model.SideType, pair string, quote float64) (model.Order, error) {
	price, ok := w.PaperWallet.LastClose(pair)
	if !ok {
		return w.PaperWallet.CreateOrderMarketQuote(side, pair, quote)
	}
	return w.CreateOrderMarket(side, pair, quote/price)
}

func (w *wallet) CreateOrderLimit(side model.SideType, pair string, size, limit float64) (model.Order, error) {
	size, err := w.size(pair, size)
	if err != nil {
		return model.Order{}, err
	}
	return w.PaperWallet.CreateOrderLimit(side, pair, size, limit)
}

func (w *wallet) CreateOrderStop(pair string, size, limit float64) (model.Order, error) {
	size, err := w.size(pair, size)
	if err != nil {
		return model.Order{}, err
	}
	return w.PaperWallet.CreateOrderStop(pair, size, limit)
}

func (w *wallet) CreateOrderOCO(side model.SideType, pair string, size, price, stop,
	stopLimit float64) ([]model.Order, error) {
	size, err := w.size(pair, size)
	if err != nil {
		return nil, err
	}
	return w.PaperWallet.CreateOrderOCO(side, pair, size, price, stop, stopLimit)
}

func (w *wallet) CreateOrderMarketProtected(side model.SideType, pair string, size, stop,
	profit float64) ([]model.Order, error) {
	size, err := w.size(pair, size)
	if err != nil {
		return nil, err
	}
	return w.PaperWallet.CreateOrderMarketProtected(side, pair, size, stop, profit)
}

func (w *wallet) CreateOrderLimitProtected(side model.SideType, pair string, size, limit, stop,
	profit float64) ([]model.Order, error) {
	size, err := w.size(pair, size)
	if err != nil {
		return nil, err
	}
	return w.PaperWallet.CreateOrderLimitProtected(side, pair, size, limit, stop, profit)
}

func (w *wallet) CreateOrderStopProtected(side model.SideType, pair string, size, level, stop,
	profit float64) ([]model.Order, error) {
	size, err := w.size(pair, size)
	if err != nil {
		return nil, err
	}
	return w.PaperWallet.CreateOrderStopProtected(side, pair, size, level, stop, profit)
}
