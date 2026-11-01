package entryfilter

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/raykavin/xtrader/agents/metalabel"
	"github.com/raykavin/xtrader/tools/log"
)

// recorder appends the samples labeled to the JSON lines file of
// -meta-record, a write each (appended at once, so two runs writing the same
// file, eg: the fit and the validation of a backtest, interleave whole lines),
// the file opened on the first one only (never by -print-warmup), its folder
// made. A file that can't be written is told once, its samples dropped: the
// strategy trades on.
type recorder struct {
	path string

	mu     sync.Mutex
	file   *os.File
	failed bool
}

// write appends s.
func (r *recorder) write(s metalabel.Sample) {
	line, err := s.MarshalLine()
	if err != nil {
		log.Errorf("[FILTER] the sample of %s at %d not written: %v", s.Pair, s.Time, err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed {
		return
	}
	if r.file == nil {
		if err := os.MkdirAll(filepath.Dir(r.path), 0o750); err != nil {
			r.fail(err)
			return
		}
		file, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
		if err != nil {
			r.fail(err)
			return
		}
		r.file = file
	}
	if _, err := r.file.Write(line); err != nil {
		r.fail(err)
	}
}

// fail tells once that the file can't be written.
func (r *recorder) fail(err error) {
	if !r.failed {
		log.Errorf("[FILTER] the samples of -meta-record not written to %s: %v", r.path, err)
	}
	r.failed = true
}

// close closes the file, if open.
func (r *recorder) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}
