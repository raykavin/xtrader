// The trading hours of all the agents on the dashboard: how they stand, the
// actions of the end of their window, and the form that sets them.

import type { TradingHoursEnd, TradingHoursSettings, TradingHoursStatus } from "../api/types";
import { timeIn } from "./dailyLimits";
import { actionStatusLabel } from "./supervisor";

/** A label and its tone, as the chips of the dashboard show them. */
export interface HoursLabel {
  tone: "on" | "off" | "warn" | "error" | "info";
  text: string;
}

/**
 * How the trading hours stand in words: off; open until the end; or closed,
 * the entries blocked, until the start (the next change in the zone of the
 * controller).
 */
export function hoursState(status: TradingHoursStatus): HoursLabel {
  if (!status.enabled) return { tone: "off", text: "Desligado" };
  const next = timeIn(status.next_at, status.zone);
  if (status.blocked) {
    return { tone: "warn", text: `Fora do horário · abre às ${next || status.start}` };
  }
  return { tone: "on", text: `Aberto · fecha às ${next || status.end}` };
}

/** The window of the hours, eg: 09:00–17:30; empty while off. */
export function hoursWindow(status: Pick<TradingHoursStatus, "enabled" | "start" | "end">): string {
  return status.enabled && status.start && status.end ? `${status.start}–${status.end}` : "";
}

/** What the end of the window does, eg: zera as posições e cancela as pendentes. */
export function endActionsText(flatten: boolean, cancelPending: boolean): string {
  if (flatten && cancelPending) return "zera as posições e cancela as ordens pendentes";
  if (flatten) return "zera as posições";
  if (cancelPending) return "cancela as ordens pendentes";
  return "só bloqueia novas entradas";
}

/** How the last end of the window went, eg: Fim às 17:30: posições zeradas e pendentes canceladas · Concluída. */
export function lastEndLabel(end: TradingHoursEnd, zone: string): HoursLabel {
  const what = [end.flatten && "posições zeradas", end.cancel_pending && "pendentes canceladas"]
    .filter(Boolean)
    .join(" e ");
  const at = timeIn(end.at, zone);
  if (end.status === "executing") {
    return { tone: "info", text: `Fim às ${at}: ${what} · executando` };
  }
  const status = actionStatusLabel(end.status);
  const tone = status.tone === "on" ? "on" : status.tone === "error" ? "error" : "warn";
  return { tone, text: `Fim às ${at}: ${what} · ${status.text}` };
}

/** The form of the trading hours, as typed. */
export type HoursForm = Pick<
  TradingHoursSettings,
  "enabled" | "start" | "end" | "flatten" | "cancel_pending"
>;

/** A time HH:MM, from 00:00 to 23:59. */
const clock = /^([01][0-9]|2[0-3]):[0-5][0-9]$/;

/** Why the form is refused, as the controller checks it; null when it is taken. */
export function hoursError(form: HoursForm): string | null {
  for (const [name, value] of [
    ["O início", form.start],
    ["O fim", form.end],
  ] as const) {
    if (value !== "" && !clock.test(value)) return `${name}: um horário de 00:00 a 23:59.`;
  }
  if (form.enabled && (form.start === "" || form.end === "")) {
    return "Ligado, informe o início e o fim.";
  }
  if (form.enabled && form.start === form.end) return "O início e o fim devem ser diferentes.";
  return null;
}

/** Whether the window of form crosses midnight, eg: 22:00–02:00. */
export function crossesMidnight(form: Pick<HoursForm, "start" | "end">): boolean {
  return clock.test(form.start) && clock.test(form.end) && form.start > form.end;
}
