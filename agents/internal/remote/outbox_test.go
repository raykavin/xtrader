package remote

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
)

// scriptedSender answers the batches of an outbox with its answers in turn,
// then writes them all, recording what it is sent.
type scriptedSender struct {
	mu      sync.Mutex
	answers []error
	batches [][]agentapi.Record
	written []agentapi.Record
}

func (s *scriptedSender) Outbox(_ context.Context, records []agentapi.Record) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, records)
	if len(s.answers) > 0 {
		err := s.answers[0]
		s.answers = s.answers[1:]
		if err != nil {
			return 0, err
		}
	}
	s.written = append(s.written, records...)
	return records[len(records)-1].Seq, nil
}

func (s *scriptedSender) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for _, r := range s.written {
		keys = append(keys, r.Key)
	}
	return keys
}

// run runs the outbox until the test ends, returning what Run returned.
func run(t *testing.T, o *Outbox) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	result, finished := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(finished)
		result <- o.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-finished
	})
	return result
}

func TestOutbox_SendsInOrderWhateverFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "outbox", "llm.db")
	s := &scriptedSender{answers: []error{
		&StatusError{Status: http.StatusServiceUnavailable, Message: "down"}, // retried
		&RefusedError{Index: 1, Message: "invalid"},                          // b set apart
	}}
	o, err := OpenOutbox(file, s)
	require.NoError(t, err)
	defer func() { _ = o.Close() }()
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, o.Add(agentapi.KindOrder, key, map[string]string{"key": key}))
	}
	run(t, o)
	require.Eventually(t, func() bool { return len(s.keys()) == 2 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, []string{"a", "c"}, s.keys(), "in order, the one refused set apart")

	pending, err := o.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	assert.Empty(t, pending)
	require.NoError(t, o.Add(agentapi.KindJournal, "d", 1))
	require.Eventually(t, func() bool { return len(s.keys()) == 3 }, 5*time.Second, time.Millisecond,
		"woken by the record added")
}

// A batch the controller can't read whole is sent one record at a time, the
// one it can't read set apart.
func TestOutbox_UnreadableBatch(t *testing.T) {
	s := &scriptedSender{answers: []error{
		&StatusError{Status: http.StatusBadRequest, Message: "invalid JSON payload"},
		&StatusError{Status: http.StatusRequestEntityTooLarge, Message: "too large"},
	}}
	o, err := OpenOutbox(filepath.Join(t.TempDir(), "llm.db"), s)
	require.NoError(t, err)
	defer func() { _ = o.Close() }()
	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, o.Add(agentapi.KindDecision, key, key))
	}
	run(t, o)
	require.Eventually(t, func() bool { return len(s.keys()) == 2 }, 5*time.Second, time.Millisecond)
	assert.Equal(t, []string{"b", "c"}, s.keys())
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Len(t, s.batches[0], 3)
	assert.Len(t, s.batches[1], 1, "a alone")
}

func TestOutbox_RefusedAgentStops(t *testing.T) {
	s := &scriptedSender{answers: []error{ErrUnauthorized}}
	o, err := OpenOutbox(filepath.Join(t.TempDir(), "llm.db"), s)
	require.NoError(t, err)
	defer func() { _ = o.Close() }()
	require.NoError(t, o.Add(agentapi.KindOrder, "1", 1))
	select {
	case err := <-run(t, o):
		require.ErrorIs(t, err, ErrUnauthorized)
	case <-time.After(5 * time.Second):
		t.Fatal("still running")
	}
	pending, err := o.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	assert.Len(t, pending, 1, "kept for the next start")
}

// The records outlive the process, the file held by one process at a time.
func TestOutbox_OutlivesTheProcess(t *testing.T) {
	file := filepath.Join(t.TempDir(), "llm.db")
	down := errors.New("connection refused")
	s := &scriptedSender{answers: []error{down, down, down, down, down, down, down, down}}
	o, err := OpenOutbox(file, s)
	require.NoError(t, err)
	require.NoError(t, o.Add(agentapi.KindOrder, "1", map[string]int{"id": 1}))
	require.NoError(t, o.Add(agentapi.KindDecision, "d1", "decision"))

	_, err = OpenOutbox(file, s)
	require.ErrorContains(t, err, "held by another process of the agent")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	require.ErrorContains(t, o.Flush(ctx), "not sent whole")
	require.NoError(t, o.Close())

	again, err := OpenOutbox(file, &scriptedSender{})
	require.NoError(t, err)
	defer func() { _ = again.Close() }()
	orders, err := again.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	require.Len(t, orders, 1)
	assert.JSONEq(t, `{"id": 1}`, string(orders[0].Payload))
	require.NoError(t, again.Add(agentapi.KindOrder, "2", 2))
	orders, err = again.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	require.Len(t, orders, 2)
	assert.Greater(t, orders[1].Seq, orders[0].Seq, "the sequence goes on")

	sender := again.client.(*scriptedSender)
	require.NoError(t, again.Flush(context.Background()))
	assert.Equal(t, []string{"1", "d1", "2"}, sender.keys())
}
