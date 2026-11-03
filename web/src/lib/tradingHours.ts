// The trading hours of all the agents on the dashboard: how they stand, the
// actions of the end of each of their windows, and the form that sets them.

import type {
  TradingHoursEnd,
  TradingHoursSettings,
  TradingHoursStatus,
  TradingWindow,
} from "../api/types";
import { timeIn } from "./dailyLimits";
import { actionStatusLabel } from "./supervisor";

/** A label and its tone, as the chips of the dashboard show them. */
export interface HoursLabel {
  tone: "on" | "off" | "warn" | "error" | "info";
  text: string;
}

/** The number of windows the trading hours take at most, as the controller (agentapi.MaxTradingWindows). */
export const MAX_WINDOWS = 24;

/**
 * How the trading hours stand in words: off; open until the end of the window
 * open; or closed, the entries blocked, until the start of the next window
 * (the next change in the zone of the controller).
 */
export function hoursState(status: TradingHoursStatus): HoursLabel {
  if (!status.enabled) return { tone: "off", text: "Desligado" };
  const next = timeIn(status.next_at, status.zone);
  if (status.blocked) {
    return { tone: "warn", text: `Fora do horário · abre às ${next || status.start}` };
  }
  return { tone: "on", text: `Aberto · fecha às ${next || status.end}` };
}

/** A window in words, eg: 08:00–10:00. */
export function windowLabel(window: Pick<TradingWindow, "start" | "end">): string {
  return `${window.start}–${window.end}`;
}

/** Windows in words, eg: 08:00–10:00, 12:00–14:00; empty for none. */
export function windowsLabel(windows: Pick<TradingWindow, "start" | "end">[]): string {
  return windows.map(windowLabel).join(", ");
}

/**
 * A window with its agents and its assets in words, each named when some, eg:
 * 08:00–10:00 (agentes: highlow, matrend; ativo: XAUUSDm); the window alone
 * for all of them.
 */
export function windowDetails(
  window: Pick<TradingWindow, "start" | "end" | "agents" | "pairs">,
): string {
  const agents = window.agents ?? [];
  const pairs = window.pairs ?? [];
  const parts = [
    agents.length > 0 && `${agents.length === 1 ? "agente" : "agentes"}: ${agents.join(", ")}`,
    pairs.length > 0 && `${pairs.length === 1 ? "ativo" : "ativos"}: ${pairs.join(", ")}`,
  ].filter(Boolean);
  return parts.length > 0 ? `${windowLabel(window)} (${parts.join("; ")})` : windowLabel(window);
}

/** The agents of a window, as its button shows them: all of them for none, the name of one, or how many. */
export function agentsLabel(agents: string[] | undefined): string {
  const [first] = agents ?? [];
  if (!agents || !first) return "Todos os agentes";
  return agents.length === 1 ? first : `${agents.length} agentes`;
}

/** The agents of a window listed, as the title of its button. */
export function agentsTitle(agents: string[] | undefined): string {
  return agents && agents.length > 0
    ? `Operam nesta janela: ${agents.join(", ")}`
    : "Todos os agentes operam nesta janela";
}

/** Whether window lets the agent of name open positions in it: any agent while it names none (agentapi.WindowAllows). */
export function windowAllows(window: Pick<TradingWindow, "agents">, name: string): boolean {
  return !window.agents || window.agents.length === 0 || window.agents.includes(name);
}

/** The agents of agents no window of windows lets open positions, in their order; none without windows. */
export function agentsOutside(
  agents: string[],
  windows: Pick<TradingWindow, "agents">[],
): string[] {
  if (windows.length === 0) return [];
  return agents.filter((name) => !windows.some((w) => windowAllows(w, name)));
}

/** The hours of a single window of before, or of many: the settings, the status, the hours of a batch. */
interface HoursShape {
  windows?: TradingWindow[] | null;
  start?: string;
  end?: string;
  flatten?: boolean;
  cancel_pending?: boolean;
}

/**
 * The windows of hours, as the controller takes them (agentapi.HoursWindows):
 * its windows when there are, else its start to end as its only one (the
 * hours of before, a batch of before), none without them.
 */
export function windowsOf(hours: HoursShape): TradingWindow[] {
  if (hours.windows && hours.windows.length > 0) return hours.windows;
  if (!hours.start && !hours.end) return [];
  return [
    {
      start: hours.start ?? "",
      end: hours.end ?? "",
      flatten: hours.flatten ?? false,
      cancel_pending: hours.cancel_pending ?? false,
    },
  ];
}

/** The windows of the hours in words, eg: 08:00–10:00 (agente: highlow), 12:00–14:00; empty while off. */
export function hoursWindows(status: TradingHoursStatus): string {
  return status.enabled ? windowsOf(status).map(windowDetails).join(", ") : "";
}

/** What the end of a window does, eg: zera as posições e cancela as ordens pendentes. */
export function endActionsText(flatten: boolean, cancelPending: boolean): string {
  if (flatten && cancelPending) return "zera as posições e cancela as ordens pendentes";
  if (flatten) return "zera as posições";
  if (cancelPending) return "cancela as ordens pendentes";
  return "só bloqueia novas entradas";
}

/**
 * How the last end of a window went, eg: Fim da janela 08:00–10:00 às 10:00:
 * posições zeradas · Concluída (Fim às 10:00: … for the ends of before, the
 * window unknown).
 */
export function lastEndLabel(end: TradingHoursEnd, zone: string): HoursLabel {
  const what = [
    end.flatten && "posições zeradas",
    end.cancel_pending && "ordens pendentes canceladas",
  ]
    .filter(Boolean)
    .join(" e ");
  const at = timeIn(end.at, zone);
  const head =
    end.start && end.end
      ? `Fim da janela ${windowLabel({ start: end.start, end: end.end })}`
      : "Fim";
  if (end.status === "executing") {
    return { tone: "info", text: `${head} às ${at}: ${what} · executando` };
  }
  const status = actionStatusLabel(end.status);
  const tone = status.tone === "on" ? "on" : status.tone === "error" ? "error" : "warn";
  return { tone, text: `${head} às ${at}: ${what} · ${status.text}` };
}

/**
 * A window of the form, as typed: its agents (none for all of them), its
 * assets as the text typed (see parsePairs; blank for all of them), and a key
 * that keeps its row while the list changes.
 */
export interface WindowForm extends Omit<TradingWindow, "pairs"> {
  agents: string[];
  pairsText: string;
  key: string;
}

/** The form of the trading hours, as typed: on or off, and the windows in the order of the user. */
export interface HoursForm {
  enabled: boolean;
  windows: WindowForm[];
}

let lastKey = 0;

/** window as a row of the form, with a key of its own. */
export function windowForm({ pairs, ...window }: TradingWindow): WindowForm {
  lastKey += 1;
  return {
    ...window,
    agents: window.agents ?? [],
    pairsText: (pairs ?? []).join(";"),
    key: `w${lastKey}`,
  };
}

/** The assets of text typed as "XAUUSDm;BTCUSDm": split by ;, trimmed, the blanks left out; the case kept. */
export function parsePairs(text: string): string[] {
  return text
    .split(";")
    .map((pair) => pair.trim())
    .filter((pair) => pair !== "");
}

/** windows as the rows of the form. */
export function windowsForm(windows: TradingWindow[]): WindowForm[] {
  return windows.map(windowForm);
}

/**
 * The windows of the form as sent: in the order of the day, by their start,
 * without their keys; their agents and their assets only when some (none for
 * all of them).
 */
export function windowsRequest(windows: WindowForm[]): TradingWindow[] {
  return [...windows]
    .sort((a, b) => (a.start < b.start ? -1 : a.start > b.start ? 1 : 0))
    .map(({ start, end, agents, pairsText, flatten, cancel_pending }) => {
      const pairs = parsePairs(pairsText);
      return {
        start,
        end,
        ...(agents.length > 0 ? { agents: [...agents] } : {}),
        ...(pairs.length > 0 ? { pairs } : {}),
        flatten,
        cancel_pending,
      };
    });
}

/**
 * The settings of form as sent: its windows by their start, and the first of
 * them as the start and the end of the clients of a single window (blank
 * without windows, so the controller never takes them as one).
 */
export function hoursSettings(form: HoursForm): TradingHoursSettings {
  const windows = windowsRequest(form.windows);
  const first = windows[0];
  return {
    enabled: form.enabled,
    windows,
    start: first?.start ?? "",
    end: first?.end ?? "",
    flatten: first?.flatten ?? false,
    cancel_pending: first?.cancel_pending ?? false,
  };
}

/** A time HH:MM, from 00:00 to 23:59. */
const clock = /^([01][0-9]|2[0-3]):[0-5][0-9]$/;

/** The minutes of the day of a time HH:MM. */
function minutesOf(text: string): number {
  const [hours = 0, minutes = 0] = text.split(":").map(Number);
  return hours * 60 + minutes;
}

/** A time HH:MM of the minutes of a day. */
function clockOf(minutes: number): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(Math.floor(minutes / 60))}:${pad(minutes % 60)}`;
}

/** An asset as MT5 names it, eg: XAUUSDm (agentapi's symbol). */
const symbol = /^[A-Za-z0-9][A-Za-z0-9._#-]{0,31}$/;

/** The problems of a window of the form, by field. */
export interface WindowErrors {
  start?: string;
  end?: string;
  /** The start not before the end. */
  window?: string;
  /** An asset invalid or repeated. */
  pairs?: string;
}

/** Why the windows of the form are refused, by row and for the whole list. */
export interface WindowsCheck {
  /** The problems of each window, by its key; none for a window taken. */
  rows: Record<string, WindowErrors>;
  /** The problem of the list: too many windows, two of them overlapping, or none while on. */
  list?: string;
  /** The first problem, in the order the controller checks them; null when the windows are taken. */
  first: string | null;
}

/**
 * Why the assets typed in window w (the i-th) are refused, as the controller
 * refuses them: the first invalid or repeated one (the case kept, MT5's
 * symbols telling it apart); undefined when they are taken. The window is
 * named by its times, else by its place while they are typed.
 */
function pairsProblem(w: WindowForm, i: number): string | undefined {
  const name = clock.test(w.start) && clock.test(w.end) ? windowLabel(w) : String(i + 1);
  const pairs = parsePairs(w.pairsText);
  for (const [at, pair] of pairs.entries()) {
    if (!symbol.test(pair)) {
      return `A janela ${name} tem um ativo inválido, ${JSON.stringify(pair)}: use o nome do ativo no MT5, ex.: XAUUSDm, separados por ;.`;
    }
    if (pairs.indexOf(pair) < at) return `A janela ${name} repete o ativo ${pair}.`;
  }
  return undefined;
}

/**
 * Why windows are refused as the controller refuses them
 * (agentapi.WindowsProblem): 24 at most; each start and end HH:MM, the start
 * before the end (a window never crossing midnight); its assets named as in
 * MT5, none repeated; none overlapping nor touching another; and, required
 * (the hours on), one at least. A window is named by its place in the order of
 * the user, from 1.
 */
export function windowsCheck(windows: WindowForm[], required: boolean): WindowsCheck {
  const rows: Record<string, WindowErrors> = {};
  const problems: string[] = [];
  const tooMany =
    windows.length > MAX_WINDOWS ? `Use no máximo ${MAX_WINDOWS} janelas.` : undefined;
  if (tooMany) problems.push(tooMany);
  // the times of every window before their assets, as the controller checks them
  const pairProblems: string[] = [];
  windows.forEach((w, i) => {
    const errors: WindowErrors = {};
    if (!clock.test(w.start)) {
      errors.start = `O início da janela ${i + 1} deve ser um horário HH:MM, de 00:00 a 23:59.`;
    }
    if (!clock.test(w.end)) {
      errors.end = `O fim da janela ${i + 1} deve ser um horário HH:MM, de 00:00 a 23:59.`;
    }
    if (!errors.start && !errors.end && minutesOf(w.start) >= minutesOf(w.end)) {
      errors.window = `A janela ${windowLabel(w)} deve começar antes de terminar: uma janela não atravessa a meia-noite.`;
    }
    const first = errors.start ?? errors.end ?? errors.window;
    if (first) problems.push(first);
    errors.pairs = pairsProblem(w, i);
    if (errors.pairs) pairProblems.push(errors.pairs);
    if (first || errors.pairs) rows[w.key] = errors;
  });
  problems.push(...pairProblems);
  // the first two windows overlapping or touching, in the order of the day,
  // among the ones whose times are taken alone
  const timed = (w: WindowForm) => {
    const errors = rows[w.key];
    return !errors || !(errors.start || errors.end || errors.window);
  };
  const taken = windows.filter(timed).sort((a, b) => minutesOf(a.start) - minutesOf(b.start));
  let overlap: string | undefined;
  for (let i = 1; i < taken.length && !overlap; i++) {
    const before = taken[i - 1];
    const after = taken[i];
    if (before && after && minutesOf(after.start) <= minutesOf(before.end)) {
      overlap = `As janelas ${windowLabel(before)} e ${windowLabel(after)} se sobrepõem ou se encostam: junte-as numa só ou deixe um intervalo entre elas.`;
    }
  }
  if (overlap) problems.push(overlap);
  const none = required && windows.length === 0 ? "Adicione ao menos uma janela." : undefined;
  if (none) problems.push(none);
  return { rows, list: tooMany ?? overlap ?? none, first: problems[0] ?? null };
}

/** Why the form is refused, as the controller checks it; null when it is taken. */
export function hoursError(form: HoursForm): string | null {
  return windowsCheck(form.windows, form.enabled).first;
}

/** The last minute of the day. */
const LAST_MINUTE = 23 * 60 + 59;

/**
 * A window to add to windows, free (neither overlapping nor touching any of
 * the ones taken), its end actions unchecked: the first hour after the latest
 * end (eg: 11:00–12:00 after 08:00–10:00), else the first free hour of the
 * day, else the first free minutes; 09:00–17:00 without windows; blank when
 * the day has no room left.
 */
export function nextWindow(windows: Pick<TradingWindow, "start" | "end">[]): TradingWindow {
  const actions = { flatten: false, cancel_pending: false };
  const taken = windows
    .filter((w) => clock.test(w.start) && clock.test(w.end) && w.start < w.end)
    .map((w) => [minutesOf(w.start), minutesOf(w.end)] as const);
  if (taken.length === 0) return { start: "09:00", end: "17:00", ...actions };
  const free = (start: number, end: number) =>
    start < end && taken.every(([from, to]) => end < from || start > to);
  const latest = Math.max(...taken.map(([, to]) => to));
  const hours: number[] = [];
  for (let h = Math.floor(latest / 60) + 1; h < 24; h++) hours.push(h);
  for (let h = 0; h < 24; h++) hours.push(h);
  for (const h of hours) {
    const start = h * 60;
    const end = Math.min(start + 60, LAST_MINUTE);
    if (free(start, end)) return { start: clockOf(start), end: clockOf(end), ...actions };
  }
  // no free hour: the first gap of the day, an hour long at most (from the
  // minute after a window to the minute before the next one)
  const sorted = [...taken].sort((a, b) => a[0] - b[0]);
  let from = 0;
  for (const [start, end] of [...sorted, [LAST_MINUTE + 1, LAST_MINUTE + 1] as const]) {
    const until = Math.min(start - 1, LAST_MINUTE);
    if (until > from) {
      return { start: clockOf(from), end: clockOf(Math.min(from + 60, until)), ...actions };
    }
    from = Math.max(from, end + 1);
  }
  return { start: "", end: "", ...actions };
}
