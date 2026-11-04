package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

const (
	// maxRuntimeWait bounds how long GET /runtime waits for a change.
	maxRuntimeWait = 30 * time.Second
	// maxOrderIDs bounds a lease of order IDs.
	maxOrderIDs = 100000
	// maxListed bounds the records a list of the internal API returns.
	maxListed = 5000
)

// agentKey is the key of the ID of the agent of a request in its context.
type agentKey struct{}

// agentOf returns the ID of the agent of the request, see authenticate.
func agentOf(r *http.Request) int64 {
	id, _ := r.Context().Value(agentKey{}).(int64)
	return id
}

// InternalHandler returns the handler of the internal API of the agents (see
// agentapi), to serve on the loopback only.
func (m *Manager) InternalHandler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Route(agentapi.Prefix, func(r chi.Router) {
		r.Use(m.authenticate)
		r.Get("/config", m.handleConfig)
		r.Get("/runtime", m.handleRuntime)
		r.Get("/prompt", m.handlePrompt)
		r.Post("/order-ids", m.handleOrderIDs)
		r.Get("/orders", m.handleOrders)
		r.Get("/discards", m.handleDiscards)
		r.Get("/decisions", m.handleDecisions)
		r.Get("/decisions/{id}", m.handleDecision)
		r.Get("/decisions/{id}/attachments/{index}", m.handleAttachment)
		r.Get("/decision-links", m.handleDecisionLinks)
		r.Get("/journal/memory", m.handleJournalMemory)
		r.Post("/outbox", m.handleOutbox)
		r.Post("/outbox/check", m.handleOutboxCheck)
		r.Post(agentapi.ExposurePath, m.handleExposureClaim)
	})
	return r
}

// authenticate lets through the requests carrying the token of a start of an
// agent, its ID in their context: nothing tells another agent.
func (m *Manager) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		id, valid := m.tokens.verify(strings.TrimSpace(token))
		if !ok || !valid {
			writeJSONError(w, http.StatusUnauthorized, "invalid token: the agent isn't the one the controller started")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, agentapi.MaxBody)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), agentKey{}, id)))
	})
}

// handleConfig answers the settings of the agent, its secrets included: the
// ones of its launch, on each start of it until it is restarted (see
// Manager.launchedAs), unless it would trade on MT5 the positions of another
// agent running (409, see Manager.admit).
func (m *Manager) handleConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	current, err := m.store.Agent(ctx, agentOf(r))
	if err != nil {
		m.fail(w, err)
		return
	}
	a := m.launchedAs(current)
	cfg, err := configOf(ctx, m.store, a)
	if err != nil {
		m.fail(w, err)
		return
	}
	// what it trades, kept apart from the positions of the agents running
	if err := m.admit(a, cfg.MT5Book()); err != nil {
		log.Errorf("controller: agent %s: %v", a.Name, err)
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, cfg)
}

// configOf returns the settings of the agent a, its secrets included, as it
// takes them from the controller: see agentapi.Config.
func configOf(ctx context.Context, st *store.Store, a store.Agent) (agentapi.Config, error) {
	secrets, err := st.SecretsOf(a)
	if err != nil {
		return agentapi.Config{}, err
	}
	protection, err := protectionOf(ctx, st, a)
	if err != nil {
		return agentapi.Config{}, err
	}
	// unknown, the agent doesn't start: it would trade a day its guard blocks
	entries, err := entriesOf(ctx, st, a)
	if err != nil {
		return agentapi.Config{}, err
	}
	cfg := agentapi.Config{ID: a.ID, Name: a.Name, Kind: a.Kind, Mode: a.Mode, APIAddress: a.APIAddress,
		TradePanel: a.TradePanel, TelegramControl: a.TelegramControl, Reanalysis: a.ReanalysisAllowed(),
		Entries: entries, Protection: protection, Params: a.Params}
	if acc := a.Account; acc != nil {
		cfg.Account = &agentapi.Account{BridgeURL: acc.BridgeURL, BridgeToken: secrets.BridgeToken, Magic: acc.Magic,
			Comment: acc.Comment, RealAllowed: acc.RealAllowed}
	}
	if cfg.News, err = newsOf(ctx, st); err != nil { // of every agent
		return agentapi.Config{}, err
	}
	if l := a.LLM; l != nil {
		cfg.LLM = &agentapi.LLM{Provider: l.Provider, Model: l.Model, APIKey: secrets.LLMAPIKey,
			BaseURL: deref(l.BaseURL), ReasoningEffort: deref(l.ReasoningEffort), Params: l.Params}
		if l.MaxTokens != nil {
			cfg.LLM.MaxTokens = *l.MaxTokens
		}
		if l.PromptID != nil {
			prompt, err := st.Prompt(ctx, *l.PromptID)
			if err != nil {
				return agentapi.Config{}, err
			}
			cfg.Prompt = &agentapi.Prompt{PromptRef: agentapi.PromptRef{ID: prompt.ID, Name: prompt.Name,
				Version: prompt.Version}, Content: prompt.Content}
		}
	}
	return cfg, nil
}

// handleRuntime answers the runtime of the agent: with If-None-Match, its
// version, and wait, eg: 25s, it waits for a change, 304 without one.
func (m *Manager) handleRuntime(w http.ResponseWriter, r *http.Request) {
	id := agentOf(r)
	doc, changed, ok := m.runtimes.get(id)
	if !ok {
		a, err := m.store.Agent(r.Context(), id)
		if err != nil {
			m.fail(w, err)
			return
		}
		if doc, ok = runtimeOf(r.Context(), m.store, a); !ok {
			writeJSONError(w, http.StatusServiceUnavailable, "the prompt of the agent can't be read now")
			return
		}
		m.runtimes.set(id, doc)
		doc, changed, _ = m.runtimes.get(id)
	}
	etag := strconv.FormatInt(doc.Version, 10)
	if strings.Trim(r.Header.Get("If-None-Match"), `"`) == etag {
		wait, _ := time.ParseDuration(r.URL.Query().Get("wait"))
		wait = min(max(wait, 0), maxRuntimeWait)
		select {
		case <-changed:
			doc, _, _ = m.runtimes.get(id)
			etag = strconv.FormatInt(doc.Version, 10)
		case <-time.After(wait):
			w.WriteHeader(http.StatusNotModified)
			return
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("ETag", `"`+etag+`"`)
	writeJSON(w, doc)
}

// handlePrompt answers the prompt the agent runs with, its content: 404 for
// its default one.
func (m *Manager) handlePrompt(w http.ResponseWriter, r *http.Request) {
	a, err := m.store.Agent(r.Context(), agentOf(r))
	if err != nil {
		m.fail(w, err)
		return
	}
	if a.LLM == nil || a.LLM.PromptID == nil {
		writeJSONError(w, http.StatusNotFound, "the agent runs with its default prompt")
		return
	}
	prompt, err := m.store.Prompt(r.Context(), *a.LLM.PromptID)
	if err != nil {
		m.fail(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"p%dv%d"`, prompt.ID, prompt.Version))
	writeJSON(w, agentapi.Prompt{PromptRef: agentapi.PromptRef{ID: prompt.ID, Name: prompt.Name,
		Version: prompt.Version}, Content: prompt.Content})
}

// handleOrderIDs leases a range of order IDs to the agent.
func (m *Manager) handleOrderIDs(w http.ResponseWriter, r *http.Request) {
	var request agentapi.OrderIDsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Count < 1 ||
		request.Count > maxOrderIDs {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("count: from 1 to %d order IDs", maxOrderIDs))
		return
	}
	first, last, err := m.store.LeaseOrderIDs(r.Context(), agentOf(r), request.Count)
	if err != nil {
		m.fail(w, err)
		return
	}
	writeJSON(w, agentapi.OrderIDs{First: first, Last: last})
}

// handleOrders answers the orders of the agent after the ID after, by ID,
// limit at most.
func (m *Manager) handleOrders(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	orders, err := m.store.Orders(r.Context(), agentOf(r), after, listLimit(r))
	if err != nil {
		m.fail(w, err)
		return
	}
	writeJSON(w, orders)
}

// handleDiscards answers the positions the order controller of the agent
// discarded, by the fill after which each one was.
func (m *Manager) handleDiscards(w http.ResponseWriter, r *http.Request) {
	discards, err := m.store.Discards(r.Context(), agentOf(r))
	if err != nil {
		m.fail(w, err)
		return
	}
	writeJSON(w, discards)
}

// handleDecisions answers the last decisions of the agent: of pair, limit at
// most, or with per_pair the last ones of each pair.
func (m *Manager) handleDecisions(w http.ResponseWriter, r *http.Request) {
	var (
		decisions []ui.Decision
		err       error
	)
	if perPair, _ := strconv.Atoi(r.URL.Query().Get("per_pair")); perPair > 0 {
		decisions, err = m.store.DecisionsPerPair(r.Context(), agentOf(r), min(perPair, maxListed))
	} else {
		decisions, err = m.store.Decisions(r.Context(), agentOf(r), r.URL.Query().Get("pair"), listLimit(r))
	}
	if err != nil {
		m.fail(w, err)
		return
	}
	writeJSON(w, decisions)
}

// handleDecision answers a decision of the agent.
func (m *Manager) handleDecision(w http.ResponseWriter, r *http.Request) {
	d, err := m.store.Decision(r.Context(), agentOf(r), chi.URLParam(r, "id"))
	if err != nil {
		m.fail(w, err)
		return
	}
	writeJSON(w, d)
}

// handleAttachment answers an attachment of a decision of the agent.
func (m *Manager) handleAttachment(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(chi.URLParam(r, "index"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid attachment index")
		return
	}
	mediaType, data, err := m.store.DecisionAttachment(r.Context(), agentOf(r), chi.URLParam(r, "id"), index)
	if err != nil {
		m.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	_, _ = w.Write(data)
}

// handleDecisionLinks answers the decision that placed each order of the
// agent.
func (m *Manager) handleDecisionLinks(w http.ResponseWriter, r *http.Request) {
	links, err := m.store.DecisionLinks(r.Context(), agentOf(r))
	if err != nil {
		m.fail(w, err)
		return
	}
	list := make([]agentapi.DecisionLink, 0, len(links))
	for order, decision := range links {
		list = append(list, agentapi.DecisionLink{OrderID: order, DecisionID: decision})
	}
	writeJSON(w, list)
}

// handleJournalMemory answers the last decisions of each pair of the journal
// of the agent, per_pair of them.
func (m *Manager) handleJournalMemory(w http.ResponseWriter, r *http.Request) {
	perPair, _ := strconv.Atoi(r.URL.Query().Get("per_pair"))
	if perPair < 1 {
		perPair = 20
	}
	remembered, err := m.store.JournalMemory(r.Context(), agentOf(r), min(perPair, maxListed))
	if err != nil {
		m.fail(w, err)
		return
	}
	list := make([]agentapi.Remembered, 0, len(remembered))
	for _, rem := range remembered {
		list = append(list, agentapi.Remembered{Pair: rem.Pair, Candle: rem.Candle, At: rem.At, Action: rem.Action,
			Confidence: rem.Confidence, Reason: rem.Reason})
	}
	writeJSON(w, list)
}

// handleOutbox writes a batch of records of the agent in a single
// transaction: 422 names the record refused for good, none written. What the
// outbox of the agent holds is kept for the dashboard, see Manager.Outbox.
func (m *Manager) handleOutbox(w http.ResponseWriter, r *http.Request) {
	m.serveOutbox(w, r, false)
}

// handleOutboxCheck answers a batch of records of the agent as handleOutbox
// would, none written: the agent checks that the records after one failing
// again and again are written, before setting that one apart.
func (m *Manager) handleOutboxCheck(w http.ResponseWriter, r *http.Request) {
	m.serveOutbox(w, r, true)
}

// errChecked rolls back the records of a check, see handleOutboxCheck.
var errChecked = errors.New("checked")

// serveOutbox writes a batch of records of the agent, or rolls them back once
// written when it checks them, see handleOutbox.
func (m *Manager) serveOutbox(w http.ResponseWriter, r *http.Request, check bool) {
	var request agentapi.OutboxRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	id := agentOf(r)
	refused := -1
	err := m.store.Transaction(r.Context(), func(ctx context.Context) error {
		for i, record := range request.Records {
			if err := m.write(ctx, id, record); err != nil {
				if refusedRecord(err) {
					refused = i
				}
				return err
			}
		}
		if check {
			return errChecked
		}
		return nil
	})
	if errors.Is(err, errChecked) {
		err = nil
	}
	what := "the records"
	if check {
		what = "the records checked, none written"
	} else {
		m.backlogs.report(id, request, err == nil)
	}
	switch {
	case refused >= 0:
		log.Warnf("controller: agent %d: %s: a %s record refused: %v", id, what, request.Records[refused].Kind, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(agentapi.OutboxError{Index: refused, Error: err.Error()})
	case err != nil:
		log.Errorf("controller: agent %d: %s: %v", id, what, err)
		writeJSONError(w, http.StatusServiceUnavailable, "the records can't be written now")
	default:
		var acked uint64
		if n := len(request.Records); n > 0 {
			acked = request.Records[n-1].Seq
		}
		writeJSON(w, agentapi.OutboxResponse{Acked: acked})
	}
}

// errRecord is the error of a record that can't be read.
var errRecord = errors.New("invalid record")

// write writes a record of the agent of id.
func (m *Manager) write(ctx context.Context, id int64, record agentapi.Record) error {
	switch record.Kind {
	case agentapi.KindOrder:
		var order model.Order
		if err := json.Unmarshal(record.Payload, &order); err != nil || order.ID < 1 {
			return fmt.Errorf("%w: order %s", errRecord, record.Key)
		}
		if err := m.store.SaveOrders(ctx, id, []model.Order{order}, true); err != nil {
			return err
		}
		// an order numbered past its lease, the controller out of reach: its
		// ID is never leased again
		return m.store.ReserveOrderIDs(ctx, id, order.ID)
	case agentapi.KindDecision:
		var d agentapi.DecisionRecord
		if err := json.Unmarshal(record.Payload, &d); err != nil || d.Decision.ID == "" {
			return fmt.Errorf("%w: decision %s", errRecord, record.Key)
		}
		attachments := make([]store.Attachment, 0, len(d.Attachments))
		for _, a := range d.Attachments {
			attachments = append(attachments, store.Attachment{MediaType: a.MediaType, Data: a.Data})
		}
		return m.store.SaveDecision(ctx, id, d.Decision, attachments, true)
	case agentapi.KindJournal:
		var e agentapi.JournalRecord
		if err := json.Unmarshal(record.Payload, &e); err != nil || e.ID == "" {
			return fmt.Errorf("%w: journal entry %s", errRecord, record.Key)
		}
		return m.store.AppendJournal(ctx, id, []store.JournalEntry{{ID: e.ID, At: e.At, Pair: e.Pair,
			Trigger: e.Trigger, RequestedBy: e.RequestedBy, Model: e.Model, Prompt: e.Prompt, LatencyMS: e.LatencyMS,
			Input: e.Input, Decision: e.Decision, Error: e.Error}})
	case agentapi.KindDiscard:
		var discard model.Discard
		if err := json.Unmarshal(record.Payload, &discard); err != nil {
			return fmt.Errorf("%w: discard %s", errRecord, record.Key)
		}
		return m.store.SaveDiscard(ctx, id, discard)
	case agentapi.KindPromptStatus:
		var s agentapi.PromptStatus
		if err := json.Unmarshal(record.Payload, &s); err != nil {
			return fmt.Errorf("%w: prompt status", errRecord)
		}
		return m.store.SetPromptStatus(ctx, id, store.PromptStatus{PromptID: s.PromptID, Name: s.Name,
			Version: s.Version, Applied: s.Applied, Error: s.Error, At: s.At})
	}
	return fmt.Errorf("%w: unknown kind %q", errRecord, record.Kind)
}

// refusedRecord reports whether err refuses a record for good: unreadable, or
// its data refused by the database, rather than the database out of reach.
func refusedRecord(err error) bool {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, errRecord), errors.Is(err, store.ErrInvalid),
		errors.Is(err, gorm.ErrCheckConstraintViolated), errors.Is(err, gorm.ErrForeignKeyViolated):
		return true
	case errors.As(err, &pgErr):
		// data exceptions, integrity constraint violations, and the limits of
		// the database, eg: an index row too large (54000): the same data fails
		// the same way again
		return strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23") ||
			strings.HasPrefix(pgErr.Code, "54")
	}
	return false
}

// fail answers the error of the store: 404 for a record not found, 500 else,
// its detail logged.
func (m *Manager) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	log.Errorf("controller: internal API: %v", err)
	writeJSONError(w, http.StatusInternalServerError, "the controller can't answer now")
}

// listLimit returns the limit of a list request, maxListed at most.
func listLimit(r *http.Request) int {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > maxListed {
		return maxListed
	}
	return limit
}

// deref returns the value of a pointer, "" for nil.
func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
