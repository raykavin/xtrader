package llm

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/storage"
	"github.com/raykavin/xtrader/ui"
)

// smcHarness runs a Trader of the prompt smc_ict as the agent does: its orders
// through the order controller of the bot, on a paper wallet, kept in a
// storage, which is the order source of the Trader, its decisions in a store
// of their own.
type smcHarness struct {
	*traderRunner
	controller *order.Controller
	memory     storage.Storage
	decisions  *MemoryDecisions
}

// smcTrader creates a Trader of the prompt content with the configuration of
// h, its orders and decisions the ones of h, as after a restart.
func (h *smcHarness) smcTrader(t *testing.T, content string) *Trader {
	t.Helper()
	trader, err := New(h.trader.cfg, h.provider, WithPrompt(Prompt{Ref: ui.DecisionPrompt{Name: "smc_ict"},
		Content: content}, PromptOverrides{}))
	require.NoError(t, err)
	trader.SetDecisionStore(h.decisions)
	trader.SetOrderSource(func(pair string) ([]model.Order, error) {
		orders, err := h.memory.Orders(storage.WithPair(pair))
		if err != nil {
			return nil, err
		}
		list := make([]model.Order, 0, len(orders))
		for _, o := range orders {
			list = append(list, *o)
		}
		return list, nil
	})
	return trader
}

// newSMCHarness creates the harness, configure changing the configuration of
// the Trader: the distances in ATR off, the levels of the tests free.
func newSMCHarness(t *testing.T, configure func(*Config)) *smcHarness {
	t.Helper()
	r := newTraderRunner(t, func(c *Config) {
		c.MaxEntryATR, c.MaxLevelATR = 0, 0
		if configure != nil {
			configure(c)
		}
	}, 0)
	memory, err := storage.FromMemory()
	require.NoError(t, err)
	h := &smcHarness{traderRunner: r, memory: memory, decisions: NewMemoryDecisions(0),
		controller: order.NewController(context.Background(), r.wallet, memory, order.NewOrderFeed())}
	h.trader = h.smcTrader(t, seededPrompt(t, "smc_ict"))
	h.broker = h.controller
	if h.trader.cfg.Live {
		h.broker = lastClose{Controller: h.controller, df: r.df} // the paper wallet without a feed
	}
	return h
}

// lastClose is the order controller reading the last price of a pair from the
// candles of the test, the last close, as the broker of a live Trader.
type lastClose struct {
	*order.Controller
	df *model.Dataframe
}

func (l lastClose) LastQuote(string) (float64, error) {
	return l.df.Close.Last(0), nil
}

// candle closes a candle of high, low and close, its open the previous close,
// the provider answering answers, all of them asked: the wallet fills on it,
// and the order controller settles the fills as its ticker does.
func (h *smcHarness) candle(high, low, close float64, answers ...answer) {
	h.t.Helper()
	df := h.df
	open := df.Close.Last(0)
	at := df.Time[len(df.Time)-1].Add(15 * time.Minute)
	candle := model.Candle{Pair: testPair, Time: at, Open: open, Close: close, High: high, Low: low, Volume: 100,
		Complete: true}
	h.wallet.OnCandle(candle)
	h.controller.UpdateOrders()
	df.Time = append(df.Time, at)
	df.Open = append(df.Open, open)
	df.Close = append(df.Close, close)
	df.High = append(df.High, high)
	df.Low = append(df.Low, low)
	df.Volume = append(df.Volume, candle.Volume)
	for key, value := range h.meta {
		series := df.Metadata[key]
		for len(series) < len(df.Close)-1 {
			series = append(series, math.NaN())
		}
		df.Metadata[key] = append(series, value)
	}
	h.provider.answers = answers
	h.trader.Indicators(df)
	h.trader.OnCandle(df, h.broker)
	if h.trader.cfg.Live {
		h.slotTick() // live, asked on the first tick of the next candle, never at the close
	}
	require.Empty(h.t, h.provider.answers, "answers not asked")
}

// candleTime returns the open time of the candle ago candles before the next
// one, as the context writes it.
func (h *smcHarness) candleTime(ago int) string {
	return h.df.Time[len(h.df.Time)-1].Add(time.Duration(1-ago) * 15 * time.Minute).UTC().Format(time.RFC3339)
}

// stored returns the orders of the storage by their type, the last of each.
func (h *smcHarness) stored(t *testing.T) map[model.OrderType]model.Order {
	t.Helper()
	orders, err := h.memory.Orders(storage.WithPair(testPair))
	require.NoError(t, err)
	byType := make(map[model.OrderType]model.Order)
	for _, o := range orders {
		byType[o.Type] = *o
	}
	return byType
}

// smcAnswer is a decision of action at entry, stop and profit with drawings.
func smcAnswer(action Action, entry, stop, profit float64, drawings ...Drawing) answer {
	d := Decision{Action: action, Confidence: 0.8, Reason: "smc", Drawings: drawings}
	if entry > 0 {
		d.Entry = ptr(entry)
	}
	if stop > 0 {
		d.StopLoss = ptr(stop)
	}
	if profit > 0 {
		d.TakeProfit = ptr(profit)
	}
	return answer{decision: d}
}

// scenario returns the drawings of a scenario: the fair value gap of the
// entry, the liquidity targeted and the change of character.
func (h *smcHarness) scenario() []Drawing {
	return []Drawing{
		{ID: "fvg-1", Kind: DrawingZone, Label: "FVG", Bias: BiasBullish, Timeframe: "15m", From: h.candleTime(4),
			Low: ptr(98.4), High: ptr(98.9)},
		{ID: "bsl", Kind: DrawingLevel, Label: "BSL", Bias: BiasBullish, Timeframe: "15m", From: h.candleTime(10),
			Price: ptr(102)},
		{ID: "choch", Kind: DrawingMarker, Label: "CHoCH", Bias: BiasBullish, Timeframe: "15m",
			From: h.candleTime(2), Price: ptr(99.6)},
	}
}

// The whole flow of the prompt smc_ict on a simulated market: the analysis and
// its drawings, a pending buy limit placed with its stop loss and take profit,
// followed by the next analyses (the model moves it), its fill (the position
// held with its legs, its stop loss moved) and its close by the take profit,
// each step told to the chart.
func TestTrader_SMCFlow(t *testing.T) {
	h := newSMCHarness(t, nil)

	// 1. the analysis: a buy limit at the gap, drawn
	h.candle(100.2, 99.8, 100, smcAnswer(ActionBuyLimit, 98.5, 97.5, 102, h.scenario()...))
	in := h.provider.last()
	require.Equal(t, []string{PendingLimit}, in.Rules.PendingOrders)
	require.Equal(t, []Action{ActionWait, ActionEnterLong, ActionEnterShort, ActionBuyLimit, ActionSellLimit},
		in.Rules.Actions, "the entries at the market and the limits, no stop: not of the prompt")
	require.NotNil(t, in.SMC, "the structure in smart money concepts")
	require.True(t, in.drawings)
	require.Empty(t, in.Drawings, "nothing drawn yet")
	require.Nil(t, in.PendingOrder)
	require.Contains(t, h.provider.systems[len(h.provider.systems)-1], "Pending orders, of the types")

	d := h.lastDecision()
	require.Equal(t, string(ActionBuyLimit), d.Action)
	require.Equal(t, OutcomeApplied, d.Outcome)
	require.Len(t, d.OrderIDs, 3, "the entry and its legs")
	require.Len(t, d.Drawings, 3)
	stored := h.stored(t)
	entry := stored[model.OrderTypeLimit]
	require.Equal(t, model.OrderStatusTypeNew, entry.Status)
	require.Equal(t, model.SideTypeBuy, entry.Side)
	require.InDelta(t, 98.5, entry.Price, 1e-9)
	require.InDelta(t, 97.5, *stored[model.OrderTypeStopLoss].Stop, 1e-9)
	require.InDelta(t, 102, stored[model.OrderTypeLimitMaker].Price, 1e-9)
	require.Equal(t, d.ID, h.decisions.OrderDecision(entry.ID), "placed by the decision")

	analysis, ok := h.trader.ChartAnalysis(testPair)
	require.True(t, ok)
	require.Equal(t, d.ID, analysis.DecisionID)
	require.Equal(t, "15m", analysis.Timeframe)
	require.Len(t, analysis.Drawings, 3)
	require.Nil(t, analysis.Intent, "the order placed is drawn as an order")

	// 2. not filled: the model sees it, and moves it
	h.candle(99.9, 99, 99.2, smcAnswer(ActionModifyPending, 98.8, 97.8, 102, h.scenario()...))
	in = h.provider.last()
	require.Equal(t, &PendingOrder{Type: ActionBuyLimit, Entry: 98.5, StopLoss: 97.5, TakeProfit: 102, Size: 10,
		Candles: 1, PlacedBy: PlacedByModel}, in.PendingOrder)
	require.Equal(t, []Action{ActionWait, ActionModifyPending, ActionCancelPending}, in.Rules.Actions,
		"no other entry while it waits")
	require.Len(t, in.Drawings, 3, "the drawings of the chart, for the model to keep")
	d = h.lastDecision()
	require.Equal(t, OutcomeApplied, d.Outcome)
	require.Len(t, d.OrderIDs, 2, "the entry and its stop loss moved, the take profit where it is")
	stored = h.stored(t)
	require.InDelta(t, 98.8, stored[model.OrderTypeLimit].Price, 1e-9)
	require.InDelta(t, 97.8, *stored[model.OrderTypeStopLoss].Stop, 1e-9)
	require.Equal(t, entry.ID, stored[model.OrderTypeLimit].ID, "moved, not replaced")

	// 3. filled at its limit: the position held with its legs, its stop loss
	// moved by the model
	h.candle(99, 98.6, 98.7, smcAnswer(ActionModifySL, 0, 98.3, 0, h.scenario()...))
	in = h.provider.last()
	require.Nil(t, in.PendingOrder)
	require.NotNil(t, in.Position)
	require.Equal(t, number(98.8), in.Position.Entry)
	require.Equal(t, number(97.8), in.Position.StopLoss)
	require.Equal(t, number(102), in.Position.TakeProfit)
	require.Equal(t, number(97.8), in.Position.InitialStop)
	require.Contains(t, in.Rules.Actions, ActionModifySL)
	require.InDelta(t, 10, h.position(), 1e-9)
	d = h.lastDecision()
	require.Equal(t, OutcomeApplied, d.Outcome)
	require.True(t, slices.ContainsFunc(d.Notes, func(n ui.DecisionNote) bool {
		return strings.Contains(n.Text, "filled at 98.8: the LONG position followed with its stop loss 97.8 and "+
			"take profit 102")
	}), "the fill told: %v", d.Notes)
	stored = h.stored(t)
	require.Equal(t, model.OrderStatusTypeFilled, stored[model.OrderTypeLimit].Status)
	require.InDelta(t, 98.3, *stored[model.OrderTypeStopLoss].Stop, 1e-9)

	// 4. the take profit hit: flat, the model asked about the next entry
	h.candle(102.3, 100.5, 102.1, smcAnswer(ActionWait, 0, 0, 0))
	require.Zero(t, h.position())
	require.Nil(t, h.provider.last().Position)
	require.Empty(t, h.trader.trades)
	require.Empty(t, h.trader.pendings)
	analysis, _ = h.trader.ChartAnalysis(testPair)
	require.Empty(t, analysis.Drawings, "the last analysis drew nothing: the scenario is over")

	decisions, err := h.trader.Decisions(testPair, 10)
	require.NoError(t, err)
	actions := make([]string, 0, len(decisions))
	for _, d := range decisions {
		actions = append(actions, d.Action)
	}
	require.Equal(t, []string{"WAIT", "MODIFY_SL", "MODIFY_PENDING", "BUY_LIMIT"}, actions)
}

// The model cancels its pending order: its legs go with it, the orders linked
// to the decision.
func TestTrader_PendingCanceledByTheModel(t *testing.T) {
	h := newSMCHarness(t, nil)
	h.candle(100.2, 99.8, 100, smcAnswer(ActionBuyLimit, 98.5, 97.5, 102))
	h.candle(100.1, 99.5, 99.8, smcAnswer(ActionCancelPending, 0, 0, 0))

	d := h.lastDecision()
	require.Equal(t, OutcomeApplied, d.Outcome)
	require.Len(t, d.OrderIDs, 3, "the entry and its legs canceled")
	require.Empty(t, h.trader.pendings)
	h.controller.UpdateOrders()
	for _, o := range h.stored(t) {
		require.Equal(t, model.OrderStatusTypeCanceled, o.Status, o.Type)
	}

	// flat again, with the entries
	h.candle(100.1, 99.5, 99.9, smcAnswer(ActionWait, 0, 0, 0))
	require.Nil(t, h.provider.last().PendingOrder)
	require.Contains(t, h.provider.last().Rules.Actions, ActionBuyLimit)
}

// The system cancels a pending order, the model not asked about it: once it
// waited its expiry, once an entry of its type isn't allowed (the spread too
// wide, the prompt switched to one without pending orders), and once a
// position is held on the pair.
func TestTrader_PendingCanceledBySystem(t *testing.T) {
	note := func(t *testing.T, d ui.Decision, text string) {
		t.Helper()
		require.True(t, slices.ContainsFunc(d.Notes, func(n ui.DecisionNote) bool {
			return strings.Contains(n.Text, text)
		}), "%q not in %v", text, d.Notes)
	}

	t.Run("its expiry", func(t *testing.T) {
		h := newSMCHarness(t, func(c *Config) { c.PendingExpiry = 2 })
		h.candle(100.2, 99.8, 100, smcAnswer(ActionBuyLimit, 98.5, 97.5, 102))
		require.Equal(t, 2, h.provider.last().Rules.PendingExpiry)
		h.candle(100.1, 99.5, 99.8, smcAnswer(ActionWait, 0, 0, 0))
		require.NotNil(t, h.provider.last().PendingOrder, "kept by WAIT")
		h.candle(100.1, 99.5, 99.8, smcAnswer(ActionWait, 0, 0, 0))
		require.Nil(t, h.provider.last().PendingOrder, "canceled at the analysis, which goes on flat")
		note(t, h.lastDecision(), "not filled after 2 candles, its expiry")
		require.Empty(t, h.trader.pendings)
		h.controller.UpdateOrders()
		require.Equal(t, model.OrderStatusTypeCanceled, h.stored(t)[model.OrderTypeLimit].Status)
	})

	t.Run("the spread too wide for an entry", func(t *testing.T) {
		h := newSMCHarness(t, func(c *Config) { c.MaxSpreadATR = 0.5 })
		h.candle(100.2, 99.8, 100, smcAnswer(ActionBuyLimit, 98.5, 97.5, 102))
		h.meta = map[string]float64{"spread": 50}
		h.candle(100.1, 99.5, 99.8) // only WAIT left, flat: nothing to ask
		d := h.lastDecision()
		require.Equal(t, OutcomeSkipped, d.Outcome)
		require.Equal(t, SkipSpread, d.Reason)
		note(t, d, "an entry of its type isn't allowed now: the spread")
		require.Empty(t, h.trader.pendings)
	})

	t.Run("a prompt without pending orders", func(t *testing.T) {
		h := newSMCHarness(t, nil)
		h.candle(100.2, 99.8, 100, smcAnswer(ActionBuyLimit, 98.5, 97.5, 102))
		h.trader.prompts = staticPrompts{Prompt{Ref: ui.DecisionPrompt{Name: "default", Version: 2},
			Content: defaultStrategy}}
		h.candle(100.1, 99.5, 99.8, decide(ActionWait, 0.5, nil, nil, nil))
		in := h.provider.last()
		require.Nil(t, in.PendingOrder)
		require.Empty(t, in.Rules.PendingOrders)
		note(t, h.lastDecision(), "an entry of its type isn't allowed now")
		require.Empty(t, h.trader.pendings)
	})

	t.Run("a position held on the pair, on a tick", func(t *testing.T) {
		h := newSMCHarness(t, nil)
		var told []string
		h.trader.SetNotifier(func(message string) { told = append(told, message) })
		h.candle(100.2, 99.8, 100, smcAnswer(ActionBuyLimit, 98.5, 97.5, 102))
		_, err := h.wallet.CreateOrderMarket(model.SideTypeBuy, testPair, 3) // eg: the trade panel
		require.NoError(t, err)
		h.trader.OnPartialCandle(h.df, h.broker)
		require.Empty(t, h.trader.pendings, "it would add to the position")
		require.Len(t, told, 1)
		require.Contains(t, told[0], "a LONG position is held on the pair, which it would add to")
		h.controller.UpdateOrders()
		require.Equal(t, model.OrderStatusTypeCanceled, h.stored(t)[model.OrderTypeLimit].Status)
	})
}

// staticPrompts is a PromptSource switching to its prompt.
type staticPrompts struct{ prompt Prompt }

func (s staticPrompts) Current(known ui.DecisionPrompt) (Prompt, bool) {
	return s.prompt, known != s.prompt.Ref
}

// The window of the news block cancels a pending order on the ticks, as an
// entry would be refused then: the start of the next window read at the
// analysis.
func TestTrader_PendingCanceledByTheNewsBlock(t *testing.T) {
	calendar := &fakeCalendar{}
	h := newSMCHarness(t, func(c *Config) {
		c.Live = true
		c.Calendar, c.CalendarCurrencies = calendar, []string{"USD"}
	})
	closeAt := h.nextClose()
	h.trader.now = func() time.Time { return closeAt.Add(time.Second) }
	h.trader.handled[testPair] = h.df.Time[len(h.df.Time)-1] // started: the slot of the close asked
	calendar.events = []Event{release(closeAt.Add(20*time.Minute), "cpi", ImportanceHigh, math.NaN())}
	var told []string
	h.trader.SetNotifier(func(message string) { told = append(told, message) })

	h.candle(100.2, 99.8, 100, smcAnswer(ActionBuyLimit, 98.5, 97.5, 102))
	require.Len(t, h.trader.pendings, 1)
	require.Equal(t, closeAt.Add(5*time.Minute), h.trader.pendings[testPair].blockAt, "15 minutes before")

	h.trader.now = func() time.Time { return closeAt.Add(4 * time.Minute) }
	h.trader.OnPartialCandle(h.df, h.broker)
	require.Len(t, h.trader.pendings, 1, "before the window")

	h.trader.now = func() time.Time { return closeAt.Add(6 * time.Minute) }
	h.trader.OnPartialCandle(h.df, h.broker)
	require.Empty(t, h.trader.pendings)
	require.Len(t, told, 1)
	require.Contains(t, told[0], "no entry around the release USD cpi")
}

// After a restart, the pending order found open is followed with its legs,
// placed by the model as its decision tells, the model asked about it.
func TestTrader_PendingFoundAfterARestart(t *testing.T) {
	h := newSMCHarness(t, nil)
	h.candle(100.2, 99.8, 100, smcAnswer(ActionBuyLimit, 98.5, 97.5, 102, h.scenario()...))
	placed := h.lastDecision()

	h.trader = h.smcTrader(t, seededPrompt(t, "smc_ict")) // a restart, on the same storage
	h.candle(100.1, 99.5, 99.8, smcAnswer(ActionWait, 0, 0, 0, h.scenario()...))
	in := h.provider.last()
	require.NotNil(t, in.PendingOrder)
	require.Equal(t, ActionBuyLimit, in.PendingOrder.Type)
	require.Equal(t, number(97.5), in.PendingOrder.StopLoss)
	require.Equal(t, number(102), in.PendingOrder.TakeProfit)
	require.Equal(t, PlacedByModel, in.PendingOrder.PlacedBy)
	require.Len(t, in.Drawings, 3, "the drawings of the chart read back from the decisions")
	d := h.lastDecision()
	require.True(t, slices.ContainsFunc(d.Notes, func(n ui.DecisionNote) bool {
		return strings.Contains(n.Text, "found open on the pair, placed by the model: followed")
	}), "%v", d.Notes)

	analysis, ok := h.trader.ChartAnalysis(testPair)
	require.True(t, ok)
	require.Equal(t, d.ID, analysis.DecisionID, "the last analysis")
	require.NotEqual(t, placed.ID, analysis.DecisionID)

	// a pending order of the trader, eg: of the trade panel, placed while flat
	h = newSMCHarness(t, nil)
	_, err := h.controller.CreateOrderLimitProtected(model.SideTypeSell, testPair, 1, 101.5, 102.5, 98)
	require.NoError(t, err)
	h.candle(100.2, 99.8, 100, smcAnswer(ActionWait, 0, 0, 0))
	require.NotNil(t, h.provider.last().PendingOrder)
	require.Equal(t, ActionSellLimit, h.provider.last().PendingOrder.Type)
	require.Equal(t, PlacedByTrader, h.provider.last().PendingOrder.PlacedBy)
}

// The entry of a stop, which the brokers can't move, is replaced: canceled and
// placed again at its new levels; its legs alone are moved where they are.
func TestTrader_PendingStopReplaced(t *testing.T) {
	stops := strings.Replace(seededPrompt(t, "smc_ict"), "# Pending orders: limit", "# Pending orders: limit, stop", 1)
	h := newSMCHarness(t, nil)
	h.trader = h.smcTrader(t, stops)
	h.candle(100.2, 99.8, 100, smcAnswer(ActionBuyStop, 101, 99.5, 104))
	require.Contains(t, h.provider.last().Rules.Actions, ActionBuyStop)
	first := h.trader.pendings[testPair]
	require.NotNil(t, first)
	require.Equal(t, PendingStop, pendingKindOf(first.entry))

	h.candle(100.4, 99.9, 100.2, smcAnswer(ActionModifyPending, 101, 99.8, 104))
	d := h.lastDecision()
	require.Len(t, d.OrderIDs, 1, "its stop loss alone moved")
	require.Equal(t, first.entry.ID, h.trader.pendings[testPair].entry.ID)

	h.candle(100.4, 99.9, 100.1, smcAnswer(ActionModifyPending, 101.5, 99.8, 104))
	d = h.lastDecision()
	require.Equal(t, OutcomeApplied, d.Outcome)
	replaced := h.trader.pendings[testPair]
	require.NotNil(t, replaced)
	require.NotEqual(t, first.entry.ID, replaced.entry.ID, "placed again")
	require.InDelta(t, 101.5, replaced.price(), 1e-9)
	require.Len(t, d.OrderIDs, 6, "the orders canceled and the ones placed")

	// filled when the price rises to it
	h.candle(101.8, 100.3, 101.6, smcAnswer(ActionHold, 0, 0, 0))
	require.InDelta(t, 10, h.position(), 1e-9)
	require.Equal(t, number(101.5), h.provider.last().Position.Entry)
}

// The analysis drawn on the chart is the last one the model answered,
// whatever its outcome, with the entry it proposed when it placed no order;
// a request that failed leaves it.
func TestTrader_ChartAnalysis(t *testing.T) {
	h := newSMCHarness(t, nil)
	_, ok := h.trader.ChartAnalysis(testPair)
	require.False(t, ok, "none yet")

	// a buy limit above the price: refused, its intent drawn
	h.candle(100.2, 99.8, 100, smcAnswer(ActionBuyLimit, 100.5, 99, 103, h.scenario()...))
	d := h.lastDecision()
	require.Equal(t, OutcomeRefused, d.Outcome)
	analysis, ok := h.trader.ChartAnalysis(testPair)
	require.True(t, ok)
	require.Equal(t, d.ID, analysis.DecisionID)
	require.Len(t, analysis.Drawings, 3, "the analysis of a decision refused drawn")
	require.Equal(t, &ui.Intent{Action: "BUY_LIMIT", Side: "BUY", OrderType: ui.IntentLimit, Entry: ptr(100.5),
		StopLoss: ptr(99), TakeProfit: ptr(103), Outcome: OutcomeRefused, Error: d.Error}, analysis.Intent)

	// ignored under the min confidence: drawn too
	ignored := smcAnswer(ActionSellLimit, 101, 102, 98)
	ignored.decision.Confidence = 0.3
	h.candle(100.2, 99.8, 100, ignored)
	analysis, _ = h.trader.ChartAnalysis(testPair)
	require.Equal(t, OutcomeIgnored, analysis.Outcome)
	require.Equal(t, "SELL", analysis.Intent.Side)
	require.Empty(t, analysis.Drawings, "none drawn by this one")

	// a request failed: the analysis before kept
	h.candle(100.2, 99.8, 100, answer{err: ErrTimeout})
	again, _ := h.trader.ChartAnalysis(testPair)
	require.Equal(t, analysis.DecisionID, again.DecisionID)
}

// The prompts without the headers of the pending orders, of the drawings and
// of the smart money concepts trade as before: no pending action, no rule,
// no drawing nor smc in the context, the schema of before, and the pending
// orders of the trader on the pair left alone.
func TestTrader_PromptsOfBeforeUnchanged(t *testing.T) {
	h := newSMCHarness(t, nil)
	h.trader = h.smcTrader(t, seededPrompt(t, "support_resistance"))
	_, err := h.controller.CreateOrderLimitProtected(model.SideTypeBuy, testPair, 1, 98, 97, 102)
	require.NoError(t, err)

	h.candle(100.2, 99.8, 100, decide(ActionWait, 0.4, nil, nil, nil))
	in := h.provider.last()
	require.Equal(t, flatActions, in.Rules.Actions)
	require.Equal(t, decisionShape{}, shapeOf(in))
	data, err := json.Marshal(in)
	require.NoError(t, err)
	for _, key := range []string{`"pending_order"`, `"chart_drawings"`, `"smc"`, `"pending_orders"`,
		`"pending_expiry_candles"`} {
		require.NotContains(t, string(data), key)
	}
	require.Equal(t, basePrompt+"\n\nStrategy:\n"+stripComments(seededPrompt(t, "support_resistance")),
		h.provider.systems[len(h.provider.systems)-1])
	require.Empty(t, h.trader.pendings, "the order of the trader not followed")
	require.Equal(t, model.OrderStatusTypeNew, h.stored(t)[model.OrderTypeLimit].Status, "nor canceled")
}
