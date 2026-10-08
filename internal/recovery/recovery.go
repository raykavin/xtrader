// Package recovery recovers the panics of the code a bot runs on its candles,
// eg: a strategy and its indicators, so that a panic fails that call only,
// rather than the whole agent: reported, never swallowed (see Reporter).
package recovery

import (
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Panic is a panic recovered from a call, see Call.
type Panic struct {
	// Call names what panicked, eg: "OnCandle of the strategy on BTCUSDT":
	// the panics of a call are reported together, see Reporter.
	Call string
	// Candle is the time of the candle it was called on, zero for none.
	Candle time.Time
	Value  any    // of the panic
	Stack  []byte // where it panicked
}

func (p *Panic) Error() string {
	if p.Candle.IsZero() {
		return fmt.Sprintf("%s panicked: %v", p.Call, p.Value)
	}
	return fmt.Sprintf("%s panicked on the candle of %s: %v", p.Call, p.Candle.UTC().Format(time.DateTime),
		p.Value)
}

// Call runs fn, the call named name on the candle of candle (zero for none),
// returning its panic recovered, nil when it returns.
func Call(name string, candle time.Time, fn func()) (recovered *Panic) {
	defer func() {
		if value := recover(); value != nil {
			recovered = &Panic{Call: name, Candle: candle, Value: value, Stack: debug.Stack()}
		}
	}()
	fn()
	return nil
}

// NotifyEvery is how often the panics of a call are notified, at most: a
// panic on every candle in progress, eg: an indicator failing on the data
// of the day, would send a message every few seconds.
const NotifyEvery = 10 * time.Minute

// Reporter reports the panics recovered: every one logged, and notified once
// every NotifyEvery at most by call, with the count of the ones since; the
// stack logged with the ones notified only, the others in a line.
type Reporter struct {
	mtx    sync.Mutex
	notify func(error)
	last   map[string]*reported // by call
	every  time.Duration
}

type reported struct {
	at    time.Time
	since int // the panics of the call after the last one notified
}

// NewReporter returns a reporter notifying notify, nil for none (logged only).
func NewReporter(notify func(error)) *Reporter {
	return &Reporter{notify: notify, last: make(map[string]*reported), every: NotifyEvery}
}

// SetNotify sets who is notified of the panics, nil for none.
func (r *Reporter) SetNotify(notify func(error)) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	r.notify = notify
}

// Report reports p, telling outcome, what came of it, eg: "the candle skipped,
// the bot goes on".
func (r *Reporter) Report(p *Panic, outcome string) {
	now := time.Now()
	r.mtx.Lock()
	last, ok := r.last[p.Call]
	if ok && now.Sub(last.at) < r.every {
		last.since++
		at := last.at
		r.mtx.Unlock()
		log.Errorf("[PANIC] %v: %s (its stack logged at %s)", p, outcome, at.Format(time.TimeOnly))
		return
	}
	var since int
	if ok {
		since = last.since
	}
	r.last[p.Call] = &reported{at: now}
	notify := r.notify
	r.mtx.Unlock()

	err := fmt.Errorf("%w: %s", p, outcome)
	if since > 0 {
		err = fmt.Errorf("%w (%d more since the last notice)", err, since)
	}
	log.Errorf("[PANIC] %v\n%s", err, p.Stack)
	if notify != nil {
		notify(err)
	}
}
