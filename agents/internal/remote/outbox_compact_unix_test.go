//go:build unix

package remote

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
)

// R6: a compaction never holds Add while it writes the copy of the records:
// the copy stalled (written to a pipe nobody reads yet, larger than its
// buffer), a record is added at once, kept in order.
func TestOutbox_AddNeverWaitsForTheCopyOfACompaction(t *testing.T) {
	size := compactMinSize
	t.Cleanup(func() { compactMinSize = size })
	compactMinSize = 64 << 10
	file := filepath.Join(t.TempDir(), "agent.db")
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.outboxAnswer = func(request agentapi.OutboxRequest) (int, any) {
			if request.Records[0].Key == "large" {
				return http.StatusUnprocessableEntity, agentapi.OutboxError{Index: 0, Error: "invalid"}
			}
			return http.StatusOK, nil
		}
	})
	o := openTestOutbox(t, f, file)
	// set apart, the copy of the records holds it: past the buffer of a pipe
	require.NoError(t, o.Add(agentapi.KindJournal, "large", strings.Repeat("x", 512<<10)))
	require.NoError(t, o.Flush(context.Background()))
	require.Greater(t, fileSize(t, file), compactMinSize)
	require.NoError(t, syscall.Mkfifo(file+".tmp", 0o600))

	compacted := make(chan struct{})
	go func() {
		defer close(compacted)
		o.compact()
	}()
	copying := make(chan *os.File, 1)
	go func() {
		pipe, err := os.Open(file + ".tmp") // once the compaction opened the copy
		if err == nil {
			_, err = pipe.Read(make([]byte, 1))
		}
		if err != nil {
			close(copying)
			return
		}
		copying <- pipe
	}()
	var pipe *os.File
	select {
	case pipe = <-copying:
		require.NotNil(t, pipe, "the copy never written")
	case <-time.After(5 * time.Second):
		t.Fatal("the compaction never wrote its copy")
	}
	added := make(chan error, 1)
	go func() { added <- o.Add(agentapi.KindOrder, "1", 1) }()
	select {
	case err := <-added:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Error("Add waits for the copy of the compaction")
	}
	_, _ = io.Copy(io.Discard, pipe) // the rest of the copy, then a pipe fails its sync
	_ = pipe.Close()
	select {
	case <-compacted:
	case <-time.After(5 * time.Second):
		t.Fatal("the compaction never ended")
	}
	if t.Failed() {
		<-added
	}
	assert.NoFileExists(t, file+".tmp")
	pending, err := o.Pending(agentapi.KindOrder)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "1", pending[0].Key)
	require.NoError(t, o.Flush(context.Background()))
	assert.Equal(t, "1", writtenKeys(f)[len(writtenKeys(f))-1])
}
