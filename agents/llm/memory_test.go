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
	trades := closedTrades(incidentOrders())
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
	})
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
