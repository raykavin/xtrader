// The period of the closed profit of all the agents on the dashboard: today,
// this week (from Monday), this month, or the days the user picks, by the
// clock of the browser; kept in the browser (localStorage), today without a
// valid one saved.

import type { AgentClosedProfit, ClosedProfitResponse, CurrencyAmount } from "../api/types";
import { browserStorage } from "./layout";

/** The kinds of the periods of the closed profit. */
export type ClosedPeriodKind = "today" | "week" | "month" | "custom";

/** A period of the closed profit: of a custom one, its first and last days, eg: 2026-10-12, both counted. */
export interface ClosedPeriod {
  kind: ClosedPeriodKind;
  from?: string;
  to?: string;
}

/** A range of the closed profit, in Unix seconds: from, until before to. */
export interface ClosedRange {
  from: number;
  to: number;
}

/** The key of the period in localStorage. */
export const CLOSED_PERIOD_KEY = "xtrader.closedPL.period";

/** The days of a custom period at most, as the controller takes them. */
export const MAX_CLOSED_DAYS = 400;

/** The labels of the kinds of the periods, as the card shows them. */
export const closedPeriodNames: Record<ClosedPeriodKind, string> = {
  today: "Hoje",
  week: "Semana",
  month: "Mês",
  custom: "Personalizado",
};

const dayPattern = /^(\d{4})-(\d{2})-(\d{2})$/;

/** The start of a day of the form 2026-10-12, by the clock of the browser; null for another text or no day. */
export function dayStart(day: string | undefined): Date | null {
  const match = day ? dayPattern.exec(day) : null;
  if (!match) return null;
  const [year, month, date] = [Number(match[1]), Number(match[2]), Number(match[3])];
  const start = new Date(year, month - 1, date);
  return start.getFullYear() === year && start.getMonth() === month - 1 && start.getDate() === date
    ? start
    : null;
}

/** The day of a date by the clock of the browser, eg: 2026-10-12, as a date input takes it. */
export function dayOf(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

/** Why the days of a custom period can't be taken, in the words of the dashboard; null when they can. */
export function customPeriodError(from: string | undefined, to: string | undefined): string | null {
  const first = dayStart(from);
  const last = dayStart(to);
  if (!first || !last) return "Escolha a data inicial e a final.";
  if (last < first) return "A data final é anterior à inicial.";
  const days = Math.round((last.getTime() - first.getTime()) / 86_400_000) + 1;
  if (days > MAX_CLOSED_DAYS) return `Escolha até ${MAX_CLOSED_DAYS} dias.`;
  return null;
}

/** The days after date, at its start. */
function addDays(date: Date, days: number): Date {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate() + days);
}

/**
 * The range of a period at now, by the clock of the browser: today, this week
 * from Monday, this month, or the days of a custom one, each from its start
 * until the start of the next one.
 */
export function closedRange(period: ClosedPeriod, now: Date = new Date()): ClosedRange {
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  let from = today;
  let to = addDays(today, 1);
  switch (period.kind) {
    case "week":
      from = addDays(today, -((today.getDay() + 6) % 7)); // Monday
      to = addDays(from, 7);
      break;
    case "month":
      from = new Date(today.getFullYear(), today.getMonth(), 1);
      to = new Date(today.getFullYear(), today.getMonth() + 1, 1);
      break;
    case "custom": {
      const first = dayStart(period.from);
      const last = dayStart(period.to);
      if (first && last && customPeriodError(period.from, period.to) === null) {
        from = first;
        to = addDays(last, 1);
      }
      break;
    }
  }
  return { from: Math.floor(from.getTime() / 1000), to: Math.floor(to.getTime() / 1000) };
}

/** A day as the card shows it, eg: 12/10, its year told when asked, eg: 12/10/2026. */
function shortDay(date: Date, year: boolean): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(date.getDate())}/${pad(date.getMonth() + 1)}${year ? `/${date.getFullYear()}` : ""}`;
}

/** The label of a period, eg: Hoje, Mês, or the days of a custom one, eg: 12/10 a 14/10. */
export function closedPeriodLabel(period: ClosedPeriod, now: Date = new Date()): string {
  if (period.kind !== "custom") return closedPeriodNames[period.kind];
  const first = dayStart(period.from);
  const last = dayStart(period.to);
  if (!first || !last) return closedPeriodNames.custom;
  const year =
    first.getFullYear() !== last.getFullYear() || first.getFullYear() !== now.getFullYear();
  if (first.getTime() === last.getTime()) return shortDay(first, year);
  return `${shortDay(first, year)} a ${shortDay(last, year)}`;
}

/** The period read back from what was saved, null for something else. */
export function normalizeClosedPeriod(value: unknown): ClosedPeriod | null {
  if (typeof value !== "object" || value === null) return null;
  const { kind, from, to } = value as Record<string, unknown>;
  if (kind === "today" || kind === "week" || kind === "month") return { kind };
  if (kind === "custom" && typeof from === "string" && typeof to === "string") {
    return customPeriodError(from, to) === null ? { kind, from, to } : null;
  }
  return null;
}

/** The period saved in storage, today without a valid one. */
export function loadClosedPeriod(storage: Storage | null = browserStorage()): ClosedPeriod {
  try {
    const saved = storage?.getItem(CLOSED_PERIOD_KEY);
    return (saved && normalizeClosedPeriod(JSON.parse(saved))) || { kind: "today" };
  } catch {
    return { kind: "today" }; // unreadable, or not JSON
  }
}

/** Saves the period in storage, when it can be reached. */
export function saveClosedPeriod(period: ClosedPeriod, storage: Storage | null = browserStorage()) {
  try {
    storage?.setItem(CLOSED_PERIOD_KEY, JSON.stringify(period));
  } catch {
    // full or blocked: the period lasts while the page is open
  }
}

/** The count of the positions closed and of their agents, eg: 3 posições fechadas em 2 agentes. */
export function closedPLSummary(read: ClosedProfitResponse): string {
  const positions = read.agents.reduce((sum, agent) => sum + agent.positions, 0);
  if (positions === 0) return "Sem posições fechadas";
  const agents = new Set(read.agents.map((a) => a.agent)).size;
  const count = positions === 1 ? "1 posição fechada" : `${positions} posições fechadas`;
  return `${count} em ${agents === 1 ? "1 agente" : `${agents} agentes`}`;
}

/** The closed profit of an agent by the currency of its account, none converted. */
export function closedAmountsOf(agent: string, read: ClosedProfitResponse): CurrencyAmount[] {
  return read.agents
    .filter((a: AgentClosedProfit) => a.agent === agent)
    .map((a) => ({ currency: a.currency, amount: a.amount }));
}
