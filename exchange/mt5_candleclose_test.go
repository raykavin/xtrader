package exchange

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// m5fLogs collects the messages logged while a test runs.
type m5fLogs struct {
	mu       sync.Mutex
	messages []string
}

func (l *m5fLogs) Levels() []logrus.Level { return logrus.AllLevels }

func (l *m5fLogs) Fire(entry *logrus.Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, entry.Message)
	return nil
}

// count returns how many messages logged contain text.
func (l *m5fLogs) count(text string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, message := range l.messages {
		if strings.Contains(message, text) {
			n++
		}
	}
	return n
}

func m5fCaptureLogs(t *testing.T) *m5fLogs {
	t.Helper()
	logs := &m5fLogs{}
	logger := logrus.StandardLogger()
	previous := logger.ReplaceHooks(make(logrus.LevelHooks))
	hooks := make(logrus.LevelHooks)
	for level, list := range previous {
		hooks[level] = append(hooks[level], list...)
	}
	hooks.Add(logs)
	logger.ReplaceHooks(hooks)
	t.Cleanup(func() { logger.ReplaceHooks(previous) })
	return logs
}

// m5fEndedCandles returns count M1 candles, the latest one opened at latest.
func m5fEndedCandles(count int, latest time.Time, base float64) []mt5Rate {
	rates := make([]mt5Rate, 0, count)
	for i := count - 1; i >= 0; i-- {
		start := latest.Add(-time.Duration(i) * time.Minute)
		open := base + float64(count-i)
		rates = append(rates, mt5Rate{Time: start.Unix(), Open: open, High: open + 2, Low: open - 1,
			Close: open + 1, TickVolume: float64(100 + i), Spread: 260})
	}
	return rates
}

// setTickTime sets the time of the last tick of a symbol, on the clock of the
// trade server.
func (f *m5fFake) setTickTime(symbol string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tick := f.ticks[symbol]
	tick.timeMsc = at.UnixMilli()
	f.ticks[symbol] = tick
}

// m5fNone requires no candle on the stream for wait.
func m5fNone(t *testing.T, candles chan model.Candle, wait time.Duration) {
	t.Helper()
	select {
	case candle := <-candles:
		t.Fatalf("unexpected candle %s complete=%v close=%v", candle.Time, candle.Complete, candle.Close)
	case <-time.After(wait):
	}
}

// A terminal disconnected from its server mid-candle keeps serving that candle,
// partial, without a newer one: over on the clock, it is not complete until the
// terminal confirms it (connected again, or a tick after its end), neither in
// the preload nor in the stream. A change after it was sent complete is
// logged, not sent again.
func TestMT5Candles_ClosedOnlyWhenTheTerminalConfirms(t *testing.T) {
	logs := m5fCaptureLogs(t)
	f := newM5F(t)
	f.setHealth("connected", false)
	m := newM5FAdapter(t, f, WithMT5DataOnly())

	// the latest candle ended a minute ago or more, its last tick in its middle
	latest := time.Now().UTC().Truncate(time.Minute).Add(-2 * time.Minute)
	rates := m5fEndedCandles(5, latest, 4000)
	f.setRates(m5fPair, "M1", rates)
	f.setTickTime(m5fPair, latest.Add(30*time.Second))

	preloaded, err := m.CandlesByLimit(context.Background(), m5fPair, "1m", 10)
	require.NoError(t, err)
	require.Len(t, preloaded, 4, "the latest candle isn't confirmed: dropped as in progress")
	require.Equal(t, latest.Add(-time.Minute), preloaded[3].Time)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	candles, errs := m.CandlesSubscription(ctx, m5fPair, "1m")
	go func() {
		for range errs {
		}
	}()

	partial := m5fNext(t, candles)
	require.Equal(t, latest, partial.Time)
	require.False(t, partial.Complete, "sent in progress")
	m5fNone(t, candles, 300*time.Millisecond) // 15 polls: still not complete
	require.NotZero(t, f.requestCount("GET", "/health"), "the state of the terminal asked")
	require.Equal(t, 1, logs.count("doesn't confirm it closed"), "held: logged once")

	// the terminal connected to its server again: complete
	f.setHealth("connected", true)
	closed := m5fNext(t, candles)
	require.Equal(t, latest, closed.Time)
	require.True(t, closed.Complete)
	require.Equal(t, rates[4].Close, closed.Close)
	require.Equal(t, 1, logs.count("the terminal confirmed it"))

	// the terminal changes it after: logged, never sent again
	changed := append([]mt5Rate(nil), rates...)
	changed[4].Close += 5
	changed[4].High += 5
	f.setRates(m5fPair, "M1", changed)
	m5fNone(t, candles, 200*time.Millisecond)
	require.Eventually(t, func() bool { return logs.count("changed the candle") == 1 }, 2*time.Second,
		5*time.Millisecond)
	m5fNone(t, candles, 100*time.Millisecond)
	require.Equal(t, 1, logs.count("changed the candle"), "logged once per change")
}

// A tick of the symbol after the end of the candle, read before the candles,
// confirms it closed, the terminal not connected to its server per GET
// /health notwithstanding.
func TestMT5Candles_ClosedByATickAfterItsEnd(t *testing.T) {
	f := newM5F(t)
	f.setHealth("connected", false)
	m := newM5FAdapter(t, f, WithMT5DataOnly())

	latest := time.Now().UTC().Truncate(time.Minute).Add(-2 * time.Minute)
	f.setRates(m5fPair, "M1", m5fEndedCandles(5, latest, 4000))
	f.setTickTime(m5fPair, latest.Add(time.Minute+time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	candles, errs := m.CandlesSubscription(ctx, m5fPair, "1m")
	go func() {
		for range errs {
		}
	}()

	// no preload: the first poll reads the tick, the next one confirms
	first := m5fNext(t, candles)
	require.Equal(t, latest, first.Time)
	require.False(t, first.Complete, "the tick was read after the candles")
	closed := m5fNext(t, candles)
	require.Equal(t, latest, closed.Time)
	require.True(t, closed.Complete)
}

// Connected to its server, the terminal confirms the candle of a quiet market
// at once, with no tick after its end: the preload keeps it.
func TestMT5Candles_ConnectedTerminalClosesByTheClock(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5DataOnly())

	latest := time.Now().UTC().Truncate(time.Minute).Add(-2 * time.Minute)
	f.setRates(m5fPair, "M1", m5fEndedCandles(5, latest, 4000))
	f.setTickTime(m5fPair, latest.Add(30*time.Second))

	preloaded, err := m.CandlesByLimit(context.Background(), m5fPair, "1m", 10)
	require.NoError(t, err)
	require.Len(t, preloaded, 5)
	require.Equal(t, latest, preloaded[4].Time)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	candles, errs := m.CandlesSubscription(ctx, m5fPair, "1m")
	go func() {
		for range errs {
		}
	}()
	m5fNone(t, candles, 200*time.Millisecond) // sent by the preload already
}

// A candle sent complete that the terminal changes after, eg: rebuilt from the
// history of the server after a reconnection, is logged with both versions:
// the strategies ran on the one sent, which is never sent again.
func TestMT5Candles_ChangedAfterSentIsLogged(t *testing.T) {
	// the candle in progress stays so during the test
	if left := time.Until(time.Now().Truncate(time.Minute).Add(time.Minute)); left < 10*time.Second {
		time.Sleep(left + 100*time.Millisecond)
	}
	logs := m5fCaptureLogs(t)
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5DataOnly())
	rates := m5fCandles(5, time.Minute, 4000) // the latest one in progress
	f.setRates(m5fPair, "M1", rates)
	_, err := m.CandlesByLimit(context.Background(), m5fPair, "1m", 10)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	candles, errs := m.CandlesSubscription(ctx, m5fPair, "1m")
	go func() {
		for range errs {
		}
	}()
	require.False(t, m5fNext(t, candles).Complete)

	// a new candle starts: the previous one sent complete
	next := rates[4]
	next.Time += 60
	rates = append(rates, next)
	f.setRates(m5fPair, "M1", rates)
	sent := m5fNext(t, candles)
	require.True(t, sent.Complete)
	require.Equal(t, time.Unix(rates[4].Time, 0).UTC(), sent.Time)
	require.False(t, m5fNext(t, candles).Complete)

	changed := append([]mt5Rate(nil), rates...)
	changed[4].Low -= 3
	f.setRates(m5fPair, "M1", changed)
	require.Eventually(t, func() bool { return logs.count("changed the candle") == 1 }, 2*time.Second,
		5*time.Millisecond)
	m5fNone(t, candles, 100*time.Millisecond)
	require.Equal(t, 1, logs.count("changed the candle"), "logged once per change")
}
