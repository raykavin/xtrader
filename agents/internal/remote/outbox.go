package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/buntdb"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/tools/log"
)

// The keys of the records of the outbox: the ones to send, and the ones the
// controller refused for good, kept apart for a look, by their Seq.
const (
	pendingPrefix = "r:"
	deadPrefix    = "d:"
)

// The limits of a batch of the outbox, one record at least.
const (
	batchRecords = 200
	batchBytes   = 16 << 20
)

// The waits after a failure to reach the controller, doubled up to the max,
// and how often a controller out of reach is warned about.
var (
	retryDelay    = time.Second
	retryMaxDelay = 30 * time.Second
	warnEvery     = 5 * time.Minute
)

// sender writes the records of an outbox, see Client.Outbox.
type sender interface {
	Outbox(ctx context.Context, records []agentapi.Record) (uint64, error)
}

// Outbox is the queue of the records an agent keeps through the controller:
// each written to a file of its own before Add returns, in order, then sent to
// the controller in batches as it answers (see Run), so the agent neither
// waits for the controller nor loses a record while it is out of reach. A
// record the controller refuses for good is set apart, the next ones sent.
type Outbox struct {
	db     *buntdb.DB
	lock   *os.File
	client sender

	mu  sync.Mutex
	seq uint64 // of the last record added

	wake    chan struct{} // a record added
	single  bool          // the next batch holds one record: the last one was refused whole
	failing time.Time     // since when the records don't reach the controller, zero when they do
	warned  time.Time
}

// OpenOutbox opens the outbox of file, eg: data/outbox/llm-3.db, its records
// sent with client, locked for this process: another process of the agent,
// eg: left running by a controller before, holds it.
func OpenOutbox(file string, client sender) (*Outbox, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, fmt.Errorf("remote: the outbox: %w", err)
	}
	locked, err := lock(file + ".lock")
	if err != nil {
		return nil, fmt.Errorf("remote: the outbox: %w", err)
	}
	db, err := buntdb.Open(file)
	if err != nil {
		_ = unlock(locked)
		return nil, fmt.Errorf("remote: the outbox %s: %w", file, err)
	}
	var config buntdb.Config
	err = db.ReadConfig(&config)
	if err == nil {
		config.SyncPolicy = buntdb.Always // on disk before Add returns
		err = db.SetConfig(config)
	}
	if err != nil {
		_ = db.Close()
		_ = unlock(locked)
		return nil, fmt.Errorf("remote: the outbox %s: %w", file, err)
	}

	o := &Outbox{db: db, lock: locked, client: client, wake: make(chan struct{}, 1)}
	pending := 0
	err = db.View(func(tx *buntdb.Tx) error {
		return tx.AscendKeys("*", func(key, _ string) bool {
			prefix, seq, ok := parseKey(key)
			if ok {
				o.seq = max(o.seq, seq)
				if prefix == pendingPrefix {
					pending++
				}
			}
			return true
		})
	})
	if err != nil {
		_ = o.Close()
		return nil, fmt.Errorf("remote: the outbox %s: %w", file, err)
	}
	if pending > 0 {
		log.Infof("[SETUP] the outbox %s holds %d records not sent yet", file, pending)
	}
	return o, nil
}

// recordKey returns the key of the record seq under prefix, sorting as the
// records came.
func recordKey(prefix string, seq uint64) string {
	return fmt.Sprintf("%s%020d", prefix, seq)
}

// parseKey returns the prefix and the Seq of the key of a record.
func parseKey(key string) (string, uint64, bool) {
	for _, prefix := range []string{pendingPrefix, deadPrefix} {
		if rest, ok := strings.CutPrefix(key, prefix); ok {
			seq, err := strconv.ParseUint(rest, 10, 64)
			return prefix, seq, err == nil
		}
	}
	return "", 0, false
}

// Add writes the record of kind and key, of the JSON of payload, at the end of
// the outbox, on disk when it returns, and wakes the sender.
func (o *Outbox) Add(kind, key string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("remote: the %s record %s: %w", kind, key, err)
	}
	o.mu.Lock()
	o.seq++
	record := agentapi.Record{Seq: o.seq, Kind: kind, Key: key, Payload: data}
	encoded, err := json.Marshal(record)
	if err == nil {
		err = o.db.Update(func(tx *buntdb.Tx) error {
			_, _, err := tx.Set(recordKey(pendingPrefix, record.Seq), string(encoded), nil)
			return err
		})
	}
	o.mu.Unlock()
	if err != nil {
		return fmt.Errorf("remote: the outbox: the %s record %s: %w", kind, key, err)
	}
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return nil
}

// Pending returns the records of kind not sent yet, in order, eg: the orders
// of a run before that didn't reach the controller.
func (o *Outbox) Pending(kind string) ([]agentapi.Record, error) {
	var records []agentapi.Record
	var failed error
	err := o.db.View(func(tx *buntdb.Tx) error {
		return tx.AscendKeys(pendingPrefix+"*", func(key, value string) bool {
			var record agentapi.Record
			if err := json.Unmarshal([]byte(value), &record); err != nil {
				failed = fmt.Errorf("remote: the outbox: %s: %w", key, err)
				return false
			}
			if record.Kind == kind {
				records = append(records, record)
			}
			return true
		})
	})
	if err == nil {
		err = failed
	}
	return records, err
}

// batch returns the first records to send, limit and batchBytes at most, one
// at least.
func (o *Outbox) batch(limit int) ([]agentapi.Record, error) {
	var records []agentapi.Record
	size := 0
	var failed error
	err := o.db.View(func(tx *buntdb.Tx) error {
		return tx.AscendKeys(pendingPrefix+"*", func(key, value string) bool {
			if len(records) > 0 && size+len(value) > batchBytes {
				return false
			}
			var record agentapi.Record
			if err := json.Unmarshal([]byte(value), &record); err != nil {
				failed = fmt.Errorf("remote: the outbox: %s: %w", key, err)
				return false
			}
			records = append(records, record)
			size += len(value)
			return len(records) < limit
		})
	})
	if err == nil {
		err = failed
	}
	return records, err
}

// Run sends the records to the controller as they come, until ctx is done:
// after a failure, the same batch again after a wait. It returns
// ErrUnauthorized when the controller refuses the agent.
func (o *Outbox) Run(ctx context.Context) error {
	delay := retryDelay
	for {
		sent, err := o.send(ctx)
		switch {
		case errors.Is(err, ErrUnauthorized):
			return err
		case err != nil && ctx.Err() != nil:
			return nil
		case err != nil:
			o.failed(err)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil
			}
			delay = min(2*delay, retryMaxDelay)
			continue
		}
		delay = retryDelay
		if sent > 0 {
			continue
		}
		select {
		case <-o.wake:
		case <-ctx.Done():
			return nil
		}
	}
}

// Flush sends the records left until none is, the controller refusing the
// agent, or ctx done, eg: as the agent stops. The records it couldn't send are
// sent by the next start.
func (o *Outbox) Flush(ctx context.Context) error {
	delay := retryDelay
	for {
		sent, err := o.send(ctx)
		switch {
		case errors.Is(err, ErrUnauthorized):
			return err
		case err != nil:
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return fmt.Errorf("remote: the outbox not sent whole: %w", err)
			}
			delay = min(2*delay, retryMaxDelay)
		case sent == 0:
			return nil
		}
	}
}

// send sends the next batch, and returns how many records left the outbox:
// the ones written, and the one refused for good, set apart.
func (o *Outbox) send(ctx context.Context) (int, error) {
	limit := batchRecords
	if o.single {
		limit = 1
	}
	records, err := o.batch(limit)
	if err != nil || len(records) == 0 {
		return 0, err
	}

	acked, err := o.client.Outbox(ctx, records)
	var refused *RefusedError
	var status *StatusError
	switch {
	case errors.As(err, &refused) && refused.Index >= 0 && refused.Index < len(records):
		o.recovered()
		return 1, o.bury(records[refused.Index], refused.Message)
	case errors.As(err, &status) && (status.Status == http.StatusBadRequest ||
		status.Status == http.StatusRequestEntityTooLarge):
		// a batch the controller can't read, eg: too large: one at a time,
		// until the one it can't is found
		o.recovered()
		if len(records) > 1 {
			o.single = true
			return 0, nil
		}
		return 1, o.bury(records[0], status.Message)
	case err != nil:
		return 0, err
	}
	o.recovered()
	o.single = false

	removed := 0
	err = o.db.Update(func(tx *buntdb.Tx) error {
		for _, record := range records {
			if record.Seq > acked {
				break
			}
			if _, err := tx.Delete(recordKey(pendingPrefix, record.Seq)); err != nil &&
				!errors.Is(err, buntdb.ErrNotFound) {
				return err
			}
			removed++
		}
		return nil
	})
	if err == nil && removed == 0 {
		err = fmt.Errorf("remote: the controller acknowledged %d, before the records sent", acked)
	}
	return removed, err
}

// bury sets the record refused for good apart, with why.
func (o *Outbox) bury(record agentapi.Record, why string) error {
	log.Errorf("remote: the controller refused the %s record %s for good, set apart: %s", record.Kind, record.Key,
		why)
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return o.db.Update(func(tx *buntdb.Tx) error {
		if _, err := tx.Delete(recordKey(pendingPrefix, record.Seq)); err != nil &&
			!errors.Is(err, buntdb.ErrNotFound) {
			return err
		}
		_, _, err := tx.Set(recordKey(deadPrefix, record.Seq), string(encoded), nil)
		return err
	})
}

// failed warns of the records not reaching the controller, now and then
// while it lasts.
func (o *Outbox) failed(err error) {
	now := time.Now()
	if o.failing.IsZero() {
		o.failing = now
	}
	if now.Sub(o.warned) >= warnEvery {
		o.warned = now
		log.Warnf("remote: the records don't reach the controller, kept until they do: %v", err)
	}
}

// recovered tells of the records reaching the controller again.
func (o *Outbox) recovered() {
	if !o.failing.IsZero() {
		log.Infof("remote: the records reach the controller again, after %s",
			time.Since(o.failing).Round(time.Second))
		o.failing, o.warned = time.Time{}, time.Time{}
	}
}

// Close closes the outbox and releases its lock.
func (o *Outbox) Close() error {
	err := o.db.Close()
	return errors.Join(err, unlock(o.lock))
}
