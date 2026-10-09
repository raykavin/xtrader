package llm

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// M7: the analyses of a pair skipped in a row for the same reason, on the
// consult interval and at the closes, are kept as one record, the first one,
// with a note of how many they are and of the last one, rather than one every
// few seconds; another reason, a decision of the model or a reanalysis start
// another record.
func TestTrader_RepeatedSkipsKeptAsOne(t *testing.T) {
	r, _ := liveRunner(t, nil, 0) // the close before: WAIT 0.4
	var told []ui.Decision
	r.trader.ListenDecisions(func(d ui.Decision) { told = append(told, d) })
	r.trader.cfg.ConsultInterval = 10 * time.Second
	r.trader.cfg.PositionSize = map[string]float64{testPair: 1e9} // no position fits the balance
	r.inProgress(100.4)
	opened := r.df.Time[len(r.df.Time)-1]
	at := func(i int) time.Time {
		return opened.Add(7*time.Minute + time.Duration(i)*10*time.Second + time.Second)
	}
	for i := range 30 {
		r.trader.now = func() time.Time { return at(i) }
		r.trader.OnPartialCandle(r.df, r.broker)
	}
	require.Len(t, r.provider.inputs, 1, "asked at the close before only")
	decisions, err := r.trader.Decisions(testPair, 100)
	require.NoError(t, err)
	require.Len(t, decisions, 2, "the close before, then one record of the skips")
	run := decisions[0]
	require.Equal(t, OutcomeSkipped, run.Outcome)
	require.Equal(t, SkipSize, run.Reason)
	require.Equal(t, TriggerInterval, run.Trigger)
	require.Equal(t, at(0).Unix(), run.DecidedAt, "the first one")
	require.Equal(t, []ui.DecisionNote{{Kind: "text", Text: "skipped 30 times in a row for the same reason " +
		"(interval 30), the last at " + at(29).UTC().Format("2006-01-02 15:04:05") + " UTC, at the price of 100.4"}},
		run.Notes)
	require.Len(t, told, 30, "each one told to the dashboard, as the record of the run")
	for _, d := range told {
		require.Equal(t, run.ID, d.ID)
	}

	// the close, not asked, and the slot after it, skipped for the same
	// reason: the same record
	last := len(r.df.Close) - 1
	r.df.Close[last] = 100.6
	r.trader.now = func() time.Time { return opened.Add(15*time.Minute + time.Second) }
	r.trader.Indicators(r.df)
	r.trader.OnCandle(r.df, r.broker)
	r.slotTick()
	decisions, err = r.trader.Decisions(testPair, 100)
	require.NoError(t, err)
	require.Len(t, decisions, 2)
	require.Equal(t, run.ID, decisions[0].ID)
	require.Equal(t, "skipped 31 times in a row for the same reason (interval 31), the last at "+
		opened.Add(15*time.Minute+time.Second).UTC().Format("2006-01-02 15:04:05")+" UTC, at the price of 100.6",
		decisions[0].Notes[0].Text)

	// another reason: another record, the run kept as it is
	r.trader.cfg.PositionSize = map[string]float64{testPair: 10}
	r.trader.cfg.HigherTrendRequired = true // no feeder: no higher trend
	r.inProgress(100.8)
	r.trader.OnPartialCandle(r.df, r.broker)
	decisions, err = r.trader.Decisions(testPair, 100)
	require.NoError(t, err)
	require.Len(t, decisions, 3)
	require.Equal(t, SkipHigherTrend, decisions[0].Reason)
	require.Empty(t, decisions[0].Notes, "the first of its run")
	require.Equal(t, decisions[1].Notes[0].Text, told[len(told)-2].Notes[0].Text, "the run before as it was")

	// a decision of the model ends the run: the same reason then starts
	// another record
	r.trader.cfg.HigherTrendRequired = false
	opened = r.df.Time[len(r.df.Time)-1]
	r.trader.now = func() time.Time { return opened.Add(9 * time.Minute) }
	r.provider.answers = []answer{decide(ActionWait, 0.5, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Empty(t, r.provider.answers, "asked")
	r.trader.cfg.HigherTrendRequired = true
	for _, minute := range []int{10, 11} {
		r.trader.now = func() time.Time { return opened.Add(time.Duration(minute) * time.Minute) }
		r.trader.OnPartialCandle(r.df, r.broker)
	}
	decisions, err = r.trader.Decisions(testPair, 100)
	require.NoError(t, err)
	require.Len(t, decisions, 5)
	require.Equal(t, SkipHigherTrend, decisions[0].Reason)
	require.Len(t, decisions[0].Notes, 1)
	require.Contains(t, decisions[0].Notes[0].Text, "skipped 2 times in a row")
	require.Equal(t, string(ActionWait), decisions[1].Action)
	require.Equal(t, SkipHigherTrend, decisions[2].Reason)
	require.Empty(t, decisions[2].Notes)

	// a reanalysis is kept apart, and starts another record
	_, err = r.trader.Reanalyze(testPair, ui.ChannelWeb, "")
	require.NoError(t, err)
	r.trader.OnPartialCandle(r.df, r.broker)
	r.trader.now = func() time.Time { return opened.Add(12 * time.Minute) }
	r.trader.OnPartialCandle(r.df, r.broker)
	decisions, err = r.trader.Decisions(testPair, 100)
	require.NoError(t, err)
	require.Len(t, decisions, 7)
	require.Equal(t, TriggerInterval, decisions[0].Trigger)
	require.Empty(t, decisions[0].Notes, "the first of its run")
	require.Equal(t, TriggerReanalysis, decisions[1].Trigger)
	require.Equal(t, string(ui.ChannelWeb), decisions[1].RequestedBy)
}

// M7: a Trader trading live without a store of its decisions, eg: on a paper
// wallet, holds the last ones in memory, never all of them as it runs for
// months; a backtest holds them all.
func TestTrader_DecisionsHeldInMemory(t *testing.T) {
	save := func(trader *Trader, count int) {
		start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		for i := range count {
			at := start.Add(time.Duration(i) * time.Minute)
			require.NoError(t, trader.store().Save(ui.Decision{ID: fmt.Sprintf("%06d-%s", i, testPair),
				Pair: testPair, DecidedAt: at.Unix(), Outcome: OutcomeApplied}, nil))
		}
	}

	live := newTraderRunner(t, func(c *Config) { c.Live = true }, 0).trader
	save(live, liveDecisionsHeld+10)
	held, err := live.Decisions("", liveDecisionsHeld+10)
	require.NoError(t, err)
	require.Len(t, held, liveDecisionsHeld)
	require.Equal(t, fmt.Sprintf("%06d-%s", liveDecisionsHeld+9, testPair), held[0].ID, "the last ones")
	_, ok, err := live.Decision(fmt.Sprintf("%06d-%s", 9, testPair))
	require.NoError(t, err)
	require.False(t, ok, "the oldest dropped")

	backtest := newTraderRunner(t, nil, 0).trader
	save(backtest, liveDecisionsHeld+10)
	all, err := backtest.Decisions("", liveDecisionsHeld+10)
	require.NoError(t, err)
	require.Len(t, all, liveDecisionsHeld+10)
}
