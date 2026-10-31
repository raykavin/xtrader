package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// hoursBy is who the trading hours act as, in the records.
const hoursBy = "horário de operação"

// evaluateHours applies the trading hours now: off, the entries freed when
// their window had blocked them (released); on, the entries freed as the
// window opens (opened) and blocked as it closes (closed, sent to the agents
// running at once), and, when it closes after it was open (at its end, or
// later when the controller was down then), the positions of the agents closed
// and their pending orders canceled as checked (see endHours). Each change is
// recorded once: its event is the revision of the entries of the agents.
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
	status := ui.TradingHoursStatus{Enabled: settings.Enabled, Zone: l.zone.String(), Open: true}
	var record *store.TradingHoursEvent
	end := false
	if !settings.Enabled {
		if blocked {
			record = &store.TradingHoursEvent{Kind: store.HoursReleased,
				Reason: "horário de operação desligado: entradas liberadas"}
		}
	} else {
		status.Start, status.End = settings.Start, settings.End
		open, next := agentapi.TradingWindow(settings.Start, settings.End, l.s.now(), l.zone)
		status.Open, status.NextAt = open, next.Unix()
		switch {
		case open && (!found || last.Kind != store.HoursOpened):
			record = &store.TradingHoursEvent{Kind: store.HoursOpened,
				Reason: fmt.Sprintf("horário de operação aberto (%s–%s)", settings.Start, settings.End)}
		case !open && !blocked:
			record = &store.TradingHoursEvent{Kind: store.HoursClosed,
				Reason: fmt.Sprintf("Fora do horário de operação (%s–%s): entradas bloqueadas até %s",
					settings.Start, settings.End, settings.Start)}
			end = found && last.Kind == store.HoursOpened && (settings.Flatten || settings.CancelPending)
		}
	}
	if record != nil {
		record.RequestedBy = "system"
		if err := l.s.store.RecordTradingHours(ctx, record); err != nil {
			log.Errorf("trading hours: %v", err)
			return
		}
		blocked = record.Kind == store.HoursClosed
		log.Warnf("trading hours: %s", record.Reason)
		l.publish(ctx, nil, blocked)
	}
	status.Blocked = blocked
	if end {
		l.endHours(ctx, settings)
	}
	if ended, ok, err := l.s.store.LastTradingHoursEnd(ctx); err != nil {
		log.Errorf("trading hours: %v", err)
	} else if ok {
		var asked ui.TradingHoursSettings
		_ = json.Unmarshal(ended.Next, &asked)
		status.LastEnd = &ui.TradingHoursEnd{At: ended.At.Unix(), Flatten: asked.Flatten,
			CancelPending: asked.CancelPending, Status: ended.Status, ActionID: ended.ActionID}
	}
	l.mu.Lock()
	l.hours = status
	l.mu.Unlock()
}

// endHours closes the positions of the agents and cancels their pending
// orders at the end of the window of the trading hours, as settings check
// them, in the background, through an action of the XTrader AI (see
// Supervision.execute): a close_positions of every position of theirs read
// then (with their pending entries when checked), or a cancel_pending only;
// recorded (an end_action event) with how it ended.
func (l *dailyLimiter) endHours(ctx context.Context, settings ui.TradingHoursSettings) {
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
	if settings.Flatten {
		what = append(what, "posições zeradas")
	}
	if settings.CancelPending {
		what = append(what, "ordens pendentes canceladas")
	}
	reason := fmt.Sprintf("Fim do horário de operação (%s): %s", settings.End, strings.Join(what, " e "))
	event := &store.TradingHoursEvent{Kind: store.HoursEndAction, RequestedBy: "system", Reason: reason,
		Next:   mustJSON(ui.TradingHoursSettings{Flatten: settings.Flatten, CancelPending: settings.CancelPending}),
		Status: store.ActionExecuting}
	if err := l.s.store.RecordTradingHours(ctx, event); err != nil {
		log.Errorf("trading hours: %v", err)
		return
	}
	action := store.SupervisorAction{Source: store.SourceSupervisor, Action: store.ActionCancelPending,
		Reason: reason, Category: store.CategoryRisk, Status: store.ActionExecuting, RequestedBy: hoursBy,
		ConfirmedBy: hoursBy}
	action.Scope, _ = json.Marshal(ui.SupervisorScope{Agents: names})
	if settings.Flatten {
		action.Action = store.ActionClosePositions
		action.Options, _ = json.Marshal(ui.SupervisorOptions{Whole: true, CancelPending: settings.CancelPending})
	}
	ctx = context.WithoutCancel(ctx)
	go func() {
		defer l.Reevaluate()
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
