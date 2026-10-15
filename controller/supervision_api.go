package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The lists of the supervisor on the dashboard, at most.
const (
	maxRuns     = 50
	maxActions  = 100
	maxMessages = 100
)

// routes serves the supervisor on the dashboard, to its users, as the
// settings: a change needs the CSRF token of the session (see auth.protect).
func (s *Supervision) routes(r chi.Router) {
	r.Get("/settings/supervisor", s.handleSettings)
	r.Patch("/settings/supervisor", s.handleUpdateSettings)
	r.Get("/supervisor/overview", s.handleOverview)
	r.Post("/supervisor/run", s.handleRun)
	r.Get("/supervisor/runs", s.handleRuns)
	r.Get("/supervisor/runs/{id}", s.handleRunDetail)
	r.Get("/supervisor/actions", s.handleActions)
	r.Post("/supervisor/actions/{id}/confirm", s.handleConfirm)
	r.Post("/supervisor/actions/{id}/reject", s.handleReject)
	r.Get("/supervisor/messages", s.handleMessages)
	r.Post("/supervisor/messages", s.handleAsk)
	r.Post("/supervisor/messages/stream", s.handleAskStream)
	r.Post("/supervisor/agents/{name}/entries", s.handleEntries)
}

// fail answers an error of the supervisor: 404, 409, 410 or 400 for the ones
// of the request, 500 else, its detail logged.
func (s *Supervision) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, store.ErrDecided), errors.Is(err, errAnalyzing):
		writeJSONError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrExpired):
		writeJSONError(w, http.StatusGone, "a proposta expirou: peça uma nova ao XTrader AI")
	case errors.Is(err, store.ErrInvalid), errors.Is(err, errEmptyMessage), errors.Is(err, errNotConfigured):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, context.Canceled):
		writeJSONError(w, http.StatusServiceUnavailable, ErrStopped.Error())
	default:
		log.Errorf("ui: supervisor: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "the supervisor can't answer now")
	}
}

// unix returns t in Unix seconds, nil for none.
func unix(t *time.Time) *int64 {
	if t == nil || t.IsZero() {
		return nil
	}
	value := t.Unix()
	return &value
}

// deref returns the value of a pointer, the zero value for nil.
func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// supervisionInfo returns the supervision of the agent a for the dashboard,
// nil for an agent never supervised.
func supervisionInfo(a store.Agent) *ui.AgentSupervisionInfo {
	if a.Supervision == nil {
		return nil
	}
	sv := *a.Supervision
	info := &ui.AgentSupervisionInfo{EntriesBlocked: sv.EntriesBlocked, StopState: sv.StopState}
	if sv.EntriesBlocked {
		info.EntriesReason, info.EntriesBy, info.EntriesAt = sv.EntriesReason, sv.EntriesBy, unix(sv.EntriesAt)
	}
	if sv.StopState != store.StopNone {
		info.StopReason, info.StopBy, info.StopAt = sv.StopReason, sv.StopBy, unix(sv.StopAt)
		info.StopCategory = deref(sv.StopCategory)
	}
	return info
}

// settingsResponse returns the settings of the supervisor and its state.
func (s *Supervision) settingsResponse(settings store.SupervisorSettings, found bool) ui.SupervisorSettingsResponse {
	analyzing, next, failures := s.state()
	response := ui.SupervisorSettingsResponse{Configured: found, Enabled: settings.Enabled,
		IntervalSeconds: settings.IntervalSeconds, Provider: deref(settings.Provider), Model: deref(settings.Model),
		BaseURL: deref(settings.BaseURL), ReasoningEffort: deref(settings.ReasoningEffort),
		MaxTokens: derefInt(settings.MaxTokens), TimeoutSeconds: settings.TimeoutSeconds,
		Instructions: settings.Instructions, APIKeySet: len(settings.APIKey) > 0,
		Autonomy: ui.SupervisorAutonomy{BlockEntries: settings.Autonomy.BlockEntries,
			StopWhenFlat: settings.Autonomy.StopWhenFlat, CancelPending: settings.Autonomy.CancelPending,
			MaxActionsPerRun: settings.Autonomy.MaxActionsPerRun},
		MinInterval: store.SupervisorMinInterval, MaxInterval: store.SupervisorMaxInterval,
		MinTimeout: store.SupervisorMinTimeout, MaxTimeout: store.SupervisorMaxTimeout,
		MaxActions: store.SupervisorMaxActions, DefaultMaxTokens: llm.DefaultMaxTokens, Analyzing: analyzing,
		FailuresInRow: failures, UpdatedAt: settings.UpdatedAt.Unix()}
	if !next.IsZero() {
		at := next.Unix()
		response.NextRunAt = &at
	}
	return response
}

// handleSettings answers the settings of the supervisor, never its key.
func (s *Supervision) handleSettings(w http.ResponseWriter, r *http.Request) {
	settings, found, err := s.store.Supervisor(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, s.settingsResponse(settings, found))
}

// handleUpdateSettings changes the settings of the supervisor, see
// ui.SupervisorSettingsRequest, applied at once.
func (s *Supervision) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var request ui.SupervisorSettingsRequest
	if !decode(w, r, &request) {
		return
	}
	update := store.SupervisorUpdate{Enabled: request.Enabled, IntervalSeconds: request.IntervalSeconds,
		Provider: request.Provider, Model: request.Model, BaseURL: request.BaseURL,
		ReasoningEffort: request.ReasoningEffort, MaxTokens: request.MaxTokens,
		TimeoutSeconds: request.TimeoutSeconds, Instructions: request.Instructions, APIKey: request.APIKey}
	if a := request.Autonomy; a != nil {
		update.Autonomy = &store.Autonomy{BlockEntries: a.BlockEntries, StopWhenFlat: a.StopWhenFlat,
			CancelPending: a.CancelPending, MaxActionsPerRun: a.MaxActionsPerRun}
	}
	settings, err := s.store.SetSupervisor(r.Context(), update)
	if err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the settings of the supervisor changed by %s", userOf(r.Context()))
	s.Trigger()
	writeJSON(w, s.settingsResponse(settings, true))
}

// handleOverview answers what the supervisor knows of the agents now, as its
// model gets it.
func (s *Supervision) handleOverview(w http.ResponseWriter, r *http.Request) {
	v, err := s.chatView(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, v.snapshot)
}

// handleRun starts an analysis now, answering at once (202): the list of the
// analyses tells how it went.
func (s *Supervision) handleRun(w http.ResponseWriter, r *http.Request) {
	if analyzing, _, _ := s.state(); analyzing {
		s.fail(w, errAnalyzing)
		return
	}
	if _, _, err := s.settingsAndChat(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	by := userOf(r.Context())
	go func() {
		if _, err := s.Analyze(context.WithoutCancel(r.Context()), store.RunManual, by); err != nil {
			log.Warnf("supervisor: the analysis asked by %s: %v", by, err)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]bool{"started": true})
}

// runInfo returns the analysis run for the dashboard, with the actions it
// asked for.
func runInfo(run store.SupervisorRun, actions []int64) ui.SupervisorRunInfo {
	info := ui.SupervisorRunInfo{ID: run.ID, Origin: run.Origin, Status: run.Status, RequestedBy: run.RequestedBy,
		Model: run.Model, StartedAt: run.StartedAt.Unix(), FinishedAt: unix(run.FinishedAt), Summary: run.Summary,
		Findings: []ui.SupervisorFinding{}, Error: run.Error, ActionIDs: actions}
	if len(run.Findings) > 0 {
		_ = json.Unmarshal(run.Findings, &info.Findings)
	}
	return info
}

// handleRuns lists the last analyses, the newest first, with the actions each
// one asked for.
func (s *Supervision) handleRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.SupervisorRuns(r.Context(), maxRuns)
	if err != nil {
		s.fail(w, err)
		return
	}
	actions, err := s.store.SupervisorActions(r.Context(), maxActions)
	if err != nil {
		s.fail(w, err)
		return
	}
	byRun := make(map[int64][]int64)
	for _, action := range actions {
		if action.RunID != nil {
			byRun[*action.RunID] = append(byRun[*action.RunID], action.ID)
		}
	}
	response := ui.SupervisorRunsResponse{Runs: make([]ui.SupervisorRunInfo, 0, len(runs))}
	for _, run := range runs {
		response.Runs = append(response.Runs, runInfo(run, byRun[run.ID]))
	}
	writeJSON(w, response)
}

// handleRunDetail answers an analysis and the data sent to its model.
func (s *Supervision) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	run, err := s.store.SupervisorRun(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	response := ui.SupervisorRunResponse{Run: runInfo(run, nil)}
	if len(run.Snapshot) > 0 {
		var snapshot ui.SupervisorSnapshot
		if json.Unmarshal(run.Snapshot, &snapshot) == nil {
			response.Snapshot = &snapshot
		}
	}
	writeJSON(w, response)
}

// actionInfo returns the action a for the dashboard.
func actionInfo(a store.SupervisorAction) ui.SupervisorActionInfo {
	scope, options := scopeOf(a)
	if scope.Agents == nil {
		scope.Agents = []string{}
	}
	info := ui.SupervisorActionInfo{ID: a.ID, Source: a.Source, RunID: a.RunID, Action: a.Action, Mode: a.Mode,
		Scope: scope, Options: options, Reason: a.Reason, Category: a.Category, Evidence: []string{},
		Status: a.Status, RequestedBy: a.RequestedBy, ConfirmedBy: a.ConfirmedBy, CreatedAt: a.CreatedAt.Unix(),
		ExpiresAt: unix(a.ExpiresAt), DecidedAt: unix(a.DecidedAt), FinishedAt: unix(a.FinishedAt)}
	_ = json.Unmarshal(a.Evidence, &info.Evidence)
	if len(a.Result) > 0 && string(a.Result) != "null" {
		var result ui.SupervisorResult
		if json.Unmarshal(a.Result, &result) == nil {
			info.Result = &result
		}
	}
	return info
}

// handleActions lists the last actions of the supervision, the newest first.
func (s *Supervision) handleActions(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.ExpireSupervisorActions(r.Context()); err != nil {
		log.Warnf("supervisor: expiring the proposals: %v", err)
	}
	actions, err := s.store.SupervisorActions(r.Context(), maxActions)
	if err != nil {
		s.fail(w, err)
		return
	}
	response := ui.SupervisorActionsResponse{Actions: make([]ui.SupervisorActionInfo, 0, len(actions))}
	for _, action := range actions {
		response.Actions = append(response.Actions, actionInfo(action))
	}
	writeJSON(w, response)
}

// messageInfo returns the message m for the dashboard, with its action, if
// any.
func (s *Supervision) messageInfo(ctx context.Context, m store.SupervisorMessage) ui.SupervisorMessageInfo {
	info := ui.SupervisorMessageInfo{ID: m.ID, Role: m.Role, Kind: m.Kind, Content: m.Content,
		DataAt: unix(m.DataAt), CreatedAt: m.CreatedAt.Unix()}
	if m.ActionID != nil {
		if action, err := s.store.SupervisorAction(ctx, *m.ActionID); err == nil {
			a := actionInfo(action)
			info.Action = &a
		}
	}
	return info
}

// messagesResponse returns messages for the dashboard.
func (s *Supervision) messagesResponse(ctx context.Context, messages []store.SupervisorMessage,
	more bool) ui.SupervisorMessagesResponse {
	response := ui.SupervisorMessagesResponse{Messages: make([]ui.SupervisorMessageInfo, 0, len(messages)),
		More: more}
	for _, m := range messages {
		response.Messages = append(response.Messages, s.messageInfo(ctx, m))
	}
	return response
}

// handleMessages answers the conversation of the user, the last ones, or the
// ones before ?before=<id>, the oldest first.
func (s *Supervision) handleMessages(w http.ResponseWriter, r *http.Request) {
	var before int64
	if value := r.URL.Query().Get("before"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 1 {
			writeJSONError(w, http.StatusBadRequest, "invalid before")
			return
		}
		before = parsed
	}
	if _, err := s.store.ExpireSupervisorActions(r.Context()); err != nil {
		log.Warnf("supervisor: expiring the proposals: %v", err)
	}
	messages, err := s.store.SupervisorMessages(r.Context(), userIDOf(r.Context()), before, maxMessages+1)
	if err != nil {
		s.fail(w, err)
		return
	}
	more := len(messages) > maxMessages
	if more {
		messages = messages[1:]
	}
	writeJSON(w, s.messagesResponse(r.Context(), messages, more))
}

// handleAsk answers a message of the user, see Ask, with the messages it added.
func (s *Supervision) handleAsk(w http.ResponseWriter, r *http.Request) {
	var request ui.SupervisorMessageRequest
	if !decode(w, r, &request) {
		return
	}
	userID := userIDOf(r.Context())
	if userID == 0 {
		writeJSONError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	added, err := s.Ask(r.Context(), userID, userOf(r.Context()), request.Text)
	if err != nil && len(added) == 0 {
		s.fail(w, err)
		return
	}
	if err != nil {
		log.Errorf("supervisor: the conversation of %s: %v", userOf(r.Context()), err)
	}
	writeJSON(w, s.messagesResponse(r.Context(), added, false))
}

// confirmResponse is the answer of a confirmation or a rejection: the action as
// it ended and the message of the conversation telling it.
type confirmResponse struct {
	Action  ui.SupervisorActionInfo   `json:"action"`
	Message *ui.SupervisorMessageInfo `json:"message,omitempty"`
}

// handleConfirm executes a proposal, confirmed by the user, and answers its
// result, checked: 409 when decided already, 410 once expired.
func (s *Supervision) handleConfirm(w http.ResponseWriter, r *http.Request) {
	s.decide(w, r, s.Confirm)
}

// handleReject rejects a proposal, see handleConfirm.
func (s *Supervision) handleReject(w http.ResponseWriter, r *http.Request) {
	s.decide(w, r, s.Reject)
}

// decide decides the proposal of the path with decision, by the user.
func (s *Supervision) decide(w http.ResponseWriter, r *http.Request, decision func(context.Context, int64, int64,
	string) (store.SupervisorAction, store.SupervisorMessage, error)) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	by := userOf(r.Context())
	action, message, err := decision(r.Context(), id, userIDOf(r.Context()), by)
	if err != nil && action.ID == 0 {
		s.fail(w, err)
		return
	}
	if err != nil && (errors.Is(err, store.ErrDecided) || errors.Is(err, store.ErrExpired)) {
		s.fail(w, err)
		return
	}
	if err != nil {
		log.Errorf("supervisor: the action %d: %v", id, err)
	}
	log.Infof("ui: the action %d of the supervision decided by %s: %s", id, by, action.Status)
	response := confirmResponse{Action: actionInfo(action)}
	if message.ID != 0 {
		info := s.messageInfo(r.Context(), message)
		response.Message = &info
	}
	writeJSON(w, response)
}

// handleEntries blocks the entries of an agent or allows them again, asked by
// the user, and answers the action with its result, checked.
func (s *Supervision) handleEntries(w http.ResponseWriter, r *http.Request) {
	var request ui.SupervisorEntriesRequest
	if !decode(w, r, &request) {
		return
	}
	a, err := s.store.AgentByName(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	by := userOf(r.Context())
	userID := userIDOf(r.Context())
	action := store.SupervisorAction{Source: store.SourceUser, Action: store.ActionAllowEntries,
		Reason: "entradas liberadas por " + by, Status: store.ActionExecuting, RequestedBy: by, ConfirmedBy: by}
	if userID != 0 {
		action.UserID = &userID
	}
	if request.Blocked {
		action.Action = store.ActionBlockEntries
		action.Reason = strings.TrimSpace(request.Reason)
		if action.Reason == "" {
			action.Reason = "entradas bloqueadas por " + by
		}
	}
	action.Scope, _ = json.Marshal(ui.SupervisorScope{Agents: []string{a.Name}})
	created, err := s.store.CreateSupervisorAction(r.Context(), action)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.execute(r.Context(), created)
	created, err = s.store.SupervisorAction(r.Context(), created.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the entries of the agent %s %s by %s", a.Name, created.Action, by)
	writeJSON(w, confirmResponse{Action: actionInfo(created)})
}
