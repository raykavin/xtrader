package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/storage"
)

// exitBroker is the paper wallet whose orders at the market fail while fails
// is above 0, eg: the market closed, counted in orders. Its legs reserve the
// position: they are canceled before a close, and placed again when it fails
// (service.PositionProtector).
type exitBroker struct {
	*exchange.PaperWallet
	fails  int
	orders int
}

func (b *exitBroker) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	b.orders++
	if b.fails > 0 {
		b.fails--
		return model.Order{}, errors.New("market closed")
	}
	return b.PaperWallet.CreateOrderMarket(side, pair, size)
}

// openLegs returns the legs of the position followed on testPair open in the
// wallet, the take profit and the stop loss, failing without both.
func (r *traderRunner) openLegs() (profit, stop model.Order) {
	r.t.Helper()
	tr, ok := r.trader.trades[testPair]
	require.True(r.t, ok, "no position followed")
	require.Len(r.t, tr.legs, 2, "the position followed without its legs")
	profit, stop = r.legs()
	require.Equal(r.t, model.OrderStatusTypeNew, profit.Status)
	require.Equal(r.t, model.OrderStatusTypeNew, stop.Status)
	return profit, stop
}

// An EXIT whose close fails leaves the position with its stop loss and take
// profit, placed again at their levels after the legs, which reserve the
// position in the paper wallet, were canceled for the close: the scenario of
// TestAuditLLMCoreCloseFailureLeavesUnprotected, where both legs were left
// CANCELED.
func TestTrader_FailedExitKeepsTheProtection(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	var messages []string
	r.trader.SetNotifier(func(message string) { messages = append(messages, message) })
	r.step(100, enterLong(100))
	canceled := r.trader.trades[testPair].legs

	r.broker = &exitBroker{PaperWallet: r.wallet, fails: 1}
	r.step(100, decide(ActionExit, 0.9, nil, nil, nil))
	require.InDelta(t, 10, r.position(), 1e-9, "the close failed")
	for _, leg := range canceled {
		got, err := r.wallet.Order(testPair, leg.ExchangeID)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusTypeCanceled, got.Status, "canceled for the close: %s", leg.Type)
	}
	profit, stop := r.openLegs()
	require.InDelta(t, 104, profit.Price, 1e-9, "the take profit placed again where it was")
	require.InDelta(t, 98, *stop.Stop, 1e-9, "the stop loss placed again where it was")
	require.InDelta(t, 10, stop.Quantity, 1e-9)
	require.Equal(t, model.SideTypeSell, stop.Side)
	require.True(t, r.trader.trades[testPair].exiting)
	require.Len(t, messages, 1)
	require.Contains(t, messages[0], "EXIT of BTCUSDT 15m failed: market closed")
	require.Contains(t, messages[0], "stop loss 98 and take profit 104 placed again")

	// the protection works: the price falls through the stop loss
	r.add(97)
	require.Zero(t, r.position(), "closed by the stop loss placed again")
	got, err := r.wallet.Order(testPair, stop.ExchangeID)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeFilled, got.Status)
	require.InDelta(t, 98, got.Price, 1e-9)
}

// A close that failed is retried on the ticks, the candles in progress, without
// asking the model: once due, the attempts spaced out twice as much each time.
func TestTrader_FailedExitRetriedOnTheTicks(t *testing.T) {
	r, messages := liveRunner(t, nil, 0)
	r.step(100, enterLong(100))
	broker := &exitBroker{PaperWallet: r.wallet, fails: 2}
	r.broker = broker
	r.step(100, decide(ActionExit, 0.9, nil, nil, nil))
	require.Equal(t, 1, broker.orders)
	require.InDelta(t, 10, r.position(), 1e-9)
	r.openLegs()

	exit, asked := r.trader.now(), len(r.provider.inputs)
	r.inProgress(100.5)
	tick := func(after time.Duration) {
		t.Helper()
		r.trader.now = func() time.Time { return exit.Add(after) }
		r.trader.OnPartialCandle(r.df, r.broker)
	}

	tick(10 * time.Second)
	require.Equal(t, 1, broker.orders, "not due yet: retried 30s after the failure")

	tick(31 * time.Second)
	require.Equal(t, 2, broker.orders, "retried on the tick")
	require.InDelta(t, 10, r.position(), 1e-9, "failed again")
	r.openLegs() // protected again

	tick(31*time.Second + 50*time.Second)
	require.Equal(t, 2, broker.orders, "the next attempt a minute later")

	tick(31*time.Second + 61*time.Second)
	require.Equal(t, 3, broker.orders)
	require.Zero(t, r.position(), "closed on the tick")
	require.Empty(t, r.trader.trades)
	require.Len(t, r.provider.inputs, asked, "the model not asked on the ticks")

	// notified: the failure once, and the close done
	var exits []string
	for _, message := range *messages {
		if strings.Contains(message, "EXIT of") {
			exits = append(exits, message)
		}
	}
	require.Len(t, exits, 2, "%q", exits)
	require.Contains(t, exits[0], "failed: market closed")
	require.Contains(t, exits[1], "EXIT of BTCUSDT 15m: the position is closed, after 2 failed attempts")
}

// A close of a candle left out of the analyses, eg: off the consult interval,
// retries the close that failed all the same, once due.
func TestTrader_FailedExitRetriedAtTheClosesLeftOut(t *testing.T) {
	r, _ := liveRunner(t, nil, 0)
	r.step(100, enterLong(100))
	broker := &exitBroker{PaperWallet: r.wallet, fails: 1}
	r.broker = broker
	r.trader.cfg.ConsultInterval = 30 * time.Minute // the candles of 15m: every other close asked
	onSchedule := func() bool {
		closes := r.df.Time[len(r.df.Time)-1].Add(30 * time.Minute) // of the candle added next
		return closes.Truncate(30 * time.Minute).Equal(closes)
	}
	if !onSchedule() {
		r.step(100)
	}
	r.step(100, decide(ActionExit, 0.9, nil, nil, nil))
	require.Equal(t, 1, broker.orders)
	require.InDelta(t, 10, r.position(), 1e-9)

	require.False(t, onSchedule())
	asked := len(r.provider.inputs)
	r.step(100) // left out, 15 minutes after the failure
	require.Len(t, r.provider.inputs, asked, "not asked")
	require.Equal(t, 2, broker.orders, "retried all the same")
	require.Zero(t, r.position())
}

// The delays between the attempts on the ticks double, up to closeRetryMax.
func TestCloseDelay(t *testing.T) {
	require.Equal(t, 30*time.Second, closeDelay(1))
	require.Equal(t, time.Minute, closeDelay(2))
	require.Equal(t, 8*time.Minute, closeDelay(5))
	require.Equal(t, closeRetryMax, closeDelay(6))
	require.Equal(t, closeRetryMax, closeDelay(1000), "no overflow")
}

// nettingBroker closes a position at the market with its legs in place, as
// MT5 does (service.ProtectedCloser): its levels go with it. Its orders at the
// market fail while fails is above 0. It records what is done, in order.
type nettingBroker struct {
	*exchange.PaperWallet
	fails  int
	legs   []model.Order // of the position, which go with it
	events []string
}

func (b *nettingBroker) ClosesProtected() bool {
	return true
}

func (b *nettingBroker) CreateOrderMarketProtected(side model.SideType, pair string, size, stop,
	profit float64) ([]model.Order, error) {
	orders, err := b.PaperWallet.CreateOrderMarketProtected(side, pair, size, stop, profit)
	if len(orders) > 1 {
		b.legs = orders[1:]
	}
	return orders, err
}

func (b *nettingBroker) CreateOrderMarket(side model.SideType, pair string, size float64) (model.Order, error) {
	if b.fails > 0 {
		b.fails--
		b.events = append(b.events, "close refused")
		return model.Order{}, errors.New("market closed")
	}
	b.events = append(b.events, "close")
	for _, leg := range b.legs {
		_ = b.PaperWallet.Cancel(leg) // gone with the position
	}
	return b.PaperWallet.CreateOrderMarket(side, pair, size)
}

func (b *nettingBroker) Cancel(order model.Order) error {
	b.events = append(b.events, "cancel "+string(order.Type))
	return b.PaperWallet.Cancel(order)
}

func (b *nettingBroker) Protect(side model.SideType, pair string, size, stop, profit float64) ([]model.Order,
	error) {
	b.events = append(b.events, "protect")
	return b.PaperWallet.Protect(side, pair, size, stop, profit)
}

// Where the close at the market takes the legs with the position (MT5), the
// legs are never canceled before it: a close refused leaves the position
// protected as it was, and the legs left once it is flat are canceled.
func TestTrader_ExitClosesWithTheLegsInPlace(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	broker := &nettingBroker{PaperWallet: r.wallet, fails: 1}
	r.broker = broker
	r.step(100, enterLong(100))
	legs := r.trader.trades[testPair].legs

	r.step(100, decide(ActionExit, 0.9, nil, nil, nil))
	require.Equal(t, []string{"close refused"}, broker.events, "nothing canceled nor placed again")
	require.InDelta(t, 10, r.position(), 1e-9)
	profit, stop := r.openLegs()
	require.Equal(t, legs[0].ExchangeID, profit.ExchangeID, "the legs of the entry, in place")
	require.InDelta(t, 98, *stop.Stop, 1e-9)

	// retried at the close of the next candle, the model not asked
	broker.events = nil
	r.step(100)
	require.Zero(t, r.position())
	require.Empty(t, r.trader.trades)
	require.Equal(t, []string{"close", "cancel LIMIT_MAKER", "cancel STOP_LOSS"}, broker.events,
		"the legs left canceled once flat")
}

// A close that fails with the order controller, as live: the legs placed again
// are stored and followed, even with the bot stopped, and a Trader started
// again on the same storage follows the position with them, its exit asked
// to the model again.
func TestTrader_FailedExitWithTheOrderController(t *testing.T) {
	setup := func(t *testing.T) (*traderRunner, *exitBroker, *order.Controller, storage.Storage) {
		t.Helper()
		r := newTraderRunner(t, nil, 0)
		memory, err := storage.FromMemory()
		require.NoError(t, err)
		broker := &exitBroker{PaperWallet: r.wallet}
		controller := order.NewController(context.Background(), broker, memory, order.NewOrderFeed())
		r.broker = controller
		r.step(100, enterLong(100))
		return r, broker, controller, memory
	}
	// the legs stored open, once the controller settled them as its ticker does
	stored := func(t *testing.T, controller *order.Controller, memory storage.Storage) (profit, stop *model.Order) {
		t.Helper()
		controller.UpdateOrders()
		open, err := memory.Orders(storage.WithStatus(model.OrderStatusTypeNew))
		require.NoError(t, err)
		require.Len(t, open, 2, "the legs stored open")
		for _, leg := range open {
			require.NotNil(t, leg.GroupID)
			require.Equal(t, model.SideTypeSell, leg.Side)
			if leg.Type == model.OrderTypeStopLoss {
				stop = leg
			} else {
				profit = leg
			}
		}
		require.NotNil(t, stop)
		require.NotNil(t, profit)
		return profit, stop
	}

	t.Run("refused by the broker", func(t *testing.T) {
		r, broker, controller, memory := setup(t)
		entered, stop := stored(t, controller, memory)

		broker.fails = 1
		r.step(100, decide(ActionExit, 0.9, nil, nil, nil))
		require.InDelta(t, 10, r.position(), 1e-9)
		profit, stop2 := stored(t, controller, memory)
		require.NotEqual(t, stop.ID, stop2.ID, "placed again")
		require.NotEqual(t, entered.ID, profit.ID)
		require.InDelta(t, 98, *stop2.Stop, 1e-9)
		require.InDelta(t, 104, profit.Price, 1e-9)

		// a restart: the exit isn't resumed, the position followed with the legs
		// placed again, its exit asked to the model again
		restarted, err := New(r.trader.cfg, r.provider)
		require.NoError(t, err)
		restarted.SetOrderSource(func(pair string) ([]model.Order, error) {
			orders, err := memory.Orders(storage.WithPair(pair))
			if err != nil {
				return nil, err
			}
			list := make([]model.Order, 0, len(orders))
			for _, order := range orders {
				list = append(list, *order)
			}
			return list, nil
		})
		r.trader = restarted
		r.step(100, decide(ActionHold, 0.8, nil, nil, nil))
		position := r.provider.last().Position
		require.NotNil(t, position)
		require.Equal(t, number(98), position.StopLoss)
		require.Equal(t, number(104), position.TakeProfit)
		require.Contains(t, r.provider.last().Rules.Actions, ActionModifySL, "its legs followed")

		r.step(100, decide(ActionExit, 0.9, nil, nil, nil))
		require.Zero(t, r.position())
	})

	t.Run("the bot stopped", func(t *testing.T) {
		r, _, controller, memory := setup(t)
		controller.Start()
		controller.Stop()

		r.step(100, decide(ActionExit, 0.9, nil, nil, nil))
		require.InDelta(t, 10, r.position(), 1e-9, "no order with the bot stopped")
		_, stop := stored(t, controller, memory)
		require.InDelta(t, 98, *stop.Stop, 1e-9, "protected again all the same")

		controller.Start()
		r.step(100)
		require.Zero(t, r.position(), "closed once started again")
	})
}

// A broker that can't protect a position held leaves it without its legs once
// its close fails: logged and notified, once, until protected again.
func TestTrader_FailedExitLeftWithoutTheLegsIsNotified(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	var messages []string
	r.trader.SetNotifier(func(message string) { messages = append(messages, message) })
	r.step(100, enterLong(100))

	broker := &exitBroker{PaperWallet: r.wallet, fails: 2}
	r.broker = &unprotectedBroker{broker, broker}
	r.step(100, decide(ActionExit, 0.9, nil, nil, nil))
	require.InDelta(t, 10, r.position(), 1e-9)
	require.Len(t, messages, 1)
	require.Contains(t, messages[0], "WITHOUT its stop loss 98 and take profit 104")

	r.step(100)
	require.Len(t, messages, 1, "notified once")
	r.step(100)
	require.Zero(t, r.position())
	require.Contains(t, messages[len(messages)-1], "the position is closed, after 2 failed attempts")
}

// unprotectedBroker hides service.PositionProtector.
type unprotectedBroker struct {
	service.Broker
	service.ProtectionBroker
}

var (
	_ service.PositionProtector = (*exchange.PaperWallet)(nil)
	_ service.PositionProtector = (*order.Controller)(nil)
	_ service.ProtectedCloser   = (*order.Controller)(nil)
	_ service.ProtectedCloser   = (*nettingBroker)(nil)
)
