package llm

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The consult interval: 10s at least; 0 (at each close) in a backtest only,
// required live.
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

	cfg := DefaultConfig()
	cfg.Live = true
	require.ErrorContains(t, cfg.validate(), "the consult interval (llm-consult-interval, eg: 5m) is required live")
	cfg.ConsultInterval = 5 * time.Minute
	require.NoError(t, cfg.validate())
}

// Live, the model is never asked at the close of a candle, on the schedule or
// not: only on the ticks of the candles in progress. In a backtest, without
// them, it is asked at the closes.
func TestTrader_NeverAskedAtTheClose(t *testing.T) {
	r, _ := liveRunner(t, func(c *Config) { c.ConsultInterval = 5 * time.Minute }, 0)
	asked := len(r.provider.inputs)
	for _, close := range []float64{100.2, 100.4, 100.1} {
		r.add(close)
		r.trader.Indicators(r.df)
		r.trader.OnCandle(r.df, r.broker)
	}
	require.Len(t, r.provider.inputs, asked, "no close asked")

	backtest := newTraderRunner(t, nil, 0)
	backtest.step(100.2, decide(ActionWait, 0.4, nil, nil, nil))
	require.Equal(t, TriggerCandle, backtest.lastDecision().Trigger, "a backtest asked at the close")
}

// Longer than the timeframe, the model is asked on the slots only, each on
// the first tick of the candle opening at it, the closes between them left
// out, and never on the ticks within a candle without a slot.
func TestTrader_ConsultIntervalAboveTheTimeframe(t *testing.T) {
	r, _ := liveRunner(t, nil, 0)
	r.trader.cfg.ConsultInterval = 30 * time.Minute
	asked := func() int { return len(r.provider.inputs) }
	start := asked()

	// the candles of 15m: every other one opens at a slot
	for i := 0; i < 4; i++ {
		closes := r.df.Time[len(r.df.Time)-1].Add(30 * time.Minute) // of the candle added next
		onSchedule := closes.Truncate(30 * time.Minute).Equal(closes)
		before := asked()
		answers := []answer{}
		if onSchedule {
			answers = append(answers, decide(ActionWait, 0.4, nil, nil, nil))
		}
		r.step(100, answers...) // the close, then the first tick of the next candle
		if onSchedule {
			require.Equal(t, before+1, asked(), "the slot of %s, on the tick after the close", closes)
			require.Equal(t, TriggerInterval, r.lastDecision().Trigger)
			require.True(t, r.provider.last().Scheduled)
		} else {
			require.Equal(t, before, asked(), "the close of %s off the schedule", closes)
		}
	}
	require.Equal(t, start+2, asked())

	r.inProgress(100.4)
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Equal(t, start+2, asked(), "no slot within the candle in progress")
}

// Shorter than the timeframe, the model is asked on the candle in progress
// once per slot, a consultation the trader didn't ask; the slot of the close
// on the first tick of the next candle.
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

	// the close, not asked; its slot on the first tick of the next candle,
	// the candle closed with the others
	r.df.Time, r.df.Open, r.df.Close = r.df.Time[:len(r.df.Time)-1], r.df.Open[:len(r.df.Open)-1],
		r.df.Close[:len(r.df.Close)-1]
	r.df.High, r.df.Low, r.df.Volume = r.df.High[:len(r.df.High)-1], r.df.Low[:len(r.df.Low)-1],
		r.df.Volume[:len(r.df.Volume)-1]
	r.step(101, decide(ActionWait, 0.4, nil, nil, nil))
	require.Len(t, r.provider.inputs, start+3)
	require.Equal(t, TriggerInterval, r.lastDecision().Trigger)
	in = r.provider.last()
	require.True(t, in.Scheduled)
	require.Equal(t, opened.Unix(), in.Candles[len(in.Candles)-1].Time.Unix(), "the candle closed, the last one")
}

// The close late, its candle still handed in progress past its close: its
// slot isn't asked on it, complete but not confirmed, nor at the close, but on
// the first tick of the next candle, once.
func TestTrader_ConsultIntervalSlotOfTheCloseOnTheNextCandle(t *testing.T) {
	r, _ := liveRunner(t, nil, 0)
	r.trader.cfg.ConsultInterval = 5 * time.Minute
	r.inProgress(101.2)
	opened := r.df.Time[len(r.df.Time)-1]
	asked := len(r.provider.inputs)

	r.trader.now = func() time.Time { return opened.Add(15*time.Minute + 12*time.Second) } // past its close
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Len(t, r.provider.inputs, asked, "the slot of the close: not asked on the candle in progress")

	r.trader.now = func() time.Time { return opened.Add(14*time.Minute + 59*time.Second) }
	r.provider.answers = []answer{decide(ActionWait, 0.5, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Len(t, r.provider.inputs, asked+1, "the slot of 10 minutes, within the candle")
	require.Equal(t, TriggerInterval, r.lastDecision().Trigger)

	r.trader.now = func() time.Time { return opened.Add(15*time.Minute + 20*time.Second) }
	r.trader.OnCandle(r.df, r.broker) // closed, late
	require.Len(t, r.provider.inputs, asked+1, "not asked at the close")

	r.provider.answers = []answer{decide(ActionWait, 0.5, nil, nil, nil)}
	r.slotTick()
	require.Len(t, r.provider.inputs, asked+2, "the slot of the close, on the next candle")
	r.slotTick()
	require.Len(t, r.provider.inputs, asked+2, "once")
}

// The decision of the start takes its slot: the model isn't asked again in it.
func TestTrader_ConsultIntervalAfterTheStart(t *testing.T) {
	r, _ := liveRunner(t, nil, 0)
	r.trader.cfg.ConsultInterval = 5 * time.Minute
	r.inProgress(101.2) // its clock 7 minutes after its open
	opened := r.df.Time[len(r.df.Time)-1]
	delete(r.trader.handled, testPair) // a start
	asked := len(r.provider.inputs)

	r.provider.answers = []answer{decide(ActionWait, 0.5, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	r.startAsked(testPair)
	require.Len(t, r.provider.inputs, asked+1, "the start")
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Equal(t, TriggerStart, r.lastDecision().Trigger, "applied")
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Len(t, r.provider.inputs, asked+1, "its slot taken by the start")

	r.trader.now = func() time.Time { return opened.Add(10*time.Minute + time.Second) }
	r.provider.answers = []answer{decide(ActionWait, 0.5, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Len(t, r.provider.inputs, asked+2, "the next slot")
	require.Equal(t, TriggerInterval, r.lastDecision().Trigger)
}

// The decision of the start answered in the next slot: that slot taken too,
// the model not asked again on the tick that applies it.
func TestTrader_ConsultIntervalStartAnsweredInTheNextSlot(t *testing.T) {
	r, _ := liveRunner(t, nil, 0)
	r.trader.cfg.ConsultInterval = 5 * time.Minute
	r.inProgress(101.2) // its clock 7 minutes after its open
	opened := r.df.Time[len(r.df.Time)-1]
	delete(r.trader.handled, testPair) // a start
	asked := len(r.provider.inputs)

	r.provider.answers = []answer{decide(ActionWait, 0.5, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	r.startAsked(testPair)
	r.trader.now = func() time.Time { return opened.Add(10*time.Minute + time.Second) }
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Equal(t, TriggerStart, r.lastDecision().Trigger)
	require.Len(t, r.provider.inputs, asked+1, "the slot it was answered in taken")
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Len(t, r.provider.inputs, asked+1)
}
