// The card of the news of the dashboard: the releases of the economic
// calendar of the main currencies in the period the user picks, filtered by
// their impact, both kept in the browser (localStorage); the days and the
// times by the clock of the browser.

import { MaxNewsCalendarDays, type NewsCalendarResponse, type NewsEvent } from "../api/types";
import { browserStorage } from "./layout";

/** The currencies of the card: the main ones, whatever the agents trade. */
export const MAJOR_CURRENCIES = [
  "USD",
  "EUR",
  "GBP",
  "JPY",
  "CHF",
  "CAD",
  "AUD",
  "NZD",
  "CNY",
] as const;

/** An impact of the filter of the card; the releases of no impact (eg: a holiday) go with the low ones. */
export type NewsImpact = "low" | "medium" | "high";

/** The impacts of the filter, from the lowest, with their labels. */
export const NEWS_IMPACTS: readonly { impact: NewsImpact; label: string }[] = [
  { impact: "low", label: "Baixo" },
  { impact: "medium", label: "Médio" },
  { impact: "high", label: "Alto" },
];

/** The impact of the filter of a release, by its importance. */
export function impactOf(importance: string): NewsImpact {
  return importance === "high" ? "high" : importance === "medium" ? "medium" : "low";
}

/** The label of the importance of a release, eg: Alto; Sem impacto for none. */
export function importanceLabel(importance: string): string {
  switch (importance) {
    case "high":
      return "Alto";
    case "medium":
      return "Médio";
    case "low":
      return "Baixo";
  }
  return "Sem impacto";
}

/** How many of the 3 bars of the impact of a release are lit: 0 for none. */
export function importanceLevel(importance: string): number {
  return { low: 1, medium: 2, high: 3 }[importance] ?? 0;
}

/** The kinds of the periods of the card. */
export type NewsPeriodKind =
  | "today"
  | "tomorrow"
  | "two"
  | "next24"
  | "week"
  | "nextweek"
  | "custom";

/** A period of the card: of a custom one, its first and last days, eg: 2026-11-13, both counted. */
export interface NewsPeriod {
  kind: NewsPeriodKind;
  from?: string;
  to?: string;
}

/** The labels of the kinds of the periods, in the order of the selector. */
export const NEWS_PERIOD_NAMES: Record<NewsPeriodKind, string> = {
  today: "Hoje",
  tomorrow: "Amanhã",
  two: "Hoje e amanhã",
  next24: "Próximas 24 h",
  week: "Esta semana",
  nextweek: "Próxima semana",
  custom: "Personalizado",
};

/** The period of the card without one saved. */
export const DEFAULT_NEWS_PERIOD: NewsPeriod = { kind: "two" };

/** The key of the period in localStorage. */
export const NEWS_PERIOD_KEY = "xtrader.news.period";

/** The key of the impacts of the filter in localStorage. */
export const NEWS_IMPACTS_KEY = "xtrader.news.impacts";

const dayPattern = /^(\d{4})-(\d{2})-(\d{2})$/;

/** The start of the day of text (eg: 2026-11-13) by the clock of the browser, null for something else. */
export function dayStart(text: string): Date | null {
  const match = dayPattern.exec(text);
  if (!match) return null;
  const [, y, m, d] = match.map(Number) as [number, number, number, number];
  const day = new Date(y, m - 1, d);
  return day.getFullYear() === y && day.getMonth() === m - 1 && day.getDate() === d ? day : null;
}

/** The day of date, eg: 2026-11-13, by the clock of the browser. */
export function dayOf(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

/** The start of the day of date, days after it (before, negative). */
function shifted(date: Date, days: number): Date {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate() + days);
}

/** Why the days of a custom period are refused, null when they are taken. */
export function customNewsError(from: string, to: string): string | null {
  const first = dayStart(from);
  const last = dayStart(to);
  if (!first || !last) return "Escolha o primeiro e o último dia.";
  if (last < first) return "O último dia vem depois do primeiro.";
  if (
    Math.round((shifted(last, 1).getTime() - first.getTime()) / 86_400_000) > MaxNewsCalendarDays
  ) {
    return `O período vai até ${MaxNewsCalendarDays} dias.`;
  }
  return null;
}

/** The range of period at now, in Unix seconds: from, until before to; null for a custom one refused. */
export function newsRange(period: NewsPeriod, now: Date): { from: number; to: number } | null {
  const today = shifted(now, 0);
  const seconds = (date: Date) => Math.floor(date.getTime() / 1000);
  const monday = shifted(today, -((today.getDay() + 6) % 7));
  switch (period.kind) {
    case "today":
      return { from: seconds(today), to: seconds(shifted(today, 1)) };
    case "tomorrow":
      return { from: seconds(shifted(today, 1)), to: seconds(shifted(today, 2)) };
    case "two":
      return { from: seconds(today), to: seconds(shifted(today, 2)) };
    case "next24":
      return { from: seconds(now), to: seconds(now) + 86_400 };
    case "week":
      return { from: seconds(monday), to: seconds(shifted(monday, 7)) };
    case "nextweek":
      return { from: seconds(shifted(monday, 7)), to: seconds(shifted(monday, 14)) };
    case "custom": {
      if (!period.from || !period.to || customNewsError(period.from, period.to)) return null;
      const first = dayStart(period.from) as Date;
      const last = dayStart(period.to) as Date;
      return { from: seconds(first), to: seconds(shifted(last, 1)) };
    }
  }
}

/** The label of period for the selector, eg: Hoje e amanhã; a custom one by its days, eg: 13/11 a 20/11. */
export function newsPeriodLabel(period: NewsPeriod): string {
  if (period.kind !== "custom") return NEWS_PERIOD_NAMES[period.kind];
  const first = period.from ? dayStart(period.from) : null;
  const last = period.to ? dayStart(period.to) : null;
  if (!first || !last) return NEWS_PERIOD_NAMES.custom;
  const short = (d: Date) =>
    `${String(d.getDate()).padStart(2, "0")}/${String(d.getMonth() + 1).padStart(2, "0")}`;
  return first.getTime() === last.getTime() ? short(first) : `${short(first)} a ${short(last)}`;
}

/** The period read back from what was saved, null for something else. */
export function normalizeNewsPeriod(value: unknown): NewsPeriod | null {
  if (typeof value !== "object" || value === null) return null;
  const { kind, from, to } = value as Record<string, unknown>;
  if (kind === "custom") {
    return typeof from === "string" && typeof to === "string" && customNewsError(from, to) === null
      ? { kind, from, to }
      : null;
  }
  return typeof kind === "string" && kind in NEWS_PERIOD_NAMES
    ? { kind: kind as NewsPeriodKind }
    : null;
}

/** The impacts of the filter read back from what was saved, in their order; null for something else or none. */
export function normalizeNewsImpacts(value: unknown): NewsImpact[] | null {
  if (!Array.isArray(value)) return null;
  const impacts = NEWS_IMPACTS.map((i) => i.impact).filter((impact) => value.includes(impact));
  return impacts.length > 0 ? impacts : null;
}

/** What was saved in storage under key, read by normalize; fallback without a valid one. */
function load<T>(
  key: string,
  normalize: (value: unknown) => T | null,
  fallback: T,
  storage: Storage | null,
): T {
  try {
    const saved = storage?.getItem(key);
    return (saved && normalize(JSON.parse(saved))) || fallback;
  } catch {
    return fallback; // unreadable, or not JSON
  }
}

/** Saves value in storage under key, when it can be reached. */
function save(key: string, value: unknown, storage: Storage | null) {
  try {
    storage?.setItem(key, JSON.stringify(value));
  } catch {
    // full or blocked: kept while the page is open
  }
}

/** The period saved, the default one without a valid one. */
export function loadNewsPeriod(storage: Storage | null = browserStorage()): NewsPeriod {
  return load(NEWS_PERIOD_KEY, normalizeNewsPeriod, DEFAULT_NEWS_PERIOD, storage);
}

export function saveNewsPeriod(period: NewsPeriod, storage: Storage | null = browserStorage()) {
  save(NEWS_PERIOD_KEY, period, storage);
}

/** The impacts of the filter saved, all of them without a valid one. */
export function loadNewsImpacts(storage: Storage | null = browserStorage()): NewsImpact[] {
  return load(
    NEWS_IMPACTS_KEY,
    normalizeNewsImpacts,
    NEWS_IMPACTS.map((i) => i.impact),
    storage,
  );
}

export function saveNewsImpacts(impacts: NewsImpact[], storage: Storage | null = browserStorage()) {
  save(NEWS_IMPACTS_KEY, impacts, storage);
}

/** The impacts of the filter with impact turned on or off: never none, the last one kept. */
export function toggleImpact(impacts: NewsImpact[], impact: NewsImpact): NewsImpact[] {
  if (impacts.includes(impact)) {
    return impacts.length > 1 ? impacts.filter((i) => i !== impact) : impacts;
  }
  return NEWS_IMPACTS.map((i) => i.impact).filter((i) => i === impact || impacts.includes(i));
}

/**
 * The day of a release by the clock of the browser: its time, or for one of a
 * day (eg: a holiday), whose time is the start of its day on the trade server
 * (UTC), the start of that date: 13/11 at 00:00 UTC is 13/11, not the evening
 * of 12/11 in Belém.
 */
export function newsDate(event: NewsEvent): Date {
  const at = new Date(event.at * 1000);
  return event.timed ? at : new Date(at.getUTCFullYear(), at.getUTCMonth(), at.getUTCDate());
}

/**
 * The releases of events of the impacts of the filter, in their order, within
 * the range read (from, until before to, in Unix seconds): the ones of a day
 * by their date, a day out of the period left out even when its hours in UTC
 * overlap it.
 */
export function filterNews(
  events: NewsEvent[],
  impacts: NewsImpact[],
  range?: { from: number; to: number },
): NewsEvent[] {
  return events.filter((e) => {
    if (!impacts.includes(impactOf(e.importance))) return false;
    if (!range || e.timed) return true;
    const day = newsDate(e).getTime() / 1000;
    return day >= range.from - 86_399 && day < range.to; // a day starting within the period, or the one it starts in
  });
}

/** The releases of a day of the card, and its label, eg: Hoje, sex. 13/11. */
export interface NewsDay {
  day: string;
  label: string;
  events: NewsEvent[];
}

const weekdays = ["dom.", "seg.", "ter.", "qua.", "qui.", "sex.", "sáb."];

/** The label of the day of date at now, eg: Hoje, sex. 13/11; Amanhã…; Ontem…; else the weekday and the date. */
export function newsDayLabel(date: Date, now: Date): string {
  const offset = Math.round((shifted(date, 0).getTime() - shifted(now, 0).getTime()) / 86_400_000);
  const day = `${weekdays[date.getDay()]} ${String(date.getDate()).padStart(2, "0")}/${String(
    date.getMonth() + 1,
  ).padStart(2, "0")}`;
  const near = { [-1]: "Ontem", 0: "Hoje", 1: "Amanhã" }[offset];
  return near ? `${near}, ${day}` : day;
}

/** The releases of events by their day, by the clock of the browser: the ones of a day first in it, then by time. */
export function newsDays(events: NewsEvent[], now: Date): NewsDay[] {
  const days: NewsDay[] = [];
  const sorted = events
    .map((event) => ({ event, day: dayOf(newsDate(event)) }))
    .sort(
      (a, b) =>
        a.day.localeCompare(b.day) ||
        Number(a.event.timed) - Number(b.event.timed) ||
        a.event.at - b.event.at,
    );
  for (const { event, day } of sorted) {
    const date = newsDate(event);
    let last = days[days.length - 1];
    if (!last || last.day !== day) {
      last = { day, label: newsDayLabel(date, now), events: [] };
      days.push(last);
    }
    last.events.push(event);
  }
  return days;
}

/** The time of a release, eg: 10:30; Dia todo for one of a day. */
export function newsTime(event: NewsEvent): string {
  if (!event.timed) return "Dia todo";
  const date = new Date(event.at * 1000);
  return `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
}

const valueFormat = new Intl.NumberFormat("pt-BR", { maximumFractionDigits: 3 });

/** A value of a release with its unit, eg: 150K, 3,2%; null for none. */
export function newsValue(value: number | undefined, unit: string | undefined): string | null {
  if (value === undefined || value === null || !Number.isFinite(value)) return null;
  const suffix = (unit ?? "").trim();
  return `${valueFormat.format(value)}${suffix}`;
}

/** Whether event is over at now: its time passed, or its day for one of a day. */
export function newsPast(event: NewsEvent, now: Date): boolean {
  const end = event.timed ? event.at * 1000 : shifted(newsDate(event), 1).getTime();
  return end <= now.getTime();
}

/**
 * The index of the next release of events at now: the first timed one not
 * over, else the first one of a day not over (a holiday of today lasts until
 * midnight, the releases of the hours of today next before it); -1 for none.
 */
export function nextNews(events: NewsEvent[], now: Date): number {
  const timed = events.findIndex((e) => e.timed && !newsPast(e, now));
  return timed >= 0 ? timed : events.findIndex((e) => !newsPast(e, now));
}

/** Whether event blocks the entries now, in the window of the news around it. */
export function blockingNow(event: NewsEvent, response: NewsCalendarResponse, now: Date): boolean {
  const window = response.window;
  if (!event.blocks || !window || !event.timed) return false;
  const at = event.at * 1000;
  return now.getTime() >= at - window.before * 60_000 && now.getTime() < at + window.after * 60_000;
}

/** What the card tells for a calendar not read, by its status. */
export function newsStatusMessage(status: string): string {
  switch (status) {
    case "disabled":
      return "As notícias estão desligadas em Configurações › Notícias.";
    case "missing_server":
      return "Falta o endereço do servidor MCP em Configurações › Notícias.";
    case "missing_token":
      return "Falta o token do servidor MCP em Configurações › Notícias.";
    case "unauthorized":
      return "O servidor MCP recusou o token: troque-o em Configurações › Notícias.";
  }
  return "O calendário econômico não pôde ser lido agora. Nova tentativa em instantes.";
}

/** The window of the news block, in a sentence, eg: Entradas bloqueadas de 10 min antes a 10 min depois das de impacto alto. */
export function newsWindowText(response: NewsCalendarResponse): string | null {
  const window = response.window;
  if (!window) return null;
  const which =
    window.importance === "high"
      ? "das de impacto alto"
      : window.importance === "medium"
        ? "das de impacto médio ou alto"
        : "de cada divulgação";
  return `Os agentes não abrem posição de ${window.before} min antes a ${window.after} min depois ${which}.`;
}
