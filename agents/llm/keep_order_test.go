package llm

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// gatedDecisions is the store of the decisions holding the save of the record
// of the second skip in a row until released, telling the notes saved.
type gatedDecisions struct {
	*MemoryDecisions
	held    chan struct{} // closed once the save held waits
	release chan struct{}

	mtx   sync.Mutex
	saved []string // the last note of each record saved, in order
}

func (g *gatedDecisions) Save(d ui.Decision, attachments []Attachment) error {
	note := ""
	if len(d.Notes) > 0 {
		note = d.Notes[len(d.Notes)-1].Text
	}
	if strings.HasPrefix(note, "skipped 2 times") {
		close(g.held)
		<-g.release
	}
	g.mtx.Lock()
	g.saved = append(g.saved, note)
	g.mtx.Unlock()
	return g.MemoryDecisions.Save(d, attachments)
}

func (g *gatedDecisions) saves() []string {
	g.mtx.Lock()
	defer g.mtx.Unlock()
	return append([]string(nil), g.saved...)
}

// M7: the record of a run of skips kept from two analyses at once ends with
// the last count, in the store and on the dashboard: the second skip saved
// slowly, the third one waits for it rather than being overwritten by it.
func TestTrader_SkipsKeptAtOnceEndWithTheLastCount(t *testing.T) {
	trader, err := New(DefaultConfig(), &fakeProvider{t: t})
	require.NoError(t, err)
	store := &gatedDecisions{MemoryDecisions: NewMemoryDecisions(0), held: make(chan struct{}),
		release: make(chan struct{})}
	trader.SetDecisionStore(store)
	var toldMtx sync.Mutex
	var told []string
	trader.ListenDecisions(func(d ui.Decision) {
		toldMtx.Lock()
		defer toldMtx.Unlock()
		note := ""
		if len(d.Notes) > 0 {
			note = d.Notes[len(d.Notes)-1].Text
		}
		told = append(told, note)
	})

	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	skip := func(i int) ui.Decision {
		at := base.Add(time.Duration(i) * 10 * time.Second)
		return ui.Decision{ID: decisionID(at, testPair), Pair: testPair, Timeframe: "1m", DecidedAt: at.Unix(),
			Trigger: TriggerInterval, Price: 100, Reason: SkipSize, Outcome: OutcomeSkipped, Error: "too small"}
	}
	first := skip(0)
	trader.keep(first, nil)

	second := make(chan struct{})
	go func() {
		defer close(second)
		trader.keep(skip(1), nil)
	}()
	select {
	case <-store.held:
	case <-time.After(5 * time.Second):
		t.Fatal("the second skip never saved")
	}
	third := make(chan struct{})
	go func() {
		defer close(third)
		trader.keep(skip(2), nil)
	}()
	// the third one saved meanwhile without the fix: given the time to
	waitUntil(300*time.Millisecond, func() bool { return len(store.saves()) == 2 })
	close(store.release)
	for _, done := range []chan struct{}{second, third} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a skip never kept")
		}
	}

	require.Len(t, store.saves(), 3)
	require.True(t, strings.HasPrefix(store.saves()[2], "skipped 3 times"), "saved last: %q", store.saves())
	kept, ok, err := trader.Decision(first.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(kept.Notes[len(kept.Notes)-1].Text, "skipped 3 times"),
		"the record kept: %q", kept.Notes)
	toldMtx.Lock()
	defer toldMtx.Unlock()
	require.Len(t, told, 3)
	require.True(t, strings.HasPrefix(told[2], "skipped 3 times"), "told last: %q", told)
}

// waitUntil waits for cond, wait at most, without failing.
func waitUntil(wait time.Duration, cond func() bool) {
	for end := time.Now().Add(wait); time.Now().Before(end) && !cond(); {
		time.Sleep(5 * time.Millisecond)
	}
}
