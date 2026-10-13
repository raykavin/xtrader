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

	unpriced bool // the profit unknown, 0: the fill or the position had no price, see Position.Update
}

type Position struct {
	Side model.SideType
	// AvgPrice is the average price of the position, 0 when unknown: a fill
	// of it had no price, see Position.Update.
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

// priced reports whether price is a price: positive and finite.
func priced(price float64) bool {
	return price > 0 && !math.IsInf(price, 1)
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
	if !priced(price) {
		price = 0 // unknown, eg: a fill reported without its price
	}

	if p.Side == order.Side {
		if priced(p.AvgPrice) && price > 0 && p.Quantity+order.Quantity > 0 {
			p.AvgPrice = (p.AvgPrice*p.Quantity + price*order.Quantity) / (p.Quantity + order.Quantity)
		} else {
			p.AvgPrice = 0 // unknown: never an average made of a price of 0
		}
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

	// The profit needs the price of the fill and the one of the position: one
	// unknown, the profit is (0), rather than a NaN or an infinity, which JSON
	// can't write, or a loss of the whole position made of a price of 0.
	unpriced := price == 0 || !priced(p.AvgPrice)
	if unpriced {
		order.Profit, order.ProfitValue = 0, 0
		log.Warnf("[ORDER] %s: the fill or the position it closes (%s %g at %g) without a price: its profit unknown",
			order, p.Side, p.Quantity, p.AvgPrice)
	} else {
		order.Profit = profitPrice / p.AvgPrice
		order.ProfitValue = profitPrice * closedQuantity
		if fee := entryFee + exitFee; fee > 0 {
			order.ProfitValue -= fee
			order.Profit = order.ProfitValue / (p.AvgPrice * closedQuantity)
		}
	}

	closedAt := filledAt(order)
	result = &Result{
		CreatedAt:     closedAt,
		Pair:          order.Pair,
		Duration:      closedAt.Sub(p.CreatedAt),
		ProfitPercent: order.Profit,
		ProfitValue:   order.ProfitValue,
		Side:          p.Side,
		unpriced:      unpriced,
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
//
// The states of an order are recorded under mtx, never one older than the one
// recorded: an end (filled, canceled, rejected, expired) is never undone by a
// move or a cancel answered after it (see record), nor a move by a state
// UpdateOrders read before it (see updateOrders); the order feed takes them in
// the order they are recorded (see publish).
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
	Results        map[string]*summary // under mtx while the bot runs, see Summaries
	lastPrice      map[string]float64
	tickerInterval time.Duration
	finish         chan bool
	status         Status

	position map[string]*Position
	// openings holds when the fills of each pair opened a position, or
	// reversed one, the stored ones replayed by restore included, the last
	// openingsKept of them, see PositionsOpened and PositionsReplayed.
	openings map[string][]opening

	// pending holds the orders waiting for an update from the exchange, keyed
	// by their storage id: the ones created by this controller plus, once
	// started, the open orders left in storage by a previous run.
	pending map[int64]model.Order
	// moves numbers the changes of the pending orders recorded apart from
	// UpdateOrders, by a move or a cancel (see record), and movedAt holds the
	// number of the last one of each pending order changed: the state of an
	// order UpdateOrders read before it is read again, see updateOrders.
	moves   uint64
	movedAt map[int64]uint64

	// the orders handed to the order feed, by pair, in the order they are
	// recorded, see publish
	feedMtx    sync.Mutex
	feedQueues map[string]*feedQueue

	// restoring is set while restore replays the stored fills: they settle
	// the positions and the results quietly, notified already.
	restoring bool
	// unheld holds the pairs whose position replayed from the storage the
	// exchange doesn't hold, kept for the open orders that may have closed
	// it, until they end, see reconcile.
	unheld map[string]bool
	// heldChecks holds the pairs whose position to check against the one the
	// exchange holds, an order that may have closed it ended without a fill
	// as an order of the pair was in flight, see heldEnded.
	heldChecks map[string]bool

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
		openings:       make(map[string][]opening),
		pending:        make(map[int64]model.Order),
		movedAt:        make(map[int64]uint64),
		unstored:       make(map[int64]model.Order),
		unheld:         make(map[string]bool),
		heldChecks:     make(map[string]bool),
	}
	c.restore()
	return c
}

// restore rebuilds the positions and the results of the pairs from the filled
// orders of the storage, replayed in the order they filled, as they stood
// before a restart: a stop loss or a take profit filling after it then closes
// the position it protects, rather than opening one on the other side that
// the next trades would settle against. The profits computed and missing from
// the storage are stored. The positions are then checked against the ones the
// exchange holds, when it tells them, see reconcile.
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
		open = append(open, fmt.Sprintf("%s %s", pair, position))
	}
	sort.Strings(open)
	log.Infof("[SETUP] %d filled orders of the storage replayed, the positions open: %v", len(orders), open)
	c.reconcile()
}

// String describes the position, eg: BUY 0.1 at 4250.5.
func (p *Position) String() string {
	return fmt.Sprintf("%s %g at %g", p.Side, p.Quantity, p.AvgPrice)
}

// reconcile checks the positions replayed from the storage (see restore)
// against the ones the exchange reports the bot holds, when it does (see
// service.PositionReporter), without sending any order: a position closed
// outside the bot, eg: by hand in the terminal or by a stop out, leaves no
// fill in the storage, and the trades of its pair would be settled against
// it, their profits and results wrong (the strategies read the position from
// the exchange). One the exchange doesn't hold is discarded, unless an open
// order on its other side may have closed it, eg: its stop loss, filled while
// the bot was off, which closes it with its profit: kept until they end (see
// unheldEnded). One the exchange holds otherwise, eg: of another size, is
// kept, an error logged: what differs is unknown; a warning only when the
// more held is explained by open orders of the storage on its side, eg: a
// pending entry filled while the bot was off, whose fill comes next.
func (c *Controller) reconcile() {
	reporter, ok := c.exchange.(service.PositionReporter)
	if !ok {
		return
	}
	pairs := make([]string, 0, len(c.Results))
	for pair := range c.Results {
		pairs = append(pairs, pair)
	}
	sort.Strings(pairs)
	for _, pair := range pairs {
		position, open := c.position[pair]
		side, quantity, known := reporter.HeldPosition(pair)
		switch {
		case !known:
			if open {
				log.Warnf("[SETUP] %s: the position held at the exchange unknown, the one replayed from the "+
					"storage (%s) not checked", pair, position)
			}
		case !open && quantity > 0 && c.mayHaveFilled(pair, side, quantity):
			log.Warnf("[SETUP] %s: the exchange holds a position (%s %g) the storage doesn't tell yet: an open "+
				"order of the bot on its side, eg: a pending entry, may have filled while it was off, settled as "+
				"its fill comes; otherwise the profits of the pair may be wrong", pair, side, quantity)
		case !open && quantity > 0:
			log.Errorf("[SETUP] %s: the exchange holds a position (%s %g) the storage doesn't tell: the trades of "+
				"the pair are settled apart from it, their profits may be wrong", pair, side, quantity)
		case !open:
			// flat on both sides
		case quantity == 0:
			closing, err := c.storedClosing(pair, position.Side)
			switch {
			case err != nil:
				log.Errorf("[SETUP] %s: the position replayed from the storage (%s) is not held at the exchange, "+
					"kept: the open orders that may have closed it unknown: %v", pair, position, err)
			case closing > 0:
				c.unheld[pair] = true
				log.Warnf("[SETUP] %s: the position replayed from the storage (%s) is not held at the exchange: "+
					"kept for the %d open orders that may have closed it, eg: its stop loss, until they end",
					pair, position, closing)
			default:
				c.discard(pair, position)
			}
		case side == position.Side && quantity > position.Quantity &&
			!sameQuantity(quantity, position.Quantity) &&
			c.mayHaveFilled(pair, side, quantity-position.Quantity):
			log.Warnf("[SETUP] %s: the position replayed from the storage (%s) is smaller than the one held at the "+
				"exchange (%s %g): an open order of the bot on its side, eg: a pending entry, may have filled while "+
				"it was off, settled as its fill comes; otherwise the profits of the pair may be wrong", pair,
				position, side, quantity)
		case side != position.Side || !sameQuantity(quantity, position.Quantity):
			log.Errorf("[SETUP] %s: the position replayed from the storage (%s) differs from the one held at the "+
				"exchange (%s %g): kept, the profits of the pair may be wrong", pair, position, side, quantity)
		}
	}
}

// mayHaveFilled reports whether the open orders of the storage on pair on side,
// eg: a pending entry (limit or stop), add up to quantity at least: filled
// while the bot was off, they explain that much more held at the exchange,
// their fills settled once Start loads them (see loadPendingOrders).
func (c *Controller) mayHaveFilled(pair string, side model.SideType, quantity float64) bool {
	orders, err := c.storedOpen(pair)
	if err != nil {
		return false
	}
	var open float64
	for _, order := range orders {
		if order.Side == side {
			open += order.Quantity
		}
	}
	return open > 0 && (open >= quantity || sameQuantity(open, quantity))
}

// storedOpen returns the open orders of the storage on pair.
func (c *Controller) storedOpen(pair string) ([]*model.Order, error) {
	return c.storage.Orders(storage.WithPair(pair), storage.WithStatusIn(model.OrderStatusTypeNew,
		model.OrderStatusTypePartiallyFilled, model.OrderStatusTypePendingCancel))
}

// storedClosing returns how many open orders of the storage on pair may close
// a position held on side: the ones on the other side, eg: its legs.
func (c *Controller) storedClosing(pair string, side model.SideType) (int, error) {
	orders, err := c.storedOpen(pair)
	if err != nil {
		return 0, err
	}
	var closing int
	for _, order := range orders {
		if order.Side != side {
			closing++
		}
	}
	return closing, nil
}

// discard discards position, the one of pair replayed from the storage, which
// the exchange doesn't hold, see reconcile. Must be called while holding
// c.mtx, once started.
func (c *Controller) discard(pair string, position *Position) {
	delete(c.position, pair)
	delete(c.unheld, pair)
	log.Warnf("[ORDER] %s: the position replayed from the storage (%s) is not held at the exchange, eg: closed "+
		"outside the bot: discarded, without any order, its trades after settled on their own", pair, position)
}

// unheldEnded checks again the position of pair the exchange didn't hold at
// the start, kept for the open orders that may have closed it (see
// reconcile), as one of them ended without a fill: once none is left, it is
// discarded if the exchange still holds none. Must be called while holding
// c.mtx.
func (c *Controller) unheldEnded(pair string) {
	position, open := c.position[pair]
	if !open {
		delete(c.unheld, pair)
		return
	}
	if c.closing(pair, position.Side) {
		return // may close it still
	}
	delete(c.unheld, pair)
	reporter, ok := c.exchange.(service.PositionReporter)
	if !ok {
		return
	}
	side, quantity, known := reporter.HeldPosition(pair)
	switch {
	case !known:
		log.Warnf("[ORDER] %s: the position replayed from the storage (%s), not held at the exchange at the "+
			"start, kept: the one held now unknown", pair, position)
	case quantity == 0:
		c.discard(pair, position)
	case side == position.Side && sameQuantity(quantity, position.Quantity):
		log.Warnf("[ORDER] %s: the position replayed from the storage (%s), not held at the exchange at the "+
			"start, is held now, eg: a fill not settled yet: kept", pair, position)
	default:
		log.Errorf("[ORDER] %s: the position replayed from the storage (%s), not held at the exchange at the "+
			"start, differs from the one held now (%s %g): kept, the profits of the pair may be wrong", pair,
			position, side, quantity)
	}
}

// closing reports whether an open order followed on pair may close a position
// held on side: one on the other side, eg: its leg. Must be called while
// holding c.mtx.
func (c *Controller) closing(pair string, side model.SideType) bool {
	for _, orders := range []map[int64]model.Order{c.pending, c.unstored} {
		for _, order := range orders {
			if order.Pair == pair && order.Side != side && isOpen(order.Status) {
				return true
			}
		}
	}
	return false
}

// heldEnded checks the position of pair against the one the exchange holds,
// when it tells it (see service.PositionReporter), as an order on its other
// side ended without a fill, eg: its legs canceled by MT5 as the position
// closed by hand in the terminal or by a stop out, which leaves no fill: once
// no order that may close it is left (the end of the last one checks it), it
// is discarded if the exchange holds none, without any order, rather than
// settling the next trades of the pair against it, their profits and results
// wrong; see reconcile, the check at the start. Checked with no order of the
// pair in flight, under its lock: an exit at the market filled at the
// exchange, its fill not settled yet, would then open a position on the
// other side; checked again by the next UpdateOrders. Must be called while
// holding c.mtx.
func (c *Controller) heldEnded(pair string) {
	delete(c.heldChecks, pair)
	reporter, ok := c.exchange.(service.PositionReporter)
	if !ok {
		return
	}
	position, open := c.position[pair]
	if !open || c.closing(pair, position.Side) {
		return
	}
	unlock, free := c.tryLockPair(pair)
	if !free {
		c.heldChecks[pair] = true
		return
	}
	defer unlock()
	side, quantity, known := reporter.HeldPosition(pair)
	switch {
	case !known:
	case quantity == 0:
		delete(c.position, pair)
		delete(c.unheld, pair)
		log.Warnf("[ORDER] %s: the position (%s) is not held at the exchange any more, its orders on the other "+
			"side ended without a fill, eg: closed outside the bot, by hand or by a stop out: discarded, without "+
			"any order, its trades after settled on their own", pair, position)
	case side != position.Side || !sameQuantity(quantity, position.Quantity):
		log.Warnf("[ORDER] %s: the position (%s) differs from the one held at the exchange (%s %g) as its orders "+
			"on the other side ended without a fill: kept, the profits of the pair may be wrong", pair, position,
			side, quantity)
	}
}

// heldAgain checks again the positions heldEnded couldn't check, an order of
// their pair in flight.
func (c *Controller) heldAgain() {
	c.mtx.RLock()
	none := len(c.heldChecks) == 0
	c.mtx.RUnlock()
	if none {
		return
	}
	c.mtx.Lock()
	defer c.mtx.Unlock()
	for pair := range c.heldChecks {
		c.heldEnded(pair)
	}
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
		price := o.Price
		if !priced(price) {
			price = 0 // unknown, see Position.Update
		}
		c.position[o.Pair] = &Position{
			AvgPrice:  price,
			Quantity:  o.Quantity,
			Fee:       o.Fee,
			CreatedAt: filledAt(o),
			Side:      o.Side,
			Entry:     *o,
		}
		c.opened(o.Pair, filledAt(o))
		return
	}

	entry, side := position.Entry, position.Side // before a reversal replaces them
	result, closed := position.Update(o)
	if closed {
		delete(c.position, o.Pair)
	} else if position.Side != side {
		c.opened(o.Pair, position.CreatedAt)
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
		switch {
		case result.unpriced:
			// unknown: left out of the results rather than a win of 0
		case result.ProfitPercent >= 0:
			if result.Side == model.SideTypeBuy {
				summary.WinLong = append(summary.WinLong, result.ProfitValue)
				summary.WinLongPercent = append(summary.WinLongPercent, result.ProfitPercent)
			} else {
				summary.WinShort = append(summary.WinShort, result.ProfitValue)
				summary.WinShortPercent = append(summary.WinShortPercent, result.ProfitPercent)
			}
		default:
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
		if result.unpriced {
			message = fmt.Sprintf("[PROFIT] unknown %s: the fill or the position it closes without a price\n`%s`",
				quote, c.Results[o.Pair].String())
		}
		reporter, ok := c.exchange.(service.AccountReporter)
		if !ok {
			c.notify(message)
			return
		}
		// as the account books it, in its currency: its deals may come a
		// moment after the fill, so the notification waits for them apart
		closing, percent, table, unpriced := *o, result.ProfitPercent, c.Results[o.Pair].String(), result.unpriced
		go func() {
			for wait := time.Now().Add(profitWait); ; {
				if booked, ok := reporter.ClosedProfit(closing, entry); ok {
					if account, ok := reporter.AccountSummary(); ok && account.Currency != "" {
						message = fmt.Sprintf("[PROFIT] %.2f %s (%.4f %%)\n`%s`", booked, account.Currency,
							percent*100, table)
						if unpriced { // its percentage unknown
							message = fmt.Sprintf("[PROFIT] %.2f %s\n`%s`", booked, account.Currency, table)
						}
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
	if c.unheld[order.Pair] && !c.restoring {
		// the position not held at the exchange closed by its order, or
		// changed: no longer the one checked, see reconcile
		delete(c.unheld, order.Pair)
		if position, ok := c.position[order.Pair]; ok && position.Side == order.Side {
			log.Warnf("[ORDER] %s: added to the position replayed from the storage (%s), not held at the "+
				"exchange at the start", order, position)
		}
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
	c.heldAgain()

	c.mtx.RLock()
	orders := make([]model.Order, 0, len(c.pending))
	for _, order := range c.pending {
		orders = append(orders, order)
	}
	read := c.moves // the changes recorded before the orders are read, see record
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

		excOrder.ID = order.ID
		updatedOrders = append(updatedOrders, excOrder)
	}

	if len(updatedOrders) == 0 {
		return
	}

	// Lock only when updating internal state: the change happened on the
	// exchange, settled even when the storage fails to write it. An order moved
	// or canceled since it was read (see record) may have been read before:
	// unless ended, it is read again next time, its move never undone.
	c.mtx.Lock()
	for _, processOrder := range updatedOrders {
		current, ok := c.pending[processOrder.ID]
		switch {
		case !ok:
			continue // settled already
		case c.movedAt[processOrder.ID] > read && !processOrder.Status.Final():
			log.Infof("[ORDER] %s changed as it was read from the exchange, %s: read again", current,
				processOrder.Status)
			continue
		}
		keepStored(&processOrder, current)
		log.Infof("[ORDER %s] %s", processOrder.Status, processOrder)
		c.store(&processOrder)
		if isOpen(processOrder.Status) {
			c.pending[processOrder.ID] = processOrder
		} else {
			delete(c.pending, processOrder.ID)
			delete(c.movedAt, processOrder.ID)
		}
		c.processTrade(&processOrder)
		c.publish(processOrder, false)
		if processOrder.Status.Final() && processOrder.Status != model.OrderStatusTypeFilled {
			position, open := c.position[processOrder.Pair]
			switch {
			case c.unheld[processOrder.Pair]:
				c.unheldEnded(processOrder.Pair)
			case open && processOrder.Side != position.Side:
				c.heldEnded(processOrder.Pair) // may have closed it
			}
		}
	}
	c.mtx.Unlock()
}

// record records order, changed on the exchange by a move or a cancel begun
// before, see UpdateLimit, UpdateProtection and Cancel; published when
// publish. An order ended meanwhile, eg: filled as it was moved, settled by
// UpdateOrders, is kept as it ended, its end returned when known (see ended),
// false: never undone. Must be called while holding c.mtx.
func (c *Controller) record(order model.Order, publish bool) (model.Order, bool) {
	if _, followed := c.pending[order.ID]; !followed {
		end, ok := c.ended(order)
		switch {
		case ok:
			log.Warnf("[ORDER %s] %s: ended as the exchange changed it, kept as it ended rather than %s",
				end.Status, end, order.Status)
			return end, false
		case order.ID < 0:
			// numbered provisionally, followed while open (see create): ended,
			// its end never stored
			log.Warnf("[ORDER] %s: ended as the exchange changed it, kept as it ended", order)
			return order, false
		}
	}

	c.store(&order)
	if _, followed := c.pending[order.ID]; followed {
		if isOpen(order.Status) {
			c.pending[order.ID] = order
		}
		c.moves++
		c.movedAt[order.ID] = c.moves
	}
	if publish {
		c.publish(order, false)
	}
	return order, true
}

// ended returns the end of order, an order not pending, if it ended: its last
// state recorded, when final, the one the storage failed to write (see
// unstored), else the stored one. Must be called while holding c.mtx.
func (c *Controller) ended(order model.Order) (model.Order, bool) {
	if last, ok := c.unstored[order.ID]; ok {
		return last, last.Status.Final()
	}
	if order.ID <= 0 || c.storage == nil {
		return model.Order{}, false
	}
	stored, err := c.storage.Orders(func(stored model.Order) bool { return stored.ID == order.ID })
	if err != nil {
		log.Errorf("order controller: the order %d stored: %v", order.ID, err)
		return model.Order{}, false
	}
	if len(stored) == 0 || !stored[0].Status.Final() {
		return model.Order{}, false
	}
	return *stored[0], true
}

// feedQueue holds the orders of a pair waiting to be handed to the order feed,
// see publish.
type feedQueue struct {
	orders  []feedOrder
	handing bool // a goroutine hands them, see hand
}

type feedOrder struct {
	order   model.Order
	created bool
}

// publish hands order, created by the controller when created, to the order
// feed in the background, after the orders of its pair published before: in the order the controller records them, never
// an older state of an order after a newer one, and without waiting for the
// consumers of the feed, eg: a notification sent. Must be called while holding
// c.mtx.
func (c *Controller) publish(order model.Order, created bool) {
	if c.orderFeed == nil {
		return
	}
	c.feedMtx.Lock()
	defer c.feedMtx.Unlock()
	if c.feedQueues == nil {
		c.feedQueues = make(map[string]*feedQueue)
	}
	queue, ok := c.feedQueues[order.Pair]
	if !ok {
		queue = &feedQueue{}
		c.feedQueues[order.Pair] = queue
	}
	queue.orders = append(queue.orders, feedOrder{order: order, created: created})
	if !queue.handing {
		queue.handing = true
		go c.hand(queue)
	}
}

// hand hands the orders of queue to the order feed, one at a time, until none
// is left.
func (c *Controller) hand(queue *feedQueue) {
	for {
		c.feedMtx.Lock()
		if len(queue.orders) == 0 {
			queue.orders, queue.handing = nil, false
			c.feedMtx.Unlock()
			return
		}
		next := queue.orders[0]
		queue.orders[0] = feedOrder{}
		queue.orders = queue.orders[1:]
		c.feedMtx.Unlock()
		c.orderFeed.Publish(next.order, next.created)
	}
}

// keepStored gives an order reported by the exchange the storage id of the
// stored one, and the fields the exchange does not report, eg: for the orders
// of a previous run rebuilt from a history, or the legs of a pending entry
// canceled before its fill, reported without side, price or size: an order
// never changes its pair or its side, and keeps its price and its size unless
// the exchange tells them.
func keepStored(excOrder *model.Order, stored model.Order) {
	excOrder.ID = stored.ID
	if excOrder.CreatedAt.IsZero() {
		excOrder.CreatedAt = stored.CreatedAt
	}
	if excOrder.Pair == "" {
		excOrder.Pair = stored.Pair
	}
	if excOrder.Side == "" {
		excOrder.Side = stored.Side
	}
	if excOrder.Price == 0 {
		excOrder.Price = stored.Price
	}
	if excOrder.Quantity == 0 {
		excOrder.Quantity = stored.Quantity
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

// Summaries returns the results of each pair, as their table, read under
// c.mtx: Results is written as the orders fill, so read it directly only once
// the bot stopped, eg: at the end of a backtest. The exclusive lock, as the
// table may write the spare capacity of the slices of a summary (see
// rebuildCache).
func (c *Controller) Summaries() map[string]string {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	summaries := make(map[string]string, len(c.Results))
	for pair, summary := range c.Results {
		summaries[pair] = summary.String()
	}
	return summaries
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
	mtx := c.pairMutex(pair)
	mtx.Lock()
	return mtx.Unlock
}

// pairMutex returns the lock of pair, see lockPair.
func (c *Controller) pairMutex(pair string) *sync.Mutex {
	c.pairsMtx.Lock()
	defer c.pairsMtx.Unlock()
	if c.pairs == nil {
		c.pairs = make(map[string]*sync.Mutex)
	}
	mtx, ok := c.pairs[pair]
	if !ok {
		mtx = new(sync.Mutex)
		c.pairs[pair] = mtx
	}
	return mtx
}

// tryLockPair takes the lock of pair (see lockPair) when free, returning the
// unlock and true; false, without waiting, when an order of pair is in flight.
func (c *Controller) tryLockPair(pair string) (func(), bool) {
	mtx := c.pairMutex(pair)
	if !mtx.TryLock() {
		return nil, false
	}
	return mtx.Unlock, true
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
		c.publish(orders[i], true)
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
		c.publish(orders[i], true)
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

// openingsKept is how many openings of a pair PositionsOpened tells, the last
// ones: plenty for the entries of a session.
const openingsKept = 1000

// opening is a fill that opened a position, or reversed one, at at: replayed
// when restore replayed it from the storage, a fill of a previous run.
type opening struct {
	at       time.Time
	replayed bool
}

// opened records a position of pair opened at at, see PositionsOpened.
// Must be called while holding c.mtx, or from restore.
func (c *Controller) opened(pair string, at time.Time) {
	openings := append(c.openings[pair], opening{at: at, replayed: c.restoring})
	if len(openings) > openingsKept {
		openings = slices.Delete(openings, 0, len(openings)-openingsKept)
	}
	c.openings[pair] = openings
}

// PositionsOpened returns how many fills of pair opened a position, or reversed
// one, at or after since: from the fills the controller settled, the ones of
// the storage replayed at the start (see restore) included, whatever placed
// them (a strategy, the trade panel, Telegram).
func (c *Controller) PositionsOpened(pair string, since time.Time) int {
	return c.countOpenings(pair, since, false)
}

// PositionsReplayed is PositionsOpened of the fills of the storage replayed at
// the start only (see restore): the positions opened by the previous runs, see
// service.PositionOpenings.
func (c *Controller) PositionsReplayed(pair string, since time.Time) int {
	return c.countOpenings(pair, since, true)
}

// countOpenings counts the openings of pair at or after since, the replayed
// ones only when replayed.
func (c *Controller) countOpenings(pair string, since time.Time, replayed bool) int {
	c.mtx.RLock()
	defer c.mtx.RUnlock()
	n := 0
	for _, o := range c.openings[pair] {
		if !o.at.Before(since) && (o.replayed || !replayed) {
			n++
		}
	}
	return n
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
// service.ProtectionBroker, and stores it moved; a leg that ended meanwhile,
// eg: filled as it was moved, is returned as it ended, see record. Like
// Cancel, it works on a stopped bot too: it only protects a position; not once
// the controller is closed (see Close).
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
	moved, recorded := c.record(moved, true)
	if recorded {
		log.Infof("[ORDER UPDATED] %s", moved)
	}
	return moved, nil
}

// UpdateLimit moves a pending limit order to limit, see
// service.ProtectionBroker, and stores it moved; an order that ended meanwhile,
// eg: filled as it was moved, is returned as it ended, see record. Unlike
// UpdateProtection, it needs the bot running: it changes when a position opens.
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
	moved, recorded := c.record(moved, true)
	if recorded {
		log.Infof("[ORDER UPDATED] %s", moved)
	}
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
	c.publish(order, true)
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
	c.publish(order, true)
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
	c.publish(order, true)
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
	c.publish(order, true)
	log.Infof("[ORDER CREATED] %s", order)
	return order, nil
}

// Cancel cancels an order, on a stopped bot too, not once the controller is
// closed (see Close): pending cancel until the exchange tells it canceled,
// unless it ended meanwhile, see record.
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
	if followed, ok := c.pending[order.ID]; ok {
		order = followed // newer than a copy, eg: moved since
	}
	order.Status = model.OrderStatusTypePendingCancel
	if _, recorded := c.record(order, false); recorded {
		log.Infof("[ORDER CANCELED] %s", order)
	}
	return nil
}
