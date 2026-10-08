// The results of the accounting periods of an agent (see AgentPeriod): the
// stats of the trades each one closed, by pair, as the cards of the dashboard
// count them, with its deposit and the balance it reached.

import type { AgentPeriod, Snapshot } from "../api/types";
import { computeStats, type Stats } from "./stats";

/** The stats of a pair in a period. */
export interface PairResult {
  pair: string;
  stats: Stats;
}

/** The results of a period: of its pairs with trades, and of all of them. */
export interface PeriodResult {
  /** The period; null for the trades before the first reset, or for all of them without any. */
  period: AgentPeriod | null;
  /** The trades counted, by their close: from since until before until, each optional. */
  since?: number;
  until?: number;
  /** The pairs that closed trades in it. */
  pairs: PairResult[];
  trades: number;
  wins: number;
  losses: number;
  /** The sum of the net profits of the pairs; null when they count them in different currencies. */
  profit: number | null;
  /** The currency of profit: the one of the pairs, else the one of the period. */
  currency: string;
  /** Gross profit / gross loss of all the pairs; Infinity without losses. */
  profitFactor: number;
  /** The deposit plus the profit; null without a period, or with the profit in another currency. */
  balance: number | null;
}

/** The results of the trades closed from since until before until, on the pairs of snapshots. */
function resultOf(
  period: AgentPeriod | null,
  snapshots: Snapshot[],
  since?: number,
  until?: number,
): PeriodResult {
  const pairs = snapshots
    .map((snapshot) => ({ pair: snapshot.pair, stats: computeStats(snapshot, { since, until }) }))
    .filter((result) => result.stats.trades > 0);
  const currencies = new Set(pairs.map((result) => result.stats.profitCurrency));
  const sum = (field: "trades" | "wins" | "losses" | "totalProfit" | "grossProfit" | "grossLoss") =>
    pairs.reduce((total, result) => total + result.stats[field], 0);
  const grossProfit = sum("grossProfit");
  const grossLoss = sum("grossLoss");
  const profit = currencies.size <= 1 ? sum("totalProfit") : null;
  const currency =
    (currencies.size === 1 ? [...currencies][0] : undefined) ?? period?.currency ?? "";
  const balance =
    period && profit !== null && (pairs.length === 0 || currency === period.currency)
      ? period.deposit + profit
      : null;
  return {
    period,
    since,
    until,
    pairs,
    trades: sum("trades"),
    wins: sum("wins"),
    losses: sum("losses"),
    profit,
    currency,
    profitFactor:
      grossLoss > 0 ? grossProfit / grossLoss : grossProfit > 0 ? Number.POSITIVE_INFINITY : 0,
    balance,
  };
}

/**
 * The results of the periods of an agent, the current one first, on the pairs
 * of snapshots: each one counts the trades closed from its start until the
 * start of the next one. Before the first reset, the trades closed before it
 * come last; without any reset, a single result counts all of them.
 */
export function periodResults(periods: AgentPeriod[], snapshots: Snapshot[]): PeriodResult[] {
  const first = periods.at(-1);
  if (!first) return [resultOf(null, snapshots)];
  const results = periods.map((period) =>
    resultOf(period, snapshots, period.started_at, period.ended_at ?? undefined),
  );
  const before = resultOf(null, snapshots, undefined, first.started_at);
  if (before.trades > 0) results.push(before);
  return results;
}

/** Digits grouped by thousands with dots, eg: 1.000 or 12.345.678: not from 0, which 0.500 is a decimal. */
const thousands = /^[1-9]\d{0,2}(\.\d{3})+$/;

/**
 * The amount typed, eg: 1.234,56, 1234,56, 1.000, 0.500 or 1234.56; null
 * unless a number from 0. A comma is the decimal separator, the dots before it
 * then those of thousands, grouping 3 digits each; without a comma, dots
 * grouping 3 digits from 1 are those of thousands too, else a dot is the
 * decimal separator: 0.500 is half. An amount of another grouping, eg:
 * 0.500,00 or 1.23,4, is ambiguous and refused.
 */
export function parseAmount(text: string): number | null {
  let value = text.trim().replace(/\s/g, "");
  if (value === "") return null;
  if (value.includes(",")) {
    const [integer = "", decimals, ...more] = value.split(",");
    if (more.length > 0 || !/^\d+$/.test(decimals ?? "")) return null;
    if (!/^\d+$/.test(integer) && !thousands.test(integer)) return null;
    value = `${integer.replace(/\./g, "")}.${decimals}`;
  } else if (thousands.test(value)) {
    value = value.replace(/\./g, "");
  }
  if (!/^\d+(\.\d+)?$/.test(value)) return null;
  const amount = Number(value);
  return Number.isFinite(amount) ? amount : null;
}

/**
 * The periods once period started, as the controller lists them: it first,
 * the one current until then ending as it starts. A period listed already is
 * left as it is.
 */
export function withPeriod(periods: AgentPeriod[] | null, period: AgentPeriod): AgentPeriod[] {
  const listed = periods ?? [];
  if (listed.some((p) => p.id === period.id)) return listed;
  return [
    period,
    ...listed.map((p, i) =>
      i === 0 && p.ended_at === undefined ? { ...p, ended_at: period.started_at } : p,
    ),
  ];
}

/** What a reset of the results did, see resetPeriods. */
export interface ResetOutcome {
  /** The period it started. */
  period: AgentPeriod;
  /** The periods to show: the ones read again, else the ones shown with the new one. */
  periods: AgentPeriod[];
  /** Why the periods couldn't be read again, the reset done; null when they were. */
  stale: string | null;
}

/**
 * Resets the results by reset, the request of a new period, then reads the
 * periods again by reread. A failed reset throws: nothing changed. A failed
 * reading doesn't, the reset being done: the periods shown get the new one,
 * stale telling why they weren't read, so that the reset isn't repeated.
 */
export async function resetPeriods(
  periods: AgentPeriod[] | null,
  reset: () => Promise<AgentPeriod>,
  reread: () => Promise<AgentPeriod[]>,
): Promise<ResetOutcome> {
  const period = await reset();
  try {
    return { period, periods: await reread(), stale: null };
  } catch (e) {
    const why = e instanceof Error ? e.message : String(e);
    return { period, periods: withPeriod(periods, period), stale: why };
  }
}
