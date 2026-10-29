//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// traded returns a deal of the tests of a trade of volume lots at price, see
// deal: a buy when buy, else a sell.
func traded(ticket, position, magic int64, entry int, buy bool, at time.Time, volume, price, profit,
	commission float64) bridgeDeal {
	d := deal(ticket, position, magic, entry, at, profit, 0, commission, 0)
	if buy {
		d.Type = dealTypeBuy
	}
	d.Volume, d.Price = volume, price
	return d
}

// readPerformance reads GET /api/performance from until before to with call,
// see settingsSession.
func readPerformance(t *testing.T, call func(method, path string, body interface{}) (*http.Response, string),
	from, to time.Time) ui.PerformanceResponse {
	t.Helper()
	resp, body := call(http.MethodGet, fmt.Sprintf("/api/performance?from=%d&to=%d", from.Unix(), to.Unix()), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var response ui.PerformanceResponse
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	return response
}

// The performance of a range is the trades of the agents closed then, each
// position once, as their accounts booked them: its side, volume and prices,
// its opening (unknown when its entry is older than the deals read), its last
// exit, the profit of its exits, the costs of its entry and exits; manual
// positions, positions still open (a part of them) and positions closed out
// of the range left out. Each account tells its balance at the start of the
// range, its balance less every deal since, a deposit included, in its
// currency, none converted.
func TestPerformance_TheTradesOfTheAgents(t *testing.T) {
	previous := performanceTTL
	performanceTTL = 0
	t.Cleanup(func() { performanceTTL = previous })
	c := newManaged(t)
	ctx := context.Background()
	brl := newFakeBridge(t, "bridge-token", 198974329, "BRL")
	usd := newFakeBridge(t, "bridge-token", 198974330, "USD")
	c.agent(t, "llm", "llm", true, onBridge(brl.URL, 101, "XAUUSDm"))
	c.agent(t, "orbit", "trend", true, onBridge(usd.URL, 202, "XAUUSDm"))
	for _, name := range []string{"llm", "orbit"} {
		a, err := c.store.AgentByName(ctx, name)
		require.NoError(t, err)
		require.NoError(t, c.store.SetAgentSecrets(ctx, a.ID, store.AgentSecrets{BridgeToken: secret("bridge-token")}))
	}
	call, srv := settingsSession(t, c)

	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-48 * time.Hour)
	next := day.Add(24 * time.Hour)
	brl.mu.Lock()
	brl.balance = 1500
	brl.deals = []bridgeDeal{
		// llm: a buy entered the day before, closed in two parts in the range
		traded(1, 10, 101, dealEntryIn, true, day.Add(-2*time.Hour), 0.02, 4000, 0, -0.5),
		traded(2, 10, 101, dealEntryOut, false, day.Add(time.Hour), 0.01, 4010, 50, -0.5),
		traded(3, 10, 101, dealEntryOut, false, day.Add(2*time.Hour), 0.01, 4020, 100, 0),
		// llm: a sell whose entry is older than the deals read
		traded(4, 20, 101, dealEntryOut, true, day.Add(3*time.Hour), 0.01, 3990, -30, 0),
		// llm: a part closed in the range, the rest still open
		traded(5, 30, 101, dealEntryIn, true, day.Add(4*time.Hour), 0.02, 4000, 0, 0),
		traded(6, 30, 101, dealEntryOut, false, day.Add(5*time.Hour), 0.01, 4005, 25, 0),
		// by hand: left out
		traded(7, 40, 0, dealEntryIn, true, day.Add(time.Hour), 0.01, 4000, 0, 0),
		traded(8, 40, 0, dealEntryOut, false, day.Add(6*time.Hour), 0.01, 4050, 250, 0),
		// llm: closed the next day, out of the range, its profit after the start
		traded(9, 50, 101, dealEntryIn, true, next.Add(time.Hour), 0.01, 4000, 0, 0),
		traded(10, 50, 101, dealEntryOut, false, next.Add(2*time.Hour), 0.01, 4001, 5, 0),
		// a deposit in the range
		{Ticket: 11, Time: day.Add(7 * time.Hour).Unix(), Type: 2, Profit: 1000},
	}
	brl.mu.Unlock()
	usd.mu.Lock()
	usd.deals = []bridgeDeal{
		traded(1, 10, 202, dealEntryIn, false, day.Add(time.Hour), 0.05, 4100, 0, 0),
		traded(2, 10, 202, dealEntryOut, true, day.Add(90*time.Minute), 0.05, 4090, 50, -1),
	}
	usd.mu.Unlock()

	got := readPerformance(t, call, day, next)
	assert.True(t, got.Complete)
	require.Len(t, got.Trades, 3, "manual, still open and out of the range left out")

	first := got.Trades[0]
	assert.Equal(t, "orbit", first.Agent, "by its close")
	assert.Equal(t, ui.PerformanceTrade{Position: 10, Account: "198974330", Agent: "orbit", Symbol: "XAUUSDm",
		Side: "SELL", Volume: 0.05, OpenedAt: day.Add(time.Hour).Unix(), ClosedAt: day.Add(90 * time.Minute).Unix(),
		Entry: 4100, Exit: 4090, Gross: 50, Costs: -1, Net: 49, Currency: "USD"}, first)

	two := got.Trades[1]
	assert.Equal(t, "llm", two.Agent)
	assert.Equal(t, "BUY", two.Side)
	assert.Equal(t, 0.02, two.Volume)
	assert.Equal(t, day.Add(-2*time.Hour).Unix(), two.OpenedAt, "its entry before the range")
	assert.Equal(t, day.Add(2*time.Hour).Unix(), two.ClosedAt, "its last exit")
	assert.Equal(t, 4000.0, two.Entry)
	assert.InDelta(t, 4015, two.Exit, 1e-9, "the average of its exits")
	assert.Equal(t, 150.0, two.Gross)
	assert.Equal(t, -1.0, two.Costs, "the commission of its entry and exits")
	assert.Equal(t, 149.0, two.Net)
	assert.Equal(t, "BRL", two.Currency)

	unknown := got.Trades[2]
	assert.Zero(t, unknown.OpenedAt, "its entry older than the deals read")
	assert.Equal(t, "SELL", unknown.Side, "the opposite of its exit")
	assert.Equal(t, -30.0, unknown.Net)

	require.Len(t, got.Accounts, 2)
	assert.Equal(t, ui.PerformanceAccount{Account: "198974329", Currency: "BRL", Agents: []string{"llm"},
		Balance: 1500, StartBalance: 1500 - 49.5 - 100 + 30 - 25 - 250 - 5 - 1000}, got.Accounts[0],
		"its balance less every deal since the start: the trades, the manual one and the deposit")
	assert.Equal(t, "USD", got.Accounts[1].Currency)
	assert.Equal(t, 1000.0-49, got.Accounts[1].StartBalance)

	// an account not read: incomplete, the other one still told
	usd.mu.Lock()
	usd.status = http.StatusInternalServerError
	usd.mu.Unlock()
	got = readPerformance(t, call, day, next)
	assert.False(t, got.Complete)
	require.Len(t, got.Missing, 1)
	assert.Equal(t, []string{"orbit"}, got.Missing[0].Agents)
	assert.Len(t, got.Trades, 2)

	resp, _ := call(http.MethodGet, "/api/performance?from=10&to=5", nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// only to a user logged in
	resp, _ = do(t, http.MethodGet, fmt.Sprintf("%s/api/performance?from=%d&to=%d", srv.URL, day.Unix(),
		next.Unix()), "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
