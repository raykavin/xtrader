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
	store     *store.Store
	manager   *Manager
	bot       BotState
	auth      func() *auth
	positions *openPositions // of the agents on their accounts, see GET /api/positions/open
	closed    *closedProfits // of the agents on their accounts, see GET /api/positions/closed
	trades    *performance   // of the agents on their accounts, see GET /api/performance
	paths     *excursions    // of their trades, see GET /api/performance/excursions
	mt5       *mt5Accounts   // the accounts of MT5 and their terminals, see GET /api/settings/mt5-accounts
}

// ServeSettings serves the settings of s on the dashboard, applied by
// manager, the state of the Telegram bot told by bot, if any: before Handler.
func (d *Dashboard) ServeSettings(s *store.Store, manager *Manager, bot BotState) {
	positions := newOpenPositions(s, manager)
	d.settings = &settings{store: s, manager: manager, bot: bot, auth: func() *auth { return d.auth },
		positions: positions, closed: newClosedProfits(positions)}
	d.settings.trades = newPerformance(positions)
	d.settings.mt5 = newMT5Accounts(s, manager, positions)
	d.settings.paths = newExcursions(d.settings.trades)
	if manager != nil {
		manager.exposure = newExposureGuard(s, positions)
	}
}

// routes serves the settings, each route needing the permissions it declares
// (see need): a list read by another area too takes the permission of it.
func (s *settings) routes(r chi.Router) {
	limits, setLimits := need(ui.PermLimitsView), need(ui.PermLimitsManage)
	r.With(limits).Get("/settings/exposure", s.handleExposure)
	r.With(setLimits).Put("/settings/exposure", s.handleSetExposure)
	r.With(need(ui.PermLimitsView, ui.PermBacktestsManage)).Get("/settings/asset-agents", s.handleAssetAgents) // the rules of a backtest
	r.With(setLimits).Put("/settings/asset-agents", s.handleSetAssetAgents)

	agents := need(ui.PermAgentsManage)
	r.With(need(ui.PermAgentsView, ui.PermLimitsView)).Get("/settings/agents", s.handleAgents) // the windows of the trading hours
	r.With(agents).Post("/settings/agents", s.handleCreateAgent)
	r.With(agents).Post("/settings/agents/params", s.handleAgentsParam)
	r.With(agents).Patch("/settings/agents/{name}", s.handleAgent)
	r.With(agents).Delete("/settings/agents/{name}", s.handleDeleteAgent)
	r.With(agents).Post("/settings/agents/{name}/restart", s.handleRestart)
	r.With(agents).Post("/settings/agents/{name}/rename", s.handleRename)
	r.With(need(ui.PermAgentsView)).Get("/settings/agents/{name}/periods", s.handlePeriods)
	r.With(agents).Post("/settings/agents/{name}/periods", s.handleStartPeriod)

	prompts := need(ui.PermPromptsManage)
	r.With(need(ui.PermPromptsView, ui.PermAgentsManage)).Get("/prompts", s.handlePrompts) // the prompt of an agent
	r.With(prompts).Post("/prompts", s.handleCreatePrompt)
	r.With(need(ui.PermPromptsView)).Get("/prompts/{id}", s.handlePrompt)
	r.With(prompts).Put("/prompts/{id}", s.handleUpdatePrompt)
	r.With(prompts).Delete("/prompts/{id}", s.handleDeletePrompt)

	telegram := need(ui.PermTelegramManage)
	r.With(need(ui.PermTelegramView)).Get("/settings/telegram", s.handleTelegram)
	r.With(need(ui.PermTelegramView)).Get("/settings/telegram/users", s.handleTelegramUsers)
	r.With(telegram).Patch("/settings/telegram", s.handleUpdateTelegram)
	r.With(telegram).Post("/settings/telegram/users", s.handleAddTelegramUser)
	r.With(telegram).Delete("/settings/telegram/users/{id}", s.handleDeleteTelegramUser)

	panel := need(ui.PermDashboardView)
	r.With(panel).Get("/positions/open", s.positions.handle)
	r.With(panel).Get("/positions/closed", s.closed.handle)
	performance := need(ui.PermPerformanceView)
	r.With(performance).Get("/performance", s.trades.handle)
	r.With(performance).Get("/performance/excursions", s.paths.handle)

	users, setUsers := need(ui.PermUsersView), need(ui.PermUsersManage)
	r.With(users).Get("/users", s.handleUsers)
	r.With(setUsers).Post("/users", s.handleCreateUser)
	r.With(setUsers).Patch("/users/{id}", s.handleUpdateUser)
	r.With(setUsers).Delete("/users/{id}", s.handleDeleteUser)
	r.With(users).Get("/roles", s.handleRoles)
	r.With(setUsers).Post("/roles", s.handleCreateRole)
	r.With(setUsers).Patch("/roles/{id}", s.handleUpdateRole)
	r.With(setUsers).Delete("/roles/{id}", s.handleDeleteRole)
	s.mt5Routes(r)
}

// decode decodes the JSON payload of the request into v, answering 400 when
// it can't.
func decode(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSettingsBody)).Decode(v); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Pedido inválido.")
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
		writeJSONError(w, http.StatusBadRequest, "Parâmetro "+name+" inválido.")
		return 0, false
	}
	return id, true
}

// fail answers an error of the store: 404, 409 or 400 for the ones of the
// request, 500 else, its detail logged; 409 for an agent that would trade the
// positions of another one running, 503 once the controller stops, or for a
// request canceled: the controller stopping cancels the requests in flight
// (the BaseContext of its server), eg: a restart waiting for its agent, and a
// client gone reads no answer.
func (s *settings) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, userMessage(err))
	case errors.Is(err, store.ErrConflict), errors.As(err, new(samePositions)):
		writeJSONError(w, http.StatusConflict, userMessage(err))
	case errors.Is(err, store.ErrInvalid):
		writeJSONError(w, http.StatusBadRequest, userMessage(err))
	case errors.Is(err, ErrStopped), errors.Is(err, context.Canceled):
		log.Debugf("ui: settings: %v", err)
		writeJSONError(w, http.StatusServiceUnavailable, stoppingMessage)
	default:
		log.Errorf("ui: settings: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "Não foi possível ler ou salvar as configurações agora. "+
			"Tente de novo em instantes.")
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
		TelegramControl: a.TelegramControl, Reanalysis: a.ReanalysisAllowed(), Running: state.Running,
		RestartPending: state.RestartPending, Restarts: state.Restarts, Restarting: state.Restarting,
		Exited: state.Exited, LastExit: state.LastExit, LaunchError: state.LaunchError, Outbox: s.manager.Outbox(a.ID),
		Params: a.AllParams(), Supervision: supervisionInfo(a), Protection: a.ProtectionOf(),
		DailyLimit: a.DailyLimitOf()}
	if stopped, by, at := a.BotStopped(); stopped {
		settings.BotStopped, settings.BotStoppedBy, settings.BotStoppedAt = true, by, at.Unix()
	}
	if acc := a.Account; acc != nil {
		settings.Account = &ui.AgentAccountSettings{BridgeURL: acc.BridgeURL, Magic: acc.Magic,
			Comment: acc.Comment, RealAllowed: acc.RealAllowed}
		settings.Secrets.BridgeToken = len(acc.BridgeToken) > 0
	}
	if account := a.MT5AccountOf(); account != nil {
		settings.MT5AccountID = account.ID
		settings.Secrets.BridgeToken = len(account.BridgeToken) > 0
	}
	if l := a.LLM; l != nil {
		settings.Model = l.Provider + " " + l.Model
		settings.LLM = &ui.AgentLLMSettings{Provider: l.Provider, Model: l.Model, BaseURL: l.BaseURL,
			ReasoningEffort: l.ReasoningEffort, MaxTokens: l.MaxTokens}
		settings.PromptID = l.PromptID
		settings.Secrets.LLMAPIKey = len(l.APIKey) > 0
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
	var linked *store.MT5Account
	if id := request.MT5AccountID; id != nil && *id != 0 {
		mt5, err := s.store.MT5Account(ctx, *id)
		if err != nil {
			s.fail(w, err)
			return
		}
		linked, account.BridgeURL = &mt5, mt5.BridgeURL // the bridge of its account
		request.BridgeToken = nil
	}
	agent := store.Agent{Name: request.Name, Kind: request.Kind, Enabled: request.Enabled, Mode: request.Mode,
		APIAddress: request.APIAddress, Launched: request.Launched == nil || *request.Launched,
		Restart: request.Restart == nil || *request.Restart, TradePanel: request.TradePanel,
		TelegramControl: request.TelegramControl, Reanalysis: &store.AgentReanalysis{Allowed: request.Reanalysis},
		Params: request.Params,
		Account: &store.Account{BridgeURL: account.BridgeURL, Magic: account.Magic, Comment: account.Comment,
			RealAllowed: account.RealAllowed}}
	switch l := request.LLM; {
	case l != nil:
		agent.LLM = &store.LLMSettings{Provider: l.Provider, Model: l.Model, BaseURL: l.BaseURL,
			ReasoningEffort: l.ReasoningEffort, MaxTokens: l.MaxTokens, PromptID: request.PromptID}
	case request.PromptID != nil:
		writeJSONError(w, http.StatusBadRequest, "Só agentes LLM têm prompt: escolha o LLM do agente ou tire o prompt.")
		return
	}
	created, err := s.store.CreateAgent(ctx, agent, store.AgentSecrets{LLMAPIKey: request.LLMAPIKey,
		BridgeToken: request.BridgeToken})
	if err != nil {
		s.fail(w, err)
		return
	}
	if linked != nil {
		if err := s.store.SetAgentMT5Account(ctx, created.ID, &linked.ID, changeOf(r, "")); err != nil {
			s.fail(w, err)
			return
		}
		if created, err = s.store.Agent(ctx, created.ID); err != nil {
			s.fail(w, err)
			return
		}
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
	linked := a.MT5AccountOf() != nil
	if request.MT5AccountID != nil {
		linked = *request.MT5AccountID != 0
	}
	if linked && (request.BridgeToken != nil || request.Account != nil && request.Account.BridgeURL != nil) {
		writeJSONError(w, http.StatusBadRequest,
			"a bridge e o token do agente vêm da conta MT5 dele: troque-os em Configurações › Contas MT5")
		return
	}
	if request.MT5AccountID != nil && s.linkAgent(w, r, a, *request.MT5AccountID) {
		return
	}
	update := store.AgentUpdate{Enabled: request.Enabled, TradePanel: request.TradePanel,
		TelegramControl: request.TelegramControl, Reanalysis: request.Reanalysis, Mode: request.Mode,
		APIAddress: request.APIAddress, Launched: request.Launched, Restart: request.Restart,
		Params: request.Params}
	if acc := request.Account; acc != nil {
		update.Account = &store.AccountUpdate{BridgeURL: acc.BridgeURL, Magic: acc.Magic, Comment: acc.Comment,
			RealAllowed: acc.RealAllowed}
	}
	if l := request.LLM; l != nil {
		update.LLM = &store.LLMUpdate{Provider: l.Provider, Model: l.Model, BaseURL: l.BaseURL,
			ReasoningEffort: l.ReasoningEffort, MaxTokens: l.MaxTokens}
	}
	err = s.store.Transaction(ctx, func(ctx context.Context) error {
		if _, err := s.store.UpdateAgent(ctx, a.ID, update); err != nil {
			return err
		}
		if request.Protection != nil {
			if _, err := s.store.SetAgentProtection(ctx, a.ID, *request.Protection, userOf(ctx)); err != nil {
				return err
			}
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
		if request.Enabled != nil && *request.Enabled && !a.Enabled {
			// enabled again by a user: a stop of the supervisor ends, with the
			// block of the entries it made
			if _, err := s.store.ClearAgentStop(ctx, a.ID, userOf(ctx)); err != nil {
				return err
			}
		}
		return s.store.SetAgentSecrets(ctx, a.ID, store.AgentSecrets{LLMAPIKey: request.LLMAPIKey,
			BridgeToken: request.BridgeToken})
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
		writeJSONError(w, http.StatusConflict, "O agente "+a.Name+" ainda está parando. Tente de novo em instantes.")
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

// handleRename renames an agent disabled and stopped, see store.RenameAgent,
// and its outbox with it, the records it holds not sent yet kept: its ID, its
// account, its orders, its decisions and its journal stay its own. The
// selections of the agent by its old name, eg: in Telegram, are made again.
func (s *settings) handleRename(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a, err := s.store.AgentByName(ctx, chi.URLParam(r, "name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var request ui.AgentRenameRequest
	if !decode(w, r, &request) {
		return
	}
	switch {
	case a.Enabled:
		writeJSONError(w, http.StatusConflict, "Desligue o agente "+a.Name+" antes.")
		return
	case s.manager.State(a.Name).Running:
		writeJSONError(w, http.StatusConflict, "O agente "+a.Name+" ainda está parando. Tente de novo em instantes.")
		return
	}
	undo := func() {}
	var renamed store.Agent
	err = s.store.Transaction(ctx, func(ctx context.Context) error {
		var err error
		if renamed, err = s.store.RenameAgent(ctx, a.ID, request.Name); err != nil || renamed.Name == a.Name {
			return err
		}
		undo, err = s.manager.RenameOutbox(a.ID, a.Name, renamed.Name)
		return err
	})
	if err != nil {
		undo()
		s.fail(w, err)
		return
	}
	log.Infof("ui: the agent %s renamed %s by %s", a.Name, renamed.Name, userOf(ctx))
	if err := s.manager.Reconcile(ctx); err != nil {
		log.Errorf("ui: %v", err)
	}
	writeJSON(w, s.agentSettings(renamed))
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

// handlePrompts lists the prompts, by name: the page and the search asked for,
// see listQuery.
func (s *settings) handlePrompts(w http.ResponseWriter, r *http.Request) {
	q, ok := listQuery(w, r)
	if !ok {
		return
	}
	prompts, err := s.store.FindPrompts(r.Context(), q)
	if err != nil {
		s.fail(w, err)
		return
	}
	list := make([]ui.PromptInfo, 0, len(prompts.Rows))
	for _, p := range prompts.Rows {
		list = append(list, promptInfo(p.Prompt, p.UsedBy))
	}
	writeJSON(w, ui.PromptsResponse{PageInfo: pageInfo(prompts), Prompts: list})
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
		writeJSONError(w, http.StatusBadRequest, "Informe o conteúdo do prompt.")
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

// handleTelegramUsers lists the Telegram users the bot answers, by their ID:
// the page and the search asked for, see listQuery.
func (s *settings) handleTelegramUsers(w http.ResponseWriter, r *http.Request) {
	q, ok := listQuery(w, r)
	if !ok {
		return
	}
	users, err := s.store.FindTelegramUsers(r.Context(), q)
	if err != nil {
		s.fail(w, err)
		return
	}
	response := ui.TelegramUsersResponse{PageInfo: pageInfo(users), Users: make([]ui.TelegramUser, 0, len(users.Rows))}
	for _, u := range users.Rows {
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

// userInfo returns the user u without its password hash, with its role.
func userInfo(u store.User) ui.UserInfo {
	info := ui.UserInfo{ID: u.ID, Username: u.Username, Enabled: u.Enabled, CreatedAt: u.CreatedAt.Unix()}
	if role := u.RoleOf(); role != nil {
		info.Role = &ui.RoleRef{ID: role.ID, Name: role.Name}
	}
	if u.LastLoginAt != nil {
		at := u.LastLoginAt.Unix()
		info.LastLoginAt = &at
	}
	return info
}

// handleUsers lists the users of the dashboard: the page and the search asked
// for, see listQuery.
func (s *settings) handleUsers(w http.ResponseWriter, r *http.Request) {
	q, ok := listQuery(w, r)
	if !ok {
		return
	}
	users, err := s.store.FindUsers(r.Context(), q)
	if err != nil {
		s.fail(w, err)
		return
	}
	list := make([]ui.UserInfo, 0, len(users.Rows))
	for _, u := range users.Rows {
		list = append(list, userInfo(u))
	}
	writeJSON(w, ui.UsersResponse{PageInfo: pageInfo(users), Users: list})
}

// handleCreateUser creates a user of the dashboard, with its role.
func (s *settings) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var request ui.UserRequest
	if !decode(w, r, &request) {
		return
	}
	switch {
	case request.Password == nil:
		writeJSONError(w, http.StatusBadRequest, "Informe a senha.")
		return
	case request.RoleID == nil || *request.RoleID <= 0:
		writeJSONError(w, http.StatusBadRequest, "Escolha o papel do usuário: sem um papel ele não entra.")
		return
	}
	by := userOf(r.Context())
	var user store.User
	err := s.store.Transaction(r.Context(), func(ctx context.Context) error {
		created, err := s.store.CreateUser(ctx, request.Username, *request.Password)
		if err != nil {
			return err
		}
		if err := s.store.SetUserRole(ctx, created.ID, *request.RoleID, by); err != nil {
			return err
		}
		user, err = s.store.User(ctx, created.ID)
		return err
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the user %s created by %s, with the role %s", user.Username, by, user.RoleOf().Name)
	writeCreated(w, userInfo(user))
}

// handleUpdateUser changes the password of a user, its role (0 takes it away,
// the user then refused at the login), or enables or disables it: its
// sessions are closed, or take its new role at their next request.
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
		if request.RoleID != nil {
			if err := s.store.SetUserRole(ctx, id, max(*request.RoleID, 0), userOf(ctx)); err != nil {
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
	switch {
	case request.Password != nil || (request.Enabled != nil && !*request.Enabled) ||
		(request.RoleID != nil && *request.RoleID <= 0):
		s.auth().closeUser(id)
	case request.RoleID != nil:
		s.auth().refreshUsers(id)
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
