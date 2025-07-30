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
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/tools/log"
)

// flushTimeout bounds the sending of the records left as the agent stops: the
// ones not sent are sent by its next start.
const flushTimeout = 10 * time.Second

// Agent is an agent launched by the controller: its settings, and what it
// keeps through the controller once Open.
type Agent struct {
	Config agentapi.Config

	client  *Client
	outbox  *Outbox
	prompts *Prompts

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
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
	return connect(ctx, client, os.Getenv(agentapi.EnvAgent))
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

// Args returns the flags of the settings of the agent, the ones set only, as
// its command line gave them: its params, then its columns, which the
// controller keeps apart. Its secrets are never among them, see Config.
func (a *Agent) Args() []string {
	cfg := a.Config
	var args []string
	add := func(name, value string) {
		args = append(args, "-"+name+"="+value)
	}
	params := func(values map[string]string) {
		names := make([]string, 0, len(values))
		for name := range values {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			add(name, values[name])
		}
	}

	params(cfg.Params)
	if l := cfg.LLM; l != nil {
		params(l.Params)
	}
	add("mode", cfg.Mode)
	if cfg.APIAddress != "" {
		add("api", cfg.APIAddress)
	}
	if cfg.TradePanel {
		add("trade-panel", "true")
	}
	if cfg.TelegramControl {
		add("telegram-control", "true")
	}
	if acc := cfg.Account; acc != nil {
		if acc.BridgeURL != "" {
			add("mt5-bridge", acc.BridgeURL)
		}
		if acc.Magic != nil {
			add("mt5-magic", strconv.FormatInt(*acc.Magic, 10))
		}
		if acc.Comment != nil {
			add("mt5-comment", *acc.Comment)
		}
		if acc.RealAllowed {
			add("mt5-live", "true")
		}
	}
	if l := cfg.LLM; l != nil {
		add("llm-provider", l.Provider)
		add("llm-model", l.Model)
		if l.BaseURL != "" {
			add("llm-base-url", l.BaseURL)
		}
		if l.ReasoningEffort != "" {
			add("llm-reasoning", l.ReasoningEffort)
		}
		if l.MaxTokens > 0 {
			add("llm-max-tokens", strconv.Itoa(l.MaxTokens))
		}
		if l.CalendarURL != "" {
			add("calendar-mcp", l.CalendarURL)
		}
	}
	return args
}

// Open opens the outbox of the agent in dir, eg: data/outbox, a file named
// after it and its ID, eg: llm-3.db: a new agent taking the name of one
// deleted never sends the records the other left. Before its records are kept.
func (a *Agent) Open(dir string) error {
	outbox, err := OpenOutbox(filepath.Join(dir, fmt.Sprintf("%s-%d.db", a.Config.Name, a.Config.ID)), a.client)
	if err != nil {
		return err
	}
	a.outbox = outbox
	if a.Config.LLM != nil {
		a.prompts = &Prompts{outbox: outbox, current: promptOf(a.Config.Prompt)}
	}
	return nil
}

// Orders returns the storage of the orders of the agent, see NewOrders.
func (a *Agent) Orders(ctx context.Context) (*Orders, error) {
	return NewOrders(ctx, a.client, a.outbox)
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
// agent, ErrUnauthorized then. The bot doesn't stop with its context: Serve
// returns, and Close sends the records left as the agent exits.
func (a *Agent) Serve(ctx context.Context, apply func(agentapi.Runtime), run func(context.Context) error) error {
	ctx = a.Run(ctx, apply)
	ended := make(chan error, 1)
	go func() { ended <- run(ctx) }()
	select {
	case err := <-ended:
		return err
	case <-ctx.Done():
		log.Infof("%s: stopping", a.Config.Name)
		if cause := context.Cause(ctx); errors.Is(cause, ErrUnauthorized) {
			return cause
		}
		return nil
	}
}

// watch applies the runtime of the agent as it changes until ctx is done, see
// Run: it returns ErrUnauthorized when the controller refuses the agent.
func (a *Agent) watch(ctx context.Context, apply func(agentapi.Runtime)) error {
	var version int64
	delay := retryDelay
	var warned time.Time
	for {
		runtime, changed, err := a.client.Runtime(ctx, version, runtimeWait)
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

// Close stops sending the records and watching the runtime, sends the records
// left, flushTimeout at most, and closes the outbox.
func (a *Agent) Close() error {
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	if a.outbox == nil {
		return nil
	}
	ctx, stop := context.WithTimeout(context.Background(), flushTimeout)
	defer stop()
	err := a.outbox.Flush(ctx)
	if err != nil {
		log.Warnf("%s: %v: sent by the next start", a.Config.Name, err)
	}
	return a.outbox.Close()
}
