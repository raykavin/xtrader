package exchange

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// m5fServerTicks sets the ticks of the symbol, as they come, on the clock of
// the trade server, behind the one of the bot by skew, until the returned
// function is called or the test ends.
func m5fServerTicks(t *testing.T, f *m5fFake, skew time.Duration) (stopped func()) {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	stopped = func() {
		once.Do(func() { close(stop) })
		<-done
	}
	t.Cleanup(stopped)
	go func() {
		defer close(done)
		for {
			f.setTickTime(m5fPair, time.Now().Add(-skew))
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	return stopped
}

// m5fReadTicks reads the ticks of the symbol a few times, a poll apart, as the
// stream does while a candle is in progress.
func m5fReadTicks(t *testing.T, m *MT5) {
	t.Helper()
	for range 5 {
		_, err := m.tick(context.Background(), m5fPair)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)
	}
}

// R13: with the clock of the agent ahead of the trade server's by more than
// mt5CloseDelay, eg: by a minute and a half, a candle the server is still
// building is over on the clock of the agent: the terminal connected (GET
// /health) doesn't confirm it closed until it is over on the server's clock,
// told by the ticks. In a market trading, the next candle of the terminal
// closes it as before, without waiting.
func TestMT5Candles_ClockAheadClosesOnTheServersClock(t *testing.T) {
	logs := m5fCaptureLogs(t)
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5DataOnly())

	// the server 10 s into a minute, the clock of the agent 80 s to 140 s ahead
	serverStart := time.Now().UTC().Add(-90 * time.Second).Truncate(time.Minute).Add(10 * time.Second)
	skew := time.Since(serverStart)
	latest := serverStart.Truncate(time.Minute) // in progress on the server, 50 s to go
	rates := m5fEndedCandles(5, latest, 4000)
	f.setRates(m5fPair, "M1", rates)
	m5fServerTicks(t, f, skew)
	m5fReadTicks(t, m)
	ahead := m.clockAhead()
	require.GreaterOrEqual(t, ahead, skew, "never less than ahead")
	require.Less(t, ahead, skew+time.Second)

	preloaded, err := m.CandlesByLimit(context.Background(), m5fPair, "1m", 10)
	require.NoError(t, err)
	require.Len(t, preloaded, 4, "the candle the server builds dropped as in progress")
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
	require.False(t, partial.Complete, "in progress on the server")
	m5fNone(t, candles, 300*time.Millisecond) // 15 polls: still in progress
	require.Zero(t, logs.count("doesn't confirm it closed"), "not held: in progress on the server")

	// the server goes on to the next candle: closed at once
	next := append(append([]mt5Rate(nil), rates...), mt5Rate{Time: latest.Add(time.Minute).Unix(), Open: 4010,
		High: 4011, Low: 4009, Close: 4010, TickVolume: 3, Spread: 260})
	f.setRates(m5fPair, "M1", next)
	closed := m5fNext(t, candles)
	require.Equal(t, latest, closed.Time)
	require.True(t, closed.Complete)
}

// The clock of the agent in time with the server's: a quiet market, no tick
// after the end of the candle, closes it by the clock with the terminal
// connected, as before.
func TestMT5Candles_ClockInTimeClosesAsBefore(t *testing.T) {
	f := newM5F(t)
	m := newM5FAdapter(t, f, WithMT5DataOnly())
	stop := m5fServerTicks(t, f, 0)
	m5fReadTicks(t, m)
	require.Zero(t, m.clockAhead())

	// the market quiet since the middle of the latest candle, ended a minute ago
	stop()
	latest := time.Now().UTC().Truncate(time.Minute).Add(-2 * time.Minute)
	f.setRates(m5fPair, "M1", m5fEndedCandles(5, latest, 4000))
	f.setTickTime(m5fPair, latest.Add(30*time.Second))

	preloaded, err := m.CandlesByLimit(context.Background(), m5fPair, "1m", 10)
	require.NoError(t, err)
	require.Len(t, preloaded, 5)
	require.Equal(t, latest, preloaded[4].Time)
}

// clockAhead tells the least of the last skews, past mt5CloseDelay, within
// mt5SkewMax, of the last mt5SkewKeep.
func TestMT5ClockAhead(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name  string
		skews []mt5Skew
		want  time.Duration
	}{
		{"unknown", nil, 0},
		{"the least", []mt5Skew{{12 * time.Second, now}, {10 * time.Second, now}, {11 * time.Second, now}},
			10 * time.Second},
		{"within the delay of the close", []mt5Skew{{12 * time.Second, now}, {time.Second, now}}, 0},
		{"no clock", []mt5Skew{{2 * time.Hour, now}}, 0},
		{"old ones left out", []mt5Skew{{time.Second, now.Add(-time.Hour)}, {5 * time.Second, now}},
			5 * time.Second},
		{"all old", []mt5Skew{{5 * time.Second, now.Add(-time.Hour)}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &MT5{skews: tc.skews}
			require.Equal(t, tc.want, m.clockAhead())
		})
	}
}
