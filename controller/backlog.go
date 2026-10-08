package controller

import (
	"sync"
	"time"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/ui"
)

// Backlog is what the outbox of an agent holds, as it told with its last
// batch (see agentapi.OutboxRequest): the records not sent yet, when the
// oldest of them was added (zero when unknown, eg: an agent of a version
// before it), whether that batch failed, the records the controller refused
// for good, set apart in it, and the ones held apart, failing again and again.
type Backlog struct {
	Pending  int
	Since    time.Time
	Failing  bool
	SetApart int
	Held     int
}

// backlogs are the backlogs of the agents, by ID, kept in memory only: an
// outbox sent whole, or of an agent that never had one, has none.
type backlogs struct {
	mu   sync.Mutex
	byID map[int64]Backlog
}

func newBacklogs() *backlogs {
	return &backlogs{byID: make(map[int64]Backlog)}
}

// report keeps the backlog the agent of id told with request, its records
// written or not.
func (b *backlogs) report(id int64, request agentapi.OutboxRequest, written bool) {
	backlog := Backlog{Pending: request.Pending, Failing: !written, SetApart: request.SetApart,
		Held: request.Held}
	if n := len(request.Records); n > 0 {
		backlog.Since = request.Records[0].At
		if written {
			// the ones left came after the ones written, the first of them
			// added at NextAt: unknown to an agent of a version before it
			backlog.Pending -= n
			backlog.Since = request.NextAt
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if backlog.Pending <= 0 && backlog.SetApart == 0 && backlog.Held == 0 {
		delete(b.byID, id)
		return
	}
	backlog.Pending = max(backlog.Pending, 0)
	b.byID[id] = backlog
}

// of returns the backlog of the agent of id, false for none.
func (b *backlogs) of(id int64) (Backlog, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	backlog, ok := b.byID[id]
	return backlog, ok
}

// Outbox returns the backlog of the outbox of the agent of id, as it told
// with its last batch, nil for none.
func (m *Manager) Outbox(id int64) *ui.AgentOutbox {
	backlog, ok := m.backlogs.of(id)
	if !ok {
		return nil
	}
	outbox := &ui.AgentOutbox{Pending: backlog.Pending, Failing: backlog.Failing, SetApart: backlog.SetApart,
		Held: backlog.Held}
	if !backlog.Since.IsZero() {
		outbox.Since = backlog.Since.Unix()
	}
	return outbox
}
