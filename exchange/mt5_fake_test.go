package exchange

// A stateful fake of tools/mt5bridge/mt5bridge.py for the tests of the MT5
// adapter: the routes, the query parameters and the JSON shapes of the bridge
// (the field names of the MetaTrader5 package, as in testdata/mt5), over a
// hedging account loaded from testdata/mt5 (account and symbols). It keeps the
// positions, the pending orders, the deals and the orders of the history, with
// tickets, partial closes, close-by, levels, pending fills, cancels and
// expiries, and the exits by the levels and by a stop out, each with its entry
// and reason as MT5 records them.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	// m5fMagic is the magic number of the bot of the tests.
	m5fMagic = int64(424242)
	// m5fPair is the symbol traded by default.
	m5fPair = "XAUUSDm"
)

// m5fRequestFields are the fields of a trade request the bridge forwards to
// order_send (REQUEST_FIELDS of mt5bridge.py): any other one is refused.
var m5fRequestFields = map[string]bool{
	"action": true, "magic": true, "order": true, "symbol": true, "volume": true, "price": true, "stoplimit": true,
	"sl": true, "tp": true, "deviation": true, "type": true, "type_filling": true, "type_time": true,
	"expiration": true, "comment": true, "position": true, "position_by": true,
}

// m5fTimeframes are the timeframes of the bridge.
var m5fTimeframes = map[string]bool{"M1": true, "M3": true, "M5": true, "M10": true, "M15": true, "M30": true,
	"H1": true, "H2": true, "H4": true, "D1": true, "W1": true, "MN1": true}

type m5fPosition struct {
	ticket    int64 // its identifier, the key of positions
	live      int64 // its ticket since a service operation (reticket), 0 for ticket
	timeMsc   int64
	kind      int
	magic     int64
	reason    int
	volume    float64
	priceOpen float64
	sl, tp    float64
	symbol    string
	comment   string
}

type m5fOrder struct {
	ticket        int64
	setupMsc      int64
	doneMsc       int64
	expiration    int64
	kind          int
	typeTime      int
	typeFilling   int
	state         int
	magic         int64
	positionID    int64
	positionByID  int64
	reason        int
	volumeInitial float64
	volumeCurrent float64
	priceOpen     float64
	sl, tp        float64
	priceCurrent  float64
	symbol        string
	comment       string
}

type m5fDeal struct {
	ticket     int64
	order      int64
	timeMsc    int64
	kind       int
	entry      int
	magic      int64
	positionID int64
	reason     int
	volume     float64
	price      float64
	profit     float64
	commission float64
	symbol     string
	comment    string
}

type m5fTick struct {
	bid, ask float64
	timeMsc  int64
}

type m5fSymbol struct {
	raw      map[string]any
	digits   int
	point    float64
	contract float64
	min      float64
	step     float64
	max      float64
	filling  int
}

type m5fFailure struct {
	method, path string
	status       int
	body         string
}

// m5fHold is a request held before it is handled, see holdNext.
type m5fHold struct {
	method, path, key string
	reached, released chan struct{}
}

type m5fResult struct {
	retcode int
	comment string
}

type m5fRequest struct {
	method   string
	path     string
	query    string
	body     map[string]any
	deadline string // MT5DeadlineHeader
	timeout  string // MT5TimeoutHeader
}

type m5fFake struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	token         string
	health        map[string]any
	account       map[string]any
	symbols       map[string]*m5fSymbol
	ticks         map[string]m5fTick
	rates         map[string][]mt5Rate // by symbol|timeframe
	positions     map[int64]*m5fPosition
	orders        map[int64]*m5fOrder
	history       []*m5fOrder
	deals         []*m5fDeal
	nextTicket    int64
	nextDeal      int64
	failures      []m5fFailure
	results       []m5fResult       // answers of the next trade requests, not executed
	resultsAt     map[int]m5fResult // answers of the trade requests of those numbers, not executed
	withoutDeal   map[int]bool      // the trade requests of those numbers answered without their deal
	trades        int               // trade requests received
	zeroPrice     bool              // answer the market deals without their price
	drift         float64           // added to the prices after every market deal
	marketClosed  bool
	requests      []m5fRequest
	afterSnapshot func()                // runs after the next GET /orders, eg: to fill an order between the two reads
	after         map[string]func()     // run after the next request of their method and path
	failing       map[string]m5fFailure // the answers of every request of their method and path, until cleared
	hold          *m5fHold              // the next request it matches, held before it is handled, see holdNext
	hidden        map[int64]bool        // positions left out of GET /positions, as by a lagging terminal

	login       int64         // of --login: the trade requests on another account are refused (409)
	tradeDelay  time.Duration // waited by the trade requests before their deadline is checked, as for the terminal
	loseAnswers int           // next trade requests executed whose answer is lost (the connection closed)
	stripTags   bool          // the comments of the trade requests recorded without their tag, as by a broker

	held       bool          // the history made since holdHistory is not listed yet, as by a lagging terminal
	heldDeal   int64         // the last deal listed while held
	heldTicket int64         // the last order of the history listed while held
	clock      time.Duration // the clock of the trade server ahead of the bot's, negative when behind

	// the share of the profit of a close-by on the deal of its position, the
	// rest on the one of its position_by: the whole by default, as on MT5 (the
	// deal of the position_by with a profit of 0, see the close-by of
	// testdata/mt5/deals.json); m5fCloseBySplitBy for none
	closeBySplit float64
	commission   float64 // of the entry deals, per lot (negative), 0 for none
}

// m5fCloseBySplitBy is a closeBySplit putting the whole profit of a close-by
// on the deal of its position_by.
const m5fCloseBySplitBy = -1

func m5fTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "mt5", name))
	require.NoError(t, err)
	return data
}

// newM5F starts a fake bridge, stopped when the test ends, with the account and
// the symbols of testdata/mt5 and the prices of their snapshots.
func newM5F(t *testing.T) *m5fFake {
	t.Helper()
	f := &m5fFake{
		t:          t,
		health:     map[string]any{"connected": true, "trade_allowed": true, "build": 5200, "login": 12345678, "server": "Exness-MT5Trial11", "read_only": false, "deadline": true, "timeout": true},
		symbols:    make(map[string]*m5fSymbol),
		ticks:      make(map[string]m5fTick),
		rates:      make(map[string][]mt5Rate),
		positions:  make(map[int64]*m5fPosition),
		orders:     make(map[int64]*m5fOrder),
		nextTicket: 680000000,
		nextDeal:   530000000,
	}
	require.NoError(t, json.Unmarshal(m5fTestdata(t, "account.json"), &f.account))
	f.login = int64(f.account["login"].(float64))
	for _, name := range []string{"XAUUSDm", "BTCUSDm", "USDBRLm", "EURUSDm"} {
		var raw map[string]any
		require.NoError(t, json.Unmarshal(m5fTestdata(t, "symbol_"+name+".json"), &raw))
		symbol := &m5fSymbol{
			raw:      raw,
			digits:   int(raw["digits"].(float64)),
			point:    raw["point"].(float64),
			contract: raw["trade_contract_size"].(float64),
			min:      raw["volume_min"].(float64),
			step:     raw["volume_step"].(float64),
			max:      raw["volume_max"].(float64),
			filling:  int(raw["filling_mode"].(float64)),
		}
		f.symbols[name] = symbol
		bid, ask := raw["bid"].(float64), raw["ask"].(float64)
		if bid == 0 { // EURUSDm was read with the market closed
			bid, ask = 1.08001, 1.08011
		}
		f.ticks[name] = m5fTick{bid: bid, ask: ask, timeMsc: time.Now().UnixMilli()}
	}
	var tick map[string]any
	require.NoError(t, json.Unmarshal(m5fTestdata(t, "tick_XAUUSDm.json"), &tick))
	f.ticks["XAUUSDm"] = m5fTick{bid: tick["bid"].(float64), ask: tick["ask"].(float64),
		timeMsc: time.Now().UnixMilli()}

	f.srv = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *m5fFake) url() string {
	return f.srv.URL
}

// options returns the options of an adapter of the bot on the fake: its symbol
// XAUUSDm unless extra sets others, its magic number, a fast poll and fast
// retries, then extra.
func (f *m5fFake) options(extra ...MT5Option) []MT5Option {
	options := []MT5Option{
		WithMT5BaseURL(f.url()),
		WithMT5Magic(m5fMagic),
		WithMT5Comment("nb:test"),
		WithMT5PollInterval(20 * time.Millisecond),
		withMT5RetryDelay(time.Millisecond),
		withMT5SyncWait(0), // the candles of the tests are old: see TestMT5AwaitsTheHistory
		withMT5TradeDeadline(time.Second, 20*time.Millisecond),
	}
	probe := &MT5{}
	for _, option := range extra {
		option(probe)
	}
	if len(probe.symbols) == 0 {
		options = append(options, WithMT5Symbols(m5fPair))
	}
	return append(options, extra...)
}

// newM5FAdapter creates an adapter on the fake, stopped when the test ends.
func newM5FAdapter(t *testing.T, f *m5fFake, extra ...MT5Option) *MT5 {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	m, err := NewMT5(ctx, f.options(extra...)...)
	require.NoError(t, err)
	t.Cleanup(cancel)
	return m
}

// nowMs is the time of the trade server, in milliseconds. The caller holds
// f.mu.
func (f *m5fFake) nowMs() int64 {
	return time.Now().Add(f.clock).UnixMilli()
}

// setServerClock sets the clock of the trade server, which times the deals and
// the orders, ahead of the bot's (negative when behind).
func (f *m5fFake) setServerClock(ahead time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = ahead
}

// holdHistory leaves the deals and the orders of the history made from now on
// out of GET /deals and /history_orders, as a terminal whose history lags,
// until releaseHistory.
func (f *m5fFake) holdHistory() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held, f.heldDeal, f.heldTicket = true, f.nextDeal, f.nextTicket
}

func (f *m5fFake) releaseHistory() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held = false
}

func (f *m5fFake) newTicketLocked() int64 {
	f.nextTicket++
	return f.nextTicket
}

func (f *m5fFake) newDealLocked() int64 {
	f.nextDeal++
	return f.nextDeal
}

// setPrice sets the tick of a symbol.
func (f *m5fFake) setPrice(symbol string, bid, ask float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ticks[symbol] = m5fTick{bid: bid, ask: ask, timeMsc: f.nowMs()}
}

// setRates sets the candles of a symbol and timeframe, oldest first.
func (f *m5fFake) setRates(symbol, timeframe string, rates []mt5Rate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rates[symbol+"|"+timeframe] = append([]mt5Rate(nil), rates...)
}

// setHealth sets a field of GET /health.
func (f *m5fFake) setHealth(key string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.health[key] = value
}

// setAccount sets a field of GET /account.
func (f *m5fFake) setAccount(key string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.account[key] = value
}

// failNext answers the next request matching method and path with status and
// body, without handling it.
func (f *m5fFake) failNext(method, path string, status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, m5fFailure{method: method, path: path, status: status, body: body})
}

// afterNext runs fn once the next request of method to path is answered.
func (f *m5fFake) afterNext(method, path string, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.after == nil {
		f.after = make(map[string]func())
	}
	f.after[method+" "+path] = fn
}

// setFailing answers every request of method to path with status and body,
// without handling it, until clearFailing.
func (f *m5fFake) setFailing(method, path string, status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing == nil {
		f.failing = make(map[string]m5fFailure)
	}
	f.failing[method+" "+path] = m5fFailure{method: method, path: path, status: status, body: body}
}

// holdNext holds the next request of method to path with key in its query,
// before it is handled, until release (called when the test ends too):
// reached is closed once it arrives.
func (f *m5fFake) holdNext(method, path, key string) (reached <-chan struct{}, release func()) {
	hold := &m5fHold{method: method, path: path, key: key, reached: make(chan struct{}),
		released: make(chan struct{})}
	f.mu.Lock()
	f.hold = hold
	f.mu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(hold.released) }) }
	f.t.Cleanup(release)
	return hold.reached, release
}

// hidePosition leaves the position of ticket out of GET /positions, or back in
// it, as a terminal that lists it late.
func (f *m5fFake) hidePosition(ticket int64, hidden bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hidden == nil {
		f.hidden = make(map[int64]bool)
	}
	f.hidden[ticket] = hidden
}

func (f *m5fFake) clearFailing(method, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.failing, method+" "+path)
}

// resultNext answers the next trade request with retcode and comment, without
// executing it.
func (f *m5fFake) resultNext(retcode int, comment string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = append(f.results, m5fResult{retcode: retcode, comment: comment})
}

// setDrift moves the prices by drift after every market deal.
func (f *m5fFake) setDrift(drift float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drift = drift
}

// resultAt answers the n-th trade request from now on (1 for the next one) with
// retcode and comment, without executing it.
func (f *m5fFake) resultAt(n, retcode int, comment string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resultsAt == nil {
		f.resultsAt = make(map[int]m5fResult)
	}
	f.resultsAt[f.trades+n] = m5fResult{retcode: retcode, comment: comment}
}

// answerWithoutDeal executes the n-th trade request from now on (1 for the next
// one), answering it without the ticket of its deal, as an answer may come.
func (f *m5fFake) answerWithoutDeal(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.withoutDeal == nil {
		f.withoutDeal = make(map[int]bool)
	}
	f.withoutDeal[f.trades+n] = true
}

func (f *m5fFake) setZeroPrice(zero bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zeroPrice = zero
}

// setTradeDelay makes the trade requests wait delay before their deadline is
// checked, as behind a slow request or a reconnection of the terminal.
func (f *m5fFake) setTradeDelay(delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tradeDelay = delay
}

// loseNext executes the next n trade requests but loses their answers: the
// connection is closed instead.
func (f *m5fFake) loseNext(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loseAnswers = n
}

func (f *m5fFake) setStripTags(strip bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stripTags = strip
}

// tradesExecuted counts the trade requests that reached order_send.
func (f *m5fFake) tradesExecuted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.trades
}

func (f *m5fFake) requestsTo(method, path string) []m5fRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var matched []m5fRequest
	for _, request := range f.requests {
		if request.method == method && request.path == path {
			matched = append(matched, request)
		}
	}
	return matched
}

// requestCount counts the requests of method to path since the last clear.
func (f *m5fFake) requestCount(method, path string) int {
	return len(f.requestsTo(method, path))
}

// tradeRequests returns the bodies of the trade requests of action.
func (f *m5fFake) tradeRequests(action int) []map[string]any {
	var bodies []map[string]any
	for _, request := range f.requestsTo("POST", "/order") {
		if a, _ := request.body["action"].(float64); int(a) == action {
			bodies = append(bodies, request.body)
		}
	}
	return bodies
}

func (f *m5fFake) clearRequests() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
}

// liveTicket is the ticket of the position, which the trade requests take:
// its identifier unless a service operation gave it another (reticket).
func (p *m5fPosition) liveTicket() int64 {
	if p.live != 0 {
		return p.live
	}
	return p.ticket
}

// byTicketLocked returns the open position of a ticket of a trade request:
// never by its identifier once its ticket differs, as on MT5.
func (f *m5fFake) byTicketLocked(ticket int64) (*m5fPosition, bool) {
	for _, p := range f.positions {
		if p.liveTicket() == ticket {
			return p, true
		}
	}
	return nil, false
}

// reticket gives an open position a new ticket, its identifier kept, as a
// service operation of the server does (eg: a swap charged by reopening it),
// and returns it.
func (f *m5fFake) reticket(identifier int64) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.positions[identifier]
	require.True(f.t, ok, "no position %d", identifier)
	p.live = f.newTicketLocked()
	return p.live
}

// position returns a copy of an open position.
func (f *m5fFake) position(ticket int64) (m5fPosition, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.positions[ticket]
	if !ok {
		return m5fPosition{}, false
	}
	return *p, true
}

// pending returns a copy of a pending order.
func (f *m5fFake) pending(ticket int64) (m5fOrder, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orders[ticket]
	if !ok {
		return m5fOrder{}, false
	}
	return *o, true
}

// positionsOf returns the open positions of magic of symbol, by ticket.
func (f *m5fFake) positionsOf(symbol string, magic int64) []m5fPosition {
	f.mu.Lock()
	defer f.mu.Unlock()
	var positions []m5fPosition
	for _, p := range f.positions {
		if p.symbol == symbol && p.magic == magic {
			positions = append(positions, *p)
		}
	}
	sort.Slice(positions, func(i, j int) bool { return positions[i].ticket < positions[j].ticket })
	return positions
}

// netLots returns the net volume of the positions of magic of symbol, negative
// when short.
func (f *m5fFake) netLots(symbol string, magic int64) float64 {
	var net float64
	for _, p := range f.positionsOf(symbol, magic) {
		if p.kind == mt5TypeSell {
			net -= p.volume
		} else {
			net += p.volume
		}
	}
	return math.Round(net*1e8) / 1e8
}

// openCount counts the open positions of the account.
func (f *m5fFake) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.positions)
}

// dealsOf returns the deals of a position.
func (f *m5fFake) dealsOf(position int64) []m5fDeal {
	f.mu.Lock()
	defer f.mu.Unlock()
	var deals []m5fDeal
	for _, d := range f.deals {
		if d.positionID == position {
			deals = append(deals, *d)
		}
	}
	return deals
}

// openPosition opens a position directly, eg: a manual trade (magic 0) or one
// of another bot, at the current price.
func (f *m5fFake) openPosition(symbol string, kind int, volume float64, magic int64, comment string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	reason := mt5ReasonExpert
	if magic == 0 {
		reason = mt5ReasonClient
	}
	return f.openLocked(symbol, kind, volume, magic, comment, f.marketPriceLocked(symbol, kind), 0, 0, 0,
		reason).ticket
}

// placePending places a pending order directly, eg: one of a previous run.
func (f *m5fFake) placePending(symbol string, kind int, volume, price, sl, tp float64, magic int64) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := &m5fOrder{ticket: f.newTicketLocked(), setupMsc: f.nowMs(), kind: kind, typeFilling: mt5FillReturn,
		state: mt5StatePlaced, magic: magic, volumeInitial: volume, volumeCurrent: volume, priceOpen: price, sl: sl,
		tp: tp, symbol: symbol, comment: "nb:test"}
	f.orders[o.ticket] = o
	return o.ticket
}

// fillPending fills a pending order at its price, the rest of it when it was
// filled in part: it opens a position of its ticket, with its levels, magic
// and comment, or adds to it.
func (f *m5fFake) fillPending(ticket int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orders[ticket]
	require.True(f.t, ok, "no pending order %d", ticket)
	volume := o.volumeCurrent
	if volume <= 0 {
		volume = o.volumeInitial
	}
	delete(f.orders, ticket)
	o.state, o.doneMsc, o.positionID, o.volumeCurrent, o.priceCurrent = mt5StateFilled, f.nowMs(), ticket, 0,
		o.priceOpen
	f.history = append(f.history, o)
	f.executePendingLocked(o, volume)
}

// fillPendingPartly fills volume of a pending order at its price: its position
// opened or added to, the order left in the book with the rest, partially
// filled.
func (f *m5fFake) fillPendingPartly(ticket int64, volume float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orders[ticket]
	require.True(f.t, ok, "no pending order %d", ticket)
	require.Less(f.t, volume, o.volumeCurrent, "a part of the order %d", ticket)
	o.volumeCurrent = math.Round((o.volumeCurrent-volume)*1e8) / 1e8
	o.state, o.positionID, o.priceCurrent = mt5StatePartial, ticket, o.priceOpen
	f.executePendingLocked(o, volume)
}

// executePendingLocked executes volume of a pending order: the position of its
// ticket opened or added to, and the entry deal.
func (f *m5fFake) executePendingLocked(o *m5fOrder, volume float64) {
	p, ok := f.positions[o.ticket]
	if !ok {
		p = &m5fPosition{ticket: o.ticket, timeMsc: f.nowMs(), kind: o.kind % 2, magic: o.magic,
			reason: mt5ReasonExpert, priceOpen: o.priceOpen, sl: o.sl, tp: o.tp, symbol: o.symbol, comment: o.comment}
		f.positions[o.ticket] = p
	}
	p.volume = math.Round((p.volume+volume)*1e8) / 1e8
	f.dealLocked(o.ticket, p, p.kind, mt5EntryIn, mt5ReasonExpert, volume, o.priceOpen, o.magic, o.comment)
}

// setPendingState sets the state of a pending order of the book, eg: PLACED
// while executed in part, as a server may list it.
func (f *m5fFake) setPendingState(ticket int64, state int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orders[ticket]
	require.True(f.t, ok, "no pending order %d", ticket)
	o.state = state
}

// expirePending expires a pending order.
func (f *m5fFake) expirePending(ticket int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orders[ticket]
	require.True(f.t, ok, "no pending order %d", ticket)
	delete(f.orders, ticket)
	o.state, o.doneMsc = mt5StateExpired, f.nowMs()
	f.history = append(f.history, o)
}

// cancelPendingByHand cancels a pending order from the terminal.
func (f *m5fFake) cancelPendingByHand(ticket int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orders[ticket]
	require.True(f.t, ok, "no pending order %d", ticket)
	delete(f.orders, ticket)
	o.state, o.doneMsc = mt5StateCanceled, f.nowMs()
	f.history = append(f.history, o)
}

// triggerSL closes a position by its stop loss, at price (its level when 0),
// as the server does: an exit of reason SL commented "[sl level]".
func (f *m5fFake) triggerSL(ticket int64, price float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.positions[ticket]
	require.True(f.t, ok, "no position %d", ticket)
	require.Positive(f.t, p.sl, "the position %d has no stop loss", ticket)
	if price == 0 {
		price = p.sl
	}
	f.exitLocked(p, p.volume, price, mt5ReasonSL, fmt.Sprintf("[sl %v]", p.sl), p.magic)
}

// triggerTP closes a position by its take profit, at its level.
func (f *m5fFake) triggerTP(ticket int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.positions[ticket]
	require.True(f.t, ok, "no position %d", ticket)
	require.Positive(f.t, p.tp, "the position %d has no take profit", ticket)
	f.exitLocked(p, p.volume, p.tp, mt5ReasonTP, fmt.Sprintf("[tp %v]", p.tp), p.magic)
}

// stopOut closes positions at the current price, reason stop out.
func (f *m5fFake) stopOut(tickets ...int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ticket := range tickets {
		p, ok := f.positions[ticket]
		require.True(f.t, ok, "no position %d", ticket)
		f.exitLocked(p, p.volume, f.closePriceLocked(p), mt5ReasonSO, "[so 0.00%/-61.62/0.00]", p.magic)
	}
}

// closeByHand closes volume of a position from the terminal (reason client,
// magic 0), at the current price.
func (f *m5fFake) closeByHand(ticket int64, volume float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.positions[ticket]
	require.True(f.t, ok, "no position %d", ticket)
	f.exitLocked(p, volume, f.closePriceLocked(p), mt5ReasonClient, "", 0)
}

// setLevelsByHand sets the levels of a position from the terminal.
func (f *m5fFake) setLevelsByHand(ticket int64, sl, tp float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.positions[ticket]
	require.True(f.t, ok, "no position %d", ticket)
	p.sl, p.tp = sl, tp
}

func (f *m5fFake) marketPriceLocked(symbol string, kind int) float64 {
	tick := f.ticks[symbol]
	if kind == mt5TypeSell {
		return tick.bid
	}
	return tick.ask
}

// closePriceLocked is the price closing a position: the bid for a buy, the ask
// for a sell.
func (f *m5fFake) closePriceLocked(p *m5fPosition) float64 {
	return f.marketPriceLocked(p.symbol, 1-p.kind)
}

func (f *m5fFake) round(symbol string, value float64) float64 {
	return mt5Round(value, f.symbols[symbol].digits)
}

// openLocked opens a position of an order of its own ticket, filled, with its
// entry deal.
func (f *m5fFake) openLocked(symbol string, kind int, volume float64, magic int64, comment string, price, sl,
	tp float64, filling, reason int) *m5fPosition {
	ticket := f.newTicketLocked()
	now := f.nowMs()
	f.history = append(f.history, &m5fOrder{ticket: ticket, setupMsc: now, doneMsc: now, kind: kind,
		typeFilling: filling, state: mt5StateFilled, magic: magic, positionID: ticket, reason: reason,
		volumeInitial: volume, priceOpen: price, priceCurrent: price, sl: sl, tp: tp, symbol: symbol,
		comment: comment})
	p := &m5fPosition{ticket: ticket, timeMsc: now, kind: kind, magic: magic, reason: reason, volume: volume,
		priceOpen: price, sl: sl, tp: tp, symbol: symbol, comment: comment}
	f.positions[ticket] = p
	p.timeMsc = now
	f.dealLocked(ticket, p, kind, mt5EntryIn, reason, volume, price, magic, comment)
	return p
}

// dealLocked records a deal of the position p.
func (f *m5fFake) dealLocked(order int64, p *m5fPosition, kind, entry, reason int, volume, price float64,
	magic int64, comment string) *m5fDeal {
	d := &m5fDeal{ticket: f.newDealLocked(), order: order, timeMsc: f.nowMs(), kind: kind, entry: entry,
		magic: magic, positionID: p.ticket, reason: reason, volume: volume, price: price, symbol: p.symbol,
		comment: comment}
	if entry == mt5EntryIn {
		d.commission = math.Round(f.commission*volume*100) / 100
	} else {
		sign := 1.0
		if p.kind == mt5TypeSell {
			sign = -1
		}
		d.profit = math.Round(sign*(price-p.priceOpen)*volume*f.symbols[p.symbol].contract*100) / 100
	}
	f.deals = append(f.deals, d)
	return d
}

// exitLocked closes volume of a position with an order of its own and its exit
// deal, the position leaving when nothing is left.
func (f *m5fFake) exitLocked(p *m5fPosition, volume, price float64, reason int, comment string,
	magic int64) (*m5fOrder, *m5fDeal) {
	now := f.nowMs()
	order := &m5fOrder{ticket: f.newTicketLocked(), setupMsc: now, doneMsc: now, kind: 1 - p.kind,
		typeFilling: mt5FillIOC, state: mt5StateFilled, magic: magic, positionID: p.ticket, reason: reason,
		volumeInitial: volume, priceOpen: price, priceCurrent: price, symbol: p.symbol, comment: comment}
	f.history = append(f.history, order)
	deal := f.dealLocked(order.ticket, p, 1-p.kind, mt5EntryOut, reason, volume, price, magic, comment)
	f.reduceLocked(p, volume)
	return order, deal
}

func (f *m5fFake) reduceLocked(p *m5fPosition, volume float64) {
	step := f.symbols[p.symbol].step
	left := math.Round((p.volume-volume)/step) * step
	left = math.Round(left*1e8) / 1e8
	if left <= 0 {
		delete(f.positions, p.ticket)
		return
	}
	p.volume = left
}

// serveHTTP answers a request as the bridge does.
func (f *m5fFake) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	hold := f.hold
	if hold != nil && hold.method == r.Method && hold.path == r.URL.Path && r.URL.Query().Has(hold.key) {
		f.hold = nil
	} else {
		hold = nil
	}
	f.mu.Unlock()
	if hold != nil {
		close(hold.reached)
		<-hold.released
	}
	received := time.Now()
	data, _ := io.ReadAll(r.Body)
	if r.Method == http.MethodPost && r.URL.Path == "/order" {
		f.mu.Lock()
		delay := f.tradeDelay
		f.mu.Unlock()
		time.Sleep(delay) // waiting for the terminal
	}
	f.mu.Lock()
	request := m5fRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
		deadline: r.Header.Get(MT5DeadlineHeader), timeout: r.Header.Get(MT5TimeoutHeader)}
	if r.Method == http.MethodPost {
		_ = json.Unmarshal(data, &request.body)
	}
	f.requests = append(f.requests, request)

	reply := func(status int, payload any) {
		f.mu.Unlock()
		body, _ := json.Marshal(payload)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
	if f.token != "" && r.Header.Get(MT5TokenHeader) != f.token {
		reply(http.StatusUnauthorized, map[string]any{"error": "invalid X-Bridge-Token"})
		return
	}
	for i, failure := range f.failures {
		if failure.method == r.Method && failure.path == r.URL.Path {
			f.failures = append(f.failures[:i], f.failures[i+1:]...)
			f.mu.Unlock()
			w.WriteHeader(failure.status)
			_, _ = w.Write([]byte(failure.body))
			return
		}
	}
	if failure, ok := f.failing[r.Method+" "+r.URL.Path]; ok {
		f.mu.Unlock()
		w.WriteHeader(failure.status)
		_, _ = w.Write([]byte(failure.body))
		return
	}

	query := r.URL.Query()
	var after func()
	lost := false
	status, payload := http.StatusOK, any(nil)
	switch r.Method + " " + r.URL.Path {
	case "GET /health":
		health := make(map[string]any, len(f.health))
		for key, value := range f.health {
			health[key] = value
		}
		health["time"] = float64(time.Now().UnixNano()) / 1e9
		payload = health
	case "GET /account":
		payload = maps.Clone(f.account) // written after the lock is released
	case "GET /symbol":
		symbol, ok := f.symbols[query.Get("symbol")]
		if !ok {
			status, payload = http.StatusNotFound, map[string]any{"error": "no symbol " + query.Get("symbol")}
			break
		}
		raw := make(map[string]any, len(symbol.raw))
		for key, value := range symbol.raw {
			raw[key] = value
		}
		tick := f.ticks[query.Get("symbol")]
		raw["bid"], raw["ask"] = tick.bid, tick.ask
		payload = raw
	case "GET /tick":
		tick, ok := f.ticks[query.Get("symbol")]
		if !ok {
			status, payload = http.StatusBadGateway, map[string]any{"error": "symbol_info_tick " +
				query.Get("symbol") + ": Terminal: Call failed", "retcode": -1}
			break
		}
		payload = map[string]any{"time": tick.timeMsc / 1000, "bid": tick.bid, "ask": tick.ask, "last": 0.0,
			"volume": 0, "time_msc": tick.timeMsc, "flags": 6, "volume_real": 0.0}
	case "GET /rates":
		status, payload = f.ratesLocked(query)
	case "GET /positions":
		payload = f.positionsJSONLocked(query.Get("symbol"))
	case "GET /orders":
		payload = f.ordersJSONLocked(query.Get("symbol"))
		after, f.afterSnapshot = f.afterSnapshot, nil
	case "GET /deals":
		status, payload = f.dealsLocked(query)
	case "GET /history_orders":
		status, payload = f.historyLocked(query)
	case "POST /order":
		if status, payload = f.refuseTradeLocked(request, received); status == 0 {
			status, payload = f.tradeLocked(r.URL.Path, request.body, data)
			if lost = f.loseAnswers > 0; lost {
				f.loseAnswers--
			}
		}
	case "POST /check":
		status, payload = f.tradeLocked(r.URL.Path, request.body, data)
	default:
		status, payload = http.StatusNotFound, map[string]any{"error": "no route " + r.Method + " " + r.URL.Path}
	}
	if hook, ok := f.after[r.Method+" "+r.URL.Path]; ok {
		delete(f.after, r.Method+" "+r.URL.Path)
		if previous := after; previous != nil {
			after = func() { previous(); hook() }
		} else {
			after = hook
		}
	}
	if lost {
		f.mu.Unlock()
		conn, _, err := w.(http.Hijacker).Hijack()
		require.NoError(f.t, err)
		_ = conn.Close()
		return
	}
	reply(status, payload)
	if after != nil {
		after()
	}
}

func m5fQueryInt(query map[string][]string, name string) (int64, bool) {
	values := query[name]
	if len(values) == 0 {
		return 0, false
	}
	value, err := strconv.ParseInt(values[0], 10, 64)
	return value, err == nil
}

func (f *m5fFake) ratesLocked(query map[string][]string) (int, any) {
	symbol := ""
	if values := query["symbol"]; len(values) > 0 {
		symbol = values[0]
	}
	if symbol == "" {
		return http.StatusBadRequest, map[string]any{"error": "missing symbol"}
	}
	timeframe := "M1"
	if values := query["timeframe"]; len(values) > 0 {
		timeframe = values[0]
	}
	if !m5fTimeframes[timeframe] {
		return http.StatusBadRequest, map[string]any{"error": "invalid timeframe " + timeframe}
	}
	rates := f.rates[symbol+"|"+timeframe]
	if _, ok := query["from"]; ok {
		from, ok1 := m5fQueryInt(query, "from")
		to, ok2 := m5fQueryInt(query, "to")
		if !ok1 || !ok2 {
			return http.StatusBadRequest, map[string]any{"error": "missing to"}
		}
		selected := []mt5Rate{}
		for _, rate := range rates {
			if rate.Time >= from && rate.Time <= to {
				selected = append(selected, rate)
			}
		}
		return http.StatusOK, selected
	}
	count, ok := m5fQueryInt(query, "count")
	if !ok {
		return http.StatusBadRequest, map[string]any{"error": "missing count"}
	}
	if count <= 0 || count > mt5MaxRates {
		return http.StatusBadRequest, map[string]any{"error": "count must be in (0, 100000]"}
	}
	start, _ := m5fQueryInt(query, "start")
	end := len(rates) - int(start)
	if end < 0 {
		end = 0
	}
	first := end - int(count)
	if first < 0 {
		first = 0
	}
	return http.StatusOK, append([]mt5Rate{}, rates[first:end]...)
}

func (f *m5fFake) positionJSONLocked(p *m5fPosition) map[string]any {
	current := f.closePriceLocked(p)
	sign := 1.0
	if p.kind == mt5TypeSell {
		sign = -1
	}
	return map[string]any{
		"ticket": p.liveTicket(), "time": p.timeMsc / 1000, "time_msc": p.timeMsc, "time_update": p.timeMsc / 1000,
		"time_update_msc": p.timeMsc, "type": p.kind, "magic": p.magic, "identifier": p.ticket, "reason": p.reason,
		"volume": p.volume, "price_open": p.priceOpen, "sl": p.sl, "tp": p.tp, "price_current": current,
		"swap": 0.0, "profit": math.Round(sign*(current-p.priceOpen)*p.volume*f.symbols[p.symbol].contract*100) / 100,
		"symbol": p.symbol, "comment": p.comment, "external_id": "",
	}
}

func (f *m5fFake) positionsJSONLocked(symbol string) []map[string]any {
	tickets := make([]int64, 0, len(f.positions))
	for ticket, p := range f.positions {
		if (symbol == "" || p.symbol == symbol) && !f.hidden[ticket] {
			tickets = append(tickets, ticket)
		}
	}
	sort.Slice(tickets, func(i, j int) bool { return tickets[i] < tickets[j] })
	positions := make([]map[string]any, 0, len(tickets))
	for _, ticket := range tickets {
		positions = append(positions, f.positionJSONLocked(f.positions[ticket]))
	}
	return positions
}

func m5fOrderJSON(o *m5fOrder) map[string]any {
	return map[string]any{
		"ticket": o.ticket, "time_setup": o.setupMsc / 1000, "time_setup_msc": o.setupMsc,
		"time_done": o.doneMsc / 1000, "time_done_msc": o.doneMsc, "time_expiration": o.expiration, "type": o.kind,
		"type_time": o.typeTime, "type_filling": o.typeFilling, "state": o.state, "magic": o.magic,
		"position_id": o.positionID, "position_by_id": o.positionByID, "reason": o.reason,
		"volume_initial": o.volumeInitial, "volume_current": o.volumeCurrent, "price_open": o.priceOpen, "sl": o.sl,
		"tp": o.tp, "price_current": o.priceCurrent, "price_stoplimit": 0.0, "symbol": o.symbol,
		"comment": o.comment, "external_id": "",
	}
}

func (f *m5fFake) ordersJSONLocked(symbol string) []map[string]any {
	tickets := make([]int64, 0, len(f.orders))
	for ticket, o := range f.orders {
		if symbol == "" || o.symbol == symbol {
			tickets = append(tickets, ticket)
		}
	}
	sort.Slice(tickets, func(i, j int) bool { return tickets[i] < tickets[j] })
	orders := make([]map[string]any, 0, len(tickets))
	for _, ticket := range tickets {
		o := f.orders[ticket]
		o.priceCurrent = f.marketPriceLocked(o.symbol, o.kind%2)
		orders = append(orders, m5fOrderJSON(o))
	}
	return orders
}

func (f *m5fFake) historyRange(query map[string][]string) (int64, int64, bool) {
	from, ok1 := m5fQueryInt(query, "from")
	to, ok2 := m5fQueryInt(query, "to")
	return from, to, ok1 && ok2
}

func (f *m5fFake) dealsLocked(query map[string][]string) (int, any) {
	from, to, ok := f.historyRange(query)
	if !ok {
		return http.StatusBadRequest, map[string]any{"error": "missing from"}
	}
	deals := []map[string]any{}
	for _, d := range f.deals {
		if f.held && d.ticket > f.heldDeal {
			continue
		}
		if at := d.timeMsc / 1000; at >= from && at <= to {
			deals = append(deals, map[string]any{
				"ticket": d.ticket, "order": d.order, "time": d.timeMsc / 1000, "time_msc": d.timeMsc, "type": d.kind,
				"entry": d.entry, "magic": d.magic, "position_id": d.positionID, "reason": d.reason,
				"volume": d.volume, "price": d.price, "commission": d.commission, "swap": 0.0, "profit": d.profit,
				"fee": 0.0, "symbol": d.symbol, "comment": d.comment, "external_id": "",
			})
		}
	}
	return http.StatusOK, deals
}

// historyLocked lists the orders of the history by their setup time.
func (f *m5fFake) historyLocked(query map[string][]string) (int, any) {
	from, to, ok := f.historyRange(query)
	if !ok {
		return http.StatusBadRequest, map[string]any{"error": "missing from"}
	}
	orders := []map[string]any{}
	for _, o := range f.history {
		if f.held && o.ticket > f.heldTicket {
			continue
		}
		if at := o.setupMsc / 1000; at >= from && at <= to {
			orders = append(orders, m5fOrderJSON(o))
		}
	}
	return http.StatusOK, orders
}

// refuseTradeLocked refuses a trade request received at received before
// order_send, as the bridge does, unsent: its deadline passed, the earliest of
// MT5DeadlineHeader and of MT5TimeoutHeader from its receipt, or the terminal
// logged in another account than its --login. 0 when it goes on.
func (f *m5fFake) refuseTradeLocked(request m5fRequest, received time.Time) (int, any) {
	var deadlines []time.Time
	if request.deadline != "" {
		ms, err := strconv.ParseInt(request.deadline, 10, 64)
		if err != nil {
			return http.StatusBadRequest, map[string]any{"error": "invalid " + MT5DeadlineHeader, "sent": false}
		}
		deadlines = append(deadlines, time.UnixMilli(ms))
	}
	if request.timeout != "" {
		ms, err := strconv.ParseInt(request.timeout, 10, 64)
		if err != nil {
			return http.StatusBadRequest, map[string]any{"error": "invalid " + MT5TimeoutHeader, "sent": false}
		}
		deadlines = append(deadlines, received.Add(time.Duration(ms)*time.Millisecond))
	}
	for _, deadline := range deadlines {
		if !time.Now().Before(deadline) {
			return http.StatusGatewayTimeout, map[string]any{
				"error": "the deadline of the request passed waiting for the terminal: not sent to it", "sent": false}
		}
	}
	if login := m5fInt(f.account["login"]); login != f.login {
		return http.StatusConflict, map[string]any{
			"error": fmt.Sprintf("the terminal is logged in %d, not %d: trade request refused", login, f.login),
			"sent":  false}
	}
	return 0, nil
}

// m5fInt reads an integer of the account, decoded from JSON or set by a test.
func m5fInt(value any) int64 {
	switch n := value.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

// m5fNumber reads a number of a trade request.
func m5fNumber(body map[string]any, name string) float64 {
	value, _ := body[name].(float64)
	return value
}

// tradeLocked handles POST /order (order_send) and POST /check (order_check).
func (f *m5fFake) tradeLocked(path string, body map[string]any, raw []byte) (int, any) {
	if read, _ := f.health["read_only"].(bool); read {
		return http.StatusForbidden, map[string]any{"error": "the bridge is read-only (--read-only)"}
	}
	if body == nil {
		if err := json.Unmarshal(raw, &body); err != nil {
			return http.StatusBadRequest, map[string]any{"error": "invalid JSON: " + err.Error()}
		}
	}
	var unknown []string
	for key := range body {
		if !m5fRequestFields[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return http.StatusBadRequest, map[string]any{"error": "unknown fields: " + strings.Join(unknown, ", ")}
	}
	if _, ok := body["action"]; !ok {
		return http.StatusBadRequest, map[string]any{"error": "missing action"}
	}
	result := map[string]any{"retcode": mt5CodeDone, "deal": 0, "order": 0, "volume": 0.0, "price": 0.0,
		"bid": 0.0, "ask": 0.0, "comment": "Request executed", "request_id": 1, "retcode_external": 0,
		"request": body}
	if path == "/check" {
		result["retcode"], result["comment"] = 0, "Done"
		return http.StatusOK, result
	}
	f.trades++
	if next, ok := f.resultsAt[f.trades]; ok {
		delete(f.resultsAt, f.trades)
		result["retcode"], result["comment"] = next.retcode, next.comment
		return http.StatusOK, result
	}
	if len(f.results) > 0 {
		next := f.results[0]
		f.results = f.results[1:]
		result["retcode"], result["comment"] = next.retcode, next.comment
		return http.StatusOK, result
	}
	code, comment := f.executeLocked(body, result)
	if f.withoutDeal[f.trades] {
		delete(f.withoutDeal, f.trades)
		result["deal"] = 0
	}
	if code != mt5CodeDone && code != mt5CodePlaced {
		result["retcode"], result["comment"] = code, comment
	}
	return http.StatusOK, result
}

// executeLocked executes a trade request as a hedging account does, filling
// result, and returns its retcode.
func (f *m5fFake) executeLocked(body map[string]any, result map[string]any) (int, string) {
	action := int(m5fNumber(body, "action"))
	symbolName, _ := body["symbol"].(string)
	magic := int64(m5fNumber(body, "magic"))
	comment, _ := body["comment"].(string)
	if len(comment) > mt5MaxComment {
		comment = comment[:mt5MaxComment]
	}
	if i := strings.LastIndex(comment, "#"); f.stripTags && i >= 0 {
		comment = comment[:i]
	}

	switch action {
	case mt5ActionDeal:
		symbol, ok := f.symbols[symbolName]
		if !ok {
			return mt5CodeInvalid, "Invalid request"
		}
		if f.marketClosed {
			return mt5CodeMarketClosed, "Market closed"
		}
		kind := int(m5fNumber(body, "type"))
		volume := m5fNumber(body, "volume")
		if code := f.checkVolumeLocked(symbol, volume); code != 0 {
			return code, "Invalid volume"
		}
		filling := int(m5fNumber(body, "type_filling"))
		if !f.fillingAllowed(symbol, filling, true) {
			return mt5CodeInvalidFill, "Unsupported filling mode"
		}
		price := f.marketPriceLocked(symbolName, kind)
		if ticket := int64(m5fNumber(body, "position")); ticket != 0 {
			p, ok := f.byTicketLocked(ticket)
			switch {
			case !ok || p.symbol != symbolName || p.kind == kind:
				return mt5CodeInvalid, "Invalid request"
			case volume > p.volume+1e-9:
				return mt5CodeInvalidVolume, "Invalid volume"
			}
			order, deal := f.exitLocked(p, volume, price, mt5ReasonExpert, comment, magic)
			result["order"], result["deal"] = order.ticket, deal.ticket
		} else {
			sl, tp := m5fNumber(body, "sl"), m5fNumber(body, "tp")
			if !f.levelsValid(kind, f.closeReference(symbolName, kind), sl, tp) {
				return mt5CodeInvalidStops, "Invalid stops"
			}
			p := f.openLocked(symbolName, kind, volume, magic, comment, price, sl, tp, filling, mt5ReasonExpert)
			result["order"], result["deal"] = p.ticket, f.deals[len(f.deals)-1].ticket
		}
		result["volume"], result["price"] = volume, price
		if f.zeroPrice {
			result["price"] = 0.0
		}
		tick := f.ticks[symbolName]
		result["bid"], result["ask"] = tick.bid, tick.ask
		if f.drift != 0 {
			tick.bid, tick.ask = f.round(symbolName, tick.bid+f.drift), f.round(symbolName, tick.ask+f.drift)
			f.ticks[symbolName] = tick
		}
		return mt5CodeDone, ""
	case mt5ActionPending:
		symbol, ok := f.symbols[symbolName]
		if !ok {
			return mt5CodeInvalid, "Invalid request"
		}
		kind := int(m5fNumber(body, "type"))
		volume, price := m5fNumber(body, "volume"), m5fNumber(body, "price")
		sl, tp := m5fNumber(body, "sl"), m5fNumber(body, "tp")
		switch {
		case !mt5PendingKind(kind):
			return mt5CodeInvalid, "Invalid request"
		case f.checkVolumeLocked(symbol, volume) != 0:
			return mt5CodeInvalidVolume, "Invalid volume"
		case !f.fillingAllowed(symbol, int(m5fNumber(body, "type_filling")), false):
			return mt5CodeInvalidFill, "Unsupported filling mode"
		case !f.pendingPriceValid(symbolName, kind, price):
			return mt5CodeInvalidPrice, "Invalid price"
		case !f.levelsValid(kind%2, price, sl, tp):
			return mt5CodeInvalidStops, "Invalid stops"
		}
		o := &m5fOrder{ticket: f.newTicketLocked(), setupMsc: f.nowMs(), kind: kind,
			typeTime: int(m5fNumber(body, "type_time")), typeFilling: int(m5fNumber(body, "type_filling")),
			state: mt5StatePlaced, magic: magic, volumeInitial: volume, volumeCurrent: volume, priceOpen: price,
			sl: sl, tp: tp, symbol: symbolName, comment: comment, expiration: int64(m5fNumber(body, "expiration"))}
		f.orders[o.ticket] = o
		result["order"], result["volume"], result["price"] = o.ticket, volume, price
		return mt5CodeDone, ""
	case mt5ActionSLTP:
		p, ok := f.byTicketLocked(int64(m5fNumber(body, "position")))
		if !ok {
			return mt5CodeInvalid, "Invalid request"
		}
		sl, tp := m5fNumber(body, "sl"), m5fNumber(body, "tp")
		if !f.levelsValid(p.kind, f.closeReference(p.symbol, p.kind), sl, tp) {
			return mt5CodeInvalidStops, "Invalid stops"
		}
		p.sl, p.tp = sl, tp
		return mt5CodeDone, ""
	case mt5ActionModify:
		o, ok := f.orders[int64(m5fNumber(body, "order"))]
		if !ok {
			return mt5CodeInvalid, "Invalid request"
		}
		price, sl, tp := m5fNumber(body, "price"), m5fNumber(body, "sl"), m5fNumber(body, "tp")
		switch {
		case !f.pendingPriceValid(o.symbol, o.kind, price):
			return mt5CodeInvalidPrice, "Invalid price"
		case !f.levelsValid(o.kind%2, price, sl, tp):
			return mt5CodeInvalidStops, "Invalid stops"
		}
		o.priceOpen, o.sl, o.tp = price, sl, tp
		o.typeTime, o.expiration = int(m5fNumber(body, "type_time")), int64(m5fNumber(body, "expiration"))
		return mt5CodeDone, ""
	case mt5ActionRemove:
		o, ok := f.orders[int64(m5fNumber(body, "order"))]
		if !ok {
			return mt5CodeInvalid, "Invalid request"
		}
		delete(f.orders, o.ticket)
		o.state, o.doneMsc = mt5StateCanceled, f.nowMs()
		f.history = append(f.history, o)
		result["order"] = o.ticket
		return mt5CodeDone, ""
	case mt5ActionCloseBy:
		p, ok1 := f.byTicketLocked(int64(m5fNumber(body, "position")))
		by, ok2 := f.byTicketLocked(int64(m5fNumber(body, "position_by")))
		if !ok1 || !ok2 || p.symbol != by.symbol || p.kind == by.kind {
			return mt5CodeInvalid, "Invalid request"
		}
		volume := math.Min(p.volume, by.volume)
		now := f.nowMs()
		order := &m5fOrder{ticket: f.newTicketLocked(), setupMsc: now, doneMsc: now, kind: mt5TypeCloseBy,
			state: mt5StateFilled, magic: magic, positionID: p.ticket, positionByID: by.ticket,
			reason: mt5ReasonExpert, volumeInitial: volume, priceOpen: by.priceOpen, symbol: p.symbol,
			comment: comment}
		f.history = append(f.history, order)
		// each closed at the open price of the other, the profit of the
		// difference split between them (see closeBySplit)
		first := f.dealLocked(order.ticket, p, 1-p.kind, mt5EntryOutBy, mt5ReasonExpert, volume, by.priceOpen,
			magic, comment)
		second := f.dealLocked(order.ticket, by, 1-by.kind, mt5EntryOutBy, mt5ReasonExpert, volume, p.priceOpen,
			magic, comment)
		share := f.closeBySplit
		switch share {
		case 0:
			share = 1
		case m5fCloseBySplitBy:
			share = 0
		}
		profit := first.profit
		first.profit = math.Round(profit*share*100) / 100
		second.profit = math.Round((profit-first.profit)*100) / 100
		f.reduceLocked(p, volume)
		f.reduceLocked(by, volume)
		result["order"], result["deal"], result["volume"] = order.ticket, first.ticket, volume
		return mt5CodeDone, ""
	}
	return mt5CodeInvalid, "Invalid request"
}

// closeReference is the price the levels of a position of kind are checked
// against: the bid for a buy, the ask for a sell.
func (f *m5fFake) closeReference(symbol string, kind int) float64 {
	return f.marketPriceLocked(symbol, 1-kind)
}

func (f *m5fFake) checkVolumeLocked(symbol *m5fSymbol, volume float64) int {
	steps := volume / symbol.step
	if volume < symbol.min-1e-12 || volume > symbol.max+1e-12 || math.Abs(steps-math.Round(steps)) > 1e-6 {
		return mt5CodeInvalidVolume
	}
	return 0
}

// fillingAllowed checks the filling of a request: fill or kill and immediate
// or cancel as the flags of the symbol allow them, return for the pending
// orders only (market execution).
func (f *m5fFake) fillingAllowed(symbol *m5fSymbol, filling int, market bool) bool {
	switch filling {
	case mt5FillFOK:
		return symbol.filling&mt5FillingFlagFOK != 0
	case mt5FillIOC:
		return symbol.filling&mt5FillingFlagIOC != 0
	case mt5FillReturn:
		return !market
	}
	return false
}

// levelsValid checks the levels of a position of kind (buy 0, sell 1) against
// reference: the stop loss on the losing side, the take profit on the winning
// one.
func (f *m5fFake) levelsValid(kind int, reference, sl, tp float64) bool {
	if kind == mt5TypeBuy {
		return (sl == 0 || sl < reference) && (tp == 0 || tp > reference)
	}
	return (sl == 0 || sl > reference) && (tp == 0 || tp < reference)
}

// pendingPriceValid checks the price of a pending order against the market:
// the limits below (buy) or above (sell) it, the stops beyond it.
func (f *m5fFake) pendingPriceValid(symbol string, kind int, price float64) bool {
	tick := f.ticks[symbol]
	switch kind {
	case mt5TypeBuyLimit:
		return price < tick.ask
	case mt5TypeSellLimit:
		return price > tick.bid
	case mt5TypeBuyStop:
		return price > tick.ask
	case mt5TypeSellStop:
		return price < tick.bid
	}
	return false
}

// m5fCandles returns count M-period candles ending with the one in progress
// at now, opened at multiples of period, prices from base.
func m5fCandles(count int, period time.Duration, base float64) []mt5Rate {
	current := time.Now().UTC().Truncate(period)
	rates := make([]mt5Rate, 0, count)
	for i := count - 1; i >= 0; i-- {
		start := current.Add(-time.Duration(i) * period)
		open := base + float64(count-i)
		rates = append(rates, mt5Rate{Time: start.Unix(), Open: open + 0.0000000001, High: open + 2,
			Low: open - 1, Close: open + 1.0000000000002, TickVolume: float64(100 + i), Spread: 260})
	}
	return rates
}

// MT5TestFake wraps the fake bridge for the end-to-end tests of package
// exchange_test (the order package imports exchange).
type MT5TestFake struct {
	f *m5fFake
}

// MT5TestHistoryWaits sets how long the outcome of an order or a position
// gone from the book is awaited (mt5ResolveWait) and the overlap of the reads
// of the history, for the tests of package exchange_test.
func MT5TestHistoryWaits(resolve, overlap time.Duration) MT5Option {
	return withMT5HistoryWaits(resolve, overlap)
}

// NewMT5TestFake starts a fake bridge, stopped when the test ends.
func NewMT5TestFake(t *testing.T) *MT5TestFake {
	return &MT5TestFake{f: newM5F(t)}
}

// Options returns the options of an adapter on the fake, then extra.
func (x *MT5TestFake) Options(extra ...MT5Option) []MT5Option { return x.f.options(extra...) }

// SetPrice sets the tick of a symbol.
func (x *MT5TestFake) SetPrice(symbol string, bid, ask float64) { x.f.setPrice(symbol, bid, ask) }

// FillPending fills a pending order at its price.
func (x *MT5TestFake) FillPending(ticket int64) { x.f.fillPending(ticket) }

// FillPendingPartly fills lots of a pending order at its price, the rest left
// in the book.
func (x *MT5TestFake) FillPendingPartly(ticket int64, lots float64) {
	x.f.fillPendingPartly(ticket, lots)
}

// TriggerSL closes a position by its stop loss at price, its level when 0.
func (x *MT5TestFake) TriggerSL(position int64, price float64) { x.f.triggerSL(position, price) }

// TriggerTP closes a position by its take profit.
func (x *MT5TestFake) TriggerTP(position int64) { x.f.triggerTP(position) }

// CloseByHand closes lots of a position from the terminal (reason client).
func (x *MT5TestFake) CloseByHand(position int64, lots float64) { x.f.closeByHand(position, lots) }

// HoldHistory leaves the history made from now on out of the lists of the
// bridge, as a terminal whose history lags, until ReleaseHistory.
func (x *MT5TestFake) HoldHistory() { x.f.holdHistory() }

// ReleaseHistory lists the history held since HoldHistory.
func (x *MT5TestFake) ReleaseHistory() { x.f.releaseHistory() }

// HoldNext holds the next request of method to path with key in its query,
// before it is handled, until release: reached is closed once it arrives.
func (x *MT5TestFake) HoldNext(method, path, key string) (reached <-chan struct{}, release func()) {
	return x.f.holdNext(method, path, key)
}

// FailReads answers every GET of path with a 503 of the terminal, until
// ClearFailedReads.
func (x *MT5TestFake) FailReads(path string) {
	x.f.setFailing(http.MethodGet, path, http.StatusServiceUnavailable, `{"error":"terminal busy"}`)
}

// ClearFailedReads ends FailReads of path.
func (x *MT5TestFake) ClearFailedReads(path string) { x.f.clearFailing(http.MethodGet, path) }

// MT5TestPoll reads the account into the book of m once, as its poll does,
// for the tests of package exchange_test whose adapter polls seldom.
func MT5TestPoll(m *MT5) error { return m.poll(m.ctx) }

// NetLots returns the net lots of the bot's positions of symbol.
func (x *MT5TestFake) NetLots(symbol string) float64 { return x.f.netLots(symbol, m5fMagic) }

// Positions counts the open positions of the account.
func (x *MT5TestFake) Positions() int { return x.f.openCount() }

// Requests counts the requests of method to path.
func (x *MT5TestFake) Requests(method, path string) int { return x.f.requestCount(method, path) }

// ClearRequests forgets the requests counted.
func (x *MT5TestFake) ClearRequests() { x.f.clearRequests() }

// RefuseNextTrade answers the next trade request with the retcode of a closed
// market, not executed.
func (x *MT5TestFake) RefuseNextTrade() { x.f.resultNext(mt5CodeMarketClosed, "Market closed") }

// FailNextTrade answers the next trade request with an error of the bridge
// after order_send, its outcome unknown to the adapter, not executed.
func (x *MT5TestFake) FailNextTrade() {
	x.f.failNext("POST", "/order", http.StatusBadGateway, `{"error":"order_send: IPC timeout","retcode":-10005}`)
}

// Levels returns the stop loss and the take profit of an open position.
func (x *MT5TestFake) Levels(position int64) (sl, tp float64, ok bool) {
	p, ok := x.f.position(position)
	return p.sl, p.tp, ok
}
