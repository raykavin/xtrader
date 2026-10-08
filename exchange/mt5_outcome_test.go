package exchange

// The outcome of the trade requests on the bridge: their deadline, the answers
// lost and reconciled, the lists the bridge fails to read, and the account
// switched in the terminal.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// Every trade request carries its deadline, within the one of the adapter; the
// reads carry none, as before.
func TestMT5Trade_SendsItsDeadline(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	f.clearRequests()
	before := time.Now()
	_, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)

	posts := f.requestsTo("POST", "/order")
	require.Len(t, posts, 1)
	ms, err := strconv.ParseInt(posts[0].deadline, 10, 64)
	require.NoError(t, err, "a deadline in Unix milliseconds")
	deadline := time.UnixMilli(ms)
	require.True(t, deadline.After(before), deadline)
	require.False(t, deadline.After(time.Now().Add(time.Second)), "the deadline of the adapter, 1s in the tests")
	left, err := strconv.ParseInt(posts[0].timeout, 10, 64)
	require.NoError(t, err, "the time left until it, in milliseconds")
	require.Positive(t, left)
	require.LessOrEqual(t, left, int64(1000))
	for _, get := range f.requestsTo("GET", "/tick") {
		require.Empty(t, get.deadline)
		require.Empty(t, get.timeout)
	}
	require.Regexp(t, `^nb:test#[0-9a-z]{6}$`, posts[0].body["comment"])
}

// B1: a trade request the bridge holds past its deadline, eg: behind a slow
// request or a reconnection, is refused unsent: never executed after the
// adapter gave up, and a clean error, not looked for in the account.
func TestMT5Trade_ExpiredInTheBridgeIsNotSent(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	f.setTradeDelay(1200 * time.Millisecond) // past the deadline of 1s
	f.clearRequests()

	_, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	var bridgeErr *MT5Error
	require.ErrorAs(t, err, &bridgeErr)
	require.Equal(t, http.StatusGatewayTimeout, bridgeErr.Status)
	require.True(t, bridgeErr.NotSent)
	require.NotErrorIs(t, err, ErrMT5OutcomeUnknown)
	require.Zero(t, f.tradesExecuted(), "never executed")
	require.Empty(t, f.positionsOf(m5fPair, m5fMagic))
	require.Zero(t, f.requestCount("GET", "/history_orders"), "refused unsent: nothing to look for")
	require.Equal(t, 1, f.requestCount("POST", "/order"), "never sent again")

	f.setTradeDelay(0)
	order, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	require.Equal(t, 1.0, order.Quantity)
}

// R1: a market order executed whose answer is lost is found in the account
// by the comment of its request: FILLED with its ticket, price and size, as if
// its answer had come, and never sent again.
func TestMT5Trade_LostAnswerOfAnEntryFound(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	f.loseNext(1)
	f.clearRequests()

	order, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 3)
	require.NoError(t, err)
	positions := f.positionsOf(m5fPair, m5fMagic)
	require.Len(t, positions, 1, "executed once")
	require.Equal(t, positions[0].ticket, order.ExchangeID)
	require.Equal(t, model.OrderStatusTypeFilled, order.Status)
	require.Equal(t, 3.0, order.Quantity)
	require.Equal(t, m5fAsk, order.Price)
	require.Equal(t, 1, f.requestCount("POST", "/order"))
	require.Equal(t, 3.0, m5fNet(t, m))

	got, err := m.Order(m5fPair, order.ExchangeID)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeFilled, got.Status)
}

// R1: the account failing to be read at once, the request is looked for again
// past its deadline.
func TestMT5Trade_LostAnswerFoundAfterTheReadsFail(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	f.loseNext(1)
	for range mt5Retries + 1 { // each attempt of the first read
		f.failNext("GET", "/deals", http.StatusBadGateway, `{"error":"history_deals_get: IPC timeout","retcode":-10005}`)
	}

	started := time.Now()
	order, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 2)
	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Since(started), time.Second, "read again after its deadline")
	positions := f.positionsOf(m5fPair, m5fMagic)
	require.Len(t, positions, 1)
	require.Equal(t, positions[0].ticket, order.ExchangeID)
	require.Equal(t, 2.0, order.Quantity)
}

// R1: the close of a position whose answer is lost is found: the order
// controller sees it FILLED, so it is not sent again, which would open a
// short.
func TestMT5Trade_LostAnswerOfACloseFound(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	_, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 3)
	require.NoError(t, err)
	f.loseNext(1)

	closed, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 3)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeFilled, closed.Status)
	require.Equal(t, 3.0, closed.Quantity)
	require.Empty(t, f.positionsOf(m5fPair, m5fMagic), "closed, no short opened")
	require.Zero(t, m5fNet(t, m))
}

// R1: a pending order placed whose answer is lost is found with its ticket.
func TestMT5Trade_LostAnswerOfAPendingOrderFound(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	f.loseNext(1)

	order, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4000)
	require.NoError(t, err)
	pending, ok := f.pending(order.ExchangeID)
	require.True(t, ok, "the ticket of the order placed")
	require.Equal(t, 4000.0, pending.priceOpen)
	got, err := m.Order(m5fPair, order.ExchangeID)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeNew, got.Status)
}

// R1: the levels set on a position whose answer is lost are found by the
// state they set: the OCO order stands, rather than the levels left untracked.
func TestMT5Trade_LostAnswerOfTheLevelsFound(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	entry, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	f.loseNext(1)

	legs, err := m.CreateOrderOCO(model.SideTypeSell, m5fPair, 1, 4400, 4200, 0)
	require.NoError(t, err)
	require.Len(t, legs, 2)
	position, ok := f.position(entry.ExchangeID)
	require.True(t, ok)
	require.Equal(t, 4200.0, position.sl)
	require.Equal(t, 4400.0, position.tp)
}

// R1: a request whose answer is lost and that is absent from the account once
// its deadline passed was not executed: the error says so, it is not unknown.
func TestMT5Trade_LostAnswerNotExecuted(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	f.failNext("POST", "/order", http.StatusBadGateway, `{"error":"order_send: IPC timeout","retcode":-10005}`)
	f.clearRequests()

	started := time.Now()
	_, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.ErrorContains(t, err, "not executed, absent from the account after its deadline")
	require.NotErrorIs(t, err, ErrMT5OutcomeUnknown)
	require.GreaterOrEqual(t, time.Since(started), time.Second, "looked for after its deadline")
	require.Positive(t, f.requestCount("GET", "/deals"))
	require.Empty(t, f.positionsOf(m5fPair, m5fMagic))
	require.Equal(t, 1, f.requestCount("POST", "/order"))
}

// R1: with a bridge that doesn't honor the deadlines, a request absent from
// the account may still be executed: its outcome is unknown.
func TestMT5Trade_LostAnswerUnknownWithoutDeadlines(t *testing.T) {
	f := newM5F(t)
	f.setHealth("deadline", false)
	m := newM5FAdapter(t, f)
	f.failNext("POST", "/order", http.StatusBadGateway, `{"error":"order_send: IPC timeout","retcode":-10005}`)

	_, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.ErrorIs(t, err, ErrMT5OutcomeUnknown)
}

// R1: a request executed whose records lost the comment of their request, eg:
// cut by the broker, is never taken for not executed: its outcome is unknown.
func TestMT5Trade_LostAnswerWithTheCommentChangedIsUnknown(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	f.setStripTags(true)
	f.loseNext(1)

	_, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.ErrorIs(t, err, ErrMT5OutcomeUnknown)
	require.Len(t, f.positionsOf(m5fPair, m5fMagic), 1, "executed")
}

func TestMT5Trade_UnsentFailures(t *testing.T) {
	for _, tc := range []struct {
		err    error
		unsent bool
	}{
		{&MT5Error{Status: http.StatusGatewayTimeout, NotSent: true}, true},
		{&MT5Error{Status: http.StatusServiceUnavailable, NotSent: true}, true},
		{&MT5Error{Status: http.StatusConflict}, true},
		{&MT5Error{Status: http.StatusForbidden}, true},
		{&MT5Error{Status: http.StatusBadRequest}, true},
		{&MT5Error{Status: http.StatusBadGateway}, false},
		{&MT5Error{Status: http.StatusServiceUnavailable}, false},
		{fmt.Errorf("mt5: POST /order: %w", &net.OpError{Op: "dial", Err: errors.New("connection refused")}), true},
		{fmt.Errorf("mt5: POST /order: %w", &net.OpError{Op: "read", Err: errors.New("connection reset")}), false},
		{fmt.Errorf("mt5: POST /order: %w", io.ErrUnexpectedEOF), false},
		{fmt.Errorf("mt5: POST /order: %w", context.DeadlineExceeded), false},
	} {
		require.Equal(t, tc.unsent, mt5Unsent(tc.err), "%v", tc.err)
	}
	require.ErrorIs(t, &MT5Error{Status: http.StatusConflict}, ErrMT5AccountChanged)
}

// The bridge down: the trade request never reached it, a clean error at once.
func TestMT5Trade_BridgeDownIsNotSent(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour))
	tick, err := m.tick(context.Background(), m5fPair)
	require.NoError(t, err)
	f.srv.Close()
	started := time.Now()
	_, err = m.trade(context.Background(), m.marketRequest(m5fPair, model.SideTypeBuy, 0.01, tick, 0))
	require.Error(t, err)
	require.True(t, mt5Unsent(err), "%v", err)
	require.NotErrorIs(t, err, ErrMT5OutcomeUnknown)
	require.Less(t, time.Since(started), time.Second, "not looked for: it never reached the bridge")
}

// B3: a list the bridge fails to read (5xx) is never taken for an empty one:
// the positions stay open in the book, Position keeps their size, and the
// history is not marked read.
func TestMT5Poll_ListFailureKeepsTheBook(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour)) // polled by the test
	order, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 3)
	require.NoError(t, err)
	require.NoError(t, m.poll(context.Background()))

	for _, path := range []string{"/orders", "/positions"} {
		for range mt5Retries + 1 { // each attempt of the read
			f.failNext("GET", path, http.StatusBadGateway, `{"error":"positions_get: IPC timeout","retcode":-10005}`)
		}
		require.Error(t, m.poll(context.Background()), path)
		require.Equal(t, 3.0, m5fNet(t, m), path)
		open, ok := m.OpenPosition(m5fPair)
		require.True(t, ok, path)
		require.Equal(t, 3.0, open.Quantity, path)
		m.book.mtx.RLock()
		_, closed := m.book.closed[order.ExchangeID]
		_, gone := m.book.gone[order.ExchangeID]
		m.book.mtx.RUnlock()
		require.False(t, closed, path)
		require.False(t, gone, path)
	}

	for _, path := range []string{"/deals", "/history_orders"} {
		m.book.historyMtx.Lock()
		from, to := m.book.historyFrom, m.book.historyTo
		m.book.historyMtx.Unlock()
		for range mt5Retries + 1 {
			f.failNext("GET", path, http.StatusBadGateway, `{"error":"history_deals_get: IPC timeout","retcode":-10005}`)
		}
		require.Error(t, m.readHistory(context.Background(), to.Add(-time.Minute), time.Now().Add(time.Hour)), path)
		m.book.historyMtx.Lock()
		require.Equal(t, from, m.book.historyFrom, path)
		require.Equal(t, to, m.book.historyTo, path, "the range failed is not marked read")
		m.book.historyMtx.Unlock()
	}
}

// B14: the terminal logged in another account after NewMT5, eg: switched by
// hand: no trade request is sent, the snapshots of that account are not
// applied to the book, and the account is not reported, until it is back.
func TestMT5Account_SwitchedStopsTheTrading(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	entry, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 3)
	require.NoError(t, err)

	f.mu.Lock()
	f.account["login"] = 999 // another account, without the bot's positions
	positions := f.positions
	f.positions = make(map[int64]*m5fPosition)
	f.mu.Unlock()
	require.Eventually(t, func() bool {
		_, _, err := m.Position(m5fPair)
		return errors.Is(err, ErrMT5AccountChanged)
	}, 5*time.Second, 10*time.Millisecond, "the account read is another one")
	require.Eventually(t, func() bool { return m.accountSwitched() != nil }, 5*time.Second, 10*time.Millisecond)
	f.clearRequests()

	_, err = m.CreateOrderMarket(model.SideTypeSell, m5fPair, 3)
	require.ErrorIs(t, err, ErrMT5AccountChanged)
	require.ErrorContains(t, err, "logged in the demo account 999")
	_, err = m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4000)
	require.ErrorIs(t, err, ErrMT5AccountChanged)
	require.Zero(t, f.requestCount("POST", "/order"), "nothing sent")
	_, ok := m.AccountSummary()
	require.False(t, ok)
	time.Sleep(100 * time.Millisecond) // polls of the other account
	open, ok := m.OpenPosition(m5fPair)
	require.True(t, ok, "the snapshots of the other account not applied")
	require.Equal(t, 3.0, open.Quantity)
	m.book.mtx.RLock()
	_, gone := m.book.gone[entry.ExchangeID]
	m.book.mtx.RUnlock()
	require.False(t, gone)

	f.mu.Lock()
	f.account["login"] = f.login // back
	f.positions = positions
	f.mu.Unlock()
	require.Eventually(t, func() bool {
		_, _, err := m.Position(m5fPair)
		return err == nil && m.accountSwitched() == nil
	}, 5*time.Second, 10*time.Millisecond)
	closed, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 3)
	require.NoError(t, err)
	require.Equal(t, 3.0, closed.Quantity)
	require.Empty(t, f.positionsOf(m5fPair, m5fMagic))
}

// B14: a demo account turned real without WithMT5Real stops the trading too.
func TestMT5Account_TurnedRealStopsTheTrading(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	f.setAccount("trade_mode", mt5AccountReal)
	f.clearRequests()

	_, _, err := m.Position(m5fPair)
	require.ErrorIs(t, err, ErrMT5AccountChanged)
	require.ErrorContains(t, err, "real account")
	_, err = m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.ErrorIs(t, err, ErrMT5AccountChanged)
	require.Zero(t, f.requestCount("POST", "/order"))
}

// B14: the bridge refusing a trade request on another account (409, sent
// false) is a clean error of the account switched.
func TestMT5Account_RefusedByTheBridge(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour)) // the adapter doesn't see it before
	f.setAccount("login", 999)

	_, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.ErrorIs(t, err, ErrMT5AccountChanged)
	require.NotErrorIs(t, err, ErrMT5OutcomeUnknown)
	require.Zero(t, f.tradesExecuted())
}

// R1: a bridge honoring X-Bridge-Deadline but not X-Bridge-Timeout, of a
// version between the two, may send a request after its deadline when its
// clock is behind the agent's: a request absent from the account is unknown.
func TestMT5Trade_LostAnswerUnknownWithoutTimeouts(t *testing.T) {
	f := newM5F(t)
	f.setHealth("timeout", false)
	m := newM5FAdapter(t, f)
	f.failNext("POST", "/order", http.StatusBadGateway, `{"error":"order_send: IPC timeout","retcode":-10005}`)

	_, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.ErrorIs(t, err, ErrMT5OutcomeUnknown)
	require.ErrorContains(t, err, "doesn't honor both deadlines")
}

// B1: the deadline of a trade request is within two thirds of the timeout of
// the HTTP client, so that the bridge gives up on it before the adapter does.
func TestMT5Trade_DeadlineWithinTheClientTimeout(t *testing.T) {
	for _, tc := range []struct {
		timeout, deadline, want time.Duration
	}{
		{30 * time.Second, mt5TradeDeadline, mt5TradeDeadline},
		{3 * time.Second, mt5TradeDeadline, 2 * time.Second},
		{15 * time.Second, mt5TradeDeadline, 10 * time.Second},
		{0, mt5TradeDeadline, mt5TradeDeadline}, // no timeout
	} {
		m := &MT5{client: &http.Client{Timeout: tc.timeout}, deadline: tc.deadline}
		before := time.Now()
		deadline := m.tradeDeadline()
		require.False(t, deadline.Before(before.Add(tc.want)), "timeout %s", tc.timeout)
		require.False(t, deadline.After(time.Now().Add(tc.want)), "timeout %s", tc.timeout)
	}
}

func TestMT5ClockSkew(t *testing.T) {
	asked := time.Unix(1000, 0)
	answered := asked.Add(100 * time.Millisecond)
	unix := func(at time.Time) float64 { return float64(at.UnixNano()) / 1e9 }
	require.Zero(t, mt5ClockSkew(0, asked, answered), "unknown")
	require.Zero(t, mt5ClockSkew(unix(asked.Add(50*time.Millisecond)), asked, answered), "within the request")
	require.InDelta(t, 3*time.Second, mt5ClockSkew(unix(answered.Add(3*time.Second)), asked, answered),
		float64(time.Millisecond), "ahead")
	require.InDelta(t, -2*time.Second, mt5ClockSkew(unix(asked.Add(-2*time.Second)), asked, answered),
		float64(time.Millisecond), "behind")
}

// R1: while the adapter waits for the deadline of a trade request whose answer
// was lost, only the trading of its pair waits: the reads, the poll and the
// trading of the other pairs go on.
func TestMT5Trade_LostAnswerWaitHoldsItsPairOnly(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5Symbols(m5fPair, "BTCUSDm"), withMT5TradeDeadline(1500*time.Millisecond,
		20*time.Millisecond))
	f.failNext("POST", "/order", http.StatusBadGateway, `{"error":"order_send: IPC timeout","retcode":-10005}`)
	f.clearRequests()

	done := make(chan error, 1)
	started := time.Now()
	go func() {
		_, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
		done <- err
	}()
	require.Eventually(t, func() bool { return f.requestCount("GET", "/history_orders") > 0 }, time.Second,
		time.Millisecond, "looked for in the account at once")

	quick := func(what string, fn func() error) {
		t.Helper()
		at := time.Now()
		require.NoError(t, fn(), what)
		require.Less(t, time.Since(at), 500*time.Millisecond, what)
	}
	quick("another pair", func() error {
		order, err := m.CreateOrderMarket(model.SideTypeBuy, "BTCUSDm", 0.01)
		if err == nil && order.Quantity != 0.01 {
			err = fmt.Errorf("filled %v", order.Quantity)
		}
		return err
	})
	quick("the position", func() error { _, _, err := m.Position(m5fPair); return err })
	quick("the poll", func() error { return m.poll(context.Background()) })
	quick("the account", func() error { _, err := m.Account(); return err })
	select {
	case err := <-done:
		t.Fatalf("returned before its deadline: %v", err)
	default:
	}

	err := <-done
	require.ErrorContains(t, err, "not executed, absent from the account after its deadline")
	require.GreaterOrEqual(t, time.Since(started), 1500*time.Millisecond)
	require.Empty(t, f.positionsOf(m5fPair, m5fMagic))
}

// R1: the settler nets each pair apart, skipping a pair trading: a pair held
// long, eg: by a trade request whose answer was lost, never holds the netting
// of the others.
func TestMT5Settler_NetsEachPairApart(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5Symbols(m5fPair, "BTCUSDm"))
	_, err := m.CreateOrderMarket(model.SideTypeBuy, "BTCUSDm", 0.02)
	require.NoError(t, err)

	unlock := m.lockPair(m5fPair) // a trade request of XAUUSDm waiting
	defer unlock()
	m.queueSettle(m5fPair)
	f.openPosition("BTCUSDm", mt5TypeSell, 0.01, m5fMagic, "nb:test") // eg: a pending order filled
	require.Eventually(t, func() bool { return len(f.tradeRequests(mt5ActionCloseBy)) == 1 }, 3*time.Second,
		10*time.Millisecond, "netted while XAUUSDm is held")
	require.Equal(t, 0.01, f.netLots("BTCUSDm", m5fMagic))
	require.Len(t, f.positionsOf("BTCUSDm", m5fMagic), 1)
}

// R1: a pending order moved whose answer is lost is found by the state it set.
func TestMT5Trade_LostAnswerOfAModifyFound(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	order, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4000)
	require.NoError(t, err)
	f.loseNext(1)
	f.clearRequests()

	moved, err := m.UpdateLimit(order, 3990)
	require.NoError(t, err)
	require.Equal(t, 3990.0, moved.Price)
	pending, ok := f.pending(order.ExchangeID)
	require.True(t, ok)
	require.Equal(t, 3990.0, pending.priceOpen)
	require.Len(t, f.tradeRequests(mt5ActionModify), 1, "never sent again")
}

// R1: a pending order moved whose answer is lost and that is still where it was
// once its deadline passed was not moved.
func TestMT5Trade_LostAnswerOfAModifyNotExecuted(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	order, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4000)
	require.NoError(t, err)
	f.failNext("POST", "/order", http.StatusBadGateway, `{"error":"order_send: IPC timeout","retcode":-10005}`)

	_, err = m.UpdateLimit(order, 3990)
	require.ErrorContains(t, err, "not executed, absent from the account after its deadline")
	require.NotErrorIs(t, err, ErrMT5OutcomeUnknown)
	pending, ok := f.pending(order.ExchangeID)
	require.True(t, ok)
	require.Equal(t, 4000.0, pending.priceOpen)
}

// R1: a pending order removed whose answer is lost is found gone, unfilled:
// canceled.
func TestMT5Trade_LostAnswerOfARemoveFound(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f)
	order, err := m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4000)
	require.NoError(t, err)
	f.loseNext(1)

	require.NoError(t, m.Cancel(order))
	_, ok := f.pending(order.ExchangeID)
	require.False(t, ok, "removed")
	require.Len(t, f.tradeRequests(mt5ActionRemove), 1)
	got, err := m.Order(m5fPair, order.ExchangeID)
	require.NoError(t, err)
	require.Equal(t, model.OrderStatusTypeCanceled, got.Status)
}

// R1: the close-by of the netting whose answer is lost is found by the deals
// of the comment of its request: the positions netted once.
func TestMT5Trade_LostAnswerOfACloseByFound(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour)) // polled by the test
	_, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 3)
	require.NoError(t, err)
	f.openPosition(m5fPair, mt5TypeSell, 0.01, m5fMagic, "nb:test") // eg: a pending order filled
	require.NoError(t, m.poll(context.Background()))
	f.loseNext(1)

	require.NoError(t, m.settlePair(context.Background(), m5fPair))
	require.Len(t, f.tradeRequests(mt5ActionCloseBy), 1, "never sent again")
	require.Equal(t, 0.02, f.netLots(m5fPair, m5fMagic))
	require.Len(t, f.positionsOf(m5fPair, m5fMagic), 1, "netted")
	require.InDelta(t, 2.0, m5fNet(t, m), 1e-9)
}

// R1: the close of a position whose answer is lost is found by the deals of
// that position only: a deal of the same comment closing another one isn't
// its own.
func TestMT5LookupTrade_TheCloseOfItsPositionOnly(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour))
	first, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	f.openPosition(m5fPair, mt5TypeBuy, 0.01, m5fMagic, "nb:test#a1b2c3") // of a request of its own
	require.NoError(t, m.poll(context.Background()))
	tick, err := m.tick(context.Background(), m5fPair)
	require.NoError(t, err)
	positions := f.positionsOf(m5fPair, m5fMagic)
	require.Len(t, positions, 2)
	other := positions[1].ticket
	require.NotEqual(t, first.ExchangeID, other)

	request := m.marketRequest(m5fPair, model.SideTypeSell, 0.01, tick, first.ExchangeID)
	request["comment"] = m.tagComment()
	sent := time.Now()
	f.mu.Lock()
	p := f.positions[other]
	f.exitLocked(p, 0.01, 4285.897, mt5ReasonExpert, request["comment"].(string), m5fMagic)
	f.mu.Unlock()

	_, outcome, err := m.lookupTrade(context.Background(), request, sent)
	require.NoError(t, err)
	require.Equal(t, mt5TradeAbsent, outcome, "the deal closes another position")

	f.mu.Lock()
	_, deal := f.exitLocked(f.positions[first.ExchangeID], 0.01, 4285.897, mt5ReasonExpert,
		request["comment"].(string), m5fMagic)
	f.mu.Unlock()
	result, outcome, err := m.lookupTrade(context.Background(), request, sent)
	require.NoError(t, err)
	require.Equal(t, mt5TradeFound, outcome)
	require.Equal(t, deal.ticket, result.Deal)
	require.Equal(t, 0.01, result.Volume)
}

// R1: a trade request whose answer is lost and the account unreadable past its
// deadline leaves its pair unsettled: nothing opening or closing a position of
// it is sent, which could double it, until a read of the account tells the
// outcome; the other pairs and the levels trade on.
func TestMT5Trade_UnknownOutcomeLeavesThePairUnsettled(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5Symbols(m5fPair, "BTCUSDm"))
	entry, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	f.failNext("POST", "/order", http.StatusBadGateway, `{"error":"order_send: IPC timeout","retcode":-10005}`)
	f.setFailing("GET", "/deals", http.StatusBadGateway, `{"error":"history_deals_get: IPC timeout","retcode":-10005}`)

	_, err = m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.ErrorIs(t, err, ErrMT5OutcomeUnknown)
	f.clearRequests()

	_, err = m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.ErrorIs(t, err, ErrMT5Unsettled)
	_, err = m.CreateOrderLimit(model.SideTypeBuy, m5fPair, 1, 4000)
	require.ErrorIs(t, err, ErrMT5Unsettled)
	require.Zero(t, f.requestCount("POST", "/order"), "nothing sent on the pair")
	_, err = m.CreateOrderMarket(model.SideTypeBuy, "BTCUSDm", 0.01)
	require.NoError(t, err, "another pair")
	_, err = m.CreateOrderOCO(model.SideTypeSell, m5fPair, 1, 4400, 4200, 0)
	require.NoError(t, err, "the levels of a position")
	position, ok := f.position(entry.ExchangeID)
	require.True(t, ok)
	require.Equal(t, 4200.0, position.sl)

	f.clearFailing("GET", "/deals")
	settled := func() bool {
		return m.checkUnsettled(map[string]any{"action": mt5ActionDeal, "symbol": m5fPair}) == nil
	}
	require.Eventually(t, settled, 5*time.Second, 10*time.Millisecond, "settled once the account is read")
	order, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	require.Equal(t, 1.0, order.Quantity)
	require.InDelta(t, 2.0, m5fNet(t, m), 1e-9, "the request unsettled was not executed")
}

// B14: the terminal back on the bot's account between the lists of a poll and
// the account read after them: the lists, of the other account, are not
// applied, as the account read before them tells.
func TestMT5Poll_AccountBackBetweenTheReadsIsNotApplied(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour)) // polled by the test
	entry, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 3)
	require.NoError(t, err)
	require.NoError(t, m.poll(context.Background()))

	f.mu.Lock()
	f.account["login"] = 999 // another account, without the bot's positions
	positions := f.positions
	f.positions = make(map[int64]*m5fPosition)
	f.mu.Unlock()
	back := func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.account["login"] = f.login
		f.positions = positions
	}
	f.afterNext("GET", "/positions", back) // the lists of the other account read, then back

	require.ErrorIs(t, m.poll(context.Background()), ErrMT5AccountChanged)
	open, ok := m.OpenPosition(m5fPair)
	require.True(t, ok, "the lists of the other account not applied")
	require.Equal(t, 3.0, open.Quantity)
	m.book.mtx.RLock()
	_, gone := m.book.gone[entry.ExchangeID]
	_, closed := m.book.closed[entry.ExchangeID]
	m.book.mtx.RUnlock()
	require.False(t, gone)
	require.False(t, closed)

	back()
	require.NoError(t, m.poll(context.Background()))
	require.NoError(t, m.accountSwitched())
	open, ok = m.OpenPosition(m5fPair)
	require.True(t, ok)
	require.Equal(t, 3.0, open.Quantity)
}

// B14: the account switched, the account read is never served from the cache:
// it is read again each time, until the bot's is back.
func TestMT5Account_NoCacheWhileSwitched(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5PollInterval(time.Hour)) // a cache of an hour
	_, err := m.Account()
	require.NoError(t, err)
	f.setAccount("login", 999)
	require.ErrorIs(t, m.poll(context.Background()), ErrMT5AccountChanged, "seen by the poll")
	f.clearRequests()

	for range 2 {
		_, err = m.accountInfo(context.Background())
		require.ErrorIs(t, err, ErrMT5AccountChanged, "not the account of the cache")
	}
	require.Equal(t, 2, f.requestCount("GET", "/account"), "read again each time")
	_, ok := m.AccountSummary()
	require.False(t, ok)

	f.setAccount("login", f.login)
	account, err := m.accountInfo(context.Background())
	require.NoError(t, err)
	require.Equal(t, f.login, account.Login)
	require.NoError(t, m.accountSwitched())
	f.clearRequests()
	_, err = m.accountInfo(context.Background())
	require.NoError(t, err)
	require.Zero(t, f.requestCount("GET", "/account"), "served from the cache again")
}

// B3: the ranges before the history of the terminal answered empty by the
// bridge (a range copy_rates_range refuses while the latest candle reads), a
// download from before its start reads the candles there are, as the end of
// the history stops the pages of CandlesByLimit; a failure of the terminal is
// an error, never a gap.
func TestMT5Candles_BeforeTheHistoryAndFailures(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5DataOnly())
	rates := m5fCandles(100, time.Minute, 4000)
	f.setRates(m5fPair, "M1", rates)

	first := time.Unix(rates[0].Time, 0).UTC()
	candles, err := m.CandlesByPeriod(context.Background(), m5fPair, "1m", first.Add(-30000*time.Minute),
		time.Time{})
	require.NoError(t, err)
	require.Len(t, candles, 99, "the complete ones there are")
	require.Equal(t, first, candles[0].Time)
	require.Len(t, f.requestsTo("GET", "/rates"), 4, "three ranges before the history, empty")

	candles, err = m.CandlesByLimit(context.Background(), m5fPair, "1m", 500)
	require.NoError(t, err)
	require.Len(t, candles, 99, "the start of the history")

	f.setFailing("GET", "/rates", http.StatusBadGateway,
		`{"error":"copy_rates_range XAUUSDm: Terminal: Call failed","retcode":-1}`)
	_, err = m.CandlesByPeriod(context.Background(), m5fPair, "1m", first, time.Time{})
	var bridgeErr *MT5Error
	require.ErrorAs(t, err, &bridgeErr)
	require.Equal(t, -1, bridgeErr.Retcode)
	_, err = m.CandlesByLimit(context.Background(), m5fPair, "1m", 50)
	require.ErrorAs(t, err, &bridgeErr, "no warmup taken for an empty history")
}
