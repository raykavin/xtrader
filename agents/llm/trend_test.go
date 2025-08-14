package llm

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/ui"
)

// stop returns the stop loss leg of the position followed, as the wallet holds
// it: its only leg, without a take profit.
func (r *traderRunner) stop() model.Order {
	r.t.Helper()
	tr, ok := r.trader.trades[testPair]
	require.True(r.t, ok, "no position followed")
	require.Len(r.t, tr.legs, 1, "the stop loss alone")
	order, err := r.wallet.Order(testPair, tr.legs[0].ExchangeID)
	require.NoError(r.t, err)
	require.Equal(r.t, model.OrderTypeStopLoss, order.Type)
	require.NotNil(r.t, order.Stop)
	return order
}

// Without a take profit, an entry goes with its stop loss alone, placed as with
// one; an entry with a take profit is refused.
func TestDecision_ValidateNoTakeProfit(t *testing.T) {
	in := testInput(nil, flatActions...)
	in.Rules.NoTakeProfit = true
	long := Decision{Action: ActionEnterLong, Confidence: 0.8, Entry: ptr(100), StopLoss: ptr(98), Reason: "ok"}
	short := Decision{Action: ActionEnterShort, Confidence: 0.8, Entry: ptr(100), StopLoss: ptr(102), Reason: "ok"}
	require.NoError(t, long.Validate(in))
	require.NoError(t, short.Validate(in))

	target := long
	target.TakeProfit = ptr(104)
	err := target.Validate(in)
	require.ErrorIs(t, err, ErrInvalidDecision)
	require.ErrorContains(t, err, "ENTER_LONG with the take profit 104: the positions go without one")
	target = short
	target.TakeProfit = ptr(96)
	require.ErrorContains(t, target.Validate(in), "ENTER_SHORT with the take profit 96")

	noStop := long
	noStop.StopLoss = nil
	require.ErrorContains(t, noStop.Validate(in), "ENTER_LONG needs an entry and a stop loss")

	// the stop loss on its side, beyond the noise and within the distances
	wrong := short
	wrong.StopLoss = ptr(99)
	require.ErrorContains(t, wrong.Validate(in), "must be above 100")
	far := long
	far.StopLoss = ptr(80)
	require.ErrorContains(t, far.Validate(in), "over the 10 allowed")
	in.Rules.MinStopDistance = 3
	require.ErrorContains(t, long.Validate(in), "inside the noise")
}

// A long followed without a take profit: its stop loss alone at the broker,
// trailed toward the price only, until it closes the position.
func TestTrader_NoTakeProfitLong(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.NoTakeProfit = true }, 0)
	r.step(100, decide(ActionEnterLong, 0.8, ptr(100), ptr(98), ptr(104)))
	in := r.provider.last()
	require.True(t, in.Rules.NoTakeProfit)
	require.False(t, known(float64(in.Rules.MinRiskReward)), "no risk:reward")
	require.Zero(t, r.position(), "an entry with a take profit: refused")
	require.Equal(t, OutcomeRefused, r.lastDecision().Outcome)

	r.step(100, decide(ActionEnterLong, 0.8, ptr(100), ptr(98), nil))
	require.InDelta(t, 10, r.position(), 1e-9)
	require.InDelta(t, 98, *r.stop().Stop, 1e-9)

	r.step(101, decide(ActionHold, 0.7, nil, nil, nil))
	in = r.provider.last()
	require.Equal(t, []Action{ActionHold, ActionExit, ActionModifySL}, in.Rules.Actions, "no take profit to move")
	require.Equal(t, number(98), in.Position.StopLoss)
	require.False(t, known(float64(in.Position.TakeProfit)))

	// trailed toward the price, behind a new structure
	r.step(102, decide(ActionModifySL, 0.8, nil, ptr(99.5), nil))
	require.InDelta(t, 99.5, *r.stop().Stop, 1e-9)
	require.Equal(t, OutcomeApplied, r.lastDecision().Outcome)
	r.step(102, decide(ActionHold, 0.7, nil, nil, nil))
	require.Equal(t, number(99.5), r.provider.last().Position.StopLoss, "the next context holds it")
	require.Equal(t, number(98), r.provider.last().Position.InitialStop)

	// never back, never beyond the price, and no take profit
	for _, refused := range []answer{
		decide(ActionModifySL, 0.8, nil, ptr(99), nil),
		decide(ActionModifySL, 0.8, nil, ptr(102.5), nil),
		decide(ActionModifyTP, 0.8, nil, nil, ptr(110)),
	} {
		r.step(102, refused)
		require.Equal(t, OutcomeRefused, r.lastDecision().Outcome, refused.decision.Action)
		require.InDelta(t, 99.5, *r.stop().Stop, 1e-9)
	}

	// the stop loss hit closes the position: flat, the next entry may come
	r.step(99, decide(ActionWait, 0.4, nil, nil, nil))
	require.Zero(t, r.position())
	require.Nil(t, r.provider.last().Position)
	require.NotContains(t, r.trader.trades, testPair)
}

// A position opened with a take profit, eg: by the prompt before, followed
// without any: its take profit shown, never moved.
func TestTrader_NoTakeProfitOfAPositionWithOne(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	prompts := &testPrompts{prompt: Prompt{Ref: ui.DecisionPrompt{ID: 1, Name: "targets", Version: 1},
		Content: "# targets\n\nEnter with a target."}}
	require.NoError(t, r.trader.SetPromptSource(prompts))
	r.step(100, enterLong(100))
	prompts.prompt = Prompt{Ref: ui.DecisionPrompt{ID: 2, Name: "trailing", Version: 1},
		Content: "# trailing\n# Take profit: none\n\nTrail the stop."}
	r.step(101, decide(ActionModifyTP, 0.8, nil, nil, ptr(110)))
	in := r.provider.last()
	require.Equal(t, number(104), in.Position.TakeProfit)
	require.Equal(t, []Action{ActionHold, ActionExit, ActionModifySL}, in.Rules.Actions)
	require.Equal(t, OutcomeRefused, r.lastDecision().Outcome)
	profit, _ := r.legs()
	require.InDelta(t, 104, profit.Price, 1e-9)
}

// A short followed without a take profit, the mirror of a long.
func TestTrader_NoTakeProfitShort(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.NoTakeProfit = true }, 0)
	r.step(100, decide(ActionEnterShort, 0.8, ptr(100), ptr(102), nil))
	require.InDelta(t, -10, r.position(), 1e-9)
	require.InDelta(t, 102, *r.stop().Stop, 1e-9)

	r.step(99, decide(ActionModifySL, 0.8, nil, ptr(100.5), nil))
	require.InDelta(t, 100.5, *r.stop().Stop, 1e-9, "trailed down, toward the price")
	for _, refused := range []answer{
		decide(ActionModifySL, 0.8, nil, ptr(101), nil),  // back up: more risk
		decide(ActionModifySL, 0.8, nil, ptr(98.5), nil), // below the price
		decide(ActionEnterShort, 0.8, ptr(99), ptr(101), nil),
	} {
		r.step(99, refused)
		require.Equal(t, OutcomeRefused, r.lastDecision().Outcome, refused.decision.Action)
		require.InDelta(t, 100.5, *r.stop().Stop, 1e-9)
	}
	require.InDelta(t, -10, r.position(), 1e-9)

	r.step(101, decide(ActionWait, 0.4, nil, nil, nil))
	require.Zero(t, r.position(), "the stop loss hit")
}

// The trend of the higher timeframes required: only the entry with it, and
// the model isn't asked while flat without one; a position held is managed
// whatever it.
func TestTrader_HigherTrendRequired(t *testing.T) {
	run := func(t *testing.T, feeder *fakeFeeder, holding float64) *traderRunner {
		r := newTraderRunner(t, func(c *Config) { c.HigherTrendRequired = true }, holding)
		closeAt := r.nextClose()
		r.trader.now = func() time.Time { return closeAt.Add(time.Second) }
		if feeder != nil {
			r.trader.SetFeeder(feeder)
		}
		return r
	}
	trending := func(r *traderRunner, step1h, step4h float64) *fakeFeeder {
		closeAt := r.nextClose()
		return &fakeFeeder{reads: map[string]int{}, candles: map[string][]model.Candle{
			testPair + " 1h": rising(200, time.Hour, closeAt.Truncate(time.Hour).Add(-time.Hour), step1h),
			testPair + " 4h": rising(200, 4*time.Hour, closeAt.Truncate(4*time.Hour).Add(-4*time.Hour), step4h),
		}}
	}

	up := run(t, nil, 0)
	up.trader.SetFeeder(trending(up, 0.5, 2))
	up.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, "up", up.provider.last().Rules.HigherTrend)
	require.Equal(t, []Action{ActionWait, ActionEnterLong}, up.provider.last().Rules.Actions)

	down := run(t, nil, 0)
	down.trader.SetFeeder(trending(down, -0.5, -2))
	down.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, []Action{ActionWait, ActionEnterShort}, down.provider.last().Rules.Actions)

	// they don't agree, or their candles are missing: no entry, nothing to ask
	mixed := run(t, nil, 0)
	mixed.trader.SetFeeder(trending(mixed, 0.5, -2))
	mixed.step(100)
	require.Empty(t, mixed.provider.inputs)
	missing := run(t, &fakeFeeder{reads: map[string]int{}, fail: true}, 0)
	missing.step(100)
	require.Empty(t, missing.provider.inputs)
	none := run(t, nil, 0) // no feeder
	none.step(100)
	require.Empty(t, none.provider.inputs)

	// a position held is managed whatever the trend
	held := run(t, nil, 10)
	held.trader.SetFeeder(trending(held, 0.5, -2))
	held.step(100, decide(ActionHold, 0.7, nil, nil, nil))
	require.NotNil(t, held.provider.last().Position)
	require.Empty(t, held.provider.last().Rules.HigherTrend)
}

// The strategy of trend following makes a Trader as its header asks: without
// a take profit, the stop loss beyond the noise, the trend of a timeframe
// above required.
func TestTrendFollowingPrompt(t *testing.T) {
	strategy := seededPrompt(t, "trend_following")
	require.True(t, StrategyWithoutTakeProfit(strategy))
	require.False(t, StrategyTakeProfitOptional(strategy))
	groups, ok := StrategyIndicators(strategy)
	require.True(t, ok)
	require.Equal(t, []string{"atr", "volume"}, groups)

	cfg := DefaultConfig()
	cfg.Timeframe, cfg.HigherTimeframes = "30m", []string{"4h"}
	cfg.Indicators, cfg.NoTakeProfit = groups, true
	cfg.HigherTrendRequired, cfg.MinStopATR, cfg.MaxSpreadATR = true, 1, 0.2
	_, err := New(cfg, &fakeProvider{t: t})
	require.NoError(t, err)

	sent := systemPrompt(strategy)
	require.Contains(t, sent, "When rules.no_take_profit is true, take_profit is always null")
	require.Contains(t, sent, "take_profit is always null")
	require.Contains(t, sent, "never back and never beyond the price")
	require.Contains(t, sent, "The time since the last trade is no reason to enter")
}

func TestStrategyWithoutTakeProfit(t *testing.T) {
	require.True(t, StrategyWithoutTakeProfit("# x: y\n# Take profit: None\n\nSetup: z."))
	require.False(t, StrategyWithoutTakeProfit("# x: y\n# Take profit: optional\n\nSetup: z."))
	require.False(t, StrategyWithoutTakeProfit("# x: y\n\nSetup: z.\n# Take profit: none"), "the header only")
	require.False(t, StrategyWithoutTakeProfit(defaultStrategy))
}
