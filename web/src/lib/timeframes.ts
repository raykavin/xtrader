// The timeframe the chart of a pair is shown on (see ViewTimeframe), apart
// from the one the strategy trades on, its execution timeframe: showing
// another one draws its candles, read from the agent, with the orders and the
// analysis of the strategy aligned to them, and never changes the trading.

import type { Candle, CandlesResponse, Order, Snapshot, ViewTimeframe } from "../api/types";
import { snapTime } from "./analysis";

/** The label of a timeframe, eg: D for the daily one. */
export function timeframeLabel(timeframe: string): string {
  return timeframe === "1d" ? "D" : timeframe;
}

/** The period of a timeframe in seconds, 0 for one unknown. */
export function timeframeSeconds(timeframe: string): number {
  const match = /^(\d+)([mhdw])$/.exec(timeframe);
  if (!match) return 0;
  const units: Record<string, number> = { m: 60, h: 3600, d: 86400, w: 604800 };
  return Number(match[1]) * (units[match[2] ?? ""] ?? 0);
}

/**
 * The timeframes the chart of the snapshot may be shown on, by their period:
 * the ones the agent tells, or the one of the strategy alone for an agent
 * that tells none (of a version before).
 */
export function viewTimeframes(snapshot: Snapshot): ViewTimeframe[] {
  if (snapshot.timeframes && snapshot.timeframes.length > 0) return snapshot.timeframes;
  return snapshot.timeframe
    ? [{ timeframe: snapshot.timeframe, execution: true, available: true }]
    : [];
}

/**
 * The timeframe shown: the one asked, when the chart may be shown on it, else
 * the one of the strategy, empty for an agent that tells none.
 */
export function shownTimeframe(snapshot: Snapshot, asked: string | null): string {
  const views = viewTimeframes(snapshot);
  const wanted = views.find((v) => v.timeframe === asked && v.available);
  return wanted?.timeframe ?? snapshot.timeframe ?? "";
}

/**
 * The candles of a timeframe other than the one of the strategy shown on the
 * chart: of agent and pair (its key, see viewKey), read from the agent, and
 * where from (see CandlesResponse.source).
 */
export interface ViewCandles {
  key: string;
  pair: string;
  timeframe: string;
  source: string;
  candles: Candle[];
}

/**
 * The key of the candles of timeframe of pair of agent: an answer for another
 * one, eg: late, of the timeframe shown before, is never shown.
 */
export const viewKey = (agent: string, pair: string, timeframe: string) =>
  `${agent}\n${pair}\n${timeframe}`;

/** The candles of an answer of the agent, of key. */
export function viewOf(key: string, response: CandlesResponse): ViewCandles {
  return {
    key,
    pair: response.pair,
    timeframe: response.timeframe,
    source: response.source,
    candles: response.candles,
  };
}

/**
 * The candles of view with the fresh ones of an answer read again, the last
 * ones: each candle of a time in its place, the newer ones after; view itself
 * when the answer is of another key (late, of another timeframe or pair).
 */
export function mergeView(
  view: ViewCandles | null,
  key: string,
  fresh: Candle[],
): ViewCandles | null {
  if (!view || view.key !== key || fresh.length === 0) return view;
  const byTime = new Map(view.candles.map((c) => [c.time, c]));
  for (const candle of fresh) byTime.set(candle.time, candle);
  const candles = [...byTime.values()].sort((a, b) => a.time - b.time);
  return { ...view, candles };
}

/**
 * Whether the fresh candles read again leave a gap after the ones of view:
 * the first of them after its last, eg: the page asleep for a while; the
 * candles are then to be read again whole.
 */
export function freshGap(view: ViewCandles, fresh: Candle[]): boolean {
  const last = view.candles[view.candles.length - 1];
  const first = fresh[0];
  return last !== undefined && first !== undefined && first.time > last.time;
}

/**
 * The candles of view with the price of the strategy of now (seconds) in the
 * last one, when its period holds now: its close, and its high or low when
 * beyond them. stale tells now is past it: the candles are to be read again.
 */
export function applyViewPrice(
  view: ViewCandles,
  price: number,
  now: number,
): { view: ViewCandles; stale: boolean } {
  const last = view.candles[view.candles.length - 1];
  const period = timeframeSeconds(view.timeframe);
  if (!last || period === 0 || !Number.isFinite(price)) return { view, stale: false };
  if (now >= last.time + period) return { view, stale: true };
  if (now < last.time || last.close === price) return { view, stale: false };
  const candle = {
    ...last,
    close: price,
    high: Math.max(last.high, price),
    low: Math.min(last.low, price),
  };
  return { view: { ...view, candles: [...view.candles.slice(0, -1), candle] }, stale: false };
}

/**
 * The orders of the strategy anchored to the candles shown: each at the
 * candle holding the time it was executed or changed (see Order.candle_time),
 * the ones before them as they are, out of them.
 */
export function anchorOrders(orders: Order[], candles: Candle[]): Order[] {
  const times = candles.map((c) => c.time);
  const first = times[0] ?? Number.POSITIVE_INFINITY;
  return orders.map((order) => {
    if (order.updated_at < first) return order;
    const time = snapTime(times, order.updated_at);
    return time === null || time === order.candle_time ? order : { ...order, candle_time: time };
  });
}

/**
 * The snapshot drawn on a timeframe other than the one of the strategy: its
 * candles, the orders anchored to them, and none of the indicators of the
 * strategy, computed on its own candles; the rest (the orders, the position,
 * the analysis) the one of the strategy.
 */
export function viewSnapshot(snapshot: Snapshot, view: ViewCandles | null): Snapshot {
  if (!view || view.pair !== snapshot.pair) return snapshot;
  return {
    ...snapshot,
    candles: view.candles,
    indicators: [],
    orders: anchorOrders(snapshot.orders, view.candles),
  };
}

const sameCandle = (a: Candle, b: Candle) =>
  a === b ||
  (a.time === b.time &&
    a.open === b.open &&
    a.high === b.high &&
    a.low === b.low &&
    a.close === b.close &&
    a.volume === b.volume);

/**
 * How the chart goes from the candles drawn to candles: the indexes of the
 * ones to draw again, the ones before the last one drawn changed (historical,
 * eg: one built from the prices received, read again from the broker), then
 * the last one drawn and the new ones; null when they are to be drawn again
 * whole: other times, eg: of another timeframe, or more than 5 new ones.
 */
export function candleUpdates(
  drawn: Candle[],
  candles: Candle[],
): { index: number; historical: boolean }[] | null {
  if (candles.length < drawn.length || candles.length - drawn.length > 5) return null;
  const updates: { index: number; historical: boolean }[] = [];
  for (const [index, candle] of drawn.entries()) {
    const next = candles[index];
    if (!next || next.time !== candle.time) return null;
    if (index < drawn.length - 1 && !sameCandle(candle, next)) {
      updates.push({ index, historical: true });
    }
  }
  for (let index = Math.max(drawn.length - 1, 0); index < candles.length; index++) {
    updates.push({ index, historical: false });
  }
  return updates;
}
