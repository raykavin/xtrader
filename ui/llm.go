package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/raykavin/xtrader/strategy"
)

// DecisionSource is a strategy keeping the decisions of its language model with
// their justification, see strategy.LLMStrategy: the dashboard lists them and
// shows the one of an order. Called from the handlers of the chart, its methods
// must be safe for concurrent use.
type DecisionSource interface {
	// Decisions returns the last decisions of pair, newest first, limit at
	// most; the ones of every pair when pair is empty.
	Decisions(pair string, limit int) ([]Decision, error)

	// Decision returns the decision of id, false when unknown.
	Decision(id string) (Decision, bool, error)

	// OrderDecision returns the decision that placed the order of orderID,
	// empty for none: a leg moved later keeps it. The chart calls it for every order it sends,
	// holding its lock: it must not block, nor call the chart.
	OrderDecision(orderID int64) string

	// Attachment returns the content of the attachment index of the decision
	// id and its media type, ErrUnknownDecision when there is none.
	Attachment(id string, index int) (mediaType string, data []byte, err error)

	// ListenDecisions calls listener with every decision the source keeps
	// from then on, and again as the orders it places are linked to it.
	ListenDecisions(listener func(Decision))
}

// ErrUnknownDecision is the error of DecisionSource.Attachment for a decision,
// or an attachment, unknown.
var ErrUnknownDecision = errors.New("unknown decision")

// Reanalyzer is a strategy whose language model may be asked again on demand,
// between the closes of the candles: POST /api/controls/reanalyze, from the
// channels where the control panel is enabled. Reanalyze returns at once, the
// analysis running in the flow of the strategy, the channel telling who asked
// and note what they tell the model, if anything (see ReanalysisRequest); its
// errors are refusals, eg: the provider rate limited, answered 409.
type Reanalyzer interface {
	Reanalyze(pair string, channel Channel, note string) (ReanalysisResponse, error)
}

// The limits of the decisions of GET /api/decisions.
const (
	defaultDecisions = 50
	maxDecisions     = 500
)

// imageTypes are the media types of the attachments served as they are, the
// images the dashboard shows; the others are downloads, never rendered by the
// browser: an SVG may hold scripts.
var imageTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// llm returns the language model of the strategy, nil for a strategy of
// another kind.
func (c *Chart) llm() *LLMInfo {
	s, ok := c.strategy.(strategy.LLMStrategy)
	if !ok {
		return nil
	}
	return &LLMInfo{Model: s.LLM(), Decisions: c.decisions != nil}
}

// reanalyzer returns the strategy when its model may be asked again, else nil.
func (c *Chart) reanalyzer() Reanalyzer {
	if _, ok := c.strategy.(strategy.LLMStrategy); !ok {
		return nil
	}
	r, _ := c.strategy.(Reanalyzer)
	return r
}

// decisionOf returns the decision of the order, empty without any. The caller
// holds c.mu.
func (c *Chart) decisionOf(orderID int64) string {
	if c.decisions == nil {
		return ""
	}
	return c.decisions.OrderDecision(orderID)
}

// onDecision sends a decision kept by the strategy to the dashboards.
func (c *Chart) onDecision(decision Decision) {
	c.events.publish("decision", DecisionEvent{Pair: decision.Pair, Decision: decision})
}

// requireDecisions returns the decisions of the strategy, or replies 404 for a
// strategy that keeps none.
func (c *Chart) requireDecisions(w http.ResponseWriter) (DecisionSource, bool) {
	if c.decisions == nil {
		writeJSONError(w, http.StatusNotFound, "the strategy keeps no decisions of a language model")
		return nil, false
	}
	return c.decisions, true
}

// handleDecisions replies the last decisions of the pair of the query, of
// every pair without it, newest first: limit of the query at most.
func (c *Chart) handleDecisions(w http.ResponseWriter, r *http.Request) {
	decisions, ok := c.requireDecisions(w)
	if !ok {
		return
	}
	pair := strings.TrimSpace(r.URL.Query().Get("pair"))
	if pair != "" {
		c.mu.Lock()
		resolved, known := c.resolvePair(pair)
		c.mu.Unlock()
		if !known {
			writeJSONError(w, http.StatusNotFound, "unknown pair")
			return
		}
		pair = resolved
	}
	limit := defaultDecisions
	if value := r.URL.Query().Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			writeJSONError(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = min(n, maxDecisions)
	}

	list, err := decisions.Decisions(pair, limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []Decision{}
	}
	writeJSON(w, DecisionsResponse{Decisions: list})
}

// handleDecision replies the decision of the {id} of the request.
func (c *Chart) handleDecision(w http.ResponseWriter, r *http.Request) {
	decisions, ok := c.requireDecisions(w)
	if !ok {
		return
	}
	decision, found, err := decisions.Decision(chi.URLParam(r, "id"))
	switch {
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, err.Error())
	case !found:
		writeJSONError(w, http.StatusNotFound, "unknown decision")
	default:
		writeJSON(w, decision)
	}
}

// handleDecisionAttachment replies the content of an attachment of a decision:
// an image as it is, any other file as a download.
func (c *Chart) handleDecisionAttachment(w http.ResponseWriter, r *http.Request) {
	decisions, ok := c.requireDecisions(w)
	if !ok {
		return
	}
	index, err := strconv.Atoi(chi.URLParam(r, "index"))
	if err != nil || index < 0 {
		writeJSONError(w, http.StatusNotFound, "unknown attachment")
		return
	}
	mediaType, data, err := decisions.Attachment(chi.URLParam(r, "id"), index)
	switch {
	case errors.Is(err, ErrUnknownDecision):
		writeJSONError(w, http.StatusNotFound, "unknown attachment")
		return
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable") // a decision never changes its files
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	isImage := false
	for _, image := range imageTypes {
		isImage = isImage || mediaType == image
	}
	if isImage {
		w.Header().Set("Content-Type", mediaType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment")
	}
	_, _ = w.Write(data)
}

// handleReanalyze asks the language model of the strategy again, now rather
// than at the next close, from a channel where the control panel is enabled.
func (c *Chart) handleReanalyze(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireController(w, r); !ok {
		return
	}
	reanalyzer := c.reanalyzer()
	if reanalyzer == nil {
		writeJSONError(w, http.StatusNotFound, "the strategy doesn't use a language model")
		return
	}

	var request ReanalysisRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	note := strings.TrimSpace(request.Note)
	if n := utf8.RuneCountInString(note); n > MaxReanalysisNote {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("the note has %d characters, %d at most", n,
			MaxReanalysisNote))
		return
	}
	c.mu.Lock()
	pair, known := c.resolvePair(request.Pair)
	c.mu.Unlock()
	if !known {
		writeJSONError(w, http.StatusNotFound, "unknown pair")
		return
	}

	response, err := reanalyzer.Reanalyze(pair, channelOf(r), note)
	if err != nil {
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, response)
}
