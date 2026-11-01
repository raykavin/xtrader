// The period of the closed profit of all the agents on the dashboard: today,
// this week (from Monday), this month, or the days the user picks, by the
// clock of the browser; kept in the browser (localStorage), today without a
// valid one saved.

import type { AgentClosedProfit, ClosedProfitResponse, CurrencyAmount, Point } from "../api/types";
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

/** The period saved in storage under key, today without a valid one. */
export function loadClosedPeriod(
  storage: Storage | null = browserStorage(),
  key = CLOSED_PERIOD_KEY,
): ClosedPeriod {
  try {
    const saved = storage?.getItem(key);
    return (saved && normalizeClosedPeriod(JSON.parse(saved))) || { kind: "today" };
  } catch {
    return { kind: "today" }; // unreadable, or not JSON
  }
}

/** Saves the period in storage under key, when it can be reached. */
export function saveClosedPeriod(
  period: ClosedPeriod,
  storage: Storage | null = browserStorage(),
  key = CLOSED_PERIOD_KEY,
) {
  try {
    storage?.setItem(key, JSON.stringify(period));
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

/** The evolution of the closed profit of a currency, as the chart of the card draws it. */
export interface ClosedChart {
  currency: string;
  /** From 0 at the start of the range, the running total after each second with an exit, held until now or the end of the range. */
  points: Point[];
  /** The seconds with an exit, the points of the series. */
  exits: number;
  /** The total of the currency, the last running total. */
  total: number;
  /** The profit open now in the currency, with open positions in a range not over. */
  open?: number;
  /** The closed total plus the profit open, at now: where the line goes if the positions closed now. */
  live?: Point;
}

/**
 * The evolution of the closed profit of each currency of a read, its series
 * (see ClosedProfitResponse), as the chart of the card draws it: from 0 at the
 * start of its range, the running total after each exit, held until now (at
 * now, in Unix seconds) or the end of the range; none for a currency without
 * an exit. With the profit open now of each currency (open, the totals of the
 * open positions) and the range not over, the chart of a currency with
 * positions open also ends at now on the closed total plus that profit (live),
 * in real time as the open positions are read again: a currency open without
 * an exit yet gets a chart of its own, its closed total 0.
 */
export function closedCharts(
  read: ClosedProfitResponse,
  now: number,
  open?: CurrencyAmount[],
): ClosedChart[] {
  const charts: ClosedChart[] = [];
  const ongoing = now < read.to;
  const opened = new Map((ongoing ? (open ?? []) : []).map((a) => [a.currency, a.amount]));
  for (const series of read.series ?? []) {
    const last = series.points[series.points.length - 1];
    if (!last) continue;
    const points: Point[] = [{ time: read.from, value: 0 }];
    for (const point of series.points) points.push({ time: point.at, value: point.amount });
    const end = Math.max(Math.min(now, read.to), last.at);
    if (end > last.at) points.push({ time: end, value: last.amount });
    const chart: ClosedChart = {
      currency: series.currency,
      points,
      exits: series.points.length,
      total: last.amount,
    };
    const amount = opened.get(series.currency);
    if (amount !== undefined) {
      chart.open = amount;
      chart.live = { time: Math.max(now, last.at), value: last.amount + amount };
      opened.delete(series.currency);
    }
    charts.push(chart);
  }
  for (const [currency, amount] of opened) {
    if (now <= read.from) continue;
    charts.push({
      currency,
      points: [
        { time: read.from, value: 0 },
        { time: now, value: 0 },
      ],
      exits: 0,
      total: 0,
      open: amount,
      live: { time: now, value: amount },
    });
  }
  return charts;
}

/** A time of the browser, eg: 14:05. */
function clockOf(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/**
 * The ends of the axis of time of the chart of a range at now (Unix seconds),
 * by the clock of the browser: the hours of a day, eg: 00:00 to agora (or
 * 24:00 of a day gone), the days of a longer one, eg: 13/10 to hoje (or its
 * last day).
 */
export function closedAxis(range: ClosedRange, now: number): { start: string; end: string } {
  const from = new Date(range.from * 1000);
  const last = new Date((range.to - 1) * 1000); // the last second of the range
  const ongoing = now < range.to;
  if (from.toDateString() === last.toDateString()) {
    return { start: clockOf(from), end: ongoing ? "agora" : "24:00" };
  }
  const year = from.getFullYear() !== last.getFullYear();
  return { start: shortDay(from, year), end: ongoing ? "hoje" : shortDay(last, year) };
}
