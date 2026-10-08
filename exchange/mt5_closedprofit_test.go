package exchange

import (
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// m5fBooked returns the profit, swap, commission and fee the fake booked on
// the deals of the position that match.
func m5fBooked(f *m5fFake, position int64, match func(m5fDeal) bool) (float64, int) {
	var profit float64
	var count int
	for _, deal := range f.dealsOf(position) {
		if match(deal) {
			profit += deal.profit
			count++
		}
	}
	return profit, count
}

// m5fOrderDeals returns the profit the fake booked on the deals of an order
// and how many there are.
func m5fOrderDeals(f *m5fFake, order int64) (float64, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var profit float64
	var count int
	for _, deal := range f.deals {
		if deal.order == order {
			profit += deal.profit
			count++
		}
	}
	return profit, count
}

// m5fClosedProfit waits for the profit the adapter reports for close.
func m5fClosedProfit(t *testing.T, m *MT5, close, entry model.Order, wait time.Duration) float64 {
	t.Helper()
	var booked float64
	var known bool
	require.Eventually(t, func() bool {
		booked, known = m.ClosedProfit(close, entry)
		return known
	}, wait, 10*time.Millisecond, "the profit of the close of %d never known", entry.ExchangeID)
	return booked
}

// The history lists the exit of a market close after the outcome of the
// positions gone is no longer awaited (mt5ResolveWait): the adapter keeps
// reading it from the exit, backing off, until the exit shows with its profit.
func TestMT5ClosedProfit_HistoryLateBeyondTheResolveWait(t *testing.T) {
	const wait = 200 * time.Millisecond
	f := newM5F(t)
	m := newM5FAdapter(t, f, withMT5HistoryWaits(wait, time.Minute))
	entry, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	f.setPrice(m5fPair, 4262, 4262.3) // the long loses

	f.holdHistory()
	closed, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 1)
	require.NoError(t, err)
	_, known := m.ClosedProfit(closed, entry)
	require.False(t, known, "its exit known from the answer only")

	time.Sleep(wait) // the outcome is no longer awaited
	reads := f.requestCount("GET", "/deals")
	time.Sleep(6 * wait)
	reads = f.requestCount("GET", "/deals") - reads
	require.Positive(t, reads, "read again after the wait")
	require.Less(t, reads, 6*int(wait/(20*time.Millisecond))/2, "backing off, not at every poll")
	_, known = m.ClosedProfit(closed, entry)
	require.False(t, known)

	f.releaseHistory()
	want, exits := m5fBooked(f, entry.ExchangeID, func(deal m5fDeal) bool { return deal.entry == mt5EntryOut })
	require.Equal(t, 1, exits)
	require.Negative(t, want)
	require.InDelta(t, want, m5fClosedProfit(t, m, closed, entry, 5*time.Second), 1e-9)
}

// The history lists the exit of a market close later than the overlap of the
// reads: the adapter reads it from the exit awaited, not from the end of the
// range read, which went past it meanwhile.
func TestMT5ClosedProfit_HistoryLateBeyondTheOverlap(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, withMT5HistoryWaits(time.Hour, time.Second))
	entry, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 1)
	require.NoError(t, err)
	f.setPrice(m5fPair, 4270, 4270.3) // the short earns

	f.holdHistory()
	closed, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	time.Sleep(3 * time.Second) // the polls read the history up to now meanwhile
	_, known := m.ClosedProfit(closed, entry)
	require.False(t, known)

	f.releaseHistory()
	want, exits := m5fBooked(f, entry.ExchangeID, func(deal m5fDeal) bool { return deal.entry == mt5EntryOut })
	require.Equal(t, 1, exits)
	require.Positive(t, want)
	require.InDelta(t, want, m5fClosedProfit(t, m, closed, entry, 5*time.Second), 1e-9)
}

// A position closed by its stop loss leaves the book before the history lists
// its exit, which shows after mt5ResolveWait: the leg is settled meanwhile, but
// the exit is still read, for its profit.
func TestMT5ClosedProfit_StopLossHitHistoryLate(t *testing.T) {
	const wait = 200 * time.Millisecond
	f := newM5F(t)
	m := newM5FAdapter(t, f, withMT5HistoryWaits(wait, time.Minute))
	orders, err := m.CreateOrderMarketProtected(model.SideTypeBuy, m5fPair, 1, 4280, 4300)
	require.NoError(t, err)
	entry, stop := orders[0], orders[1]
	if stop.Type != model.OrderTypeStopLoss {
		stop = orders[2]
	}

	f.holdHistory()
	f.triggerSL(entry.ExchangeID, 0)
	time.Sleep(4 * wait)
	_, known := m.ClosedProfit(stop, entry)
	require.False(t, known)

	f.releaseHistory()
	want, exits := m5fBooked(f, entry.ExchangeID, func(deal m5fDeal) bool { return deal.reason == mt5ReasonSL })
	require.Equal(t, 1, exits)
	require.Negative(t, want)
	require.InDelta(t, want, m5fClosedProfit(t, m, stop, entry, 5*time.Second), 1e-9)
}

// A list of the history failing (an error of the terminal, 502) is never taken
// for an empty one: the range isn't read, and the exit awaited is read once
// the bridge answers again, however long after.
func TestMT5ClosedProfit_HistoryFailingIsNotRead(t *testing.T) {
	const wait = 200 * time.Millisecond
	f := newM5F(t)
	m := newM5FAdapter(t, f, withMT5HistoryWaits(wait, time.Minute))
	entry, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	f.setPrice(m5fPair, 4300, 4300.3) // the long earns

	b := m.book
	b.historyMtx.Lock()
	readTo := b.historyTo
	b.historyMtx.Unlock()
	f.setFailing("GET", "/deals", http.StatusBadGateway,
		`{"error":"history_deals_get: Terminal: Call failed","retcode":-1}`)
	closed, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 1)
	require.NoError(t, err, "the close done, whatever the read after it")
	time.Sleep(3 * wait)
	_, known := m.ClosedProfit(closed, entry)
	require.False(t, known)
	b.historyMtx.Lock()
	require.Equal(t, readTo, b.historyTo, "nothing read")
	b.historyMtx.Unlock()

	f.clearFailing("GET", "/deals")
	want, exits := m5fBooked(f, entry.ExchangeID, func(deal m5fDeal) bool { return deal.entry == mt5EntryOut })
	require.Equal(t, 1, exits)
	require.Positive(t, want)
	require.InDelta(t, want, m5fClosedProfit(t, m, closed, entry, 5*time.Second), 1e-9)
}

// A stop order without a group (CreateOrderStop) closes the long by its fill,
// netted by close-by: the exits are OUT_BY, of reason expert, never SL. Its
// profit is the one of the deals of the close-by, both positions, however the
// broker splits it between them (on MT5, the whole on the deal of the position,
// the fill here, the other one at 0: see the close-by of
// testdata/mt5/deals.json), with the commission of the entry deal of the fill
// in the share of its volume closed: the economic result of the long, from its
// entry to the fill of the stop; after a restart too, from the history.
func TestMT5ClosedProfit_StopClosedBy(t *testing.T) {
	const commission = -7.0 // per lot, of the entry deals
	for name, split := range map[string]float64{"the whole on the fill, as MT5": 0,
		"the whole on the entry": m5fCloseBySplitBy, "half on each": 0.5} {
		for _, units := range []float64{1, 2} { // the stop of the whole long, or of twice it: half its fill closed
			t.Run(fmt.Sprintf("%s, a stop of %g", name, units), func(t *testing.T) {
				f := newM5F(t)
				f.closeBySplit, f.commission = split, commission
				m := newM5FAdapter(t, f)
				entry, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
				require.NoError(t, err)
				stop, err := m.CreateOrderStop(m5fPair, units, 4260)
				require.NoError(t, err)
				require.Nil(t, stop.GroupID)
				f.setPrice(m5fPair, 4259, 4259.3)
				f.fillPending(stop.ExchangeID)
				require.Eventually(t, func() bool { return len(f.tradeRequests(mt5ActionCloseBy)) == 1 }, 5*time.Second,
					5*time.Millisecond, "the fill netted")
				filled := m5fWaitStatus(t, m, stop.ExchangeID, model.OrderStatusTypeFilled)

				var closeBy int64
				for _, deal := range f.dealsOf(entry.ExchangeID) {
					if deal.entry == mt5EntryOutBy {
						closeBy = deal.order
						require.Equal(t, mt5ReasonExpert, deal.reason)
					}
				}
				require.NotZero(t, closeBy)
				booked, deals := m5fOrderDeals(f, closeBy)
				require.Equal(t, 2, deals, "one per position")
				own, _ := m5fBooked(f, entry.ExchangeID, func(deal m5fDeal) bool { return deal.order == closeBy })
				result := math.Round((filled.Price-entry.Price)*1*100) / 100 // the long of 1 unit, in USD
				require.Negative(t, result)
				require.InDelta(t, result, booked, 1e-9, "the profit of the close-by, counted once")
				share := map[float64]float64{0: 1, m5fCloseBySplitBy: 0}[split] // of the deal of the fill
				if split > 0 {
					share = split
				}
				require.InDelta(t, result*(1-share), own, 1e-9, "the share of the deal of the long")

				// the commission of the entry deal of the fill (units/100 lot), in
				// the share of it the close-by closed: 1 unit
				fill := commission * units / 100
				want := result + fill*math.Min(1/units, 1)
				require.InDelta(t, want, m5fClosedProfit(t, m, filled, entry, 5*time.Second), 1e-9)

				restarted := newM5FAdapter(t, f)
				again, known := restarted.ClosedProfit(filled, entry)
				require.True(t, known, "from the history read on startup")
				require.InDelta(t, want, again, 1e-9)
			})
		}
	}
}

// A market order inverting the position closes the long and opens a short of
// its own ticket: the exit is the one of its closing order, matched by its
// ticket, never by the clock of the bot against the one of the server, here 30s
// behind; after a restart, by the clock of the server: the exit within seconds
// of the fill of the order.
func TestMT5ClosedProfit_MarketInversion(t *testing.T) {
	f := newM5F(t)
	f.setServerClock(-30 * time.Second)
	m := newM5FAdapter(t, f)
	entry, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	f.setPrice(m5fPair, 4300, 4300.3) // the long earns

	inverted, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 2)
	require.NoError(t, err)
	_, open := f.position(inverted.ExchangeID)
	require.True(t, open, "the ticket of the short opened")
	want, exits := m5fBooked(f, entry.ExchangeID, func(deal m5fDeal) bool { return deal.entry == mt5EntryOut })
	require.Equal(t, 1, exits)
	require.Positive(t, want)
	require.InDelta(t, want, m5fClosedProfit(t, m, inverted, entry, 5*time.Second), 1e-9)

	restarted := newM5FAdapter(t, f)
	booked, known := restarted.ClosedProfit(inverted, entry)
	require.True(t, known, "from the history read on startup")
	require.InDelta(t, want, booked, 1e-9)
}

// A market order closing several positions of the bot, first in first out:
// the order carries the ticket of its first closing order, and the exit of the
// second position is the one of the second closing order, matched by its
// ticket as the adapter sent it.
func TestMT5ClosedProfit_MarketCloseOfSeveralPositions(t *testing.T) {
	f := newM5F(t)
	f.setServerClock(-30 * time.Second)
	m := newM5FAdapter(t, f)
	first, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	second, err := m.CreateOrderMarket(model.SideTypeBuy, m5fPair, 1)
	require.NoError(t, err)
	require.NotEqual(t, first.ExchangeID, second.ExchangeID)
	f.setPrice(m5fPair, 4250, 4250.3) // the longs lose

	closed, err := m.CreateOrderMarket(model.SideTypeSell, m5fPair, 2)
	require.NoError(t, err)
	for _, entry := range []model.Order{first, second} {
		want, exits := m5fBooked(f, entry.ExchangeID, func(deal m5fDeal) bool { return deal.entry == mt5EntryOut })
		require.Equal(t, 1, exits)
		require.Negative(t, want)
		require.InDelta(t, want, m5fClosedProfit(t, m, closed, entry, 5*time.Second), 1e-9)
	}
}

// The reads of the history for the exits awaited back off from every poll to
// a ceiling.
func TestMT5AwaitDelay_BacksOffToTheCeiling(t *testing.T) {
	m := &MT5{resolveWait: time.Minute}
	require.Zero(t, m.awaitDelay(0))
	require.Zero(t, m.awaitDelay(59*time.Second), "every poll while the outcome is awaited")
	require.Equal(t, 15*time.Second, m.awaitDelay(time.Minute))
	require.Equal(t, 2*time.Minute+30*time.Second, m.awaitDelay(10*time.Minute))
	require.Equal(t, mt5AwaitCeiling, m.awaitDelay(time.Hour))
	require.Equal(t, mt5AwaitCeiling, m.awaitDelay(23*time.Hour))
}
