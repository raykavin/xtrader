package order

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/testdata/mocks"
)

// raceBook is an exchange keeping its orders as a broker does, by exchange id,
// on a clock of its own: created, moved and canceled at once, filled when told
// (see fill). A move or a cancel calls answered, when set, once done on the
// exchange: its answer on its way back, eg: while the order fills; so does a
// read of an order, calling read, the state read.
type raceBook struct {
	*mocks.Exchange
	mtx      sync.Mutex
	orders   map[int64]model.Order
	next     int64
	clock    time.Time
	price    float64 // of the market orders
	answered func()
	read     func()
}

func newRaceBook() *raceBook {
	return &raceBook{Exchange: &mocks.Exchange{}, orders: make(map[int64]model.Order),
		clock: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
}

// tick advances the clock of the exchange. Must be called while holding b.mtx.
func (b *raceBook) tick() time.Time {
	b.clock = b.clock.Add(time.Second)
	return b.clock
}

func (b *raceBook) create(side model.SideType, pair string, kind model.OrderType, size, price float64,
	status model.OrderStatusType) model.Order {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	b.next++
	at := b.tick()
	order := model.Order{ExchangeID: b.next, Pair: pair, Side: side, Type: kind, Status: status, Price: price,
		Quantity: size, CreatedAt: at, UpdatedAt: at}
	if kind == model.OrderTypeStopLoss {
		stop := price
		order.Stop = &stop
	}
	b.orders[order.ExchangeID] = order
	return order
}

// fill fills the order of id at its price, as it stands on the exchange.
func (b *raceBook) fill(id int64) {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	order := b.orders[id]
	order.Status, order.UpdatedAt = model.OrderStatusTypeFilled, b.tick()
	b.orders[id] = order
}

// change changes the order of id on the exchange, an open one: then answers,
// see answered.
func (b *raceBook) change(id int64, change func(*model.Order)) (model.Order, error) {
	b.mtx.Lock()
	order, ok := b.orders[id]
	if !ok || !isOpen(order.Status) {
		b.mtx.Unlock()
		return model.Order{}, exchange.ErrOrderNotOpen
	}
	change(&order)
	order.UpdatedAt = b.tick()
	b.orders[id] = order
	answered := b.answered
	b.mtx.Unlock()
	if answered != nil {
		answered()
	}
	return order, nil
}

func (b *raceBook) answer(answered func()) {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	b.answered = answered
}

func (b *raceBook) reading(read func()) {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	b.read = read
}

func (b *raceBook) Order(_ string, id int64) (model.Order, error) {
	b.mtx.Lock()
	order, read := b.orders[id], b.read
	b.mtx.Unlock()
	if read != nil {
		read()
	}
	return order, nil
}

func (b *raceBook) CreateOrderLimit(side model.SideType, pair string, size, limit float64) (model.Order, error) {
	return b.create(side, pair, model.OrderTypeLimit, size, limit, model.OrderStatusTypeNew), nil
}

func (b *raceBook) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	b.mtx.Lock()
	price := b.price
	b.mtx.Unlock()
	return b.create(side, pair, model.OrderTypeMarket, size, price, model.OrderStatusTypeFilled), nil
}

func (b *raceBook) CreateOrderStop(pair string, size, limit float64) (model.Order, error) {
	return b.create(model.SideTypeSell, pair, model.OrderTypeStopLoss, size, limit, model.OrderStatusTypeNew), nil
}

func (b *raceBook) Cancel(order model.Order) error {
	_, err := b.change(order.ExchangeID, func(order *model.Order) { order.Status = model.OrderStatusTypeCanceled })
	return err
}

func (b *raceBook) UpdateLimit(order model.Order, limit float64) (model.Order, error) {
	return b.change(order.ExchangeID, func(order *model.Order) { order.Price = limit })
}

func (b *raceBook) UpdateProtection(leg model.Order, level float64) (model.Order, error) {
	return b.change(leg.ExchangeID, func(order *model.Order) {
		order.Price = level
		order.Stop = &level
	})
}

func (b *raceBook) CreateOrderMarketProtected(model.SideType, string, float64, float64,
	float64) ([]model.Order, error) {
	return nil, exchange.ErrNotSupported
}

func (b *raceBook) CreateOrderLimitProtected(model.SideType, string, float64, float64, float64,
	float64) ([]model.Order, error) {
	return nil, exchange.ErrNotSupported
}

func (b *raceBook) CreateOrderStopProtected(model.SideType, string, float64, float64, float64,
	float64) ([]model.Order, error) {
	return nil, exchange.ErrNotSupported
}

// held runs call in the background, held once done on the exchange of book
// until the returned release is called, which returns its result.
func held[T any](t *testing.T, book *raceBook, call func() (T, error)) func() (T, error) {
	t.Helper()
	done, release := make(chan struct{}), make(chan struct{})
	book.answer(func() {
		close(done)
		<-release
	})
	type result struct {
		value T
		err   error
	}
	results := make(chan result, 1)
	go func() {
		value, err := call()
		results <- result{value, err}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("never done on the exchange")
	}
	book.answer(nil)
	return func() (T, error) {
		close(release)
		r := <-results
		return r.value, r.err
	}
}

// storedOrder returns the order of id in db.
func storedOrder(t *testing.T, db storage.Storage, id int64) model.Order {
	t.Helper()
	orders, err := db.Orders(func(order model.Order) bool { return order.ID == id })
	require.NoError(t, err)
	require.Len(t, orders, 1, "order %d", id)
	return *orders[0]
}

// handedOrders records the orders handed to the order feed of a pair, in the
// order they come.
type handedOrders struct {
	mtx    sync.Mutex
	orders []model.Order
}

func (p *handedOrders) add(order model.Order) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	p.orders = append(p.orders, order)
}

// last returns the last state of the order of id handed, and whether the
// order of until was handed after it.
func (p *handedOrders) last(id, until int64) (model.Order, bool) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	var last model.Order
	after := false
	for _, order := range p.orders {
		switch order.ID {
		case id:
			last, after = order, false
		case until:
			after = true
		}
	}
	return last, after
}

// B19: a limit entry moved on the exchange as it fills ends filled: in the
// storage, in the order feed and for the caller; the stop loss closing the
// position after it leaves nothing open after a restart, nor a fill to settle
// again.
func TestController_LimitEntryMovedAsItFillsEndsFilled(t *testing.T) {
	ctx := context.Background()
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	book := newRaceBook()
	feed := NewOrderFeed()
	handed := &handedOrders{}
	feed.Subscribe("BTCUSDT", handed.add, false)
	feed.Start()
	c := NewController(ctx, book, memory, feed)

	entry, err := c.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 2000)
	require.NoError(t, err)

	// moved on the exchange, its answer on its way back while the entry fills
	// and UpdateOrders settles the fill
	move := held(t, book, func() (model.Order, error) { return c.UpdateLimit(entry, 2005) })
	book.fill(entry.ExchangeID)
	c.UpdateOrders()
	require.Empty(t, c.pending)
	require.Equal(t, 1.0, c.position["BTCUSDT"].Quantity)
	moved, err := move()
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeFilled, moved.Status, "returned as it ended")
	require.Equal(t, 2005.0, moved.Price)

	require.Equal(t, model.OrderStatusTypeFilled, storedOrder(t, memory, entry.ID).Status, "stored filled")
	require.Empty(t, c.pending)

	// the stop loss closes the long
	book.price = 1990
	exit, err := c.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 1)
	require.NoError(t, err)
	require.NotContains(t, c.position, "BTCUSDT", "flat")
	require.InDeltaSlice(t, []float64{-15}, c.Results["BTCUSDT"].Lose(), 1e-9)

	// the order feed hands the states of an order in their order: never the
	// entry as it was moved after it filled
	require.Eventually(t, func() bool {
		_, after := handed.last(entry.ID, exit.ID)
		return after
	}, 5*time.Second, time.Millisecond, "the exit handed")
	time.Sleep(50 * time.Millisecond) // a late copy would come by now
	last, _ := handed.last(entry.ID, exit.ID)
	require.Equal(t, model.OrderStatusTypeFilled, last.Status, "the last state of the entry handed")

	// restarted: the long opened and closed, nothing open nor pending
	again := NewController(ctx, book, memory, NewOrderFeed())
	require.NotContains(t, again.position, "BTCUSDT", "no position left by the restart")
	require.InDeltaSlice(t, []float64{-15}, again.Results["BTCUSDT"].Lose(), 1e-9)
	again.loadPendingOrders()
	require.Empty(t, again.pending, "no fill to settle again")
}

// B19: a move or a cancel done on the exchange never writes the order back
// open over its end, settled meanwhile by UpdateOrders: stored, or kept for
// the storage when it fails.
func TestController_MovesAndCancelsNeverUndoTheEndOfAnOrder(t *testing.T) {
	setup := func(t *testing.T) (*Controller, *raceBook, *flakyStorage) {
		t.Helper()
		book := newRaceBook()
		flaky := newFlakyStorage(t)
		c := NewController(context.Background(), book, flaky, NewOrderFeed())
		notifier := &mocks.Notifier{}
		notifier.On("OnError", mock.Anything).Return()
		notifier.On("Notify", mock.Anything).Return()
		c.SetNotifier(notifier)
		return c, book, flaky
	}

	t.Run("a stop loss moved as it fills", func(t *testing.T) {
		c, book, flaky := setup(t)
		book.price = 2000
		_, err := c.CreateOrderMarket(model.SideTypeBuy, "BTCUSDT", 1)
		require.NoError(t, err)
		leg, err := c.CreateOrderStop("BTCUSDT", 1, 1990)
		require.NoError(t, err)

		move := held(t, book, func() (model.Order, error) { return c.UpdateProtection(leg, 1995) })
		book.fill(leg.ExchangeID)
		c.UpdateOrders()
		require.NotContains(t, c.position, "BTCUSDT", "closed by the stop loss")
		moved, err := move()
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeFilled, moved.Status)

		order := storedOrder(t, flaky, leg.ID)
		require.Equal(t, model.OrderStatusTypeFilled, order.Status)
		require.InDelta(t, -5, order.ProfitValue, 1e-9, "with its profit")
		again := NewController(context.Background(), book, flaky, NewOrderFeed())
		require.NotContains(t, again.position, "BTCUSDT", "no position left by the restart")
	})

	t.Run("a cancel as the order is canceled", func(t *testing.T) {
		c, book, flaky := setup(t)
		entry, err := c.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 2000)
		require.NoError(t, err)

		cancel := held(t, book, func() (struct{}, error) { return struct{}{}, c.Cancel(entry) })
		c.UpdateOrders()
		require.Empty(t, c.pending)
		_, err = cancel()
		require.NoError(t, err)

		require.Equal(t, model.OrderStatusTypeCanceled, storedOrder(t, flaky, entry.ID).Status)
		require.Empty(t, c.pending)
	})

	t.Run("a move as it fills, the storage failing", func(t *testing.T) {
		c, book, flaky := setup(t)
		entry, err := c.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 2000)
		require.NoError(t, err)

		move := held(t, book, func() (model.Order, error) { return c.UpdateLimit(entry, 2005) })
		book.fill(entry.ExchangeID)
		flaky.fail(true)
		c.UpdateOrders()
		flaky.fail(false)
		moved, err := move()
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeFilled, moved.Status)

		c.UpdateOrders() // writes the orders kept for the storage
		require.Empty(t, c.unstored)
		require.Equal(t, model.OrderStatusTypeFilled, storedOrder(t, flaky, entry.ID).Status)
	})

	t.Run("a move as it fills, never numbered by the storage", func(t *testing.T) {
		book := newRaceBook()
		feed := NewOrderFeed()
		handed := &handedOrders{}
		feed.Subscribe("BTCUSDT", handed.add, false)
		feed.Start()
		c := NewController(context.Background(), book, failingStorage{}, feed)
		notifier := &mocks.Notifier{}
		notifier.On("OnError", mock.Anything).Return()
		notifier.On("Notify", mock.Anything).Return()
		c.SetNotifier(notifier)
		entry, err := c.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 2000)
		require.NoError(t, err)
		require.Equal(t, int64(-1), entry.ID)

		move := held(t, book, func() (model.Order, error) { return c.UpdateLimit(entry, 2005) })
		book.fill(entry.ExchangeID)
		c.UpdateOrders()
		_, err = move()
		require.NoError(t, err)
		require.Empty(t, c.pending)
		require.Equal(t, 1.0, c.position["BTCUSDT"].Quantity)

		book.price = 2010
		exit, err := c.CreateOrderMarket(model.SideTypeSell, "BTCUSDT", 1)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			_, after := handed.last(entry.ID, exit.ID)
			return after
		}, 5*time.Second, time.Millisecond, "the exit handed")
		last, _ := handed.last(entry.ID, exit.ID)
		require.Equal(t, model.OrderStatusTypeFilled, last.Status, "never handed open after its fill")
	})
}

// B19: UpdateOrders never writes a state read from the exchange before a move
// done meanwhile: it reads the order again.
func TestController_UpdateOrdersReadsAgainAnOrderMovedAsItReads(t *testing.T) {
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	book := newRaceBook()
	c := NewController(context.Background(), book, memory, NewOrderFeed())

	entry, err := c.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 2, 2000)
	require.NoError(t, err)
	book.mtx.Lock()
	partial := book.orders[entry.ExchangeID]
	partial.Status = model.OrderStatusTypePartiallyFilled
	book.orders[entry.ExchangeID] = partial
	book.mtx.Unlock()

	// the order read, partially filled at 2000, and moved to 2005 before the
	// read is recorded
	read, release := make(chan struct{}), make(chan struct{})
	book.reading(func() {
		close(read)
		<-release
	})
	updated := make(chan struct{})
	go func() {
		defer close(updated)
		c.UpdateOrders()
	}()
	<-read
	book.reading(nil)
	moved, err := c.UpdateLimit(entry, 2005)
	require.NoError(t, err)
	require.Equal(t, 2005.0, moved.Price)
	close(release)
	<-updated

	c.UpdateOrders()
	require.Equal(t, 2005.0, c.pending[entry.ID].Price)
	order := storedOrder(t, memory, entry.ID)
	require.Equal(t, model.OrderStatusTypePartiallyFilled, order.Status)
	require.Equal(t, 2005.0, order.Price, "the order moved, never the state read before")
}

// B19: the order feed takes the states of an order in the order they are
// recorded, its moves included; a consumer of the feed that waits, eg: for a
// notification to be sent, never holds the controller.
func TestController_OrderFeedTakesTheStatesInTheirOrder(t *testing.T) {
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	book := newRaceBook()
	feed := NewOrderFeed()
	handed := &handedOrders{}
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	feed.Subscribe("BTCUSDT", func(order model.Order) {
		<-release
		handed.add(order)
	}, false)
	feed.Start()
	c := NewController(context.Background(), book, memory, feed)

	const moves = 50
	within(t, time.Second, "the orders, the consumer waiting", func() {
		entry, err := c.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 2000)
		require.NoError(t, err)
		for i := 1; i <= moves; i++ {
			_, err := c.UpdateLimit(entry, 2000+float64(i))
			require.NoError(t, err)
		}
		book.fill(entry.ExchangeID)
		c.UpdateOrders()
	})
	close(release)

	require.Eventually(t, func() bool {
		handed.mtx.Lock()
		defer handed.mtx.Unlock()
		return len(handed.orders) == moves+2
	}, 5*time.Second, time.Millisecond)
	handed.mtx.Lock()
	defer handed.mtx.Unlock()
	for i, order := range handed.orders[:moves+1] {
		require.Equal(t, model.OrderStatusTypeNew, order.Status)
		require.Equal(t, 2000+float64(i), order.Price, "handed in their order")
	}
	require.Equal(t, model.OrderStatusTypeFilled, handed.orders[moves+1].Status, "the fill last")
}

// B19: a cancel records the order as the controller follows it, pending
// cancel: moved since the copy canceled, never moved back.
func TestController_CancelRecordsTheOrderAsFollowed(t *testing.T) {
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	book := newRaceBook()
	c := NewController(context.Background(), book, memory, NewOrderFeed())

	entry, err := c.CreateOrderLimit(model.SideTypeBuy, "BTCUSDT", 1, 2000)
	require.NoError(t, err)
	moved, err := c.UpdateLimit(entry, 2005)
	require.NoError(t, err)

	require.NoError(t, c.Cancel(entry)) // a copy before the move
	order := storedOrder(t, memory, entry.ID)
	require.Equal(t, model.OrderStatusTypePendingCancel, order.Status)
	require.Equal(t, 2005.0, order.Price, "as moved")
	require.True(t, moved.UpdatedAt.Equal(order.UpdatedAt), "as recorded by the move, never older")
	require.Equal(t, order.Price, c.pending[entry.ID].Price)

	c.UpdateOrders()
	require.Empty(t, c.pending)
	require.Equal(t, model.OrderStatusTypeCanceled, storedOrder(t, memory, entry.ID).Status)
}
