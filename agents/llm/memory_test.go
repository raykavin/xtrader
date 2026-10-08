package llm

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// The trades of the incident: a short stopped out, a short at its take profit,
// a long at its take profit.
func incidentOrders() []model.Order {
	base := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	group := int64(1)
	fill := func(id int64, minute int, side model.SideType, kind model.OrderType, price float64, grouped bool) model.Order {
		at := base.Add(time.Duration(minute) * time.Minute)
		order := model.Order{ID: id, Pair: testPair, Side: side, Type: kind, Status: model.OrderStatusTypeFilled,
			Price: price, Quantity: 0.1, CreatedAt: at, UpdatedAt: at}
		if grouped {
			order.GroupID = &group
		}
		return order
	}
	return []model.Order{
		fill(1, 0, model.SideTypeSell, model.OrderTypeMarket, 84329.07, false),
		fill(3, 15, model.SideTypeBuy, model.OrderTypeStopLoss, 84474, true),
		fill(4, 270, model.SideTypeSell, model.OrderTypeMarket, 84447.38, false),
		fill(5, 300, model.SideTypeBuy, model.OrderTypeLimitMaker, 84320, true),
		fill(7, 315, model.SideTypeBuy, model.OrderTypeMarket, 84299.94, false),
		fill(8, 345, model.SideTypeSell, model.OrderTypeLimitMaker, 84449, true),
		{ID: 9, Pair: testPair, Side: model.SideTypeSell, Type: model.OrderTypeStopLoss, Price: 84248.6, Quantity: 0.1,
			Status: model.OrderStatusTypeCanceled, GroupID: &group, UpdatedAt: base.Add(345 * time.Minute)},
	}
}

func TestClosedTrades(t *testing.T) {
	trades := closedTrades(incidentOrders(), nil)
	require.Len(t, trades, 3)
	require.Equal(t, model.SideTypeSell, trades[0].side)
	require.Equal(t, "stop loss", trades[0].exitBy)
	require.InDelta(t, -0.1719, trades[0].resultPct, 1e-4)
	require.Equal(t, "take profit", trades[1].exitBy)
	require.InDelta(t, 0.1508, trades[1].resultPct, 1e-4)
	require.Equal(t, model.SideTypeBuy, trades[2].side)
	require.Equal(t, 84299.94, trades[2].entry)
	require.Equal(t, 84449.0, trades[2].exit)
	require.InDelta(t, 0.1768, trades[2].resultPct, 1e-4)

	// a reversal closes the position and opens the rest at its price
	at := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	reversal := closedTrades([]model.Order{
		{ID: 1, Side: model.SideTypeBuy, Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 100,
			Quantity: 1, UpdatedAt: at},
		{ID: 2, Side: model.SideTypeSell, Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 110,
			Quantity: 3, UpdatedAt: at.Add(time.Minute)},
		{ID: 3, Side: model.SideTypeBuy, Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled, Price: 105,
			Quantity: 2, UpdatedAt: at.Add(2 * time.Minute)},
	}, nil)
	require.Len(t, reversal, 2)
	require.Equal(t, "exit at the market", reversal[0].exitBy)
	require.Equal(t, 110.0, reversal[1].entry, "the short opened by the reversal")
	require.InDelta(t, 4.5454, reversal[1].resultPct, 1e-3)
}

// The context remembers the last decisions and the trades closed, newest first.
func TestTrader_Memory(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.Memory = 2 }, 0)
	r.trader.SetOrderSource(func(string) ([]model.Order, error) { return incidentOrders(), nil })
	long := strings.Repeat("a", 300)
	r.step(100, answer{decision: Decision{Action: ActionWait, Confidence: 0.4, Reason: "first"}})
	r.step(100.5, answer{decision: Decision{Action: ActionWait, Confidence: 0.5, Reason: long}})
	r.step(101, answer{decision: Decision{Action: ActionWait, Confidence: 0.6, Reason: "third"}})

	memory := r.provider.last().Memory
	require.NotNil(t, memory)
	require.Len(t, memory.Decisions, 2, "Config.Memory")
	require.Equal(t, 1, memory.Decisions[0].CandlesAgo, "the newest first")
	require.Equal(t, ratio(0.5), memory.Decisions[0].Confidence)
	require.Len(t, []rune(memory.Decisions[0].Reason), maxReason, "cut")
	require.Equal(t, "first", memory.Decisions[1].Reason)
	require.Len(t, memory.Trades, 2)
	require.Equal(t, "LONG", memory.Trades[0].Side, "the last trade first")
	require.Equal(t, "take profit", memory.Trades[0].ExitBy)
	require.Equal(t, 2, memory.Trades[0].HeldCandles, "30 minutes of 15m")

	first := newTraderRunner(t, func(c *Config) { c.Memory = 0 }, 0)
	first.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Nil(t, first.provider.last().Memory, "disabled")
}

// The journal holds every decision with its context, and gives back the last
// ones to the memory of a Trader started again.
func TestTrader_Journal(t *testing.T) {
	journal := &testJournal{}
	r := newTraderRunner(t, nil, 0)
	require.NoError(t, r.trader.SetJournal(journal))
	r.step(100, answer{decision: Decision{Action: ActionWait, Confidence: 0.4, Reason: "no level"}})
	r.provider.answers = []answer{{err: ErrUnavailable}}
	r.add(100.5)
	r.trader.Indicators(r.df)
	r.trader.OnCandle(r.df, r.broker)

	require.Len(t, journal.entries, 2)
	first := journal.entries[0]
	require.Equal(t, "no level", first.Decision.Reason)
	require.Equal(t, "fake test-model", first.Model)
	require.Equal(t, testPair, first.Input.Symbol)
	data, err := json.Marshal(first.Input)
	require.NoError(t, err)
	require.Contains(t, string(data), `"candles":`, "the whole context")
	require.Contains(t, string(data), `"structure":`)
	require.Nil(t, journal.entries[1].Decision)
	require.Contains(t, journal.entries[1].Error, "unavailable")

	again := newTraderRunner(t, nil, 0)
	require.NoError(t, again.trader.SetJournal(journal))
	require.Len(t, again.trader.remembered[testPair], 1, "the decision given back, not the error")
	require.Equal(t, "no level", again.trader.remembered[testPair][0].reason)
}

// links tells the positions of the fills of a test by their ID, as the MT5
// adapter tells them (see fillPositions): the one each opened and the ones it
// closed.
type links map[int64]struct {
	opened int64
	closed []int64
}

func (l links) link(fill model.Order) (int64, []int64, bool) {
	positions, ok := l[fill.ID]
	return positions.opened, positions.closed, ok
}

// B6: a fill closing a position whose entry isn't among the orders, eg: the
// take profit of yesterday of an entry never stored, is left out, never taken
// for an entry: the trades after it keep their own positions, rather than the
// long closed at its take profit read as a short closed at the entry of the
// long and a short opened at the take profit.
func TestTradesOf_OrphanLegLeftOut(t *testing.T) {
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	orphanGroup, group := int64(99), int64(10)
	orders := []model.Order{
		{ID: 1, GroupID: &orphanGroup, Side: model.SideTypeSell, Type: model.OrderTypeLimitMaker,
			Status: model.OrderStatusTypeFilled, Price: 95, Quantity: 1, CreatedAt: at.Add(-26 * time.Hour),
			UpdatedAt: at.Add(-25 * time.Hour)},
		filled(10, model.SideTypeBuy, 100, 1, at),
		legOf(11, group, model.SideTypeSell, model.OrderTypeStopLoss, model.OrderStatusTypeCanceled, 98, 1,
			at.Add(30*time.Minute)),
		legOf(12, group, model.SideTypeSell, model.OrderTypeLimitMaker, model.OrderStatusTypeFilled, 104, 1,
			at.Add(30*time.Minute)),
	}
	trades, open, roles, orphans := tradesOf(orders, nil)
	require.Len(t, trades, 1)
	require.Equal(t, model.SideTypeBuy, trades[0].side, "the long, not a short")
	require.Equal(t, 100.0, trades[0].entry)
	require.Equal(t, 104.0, trades[0].exit)
	require.Equal(t, "take profit", trades[0].exitBy)
	require.Equal(t, int64(10), trades[0].open.ID)
	require.Nil(t, open, "flat: the take profit opens nothing")
	require.Equal(t, map[int64]string{1: "exit", 10: "entry", 12: "exit"}, roles)
	require.Len(t, orphans, 1)
	require.Equal(t, int64(1), orphans[0].ID)
}

// B6: told apart by position (MT5, by their tickets), a market order closing
// a position the orders don't hold is left out too, and each exit closes its
// own position, whatever the order of the entries: not first in, first out.
func TestTradesOf_ByPosition(t *testing.T) {
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	orders := []model.Order{
		filled(1, model.SideTypeSell, 95, 1, at.Add(-25*time.Hour)), // closes the position 5, unknown
		filled(10, model.SideTypeBuy, 100, 1, at),
		filled(20, model.SideTypeBuy, 102, 1, at.Add(time.Minute)),
		filled(30, model.SideTypeSell, 103, 1, at.Add(2*time.Minute)), // closes the second long
		filled(40, model.SideTypeSell, 99, 1, at.Add(3*time.Minute)),  // closes the first one
	}
	link := links{1: {closed: []int64{5}}, 10: {opened: 10}, 20: {opened: 20}, 30: {closed: []int64{20}},
		40: {closed: []int64{10}}}
	trades, open, roles, orphans := tradesOf(orders, link.link)
	require.Len(t, trades, 2)
	require.Equal(t, int64(20), trades[0].open.ID)
	require.Equal(t, int64(30), trades[0].close.ID)
	require.InDelta(t, 0.98, trades[0].resultPct, 1e-2, "102 to 103")
	require.Equal(t, int64(10), trades[1].open.ID)
	require.Equal(t, int64(40), trades[1].close.ID)
	require.InDelta(t, -1, trades[1].resultPct, 1e-9, "100 to 99")
	require.Nil(t, open)
	require.Equal(t, map[int64]string{1: "exit", 10: "entry", 20: "entry", 30: "exit", 40: "exit"}, roles)
	require.Len(t, orphans, 1)

	// an order of a previous run closing both, the broker telling its first
	// position only; then an inversion, its rest opening the position told
	orders = []model.Order{
		filled(10, model.SideTypeBuy, 100, 1, at),
		filled(20, model.SideTypeBuy, 102, 1, at.Add(time.Minute)),
		filled(30, model.SideTypeSell, 104, 2, at.Add(2*time.Minute)),
		filled(40, model.SideTypeBuy, 101, 1, at.Add(3*time.Minute)),
		filled(50, model.SideTypeSell, 103, 3, at.Add(4*time.Minute)),
	}
	link = links{10: {opened: 10}, 20: {opened: 20}, 30: {closed: []int64{10}}, 40: {opened: 40},
		50: {opened: 50, closed: []int64{40}}}
	trades, open, _, orphans = tradesOf(orders, link.link)
	require.Len(t, trades, 3)
	require.Equal(t, []int64{10, 20, 40}, []int64{trades[0].open.ID, trades[1].open.ID, trades[2].open.ID})
	require.Empty(t, orphans)
	require.NotNil(t, open)
	require.Equal(t, model.SideTypeSell, open.side, "the short of the inversion")
	require.Equal(t, 2.0, open.size)
	require.Equal(t, int64(50), open.open.ID)

	// unknown to the broker, eg: an inversion of a previous run, first in,
	// first out, its rest the position of its ticket, which a fill told
	// apart closes then
	inversion := filled(20, model.SideTypeSell, 104, 2, at.Add(time.Minute))
	inversion.ExchangeID = 20
	orders = []model.Order{filled(10, model.SideTypeBuy, 100, 1, at), inversion,
		filled(30, model.SideTypeBuy, 101, 1, at.Add(2*time.Minute))}
	link = links{30: {closed: []int64{20}}}
	trades, open, _, orphans = tradesOf(orders, link.link)
	require.Len(t, trades, 2)
	require.Equal(t, [2]int64{10, 20}, [2]int64{trades[0].open.ID, trades[0].close.ID})
	require.Equal(t, [2]int64{20, 30}, [2]int64{trades[1].open.ID, trades[1].close.ID})
	require.Nil(t, open)
	require.Empty(t, orphans)
}

// B6: an exit the broker tells closing a known position and an unknown one
// closes the known one only: the rest is the unknown one's, an orphan, never
// another position of the bot, which stays open at the broker.
func TestTradesOf_LinkedKnownAndUnknownPosition(t *testing.T) {
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	orders := []model.Order{
		filled(1, model.SideTypeBuy, 100, 1, at),
		filled(2, model.SideTypeBuy, 101, 1, at.Add(time.Minute)),
		filled(3, model.SideTypeSell, 102, 2, at.Add(2*time.Minute)),
	}
	link := links{1: {opened: 11}, 2: {opened: 33}, 3: {closed: []int64{11, 99}}}
	trades, open, roles, orphans := tradesOf(orders, link.link)
	require.Len(t, trades, 1)
	require.Equal(t, [2]int64{1, 3}, [2]int64{trades[0].open.ID, trades[0].close.ID})
	require.Equal(t, 1.0, trades[0].size)
	require.NotNil(t, open, "the position 33, open at the broker")
	require.Equal(t, int64(2), open.open.ID)
	require.Equal(t, 1.0, open.size)
	require.Equal(t, map[int64]string{1: "entry", 2: "entry", 3: "exit"}, roles)
	require.Len(t, orphans, 1)
	require.Equal(t, int64(3), orphans[0].ID)
}
