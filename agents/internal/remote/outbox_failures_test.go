package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/buntdb"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/ui"
)

// openTestOutbox opens the outbox of file, sent to the fake controller, closed
// as the test ends.
func openTestOutbox(t *testing.T, f *fakeController, file string) *Outbox {
	t.Helper()
	o, err := OpenOutbox(file, f.client())
	require.NoError(t, err)
	t.Cleanup(func() { _ = o.Close() })
	return o
}

// runStopped runs the outbox until stop is called, or the test ends.
func runStopped(t *testing.T, o *Outbox) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_ = o.Run(ctx)
	}()
	stop = func() {
		cancel()
		<-finished
	}
	t.Cleanup(stop)
	return stop
}

// writtenKeys returns the keys of the records the fake controller wrote.
func writtenKeys(f *fakeController) []string {
	var keys []string
	for _, r := range f.written() {
		keys = append(keys, r.Key)
	}
	return keys
}

// setApartKeys returns the keys of the records set apart in the outbox.
func setApartKeys(t *testing.T, o *Outbox) []string {
	t.Helper()
	return keysOf(t, o, deadPrefix)
}

// heldKeys returns the keys of the records held apart in the outbox.
func heldKeys(t *testing.T, o *Outbox) []string {
	t.Helper()
	return keysOf(t, o, heldPrefix)
}

// keysOf returns the keys of the records of the outbox under prefix.
func keysOf(t *testing.T, o *Outbox, prefix string) []string {
	t.Helper()
	var keys []string
	o.dbmu.RLock()
	defer o.dbmu.RUnlock()
	require.NoError(t, o.db.View(func(tx *buntdb.Tx) error {
		return tx.AscendKeys(prefix+"*", func(_, value string) bool {
			keys = append(keys, gjsonKey(value))
			return true
		})
	}))
	return keys
}

// gjsonKey returns the key of the JSON of a record.
func gjsonKey(value string) string {
	_, rest, _ := strings.Cut(value, `"key":"`)
	key, _, _ := strings.Cut(rest, `"`)
	return key
}

// unreadableBatches answers 400 to the batches of more than one record, as the
// controller does to a batch it can't read, eg: past agentapi.MaxBody; single
// answers the records alone.
func unreadableBatches(single int) func(agentapi.OutboxRequest) (int, any) {
	return func(request agentapi.OutboxRequest) (int, any) {
		if len(request.Records) > 1 {
			return http.StatusBadRequest, ui.ErrorResponse{Error: "invalid JSON payload"}
		}
		return single, ui.ErrorResponse{Error: "the records can't be written now"}
	}
}

// B26: a batch the controller can't read whole (400, or 413 too large) is
// sent a record at a time at once, every record of it, never waiting for a
// record added to go on.
func TestOutbox_UnreadableBatchSentOneByOneAtOnce(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			f := newFakeController(t)
			f.set(func(f *fakeController) {
				f.outboxAnswer = func(request agentapi.OutboxRequest) (int, any) {
					if len(request.Records) > 1 {
						return status, ui.ErrorResponse{Error: http.StatusText(status)}
					}
					return http.StatusOK, nil
				}
			})
			o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
			for _, key := range []string{"a", "b", "c", "d"} {
				require.NoError(t, o.Add(agentapi.KindJournal, key, key))
			}
			run(t, o)
			require.Eventually(t, func() bool { return len(f.written()) == 4 && o.left() == 0 }, 3*time.Second,
				time.Millisecond, "every record sent, none added since")
			assert.Equal(t, []string{"a", "b", "c", "d"}, writtenKeys(f), "in order")
			pending, err := o.Pending(agentapi.KindJournal)
			require.NoError(t, err)
			assert.Empty(t, pending)
			f.mu.Lock()
			defer f.mu.Unlock()
			for i := 1; i < len(f.outboxes); i++ {
				before, next := f.outboxes[i-1].Records, f.outboxes[i].Records
				assert.False(t, len(before) > 1 && next[0].Seq == before[0].Seq && len(next) >= len(before),
					"a batch it can't read halved at once, never sent again as it was")
			}
		})
	}
}

// B26: Flush sends a batch the controller can't read whole a record at a time,
// and tells it sent only once none is left.
func TestOutbox_FlushSendsAnUnreadableBatchOneByOne(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) { f.outboxAnswer = unreadableBatches(http.StatusOK) })
	o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, o.Add(agentapi.KindDecision, key, key))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, o.Flush(ctx))
	assert.Equal(t, []string{"a", "b", "c"}, writtenKeys(f))
}

// B26: Flush never tells the outbox sent with records left: it tries until
// its context is done, then fails.
func TestOutbox_FlushNeverTellsSentWithRecordsLeft(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) { f.outboxAnswer = unreadableBatches(http.StatusServiceUnavailable) })
	o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, o.Add(agentapi.KindOrder, key, key))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := o.Flush(ctx)
	require.Error(t, err)
	assert.ErrorContains(t, err, "not sent whole")
	pending, err := o.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	assert.Len(t, pending, 3, "kept for the next start")
	assert.Empty(t, f.written())
}

// failingKey answers 503 to the requests holding a record of key, as the
// controller does to a record it fails to write for another reason than its
// data, eg: SQLSTATE 54000 before it refused it: nothing written.
func failingKey(key string) func(agentapi.OutboxRequest) (int, any) {
	return func(request agentapi.OutboxRequest) (int, any) {
		for _, record := range request.Records {
			if record.Key == key {
				return http.StatusServiceUnavailable, ui.ErrorResponse{Error: "the records can't be written now"}
			}
		}
		return http.StatusOK, nil
	}
}

// R7: a record the controller fails to write again and again, never refusing
// it for good (422), while it writes the records of its kind after it, is
// held apart once found alone and failing for holdAfter, the records around
// it written: the queue never stops for it. Its count reaches the dashboard,
// and it is kept through a restart.
func TestOutbox_ARecordFailingForeverIsHeldApart(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) { f.outboxAnswer = failingKey("b") })
	file := filepath.Join(t.TempDir(), "agent.db")
	o, err := OpenOutbox(file, f.client())
	require.NoError(t, err)
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, o.Add(agentapi.KindOrder, key, key))
	}
	stop := runStopped(t, o)
	require.Eventually(t, func() bool { return len(f.written()) == 2 && o.left() == 0 }, 10*time.Second,
		time.Millisecond)
	assert.Equal(t, []string{"a", "c"}, writtenKeys(f))
	pending, err := o.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	require.Len(t, pending, 1, "not sent yet, for the memory of the agent")
	assert.Equal(t, "b", pending[0].Key)
	assert.Equal(t, []string{"b"}, heldKeys(t, o), "kept apart")
	assert.Empty(t, setApartKeys(t, o), "never refused for good")

	require.NoError(t, o.Add(agentapi.KindOrder, "d", "d"))
	// written, and its answer read: off the outbox before it stops, or it is
	// still there, not sent yet, when it opens again
	require.Eventually(t, func() bool { return len(f.written()) == 3 && o.left() == 0 }, 5*time.Second,
		time.Millisecond, "the next ones sent")
	f.mu.Lock()
	last := f.outboxes[len(f.outboxes)-1]
	f.mu.Unlock()
	assert.Equal(t, 1, last.Held, "told to the dashboard")
	stop()
	require.NoError(t, o.Close())

	again := openTestOutbox(t, f, file)
	assert.Zero(t, again.left())
	assert.Equal(t, []string{"b"}, heldKeys(t, again), "kept through a restart")
	require.NoError(t, again.Add(agentapi.KindJournal, "e", "e"))
	require.NoError(t, again.Flush(context.Background()))
	f.mu.Lock()
	last = f.outboxes[len(f.outboxes)-1]
	f.mu.Unlock()
	assert.Equal(t, 1, last.Held)
}

// R7: nothing is set apart while the controller fails every record, eg: its
// database out of reach, however long it lasts: once it writes again, all of
// them are written, in order.
func TestOutbox_NothingSetApartWhileTheControllerFailsThemAll(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) { f.down = true })
	o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, o.Add(agentapi.KindOrder, key, key))
	}
	run(t, o)
	start := time.Now()
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.requests) > 6*poisonAfter && time.Since(start) > 2*holdAfter
	}, 10*time.Second, time.Millisecond)
	assert.Empty(t, setApartKeys(t, o))
	assert.Empty(t, heldKeys(t, o))
	pending, err := o.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	assert.Len(t, pending, 3)

	f.set(func(f *fakeController) { f.down = false })
	require.Eventually(t, func() bool { return len(f.written()) == 3 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, []string{"a", "b", "c"}, writtenKeys(f))
}

// B26: nothing is set apart while the controller reads no batch, eg: a fault
// of its own, the records alone included: the queue never drains itself into
// the records set apart.
func TestOutbox_NothingSetApartWhileTheControllerReadsNone(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.outboxAnswer = func(agentapi.OutboxRequest) (int, any) {
			return http.StatusBadRequest, ui.ErrorResponse{Error: "invalid JSON payload"}
		}
	})
	o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, o.Add(agentapi.KindOrder, key, key))
	}
	run(t, o)
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.checks) > 5
	}, 10*time.Second, time.Millisecond)
	assert.Empty(t, setApartKeys(t, o))
	assert.Equal(t, 3, o.left())

	f.set(func(f *fakeController) { f.outboxAnswer = unreadableBatches(http.StatusOK) })
	require.Eventually(t, func() bool { return len(f.written()) == 3 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, []string{"a", "b", "c"}, writtenKeys(f))
}

// fileSize returns the size of file.
func fileSize(t *testing.T, file string) int64 {
	t.Helper()
	info, err := os.Stat(file)
	require.NoError(t, err)
	return info.Size()
}

// R6: the file of the outbox is compacted as it opens: its records alone, the
// ones not sent and the ones set apart, nothing left of the ones sent; never
// by buntdb in the background, whose rewrite syncs neither the file nor its
// directory.
func TestOutbox_CompactedAsItOpens(t *testing.T) {
	file := filepath.Join(t.TempDir(), "agent.db")
	f := newFakeController(t)
	o, err := OpenOutbox(file, f.client())
	require.NoError(t, err)
	payload := strings.Repeat("x", 1024)
	for i := range 300 {
		require.NoError(t, o.Add(agentapi.KindJournal, "sent"+strings.Repeat("0", i%3), payload))
	}
	require.NoError(t, o.Flush(context.Background()))
	f.set(func(f *fakeController) {
		f.outboxAnswer = func(request agentapi.OutboxRequest) (int, any) {
			if request.Records[0].Key == "refused" {
				return http.StatusUnprocessableEntity, agentapi.OutboxError{Index: 0, Error: "invalid"}
			}
			return http.StatusServiceUnavailable, ui.ErrorResponse{Error: "down"}
		}
	})
	require.NoError(t, o.Add(agentapi.KindOrder, "refused", 1))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = o.Flush(ctx)
	require.NoError(t, o.Add(agentapi.KindOrder, "left", 2))
	require.NoError(t, o.Close())
	before := fileSize(t, file)

	again := openTestOutbox(t, f, file)
	after := fileSize(t, file)
	assert.Less(t, after, before/20, "the records sent left out")
	var config buntdb.Config
	require.NoError(t, again.db.ReadConfig(&config))
	assert.True(t, config.AutoShrinkDisabled, "never rewritten in the background")
	assert.NoFileExists(t, file+".tmp")
	pending, err := again.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "left", pending[0].Key)
	assert.Equal(t, []string{"refused"}, setApartKeys(t, again))
	require.NoError(t, again.Add(agentapi.KindOrder, "next", 3))
	pending, err = again.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	assert.Greater(t, pending[1].Seq, pending[0].Seq, "the sequence goes on")
}

// corruptMiddle makes the second record of the file of an outbox unreadable,
// the ones around it as they were.
func corruptMiddle(t *testing.T, file string) {
	t.Helper()
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	first := bytes.Index(data, []byte("*3\r\n"))
	second := bytes.Index(data[first+1:], []byte("*3\r\n"))
	require.Positive(t, second)
	data[first+1+second] = '?'
	require.NoError(t, os.WriteFile(file, data, 0o600))
}

// M10: an outbox the agent can't read tells which file and how to go on,
// rather than a bare "invalid database", and leaves it as it is.
func TestOutbox_UnreadableFileNamesItAndTheWayOut(t *testing.T) {
	file := filepath.Join(t.TempDir(), "llm-3.db")
	f := newFakeController(t)
	f.set(func(f *fakeController) { f.down = true })
	o, err := OpenOutbox(file, f.client())
	require.NoError(t, err)
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, o.Add(agentapi.KindOrder, key, key))
	}
	require.NoError(t, o.Close())
	corruptMiddle(t, file)
	data, err := os.ReadFile(file)
	require.NoError(t, err)

	_, err = OpenOutbox(file, f.client())
	require.Error(t, err)
	assert.ErrorContains(t, err, file)
	assert.ErrorContains(t, err, "outbox-quarantine")
	again, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, data, again, "left as it is")
	_, err = OpenOutbox(file, f.client())
	assert.NotContains(t, err.Error(), "held by another process", "unlocked")
}

// M10, R6: a file cut short, eg: the agent killed in the middle of a write,
// opens with the records before the cut, telling the bytes dropped.
func TestOutbox_TornTailOpens(t *testing.T) {
	file := filepath.Join(t.TempDir(), "agent.db")
	f := newFakeController(t)
	f.set(func(f *fakeController) { f.down = true })
	o, err := OpenOutbox(file, f.client())
	require.NoError(t, err)
	require.NoError(t, o.Add(agentapi.KindOrder, "a", 1))
	whole := fileSize(t, file)
	require.NoError(t, o.Add(agentapi.KindOrder, "b", 2))
	require.NoError(t, o.Close())
	require.NoError(t, os.Truncate(file, fileSize(t, file)-5))
	cut := fileSize(t, file) - whole // the record of b, but its last 5 bytes

	hook := logtest.NewGlobal()
	defer hook.Reset()
	again := openTestOutbox(t, f, file)
	pending, err := again.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "a", pending[0].Key)
	var warned []string
	for _, entry := range hook.AllEntries() {
		if entry.Level == logrus.WarnLevel && strings.Contains(entry.Message, file) {
			warned = append(warned, entry.Message)
		}
	}
	require.Len(t, warned, 1, "the cut told")
	assert.Contains(t, warned[0], "ended in the middle of a write")
	assert.Contains(t, warned[0], "its last "+strconv.FormatInt(cut, 10)+" bytes dropped")
}

// R7: the batches tell the controller what the outbox holds: the records not
// sent, the ones of the batch included, the ones set apart, and when each was
// added.
func TestOutbox_BatchesTellWhatTheOutboxHolds(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.outboxAnswer = func(request agentapi.OutboxRequest) (int, any) {
			if request.Records[0].Key == "refused" {
				return http.StatusUnprocessableEntity, agentapi.OutboxError{Index: 0, Error: "invalid"}
			}
			return http.StatusOK, nil
		}
	})
	o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
	before := time.Now()
	for _, key := range []string{"refused", "a", "b"} {
		require.NoError(t, o.Add(agentapi.KindOrder, key, key))
	}
	require.NoError(t, o.Flush(context.Background()))
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.outboxes, 2)
	assert.Equal(t, 3, f.outboxes[0].Pending)
	assert.Zero(t, f.outboxes[0].SetApart)
	assert.Equal(t, 2, f.outboxes[1].Pending, "the one refused set apart")
	assert.Equal(t, 1, f.outboxes[1].SetApart)
	for _, r := range f.outboxes[1].Records {
		assert.False(t, r.At.Before(before.Truncate(time.Second)), "when it was added")
	}
}

// R7: a record failing again and again isn't set apart while no record comes
// after it, nothing telling the controller writes others.
func TestOutbox_ARecordFailingAloneIsKeptWithoutOthers(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) { f.outboxAnswer = failingKey("b") })
	o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
	require.NoError(t, o.Add(agentapi.KindOrder, "b", "b"))
	run(t, o)
	start := time.Now()
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.outboxes) > 4*poisonAfter && time.Since(start) > 2*holdAfter
	}, 10*time.Second, time.Millisecond)
	assert.Empty(t, setApartKeys(t, o))
	assert.Empty(t, heldKeys(t, o))
	assert.Equal(t, 1, o.left())
}

// R7: a record the controller writes once it writes the records after it,
// tried once more at once after their check, is written, never set apart.
func TestOutbox_ARecordWrittenOnceTheOthersAreIsNeverSetApart(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.outboxAnswer = func(request agentapi.OutboxRequest) (int, any) {
			if len(f.checks) > 0 { // the controller back as the others are checked
				return http.StatusOK, nil
			}
			return failingKey("b")(request)
		}
	})
	o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, o.Add(agentapi.KindOrder, key, key))
	}
	run(t, o)
	require.Eventually(t, func() bool { return len(f.written()) == 3 }, 10*time.Second, time.Millisecond)
	assert.Equal(t, []string{"a", "b", "c"}, writtenKeys(f))
	assert.Empty(t, setApartKeys(t, o))
	assert.Empty(t, heldKeys(t, o))
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.checks)
	assert.Equal(t, "c", f.checks[0].Records[0].Key, "the records after it checked")
}

// outOfReach writes the records sent to it, but fails the batches of the
// record of key as out of reach, eg: the connection reset, never a
// StatusError; the records after it checked written.
type outOfReach struct {
	key              string
	outboxes, checks atomic.Int64
}

func (s *outOfReach) Outbox(_ context.Context, request agentapi.OutboxRequest) (uint64, error) {
	s.outboxes.Add(1)
	for _, record := range request.Records {
		if record.Key == s.key {
			return 0, errors.New("read tcp: connection reset by peer")
		}
	}
	return request.Records[len(request.Records)-1].Seq, nil
}

func (s *outOfReach) CheckOutbox(context.Context, []agentapi.Record) error {
	s.checks.Add(1)
	return nil
}

// R7: a record out of reach alone, tried once more at once after the check of
// the records after it, out of reach again, is never set apart and waits:
// send returns the failure, for Run to wait and warn, never trying it again
// and again at once.
func TestOutbox_ARecordOutOfReachAloneWaits(t *testing.T) {
	s := &outOfReach{key: "b"}
	o, err := OpenOutbox(filepath.Join(t.TempDir(), "agent.db"), s)
	require.NoError(t, err)
	t.Cleanup(func() { _ = o.Close() })
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, o.Add(agentapi.KindOrder, key, key))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	for s.checks.Load() < 3 {
		require.Less(t, time.Since(start), 5*time.Second, "the records after it never checked")
		before := s.outboxes.Load()
		done := make(chan error, 1)
		go func() {
			_, err := o.send(ctx)
			done <- err
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			cancel()
			<-done
			t.Fatalf("send never returned: %d batches sent", s.outboxes.Load()-before)
		}
		assert.LessOrEqual(t, s.outboxes.Load()-before, int64(2), "at most once more at once")
		time.Sleep(poisonFor / 5)
	}
	assert.Empty(t, setApartKeys(t, o))
	assert.Empty(t, heldKeys(t, o))
	pending, err := o.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	assert.Len(t, pending, 2, "b and the ones after it")
}

// failingDisk is the file of an outbox whose syncs fail, eg: an I/O error.
type failingDisk struct{ syncer }

func (failingDisk) Sync() error { return errors.New("input/output error") }

// R6: a record the file of the outbox fails to sync is an error of Add, never
// told on disk, sent all the same while the agent runs.
func TestOutbox_ARecordNotSyncedIsAnError(t *testing.T) {
	f := newFakeController(t)
	o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
	o.disk = failingDisk{o.disk}
	err := o.Add(agentapi.KindOrder, "1", 1)
	require.ErrorIs(t, err, errNotOnDisk)
	assert.ErrorContains(t, err, "input/output error")
	require.NoError(t, o.Flush(context.Background()))
	assert.Equal(t, []string{"1"}, writtenKeys(f), "sent all the same")
}

// R6: the file of the outbox, grown past its size, is compacted while the
// agent runs, durably, the records added meanwhile kept, in order.
func TestOutbox_CompactedAsItGrows(t *testing.T) {
	size := compactMinSize
	t.Cleanup(func() { compactMinSize = size }) // once the outbox stopped
	compactMinSize = 64 << 10
	file := filepath.Join(t.TempDir(), "agent.db")
	f := newFakeController(t)
	o := openTestOutbox(t, f, file)
	run(t, o)
	payload := strings.Repeat("x", 1024)

	// added faster than sent, as the outbox is compacted
	const burst = 300
	for i := range burst {
		require.NoError(t, o.Add(agentapi.KindJournal, strconv.Itoa(i), payload))
	}
	require.Eventually(t, func() bool { return len(f.written()) == burst }, 10*time.Second, time.Millisecond)
	for i, key := range writtenKeys(f) {
		require.Equal(t, strconv.Itoa(i), key, "in order")
	}

	// each one sent before the next: once twice the size of its last
	// compaction, the file holds a record alone, compacted again past its size
	for i := burst; i < 2*burst; i++ {
		require.NoError(t, o.Add(agentapi.KindJournal, strconv.Itoa(i), payload))
		require.Eventually(t, func() bool { return o.left() == 0 }, 5*time.Second, time.Millisecond)
	}
	assert.Less(t, fileSize(t, file), compactMinSize+4<<10, "compacted as it grew")
	assert.NoFileExists(t, file+".tmp")
	require.NoError(t, o.Close())
	again := openTestOutbox(t, f, file)
	assert.Zero(t, again.left())
	assert.Len(t, f.written(), 2*burst)
}

// M10: an agent given the param outbox-quarantine moves an outbox it can't
// read aside, kept for a look, and starts with an empty one.
func TestAgent_OpenQuarantinesAnOutboxItCantRead(t *testing.T) {
	dir := t.TempDir()
	f := newFakeController(t)
	f.set(func(f *fakeController) { f.down = true })
	a := &Agent{Config: testConfig(), client: f.client()}
	require.NoError(t, a.Open(dir))
	require.NoError(t, a.outbox.Add(agentapi.KindOrder, "a", 1))
	require.NoError(t, a.outbox.Add(agentapi.KindOrder, "b", 2))
	require.NoError(t, a.outbox.Close())
	file := filepath.Join(dir, "llm-1.db")
	corruptMiddle(t, file)
	data, err := os.ReadFile(file)
	require.NoError(t, err)

	err = (&Agent{Config: testConfig(), client: f.client()}).Open(dir)
	require.ErrorIs(t, err, ErrOutboxCorrupt)

	quarantined := &Agent{Config: testConfig(), client: f.client(), QuarantineOutbox: true}
	require.NoError(t, quarantined.Open(dir))
	t.Cleanup(func() { _ = quarantined.outbox.Close() })
	assert.Zero(t, quarantined.outbox.left(), "started empty")
	aside, err := filepath.Glob(file + ".corrupt-*")
	require.NoError(t, err)
	require.Len(t, aside, 1)
	kept, err := os.ReadFile(aside[0])
	require.NoError(t, err)
	assert.Equal(t, data, kept, "kept as it was")
}

// unreadableRecord writes the outbox of file with the records a and b, the
// one of b not JSON, as a file written over inside a record would.
func unreadableRecord(t *testing.T, file string) {
	t.Helper()
	db, err := buntdb.Open(file)
	require.NoError(t, err)
	require.NoError(t, db.Update(func(tx *buntdb.Tx) error {
		_, _, err := tx.Set(recordKey(pendingPrefix, 1), `{"seq":1,"kind":"order","key":"a","payload":1}`, nil)
		if err == nil {
			_, _, err = tx.Set(recordKey(pendingPrefix, 2), `{"seq":2,"kind":"ord?r","k`, nil)
		}
		return err
	}))
	require.NoError(t, db.Close())
}

// M10: a record of the outbox the agent can't read fails the start, naming
// it; with outbox-quarantine, that record alone is set apart, the others sent.
func TestOutbox_ARecordItCantReadSetApartWithQuarantine(t *testing.T) {
	file := filepath.Join(t.TempDir(), "agent.db")
	unreadableRecord(t, file)
	f := newFakeController(t)

	_, err := OpenOutbox(file, f.client())
	require.ErrorIs(t, err, ErrOutboxCorrupt)
	assert.ErrorContains(t, err, recordKey(pendingPrefix, 2))

	o, err := openOutboxFile(file, f.client(), true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = o.Close() })
	assert.Equal(t, 1, o.left())
	require.NoError(t, o.Flush(context.Background()))
	assert.Equal(t, []string{"a"}, writtenKeys(f))
	var dead []string
	require.NoError(t, o.db.View(func(tx *buntdb.Tx) error {
		return tx.AscendKeys(deadPrefix+"*", func(key, _ string) bool {
			dead = append(dead, key)
			return true
		})
	}))
	assert.Equal(t, []string{recordKey(deadPrefix, 2)}, dead)
}

// failingKind answers 503 to the requests holding a record of kind, as the
// controller does to the records it fails to write for another reason than
// their data, eg: a fault of its own with the orders: nothing written.
func failingKind(kind string) func(agentapi.OutboxRequest) (int, any) {
	return func(request agentapi.OutboxRequest) (int, any) {
		for _, record := range request.Records {
			if record.Kind == kind {
				return http.StatusServiceUnavailable, ui.ErrorResponse{Error: "the records can't be written now"}
			}
		}
		return http.StatusOK, nil
	}
}

// R7: while the controller fails the records of a kind, eg: every order, an
// order failing alone is never held, however long it lasts, the records of
// other kinds after it written when checked (eg: the journal): only the next
// ones of its kind prove the controller writes them. Once it writes the
// orders again, all of them are, in order.
func TestOutbox_NoRecordHeldWhileTheControllerFailsItsKind(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) { f.outboxAnswer = failingKind(agentapi.KindOrder) })
	o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
	keys := []string{"a"}
	require.NoError(t, o.Add(agentapi.KindOrder, "a", "a"))
	for i := range checkRecords + 2 {
		key := "j" + strconv.Itoa(i)
		keys = append(keys, key)
		require.NoError(t, o.Add(agentapi.KindJournal, key, key))
	}
	require.NoError(t, o.Add(agentapi.KindOrder, "b", "b"))
	keys = append(keys, "b")
	run(t, o)
	start := time.Now()
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.outboxes) > 6*poisonAfter && time.Since(start) > 2*holdAfter
	}, 10*time.Second, time.Millisecond)
	assert.Empty(t, heldKeys(t, o))
	assert.Empty(t, setApartKeys(t, o))
	assert.Equal(t, len(keys), o.left(), "the queue waits for the orders")
	f.mu.Lock()
	assert.NotEmpty(t, f.checks)
	for _, check := range f.checks {
		for _, record := range check.Records {
			assert.Equal(t, agentapi.KindOrder, record.Kind, "the next ones of its kind checked")
		}
	}
	f.mu.Unlock()

	f.set(func(f *fakeController) { f.outboxAnswer = nil })
	require.Eventually(t, func() bool { return len(f.written()) == len(keys) }, 5*time.Second, time.Millisecond)
	assert.Equal(t, keys, writtenKeys(f))
}

// R7: a record the controller fails for a while, the records of its kind
// after it written, is never held before it failed for holdAfter, whatever
// the proofs against it: once the controller writes it again, it is written,
// in order. The controller fails it until it checked the records after it
// poisonAfter+1 times, the outbox holding poisonAfter proofs against it then,
// rather than for a time, which a busy machine (the whole suite under -race)
// could spend before the first check; holdAfter long enough for them.
func TestOutbox_ARecordFailingForAWhileIsNeverHeld(t *testing.T) {
	hold := holdAfter
	holdAfter = time.Minute
	t.Cleanup(func() { holdAfter = hold }) // once the outbox stopped: the cleanups run last in first
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.outboxAnswer = func(request agentapi.OutboxRequest) (int, any) {
			if len(f.checks) <= poisonAfter { // f.mu held: the checks answered so far
				return failingKey("b")(request)
			}
			return http.StatusOK, nil
		}
	})
	o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, o.Add(agentapi.KindOrder, key, key))
	}
	run(t, o)
	require.Eventually(t, func() bool { return o.left() == 0 }, 10*time.Second, time.Millisecond)
	assert.Equal(t, []string{"a", "b", "c"}, writtenKeys(f))
	assert.Empty(t, heldKeys(t, o))
	assert.Empty(t, setApartKeys(t, o))
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Greater(t, len(f.checks), poisonAfter, "the records after it checked meanwhile, a proof against it after each")
}

// R7: a record failing alone while the database of the controller fails now
// and then, the records of its kind after it written at one check and not at
// the next, is never held: a check failing starts its proofs again.
func TestOutbox_NoRecordHeldWhileTheDatabaseFailsNowAndThen(t *testing.T) {
	f := newFakeController(t)
	others := 0
	f.set(func(f *fakeController) {
		f.outboxAnswer = func(request agentapi.OutboxRequest) (int, any) {
			if status, body := failingKey("b")(request); status != http.StatusOK {
				return status, body
			}
			if others++; others%2 == 0 {
				return http.StatusServiceUnavailable, ui.ErrorResponse{Error: "the records can't be written now"}
			}
			return http.StatusOK, nil
		}
	})
	o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, o.Add(agentapi.KindOrder, key, key))
	}
	run(t, o)
	time.Sleep(3 * holdAfter)
	assert.Empty(t, heldKeys(t, o))
	assert.Empty(t, setApartKeys(t, o))
	assert.Equal(t, []string{"a"}, writtenKeys(f))
	assert.Equal(t, 2, o.left())
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Greater(t, len(f.checks), 2*poisonAfter, "checked again and again")
}

// holdB runs an outbox of the orders a, b and c whose b the controller fails
// alone, until b is held apart; and returns the outbox, sending.
func holdB(t *testing.T, f *fakeController) *Outbox {
	t.Helper()
	f.set(func(f *fakeController) { f.outboxAnswer = failingKey("b") })
	o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, o.Add(agentapi.KindOrder, key, key))
	}
	run(t, o)
	require.Eventually(t, func() bool { return len(f.written()) == 2 && o.left() == 0 }, 10*time.Second,
		time.Millisecond)
	require.Equal(t, []string{"b"}, heldKeys(t, o))
	return o
}

// R7: a record held apart is sent again alone once the controller wrote a
// record of its kind since and heldRetry passed: written, it leaves the
// outbox, and the dashboard counts it no more.
func TestOutbox_ARecordHeldIsSentAgainOnceItsKindIsWritten(t *testing.T) {
	f := newFakeController(t)
	o := holdB(t, f)
	f.set(func(f *fakeController) { f.outboxAnswer = nil }) // fixed
	time.Sleep(heldRetry)
	require.NoError(t, o.Add(agentapi.KindOrder, "d", "d"))
	require.Eventually(t, func() bool { return len(f.written()) == 4 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, []string{"a", "c", "b", "d"}, writtenKeys(f), "before the next batch")
	assert.Empty(t, heldKeys(t, o))
	f.mu.Lock()
	defer f.mu.Unlock()
	retry, last := f.outboxes[len(f.outboxes)-2], f.outboxes[len(f.outboxes)-1]
	require.Len(t, retry.Records, 1)
	assert.Equal(t, "b", retry.Records[0].Key)
	assert.Equal(t, 2, retry.Pending, "itself and d, not sent")
	assert.Zero(t, retry.Held)
	assert.Zero(t, last.Held)
}

// R7: a record held apart the controller fails again is sent again later,
// twice as late each time, never in the way of the next records; refused for
// good, it is set apart.
func TestOutbox_ARecordHeldFailingAgainWaitsThenRefusedIsSetApart(t *testing.T) {
	f := newFakeController(t)
	o := holdB(t, f)
	tries := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, request := range f.outboxes {
			if len(request.Records) == 1 && request.Records[0].Key == "b" {
				n++
			}
		}
		return n
	}
	before := tries()
	for i := range 6 {
		require.NoError(t, o.Add(agentapi.KindOrder, "n"+strconv.Itoa(i), i))
		time.Sleep(heldRetry / 5)
	}
	require.Eventually(t, func() bool { return o.left() == 0 }, 5*time.Second, time.Millisecond)
	assert.LessOrEqual(t, tries()-before, 1, "once in heldRetry at most")
	assert.Equal(t, []string{"b"}, heldKeys(t, o))

	f.set(func(f *fakeController) {
		f.outboxAnswer = func(request agentapi.OutboxRequest) (int, any) {
			if request.Records[0].Key == "b" {
				return http.StatusUnprocessableEntity, agentapi.OutboxError{Index: 0, Error: "invalid"}
			}
			return http.StatusOK, nil
		}
	})
	time.Sleep(heldRetryMax)
	require.NoError(t, o.Add(agentapi.KindOrder, "z", "z"))
	require.Eventually(t, func() bool { return len(setApartKeys(t, o)) == 1 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, []string{"b"}, setApartKeys(t, o))
	assert.Empty(t, heldKeys(t, o))
	require.Eventually(t, func() bool { return o.left() == 0 }, 5*time.Second, time.Millisecond)
	assert.NotContains(t, writtenKeys(f), "b")
}

// R7: a record held apart, eg: an older state of an order, is dropped once a
// later record of the same kind and key is written, never sent again over it.
func TestOutbox_ARecordHeldSupersededIsDropped(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.outboxAnswer = func(request agentapi.OutboxRequest) (int, any) {
			for _, record := range request.Records {
				if record.Key == "7" && string(record.Payload) == `"filled"` {
					return http.StatusServiceUnavailable, ui.ErrorResponse{Error: "the records can't be written now"}
				}
			}
			return http.StatusOK, nil
		}
	})
	o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
	require.NoError(t, o.Add(agentapi.KindOrder, "7", "filled"))
	require.NoError(t, o.Add(agentapi.KindOrder, "8", "new"))
	run(t, o)
	require.Eventually(t, func() bool { return o.left() == 0 }, 10*time.Second, time.Millisecond)
	require.Equal(t, []string{"7"}, heldKeys(t, o))

	require.NoError(t, o.Add(agentapi.KindOrder, "7", "profit"))
	require.Eventually(t, func() bool { return len(heldKeys(t, o)) == 0 }, 5*time.Second, time.Millisecond)
	f.set(func(f *fakeController) { f.outboxAnswer = nil })
	time.Sleep(heldRetry)
	require.NoError(t, o.Add(agentapi.KindOrder, "9", "new"))
	require.Eventually(t, func() bool { return o.left() == 0 }, 5*time.Second, time.Millisecond)
	var payloads []string
	for _, record := range f.written() {
		payloads = append(payloads, record.Key+" "+string(record.Payload))
	}
	assert.Equal(t, []string{`8 "new"`, `7 "profit"`, `9 "new"`}, payloads, "never written over its later one")
}

// R7: the batches tell the controller when the first record left after them
// was added, for the age of the records not written; none after the last.
func TestOutbox_BatchesTellWhenTheFirstRecordLeftWasAdded(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) { f.outboxAnswer = unreadableBatches(http.StatusOK) })
	o := openTestOutbox(t, f, filepath.Join(t.TempDir(), "agent.db"))
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, o.Add(agentapi.KindOrder, key, key))
		time.Sleep(2 * time.Millisecond)
	}
	added := map[string]time.Time{}
	pending, err := o.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	for _, record := range pending {
		added[record.Key] = record.At
	}
	require.NoError(t, o.Flush(context.Background()))
	f.mu.Lock()
	defer f.mu.Unlock()
	next := map[string]time.Time{}
	for _, request := range f.outboxes {
		if len(request.Records) == 1 { // the ones written
			next[request.Records[0].Key] = request.NextAt
		}
	}
	require.Len(t, next, 3)
	assert.True(t, next["a"].Equal(added["b"]), "a then b")
	assert.True(t, next["b"].Equal(added["c"]), "b then c")
	assert.True(t, next["c"].IsZero(), "none after c")
}

// R6: the file of an outbox holding records not sent is never compacted, its
// records then copied as the agent adds others: only once they are sent.
func TestOutbox_NeverCompactedWithRecordsPending(t *testing.T) {
	size := compactMinSize
	t.Cleanup(func() { compactMinSize = size })
	compactMinSize = 16 << 10
	file := filepath.Join(t.TempDir(), "agent.db")
	f := newFakeController(t)
	f.set(func(f *fakeController) { f.down = true })
	o := openTestOutbox(t, f, file)
	payload := strings.Repeat("x", 1024)
	for i := range 40 {
		require.NoError(t, o.Add(agentapi.KindJournal, strconv.Itoa(i), payload))
	}
	before, err := os.Stat(file)
	require.NoError(t, err)
	require.Greater(t, before.Size(), compactMinSize)

	o.compact()
	after, err := os.Stat(file)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "never rewritten")
	assert.Equal(t, before.Size(), after.Size())

	f.set(func(f *fakeController) { f.down = false })
	require.NoError(t, o.Flush(context.Background()))
	o.compact()
	after, err = os.Stat(file)
	require.NoError(t, err)
	assert.False(t, os.SameFile(before, after), "rewritten once sent")
	assert.Less(t, after.Size(), compactMinSize)
}

// R6: a compaction takes the records added while it wrote the copy of the
// others, in order, and the ones set apart; the sequence goes on.
func TestOutbox_CompactionKeepsTheRecordsAddedMeanwhile(t *testing.T) {
	size := compactMinSize
	t.Cleanup(func() { compactMinSize, compactCopied = size, func() {} })
	compactMinSize = 16 << 10
	file := filepath.Join(t.TempDir(), "agent.db")
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.outboxAnswer = func(request agentapi.OutboxRequest) (int, any) {
			if request.Records[0].Key == "refused" {
				return http.StatusUnprocessableEntity, agentapi.OutboxError{Index: 0, Error: "invalid"}
			}
			return http.StatusOK, nil
		}
	})
	o := openTestOutbox(t, f, file)
	payload := strings.Repeat("x", 1024)
	require.NoError(t, o.Add(agentapi.KindOrder, "refused", 1))
	for i := range 40 {
		require.NoError(t, o.Add(agentapi.KindJournal, strconv.Itoa(i), payload))
	}
	require.NoError(t, o.Flush(context.Background()))
	f.set(func(f *fakeController) { f.down = true })
	compactCopied = func() {
		for _, key := range []string{"x", "y"} {
			require.NoError(t, o.Add(agentapi.KindOrder, key, key)) // never waiting for the compaction
		}
	}
	before := fileSize(t, file)
	o.compact()
	assert.Less(t, fileSize(t, file), before/4, "compacted")
	assert.NoFileExists(t, file+".tmp")
	check := func(o *Outbox) {
		pending, err := o.Pending(agentapi.KindOrder)
		require.NoError(t, err)
		require.Len(t, pending, 2)
		assert.Equal(t, "x", pending[0].Key)
		assert.Equal(t, "y", pending[1].Key)
		assert.Equal(t, []string{"refused"}, setApartKeys(t, o))
	}
	check(o)
	require.NoError(t, o.Add(agentapi.KindOrder, "z", "z"), "added after it")
	require.NoError(t, o.Close())

	compactCopied = func() {}
	again := openTestOutbox(t, f, file)
	pending, err := again.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	require.Len(t, pending, 3)
	assert.Equal(t, "z", pending[2].Key)
	assert.Greater(t, pending[2].Seq, pending[1].Seq)
	assert.Equal(t, []string{"refused"}, setApartKeys(t, again))
}

// R7: an order held apart, never written by the controller, is still one of
// the agent as it starts again: its state kept, its ID never numbered again,
// even past the lease (numbered while the controller was out of reach).
func TestOrders_AnOrderHeldApartIsKeptAsTheAgentStarts(t *testing.T) {
	f := newFakeController(t)
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	filled := model.Order{ID: 1005, Pair: "XAUUSDm", Side: model.SideTypeBuy, Type: model.OrderTypeLimit,
		Status: model.OrderStatusTypeFilled, Price: 2000, Quantity: 1, CreatedAt: at, UpdatedAt: at}
	file := filepath.Join(t.TempDir(), "agent.db")
	payload, err := json.Marshal(filled)
	require.NoError(t, err)
	record, err := json.Marshal(agentapi.Record{Seq: 1, Kind: agentapi.KindOrder, Key: "1005", At: at,
		Payload: payload})
	require.NoError(t, err)
	db, err := buntdb.Open(file)
	require.NoError(t, err)
	require.NoError(t, db.Update(func(tx *buntdb.Tx) error {
		_, _, err := tx.Set(recordKey(heldPrefix, 1), string(record), nil)
		return err
	}))
	require.NoError(t, db.Close())

	outbox := openTestOutbox(t, f, file)
	orders, err := NewOrders(context.Background(), f.client(), outbox)
	require.NoError(t, err)
	t.Cleanup(func() { _ = orders.Close() })
	kept, err := orders.Orders()
	require.NoError(t, err)
	require.Len(t, kept, 1)
	assert.Equal(t, filled.ID, kept[0].ID)
	assert.Equal(t, model.OrderStatusTypeFilled, kept[0].Status, "the state held")
	next := model.Order{Pair: "XAUUSDm", Side: model.SideTypeSell, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Quantity: 1, CreatedAt: at, UpdatedAt: at}
	require.NoError(t, orders.CreateOrder(&next))
	assert.EqualValues(t, 1006, next.ID, "past the order held")
}
