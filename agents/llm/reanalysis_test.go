package llm

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/ui"
)

// liveRunner is a runner of a Trader trading live candles, its clock a second
// after the close of the last candle, the decision of the start asked already.
func liveRunner(t *testing.T, configure func(*Config), holding float64) (*traderRunner, *[]string) {
	t.Helper()
	r := newTraderRunner(t, func(c *Config) {
		c.Live = true
		if configure != nil {
			configure(c)
		}
	}, holding)
	r.trader.now = func() time.Time { return r.df.Time[len(r.df.Time)-1].Add(15*time.Minute + time.Second) }
	// started: the first close of the runner asked on its slot, as the next ones
	r.trader.handled[testPair] = r.df.Time[len(r.df.Time)-1]
	messages := &[]string{}
	r.trader.SetNotifier(func(message string) { *messages = append(*messages, message) })
	answer := decide(ActionWait, 0.4, nil, nil, nil)
	if holding > 0 {
		answer = decide(ActionHold, 0.6, nil, nil, nil)
	}
	r.step(100, answer)
	return r, messages
}

// inProgress adds the candle in progress closing at close so far, the last row
// of the dataframe, as the controller hands it to OnPartialCandle, the clock in
// the middle of it.
func (r *traderRunner) inProgress(close float64) {
	df := r.df
	open := df.Close.Last(0)
	at := df.Time[len(df.Time)-1].Add(15 * time.Minute)
	candle := model.Candle{Pair: testPair, Time: at, Open: open, Close: close, High: math.Max(open, close) + 0.1,
		Low: math.Min(open, close) - 0.1, Volume: 50}
	r.wallet.OnCandle(candle)
	df.Time = append(df.Time, at)
	df.Open = append(df.Open, candle.Open)
	df.Close = append(df.Close, candle.Close)
	df.High = append(df.High, candle.High)
	df.Low = append(df.Low, candle.Low)
	df.Volume = append(df.Volume, candle.Volume)
	r.trader.now = func() time.Time { return at.Add(7 * time.Minute) }
	r.trader.Indicators(df)
}

// A reanalysis asks the model on the next tick of the candle in progress, at
// its price, the candles closed as the context: its decision kept and its
// result notified, once.
func TestTrader_Reanalysis(t *testing.T) {
	r, messages := liveRunner(t, nil, 0)
	var _ ui.Reanalyzer = r.trader
	closedAt := r.df.Time[len(r.df.Time)-1]

	response, err := r.trader.Reanalyze(testPair, ui.ChannelTelegram, "")
	require.NoError(t, err)
	require.Equal(t, ReanalysisQueued, response.Status)
	again, err := r.trader.Reanalyze(testPair, ui.ChannelWeb, "")
	require.NoError(t, err)
	require.Equal(t, ReanalysisQueued, again.Status)
	require.Contains(t, again.Message, "queued already", "asked once")

	r.inProgress(101.3)
	r.provider.answers = []answer{decide(ActionWait, 0.55, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Empty(t, r.provider.answers, "asked on the tick")
	in := r.provider.last()
	require.True(t, in.Reanalysis)
	require.Equal(t, number(101.3), in.Price, "the price of now")
	require.NotNil(t, in.CandleInProgress)
	require.Equal(t, number(101.3), in.CandleInProgress.Close)
	require.Equal(t, closedAt.Add(15*time.Minute), in.CandleInProgress.Time)
	require.Equal(t, closedAt, in.Time, "the candles closed as the context")

	d := r.lastDecision()
	require.Equal(t, TriggerReanalysis, d.Trigger)
	require.Equal(t, "telegram", d.RequestedBy)
	require.Equal(t, 101.3, d.Price)
	require.Equal(t, []string{"🔄 Reanalysis of BTCUSDT 15m, asked from telegram: WAIT 0.55, applied | test"},
		*messages)

	// once: the next ticks leave the candle in progress to its close
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Len(t, r.provider.inputs, 2, "the start, then the reanalysis")
}

// A reanalysis asked before a slot of the consult interval is asked on its
// tick and takes the slot: the model isn't asked twice.
func TestTrader_ReanalysisTakesTheSlot(t *testing.T) {
	r, messages := liveRunner(t, nil, 0)
	_, err := r.trader.Reanalyze(testPair, ui.ChannelWeb, "")
	require.NoError(t, err)

	r.step(100.2, decide(ActionWait, 0.5, nil, nil, nil)) // not at the close: on the tick of the slot after it
	d := r.lastDecision()
	require.Equal(t, TriggerReanalysis, d.Trigger)
	require.Equal(t, "web", d.RequestedBy)
	require.Len(t, *messages, 1)
	require.False(t, r.trader.reanalysisPending(testPair))

	r.inProgress(100.4) // in the same slot
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Len(t, r.provider.inputs, 2, "the slot not asked again")
}

// hookProvider calls during before it answers as its provider does.
type hookProvider struct {
	*fakeProvider
	during func()
}

func (h hookProvider) Analyze(ctx context.Context, in Input) (Decision, error) {
	h.during()
	return h.fakeProvider.Analyze(ctx, in)
}

// A reanalysis asked while the model is asked is answered by that analysis.
func TestTrader_ReanalysisWhileRunning(t *testing.T) {
	r, messages := liveRunner(t, nil, 0)
	var response ui.ReanalysisResponse
	r.trader.provider = hookProvider{fakeProvider: r.provider, during: func() {
		var err error
		response, err = r.trader.Reanalyze(testPair, ui.ChannelTelegram, "")
		require.NoError(t, err)
	}}

	r.step(100.2, decide(ActionWait, 0.5, nil, nil, nil))
	require.Equal(t, ReanalysisRunning, response.Status)
	require.Contains(t, response.Message, "in progress")
	require.Len(t, *messages, 1, "its result notified")
	require.False(t, r.trader.reanalysisPending(testPair), "nothing left to ask")
	require.Empty(t, r.lastDecision().RequestedBy, "asked before the request")
}

// A position held: a reanalysis doesn't count a candle held.
func TestTrader_ReanalysisOfAPosition(t *testing.T) {
	r, _ := liveRunner(t, nil, 10)
	held := r.trader.trades[testPair].bars
	_, err := r.trader.Reanalyze(testPair, ui.ChannelTelegram, "")
	require.NoError(t, err)
	r.inProgress(99.8)
	r.provider.answers = []answer{decide(ActionHold, 0.7, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Equal(t, held, r.trader.trades[testPair].bars)
	require.Equal(t, held, r.provider.last().Position.Candles)
	require.Equal(t, "LONG", r.lastDecision().Position)
}

// The requests the model isn't asked for are answered with why.
func TestTrader_ReanalysisRefused(t *testing.T) {
	backtest := newTraderRunner(t, nil, 0)
	_, err := backtest.trader.Reanalyze(testPair, ui.ChannelWeb, "")
	require.ErrorContains(t, err, "not in a backtest")

	r, messages := liveRunner(t, nil, 0)
	r.trader.pause(r.trader.now().Add(30 * time.Second))
	_, err = r.trader.Reanalyze(testPair, ui.ChannelTelegram, "")
	require.ErrorContains(t, err, "rate limited for 30s")
	r.trader.pause(time.Time{})

	// nothing to ask: no position fits the balance
	r.trader.cfg.PositionSize = map[string]float64{testPair: 1e9}
	_, err = r.trader.Reanalyze(testPair, ui.ChannelTelegram, "")
	require.NoError(t, err)
	r.inProgress(100.5)
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Len(t, r.provider.inputs, 1, "not asked")
	require.Equal(t, []string{"🔄 Reanalysis of BTCUSDT 15m, asked from telegram: the model wasn't asked: no " +
		"position fits the balance and the sizing"}, *messages)
	require.False(t, r.trader.reanalysisPending(testPair), "answered, not retried on every tick")
	d := r.lastDecision()
	require.Equal(t, OutcomeSkipped, d.Outcome, "kept, not asked")
	require.Equal(t, SkipSize, d.Reason)
	require.Equal(t, TriggerReanalysis, d.Trigger)
	require.Equal(t, string(ui.ChannelTelegram), d.RequestedBy)
}

// The journal keeps the trigger and the channel of a reanalysis, in the shape
// of the lines of the journal files, which JournalDecisions reads back.
func TestTrader_JournalKeepsReanalyses(t *testing.T) {
	journal := &testJournal{}
	r, _ := liveRunner(t, func(c *Config) {}, 0)
	require.NoError(t, r.trader.SetJournal(journal))
	_, err := r.trader.Reanalyze(testPair, ui.ChannelTelegram, "")
	require.NoError(t, err)
	r.inProgress(100.7)
	r.provider.answers = []answer{decide(ActionWait, 0.6, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	kept := r.lastDecision()

	require.Len(t, journal.entries, 1)
	entry := journal.entries[0]
	require.Equal(t, TriggerReanalysis, entry.Trigger)
	require.Equal(t, "telegram", entry.Requested)
	line, err := json.Marshal(entry)
	require.NoError(t, err)
	imported := JournalDecisions(line, nil)
	require.Len(t, imported, 1)
	require.Equal(t, kept.ID, imported[0].ID)
	require.Equal(t, TriggerReanalysis, imported[0].Trigger)
	require.Equal(t, "telegram", imported[0].RequestedBy)
	require.Equal(t, kept.Reason, imported[0].Reason)
}

// The note of a reanalysis goes to the model in the context, and is kept with
// the decision that answers it, in the store and in the journal.
func TestTrader_ReanalysisNote(t *testing.T) {
	journal := &testJournal{}
	r, messages := liveRunner(t, nil, 10)
	require.NoError(t, r.trader.SetJournal(journal))

	note := "Estamos no lucro, acredito que você pode encerrar a posição? O que acha?"
	response, err := r.trader.Reanalyze(testPair, ui.ChannelTelegram, "  "+note+"\n")
	require.NoError(t, err)
	require.Equal(t, ReanalysisQueued, response.Status)
	r.inProgress(101.2)
	r.provider.answers = []answer{decide(ActionHold, 0.7, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	in := r.provider.last()
	require.True(t, in.Reanalysis)
	require.Equal(t, note, in.ReanalysisNote, "trimmed")
	data, err := json.Marshal(in)
	require.NoError(t, err)
	require.Contains(t, string(data), `"reanalysis_note":"Estamos no lucro`)

	kept := r.lastDecision()
	require.Equal(t, note, kept.RequestNote)
	require.Equal(t, "telegram", kept.RequestedBy)
	require.Len(t, *messages, 1, "its result notified")

	require.Len(t, journal.entries, 1)
	require.Equal(t, note, journal.entries[0].Input.ReanalysisNote, "in the journal")
	require.Equal(t, "telegram", journal.entries[0].Requested)

	// the next analyses go without it
	r.step(101.3, decide(ActionHold, 0.7, nil, nil, nil))
	require.Empty(t, r.provider.last().ReanalysisNote)
	require.Empty(t, r.lastDecision().RequestNote)
}

// A note is answered by an analysis asked with it: it joins a reanalysis
// queued, and waits for the end of an analysis in progress.
func TestTrader_ReanalysisNoteQueued(t *testing.T) {
	r, _ := liveRunner(t, nil, 0)
	_, err := r.trader.Reanalyze(testPair, ui.ChannelWeb, "")
	require.NoError(t, err)
	response, err := r.trader.Reanalyze(testPair, ui.ChannelTelegram, "the news at 14:30?")
	require.NoError(t, err)
	require.Equal(t, ReanalysisQueued, response.Status)
	require.Contains(t, response.Message, "queued already, now with your note")
	r.inProgress(100.4)
	r.provider.answers = []answer{decide(ActionWait, 0.5, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Equal(t, "the news at 14:30?", r.provider.last().ReanalysisNote)
	require.Equal(t, "telegram", r.lastDecision().RequestedBy, "the channel of the note")

	// asked while the model is asked: a note waits for the next analysis, a
	// request without one is answered by the one in progress
	var withNote, without ui.ReanalysisResponse
	r.trader.provider = hookProvider{fakeProvider: r.provider, during: func() {
		var err error
		without, err = r.trader.Reanalyze(testPair, ui.ChannelWeb, "")
		require.NoError(t, err)
		withNote, err = r.trader.Reanalyze(testPair, ui.ChannelTelegram, "close it?")
		require.NoError(t, err)
	}}
	r.step(100.2, decide(ActionWait, 0.5, nil, nil, nil))
	require.Equal(t, ReanalysisRunning, without.Status)
	require.Equal(t, ReanalysisQueued, withNote.Status)
	require.Contains(t, withNote.Message, "asked without your note")
	require.Empty(t, r.provider.last().ReanalysisNote, "the close, asked before the note")
	require.True(t, r.trader.reanalysisPending(testPair))

	r.trader.provider = r.provider
	r.inProgress(100.3)
	r.provider.answers = []answer{decide(ActionWait, 0.5, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	require.Equal(t, "close it?", r.provider.last().ReanalysisNote)
}

// A request of the trader reducing the risk of the position held is applied
// whatever its confidence: EXIT, and the stop loss moved toward the price; the
// other decisions under the minimum are ignored, a note or not.
func TestTrader_ReanalysisRequestReducesRisk(t *testing.T) {
	held := func(t *testing.T, configure func(*Config)) *traderRunner {
		r, _ := liveRunner(t, configure, 0)
		r.step(100, enterLong(100)) // the stop loss at 98
		require.InDelta(t, 10, r.position(), 1e-9)
		return r
	}
	reanalyze := func(r *traderRunner, note string, price float64, d answer) {
		_, err := r.trader.Reanalyze(testPair, ui.ChannelTelegram, note)
		require.NoError(t, err)
		r.inProgress(price)
		r.provider.answers = []answer{d}
		r.trader.OnPartialCandle(r.df, r.broker)
		require.Empty(t, r.provider.answers, "asked")
	}

	// without a note, an exit under the minimum is ignored
	r := held(t, nil)
	reanalyze(r, "", 100.5, decide(ActionExit, 0.3, nil, nil, nil))
	require.Equal(t, OutcomeIgnored, r.lastDecision().Outcome)
	require.InDelta(t, 10, r.position(), 1e-9)

	// asked by the trader, it closes the position
	reanalyze(r, "Encerre a posição", 100.5, decide(ActionExit, 0.3, nil, nil, nil))
	require.Equal(t, OutcomeApplied, r.lastDecision().Outcome)
	require.Zero(t, r.position())

	// the stop loss tightened on request
	r = held(t, nil)
	reanalyze(r, "proteja o lucro", 100.5, decide(ActionModifySL, 0.3, nil, ptr(99.5), nil))
	require.Equal(t, OutcomeApplied, r.lastDecision().Outcome)
	_, stop := r.legs()
	require.InDelta(t, 99.5, *stop.Stop, 1e-9)

	// but not widened, even where the rules let it widen
	r = held(t, func(c *Config) { c.WidenStop = true })
	reanalyze(r, "afaste o stop", 100.5, decide(ActionModifySL, 0.3, nil, ptr(97), nil))
	require.Equal(t, OutcomeIgnored, r.lastDecision().Outcome)
	_, stop = r.legs()
	require.InDelta(t, 98, *stop.Stop, 1e-9)

	// and no entry on request: flat, the minimum stands
	flat, _ := liveRunner(t, nil, 0)
	reanalyze(flat, "compre agora", 100.5, decide(ActionEnterLong, 0.3, ptr(100.5), ptr(98.5), ptr(104.5)))
	require.Equal(t, OutcomeIgnored, flat.lastDecision().Outcome)
	require.Zero(t, flat.position())
}
