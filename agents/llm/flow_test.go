package llm

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/order"
	"github.com/raykavin/xtrader/ui"
)

// the broker of the strategies reads the price of now, see recheck
var _ quoter = (*order.Controller)(nil)

// quotingBroker is the paper wallet reading the last price of a pair as the
// order controller of the bot does (see quoter): price, or err.
type quotingBroker struct {
	*exchange.PaperWallet
	price float64
	err   error
	reads int
}

func (q *quotingBroker) LastQuote(string) (float64, error) {
	q.reads++
	return q.price, q.err
}

// R4: an order of someone else, eg: the order ticket of the dashboard, opens a
// position on the pair while the model is asked: the entry decided on the
// context, flat, is refused with why, rather than a second position opened.
func TestTrader_EntryRefusedOnAPositionOpenedMeanwhile(t *testing.T) {
	r, _ := liveRunner(t, nil, 0)
	r.trader.provider = hookProvider{fakeProvider: r.provider, during: func() {
		_, err := r.wallet.CreateOrderMarket(model.SideTypeBuy, testPair, 3)
		require.NoError(t, err)
	}}

	r.step(100, enterLong(100))
	require.Nil(t, r.provider.last().Position, "flat when the context was read")
	d := r.lastDecision()
	require.Equal(t, string(ActionEnterLong), d.Action)
	require.Equal(t, OutcomeRefused, d.Outcome)
	require.Contains(t, d.Error, "LONG position of 3 is held on the pair now")
	require.Empty(t, d.OrderIDs)
	require.InDelta(t, 3, r.position(), 1e-9, "the position of the order ticket alone")
}

// R4: the price moves while the model is asked: the entry is validated again
// at the price of now, read from the broker, by the rules of the context (the
// sides of its levels, the risk:reward net of the spread), refused when they
// fail; a price unread refuses it too.
func TestTrader_EntryValidatedAtThePriceOfNow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		price  float64
		err    error
		refuse string // in the error kept, empty when applied
	}{
		{name: "beyond the stop loss", price: 97.5, refuse: "the stop loss 98 of a LONG must be below 97.5"},
		{name: "the risk:reward under the minimum", price: 100.6, refuse: "the risk:reward"},
		{name: "unread", err: errors.New("bridge down"), refuse: "the price of now unread: bridge down"},
		{name: "within the rules", price: 100.2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := liveRunner(t, func(c *Config) {
				c.MinRiskReward = 1.5
				c.MaxEntryATR, c.MaxLevelATR = 0, 0 // the sides of the levels and the risk:reward alone
			}, 0)
			broker := &quotingBroker{PaperWallet: r.wallet, price: tc.price, err: tc.err}
			r.broker = broker

			r.step(100, enterLong(100)) // stop loss 98, take profit 104: 2.0 at 100
			require.Equal(t, number(100), r.provider.last().Price)
			d := r.lastDecision()
			if tc.refuse == "" {
				require.Equal(t, OutcomeApplied, d.Outcome)
				require.Empty(t, d.Error)
				require.InDelta(t, 10, r.position(), 1e-9)
			} else {
				require.Equal(t, OutcomeRefused, d.Outcome)
				require.Contains(t, d.Error, tc.refuse)
				require.Zero(t, r.position(), "no entry")
			}
			require.Equal(t, 1, broker.reads, "the price read again")
		})
	}

	// in a backtest, the price of the context is the one of now: not read
	r := newTraderRunner(t, nil, 0)
	broker := &quotingBroker{PaperWallet: r.wallet, err: errors.New("invalid operation")}
	r.broker = broker
	r.step(100, enterLong(100))
	require.Zero(t, broker.reads)
	require.Equal(t, OutcomeApplied, r.lastDecision().Outcome)
}

// R4: the decision of the start, asked on the candle closed before the one in
// progress, is applied on a later tick at the market of that tick: refused
// once the price went beyond its stop loss.
func TestTrader_StartEntryRefusedAtThePriceOfNow(t *testing.T) {
	r := startRunner(t, func(c *Config) { c.MaxEntryATR, c.MaxLevelATR = 0, 0 })
	price := r.df.Close[len(r.df.Close)-2] // the close of the candle before the one in progress
	r.provider.answers = []answer{decide(ActionEnterLong, 0.8, ptr(price), ptr(price-2), ptr(price+4))}
	r.trader.OnPartialCandle(r.df, r.broker)
	r.startAsked(testPair)
	require.Equal(t, number(price), r.provider.last().Price)

	last := len(r.df.Close) - 1
	r.df.Close[last], r.df.Low[last] = price-2.5, price-2.6 // the next tick
	r.trader.OnPartialCandle(r.df, r.broker)
	d := r.lastDecision()
	require.Equal(t, TriggerStart, d.Trigger)
	require.Equal(t, OutcomeRefused, d.Outcome)
	require.Contains(t, d.Error, "at the price of now")
	require.Contains(t, d.Error, "stop loss")
	require.Zero(t, r.position())
}

// B30: the analyses of the candle in progress (a consult interval shorter than
// the timeframe) see the decisions taken before them in it, the one of the
// close included, newest first.
func TestTrader_MemorySeesTheDecisionsOfTheCandleInProgress(t *testing.T) {
	r, _ := liveRunner(t, func(c *Config) { c.Memory = 5 }, 0) // the close, WAIT 0.4
	r.trader.cfg.ConsultInterval = 5 * time.Minute
	r.inProgress(100.4) // 7 minutes after its open
	opened := r.df.Time[len(r.df.Time)-1]

	r.provider.answers = []answer{{decision: Decision{Action: ActionWait, Confidence: 0.5, Reason: "first"}}}
	r.trader.OnPartialCandle(r.df, r.broker)
	memory := r.provider.last().Memory
	require.NotNil(t, memory)
	require.Len(t, memory.Decisions, 1, "the decision of the close")
	require.Equal(t, ratio(0.4), memory.Decisions[0].Confidence)
	require.Zero(t, memory.Decisions[0].CandlesAgo, "7 minutes ago")

	r.trader.now = func() time.Time { return opened.Add(10*time.Minute + time.Second) }
	r.provider.answers = []answer{{decision: Decision{Action: ActionWait, Confidence: 0.55, Reason: "second"}}}
	r.trader.OnPartialCandle(r.df, r.broker)
	memory = r.provider.last().Memory
	require.Len(t, memory.Decisions, 2)
	require.Equal(t, "first", memory.Decisions[0].Reason, "the newest first")
	require.Zero(t, memory.Decisions[0].CandlesAgo)
	require.Equal(t, ratio(0.4), memory.Decisions[1].Confidence)
}

// B30: a consult interval of seconds fills the decisions kept with the ones of
// the candle in progress: the memory holds the last ones all the same.
func TestTrader_MemoryOfAShortConsultInterval(t *testing.T) {
	r, _ := liveRunner(t, func(c *Config) { c.Memory = 5 }, 0)
	r.trader.cfg.ConsultInterval = 10 * time.Second
	r.inProgress(100.4)
	opened := r.df.Time[len(r.df.Time)-1]
	for i := 1; i <= maxRemembered+5; i++ {
		r.trader.now = func() time.Time { return opened.Add(time.Duration(i)*10*time.Second + time.Second) }
		r.provider.answers = []answer{{decision: Decision{Action: ActionWait, Confidence: 0.5,
			Reason: time.Duration(i * 10 * int(time.Second)).String()}}}
		r.trader.OnPartialCandle(r.df, r.broker)
		require.Empty(t, r.provider.answers, "asked in the slot %d", i)
	}
	memory := r.provider.last().Memory
	require.Len(t, memory.Decisions, 5, "Config.Memory")
	require.Equal(t, "4m0s", memory.Decisions[0].Reason, "the one before the last asked")
	require.Equal(t, "3m20s", memory.Decisions[4].Reason)
}

// B30: a memory longer than the decisions kept by default keeps as many.
func TestTrader_MemoryLongerThanTheDefault(t *testing.T) {
	r := newTraderRunner(t, func(c *Config) { c.Memory = maxRemembered + 5 }, 0)
	for range maxRemembered + 6 {
		r.step(100, decide(ActionWait, 0.4, nil, nil, nil))
	}
	memory := r.provider.last().Memory
	require.Len(t, memory.Decisions, maxRemembered+5)
	require.Equal(t, 1, memory.Decisions[0].CandlesAgo)
	require.Equal(t, maxRemembered+5, memory.Decisions[maxRemembered+4].CandlesAgo)
}

// rememberedJournal is a journal giving back remembered.
type rememberedJournal struct {
	remembered []Remembered
	perPair    int
}

func (j *rememberedJournal) Append(JournalEntry) error { return nil }

func (j *rememberedJournal) Recent(perPair int) ([]Remembered, error) {
	j.perPair = perPair
	return j.remembered, nil
}

// B30: the decisions of the journal are remembered by the time they were
// taken, else at the close of their candle, from a controller of a version
// before it: the ones taken on the candle in progress are seen by the next
// analysis in it, after a restart.
func TestTrader_MemoryFromTheJournal(t *testing.T) {
	r, _ := liveRunner(t, nil, 0)
	r.trader.remembered = map[string][]remembered{}
	r.inProgress(100.4) // 7 minutes after its open
	opened := r.df.Time[len(r.df.Time)-1]
	closed := opened.Add(-15 * time.Minute) // the candle before
	journal := &rememberedJournal{remembered: []Remembered{
		{Pair: testPair, Candle: closed.Add(-15 * time.Minute), Action: ActionWait, Confidence: 0.3, Reason: "old"},
		{Pair: testPair, Candle: closed, Action: ActionWait, Confidence: 0.32, Reason: "close"},
		{Pair: testPair, Candle: closed, At: opened.Add(5 * time.Minute), Action: ActionWait, Confidence: 0.35,
			Reason: "in progress"},
	}}
	require.NoError(t, r.trader.SetJournal(journal))
	require.Equal(t, maxRemembered, journal.perPair)

	_, err := r.trader.Reanalyze(testPair, ui.ChannelWeb, "")
	require.NoError(t, err)
	r.provider.answers = []answer{decide(ActionWait, 0.4, nil, nil, nil)}
	r.trader.OnPartialCandle(r.df, r.broker)
	memory := r.provider.last().Memory
	require.Len(t, memory.Decisions, 3)
	require.Equal(t, "in progress", memory.Decisions[0].Reason)
	require.Zero(t, memory.Decisions[0].CandlesAgo, "2 minutes ago")
	require.Equal(t, "close", memory.Decisions[1].Reason, "at the close of its candle, the open of this one")
	require.Zero(t, memory.Decisions[1].CandlesAgo, "7 minutes ago")
	require.Equal(t, "old", memory.Decisions[2].Reason)
	require.Equal(t, 1, memory.Decisions[2].CandlesAgo)
}

// B31: the candles held are the ones closed since the entry, the closes the
// consult interval leaves out included: 4 and 8 with an interval of 1h on
// candles of 15m, as after a restart, the position followed from its stored
// entry.
func TestTrader_CandlesHeldWithAConsultIntervalAboveTheTimeframe(t *testing.T) {
	r, _ := liveRunner(t, nil, 0)
	r.trader.cfg.ConsultInterval = time.Hour
	onSchedule := func() bool {
		closes := r.df.Time[len(r.df.Time)-1].Add(30 * time.Minute) // of the candle added next
		return closes.Truncate(time.Hour).Equal(closes)
	}
	for !onSchedule() {
		r.step(100)
	}
	r.step(100, enterLong(100))
	require.InDelta(t, 10, r.position(), 1e-9)
	entered := r.trader.now()

	var held []int
	for range 8 {
		if onSchedule() {
			r.step(100, decide(ActionHold, 0.7, nil, nil, nil))
			held = append(held, r.provider.last().Position.Candles)
		} else {
			r.step(100)
		}
	}
	require.Equal(t, []int{4, 8}, held)

	r.inProgress(100)
	restarted, err := New(r.trader.cfg, r.provider)
	require.NoError(t, err)
	restarted.now = r.trader.now
	restarted.SetOrderSource(func(string) ([]model.Order, error) {
		return []model.Order{{ID: 1, Pair: testPair, Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
			Status: model.OrderStatusTypeFilled, Price: 100, Quantity: 10, CreatedAt: entered, UpdatedAt: entered}}, nil
	})
	restarted.Indicators(r.df)
	r.provider.answers = []answer{decide(ActionHold, 0.7, nil, nil, nil)}
	restarted.OnPartialCandle(r.df, r.broker) // the start
	select {
	case <-restarted.starting[testPair].done:
	case <-time.After(10 * time.Second):
		t.Fatal("the start not asked")
	}
	require.Equal(t, 8, r.provider.last().Position.Candles, "from its stored entry")
}

// B31: the candles held are counted on the candles closed, a market closed
// closing none, the ones before the window counted by the time.
func TestTrader_HeldCandles(t *testing.T) {
	r := newTraderRunner(t, nil, 0)
	open := time.Date(2026, 10, 2, 20, 0, 0, 0, time.UTC) // a Friday
	df := &model.Dataframe{Pair: testPair, Time: []time.Time{open, open.Add(15 * time.Minute),
		open.Add(30 * time.Minute), open.Add(63 * time.Hour), open.Add(63*time.Hour + 15*time.Minute)}}
	at := open.Add(63*time.Hour + 30*time.Minute + time.Second)
	for _, tc := range []struct {
		since time.Time
		held  int
	}{
		{open.Add(16 * time.Minute), 4},      // within the second candle: its close and the 3 after
		{open.Add(15 * time.Minute), 4},      // at the close of the first one
		{open.Add(45 * time.Minute), 2},      // after the close of Friday: the 2 of Monday
		{at, 0},                              // followed from now
		{open.Add(-30 * time.Minute), 5 + 2}, // 2 before the window
		{time.Time{}, 0},
	} {
		require.Equal(t, tc.held, r.trader.heldCandles(df, tc.since, at), tc.since)
	}
	require.Equal(t, 3, r.trader.heldCandles(&model.Dataframe{}, at.Add(-50*time.Minute), at), "by the time")
}

// M13: a decision reducing the risk on a note of the trader, applied under
// the min confidence, keeps that the minimum was waived for it; none is told
// at or over the minimum.
func TestTrader_ReanalysisWaiverKept(t *testing.T) {
	r, _ := liveRunner(t, nil, 0)
	r.step(100, enterLong(100))
	reanalyze := func(note string, d answer) ui.Decision {
		_, err := r.trader.Reanalyze(testPair, ui.ChannelTelegram, note)
		require.NoError(t, err)
		r.inProgress(100.5)
		r.provider.answers = []answer{d}
		r.trader.OnPartialCandle(r.df, r.broker)
		require.Empty(t, r.provider.answers, "asked")
		return r.lastDecision()
	}

	d := reanalyze("proteja o lucro", decide(ActionModifySL, 0.65, nil, ptr(99), nil))
	require.Equal(t, OutcomeApplied, d.Outcome)
	require.Empty(t, d.Notes, "over the minimum: nothing waived")

	d = reanalyze("Encerre a posição", decide(ActionExit, 0.3, nil, nil, nil))
	require.Equal(t, OutcomeApplied, d.Outcome)
	require.Equal(t, "Encerre a posição", d.RequestNote)
	require.Len(t, d.Notes, 1)
	require.Equal(t, "text", d.Notes[0].Kind)
	require.Contains(t, d.Notes[0].Text, "the min confidence 0.60 waived for this EXIT of confidence 0.30")
	require.Contains(t, d.Notes[0].Text, "note of the trader")
	require.Zero(t, r.position())
}
