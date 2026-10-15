// The labels and the rules of the supervisor of the agents on the dashboard:
// its actions, their statuses and scopes, its analyses and its settings.

import type {
  AgentSupervisionInfo,
  SupervisorActionInfo,
  SupervisorMessageInfo,
  SupervisorResultItem,
} from "../api/types";
import { formatProfit } from "./format";

/** The tone of a status, as the chips of the dashboard show it. */
export type SupervisorTone = "on" | "off" | "warn" | "error" | "info";

/** A label and its tone. */
export interface ToneLabel {
  text: string;
  tone: SupervisorTone;
}

/** The action, in words, eg: Parar o agente quando zerado. */
export function actionLabel(action: string, mode?: string): string {
  switch (action) {
    case "block_entries":
      return "Bloquear novas entradas";
    case "allow_entries":
      return "Liberar as entradas";
    case "stop_agent":
      return mode === "now" ? "Parar o agente agora" : "Parar o agente quando zerado";
    case "cancel_pending":
      return "Cancelar ordens pendentes de entrada";
    case "close_positions":
      return "Encerrar posições";
  }
  return action;
}

/** What the action does and doesn't, so that the user never confuses them. */
export function actionNote(action: string, mode?: string): string {
  switch (action) {
    case "block_entries":
      return "O agente não abre nem aumenta posições; continua gerenciando as que tem. Nada é fechado nem cancelado.";
    case "allow_entries":
      return "O agente volta a abrir posições conforme a estratégia.";
    case "stop_agent":
      return mode === "now"
        ? "Bloqueia as entradas, cancela as pendentes de entrada e desliga o agente já. As posições ficam abertas, com o stop loss e o take profit na corretora, sem o gerenciamento da estratégia: recusado se alguma posição estiver sem stop loss."
        : "Bloqueia as entradas e cancela as pendentes de entrada agora; o agente continua gerenciando as posições e é desligado quando ficar sem posição e sem ordem pendente. Nenhuma posição é liquidada.";
    case "cancel_pending":
      return "Cancela só as ordens pendentes de entrada; as proteções das posições (stop loss e take profit) ficam.";
    case "close_positions":
      return "Fecha a mercado só as posições dos agentes da aplicação listadas abaixo, nunca operações feitas fora deles.";
  }
  return "";
}

/** The status of an action. */
export function actionStatusLabel(status: string): ToneLabel {
  switch (status) {
    case "proposed":
      return { text: "Aguardando confirmação", tone: "warn" };
    case "executing":
      return { text: "Executando", tone: "info" };
    case "succeeded":
      return { text: "Concluída", tone: "on" };
    case "partial":
      return { text: "Parcial", tone: "warn" };
    case "failed":
      return { text: "Falhou", tone: "error" };
    case "unknown":
      return { text: "Resultado desconhecido", tone: "warn" };
    case "rejected":
      return { text: "Recusada", tone: "off" };
    case "expired":
      return { text: "Expirada", tone: "off" };
  }
  return { text: status, tone: "off" };
}

/** The status of an item of the result of an action. */
export function itemStatusLabel(item: SupervisorResultItem): ToneLabel {
  switch (item.status) {
    case "done":
      return { text: "Feito", tone: "on" };
    case "already":
      return { text: "Nada a fazer", tone: "off" };
    case "pending":
      return { text: "Pendente", tone: "info" };
    case "failed":
      return { text: "Falhou", tone: "error" };
    case "unknown":
      return { text: "Desconhecido", tone: "warn" };
  }
  return { text: item.status, tone: "off" };
}

/** What an item of a result is about, eg: Posição 1234 (XAUUSDm). */
export function itemSubject(item: SupervisorResultItem): string {
  switch (item.kind) {
    case "entries":
      return "Entradas";
    case "stop":
      return "Parada";
    case "position":
      return item.ticket
        ? `Posição ${item.ticket}${item.pair ? ` (${item.pair})` : ""}`
        : `Posição ${item.pair}`;
    case "order":
      return item.ticket ? `Ordem ${item.ticket}${item.pair ? ` (${item.pair})` : ""}` : "Ordem";
    case "orders":
      return "Ordens pendentes";
    case "positions":
      return "Posições";
  }
  return item.kind;
}

/** The category of a finding or of an action. */
export function categoryLabel(category?: string): string {
  switch (category) {
    case "operational":
      return "Operacional";
    case "risk":
      return "Risco";
    case "performance":
      return "Desempenho";
  }
  return category ?? "";
}

/** The severity of a finding. */
export function severityLabel(severity: string): ToneLabel {
  switch (severity) {
    case "critical":
      return { text: "Crítico", tone: "error" };
    case "warning":
      return { text: "Atenção", tone: "warn" };
  }
  return { text: "Informativo", tone: "info" };
}

/** The status of an analysis. */
export function runStatusLabel(status: string): ToneLabel {
  switch (status) {
    case "running":
      return { text: "Em andamento", tone: "info" };
    case "ok":
      return { text: "Concluída", tone: "on" };
    case "failed":
      return { text: "Falhou", tone: "error" };
    case "skipped":
      return { text: "Ignorada", tone: "off" };
  }
  return { text: status, tone: "off" };
}

/** Who asked for an analysis or an action. */
export function originLabel(origin: string, by?: string): string {
  if (origin === "schedule") return "Periódica";
  if (origin === "manual") return by ? `Manual (${by})` : "Manual";
  if (origin === "supervisor") return "Supervisor";
  if (origin === "user") return by ? `Usuário (${by})` : "Usuário";
  return origin;
}

/** The badges of the supervision of an agent: its entries blocked, its stop. */
export function supervisionBadges(info?: AgentSupervisionInfo): (ToneLabel & { title: string })[] {
  if (!info) return [];
  const badges: (ToneLabel & { title: string })[] = [];
  if (info.stop_state === "stopped") {
    badges.push({
      text: "Interrompido pelo supervisor",
      tone: "error",
      title: info.stop_reason ? `Motivo: ${info.stop_reason}` : "Parado pelo supervisor",
    });
  } else if (info.stop_state === "pending_flat") {
    badges.push({
      text: "Parada pendente",
      tone: "warn",
      title: `Desligado quando ficar sem posição e sem pendente. Motivo: ${info.stop_reason ?? "—"}`,
    });
  }
  if (info.entries_blocked) {
    badges.push({
      text: "Entradas bloqueadas",
      tone: "warn",
      title: [
        info.entries_reason ? `Motivo: ${info.entries_reason}` : "",
        info.entries_by ? `Por: ${info.entries_by}` : "",
      ]
        .filter(Boolean)
        .join(" · "),
    });
  }
  return badges;
}

/** The intervals offered for the analyses, in seconds. */
export const intervalOptions = [300, 600, 900, 1800, 3600, 7200, 14400, 43200, 86400];

/** An interval in words, eg: 15 min, 2 h. */
export function formatInterval(seconds: number): string {
  if (seconds < 3600) return `${Math.round(seconds / 60)} min`;
  const hours = seconds / 3600;
  return Number.isInteger(hours) ? `${hours} h` : `${Math.round(seconds / 60)} min`;
}

/** Why an interval of minutes is refused, null when it is taken. */
export function intervalError(minutes: number, min: number, max: number): string | null {
  if (!Number.isFinite(minutes) || !Number.isInteger(minutes))
    return "Informe um número inteiro de minutos.";
  const seconds = minutes * 60;
  if (seconds < min || seconds > max) {
    return `De ${formatInterval(min)} a ${formatInterval(max)}.`;
  }
  return null;
}

/** The scope of an action written out, eg: what a confirmation does and to whom. */
export function scopeText(action: SupervisorActionInfo): string {
  const agents = action.scope.agents;
  const who =
    agents.length === 1
      ? `o agente ${agents[0]}`
      : `${agents.length} agentes (${agents.join(", ")})`;
  const pairs = action.scope.pairs?.length ? ` nos pares ${action.scope.pairs.join(", ")}` : "";
  switch (action.action) {
    case "close_positions": {
      const positions = action.scope.positions ?? [];
      const owners = [...new Set(positions.map((p) => p.agent))];
      const parts = [
        `Fechar ${positions.length} posição(ões) de ${owners.length} agente(s)${pairs}, só dos agentes da aplicação.`,
        action.options.cancel_pending
          ? "As pendentes de entrada também são canceladas."
          : "As ordens pendentes não são canceladas.",
        action.options.keep_blocked
          ? "As entradas ficam bloqueadas depois."
          : "As entradas voltam a ser liberadas um candle depois.",
      ];
      const stopped = positions.filter((p) => !p.running && !p.paper);
      if (stopped.length > 0) {
        parts.push(
          `${stopped.length} posição(ões) de agentes que não estão rodando são fechadas direto na conta.`,
        );
      }
      return parts.join(" ");
    }
    case "cancel_pending":
      return `Cancelar ${(action.scope.orders ?? []).length} ordem(ns) pendente(s) de entrada de ${who}${pairs}.`;
  }
  return `${actionLabel(action.action, action.mode)}: ${who}.`;
}

/** The open profit of the positions of a scope, by currency, eg: +12,50 BRL. */
export function scopeProfit(action: SupervisorActionInfo): string {
  const totals = new Map<string, number>();
  for (const position of action.scope.positions ?? []) {
    if (!position.currency) continue;
    totals.set(position.currency, (totals.get(position.currency) ?? 0) + position.profit);
  }
  return [...totals.entries()]
    .map(([currency, amount]) => `${formatProfit(amount)} ${currency}`)
    .join(" · ");
}

/** Whether a proposal still waits for a decision, as of now (Unix seconds). */
export function awaitingDecision(action: SupervisorActionInfo, now: number): boolean {
  return action.status === "proposed" && (!action.expires_at || action.expires_at > now);
}

/** The messages of a conversation merged with new ones, by ID, in their order. */
export function mergeMessages(
  current: SupervisorMessageInfo[],
  added: SupervisorMessageInfo[],
): SupervisorMessageInfo[] {
  const byID = new Map(current.map((m) => [m.id, m]));
  for (const m of added) byID.set(m.id, m);
  return [...byID.values()].sort((a, b) => a.id - b.id);
}

/** The message of a proposal with its action replaced, eg: once confirmed. */
export function withAction(
  messages: SupervisorMessageInfo[],
  action: SupervisorActionInfo,
): SupervisorMessageInfo[] {
  return messages.map((m) => (m.action?.id === action.id ? { ...m, action } : m));
}
