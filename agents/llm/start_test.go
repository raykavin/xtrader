package llm

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// gateProvider holds each request until released, the contexts asked handed
// to asked as they come: the requests in flight together are seen at once.
type gateProvider struct {
	asked   chan Input
	release chan struct{}
}

func (g *gateProvider) Analyze(ctx context.Context, in Input) (Decision, error) {
	g.asked <- in
	select {
	case <-g.release:
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	}
	return Decision{Action: ActionWait, Confidence: 0.4, Reason: "test"}, nil
}

// startRunner is a runner of a Trader trading live candles, a candle in
// progress handed for the first time: the start of the agent.
func startRunner(t *testing.T, configure func(*Config)) *traderRunner {
	t.Helper()
	r := newTraderRunner(t, func(c *Config) {
		c.Live = true
		if configure != nil {
			configure(c)
		}
	}, 0)
	r.add(101) // the candle in progress, the last row
	r.trader.now = func() time.Time { return r.df.Time[len(r.df.Time)-1].Add(time.Minute) }
	r.trader.Indicators(r.df)
	return r
}

// copyTo returns the candles of the runner as the ones of pair, their
// indicators computed.
func (r *traderRunner) copyTo(pair string) *model.Dataframe {
	df := &model.Dataframe{Pair: pair, Time: slices.Clone(r.df.Time), Open: slices.Clone(r.df.Open),
		Close: slices.Clone(r.df.Close), High: slices.Clone(r.df.High), Low: slices.Clone(r.df.Low),
		Volume: slices.Clone(r.df.Volume), Metadata: map[string]model.Series[float64]{}}
	r.trader.Indicators(df)
	return df
}

// At the start, the pairs are asked at once: the decision of each of them
// kept, applied on its next candle.
func TestTrader_StartAsksThePairsAtOnce(t *testing.T) {
	r := startRunner(t, func(c *Config) { c.PositionSize = map[string]float64{testPair: 10, "ETHUSDT": 10} })
	other := r.copyTo("ETHUSDT")
	gate := &gateProvider{asked: make(chan Input, 2), release: make(chan struct{})}
	r.trader.provider = gate

	r.trader.OnPartialCandle(r.df, r.broker)
	r.trader.OnPartialCandle(other, r.broker)
	asked := map[string]Input{}
	for range 2 {
		select {
		case in := <-gate.asked:
			asked[in.Symbol] = in
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d asked before an answer: not at once", len(asked))
		}
	}
	require.Contains(t, asked, testPair)
	require.Contains(t, asked, "ETHUSDT")
	for pair, in := range asked {
		require.Contains(t, in.Rules.Actions, ActionEnterLong, pair)
	}

	r.trader.OnPartialCandle(r.df, r.broker) // still asked: nothing applied, nothing else asked
	close(gate.release)
	for _, df := range []*model.Dataframe{r.df, other} {
		r.startAsked(df.Pair)
		r.trader.OnPartialCandle(df, r.broker)
		decisions, err := r.trader.Decisions(df.Pair, 1)
		require.NoError(t, err)
		require.Len(t, decisions, 1, df.Pair)
		require.Equal(t, TriggerStart, decisions[0].Trigger, df.Pair)
		require.Equal(t, string(ActionWait), decisions[0].Action, df.Pair)
	}
	require.Empty(t, r.trader.starting)
	require.Empty(t, gate.asked, "once each")
}

// The start follows the rules of any analysis: without any entry to open, eg:
// the trend of the higher timeframes required but mixed, or a pair without a
// position size, the model isn't asked, at the start as at the close.
func TestTrader_StartWithoutAnyEntry(t *testing.T) {
	r := startRunner(t, func(c *Config) { c.HigherTrendRequired = true })
	closeAt := r.df.Time[len(r.df.Time)-1] // the close of the candle before the one in progress
	r.trader.SetFeeder(&fakeFeeder{reads: map[string]int{}, candles: map[string][]model.Candle{
		testPair + " 1h": rising(200, time.Hour, closeAt.Truncate(time.Hour).Add(-time.Hour), 0.5),
		testPair + " 4h": rising(200, 4*time.Hour, closeAt.Truncate(4*time.Hour).Add(-4*time.Hour), -2),
	}})

	unsized := r.copyTo("ETHUSDT")

	for _, tc := range []struct {
		df   *model.Dataframe
		code string
	}{{r.df, SkipHigherTrend}, {unsized, SkipSize}} {
		r.trader.OnPartialCandle(tc.df, r.broker)
		require.NotContains(t, r.trader.starting, tc.df.Pair, "not asked")
		decisions, err := r.trader.Decisions(tc.df.Pair, 5)
		require.NoError(t, err)
		require.Len(t, decisions, 1, tc.df.Pair)
		require.Equal(t, OutcomeSkipped, decisions[0].Outcome, "kept, not asked")
		require.Equal(t, tc.code, decisions[0].Reason)
		require.Equal(t, TriggerStart, decisions[0].Trigger)
		r.trader.OnPartialCandle(tc.df, r.broker) // the start handled: not tried again
	}
	r.trader.now = func() time.Time { return closeAt.Add(15*time.Minute + time.Second) }
	r.trader.OnCandle(r.df, r.broker) // the candle in progress closed
	require.Empty(t, r.provider.inputs, "never asked without an entry")
	require.Equal(t, TriggerCandle, r.lastDecision().Trigger)
	require.Equal(t, OutcomeSkipped, r.lastDecision().Outcome)
}

// The candle in progress closing before the decision of the start is applied:
// the decision awaited and applied first, then the close asked.
func TestTrader_StartAppliedBeforeTheClose(t *testing.T) {
	r := startRunner(t, nil)
	opened := r.df.Time[len(r.df.Time)-1]
	r.provider.answers = []answer{decide(ActionWait, 0.4, nil, nil, nil), decide(ActionWait, 0.45, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)

	r.trader.now = func() time.Time { return opened.Add(15*time.Minute + time.Second) }
	r.trader.OnCandle(r.df, r.broker)
	require.Empty(t, r.provider.answers, "the start and the close asked")
	require.Empty(t, r.trader.starting)
	decisions, err := r.trader.Decisions(testPair, 2)
	require.NoError(t, err)
	require.Len(t, decisions, 2)
	require.Equal(t, TriggerCandle, decisions[0].Trigger, "the last one")
	require.Equal(t, TriggerStart, decisions[1].Trigger)
}
