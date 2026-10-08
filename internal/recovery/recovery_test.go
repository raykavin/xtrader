package recovery

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCall_RecoversThePanic(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	recovered := Call("OnCandle of the strategy on BTCUSDT", at, func() {
		var values []float64
		_ = values[3] // index out of range
	})
	require.NotNil(t, recovered)
	require.Contains(t, recovered.Error(), "OnCandle of the strategy on BTCUSDT panicked on the candle of "+
		"2026-10-07 12:00:00: runtime error: index out of range")
	require.Contains(t, string(recovered.Stack), "recovery_test.go", "where it panicked")

	require.Nil(t, Call("OnCandle", at, func() {}), "no panic")
}

// R10: every panic is logged, and notified once every NotifyEvery at most by
// call, with the count of the ones since: a panic on every candle in progress
// doesn't send a message every few seconds.
func TestReporter_NotifiesACallOnceAWhile(t *testing.T) {
	var notified []error
	r := NewReporter(func(err error) { notified = append(notified, err) })
	panicked := func(call string) *Panic {
		return Call(call, time.Time{}, func() { panic(errors.New("boom")) })
	}

	r.Report(panicked("Indicators of the strategy on A"), "the strategy skipped the candle")
	r.Report(panicked("Indicators of the strategy on A"), "the strategy skipped the candle")
	r.Report(panicked("Indicators of the strategy on B"), "the strategy skipped the candle")
	require.Len(t, notified, 2, "once by call")
	require.EqualError(t, notified[0], "Indicators of the strategy on A panicked: boom: the strategy skipped the candle")
	var recovered *Panic
	require.ErrorAs(t, notified[0], &recovered)
	require.True(t, strings.HasPrefix(notified[1].Error(), "Indicators of the strategy on B"))

	r.every = 0 // the time passed
	r.Report(panicked("Indicators of the strategy on A"), "the strategy skipped the candle")
	require.Len(t, notified, 3)
	require.EqualError(t, notified[2], "Indicators of the strategy on A panicked: boom: the strategy skipped "+
		"the candle (1 more since the last notice)")

	r.SetNotify(nil)
	r.Report(panicked("Indicators of the strategy on A"), "the strategy skipped the candle") // logged only
	require.Len(t, notified, 3)
}
