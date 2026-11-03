package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// hoursBy is who the trading hours act as, in the records.
const hoursBy = "horário de operação"

// evaluateHours applies the trading hours now: off, the entries freed when
// their windows had blocked them, or the window open forgotten (released); on,
// the entries freed as a window opens (opened, with the window) and blocked
// as it closes (closed, sent to the agents running at once), and, at the end
// of a window that was open (or later when the controller was down then, even
// inside the next window), the positions of the agents closed and their
// pending orders canceled as that window checks (see endHours), the next
// window opened once they end. Each change is recorded once: its event is the
// revision of the entries of the agents.
func (l *dailyLimiter) evaluateHours(ctx context.Context) {
	l.hoursMu.Lock()
	defer l.hoursMu.Unlock()
	hours, _, err := l.s.store.TradingHours(ctx)
	if err != nil {
		log.Errorf("trading hours: %v", err)
		return
	}
	last, found, err := l.s.store.TradingHoursState(ctx)
	if err != nil {
		log.Errorf("trading hours: %v", err)
		return
	}
	settings := hours.Settings()
	blocked := found && last.Kind == store.HoursClosed
	opened := found && last.Kind == store.HoursOpened
	status := ui.TradingHoursStatus{Enabled: settings.Enabled, Zone: l.zone.String(), Open: true}
	var record *store.TradingHoursEvent
	var ending *ui.TradingWindow // the window whose end acts now
	if !settings.Enabled {
		if blocked || opened {
			record = &store.TradingHoursEvent{Kind: store.HoursReleased,
				Reason: "horário de operação desligado: entradas liberadas"}
		}
	} else {
		now := l.s.now()
		open, upcoming, next := agentapi.TradingWindows(settings.Windows, now, l.zone)
		status.Windows, status.Open, status.NextAt = settings.Windows, open >= 0, next.Unix()
		shown := upcoming
		if open >= 0 {
			shown = open
			w := settings.Windows[open]
			status.Window = &w
		}
		if shown >= 0 {
			status.Start, status.End = settings.Windows[shown].Start, settings.Windows[shown].End
		}
		var was ui.TradingWindow // the window open, as recorded
		over := false            // and over: ended, its actions due
		if opened {
			was = openedWindow(last, settings.Windows, l.zone)
			over = !now.Before(agentapi.WindowEnd(was, last.At, l.zone))
		}
		switch {
		case opened && (over || open < 0) && open >= 0 && !was.Flatten && !was.CancelPending:
			// the window open ended without actions, now inside the next one
			// (the controller down then): the next one opened
			w := settings.Windows[open]
			record = &store.TradingHoursEvent{Kind: store.HoursOpened, Next: mustJSON(w),
				Reason: fmt.Sprintf("horário de operação aberto (%s)", agentapi.WindowLabel(w))}
		case opened && (over || open < 0):
			// the window open ended, or was changed so that now is outside every
			// window: the entries blocked while its actions run, the next window
			// opened after them
			record = &store.TradingHoursEvent{Kind: store.HoursClosed, Reason: closedReason(was, next, open >= 0)}
			if was.Flatten || was.CancelPending {
				ending = &was
			}
		case open >= 0 && !l.hoursEnding.Load() && (!opened || !agentapi.SameWindow(was, settings.Windows[open])):
			// a window opened, or the one open changed (still inside it): its
			// actions as it checks them now
			w := settings.Windows[open]
			record = &store.TradingHoursEvent{Kind: store.HoursOpened, Next: mustJSON(w),
				Reason: fmt.Sprintf("horário de operação aberto (%s)", agentapi.WindowLabel(w))}
		case open < 0 && !blocked && !opened:
			// outside every window, as the hours are turned on or saved
			record = &store.TradingHoursEvent{Kind: store.HoursClosed, Reason: closedReason(ui.TradingWindow{}, next,
				false)}
		}
	}
	if record != nil {
		record.RequestedBy, record.At = "system", l.s.now() // the clock the windows are weighed on
		if err := l.s.store.RecordTradingHours(ctx, record); err != nil {
			log.Errorf("trading hours: %v", err)
			return
		}
		blocked = record.Kind == store.HoursClosed
		log.Warnf("trading hours: %s", record.Reason)
		l.publish(ctx, nil, blocked)
	}
	status.Blocked = blocked
	if ending != nil {
		l.endHours(ctx, *ending)
	}
	if ended, ok, err := l.s.store.LastTradingHoursEnd(ctx); err != nil {
		log.Errorf("trading hours: %v", err)
	} else if ok {
		var asked ui.TradingWindow
		_ = json.Unmarshal(ended.Next, &asked)
		status.LastEnd = &ui.TradingHoursEnd{At: ended.At.Unix(), Start: asked.Start, End: asked.End,
			Flatten: asked.Flatten, CancelPending: asked.CancelPending, Status: ended.Status, ActionID: ended.ActionID}
	}
	l.mu.Lock()
	l.hours = status
	l.mu.Unlock()
}

// openedWindow returns the window an opened event opened: as it records it,
// else (an event of a single window, before) the window of windows its time
// was inside, else the first one.
func openedWindow(event store.TradingHoursEvent, windows []ui.TradingWindow, zone *time.Location) ui.TradingWindow {
	var w ui.TradingWindow
	if len(event.Next) > 0 && json.Unmarshal(event.Next, &w) == nil && w.Start != "" && w.End != "" {
		return w
	}
	if i, _, _ := agentapi.TradingWindows(windows, event.At, zone); i >= 0 {
		return windows[i]
	}
	if len(windows) > 0 {
		return windows[0]
	}
	return w
}

// closedReason returns why the entries are blocked: the window ended (its
// actions running before the next window, already open, opens) or outside
// every window, until the next start (next).
func closedReason(ended ui.TradingWindow, next time.Time, inside bool) string {
	switch {
	case ended.Start != "" && inside:
		return fmt.Sprintf("Fim da janela %s: entradas bloqueadas até as posições serem fechadas",
			agentapi.WindowLabel(ended))
	case ended.Start != "":
		return fmt.Sprintf("Fim da janela %s: entradas bloqueadas até %s", agentapi.WindowLabel(ended),
			next.Format("15:04"))
	}
	return fmt.Sprintf("Fora do horário de operação: entradas bloqueadas até %s", next.Format("15:04"))
}

// endHours closes the positions of the agents and cancels their pending
// orders at the end of the window w of the trading hours, as it checks them,
// in the background, through an action of the XTrader AI (see
// Supervision.execute): a close_positions of every position of theirs read
// then (with their pending entries when checked), or a cancel_pending only;
// recorded (an end_action event, with the window) with how it ended. The next
// window doesn't open while it runs (hoursEnding).
func (l *dailyLimiter) endHours(ctx context.Context, w ui.TradingWindow) {
	agents, err := l.s.store.Agents(ctx)
	if err != nil {
		log.Errorf("trading hours: %v", err)
		return
	}
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		names = append(names, a.Name)
	}
	var what []string
	if w.Flatten {
		what = append(what, "posições zeradas")
	}
	if w.CancelPending {
		what = append(what, "ordens pendentes canceladas")
	}
	reason := fmt.Sprintf("Fim da janela %s do horário de operação: %s", agentapi.WindowLabel(w),
		strings.Join(what, " e "))
	event := &store.TradingHoursEvent{Kind: store.HoursEndAction, RequestedBy: "system", Reason: reason,
		Next: mustJSON(w), Status: store.ActionExecuting}
	if err := l.s.store.RecordTradingHours(ctx, event); err != nil {
		log.Errorf("trading hours: %v", err)
		return
	}
	action := store.SupervisorAction{Source: store.SourceSupervisor, Action: store.ActionCancelPending,
		Reason: reason, Category: store.CategoryRisk, Status: store.ActionExecuting, RequestedBy: hoursBy,
		ConfirmedBy: hoursBy}
	action.Scope, _ = json.Marshal(ui.SupervisorScope{Agents: names})
	if w.Flatten {
		action.Action = store.ActionClosePositions
		action.Options, _ = json.Marshal(ui.SupervisorOptions{Whole: true, CancelPending: w.CancelPending})
	}
	ctx = context.WithoutCancel(ctx)
	l.hoursEnding.Store(true)
	go func() {
		defer l.Reevaluate()
		defer l.hoursEnding.Store(false)
		created, err := l.s.store.CreateSupervisorAction(ctx, action)
		if err != nil {
			log.Errorf("trading hours: recording the actions of the end: %v", err)
			_ = l.s.store.SetTradingHoursEnd(ctx, event.ID, store.ActionFailed, nil)
			return
		}
		status, _ := l.s.execute(ctx, created)
		if err := l.s.store.SetTradingHoursEnd(ctx, event.ID, status, &created.ID); err != nil {
			log.Errorf("trading hours: %v", err)
		}
		log.Warnf("trading hours: %s (action %d): %s", reason, created.ID, status)
	}()
}

// mustJSON returns v as JSON.
func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// tradingHoursResponse returns the trading hours and how they stand.
func (s *Supervision) tradingHoursResponse(ctx context.Context) (ui.TradingHoursResponse, error) {
	hours, _, err := s.store.TradingHours(ctx)
	if err != nil {
		return ui.TradingHoursResponse{}, err
	}
	return ui.TradingHoursResponse{Settings: hours.Settings(), Status: s.daily.Status().Hours}, nil
}

// handleTradingHours answers the trading hours, see ui.TradingHoursResponse.
func (s *Supervision) handleTradingHours(w http.ResponseWriter, r *http.Request) {
	response, err := s.tradingHoursResponse(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, response)
}

// handleSetTradingHours sets the trading hours, by the user of the session,
// applied at once (see dailyLimiter.evaluateHours): the entries blocked or
// freed as the window now says.
func (s *Supervision) handleSetTradingHours(w http.ResponseWriter, r *http.Request) {
	var request ui.TradingHoursRequest
	if !decode(w, r, &request) {
		return
	}
	request.Settings.Start = strings.TrimSpace(request.Settings.Start)
	request.Settings.End = strings.TrimSpace(request.Settings.End)
	for i, w := range request.Settings.Windows {
		request.Settings.Windows[i].Start, request.Settings.Windows[i].End = strings.TrimSpace(w.Start),
			strings.TrimSpace(w.End)
	}
	var userID *int64
	if id := userIDOf(r.Context()); id != 0 {
		userID = &id
	}
	by := userOf(r.Context())
	if _, _, err := s.store.SetTradingHours(r.Context(), request.Settings, by, userID,
		strings.TrimSpace(request.Reason)); err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the trading hours changed by %s", by)
	s.daily.evaluateHours(r.Context())
	response, err := s.tradingHoursResponse(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, response)
}
