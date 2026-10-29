// The performance of the agents trading live on the page Performance: the
// trades their MT5 accounts booked (see PerformanceResponse), filtered by
// agent and asset, summed apart by the currency of their accounts (none
// converted), as the "Resumo de Performance" of the Profit of Nelogica sums
// them: the results before the costs, the costs, the net results; the
// averages, the extremes and the streaks; the capital and the drawdown.

import type {
  PerformanceAccount,
  PerformanceExcursion,
  PerformanceTrade,
  Point,
} from "../api/types";

/** The trades shown: of an agent and of an asset, all of them when absent. */
export interface PerformanceFilter {
  agent?: string;
  symbol?: string;
}

/** The trades of filter. */
export function filterTrades(
  trades: PerformanceTrade[],
  filter: PerformanceFilter,
): PerformanceTrade[] {
  return trades.filter(
    (t) =>
      (filter.agent === undefined || t.agent === filter.agent) &&
      (filter.symbol === undefined || t.symbol === filter.symbol),
  );
}

/** The currencies of the trades and of the accounts read, sorted: one summary each. */
export function currenciesOf(trades: PerformanceTrade[], accounts: PerformanceAccount[]): string[] {
  const all = new Set([...trades.map((t) => t.currency), ...accounts.map((a) => a.currency)]);
  return [...all].filter((c) => c !== "").sort();
}

/**
 * The capital of the return in currency: the balance at the start of the
 * period of the accounts in it, of the ones of agent when told; null without
 * any.
 */
export function capitalOf(
  accounts: PerformanceAccount[],
  currency: string,
  agent?: string,
): number | null {
  const of = accounts.filter(
    (a) => a.currency === currency && (agent === undefined || a.agents.includes(agent)),
  );
  if (of.length === 0) return null;
  return of.reduce((sum, a) => sum + a.start_balance, 0);
}

/** The summary of the trades of a currency, see summarize. */
export interface PerformanceSummary {
  currency: string;
  /** The sum of the net results: after the costs. */
  net: number;
  /** The sum of the results before the costs, and its parts: of the trades won and lost. */
  gross: number;
  grossProfit: number;
  grossLoss: number;
  /** The commissions, fees and swaps, negative for a cost. */
  costs: number;
  /** grossProfit / |grossLoss|; Infinity without a loss, 0 without a trade won. */
  profitFactor: number;
  trades: number;
  /** Won, lost and zeroed by their result before the costs. */
  wins: number;
  losses: number;
  zeroed: number;
  /** 0..1 */
  winRate: number;
  /** The average net result of a trade. */
  average: number;
  /** The average result of the trades won and lost (negative), before the costs. */
  averageWin: number;
  averageLoss: number;
  /** averageWin / |averageLoss|; null without a win or a loss. */
  payoff: number | null;
  largestWin: number;
  largestLoss: number;
  longestWinStreak: number;
  longestLossStreak: number;
  /** The average durations in seconds, of the trades whose opening is known; null without any. */
  averageDuration: number | null;
  averageWinDuration: number | null;
  averageLossDuration: number | null;
  /** The largest volume of a trade, in lots. */
  maxVolume: number;
  /** The capital of the return, see capitalOf; null when unknown. */
  capital: number | null;
  /** net / capital; null without a capital above 0. */
  returnOnCapital: number | null;
  /** The peak of the net results summed trade after trade, from 0. */
  peak: number;
  /** The largest fall of that sum from a peak, positive, and its share of the equity at the peak. */
  drawdown: number;
  drawdownPct: number | null;
  /** The share of the period with a trade open, 0..1; null without an opening known. */
  timeInMarket: number | null;
}

/** The trades by their close, then their position: the order of the sums. */
function byClose(trades: PerformanceTrade[]): PerformanceTrade[] {
  return [...trades].sort((a, b) => a.closed_at - b.closed_at || a.position - b.position);
}

const average = (values: number[]): number | null =>
  values.length ? values.reduce((s, v) => s + v, 0) / values.length : null;

/**
 * The summary of trades, all in currency, of a period from from until before
 * to (Unix seconds, to bounded by now for the time in the market), capital
 * the one of the return (see capitalOf).
 */
export function summarize(
  trades: PerformanceTrade[],
  currency: string,
  capital: number | null,
  range: { from: number; to: number },
  now: number,
): PerformanceSummary {
  const sorted = byClose(trades);
  const sum = (values: number[]) => values.reduce((s, v) => s + v, 0);
  const won = sorted.filter((t) => t.gross > 0);
  const lost = sorted.filter((t) => t.gross < 0);
  const grossProfit = sum(won.map((t) => t.gross));
  const grossLoss = sum(lost.map((t) => t.gross));
  const net = sum(sorted.map((t) => t.net));

  let winStreak = 0;
  let lossStreak = 0;
  let longestWinStreak = 0;
  let longestLossStreak = 0;
  for (const t of sorted) {
    winStreak = t.gross > 0 ? winStreak + 1 : 0;
    lossStreak = t.gross < 0 ? lossStreak + 1 : 0;
    longestWinStreak = Math.max(longestWinStreak, winStreak);
    longestLossStreak = Math.max(longestLossStreak, lossStreak);
  }

  let running = 0;
  let peak = 0;
  let drawdown = 0;
  let peakAtDrawdown = 0;
  for (const t of sorted) {
    running += t.net;
    peak = Math.max(peak, running);
    if (peak - running > drawdown) {
      drawdown = peak - running;
      peakAtDrawdown = peak;
    }
  }
  const equityAtPeak = (capital ?? 0) + peakAtDrawdown;

  const duration = (t: PerformanceTrade) =>
    t.opened_at ? Math.max(0, t.closed_at - t.opened_at) : null;
  const durations = (list: PerformanceTrade[]) =>
    list.map(duration).filter((d): d is number => d !== null);

  const averageWin = won.length ? grossProfit / won.length : 0;
  const averageLoss = lost.length ? grossLoss / lost.length : 0;
  return {
    currency,
    net,
    gross: grossProfit + grossLoss,
    grossProfit,
    grossLoss,
    costs: sum(sorted.map((t) => t.costs)),
    profitFactor:
      grossLoss < 0 ? grossProfit / -grossLoss : grossProfit > 0 ? Number.POSITIVE_INFINITY : 0,
    trades: sorted.length,
    wins: won.length,
    losses: lost.length,
    zeroed: sorted.length - won.length - lost.length,
    winRate: sorted.length ? won.length / sorted.length : 0,
    average: sorted.length ? net / sorted.length : 0,
    averageWin,
    averageLoss,
    payoff: won.length && lost.length ? averageWin / -averageLoss : null,
    largestWin: won.length ? Math.max(...won.map((t) => t.gross)) : 0,
    largestLoss: lost.length ? Math.min(...lost.map((t) => t.gross)) : 0,
    longestWinStreak,
    longestLossStreak,
    averageDuration: average(durations(sorted)),
    averageWinDuration: average(durations(won)),
    averageLossDuration: average(durations(lost)),
    maxVolume: sorted.length ? Math.max(...sorted.map((t) => t.volume)) : 0,
    capital,
    returnOnCapital: capital !== null && capital > 0 ? net / capital : null,
    peak,
    drawdown,
    drawdownPct: drawdown > 0 && equityAtPeak > 0 ? drawdown / equityAtPeak : null,
    timeInMarket: timeInMarket(sorted, range, now),
  };
}

/** The share of the period with a trade open: the union of their spans, of the ones whose opening is known. */
function timeInMarket(
  trades: PerformanceTrade[],
  range: { from: number; to: number },
  now: number,
): number | null {
  const end = Math.min(range.to, now);
  const length = end - range.from;
  const spans = trades
    .filter((t) => t.opened_at)
    .map((t) => [Math.max(t.opened_at ?? 0, range.from), Math.min(t.closed_at, end)] as const)
    .filter(([a, b]) => b > a)
    .sort((x, y) => x[0] - y[0]);
  if (length <= 0 || !trades.some((t) => t.opened_at)) return null;
  let covered = 0;
  let start = Number.NEGATIVE_INFINITY;
  let stop = Number.NEGATIVE_INFINITY;
  for (const [a, b] of spans) {
    if (a > stop) {
      if (stop > start) covered += stop - start;
      start = a;
      stop = b;
    } else stop = Math.max(stop, b);
  }
  if (stop > start) covered += stop - start;
  return Math.min(1, covered / length);
}

/**
 * The net results summed trade after trade, from 0 at from: a point a second
 * with a close (its last sum), as the chart of the equity draws it.
 */
export function equityCurve(trades: PerformanceTrade[], from: number): Point[] {
  const points: Point[] = [];
  const first = byClose(trades)[0];
  if (!first) return points;
  points.push({ time: Math.min(from, first.closed_at - 1), value: 0 });
  let running = 0;
  for (const t of byClose(trades)) {
    running += t.net;
    const last = points[points.length - 1];
    if (last && last.time === t.closed_at) last.value = running;
    else points.push({ time: t.closed_at, value: running });
  }
  return points;
}

/** A bar of the charts: its label, its net result and its trades. */
export interface PerformanceBar {
  key: string;
  label: string;
  net: number;
  trades: number;
  wins: number;
}

/** The size of the buckets of the chart by period, see periodBars. */
export type PeriodBucket = "day" | "week" | "month";

/** The bucket of a period of length seconds: days up to 31 days, weeks up to 180, else months. */
export function bucketOf(length: number): PeriodBucket {
  const days = length / 86400;
  if (days <= 31) return "day";
  if (days <= 180) return "week";
  return "month";
}

const pad = (n: number) => String(n).padStart(2, "0");

/** The start of the bucket of a time, by the clock of the browser: its key and its label. */
function bucketKey(unix: number, bucket: PeriodBucket): { key: string; label: string } {
  const date = new Date(unix * 1000);
  if (bucket === "month") {
    return {
      key: `${date.getFullYear()}-${pad(date.getMonth() + 1)}`,
      label: `${pad(date.getMonth() + 1)}/${date.getFullYear()}`,
    };
  }
  if (bucket === "week") {
    const monday = new Date(date.getFullYear(), date.getMonth(), date.getDate());
    monday.setDate(monday.getDate() - ((monday.getDay() + 6) % 7));
    return {
      key: `${monday.getFullYear()}-${pad(monday.getMonth() + 1)}-${pad(monday.getDate())}`,
      label: `sem. ${pad(monday.getDate())}/${pad(monday.getMonth() + 1)}`,
    };
  }
  return {
    key: `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`,
    label: `${pad(date.getDate())}/${pad(date.getMonth() + 1)}`,
  };
}

/** The net results of the trades by bucket of their close, in time order. */
export function periodBars(trades: PerformanceTrade[], bucket: PeriodBucket): PerformanceBar[] {
  const bars = new Map<string, PerformanceBar>();
  for (const t of byClose(trades)) {
    const { key, label } = bucketKey(t.closed_at, bucket);
    const bar = bars.get(key) ?? { key, label, net: 0, trades: 0, wins: 0 };
    bar.net += t.net;
    bar.trades++;
    if (t.gross > 0) bar.wins++;
    bars.set(key, bar);
  }
  return [...bars.values()];
}

/** The net results of the trades by asset or by agent, the best first. */
export function groupBars(trades: PerformanceTrade[], by: "symbol" | "agent"): PerformanceBar[] {
  const bars = new Map<string, PerformanceBar>();
  for (const t of trades) {
    const key = t[by];
    const bar = bars.get(key) ?? { key, label: key, net: 0, trades: 0, wins: 0 };
    bar.net += t.net;
    bar.trades++;
    if (t.gross > 0) bar.wins++;
    bars.set(key, bar);
  }
  return [...bars.values()].sort((a, b) => b.net - a.net || a.key.localeCompare(b.key));
}

/** A trade of the list: with the net results summed until it, in time order. */
export interface ListedTrade extends PerformanceTrade {
  cumulative: number;
}

/** The trades with their running sum, the latest first, as the list shows them. */
export function listTrades(trades: PerformanceTrade[]): ListedTrade[] {
  let running = 0;
  return byClose(trades)
    .map((t) => {
      running += t.net;
      return { ...t, cumulative: running };
    })
    .reverse();
}

/** A duration in words, eg: 2h39min14s, 47s, 3d4h. */
export function formatDuration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds)) return "—";
  const s = Math.round(seconds);
  const days = Math.floor(s / 86400);
  const hours = Math.floor((s % 86400) / 3600);
  const minutes = Math.floor((s % 3600) / 60);
  const rest = s % 60;
  if (days > 0) return `${days}d${hours}h${minutes ? `${minutes}min` : ""}`;
  if (hours > 0) return `${hours}h${minutes}min${rest}s`;
  if (minutes > 0) return `${minutes}min${rest}s`;
  return `${rest}s`;
}

/** The key of a trade, and of its excursion: its account and its position. */
export function tradeKey(t: { account: string; position: number }): string {
  return `${t.account}/${t.position}`;
}

/** The excursions read, by the key of their trade, the ones without one (an error) left out. */
export function excursionsByTrade(list: PerformanceExcursion[]): Map<string, PerformanceExcursion> {
  return new Map(list.filter((x) => !x.error).map((x) => [tradeKey(x), x]));
}

/**
 * The efficiencies of a trade along its excursion, 0..1, as TradeStation and
 * the Profit count them over the range of the price while it was open (from
 * its low to its high): of its entry, how close to the best price it entered
 * (the low of a long); of its exit, how close to the best price it left (the
 * high of a long); total, its move over the range, negative for a loss. Null
 * without a range.
 */
export interface Efficiency {
  entry: number | null;
  exit: number | null;
  total: number | null;
}

/** The efficiencies of trade along x, see Efficiency. */
export function efficiencyOf(
  trade: PerformanceTrade,
  x: PerformanceExcursion | undefined,
): Efficiency {
  const high = x?.high ?? 0;
  const low = x?.low ?? 0;
  const range = high - low;
  if (!x || !(range > 0) || !trade.entry) return { entry: null, exit: null, total: null };
  if (trade.side === "SELL") {
    return {
      entry: (trade.entry - low) / range,
      exit: (high - trade.exit) / range,
      total: (trade.entry - trade.exit) / range,
    };
  }
  return {
    entry: (high - trade.entry) / range,
    exit: (trade.exit - low) / range,
    total: (trade.exit - trade.entry) / range,
  };
}

/** The efficiency the page passes to its views: its summary, the excursions by trade, how their read stands. */
export interface EfficiencyState {
  /** Null while the excursions aren't read. */
  summary: EfficiencySummary | null;
  excursions: Map<string, PerformanceExcursion>;
  loading: boolean;
  error: string | null;
}

/** The efficiency of the trades of a currency, see summarizeEfficiency. */
export interface EfficiencySummary {
  /** The trades with their excursion, and the ones without (their opening unknown, their candles unread). */
  trades: number;
  missing: number;
  /** The average most up and down, positive. */
  averageMFE: number | null;
  averageMAE: number | null;
  /** The average efficiencies, see Efficiency, of the trades with one. */
  entry: number | null;
  exit: number | null;
  total: number | null;
  /** What the trades won gave back from their best: the sum of their MFE less their gross. */
  leftOnTable: number;
  /** The most a trade won was down before, its MAE: how far a stop must be not to lose it. */
  largestWinMAE: number | null;
}

/** The efficiency of trades along their excursions, see EfficiencySummary. */
export function summarizeEfficiency(
  trades: PerformanceTrade[],
  excursions: Map<string, PerformanceExcursion>,
): EfficiencySummary {
  const paired = trades
    .map((t) => ({ t, x: excursions.get(tradeKey(t)) }))
    .filter((p): p is { t: PerformanceTrade; x: PerformanceExcursion } => p.x !== undefined);
  const efficiencies = paired.map((p) => efficiencyOf(p.t, p.x));
  const mean = (values: (number | null)[]) =>
    average(values.filter((v): v is number => v !== null && Number.isFinite(v)));
  const won = paired.filter((p) => p.t.gross > 0);
  return {
    trades: paired.length,
    missing: trades.length - paired.length,
    averageMFE: average(paired.map((p) => p.x.mfe)),
    averageMAE: average(paired.map((p) => p.x.mae)),
    entry: mean(efficiencies.map((e) => e.entry)),
    exit: mean(efficiencies.map((e) => e.exit)),
    total: mean(efficiencies.map((e) => e.total)),
    leftOnTable: won.reduce((sum, p) => sum + Math.max(0, p.x.mfe - p.t.gross), 0),
    largestWinMAE: won.length ? Math.max(...won.map((p) => p.x.mae)) : null,
  };
}

/** A time for the CSV, eg: 2026-10-29 15:04:05, in UTC. */
function csvTime(unix: number | undefined): string {
  if (!unix) return "";
  return new Date(unix * 1000).toISOString().replace("T", " ").slice(0, 19);
}

/** A field of the CSV, quoted when it holds a separator, a quote or a line break. */
function csvField(value: string | number): string {
  const text = String(value);
  return /[;"\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}

/**
 * The trades as a CSV, as a spreadsheet in Portuguese opens it: separated by
 * semicolons, the decimals with a dot, the times in UTC, the oldest first.
 */
export function tradesCSV(
  trades: PerformanceTrade[],
  excursions: Map<string, PerformanceExcursion> = new Map(),
): string {
  const header = [
    "ativo",
    "agente",
    "conta",
    "posicao",
    "lado",
    "lotes",
    "abertura_utc",
    "fechamento_utc",
    "duracao_s",
    "preco_entrada",
    "preco_saida",
    "resultado_bruto",
    "custos",
    "resultado_liquido",
    "acumulado",
    "moeda",
    "mfe",
    "mae",
    "eficiencia_entrada",
    "eficiencia_saida",
    "eficiencia_total",
  ];
  const ratio = (v: number | null) => (v === null ? "" : Math.round(v * 10000) / 10000);
  const rows = [...listTrades(trades)].reverse().map((t) => {
    const x = excursions.get(tradeKey(t));
    const e = efficiencyOf(t, x);
    return [
      t.symbol,
      t.agent,
      t.account,
      t.position,
      t.side,
      t.volume,
      csvTime(t.opened_at),
      csvTime(t.closed_at),
      t.opened_at ? t.closed_at - t.opened_at : "",
      t.entry || "",
      t.exit,
      t.gross,
      t.costs,
      t.net,
      Math.round(t.cumulative * 100) / 100,
      t.currency,
      x ? x.mfe : "",
      x ? x.mae : "",
      ratio(e.entry),
      ratio(e.exit),
      ratio(e.total),
    ]
      .map(csvField)
      .join(";");
  });
  return `${[header.join(";"), ...rows].join("\n")}\n`;
}
