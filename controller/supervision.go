package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

var (
	// supervisionEvery is how often the supervisor looks whether an analysis
	// is due, and at the agents it stops once flat (see watchStops).
	supervisionEvery = 15 * time.Second
	// supervisorCallTimeout bounds a call of the supervisor to the API of an
	// agent, eg: the close of its positions, a deal at a time.
	supervisorCallTimeout = 2 * time.Minute
	// maxToolRounds bounds the answers of the model a request takes, each one
	// with the results of the calls of tools of the one before.
	maxToolRounds = 6
)

// errAnalyzing is the error of an analysis asked while another one runs.
var errAnalyzing = errors.New("an analysis of the supervisor is running already")

// errNotConfigured is the error of an analysis or a conversation without the
// language model of the supervisor.
var errNotConfigured = errors.New("the supervisor has no language model: set its provider, model and key in " +
	"its settings")

// Supervision is the supervisor of the agents: a language model that analyzes
// every agent each interval of its settings, while enabled (see Run), and
// answers the users of the dashboard (see the chat), with what it read of
// them (see view), never a secret. What it does goes through tools validated
// here, never through its text: alone, within the autonomy of its settings,
// it blocks the entries of an agent, stops it once flat or cancels its
// pending entries; the rest, closing positions among them, it proposes, and a
// user confirms. Every action is recorded with what it rests on, executed
// against the state read then (see execute), and checked on the accounts
// before it is told done.
type Supervision struct {
	store     *store.Store
	manager   *Manager
	dashboard *Dashboard
	positions *openPositions
	closed    *closedProfits
	client    *http.Client // of the calls to the agents, see callAgent
	bridges   *bridgeTrader
	daily     *dailyLimiter // the daily limits, see dailyLimiter

	// newChat creates the chat of the language model of settings, see
	// llm.NewChat, and stateOf tells the state of an agent, see Manager.State:
	// the tests answer in their place
	newChat func(settings store.SupervisorSettings, key string) (llm.Chat, error)
	stateOf func(name string) AgentState
	now     func() time.Time
	wake    chan struct{} // the settings changed, see Trigger

	analyzing atomic.Bool // an analysis at a time
	bulk      sync.Mutex  // the actions on several agents, one at a time, before the locks of the agents
	locksMu   sync.Mutex
	locks     map[string]*sync.Mutex // the actions on each agent, one at a time
	closing   map[string]bool        // the closes asked from the dashboard running, by agent, "" for all of them

	mu       sync.Mutex
	next     time.Time            // of the next analysis, zero while disabled
	interval time.Duration        // of the analyses, as last read
	failures int                  // the analyses failed in a row
	flat     map[int64]time.Time  // since when each agent stopped once flat was seen flat, see watchStops
	shared   *view                // the last view of the conversations, see chatView
	stopping map[int64]stopTarget // the agents stopped once flat, the action of each
}

// stopTarget is an agent the supervisor stops once flat, by the action of id.
type stopTarget struct {
	action int64
	name   string
}

// ServeSupervision serves the supervisor of the agents of s on the dashboard,
// run by manager, after ServeSettings, and returns it: its Run applies its
// settings.
func (d *Dashboard) ServeSupervision(s *store.Store, manager *Manager) *Supervision {
	sup := &Supervision{store: s, manager: manager, dashboard: d, positions: d.settings.positions,
		closed: d.settings.closed, client: &http.Client{Timeout: supervisorCallTimeout},
		bridges: newBridgeTrader(), newChat: chatOf, stateOf: manager.State, now: time.Now, wake: make(chan struct{}, 1),
		locks: make(map[string]*sync.Mutex), flat: make(map[int64]time.Time), stopping: make(map[int64]stopTarget)}
	sup.daily = newDailyLimiter(sup)
	d.supervision = sup
	return sup
}

// chatOf creates the chat of the language model of settings with key.
func chatOf(settings store.SupervisorSettings, key string) (llm.Chat, error) {
	if settings.Provider == nil || settings.Model == nil || key == "" {
		return nil, errNotConfigured
	}
	var options []llm.ProviderOption
	if settings.BaseURL != nil {
		options = append(options, llm.WithBaseURL(*settings.BaseURL))
	}
	if settings.ReasoningEffort != nil {
		options = append(options, llm.WithReasoningEffort(*settings.ReasoningEffort))
	}
	if settings.MaxTokens != nil {
		options = append(options, llm.WithMaxTokens(*settings.MaxTokens))
	}
	return llm.NewChat(*settings.Provider, *settings.Model, key, options...)
}

// Trigger makes the supervisor read its settings again now, eg: its interval
// changed.
func (s *Supervision) Trigger() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run applies the settings of the supervisor until ctx is done: an analysis
// each interval while enabled, the first one an interval after it is enabled
// or the controller starts; the agents it stops once flat watched; the
// proposals expired. The analyses and the actions a controller before left in
// their middle end first: failed, unknown.
func (s *Supervision) Run(ctx context.Context) {
	if n, err := s.store.FailRunningSupervisorRuns(ctx, "the controller stopped during the analysis"); err != nil {
		log.Errorf("supervisor: %v", err)
	} else if n > 0 {
		log.Warnf("supervisor: %d analyses left running by the controller before, failed", n)
	}
	if n, err := s.store.UnknownExecutingSupervisorActions(ctx,
		"the controller stopped during the action: check the accounts, it is never executed again"); err != nil {
		log.Errorf("supervisor: %v", err)
	} else if n > 0 {
		log.Warnf("supervisor: %d actions left executing by the controller before, their outcome unknown", n)
	}
	go s.daily.Run(ctx) // whatever the settings of the supervisor
	ticker := time.NewTicker(supervisionEvery)
	defer ticker.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// tick starts an analysis when one is due, watches the agents stopped once
// flat and expires the proposals past their time.
func (s *Supervision) tick(ctx context.Context) {
	settings, found, err := s.store.Supervisor(ctx)
	if err != nil {
		log.Errorf("supervisor: its settings: %v", err)
		return
	}
	now := s.now()
	interval := time.Duration(settings.IntervalSeconds) * time.Second
	s.mu.Lock()
	switch {
	case !found || !settings.Enabled:
		s.next = time.Time{}
	case s.next.IsZero() || interval != s.interval:
		s.next = now.Add(interval) // enabled, or the interval changed: an interval from now
	}
	s.interval = interval
	due := !s.next.IsZero() && !now.Before(s.next)
	if due {
		s.next = now.Add(interval)
	}
	s.mu.Unlock()
	if due {
		go func() {
			if _, err := s.Analyze(ctx, store.RunSchedule, ""); err != nil && !errors.Is(err, errAnalyzing) {
				log.Warnf("supervisor: the analysis: %v", err)
			}
		}()
	}
	s.watchStops(ctx)
	s.syncArms(ctx)
	if _, err := s.store.ExpireSupervisorActions(ctx); err != nil {
		log.Warnf("supervisor: expiring the proposals: %v", err)
	}
}

// state returns whether an analysis runs, when the next one is due, zero while
// disabled, and the ones failed in a row.
func (s *Supervision) state() (analyzing bool, next time.Time, failures int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.analyzing.Load(), s.next, s.failures
}

// lockAgent takes the lock of the actions on the agent of name, one at a time,
// and returns its unlock.
func (s *Supervision) lockAgent(name string) func() {
	s.locksMu.Lock()
	lock, ok := s.locks[name]
	if !ok {
		lock = new(sync.Mutex)
		s.locks[name] = lock
	}
	s.locksMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// settingsAndChat returns the settings of the supervisor and the chat of its
// language model, errNotConfigured without one.
func (s *Supervision) settingsAndChat(ctx context.Context) (store.SupervisorSettings, llm.Chat, error) {
	settings, found, err := s.store.Supervisor(ctx)
	if err != nil {
		return store.SupervisorSettings{}, nil, err
	}
	if !found {
		return store.SupervisorSettings{}, nil, errNotConfigured
	}
	key, err := s.store.SupervisorKey(ctx)
	if err != nil {
		return store.SupervisorSettings{}, nil, err
	}
	chat, err := s.newChat(settings, key)
	if err != nil {
		return store.SupervisorSettings{}, nil, err
	}
	return settings, chat, nil
}

// Analyze runs an analysis of the agents, the only one: another one asked
// meanwhile is recorded skipped (errAnalyzing). The model answers with its
// findings, and with the actions it asks for, executed within its autonomy,
// proposed beyond it (see analysisTools). A model unavailable fails the
// analysis, the agents untouched.
func (s *Supervision) Analyze(ctx context.Context, origin, by string) (store.SupervisorRun, error) {
	if !s.analyzing.CompareAndSwap(false, true) {
		run, err := s.store.StartSupervisorRun(ctx, origin, store.RunSkipped, by, "")
		if err == nil {
			run.Error = errAnalyzing.Error()
			_ = s.store.FinishSupervisorRun(ctx, store.SupervisorRun{ID: run.ID, Status: store.RunSkipped,
				Error: errAnalyzing.Error()})
		}
		return run, errAnalyzing
	}
	defer s.analyzing.Store(false)

	settings, chat, err := s.settingsAndChat(ctx)
	if err != nil {
		return store.SupervisorRun{}, err
	}
	run, err := s.store.StartSupervisorRun(ctx, origin, store.RunRunning, by, chat.String())
	if err != nil {
		return store.SupervisorRun{}, err
	}
	log.Infof("supervisor: analysis %d (%s) with %s", run.ID, origin, chat)
	analysis := &analysis{s: s, settings: settings, run: run, by: by}
	err = analysis.ask(ctx, chat)

	run.Model = chat.String()
	run.Summary, run.Snapshot, run.Usage = analysis.summary, analysis.snapshot, analysis.usageJSON()
	run.Findings, _ = json.Marshal(analysis.findings)
	run.Status = store.RunOK
	if err != nil {
		run.Status, run.Error = store.RunFailed, err.Error()
	}
	if finishErr := s.store.FinishSupervisorRun(context.WithoutCancel(ctx), run); finishErr != nil {
		log.Errorf("supervisor: recording the analysis %d: %v", run.ID, finishErr)
	}
	s.mu.Lock()
	if err != nil {
		s.failures++
	} else {
		s.failures = 0
	}
	s.mu.Unlock()
	if err != nil {
		log.Warnf("supervisor: analysis %d failed, the agents untouched: %v", run.ID, err)
		return run, err
	}
	log.Infof("supervisor: analysis %d: %s", run.ID, analysis.summary)
	return run, nil
}

// analysis is an analysis of the supervisor running: what the model found and
// asked for.
type analysis struct {
	s        *Supervision
	settings store.SupervisorSettings
	run      store.SupervisorRun
	by       string
	view     *view

	summary  string
	findings []ui.SupervisorFinding
	snapshot json.RawMessage
	usage    llm.Usage
	actions  int // the ones asked alone, see Autonomy.MaxActionsPerRun
	reported bool
}

// usageJSON returns the tokens of the answers of the analysis.
func (a *analysis) usageJSON() json.RawMessage {
	data, _ := json.Marshal(map[string]int{"input": a.usage.Input, "cached": a.usage.Cached,
		"output": a.usage.Output, "reasoning": a.usage.Reasoning})
	return data
}

// ask reads the agents and asks the model, within the timeout of the
// settings, handing it the results of its calls of tools.
func (a *analysis) ask(ctx context.Context, chat llm.Chat) error {
	view, err := a.s.read(ctx)
	if err != nil {
		return fmt.Errorf("reading the agents: %w", err)
	}
	a.view = view
	snapshot, err := json.Marshal(view.snapshot)
	if err != nil {
		return err
	}
	a.snapshot = snapshot

	ctx, cancel := context.WithTimeout(ctx, time.Duration(a.settings.TimeoutSeconds)*time.Second)
	defer cancel()
	messages := []llm.ChatMessage{{Role: llm.ChatUser, Content: "Retrato dos agentes (JSON):\n" + string(snapshot) +
		"\n\nAnalise os agentes e responda chamando a ferramenta report uma vez."}}
	for round := 0; round < maxToolRounds; round++ {
		answer, err := chat.Complete(ctx, analysisSystem(a.settings), messages, analysisTools(a.settings.Autonomy))
		a.usage.Input += answer.Usage.Input
		a.usage.Cached += answer.Usage.Cached
		a.usage.Output += answer.Usage.Output
		a.usage.Reasoning += answer.Usage.Reasoning
		if err != nil {
			return fmt.Errorf("the model: %w", err)
		}
		if len(answer.ToolCalls) == 0 {
			if !a.reported {
				a.summary = strings.TrimSpace(answer.Content)
			}
			if a.summary == "" && answer.Truncated {
				return errors.New(maxTokensMessage(a.settings, answer.Usage))
			}
			if a.summary == "" {
				return errors.New("the model answered without its report")
			}
			return nil
		}
		messages = append(messages, llm.ChatMessage{Role: llm.ChatAssistant, Content: answer.Content,
			ToolCalls: answer.ToolCalls})
		for _, call := range answer.ToolCalls {
			messages = append(messages, llm.ChatMessage{Role: llm.ChatTool, ToolCallID: call.ID,
				Content: a.call(ctx, call)})
		}
		if a.reported && !hasAction(answer.ToolCalls) {
			return nil // reported, nothing more asked
		}
	}
	if !a.reported {
		return fmt.Errorf("the model didn't report in %d answers", maxToolRounds)
	}
	return nil
}

// maxTokensMessage tells the user an answer of the model was cut at its max
// tokens before any text or call, usage its tokens: a model that reasons
// spends them thinking. The max tokens of settings, the default of the client
// without one.
func maxTokensMessage(settings store.SupervisorSettings, usage llm.Usage) string {
	limit := llm.DefaultMaxTokens
	if settings.MaxTokens != nil {
		limit = *settings.MaxTokens
	}
	spent := fmt.Sprintf("%d tokens", usage.Output)
	if usage.Reasoning > 0 {
		spent = fmt.Sprintf("%d tokens, %d deles raciocinando", usage.Output, usage.Reasoning)
	}
	return fmt.Sprintf("O modelo gastou todo o limite da resposta (%s; o limite é %d) antes de responder. "+
		"Aumente o Máximo de tokens em Configurações → XTrader AI ou reduza o raciocínio.", spent, limit)
}

// hasAction reports whether calls ask for an action, not only the report.
func hasAction(calls []llm.ToolCall) bool {
	for _, call := range calls {
		if call.Name != "report" {
			return true
		}
	}
	return false
}

// call runs a call of a tool of the analysis and returns its result, JSON, to
// the model.
func (a *analysis) call(ctx context.Context, call llm.ToolCall) string {
	switch call.Name {
	case "report":
		var report struct {
			Summary  string                 `json:"summary"`
			Findings []ui.SupervisorFinding `json:"findings"`
		}
		if err := json.Unmarshal(call.Arguments, &report); err != nil || strings.TrimSpace(report.Summary) == "" {
			return toolError("report: a summary and the findings are required")
		}
		a.summary, a.reported = strings.TrimSpace(report.Summary), true
		for _, finding := range report.Findings {
			if finding.Category != store.CategoryOperational && finding.Category != store.CategoryRisk &&
				finding.Category != store.CategoryPerformance {
				finding.Category = store.CategoryOperational
			}
			if _, known := a.view.agents[finding.Agent]; finding.Agent != "" && !known {
				finding.Agent = ""
			}
			a.findings = append(a.findings, finding)
		}
		return toolResult(map[string]any{"recorded": true})
	case store.ActionBlockEntries, store.ActionStopAgent, store.ActionCancelPending:
		return a.act(ctx, call)
	case "protect_position":
		if a.actions >= a.settings.Autonomy.MaxActionsPerRun {
			return toolError("the limit of actions alone in an analysis is reached: report the finding")
		}
		result, arm := a.s.protectPosition(ctx, a.view, call.Arguments, &a.run.ID, nil, "supervisor")
		if arm != nil {
			a.actions++
		}
		return result
	}
	return toolError("unknown tool " + call.Name)
}

// toolResult returns the JSON of a result of a tool.
func toolResult(result any) string {
	data, err := json.Marshal(result)
	if err != nil {
		return toolError(err.Error())
	}
	return string(data)
}

// toolError returns the JSON of a call of a tool refused.
func toolError(message string) string {
	data, _ := json.Marshal(map[string]string{"error": message})
	return string(data)
}

// analysisSystem returns the instructions of the model in an analysis: the
// rules of the code, then the criteria of the settings.
func analysisSystem(settings store.SupervisorSettings) string {
	autonomy := settings.Autonomy
	yes := func(allowed bool) string {
		if allowed {
			return "sim"
		}
		return "não (vira proposta para o usuário)"
	}
	return supervisorRules + fmt.Sprintf(`

Nesta análise periódica:
- Chame report uma única vez, com um resumo curto e os achados (um por problema, com o agente, a categoria, a severidade, a evidência com números, moeda e janela, e a recomendação).
- Ações que você executa sozinho, no máximo %d por análise:
  - block_entries (bloquear novas entradas): %s;
  - stop_agent (parar o agente assim que ele estiver sem posição e sem ordem pendente; exige que a análise anterior também tenha pedido a parada do mesmo agente): %s;
  - cancel_pending (cancelar ordens pendentes de entrada): %s.
- protect_position (armar o breakeven ou o trailing stop do agente numa posição aberta): só nos agentes que autorizam o recurso (protection.breakeven.supervisor, protection.trailing.supervisor); conta no limite de ações. Use quando os números de protections indicarem risco de devolver o lucro (avanço a favor em ATR, preço recuando do melhor nível, stop longe do preço); trate a reversão como possibilidade, nunca como certeza, e diga o risco e a proteção pedida. O gatilho configurado no agente vale: antes dele, o recurso só fica armado.
- Nunca feche posições: isso só o usuário pede, pela conversa.
- Não aja sobre agente com dados incompletos (lacunas em gaps, conta não lida, API sem resposta): registre o achado.`,
		autonomy.MaxActionsPerRun, yes(autonomy.BlockEntries), yes(autonomy.StopWhenFlat), yes(autonomy.CancelPending)) +
		criteria(settings)
}

// criteria returns the criteria of the settings, as the instructions end.
func criteria(settings store.SupervisorSettings) string {
	if strings.TrimSpace(settings.Instructions) == "" {
		return ""
	}
	return "\n\nCritérios definidos pelo usuário:\n" + settings.Instructions
}

// supervisorRules are the rules of the supervisor the code relies on, which
// the criteria of the settings follow and can't change.
const supervisorRules = `Você é o XTrader AI, o supervisor dos agentes de trading do xtrader. Recebe um retrato em JSON de todos os agentes: o estado do processo, a estratégia, os ativos, as posições abertas (com o stop loss e o take profit na corretora, 0 quando não há), as ordens pendentes, os resultados realizados por janela (today, last_24h e period, desde o início do período de resultados do agente), o resultado aberto, os limites de risco configurados em cada agente, as decisões recentes dos agentes LLM e as falhas operacionais.

Regras:
- Use somente os dados do retrato. Nunca invente números, limites ou fatos. Se um dado falta (veja gaps), diga que falta e não conclua nada a partir dele, como dizer que um agente está zerado.
- Todo valor em dinheiro vem com a moeda da conta. Nunca some nem compare valores de moedas diferentes. Cite a janela e o horário dos dados (generated_at).
- Os volumes estão em lotes. As contas aparecem por apelido (conta 1, conta 2...). Vários agentes podem operar a mesma conta: a exposição por conta e par já está somada em exposure.
- Os limites diários da aplicação (daily_guard: de todos os agentes somados e de cada agente, sobre o resultado do dia, realizado + aberto) são aplicados pelo sistema, que zera as posições, cancela as pendentes de entrada e bloqueia as entradas sozinho até 00:00: informe o estado, não repita a ação. Fora deles, compare com os limites configurados no próprio agente (em risk e daily_limits) e com o saldo da conta.
- Classifique cada problema como operational (operacional), risk (risco) ou performance (desempenho). Julgue desempenho só com amostra suficiente de posições fechadas.
- Bloquear novas entradas não fecha posições nem cancela pendentes: o agente continua gerenciando o que tem. Parar um agente nunca liquida posições.
- Escreva em português do Brasil, direto e curto.`
