package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
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

// samePositions is the error of an agent that would trade on MT5 the
// positions of another agent running, see Manager.conflict.
type samePositions string

func (e samePositions) Error() string { return string(e) }

// AgentState is the state of an agent of the controller: its process, whether
// a change of its settings waits for a restart to apply, why the controller
// didn't launch it, eg: another agent running trades the same positions, and
// Exited, that it exited on its own without Restart: the controller doesn't
// launch it again until it is restarted, enabled again or the settings of its
// launch change after it exited (see held), a state kept in memory only: a
// controller started again launches it.
type AgentState struct {
	ProcessState
	RestartPending bool
	LaunchError    string
	Exited         bool
}

// Manager applies the database to the controller: the agents enabled launched
// and the others stopped, their list served by the dashboard, the runtime of
// each one (see agentapi.Runtime), and the Telegram bot. It runs after every
// change made through the controller (see Trigger), and every reconcileEvery.
// A change of the settings an agent was launched with waits for its restart
// (see Restart): it may be trading. The supervisor starts an agent of Restart
// again with the settings of its launch once it exited, its secrets the ones
// of now (see launchedAs), and an agent without Restart that exited stays
// stopped (see AgentState.Exited).
// An agent that would trade on MT5 the positions of another one running isn't
// launched (see conflict), whatever the database says, eg: two agents of the
// same magic number saved before the store refused them.
//
// The Reconciles run one at a time, and the starts, stops and restarts of an
// agent one at a time (see agentLocks), a stop or a restart holding its agent
// alone while its process exits: the lock of an agent comes before
// reconciling (a Reconcile only tries the ones of the agents), then starting,
// then mu, then the ones of the supervisor, whose callbacks (see Prepare) take
// none of them.
type Manager struct {
	store      *store.Store
	agents     config.Agents
	supervisor *Supervisor
	tokens     *tokens
	runtimes   *runtimes
	backlogs   *backlogs
	dashboard  *Dashboard
	bot        Bot
	exposure   *exposureGuard // weighs the entries the agents claim, see ServeSettings

	trigger chan struct{}

	// reconciling makes each Reconcile apply the database it read whole before
	// the next one reads it, and a restart launch its agent between two of
	// them: nothing a Reconcile read applies after a newer change.
	reconciling sync.Mutex
	locks       agentLocks
	starting    sync.Mutex // the launches one at a time, each checked with the others (see conflict)

	// fences keeps agents from launching while the supervisor closes their
	// positions on their bridge, by name, see Fence: a leaf, taken alone, by
	// the starts and the prepares of the supervisor of the processes too
	fenceMu sync.Mutex
	fences  map[string]int

	mu        sync.Mutex
	closed    bool              // the controller stops: nothing launched anymore, see Run
	launched  map[string]launch // what each agent runs with
	byName    map[string]store.Agent
	launchErr map[string]string   // why an agent wasn't launched, see AgentState.LaunchError
	exited    map[string][32]byte // see AgentState.Exited: the hash of the settings each one exited with
}

// launch is what an agent was launched with: its settings, the ones it reads
// on each start until it is restarted (see launchedAs), with the secrets it
// read last, their hash (see launchHash), the address of its API, where the
// process listens until it is restarted, whatever the database says
// meanwhile, and what it trades on MT5 in live mode, of the settings it read
// last (see admit).
type launch struct {
	agent   store.Agent
	hash    [32]byte
	address string
	live    bool
	book    agentapi.MT5Book
}

// agentLocks are the locks of the agents, by name: one start, stop or restart
// of an agent at a time, a stop holding it until the process exited.
type agentLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// of returns the lock of the agent of name.
func (l *agentLocks) of(name string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	lock, ok := l.locks[name]
	if !ok {
		lock = new(sync.Mutex)
		l.locks[name] = lock
	}
	return lock
}

// NewManager creates the manager of the agents of s, run by sup with the
// commands of agents, served by dashboard, with the Telegram bot, if any.
func NewManager(s *store.Store, agents config.Agents, sup *Supervisor, dashboard *Dashboard, bot Bot) *Manager {
	return &Manager{store: s, agents: agents, supervisor: sup, tokens: newTokens(), runtimes: newRuntimes(),
		backlogs: newBacklogs(), dashboard: dashboard, bot: bot, trigger: make(chan struct{}, 1),
		locks: agentLocks{locks: make(map[string]*sync.Mutex)}, launched: make(map[string]launch),
		byName: make(map[string]store.Agent), launchErr: make(map[string]string),
		exited: make(map[string][32]byte), fences: make(map[string]int)}
}

// errFenced is the error of a launch of an agent kept from launching, see
// Fence.
var errFenced = errors.New("the supervisor closes its positions: launched once done")

// Fence keeps the agent of name from launching until release: its starts, the
// ones of the supervisor of the processes once it exited included, fail
// (errFenced) and are tried again later, eg: while the supervisor closes its
// positions on its bridge, which nothing else trades meanwhile. A start
// begun before goes on: State, which waits for it, tells it.
func (m *Manager) Fence(name string) (release func()) {
	m.fenceMu.Lock()
	m.fences[name]++
	m.fenceMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.fenceMu.Lock()
			if m.fences[name]--; m.fences[name] <= 0 {
				delete(m.fences, name)
			}
			m.fenceMu.Unlock()
			m.Trigger() // launched now if it is wanted
		})
	}
}

// fenced reports whether the agent of name is kept from launching, see Fence.
func (m *Manager) fenced(name string) bool {
	m.fenceMu.Lock()
	defer m.fenceMu.Unlock()
	return m.fences[name] > 0
}

// RefreshRuntime reads the agent of name again and publishes its runtime at
// once, eg: its entries just blocked, rather than at the next Reconcile.
func (m *Manager) RefreshRuntime(ctx context.Context, name string) error {
	a, err := m.store.AgentByName(ctx, name)
	if err != nil {
		return err
	}
	doc, ok := runtimeOf(ctx, m.store, a)
	if !ok {
		return fmt.Errorf("the runtime of the agent %s can't be read now", name)
	}
	m.runtimes.set(a.ID, doc)
	return nil
}

// Trigger makes the manager apply the database now.
func (m *Manager) Trigger() {
	select {
	case m.trigger <- struct{}{}:
	default:
	}
}

// Run applies the database until ctx is done, then stops the agents for good:
// a restart in flight, or asked afterwards, launches nothing (ErrStopped).
func (m *Manager) Run(ctx context.Context) {
	defer m.close()
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

// close stops the agents for good, see Run.
func (m *Manager) close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.supervisor.StopAll()
}

// stopping reports whether the controller stops, see Run.
func (m *Manager) stopping() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// Reconcile applies the database once, the Reconciles one at a time (see
// reconciling). It never waits for an agent to stop: an agent being started,
// stopped or restarted is left to the Reconcile after that, see Trigger.
func (m *Manager) Reconcile(ctx context.Context) error {
	m.reconciling.Lock()
	defer m.reconciling.Unlock()
	if m.stopping() {
		return nil
	}
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
	unserved := make(map[string]error) // the agents of an address off the loopback, see agentAPI
	for _, a := range agents {
		byName[a.Name] = a
		if !a.Enabled {
			continue
		}
		address := a.APIAddress
		if l, ok := launched[a.Name]; ok && m.supervisor.Running(a.Name) {
			address = l.address // a new one waits for the restart, as the process listens there
		}
		if _, err := agentAPI(address); err != nil {
			// saved before the store refused it: neither served nor launched
			unserved[a.Name] = fmt.Errorf("the address of its API: %w: change it in its settings", err)
			continue
		}
		served = append(served, Agent{Name: a.Name, Address: address, Kind: a.Kind})
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
		held := "" // why its account of MT5 keeps it stopped: inactive, or its terminal switching
		if wanted {
			held = m.dashboard.mt5Hold(a)
		}
		m.apply(a, wanted && held == "")
		switch {
		case held != "":
			if m.noteLaunch(a.Name, errors.New(held)) {
				log.Infof("controller: agent %s stopped: %s", a.Name, held)
			}
		case !wanted:
			if err := unserved[a.Name]; m.noteLaunch(a.Name, err) {
				log.Errorf("controller: agent %s: %v", a.Name, err)
			}
		}
	}
	m.mu.Lock()
	var gone []string
	for name := range m.launched {
		if _, ok := byName[name]; !ok {
			gone = append(gone, name)
		}
	}
	for name := range m.launchErr {
		if _, ok := byName[name]; !ok {
			delete(m.launchErr, name)
		}
	}
	for name := range m.exited {
		if _, ok := byName[name]; !ok {
			delete(m.exited, name)
		}
	}
	m.mu.Unlock()
	for _, name := range gone {
		m.apply(store.Agent{Name: name}, false)
	}

	if m.bot != nil {
		cfg, err := m.telegramConfig(ctx)
		if err != nil {
			return err
		}
		m.bot.Apply(cfg)
	}
	return nil
}

// apply launches the agent a of the database when wanted and not running,
// unless it exited on its own (see held), or stops it when not wanted and
// running, the stop holding the lock of the agent until its process exited.
// An agent being started, stopped or restarted is left as it is: the
// Reconcile after that applies it (see Trigger).
func (m *Manager) apply(a store.Agent, wanted bool) {
	lock := m.locks.of(a.Name)
	if !lock.TryLock() {
		return
	}
	running := m.supervisor.Running(a.Name)
	switch {
	case wanted && !running:
		if !m.held(a) {
			if err := m.start(a); !errors.Is(err, ErrStopped) && m.noteLaunch(a.Name, err) {
				log.Errorf("controller: agent %s: %v", a.Name, err)
			}
		}
	case !wanted && running:
		go func() {
			m.stop(a.Name)
			lock.Unlock()
			m.Trigger() // an agent it kept from launching may launch now
		}()
		return
	case !wanted:
		m.mu.Lock()
		delete(m.launched, a.Name) // exited meanwhile: enabled again, it is launched
		delete(m.exited, a.Name)
		m.mu.Unlock()
	}
	lock.Unlock()
}

// held reports whether the agent a, wanted and not running, stays stopped: it
// exited on its own, without Restart, and is launched again once restarted,
// enabled again, or once the settings of its launch differ from the ones it
// exited with (see AgentState.Exited): the ones of a as the first Reconcile
// after its exit reads them, a change saved while it ran waiting for its
// restart. That Reconcile is the one its exit triggers, the process forgotten
// by then (see start): a change saved before it read them counts as one
// saved while the agent ran, and keeps it stopped. The caller holds the lock
// of the agent.
func (m *Manager) held(a store.Agent) bool {
	hash := launchHash(a)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.launched[a.Name]; ok {
		// launched, never stopped, not running: it exited on its own
		delete(m.launched, a.Name)
		m.exited[a.Name] = hash
		log.Warnf("controller: agent %s exited, without restart: launched again once restarted, enabled again "+
			"or its settings changed", a.Name)
	}
	exited, ok := m.exited[a.Name]
	if ok && exited != hash {
		delete(m.exited, a.Name) // its settings changed since
		return false
	}
	return ok
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
// token of its own and a key of its API (see agentKeys), unless it would trade
// the positions of another agent running (see conflict), its API is off the
// loopback (see agentAPI) or the controller stops (ErrStopped). The settings
// of a are the ones of each start of it, the ones of the supervisor once it
// exited included, until it is restarted, see launchedAs. The caller holds the
// lock of the agent.
func (m *Manager) start(a store.Agent) error {
	if _, err := agentAPI(a.APIAddress); err != nil {
		return fmt.Errorf("the address of its API: %w: change it in its settings", err)
	}
	if m.fenced(a.Name) {
		return errFenced
	}
	if account := a.MT5AccountOf(); account != nil && !account.Active {
		return fmt.Errorf("a conta MT5 %s não é a ativa do terminal: ative-a ou associe outra conta ao agente "+
			"em Configurações › Contas MT5", account.Name)
	}
	m.starting.Lock()
	defer m.starting.Unlock()
	live, book := a.Mode == "live", a.MT5Book()
	m.mu.Lock()
	err := m.conflict(a.Name, live, book)
	if err == nil && m.closed {
		err = fmt.Errorf("%s: %w", a.Name, ErrStopped)
	}
	if err == nil { // before the process reads them
		m.launched[a.Name] = launch{agent: a, hash: launchHash(a), address: a.APIAddress, live: live, book: book}
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}

	env := make(map[string]string, len(m.agents.Env)+2)
	for name, value := range m.agents.Env.Map() {
		env[name] = value
	}
	env[agentapi.EnvURL] = "http://" + m.agents.API
	env[agentapi.EnvAgent] = a.Name
	spec := ProcessSpec{Name: a.Name, Command: strings.Fields(m.agents.Command(a.Kind)), Dir: m.agents.Dir,
		Env: env, Restart: a.Restart}
	id, name, restart := a.ID, a.Name, a.Restart
	keys := m.dashboard.keys
	prepare := func() (map[string]string, func(), error) {
		if m.fenced(name) {
			return nil, nil, errFenced // started again later, once its positions are closed
		}
		token, err := m.tokens.issue(id)
		if err != nil {
			return nil, nil, err
		}
		key, err := randomToken()
		if err != nil {
			m.tokens.revoke(token)
			return nil, nil, err
		}
		keys.set(name, key)
		return map[string]string{agentapi.EnvToken: token, agentapi.EnvAPIKey: key}, func() {
			m.tokens.revoke(token)
			keys.drop(name, key)
			if !restart {
				m.Trigger() // exited for good: stopped, its state told at once (see held)
			}
		}, nil
	}
	if err := m.supervisor.Start(spec, prepare); err != nil {
		m.mu.Lock()
		delete(m.launched, a.Name)
		m.mu.Unlock()
		return err
	}
	return nil
}

// launchedAs returns the settings the agent current, as the database has it,
// runs with: the ones of its launch, the starts of the supervisor once it
// exited included, a change of which waits for its restart (see Restart),
// with its switches and its prompt of current, which apply while it runs,
// and its secrets of current, see withSecretsOf. current itself for an agent
// the controller didn't launch.
func (m *Manager) launchedAs(current store.Agent) store.Agent {
	m.mu.Lock()
	l, ok := m.launched[current.Name]
	m.mu.Unlock()
	if !ok || l.agent.ID != current.ID {
		return current
	}
	a := withSecretsOf(l.agent, current)
	a.TradePanel, a.TelegramControl, a.Reanalysis = current.TradePanel, current.TelegramControl, current.Reanalysis
	a.Supervision = current.Supervision // its entries apply while it runs
	a.Protection = current.Protection   // and the protection of its positions
	if a.LLM != nil && current.LLM != nil {
		model := *a.LLM
		model.PromptID, model.PromptStatus = current.LLM.PromptID, current.LLM.PromptStatus
		a.LLM = &model
	}
	return a
}

// withSecretsOf returns a, the settings of a launch, with the secrets of
// current, the ones of now: a secret is no setting of trading, and the old
// one, eg: a token of the bridge revoked and replaced, would keep the agent
// started again by the supervisor failing until it is restarted. Each secret
// goes with its endpoint, never to another one: the token of the bridge with
// the same bridge, the key of the model with the same provider and base URL;
// the one of the launch stays with the endpoint of the launch. The token of
// the calendar is the one of the news of all the agents, in their runtime.
func withSecretsOf(a, current store.Agent) store.Agent {
	if a.Account != nil && current.Account != nil && a.Account.BridgeURL == current.Account.BridgeURL {
		account := *a.Account
		account.BridgeToken = current.Account.BridgeToken
		a.Account = &account
	}
	if a.LLM != nil && current.LLM != nil {
		model := *a.LLM
		if model.Provider == current.LLM.Provider && deref(model.BaseURL) == deref(current.LLM.BaseURL) {
			model.APIKey = current.LLM.APIKey
		}
		a.LLM = &model
	}
	return a
}

// conflict fails when the agent of name, in live mode, would trade on MT5 the
// positions of book that another agent running in live mode trades (see
// agentapi.MT5Book.Overlaps): two processes of the same magic number on a
// pair close, move and net the positions of each other. The caller holds mu.
func (m *Manager) conflict(name string, live bool, book agentapi.MT5Book) error {
	if !live {
		return nil
	}
	for _, other := range slices.Sorted(maps.Keys(m.launched)) {
		l := m.launched[other]
		if other == name || !l.live || !l.book.Overlaps(book) || !m.supervisor.Running(other) {
			continue
		}
		return samePositions(fmt.Sprintf("O agente %s, em execução no modo live, opera %s com o magic %d "+
			"(comentário %q) na bridge %s: dê a um dos dois um comentário ou um magic próprio.", other,
			strings.Join(book.Common(l.book), ";"), book.Magic, book.Comment, book.Bridge))
	}
	return nil
}

// admit takes the book of the agent a, of the settings it read (see
// handleConfig), as the one it trades until it starts again, unless another
// agent running trades its positions (see conflict), and a as the settings
// of its launch: their secrets the ones it read (see launchedAs).
func (m *Manager) admit(a store.Agent, book agentapi.MT5Book) error {
	m.starting.Lock()
	defer m.starting.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	live := a.Mode == "live"
	if err := m.conflict(a.Name, live, book); err != nil {
		m.launchErr[a.Name] = err.Error()
		return err
	}
	if l, ok := m.launched[a.Name]; ok {
		l.live, l.book = live, book
		if l.agent.ID == a.ID {
			l.agent, l.hash = a, launchHash(a) // a secret changed and read isn't pending anymore
		}
		m.launched[a.Name] = l
	}
	delete(m.launchErr, a.Name)
	return nil
}

// noteLaunch keeps err, the error of a launch of the agent of name, nil for
// none, as its state, see AgentState.LaunchError. It reports whether it is a
// new one, to log.
func (m *Manager) noteLaunch(name string, err error) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		delete(m.launchErr, name)
		return false
	}
	known := m.launchErr[name] == err.Error()
	m.launchErr[name] = err.Error()
	return !known
}

// stop stops the agent of name, and waits for it. The caller holds the lock
// of the agent.
func (m *Manager) stop(name string) {
	m.supervisor.Stop(name)
	m.mu.Lock()
	delete(m.launched, name)
	m.mu.Unlock()
}

// Restart stops the agent of name and launches it again when it is enabled,
// with its settings of the database read once it stopped: the way a change of
// the settings it was launched with applies (see launchedAs). It waits for the
// stop holding the agent alone: the Reconciles meanwhile leave it to the one
// after the restart. ErrStopped once the controller stops.
func (m *Manager) Restart(ctx context.Context, name string) error {
	if _, err := m.store.AgentByName(ctx, name); err != nil {
		return err
	}
	lock := m.locks.of(name)
	lock.Lock()
	if m.stopping() {
		lock.Unlock()
		return ErrStopped
	}
	m.stop(name)

	m.reconciling.Lock()
	err := ErrStopped // the controller stopped meanwhile: nothing read, nothing launched
	if !m.stopping() {
		err = m.relaunch(ctx, name)
	}
	lock.Unlock() // before a Reconcile waiting, which applies the agent
	m.reconciling.Unlock()
	m.Trigger() // the dashboard follows the address it listens at now
	return err
}

// relaunch launches the agent of name, stopped, when enabled, with its
// settings read now. The caller holds the lock of the agent and reconciling.
func (m *Manager) relaunch(ctx context.Context, name string) error {
	a, err := m.store.AgentByName(ctx, name)
	if err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.exited, name)
	m.mu.Unlock()
	if !a.Enabled || !a.Launched {
		m.noteLaunch(name, nil)
		return nil
	}
	err = m.start(a)
	if !errors.Is(err, ErrStopped) && m.noteLaunch(name, err) {
		log.Errorf("controller: agent %s: %v", name, err)
	}
	return err
}

// State returns the state of the agent of name.
func (m *Manager) State(name string) AgentState {
	state := AgentState{ProcessState: m.supervisor.State(name)}
	m.mu.Lock()
	defer m.mu.Unlock()
	state.LaunchError = m.launchErr[name]
	_, state.Exited = m.exited[name]
	if l, ok := m.launched[name]; ok && state.Running {
		if a, found := m.byName[name]; found {
			state.RestartPending = l.hash != launchHash(a)
		}
	}
	return state
}

// runtimeOf returns the runtime of the agent a: its channels, the news, and
// of an LLM agent the switch of its reanalysis and its prompt. false when its
// prompt or the news can't be read: the agent keeps the runtime it has rather
// than switching to its default prompt.
func runtimeOf(ctx context.Context, s *store.Store, a store.Agent) (agentapi.Runtime, bool) {
	doc := agentapi.Runtime{TradePanel: a.TradePanel, TelegramControl: a.TelegramControl,
		Reanalysis: a.ReanalysisAllowed()}
	entries, err := entriesOf(ctx, s, a)
	if err != nil {
		log.Warnf("controller: the entries of the agent %s: %v", a.Name, err)
		return doc, false // it keeps the entries it has, blocked by a daily guard if they are
	}
	doc.Entries = entries
	protection, err := protectionOf(ctx, s, a)
	if err != nil {
		log.Warnf("controller: the protection of the positions of the agent %s: %v", a.Name, err)
		return doc, false
	}
	doc.Protection = protection
	if a.LLM != nil && a.LLM.PromptID != nil {
		prompt, err := s.Prompt(ctx, *a.LLM.PromptID)
		if err != nil {
			log.Warnf("controller: the prompt of the agent %s: %v", a.Name, err)
			return doc, false
		}
		doc.Prompt = &agentapi.PromptRef{ID: prompt.ID, Name: prompt.Name, Version: prompt.Version}
	}
	news, err := newsOf(ctx, s)
	if err != nil {
		log.Warnf("controller: the news of the agent %s: %v", a.Name, err)
		return doc, false // it keeps the news it has
	}
	doc.News = news
	return doc, true
}

// newsOf returns the news of all the agents for an agent, LLM or not: on or
// off, and only while on the MCP server of the calendar, its token and the
// window of the news block; off before the seeds are applied.
func newsOf(ctx context.Context, s *store.Store) (*agentapi.News, error) {
	settings, found, err := s.News(ctx)
	if err != nil {
		return nil, err
	}
	news := &agentapi.News{Enabled: found && settings.Enabled}
	if !news.Enabled {
		return news, nil
	}
	if news.Token, err = s.NewsToken(ctx); err != nil {
		return nil, err
	}
	rules, _, err := s.NewsRules(ctx)
	if err != nil {
		return nil, err
	}
	news.URL, news.BlockBefore, news.BlockAfter = deref(rules.MCPURL), rules.BlockBefore, rules.BlockAfter
	news.BlockImportance = rules.BlockImportance
	return news, nil
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
		APIKey                 []byte
	}{Kind: a.Kind, Mode: a.Mode, APIAddress: a.APIAddress, Restart: a.Restart, Params: a.Params}
	if a.Account != nil {
		account := *a.Account
		settings.BridgeToken = account.BridgeToken
		account.CreatedAt, account.UpdatedAt = time.Time{}, time.Time{}
		settings.Account = &account
	}
	if a.LLM != nil {
		model := *a.LLM
		settings.APIKey = model.APIKey
		model.PromptID, model.PromptStatus = nil, nil
		model.CreatedAt, model.UpdatedAt = time.Time{}, time.Time{}
		settings.LLM = &model
	}
	data, _ := json.Marshal(settings)
	return sha256.Sum256(data)
}

// entriesOf returns the entries of the agent a, as its supervision tells them.
// protectionOf returns the protection of the positions of the agent a: its
// settings when it runs or may be armed (see ui.ProtectionSettings.Active),
// the arms of the XTrader AI in force on its positions, and the stops the
// daily loss limits ask of them (see daily_stops.go).
func protectionOf(ctx context.Context, s *store.Store, a store.Agent) (agentapi.Protection, error) {
	var protection agentapi.Protection
	if settings := a.ProtectionOf(); settings.Active() {
		protection.ProtectionSettings = &settings
	}
	arms, err := s.ProtectionArmsInForce(ctx, a.ID)
	if err != nil {
		return protection, err
	}
	for _, arm := range arms {
		protection.ProtectionArms = append(protection.ProtectionArms, arm.Arm())
	}
	stops, err := s.DailyStopsInForce(ctx, a.ID)
	if err != nil {
		return protection, err
	}
	for _, stop := range stops {
		protection.DailyStops = append(protection.DailyStops, stop.Request())
	}
	return protection, nil
}

// entriesOf returns the entries of the agent a, see combinedEntries.
func entriesOf(ctx context.Context, s *store.Store, a store.Agent) (agentapi.Entries, error) {
	return combinedEntries(ctx, s, a, a.SupervisionOf())
}

// combinedEntries returns the entries of the agent a: blocked by its
// supervision (supervision), by a daily guard, its own or the one of all the
// agents (see store.DailyEntries), or by the trading hours, outside their
// windows or in a window for other agents (see store.TradingHoursEntries), and
// the only assets it may open positions on: the ones of the window open, if
// any, and the ones the rules of the agents per asset allow it (see
// assetEntries); the reasons of the daily guard and of the hours first; their
// revision the sum of the four, which only grow, so that allowing the entries
// in its supervision (eg: a user, or the agent enabled again) never lifts a
// daily guard, the hours nor a rule of the assets.
func combinedEntries(ctx context.Context, s *store.Store, a store.Agent,
	supervision store.AgentSupervision) (agentapi.Entries, error) {
	daily, dailyReason, dailyRevision, err := s.DailyEntries(ctx, a.ID)
	if err != nil {
		return agentapi.Entries{}, err
	}
	hours, hoursReason, pairs, pairsReason, hoursRevision, err := s.TradingHoursEntries(ctx, a.ID)
	if err != nil {
		return agentapi.Entries{}, err
	}
	rules, _, err := s.AssetAgents(ctx)
	if err != nil {
		return agentapi.Entries{}, err
	}
	entries := agentapi.Entries{EntriesBlocked: supervision.EntriesBlocked || daily || hours,
		EntriesRevision: supervision.EntriesRevision + dailyRevision + hoursRevision,
		EntriesReason:   supervision.EntriesReason, EntriesPairs: pairs, PairsReason: pairsReason,
		Exposure: exposureOn(ctx, s)}
	if !daily && !hours {
		return assetEntries(entries, rules, a), nil
	}
	var reasons []string
	if daily {
		reasons = append(reasons, dailyReason)
	}
	if hours {
		reasons = append(reasons, hoursReason)
	}
	if supervision.EntriesBlocked && supervision.EntriesReason != "" {
		reasons = append(reasons, supervision.EntriesReason)
	}
	entries.EntriesReason = strings.Join(reasons, "; ")
	return assetEntries(entries, rules, a), nil
}
