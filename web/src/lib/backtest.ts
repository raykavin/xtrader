// The backtests on the page Backtesting: the runs of a batch compared under
// the same conditions, each with its robustness told (a small sample, a deep
// drawdown, a validation worse than the fit, a language model, gaps in the
// data), never ranked by the profit alone.

import type {
  BacktestAssumptions,
  BacktestCompatibility,
  BacktestHours,
  BacktestRequest,
  BacktestRunInfo,
  BacktestSummary,
  ProtectionSettings,
  TradingHoursSettings,
} from "../api/types";
import { dayOf, dayStart } from "./closedPL";
import type { SupervisorTone } from "./supervisor";
import {
  agentsOutside,
  endActionsText,
  type WindowForm,
  windowDetails,
  windowsCheck,
  windowsForm,
  windowsOf,
  windowsRequest,
} from "./tradingHours";

/** A trade of the report of a run, see agentapi.BacktestTrade. */
export interface BacktestTrade {
  pair: string;
  side: string;
  quantity: number;
  opened_at: number;
  closed_at: number;
  entry: number;
  exit: number;
  gross: number;
  fees: number;
  net: number;
  entry_reason: string;
  exit_reason: string;
}

/** A position open at the end of a run, valued at the market, see agentapi.BacktestOpen. */
export interface BacktestOpen {
  pair: string;
  side: string;
  quantity: number;
  opened_at: number;
  entry: number;
  mark: number;
  profit: number;
}

/** A call of the model of an LLM run, see llm.RecordedCall. */
export interface RecordedCall {
  at: number;
  pair: string;
  input_sha256: string;
  action?: string;
  confidence?: number;
  reason?: string;
  error?: string;
  input_tokens: number;
  output_tokens: number;
  cost: number;
}

/** What an LLM run adds to its report, see llm.RecorderReport. */
export interface BacktestLLMExtra {
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cost?: number;
  max_calls: number;
  max_cost: number;
  exhausted: boolean;
  model: string;
  context: string[];
  recorded: RecordedCall[];
}

/** The report of a run, see agentapi.BacktestReport. */
export interface BacktestReport {
  status: string;
  error?: string;
  currency: string;
  pairs: string[];
  timeframe: string;
  from: number;
  to: number;
  data_from?: number;
  data_to?: number;
  candles: number;
  processed: number;
  summary: {
    initial_balance: number;
    final_equity: number;
    protection_moves: number;
    refused: number;
  };
  trades: BacktestTrade[] | null;
  open: BacktestOpen[] | null;
  equity: { time: number; value: number }[] | null;
  warnings: string[] | null;
  extra?: BacktestLLMExtra;
}

/** The config a run ran with, copied, see controller.backtestSnapshot. */
export interface BacktestRunConfig {
  agent: string;
  kind: string;
  mode: string;
  strategy?: string;
  pairs: string[];
  timeframe?: string;
  args: string[];
  command: string[];
  code: string;
  prompt?: { id: number; name: string; version: number; sha256: string; content: string };
  protection?: ProtectionSettings;
  bridge: string;
  llm?: {
    provider: string;
    model: string;
    max_calls: number;
    max_cost: number;
    prices?: { input: number; output: number };
  };
  assumptions: BacktestAssumptions;
  notes?: string[];
}

/** A run with its config and its report, see GET /api/backtests/runs/{id}. */
export interface BacktestRunDetail extends BacktestRunInfo {
  config: BacktestRunConfig;
  report?: BacktestReport | null;
}

/** The least trades of a sample the dashboard takes for more than chance. */
export const MIN_SAMPLE = 30;
/** A drawdown deeper than this share of the peak is told. */
export const DEEP_DRAWDOWN = 0.2;

/** The statuses of a run in words, and their tone. */
export const runStatusLabels: Record<string, { text: string; tone: SupervisorTone }> = {
  queued: { text: "Na fila", tone: "info" },
  running: { text: "Executando", tone: "info" },
  succeeded: { text: "Concluído", tone: "on" },
  failed: { text: "Falhou", tone: "error" },
  canceled: { text: "Cancelado", tone: "off" },
  interrupted: { text: "Interrompido", tone: "warn" },
  budget_exhausted: { text: "Teto do LLM atingido", tone: "warn" },
};

/** The reasons of the entries and the exits of the trades in words. */
export const reasonLabels: Record<string, string> = {
  market: "Sinal (mercado)",
  limit: "Ordem limitada",
  stop_entry: "Ordem stop",
  stop_loss: "Stop loss",
  take_profit: "Take profit",
  daily_limit: "Limite diário",
  stop_out: "Patrimônio zerado",
  trading_hours: "Fim do horário",
};

/**
 * The trading hours of assumptions in words: their windows (the start to the
 * end of a batch of before), the agents and the assets of each when some (see
 * windowDetails), and what the end of each does, eg: 09:00–17:30, no fim zera
 * as posições; 08:00–10:00 (agente: highlow; ativo: XAUUSDm), 12:00–14:00, no
 * fim de cada janela zera as posições; 08:00–10:00, no fim zera as posições;
 * 12:00–14:00, no fim só bloqueia novas entradas. Empty for none.
 */
export function hoursAssumption(hours: BacktestHours | undefined): string {
  const windows = hours ? windowsOf(hours) : [];
  const [first] = windows;
  if (!first) return "";
  const ends = (w: { flatten: boolean; cancel_pending: boolean }) =>
    endActionsText(w.flatten, w.cancel_pending);
  if (windows.length === 1) return `${windowDetails(first)}, no fim ${ends(first)}`;
  if (windows.every((w) => ends(w) === ends(first))) {
    return `${windows.map(windowDetails).join(", ")}, no fim de cada janela ${ends(first)}`;
  }
  return windows.map((w) => `${windowDetails(w)}, no fim ${ends(w)}`).join("; ");
}

/** Whether a run is still to end. */
export function active(run: BacktestRunInfo): boolean {
  return run.status === "queued" || run.status === "running";
}

/** The progress of a run, 0..1; null while unknown. */
export function progressOf(run: BacktestRunInfo): number | null {
  if (run.total <= 0) return run.status === "succeeded" ? 1 : null;
  return Math.min(1, run.processed / run.total);
}

/** A flag of the robustness of a run. */
export interface RobustnessFlag {
  text: string;
  tone: "warn" | "error" | "info";
  title: string;
}

/** The flags of the robustness of summary, of a run of an LLM when llm. */
export function robustnessFlags(
  summary: BacktestSummary,
  llm: boolean,
  degraded?: boolean,
): RobustnessFlag[] {
  const flags: RobustnessFlag[] = [];
  if (summary.trades < MIN_SAMPLE) {
    flags.push({
      text: "Amostra pequena",
      tone: "warn",
      title: `${summary.trades} operações: abaixo de ${MIN_SAMPLE}, o resultado pode ser acaso`,
    });
  }
  if (summary.max_drawdown_pct > DEEP_DRAWDOWN) {
    flags.push({
      text: "Drawdown alto",
      tone: "error",
      title: `Queda de ${(summary.max_drawdown_pct * 100).toFixed(1)}% do topo`,
    });
  }
  if (degraded) {
    flags.push({
      text: "Degradou na validação",
      tone: "error",
      title: "Resultado da validação bem pior que o do ajuste: possível ajuste excessivo",
    });
  }
  if (llm) {
    flags.push({
      text: "LLM: não determinístico",
      tone: "info",
      title:
        "As respostas do LLM variam entre execuções, o contexto é parcial e o LLM pode ter visto o período no treino",
    });
  }
  if (summary.gaps > 0) {
    flags.push({
      text: "Dados com lacunas",
      tone: "warn",
      title: `${summary.gaps} lacuna(s) nos candles: veja os avisos da execução`,
    });
  }
  return flags;
}

/** The columns the comparison sorts by. */
export type SortKey =
  | "agent"
  | "net"
  | "return"
  | "drawdown"
  | "ratio"
  | "factor"
  | "winrate"
  | "trades"
  | "months"
  | "validation";

/** The candles a strategy analyzes in a period of seconds on a timeframe, eg: the calls of an LLM. */
export function candlesIn(seconds: number, timeframe: string | undefined): number {
  const match = /^(\d+)([mhdw])$/.exec(timeframe ?? "");
  const unit = { m: 60, h: 3600, d: 86400, w: 604800 }[
    (match?.[2] ?? "h") as "m" | "h" | "d" | "w"
  ];
  const length = (match ? Number(match[1]) : 1) * unit;
  return Math.ceil(seconds / length);
}

/** The calls an LLM agent makes over a period at most: its candles by pair, its ceiling. */
export function estimatedCalls(
  agent: BacktestCompatibility,
  seconds: number,
  timeframe: string | undefined,
  maxCalls: number,
): number {
  const candles =
    candlesIn(seconds, timeframe || agent.timeframe || "15m") * Math.max(agent.pairs.length, 1);
  return Math.min(candles, maxCalls);
}

/** The timeframes a batch may replace the ones of its agents with, see the controller. */
export const backtestTimeframes = ["1m", "5m", "15m", "30m", "1h", "4h", "1d"];

/** The largest share of the period run apart as the validation. */
export const MAX_VALIDATION = 0.5;

/** The form of a batch, as typed: the numbers as text, a blank for none or the default. */
export interface BacktestForm {
  name: string;
  agents: string[];
  /** The pairs tested, a subset of the ones of the agents; none for all of each. */
  pairs: string[];
  /** The first and the last day, YYYY-MM-DD in the time of the browser. */
  from: string;
  to: string;
  /** The share of the period run apart as the validation, in percent. */
  validation: string;
  timeframe: string;
  balance: string;
  /** The fee of each fill, in percent of its notional. */
  fee: string;
  spread: string;
  slippage: string;
  dailyLoss: string;
  dailyGain: string;
  /** The trading hours of the runs: applied or not, and their windows with the actions of the end of each. */
  hours: boolean;
  hoursWindows: WindowForm[];
  llmMaxCalls: string;
  llmMaxCost: string;
  /** Params added to every agent of the batch, eg: the entry filter, "regime=trend; equity-curve=20". */
  params: string;
}

/** The limits of a batch, see BacktestsResponse. */
export interface BacktestLimits {
  max_period_days: number;
  max_runs: number;
}

/** The form of a new batch: the last 90 days until today, 30% of them for the validation. */
export function defaultForm(now: Date = new Date()): BacktestForm {
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const first = new Date(today);
  first.setDate(first.getDate() - 89);
  return {
    name: "",
    agents: [],
    pairs: [],
    from: dayOf(first),
    to: dayOf(today),
    validation: "30",
    timeframe: "",
    balance: "10000",
    fee: "0",
    spread: "0",
    slippage: "0",
    dailyLoss: "",
    dailyGain: "",
    hours: false,
    hoursWindows: [],
    llmMaxCalls: "",
    llmMaxCost: "",
    params: "",
  };
}

/** The params a backtest sets itself, refused in the params of a batch (as the controller refuses them). */
const batchParamsSet = [
  "pair",
  "timeframe",
  "strategy",
  "mode",
  "csv",
  "csv-timeframe",
  "backtest-config",
  "print-warmup",
];

/**
 * The params of a batch typed as "name=value; name=value" (see
 * BacktestRequest): a name of a flag, lowercase letters, digits and -, never
 * one the backtest sets itself nor one of a language model; null for none, or
 * why they are refused.
 */
export function batchParams(text: string): {
  params: Record<string, string> | null;
  error?: string;
} {
  const params: Record<string, string> = {};
  for (const item of text.split(";")) {
    const part = item.trim();
    if (part === "") continue;
    const at = part.indexOf("=");
    const name = (at < 0 ? part : part.slice(0, at)).trim().replace(/^-+/, "");
    const value = at < 0 ? "" : part.slice(at + 1).trim();
    if (at < 0 || value === "") return { params: null, error: `${name}: use nome=valor.` };
    if (!/^[a-z0-9][a-z0-9-]*$/.test(name))
      return {
        params: null,
        error: `${name}: use só letras minúsculas, dígitos e -.`,
      };
    if (batchParamsSet.includes(name) || name.startsWith("mt5-"))
      return { params: null, error: `${name} é definido pelo próprio backtest.` };
    if (/^(llm|calendar|news)-/.test(name))
      return { params: null, error: `${name} só vale para agentes LLM.` };
    params[name] = value;
  }
  return { params: Object.keys(params).length > 0 ? params : null };
}

/**
 * form with the trading hours of the agents (the tab Limites) as its hours:
 * applied when they are on, their windows and the actions of the end of each
 * copied, to change as the batch tests.
 */
export function withHours(form: BacktestForm, hours: TradingHoursSettings): BacktestForm {
  return { ...form, hours: hours.enabled, hoursWindows: windowsForm(windowsOf(hours)) };
}

/** The number of text, a comma or a point for the decimals; NaN when it isn't one, null when blank. */
function numberOf(text: string): number | null {
  const trimmed = text.trim();
  if (trimmed === "") return null;
  return /^-?\d+([.,]\d+)?$/.test(trimmed) ? Number(trimmed.replace(",", ".")) : Number.NaN;
}

/** The period of the days of form, Unix seconds: until the end of its last day, now at most. */
export function formPeriod(
  form: BacktestForm,
  now: Date = new Date(),
): { from: number; to: number } | null {
  const first = dayStart(form.from);
  const last = dayStart(form.to);
  if (!first || !last) return null;
  const end = new Date(last);
  end.setDate(end.getDate() + 1);
  const to = Math.min(end.getTime(), Math.floor(now.getTime() / 60_000) * 60_000);
  return { from: first.getTime() / 1000, to: Math.floor(to / 1000) };
}

/** The runs of form: one by agent, two with a validation. */
export function runsOf(form: BacktestForm): number {
  const share = numberOf(form.validation);
  return form.agents.length * (share !== null && share > 0 ? 2 : 1);
}

export type FormErrors = Partial<Record<keyof BacktestForm, string>>;

/**
 * The request of form, or why its fields are refused, as the controller
 * would refuse them (it checks them again).
 */
export function formRequest(
  form: BacktestForm,
  limits: BacktestLimits,
  now: Date = new Date(),
): { request: BacktestRequest | null; errors: FormErrors } {
  const errors: FormErrors = {};
  const number = (field: keyof BacktestForm, check: (v: number) => boolean, message: string) => {
    const value = numberOf(form[field] as string);
    if (value === null) return 0;
    if (Number.isNaN(value) || !check(value)) {
      errors[field] = message;
      return 0;
    }
    return value;
  };

  if (form.agents.length === 0) errors.agents = "Escolha ao menos um agente.";
  const period = formPeriod(form, now);
  if (!period) {
    errors.from = "Escolha a data inicial e a final.";
  } else if (period.to <= period.from) {
    errors.to =
      form.to < form.from ? "A data final é anterior à inicial." : "O período ainda não começou.";
  } else if (period.to - period.from > limits.max_period_days * 86400) {
    errors.to = `O período pode ter até ${limits.max_period_days} dias.`;
  }
  const share = number(
    "validation",
    (v) => v >= 0 && v <= MAX_VALIDATION * 100,
    "De 0 a 50% do período.",
  );
  const balance = numberOf(form.balance);
  if (balance === null || Number.isNaN(balance) || balance <= 0) {
    errors.balance = "Informe um saldo maior que zero.";
  }
  const fee = number("fee", (v) => v >= 0 && v < 10, "De 0 a 10% do valor.");
  const spread = number("spread", (v) => v >= 0, "0 ou mais pontos.");
  const slippage = number("slippage", (v) => v >= 0, "0 ou mais pontos.");
  const dailyLoss = number("dailyLoss", (v) => v >= 0, "0 ou mais; em branco, sem limite.");
  const dailyGain = number("dailyGain", (v) => v >= 0, "0 ou mais; em branco, sem limite.");
  const llmMaxCalls = number(
    "llmMaxCalls",
    (v) => v >= 0 && Number.isInteger(v),
    "Use um número inteiro, 0 ou mais.",
  );
  const llmMaxCost = number("llmMaxCost", (v) => v >= 0, "0 ou mais dólares.");
  const params = batchParams(form.params);
  if (params.error) errors.params = params.error;
  if (form.hours) {
    const refused = windowsCheck(form.hoursWindows, true).first ?? outsideWindows(form)[0] ?? null;
    if (refused) errors.hoursWindows = refused;
  }
  if (form.timeframe !== "" && !backtestTimeframes.includes(form.timeframe)) {
    errors.timeframe = "Escolha um timeframe da lista.";
  }
  const runs = runsOf(form);
  if (!errors.agents && runs > limits.max_runs) {
    errors.agents = `O lote teria ${runs} execuções; o máximo é ${limits.max_runs}.`;
  }
  if (Object.keys(errors).length > 0 || !period || balance === null)
    return { request: null, errors };
  return {
    request: {
      name: form.name.trim(),
      agents: form.agents,
      pairs: form.pairs.length > 0 ? form.pairs : undefined,
      from: period.from,
      to: period.to,
      validation_share: share / 100,
      timeframe: form.timeframe || undefined,
      balance,
      fee: fee / 100,
      spread_points: spread,
      slippage_points: slippage,
      daily_loss: dailyLoss || undefined,
      daily_gain: dailyGain || undefined,
      hours: form.hours ? batchHours(form.hoursWindows) : undefined,
      llm_max_calls: llmMaxCalls || undefined,
      llm_max_cost: llmMaxCost || undefined,
      params: params.params ?? undefined,
    },
    errors,
  };
}

/**
 * Why the agents of the batch of form are refused by its trading hours, one
 * by agent no window lets open positions (as the controller refuses them);
 * none while they are off.
 */
export function outsideWindows(
  form: Pick<BacktestForm, "agents" | "hours" | "hoursWindows">,
): string[] {
  if (!form.hours) return [];
  return agentsOutside(form.agents, form.hoursWindows).map(
    (name) =>
      `O agente ${name} não está em nenhuma janela do horário: inclua-o numa janela ou tire-o do lote.`,
  );
}

/**
 * The trading hours of a batch as sent: its windows by their start, the
 * agents and the actions of the end of each in them (the ones of the hours of
 * before, flatten and cancel_pending, unchecked).
 */
export function batchHours(windows: WindowForm[]): BacktestHours {
  return { windows: windowsRequest(windows), flatten: false, cancel_pending: false };
}

/** The pairs agent tests in form: its own, the ones chosen among them when some are. */
export function pairsTested(agent: BacktestCompatibility, form: BacktestForm): string[] {
  return form.pairs.length === 0 ? agent.pairs : agent.pairs.filter((p) => form.pairs.includes(p));
}

/**
 * The calls the LLM agents of form make at most over its period: by run (the
 * fit and the validation apart), the candles of their pairs, their ceiling.
 */
export function formCalls(
  agents: BacktestCompatibility[],
  form: BacktestForm,
  period: { from: number; to: number } | null,
  maxCalls: number,
): number {
  if (!period) return 0;
  const seconds = period.to - period.from;
  const share = (numberOf(form.validation) ?? 0) / 100;
  const segments = share > 0 ? [seconds * (1 - share), seconds * share] : [seconds];
  return agents
    .filter((a) => a.llm && form.agents.includes(a.agent))
    .reduce((sum, a) => {
      const tested = { ...a, pairs: pairsTested(a, form) };
      return (
        sum +
        segments.reduce(
          (n, s) => n + estimatedCalls(tested, s, form.timeframe || undefined, maxCalls),
          0,
        )
      );
    }, 0);
}

/**
 * The call of the model that opened trade: the last one on its pair deciding
 * its side at or before its opening; undefined when none did (eg: a pending
 * order of an earlier call).
 */
export function callFor(trade: BacktestTrade, calls: RecordedCall[]): RecordedCall | undefined {
  let found: RecordedCall | undefined;
  for (const call of calls) {
    if (call.at > trade.opened_at) break;
    if (call.pair === trade.pair && call.action === trade.side) found = call;
  }
  return found;
}
