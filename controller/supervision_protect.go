package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// This file has the XTrader AI arm the protection of an open position (the
// breakeven or the trailing stop of its agent), see protectPosition: only
// where the agent allows it, on a position its agent told, the trigger of the
// agent kept: the agent applies it, only tightening the stop loss.

// protectTool is the tool arming the protection of a position.
func protectTool() llm.ToolSpec {
	return llm.ToolSpec{Name: "protect_position", Description: "Arma o breakeven ou o trailing stop do agente " +
		"numa posição aberta, para proteger o lucro quando os dados indicam risco de reversão. Só nos agentes " +
		"que autorizam o recurso (protection.*.supervisor no retrato); o gatilho configurado no agente vale: " +
		"antes dele, o recurso fica armado e o agente aplica quando o preço chegar lá. O stop só aperta, nunca " +
		"afrouxa. Use a referência da posição de protections.",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"agent":{"type":"string","description":"o nome exato do agente"},` +
			`"pair":{"type":"string","description":"o par da posição"},` +
			`"position_ref":{"type":"string","description":"a referência da posição, de protections"},` +
			`"feature":{"type":"string","enum":["breakeven","trailing"]},` +
			`"reason":{"type":"string","description":"o risco observado e a proteção pedida, sem tratar uma reversão como certa"},` +
			`"category":{"type":"string","enum":["operational","risk","performance"]},` +
			`"evidence":{"type":"string","description":"os números observados: preço, stop, ATR, avanço, P/L, com a moeda e o horário"}` +
			`},"required":["agent","pair","position_ref","feature","reason","category","evidence"]}`)}
}

// protectArgs are the arguments of protect_position.
type protectArgs struct {
	Agent       string `json:"agent"`
	Pair        string `json:"pair"`
	PositionRef string `json:"position_ref"`
	Feature     string `json:"feature"`
	Reason      string `json:"reason"`
	Category    string `json:"category"`
	Evidence    string `json:"evidence"`
}

// protectPosition arms the protection of args on a position, asked by the
// analysis runID or by the user userID of a conversation (by): refused unless
// the agent allows the feature to the supervisor, the data of the agent is
// whole and the position is one its agent told; recorded, delivered to the
// agent by its runtime and asked of it at once. It returns the result for
// the model, and the arm recorded (nil when refused before).
func (s *Supervision) protectPosition(ctx context.Context, v *view, raw json.RawMessage, runID, userID *int64,
	by string) (string, *store.PositionProtection) {
	var args protectArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("invalid arguments: " + err.Error()), nil
	}
	args.Reason, args.Evidence = strings.TrimSpace(args.Reason), strings.TrimSpace(args.Evidence)
	agent, ok := v.agents[args.Agent]
	switch {
	case !ok:
		return toolError(fmt.Sprintf("no agent %q: use the exact name of the snapshot", args.Agent)), nil
	case args.Feature != ui.ProtectionBreakeven && args.Feature != ui.ProtectionTrailing:
		return toolError("feature: breakeven or trailing"), nil
	case args.Reason == "" || utf8.RuneCountInString(args.Reason) > maxReasonLength:
		return toolError(fmt.Sprintf("the reason is required, %d characters at most", maxReasonLength)), nil
	case args.Category != store.CategoryOperational && args.Category != store.CategoryRisk &&
		args.Category != store.CategoryPerformance:
		return toolError("category: operational, risk or performance"), nil
	}
	if gap := v.agentGap(agent.Name); gap != "" {
		return toolError("the data of the agent is incomplete (" + gap + "): nothing armed"), nil
	}
	settings := agent.ProtectionOf()
	allowed := args.Feature == ui.ProtectionBreakeven && settings.Breakeven.Supervisor ||
		args.Feature == ui.ProtectionTrailing && settings.Trailing.Supervisor
	if !allowed {
		return toolError(fmt.Sprintf("the agent %s doesn't let the XTrader AI arm its %s: nothing armed", agent.Name,
			args.Feature)), nil
	}
	var position *ui.PositionProtection
	for _, p := range v.protections[agent.Name] {
		if p.Pair == args.Pair && p.PositionRef == args.PositionRef {
			position = &p
			break
		}
	}
	if position == nil {
		return toolError(fmt.Sprintf("the agent %s told no open position %s on %s: use protections of the "+
			"snapshot", agent.Name, args.PositionRef, args.Pair)), nil
	}

	evidence, _ := json.Marshal([]string{args.Evidence})
	category := args.Category
	arm, err := s.store.ArmProtection(ctx, store.PositionProtection{AgentID: agent.ID, RunID: runID,
		UserID: userID, Pair: args.Pair, PositionRef: args.PositionRef, Ticket: ticketOf(args.PositionRef),
		Side: position.Side, Feature: args.Feature, Reason: args.Reason, Category: &category, Evidence: evidence,
		RequestedBy: by})
	switch {
	case errors.Is(err, store.ErrConflict):
		return toolError(fmt.Sprintf("the %s of the position %s is armed already", args.Feature,
			args.PositionRef)), nil
	case err != nil:
		return notRecorded(err), nil
	}
	if err := s.manager.RefreshRuntime(ctx, agent.Name); err != nil {
		log.Warnf("supervisor: the runtime of the agent %s: %v", agent.Name, err)
	}

	var response ui.SupervisionProtectResponse
	err = s.callAgent(ctx, agent.Name, "POST", "supervision/protect", ui.SupervisionProtectRequest{
		ArmID: arm.ID, Pair: args.Pair, PositionRef: args.PositionRef, Feature: args.Feature,
		Reason: args.Reason}, &response)
	if err != nil {
		// the runtime delivers it all the same: its outcome read later, see syncArms
		log.Warnf("supervisor: the arm %d of the agent %s: %v", arm.ID, agent.Name, err)
		return toolResult(map[string]any{"arm": arm.ID, "status": store.ArmRequested,
			"detail": "the agent didn't answer: armed through its runtime, its outcome read later"}), &arm
	}
	status, move := armOutcome(response)
	if err := s.store.SetArmStatus(ctx, arm.ID, status, move); err != nil {
		log.Errorf("supervisor: recording the arm %d: %v", arm.ID, err)
	}
	arm.Status = status
	if move != nil {
		arm.Move, arm.Previous, arm.Requested, arm.Confirmed = move.Move, move.Previous, move.Requested, move.Confirmed
		arm.Detail = move.Detail
	}
	log.Infof("supervisor: the %s of the position %s of %s armed by %s: %s %s", args.Feature, args.PositionRef,
		agent.Name, by, response.Status, response.Detail)
	return toolResult(map[string]any{"arm": arm.ID, "status": response.Status, "previous": response.Previous,
		"requested": response.Requested, "confirmed": response.Confirmed, "detail": response.Detail}), &arm
}

// armOutcome returns the status of an arm and its move from the answer of its
// agent: applied, the stop loss confirmed by the broker; armed, waiting for
// its trigger; armed with the move rejected, or unknown, the stop loss kept;
// refused.
func armOutcome(response ui.SupervisionProtectResponse) (string, *store.ProtectionMove) {
	move := &store.ProtectionMove{Previous: response.Previous, Requested: response.Requested,
		Confirmed: response.Confirmed, Detail: response.Detail}
	switch response.Status {
	case ui.ProtectApplied:
		move.Move = store.MoveConfirmed
		return store.ArmActive, move
	case ui.ProtectFailed:
		move.Move = store.MoveRejected
		return store.ArmArmed, move
	case ui.ProtectUnknown:
		move.Move = store.MoveUnknown
		return store.ArmArmed, move
	case ui.ProtectRefused:
		return store.ArmRefused, move
	}
	return store.ArmArmed, &store.ProtectionMove{Previous: response.Previous, Detail: response.Detail}
}

// ticketOf returns the ticket of an MT5 position of its reference, mt5:<id>,
// 0 for another one.
func ticketOf(ref string) int64 {
	id, ok := strings.CutPrefix(ref, "mt5:")
	if !ok {
		return 0
	}
	ticket, _ := strconv.ParseInt(id, 10, 64)
	return ticket
}

// syncArms records how the arms in force stand, as their agents tell them:
// active once their feature moved the stop loss, with their last move; ended
// once their position closed (read whole from the agent running); left as
// they are when the agent can't be read.
func (s *Supervision) syncArms(ctx context.Context) {
	agents, err := s.store.Agents(ctx)
	if err != nil {
		return
	}
	for _, a := range agents {
		arms, err := s.store.ProtectionArmsInForce(ctx, a.ID)
		if err != nil || len(arms) == 0 || !s.running(a.Name) {
			continue
		}
		state, err := s.supervision(ctx, a.Name)
		if err != nil {
			continue
		}
		byRef := make(map[string]ui.PositionProtection, len(state.Protections))
		told := make(map[string]bool, len(state.Protections)) // the pairs whose protection the agent told
		for _, p := range state.Protections {
			byRef[p.PositionRef], told[p.Pair] = p, true
		}
		held := make(map[string]bool, len(state.Positions))
		for _, p := range state.Positions {
			held[p.Pair] = true
		}
		for _, arm := range arms {
			p, ok := byRef[arm.PositionRef]
			if !ok {
				// its position closed: its pair flat, or another position told on
				// it; else not told yet, eg: just after a restart
				if !held[arm.Pair] || told[arm.Pair] {
					if err := s.store.SetArmStatus(ctx, arm.ID, store.ArmEnded, nil); err != nil {
						log.Warnf("supervisor: ending the arm %d: %v", arm.ID, err)
					}
				}
				continue
			}
			feature := p.Breakeven
			if arm.Feature == ui.ProtectionTrailing {
				feature = p.Trailing
			}
			status, move := arm.Status, (*store.ProtectionMove)(nil)
			if feature.Last != nil && feature.Last.At >= arm.CreatedAt.Unix() && feature.Last.Status != ui.MoveRequested {
				move = &store.ProtectionMove{Move: feature.Last.Status, Previous: feature.Last.Previous,
					Requested: feature.Last.Requested, Confirmed: feature.Last.Confirmed, Detail: feature.Last.Detail}
				if feature.Last.Status == ui.MoveConfirmed {
					status = store.ArmActive
				}
			}
			if status == store.ArmRequested && feature.Armed {
				status = store.ArmArmed
			}
			if status == arm.Status && (move == nil || move.Move == arm.Move && move.Confirmed == arm.Confirmed &&
				move.Requested == arm.Requested) {
				continue
			}
			if err := s.store.SetArmStatus(ctx, arm.ID, status, move); err != nil {
				log.Warnf("supervisor: recording the arm %d: %v", arm.ID, err)
			}
		}
	}
}

// protectionInfo returns the arm a of the agent named name for the dashboard.
func protectionInfo(a store.PositionProtection, name string) ui.SupervisorProtectionInfo {
	info := ui.SupervisorProtectionInfo{ID: a.ID, Agent: name, RunID: a.RunID, Pair: a.Pair,
		PositionRef: a.PositionRef, Ticket: a.Ticket, Side: a.Side, Feature: a.Feature, Reason: a.Reason,
		Evidence: []string{}, RequestedBy: a.RequestedBy, Status: a.Status, Move: a.Move, Previous: a.Previous,
		Requested: a.Requested, Confirmed: a.Confirmed, Detail: a.Detail, CreatedAt: a.CreatedAt.Unix(),
		UpdatedAt: a.UpdatedAt.Unix(), EndedAt: unix(a.EndedAt)}
	if a.Category != nil {
		info.Category = *a.Category
	}
	_ = json.Unmarshal(a.Evidence, &info.Evidence)
	return info
}

// featureLabel names a feature of the protection for the user.
func featureLabel(feature string) string {
	if feature == ui.ProtectionTrailing {
		return "trailing stop"
	}
	return "breakeven"
}

// protectionText tells the user how the arm a went, as the agent and its
// broker answered: written by the system, never by the model.
func protectionText(a store.PositionProtection, v *view) string {
	name := fmt.Sprintf("agente %d", a.AgentID)
	for _, agent := range v.agents {
		if agent.ID == a.AgentID {
			name = agent.Name
		}
	}
	subject := fmt.Sprintf("Proteção #%d: %s de %s em %s (posição %s)", a.ID, featureLabel(a.Feature), name, a.Pair,
		a.PositionRef)
	switch {
	case a.Status == store.ArmActive && a.Move == store.MoveConfirmed:
		return fmt.Sprintf("%s aplicada: stop %s → %s, confirmado pela corretora.", subject, level(a.Previous),
			level(a.Confirmed))
	case a.Status == store.ArmRefused:
		return fmt.Sprintf("%s recusada pelo agente: %s", subject, a.Detail)
	case a.Move == store.MoveRejected:
		return fmt.Sprintf("%s armada; o stop em %s foi recusado pela corretora (%s). O stop segue em %s.", subject,
			level(a.Requested), a.Detail, level(a.Previous))
	case a.Move == store.MoveUnknown:
		return fmt.Sprintf("%s armada; a resposta da corretora ao stop em %s se perdeu: o stop é relido, nunca "+
			"reenviado às cegas.", subject, level(a.Requested))
	case a.Status == store.ArmRequested:
		return fmt.Sprintf("%s enviada ao agente, que não respondeu agora: o resultado aparece no registro.", subject)
	}
	detail := strings.TrimPrefix(a.Detail, "armado: ")
	if detail == "" {
		detail = "aguardando o gatilho do agente"
	}
	return fmt.Sprintf("%s armada: %s.", subject, strings.TrimSuffix(detail, "."))
}

// level writes a stop loss, "sem stop" for none.
func level(price float64) string {
	if price <= 0 {
		return "sem stop"
	}
	return strconv.FormatFloat(price, 'f', -1, 64)
}
