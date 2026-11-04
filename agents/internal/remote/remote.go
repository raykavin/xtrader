// Package remote runs an agent launched by the controller (cmd/controller)
// with the settings and the records of the controller, which alone reaches
// the database, through its internal API (see agentapi): the flags of its
// settings, its secrets, the switches of its control panel and the prompt of an
// LLM agent as they change while it runs, and the storage of its orders, of its
// decisions and of its journal.
//
// Nothing of the flow of the agent waits for the controller: its records are
// written to an outbox of its own, a file sent to the controller as it
// answers, and read back from memory. The controller refusing its token, eg:
// the agent left running by a controller before, the agent stops.
package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

// flushTimeout bounds the sending of the records left as the agent stops: the
// ones not sent are sent by its next start.
const flushTimeout = 10 * time.Second

// Agent is an agent launched by the controller: its settings, the key of its
// API for this start (see ui.WithAPIKey), and what it keeps through the
// controller once Open. With QuarantineOutbox, eg: the flag
// -outbox-quarantine, Open moves an outbox that can't be read aside rather
// than failing, see OpenOutbox.
type Agent struct {
	Config           agentapi.Config
	APIKey           string
	QuarantineOutbox bool

	client  *Client
	outbox  *Outbox
	prompts *Prompts

	mu         sync.Mutex
	entries    Entries            // of the bot, see Supervise
	gate       *agentapi.PairGate // the assets of its entries, see Gate
	exposure   atomic.Bool        // the exposure limit on: its entries claimed, see ClaimEntry
	protection Protection         // of the positions of the bot, see Protect
	orders     *Orders
	cancel     context.CancelFunc
	done       chan struct{}
}

// Connect returns the agent launched by the controller, its settings read from
// it, nil when the agent runs on its own: without the token of the controller
// in its environment (agentapi.EnvToken).
func Connect(ctx context.Context) (*Agent, error) {
	token := os.Getenv(agentapi.EnvToken)
	if token == "" {
		return nil, nil
	}
	client, err := NewClient(os.Getenv(agentapi.EnvURL), token)
	if err != nil {
		return nil, err
	}
	a, err := connect(ctx, client, os.Getenv(agentapi.EnvAgent))
	if err != nil {
		return nil, err
	}
	a.APIKey = os.Getenv(agentapi.EnvAPIKey)
	return a, nil
}

// connect reads the settings of the agent name with client.
func connect(ctx context.Context, client *Client, name string) (*Agent, error) {
	cfg, err := client.Config(ctx)
	if err != nil {
		return nil, fmt.Errorf("remote: the settings of the agent: %w", err)
	}
	if name != "" && name != cfg.Name {
		return nil, fmt.Errorf("remote: the controller gave the settings of %s to %s", cfg.Name, name)
	}
	a := &Agent{Config: cfg, client: client}
	log.Infof("[SETUP] %s: the settings of the controller, %s %s", cfg.Name, cfg.Kind, cfg.Mode)
	return a, nil
}

// Args returns the flags of the settings of the agent, see agentapi.Config.Args.
func (a *Agent) Args() []string {
	return a.Config.Args()
}

// Open opens the outbox of the agent in dir, eg: data/outbox, a file named
// after it and its ID, eg: llm-3.db: a new agent taking the name of one
// deleted never sends the records the other left. Before its records are kept.
// A file that can't be read fails with ErrOutboxCorrupt, unless
// QuarantineOutbox: then it is moved aside, to <file>.corrupt-<time>, and the
// agent starts with an empty outbox (the records of a file read but those it
// can't, set apart).
func (a *Agent) Open(dir string) error {
	outbox, err := openOutboxFile(filepath.Join(dir, fmt.Sprintf("%s-%d.db", a.Config.Name, a.Config.ID)),
		a.client, a.QuarantineOutbox)
	if err != nil {
		return err
	}
	a.outbox = outbox
	if a.Config.LLM != nil {
		a.prompts = &Prompts{outbox: outbox, current: promptOf(a.Config.Prompt)}
	}
	return nil
}

// Orders returns the storage of the orders of the agent, see NewOrders, the
// orders it failed to write written again by Close.
func (a *Agent) Orders(ctx context.Context) (*Orders, error) {
	orders, err := NewOrders(ctx, a.client, a.outbox)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.orders = orders
	a.mu.Unlock()
	return orders, nil
}

// Decisions returns the store of the decisions of the LLM agent, see
// NewDecisions.
func (a *Agent) Decisions(ctx context.Context) (*Decisions, error) {
	return NewDecisions(ctx, a.client, a.outbox)
}

// Journal returns the journal of the LLM agent.
func (a *Agent) Journal() *Journal {
	return NewJournal(a.client, a.outbox)
}

// Prompts returns the source of the prompts of the LLM agent, nil for an
// agent of another kind.
func (a *Agent) Prompts() *Prompts {
	return a.prompts
}

// Prompt returns the prompt selected for the LLM agent, its default one when
// none is.
func (a *Agent) Prompt() llm.Prompt {
	if a.prompts == nil {
		return llm.DefaultPrompt()
	}
	return a.prompts.Prompt()
}

// Run sends the records of the outbox to the controller, and applies the
// runtime of the agent as it changes: its prompt, kept by Prompts, and its
// switches, handed to apply with the rest, once at first. It returns the
// context of the agent: done with ctx, or when the controller refuses the
// agent, which must then stop, ErrUnauthorized its cause. After Open.
func (a *Agent) Run(ctx context.Context, apply func(agentapi.Runtime)) context.Context {
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	a.mu.Lock()
	a.cancel, a.done = func() { cancel(context.Canceled) }, done
	a.mu.Unlock()

	refused := func(err error) {
		if errors.Is(err, ErrUnauthorized) {
			log.Errorf("%s: %v: stopping", a.Config.Name, err)
			cancel(err)
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		refused(a.outbox.Run(ctx))
	}()
	go func() {
		defer wg.Done()
		refused(a.watch(ctx, apply))
	}()
	go func() {
		wg.Wait()
		close(done)
	}()
	return ctx
}

// Serve runs the agent (see Run) and its bot with run, until the bot ends or
// the context of the agent is done: a signal, or the controller refusing the
// agent, ErrUnauthorized then. Then the bot stops trading before Serve returns:
// run must return once its context is done, the orders it executed recorded
// in the outbox, as xtrader.XTrader.Run does, so that Close sends and closes
// an outbox nothing writes an order to any more.
func (a *Agent) Serve(ctx context.Context, apply func(agentapi.Runtime), run func(context.Context) error) error {
	ctx = a.Run(ctx, apply)
	ended := make(chan error, 1)
	go func() { ended <- run(ctx) }()
	select {
	case err := <-ended:
		return err
	case <-ctx.Done():
	}
	log.Infof("%s: stopping, the bot first", a.Config.Name)
	if err := <-ended; err != nil {
		log.Warnf("%s: the bot, stopping: %v", a.Config.Name, err)
	}
	if cause := context.Cause(ctx); errors.Is(cause, ErrUnauthorized) {
		return cause
	}
	return nil
}

// Entries is the order controller of a bot whose entries the controller
// blocks and allows again, eg: *order.Controller, see Supervise.
type Entries interface {
	BlockEntries(revision int64, reason string) bool
	AllowEntries(revision int64) bool
}

// Supervise has the entries of the bot of controller follow the ones the
// controller tells (see agentapi.Entries): the ones of its settings at once,
// before the bot trades, then the ones of its runtime as they change, before
// its prompt, whose switch may fail: a block never waits for it. Before Run.
func (a *Agent) Supervise(controller Entries) {
	a.mu.Lock()
	a.entries = controller
	a.mu.Unlock()
	applyEntries(a.Config.Name, controller, a.Config.Entries)
	a.exposure.Store(a.Config.Entries.Exposure)
}

// ClaimEntry claims an entry of the bot on pair on side of the controller,
// while the exposure limit of the agents is on (see agentapi.Entries), before
// the bot sends it: whether it may go, and why not. A claim that can't be
// asked refuses the entry, the limit unknown.
func (a *Agent) ClaimEntry(pair string, side model.SideType) (bool, string) {
	if !a.exposure.Load() {
		return true, ""
	}
	answer, err := a.client.ClaimEntry(context.Background(), pair, string(side))
	if err != nil {
		log.Warnf("%s: the exposure of the entry on %s: %v", a.Config.Name, pair, err)
		return false, "Não foi possível conferir a exposição no controller"
	}
	return answer.Granted, answer.Reason
}

// Gate has the assets the strategy of the bot may open positions on follow
// the ones the controller tells (see agentapi.Entries.EntriesPairs): the ones
// of its settings at once, then the ones of its runtime as they change, with
// its entries. Before Run.
func (a *Agent) Gate(gate *agentapi.PairGate) {
	a.mu.Lock()
	a.gate = gate
	a.mu.Unlock()
	applyPairs(gate, a.Config.Entries)
}

// applyPairs applies the assets of entries to gate, an older revision
// ignored.
func applyPairs(gate *agentapi.PairGate, entries agentapi.Entries) {
	if gate.Set(entries.EntriesRevision, entries.EntriesPairs, entries.PairsReason) && len(entries.EntriesPairs) > 0 {
		log.Infof("the entries only on %s (revision %d)", strings.Join(entries.EntriesPairs, ", "),
			entries.EntriesRevision)
	}
}

// Protection is the protection of the positions of the bot as the controller
// sets it, see agentapi.Protection and protect.Manager.
type Protection interface {
	Apply(settings *ui.ProtectionSettings, arms []ui.ProtectionArm)
}

// Protect has the protection of the positions of the bot follow the one the
// controller tells (see agentapi.Protection): the one of its settings at once,
// before the bot trades, then the one of its runtime as it changes, with its
// entries, before its prompt. Before Run.
func (a *Agent) Protect(protection Protection) {
	a.mu.Lock()
	a.protection = protection
	a.mu.Unlock()
	protection.Apply(a.Config.ProtectionSettings, a.Config.ProtectionArms)
}

// applyEntries applies entries to controller, an older revision ignored.
func applyEntries(name string, controller Entries, entries agentapi.Entries) {
	if entries.EntriesBlocked {
		if controller.BlockEntries(entries.EntriesRevision, entries.EntriesReason) {
			log.Warnf("%s: the entries blocked (revision %d): %s", name, entries.EntriesRevision,
				entries.EntriesReason)
		}
		return
	}
	controller.AllowEntries(entries.EntriesRevision)
}

// watch applies the runtime of the agent as it changes until ctx is done, see
// Run: it returns ErrUnauthorized when the controller refuses the agent. Its
// entries apply first (see Supervise), whatever its prompt.
func (a *Agent) watch(ctx context.Context, apply func(agentapi.Runtime)) error {
	var version int64
	delay := retryDelay
	var warned time.Time
	for {
		runtime, changed, err := a.client.Runtime(ctx, version, runtimeWait)
		if err == nil && changed {
			a.mu.Lock()
			entries, gate, protection := a.entries, a.gate, a.protection
			a.mu.Unlock()
			if entries != nil {
				applyEntries(a.Config.Name, entries, runtime.Entries)
			}
			if gate != nil {
				applyPairs(gate, runtime.Entries)
			}
			a.exposure.Store(runtime.Entries.Exposure)
			if protection != nil {
				protection.Apply(runtime.ProtectionSettings, runtime.ProtectionArms)
			}
		}
		if err == nil && changed && a.prompts != nil {
			err = a.prompts.update(ctx, a.client, runtime.Prompt)
		}
		switch {
		case errors.Is(err, ErrUnauthorized):
			return err
		case err != nil && ctx.Err() != nil:
			return nil
		case err != nil:
			if time.Since(warned) >= warnEvery {
				warned = time.Now()
				log.Warnf("remote: the runtime of the agent: %v, again in %s", err, delay)
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil
			}
			delay = min(2*delay, retryMaxDelay)
			continue
		}
		delay = retryDelay
		if changed {
			version = runtime.Version
			if apply != nil {
				apply(runtime)
			}
		}
	}
}

// Close stops sending the records and watching the runtime, writes the orders
// the outbox failed to write a last time, sends the records left,
// flushTimeout at most, and closes the outbox. After Serve, the bot stopped.
func (a *Agent) Close() error {
	a.mu.Lock()
	cancel, done, orders := a.cancel, a.done, a.orders
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	if a.outbox == nil {
		return nil
	}
	if orders != nil {
		if err := orders.Close(); err != nil {
			log.Errorf("%s: %v", a.Config.Name, err)
		}
	}
	ctx, stop := context.WithTimeout(context.Background(), flushTimeout)
	defer stop()
	err := a.outbox.Flush(ctx)
	if err != nil {
		log.Warnf("%s: %v: sent by the next start", a.Config.Name, err)
	}
	return a.outbox.Close()
}
