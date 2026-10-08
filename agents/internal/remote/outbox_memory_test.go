package remote

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/buntdb"

	"github.com/raykavin/xtrader/agentapi"
)

// refusingKeys refuses the records of keys for good (422), writing the others.
func refusingKeys(keys ...string) func(agentapi.OutboxRequest) (int, any) {
	return func(request agentapi.OutboxRequest) (int, any) {
		for i, record := range request.Records {
			for _, key := range keys {
				if record.Key == key {
					return http.StatusUnprocessableEntity, agentapi.OutboxError{Index: i, Error: "invalid"}
				}
			}
		}
		return http.StatusOK, nil
	}
}

// alertsOf returns the messages of the log at level about the controller out
// of reach or the outbox holding too much.
func alertsOf(hook *logtest.Hook, level logrus.Level) []string {
	var messages []string
	for _, entry := range hook.AllEntries() {
		if entry.Level == level && (strings.Contains(entry.Message, "don't reach the controller") ||
			strings.Contains(entry.Message, "all in memory")) {
			messages = append(messages, entry.Message)
		}
	}
	return messages
}

// R17h: the records set apart for good are kept for a look deadKeep, deadMax
// at most, then dropped: from the memory of the agent and from its file,
// through a restart.
func TestOutbox_RecordsSetApartLongAgoDropped(t *testing.T) {
	keep, most := deadKeep, deadMax
	t.Cleanup(func() { deadKeep, deadMax = keep, most })
	deadKeep, deadMax = time.Hour, 2

	f := newFakeController(t)
	f.set(func(f *fakeController) { f.outboxAnswer = refusingKeys("a", "b", "c") })
	file := filepath.Join(t.TempDir(), "agent.db")
	o, err := OpenOutbox(file, f.client())
	require.NoError(t, err)
	for _, key := range []string{"a", "b", "ok", "c"} {
		require.NoError(t, o.Add(agentapi.KindOrder, key, key))
	}
	require.NoError(t, o.Flush(context.Background()))
	assert.Equal(t, []string{"b", "c"}, setApartKeys(t, o), "the oldest past deadMax dropped")
	assert.Equal(t, 2, o.setApart)

	// set apart long ago, through a restart
	require.NoError(t, o.Close())
	again := openTestOutbox(t, f, file)
	require.Len(t, again.dead, 2)
	assert.WithinDuration(t, time.Now(), again.dead[0].buried, time.Minute, "when, kept in the file")
	deadKeep = 0
	again.pruneDead()
	assert.Empty(t, setApartKeys(t, again))
	assert.Zero(t, again.setApart)
	require.NoError(t, again.Add(agentapi.KindOrder, "next", 1))
	require.NoError(t, again.Flush(context.Background()))
	f.mu.Lock()
	last := f.outboxes[len(f.outboxes)-1]
	f.mu.Unlock()
	assert.Zero(t, last.SetApart, "told to the dashboard")
}

// R17h: a record set apart by an older outbox, which didn't keep when, is
// kept deadKeep since the outbox opened, then dropped as it runs.
func TestOutbox_RecordsSetApartBeforeKeptSinceTheStart(t *testing.T) {
	keep, every := deadKeep, pruneEvery
	t.Cleanup(func() { deadKeep, pruneEvery = keep, every })
	deadKeep, pruneEvery = 200*time.Millisecond, time.Millisecond

	f := newFakeController(t)
	file := filepath.Join(t.TempDir(), "agent.db")
	db, err := buntdb.Open(file)
	require.NoError(t, err)
	require.NoError(t, db.Update(func(tx *buntdb.Tx) error {
		_, _, err := tx.Set(recordKey(deadPrefix, 7), `{"seq":7,"kind":"order","key":"old","payload":1}`, nil)
		return err
	}))
	require.NoError(t, db.Close())

	o := openTestOutbox(t, f, file)
	assert.Equal(t, []string{"old"}, setApartKeys(t, o), "kept as the outbox opens")
	stop := runStopped(t, o)
	require.NoError(t, o.Add(agentapi.KindOrder, "a", 1))
	require.Eventually(t, func() bool {
		_ = o.Add(agentapi.KindOrder, "a", 1) // the sender loops
		return len(setApartKeys(t, o)) == 0
	}, 5*time.Second, 10*time.Millisecond)
	stop()
	assert.Zero(t, o.setApart)
}

// R17h: the records not reaching the controller are an error of the log, not
// a warning, once it is out of reach for alertDown, or the outbox, all in
// memory, holds alertRecords or alertBytes; none dropped.
func TestOutbox_AlertsWhileTheControllerIsOutForLong(t *testing.T) {
	down, records, bytes := alertDown, alertRecords, alertBytes
	t.Cleanup(func() { alertDown, alertRecords, alertBytes = down, records, bytes })
	alertDown, alertRecords, alertBytes = time.Hour, 3, 1<<20
	hook := logtest.NewGlobal()
	defer hook.Reset()

	f := newFakeController(t)
	file := filepath.Join(t.TempDir(), "agent.db")
	o := openTestOutbox(t, f, file)
	failure := errors.New("connection refused")

	require.NoError(t, o.Add(agentapi.KindJournal, "a", "a"))
	o.warned = time.Time{} // warned again
	o.failed(failure)
	assert.Len(t, alertsOf(hook, logrus.WarnLevel), 1, "a warning, for a while")
	assert.Empty(t, alertsOf(hook, logrus.ErrorLevel))

	o.failing = time.Now().Add(-alertDown)
	o.warned = time.Time{} // warned again
	o.failed(failure)
	require.Len(t, alertsOf(hook, logrus.ErrorLevel), 1, "out of reach for long")
	assert.Contains(t, alertsOf(hook, logrus.ErrorLevel)[0], "for 1h0m0s")
	assert.Contains(t, alertsOf(hook, logrus.ErrorLevel)[0], "holds 1 records")

	hook.Reset()
	o.failing = time.Now()
	require.NoError(t, o.Add(agentapi.KindJournal, "b", "b"))
	require.NoError(t, o.Add(agentapi.KindJournal, "c", "c"))
	o.warned = time.Time{} // warned again
	o.failed(failure)
	require.Len(t, alertsOf(hook, logrus.ErrorLevel), 1, "too many records")
	assert.Contains(t, alertsOf(hook, logrus.ErrorLevel)[0], "holds 3 records")

	hook.Reset()
	alertRecords = 1000
	require.NoError(t, o.Add(agentapi.KindJournal, "large", strings.Repeat("x", 1<<20)))
	o.warned = time.Time{} // warned again
	o.failed(failure)
	require.Len(t, alertsOf(hook, logrus.ErrorLevel), 1, "too many bytes")
	assert.Equal(t, 4, o.left(), "none dropped")

	// told as the agent starts again
	require.NoError(t, o.Close())
	hook.Reset()
	again := openTestOutbox(t, f, file)
	require.Len(t, alertsOf(hook, logrus.ErrorLevel), 1)
	assert.Equal(t, 4, again.left())
}
