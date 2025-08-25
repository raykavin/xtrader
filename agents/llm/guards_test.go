package llm

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/model"
)

func TestDecision_MinStop(t *testing.T) {
	enter := func(action Action, stop, profit float64) Decision {
		return Decision{Action: action, Confidence: 0.8, Entry: ptr(100), StopLoss: ptr(stop), TakeProfit: ptr(profit),
			Reason: "ok"}
	}
	in := testInput(nil, flatActions...)
	require.NoError(t, enter(ActionEnterLong, 99.9, 104).Validate(in), "no minimum")

	in.Rules.MinStopDistance = 2
	require.NoError(t, enter(ActionEnterLong, 98, 104).Validate(in), "2 below the bid")
	err := enter(ActionEnterLong, 98.5, 104).Validate(in)
	require.ErrorIs(t, err, ErrInvalidDecision)
	require.ErrorContains(t, err, "leaves 1.5 of room before it is hit (from 100), under the 2 required")

	// a short is stopped at the ask: the spread takes from its room
	in.Spread = 0.5
	require.ErrorContains(t, enter(ActionEnterShort, 102, 96).Validate(in), "leaves 1.5 of room")
	require.NoError(t, enter(ActionEnterShort, 102.5, 96).Validate(in))

	// the moves of a stop held aren't bound: the break-even is near
	held := testInput(longPosition(), heldActions...)
	held.Rules.MinStopDistance = 2
	require.NoError(t, Decision{Action: ActionModifySL, Confidence: 0.8, StopLoss: ptr(99.5), Reason: "ok"}.Validate(held))
}

func TestTrader_MinStop(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.MinStopATR = 1 }, 0)
	r.step(100, decide(ActionEnterLong, 0.8, ptr(100), ptr(99.99), ptr(110)))
	in := r.provider.last()
	atr := r.df.Metadata["llm_atr"].Last(0)
	require.InDelta(t, atr, float64(in.Rules.MinStopDistance), 1e-9, "1 ATR")
	require.Zero(t, r.position(), "a stop inside the noise: refused")
}

// rising returns n candles of period rising by step, the last opened at last.
func rising(n int, period time.Duration, last time.Time, step float64) []model.Candle {
	candles := make([]model.Candle, n)
	for i := range candles {
		c := 100 + step*float64(i)
		candles[i] = model.Candle{Time: last.Add(-time.Duration(n-1-i) * period), Open: c - step, Close: c,
			High: math.Max(c, c-step) + 1, Low: math.Min(c, c-step) - 1, Volume: 100}
	}
	return candles
}

func TestTrader_HigherTrendFilter(t *testing.T) {
	run := func(t *testing.T, step1h, step4h float64, configure func(*Config)) *traderRunner {
		r := newTraderRunner(t, func(c *Config) {
			c.HigherTrendFilter = true
			if configure != nil {
				configure(c)
			}
		}, 0)
		closeAt := r.nextClose()
		r.trader.now = func() time.Time { return closeAt.Add(time.Second) }
		r.trader.SetFeeder(&fakeFeeder{reads: map[string]int{}, candles: map[string][]model.Candle{
			testPair + " 1h": rising(200, time.Hour, closeAt.Truncate(time.Hour).Add(-time.Hour), step1h),
			testPair + " 4h": rising(200, 4*time.Hour, closeAt.Truncate(4*time.Hour).Add(-4*time.Hour), step4h),
		}})
		return r
	}

	up := run(t, 0.5, 2, nil)
	up.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	in := up.provider.last()
	require.Equal(t, "up", in.Rules.HigherTrend)
	require.Equal(t, []Action{ActionWait, ActionEnterLong}, in.Rules.Actions, "longs only")

	down := run(t, -0.5, -2, nil)
	down.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, []Action{ActionWait, ActionEnterShort}, down.provider.last().Rules.Actions)

	mixed := run(t, 0.5, -2, nil)
	mixed.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, flatActions, mixed.provider.last().Rules.Actions, "they don't agree: no filter")
	require.Empty(t, mixed.provider.last().Rules.HigherTrend)

	// shorts only, under timeframes trending up: nothing to ask
	shorts := run(t, 0.5, 2, func(c *Config) { c.Direction = strategies.RKDirectionSellOnly })
	shorts.step(100)
	require.Empty(t, shorts.provider.inputs)
}
