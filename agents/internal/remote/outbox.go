package remote

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/buntdb"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/tools/log"
)

// The keys of the records of the outbox: the ones to send, the ones held
// apart after failing again and again (see Outbox.hold), and the ones the
// controller refused for good, kept apart for a look, by their Seq.
const (
	pendingPrefix = "r:"
	heldPrefix    = "h:"
	deadPrefix    = "d:"
)

// The limits of a batch of the outbox, one record at least; and of the check
// of the records after one failing again and again, see Outbox.send: the
// next checkRecords, or the next ones of its kind among the next checkScan.
const (
	batchRecords = 200
	batchBytes   = 16 << 20
	checkRecords = 10
	checkScan    = 1000
)

// The waits after a failure to reach the controller, doubled up to the max,
// and how often a controller out of reach is warned about.
var (
	retryDelay    = time.Second
	retryMaxDelay = 30 * time.Second
	warnEvery     = 5 * time.Minute
)

// A batch failing poisonAfter times in a row, or for poisonFor, is halved on
// each failure since, down to its first record alone, which is held apart
// once it failed alone for holdAfter, poisonAfter times in a row right after
// the controller wrote the records of its kind after it, see Outbox.send. A
// record held is sent again after heldRetry, doubled on each failure up to
// heldRetryMax, see Outbox.retryHeld.
var (
	poisonAfter  = 5
	poisonFor    = 5 * time.Minute
	holdAfter    = 30 * time.Minute
	heldRetry    = 15 * time.Minute
	heldRetryMax = 4 * time.Hour
)

// The file of the outbox is compacted as it opens once it holds more than its
// records, and while it runs once it grew past compactMinSize and twice its
// size after the last compaction, as buntdb would, with no record pending, see
// Outbox.compact.
var (
	compactWaste   int64 = 1 << 20
	compactMinSize int64 = 32 << 20
)

// compactCopied is called once a compaction wrote the copy of the records,
// before it takes the ones added meanwhile: for the tests.
var compactCopied = func() {}

// ErrOutboxCorrupt is the error of an outbox whose file can't be read, see
// OpenOutbox.
var ErrOutboxCorrupt = errors.New("remote: the outbox can't be read")

// errNotOnDisk is the error of a write of the outbox whose file failed to
// sync: the write may be lost with the machine, kept all the same.
var errNotOnDisk = errors.New("not confirmed on disk")

// sender writes the records of an outbox, see Client.Outbox, and checks that
// the controller writes them, see Client.CheckOutbox.
type sender interface {
	Outbox(ctx context.Context, request agentapi.OutboxRequest) (uint64, error)
	CheckOutbox(ctx context.Context, records []agentapi.Record) error
}

// syncer syncs the file of the outbox, see Outbox.update.
type syncer interface {
	Sync() error
	Close() error
}

// Outbox is the queue of the records an agent keeps through the controller:
// each written to a file of its own before Add returns, in order, then sent to
// the controller in batches as it answers (see Run), so the agent neither
// waits for the controller nor loses a record while it is out of reach. A
// record the controller refuses for good is set apart, and one it fails again
// and again alone while it writes the next ones of its kind, held apart (see
// send): the next ones sent.
//
// The file is a buntdb synced by the outbox itself after each write, whose
// failure is an error of the write, and never rewritten by buntdb, whose
// rewrite syncs neither the new file nor its directory: the outbox compacts it
// itself, durably (see compact). A record written is lost only with the disk
// or a machine that lies about its syncs.
type Outbox struct {
	file   string
	lock   *os.File
	client sender

	// db and disk, the file of db opened apart to sync it (see update), are
	// opened again by a compaction, which holds dbmu alone, after mu
	dbmu      sync.RWMutex
	db        *buntdb.DB
	disk      syncer
	compacted int64 // the size of the file after its last compaction

	mu       sync.Mutex
	seq      uint64       // of the last record added
	pending  int          // the records not sent
	setApart int          // the records refused for good
	held     []heldRecord // the records held apart, by Seq

	wake chan struct{} // a record added

	// of the sender, Run or Flush, one at a time
	limit   int                  // the records of the next batch at most, 0 batchRecords: halved after a failure, see send
	stuck   stuck                // the first record of the batches failing
	wrote   map[string]time.Time // when a record of each kind was written last
	failing time.Time
	warned  time.Time
}

// stuck is the first record of the batches failing in a row, see Outbox.send:
// since when, how many times, alone how many of them, how many of those in a
// row right after the controller wrote the records of its kind after it, and
// whether it did just before the last try.
type stuck struct {
	seq      uint64
	since    time.Time
	failures int
	alone    int
	proofs   int
	others   bool
}

// heldRecord is a record held apart, see Outbox.hold: of its kind and key,
// tried last at tried (zero since the outbox opened), sent again once the
// controller wrote a record of its kind after that and wait passed.
type heldRecord struct {
	seq       uint64
	kind, key string
	tried     time.Time
	wait      time.Duration
}

// OpenOutbox opens the outbox of file, eg: data/outbox/llm-3.db, its records
// sent with client, locked for this process: another process of the agent,
// eg: left running by a controller before, holds it. A file that can't be
// read, eg: written over in the middle, fails with ErrOutboxCorrupt, left as
// it is, see openOutboxFile; one cut short, eg: the agent killed in the middle
// of a write, opens with the records before the cut.
func OpenOutbox(file string, client sender) (*Outbox, error) {
	return openOutboxFile(file, client, false)
}

// openOutboxFile opens the outbox of file, see OpenOutbox: with quarantine,
// rather than failing, a file that can't be read is moved aside (see
// quarantineFile) for an empty one, and the records that can't be read of one
// that can, set apart.
func openOutboxFile(file string, client sender, quarantine bool) (*Outbox, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, fmt.Errorf("remote: the outbox: %w", err)
	}
	locked, err := lock(file + ".lock")
	if err != nil {
		return nil, fmt.Errorf("remote: the outbox: %w", err)
	}
	o := &Outbox{file: file, lock: locked, client: client, wake: make(chan struct{}, 1),
		wrote: make(map[string]time.Time)}
	if err := o.open(quarantine); err != nil {
		_ = unlock(locked)
		return nil, err
	}
	if o.pending > 0 {
		log.Infof("[SETUP] the outbox %s holds %d records not sent yet", file, o.pending)
	}
	if o.setApart > 0 {
		log.Warnf("[SETUP] the outbox %s holds %d records the controller refused for good, set apart", file,
			o.setApart)
	}
	if len(o.held) > 0 {
		log.Warnf("[SETUP] the outbox %s holds %d records held apart, failing again and again: sent again once "+
			"the controller writes records of their kind", file, len(o.held))
	}
	return o, nil
}

// open opens the file of the outbox, see openOutboxFile, and compacts it
// when it holds more than its records.
func (o *Outbox) open(quarantine bool) error {
	size := sizeOf(o.file)
	db, err := openDB(o.file)
	if cut := size - sizeOf(o.file); err == nil && cut > 0 {
		// buntdb drops what follows the last record it reads whole
		log.Warnf("remote: the outbox %s ended in the middle of a write, eg: the agent killed as it wrote: its "+
			"last %d bytes dropped", o.file, cut)
	}
	if errors.Is(err, buntdb.ErrInvalid) && quarantine {
		aside, moved := quarantineFile(o.file)
		if moved != nil {
			return fmt.Errorf("%w: %s: %v, nor moved aside: %w", ErrOutboxCorrupt, o.file, err, moved)
		}
		log.Errorf("remote: the outbox %s can't be read (%v): moved aside to %s, kept for a look, and started "+
			"empty: its records not sent never reach the controller", o.file, err, aside)
		db, err = openDB(o.file)
	}
	switch {
	case errors.Is(err, buntdb.ErrInvalid):
		return corrupt(o.file, err)
	case err != nil:
		return fmt.Errorf("remote: the outbox %s: %w", o.file, err)
	}

	var unreadable []string
	var live int64
	err = db.View(func(tx *buntdb.Tx) error {
		return tx.AscendKeys("*", func(key, value string) bool {
			live += setSize(key, value)
			prefix, seq, ok := parseKey(key)
			if !ok {
				return true
			}
			o.seq = max(o.seq, seq)
			if prefix == deadPrefix {
				o.setApart++
				return true
			}
			var record agentapi.Record
			if json.Unmarshal([]byte(value), &record) != nil {
				unreadable = append(unreadable, key)
				return true
			}
			if prefix == heldPrefix {
				o.held = append(o.held, heldRecord{seq: seq, kind: record.Kind, key: record.Key})
				return true
			}
			o.pending++
			return true
		})
	})
	if err == nil && len(unreadable) > 0 {
		if !quarantine {
			err = corrupt(o.file, fmt.Errorf("the records %s can't be read", strings.Join(unreadable, ", ")))
		} else {
			err = setApartUnreadable(db, unreadable)
			o.setApart += len(unreadable)
			log.Errorf("remote: the records %s of the outbox %s can't be read: set apart, never sent to the "+
				"controller", strings.Join(unreadable, ", "), o.file)
		}
	}
	if err == nil {
		db, err = compactOnOpen(db, o.file, live)
	}
	if err != nil {
		if db != nil {
			_ = db.Close()
		}
		if errors.Is(err, ErrOutboxCorrupt) {
			return err
		}
		return fmt.Errorf("remote: the outbox %s: %w", o.file, err)
	}
	disk, err := os.OpenFile(o.file, os.O_RDWR, 0)
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("remote: the outbox %s: %w", o.file, err)
	}
	o.db, o.disk, o.compacted = db, disk, sizeOf(o.file)
	return nil
}

// corrupt returns the error of the file of an outbox that can't be read, of
// cause, telling what to do.
func corrupt(file string, cause error) error {
	return fmt.Errorf("%w: %s: %v: the file is left as it is, with its records not sent yet; to start the agent "+
		"all the same, give it the param outbox-quarantine=true, which moves the file aside to %s.corrupt-<time>, "+
		"kept for a look (or the records it can't read alone, set apart), and starts with an empty outbox: its "+
		"records never reach the controller, eg: the last state of an order", ErrOutboxCorrupt, file, cause, file)
}

// quarantineFile moves the file of an outbox that can't be read aside, to
// <file>.corrupt-<time>, kept for a look, and returns where.
func quarantineFile(file string) (string, error) {
	aside := file + ".corrupt-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.Rename(file, aside); err != nil {
		return "", err
	}
	return aside, syncDir(filepath.Dir(file))
}

// setApartUnreadable sets the records of keys apart, as they are.
func setApartUnreadable(db *buntdb.DB, keys []string) error {
	return db.Update(func(tx *buntdb.Tx) error {
		for _, key := range keys {
			value, err := tx.Delete(key)
			if err != nil {
				return err
			}
			_, seq, _ := parseKey(key)
			if _, _, err := tx.Set(recordKey(deadPrefix, seq), value, nil); err != nil {
				return err
			}
		}
		return nil
	})
}

// openDB opens the buntdb of file as the outbox keeps it: synced by the
// outbox itself, see Outbox.update, and never rewritten by buntdb, see
// Outbox.compact.
func openDB(file string) (*buntdb.DB, error) {
	db, err := buntdb.Open(file)
	if err != nil {
		return nil, err
	}
	var config buntdb.Config
	err = db.ReadConfig(&config)
	if err == nil {
		config.SyncPolicy = buntdb.Never
		config.AutoShrinkDisabled = true
		err = db.SetConfig(config)
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// compactOnOpen compacts the file of db, see rewrite, when it holds more than
// its records, of live bytes: compactWaste more, or twice as many.
func compactOnOpen(db *buntdb.DB, file string, live int64) (*buntdb.DB, error) {
	size := sizeOf(file)
	if size <= live || (size-live < compactWaste && size < 2*live) {
		return db, nil
	}
	compacted, err := rewrite(db, file)
	if compacted != nil && err != nil {
		log.Warnf("remote: the outbox %s: %v", file, err)
		err = nil
	}
	return compacted, err
}

// setSize returns the bytes of the record of key and value in the file of a
// buntdb.
func setSize(key, value string) int64 {
	bulk := func(s string) int64 {
		return int64(len(strconv.Itoa(len(s))) + len(s) + 5)
	}
	return int64(len("*3\r\n")) + bulk("set") + bulk(key) + bulk(value)
}

// sizeOf returns the size of file, 0 when it can't be read.
func sizeOf(file string) int64 {
	info, err := os.Stat(file)
	if err != nil {
		return 0
	}
	return info.Size()
}

// rewrite writes the records of db alone to its file, durably, and returns db
// opened again on it: a snapshot of db written to <file>.tmp and synced, then
// replacing the file (see replace). buntdb's own rewrite (Shrink) syncs
// neither the copy nor its directory, which a crash of the machine after it
// could leave with an empty or a truncated file.
func rewrite(db *buntdb.DB, file string) (*buntdb.DB, error) {
	tmp := file + ".tmp"
	if err := writeSynced(tmp, db.Save); err != nil {
		_ = os.Remove(tmp)
		return db, fmt.Errorf("the compaction of its file: %w", err)
	}
	return replace(db, tmp, file)
}

// replace replaces the file of db by its compacted copy tmp, synced already,
// and returns db opened again on it: tmp renamed over the file, its directory
// synced. A failure to rename returns db opened again on the file as it was;
// after the rename, db is nil when it can't be opened again, the error a
// failure to sync the directory, the file compacted all the same.
func replace(db *buntdb.DB, tmp, file string) (*buntdb.DB, error) {
	_ = db.Close() // its writes synced already
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		again, openErr := openDB(file)
		return again, errors.Join(fmt.Errorf("the compaction of its file: %w", err), openErr)
	}
	synced := syncDir(filepath.Dir(file))
	again, err := openDB(file)
	if err != nil {
		return nil, fmt.Errorf("opened again after its compaction: %w", err)
	}
	if synced != nil {
		synced = fmt.Errorf("compacted, its directory not synced: %w", synced)
	}
	return again, synced
}

// appendSince appends to the copy of a file what the file holds past end,
// synced.
func appendSince(copied, file string, end int64) error {
	from, err := os.Open(file)
	if err != nil {
		return err
	}
	defer func() { _ = from.Close() }()
	if _, err := from.Seek(end, io.SeekStart); err != nil {
		return err
	}
	to, err := os.OpenFile(copied, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	n, err := io.Copy(to, from)
	if err == nil && n > 0 {
		err = to.Sync()
	}
	return errors.Join(err, to.Close())
}

// writeSynced writes file with write, synced before it returns.
func writeSynced(file string, write func(io.Writer) error) error {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = write(f)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// recordKey returns the key of the record seq under prefix, sorting as the
// records came.
func recordKey(prefix string, seq uint64) string {
	return fmt.Sprintf("%s%020d", prefix, seq)
}

// parseKey returns the prefix and the Seq of the key of a record.
func parseKey(key string) (string, uint64, bool) {
	for _, prefix := range []string{pendingPrefix, heldPrefix, deadPrefix} {
		if rest, ok := strings.CutPrefix(key, prefix); ok {
			seq, err := strconv.ParseUint(rest, 10, 64)
			return prefix, seq, err == nil
		}
	}
	return "", 0, false
}

// view runs fn in a read-only transaction of the outbox.
func (o *Outbox) view(fn func(tx *buntdb.Tx) error) error {
	o.dbmu.RLock()
	defer o.dbmu.RUnlock()
	if o.db == nil {
		return buntdb.ErrDatabaseClosed
	}
	return o.db.View(fn)
}

// update runs fn in a writable transaction of the outbox, on disk once it
// returns: the file synced, its failure an error wrapping errNotOnDisk, what
// fn wrote kept all the same.
func (o *Outbox) update(fn func(tx *buntdb.Tx) error) error {
	o.dbmu.RLock()
	defer o.dbmu.RUnlock()
	if o.db == nil {
		return buntdb.ErrDatabaseClosed
	}
	if err := o.db.Update(fn); err != nil {
		return err
	}
	if err := o.disk.Sync(); err != nil {
		return fmt.Errorf("%w: %w", errNotOnDisk, err)
	}
	return nil
}

// Add writes the record of kind and key, of the JSON of payload, at the end of
// the outbox, on disk when it returns, and wakes the sender. A record the
// file fails to sync is an error, sent all the same while the agent runs.
func (o *Outbox) Add(kind, key string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("remote: the %s record %s: %w", kind, key, err)
	}
	o.mu.Lock()
	o.seq++
	record := agentapi.Record{Seq: o.seq, Kind: kind, Key: key, At: time.Now(), Payload: data}
	encoded, err := json.Marshal(record)
	if err == nil {
		err = o.update(func(tx *buntdb.Tx) error {
			_, _, err := tx.Set(recordKey(pendingPrefix, record.Seq), string(encoded), nil)
			return err
		})
	}
	queued := err == nil || errors.Is(err, errNotOnDisk)
	if queued {
		o.pending++
	}
	o.mu.Unlock()
	if queued {
		select {
		case o.wake <- struct{}{}:
		default:
		}
	}
	if err != nil {
		return fmt.Errorf("remote: the outbox: the %s record %s: %w", kind, key, err)
	}
	return nil
}

// Pending returns the records of kind not sent yet, the ones held apart (see
// hold) included, in order, eg: the orders of a run before that didn't reach
// the controller.
func (o *Outbox) Pending(kind string) ([]agentapi.Record, error) {
	var records []agentapi.Record
	var failed error
	err := o.view(func(tx *buntdb.Tx) error {
		for _, prefix := range []string{heldPrefix, pendingPrefix} {
			err := tx.AscendKeys(prefix+"*", func(key, value string) bool {
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
			if err != nil || failed != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		err = failed
	}
	slices.SortFunc(records, func(a, b agentapi.Record) int { return cmp.Compare(a.Seq, b.Seq) })
	return records, err
}

// left returns how many records are not sent yet.
func (o *Outbox) left() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.pending
}

// batch returns the first records to send after the Seq after, limit and
// batchBytes at most, one at least.
func (o *Outbox) batch(after uint64, limit int) ([]agentapi.Record, error) {
	var records []agentapi.Record
	size := 0
	var failed error
	err := o.view(func(tx *buntdb.Tx) error {
		return tx.AscendGreaterOrEqual("", recordKey(pendingPrefix, after+1), func(key, value string) bool {
			if !strings.HasPrefix(key, pendingPrefix) || (len(records) > 0 && size+len(value) > batchBytes) {
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
// after a failure, the same batch again after a wait (see send). It returns
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
			o.compact()
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
// agent, or ctx done, eg: as the agent stops: then it fails, telling how many
// are left, sent by the next start.
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
				return fmt.Errorf("remote: the outbox not sent whole, %d records left: %w", o.left(), err)
			}
			delay = min(2*delay, retryMaxDelay)
		case sent == 0:
			if left := o.left(); left > 0 { // never: send sends while there are
				return fmt.Errorf("remote: the outbox not sent whole, %d records left", left)
			}
			return nil
		}
	}
}

// request returns the request of the batch records, with what the outbox
// holds, see agentapi.OutboxRequest.
func (o *Outbox) request(records []agentapi.Record) agentapi.OutboxRequest {
	next := o.addedAt(records[len(records)-1].Seq)
	o.mu.Lock()
	defer o.mu.Unlock()
	return agentapi.OutboxRequest{Records: records, Pending: o.pending, SetApart: o.setApart, Held: len(o.held),
		NextAt: next}
}

// heldRequest returns the request of the record held apart sent again alone,
// see retryHeld: what the outbox holds, the record counted among the ones not
// sent rather than held, as the request of a batch would.
func (o *Outbox) heldRequest(record agentapi.Record) agentapi.OutboxRequest {
	next := o.addedAt(0)
	o.mu.Lock()
	defer o.mu.Unlock()
	return agentapi.OutboxRequest{Records: []agentapi.Record{record}, Pending: o.pending + 1, SetApart: o.setApart,
		Held: max(len(o.held)-1, 0), NextAt: next}
}

// addedAt returns when the first record not sent after the Seq after was
// added, zero for none.
func (o *Outbox) addedAt(after uint64) time.Time {
	next, err := o.batch(after, 1)
	if err != nil || len(next) == 0 {
		return time.Time{}
	}
	return next[0].At
}

// send sends the next batch, and returns how many records left the outbox:
// the ones written, and the one refused for good, set apart, or held apart. 0
// without an error only when none is left. Before a batch, it sends the
// records held apart that are due again, see retryHeld.
//
// A batch the controller can't read whole (400, 413) is halved at once, down
// to its first record alone, set apart when the controller can't read it
// either while it reads the records after it, checked without writing them
// (see Client.CheckOutbox). A batch it fails to write poisonAfter times in a
// row, or for poisonFor, out of reach or answering another failure (eg: 503),
// is halved on each failure since, down to its first record alone. Once that
// record failed alone poisonAfter times and for poisonFor, each failure of it
// is followed by a check of the next records of its kind (see others): when
// the controller writes them, it is tried again at once, and failing again,
// eg: an error of the database the controller doesn't refuse for good, that's
// a proof against it. It is held apart (see hold) once it failed for
// holdAfter with poisonAfter proofs in a row, a check of the others failing
// starting them again, eg: the database of the controller failing now and
// then. No record is held while the controller fails the records of its kind
// after it, eg: its database out of reach or a fault with these records, nor
// without a record of its kind after it to check. Once a batch is written,
// the next one doubles back to batchRecords.
func (o *Outbox) send(ctx context.Context) (int, error) {
	for {
		limit := o.limit
		if limit == 0 {
			limit = batchRecords
		}
		records, err := o.batch(0, limit)
		if err != nil || len(records) == 0 {
			return 0, err
		}
		if err := o.retryHeld(ctx); err != nil {
			return 0, err
		}
		first := records[0]
		if o.stuck.seq != first.Seq {
			o.stuck = stuck{seq: first.Seq}
		}
		retried := o.stuck.others // at once after the check of the records after it
		o.stuck.others = false

		acked, err := o.client.Outbox(ctx, o.request(records))
		var refused *RefusedError
		var status *StatusError
		switch {
		case err == nil:
			o.recovered()
			o.stuck = stuck{}
			if o.limit > 0 {
				if o.limit *= 2; o.limit >= batchRecords {
					o.limit = 0
				}
			}
			return o.remove(records, acked)
		case errors.As(err, &refused) && refused.Index >= 0 && refused.Index < len(records):
			o.recovered()
			record := records[refused.Index]
			return 1, o.bury(record, pendingPrefix, "refused by the controller: "+refused.Message)
		case errors.As(err, &status) && (status.Status == http.StatusBadRequest ||
			status.Status == http.StatusRequestEntityTooLarge):
			// a batch the controller can't read, eg: too large: halved at
			// once, until the record it can't is alone
			if len(records) > 1 {
				o.limit = len(records) / 2
				continue
			}
			if o.others(ctx, first, false) == othersWritten {
				o.recovered()
				return 1, o.bury(first, pendingPrefix, "unreadable by the controller, which reads the records "+
					"after it: "+status.Message)
			}
			return 0, err
		case errors.Is(err, ErrUnauthorized), ctx.Err() != nil:
			return 0, err
		}

		s := &o.stuck
		now := time.Now()
		if s.since.IsZero() {
			s.since = now
		}
		s.failures++
		if len(records) > 1 {
			if s.failures >= poisonAfter || now.Sub(s.since) >= poisonFor {
				o.limit = max(len(records)/2, 1)
			}
			return 0, err
		}
		s.alone++
		if retried {
			// out of reach, eg: the connection reset: no proof, tried again
			// after the wait, never at once
			if !errors.As(err, &status) {
				return 0, err
			}
			if s.proofs++; s.proofs >= poisonAfter && now.Sub(s.since) >= holdAfter {
				return 1, o.hold(first, fmt.Sprintf("failed by the controller %d times in a row for %s, the last %d "+
					"right after it wrote the records of its kind after it: %v", s.alone,
					now.Sub(s.since).Round(time.Second), s.proofs, err))
			}
			return 0, err
		}
		if s.alone < poisonAfter || now.Sub(s.since) < poisonFor {
			return 0, err
		}
		switch o.others(ctx, first, true) {
		case othersNone:
			return 0, err
		case othersFailed:
			s.proofs = 0 // the controller fails them too
			return 0, err
		}
		s.others = true // tried once more at once
	}
}

// The answers of the check of the records after one, see Outbox.others.
const (
	othersNone    = iota // no record to check
	othersWritten        // the controller writes them
	othersFailed         // it fails them too
)

// others checks the records after record, none written, see
// Client.CheckOutbox: the next checkRecords, or ofKind, the next ones of its
// kind among the next checkScan; othersNone without any.
func (o *Outbox) others(ctx context.Context, record agentapi.Record, ofKind bool) int {
	var next []agentapi.Record
	var err error
	if ofKind {
		next, err = o.ofKind(record)
	} else {
		next, err = o.batch(record.Seq, checkRecords)
	}
	if err != nil || len(next) == 0 {
		return othersNone
	}
	err = o.client.CheckOutbox(ctx, next)
	var refused *RefusedError
	if err == nil || (errors.As(err, &refused) && refused.Index > 0) {
		return othersWritten
	}
	return othersFailed
}

// ofKind returns the first checkRecords records of the kind of record after
// it, among the next checkScan, batchBytes at most, one at least.
func (o *Outbox) ofKind(record agentapi.Record) ([]agentapi.Record, error) {
	var records []agentapi.Record
	scanned, size := 0, 0
	var failed error
	err := o.view(func(tx *buntdb.Tx) error {
		return tx.AscendGreaterOrEqual("", recordKey(pendingPrefix, record.Seq+1), func(key, value string) bool {
			if !strings.HasPrefix(key, pendingPrefix) || scanned >= checkScan ||
				(len(records) > 0 && size+len(value) > batchBytes) {
				return false
			}
			scanned++
			if kindOf(value) != record.Kind {
				return true
			}
			var next agentapi.Record
			if err := json.Unmarshal([]byte(value), &next); err != nil {
				failed = fmt.Errorf("remote: the outbox: %s: %w", key, err)
				return false
			}
			records = append(records, next)
			size += len(value)
			return len(records) < checkRecords
		})
	})
	if err == nil {
		err = failed
	}
	return records, err
}

// kindOf returns the kind of the JSON of a record, read up to it alone: ""
// when it can't be read.
func kindOf(value string) string {
	decoder := json.NewDecoder(strings.NewReader(value))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return ""
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		var kind string
		switch token {
		case "kind":
			if decoder.Decode(&kind) != nil {
				return ""
			}
			return kind
		case "payload":
			return ""
		}
		var skipped json.RawMessage
		if decoder.Decode(&skipped) != nil {
			return ""
		}
	}
	return ""
}

// remove removes the records the controller acknowledged, up to acked, and
// returns how many: and the records held apart of the same kind and key
// before them, superseded, eg: an older state of the same order.
func (o *Outbox) remove(records []agentapi.Record, acked uint64) (int, error) {
	o.mu.Lock()
	held := slices.Clone(o.held)
	o.mu.Unlock()
	removed := 0
	var superseded []heldRecord
	err := o.update(func(tx *buntdb.Tx) error {
		removed, superseded = 0, nil
		for _, record := range records {
			if record.Seq > acked {
				break
			}
			if _, err := tx.Delete(recordKey(pendingPrefix, record.Seq)); err != nil &&
				!errors.Is(err, buntdb.ErrNotFound) {
				return err
			}
			removed++
			for _, h := range held {
				if h.kind == record.Kind && h.key == record.Key && h.seq < record.Seq &&
					!slices.ContainsFunc(superseded, func(s heldRecord) bool { return s.seq == h.seq }) {
					if _, err := tx.Delete(recordKey(heldPrefix, h.seq)); err != nil &&
						!errors.Is(err, buntdb.ErrNotFound) {
						return err
					}
					superseded = append(superseded, h)
				}
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, errNotOnDisk):
		// sent again by the next start, at worst, written the same
		log.Warnf("remote: the outbox: the records sent: %v", err)
		err = nil
	case err != nil:
		removed, superseded = 0, nil
	}
	now := time.Now()
	for _, record := range records[:removed] {
		o.wrote[record.Kind] = now
	}
	o.mu.Lock()
	o.pending -= removed
	o.held = slices.DeleteFunc(o.held, func(h heldRecord) bool {
		return slices.ContainsFunc(superseded, func(s heldRecord) bool { return s.seq == h.seq })
	})
	o.mu.Unlock()
	for _, h := range superseded {
		log.Infof("remote: the %s record %s held apart, a later one of it written: dropped", h.kind, h.key)
	}
	if err == nil && removed == 0 {
		err = fmt.Errorf("remote: the controller acknowledged %d, before the records sent", acked)
	}
	return removed, err
}

// bury sets the record the controller refused for good apart, never sent
// again, logged with how: a record not sent, or held apart, of the keys of
// from.
func (o *Outbox) bury(record agentapi.Record, from, how string) error {
	log.Errorf("remote: the %s record %s, %s: set apart for good, kept in the outbox %s, never sent", record.Kind,
		record.Key, how, o.file)
	err := o.move(record, from, deadPrefix)
	if err == nil {
		o.mu.Lock()
		if from == heldPrefix {
			o.held = slices.DeleteFunc(o.held, func(h heldRecord) bool { return h.seq == record.Seq })
		} else {
			o.pending--
		}
		o.setApart++
		o.mu.Unlock()
	}
	return err
}

// hold holds the record not sent failing again and again apart, see send,
// logged with how: kept in the outbox, the next ones sent, it is sent again
// alone later (see retryHeld), and dropped once a later record of the same
// kind and key is written (see remove).
func (o *Outbox) hold(record agentapi.Record, how string) error {
	log.Errorf("remote: the %s record %s, %s: held apart, kept in the outbox %s, the next ones sent: sent again "+
		"once the controller writes records of its kind, in %s at least", record.Kind, record.Key, how, o.file,
		heldRetry)
	err := o.move(record, pendingPrefix, heldPrefix)
	if err == nil {
		o.mu.Lock()
		o.pending--
		o.held = append(o.held, heldRecord{seq: record.Seq, kind: record.Kind, key: record.Key, tried: time.Now(),
			wait: heldRetry})
		o.mu.Unlock()
	}
	return err
}

// move moves the record of the keys of from to the ones of to, as it is.
func (o *Outbox) move(record agentapi.Record, from, to string) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	err = o.update(func(tx *buntdb.Tx) error {
		if _, err := tx.Delete(recordKey(from, record.Seq)); err != nil && !errors.Is(err, buntdb.ErrNotFound) {
			return err
		}
		_, _, err := tx.Set(recordKey(to, record.Seq), string(encoded), nil)
		return err
	})
	if errors.Is(err, errNotOnDisk) {
		log.Warnf("remote: the outbox: the record set apart: %v", err)
		err = nil
	}
	return err
}

// retryHeld sends the records held apart again, each alone, oldest first:
// each once the controller wrote a record of its kind since it was held or
// tried last, and its wait passed. Written, a record leaves the outbox;
// refused for good, it is set apart; failing again, it waits twice as long,
// heldRetryMax at most. It fails only as the controller refuses the agent, or
// ctx done.
func (o *Outbox) retryHeld(ctx context.Context) error {
	now := time.Now()
	o.mu.Lock()
	var due []heldRecord
	for _, h := range o.held {
		if o.wrote[h.kind].After(h.tried) && now.Sub(h.tried) >= h.wait {
			due = append(due, h)
		}
	}
	o.mu.Unlock()
	for _, h := range due {
		var record agentapi.Record
		err := o.view(func(tx *buntdb.Tx) error {
			value, err := tx.Get(recordKey(heldPrefix, h.seq))
			if err == nil {
				err = json.Unmarshal([]byte(value), &record)
			}
			return err
		})
		if errors.Is(err, buntdb.ErrNotFound) { // gone from the file: never sent again
			o.mu.Lock()
			o.held = slices.DeleteFunc(o.held, func(held heldRecord) bool { return held.seq == h.seq })
			o.mu.Unlock()
		}
		if err != nil {
			log.Warnf("remote: the outbox: the record held apart %d: %v", h.seq, err)
			continue
		}
		acked, err := o.client.Outbox(ctx, o.heldRequest(record))
		var refused *RefusedError
		switch {
		case err == nil && acked >= record.Seq:
			// and the ones of the same kind and key before it, superseded
			gone := func(held heldRecord) bool {
				return held.seq == h.seq || (held.kind == h.kind && held.key == h.key && held.seq < h.seq)
			}
			o.mu.Lock()
			var written []heldRecord
			for _, held := range o.held {
				if gone(held) {
					written = append(written, held)
				}
			}
			o.mu.Unlock()
			err = o.update(func(tx *buntdb.Tx) error {
				for _, held := range written {
					if _, err := tx.Delete(recordKey(heldPrefix, held.seq)); err != nil &&
						!errors.Is(err, buntdb.ErrNotFound) {
						return err
					}
				}
				return nil
			})
			if errors.Is(err, errNotOnDisk) {
				err = nil
			}
			if err != nil {
				log.Warnf("remote: the outbox: the record held apart %d, written: %v", h.seq, err)
				continue
			}
			o.wrote[record.Kind] = time.Now()
			o.mu.Lock()
			o.held = slices.DeleteFunc(o.held, gone)
			o.mu.Unlock()
			log.Infof("remote: the %s record %s held apart written at last", record.Kind, record.Key)
		case errors.As(err, &refused) && refused.Index == 0:
			if err := o.bury(record, heldPrefix, "refused by the controller: "+refused.Message); err != nil {
				log.Warnf("remote: the outbox: the record held apart %d: %v", h.seq, err)
			}
		case errors.Is(err, ErrUnauthorized), ctx.Err() != nil:
			return err
		default:
			o.mu.Lock()
			for i := range o.held {
				if o.held[i].seq == h.seq {
					o.held[i].tried, o.held[i].wait = time.Now(), min(max(2*h.wait, heldRetry), heldRetryMax)
					h = o.held[i]
				}
			}
			o.mu.Unlock()
			log.Warnf("remote: the %s record %s held apart failed again, sent again in %s at least: %v",
				record.Kind, record.Key, h.wait, err)
		}
	}
	return nil
}

// compact compacts the file of the outbox once it grew past compactMinSize
// and twice its size after the last compaction, with no record pending: the
// file then holds the records set apart and held alone, few. Durably (see
// rewrite), and never holding Add while the copy of the records is written
// and synced: Add waits only for the copy to take the records added
// meanwhile, and replace the file. By the sender only.
func (o *Outbox) compact() {
	size := sizeOf(o.file)
	if size < compactMinSize || size < 2*o.compacted || o.left() > 0 {
		return
	}
	start := time.Now()
	// where the file ends: no write runs meanwhile, Add holding mu, and the
	// sender, which writes too, being the caller
	o.mu.Lock()
	end := sizeOf(o.file)
	o.mu.Unlock()
	tmp := o.file + ".tmp"
	var records bytes.Buffer
	o.dbmu.RLock()
	err := buntdb.ErrDatabaseClosed
	if o.db != nil {
		err = o.db.Save(&records) // in memory: a write of Add waits for this copy, never for the disk
	}
	o.dbmu.RUnlock()
	if err == nil {
		err = writeSynced(tmp, func(w io.Writer) error {
			_, err := records.WriteTo(w)
			return err
		})
	}
	if err != nil {
		_ = os.Remove(tmp)
		log.Warnf("remote: the outbox %s: the compaction of its file: %v", o.file, err)
		return
	}
	compactCopied()

	o.mu.Lock()
	defer o.mu.Unlock()
	o.dbmu.Lock()
	defer o.dbmu.Unlock()
	if o.db == nil {
		_ = os.Remove(tmp)
		return
	}
	// the writes of the records added since end, after the copy: replayed
	// over it, they leave the records as they are now
	if err := appendSince(tmp, o.file, end); err != nil {
		_ = os.Remove(tmp)
		log.Warnf("remote: the outbox %s: the compaction of its file: %v", o.file, err)
		return
	}
	db, err := replace(o.db, tmp, o.file)
	if db != o.db {
		_ = o.disk.Close()
		o.db, o.disk = db, nil
		if db != nil {
			disk, openErr := os.OpenFile(o.file, os.O_RDWR, 0)
			if openErr == nil {
				o.disk = disk
			} else {
				_ = db.Close()
				o.db, err = nil, errors.Join(err, openErr)
			}
		}
	}
	o.compacted = sizeOf(o.file)
	switch {
	case o.db == nil:
		log.Errorf("remote: the outbox %s: %v: the records added from now on fail, kept by the orders in "+
			"memory, the ones not sent yet in the file, sent by the next start", o.file, err)
	case err != nil:
		log.Warnf("remote: the outbox %s: %v", o.file, err)
	default:
		log.Infof("remote: the outbox %s compacted from %d to %d bytes in %s", o.file, size, o.compacted,
			time.Since(start).Round(time.Millisecond))
	}
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
	o.dbmu.Lock()
	defer o.dbmu.Unlock()
	var err error
	if o.db != nil {
		err = errors.Join(o.db.Close(), o.disk.Close())
		o.db, o.disk = nil, nil
	}
	if o.lock != nil {
		err = errors.Join(err, unlock(o.lock))
		o.lock = nil
	}
	return err
}
