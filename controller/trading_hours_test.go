//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	glog "gorm.io/gorm/logger"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The window of the trading hours: inside from the start until before the
// end, crossing midnight when the start is after the end; the next change the
// end while inside, the start else.
func TestHoursWindow(t *testing.T) {
	zone := belem(t)
	day := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, zone) }
	for _, c := range []struct {
		start, end string
		at         time.Time
		open       bool
		next       time.Time
	}{
		{"09:00", "17:30", day(30, 8, 59), false, day(30, 9, 0)},
		{"09:00", "17:30", day(30, 9, 0), true, day(30, 17, 30)},
		{"09:00", "17:30", day(30, 17, 29), true, day(30, 17, 30)},
		{"09:00", "17:30", day(30, 17, 30), false, day(31, 9, 0)},
		{"22:00", "02:00", day(30, 21, 59), false, day(30, 22, 0)},
		{"22:00", "02:00", day(30, 23, 0), true, day(31, 2, 0)},
		{"22:00", "02:00", day(31, 1, 59), true, day(31, 2, 0)},
		{"22:00", "02:00", day(31, 2, 0), false, day(31, 22, 0)},
		{"00:00", "23:59", day(30, 23, 59), false, day(31, 0, 0)},
	} {
		open, next := agentapi.TradingWindow(c.start, c.end, c.at, zone)
		assert.Equal(t, c.open, open, "%s–%s at %s", c.start, c.end, c.at.Format("15:04"))
		assert.Equal(t, c.next, next, "%s–%s at %s", c.start, c.end, c.at.Format("15:04"))
	}
}

// putHours sets the trading hours through the dashboard and returns the
// answer, its status code checked.
func putHours(t *testing.T, call func(string, string, any) (*http.Response, string), settings ui.TradingHoursSettings,
	status int) ui.TradingHoursResponse {
	t.Helper()
	resp, body := call(http.MethodPut, "/api/settings/trading-hours", ui.TradingHoursRequest{Settings: settings,
		Reason: "testes"})
	require.Equal(t, status, resp.StatusCode, body)
	var response ui.TradingHoursResponse
	if status == http.StatusOK {
		require.NoError(t, json.Unmarshal([]byte(body), &response))
	}
	return response
}

// sqlExecScan runs a query on the database of the tests, its row in out.
func (s *supervised) sqlExecScan(query string, out any) error {
	db, err := gorm.Open(postgres.Open(s.dsn), &gorm.Config{Logger: glog.Discard})
	if err != nil {
		return err
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()
	return db.Raw(query).Scan(out).Error
}

// endActions returns the actions at the end of the window recorded.
func endActions(t *testing.T, s *supervised) int {
	t.Helper()
	var count int64
	require.NoError(t, s.sqlExecScan("SELECT count(*) FROM trading_hours_events WHERE kind = 'end_action'", &count))
	return int(count)
}

// The trading hours of all the agents: set inside the window, the entries
// free; at the end, the entries of every agent blocked (sent to the ones
// running at once), their positions closed and their pending orders canceled,
// the ones by hand untouched, recorded once whatever the evaluations and a
// controller started again; at the next start, the entries free again.
func TestTradingHours_BlocksOutsideTheWindowAndActsAtItsEnd(t *testing.T) {
	s, clock, trend := newDailySupervised(t) // 14:00 in Belém
	ctx := context.Background()
	l := s.sup.daily
	s.liveAgent(t, "orb", 102, false)
	byHand := position(9, 0, "EURUSDm", false, 0.1, 50, 0)
	handOrder := bridgeOrder{Ticket: 90, Type: 2, Magic: 0, Symbol: "EURUSDm", PriceOpen: 1.1, VolumeCurrent: 0.1}
	s.bridge.set(protected(1, 101), protected(2, 102), byHand)
	s.bridge.mu.Lock()
	s.bridge.orders = []bridgeOrder{handOrder,
		{Ticket: 21, Type: 2, Magic: 102, Symbol: "XAUUSDm", PriceOpen: 4100, VolumeCurrent: 0.01}}
	s.bridge.mu.Unlock()
	trend.mu.Lock()
	trend.pending = []ui.SupervisedOrder{{ID: 11, Pair: "XAUUSDm", Side: "BUY", Type: "LIMIT", Entry: true}}
	trend.mu.Unlock()
	call, _ := settingsSession(t, s.managed)
	entries := func(name string) agentapi.Entries {
		t.Helper()
		a, err := s.store.AgentByName(ctx, name)
		require.NoError(t, err)
		got, err := entriesOf(ctx, s.store, a)
		require.NoError(t, err)
		return got
	}

	got := putHours(t, call, ui.TradingHoursSettings{Enabled: true, Start: "09:00", End: "17:30", Flatten: true,
		CancelPending: true}, http.StatusOK)
	assert.Equal(t, ui.TradingHoursSettings{Enabled: true, Start: "09:00", End: "17:30", Flatten: true,
		CancelPending: true, UpdatedBy: "admin", UpdatedAt: got.Settings.UpdatedAt,
		Windows: []ui.TradingWindow{{Start: "09:00", End: "17:30", Flatten: true, CancelPending: true}}},
		got.Settings, "a single window, as the clients of before send it")
	assert.True(t, got.Status.Open)
	assert.False(t, got.Status.Blocked)
	assert.Equal(t, time.Date(2026, 10, 28, 17, 30, 0, 0, belem(t)).Unix(), got.Status.NextAt)
	assert.False(t, entries("trend").EntriesBlocked, "inside the window")
	before := entries("trend").EntriesRevision

	// the end: blocked, closed and canceled
	clock.set(time.Date(2026, 10, 28, 17, 30, 0, 0, belem(t)))
	require.NoError(t, l.evaluate(ctx))
	status := l.Status().Hours
	assert.False(t, status.Open)
	assert.True(t, status.Blocked)
	assert.Equal(t, time.Date(2026, 10, 29, 9, 0, 0, 0, belem(t)).Unix(), status.NextAt)
	blocked := entries("trend")
	assert.True(t, blocked.EntriesBlocked)
	assert.Greater(t, blocked.EntriesRevision, before)
	assert.Equal(t, "Fim da janela 09:00–17:30: entradas bloqueadas até 09:00", blocked.EntriesReason)
	assert.True(t, entries("orb").EntriesBlocked, "every agent, running or not")
	sent, _ := trend.entries()
	assert.True(t, sent, "sent to the agent running at once")
	trend.mu.Lock()
	assert.Positive(t, trend.drains, "the orders being sent waited for")
	trend.mu.Unlock()
	require.Eventually(t, func() bool {
		l.evaluateHours(ctx)
		end := l.Status().Hours.LastEnd
		return end != nil && end.Status == store.ActionSucceeded
	}, 20*time.Second, 50*time.Millisecond, "the actions of the end done")
	end := l.Status().Hours.LastEnd
	assert.Equal(t, []string{"09:00", "17:30"}, []string{end.Start, end.End}, "the window ended")
	assert.True(t, end.Flatten)
	assert.True(t, end.CancelPending)
	require.NotNil(t, end.ActionID)
	assert.Equal(t, []bridgePosition{byHand}, s.bridge.held(), "only the position by hand left")
	s.bridge.mu.Lock()
	assert.Equal(t, []bridgeOrder{handOrder}, s.bridge.orders, "only the order by hand left")
	s.bridge.mu.Unlock()
	trend.mu.Lock()
	assert.Empty(t, trend.pending, "the pending entry of the agent running canceled")
	trend.mu.Unlock()
	action, err := s.store.SupervisorAction(ctx, *end.ActionID)
	require.NoError(t, err)
	assert.Equal(t, store.ActionClosePositions, action.Action)
	assert.Equal(t, hoursBy, action.RequestedBy)
	assert.Equal(t, store.SourceSupervisor, action.Source)
	assert.Equal(t, originRule, actionInfo(action).Origin, "the trading hours, never the supervisor")
	_, options := scopeOf(action)
	assert.Equal(t, ui.SupervisorOptions{Whole: true, CancelPending: true}, options, "never blocked for good")

	// once: evaluated again, and a controller started again
	clock.add(time.Minute)
	require.NoError(t, l.evaluate(ctx))
	restarted := newDailyLimiter(s.sup)
	restarted.zone = belem(t)
	require.NoError(t, restarted.evaluate(ctx))
	assert.True(t, restarted.Status().Hours.Blocked, "still blocked after a start")
	assert.Equal(t, 1, endActions(t, s), "the end acted on once")

	// the next start: free again
	clock.set(time.Date(2026, 10, 29, 9, 0, 0, 0, belem(t)))
	require.NoError(t, restarted.evaluate(ctx))
	assert.True(t, restarted.Status().Hours.Open)
	assert.False(t, restarted.Status().Hours.Blocked)
	free := entries("trend")
	assert.False(t, free.EntriesBlocked)
	assert.Greater(t, free.EntriesRevision, blocked.EntriesRevision)
	a, err := s.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	doc, _, ok := s.manager.runtimes.get(a.ID)
	require.True(t, ok)
	assert.False(t, doc.EntriesBlocked, "freed in the runtime the agents read")
	assert.Equal(t, free.EntriesRevision, doc.EntriesRevision)
}

// A window without the actions of the end: set outside it, the entries
// blocked without acting; opened and closed again without closing nor
// canceling anything; off, the entries free. Invalid hours refused: a window
// across midnight, windows overlapping or touching, none while on.
func TestTradingHours_WithoutActionsAndOff(t *testing.T) {
	s, clock, trend := newDailySupervised(t) // 14:00 in Belém
	ctx := context.Background()
	l := s.sup.daily
	s.bridge.set(protected(1, 101))
	trend.mu.Lock()
	trend.pending = []ui.SupervisedOrder{{ID: 11, Pair: "XAUUSDm", Side: "BUY", Type: "LIMIT", Entry: true}}
	trend.mu.Unlock()
	call, _ := settingsSession(t, s.managed)

	got := putHours(t, call, ui.TradingHoursSettings{Enabled: true, Start: "22:00", End: "23:30", Flatten: true},
		http.StatusOK)
	assert.False(t, got.Status.Open)
	assert.True(t, got.Status.Blocked, "set outside its window: blocked at once")
	assert.Nil(t, got.Status.LastEnd, "without acting: no window had ended")
	assert.Equal(t, "Fora do horário de operação: entradas bloqueadas até 22:00", reasonOf(t, s, "trend"))

	putHours(t, call, ui.TradingHoursSettings{Enabled: true, Start: "22:00", End: "23:30"}, http.StatusOK)
	clock.set(time.Date(2026, 10, 28, 23, 0, 0, 0, belem(t)))
	require.NoError(t, l.evaluate(ctx))
	assert.False(t, l.Status().Hours.Blocked, "opened at night")
	clock.set(time.Date(2026, 10, 28, 23, 30, 0, 0, belem(t)))
	require.NoError(t, l.evaluate(ctx))
	assert.True(t, l.Status().Hours.Blocked, "closed at its end")
	assert.Zero(t, endActions(t, s), "nothing to do at the end")
	assert.Equal(t, []bridgePosition{protected(1, 101)}, s.bridge.held(), "the position left as it is")
	trend.mu.Lock()
	assert.Len(t, trend.pending, 1, "the pending entry left as it is")
	trend.mu.Unlock()

	got = putHours(t, call, ui.TradingHoursSettings{Start: "22:00", End: "23:30"}, http.StatusOK)
	assert.False(t, got.Status.Enabled)
	assert.False(t, got.Status.Blocked, "off: free")
	a, err := s.store.AgentByName(ctx, "trend")
	require.NoError(t, err)
	entries, err := entriesOf(ctx, s.store, a)
	require.NoError(t, err)
	assert.False(t, entries.EntriesBlocked)

	window := func(start, end string) ui.TradingWindow { return ui.TradingWindow{Start: start, End: end} }
	for _, refused := range []ui.TradingHoursSettings{
		{Enabled: true, Start: "09:00", End: "09:00"},
		{Enabled: true, Start: "09:00"},
		{Start: "24:00", End: "10:00"},
		{Start: "9:00", End: "10:00"},
		{Enabled: true, Start: "22:00", End: "02:00"},
		{Enabled: true},
		{Enabled: true, Windows: []ui.TradingWindow{window("08:00", "10:00"), window("09:30", "11:00")}},
		{Enabled: true, Windows: []ui.TradingWindow{window("10:00", "12:00"), window("08:00", "10:00")}},
	} {
		putHours(t, call, refused, http.StatusBadRequest)
	}
}

// reasonOf returns why the entries of the agent of name are blocked, "" when
// they aren't.
func reasonOf(t *testing.T, s *supervised, name string) string {
	t.Helper()
	a, err := s.store.AgentByName(context.Background(), name)
	require.NoError(t, err)
	entries, err := entriesOf(context.Background(), s.store, a)
	require.NoError(t, err)
	return entries.EntriesReason
}

// awaitEnd evaluates the trading hours until the actions of the last end are
// done, and returns them.
func awaitEnd(t *testing.T, l *dailyLimiter) *ui.TradingHoursEnd {
	t.Helper()
	ctx := context.Background()
	require.Eventually(t, func() bool {
		l.evaluateHours(ctx)
		end := l.Status().Hours.LastEnd
		return end != nil && end.Status == store.ActionSucceeded && !l.hoursEnding.Load()
	}, 20*time.Second, 50*time.Millisecond, "the actions of the end done")
	return l.Status().Hours.LastEnd
}

// Several windows a day, each with the actions of its own end: saved in any
// order, kept in the order of the day; the entries free inside each and
// blocked between them, until the next start; at the end of the first one, the
// positions closed and the pending orders canceled; at the end of the second
// one, only the pending orders canceled, the positions left.
func TestTradingHours_EachWindowActsAtItsOwnEnd(t *testing.T) {
	s, clock, trend := newDailySupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	at := func(h, m int) { clock.set(time.Date(2026, 10, 29, h, m, 0, 0, belem(t))) }
	call, _ := settingsSession(t, s.managed)
	morning := ui.TradingWindow{Start: "08:00", End: "10:00", Flatten: true, CancelPending: true}
	noon := ui.TradingWindow{Start: "12:00", End: "14:00", CancelPending: true}

	at(7, 0)
	got := putHours(t, call, ui.TradingHoursSettings{Enabled: true, Windows: []ui.TradingWindow{noon, morning}},
		http.StatusOK)
	assert.Equal(t, []ui.TradingWindow{morning, noon}, got.Settings.Windows, "in the order of the day")
	assert.Equal(t, "08:00", got.Settings.Start, "the first window as the single one of before")
	assert.True(t, got.Status.Blocked)
	assert.Equal(t, time.Date(2026, 10, 29, 8, 0, 0, 0, belem(t)).Unix(), got.Status.NextAt)

	at(8, 0)
	require.NoError(t, l.evaluate(ctx))
	assert.False(t, l.Status().Hours.Blocked)
	require.NotNil(t, l.Status().Hours.Window)
	assert.Equal(t, morning, *l.Status().Hours.Window)
	s.bridge.set(protected(1, 101))
	trend.mu.Lock()
	trend.pending = []ui.SupervisedOrder{{ID: 11, Pair: "XAUUSDm", Side: "BUY", Type: "LIMIT", Entry: true}}
	trend.mu.Unlock()

	at(10, 0)
	require.NoError(t, l.evaluate(ctx))
	assert.True(t, l.Status().Hours.Blocked)
	assert.Equal(t, "Fim da janela 08:00–10:00: entradas bloqueadas até 12:00", reasonOf(t, s, "trend"))
	assert.Equal(t, time.Date(2026, 10, 29, 12, 0, 0, 0, belem(t)).Unix(), l.Status().Hours.NextAt)
	end := awaitEnd(t, l)
	assert.Equal(t, ui.TradingHoursEnd{At: end.At, Start: "08:00", End: "10:00", Flatten: true, CancelPending: true,
		Status: store.ActionSucceeded, ActionID: end.ActionID}, *end)
	assert.Empty(t, s.bridge.held(), "closed at the end of the first window")
	trend.mu.Lock()
	assert.Empty(t, trend.pending, "canceled")
	trend.mu.Unlock()

	at(12, 0)
	require.NoError(t, l.evaluate(ctx))
	assert.False(t, l.Status().Hours.Blocked, "free in the second window")
	assert.Empty(t, reasonOf(t, s, "trend"))
	s.bridge.set(protected(2, 101))
	trend.mu.Lock()
	trend.pending = []ui.SupervisedOrder{{ID: 12, Pair: "XAUUSDm", Side: "BUY", Type: "LIMIT", Entry: true}}
	trend.mu.Unlock()

	at(14, 0)
	require.NoError(t, l.evaluate(ctx))
	assert.True(t, l.Status().Hours.Blocked)
	assert.Equal(t, "Fim da janela 12:00–14:00: entradas bloqueadas até 08:00", reasonOf(t, s, "trend"))
	end = awaitEnd(t, l)
	assert.Equal(t, []string{"12:00", "14:00"}, []string{end.Start, end.End})
	assert.False(t, end.Flatten)
	action, err := s.store.SupervisorAction(ctx, *end.ActionID)
	require.NoError(t, err)
	assert.Equal(t, store.ActionCancelPending, action.Action, "only the pending orders at the second end")
	assert.Equal(t, []bridgePosition{protected(2, 101)}, s.bridge.held(), "the position left with its stop")
	assert.Equal(t, 2, endActions(t, s))
}

// The end of a window missed with the controller down, back inside the next
// window: its actions taken then, the entries blocked while they run, and the
// next window opened once they end.
func TestTradingHours_AMissedEndActedOnInTheNextWindow(t *testing.T) {
	s, clock, _ := newDailySupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	at := func(h, m int) { clock.set(time.Date(2026, 10, 29, h, m, 0, 0, belem(t))) }
	call, _ := settingsSession(t, s.managed)
	at(9, 0)
	putHours(t, call, ui.TradingHoursSettings{Enabled: true, Windows: []ui.TradingWindow{
		{Start: "08:00", End: "10:00", Flatten: true}, {Start: "12:00", End: "14:00"}}}, http.StatusOK)
	assert.False(t, l.Status().Hours.Blocked)
	s.bridge.set(protected(1, 101))

	at(12, 30) // down from 09:00
	require.NoError(t, l.evaluate(ctx))
	assert.Equal(t, "Fim da janela 08:00–10:00: entradas bloqueadas até as posições serem fechadas",
		reasonOf(t, s, "trend"))
	end := awaitEnd(t, l)
	assert.Equal(t, []string{"08:00", "10:00"}, []string{end.Start, end.End})
	assert.Empty(t, s.bridge.held(), "closed, late")
	require.NoError(t, l.evaluate(ctx))
	assert.False(t, l.Status().Hours.Blocked, "the second window opened after")
	assert.Equal(t, 1, endActions(t, s))
}

// A window changed while open: still inside it, nothing done and its new end
// taken; moved so that now is outside every window, its end acted on at once;
// the hours off while a window is open, nothing done when turned on later.
func TestTradingHours_ChangedWhileOpen(t *testing.T) {
	s, clock, _ := newDailySupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	at := func(h, m int) { clock.set(time.Date(2026, 10, 29, h, m, 0, 0, belem(t))) }
	call, _ := settingsSession(t, s.managed)
	s.bridge.set(protected(1, 101))
	at(9, 0)
	putHours(t, call, ui.TradingHoursSettings{Enabled: true, Windows: []ui.TradingWindow{
		{Start: "08:00", End: "10:00", Flatten: true}}}, http.StatusOK)

	got := putHours(t, call, ui.TradingHoursSettings{Enabled: true, Windows: []ui.TradingWindow{
		{Start: "08:00", End: "11:00", Flatten: true}}}, http.StatusOK)
	assert.False(t, got.Status.Blocked, "still inside: free")
	at(10, 0)
	require.NoError(t, l.evaluate(ctx))
	assert.False(t, l.Status().Hours.Blocked, "its new end taken")
	assert.Zero(t, endActions(t, s))

	at(10, 30)
	got = putHours(t, call, ui.TradingHoursSettings{Enabled: true, Windows: []ui.TradingWindow{
		{Start: "08:00", End: "10:15", Flatten: true}}}, http.StatusOK)
	assert.True(t, got.Status.Blocked, "now outside: its end at once")
	awaitEnd(t, l)
	assert.Empty(t, s.bridge.held())

	// off while open, on again outside: blocked, nothing closed
	at(12, 0)
	putHours(t, call, ui.TradingHoursSettings{Enabled: true, Windows: []ui.TradingWindow{
		{Start: "11:00", End: "13:00", Flatten: true}}}, http.StatusOK)
	assert.False(t, l.Status().Hours.Blocked)
	putHours(t, call, ui.TradingHoursSettings{Windows: []ui.TradingWindow{
		{Start: "11:00", End: "13:00", Flatten: true}}}, http.StatusOK)
	s.bridge.set(protected(2, 101))
	at(14, 0)
	got = putHours(t, call, ui.TradingHoursSettings{Enabled: true, Windows: []ui.TradingWindow{
		{Start: "11:00", End: "13:00", Flatten: true}}}, http.StatusOK)
	assert.True(t, got.Status.Blocked)
	assert.Equal(t, 1, endActions(t, s), "no end for the window turned off")
	assert.Equal(t, []bridgePosition{protected(2, 101)}, s.bridge.held())
}

// A window for some agents: inside it, only they open positions, the others
// blocked with why; in a window for every agent, all of them free; an agent
// unknown refused; the agents kept by id across a rename.
func TestTradingHours_AWindowForSomeAgents(t *testing.T) {
	s, clock, _ := newDailySupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	s.liveAgent(t, "orb", 102, false)
	at := func(h, m int) { clock.set(time.Date(2026, 10, 29, h, m, 0, 0, belem(t))) }
	call, _ := settingsSession(t, s.managed)
	at(9, 0)
	got := putHours(t, call, ui.TradingHoursSettings{Enabled: true, Windows: []ui.TradingWindow{
		{Start: "08:00", End: "10:00", Agents: []string{"trend"}}, {Start: "12:00", End: "14:00"}}}, http.StatusOK)
	assert.Equal(t, []string{"trend"}, got.Settings.Windows[0].Agents)
	assert.Empty(t, got.Settings.Windows[1].Agents, "for every agent")
	assert.Empty(t, reasonOf(t, s, "trend"), "allowed in the window")
	assert.Equal(t, "Janela 08:00–10:00 só para trend", reasonOf(t, s, "orb"))

	at(12, 0)
	require.NoError(t, l.evaluate(ctx))
	assert.Empty(t, reasonOf(t, s, "trend"))
	assert.Empty(t, reasonOf(t, s, "orb"), "a window for every agent")

	putHours(t, call, ui.TradingHoursSettings{Enabled: true, Windows: []ui.TradingWindow{
		{Start: "08:00", End: "10:00", Agents: []string{"ghost"}}}}, http.StatusBadRequest)
	putHours(t, call, ui.TradingHoursSettings{Enabled: true, Windows: []ui.TradingWindow{
		{Start: "08:00", End: "10:00", Agents: []string{"orb", "orb"}}}}, http.StatusBadRequest)

	// renamed: still allowed, by id
	orb, err := s.store.AgentByName(ctx, "orb")
	require.NoError(t, err)
	putHours(t, call, ui.TradingHoursSettings{Enabled: true, Windows: []ui.TradingWindow{
		{Start: "12:00", End: "14:00", Agents: []string{"orb"}}}}, http.StatusOK)
	off := false
	_, err = s.store.UpdateAgent(ctx, orb.ID, store.AgentUpdate{Enabled: &off})
	require.NoError(t, err)
	_, err = s.store.RenameAgent(ctx, orb.ID, "orb2")
	require.NoError(t, err)
	hours, _, err := s.store.TradingHours(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"orb2"}, hours.Settings().Windows[0].Agents)
}

// A window for some assets: inside it, the agents told the only assets they
// may open positions on (their entries otherwise free), with why; in a window
// for every asset, none; an asset invalid or repeated refused.
func TestTradingHours_AWindowForSomeAssets(t *testing.T) {
	s, clock, _ := newDailySupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	at := func(h, m int) { clock.set(time.Date(2026, 10, 29, h, m, 0, 0, belem(t))) }
	call, _ := settingsSession(t, s.managed)
	entries := func() agentapi.Entries {
		t.Helper()
		a, err := s.store.AgentByName(ctx, "trend")
		require.NoError(t, err)
		got, err := entriesOf(ctx, s.store, a)
		require.NoError(t, err)
		return got
	}
	at(9, 0)
	got := putHours(t, call, ui.TradingHoursSettings{Enabled: true, Windows: []ui.TradingWindow{
		{Start: "08:00", End: "10:00", Pairs: []string{"XAUUSDm", "BTCUSDm"}}, {Start: "12:00", End: "14:00"}}},
		http.StatusOK)
	assert.Equal(t, []string{"BTCUSDm", "XAUUSDm"}, got.Settings.Windows[0].Pairs, "kept")
	first := entries()
	assert.False(t, first.EntriesBlocked, "free, on its assets")
	assert.ElementsMatch(t, []string{"XAUUSDm", "BTCUSDm"}, first.EntriesPairs)
	assert.Contains(t, first.PairsReason, "Janela 08:00–10:00 só para os ativos")

	at(12, 0)
	require.NoError(t, l.evaluate(ctx))
	second := entries()
	assert.Empty(t, second.EntriesPairs, "every asset in the second window")
	assert.Greater(t, second.EntriesRevision, first.EntriesRevision)

	for _, pairs := range [][]string{{"XAU USDm"}, {"XAUUSDm", "XAUUSDm"}} {
		putHours(t, call, ui.TradingHoursSettings{Enabled: true, Windows: []ui.TradingWindow{
			{Start: "08:00", End: "10:00", Pairs: pairs}}}, http.StatusBadRequest)
	}
}

// The pending orders canceled at the end without closing the positions: a
// cancel_pending of every agent, the positions left with their stops.
func TestTradingHours_CancelsOnlyThePendingOrders(t *testing.T) {
	s, clock, trend := newDailySupervised(t)
	ctx := context.Background()
	l := s.sup.daily
	s.bridge.set(protected(1, 101))
	trend.mu.Lock()
	trend.pending = []ui.SupervisedOrder{{ID: 11, Pair: "XAUUSDm", Side: "BUY", Type: "LIMIT", Entry: true}}
	trend.mu.Unlock()
	call, _ := settingsSession(t, s.managed)
	putHours(t, call, ui.TradingHoursSettings{Enabled: true, Start: "09:00", End: "17:30", CancelPending: true},
		http.StatusOK)

	clock.set(time.Date(2026, 10, 28, 18, 0, 0, 0, belem(t)))
	require.NoError(t, l.evaluate(ctx))
	require.Eventually(t, func() bool {
		l.evaluateHours(ctx)
		end := l.Status().Hours.LastEnd
		return end != nil && end.Status == store.ActionSucceeded
	}, 20*time.Second, 50*time.Millisecond)
	action, err := s.store.SupervisorAction(ctx, *l.Status().Hours.LastEnd.ActionID)
	require.NoError(t, err)
	assert.Equal(t, store.ActionCancelPending, action.Action)
	trend.mu.Lock()
	assert.Empty(t, trend.pending, "canceled")
	trend.mu.Unlock()
	assert.Equal(t, []bridgePosition{protected(1, 101)}, s.bridge.held(), "the position left with its stop")
}
