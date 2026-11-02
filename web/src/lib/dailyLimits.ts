// The daily limits on the dashboard: of all the agents together and of each
// agent, the result of the day they weigh (realized plus open), how they
// stand (free, blocked, incomplete) and the form of their settings, checked as
// the controller checks them.

import type {
  DailyLimitSettings,
  DailyLimitsStatus,
  DailyResult,
  DailyScopeStatus,
} from "../api/types";
import { formatMoney, formatProfit } from "./format";

/** The highest limit the controller takes. */
export const maxDailyLimit = 1e9;

/** A tone of the chips of the dashboard. */
export type DailyTone = "on" | "off" | "warn" | "error" | "info";

/** A label and its tone. */
export interface DailyLabel {
  text: string;
  tone: DailyTone;
}

/** The state of a scope in words, eg: Bloqueado até 00:00. */
export function dailyStateLabel(scope: DailyScopeStatus | undefined): DailyLabel {
  switch (scope?.state) {
    case "blocked":
      return {
        text:
          scope.guard?.trigger === "gain"
            ? "Ganho atingido: bloqueado"
            : "Perda atingida: bloqueado",
        tone: "error",
      };
    case "incomplete":
      return { text: "Dados incompletos: entradas bloqueadas", tone: "warn" };
    case "free":
      return { text: "Liberado", tone: "on" };
  }
  return { text: "Sem limite", tone: "off" };
}

/**
 * The tone of the strip of a scope on the panel: the limit reached, a gain
 * or a loss; its result unknown; under its limits; or no limit.
 */
export type StripTone = "gain" | "loss" | "incomplete" | "free" | "off";

/** The tone of the strip of scope, see StripTone. */
export function stripTone(scope: DailyScopeStatus | undefined): StripTone {
  switch (scope?.state) {
    case "blocked":
      return scope.guard?.trigger === "gain" ? "gain" : "loss";
    case "incomplete":
      return "incomplete";
    case "free":
      return "free";
  }
  return "off";
}

/** The headline of the strip of scope, eg: Ganho diário atingido. */
export function stripHeadline(scope: DailyScopeStatus | undefined): string {
  switch (stripTone(scope)) {
    case "gain":
      return "Ganho diário atingido";
    case "loss":
      return "Perda diária atingida";
    case "incomplete":
      return "Resultado do dia incompleto";
    case "free":
      return "Liberado";
  }
  return "Sem limite";
}

/** The time of day of a Unix time in zone, eg: 14:32; empty without it. */
export function timeIn(unix: number | undefined, zone: string): string {
  if (!unix) return "";
  try {
    return new Date(unix * 1000).toLocaleTimeString("pt-BR", {
      hour: "2-digit",
      minute: "2-digit",
      timeZone: zone || undefined,
    });
  } catch {
    return "";
  }
}

/** How the positions of a guard were closed, eg: posições zeradas. */
export function flattenLabel(status: string | undefined): DailyLabel | null {
  switch (status) {
    case "running":
      return { text: "RISCO: ZERANDO POSIÇÕES…", tone: "info" };
    case "done":
      return { text: "RISCO: POSIÇÕES ZERADAS", tone: "error" };
    case "partial":
      return { text: "RISCO: ZERAGEM PARCIAL", tone: "warn" };
    case "failed":
      return { text: "RISCO: ZERAGEM FALHOU", tone: "error" };
  }
  return null;
}

/** An amount of a result, signed, with its currency, eg: −152,30 BRL. */
export function resultAmount(value: number, currency: string): string {
  return `${formatProfit(value)} ${currency}`.trim();
}

/** The limits in words, eg: perda máx. 150,00 · ganho máx. 300,00 BRL. */
export function limitsLabel(limits: DailyLimitSettings, currency?: string): string {
  if (!limits.enabled) return "sem limite";
  const unit = currency ?? "";
  const parts: string[] = [];
  if (limits.max_loss != null) parts.push(`perda máx. ${formatMoney(limits.max_loss)}`);
  if (limits.max_gain != null) parts.push(`ganho máx. ${formatMoney(limits.max_gain)}`);
  return `${parts.join(" · ")}${unit ? ` ${unit}` : ""}`;
}

/**
 * The results of the day of a scope: of all the agents, one by currency of
 * their accounts (the limits apply to each apart, none converted); of an
 * agent, its one.
 */
export function scopeResults(scope: DailyScopeStatus | undefined): DailyResult[] {
  if (scope?.results && scope.results.length > 0) return scope.results;
  return scope?.result ? [scope.result] : [];
}

/** The unit of the limits of all the agents: the currency of the accounts, or each one's when several. */
export function globalUnit(results: DailyResult[]): string {
  const currencies = [...new Set(results.map((r) => r.currency).filter((c) => c !== ""))];
  if (currencies.length === 1) return currencies[0] ?? "";
  return currencies.length > 1 ? "em cada moeda" : "";
}

/** How far the result went toward each limit, from 0 to 1: the loss and the gain. */
export function limitProgress(
  result: DailyResult | undefined,
  limits: DailyLimitSettings,
): { loss: number | null; gain: number | null } {
  const total = result?.total ?? 0;
  const ratio = (value: number, limit: number | undefined) =>
    limit ? Math.min(1, Math.max(0, value / limit)) : null;
  return {
    loss: limits.enabled ? ratio(-total, limits.max_loss) : null,
    gain: limits.enabled ? ratio(total, limits.max_gain) : null,
  };
}

/** The agents of a status blocked or incomplete, by name. */
export function guardedAgents(status: DailyLimitsStatus | null): Record<string, DailyScopeStatus> {
  const guarded: Record<string, DailyScopeStatus> = {};
  for (const scope of status?.agents ?? []) {
    if (scope.agent && (scope.state === "blocked" || scope.state === "incomplete")) {
      guarded[scope.agent] = scope;
    }
  }
  return guarded;
}

/** The fields of the form of the daily limits, as typed: in the currency of each account. */
export interface DailyLimitForm {
  enabled: boolean;
  maxLoss: string;
  maxGain: string;
}

/** The form of the settings, the limits left blank when absent. */
export function dailyLimitForm(settings: DailyLimitSettings | undefined): DailyLimitForm {
  return {
    enabled: settings?.enabled ?? false,
    maxLoss: settings?.max_loss != null ? String(settings.max_loss) : "",
    maxGain: settings?.max_gain != null ? String(settings.max_gain) : "",
  };
}

/** An amount typed, with a comma or a dot, null when blank; NaN when not a number. */
export function parseLimit(text: string): number | null {
  const trimmed = text
    .trim()
    .replace(/\./g, (dot, offset, all: string) =>
      all.includes(",") && offset < all.indexOf(",") ? "" : dot,
    );
  const normalized = trimmed.replace(",", ".");
  if (normalized === "") return null;
  return /^\d+(\.\d+)?$/.test(normalized) ? Number(normalized) : Number.NaN;
}

/** The settings of a form, without a currency: the one of each account. */
export function dailyLimitSettings(form: DailyLimitForm): DailyLimitSettings {
  const settings: DailyLimitSettings = { enabled: form.enabled };
  const loss = parseLimit(form.maxLoss);
  const gain = parseLimit(form.maxGain);
  if (loss !== null) settings.max_loss = loss;
  if (gain !== null) settings.max_gain = gain;
  return settings;
}

/** Why each field is refused, by field, as the controller refuses it; empty when the form is taken. */
export function dailyLimitErrors(
  form: DailyLimitForm,
): Partial<Record<keyof DailyLimitForm, string>> {
  const errors: Partial<Record<keyof DailyLimitForm, string>> = {};
  const check = (field: "maxLoss" | "maxGain", value: number | null) => {
    if (value === null) return;
    if (Number.isNaN(value)) errors[field] = "Digite um número, ex.: 150 ou 150,50.";
    else if (value <= 0) errors[field] = "Use um valor acima de zero.";
    else if (value > maxDailyLimit) errors[field] = "Valor alto demais.";
  };
  const loss = parseLimit(form.maxLoss);
  const gain = parseLimit(form.maxGain);
  check("maxLoss", loss);
  check("maxGain", gain);
  if (form.enabled && loss === null && gain === null) {
    errors.maxLoss = "Defina a perda, o ganho ou os dois.";
  }
  return errors;
}
