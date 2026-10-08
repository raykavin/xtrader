package exchange

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/tools/log"
)

type assetInfo struct {
	Free float64
	Lock float64
}

type AssetValue struct {
	Time  time.Time
	Value float64
}

// orderLock records the balances a pending order moved from Free to Lock, so
// that a fill or a cancel releases exactly what was reserved. OCO legs share
// one lock, keyed by their group id.
type orderLock struct {
	asset float64 // base asset backing a sell that closes a long position
	quote float64 // quote paying a buy, or the collateral of a short entry
}

// PaperWallet simulates the wallet of an exchange. Its methods are safe for
// concurrent use: the bot feeds it candles and orders while the dashboard and
// the Telegram bot read the account and the equity.
type PaperWallet struct {
	sync.RWMutex
	ctx           context.Context
	baseCoin      string
	counter       int64
	takerFee      float64
	makerFee      float64
	feesPaid      float64
	initialValue  float64
	feeder        service.Feeder
	orders        []model.Order
	ordersByID    map[int64]int        // exchange id -> index in orders
	pendingOrders map[string][]int     // pair -> order indices for fast lookup
	locks         map[int64]*orderLock // order (or OCO group) id -> reserved funds
	protections   map[int64][]int      // entry id -> indices of the legs waiting for its fill
	assets        map[string]*assetInfo
	avgShortPrice map[string]float64
	avgLongPrice  map[string]float64
	volume        map[string]float64
	lastCandle    map[string]model.Candle
	fistCandle    map[string]model.Candle
	assetValues   map[string][]AssetValue
	equityValues  []AssetValue
}

func (p *PaperWallet) AssetsInfo(pair string) model.AssetInfo {
	asset, quote := SplitAssetQuote(pair)
	return model.AssetInfo{
		BaseAsset:          asset,
		QuoteAsset:         quote,
		MaxPrice:           math.MaxFloat64,
		MaxQuantity:        math.MaxFloat64,
		StepSize:           0.00000001,
		TickSize:           0.00000001,
		QuotePrecision:     8,
		BaseAssetPrecision: 8,
	}
}

type PaperWalletOption func(*PaperWallet)

func WithPaperAsset(pair string, amount float64) PaperWalletOption {
	return func(wallet *PaperWallet) {
		wallet.assets[pair] = &assetInfo{
			Free: amount,
			Lock: 0,
		}
	}
}

func WithPaperFee(maker, taker float64) PaperWalletOption {
	return func(wallet *PaperWallet) {
		wallet.makerFee = maker
		wallet.takerFee = taker
	}
}

func WithDataFeed(feeder service.Feeder) PaperWalletOption {
	return func(wallet *PaperWallet) {
		wallet.feeder = feeder
	}
}

func NewPaperWallet(ctx context.Context, baseCoin string, options ...PaperWalletOption) *PaperWallet {
	wallet := PaperWallet{
		ctx:           ctx,
		baseCoin:      baseCoin,
		orders:        make([]model.Order, 0, 100), // Pre-allocate with capacity
		ordersByID:    make(map[int64]int),
		pendingOrders: make(map[string][]int),
		locks:         make(map[int64]*orderLock),
		protections:   make(map[int64][]int),
		assets:        make(map[string]*assetInfo),
		fistCandle:    make(map[string]model.Candle),
		lastCandle:    make(map[string]model.Candle),
		avgShortPrice: make(map[string]float64),
		avgLongPrice:  make(map[string]float64),
		volume:        make(map[string]float64),
		assetValues:   make(map[string][]AssetValue),
		equityValues:  make([]AssetValue, 0, 1000), // Pre-allocate for equity tracking
	}

	for _, option := range options {
		option(&wallet)
	}

	wallet.initialValue = wallet.assets[wallet.baseCoin].Free
	log.Info("[SETUP] Using paper wallet")
	log.Infof("[SETUP] Initial Portfolio = %f %s", wallet.initialValue, wallet.baseCoin)

	return &wallet
}

// ID returns a new order id.
func (p *PaperWallet) ID() int64 {
	p.Lock()
	defer p.Unlock()
	return p.nextID()
}

// nextID is ID for the callers holding the lock.
func (p *PaperWallet) nextID() int64 {
	p.counter++
	return p.counter
}

func (p *PaperWallet) Pairs() []string {
	p.RLock()
	defer p.RUnlock()
	pairs := make([]string, 0, len(p.assets))
	for pair := range p.assets {
		pairs = append(pairs, pair)
	}
	return pairs
}

func (p *PaperWallet) LastQuote(ctx context.Context, pair string) (float64, error) {
	return p.feeder.LastQuote(ctx, pair)
}

// AssetValues returns a copy of the values of asset, one per complete candle:
// OnCandle keeps appending to them.
func (p *PaperWallet) AssetValues(asset string) []AssetValue {
	p.RLock()
	defer p.RUnlock()
	return slices.Clone(p.assetValues[asset])
}

// EquityValues returns a copy of the equity, one per complete candle.
func (p *PaperWallet) EquityValues() []AssetValue {
	p.RLock()
	defer p.RUnlock()
	return slices.Clone(p.equityValues)
}

func (p *PaperWallet) MaxDrawdown() (float64, time.Time, time.Time) {
	p.RLock()
	defer p.RUnlock()
	return p.maxDrawdown()
}

// maxDrawdown is MaxDrawdown for the callers holding the lock.
func (p *PaperWallet) maxDrawdown() (float64, time.Time, time.Time) {
	if len(p.equityValues) < 1 {
		return 0, time.Time{}, time.Time{}
	}

	localMin := math.MaxFloat64
	localMinBase := p.equityValues[0].Value
	localMinStart := p.equityValues[0].Time
	localMinEnd := p.equityValues[0].Time

	globalMin := localMin
	globalMinBase := localMinBase
	globalMinStart := localMinStart
	globalMinEnd := localMinEnd

	for i := 1; i < len(p.equityValues); i++ {
		diff := p.equityValues[i].Value - p.equityValues[i-1].Value

		if localMin > 0 {
			localMin = diff
			localMinBase = p.equityValues[i-1].Value
			localMinStart = p.equityValues[i-1].Time
			localMinEnd = p.equityValues[i].Time
		} else {
			localMin += diff
			localMinEnd = p.equityValues[i].Time
		}

		if localMin < globalMin {
			globalMin = localMin
			globalMinBase = localMinBase
			globalMinStart = localMinStart
			globalMinEnd = localMinEnd
		}
	}

	return globalMin / globalMinBase, globalMinStart, globalMinEnd
}

func (p *PaperWallet) Summary() {
	p.RLock()
	defer p.RUnlock()

	var (
		total        float64
		marketChange float64
		volume       float64
	)

	fmt.Println("----- FINAL WALLET -----")
	for pair := range p.lastCandle {
		asset, quote := SplitAssetQuote(pair)
		assetInfo, ok := p.assets[asset]
		if !ok {
			continue
		}

		quantity := assetInfo.Free + assetInfo.Lock
		value := quantity * p.lastCandle[pair].Close
		if quantity < 0 {
			totalShort := 2.0*p.avgShortPrice[pair]*quantity - p.lastCandle[pair].Close*quantity
			value = math.Abs(totalShort)
		}
		total += value
		marketChange += (p.lastCandle[pair].Close - p.fistCandle[pair].Close) / p.fistCandle[pair].Close
		fmt.Printf("%.4f %s = %.4f %s\n", quantity, asset, value, quote)
	}

	avgMarketChange := marketChange / float64(len(p.lastCandle))
	baseCoinValue := p.assets[p.baseCoin].Free + p.assets[p.baseCoin].Lock
	profit := total + baseCoinValue - p.initialValue
	fmt.Printf("%.4f %s\n", baseCoinValue, p.baseCoin)
	fmt.Println()
	maxDrawDown, _, _ := p.maxDrawdown()
	fmt.Println("----- RETURNS -----")
	fmt.Printf("START PORTFOLIO     = %.2f %s\n", p.initialValue, p.baseCoin)
	fmt.Printf("FINAL PORTFOLIO     = %.2f %s\n", total+baseCoinValue, p.baseCoin)
	fmt.Printf("GROSS PROFIT        =  %f %s (%.2f%%)\n", profit+p.feesPaid, p.baseCoin,
		(profit+p.feesPaid)/p.initialValue*100)
	fmt.Printf("TRADING FEES        =  %f %s\n", p.feesPaid, p.baseCoin)
	fmt.Printf("NET PROFIT          =  %f %s (%.2f%%)\n", profit, p.baseCoin, profit/p.initialValue*100)
	fmt.Printf("MARKET CHANGE (B&H) =  %.2f%%\n", avgMarketChange*100)
	fmt.Println()
	fmt.Println("------ RISK -------")
	fmt.Printf("MAX DRAWDOWN = %.2f %%\n", maxDrawDown*100)
	fmt.Println()
	fmt.Println("------ VOLUME -----")
	for pair, vol := range p.volume {
		volume += vol
		fmt.Printf("%s         = %.2f %s\n", pair, vol, p.baseCoin)
	}
	fmt.Printf("TOTAL           = %.2f %s\n", volume, p.baseCoin)
	fmt.Println("-------------------")
}

// feeRate returns the fee charged for an order type. Orders that rest on the
// book and fill at their own price add liquidity and pay the maker fee, the
// ones that cross the spread - market and triggered stop orders - pay the taker
// fee.
func (p *PaperWallet) feeRate(orderType model.OrderType) float64 {
	switch orderType {
	case model.OrderTypeLimit, model.OrderTypeLimitMaker,
		model.OrderTypeTakeProfit, model.OrderTypeTakeProfitLimit:
		return p.makerFee
	default:
		return p.takerFee
	}
}

// chargeFee deducts a fee, in quote currency, from the wallet, registers it in
// the total paid so far and returns it.
func (p *PaperWallet) chargeFee(quote string, fee float64) float64 {
	if fee == 0 {
		return 0
	}

	if _, ok := p.assets[quote]; !ok {
		p.assets[quote] = &assetInfo{}
	}

	p.assets[quote].Free -= fee
	p.feesPaid += fee

	return fee
}

// ensureAssets creates the balance entries of a pair when missing.
func (p *PaperWallet) ensureAssets(pair string) (asset, quote string) {
	asset, quote = SplitAssetQuote(pair)
	if _, ok := p.assets[asset]; !ok {
		p.assets[asset] = &assetInfo{}
	}
	if _, ok := p.assets[quote]; !ok {
		p.assets[quote] = &assetInfo{}
	}
	return asset, quote
}

// lockKey returns the id under which the reserved funds of an order are
// recorded. OCO legs share one reservation, keyed by the group id.
func lockKey(order model.Order) int64 {
	if order.GroupID != nil {
		return *order.GroupID
	}
	return order.ExchangeID
}

// checkFunds verifies the wallet can afford an order of the given side and
// size at the given price and returns the amount it can fill.
//
// Shorts are modelled as a negative asset balance backed by collateral: a
// short entry takes price*quantity out of the quote balance, and its
// liquidation value is 2*avg*qty - price*qty (collateral plus profit). A buy
// that covers an open short is paid with that value; only the part opening a
// long consumes quote balance. Likewise a sell that closes a long pays its fee
// from the proceeds, but a short entry receives nothing, so the wallet must
// hold enough quote for its collateral and its fee.
//
// When trim is true the amount may come back slightly smaller than requested:
// an order sized with the whole available balance leaves no room for its fee,
// so instead of rejecting it we trim it by the fee. The trim is capped at the
// fee itself, so a genuinely underfunded order still fails.
func (p *PaperWallet) checkFunds(side model.SideType, pair string, amount, price, feeRate float64,
	trim bool) (float64, error) {

	asset, quote := p.ensureAssets(pair)
	free := p.assets[quote].Free

	var need, maxAmount float64
	if side == model.SideTypeSell {
		long := math.Max(p.assets[asset].Free, 0)
		short := math.Max(amount-long, 0)
		if short <= long*1e-9 {
			// closing a long needs no quote at all, so the rounding error left
			// on the balance by a trimmed all-in buy can't trim the sale, and a
			// size overshooting the long by a rounding error closes it exactly
			return math.Min(amount, long), nil
		}
		need = short*price + amount*price*feeRate
		maxAmount = (free + long*price) / (price * (1 + feeRate))
	} else {
		short := math.Max(-p.assets[asset].Free, 0)
		cover := math.Min(short, amount)
		free += cover * (2*p.avgShortPrice[pair] - price) // liquidation of the covered short
		need = (amount-cover)*price + amount*price*feeRate
		maxAmount = (free + short*price) / (price * (1 + feeRate))
	}

	if free >= need {
		return amount, nil
	}

	if trim {
		if trimmed := trimByFee(amount, maxAmount, feeRate); trimmed > 0 {
			return trimmed, nil
		}
	}

	return 0, &OrderError{
		Err:      ErrInsufficientFunds,
		Pair:     pair,
		Quantity: amount,
	}
}

// trimByFee reduces amount to maxAmount when the shortfall is no bigger than
// the fee charged over the order, and returns 0 otherwise - the wallet is then
// underfunded for reasons other than the fee, and the order must be rejected.
func trimByFee(amount, maxAmount, feeRate float64) float64 {
	if feeRate <= 0 {
		return 0
	}

	// the epsilon absorbs the rounding noise of a size derived from the exact
	// available balance, which lands within an ulp of the limit
	if maxAmount < amount/(1+feeRate)*(1-1e-9) {
		return 0
	}

	return math.Min(amount, maxAmount)
}

// reserve moves the funds backing a pending order from Free to Lock and
// records them under key: the asset sold out of a long position, and the quote
// paying a buy or collateralizing a short entry. The share of a buy that covers
// an open short is paid by the short liquidation on fill, so nothing is
// reserved for it.
func (p *PaperWallet) reserve(key int64, side model.SideType, pair string, amount, price float64) {
	asset, quote := p.ensureAssets(pair)

	lock := &orderLock{}
	if side == model.SideTypeSell {
		lock.asset = math.Min(math.Max(p.assets[asset].Free, 0), amount)
		lock.quote = (amount - lock.asset) * price
	} else {
		cover := math.Min(math.Max(-p.assets[asset].Free, 0), amount)
		lock.quote = (amount - cover) * price
	}

	p.assets[asset].Free -= lock.asset
	p.assets[asset].Lock += lock.asset
	p.assets[quote].Free -= lock.quote
	p.assets[quote].Lock += lock.quote
	p.locks[key] = lock

	log.Debugf("%s -> LOCK = %f / FREE %f", asset, p.assets[asset].Lock, p.assets[asset].Free)
}

// release gives the funds reserved under key back to the free balance. It is
// a no-op when nothing is reserved, so cancelling twice is harmless.
func (p *PaperWallet) release(key int64, pair string) {
	lock, ok := p.locks[key]
	if !ok {
		return
	}
	delete(p.locks, key)

	asset, quote := p.ensureAssets(pair)
	p.assets[asset].Lock -= lock.asset
	p.assets[asset].Free += lock.asset
	p.assets[quote].Lock -= lock.quote
	p.assets[quote].Free += lock.quote
}

// settle applies a fill to the free balances and charges its fee, which is
// returned. The caller must have checked the funds (and released any
// reservation of the order) beforehand.
func (p *PaperWallet) settle(side model.SideType, pair string, amount, price, feeRate float64) float64 {
	asset, quote := p.ensureAssets(pair)

	// the average price must be updated before the balance moves, it reads the
	// current position from it
	p.updateAveragePrice(side, pair, amount, price)

	if side == model.SideTypeSell {
		long := math.Min(math.Max(p.assets[asset].Free, 0), amount) // closes a long
		short := amount - long                                      // opens a short
		p.assets[asset].Free -= amount
		p.assets[quote].Free += long*price - short*price // proceeds minus collateral
	} else {
		cover := math.Min(math.Max(-p.assets[asset].Free, 0), amount) // closes a short
		open := amount - cover                                        // opens a long
		p.assets[asset].Free += amount
		p.assets[quote].Free += cover*(2*p.avgShortPrice[pair]-price) - open*price
	}

	log.Debugf("%s -> LOCK = %f / FREE %f", asset, p.assets[asset].Lock, p.assets[asset].Free)

	return p.chargeFee(quote, amount*price*feeRate)
}

// lockOrder checks the funds of a pending order and reserves them under key.
func (p *PaperWallet) lockOrder(key int64, side model.SideType, pair string, amount, price, feeRate float64) error {
	if _, err := p.checkFunds(side, pair, amount, price, feeRate, false); err != nil {
		return err
	}
	p.reserve(key, side, pair, amount, price)
	return nil
}

// fillOrder checks the funds of an immediate fill and settles it, returning
// the filled amount (see checkFunds for why it may be trimmed) and the fee.
func (p *PaperWallet) fillOrder(side model.SideType, pair string, amount, price, feeRate float64) (float64, float64, error) {
	filled, err := p.checkFunds(side, pair, amount, price, feeRate, true)
	if err != nil {
		return 0, 0, err
	}
	return filled, p.settle(side, pair, filled, price, feeRate), nil
}

func (p *PaperWallet) updateAveragePrice(side model.SideType, pair string, amount, value float64) {
	actualQty := 0.0
	asset, quote := SplitAssetQuote(pair)

	if p.assets[asset] != nil {
		actualQty = p.assets[asset].Free
	}

	// without previous position
	if actualQty == 0 {
		if side == model.SideTypeBuy {
			p.avgLongPrice[pair] = value
		} else {
			p.avgShortPrice[pair] = value
		}
		return
	}

	// actual long + order buy
	if actualQty > 0 && side == model.SideTypeBuy {
		positionValue := p.avgLongPrice[pair] * actualQty
		p.avgLongPrice[pair] = (positionValue + amount*value) / (actualQty + amount)
		return
	}

	// actual long + order sell
	if actualQty > 0 && side == model.SideTypeSell {
		closed := math.Min(amount, actualQty)
		profitValue := closed*value - closed*p.avgLongPrice[pair]
		percentage := profitValue / (closed * p.avgLongPrice[pair])
		log.Infof("PROFIT = %.4f %s (%.2f %%)", profitValue, quote, percentage*100.0) // TODO: store profits

		if amount <= actualQty { // not enough quantity to close the position
			return
		}

		p.avgShortPrice[pair] = value

		return
	}

	// actual short + order sell
	if actualQty < 0 && side == model.SideTypeSell {
		positionValue := p.avgShortPrice[pair] * -actualQty
		p.avgShortPrice[pair] = (positionValue + amount*value) / (-actualQty + amount)

		return
	}

	// actual short + order buy
	if actualQty < 0 && side == model.SideTypeBuy {
		closed := math.Min(amount, -actualQty)
		profitValue := closed*p.avgShortPrice[pair] - closed*value
		percentage := profitValue / (closed * p.avgShortPrice[pair])
		log.Infof("PROFIT = %.4f %s (%.2f %%)", profitValue, quote, percentage*100.0) // TODO: store profits

		if amount <= -actualQty { // not enough quantity to close the position
			return
		}

		p.avgLongPrice[pair] = value
	}
}

func (p *PaperWallet) OnCandle(candle model.Candle) {
	p.Lock()
	defer p.Unlock()

	p.lastCandle[candle.Pair] = candle
	if _, ok := p.fistCandle[candle.Pair]; !ok {
		p.fistCandle[candle.Pair] = candle
	}

	// Use indexed lookup for pending orders - O(1) instead of O(n)
	pendingIndices, hasPending := p.pendingOrders[candle.Pair]
	if !hasPending {
		// Fast path: no pending orders for this pair
		p.updateEquityValues(candle)
		return
	}

	if _, ok := p.volume[candle.Pair]; !ok {
		p.volume[candle.Pair] = 0
	}

	// Process only pending orders for this pair
	completedIndices := make([]int, 0, len(pendingIndices))
	for _, i := range pendingIndices {
		order := p.orders[i]
		if order.Status != model.OrderStatusTypeNew {
			completedIndices = append(completedIndices, i)
			continue
		}

		price, ok := fillPrice(order, candle)
		if !ok {
			continue
		}

		// Cancel other orders from same group
		if order.GroupID != nil {
			for j, groupOrder := range p.orders {
				if groupOrder.GroupID != nil && *groupOrder.GroupID == *order.GroupID &&
					groupOrder.ExchangeID != order.ExchangeID &&
					groupOrder.Status == model.OrderStatusTypeNew {
					p.orders[j].Status = model.OrderStatusTypeCanceled
					p.orders[j].UpdatedAt = candle.Time
				}
			}
		}

		// give the reservation back and settle the fill as a trade at the
		// fill price, exactly like a market order
		p.release(lockKey(order), order.Pair)
		fee := p.settle(order.Side, order.Pair, order.Quantity, price, p.feeRate(order.Type))

		p.volume[candle.Pair] += order.Quantity * price
		p.orders[i].UpdatedAt = candle.Time
		p.orders[i].Status = model.OrderStatusTypeFilled
		p.orders[i].Price = price
		p.orders[i].Fee = fee
		completedIndices = append(completedIndices, i)

		// the protection of the position it opened takes over from the next
		// candle
		if legs, ok := p.protections[order.ExchangeID]; ok {
			delete(p.protections, order.ExchangeID)
			p.activate(order.Pair, legs)
		}
	}

	// Remove completed orders from pending index
	if len(completedIndices) > 0 {
		p.removeCompletedOrders(candle.Pair, completedIndices)
	}

	p.updateEquityValues(candle)
}

// fillPrice reports whether a pending order is filled by the candle and at
// which price. Limit orders fill at their own price once the candle trades
// through it; stop orders fill at their stop price once it is touched. A buy
// limit rests below the market and a buy stop above it, the sell side is the
// mirror image.
func fillPrice(order model.Order, candle model.Candle) (float64, bool) {
	switch order.Type {
	case model.OrderTypeLimit, model.OrderTypeLimitMaker,
		model.OrderTypeTakeProfit, model.OrderTypeTakeProfitLimit:
		if order.Side == model.SideTypeBuy && candle.Low <= order.Price ||
			order.Side == model.SideTypeSell && candle.High >= order.Price {
			return order.Price, true
		}
	case model.OrderTypeStopLoss, model.OrderTypeStopLossLimit:
		if order.Stop == nil {
			return 0, false
		}
		if order.Side == model.SideTypeBuy && candle.High >= *order.Stop ||
			order.Side == model.SideTypeSell && candle.Low <= *order.Stop {
			return *order.Stop, true
		}
	}
	return 0, false
}

// removeCompletedOrders removes completed order indices from the pending map
func (p *PaperWallet) removeCompletedOrders(pair string, completedIndices []int) {
	if len(completedIndices) == 0 {
		return
	}

	pending := p.pendingOrders[pair]
	newPending := make([]int, 0, len(pending)-len(completedIndices))
	completedMap := make(map[int]bool, len(completedIndices))
	for _, idx := range completedIndices {
		completedMap[idx] = true
	}

	for _, idx := range pending {
		if !completedMap[idx] {
			newPending = append(newPending, idx)
		}
	}

	if len(newPending) == 0 {
		delete(p.pendingOrders, pair)
	} else {
		p.pendingOrders[pair] = newPending
	}
}

// updateEquityValues calculates and stores equity values for complete candles
func (p *PaperWallet) updateEquityValues(candle model.Candle) {
	if !candle.Complete {
		return
	}

	if candle.Complete {
		// the pair of each asset, which is not always asset + quote, eg: the
		// XAUUSDm symbol of MT5
		pairs := make(map[string]string, len(p.lastCandle))
		for pair := range p.lastCandle {
			if asset, quote := SplitAssetQuote(pair); quote == p.baseCoin {
				pairs[asset] = pair
			}
		}

		var total float64
		for asset, info := range p.assets {
			amount := info.Free + info.Lock
			pair, ok := pairs[asset]
			if !ok {
				pair = strings.ToUpper(asset + p.baseCoin)
			}
			if amount < 0 {
				v := math.Abs(amount)
				liquid := 2*v*p.avgShortPrice[pair] - v*p.lastCandle[pair].Close
				total += liquid
			} else {
				total += amount * p.lastCandle[pair].Close
			}

			p.assetValues[asset] = append(p.assetValues[asset], AssetValue{
				Time:  candle.Time,
				Value: amount * p.lastCandle[pair].Close,
			})
		}

		baseCoinInfo := p.assets[p.baseCoin]
		p.equityValues = append(p.equityValues, AssetValue{
			Time:  candle.Time,
			Value: total + baseCoinInfo.Lock + baseCoinInfo.Free,
		})
	}
}

func (p *PaperWallet) Account() (model.Account, error) {
	p.RLock()
	defer p.RUnlock()
	return p.account(), nil
}

// account is Account for the callers holding the lock.
func (p *PaperWallet) account() model.Account {
	balances := make([]model.Balance, 0, len(p.assets))
	for pair, info := range p.assets {
		balances = append(balances, model.Balance{
			Asset: pair,
			Free:  info.Free,
			Lock:  info.Lock,
		})
	}

	return model.Account{
		Balances: balances,
	}
}

func (p *PaperWallet) Position(pair string) (asset, quote float64, err error) {
	p.RLock()
	defer p.RUnlock()

	assetTick, quoteTick := SplitAssetQuote(pair)
	acc := p.account()
	assetBalance, quoteBalance := acc.Balance(assetTick, quoteTick)

	return assetBalance.Free + assetBalance.Lock, quoteBalance.Free + quoteBalance.Lock, nil
}

func (p *PaperWallet) CreateOrderOCO(side model.SideType, pair string,
	size, price, stop, stopLimit float64) ([]model.Order, error) {
	p.Lock()
	defer p.Unlock()

	if size == 0 {
		return nil, ErrInvalidQuantity
	}

	// both legs share one reservation, released when either fills or cancels
	groupID := p.nextID()
	err := p.lockOrder(groupID, side, pair, size, price, p.feeRate(model.OrderTypeLimitMaker))
	if err != nil {
		return nil, err
	}

	limitMaker := model.Order{
		ExchangeID: p.nextID(),
		CreatedAt:  p.lastCandle[pair].Time,
		UpdatedAt:  p.lastCandle[pair].Time,
		Pair:       pair,
		Side:       side,
		Type:       model.OrderTypeLimitMaker,
		Status:     model.OrderStatusTypeNew,
		Price:      price,
		Quantity:   size,
		GroupID:    &groupID,
		RefPrice:   p.lastCandle[pair].Close,
	}

	stopOrder := model.Order{
		ExchangeID: p.nextID(),
		CreatedAt:  p.lastCandle[pair].Time,
		UpdatedAt:  p.lastCandle[pair].Time,
		Pair:       pair,
		Side:       side,
		Type:       model.OrderTypeStopLoss,
		Status:     model.OrderStatusTypeNew,
		Price:      stopLimit,
		Stop:       &stop,
		Quantity:   size,
		GroupID:    &groupID,
		RefPrice:   p.lastCandle[pair].Close,
	}
	p.addOrder(limitMaker, true)
	p.addOrder(stopOrder, true)

	return []model.Order{limitMaker, stopOrder}, nil
}

func (p *PaperWallet) CreateOrderLimit(side model.SideType, pair string,
	size float64, limit float64) (model.Order, error) {

	p.Lock()
	defer p.Unlock()

	return p.createOrderLimit(side, pair, size, limit)
}

func (p *PaperWallet) createOrderLimit(side model.SideType, pair string, size, limit float64) (model.Order, error) {
	if size == 0 {
		return model.Order{}, ErrInvalidQuantity
	}

	id := p.nextID()
	err := p.lockOrder(id, side, pair, size, limit, p.feeRate(model.OrderTypeLimit))
	if err != nil {
		return model.Order{}, err
	}
	order := model.Order{
		ExchangeID: id,
		CreatedAt:  p.lastCandle[pair].Time,
		UpdatedAt:  p.lastCandle[pair].Time,
		Pair:       pair,
		Side:       side,
		Type:       model.OrderTypeLimit,
		Status:     model.OrderStatusTypeNew,
		Price:      limit,
		Quantity:   size,
	}
	p.addOrder(order, true)
	return order, nil
}

func (p *PaperWallet) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	p.Lock()
	defer p.Unlock()

	return p.createOrderMarket(side, pair, size)
}

func (p *PaperWallet) CreateOrderStop(pair string, size float64, limit float64) (model.Order, error) {
	p.Lock()
	defer p.Unlock()

	if size == 0 {
		return model.Order{}, ErrInvalidQuantity
	}

	id := p.nextID()
	err := p.lockOrder(id, model.SideTypeSell, pair, size, limit, p.feeRate(model.OrderTypeStopLossLimit))
	if err != nil {
		return model.Order{}, err
	}

	order := model.Order{
		ExchangeID: id,
		CreatedAt:  p.lastCandle[pair].Time,
		UpdatedAt:  p.lastCandle[pair].Time,
		Pair:       pair,
		Side:       model.SideTypeSell,
		Type:       model.OrderTypeStopLossLimit,
		Status:     model.OrderStatusTypeNew,
		Price:      limit,
		Stop:       &limit,
		Quantity:   size,
	}
	p.addOrder(order, true)
	return order, nil
}

func (p *PaperWallet) createOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	if size == 0 {
		return model.Order{}, ErrInvalidQuantity
	}

	price := p.lastCandle[pair].Close

	// the wallet may fill less than requested to make room for the fee
	filled, fee, err := p.fillOrder(side, pair, size, price, p.feeRate(model.OrderTypeMarket))
	if err != nil {
		return model.Order{}, err
	}

	if _, ok := p.volume[pair]; !ok {
		p.volume[pair] = 0
	}

	p.volume[pair] += price * filled

	order := model.Order{
		ExchangeID: p.nextID(),
		CreatedAt:  p.lastCandle[pair].Time,
		UpdatedAt:  p.lastCandle[pair].Time,
		Pair:       pair,
		Side:       side,
		Type:       model.OrderTypeMarket,
		Status:     model.OrderStatusTypeFilled,
		Price:      price,
		Quantity:   filled,
		Fee:        fee,
	}

	p.addOrder(order, false)

	return order, nil
}

func (p *PaperWallet) CreateOrderMarketQuote(side model.SideType, pair string,
	quoteQuantity float64) (model.Order, error) {
	p.Lock()
	defer p.Unlock()

	info := p.AssetsInfo(pair)
	quantity, err := amountToLotSize(quoteQuantity/p.lastCandle[pair].Close, info.MinQuantity, info.StepSize,
		info.BaseAssetPrecision)
	if err != nil {
		return model.Order{}, err
	}
	return p.createOrderMarket(side, pair, quantity)
}

// amountToLotSize cuts amount down to a whole number of steps above the least
// quantity, precision decimals at most, as an exchange sizes an order: 0 under
// the least quantity. It computes in decimal, where a float division can leave
// a step short.
func amountToLotSize(amount, minQuantity, step float64, precision int) (float64, error) {
	var values [3]decimal.Decimal
	for i, v := range []float64{amount, minQuantity, step} {
		d, err := decimal.NewFromString(strconv.FormatFloat(v, 'f', -1, 64))
		if err != nil {
			return 0, fmt.Errorf("the lot size of %v: %w", amount, err)
		}
		values[i] = d
	}
	value := values[0].Sub(values[1])
	if value.IsNegative() {
		return 0, nil
	}
	if values[2].IsPositive() {
		value = value.Div(values[2]).Truncate(0).Mul(values[2])
	}
	return strconv.ParseFloat(value.Add(values[1]).Truncate(int32(precision)).String(), 64)
}

// Cancel cancels an open order and releases its reserved funds. Cancelling a
// leg of an OCO cancels the whole group, as exchanges do. It fails with
// ErrOrderNotOpen for orders already filled or canceled, so their balances are
// never touched twice.
func (p *PaperWallet) Cancel(order model.Order) error {
	p.Lock()
	defer p.Unlock()

	idx, ok := p.ordersByID[order.ExchangeID]
	if !ok {
		return ErrOrderNotFound
	}

	target := p.orders[idx]
	if target.Status != model.OrderStatusTypeNew {
		return &OrderError{Err: ErrOrderNotOpen, Pair: target.Pair, Quantity: target.Quantity}
	}

	for i, o := range p.orders {
		sameGroup := target.GroupID != nil && o.GroupID != nil && *o.GroupID == *target.GroupID
		if (i == idx || sameGroup) && o.Status == model.OrderStatusTypeNew {
			p.orders[i].Status = model.OrderStatusTypeCanceled
			p.orders[i].UpdatedAt = p.lastCandle[target.Pair].Time
		}
	}

	// the legs waiting for an entry go with it
	for _, i := range p.protections[target.ExchangeID] {
		if p.orders[i].Status == model.OrderStatusTypeNew {
			p.orders[i].Status = model.OrderStatusTypeCanceled
			p.orders[i].UpdatedAt = p.lastCandle[target.Pair].Time
		}
	}
	delete(p.protections, target.ExchangeID)

	p.release(lockKey(target), target.Pair)
	return nil
}

// CreateOrderMarketProtected fills a market order and protects the position it
// opens with a stop loss at stop and a take profit at profit, as the brokers
// placing them with the order do, see service.ProtectionBroker: the levels set
// are the legs of an OCO order, reserving the position.
func (p *PaperWallet) CreateOrderMarketProtected(side model.SideType, pair string, size, stop,
	profit float64) ([]model.Order, error) {

	p.Lock()
	defer p.Unlock()

	if err := checkProtection(side, p.lastCandle[pair].Close, stop, profit); err != nil {
		return nil, &OrderError{Err: err, Pair: pair, Quantity: size}
	}

	order, err := p.createOrderMarket(side, pair, size)
	if err != nil {
		return nil, err
	}

	legs := p.addLegs(side, pair, order.Quantity, stop, profit)
	p.activate(pair, legs)
	return append([]model.Order{order}, p.legOrders(legs)...), nil
}

// CreateOrderLimitProtected places a limit order at limit, whose position is
// protected by a stop loss at stop and a take profit at profit once it fills,
// see service.ProtectionBroker.
func (p *PaperWallet) CreateOrderLimitProtected(side model.SideType, pair string, size, limit, stop,
	profit float64) ([]model.Order, error) {

	p.Lock()
	defer p.Unlock()

	if err := checkProtection(side, limit, stop, profit); err != nil {
		return nil, &OrderError{Err: err, Pair: pair, Quantity: size}
	}

	entry, err := p.createOrderLimit(side, pair, size, limit)
	if err != nil {
		return nil, err
	}
	return p.protectEntry(entry, stop, profit), nil
}

// CreateOrderStopProtected places a stop order triggered at level, whose
// position is protected by a stop loss at stop and a take profit at profit once
// it fills, see service.ProtectionBroker.
func (p *PaperWallet) CreateOrderStopProtected(side model.SideType, pair string, size, level, stop,
	profit float64) ([]model.Order, error) {

	p.Lock()
	defer p.Unlock()

	if err := checkProtection(side, level, stop, profit); err != nil {
		return nil, &OrderError{Err: err, Pair: pair, Quantity: size}
	}
	if size == 0 {
		return nil, ErrInvalidQuantity
	}

	id := p.nextID()
	if err := p.lockOrder(id, side, pair, size, level, p.feeRate(model.OrderTypeStopLoss)); err != nil {
		return nil, err
	}
	entry := model.Order{
		ExchangeID: id,
		CreatedAt:  p.lastCandle[pair].Time,
		UpdatedAt:  p.lastCandle[pair].Time,
		Pair:       pair,
		Side:       side,
		Type:       model.OrderTypeStopLoss,
		Status:     model.OrderStatusTypeNew,
		Price:      level,
		Stop:       &level,
		Quantity:   size,
	}
	p.addOrder(entry, true)
	return p.protectEntry(entry, stop, profit), nil
}

// protectEntry adds the legs protecting the position an entry opens, which wait
// for its fill.
func (p *PaperWallet) protectEntry(entry model.Order, stop, profit float64) []model.Order {
	legs := p.addLegs(entry.Side, entry.Pair, entry.Quantity, stop, profit)
	p.protections[entry.ExchangeID] = legs
	return append([]model.Order{entry}, p.legOrders(legs)...)
}

// addLegs stores the legs of the levels protecting a position of size opened on
// side, the take profit first, then the stop loss, as an OCO order, without
// putting them on the book, and returns their indices.
func (p *PaperWallet) addLegs(side model.SideType, pair string, size, stop, profit float64) []int {
	closing := model.SideTypeBuy
	if side == model.SideTypeBuy {
		closing = model.SideTypeSell
	}

	group := p.nextID()
	var legs []int
	for _, level := range []struct {
		orderType model.OrderType
		price     float64
	}{{model.OrderTypeLimitMaker, profit}, {model.OrderTypeStopLoss, stop}} {
		if level.price <= 0 {
			continue
		}

		leg := model.Order{
			ExchangeID: p.nextID(),
			CreatedAt:  p.lastCandle[pair].Time,
			UpdatedAt:  p.lastCandle[pair].Time,
			Pair:       pair,
			Side:       closing,
			Type:       level.orderType,
			Status:     model.OrderStatusTypeNew,
			Price:      level.price,
			Quantity:   size,
			GroupID:    &group,
			RefPrice:   p.lastCandle[pair].Close,
		}
		if level.orderType == model.OrderTypeStopLoss {
			price := level.price
			leg.Stop = &price
		}

		legs = append(legs, len(p.orders))
		p.addOrder(leg, false)
	}
	return legs
}

// activate puts the legs of a protection on the book: they reserve the position
// and fill from the next candle on, the ones canceled meanwhile stay out. They
// only close the position, so unlike an entry they need no funds: a broker
// always takes them, whatever the fee of their fill.
//
// The stop loss goes on the book first, so a candle touching both levels fills
// it: which one the price reached first is unknown, and the worst is assumed.
func (p *PaperWallet) activate(pair string, legs []int) {
	var open []int
	for _, i := range legs {
		if p.orders[i].Status == model.OrderStatusTypeNew {
			open = append(open, i)
		}
	}
	if len(open) == 0 {
		return
	}
	slices.SortStableFunc(open, func(a, b int) int {
		return p.stopFirst(a) - p.stopFirst(b)
	})

	leg := p.orders[open[0]]
	p.reserve(lockKey(leg), leg.Side, pair, leg.Quantity, leg.Price)
	p.pendingOrders[pair] = append(p.pendingOrders[pair], open...)
}

// Protect protects size units of the position held on side of pair with a stop
// loss at stop and a take profit at profit, 0 for none, see
// service.PositionProtector: the legs CreateOrderMarketProtected places, on the
// book at once, reserving the position. As a broker requires, the levels are
// on their side of the last price, and the position is held, free of other
// legs.
func (p *PaperWallet) Protect(side model.SideType, pair string, size, stop, profit float64) ([]model.Order, error) {
	p.Lock()
	defer p.Unlock()

	fail := func(err error) ([]model.Order, error) {
		return nil, &OrderError{Err: err, Pair: pair, Quantity: size}
	}
	if size <= 0 {
		return fail(ErrInvalidQuantity)
	}
	if err := checkProtection(side, p.lastCandle[pair].Close, stop, profit); err != nil {
		return fail(err)
	}

	asset, _ := p.ensureAssets(pair)
	held := p.assets[asset].Free // a long reserved by its legs isn't free
	if side == model.SideTypeSell {
		held = -held
	}
	closing := model.SideTypeSell
	if side == model.SideTypeSell {
		closing = model.SideTypeBuy
	}
	for _, i := range p.pendingOrders[pair] {
		if order := p.orders[i]; order.GroupID != nil && order.Side == closing &&
			order.Status == model.OrderStatusTypeNew {
			return fail(fmt.Errorf("the %s position of %s is protected already by the order %d: %w", side, pair,
				order.ExchangeID, ErrNotSupported))
		}
	}
	switch {
	case held <= 0:
		return fail(fmt.Errorf("no %s position of %s to protect: %w", side, pair, ErrNotSupported))
	case held < size*(1-1e-9):
		return fail(fmt.Errorf("the %s position of %s is %v, not %v: %w", side, pair, held, size,
			ErrInvalidQuantity))
	}

	legs := p.addLegs(side, pair, size, stop, profit)
	p.activate(pair, legs)
	return p.legOrders(legs), nil
}

// UpdateProtection moves a leg of a protection, or of an OCO order, to level: it
// fills there from the next candle on. As a broker requires, the level must be
// on its side of the entry of a leg waiting for it, else of the last price, and
// of the other leg.
func (p *PaperWallet) UpdateProtection(leg model.Order, level float64) (model.Order, error) {
	p.Lock()
	defer p.Unlock()

	idx, ok := p.ordersByID[leg.ExchangeID]
	if !ok {
		return model.Order{}, ErrOrderNotFound
	}
	order := p.orders[idx]
	fail := func(err error) (model.Order, error) {
		return model.Order{}, &OrderError{Err: err, Pair: order.Pair, Quantity: order.Quantity}
	}
	switch {
	case order.Status != model.OrderStatusTypeNew:
		return fail(ErrOrderNotOpen)
	case order.GroupID == nil || order.Type != model.OrderTypeLimitMaker && order.Type != model.OrderTypeStopLoss:
		return fail(fmt.Errorf("order %d is not a stop loss or a take profit: %w", order.ExchangeID,
			ErrNotSupported))
	}

	// the levels of the group once moved, and the price they are set against
	var stop, profit float64
	for _, other := range p.orders {
		if other.GroupID != nil && *other.GroupID == *order.GroupID && other.Status == model.OrderStatusTypeNew {
			if other.Type == model.OrderTypeLimitMaker {
				profit = other.Price
			} else {
				stop = other.Price
			}
		}
	}
	if order.Type == model.OrderTypeLimitMaker {
		profit = level
	} else {
		stop = level
	}
	reference := p.lastCandle[order.Pair].Close
	for entryID, legs := range p.protections {
		if slices.Contains(legs, idx) {
			reference = p.orders[p.ordersByID[entryID]].Price
		}
	}

	side := model.SideTypeBuy // of the position the leg closes
	if order.Side == model.SideTypeBuy {
		side = model.SideTypeSell
	}
	if err := checkProtection(side, reference, stop, profit); err != nil {
		return fail(err)
	}

	p.orders[idx].Price = level
	p.orders[idx].UpdatedAt = p.lastCandle[order.Pair].Time
	if order.Type == model.OrderTypeStopLoss {
		p.orders[idx].Stop = &level
	}
	return p.orders[idx], nil
}

// UpdateLimit moves a pending limit order to limit, see
// service.ProtectionBroker: the funds it reserves follow it, and the legs
// waiting for it stay where they are.
func (p *PaperWallet) UpdateLimit(order model.Order, limit float64) (model.Order, error) {
	p.Lock()
	defer p.Unlock()

	idx, ok := p.ordersByID[order.ExchangeID]
	if !ok {
		return model.Order{}, ErrOrderNotFound
	}
	current := p.orders[idx]
	fail := func(err error) (model.Order, error) {
		return model.Order{}, &OrderError{Err: err, Pair: current.Pair, Quantity: current.Quantity}
	}
	switch {
	case current.Status != model.OrderStatusTypeNew:
		return fail(ErrOrderNotOpen)
	case current.Type != model.OrderTypeLimit || current.GroupID != nil:
		return fail(fmt.Errorf("order %d is not a pending limit order: %w", current.ExchangeID, ErrNotSupported))
	case limit <= 0:
		return fail(fmt.Errorf("%w: limit %v", ErrInvalidProtection, limit))
	}

	var stop, profit float64
	for _, i := range p.protections[current.ExchangeID] {
		switch leg := p.orders[i]; {
		case leg.Status != model.OrderStatusTypeNew:
		case leg.Type == model.OrderTypeLimitMaker:
			profit = leg.Price
		default:
			stop = leg.Price
		}
	}
	if stop > 0 || profit > 0 {
		if err := checkProtection(current.Side, limit, stop, profit); err != nil {
			return fail(err)
		}
	}

	// the funds reserved at the old limit, or back at it without enough for
	// the new one
	key := lockKey(current)
	p.release(key, current.Pair)
	feeRate := p.feeRate(model.OrderTypeLimit)
	if err := p.lockOrder(key, current.Side, current.Pair, current.Quantity, limit, feeRate); err != nil {
		p.reserve(key, current.Side, current.Pair, current.Quantity, current.Price)
		return model.Order{}, err
	}

	p.orders[idx].Price = limit
	p.orders[idx].UpdatedAt = p.lastCandle[current.Pair].Time
	return p.orders[idx], nil
}

// stopFirst orders the stop loss legs before the take profit ones.
func (p *PaperWallet) stopFirst(i int) int {
	if p.orders[i].Type == model.OrderTypeStopLoss {
		return 0
	}
	return 1
}

func (p *PaperWallet) legOrders(legs []int) []model.Order {
	orders := make([]model.Order, 0, len(legs))
	for _, i := range legs {
		orders = append(orders, p.orders[i])
	}
	return orders
}

// addOrder stores an order and, when it is still open, indexes it for the
// fills of the next candles.
func (p *PaperWallet) addOrder(order model.Order, pending bool) {
	idx := len(p.orders)
	p.orders = append(p.orders, order)
	p.ordersByID[order.ExchangeID] = idx
	if pending {
		p.pendingOrders[order.Pair] = append(p.pendingOrders[order.Pair], idx)
	}
}

func (p *PaperWallet) Order(_ string, id int64) (model.Order, error) {
	p.RLock()
	defer p.RUnlock()

	if idx, ok := p.ordersByID[id]; ok {
		return p.orders[idx], nil
	}
	return model.Order{}, ErrOrderNotFound
}

func (p *PaperWallet) CandlesByPeriod(ctx context.Context, pair, period string,
	start, end time.Time) ([]model.Candle, error) {
	return p.feeder.CandlesByPeriod(ctx, pair, period, start, end)
}

func (p *PaperWallet) CandlesByLimit(ctx context.Context, pair, period string, limit int) ([]model.Candle, error) {
	return p.feeder.CandlesByLimit(ctx, pair, period, limit)
}

func (p *PaperWallet) CandlesSubscription(ctx context.Context, pair, timeframe string) (chan model.Candle, chan error) {
	return p.feeder.CandlesSubscription(ctx, pair, timeframe)
}
