// The decisions of the language model of a strategy (see Decision): their
// Portuguese labels, and how the events of the API update what is shown.

import type { ControlsResponse, Decision, DecisionEvent, Drawing, Snapshot } from "../api/types";
import { isSupplyDemand, zoneStateLabel } from "./analysis";
import { formatChartPrice, formatTime } from "./format";

const actions: Record<string, string> = {
  WAIT: "Aguardar",
  ENTER_LONG: "Comprar",
  ENTER_SHORT: "Vender",
  HOLD: "Manter",
  EXIT: "Sair",
  MODIFY_SL: "Mover stop loss",
  MODIFY_TP: "Mover take profit",
  BUY_LIMIT: "Compra limite",
  SELL_LIMIT: "Venda limite",
  BUY_STOP: "Compra stop",
  SELL_STOP: "Venda stop",
  MODIFY_PENDING: "Alterar ordem pendente",
  CANCEL_PENDING: "Cancelar ordem pendente",
};

const outcomes: Record<string, string> = {
  applied: "Aplicada",
  ignored: "Ignorada: confiança baixa",
  refused: "Recusada pelas regras",
  failed: "Sem decisão",
  skipped: "Não consultado",
};

// why an analysis didn't ask the model (see the Skip constants of
// agents/llm/decisions.go), the code in the reason of a skipped one
const skips: Record<string, string> = {
  position: "Não foi possível ler a posição na corretora",
  close_retry: "Tentando de novo um fechamento que falhou",
  protection: "A corretora não aceita stop loss e take profit na ordem",
  daily_limit: "Entradas bloqueadas pelo limite diário do ativo",
  news: "Entradas bloqueadas perto de notícia de alta importância",
  size: "Sem lote para o ativo ou saldo insuficiente",
  spread: "Spread acima do máximo permitido",
  setup: "Sem tendência para o setup de pullback",
  direction: "A direção configurada não permite entrada",
  higher_trend: "Sem tendência definida nos timeframes maiores (exigida para entrar)",
  against_trend: "Entradas permitidas só contra a tendência dos timeframes maiores",
  rate_limited: "Limite de requisições do provedor do LLM atingido",
  pending_cancel: "O cancelamento da ordem pendente aguarda a confirmação da corretora",
};

const triggers: Record<string, string> = {
  candle: "Fechamento do candle",
  start: "Início do agente",
  reanalysis: "Reanálise",
  interval: "Intervalo de consulta",
};

const channels: Record<string, string> = { web: "painel", telegram: "Telegram" };

const noteKinds: Record<string, string> = {
  text: "Texto",
  reasoning: "Raciocínio",
  citation: "Citação",
  image: "Imagem",
};

const levelKinds: Record<string, string> = { support: "Suporte", resistance: "Resistência" };

const drawingKinds: Record<string, string> = {
  zone: "Zona",
  supply: "Oferta",
  demand: "Demanda",
  level: "Nível",
  marker: "Marcador",
};

const biases: Record<string, string> = { bullish: "alta", bearish: "baixa", neutral: "neutro" };

/** The label of value, or value itself when it has none. */
function label(labels: Record<string, string>, value: string): string {
  return labels[value] ?? value;
}

export const decisionActionLabel = (action: string | undefined) =>
  action ? label(actions, action) : "—";
export const decisionOutcomeLabel = (outcome: string) => label(outcomes, outcome);
export const noteKindLabel = (kind: string) => label(noteKinds, kind);
export const levelKindLabel = (kind: string) => label(levelKinds, kind);
export const drawingKindLabel = (kind: string) => label(drawingKinds, kind);
export const biasLabel = (bias: string) => label(biases, bias);

/**
 * Where a drawing of a decision is: the range of a zone (of supply or demand
 * too, with its state) or the price of a level or a marker, from its candle,
 * to the candle it ended at, if any.
 */
export function drawingPlace(drawing: Drawing): string {
  const price =
    drawing.kind === "zone" || isSupplyDemand(drawing)
      ? `${formatChartPrice(drawing.low ?? 0)} – ${formatChartPrice(drawing.high ?? 0)}`
      : formatChartPrice(drawing.price ?? 0);
  const state = drawing.state ? `, ${zoneStateLabel(drawing.state)}` : "";
  const end = drawing.to != null ? ` até ${formatTime(drawing.to)}` : "";
  return `${price}${state}, desde ${formatTime(drawing.from)}${end}`;
}

/** Whether the analysis didn't ask the model, see decisionReason. */
export const isSkipped = (decision: Decision) => decision.outcome === "skipped";

/**
 * The justification of a decision: the one of the model, or else its error;
 * why the model wasn't asked for a skipped analysis, in Portuguese, its
 * detail kept in the error.
 */
export function decisionReason(decision: Decision): string {
  if (isSkipped(decision)) {
    return skips[decision.reason ?? ""] ?? decision.error ?? decision.reason ?? "—";
  }
  return decision.reason || decision.error || "—";
}

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
  if (action === "ENTER_LONG" || action === "BUY_LIMIT" || action === "BUY_STOP") return "buy";
  if (
    action === "ENTER_SHORT" ||
    action === "EXIT" ||
    action === "SELL_LIMIT" ||
    action === "SELL_STOP"
  ) {
    return "sell";
  }
  return "";
}

/**
 * How the reanalysis stands on the dashboard: enabled; disabled, switched off
 * in the settings of the agent (Permitir reanálise), whatever its trade panel;
 * unavailable, the strategy can't be asked again; unknown, the controls of the
 * agent not read yet.
 */
export type ReanalysisState = "enabled" | "disabled" | "unavailable" | "unknown";

/** The state of the reanalysis on the dashboard, from its controls. */
export function reanalysisState(controls: ControlsResponse | null): ReanalysisState {
  if (!controls) return "unknown";
  if (controls.reanalysis) return "enabled";
  return controls.reanalysis_disabled ? "disabled" : "unavailable";
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
          "Ligue “Permitir reanálise” em Configurações › Agentes (independe do “Painel de trading”)",
      };
    case "unknown":
      return { label: "Reanalisar agora", title: "Carregando os controles do agente…" };
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
 * The snapshot with the analysis of the event, drawn on the chart: the one of
 * its pair once the decision is kept, see DecisionEvent; the snapshot as it is
 * when the analysis didn't change, eg: an analysis skipped.
 */
export function applyAnalysisEvent(snapshot: Snapshot, event: DecisionEvent): Snapshot {
  if (event.pair !== snapshot.pair || !event.analysis) return snapshot;
  if (JSON.stringify(event.analysis) === JSON.stringify(snapshot.analysis)) return snapshot;
  return { ...snapshot, analysis: event.analysis };
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
