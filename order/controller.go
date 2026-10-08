package order

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
	log "github.com/sirupsen/logrus"
)

// ErrBotStopped is returned by CreateOrder* when the controller has been stopped.
// Cancel is intentionally not gated, so open orders can still be cleaned up,
// nor are the protections of the positions: they are refused, ErrBotStopped
// too, only once the controller is closed, see Close.
var ErrBotStopped = errors.New("bot is stopped")

// errClosed refuses every call to the exchange of a closed controller.
var errClosed = fmt.Errorf("%w for good, as it exits", ErrBotStopped)

type summary struct {
	Pair             string
	WinLong          []float64
	WinLongPercent   []float64
	WinShort         []float64
	WinShortPercent  []float64
	LoseLong         []float64
	LoseLongPercent  []float64
	LoseShort        []float64
	LoseShortPercent []float64
	Volume           float64

	// Cached values to avoid repeated slice concatenations
	cachedWin         []float64
	cachedWinPercent  []float64
	cachedLose        []float64
	cachedLosePercent []float64
	cacheValid        bool
}

func (s *summary) invalidateCache() {
	s.cacheValid = false
}

func (s *summary) Win() []float64 {
	if !s.cacheValid {
		s.rebuildCache()
	}
	return s.cachedWin
}

func (s *summary) WinPercent() []float64 {
	if !s.cacheValid {
		s.rebuildCache()
	}
	return s.cachedWinPercent
}

func (s *summary) Lose() []float64 {
	if !s.cacheValid {
		s.rebuildCache()
	}
	return s.cachedLose
}

func (s *summary) LosePercent() []float64 {
	if !s.cacheValid {
		s.rebuildCache()
	}
	return s.cachedLosePercent
}

func (s *summary) rebuildCache() {
	s.cachedWin = append(s.WinLong, s.WinShort...)
	s.cachedWinPercent = append(s.WinLongPercent, s.WinShortPercent...)
	s.cachedLose = append(s.LoseLong, s.LoseShort...)
	s.cachedLosePercent = append(s.LoseLongPercent, s.LoseShortPercent...)
	s.cacheValid = true
}

func (s summary) Profit() float64 {
	profit := 0.0
	for _, value := range append(s.Win(), s.Lose()...) {
		profit += value
	}
	return profit
}

// SQN is the System Quality Number (Van Tharp): sqrt(trades) * mean / stddev
// of the trade results. It needs at least two trades with distinct results,
// and is 0 otherwise.
func (s summary) SQN() float64 {
	total := float64(len(s.Win()) + len(s.Lose()))
	if total < 2 {
		return 0
	}
	avgProfit := s.Profit() / total
	stdDev := 0.0
	for _, profit := range append(s.Win(), s.Lose()...) {
		stdDev += (profit - avgProfit) * (profit - avgProfit)
	}
	stdDev = math.Sqrt(stdDev / total)
	if stdDev == 0 {
		return 0
	}
	return math.Sqrt(total) * avgProfit / stdDev
}

func (s summary) Payoff() float64 {
	avgWin := 0.0
	avgLose := 0.0

	for _, value := range s.WinPercent() {
		avgWin += value
	}

	for _, value := range s.LosePercent() {
		avgLose += value
	}

	if len(s.Win()) == 0 || len(s.Lose()) == 0 || avgLose == 0 {
		return 0
	}

	return (avgWin / float64(len(s.Win()))) / math.Abs(avgLose/float64(len(s.Lose())))
}

func (s summary) ProfitFactor() float64 {
	if len(s.Lose()) == 0 {
		return 0
	}
	profit := 0.0
	for _, value := range s.WinPercent() {
		profit += value
	}

	loss := 0.0
	for _, value := range s.LosePercent() {
		loss += value
	}
	return profit / math.Abs(loss)
}

func (s summary) WinPercentage() float64 {
	if len(s.Win())+len(s.Lose()) == 0 {
		return 0
	}

	return float64(len(s.Win())) / float64(len(s.Win())+len(s.Lose())) * 100
}

func (s summary) String() string {
	tableString := &strings.Builder{}
	table := tablewriter.NewTable(tableString,
		tablewriter.WithRenderer(renderer.NewBlueprint(tw.Rendition{
			Symbols: tw.NewSymbols(tw.StyleASCII),
		})),
		tablewriter.WithRowAlignmentConfig(tw.CellAlignment{
			PerColumn: []tw.Align{tw.AlignLeft, tw.AlignRight},
		}),
	)
	_, quote := exchange.SplitAssetQuote(s.Pair)
	data := [][]string{
		{"Coin", s.Pair},
		{"Trades", strconv.Itoa(len(s.Lose()) + len(s.Win()))},
		{"Win", strconv.Itoa(len(s.Win()))},
		{"Loss", strconv.Itoa(len(s.Lose()))},
		{"% Win", fmt.Sprintf("%.1f", s.WinPercentage())},
		{"Payoff", fmt.Sprintf("%.3f", s.Payoff())},
		{"Pr.Fact", fmt.Sprintf("%.3f", s.ProfitFactor())},
		{"Profit", fmt.Sprintf("%.4f %s", s.Profit(), quote)},
		{"Volume", fmt.Sprintf("%.4f %s", s.Volume, quote)},
	}
	if err := table.Bulk(data); err != nil {
		log.Error(err)
	}

	if err := table.Render(); err != nil {
		log.Error(err)
	}

	return tableString.String()
}

func (s summary) SaveReturns(filename string) (err error) {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer func() {
		// the close of the file written may fail too, eg: on a full disk
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()

	for _, value := range s.WinPercent() {
		_, err = fmt.Fprintf(file, "%.4f\n", value)
		if err != nil {
			return err
		}
	}

	for _, value := range s.LosePercent() {
		_, err = fmt.Fprintf(file, "%.4f\n", value)
		if err != nil {
			return err
		}
	}
	return nil
}

type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	StatusError   Status = "error"
)

type Result struct {
	Pair          string
	ProfitPercent float64
	ProfitValue   float64
	Side          model.SideType
	Duration      time.Duration
	CreatedAt     time.Time
}

type Position struct {
	Side     model.SideType
	AvgPrice float64
	Quantity float64
	// Fee paid to open the position, in quote currency. It is settled against
	// the profit as the position is closed.
	Fee       float64
	CreatedAt time.Time
	// Entry is the fill that opened the position, for the profit the broker
	// books when it closes, see service.AccountReporter.
	Entry model.Order
}

// quantityEpsilon is the relative tolerance used to consider two quantities
// equal. Quantities are accumulated in floating point (0.1 + 0.2 != 0.3), and
// an order sized with the position reported by the exchange may differ from
// the tracked quantity by a rounding error, which would leave a dust position
// open forever.
const quantityEpsilon = 1e-9

func sameQuantity(a, b float64) bool {
	return math.Abs(a-b) <= quantityEpsilon*math.Max(math.Abs(a), math.Abs(b))
}

// filledAt is the time an order was executed: the last update for orders that
// rested on the book, the creation for the ones filled immediately.
func filledAt(order *model.Order) time.Time {
	if !order.UpdatedAt.IsZero() {
		return order.UpdatedAt
	}
	return order.CreatedAt
}

func (p *Position) Update(order *model.Order) (result *Result, finished bool) {
	// Price is the average fill price reported by the exchange, also for stop
	// orders: live exchanges return the executed price, and the paper wallet
	// records the trigger price it filled at.
	price := order.Price

	if p.Side == order.Side {
		p.AvgPrice = (p.AvgPrice*p.Quantity + price*order.Quantity) / (p.Quantity + order.Quantity)
		p.Quantity += order.Quantity
		p.Fee += order.Fee
		return nil, false
	}

	// The order (partially) closes the position: settle the closed leg
	// against the position state before any mutation. Shorts profit when
	// the price falls.
	closedQuantity := math.Min(p.Quantity, order.Quantity)
	profitPrice := price - p.AvgPrice
	if p.Side == model.SideTypeSell {
		profitPrice = p.AvgPrice - price
	}

	// Both legs are charged a fee, but only the share matching the closed
	// quantity belongs to this result.
	var entryFee, exitFee float64
	if p.Quantity > 0 {
		entryFee = p.Fee * closedQuantity / p.Quantity
	}
	if order.Quantity > 0 {
		exitFee = order.Fee * closedQuantity / order.Quantity
	}

	order.Profit = profitPrice / p.AvgPrice
	order.ProfitValue = profitPrice * closedQuantity
	if fee := entryFee + exitFee; fee > 0 {
		order.ProfitValue -= fee
		order.Profit = order.ProfitValue / (p.AvgPrice * closedQuantity)
	}

	closedAt := filledAt(order)
	result = &Result{
		CreatedAt:     closedAt,
		Pair:          order.Pair,
		Duration:      closedAt.Sub(p.CreatedAt),
		ProfitPercent: order.Profit,
		ProfitValue:   order.ProfitValue,
		Side:          p.Side,
	}

	if sameQuantity(p.Quantity, order.Quantity) {
		finished = true
	} else if p.Quantity > order.Quantity {
		p.Quantity -= order.Quantity
		p.Fee -= entryFee
	} else {
		// the order overshoots and opens a position on the other side, the
		// share of its fee not consumed by the close opens the new position
		p.Fee = order.Fee - exitFee
		p.Quantity = order.Quantity - p.Quantity
		p.Side = order.Side
		p.CreatedAt = closedAt
		p.AvgPrice = price
		p.Entry = *order
	}

	return result, finished
}

// Controller creates the orders of the strategies on the exchange, stores them
// and follows them until they fill or are canceled, settling the positions
// and the results of their pairs.
//
// The orders of a pair are serialized, each one from its call to the exchange
// to its recording (see lockPair), but nothing else waits for the exchange: a
// slow call, eg: an MT5 trade request whose answer was lost, looked for in the
// account, holds the orders of its pair only, while UpdateOrders, the reads
// and the orders of the other pairs go on.
type Controller struct {
	mtx            sync.RWMutex // the state below, never held during a call to the exchange
	updateMtx      sync.Mutex   // serializes UpdateOrders, so a fill is never settled twice
	pairsMtx       sync.Mutex
	pairs          map[string]*sync.Mutex // serialize the orders of each pair, see lockPair
	inflight       int                    // calls begun and not recorded yet, under mtx, see begin
	idle           *sync.Cond             // on mtx, broadcast when inflight drops to 0
	closed         bool                   // under mtx: nothing reaches the exchange any more, see Close
	updatesClosed  bool                   // under updateMtx: UpdateOrders does nothing, see Close
	ctx            context.Context
	exchange       service.Exchange
	storage        storage.Storage
	orderFeed      *Feed
	notifier       service.Notifier
	Results        map[string]*summary
	lastPrice      map[string]float64
	tickerInterval time.Duration
	finish         chan bool
	status         Status

	position map[string]*Position

	// pending holds the orders waiting for an update from the exchange, keyed
	// by their storage id: the ones created by this controller plus, once
	// started, the open orders left in storage by a previous run.
	pending map[int64]model.Order

	// restoring is set while restore replays the stored fills: they settle
	// the positions and the results quietly, notified already.
	restoring bool

	// unstored holds the orders created or changed on the exchange that the
	// storage failed to write, in their last state, by id: followed all the
	// same, and written again by UpdateOrders until the storage takes them.
	unstored map[int64]model.Order
	// provisional numbers, below zero, the orders the storage failed to
	// number: followed in memory only.
	provisional int64
	warned      time.Time // the last warning of the orders unstored
	// the notices of the orders the storage fails to write, see notStored:
	// the last one; the new failures since, the ones of orders never numbered
	// among them, and the last of them; the last notice of the orders stored
	// at last, and how many were since
	notified   time.Time
	unnotified int
	unnumbered int
	lastFailed error
	recovered  time.Time
	storedLate int
}

// unstoredWarnEvery is how often the orders the storage still fails to write
// are warned about.
const unstoredWarnEvery = 5 * time.Minute

// unstoredNotifyEvery bounds how often the orders the storage fails to write,
// and the ones it writes at last, are notified: the new failures meanwhile are
// notified together, that late at most.
var unstoredNotifyEvery = time.Minute

func NewController(ctx context.Context, exchange service.Exchange, storage storage.Storage,
	orderFeed *Feed) *Controller {

	c := &Controller{
		ctx:            ctx,
		storage:        storage,
		exchange:       exchange,
		orderFeed:      orderFeed,
		lastPrice:      make(map[string]float64),
		Results:        make(map[string]*summary),
		tickerInterval: time.Second,
		finish:         make(chan bool),
		position:       make(map[string]*Position),
		pending:        make(map[int64]model.Order),
		unstored:       make(map[int64]model.Order),
	}
	c.restore()
	return c
}

// restore rebuilds the positions and the results of the pairs from the filled
// orders of the storage, replayed in the order they filled, as they stood
// before a restart: a stop loss or a take profit filling after it then closes
// the position it protects, rather than opening one on the other side that
// the next trades would settle against. The profits computed and missing from
// the storage are stored.
func (c *Controller) restore() {
	if c.storage == nil {
		return
	}
	orders, err := c.storage.Orders(storage.WithStatus(model.OrderStatusTypeFilled))
	if err != nil {
		log.Errorf("order controller: the stored orders: %v", err)
		return
	}
	if len(orders) == 0 {
		return
	}
	sort.SliceStable(orders, func(i, j int) bool {
		if a, b := filledAt(orders[i]), filledAt(orders[j]); !a.Equal(b) {
			return a.Before(b)
		}
		return orders[i].ID < orders[j].ID
	})

	c.restoring = true
	defer func() { c.restoring = false }()
	for _, order := range orders {
		profit, value := order.Profit, order.ProfitValue
		c.processTrade(order)
		if order.Profit != profit || order.ProfitValue != value {
			c.store(order)
		}
	}
	open := make([]string, 0, len(c.position))
	for pair, position := range c.position {
		open = append(open, fmt.Sprintf("%s %s %g at %g", pair, position.Side, position.Quantity, position.AvgPrice))
	}
	sort.Strings(open)
	log.Infof("[SETUP] %d filled orders of the storage replayed, the positions open: %v", len(orders), open)
}

func (c *Controller) SetNotifier(notifier service.Notifier) {
	c.notifier = notifier
}

func (c *Controller) OnCandle(candle model.Candle) {
	c.lastPrice[candle.Pair] = candle.Close
}

func (c *Controller) updatePosition(o *model.Order) {
	// get filled orders before the current order
	position, ok := c.position[o.Pair]
	if !ok {
		c.position[o.Pair] = &Position{
			AvgPrice:  o.Price,
			Quantity:  o.Quantity,
			Fee:       o.Fee,
			CreatedAt: filledAt(o),
			Side:      o.Side,
			Entry:     *o,
		}
		return
	}

	entry := position.Entry // before a reversal replaces it
	result, closed := position.Update(o)
	if closed {
		delete(c.position, o.Pair)
	}

	if result != nil {
		// the profit belongs to the order, stored with it: the history of a
		// restart shows it
		if !c.restoring && c.storage != nil {
			c.store(o)
		}

		// TODO: replace by a slice of Result
		summary := c.Results[o.Pair]
		summary.invalidateCache() // Invalidate cache when modifying
		if result.ProfitPercent >= 0 {
			if result.Side == model.SideTypeBuy {
				summary.WinLong = append(summary.WinLong, result.ProfitValue)
				summary.WinLongPercent = append(summary.WinLongPercent, result.ProfitPercent)
			} else {
				summary.WinShort = append(summary.WinShort, result.ProfitValue)
				summary.WinShortPercent = append(summary.WinShortPercent, result.ProfitPercent)
			}
		} else {
			if result.Side == model.SideTypeBuy {
				summary.LoseLong = append(summary.LoseLong, result.ProfitValue)
				summary.LoseLongPercent = append(summary.LoseLongPercent, result.ProfitPercent)
			} else {
				summary.LoseShort = append(summary.LoseShort, result.ProfitValue)
				summary.LoseShortPercent = append(summary.LoseShortPercent, result.ProfitPercent)
			}
		}

		if c.restoring {
			return
		}
		_, quote := exchange.SplitAssetQuote(o.Pair)
		message := fmt.Sprintf("[PROFIT] %f %s (%f %%)\n`%s`", result.ProfitValue, quote, result.ProfitPercent*100,
			c.Results[o.Pair].String())
		reporter, ok := c.exchange.(service.AccountReporter)
		if !ok {
			c.notify(message)
			return
		}
		// as the account books it, in its currency: its deals may come a
		// moment after the fill, so the notification waits for them apart
		closing, percent, table := *o, result.ProfitPercent, c.Results[o.Pair].String()
		go func() {
			for wait := time.Now().Add(profitWait); ; {
				if booked, ok := reporter.ClosedProfit(closing, entry); ok {
					if account, ok := reporter.AccountSummary(); ok && account.Currency != "" {
						message = fmt.Sprintf("[PROFIT] %.2f %s (%.4f %%)\n`%s`", booked, account.Currency,
							percent*100, table)
					}
					break
				}
				if time.Now().After(wait) {
					break // the one computed from the prices, in the quote
				}
				time.Sleep(profitPoll)
			}
			c.notify(message)
		}()
	}
}

// profitWait bounds how long the notification of a closed trade waits for the
// profit the broker books, polled every profitPoll.
var (
	profitWait = 5 * time.Second
	profitPoll = 250 * time.Millisecond
)

// AccountSummary returns the account as the broker reports it, in its
// currency, when it does (see service.AccountReporter): for the display.
func (c *Controller) AccountSummary() (service.AccountSummary, bool) {
	reporter, ok := c.exchange.(service.AccountReporter)
	if !ok {
		return service.AccountSummary{}, false
	}
	return reporter.AccountSummary()
}

func (c *Controller) notify(message string) {
	log.Info(message)
	if c.notifier != nil {
		c.notifier.Notify(message)
	}
}

func (c *Controller) notifyError(err error) {
	log.Error(err)
	if c.notifier != nil {
		c.notifier.OnError(err)
	}
}

// create stores an order the exchange created. The order exists on the
// exchange: a failure of the storage never loses it, see notStored. Must be
// called while holding c.mtx.
func (c *Controller) create(order *model.Order) {
	err := c.storage.CreateOrder(order)
	if err == nil {
		return
	}
	if order.ID == 0 {
		c.provisional--
		order.ID = c.provisional
	}
	c.notStored(*order, err)
}

// store writes an order changed on the exchange, see create. Must be called
// while holding c.mtx.
func (c *Controller) store(order *model.Order) {
	if order.ID < 0 {
		return // never numbered by the storage
	}
	if err := c.storage.UpdateOrder(order); err != nil {
		c.notStored(*order, err)
		return
	}
	delete(c.unstored, order.ID) // its last state written
}

// notStored keeps an order the storage failed to write, to write it again (see
// storeAgain), and notifies of the failure: the order is followed all the
// same, its fills settled and published. The failure of an order already kept
// is logged only; the ones of new orders are notified, unstoredNotifyEvery
// apart at most, the ones meanwhile with the next notice (see
// notifyUnstored), so that an order the storage refuses for good never hides
// the failures of the others. Must be called while holding c.mtx.
func (c *Controller) notStored(order model.Order, err error) {
	if order.ID < 0 {
		c.failedToStore(fmt.Errorf("order controller: %s executed on the exchange (%d) but not stored, the "+
			"storage failing to number it: followed in memory only, lost by a restart: %w", order,
			order.ExchangeID, err), true)
		return
	}
	_, kept := c.unstored[order.ID]
	c.unstored[order.ID] = order
	if kept {
		log.Errorf("order controller: %s executed on the exchange (%d) not stored yet: %v", order,
			order.ExchangeID, err)
		return
	}
	c.failedToStore(fmt.Errorf("order controller: %s executed on the exchange (%d) but not stored: followed all "+
		"the same, stored again until it is: %w", order, order.ExchangeID, err), false)
}

// failedToStore notifies err, the failure of a new order to be stored, never
// numbered when unnumbered, unless another one was notified less than
// unstoredNotifyEvery ago: then it is logged, and notified with the next
// notice. Must be called while holding c.mtx.
func (c *Controller) failedToStore(err error, unnumbered bool) {
	c.unnotified++
	if unnumbered {
		c.unnumbered++
	}
	c.lastFailed = err
	if !c.notified.IsZero() && time.Since(c.notified) < unstoredNotifyEvery {
		log.Error(err)
		return
	}
	c.notifyUnstored()
}

// notifyUnstored notifies the failures of the new orders to be stored since
// the last notice, the last one with how many others failed. Must be called
// while holding c.mtx.
func (c *Controller) notifyUnstored() {
	err := c.lastFailed
	if others := c.unnotified - 1; others > 0 {
		err = fmt.Errorf("%w (and %d other orders since the last notice, %d not stored yet in all)", err, others,
			len(c.unstored))
	}
	c.notified, c.unnotified, c.unnumbered, c.lastFailed = time.Now(), 0, 0, nil
	c.notifyError(err)
}

// storeAgain writes again the orders the storage failed to write, in their
// last state, see unstored: UpdateOrder writes an order under its id, as the
// storages do. The ones stored at last are notified: at once when none is left,
// else unstoredNotifyEvery apart at most; so are the failures of new orders
// waiting for their notice, see failedToStore.
func (c *Controller) storeAgain() {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.unnotified > 0 && time.Since(c.notified) >= unstoredNotifyEvery {
		c.notifyUnstored()
	}
	if len(c.unstored) == 0 {
		return
	}
	ids := make([]int64, 0, len(c.unstored))
	for id := range c.unstored {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var failed error
	for _, id := range ids {
		order := c.unstored[id]
		if err := c.storage.UpdateOrder(&order); err != nil {
			failed = err
			continue // one refused for good never holds the others
		}
		delete(c.unstored, id)
		c.storedLate++
		log.Infof("order controller: the order %d stored at last", id)
	}
	switch {
	case len(c.unstored) == 0:
		c.notify(fmt.Sprintf("✅ order controller: the %d orders the storage failed to write are stored at last",
			c.storedLate))
		c.storedLate, c.recovered = 0, time.Now()
		// the failures not notified yet, of the orders stored now, told by this
		// notice; the next one, of another interruption, notified at once
		c.notified = time.Time{}
		if c.unnumbered == 0 {
			c.unnotified, c.lastFailed = 0, nil
		}
	case c.storedLate > 0 && time.Since(c.recovered) >= unstoredNotifyEvery:
		c.notify(fmt.Sprintf("✅ order controller: %d orders the storage failed to write are stored at last, %d "+
			"not yet: %v", c.storedLate, len(c.unstored), failed))
		c.storedLate, c.recovered = 0, time.Now()
	}
	if len(c.unstored) > 0 && time.Since(c.warned) >= unstoredWarnEvery {
		c.warned = time.Now()
		log.Errorf("order controller: %d orders executed on the exchange not stored yet, followed in memory: %v",
			len(c.unstored), failed)
	}
}

func (c *Controller) processTrade(order *model.Order) {
	if order.Status != model.OrderStatusTypeFilled {
		return
	}

	// initializer results map if needed
	if _, ok := c.Results[order.Pair]; !ok {
		c.Results[order.Pair] = &summary{Pair: order.Pair}
	}

	// register order volume
	c.Results[order.Pair].Volume += order.Price * order.Quantity

	// update position size / avg price
	c.updatePosition(order)
}

// isOpen reports whether an order still waits for an update from the exchange.
func isOpen(status model.OrderStatusType) bool {
	switch status {
	case model.OrderStatusTypeNew, model.OrderStatusTypePartiallyFilled, model.OrderStatusTypePendingCancel:
		return true
	}
	return false
}

// track registers an order created by the controller, so that UpdateOrders
// follows it until the exchange reports it filled or canceled.
// Must be called while holding c.mtx.
func (c *Controller) track(order model.Order) {
	if isOpen(order.Status) {
		c.pending[order.ID] = order
	}
}

// loadPendingOrders recovers the open orders left in storage by a previous
// run, so that their fills are still accounted after a restart.
func (c *Controller) loadPendingOrders() {
	orders, err := c.storage.Orders(storage.WithStatusIn(
		model.OrderStatusTypeNew,
		model.OrderStatusTypePartiallyFilled,
		model.OrderStatusTypePendingCancel,
	))
	if err != nil {
		c.notifyError(err)
		return
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	for _, order := range orders {
		c.pending[order.ID] = *order
	}
}

// UpdateOrders fetches the status of the pending orders from the exchange,
// persists the ones that changed and settles their fills: the position, the
// profit of the trade and the order feed are updated here.
//
// A started controller runs it every second on its own. Backtests replay years
// of candles in a few seconds, so they must call it after every candle to
// account the fills in the order they happen; it is cheap when nothing is
// pending.
func (c *Controller) UpdateOrders() {
	c.updateMtx.Lock()
	defer c.updateMtx.Unlock()
	if c.updatesClosed {
		return
	}
	c.updateOrders()
}

// updateOrders is UpdateOrders. Must be called while holding c.updateMtx.
func (c *Controller) updateOrders() {
	c.storeAgain()

	c.mtx.RLock()
	orders := make([]model.Order, 0, len(c.pending))
	for _, order := range c.pending {
		orders = append(orders, order)
	}
	c.mtx.RUnlock()

	if len(orders) == 0 {
		return
	}

	// settle in creation order, so that fills of the same tick are applied
	// to the position deterministically
	sort.Slice(orders, func(i, j int) bool { return orders[i].ID < orders[j].ID })

	// For each pending order, check for updates (no lock needed for reads)
	var updatedOrders []model.Order
	for _, order := range orders {
		excOrder, err := c.exchangeOrder(order)
		if err != nil {
			log.WithField("id", order.ExchangeID).Error("orderControler/get: ", err)
			continue
		}

		// no status change
		if excOrder.Status == order.Status {
			continue
		}

		keepStored(&excOrder, order)
		log.Infof("[ORDER %s] %s", excOrder.Status, excOrder)
		updatedOrders = append(updatedOrders, excOrder)
	}

	if len(updatedOrders) == 0 {
		return
	}

	// Lock only when updating internal state: the change happened on the
	// exchange, settled even when the storage fails to write it
	c.mtx.Lock()
	for _, processOrder := range updatedOrders {
		c.store(&processOrder)
		if isOpen(processOrder.Status) {
			c.pending[processOrder.ID] = processOrder
		} else {
			delete(c.pending, processOrder.ID)
		}
		c.processTrade(&processOrder)
		c.orderFeed.Publish(processOrder, false)
	}
	c.mtx.Unlock()
}

// keepStored gives an order reported by the exchange the storage id of the
// stored one, and the fields the exchange does not report, eg: for the orders
// of a previous run rebuilt from a history.
func keepStored(excOrder *model.Order, stored model.Order) {
	excOrder.ID = stored.ID
	if excOrder.CreatedAt.IsZero() {
		excOrder.CreatedAt = stored.CreatedAt
	}
	if excOrder.Type == "" {
		excOrder.Type = stored.Type
	}
	if excOrder.Stop == nil {
		excOrder.Stop = stored.Stop
	}
	if excOrder.GroupID == nil {
		excOrder.GroupID = stored.GroupID
	}
}

// exchangeOrder fetches the state of a stored order from the exchange: from its
// creation on, for the exchanges reading their history by periods.
func (c *Controller) exchangeOrder(order model.Order) (model.Order, error) {
	if history, ok := c.exchange.(service.OrderHistory); ok {
		return history.OrderSince(order.Pair, order.ExchangeID, order.CreatedAt)
	}
	return c.exchange.Order(order.Pair, order.ExchangeID)
}

func (c *Controller) Status() Status {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	return c.status
}

// requireRunning rejects new orders only after an explicit Stop().
// Controllers that never called Start() keep the zero-value status and still
// accept orders, preserving the public API for direct embedding / tests.
// Must be called while holding c.mtx.
func (c *Controller) requireRunning() error {
	if c.status == StatusStopped {
		return ErrBotStopped
	}
	return nil
}

// lockPair serializes the orders of pair, each one from its call to the
// exchange to its recording, returning the unlock: c.mtx is never held during
// a call to the exchange, which may be slow, eg: an MT5 trade request whose
// answer was lost, looked for in the account.
func (c *Controller) lockPair(pair string) func() {
	c.pairsMtx.Lock()
	if c.pairs == nil {
		c.pairs = make(map[string]*sync.Mutex)
	}
	mtx, ok := c.pairs[pair]
	if !ok {
		mtx = new(sync.Mutex)
		c.pairs[pair] = mtx
	}
	c.pairsMtx.Unlock()
	mtx.Lock()
	return mtx.Unlock
}

// beginOrder begins an order of pair, see begin, refused on a stopped bot
// (ErrBotStopped). It returns the end of the order.
func (c *Controller) beginOrder(pair string) (func(), error) {
	return c.begin(pair, true)
}

// beginProtection begins a call protecting a position of pair, or cleaning
// its orders up (see Protect, UpdateProtection and Cancel), see begin: taken
// on a stopped bot, refused only once the controller is closed. It returns
// the end of the call.
func (c *Controller) beginProtection(pair string) (func(), error) {
	return c.begin(pair, false)
}

// begin begins a call to the exchange about pair, an order when order, under
// the lock of the pair (see lockPair), refused once the controller is closed
// (see Close), and an order on a stopped bot: at once, without waiting for the
// lock of the pair, and again once it is taken. Stop and Close wait for the
// calls begun to end. It returns the end of the call.
func (c *Controller) begin(pair string, order bool) (func(), error) {
	refused := func() error {
		switch {
		case c.closed:
			return errClosed
		case order:
			return c.requireRunning()
		}
		return nil
	}
	c.mtx.RLock()
	err := refused()
	c.mtx.RUnlock()
	if err != nil {
		return nil, err
	}
	unlock := c.lockPair(pair)
	c.mtx.Lock()
	if err := refused(); err != nil {
		c.mtx.Unlock()
		unlock()
		return nil, err
	}
	c.inflight++
	c.mtx.Unlock()
	return func() {
		c.mtx.Lock()
		if c.inflight--; c.inflight == 0 {
			c.idleCond().Broadcast()
		}
		c.mtx.Unlock()
		unlock()
	}, nil
}

// idleCond returns the condition the orders begun ending signal, see
// begin. Must be called while holding c.mtx.
func (c *Controller) idleCond() *sync.Cond {
	if c.idle == nil {
		c.idle = sync.NewCond(&c.mtx)
	}
	return c.idle
}

func (c *Controller) Start() {
	c.mtx.Lock()
	if c.status == StatusRunning {
		c.mtx.Unlock()
		return
	}
	if c.closed {
		c.mtx.Unlock()
		log.Warn("order controller: closed as the bot exits, never started again")
		return
	}
	c.status = StatusRunning
	c.mtx.Unlock()

	c.loadPendingOrders()

	go func() {
		ticker := time.NewTicker(c.tickerInterval)
		for {
			select {
			case <-ticker.C:
				c.UpdateOrders()
			case <-c.finish:
				ticker.Stop()
				return
			}
		}
	}()
	log.Info("Bot started.")
}

// Stop stops the controller: no order begins any more (ErrBotStopped), and
// once the calls begun are recorded, the pending orders are updated a last time.
func (c *Controller) Stop() {
	c.mtx.Lock()
	if c.status != StatusRunning {
		c.mtx.Unlock()
		return
	}
	c.status = StatusStopped
	for c.inflight > 0 {
		c.idleCond().Wait()
	}
	c.mtx.Unlock()

	c.UpdateOrders()
	c.finish <- true
	log.Info("Bot stopped.")
}

// Close stops the controller for good, as the bot exits (see XTrader.Run): it
// stops (see Stop), then nothing reaches the exchange any more, the
// protections of the positions, their moves and the cancels refused too
// (ErrBotStopped), once the calls begun are recorded; the pending orders are
// updated a last time, and never after. What the exchange did is then all
// handed to the storage, eg: an outbox about to be closed. Start never starts
// it again.
func (c *Controller) Close() {
	c.Stop()
	c.mtx.Lock()
	if c.closed {
		c.mtx.Unlock()
		return
	}
	c.closed, c.status = true, StatusStopped
	for c.inflight > 0 {
		c.idleCond().Wait()
	}
	c.mtx.Unlock()

	c.updateMtx.Lock()
	defer c.updateMtx.Unlock()
	c.updateOrders()
	c.updatesClosed = true
}

func (c *Controller) Account() (model.Account, error) {
	return c.exchange.Account()
}

func (c *Controller) Position(pair string) (asset, quote float64, err error) {
	return c.exchange.Position(pair)
}

func (c *Controller) LastQuote(pair string) (float64, error) {
	return c.exchange.LastQuote(c.ctx, pair)
}

func (c *Controller) PositionValue(pair string) (float64, error) {
	asset, _, err := c.exchange.Position(pair)
	if err != nil {
		return 0, err
	}
	c.mtx.RLock()
	price := c.lastPrice[pair]
	c.mtx.RUnlock()
	return asset * price, nil
}

func (c *Controller) Order(pair string, id int64) (model.Order, error) {
	return c.exchange.Order(pair, id)
}

func (c *Controller) CreateOrderOCO(side model.SideType, pair string, size, price, stop,
	stopLimit float64) ([]model.Order, error) {
	end, err := c.beginOrder(pair)
	if err != nil {
		return nil, err
	}
	defer end()

	log.Infof("[ORDER] Creating OCO order for %s", pair)
	orders, err := c.exchange.CreateOrderOCO(side, pair, size, price, stop, stopLimit)
	if err != nil {
		c.notifyError(err)
		return nil, err
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	for i := range orders {
		c.create(&orders[i])
		c.track(orders[i])
		go c.orderFeed.Publish(orders[i], true)
	}

	return orders, nil
}

// CreateOrderMarketProtected opens a position at the market with its stop loss
// and take profit, placed with the order when the exchange supports it, see
// service.ProtectionBroker. The entry and the legs are stored and followed as
// any other order.
func (c *Controller) CreateOrderMarketProtected(side model.SideType, pair string, size, stop,
	profit float64) ([]model.Order, error) {

	return c.createProtected("MARKET", side, pair, func(broker service.ProtectionBroker) ([]model.Order, error) {
		return broker.CreateOrderMarketProtected(side, pair, size, stop, profit)
	})
}

// CreateOrderLimitProtected places a limit order opening a position with its
// stop loss and take profit, see CreateOrderMarketProtected.
func (c *Controller) CreateOrderLimitProtected(side model.SideType, pair string, size, limit, stop,
	profit float64) ([]model.Order, error) {

	return c.createProtected("LIMIT", side, pair, func(broker service.ProtectionBroker) ([]model.Order, error) {
		return broker.CreateOrderLimitProtected(side, pair, size, limit, stop, profit)
	})
}

// CreateOrderStopProtected places a stop order opening a position with its stop
// loss and take profit, see CreateOrderMarketProtected.
func (c *Controller) CreateOrderStopProtected(side model.SideType, pair string, size, level, stop,
	profit float64) ([]model.Order, error) {

	return c.createProtected("STOP", side, pair, func(broker service.ProtectionBroker) ([]model.Order, error) {
		return broker.CreateOrderStopProtected(side, pair, size, level, stop, profit)
	})
}

// createProtected creates a protected entry on the exchange and stores its
// orders, the entry and its legs. An entry filled but left without protection
// is stored too, with the error.
func (c *Controller) createProtected(kind string, side model.SideType, pair string,
	create func(service.ProtectionBroker) ([]model.Order, error)) ([]model.Order, error) {

	end, err := c.beginOrder(pair)
	if err != nil {
		return nil, err
	}
	defer end()

	broker, ok := c.exchange.(service.ProtectionBroker)
	if !ok {
		err := fmt.Errorf("protected orders: %w", exchange.ErrNotSupported)
		c.notifyError(err)
		return nil, err
	}

	log.Infof("[ORDER] Creating protected %s %s order for %s", kind, side, pair)
	orders, err := create(broker)
	c.keepCreated(orders)
	if err != nil {
		c.notifyError(err)
		return orders, err
	}
	return orders, nil
}

// keepCreated stores the orders created on the exchange and follows them as any
// other order, stored or not, see create.
func (c *Controller) keepCreated(orders []model.Order) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	for i := range orders {
		c.create(&orders[i])
		c.processTrade(&orders[i])
		c.track(orders[i])
		go c.orderFeed.Publish(orders[i], true)
		log.Infof("[ORDER CREATED] %s", orders[i])
	}
}

// ClosesProtected reports whether a market order of the exchange closes a
// protected position with its legs in place, see service.ProtectedCloser.
func (c *Controller) ClosesProtected() bool {
	closer, ok := c.exchange.(service.ProtectedCloser)
	return ok && closer.ClosesProtected()
}

// HeldLegs returns the open legs of the position held on side of pair, and when
// it opened, zero when unknown, see service.PositionLegs: from the orders the
// controller stores, in the state it follows them, a restart included (the
// positions replayed from the stored fills, see restore). A leg is an open
// order of a group on the other side, protecting the entry of the position:
// as the exchange tells it, when it knows both (see fillPositions), else
// placed after it, never one of an older entry, eg: of a position closed by
// its stop loss whose exit isn't booked yet. Any, with the entry unknown.
func (c *Controller) HeldLegs(pair string, side model.SideType) ([]model.Order, time.Time, error) {
	open := make(map[int64]model.Order)
	if c.storage != nil {
		stored, err := c.storage.Orders(storage.WithPair(pair), storage.WithStatusIn(model.OrderStatusTypeNew,
			model.OrderStatusTypePartiallyFilled))
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("the open orders of %s: %w", pair, err)
		}
		for _, order := range stored {
			open[order.ID] = *order
		}
	}

	c.mtx.RLock()
	// the state followed is newer than the stored one, or the only one
	for _, orders := range []map[int64]model.Order{c.unstored, c.pending} {
		for id, order := range orders {
			if order.Pair == pair {
				open[id] = order
			}
		}
	}
	var entry *model.Order
	var opened time.Time
	if position, ok := c.position[pair]; ok && position.Side == side {
		held := position.Entry
		entry, opened = &held, position.CreatedAt
	}
	c.mtx.RUnlock()

	var legs []model.Order
	for _, order := range open {
		if order.GroupID == nil || order.Side == side || order.Status != model.OrderStatusTypeNew &&
			order.Status != model.OrderStatusTypePartiallyFilled {
			continue
		}
		if entry != nil && !c.legOf(order, *entry) {
			continue
		}
		legs = append(legs, order)
	}
	sort.Slice(legs, func(i, j int) bool { return legs[i].ID < legs[j].ID })
	return legs, opened, nil
}

// fillPositions is implemented by the exchanges that tell the positions a fill
// opened and closed, eg: MT5, by their tickets.
type fillPositions interface {
	FillPositions(fill model.Order) (opened int64, closed []int64, ok bool)
}

// legOf reports whether leg protects the position entry opened, see HeldLegs.
func (c *Controller) legOf(leg, entry model.Order) bool {
	if positions, ok := c.exchange.(fillPositions); ok {
		opened, _, known := positions.FillPositions(entry)
		_, protected, legKnown := positions.FillPositions(leg)
		if known && opened != 0 && legKnown {
			return slices.Contains(protected, opened)
		}
	}
	if leg.ID > 0 && entry.ID > 0 {
		return leg.ID > entry.ID
	}
	return !leg.CreatedAt.Before(entry.CreatedAt)
}

// Protect protects the position held on side of pair with a stop loss and a
// take profit, see service.PositionProtector, and stores the legs, followed as
// any other order. Like UpdateProtection, it works on a stopped bot too: it
// only protects a position; not once the controller is closed (see Close).
func (c *Controller) Protect(side model.SideType, pair string, size, stop, profit float64) ([]model.Order, error) {
	end, err := c.beginProtection(pair)
	if err != nil {
		return nil, err
	}
	defer end()

	protector, ok := c.exchange.(service.PositionProtector)
	if !ok {
		err := fmt.Errorf("protecting a position held: %w", exchange.ErrNotSupported)
		c.notifyError(err)
		return nil, err
	}

	log.Infof("[ORDER] Protecting the %s position of %s with a stop loss at %f and a take profit at %f", side,
		pair, stop, profit)
	legs, err := protector.Protect(side, pair, size, stop, profit)
	c.keepCreated(legs)
	if err != nil {
		c.notifyError(err)
		return legs, err
	}
	return legs, nil
}

// UpdateProtection moves a stop loss or take profit leg to level, see
// service.ProtectionBroker, and stores it moved. Like Cancel, it works on a
// stopped bot too: it only protects a position; not once the controller is
// closed (see Close).
func (c *Controller) UpdateProtection(leg model.Order, level float64) (model.Order, error) {
	end, err := c.beginProtection(leg.Pair)
	if err != nil {
		return model.Order{}, err
	}
	defer end()

	broker, ok := c.exchange.(service.ProtectionBroker)
	if !ok {
		err := fmt.Errorf("protected orders: %w", exchange.ErrNotSupported)
		c.notifyError(err)
		return model.Order{}, err
	}

	log.Infof("[ORDER] Moving the %s of %s to %f", leg.Type, leg.Pair, level)
	moved, err := broker.UpdateProtection(leg, level)
	if err != nil {
		c.notifyError(err)
		return model.Order{}, err
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	keepStored(&moved, leg)
	c.store(&moved)
	if _, ok := c.pending[moved.ID]; ok {
		c.pending[moved.ID] = moved
	}
	go c.orderFeed.Publish(moved, false)
	log.Infof("[ORDER UPDATED] %s", moved)
	return moved, nil
}

// UpdateLimit moves a pending limit order to limit, see
// service.ProtectionBroker, and stores it moved. Unlike UpdateProtection, it
// needs the bot running: it changes when a position opens.
func (c *Controller) UpdateLimit(order model.Order, limit float64) (model.Order, error) {
	end, err := c.beginOrder(order.Pair)
	if err != nil {
		return model.Order{}, err
	}
	defer end()

	broker, ok := c.exchange.(service.ProtectionBroker)
	if !ok {
		err := fmt.Errorf("moving limit orders: %w", exchange.ErrNotSupported)
		c.notifyError(err)
		return model.Order{}, err
	}

	log.Infof("[ORDER] Moving the %s %s order of %s to %f", order.Type, order.Side, order.Pair, limit)
	moved, err := broker.UpdateLimit(order, limit)
	if err != nil {
		c.notifyError(err)
		return model.Order{}, err
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	keepStored(&moved, order)
	c.store(&moved)
	if _, ok := c.pending[moved.ID]; ok {
		c.pending[moved.ID] = moved
	}
	go c.orderFeed.Publish(moved, false)
	log.Infof("[ORDER UPDATED] %s", moved)
	return moved, nil
}

func (c *Controller) CreateOrderLimit(side model.SideType, pair string, size, limit float64) (model.Order, error) {
	end, err := c.beginOrder(pair)
	if err != nil {
		return model.Order{}, err
	}
	defer end()

	log.Infof("[ORDER] Creating LIMIT %s order for %s", side, pair)
	order, err := c.exchange.CreateOrderLimit(side, pair, size, limit)
	if err != nil {
		c.notifyError(err)
		return model.Order{}, err
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.create(&order)
	c.track(order)
	go c.orderFeed.Publish(order, true)
	log.Infof("[ORDER CREATED] %s", order)
	return order, nil
}

func (c *Controller) CreateOrderMarketQuote(side model.SideType, pair string, amount float64) (model.Order, error) {
	end, err := c.beginOrder(pair)
	if err != nil {
		return model.Order{}, err
	}
	defer end()

	log.Infof("[ORDER] Creating MARKET %s order for %s", side, pair)
	order, err := c.exchange.CreateOrderMarketQuote(side, pair, amount)
	if err != nil {
		c.notifyError(err)
		return model.Order{}, err
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.create(&order)

	// calculate profit
	c.processTrade(&order)
	c.track(order) // exchanges may report a market order as new and fill it right after
	go c.orderFeed.Publish(order, true)
	log.Infof("[ORDER CREATED] %s", order)
	return order, nil
}

func (c *Controller) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	end, err := c.beginOrder(pair)
	if err != nil {
		return model.Order{}, err
	}
	defer end()

	log.Infof("[ORDER] Creating MARKET %s order for %s", side, pair)
	order, err := c.exchange.CreateOrderMarket(side, pair, size)
	if err != nil {
		c.notifyError(err)
		return model.Order{}, err
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.create(&order)

	// calculate profit
	c.processTrade(&order)
	c.track(order) // exchanges may report a market order as new and fill it right after
	go c.orderFeed.Publish(order, true)
	log.Infof("[ORDER CREATED] %s", order)
	return order, nil
}

func (c *Controller) CreateOrderStop(pair string, size float64, limit float64) (model.Order, error) {
	end, err := c.beginOrder(pair)
	if err != nil {
		return model.Order{}, err
	}
	defer end()

	log.Infof("[ORDER] Creating STOP order for %s", pair)
	order, err := c.exchange.CreateOrderStop(pair, size, limit)
	if err != nil {
		c.notifyError(err)
		return model.Order{}, err
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.create(&order)
	c.track(order)
	go c.orderFeed.Publish(order, true)
	log.Infof("[ORDER CREATED] %s", order)
	return order, nil
}

// Cancel cancels an order, on a stopped bot too, not once the controller is
// closed (see Close).
func (c *Controller) Cancel(order model.Order) error {
	end, err := c.beginProtection(order.Pair)
	if err != nil {
		return err
	}
	defer end()

	log.Infof("[ORDER] Cancelling order for %s", order.Pair)
	if err := c.exchange.Cancel(order); err != nil {
		return err
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	order.Status = model.OrderStatusTypePendingCancel
	c.store(&order)
	if _, ok := c.pending[order.ID]; ok {
		c.pending[order.ID] = order
	}
	log.Infof("[ORDER CANCELED] %s", order)
	return nil
}
