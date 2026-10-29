package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// executeTool is the tool of the conversation executing at once an action the
// user asked for explicitly and whole, see chatTurn.execute.
func executeTool() llm.ToolSpec {
	return llm.ToolSpec{Name: "execute_action", Description: "Executa na hora uma ação de controle que o " +
		"usuário pediu de forma explícita e completa nesta mensagem (a ação, os agentes nomeados ou todos, e as " +
		"opções). Sem confirmação na interface: o resultado, conferido na conta, aparece na conversa. Se faltar " +
		"algo, pergunte em vez de chamar.",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"action":{"type":"string","enum":["block_entries","allow_entries","stop_agent","cancel_pending",` +
			`"close_positions"]},` +
			`"agents":{"type":"array","items":{"type":"string"},"description":"os nomes exatos dos agentes"},` +
			`"all":{"type":"boolean","description":"todos os agentes da aplicação, em vez de agents"},` +
			`"pairs":{"type":"array","items":{"type":"string"},"description":"só estes pares, todos sem nenhum"},` +
			`"mode":{"type":"string","enum":["when_flat","now"],"description":"de stop_agent: obrigatório"},` +
			`"cancel_pending":{"type":"boolean","description":"de close_positions: obrigatório, cancelar também as pendentes de entrada"},` +
			`"keep_blocked":{"type":"boolean","description":"de close_positions: obrigatório, manter as entradas bloqueadas depois"},` +
			`"reason":{"type":"string","description":"o motivo, nas palavras do pedido do usuário"},` +
			`"category":{"type":"string","enum":["operational","risk","performance"]}` +
			`},"required":["action","reason"]}`)}
}

// dailyTools are the tools of the conversation of the daily limits.
func dailyTools() []llm.ToolSpec {
	return []llm.ToolSpec{
		{Name: "get_daily_limits", Description: "Os limites diários de perda e ganho (de todos os agentes somados " +
			"e de cada agente), o resultado do dia considerado (realizado + aberto), e o estado das travas.",
			Parameters: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "set_daily_limits", Description: "Configura limites diários de perda e ganho, só quando o usuário " +
			"pedir. scope aggregate_all: UM limite para o resultado somado de todos os agentes (na moeda das " +
			"contas, sem conversão: com contas em moedas diferentes, o mesmo valor vale em cada moeda); each_agent: o " +
			"mesmo limite para CADA agente, individualmente. Se o pedido não deixar claro qual dos dois, pergunte. " +
			"Ao atingir um limite, o sistema zera as posições do escopo, cancela as pendentes de entrada e bloqueia " +
			"as entradas até 00:00. Uma mudança que dispararia ou liberaria a trava agora só é aplicada com " +
			"confirm_immediate_effect, depois de o usuário concordar com o efeito devolvido.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"scope":{"type":"string","enum":["aggregate_all","each_agent"]},` +
				`"agents":{"type":"array","items":{"type":"string"},"description":"de each_agent: os nomes exatos"},` +
				`"all":{"type":"boolean","description":"de each_agent: todos os agentes em live"},` +
				`"enabled":{"type":"boolean","description":"ligar (padrão, com um limite) ou desligar"},` +
				`"max_loss":{"type":["number","null"],"description":"a perda máxima do dia, positiva; null para nenhuma"},` +
				`"max_gain":{"type":["number","null"],"description":"o ganho máximo do dia; null para nenhum"},` +
				`"reason":{"type":"string","description":"o motivo, nas palavras do usuário"},` +
				`"confirm_immediate_effect":{"type":"boolean","description":"o usuário concordou com o efeito imediato devolvido antes"}` +
				`},"required":["scope","reason"]}`)},
	}
}

// explicitAction is an action the user asked for explicitly, see executeTool: the
// options of a close and the mode of a stop told, never taken by default.
type explicitAction struct {
	proposal
	CancelPending *bool `json:"cancel_pending"`
}

// execute executes at once the action the user asked for, see executeTool:
// validated as a proposal (see buildAction) against the accounts read now,
// its options told explicitly; recorded as asked by the user (the message the
// evidence), executed (see Supervision.execute), its result checked added to
// the conversation.
func (t *chatTurn) execute(ctx context.Context, arguments json.RawMessage) string {
	var args explicitAction
	if err := json.Unmarshal(arguments, &args); err != nil {
		return toolError("invalid arguments: " + err.Error())
	}
	switch {
	case args.Action == store.ActionClosePositions && (args.CancelPending == nil || args.KeepBlocked == nil):
		return toolError("ambiguous: closing, cancel_pending and keep_blocked are required: ask the user")
	case args.Action == store.ActionStopAgent && args.Mode == "":
		return toolError("ambiguous: stopping, the mode (when_flat or now) is required: ask the user")
	}
	if args.CancelPending != nil {
		args.proposal.CancelPending = *args.CancelPending
	}
	v, err := t.s.read(ctx)
	if err != nil {
		return toolError("the accounts can't be read now: nothing done: " + err.Error())
	}
	action, summary, err := t.s.buildAction(v, args.proposal, true)
	if err != nil {
		return toolError(err.Error())
	}
	evidence := t.text
	if utf8.RuneCountInString(evidence) > maxReasonLength {
		evidence = string([]rune(evidence)[:maxReasonLength])
	}
	action.Source, action.Status, action.RequestedBy, action.ConfirmedBy = store.SourceUser,
		store.ActionExecuting, t.by, t.by
	action.UserID = &t.userID
	action.Evidence, _ = json.Marshal([]string{"pedido do usuário: " + evidence})
	created, err := t.s.store.CreateSupervisorAction(ctx, action)
	if err != nil {
		return notRecorded(err)
	}
	log.Infof("supervisor: action %d (%s) asked by %s in the conversation, executed", created.ID, created.Action,
		t.by)
	status, result := t.s.execute(ctx, created)
	id := created.ID
	m, err := t.s.store.AddSupervisorMessage(context.WithoutCancel(ctx), store.SupervisorMessage{UserID: t.userID,
		Role: store.RoleAssistant, Kind: store.MessageResult, Content: resultText(status, result), ActionID: &id})
	if err != nil {
		return notRecorded(err)
	}
	t.proposals = append(t.proposals, m)
	return toolResult(map[string]any{"action": id, "status": status, "affects": summary, "items": result.Items})
}

// dailyLimits answers the daily limits and how they stand, see dailyTools.
func (t *chatTurn) dailyLimits(ctx context.Context) string {
	limit, _, err := t.s.store.GlobalDailyLimit(ctx)
	if err != nil {
		return toolError(err.Error())
	}
	return toolResult(map[string]any{"aggregate_all": limit.Settings(), "status": t.s.daily.Status()})
}

// dailyArgs are the arguments of set_daily_limits, see dailyTools.
type dailyArgs struct {
	Scope   string   `json:"scope"`
	Agents  []string `json:"agents"`
	All     bool     `json:"all"`
	Enabled *bool    `json:"enabled"`
	MaxLoss *float64 `json:"max_loss"`
	MaxGain *float64 `json:"max_gain"`
	Reason  string   `json:"reason"`
	Confirm bool     `json:"confirm_immediate_effect"`
}

// The scopes of set_daily_limits.
const (
	scopeAggregate = "aggregate_all"
	scopeEach      = "each_agent"
)

// setDailyLimits sets the daily limits the user asked for, see dailyTools:
// one limit of all the agents together, or the same limit of each agent
// named (or all the agents live), never guessed; through the settings of the
// limits (see dailyLimiter.change), a change tripping or releasing a guard now
// left unapplied, its effect told, until the user confirms it; the result
// added to the conversation.
func (t *chatTurn) setDailyLimits(ctx context.Context, arguments json.RawMessage) string {
	var args dailyArgs
	if err := json.Unmarshal(arguments, &args); err != nil {
		return toolError("invalid arguments: " + err.Error())
	}
	args.Reason = strings.TrimSpace(args.Reason)
	enabled := args.MaxLoss != nil || args.MaxGain != nil
	if args.Enabled != nil {
		enabled = *args.Enabled
	}
	limits := ui.DailyLimitSettings{Enabled: enabled, MaxLoss: args.MaxLoss, MaxGain: args.MaxGain}
	switch {
	case args.Reason == "" || utf8.RuneCountInString(args.Reason) > maxReasonLength:
		return toolError(fmt.Sprintf("the reason is required, %d characters at most", maxReasonLength))
	case args.Scope == scopeAggregate && (len(args.Agents) > 0 || args.All):
		return toolError("ambiguous: aggregate_all is one limit of all the agents together, without agents: " +
			"ask the user whether they want it, or a limit for each agent")
	case args.Scope == scopeEach && args.All == (len(args.Agents) > 0):
		return toolError("ambiguous: each_agent needs the agents named, or all, not both nor none: ask the user")
	case args.Scope != scopeAggregate && args.Scope != scopeEach:
		return toolError("scope: aggregate_all (one limit of all the agents together) or each_agent (the same " +
			"limit for each agent): ask the user which")
	}
	change := store.DailyChange{Via: store.ViaChat, By: t.by, UserID: &t.userID, Reason: args.Reason}
	if args.Scope == scopeAggregate {
		preview, applied, err := t.s.daily.change(ctx, nil, limits, change, args.Confirm)
		if err != nil {
			return toolError(err.Error())
		}
		return t.dailyResult([]dailyOutcome{{Scope: "todos os agentes somados, na moeda de cada conta",
			Applied: applied, Preview: preview,
			Limits: limits}})
	}

	agents, err := t.s.store.Agents(ctx)
	if err != nil {
		return toolError(err.Error())
	}
	var targets []store.Agent
	var outcomes []dailyOutcome
	if args.All {
		for _, a := range agents {
			if a.Mode == "live" {
				targets = append(targets, a)
			} else {
				outcomes = append(outcomes, dailyOutcome{Scope: a.Name, Skipped: "em paper: não se aplica " +
					"(sem resultado na conta)"})
			}
		}
	} else {
		var unknown []string
		for _, name := range args.Agents {
			i := slices.IndexFunc(agents, func(a store.Agent) bool { return a.Name == name })
			if i < 0 {
				unknown = append(unknown, name)
				continue
			}
			targets = append(targets, agents[i])
		}
		if len(unknown) > 0 {
			return toolError(fmt.Sprintf("unknown agents %s: use the exact names of the snapshot, or ask the user",
				strings.Join(unknown, ", ")))
		}
	}
	for _, a := range targets {
		preview, applied, err := t.s.daily.change(ctx, &a, limits, change, args.Confirm)
		outcome := dailyOutcome{Scope: a.Name, Applied: applied, Preview: preview, Limits: limits}
		if err != nil {
			outcome.Error = err.Error()
		}
		outcomes = append(outcomes, outcome)
	}
	return t.dailyResult(outcomes)
}

// dailyOutcome is what a change of the daily limits of a scope came to.
type dailyOutcome struct {
	Scope   string                `json:"scope"`
	Applied bool                  `json:"applied"`
	Limits  ui.DailyLimitSettings `json:"limits"`
	Preview ui.DailyLimitPreview  `json:"effect"`
	Skipped string                `json:"skipped,omitempty"`
	Error   string                `json:"error,omitempty"`
}

// dailyResult returns the outcomes of a change of the daily limits for the
// model, the ones applied added to the conversation by the system.
func (t *chatTurn) dailyResult(outcomes []dailyOutcome) string {
	var lines []string
	pending := false
	for _, o := range outcomes {
		switch {
		case o.Error != "":
			lines = append(lines, fmt.Sprintf("- %s: não aplicado (%s)", o.Scope, o.Error))
		case o.Skipped != "":
			lines = append(lines, fmt.Sprintf("- %s: %s", o.Scope, o.Skipped))
		case o.Applied:
			lines = append(lines, fmt.Sprintf("- %s: %s, aplicado agora. %s", o.Scope, limitsText(o.Limits),
				o.Preview.Message))
		default:
			pending = true
		}
	}
	if len(lines) > 0 {
		m, err := t.s.store.AddSupervisorMessage(context.Background(), store.SupervisorMessage{UserID: t.userID,
			Role: store.RoleAssistant, Kind: store.MessageResult,
			Content: "Limites diários:\n" + strings.Join(lines, "\n")})
		if err != nil {
			return notRecorded(err)
		}
		t.proposals = append(t.proposals, m)
	}
	result := map[string]any{"outcomes": outcomes}
	if pending {
		result["needs_confirmation"] = "some changes would trip or release a guard now: tell the user the effect " +
			"and call again with confirm_immediate_effect only if they agree"
	}
	return toolResult(result)
}

// limitsText returns limits in the words of the conversation, eg: perda
// máxima 150,00 BRL, ganho máximo 300,00 BRL.
func limitsText(limits ui.DailyLimitSettings) string {
	if !limits.Enabled {
		return "limites desligados"
	}
	var parts []string
	if limits.MaxLoss != nil {
		parts = append(parts, "perda máxima "+money(*limits.MaxLoss, limits.Currency))
	}
	if limits.MaxGain != nil {
		parts = append(parts, "ganho máximo "+money(*limits.MaxGain, limits.Currency))
	}
	return strings.Join(parts, ", ")
}
