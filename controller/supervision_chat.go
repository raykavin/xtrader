package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

var (
	// chatViewTTL is how long the view of a conversation answers the next
	// messages before the agents are read again.
	chatViewTTL = 30 * time.Second
	// proposalTTL is how long a proposal of the conversation waits for its
	// confirmation: the data it rests on gets old.
	proposalTTL = 10 * time.Minute
)

// The bounds of the conversations.
const (
	maxMessageLength = 4000 // characters of a message of the user
	chatHistory      = 20   // messages of the conversation sent with a new one
	maxAgentOrders   = 20   // orders of an agent its details tell
)

// errEmptyMessage is the error of a message without text, or too long.
var errEmptyMessage = fmt.Errorf("a message of 1 to %d characters", maxMessageLength)

// chatView returns a view of the agents read chatViewTTL ago at most, read
// again else.
func (s *Supervision) chatView(ctx context.Context) (*view, error) {
	s.mu.Lock()
	shared := s.shared
	s.mu.Unlock()
	if shared != nil && s.now().Sub(shared.at) < chatViewTTL {
		return shared, nil
	}
	v, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.shared = v
	s.mu.Unlock()
	return v, nil
}

// The stages of an answer of the conversation, see chatEvents.
const (
	stageReading  = "reading"  // the agents read for the model
	stageThinking = "thinking" // the model asked, writing its answer
	stageTool     = "tool"     // a tool the model called runs, eg: get_agent
)

// chatEvents is told what an answer of the conversation does as it goes, eg:
// streamed to the browser: the messages kept, the stage it is at, the pieces
// of the text the model writes, the ones of a stage dropped at the next one
// (the text written before a call of a tool isn't the answer). Called from
// the goroutine of the answer, in order.
type chatEvents interface {
	message(m store.SupervisorMessage)
	stage(stage, tool string)
	text(piece string)
}

// noEvents is the chatEvents of an answer nobody follows.
type noEvents struct{}

func (noEvents) message(store.SupervisorMessage) {}
func (noEvents) stage(string, string)            {}
func (noEvents) text(string)                     {}

// checkMessage returns the text of a message of the user trimmed, or
// errEmptyMessage.
func checkMessage(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > maxMessageLength {
		return "", errEmptyMessage
	}
	return text, nil
}

// Ask answers the message text of the user of userID, by, with the model of
// the supervisor, and returns the messages it added to the conversation: the
// question, the proposals of actions the model made through its tool (see
// propose), validated, executed only once a user confirms them, and the
// answer, or the error, eg: the model unavailable. Nothing the model writes
// runs.
func (s *Supervision) Ask(ctx context.Context, userID int64, by, text string) ([]store.SupervisorMessage, error) {
	return s.AskFollowed(ctx, userID, by, text, noEvents{})
}

// AskFollowed answers as Ask does, telling events what it does as it goes,
// the model asked for a stream of its answer when it has one (see
// llm.ChatStreamer).
func (s *Supervision) AskFollowed(ctx context.Context, userID int64, by, text string,
	events chatEvents) ([]store.SupervisorMessage, error) {
	text, err := checkMessage(text)
	if err != nil {
		return nil, err
	}
	ctx = context.WithoutCancel(ctx) // the answer is kept whatever the request
	history, err := s.store.SupervisorMessages(ctx, userID, 0, chatHistory)
	if err != nil {
		return nil, err
	}
	question, err := s.store.AddSupervisorMessage(ctx, store.SupervisorMessage{UserID: userID,
		Role: store.RoleUser, Kind: store.MessageQuestion, Content: text})
	if err != nil {
		return nil, err
	}
	events.message(question)
	added := []store.SupervisorMessage{question}
	fail := func(message string, cause error) ([]store.SupervisorMessage, error) {
		log.Warnf("supervisor: the answer to %s: %v", by, cause)
		m, err := s.store.AddSupervisorMessage(ctx, store.SupervisorMessage{UserID: userID,
			Role: store.RoleAssistant, Kind: store.MessageError, Content: message})
		if err != nil {
			return added, err
		}
		return append(added, m), nil
	}

	settings, chat, err := s.settingsAndChat(ctx)
	if err != nil {
		if errors.Is(err, errNotConfigured) {
			return fail("O XTrader AI ainda não tem um modelo configurado: defina o provedor, o modelo e a chave "+
				"em Configurações → XTrader AI.", err)
		}
		return fail("Não consegui ler as configurações do XTrader AI agora.", err)
	}
	events.stage(stageReading, "")
	v, err := s.chatView(ctx)
	if err != nil {
		return fail("Não consegui ler os agentes agora: tente de novo em instantes.", err)
	}
	snapshot, err := json.Marshal(v.snapshot)
	if err != nil {
		return fail("Não consegui montar o retrato dos agentes.", err)
	}

	turn := &chatTurn{s: s, userID: userID, by: by, view: v}
	messages := conversationOf(history)
	messages = append(messages, llm.ChatMessage{Role: llm.ChatUser, Content: "Retrato atual dos agentes (JSON):\n" +
		string(snapshot) + "\n\nMensagem do usuário:\n" + text})
	askCtx, cancel := context.WithTimeout(ctx, time.Duration(settings.TimeoutSeconds)*time.Second)
	defer cancel()
	var answer string
	for round := 0; ; round++ {
		if round == maxToolRounds {
			return fail("O XTrader AI não concluiu a resposta: tente reformular o pedido.",
				errors.New("too many calls of tools"))
		}
		events.stage(stageThinking, "")
		reply, err := complete(askCtx, chat, chatSystem(settings), messages, events)
		if err != nil {
			return fail("O modelo do XTrader AI não respondeu ("+modelFailure(err)+"). Os agentes não foram "+
				"afetados; tente de novo em instantes.", err)
		}
		if len(reply.ToolCalls) == 0 {
			answer = strings.TrimSpace(reply.Content)
			break
		}
		messages = append(messages, llm.ChatMessage{Role: llm.ChatAssistant, Content: reply.Content,
			ToolCalls: reply.ToolCalls})
		for _, call := range reply.ToolCalls {
			events.stage(stageTool, call.Name)
			proposed := len(turn.proposals)
			messages = append(messages, llm.ChatMessage{Role: llm.ChatTool, ToolCallID: call.ID,
				Content: turn.call(ctx, call)})
			for _, m := range turn.proposals[proposed:] {
				events.message(m)
			}
		}
	}
	added = append(added, turn.proposals...)
	if answer == "" && len(turn.proposals) > 0 {
		answer = "Preparei a proposta acima: confira o escopo e confirme ou recuse."
	}
	if answer == "" {
		return fail("O XTrader AI respondeu vazio: tente de novo.", errors.New("an empty answer"))
	}
	at := v.at
	m, err := s.store.AddSupervisorMessage(ctx, store.SupervisorMessage{UserID: userID, Role: store.RoleAssistant,
		Kind: store.MessageAnswer, Content: answer, DataAt: &at})
	if err != nil {
		return added, err
	}
	return append(added, m), nil
}

// complete returns the answer of the model of chat to the conversation of
// messages with the tools of the conversation: streamed, its pieces told to
// events, when chat streams; else whole, its text told at once.
func complete(ctx context.Context, chat llm.Chat, system string, messages []llm.ChatMessage,
	events chatEvents) (llm.ChatAnswer, error) {
	if streamer, ok := chat.(llm.ChatStreamer); ok {
		return streamer.Stream(ctx, system, messages, chatTools(), events.text)
	}
	reply, err := chat.Complete(ctx, system, messages, chatTools())
	if err == nil && reply.Content != "" {
		events.text(reply.Content)
	}
	return reply, err
}

// modelFailure names the failure of the model for the user.
func modelFailure(err error) string {
	switch {
	case errors.Is(err, llm.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return "tempo esgotado"
	case errors.Is(err, llm.ErrRateLimited):
		return "limite de uso do provedor"
	case errors.Is(err, llm.ErrUnavailable):
		return "provedor indisponível"
	}
	var apiErr *llm.APIError
	if errors.As(err, &apiErr) && (apiErr.Status == 401 || apiErr.Status == 403) {
		return "chave recusada pelo provedor"
	}
	return "erro do provedor"
}

// conversationOf returns the messages of history for the model: the questions
// and the answers, the proposals and their results as what the supervisor
// said of them; the errors left out.
func conversationOf(history []store.SupervisorMessage) []llm.ChatMessage {
	messages := make([]llm.ChatMessage, 0, len(history))
	for _, m := range history {
		switch m.Kind {
		case store.MessageQuestion:
			messages = append(messages, llm.ChatMessage{Role: llm.ChatUser, Content: m.Content})
		case store.MessageAnswer:
			messages = append(messages, llm.ChatMessage{Role: llm.ChatAssistant, Content: m.Content})
		case store.MessageProposal, store.MessageResult:
			id := int64(0)
			if m.ActionID != nil {
				id = *m.ActionID
			}
			messages = append(messages, llm.ChatMessage{Role: llm.ChatAssistant,
				Content: fmt.Sprintf("[%s da ação %d] %s", map[string]string{store.MessageProposal: "proposta",
					store.MessageResult: "resultado"}[m.Kind], id, m.Content)})
		}
	}
	// the model wants the conversation to start with the user
	for len(messages) > 0 && messages[0].Role != llm.ChatUser {
		messages = messages[1:]
	}
	return messages
}

// chatSystem returns the instructions of the model in a conversation.
func chatSystem(settings store.SupervisorSettings) string {
	return supervisorRules + `

Nesta conversa com o usuário:
- Responda perguntas sobre os agentes com os dados do retrato; use get_agent para os detalhes de um agente (ordens e decisões recentes).
- Para qualquer ação de controle, chame propose_action: você nunca executa nada. A interface mostra a proposta com os agentes, as posições e as ordens afetados, e só o usuário confirma.
- As ações são separadas: bloquear novas entradas (block_entries), liberar entradas (allow_entries), parar o agente (stop_agent, mode when_flat: quando zerado; now: já, só se toda posição tiver stop loss na corretora), cancelar ordens pendentes de entrada (cancel_pending) e encerrar posições (close_positions).
- Encerrar posições atinge só as posições dos agentes da aplicação, nunca operações feitas fora deles. Diga se o usuário também quer cancelar as pendentes (cancel_pending) e manter as entradas bloqueadas depois (keep_blocked, padrão sim).
- Se o pedido for ambíguo (qual agente, quais pares, fechar ou só bloquear, parar quando zerado ou agora), pergunte antes de propor. Nunca adivinhe nomes de agentes: use os nomes exatos do retrato.
- Deixe claro o que é análise, o que é proposta e o que já foi confirmado: o resultado de uma ação confirmada aparece na conversa, conferido na conta.
- A interface mostra suas respostas em Markdown: use listas, **negrito** e tabelas quando ajudarem a ler os números (por exemplo, uma linha por agente), sem títulos longos nem HTML.` +
		criteria(settings)
}

// chatTools returns the tools of the model in a conversation.
func chatTools() []llm.ToolSpec {
	return []llm.ToolSpec{
		{Name: "get_agent", Description: "Os detalhes de um agente: as ordens e as decisões recentes.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string",` +
				`"description":"o nome exato do agente"}},"required":["name"]}`)},
		{Name: "propose_action", Description: "Propõe ao usuário uma ação de controle, que só ele confirma na " +
			"interface. Devolve o que a proposta atinge.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"action":{"type":"string","enum":["block_entries","allow_entries","stop_agent","cancel_pending",` +
				`"close_positions"]},` +
				`"agents":{"type":"array","items":{"type":"string"},"description":"os nomes exatos dos agentes"},` +
				`"all":{"type":"boolean","description":"todos os agentes da aplicação, em vez de agents"},` +
				`"pairs":{"type":"array","items":{"type":"string"},"description":"só estes pares, todos sem nenhum"},` +
				`"mode":{"type":"string","enum":["when_flat","now"],"description":"de stop_agent"},` +
				`"cancel_pending":{"type":"boolean","description":"de close_positions: cancelar também as pendentes de entrada"},` +
				`"keep_blocked":{"type":"boolean","description":"de close_positions: manter as entradas bloqueadas depois (padrão sim)"},` +
				`"reason":{"type":"string","description":"o motivo, nas palavras do pedido do usuário"},` +
				`"category":{"type":"string","enum":["operational","risk","performance"]}` +
				`},"required":["action","reason"]}`)},
	}
}

// chatTurn is a conversation answering a message: the proposals the model
// made.
type chatTurn struct {
	s         *Supervision
	userID    int64
	by        string
	view      *view
	proposals []store.SupervisorMessage
}

// call runs a call of a tool of the conversation and returns its result, JSON.
func (t *chatTurn) call(ctx context.Context, call llm.ToolCall) string {
	switch call.Name {
	case "get_agent":
		var args struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return toolError("invalid arguments")
		}
		return t.agent(ctx, args.Name)
	case "propose_action":
		var args proposal
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return toolError("invalid arguments: " + err.Error())
		}
		action, summary, err := t.s.propose(ctx, t.view, args, t.userID, t.by)
		if err != nil {
			return toolError(err.Error())
		}
		id := action.ID
		m, err := t.s.store.AddSupervisorMessage(ctx, store.SupervisorMessage{UserID: t.userID,
			Role: store.RoleAssistant, Kind: store.MessageProposal, Content: action.Reason, ActionID: &id})
		if err != nil {
			return notRecorded(err)
		}
		t.proposals = append(t.proposals, m)
		return toolResult(map[string]any{"proposal": action.ID, "affects": summary,
			"status": "waiting for the confirmation of the user in the interface, for 10 minutes"})
	}
	return toolError("unknown tool " + call.Name)
}

// agent returns the details of the agent of name: its summary of the view, its
// last orders and decisions.
func (t *chatTurn) agent(ctx context.Context, name string) string {
	a, ok := t.view.agents[name]
	if !ok {
		return toolError(fmt.Sprintf("no agent %q: use the exact name of the snapshot", name))
	}
	details := map[string]any{"name": a.Name}
	for _, summary := range t.view.snapshot.Agents {
		if summary.Name == a.Name {
			details["summary"] = summary
		}
	}
	orders, err := t.s.store.RecentOrders(ctx, a.ID, maxAgentOrders)
	if err == nil {
		var recent []map[string]any
		for _, o := range orders {
			recent = append(recent, map[string]any{"id": o.ID, "pair": o.Pair, "side": o.Side, "type": o.Type,
				"status": o.Status, "price": o.Price, "quantity": o.Quantity,
				"at": o.UpdatedAt.In(t.view.at.Location()).Format(time.RFC3339)})
		}
		details["orders"] = recent
	}
	if a.LLM != nil {
		decisions, err := t.s.store.Decisions(ctx, a.ID, "", 10)
		if err == nil {
			var recent []ui.SupervisorDecision
			for _, d := range decisions {
				recent = append(recent, ui.SupervisorDecision{
					At: time.Unix(d.DecidedAt, 0).In(t.view.at.Location()).Format(time.RFC3339), Pair: d.Pair,
					Action: d.Action, Confidence: d.Confidence, Outcome: d.Outcome, Reason: d.Reason})
			}
			details["decisions"] = recent
		}
	}
	return toolResult(details)
}

// proposal is an action the model proposes, see chatTools.
type proposal struct {
	Action        string   `json:"action"`
	Agents        []string `json:"agents"`
	All           bool     `json:"all"`
	Pairs         []string `json:"pairs"`
	Mode          string   `json:"mode"`
	CancelPending bool     `json:"cancel_pending"`
	KeepBlocked   *bool    `json:"keep_blocked"`
	Reason        string   `json:"reason"`
	Category      string   `json:"category"`
}

// propose records the proposal p of the user of userID, by, validated against
// v: the agents named exactly, or all; for a close, the positions of the
// agents of the application on the pairs, the ones of agents not running
// apart, none of the account otherwise; for a cancel, their pending entries.
// It returns the action proposed and what it affects, for the model.
func (s *Supervision) propose(ctx context.Context, v *view, p proposal, userID int64, by string) (
	store.SupervisorAction, map[string]any, error) {
	p.Reason = strings.TrimSpace(p.Reason)
	switch {
	case !slices.Contains([]string{store.ActionBlockEntries, store.ActionAllowEntries, store.ActionStopAgent,
		store.ActionCancelPending, store.ActionClosePositions}, p.Action):
		return store.SupervisorAction{}, nil, fmt.Errorf("unknown action %q", p.Action)
	case p.Reason == "" || utf8.RuneCountInString(p.Reason) > maxReasonLength:
		return store.SupervisorAction{}, nil, fmt.Errorf("the reason is required, %d characters at most",
			maxReasonLength)
	case p.All == (len(p.Agents) > 0):
		return store.SupervisorAction{}, nil, errors.New("the scope is ambiguous: name the agents, or all of " +
			"them, not both nor none: ask the user")
	case p.Category != "" && p.Category != store.CategoryOperational && p.Category != store.CategoryRisk &&
		p.Category != store.CategoryPerformance:
		return store.SupervisorAction{}, nil, errors.New("category: operational, risk or performance")
	case p.Action == store.ActionStopAgent && p.Mode != "" && p.Mode != store.ModeWhenFlat && p.Mode != store.ModeNow:
		return store.SupervisorAction{}, nil, errors.New("mode: when_flat or now")
	}

	// the agents of the scope, by their exact names
	var names []string
	if p.All {
		for _, summary := range v.snapshot.Agents {
			if summary.Enabled || len(summary.Positions) > 0 || len(summary.Pending) > 0 {
				names = append(names, summary.Name)
			}
		}
	} else {
		var unknown []string
		for _, name := range p.Agents {
			if _, ok := v.agents[name]; ok {
				if !slices.Contains(names, name) {
					names = append(names, name)
				}
				continue
			}
			unknown = append(unknown, name)
		}
		if len(unknown) > 0 {
			return store.SupervisorAction{}, nil, fmt.Errorf("unknown agents %s: use the exact names of the "+
				"snapshot, or ask the user", strings.Join(unknown, ", "))
		}
	}
	slices.Sort(names)
	scope := ui.SupervisorScope{Agents: names, Pairs: p.Pairs}
	options := ui.SupervisorOptions{CancelPending: p.CancelPending, KeepBlocked: p.KeepBlocked == nil ||
		*p.KeepBlocked}
	onPair := func(pair string) bool {
		return len(p.Pairs) == 0 || slices.ContainsFunc(p.Pairs, func(want string) bool {
			return strings.EqualFold(want, pair)
		})
	}
	var gaps []string
	for _, name := range names {
		if gap := v.agentGap(name); gap != "" {
			gaps = append(gaps, name+": "+gap)
		}
	}
	summary := map[string]any{"agents": names}
	switch p.Action {
	case store.ActionClosePositions:
		for _, agent := range v.snapshot.Agents {
			if !slices.Contains(names, agent.Name) {
				continue
			}
			for _, position := range agent.Positions {
				if !onPair(position.Pair) {
					continue
				}
				scope.Positions = append(scope.Positions, ui.SupervisorScopePosition{Agent: agent.Name,
					SupervisorPosition: position, Account: agent.Account, Running: agent.State == "running"})
			}
			if options.CancelPending {
				scope.Orders = append(scope.Orders, ordersOf(agent, onPair)...)
			}
		}
		if len(scope.Positions) == 0 {
			return store.SupervisorAction{}, nil, errors.New("no open position of the agents of the scope: " +
				"nothing to close")
		}
		summary["positions"], summary["pending_entries"] = len(scope.Positions), len(scope.Orders)
		summary["keep_blocked"], summary["cancel_pending"] = options.KeepBlocked, options.CancelPending
	case store.ActionCancelPending:
		for _, agent := range v.snapshot.Agents {
			if slices.Contains(names, agent.Name) {
				scope.Orders = append(scope.Orders, ordersOf(agent, onPair)...)
			}
		}
		if len(scope.Orders) == 0 {
			return store.SupervisorAction{}, nil, errors.New("no pending entry of the agents of the scope")
		}
		summary["pending_entries"] = len(scope.Orders)
	case store.ActionStopAgent:
		if p.Mode == "" {
			p.Mode = store.ModeWhenFlat
		}
		summary["mode"] = p.Mode
	}
	if len(gaps) > 0 {
		summary["incomplete_data"] = gaps
	}
	expires := s.now().Add(proposalTTL)
	action := store.SupervisorAction{Source: store.SourceSupervisor, UserID: &userID, Action: p.Action,
		Reason: p.Reason, Category: p.Category, Status: store.ActionProposed, RequestedBy: by, ExpiresAt: &expires}
	if p.Action == store.ActionStopAgent {
		action.Mode = p.Mode
	}
	action.Scope, _ = json.Marshal(scope)
	action.Options, _ = json.Marshal(options)
	created, err := s.store.CreateSupervisorAction(ctx, action)
	return created, summary, err
}

// ordersOf returns the pending entries of the agent on the pairs onPair takes.
func ordersOf(agent ui.SupervisorAgent, onPair func(string) bool) []ui.SupervisorScopeOrder {
	var orders []ui.SupervisorScopeOrder
	for _, pending := range agent.Pending {
		if pending.Entry && onPair(pending.Pair) {
			orders = append(orders, ui.SupervisorScopeOrder{Agent: agent.Name, SupervisorPending: pending,
				Running: agent.State == "running"})
		}
	}
	return orders
}

// Confirm executes the proposal of id, confirmed by the user of userID, by, and
// adds its result, checked, to the conversation: store.ErrDecided when it was
// decided already, store.ErrExpired once expired.
func (s *Supervision) Confirm(ctx context.Context, id, userID int64, by string) (store.SupervisorAction,
	store.SupervisorMessage, error) {
	action, err := s.store.ClaimSupervisorAction(ctx, id, by)
	if err != nil {
		return action, store.SupervisorMessage{}, err
	}
	status, result := s.execute(ctx, action)
	action, err = s.store.SupervisorAction(context.WithoutCancel(ctx), id)
	if err != nil {
		return action, store.SupervisorMessage{}, err
	}
	m, err := s.store.AddSupervisorMessage(context.WithoutCancel(ctx), store.SupervisorMessage{UserID: userID,
		Role: store.RoleAssistant, Kind: store.MessageResult, Content: resultText(status, result), ActionID: &id})
	return action, m, err
}

// Reject rejects the proposal of id, by the user of userID, by, and adds it to
// the conversation.
func (s *Supervision) Reject(ctx context.Context, id, userID int64, by string) (store.SupervisorAction,
	store.SupervisorMessage, error) {
	action, err := s.store.RejectSupervisorAction(ctx, id, by)
	if err != nil {
		return action, store.SupervisorMessage{}, err
	}
	m, err := s.store.AddSupervisorMessage(ctx, store.SupervisorMessage{UserID: userID,
		Role: store.RoleAssistant, Kind: store.MessageResult, Content: "Proposta recusada: nada foi executado.",
		ActionID: &id})
	return action, m, err
}

// resultText tells the result of an action, checked, in a sentence.
func resultText(status string, result ui.SupervisorResult) string {
	var done, failed, unknown int
	for _, item := range result.Items {
		switch item.Status {
		case itemDone, itemAlready, itemPending:
			done++
		case itemFailed:
			failed++
		case itemUnknown:
			unknown++
		}
	}
	switch status {
	case store.ActionSucceeded:
		return fmt.Sprintf("Ação concluída e conferida: %d item(ns).", done)
	case store.ActionPartial:
		return fmt.Sprintf("Ação parcial: %d concluído(s), %d com falha, %d desconhecido(s). Veja os detalhes.",
			done, failed, unknown)
	case store.ActionUnknown:
		return fmt.Sprintf("Resultado desconhecido em %d item(ns): confira na conta; nada foi reenviado.", unknown)
	}
	return fmt.Sprintf("A ação falhou: %d item(ns) sem efeito. Veja os detalhes.", failed)
}
