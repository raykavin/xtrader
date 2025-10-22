package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/tools/log"
)

// reconcileEvery is how often the Manager applies the database without a
// change made through the controller, eg: one made in SQL.
var reconcileEvery = 30 * time.Second

// TelegramConfig are the settings of the Telegram bot of the database.
type TelegramConfig struct {
	Enabled bool
	Token   string
	Users   []int64
}

// Bot is the Telegram bot of the controller, see TelegramRunner.
type Bot interface {
	// Apply starts, stops or restarts the bot for cfg, or changes its users.
	Apply(cfg TelegramConfig)
	// SyncRelays relays the events of the agents of names, the ones
	// enabled, and no other.
	SyncRelays(names []string)
}

// AgentState is the state of an agent of the controller: its process, and
// whether a change of its settings waits for a restart to apply.
type AgentState struct {
	ProcessState
	RestartPending bool
}

// Manager applies the database to the controller: the agents enabled launched
// and the others stopped, their list served by the dashboard, the runtime of
// each one (see agentapi.Runtime), and the Telegram bot. It runs after every
// change made through the controller (see Trigger), and every reconcileEvery.
// A change of the settings an agent was launched with waits for its restart
// (see Restart): it may be trading.
type Manager struct {
	store      *store.Store
	agents     config.Agents
	supervisor *Supervisor
	tokens     *tokens
	runtimes   *runtimes
	dashboard  *Dashboard
	bot        Bot

	trigger chan struct{}

	mu       sync.Mutex
	launched map[string]launch // what each agent runs with
	byName   map[string]store.Agent
}

// launch is what an agent was launched with: the hash of its settings (see
// launchHash), and the address of its API, where the process listens until it
// is restarted, whatever the database says meanwhile.
type launch struct {
	hash    [32]byte
	address string
}

// NewManager creates the manager of the agents of s, run by sup with the
// commands of agents, served by dashboard, with the Telegram bot, if any.
func NewManager(s *store.Store, agents config.Agents, sup *Supervisor, dashboard *Dashboard, bot Bot) *Manager {
	return &Manager{store: s, agents: agents, supervisor: sup, tokens: newTokens(), runtimes: newRuntimes(),
		dashboard: dashboard, bot: bot, trigger: make(chan struct{}, 1), launched: make(map[string]launch),
		byName: make(map[string]store.Agent)}
}

// Trigger makes the manager apply the database now.
func (m *Manager) Trigger() {
	select {
	case m.trigger <- struct{}{}:
	default:
	}
}

// Run applies the database until ctx is done, then stops the agents.
func (m *Manager) Run(ctx context.Context) {
	defer m.supervisor.StopAll()
	ticker := time.NewTicker(reconcileEvery)
	defer ticker.Stop()
	for {
		if err := m.Reconcile(ctx); err != nil && ctx.Err() == nil {
			log.Errorf("controller: the agents of the database: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-m.trigger:
		case <-ticker.C:
		}
	}
}

// Reconcile applies the database once.
func (m *Manager) Reconcile(ctx context.Context) error {
	agents, err := m.store.Agents(ctx)
	if err != nil {
		return err
	}
	byName := make(map[string]store.Agent, len(agents))
	m.mu.Lock()
	launched := maps.Clone(m.launched)
	m.mu.Unlock()
	var served []Agent
	var relayed []string
	for _, a := range agents {
		byName[a.Name] = a
		if !a.Enabled {
			continue
		}
		address := a.APIAddress
		if l, ok := launched[a.Name]; ok && m.supervisor.Running(a.Name) {
			address = l.address // a new one waits for the restart, as the process listens there
		}
		served = append(served, Agent{Name: a.Name, Address: address})
		relayed = append(relayed, a.Name)
		if doc, ok := runtimeOf(ctx, m.store, a); ok {
			m.runtimes.set(a.ID, doc)
		}
	}
	if err := m.dashboard.SetAgents(served); err != nil {
		log.Errorf("controller: %v", err)
	}
	m.mu.Lock()
	m.byName = byName
	m.mu.Unlock()
	if m.bot != nil {
		m.bot.SyncRelays(relayed)
	}

	for _, a := range agents {
		wanted := a.Enabled && a.Launched
		running := m.supervisor.Running(a.Name)
		switch {
		case wanted && !running:
			if err := m.start(a); err != nil {
				log.Errorf("controller: agent %s: %v", a.Name, err)
			}
		case !wanted && running:
			go m.stop(a.Name)
		}
	}
	m.mu.Lock()
	for name := range m.launched {
		if _, ok := byName[name]; !ok && m.supervisor.Running(name) {
			go m.stop(name) // gone from the database
		}
	}
	m.mu.Unlock()

	if m.bot != nil {
		cfg, err := m.telegramConfig(ctx)
		if err != nil {
			return err
		}
		m.bot.Apply(cfg)
	}
	return nil
}

// telegramConfig returns the settings of the Telegram bot of the database.
func (m *Manager) telegramConfig(ctx context.Context) (TelegramConfig, error) {
	settings, _, err := m.store.Telegram(ctx)
	if err != nil {
		return TelegramConfig{}, err
	}
	token, err := m.store.TelegramToken(ctx)
	if err != nil {
		return TelegramConfig{}, err
	}
	users, err := m.store.TelegramUsers(ctx)
	if err != nil {
		return TelegramConfig{}, err
	}
	cfg := TelegramConfig{Enabled: settings.Enabled, Token: token}
	for _, user := range users {
		cfg.Users = append(cfg.Users, user.TelegramID)
	}
	return cfg, nil
}

// start launches the agent a with the command of its kind, each start with a
// token of its own.
func (m *Manager) start(a store.Agent) error {
	env := make(map[string]string, len(m.agents.Env)+2)
	for name, value := range m.agents.Env.Map() {
		env[name] = value
	}
	env[agentapi.EnvURL] = "http://" + m.agents.API
	env[agentapi.EnvAgent] = a.Name
	spec := ProcessSpec{Name: a.Name, Command: strings.Fields(m.agents.Command(a.Kind)), Dir: m.agents.Dir,
		Env: env, Restart: a.Restart}
	id := a.ID
	prepare := func() (map[string]string, func(), error) {
		token, err := m.tokens.issue(id)
		if err != nil {
			return nil, nil, err
		}
		return map[string]string{agentapi.EnvToken: token}, func() { m.tokens.revoke(token) }, nil
	}
	if err := m.supervisor.Start(spec, prepare); err != nil {
		return err
	}
	m.mu.Lock()
	m.launched[a.Name] = launch{hash: launchHash(a), address: a.APIAddress}
	m.mu.Unlock()
	return nil
}

// stop stops the agent of name.
func (m *Manager) stop(name string) {
	m.supervisor.Stop(name)
	m.mu.Lock()
	delete(m.launched, name)
	m.mu.Unlock()
}

// Restart stops the agent of name and launches it again, with its settings of
// the database, when it is enabled.
func (m *Manager) Restart(ctx context.Context, name string) error {
	a, err := m.store.AgentByName(ctx, name)
	if err != nil {
		return err
	}
	m.stop(name)
	defer m.Trigger() // the dashboard follows the address it listens at now
	if !a.Enabled || !a.Launched {
		return nil
	}
	return m.start(a)
}

// State returns the state of the agent of name.
func (m *Manager) State(name string) AgentState {
	state := AgentState{ProcessState: m.supervisor.State(name)}
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.launched[name]; ok && state.Running {
		if a, found := m.byName[name]; found {
			state.RestartPending = l.hash != launchHash(a)
		}
	}
	return state
}

// runtimeOf returns the runtime of the agent a: its channels, and the prompt
// of an LLM agent. false when its prompt can't be read: the agent keeps the
// runtime it has rather than switching to its default prompt.
func runtimeOf(ctx context.Context, s *store.Store, a store.Agent) (agentapi.Runtime, bool) {
	doc := agentapi.Runtime{TradePanel: a.TradePanel, TelegramControl: a.TelegramControl}
	if a.LLM != nil && a.LLM.PromptID != nil {
		prompt, err := s.Prompt(ctx, *a.LLM.PromptID)
		if err != nil {
			log.Warnf("controller: the prompt of the agent %s: %v", a.Name, err)
			return doc, false
		}
		doc.Prompt = &agentapi.PromptRef{ID: prompt.ID, Name: prompt.Name, Version: prompt.Version}
	}
	return doc, true
}

// launchHash is the hash of the settings an agent is launched with: the ones
// it applies while it runs (its switches, its prompt) left out.
func launchHash(a store.Agent) [32]byte {
	settings := struct {
		Kind, Mode, APIAddress string
		Restart                bool
		Params                 store.Params
		Account                *store.Account
		LLM                    *store.LLMSettings
		BridgeToken            []byte
		APIKey, CalendarToken  []byte
	}{Kind: a.Kind, Mode: a.Mode, APIAddress: a.APIAddress, Restart: a.Restart, Params: a.Params}
	if a.Account != nil {
		account := *a.Account
		settings.BridgeToken = account.BridgeToken
		account.CreatedAt, account.UpdatedAt = time.Time{}, time.Time{}
		settings.Account = &account
	}
	if a.LLM != nil {
		model := *a.LLM
		settings.APIKey, settings.CalendarToken = model.APIKey, model.CalendarToken
		model.PromptID, model.PromptStatus = nil, nil
		model.CreatedAt, model.UpdatedAt = time.Time{}, time.Time{}
		settings.LLM = &model
	}
	data, _ := json.Marshal(settings)
	return sha256.Sum256(data)
}
