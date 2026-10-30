package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// newsProbeTimeout bounds the test of an MCP server of the news.
const newsProbeTimeout = 20 * time.Second

// newsResponse returns the news of all the agents as the dashboard shows
// them: never the token, only whether it is set.
func (s *Supervision) newsResponse(ctx context.Context) (ui.NewsSettingsResponse, error) {
	settings, _, err := s.store.News(ctx)
	if err != nil {
		return ui.NewsSettingsResponse{}, err
	}
	rules, _, err := s.store.NewsRules(ctx)
	if err != nil {
		return ui.NewsSettingsResponse{}, err
	}
	conflict, err := s.store.NewsConflict(ctx)
	if err != nil {
		return ui.NewsSettingsResponse{}, err
	}
	urlConflict, err := s.store.NewsURLConflict(ctx)
	if err != nil {
		return ui.NewsSettingsResponse{}, err
	}
	agents, err := s.store.Agents(ctx)
	if err != nil {
		return ui.NewsSettingsResponse{}, err
	}
	response := ui.NewsSettingsResponse{Enabled: settings.Enabled, URL: deref(rules.MCPURL),
		TokenSet: len(settings.MCPToken) > 0, BlockBefore: rules.BlockBefore, BlockAfter: rules.BlockAfter,
		BlockImportance: rules.BlockImportance, Conflict: append([]string{}, conflict...),
		URLConflict: append([]string{}, urlConflict...), UpdatedBy: settings.UpdatedBy, Agents: []ui.NewsAgent{}}
	if !settings.UpdatedAt.IsZero() && settings.UpdatedBy != "" {
		response.UpdatedAt = settings.UpdatedAt.Unix()
	}
	for _, a := range agents {
		response.Agents = append(response.Agents, ui.NewsAgent{Agent: a.Name, Kind: a.Kind, Enabled: a.Enabled,
			Running: s.running(a.Name)})
	}
	return response, nil
}

// handleNews answers the news of all the agents, never the token.
func (s *Supervision) handleNews(w http.ResponseWriter, r *http.Request) {
	response, err := s.newsResponse(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, response)
}

// handleSetNews changes the news of all the agents (see
// ui.NewsSettingsRequest: a token absent is kept), and applies them to every
// agent while it runs, through its runtime.
func (s *Supervision) handleSetNews(w http.ResponseWriter, r *http.Request) {
	var request ui.NewsSettingsRequest
	if !decode(w, r, &request) {
		return
	}
	by := userOf(r.Context())
	if _, err := s.store.SetNews(r.Context(), store.NewsUpdate{Enabled: request.Enabled, Token: request.Token,
		URL: request.URL, BlockBefore: request.BlockBefore, BlockAfter: request.BlockAfter,
		BlockImportance: request.BlockImportance, By: by}); err != nil {
		s.fail(w, err)
		return
	}
	token := "kept"
	if request.Token != nil {
		token = "replaced"
		if strings.TrimSpace(*request.Token) == "" {
			token = "removed"
		}
	}
	log.Infof("ui: the news changed by %s (on: %v, token %s)", by, request.Enabled != nil && *request.Enabled, token)
	s.refreshNews(r.Context())
	response, err := s.newsResponse(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, response)
}

// refreshNews publishes the runtime of every agent again, the news in it: the
// agents running apply them at once (their calendar switched, its server, its
// token and the window replaced); the others get them as they start.
func (s *Supervision) refreshNews(ctx context.Context) {
	agents, err := s.store.Agents(ctx)
	if err != nil {
		log.Warnf("ui: the news not sent to the agents: %v", err)
		return
	}
	for _, a := range agents {
		if err := s.manager.RefreshRuntime(ctx, a.Name); err != nil {
			log.Warnf("ui: the news not sent to the agent %s: %v", a.Name, err)
		}
	}
}

// handleTestNews reads once the MCP server of the news with its token, asked
// by the user, see ui.NewsTestResponse.
func (s *Supervision) handleTestNews(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token, err := s.store.NewsToken(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	rules, _, err := s.store.NewsRules(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	agents, err := s.store.Agents(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	result := ui.NewsTestResult{URL: deref(rules.MCPURL), Agents: []string{}}
	for _, a := range agents {
		if a.Enabled {
			result.Agents = append(result.Agents, a.Name)
		}
	}
	switch {
	case result.URL == "":
		result.Status = llm.CalendarMissingServer
	case strings.TrimSpace(token) == "":
		result.Status = llm.CalendarMissingToken
	default:
		probe, cancel := context.WithTimeout(ctx, newsProbeTimeout)
		err := llm.ProbeNews(probe, result.URL, token)
		cancel()
		result.Status, result.Detail = newsTestStatus(err, token)
	}
	log.Infof("ui: the MCP server of the news tested by %s: %s", userOf(ctx), result.Status)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, ui.NewsTestResponse{Results: []ui.NewsTestResult{result}, At: time.Now().Unix()})
}

// newsTestStatus returns the status of the test of an MCP server that answered
// err, and how it failed, without token.
func newsTestStatus(err error, token string) (status, detail string) {
	switch {
	case err == nil:
		return "ok", ""
	case errors.Is(err, llm.ErrMCPUnauthorized):
		status = llm.CalendarUnauthorized
	case errors.Is(err, llm.ErrNewsNoToken):
		status = llm.CalendarMissingToken
	default:
		status = llm.CalendarUnavailable
	}
	detail = err.Error()
	if token != "" {
		detail = strings.ReplaceAll(detail, token, "[redacted]")
	}
	return status, detail
}
