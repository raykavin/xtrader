package llm

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/ui"
)

func TestDecision_MinRiskReward(t *testing.T) {
	enter := func(action Action, stop, profit float64) Decision {
		return Decision{Action: action, Confidence: 0.8, Entry: ptr(100), StopLoss: ptr(stop), TakeProfit: ptr(profit),
			Reason: "ok"}
	}
	in := testInput(nil, flatActions...)
	require.NoError(t, enter(ActionEnterLong, 98, 101).Validate(in), "no minimum: 0.5 R taken")

	in.Rules.MinRiskReward = 1.5
	require.NoError(t, enter(ActionEnterLong, 98, 103).Validate(in))
	require.NoError(t, enter(ActionEnterShort, 102, 97).Validate(in))
	err := enter(ActionEnterLong, 98, 102.9).Validate(in)
	require.ErrorIs(t, err, ErrInvalidDecision)
	require.ErrorContains(t, err, "the risk:reward 1.45")
	require.ErrorIs(t, enter(ActionEnterShort, 102, 97.1).Validate(in), ErrInvalidDecision)

	// a long is filled at the ask: 100.5, 2.5 of risk, the reward only 2.5
	in.Spread = 0.5
	err = enter(ActionEnterLong, 98, 103).Validate(in)
	require.ErrorContains(t, err, "the risk:reward 1.00 (reward 2.5, risk 2.5 from the fill 100.5")
	require.NoError(t, enter(ActionEnterLong, 98, 104.25).Validate(in), "3.75 for 2.5")
	// a short is filled at the bid, the price, its levels hit at the ask
	require.NoError(t, enter(ActionEnterShort, 102, 97).Validate(in))

	// the levels, the holds and the moves aren't entries
	pos := testInput(longPosition(), heldActions...)
	pos.Rules.MinRiskReward = 3
	require.NoError(t, Decision{Action: ActionModifyTP, Confidence: 0.8, TakeProfit: ptr(101), Reason: "ok"}.Validate(pos))
}

// trending adds count candles to the runner, each one step from the last.
func (r *traderRunner) trending(count int, step float64) {
	for i := 0; i < count; i++ {
		r.add(r.df.Close.Last(0) + step)
	}
}

func TestTrader_TrendPullbackSetup(t *testing.T) {
	setup := func(c *Config) { c.Setup = SetupTrendPullback; c.MinRiskReward = 1.5 }

	up := newTraderRunner(t, setup, 0)
	up.trending(60, 0.2)
	up.step(up.df.Close.Last(0)-0.1, decide(ActionWait, 0.4, nil, nil, nil))
	in := up.provider.last()
	require.Equal(t, "up", in.Summary.Trend)
	require.Equal(t, []Action{ActionWait, ActionEnterLong}, in.Rules.Actions, "only the entry of the trend")
	require.Equal(t, SetupTrendPullback, in.Rules.Setup)
	require.Equal(t, number(1.5), in.Rules.MinRiskReward)

	down := newTraderRunner(t, setup, 0)
	down.trending(60, -0.2)
	down.step(down.df.Close.Last(0)+0.1, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, []Action{ActionWait, ActionEnterShort}, down.provider.last().Rules.Actions)

	// against the trend: refused by the system
	down.step(down.df.Close.Last(0)-0.2, enterLong(down.df.Close.Last(0)-0.2))
	require.Zero(t, down.position())

	// no trend: the close still above the SMA, the fast EMA fallen under the slow one
	mixed := newTraderRunner(t, setup, 0)
	mixed.trending(60, 0.2)
	mixed.trending(12, -0.3)
	mixed.step(mixed.df.Close.Last(0) - 0.3)
	require.Less(t, mixed.df.Metadata["llm_ema_fast"].Last(0), mixed.df.Metadata["llm_ema_slow"].Last(0))
	require.Greater(t, mixed.df.Close.Last(0), mixed.df.Metadata["llm_sma"].Last(0))
	require.Empty(t, mixed.provider.inputs, "not asked")
}

func TestTrader_FreeSetup(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	r.trending(60, 0.2)
	r.step(r.df.Close.Last(0), decide(ActionWait, 0.4, nil, nil, nil))
	in := r.provider.last()
	require.Equal(t, SetupFree, in.Rules.Setup)
	require.Equal(t, flatActions, in.Rules.Actions, "both entries, against the trend too")
	require.True(t, math.IsNaN(float64(in.Rules.MinRiskReward)), "no minimum")
}

func TestTrader_MinRiskRewardEnforced(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.MinRiskReward = 3 }, 0)
	r.step(100, enterLong(100)) // 4 for 2: 2 R
	require.Zero(t, r.position(), "refused under 3 R")
	require.Empty(t, r.trader.trades)

	r.step(100, decide(ActionEnterLong, 0.8, ptr(100), ptr(99), ptr(103.5)))
	require.InDelta(t, 10, r.position(), 1e-9)
}

func TestTrader_MaxSpreadATR(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.MaxSpreadATR = 0.1 }, 0)
	r.meta = map[string]float64{"ask": 105, "spread": 5}
	r.step(100)
	require.Empty(t, r.provider.inputs, "the spread over 0.1 ATR: not asked")

	r.meta = map[string]float64{"ask": 100.01, "spread": 0.01}
	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, flatActions, r.provider.last().Rules.Actions)
}

func TestTrader_RMultiple(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	r.meta = map[string]float64{"ask": 100.5, "spread": 0.5}
	r.step(100, enterShort(100)) // its stop at 102: 2 of risk
	r.step(99, decide(ActionHold, 0.7, nil, nil, nil))
	in := r.provider.last()
	require.Equal(t, number(102), in.Position.InitialStop)
	require.InDelta(t, 0.25, float64(in.Position.RMultiple), 1e-9, "1 under the entry, closed at the ask 99.5")
}

func TestNew_InvalidSetup(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Setup = "scalp"
	_, err := New(cfg, &fakeProvider{t: t})
	require.ErrorContains(t, err, `invalid setup "scalp": free or trend_pullback`)

	cfg = DefaultConfig()
	cfg.MinRiskReward = -1
	_, err = New(cfg, &fakeProvider{t: t})
	require.ErrorContains(t, err, "can't be negative")
}

// A candle handed again (or an older one) is never asked twice: each request
// is billed.
func TestTrader_OneRequestPerCandle(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Len(t, r.provider.inputs, 1)

	r.trader.OnCandle(r.df, r.broker) // the same candle again
	require.Len(t, r.provider.inputs, 1, "not asked again")

	r.step(100.5, decide(ActionWait, 0.4, nil, nil, nil))
	require.Len(t, r.provider.inputs, 2, "the next candle is")
}

// Live, the candles of history (eg: handed after a restart, the terminal not
// synced yet) update the indicators but aren't asked: they would be traded at
// the market of now.
func TestTrader_LiveSkipsHistory(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.Live = true }, 0)
	next := r.nextClose() // the close of the next candle added
	r.trader.now = func() time.Time { return next.Add(5 * time.Hour) }
	r.step(100)
	r.step(100.5)
	require.Empty(t, r.provider.inputs, "history: not asked")
	require.Equal(t, 2, r.trader.history[testPair])

	r.trader.now = func() time.Time { return r.nextClose().Add(2 * time.Second) }
	r.step(101, decide(ActionWait, 0.4, nil, nil, nil))
	require.Len(t, r.provider.inputs, 1, "the live candle is")
	require.Zero(t, r.trader.history[testPair])
}

// The levels of the last decision are drawn on the chart: what the model saw.
func TestTrader_ChartLevels(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	var _ ui.LevelsSource = r.trader
	require.Empty(t, r.trader.ChartLevels(testPair))
	require.NotNil(t, r.trader.ChartLevels(testPair), "empty, not nil")

	wait := decide(ActionWait, 0.4, nil, nil, nil)
	wait.decision.Levels = []Level{{Kind: LevelSupport, Price: 98}, {Kind: LevelResistance, Price: 104}}
	r.step(100, wait)
	require.Equal(t, []ui.Level{{Kind: "support", Price: 98}, {Kind: "resistance", Price: 104}},
		r.trader.ChartLevels(testPair))

	r.step(100.5, decide(ActionWait, 0.4, nil, nil, nil))
	require.Empty(t, r.trader.ChartLevels(testPair), "the last decision saw none")
}

// Live, the first candle in progress after the start asks about the candle
// closed before it, once: the decision of the start, not a period later.
func TestTrader_FirstQueryAtStart(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.Live = true }, 0)
	var _ strategy.HighFrequencyStrategy = r.trader
	closedAt := r.df.Time[len(r.df.Time)-1]
	r.add(101) // the candle in progress, the last row
	r.trader.now = func() time.Time { return r.df.Time[len(r.df.Time)-1].Add(time.Minute) }
	r.trader.Indicators(r.df)

	r.provider.answers = []answer{decide(ActionWait, 0.4, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	r.startAsked(testPair)
	require.Len(t, r.provider.inputs, 1)
	require.Equal(t, closedAt, r.provider.last().Time, "the candle closed before the one in progress")
	decisions, err := r.trader.Decisions(testPair, 1)
	require.NoError(t, err)
	require.Empty(t, decisions, "applied on the next candle in progress")

	r.trader.OnPartialCandle(r.df, r.broker)
	require.Equal(t, TriggerStart, r.lastDecision().Trigger)
	require.Empty(t, r.trader.starting)
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Len(t, r.provider.inputs, 1, "once")

	// not in a backtest
	b := newTraderRunner(t, nil, 0)
	b.add(101)
	b.trader.Indicators(b.df)
	b.trader.OnPartialCandle(b.df, b.broker)
	require.Empty(t, b.provider.inputs)
}

// A strategy of pure chart analysis: only its indicators drawn and sent, the
// ATR of the rules computed anyway.
func TestTrader_IndicatorGroups(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.Indicators = []string{"atr", "volume"} }, 0)
	r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	in := r.provider.last()
	names := make([]string, 0, len(in.Indicators))
	for name := range in.Indicators {
		names = append(names, name)
	}
	require.ElementsMatch(t, []string{"atr_14", "volume_sma_20"}, names)
	drawn := r.trader.Indicators(r.df)
	require.Len(t, drawn, 2, "no average on the chart: the ATR and the volume, in panes")
	require.Equal(t, "ATR 14", drawn[0].GroupName)
	require.False(t, drawn[0].Overlay)
	require.Equal(t, "Volume SMA 20", drawn[1].GroupName)
	require.True(t, known(float64(in.Rules.MaxEntryDistance)), "the ATR of the rules")

	all := newTraderRunner(t, nil, 0)
	all.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Len(t, all.provider.last().Indicators, 15)
	drawnAll := all.trader.Indicators(all.df)
	require.Len(t, drawnAll[0].Metrics, 6, "the averages and the bands on the price")
	require.Len(t, drawnAll, 6, "then the ATR, the RSI, the ADX, the MACD and the volume")

	cfg := DefaultConfig()
	cfg.Indicators = []string{"heikinashi"}
	_, err := New(cfg, &fakeProvider{t: t})
	require.ErrorContains(t, err, `the indicator "heikinashi": unknown kind`)
}

// A position found at the start, opened before a restart, is followed with the
// entry and the legs of the stored orders: its levels may be moved again.
func TestTrader_RecoversStoredPosition(t *testing.T) {
	r := newTraderRunner(t, nil, 10) // 10 held at the start
	group := int64(7)
	stop := 97.0
	r.trader.SetOrderSource(func(pair string) ([]model.Order, error) {
		require.Equal(t, testPair, pair)
		return []model.Order{
			{ID: 1, Pair: pair, Side: model.SideTypeBuy, Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled,
				Price: 90, Quantity: 10}, // an older trade
			{ID: 4, Pair: pair, Side: model.SideTypeBuy, Type: model.OrderTypeMarket, Status: model.OrderStatusTypeFilled,
				Price: 99, Quantity: 10},
			{ID: 5, Pair: pair, Side: model.SideTypeSell, Type: model.OrderTypeLimitMaker, Status: model.OrderStatusTypeNew,
				Price: 104, Quantity: 10, GroupID: &group},
			{ID: 6, Pair: pair, Side: model.SideTypeSell, Type: model.OrderTypeStopLoss, Status: model.OrderStatusTypeNew,
				Price: 97, Stop: &stop, Quantity: 10, GroupID: &group},
			{ID: 3, Pair: pair, Side: model.SideTypeSell, Type: model.OrderTypeStopLoss,
				Status: model.OrderStatusTypeCanceled, Price: 88, Stop: &stop, GroupID: &group}, // of the older trade
		}, nil
	})

	r.step(100, decide(ActionHold, 0.7, nil, nil, nil))
	in := r.provider.last()
	require.Equal(t, number(99), in.Position.Entry)
	require.Equal(t, number(97), in.Position.StopLoss)
	require.Equal(t, number(104), in.Position.TakeProfit)
	require.Equal(t, number(97), in.Position.InitialStop)
	require.InDelta(t, 0.5, float64(in.Position.RMultiple), 1e-9, "1 over the entry, 2 risked")
	require.Equal(t, []Action{ActionHold, ActionExit, ActionModifySL, ActionModifyTP}, in.Rules.Actions,
		"its levels may be moved")

	// without stored orders: as before, the levels unknown
	b := newTraderRunner(t, nil, 10)
	b.step(100, decide(ActionHold, 0.7, nil, nil, nil))
	require.Equal(t, []Action{ActionHold, ActionExit}, b.provider.last().Rules.Actions)
}

// A position found held whose entry is newer than the open legs of the stored
// orders, eg: a long opened by another channel, without legs, while the legs
// of a long closed at the broker by its stop loss are NEW, its exit not in
// the history yet (see the MT5 adapter): those legs aren't taken for its stop
// loss and take profit. With the broker telling the positions (see
// fillPositions), the legs protecting its position are, never the ones of
// another, even placed after its entry.
func TestTrader_RecoverSkipsStaleLegs(t *testing.T) {
	r := newTraderRunner(t, nil, 0.1)
	at := r.df.Time[len(r.df.Time)-1]
	stored := []model.Order{
		filled(1, model.SideTypeBuy, 95, 0.1, at.Add(-2*time.Hour)),
		legOf(2, 1, model.SideTypeSell, model.OrderTypeStopLoss, model.OrderStatusTypeNew, 93, 0.1, at.Add(-2*time.Hour)),
		legOf(3, 1, model.SideTypeSell, model.OrderTypeLimitMaker, model.OrderStatusTypeNew, 104, 0.1,
			at.Add(-2*time.Hour)),
		filled(4, model.SideTypeBuy, 100, 0.1, at.Add(-time.Minute)), // the long held, without legs
	}
	r.trader.SetOrderSource(func(string) ([]model.Order, error) { return stored, nil })
	r.step(100, decide(ActionHold, 0.6, nil, nil, nil))
	in := r.provider.last()
	require.NotNil(t, in.Position)
	require.Equal(t, number(100), in.Position.Entry)
	require.True(t, math.IsNaN(float64(in.Position.StopLoss)), "not the stop loss of the long closed")
	require.True(t, math.IsNaN(float64(in.Position.TakeProfit)), "nor its take profit")
	require.Equal(t, []Action{ActionHold, ActionExit}, in.Rules.Actions, "no leg of its own to move")

	b := newTraderRunner(t, nil, 0.1)
	b.trader.SetAccountReporter(&linkedReporter{fakeReporter: &fakeReporter{profits: map[int64]float64{}},
		links: map[int64][2][]int64{1: {{11}, nil}, 4: {{44}, nil}, 5: {nil, {44}}, 6: {nil, {44}}, 7: {nil, {11}},
			8: {nil, {11}}}})
	stored = append(stored[:1:1], stored[3],
		legOf(5, 5, model.SideTypeSell, model.OrderTypeStopLoss, model.OrderStatusTypeNew, 97, 0.1, at), // of 44
		legOf(6, 5, model.SideTypeSell, model.OrderTypeLimitMaker, model.OrderStatusTypeNew, 103, 0.1, at),
		// of 11, moved later, before it closed
		legOf(7, 7, model.SideTypeSell, model.OrderTypeStopLoss, model.OrderStatusTypeNew, 94, 0.1, at),
		legOf(8, 7, model.SideTypeSell, model.OrderTypeLimitMaker, model.OrderStatusTypeNew, 105, 0.1, at))
	slices.Reverse(stored) // the order of the storage doesn't matter
	b.trader.SetOrderSource(func(string) ([]model.Order, error) { return stored, nil })
	b.step(100, decide(ActionHold, 0.6, nil, nil, nil))
	in = b.provider.last()
	require.Equal(t, number(100), in.Position.Entry)
	require.Equal(t, number(97), in.Position.StopLoss, "the legs of its position")
	require.Equal(t, number(103), in.Position.TakeProfit)
}
