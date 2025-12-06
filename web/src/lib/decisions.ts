// The decisions of the language model of a strategy (see Decision): their
// Portuguese labels, and how the events of the API update what is shown.

import type { ControlsResponse, Decision, DecisionEvent, Snapshot } from "../api/types";

const actions: Record<string, string> = {
  WAIT: "Aguardar",
  ENTER_LONG: "Comprar",
  ENTER_SHORT: "Vender",
  HOLD: "Manter",
  EXIT: "Sair",
  MODIFY_SL: "Mover stop",
  MODIFY_TP: "Mover alvo",
};

const outcomes: Record<string, string> = {
  applied: "Aplicada",
  ignored: "Ignorada: confiança baixa",
  refused: "Recusada pelas regras",
  failed: "Sem decisão",
};

const triggers: Record<string, string> = {
  candle: "Fechamento do candle",
  start: "Início do bot",
  reanalysis: "Reanálise",
};

const channels: Record<string, string> = { web: "painel", telegram: "Telegram" };

const noteKinds: Record<string, string> = {
  text: "Texto",
  reasoning: "Raciocínio",
  citation: "Citação",
  image: "Imagem",
};

const levelKinds: Record<string, string> = { support: "Suporte", resistance: "Resistência" };

/** The label of value, or value itself when it has none. */
function label(labels: Record<string, string>, value: string): string {
  return labels[value] ?? value;
}

export const decisionActionLabel = (action: string | undefined) =>
  action ? label(actions, action) : "—";
export const decisionOutcomeLabel = (outcome: string) => label(outcomes, outcome);
export const noteKindLabel = (kind: string) => label(noteKinds, kind);
export const levelKindLabel = (kind: string) => label(levelKinds, kind);

/** Why the model was asked, with the channel of the reanalysis it answers. */
export function decisionTriggerLabel(decision: Decision): string {
  const trigger = label(triggers, decision.trigger);
  if (!decision.requested_by) return trigger;
  return `${trigger} (via ${label(channels, decision.requested_by)})`;
}

/** The confidence of a decision, from 0 to 1, in %, eg: 72%. */
export function formatConfidence(confidence: number): string {
  return `${Math.round(confidence * 100)}%`;
}

/** The side of the action, for its color: buy, sell or none. */
export function decisionTone(action: string | undefined): "buy" | "sell" | "" {
  if (action === "ENTER_LONG") return "buy";
  if (action === "ENTER_SHORT" || action === "EXIT") return "sell";
  return "";
}

/**
 * How the reanalysis stands on the dashboard: enabled; disabled, the control
 * panel of the dashboard off, eg: an agent whose trade panel is off in the settings;
 * unavailable, the strategy can't be asked again.
 */
export type ReanalysisState = "enabled" | "disabled" | "unavailable";

/** The state of the reanalysis on the dashboard, from its controls. */
export function reanalysisState(controls: ControlsResponse | null): ReanalysisState {
  if (controls?.reanalysis) return "enabled";
  return controls?.enabled ? "unavailable" : "disabled";
}

/** The label of the button of the reanalysis in state, and its tooltip. */
export function reanalysisButton(state: ReanalysisState): { label: string; title: string } {
  switch (state) {
    case "enabled":
      return { label: "Reanalisar agora", title: "Pedir uma nova análise ao LLM agora" };
    case "disabled":
      return {
        label: "Reanalisar agora (DESATIVADO)",
        title:
          "Controles do painel desativados: ative o painel de trading do agente em Configurações",
      };
    default:
      return {
        label: "Reanalisar agora (INDISPONÍVEL)",
        title: "A estratégia do agente não aceita reanálise",
      };
  }
}

/** The media types the dashboard shows as images; the others are downloads. */
const imageTypes = ["image/png", "image/jpeg", "image/gif", "image/webp"];

export const isImage = (mediaType: string) => imageTypes.includes(mediaType.toLowerCase());

/**
 * The decisions shown at most, the newest: the most the API answers, see
 * maxDecisions in ui/llm.go. The panel pages them.
 */
export const DECISIONS = 500;

/**
 * The decisions of pair with the one of the event: newest first, in place of
 * its previous version (the same decision sent again once its orders are
 * linked), DECISIONS at most.
 */
export function applyDecisionEvent(
  list: Decision[],
  event: DecisionEvent,
  pair: string | null,
): Decision[] {
  if (event.pair !== pair) return list;
  const next = [event.decision, ...list.filter((d) => d.id !== event.decision.id)];
  next.sort((a, b) => b.decided_at - a.decided_at || (a.id < b.id ? 1 : a.id > b.id ? -1 : 0));
  return next.slice(0, DECISIONS);
}

/**
 * The snapshot with the orders the decision placed linked to it, as the API
 * sends them once it knows: an order keeps the decision that placed it.
 */
export function linkOrders(snapshot: Snapshot, decision: Decision): Snapshot {
  if (decision.pair !== snapshot.pair || decision.order_ids.length === 0) return snapshot;
  let changed = false;
  const orders = snapshot.orders.map((order) => {
    if (order.decision_id || !decision.order_ids.includes(order.id)) return order;
    changed = true;
    return { ...order, decision_id: decision.id };
  });
  return changed ? { ...snapshot, orders } : snapshot;
}
