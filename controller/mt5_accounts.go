package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The cadence of the accounts of MT5.
var (
	mt5ReconcileEvery = 30 * time.Second // between two looks at the terminals of the accounts active
	mt5SessionTimeout = 2 * time.Minute  // a bridge has to log its terminal in an account
)

// mt5Accounts manages the accounts of MetaTrader 5 (Configurações > Contas
// MT5): their terminals logged in the active ones (see reconcile), the
// accounts of the bridges imported once (see importAccounts), and how each
// terminal stands, as last read.
type mt5Accounts struct {
	store     *store.Store
	manager   *Manager
	positions *openPositions
	client    *http.Client // a session given to a bridge, its terminal logging in
	reads     *http.Client // the other requests
	now       func() time.Time
	// stateOf tells the state of an agent, see Manager.State: the tests
	// answer in its place
	stateOf func(name string) AgentState

	mu       sync.Mutex
	sessions map[int64]ui.MT5SessionStatus // by account
	running  sync.Mutex                    // a reconciliation at a time
}

func newMT5Accounts(s *store.Store, manager *Manager, positions *openPositions) *mt5Accounts {
	m := &mt5Accounts{store: s, manager: manager, positions: positions,
		client: &http.Client{Timeout: mt5SessionTimeout}, reads: &http.Client{Timeout: 10 * time.Second},
		now: time.Now, sessions: make(map[int64]ui.MT5SessionStatus)}
	m.stateOf = func(name string) AgentState {
		if m.manager == nil {
			return AgentState{}
		}
		return m.manager.State(name)
	}
	return m
}

// isRunning reports whether the agent of name runs.
func (m *mt5Accounts) isRunning(name string) bool { return m.stateOf(name).Running }

// bridgeSession is the session of a bridge, GET /session: the account it
// serves, the one its terminal is logged in, and what it waits for.
type bridgeSession struct {
	Login       int64  `json:"login"`
	Server      string `json:"server"`
	AllowReal   bool   `json:"allow_real"`
	ReadOnly    bool   `json:"read_only"`
	Legacy      bool   `json:"legacy"`
	Connected   bool   `json:"connected"`
	LoggedLogin int64  `json:"logged_login"`
	Waiting     string `json:"waiting"`
}

// bridgeAccountInfo is what GET /account of a bridge tells of its account.
type bridgeAccountInfo struct {
	Login     int64  `json:"login"`
	TradeMode int    `json:"trade_mode"`
	Server    string `json:"server"`
	Currency  string `json:"currency"`
	Company   string `json:"company"`
}

// bridgeHealthInfo is what GET /health of a bridge tells.
type bridgeHealthInfo struct {
	Connected bool   `json:"connected"`
	Login     int64  `json:"login"`
	Server    string `json:"server"`
	ReadOnly  bool   `json:"read_only"`
	Waiting   string `json:"waiting"`
}

// kindOf returns the kind of an account of its trade_mode: 0 demo, 1 contest,
// 2 real.
func kindOf(tradeMode int) string {
	switch tradeMode {
	case 0:
		return store.MT5KindDemo
	case 1:
		return store.MT5KindContest
	case 2:
		return store.MT5KindReal
	}
	return ""
}

// get reads path of the bridge of account into out.
func (m *mt5Accounts) get(ctx context.Context, account store.MT5Account, path string, out any) error {
	_, token, err := m.store.MT5AccountSecrets(account)
	if err != nil {
		return err
	}
	return bridgeGet(ctx, m.reads, 1<<20, account.BridgeURL, path, token, out)
}

// postSession logs the terminal of the bridge of account in it: POST /session
// with its password, the bridge's answer returned.
func (m *mt5Accounts) postSession(ctx context.Context, account store.MT5Account) (bridgeSession, error) {
	password, token, err := m.store.MT5AccountSecrets(account)
	if err != nil {
		return bridgeSession{}, err
	}
	body, _ := json.Marshal(map[string]any{"login": account.Login, "server": account.Server, "password": password,
		"allow_real": account.AllowReal, "read_only": account.ReadOnly})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(account.BridgeURL, "/")+"/session", bytes.NewReader(body))
	if err != nil {
		return bridgeSession{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set(exchange.MT5TokenHeader, token)
	}
	response, err := m.client.Do(request)
	if err != nil {
		return bridgeSession{}, err
	}
	defer func() { _ = response.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return bridgeSession{}, &refusedToken{status: response.StatusCode}
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		var answer struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &answer)
		return bridgeSession{}, &bridgeFailure{message: fmt.Sprintf("POST /session: %d %s", response.StatusCode,
			strings.ReplaceAll(answer.Error, password, "***")), status: response.StatusCode}
	}
	var session bridgeSession
	if err := json.Unmarshal(data, &session); err != nil {
		return bridgeSession{}, &bridgeFailure{message: fmt.Sprintf("POST /session: invalid answer: %v", err)}
	}
	return session, nil
}

// bridgeError returns err in words, never a credential: the token refused, the
// bridge unreachable, or its answer.
func bridgeError(err error) string {
	var refused *refusedToken
	var failure *bridgeFailure
	switch {
	case errors.As(err, &refused):
		return "a bridge recusou o token"
	case errors.As(err, &failure):
		return failure.message
	case err != nil:
		return "a bridge não respondeu: " + err.Error()
	}
	return ""
}

// status reads how the terminal of account stands and keeps it, see
// ui.MT5SessionStatus.
func (m *mt5Accounts) status(ctx context.Context, account store.MT5Account) (ui.MT5SessionStatus, bridgeSession) {
	status := ui.MT5SessionStatus{At: m.now().Unix()}
	var session bridgeSession
	if err := m.get(ctx, account, "/session", &session); err != nil {
		status.Error = bridgeError(err)
		var refused *refusedToken
		status.Reachable = errors.As(err, &refused)
	} else {
		status.Reachable, status.Connected, status.LoggedLogin = true, session.Connected, session.LoggedLogin
		status.Waiting = session.Waiting
		status.Current = session.Connected && session.LoggedLogin == account.Login
	}
	m.keep(account.ID, status)
	return status, session
}

func (m *mt5Accounts) keep(id int64, status ui.MT5SessionStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[id] = status
}

func (m *mt5Accounts) sessionOf(id int64) *ui.MT5SessionStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if status, ok := m.sessions[id]; ok {
		return &status
	}
	return nil
}

// runningOn returns the agents running that trade on the bridge of account,
// linked to it or to another account of the same terminal.
func (m *mt5Accounts) runningOn(ctx context.Context, account store.MT5Account) ([]string, error) {
	agents, err := m.store.Agents(ctx)
	if err != nil {
		return nil, err
	}
	var running []string
	for _, a := range agents {
		if a.Account == nil || !agentapi.SameMT5Bridge(a.Account.BridgeURL, account.BridgeURL) {
			continue
		}
		if m.isRunning(a.Name) {
			running = append(running, a.Name)
		}
	}
	return running, nil
}

// exposure returns the positions and the pending orders of agents (all the
// agents of the bridge without any) open on the account the terminal of
// bridge is logged in, eg: "matrend XAUUSDm #123", as read now; an error when
// the account can't be read, never taken as flat.
func (m *mt5Accounts) exposure(ctx context.Context, bridge string, agents []string) ([]string, error) {
	accounts, reads, err := m.positions.readFresh(ctx)
	if err != nil {
		return nil, err
	}
	var open []string
	for i, account := range accounts {
		if !agentapi.SameMT5Bridge(account.bridge, bridge) {
			continue
		}
		read := reads[i]
		if read.gap != "" {
			return nil, fmt.Errorf("as posições da conta não puderam ser lidas (%s): nada é alterado sem conferir",
				read.gap)
		}
		of := func(magic int64, symbol string) (string, bool) {
			owner, ok := ownerOf(account.agents, magic, symbol)
			if !ok || len(agents) > 0 && !slices.Contains(agents, owner.Name) {
				return "", false
			}
			return owner.Name, true
		}
		for _, p := range read.positions {
			if name, ok := of(p.Magic, p.Symbol); ok {
				open = append(open, fmt.Sprintf("posição de %s em %s #%d", name, p.Symbol, p.Ticket))
			}
		}
		for _, o := range read.orders {
			if name, ok := of(o.Magic, o.Symbol); ok {
				open = append(open, fmt.Sprintf("ordem pendente de %s em %s #%d", name, o.Symbol, o.Ticket))
			}
		}
	}
	return open, nil
}

// inUse returns why the account of MT5 can't change what its terminal is
// logged in (its login, server or bridge, deactivated, deleted, another
// account active in its place): agents running on its terminal, or positions
// and pending orders of the agents open on it, never left unwatched; "" when
// it can. An inactive account isn't the one of its terminal: never in use.
func (m *mt5Accounts) inUse(ctx context.Context, account store.MT5Account) (string, error) {
	if !account.Active {
		return "", nil
	}
	running, err := m.runningOn(ctx, account)
	if err != nil {
		return "", err
	}
	if len(running) > 0 {
		return fmt.Sprintf("agentes rodando no terminal da conta %s: %s; pare-os antes", account.Name,
			strings.Join(running, ", ")), nil
	}
	open, err := m.exposure(ctx, account.BridgeURL, nil)
	if err != nil {
		return err.Error(), nil
	}
	if len(open) > 0 {
		more := ""
		if len(open) > 5 {
			open, more = open[:5], fmt.Sprintf(" e mais %d", len(open)-5)
		}
		return fmt.Sprintf("a conta %s tem %s%s; feche-as antes, ou elas ficariam sem acompanhamento", account.Name,
			strings.Join(open, ", "), more), nil
	}
	return "", nil
}

// reconcile logs the terminal of each account active in it when it isn't, or
// gives the bridge its switches (allow_real, read_only) when they differ: only
// with no agent running on its terminal for a change of account; one at a
// time. A terminal waiting for a password the account lacks is told so.
func (m *mt5Accounts) reconcile(ctx context.Context) {
	m.running.Lock()
	defer m.running.Unlock()
	accounts, err := m.store.MT5Accounts(ctx)
	if err != nil {
		log.Errorf("mt5 accounts: %v", err)
		return
	}
	for _, account := range accounts {
		if !account.Active {
			continue
		}
		status, session := m.status(ctx, account)
		if status.Error != "" { // unreachable, the token refused, or a bridge without sessions yet
			continue
		}
		same := session.Login == account.Login && strings.EqualFold(session.Server, account.Server) &&
			!session.Legacy
		current := status.Current && same && session.AllowReal == account.AllowReal &&
			session.ReadOnly == account.ReadOnly
		if current {
			continue
		}
		switching := !(status.Current && session.LoggedLogin == account.Login)
		if switching {
			running, err := m.runningOn(ctx, account)
			if err != nil || len(running) > 0 {
				continue // never switched under agents running
			}
		}
		if switching && len(account.Password) == 0 && session.Waiting == "password" {
			status.Error = "o terminal pede a senha da conta: defina-a em Configurações › Contas MT5"
			m.keep(account.ID, status)
			continue
		}
		reason := "a conta ativa do terminal"
		if !switching {
			reason = "as opções da conta (permitir conta real, somente leitura)"
		}
		_, err = m.postSession(ctx, account)
		failure := bridgeError(err)
		if err := m.store.RecordMT5Session(ctx, account.ID, reason, failure); err != nil {
			log.Warnf("mt5 accounts: %v", err)
		}
		if failure != "" {
			log.Warnf("mt5 accounts: the session of %s (%d): %s", account.Name, account.Login, failure)
		} else {
			log.Infof("mt5 accounts: the terminal of %s serves %d on %s", account.BridgeURL, account.Login,
				account.Server)
		}
		status, _ = m.status(ctx, account)
		if failure != "" && status.Error == "" {
			status.Error = failure
			m.keep(account.ID, status)
		}
	}
}

// importAccounts creates an account of MT5 for each bridge of agents not
// linked to one yet, as the bridge tells it (its login, server, kind and
// currency; real allowed when it serves a real account already, which it only
// does allowed; read-only as it is), with the token one of its agents reaches
// it with, active; the account of the same login on the same server reused,
// never created twice nor changed; the agents of the bridge linked to it.
func (m *mt5Accounts) importAccounts(ctx context.Context) {
	agents, err := m.store.Agents(ctx)
	if err != nil {
		log.Errorf("mt5 accounts: %v", err)
		return
	}
	type bridgeAgents struct {
		url    string
		agents []store.Agent
	}
	var bridges []*bridgeAgents
	for _, a := range agents {
		if a.Account == nil || a.MT5AccountOf() != nil || a.Account.BridgeURL == "" {
			continue
		}
		i := slices.IndexFunc(bridges, func(b *bridgeAgents) bool {
			return agentapi.SameMT5Bridge(b.url, a.Account.BridgeURL)
		})
		if i < 0 {
			bridges = append(bridges, &bridgeAgents{url: a.Account.BridgeURL})
			i = len(bridges) - 1
		}
		bridges[i].agents = append(bridges[i].agents, a)
	}
	for _, b := range bridges {
		var info bridgeAccountInfo
		var health bridgeHealthInfo
		token, found := "", false
		tried := map[string]bool{}
		for _, a := range b.agents {
			secrets, err := m.store.SecretsOf(a)
			if err != nil || tried[secrets.BridgeToken] {
				continue
			}
			tried[secrets.BridgeToken] = true
			if bridgeGet(ctx, m.reads, 1<<20, b.url, "/account", secrets.BridgeToken, &info) == nil &&
				info.Login > 0 {
				token, found = secrets.BridgeToken, true
				_ = bridgeGet(ctx, m.reads, 1<<20, b.url, "/health", token, &health)
				break
			}
		}
		if !found {
			log.Warnf("mt5 accounts: the account of the bridge %s can't be read: its %d agents left on their own "+
				"settings, link them in Configurações › Contas MT5", b.url, len(b.agents))
			continue
		}
		if info.Server == "" {
			info.Server = health.Server
		}
		account, exists, err := m.store.MT5AccountByLogin(ctx, info.Login, info.Server)
		if err != nil {
			log.Errorf("mt5 accounts: %v", err)
			continue
		}
		if !exists {
			name, login, server, url := fmt.Sprintf("Conta %d", info.Login), info.Login, info.Server, b.url
			kind := kindOf(info.TradeMode)
			allowReal := kind == store.MT5KindReal // served already: the terminal allowed it
			account, err = m.store.CreateMT5Account(ctx, store.MT5AccountInput{Name: &name, Login: &login,
				Server: &server, BridgeURL: &url, AllowReal: &allowReal, ReadOnly: &health.ReadOnly,
				BridgeToken: &token}, !m.activeOn(ctx, url), kind,
				store.MT5Change{By: store.ImportedBy, Reason: "importada da bridge " + url})
			if err != nil {
				log.Errorf("mt5 accounts: importing %d of %s: %v", info.Login, url, err)
				continue
			}
			_ = m.store.SetMT5AccountCheck(ctx, account.ID, kind, info.Currency, info.Company, "", m.now(),
				store.MT5Change{})
			log.Infof("mt5 accounts: %d on %s imported from the bridge %s", info.Login, info.Server, url)
		}
		for _, a := range b.agents {
			if err := m.store.SetAgentMT5Account(ctx, a.ID, &account.ID,
				store.MT5Change{By: store.ImportedBy, Reason: "a conta da bridge do agente"}); err != nil {
				log.Errorf("mt5 accounts: linking %s: %v", a.Name, err)
			}
		}
	}
}

// activeOn reports whether an account is active on the terminal of bridge.
func (m *mt5Accounts) activeOn(ctx context.Context, bridge string) bool {
	accounts, err := m.store.MT5Accounts(ctx)
	if err != nil {
		return true // never two active: the import leaves it inactive
	}
	return slices.ContainsFunc(accounts, func(a store.MT5Account) bool {
		return a.Active && agentapi.SameMT5Bridge(a.BridgeURL, bridge)
	})
}

// PrepareMT5Accounts imports the accounts of the bridges and logs their
// terminals in the active ones, before the agents are launched, see
// mt5Accounts.
func (d *Dashboard) PrepareMT5Accounts(ctx context.Context) {
	if d.settings == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*mt5SessionTimeout)
	defer cancel()
	d.settings.mt5.importAccounts(ctx)
	d.settings.mt5.reconcile(ctx)
}

// RunMT5Accounts keeps the terminals logged in their accounts active until ctx
// is done, every mt5ReconcileEvery.
func (d *Dashboard) RunMT5Accounts(ctx context.Context) {
	if d.settings == nil {
		return
	}
	ticker := time.NewTicker(mt5ReconcileEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.settings.mt5.reconcile(ctx)
		}
	}
}

// infoOf returns the account as the dashboard shows it.
func (m *mt5Accounts) infoOf(account store.MT5Account, agents []string) ui.MT5AccountInfo {
	info := ui.MT5AccountInfo{ID: account.ID, Name: account.Name, Login: account.Login, Server: account.Server,
		BridgeURL: account.BridgeURL, AllowReal: account.AllowReal, ReadOnly: account.ReadOnly,
		Active: account.Active, Kind: account.Kind, Currency: account.Currency, Company: account.Company,
		CheckError: account.CheckError, PasswordSet: len(account.Password) > 0,
		TokenSet: len(account.BridgeToken) > 0, Agents: agents, UpdatedBy: account.UpdatedBy,
		UpdatedAt: account.UpdatedAt.Unix()}
	if info.Agents == nil {
		info.Agents = []string{}
	}
	if account.CheckedAt != nil {
		info.CheckedAt = account.CheckedAt.Unix()
	}
	if account.Active {
		info.Session = m.sessionOf(account.ID)
	}
	return info
}

// changeOf returns who changes the accounts in the request r, and why.
func changeOf(r *http.Request, reason string) store.MT5Change {
	change := store.MT5Change{By: userOf(r.Context()), Reason: strings.TrimSpace(reason)}
	if id := userIDOf(r.Context()); id != 0 {
		change.UserID = &id
	}
	return change
}

// secretWord tells what a change did of a secret, for the logs: never it.
func secretWord(secret *string) string {
	switch {
	case secret == nil:
		return "kept"
	case *secret == "":
		return "removed"
	}
	return "replaced"
}

func (s *settings) mt5Routes(r chi.Router) {
	r.Get("/settings/mt5-accounts", s.handleMT5Accounts)
	r.Post("/settings/mt5-accounts", s.handleCreateMT5Account)
	r.Patch("/settings/mt5-accounts/{id}", s.handleUpdateMT5Account)
	r.Delete("/settings/mt5-accounts/{id}", s.handleDeleteMT5Account)
	r.Post("/settings/mt5-accounts/{id}/activate", s.handleActivateMT5Account)
	r.Post("/settings/mt5-accounts/{id}/test", s.handleTestMT5Account)
	r.Get("/settings/mt5-accounts/{id}/events", s.handleMT5AccountEvents)
}

// handleMT5Accounts answers the accounts of MT5, see ui.MT5AccountsResponse.
func (s *settings) handleMT5Accounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.MT5Accounts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	agents, err := s.store.MT5AccountAgents(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	response := ui.MT5AccountsResponse{Accounts: make([]ui.MT5AccountInfo, 0, len(accounts))}
	for _, account := range accounts {
		response.Accounts = append(response.Accounts, s.mt5.infoOf(account, agents[account.ID]))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, response)
}

// inputOf returns the account of the request.
func inputOf(request ui.MT5AccountRequest) store.MT5AccountInput {
	return store.MT5AccountInput{Name: request.Name, Login: request.Login, Server: request.Server,
		BridgeURL: request.BridgeURL, AllowReal: request.AllowReal, ReadOnly: request.ReadOnly,
		Password: request.Password, BridgeToken: request.BridgeToken}
}

// handleCreateMT5Account creates an account of MT5, inactive (see
// handleActivateMT5Account): its name, login, server and bridge required.
func (s *settings) handleCreateMT5Account(w http.ResponseWriter, r *http.Request) {
	var request ui.MT5AccountRequest
	if !decode(w, r, &request) {
		return
	}
	if request.Name == nil || request.Login == nil || request.Server == nil || request.BridgeURL == nil {
		writeJSONError(w, http.StatusBadRequest, "o nome, o login, o servidor e a bridge da conta são obrigatórios")
		return
	}
	account, err := s.store.CreateMT5Account(r.Context(), inputOf(request), false, "", changeOf(r, request.Reason))
	if err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the account of MT5 %s (%d) created by %s, its password %s, its token %s", account.Name,
		account.Login, userOf(r.Context()), secretWord(request.Password), secretWord(request.BridgeToken))
	writeCreated(w, s.mt5.infoOf(account, nil))
}

// mt5Account returns the account of the path, answering when it can't.
func (s *settings) mt5Account(w http.ResponseWriter, r *http.Request) (store.MT5Account, bool) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return store.MT5Account{}, false
	}
	account, err := s.store.MT5Account(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return store.MT5Account{}, false
	}
	return account, true
}

// refuseInUse answers 409 with why the account is in use, see
// mt5Accounts.inUse, and reports whether it did.
func (s *settings) refuseInUse(w http.ResponseWriter, r *http.Request, account store.MT5Account,
	what string) bool {
	why, err := s.mt5.inUse(r.Context(), account)
	if err != nil {
		s.fail(w, err)
		return true
	}
	if why == "" {
		return false
	}
	writeJSONError(w, http.StatusConflict, what+": "+why)
	return true
}

// handleUpdateMT5Account changes an account of MT5, see ui.MT5AccountRequest:
// its login, server or bridge, or its deactivation, refused (409) while it is
// in use (see mt5Accounts.inUse); its password, token and switches whenever,
// the switches given to its terminal at once.
func (s *settings) handleUpdateMT5Account(w http.ResponseWriter, r *http.Request) {
	account, ok := s.mt5Account(w, r)
	if !ok {
		return
	}
	var request ui.MT5AccountRequest
	if !decode(w, r, &request) {
		return
	}
	moved := request.Login != nil && *request.Login != account.Login ||
		request.Server != nil && !strings.EqualFold(strings.TrimSpace(*request.Server), account.Server) ||
		request.BridgeURL != nil && strings.TrimSpace(*request.BridgeURL) != account.BridgeURL
	deactivated := request.Active != nil && !*request.Active && account.Active
	if request.Active != nil && *request.Active && !account.Active {
		writeJSONError(w, http.StatusBadRequest, "para ativar a conta, use Ativar (POST .../activate)")
		return
	}
	if (moved || deactivated) && s.refuseInUse(w, r, account, "a conta está em uso") {
		return
	}
	change := changeOf(r, request.Reason)
	_, next, err := s.store.UpdateMT5Account(r.Context(), account.ID, inputOf(request), change)
	if err != nil {
		s.fail(w, err)
		return
	}
	if deactivated {
		if _, err := s.store.ActivateMT5Account(r.Context(), account.ID, false, change); err != nil {
			s.fail(w, err)
			return
		}
		next.Active = false
	}
	log.Infof("ui: the account of MT5 %s (%d) changed by %s, its password %s, its token %s", next.Name,
		next.Login, userOf(r.Context()), secretWord(request.Password), secretWord(request.BridgeToken))
	if next.Active {
		s.mt5.reconcile(context.WithoutCancel(r.Context()))
	}
	s.manager.Trigger()
	s.answerMT5Account(w, r, next.ID)
}

// answerMT5Account answers the account of MT5 of id as it stands.
func (s *settings) answerMT5Account(w http.ResponseWriter, r *http.Request, id int64) {
	account, err := s.store.MT5Account(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	agents, err := s.store.MT5AccountAgents(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, s.mt5.infoOf(account, agents[id]))
}

// handleDeleteMT5Account deletes an account of MT5, inactive and without
// agents (409 else); its record kept.
func (s *settings) handleDeleteMT5Account(w http.ResponseWriter, r *http.Request) {
	account, ok := s.mt5Account(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteMT5Account(r.Context(), account.ID, changeOf(r, "")); err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the account of MT5 %s (%d) deleted by %s", account.Name, account.Login, userOf(r.Context()))
	w.WriteHeader(http.StatusNoContent)
}

// handleActivateMT5Account makes an account of MT5 the one of its terminal,
// logged in it at once: refused (409) while the account active of the
// terminal is in use (see mt5Accounts.inUse).
func (s *settings) handleActivateMT5Account(w http.ResponseWriter, r *http.Request) {
	account, ok := s.mt5Account(w, r)
	if !ok {
		return
	}
	if account.Active {
		s.answerMT5Account(w, r, account.ID)
		return
	}
	if len(account.Password) == 0 {
		writeJSONError(w, http.StatusBadRequest,
			"defina a senha da conta antes de ativá-la: o terminal precisa dela para logar")
		return
	}
	accounts, err := s.store.MT5Accounts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, other := range accounts {
		if other.Active && other.BridgeKey == account.BridgeKey &&
			s.refuseInUse(w, r, other, "o terminal está em uso pela conta "+other.Name) {
			return
		}
	}
	if _, err := s.store.ActivateMT5Account(r.Context(), account.ID, true, changeOf(r, "")); err != nil {
		s.fail(w, err)
		return
	}
	log.Warnf("ui: the account of MT5 %s (%d) activated on %s by %s", account.Name, account.Login,
		account.BridgeURL, userOf(r.Context()))
	s.mt5.reconcile(context.WithoutCancel(r.Context()))
	s.manager.Trigger()
	s.answerMT5Account(w, r, account.ID)
}

// handleTestMT5Account tests an account of MT5, see ui.MT5AccountTest: never
// logs its terminal in another account than the one it serves.
func (s *settings) handleTestMT5Account(w http.ResponseWriter, r *http.Request) {
	account, ok := s.mt5Account(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	result := ui.MT5AccountTest{At: s.mt5.now().Unix()}
	var health bridgeHealthInfo
	err := s.mt5.get(ctx, account, "/health", &health)
	var refused *refusedToken
	switch {
	case errors.As(err, &refused):
		result.Reachable, result.Error = true, bridgeError(err)
		result.Detail = "confira o token da bridge da conta (o MT5_BRIDGE_TOKEN do terminal)"
	case err != nil:
		result.Error = bridgeError(err)
		result.Detail = "confira a URL da bridge e se o terminal está no ar"
	default:
		result.Reachable, result.TokenOK, result.Connected = true, true, health.Connected
		result.LoggedLogin = health.Login
		result.Current = health.Connected && health.Login == account.Login
		switch {
		case result.Current:
			var info bridgeAccountInfo
			if err := s.mt5.get(ctx, account, "/account", &info); err != nil {
				result.Error = bridgeError(err)
				break
			}
			result.Kind, result.Currency, result.Company = kindOf(info.TradeMode), info.Currency, info.Company
			result.Server = info.Server
			result.Detail = "o terminal está nesta conta"
		case health.Waiting != "":
			result.Detail = map[string]string{"session": "o terminal aguarda a conta: ative-a",
				"password": "o terminal pede a senha: defina-a e ative a conta"}[health.Waiting]
		case health.Connected:
			result.Detail = fmt.Sprintf("o terminal está na conta %d: testar esta exigiria trocar a conta dele; "+
				"ative-a para trocar", health.Login)
		default:
			result.Detail = "o terminal não está conectado"
		}
	}
	failure := result.Error
	if result.Current || failure != "" {
		if err := s.store.SetMT5AccountCheck(ctx, account.ID, result.Kind, result.Currency, result.Company,
			failure, time.Unix(result.At, 0), changeOf(r, "")); err != nil {
			log.Warnf("mt5 accounts: %v", err)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, result)
}

// handleMT5AccountEvents answers the last events of an account of MT5, never
// a secret.
func (s *settings) handleMT5AccountEvents(w http.ResponseWriter, r *http.Request) {
	account, ok := s.mt5Account(w, r)
	if !ok {
		return
	}
	events, err := s.store.MT5AccountEvents(r.Context(), account.ID, 50)
	if err != nil {
		s.fail(w, err)
		return
	}
	agents, err := s.store.Agents(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	names := make(map[int64]string, len(agents))
	for _, a := range agents {
		names[a.ID] = a.Name
	}
	response := ui.MT5AccountEventsResponse{Events: make([]ui.MT5AccountEventInfo, 0, len(events))}
	for _, e := range events {
		info := ui.MT5AccountEventInfo{At: e.At.Unix(), Kind: e.Kind, By: e.RequestedBy, Reason: e.Reason}
		if e.AgentID != nil {
			info.Agent = names[*e.AgentID]
		}
		if len(e.Previous) > 0 {
			info.Previous = json.RawMessage(e.Previous)
		}
		if len(e.Next) > 0 {
			info.Next = json.RawMessage(e.Next)
		}
		response.Events = append(response.Events, info)
	}
	writeJSON(w, response)
}

// linkAgent links the agent a to the account of MT5 of id (0 unlinks it), by
// the user of r: refused while it runs, or holds positions or pending orders
// on its current account, never switched under it. It reports whether it
// answered.
func (s *settings) linkAgent(w http.ResponseWriter, r *http.Request, a store.Agent, id int64) bool {
	current := int64(0)
	if account := a.MT5AccountOf(); account != nil {
		current = account.ID
	}
	if id == current {
		return false
	}
	if s.mt5.isRunning(a.Name) {
		writeJSONError(w, http.StatusConflict, "pare o agente antes de trocar a conta MT5 dele")
		return true
	}
	if a.Account != nil && a.Account.BridgeURL != "" {
		open, err := s.mt5.exposure(r.Context(), a.Account.BridgeURL, []string{a.Name})
		if err != nil {
			writeJSONError(w, http.StatusConflict, err.Error())
			return true
		}
		if len(open) > 0 {
			writeJSONError(w, http.StatusConflict, fmt.Sprintf("o agente tem %s na conta atual: feche-as antes "+
				"de trocar a conta, ou ficariam sem acompanhamento", strings.Join(open, ", ")))
			return true
		}
	}
	var target *int64
	if id != 0 {
		target = &id
	}
	if err := s.store.SetAgentMT5Account(r.Context(), a.ID, target, changeOf(r, "")); err != nil {
		s.fail(w, err)
		return true
	}
	return false
}
