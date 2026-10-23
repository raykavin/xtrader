//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// deal returns a deal of the tests of a trade: of position, entry (in, out or
// out by), at, its profit, swap, commission and fee.
func deal(ticket, position, magic int64, entry int, at time.Time, profit, swap, commission, fee float64) bridgeDeal {
	return bridgeDeal{Ticket: ticket, Time: at.Unix(), Type: dealTypeSell, Entry: entry, Magic: magic,
		PositionID: position, Profit: profit, Swap: swap, Commission: commission, Fee: fee, Symbol: "XAUUSDm"}
}

// closedPL reads GET /api/positions/closed from until before to with call, see
// settingsSession.
func closedPL(t *testing.T, call func(method, path string, body interface{}) (*http.Response, string),
	from, to time.Time) ui.ClosedProfitResponse {
	t.Helper()
	resp, body := call(http.MethodGet, fmt.Sprintf("/api/positions/closed?from=%d&to=%d", from.Unix(), to.Unix()),
		nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var response ui.ClosedProfitResponse
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	return response
}

// noClosedCache reads the closed profit again on every request of the test.
func noClosedCache(t *testing.T) {
	t.Helper()
	previous := closedProfitTTL
	closedProfitTTL = 0
	t.Cleanup(func() { closedProfitTTL = previous })
}

// The closed profit of the agents in a range is what their accounts booked
// for the exits of their positions then: the profit, swap, commission and fee
// of each exit, of the agent of the magic number of the entry of its position
// (closed by the bot, by its stop, or by hand), every agent trading live,
// running or not, each deal once whatever the URLs of its account; the
// entries, the exits out of the range, the operations of the balance and the
// positions of nobody left out.
func TestClosedProfit_TheAgentsOnTheirAccounts(t *testing.T) {
	noClosedCache(t)
	c := newManaged(t)
	ctx := context.Background()
	demo := newFakeBridge(t, "bridge-token", 198974329, "BRL")
	port := demo.URL[strings.LastIndex(demo.URL, ":")+1:]
	c.agent(t, "llm", "llm", true, onBridge(demo.URL, 101, "XAUUSDm"))
	c.agent(t, "fibonacci", "trend", false, onBridge("http://localhost:"+port, 303, "XAUUSDm"))
	c.agent(t, "paper", "trend", true, onBridge(demo.URL, 404, "XAUUSDm"), func(a *store.Agent) { a.Mode = "paper" })
	for _, name := range []string{"llm", "fibonacci", "paper"} {
		a, err := c.store.AgentByName(ctx, name)
		require.NoError(t, err)
		require.NoError(t, c.store.SetAgentSecrets(ctx, a.ID, store.AgentSecrets{BridgeToken: secret("bridge-token")}))
	}
	call, srv := settingsSession(t, c)

	day := time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC) // 00:00 in Belém
	next := day.Add(24 * time.Hour)
	got := closedPL(t, call, day, next)
	assert.Equal(t, []ui.CurrencyAmount{{Currency: "BRL"}}, got.Totals, "the account read, its currency, 0")
	assert.Empty(t, got.Agents)
	assert.Equal(t, []ui.ClosedProfitSeries{{Currency: "BRL", Points: []ui.ClosedProfitPoint{}}}, got.Series,
		"its series, without a point")
	assert.True(t, got.Complete)
	assert.Equal(t, day.Unix(), got.From)
	assert.Equal(t, next.Unix(), got.To)

	demo.deals = []bridgeDeal{
		// llm: entered yesterday, closed by its stop loss today, in two parts
		deal(1, 10, 101, dealEntryIn, day.Add(-5*time.Hour), 0, 0, -1, 0),
		deal(2, 10, 101, dealEntryOut, day.Add(2*time.Hour), -30, -0.5, -0.25, -0.05),
		deal(3, 10, 101, dealEntryOut, day.Add(3*time.Hour), -20, 0, 0, 0),
		// fibonacci, disabled: its position closed by hand today, the exit
		// without its magic number
		deal(4, 20, 303, dealEntryIn, day.Add(-48*time.Hour), 0, 0, 0, 0),
		deal(5, 20, 0, dealEntryOut, day.Add(4*time.Hour), 75.5, -1.5, 0, 0),
		// llm: a position closed yesterday, out of the range
		deal(6, 30, 101, dealEntryIn, day.Add(-30*time.Hour), 0, 0, 0, 0),
		deal(7, 30, 101, dealEntryOut, day.Add(-time.Hour), 999, 0, 0, 0),
		// llm: an entry today, open still
		deal(8, 40, 101, dealEntryIn, day.Add(5*time.Hour), 0, 0, -2, 0),
		// by hand, and of the paper agent: nobody's
		deal(9, 50, 0, dealEntryIn, day.Add(time.Hour), 0, 0, 0, 0),
		deal(10, 50, 0, dealEntryOut, day.Add(6*time.Hour), 500, 0, 0, 0),
		deal(11, 60, 404, dealEntryOut, day.Add(6*time.Hour), 400, 0, 0, 0),
		// a deposit, today
		{Ticket: 12, Time: day.Add(7 * time.Hour).Unix(), Type: 2, Profit: 5000},
		// llm: an exit by its stop, its entry older than the deals read: of the
		// magic number of the exit
		deal(13, 70, 101, dealEntryOut, day.Add(8*time.Hour), 12, 0, 0, 0),
		// llm: an exit tomorrow
		deal(14, 80, 101, dealEntryOut, next.Add(time.Hour), 777, 0, 0, 0),
	}
	got = closedPL(t, call, day, next)
	assert.True(t, got.Complete)
	llmProfit := -30 - 0.5 - 0.25 - 0.05 - 20 + 12
	fibonacciProfit := 75.5 - 1.5
	require.Len(t, got.Totals, 1)
	assert.InDelta(t, llmProfit+fibonacciProfit, got.Totals[0].Amount, 1e-9)
	require.Len(t, got.Agents, 2)
	assert.Equal(t, "llm", got.Agents[0].Agent, "in the order of the dashboard")
	assert.InDelta(t, llmProfit, got.Agents[0].Amount, 1e-9, "its exits: profit, swap, commission and fee")
	assert.Equal(t, 2, got.Agents[0].Positions, "a position closed in two parts counted once")
	assert.Equal(t, "BRL", got.Agents[0].Currency)
	assert.Equal(t, "fibonacci", got.Agents[1].Agent, "its position closed by hand, of the magic number of its entry")
	assert.InDelta(t, fibonacciProfit, got.Agents[1].Amount, 1e-9)
	assert.Equal(t, 1, got.Agents[1].Positions)
	assert.False(t, got.Agents[1].Running)

	// the evolution: the same exits summed in the order of their times, the
	// last point the total
	require.Len(t, got.Series, 1)
	assert.Equal(t, "BRL", got.Series[0].Currency)
	points := got.Series[0].Points
	require.Len(t, points, 4)
	for i, want := range []struct {
		at     time.Time
		amount float64
	}{
		{day.Add(2 * time.Hour), -30.8},
		{day.Add(3 * time.Hour), -50.8},
		{day.Add(4 * time.Hour), -50.8 + fibonacciProfit},
		{day.Add(8 * time.Hour), llmProfit + fibonacciProfit},
	} {
		assert.Equal(t, want.at.Unix(), points[i].At, "point %d", i)
		assert.InDelta(t, want.amount, points[i].Amount, 1e-9, "point %d", i)
	}
	assert.Equal(t, got.Totals[0].Amount, points[3].Amount, "the last point the total")

	demo.mu.Lock()
	queries := append([]string(nil), demo.dealsRead...)
	demo.mu.Unlock()
	require.NotEmpty(t, queries)
	query, err := url.ParseQuery(queries[len(queries)-1])
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprint(day.Add(-closedLookback).Unix()), query.Get("from"),
		"the entries of the positions read from before the range")
	assert.Equal(t, fmt.Sprint(next.Unix()), query.Get("to"))
	assert.Len(t, queries, 2, "the account read once for its agents, whatever its URLs")

	// another range: yesterday
	got = closedPL(t, call, day.Add(-24*time.Hour), day)
	require.Len(t, got.Agents, 1)
	assert.InDelta(t, 999, got.Agents[0].Amount, 1e-9)

	// only to a user logged in
	resp, _ := do(t, http.MethodGet, srv.URL+fmt.Sprintf("/api/positions/closed?from=%d&to=%d", day.Unix(),
		next.Unix()), "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// The closed profit of each account in its currency, none converted; an
// account that can't be read makes it incomplete, told, never counted as 0.
func TestClosedProfit_EachInItsCurrencyAndIncomplete(t *testing.T) {
	noClosedCache(t)
	c := newManaged(t)
	ctx := context.Background()
	brl := newFakeBridge(t, "bridge-token", 1, "BRL")
	usd := newFakeBridge(t, "bridge-token", 2, "usd")
	refusing := newFakeBridge(t, "the-right-token", 3, "BRL")
	c.agent(t, "llm", "llm", true, onBridge(brl.URL, 101, "XAUUSDm"))
	c.agent(t, "dollar", "trend", true, onBridge(usd.URL, 202, "XAUUSDm"))
	c.agent(t, "wrong", "trend", true, onBridge(refusing.URL, 303, "XAUUSDm"))
	for name, token := range map[string]string{"llm": "bridge-token", "dollar": "bridge-token",
		"wrong": "a-wrong-token"} {
		a, err := c.store.AgentByName(ctx, name)
		require.NoError(t, err)
		require.NoError(t, c.store.SetAgentSecrets(ctx, a.ID, store.AgentSecrets{BridgeToken: secret(token)}))
	}
	day := time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)
	brl.deals = []bridgeDeal{deal(1, 1, 101, dealEntryOut, day.Add(time.Hour), -50.046, 0, 0, 0)}
	usd.deals = []bridgeDeal{deal(1, 1, 202, dealEntryOut, day.Add(time.Hour), 3.25, -0.05, 0, 0)}
	refusing.deals = []bridgeDeal{deal(1, 1, 303, dealEntryOut, day.Add(time.Hour), 100, 0, 0, 0)}
	call, _ := settingsSession(t, c)

	got := closedPL(t, call, day, day.Add(24*time.Hour))
	assert.False(t, got.Complete)
	assert.Equal(t, []ui.CurrencyAmount{{Currency: "BRL", Amount: -50.05}, {Currency: "USD", Amount: 3.2}},
		roundAmounts(got.Totals))
	require.Len(t, got.Missing, 1)
	assert.Equal(t, ui.OpenPositionsGap{Agents: []string{"wrong"}, Reason: "refused"}, got.Missing[0])
	require.Len(t, got.Agents, 2)
	assert.Equal(t, "USD", got.Agents[1].Currency)
	require.Len(t, got.Series, 2, "a series a currency read, the account not read in none")
	for i, currency := range []string{"BRL", "USD"} {
		assert.Equal(t, currency, got.Series[i].Currency)
		require.Len(t, got.Series[i].Points, 1)
		assert.Equal(t, got.Totals[i].Amount, got.Series[i].Points[0].Amount)
	}
}

// The series of a currency sums its exits one after another in the order of
// their times, whatever the order of the accounts read: the exits of a second
// one point, the last point the total, the other currencies apart.
func TestClosedProfit_Series(t *testing.T) {
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	exits := []closedExit{
		{currency: "BRL", at: at.Add(3 * time.Hour), profit: 10},
		{currency: "USD", at: at.Add(time.Hour), profit: 1.5},
		{currency: "BRL", at: at.Add(time.Hour), profit: -4},
		{currency: "BRL", at: at.Add(time.Hour + 400*time.Millisecond), profit: -1}, // the same second
		{currency: "BRL", at: at.Add(2 * time.Hour), profit: 2.25},
	}
	got := closedSeries([]ui.CurrencyAmount{{Currency: "BRL", Amount: 7.25}, {Currency: "USD", Amount: 1.5},
		{Currency: "KWD"}}, exits)
	assert.Equal(t, []ui.ClosedProfitSeries{
		{Currency: "BRL", Points: []ui.ClosedProfitPoint{{At: at.Add(time.Hour).Unix(), Amount: -5},
			{At: at.Add(2 * time.Hour).Unix(), Amount: -2.75}, {At: at.Add(3 * time.Hour).Unix(), Amount: 7.25}}},
		{Currency: "USD", Points: []ui.ClosedProfitPoint{{At: at.Add(time.Hour).Unix(), Amount: 1.5}}},
		{Currency: "KWD", Points: []ui.ClosedProfitPoint{}},
	}, got)
}

// A range is from before to, in Unix seconds, 400 days apart at most.
func TestClosedProfit_TheRange(t *testing.T) {
	c := newManaged(t)
	call, _ := settingsSession(t, c)
	for query, why := range map[string]string{
		"":                              "none",
		"from=1760410800":               "no end",
		"from=abc&to=1760497200":        "not a number",
		"from=-5&to=1760497200":         "before 1970",
		"from=1760497200&to=1760497200": "empty",
		"from=1760497200&to=1760410800": "backwards",
		"from=1700000000&to=1760497200": "over 400 days",
	} {
		resp, body := call(http.MethodGet, "/api/positions/closed?"+query, nil)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "%s: %s", why, body)
	}
	resp, body := call(http.MethodGet, "/api/positions/closed?from=1760410800&to=1760497200", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode, body)
}

// The dashboards reading the closed profit of a range at once share a read of
// the bridges, read again once closedProfitTTL old.
func TestClosedProfit_AReadForAll(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	demo := newFakeBridge(t, "bridge-token", 1, "BRL")
	a := c.agent(t, "llm", "llm", true, onBridge(demo.URL, 101, "XAUUSDm"))
	require.NoError(t, c.store.SetAgentSecrets(ctx, a.ID, store.AgentSecrets{BridgeToken: secret("bridge-token")}))
	day := time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)
	demo.deals = []bridgeDeal{deal(1, 1, 101, dealEntryOut, day.Add(time.Hour), 5, 0, 0, 0)}
	closed := newClosedProfits(newOpenPositions(c.store, c.manager))
	window := closedRange{from: day, to: day.Add(24 * time.Hour)}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := closed.read(ctx, window)
			assert.NoError(t, err)
			require.Len(t, got.Totals, 1)
			assert.InDelta(t, 5, got.Totals[0].Amount, 1e-9)
		}()
	}
	wg.Wait()
	demo.mu.Lock()
	requests := demo.requests
	demo.mu.Unlock()
	assert.Equal(t, 2, requests, "/account and /deals once")
}
