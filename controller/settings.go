package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// maxSettingsBody is the size of the payload of a change of the settings at
// most, a prompt included.
const maxSettingsBody = 256 << 10

// BotState tells whether the Telegram bot runs, see TelegramRunner.
type BotState interface {
	Running() bool
}

// settings serves the settings of the controller on the dashboard, to its
// users: the agents, the prompts, Telegram and the users. Every change is
// applied by the manager at once.
type settings struct {
	store   *store.Store
	manager *Manager
	bot     BotState
	auth    func() *auth
}

// ServeSettings serves the settings of s on the dashboard, applied by
// manager, the state of the Telegram bot told by bot, if any: before Handler.
func (d *Dashboard) ServeSettings(s *store.Store, manager *Manager, bot BotState) {
	d.settings = &settings{store: s, manager: manager, bot: bot, auth: func() *auth { return d.auth }}
}

func (s *settings) routes(r chi.Router) {
	r.Get("/settings/agents", s.handleAgents)
	r.Post("/settings/agents", s.handleCreateAgent)
	r.Patch("/settings/agents/{name}", s.handleAgent)
	r.Delete("/settings/agents/{name}", s.handleDeleteAgent)
	r.Post("/settings/agents/{name}/restart", s.handleRestart)
	r.Get("/settings/agents/{name}/periods", s.handlePeriods)
	r.Post("/settings/agents/{name}/periods", s.handleStartPeriod)
	r.Get("/prompts", s.handlePrompts)
	r.Post("/prompts", s.handleCreatePrompt)
	r.Get("/prompts/{id}", s.handlePrompt)
	r.Put("/prompts/{id}", s.handleUpdatePrompt)
	r.Delete("/prompts/{id}", s.handleDeletePrompt)
	r.Get("/settings/telegram", s.handleTelegram)
	r.Patch("/settings/telegram", s.handleUpdateTelegram)
	r.Post("/settings/telegram/users", s.handleAddTelegramUser)
	r.Delete("/settings/telegram/users/{id}", s.handleDeleteTelegramUser)
	r.Get("/users", s.handleUsers)
	r.Post("/users", s.handleCreateUser)
	r.Patch("/users/{id}", s.handleUpdateUser)
	r.Delete("/users/{id}", s.handleDeleteUser)
}

// decode decodes the JSON payload of the request into v, answering 400 when
// it can't.
func decode(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSettingsBody)).Decode(v); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON payload")
		return false
	}
	return true
}

// writeCreated answers payload, the JSON of a record created (201).
func writeCreated(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, payload)
}

// idParam returns the ID of the path parameter name, answering 400 when it
// isn't one.
func idParam(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || id < 1 {
		writeJSONError(w, http.StatusBadRequest, "invalid "+name)
		return 0, false
	}
	return id, true
}

// fail answers an error of the store: 404, 409 or 400 for the ones of the
// request, 500 else, its detail logged.
func (s *settings) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, store.ErrConflict):
		writeJSONError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrInvalid):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	default:
		log.Errorf("ui: settings: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "the settings can't be read or changed now")
	}
}

// handleAgents lists the agents with their settings and state.
func (s *settings) handleAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.Agents(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	list := make([]ui.AgentSettings, 0, len(agents))
	for _, a := range agents {
		list = append(list, s.agentSettings(a))
	}
	writeJSON(w, ui.AgentSettingsResponse{Agents: list})
}

// agentSettings returns the settings and the state of the agent a, which of
// its secrets are set, never their values.
func (s *settings) agentSettings(a store.Agent) ui.AgentSettings {
	state := s.manager.State(a.Name)
	settings := ui.AgentSettings{Name: a.Name, Kind: a.Kind, Mode: a.Mode, APIAddress: a.APIAddress,
		Enabled: a.Enabled, Launched: a.Launched, Restart: a.Restart, TradePanel: a.TradePanel,
		TelegramControl: a.TelegramControl, Running: state.Running, RestartPending: state.RestartPending,
		LastExit: state.LastExit, Params: a.AllParams()}
	if acc := a.Account; acc != nil {
		settings.Account = &ui.AgentAccountSettings{BridgeURL: acc.BridgeURL, Magic: acc.Magic,
			Comment: acc.Comment, RealAllowed: acc.RealAllowed}
		settings.Secrets.BridgeToken = len(acc.BridgeToken) > 0
	}
	if l := a.LLM; l != nil {
		settings.Model = l.Provider + " " + l.Model
		settings.LLM = &ui.AgentLLMSettings{Provider: l.Provider, Model: l.Model, BaseURL: l.BaseURL,
			ReasoningEffort: l.ReasoningEffort, MaxTokens: l.MaxTokens, CalendarURL: l.CalendarURL}
		settings.PromptID = l.PromptID
		settings.Secrets.LLMAPIKey = len(l.APIKey) > 0
		settings.Secrets.CalendarToken = len(l.CalendarToken) > 0
		if st := l.PromptStatus; st != nil {
			settings.PromptStatus = &ui.AgentPromptStatus{PromptID: st.PromptID, Name: st.Name, Version: st.Version,
				Applied: st.Applied, Error: st.Error, At: st.At.Unix()}
		}
	}
	return settings
}

// handleCreateAgent creates an agent, launched at once when enabled.
func (s *settings) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var request ui.AgentCreateRequest
	if !decode(w, r, &request) {
		return
	}
	account := request.Account
	agent := store.Agent{Name: request.Name, Kind: request.Kind, Enabled: request.Enabled, Mode: request.Mode,
		APIAddress: request.APIAddress, Launched: request.Launched == nil || *request.Launched,
		Restart: request.Restart == nil || *request.Restart, TradePanel: request.TradePanel,
		TelegramControl: request.TelegramControl, Params: request.Params,
		Account: &store.Account{BridgeURL: account.BridgeURL, Magic: account.Magic, Comment: account.Comment,
			RealAllowed: account.RealAllowed}}
	switch l := request.LLM; {
	case l != nil:
		agent.LLM = &store.LLMSettings{Provider: l.Provider, Model: l.Model, BaseURL: l.BaseURL,
			ReasoningEffort: l.ReasoningEffort, MaxTokens: l.MaxTokens, CalendarURL: l.CalendarURL,
			PromptID: request.PromptID}
	case request.PromptID != nil:
		writeJSONError(w, http.StatusBadRequest, "a prompt without a language model: for an LLM agent only")
		return
	}
	created, err := s.store.CreateAgent(ctx, agent, store.AgentSecrets{LLMAPIKey: request.LLMAPIKey,
		BridgeToken: request.BridgeToken, CalendarToken: request.CalendarToken})
	if err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the agent %s created by %s", created.Name, userOf(ctx))
	if err := s.manager.Reconcile(ctx); err != nil {
		log.Errorf("ui: %v", err)
	}
	writeCreated(w, s.agentSettings(created))
}

// handleAgent changes the settings of an agent: its switches, its settings,
// its prompt and its secrets, see ui.AgentSettingsRequest.
func (s *settings) handleAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a, err := s.store.AgentByName(ctx, chi.URLParam(r, "name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var request ui.AgentSettingsRequest
	if !decode(w, r, &request) {
		return
	}
	update := store.AgentUpdate{Enabled: request.Enabled, TradePanel: request.TradePanel,
		TelegramControl: request.TelegramControl, Mode: request.Mode, APIAddress: request.APIAddress,
		Launched: request.Launched, Restart: request.Restart, Params: request.Params}
	if acc := request.Account; acc != nil {
		update.Account = &store.AccountUpdate{BridgeURL: acc.BridgeURL, Magic: acc.Magic, Comment: acc.Comment,
			RealAllowed: acc.RealAllowed}
	}
	if l := request.LLM; l != nil {
		update.LLM = &store.LLMUpdate{Provider: l.Provider, Model: l.Model, BaseURL: l.BaseURL,
			ReasoningEffort: l.ReasoningEffort, MaxTokens: l.MaxTokens, CalendarURL: l.CalendarURL}
	}
	err = s.store.Transaction(ctx, func(ctx context.Context) error {
		if _, err := s.store.UpdateAgent(ctx, a.ID, update); err != nil {
			return err
		}
		switch {
		case request.DefaultPrompt:
			if err := s.store.SetAgentPrompt(ctx, a.ID, nil); err != nil {
				return err
			}
		case request.PromptID != nil:
			if err := s.store.SetAgentPrompt(ctx, a.ID, request.PromptID); err != nil {
				return err
			}
		}
		return s.store.SetAgentSecrets(ctx, a.ID, store.AgentSecrets{LLMAPIKey: request.LLMAPIKey,
			BridgeToken: request.BridgeToken, CalendarToken: request.CalendarToken})
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the settings of the agent %s changed by %s", a.Name, userOf(ctx))
	if err := s.manager.Reconcile(ctx); err != nil {
		log.Errorf("ui: %v", err)
	}
	if a, err = s.store.Agent(ctx, a.ID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, s.agentSettings(a))
}

// handleDeleteAgent deletes an agent disabled and stopped, see
// store.DeleteAgent: its orders, its decisions and its journal are kept, and a
// new agent may take its name.
func (s *settings) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a, err := s.store.AgentByName(ctx, chi.URLParam(r, "name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !a.Enabled && s.manager.State(a.Name).Running {
		writeJSONError(w, http.StatusConflict, "the agent "+a.Name+" is still stopping: try again in a moment")
		return
	}
	if err := s.store.DeleteAgent(ctx, a.ID); err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the agent %s deleted by %s", a.Name, userOf(ctx))
	if err := s.manager.Reconcile(ctx); err != nil {
		log.Errorf("ui: %v", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRestart restarts an agent with its settings of the database.
func (s *settings) handleRestart(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := s.manager.Restart(r.Context(), name); err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the agent %s restarted by %s", name, userOf(r.Context()))
	a, err := s.store.AgentByName(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, s.agentSettings(a))
}

// promptInfo returns the prompt p without its content.
func promptInfo(p store.Prompt, usedBy []string) ui.PromptInfo {
	if usedBy == nil {
		usedBy = []string{}
	}
	return ui.PromptInfo{ID: p.ID, Name: p.Name, Description: p.Description, Version: p.Version,
		UpdatedAt: p.UpdatedAt.Unix(), UsedBy: usedBy}
}

// handlePrompts lists the prompts, by name.
func (s *settings) handlePrompts(w http.ResponseWriter, r *http.Request) {
	prompts, err := s.store.Prompts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	list := make([]ui.PromptInfo, 0, len(prompts))
	for _, p := range prompts {
		list = append(list, promptInfo(p.Prompt, p.UsedBy))
	}
	writeJSON(w, ui.PromptsResponse{Prompts: list})
}

// promptDetail answers the prompt of id with its content.
func (s *settings) promptDetail(w http.ResponseWriter, r *http.Request, id int64) {
	prompts, err := s.store.Prompts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	prompt, err := s.store.Prompt(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	var usedBy []string
	for _, p := range prompts {
		if p.ID == id {
			usedBy = p.UsedBy
		}
	}
	writeJSON(w, ui.PromptDetail{PromptInfo: promptInfo(prompt, usedBy), Content: prompt.Content})
}

// handlePrompt answers a prompt with its content.
func (s *settings) handlePrompt(w http.ResponseWriter, r *http.Request) {
	if id, ok := idParam(w, r, "id"); ok {
		s.promptDetail(w, r, id)
	}
}

// handleCreatePrompt creates a prompt.
func (s *settings) handleCreatePrompt(w http.ResponseWriter, r *http.Request) {
	var request ui.PromptRequest
	if !decode(w, r, &request) {
		return
	}
	if request.Content == nil {
		writeJSONError(w, http.StatusBadRequest, "the content of the prompt is required")
		return
	}
	description := ""
	if request.Description != nil {
		description = *request.Description
	}
	prompt, err := s.store.CreatePrompt(r.Context(), request.Name, description, *request.Content,
		"web:"+userOf(r.Context()))
	if err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the prompt %s created by %s", prompt.Name, userOf(r.Context()))
	writeCreated(w, ui.PromptDetail{PromptInfo: promptInfo(prompt, nil), Content: prompt.Content})
}

// handleUpdatePrompt edits a prompt: a new content is a new version, applied
// by the agents using it from their next analysis.
func (s *settings) handleUpdatePrompt(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var request ui.PromptRequest
	if !decode(w, r, &request) {
		return
	}
	prompt, err := s.store.UpdatePrompt(r.Context(), id, store.PromptEdit{Description: request.Description,
		Content: request.Content, Version: request.Version}, "web:"+userOf(r.Context()))
	if err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the prompt %s edited by %s, version %d", prompt.Name, userOf(r.Context()), prompt.Version)
	s.manager.Trigger()
	s.promptDetail(w, r, id)
}

// handleDeletePrompt deletes a prompt no agent uses.
func (s *settings) handleDeletePrompt(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := s.store.DeletePrompt(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the prompt %d deleted by %s", id, userOf(r.Context()))
	w.WriteHeader(http.StatusNoContent)
}

// handleTelegram answers the settings of the Telegram bot.
func (s *settings) handleTelegram(w http.ResponseWriter, r *http.Request) {
	settings, hasToken, err := s.store.Telegram(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	users, err := s.store.TelegramUsers(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	response := ui.TelegramSettingsResponse{Enabled: settings.Enabled, HasToken: hasToken,
		Users: make([]ui.TelegramUser, 0, len(users))}
	if s.bot != nil {
		response.Running = s.bot.Running()
	}
	for _, u := range users {
		response.Users = append(response.Users, ui.TelegramUser{ID: u.ID, TelegramID: u.TelegramID, Name: u.Name})
	}
	writeJSON(w, response)
}

// handleUpdateTelegram changes the bot: enabled, and its token.
func (s *settings) handleUpdateTelegram(w http.ResponseWriter, r *http.Request) {
	var request ui.TelegramSettingsRequest
	if !decode(w, r, &request) {
		return
	}
	if err := s.store.SetTelegram(r.Context(), request.Enabled, request.Token); err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the Telegram bot changed by %s", userOf(r.Context()))
	s.manager.Trigger()
	s.handleTelegram(w, r)
}

// handleAddTelegramUser authorizes a Telegram user.
func (s *settings) handleAddTelegramUser(w http.ResponseWriter, r *http.Request) {
	var request ui.TelegramUserRequest
	if !decode(w, r, &request) {
		return
	}
	if _, err := s.store.AddTelegramUser(r.Context(), request.TelegramID, request.Name); err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the Telegram user %d authorized by %s", request.TelegramID, userOf(r.Context()))
	s.manager.Trigger()
	s.handleTelegram(w, r)
}

// handleDeleteTelegramUser stops answering a Telegram user.
func (s *settings) handleDeleteTelegramUser(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := s.store.DeleteTelegramUser(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: a Telegram user removed by %s", userOf(r.Context()))
	s.manager.Trigger()
	s.handleTelegram(w, r)
}

// userInfo returns the user u without its password hash.
func userInfo(u store.User) ui.UserInfo {
	info := ui.UserInfo{ID: u.ID, Username: u.Username, Enabled: u.Enabled, CreatedAt: u.CreatedAt.Unix()}
	if u.LastLoginAt != nil {
		at := u.LastLoginAt.Unix()
		info.LastLoginAt = &at
	}
	return info
}

// handleUsers lists the users of the dashboard.
func (s *settings) handleUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.Users(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	list := make([]ui.UserInfo, 0, len(users))
	for _, u := range users {
		list = append(list, userInfo(u))
	}
	writeJSON(w, ui.UsersResponse{Users: list})
}

// handleCreateUser creates a user of the dashboard.
func (s *settings) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var request ui.UserRequest
	if !decode(w, r, &request) {
		return
	}
	if request.Password == nil {
		writeJSONError(w, http.StatusBadRequest, "the password is required")
		return
	}
	user, err := s.store.CreateUser(r.Context(), request.Username, *request.Password)
	if err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the user %s created by %s", user.Username, userOf(r.Context()))
	writeCreated(w, userInfo(user))
}

// handleUpdateUser changes the password of a user, or enables or disables
// it: its sessions are closed.
func (s *settings) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var request ui.UserRequest
	if !decode(w, r, &request) {
		return
	}
	err := s.store.Transaction(r.Context(), func(ctx context.Context) error {
		if request.Password != nil {
			if err := s.store.SetPassword(ctx, id, *request.Password); err != nil {
				return err
			}
		}
		if request.Enabled != nil {
			return s.store.SetUserEnabled(ctx, id, *request.Enabled)
		}
		return nil
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	if request.Password != nil || (request.Enabled != nil && !*request.Enabled) {
		s.auth().closeUser(id)
	}
	user, err := s.store.User(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the user %s changed by %s", user.Username, userOf(r.Context()))
	writeJSON(w, userInfo(user))
}

// handleDeleteUser deletes a user, not the last one enabled, and closes its
// sessions.
func (s *settings) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	s.auth().closeUser(id)
	log.Infof("ui: the user %d deleted by %s", id, userOf(r.Context()))
	w.WriteHeader(http.StatusNoContent)
}
