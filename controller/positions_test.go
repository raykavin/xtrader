//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// fakeBridge is a bridge of MT5 of the tests: its account and positions, the
// token it takes, and the requests it got.
type fakeBridge struct {
	*httptest.Server
	mu        sync.Mutex
	token     string
	login     int64
	currency  string
	balance   float64
	positions []bridgePosition
	orders    []bridgeOrder
	deals     []bridgeDeal
	status    int // answered instead, when set
	requests  int
	dealsRead []string // the queries of GET /deals
	tradeMode int      // of the account: 0 demo, 2 real
	trades    []map[string]any
	// tradeAnswer answers POST /order in place of the bridge when set, onTrade
	// is called with each trade request, before it, the bridge unlocked
	tradeAnswer func(request map[string]any) (int, any)
	onTrade     func(request map[string]any)
}

func newFakeBridge(t *testing.T, token string, login int64, currency string) *fakeBridge {
	t.Helper()
	b := &fakeBridge{token: token, login: login, currency: currency, balance: 1000}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var trade map[string]any
		if r.Method == http.MethodPost && r.URL.Path == "/order" {
			_ = json.NewDecoder(r.Body).Decode(&trade)
			b.mu.Lock()
			onTrade := b.onTrade
			b.mu.Unlock()
			if onTrade != nil {
				onTrade(trade)
			}
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		b.requests++
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Header.Get(exchange.MT5TokenHeader) != b.token:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid X-Bridge-Token"}`))
		case b.status != 0:
			w.WriteHeader(b.status)
			_, _ = w.Write([]byte(`{"error":"account_info: terminal disconnected"}`))
		case r.URL.Path == "/account":
			_ = json.NewEncoder(w).Encode(map[string]any{"login": b.login, "currency": b.currency, "balance": b.balance,
				"trade_mode": b.tradeMode})
		case r.URL.Path == "/orders":
			orders := b.orders
			if orders == nil {
				orders = []bridgeOrder{}
			}
			_ = json.NewEncoder(w).Encode(orders)
		case r.URL.Path == "/symbol":
			_ = json.NewEncoder(w).Encode(map[string]any{"digits": 3, "filling_mode": 1})
		case r.URL.Path == "/tick":
			_ = json.NewEncoder(w).Encode(map[string]any{"bid": 4160.0, "ask": 4160.2})
		case r.URL.Path == "/order":
			b.trades = append(b.trades, trade)
			var status int
			var answer any
			if b.tradeAnswer != nil {
				status, answer = b.tradeAnswer(trade)
			} else {
				status, answer = http.StatusOK, b.trade(trade)
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(answer)
		case r.URL.Path == "/positions":
			positions := b.positions
			if positions == nil {
				positions = []bridgePosition{}
			}
			_ = json.NewEncoder(w).Encode(positions)
		case r.URL.Path == "/deals":
			// as history_deals_get: the deals of the range, by their time
			b.dealsRead = append(b.dealsRead, r.URL.RawQuery)
			from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
			to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
			deals := []bridgeDeal{}
			for _, deal := range b.deals {
				if deal.Time >= from && deal.Time <= to {
					deals = append(deals, deal)
				}
			}
			_ = json.NewEncoder(w).Encode(deals)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(b.Close)
	return b
}

// trade executes a trade request as the terminal: a position closed by its
// ticket, a pending order removed. The caller holds b.mu.
func (b *fakeBridge) trade(request map[string]any) map[string]any {
	number := func(name string) int64 {
		value, _ := request[name].(float64)
		return int64(value)
	}
	switch number("action") {
	case 1:
		for i, p := range b.positions {
			if p.Ticket == number("position") {
				b.positions = append(b.positions[:i:i], b.positions[i+1:]...)
				return map[string]any{"retcode": 10009, "deal": 9000 + p.Ticket, "order": 8000 + p.Ticket}
			}
		}
	case 8:
		for i, o := range b.orders {
			if o.Ticket == number("order") {
				b.orders = append(b.orders[:i:i], b.orders[i+1:]...)
				return map[string]any{"retcode": 10009, "order": o.Ticket}
			}
		}
	}
	return map[string]any{"retcode": 10013, "comment": "Invalid request"}
}

// tradeRequests returns the trade requests the bridge got.
func (b *fakeBridge) tradeRequests() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]map[string]any(nil), b.trades...)
}

// held returns the positions of the account.
func (b *fakeBridge) held() []bridgePosition {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]bridgePosition(nil), b.positions...)
}

func (b *fakeBridge) set(positions ...bridgePosition) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.positions = positions
}

// position returns a position of the tests.
func position(ticket, magic int64, symbol string, sell bool, volume, profit, swap float64) bridgePosition {
	p := bridgePosition{Ticket: ticket, Magic: magic, Symbol: symbol, Volume: volume, PriceOpen: 4160,
		PriceCurrent: 4161, Profit: profit, Swap: swap}
	if sell {
		p.Type = 1
	}
	return p
}

// onBridge returns an edit of an agent of the tests: its account on the bridge
// at url, with magic, trading pairs, its token token.
func onBridge(url string, magic int64, pairs string) func(a *store.Agent) {
	return func(a *store.Agent) {
		a.Account = &store.Account{BridgeURL: url, Magic: &magic}
		a.Params = store.Params{"pair": pairs, "lot": "0.01"}
	}
}

// openPL reads GET /api/positions/open with call, see settingsSession.
func openPL(t *testing.T, call func(method, path string, body interface{}) (*http.Response, string)) ui.OpenPositionsResponse {
	t.Helper()
	resp, body := call(http.MethodGet, "/api/positions/open", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var response ui.OpenPositionsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	return response
}

// The open profit of the agents is the profit and the swap of the positions
// they hold open on their accounts, in BRL: every agent trading live, enabled
// or not, running or not, each position once whatever the agents and the URLs
// of its account, the closed ones and the ones of nobody left out.
func TestOpenPositions_TheAgentsOnTheirAccounts(t *testing.T) {
	previous := openPositionsTTL
	openPositionsTTL = 0
	t.Cleanup(func() { openPositionsTTL = previous })
	c := newManaged(t)
	ctx := context.Background()
	demo := newFakeBridge(t, "bridge-token", 198974329, "BRL")
	port := demo.URL[strings.LastIndex(demo.URL, ":")+1:]

	// two agents on the account, the second through another name of the
	// loopback, a third one disabled (its positions stay open), one in paper
	llm := c.agent(t, "llm", "llm", true, onBridge(demo.URL, 101, "BTCUSDm;XAUUSDm"))
	c.agent(t, "matrend", "trend", true, onBridge("http://localhost:"+port, 202, "BTCUSDm"))
	c.agent(t, "fibonacci", "trend", false, onBridge(demo.URL, 303, "XAUUSDm"))
	c.agent(t, "paper", "trend", true, onBridge(demo.URL, 404, "XAUUSDm"), func(a *store.Agent) { a.Mode = "paper" })
	for _, name := range []string{"llm", "matrend", "fibonacci", "paper"} {
		a, err := c.store.AgentByName(ctx, name)
		require.NoError(t, err)
		require.NoError(t, c.store.SetAgentSecrets(ctx, a.ID, store.AgentSecrets{BridgeToken: secret("bridge-token")}))
	}
	call, srv := settingsSession(t, c)

	// none open
	got := openPL(t, call)
	assert.Equal(t, []ui.CurrencyAmount{{Currency: "BRL"}}, got.Totals, "the account read, its currency, 0")
	assert.True(t, got.Complete)
	assert.Empty(t, got.Positions)
	assert.Equal(t, []ui.CurrencyAmount{{Currency: "BRL", Amount: 1000}}, got.Balances,
		"the balance of the account, once for its agents")
	assert.Equal(t, 1, got.Accounts)

	// gains and losses of several agents, one of a disabled agent, one of a
	// paper agent and one by hand left out
	demo.set(
		position(1, 101, "XAUUSDm", true, 1, 81.63, -1.2),    // llm, short
		position(2, 202, "BTCUSDm", false, 0.03, -6.5, 0),    // matrend, long
		position(3, 303, "XAUUSDm", false, 0.03, 25.0, -0.4), // fibonacci, disabled
		position(4, 404, "XAUUSDm", false, 0.01, 999, 0),     // the paper agent: no real position of it
		position(5, 0, "EURUSDm", false, 0.1, 50, 0),         // by hand
	)
	got = openPL(t, call)
	assert.True(t, got.Complete)
	assert.InDelta(t, 81.63-1.2-6.5+25.0-0.4, totalIn(t, got, "BRL"), 1e-9)
	require.Len(t, got.Positions, 3)
	byTicket := map[int64]ui.OpenPosition{}
	for _, p := range got.Positions {
		byTicket[p.Ticket] = p
	}
	short := byTicket[1]
	assert.InDelta(t, 81.63-1.2, short.Profit, 1e-9, "its profit and its swap")
	short.Profit = 0
	assert.Equal(t, ui.OpenPosition{Agent: "llm", Pair: "XAUUSDm", Side: "SELL", Volume: 1, Entry: 4160, Price: 4161,
		Currency: "BRL", Ticket: 1}, short)
	assert.Equal(t, "matrend", byTicket[2].Agent)
	assert.Equal(t, "BUY", byTicket[2].Side)
	assert.Equal(t, "fibonacci", byTicket[3].Agent, "a disabled agent with a position open")
	assert.False(t, byTicket[3].Running)
	demo.mu.Lock()
	requests := demo.requests
	demo.mu.Unlock()
	assert.LessOrEqual(t, requests, 4, "the account read once for the agents on it: /account and /positions")

	// a second URL of the same account: its positions counted once
	twin := newFakeBridge(t, "twin-token", 198974329, "BRL")
	twin.set(position(1, 101, "XAUUSDm", true, 1, 81.63, -1.2))
	other, err := c.store.CreateAgent(ctx, store.Agent{Name: "twin", Kind: "trend", Mode: "live", APIAddress: "localhost:15620",
		Launched: true, Params: store.Params{"pair": "XAUUSDm", "lot": "0.01"},
		Account: &store.Account{BridgeURL: twin.URL, Magic: ptrTo(int64(101)), Comment: secret("nb:twin")}},
		store.AgentSecrets{BridgeToken: secret("twin-token")})
	require.NoError(t, err)
	got = openPL(t, call)
	assert.InDelta(t, 81.63-1.2-6.5+25.0-0.4, totalIn(t, got, "BRL"), 1e-9, "the same ticket of the same login once")
	assert.Len(t, got.Totals, 1)
	assert.Len(t, got.Positions, 3)
	assert.Equal(t, []ui.CurrencyAmount{{Currency: "BRL", Amount: 1000}}, got.Balances, "the same login once")
	assert.Equal(t, 1, got.Accounts)
	require.NoError(t, c.store.DeleteAgent(ctx, other.ID))

	// a position closed, another one opened: the next read follows them
	demo.set(position(2, 202, "BTCUSDm", false, 0.03, 4.2, 0))
	got = openPL(t, call)
	assert.InDelta(t, 4.2, totalIn(t, got, "BRL"), 1e-9)
	require.Len(t, got.Positions, 1)
	assert.Equal(t, "matrend", got.Positions[0].Agent)

	// all closed
	demo.set()
	got = openPL(t, call)
	assert.True(t, got.Complete)
	assert.Equal(t, []ui.CurrencyAmount{{Currency: "BRL"}}, got.Totals)
	assert.Empty(t, got.Positions)

	// running: told so
	demo.set(position(1, 101, "XAUUSDm", true, 1, 10, 0))
	require.NoError(t, c.manager.Reconcile(ctx))
	require.Eventually(t, func() bool { return c.manager.State(llm.Name).Running }, 5*time.Second, 10*time.Millisecond)
	got = openPL(t, call)
	require.Len(t, got.Positions, 1)
	assert.True(t, got.Positions[0].Running)

	// only to a user logged in
	resp, _ := do(t, http.MethodGet, srv.URL+"/api/positions/open", "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// An account that can't be read, or a position in another currency, makes the
// open profit incomplete, told, never counted as 0 silently.
func TestOpenPositions_Incomplete(t *testing.T) {
	previous := openPositionsTTL
	openPositionsTTL = 0
	t.Cleanup(func() { openPositionsTTL = previous })
	c := newManaged(t)
	ctx := context.Background()
	brl := newFakeBridge(t, "bridge-token", 1, "BRL")
	usd := newFakeBridge(t, "usd-token", 2, "USD")
	refusing := newFakeBridge(t, "the-right-token", 3, "BRL")
	down := newFakeBridge(t, "bridge-token", 4, "BRL")
	down.Close()
	c.agent(t, "llm", "llm", true, onBridge(brl.URL, 101, "XAUUSDm"))
	c.agent(t, "dollar", "trend", true, onBridge(usd.URL, 202, "BTCUSDm"))
	c.agent(t, "wrong", "trend", true, onBridge(refusing.URL, 303, "BTCUSDm"))
	c.agent(t, "offline", "trend", true, onBridge(down.URL, 404, "BTCUSDm"))
	for name, token := range map[string]string{"llm": "bridge-token", "dollar": "usd-token", "wrong": "a-wrong-token",
		"offline": "bridge-token"} {
		a, err := c.store.AgentByName(ctx, name)
		require.NoError(t, err)
		require.NoError(t, c.store.SetAgentSecrets(ctx, a.ID, store.AgentSecrets{BridgeToken: secret(token)}))
	}
	brl.set(position(1, 101, "XAUUSDm", false, 1, -12.5, 0))
	usd.set(position(9, 202, "BTCUSDm", true, 0.01, 3.25, -0.05))
	call, _ := settingsSession(t, c)

	got := openPL(t, call)
	assert.False(t, got.Complete, "two accounts not read")
	assert.Equal(t, []ui.CurrencyAmount{{Currency: "BRL", Amount: -12.5}, {Currency: "USD", Amount: 3.2}},
		roundAmounts(got.Totals), "each account in its currency, the ones read")
	reasons := map[string]string{}
	for _, gap := range got.Missing {
		for _, agent := range gap.Agents {
			reasons[agent] = gap.Reason
		}
	}
	assert.Equal(t, map[string]string{"wrong": "refused", "offline": "unreachable"}, reasons)
	require.Len(t, got.Positions, 2)
	assert.Equal(t, []ui.CurrencyAmount{{Currency: "BRL", Amount: 1000}, {Currency: "USD", Amount: 1000}},
		got.Balances, "the balances of the accounts read only")
	assert.Equal(t, 2, got.Accounts)

	// the terminal of a bridge disconnected
	brl.mu.Lock()
	brl.status = http.StatusBadGateway
	brl.mu.Unlock()
	got = openPL(t, call)
	assert.Equal(t, []ui.CurrencyAmount{{Currency: "USD", Amount: 3.2}}, roundAmounts(got.Totals),
		"the account in BRL not read: no total of it, never a 0")
	assert.False(t, got.Complete)
	reasons = map[string]string{}
	for _, gap := range got.Missing {
		for _, agent := range gap.Agents {
			reasons[agent] = gap.Reason
		}
	}
	assert.Equal(t, "failed", reasons["llm"])
}

// totalIn returns the total of got in currency, failing without one.
func totalIn(t *testing.T, got ui.OpenPositionsResponse, currency string) float64 {
	t.Helper()
	for _, total := range got.Totals {
		if total.Currency == currency {
			return total.Amount
		}
	}
	t.Fatalf("no total in %s: %v", currency, got.Totals)
	return 0
}

// Each account in its currency, none converted: the accounts in BRL, in USD
// and in KWD each with its total, the sum complete, every account read.
func TestOpenPositions_EachInTheCurrencyOfItsAccount(t *testing.T) {
	previous := openPositionsTTL
	openPositionsTTL = 0
	t.Cleanup(func() { openPositionsTTL = previous })
	c := newManaged(t)
	ctx := context.Background()
	brl := newFakeBridge(t, "bridge-token", 1, "BRL")
	usd := newFakeBridge(t, "bridge-token", 2, "USD")
	kwd := newFakeBridge(t, "bridge-token", 3, "kwd")
	c.agent(t, "llm", "llm", true, onBridge(brl.URL, 101, "XAUUSDm"))
	c.agent(t, "dollar", "trend", true, onBridge(usd.URL, 202, "BTCUSDm"))
	c.agent(t, "dinar", "trend", true, onBridge(kwd.URL, 303, "XAUUSDm"))
	for _, name := range []string{"llm", "dollar", "dinar"} {
		a, err := c.store.AgentByName(ctx, name)
		require.NoError(t, err)
		require.NoError(t, c.store.SetAgentSecrets(ctx, a.ID, store.AgentSecrets{BridgeToken: secret("bridge-token")}))
	}
	brl.set(position(1, 101, "XAUUSDm", false, 1, 81.63, -1.2))
	usd.set(position(2, 202, "BTCUSDm", true, 0.01, 3.25, -0.05), position(3, 202, "BTCUSDm", false, 0.01, -1, 0))
	brl.balance, usd.balance, kwd.balance = 10250.5, 1980.25, 300
	call, _ := settingsSession(t, c)

	got := openPL(t, call)
	assert.True(t, got.Complete, "every account read: another currency isn't a gap")
	assert.Equal(t, []ui.CurrencyAmount{{Currency: "BRL", Amount: 80.43}, {Currency: "USD", Amount: 2.2},
		{Currency: "KWD", Amount: 0}}, roundAmounts(got.Totals))
	currencies := map[int64]string{}
	for _, position := range got.Positions {
		currencies[position.Ticket] = position.Currency
	}
	assert.Equal(t, map[int64]string{1: "BRL", 2: "USD", 3: "USD"}, currencies)
	assert.Equal(t, []ui.CurrencyAmount{{Currency: "BRL", Amount: 10250.5}, {Currency: "USD", Amount: 1980.25},
		{Currency: "KWD", Amount: 300}}, got.Balances, "the balance of each account in its currency")
	assert.Equal(t, 3, got.Accounts)
}

// roundAmounts returns amounts rounded to the cent.
func roundAmounts(amounts []ui.CurrencyAmount) []ui.CurrencyAmount {
	out := make([]ui.CurrencyAmount, 0, len(amounts))
	for _, a := range amounts {
		out = append(out, ui.CurrencyAmount{Currency: a.Currency, Amount: math.Round(a.Amount*100) / 100})
	}
	return out
}

// The dashboards and their tabs reading the open profit at once share a read
// of the bridges, read again once openPositionsTTL old.
func TestOpenPositions_AReadForAll(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	demo := newFakeBridge(t, "bridge-token", 1, "BRL")
	a := c.agent(t, "llm", "llm", true, onBridge(demo.URL, 101, "XAUUSDm"))
	require.NoError(t, c.store.SetAgentSecrets(ctx, a.ID, store.AgentSecrets{BridgeToken: secret("bridge-token")}))
	demo.set(position(1, 101, "XAUUSDm", false, 1, 5, 0))
	positions := newOpenPositions(c.store, c.manager)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := positions.read(ctx)
			assert.NoError(t, err)
			assert.InDelta(t, 5, totalIn(t, got, "BRL"), 1e-9)
		}()
	}
	wg.Wait()
	demo.mu.Lock()
	requests := demo.requests
	demo.mu.Unlock()
	assert.Equal(t, 2, requests, "/account and /positions once")
}
