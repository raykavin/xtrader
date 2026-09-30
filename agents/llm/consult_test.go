package llm

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The consult interval: 10s at least, or none.
func TestConfig_ConsultInterval(t *testing.T) {
	for interval, ok := range map[time.Duration]bool{0: true, 10 * time.Second: true, 2 * time.Hour: true,
		9 * time.Second: false, -time.Minute: false} {
		cfg := DefaultConfig()
		cfg.ConsultInterval = interval
		if ok {
			require.NoError(t, cfg.validate(), interval)
		} else {
			require.ErrorContains(t, cfg.validate(), "consult interval", interval)
		}
	}
}

// Longer than the timeframe, the model is asked at the closes on the schedule
// only, the ones between them left out, and never on the candles in progress.
func TestTrader_ConsultIntervalAboveTheTimeframe(t *testing.T) {
	r, _ := liveRunner(t, nil, 0)
	r.trader.cfg.ConsultInterval = 30 * time.Minute // after the close of the start
	asked := func() int { return len(r.provider.inputs) }
	start := asked()

	// the candles of 15m: every other close falls on the schedule
	for i := 0; i < 4; i++ {
		closes := r.df.Time[len(r.df.Time)-1].Add(30 * time.Minute) // of the candle added next
		onSchedule := closes.Truncate(30 * time.Minute).Equal(closes)
		before := asked()
		answers := []answer{}
		if onSchedule {
			answers = append(answers, decide(ActionWait, 0.4, nil, nil, nil))
		}
		r.step(100, answers...)
		if onSchedule {
			require.Equal(t, before+1, asked(), "the close of %s on the schedule", closes)
			require.Equal(t, TriggerCandle, r.lastDecision().Trigger)
		} else {
			require.Equal(t, before, asked(), "the close of %s left out", closes)
		}
	}
	require.Equal(t, start+2, asked())

	r.inProgress(100.4)
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Equal(t, start+2, asked(), "no slot within the candle in progress")
}

// Shorter than the timeframe, the model is asked on the candle in progress
// once per slot, a consultation the trader didn't ask, and at the close.
func TestTrader_ConsultIntervalBelowTheTimeframe(t *testing.T) {
	r, messages := liveRunner(t, nil, 0)
	r.trader.cfg.ConsultInterval = 5 * time.Minute
	start := len(r.provider.inputs)

	r.inProgress(101.2) // its clock 7 minutes after its open: the slot of 5m
	opened := r.df.Time[len(r.df.Time)-1]
	r.provider.answers = []answer{decide(ActionWait, 0.5, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Empty(t, r.provider.answers, "asked in the slot")
	in := r.provider.last()
	require.True(t, in.Scheduled)
	require.False(t, in.Reanalysis, "not asked by the trader")
	require.NotNil(t, in.CandleInProgress)
	require.Equal(t, number(101.2), in.Price)
	d := r.lastDecision()
	require.Equal(t, TriggerInterval, d.Trigger)
	require.Empty(t, d.RequestedBy)
	require.Empty(t, *messages, "nothing to tell: nobody asked")

	r.trader.now = func() time.Time { return opened.Add(9 * time.Minute) }
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Len(t, r.provider.inputs, start+1, "once per slot")

	r.trader.now = func() time.Time { return opened.Add(10*time.Minute + time.Second) }
	r.provider.answers = []answer{decide(ActionWait, 0.5, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Len(t, r.provider.inputs, start+2, "the next slot")

	// the close, on the schedule, asked as a close
	r.df.Time, r.df.Open, r.df.Close = r.df.Time[:len(r.df.Time)-1], r.df.Open[:len(r.df.Open)-1],
		r.df.Close[:len(r.df.Close)-1]
	r.df.High, r.df.Low, r.df.Volume = r.df.High[:len(r.df.High)-1], r.df.Low[:len(r.df.Low)-1],
		r.df.Volume[:len(r.df.Volume)-1]
	r.step(101, decide(ActionWait, 0.4, nil, nil, nil))
	require.Len(t, r.provider.inputs, start+3)
	require.Equal(t, TriggerCandle, r.lastDecision().Trigger)
	require.False(t, r.provider.last().Scheduled)
}
