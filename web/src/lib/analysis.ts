// The analysis of the language model of a strategy drawn on the price chart
// (see Analysis): its drawings, the zones, levels and markers of the structure
// it relies on, and its intent, the entry it proposed without placing an
// order; apart from them, the orders at the broker, the pending entries among
// them with their legs.

import {
  type Analysis,
  type Candle,
  type Decision,
  type Drawing,
  DrawingDemand,
  DrawingSupply,
  DrawingZone,
  type Intent,
  type Order,
  ZoneBroken,
  ZoneFresh,
  ZoneTested,
} from "../api/types";
import { formatChartPrice, formatCompact, formatTime } from "./format";
import type { Palette } from "./theme";

/** The kinds of zones: one of a structure (eg: an FVG), of supply or of demand. */
export type ZoneKind = typeof DrawingZone | typeof DrawingSupply | typeof DrawingDemand;

/** A zone of the analysis: a box from `from` to `to` (null: up to now), from low to high. */
export interface ZoneShape {
  kind: ZoneKind;
  /** The state of a zone of supply or demand (see ZoneFresh), null for the others. */
  state: string | null;
  from: number;
  to: number | null;
  low: number;
  high: number;
  color: string;
  label: string;
  /** Ended before now: drawn faded, its border dashed. */
  ended: boolean;
  /**
   * The edge of its label: the bottom of a bullish zone, the top of the
   * others, away from the limit a buy rests at its top (a sell at the bottom
   * of a bearish one) with its own label.
   */
  labelAt: "top" | "bottom";
}

/** A level of the analysis, or of its intent: a line from `from` to `to` (null: up to now). */
export interface LevelShape {
  from: number;
  to: number | null;
  price: number;
  color: string;
  label: string;
  ended: boolean;
  /** A price of the intent, dashed: never an order at the broker. */
  intent: boolean;
}

/** A marker of the analysis: a point at time and price, its label above it. */
export interface MarkerShape {
  time: number;
  price: number;
  color: string;
  label: string;
}

/** What the chart draws of an analysis. */
export interface AnalysisShapes {
  zones: ZoneShape[];
  levels: LevelShape[];
  markers: MarkerShape[];
}

export const noShapes: AnalysisShapes = { zones: [], levels: [], markers: [] };

/**
 * The open time of the candle of times (ascending) that holds time: the last
 * one at or before it, the first one before them all; null without candles.
 */
export function snapTime(times: number[], time: number): number | null {
  const first = times[0];
  const last = times[times.length - 1];
  if (first === undefined || last === undefined) return null;
  if (time <= first) return first;
  if (time >= last) return last;
  let low = 0;
  let high = times.length - 1;
  while (low < high) {
    const middle = (low + high + 1) >> 1;
    if ((times[middle] ?? Number.POSITIVE_INFINITY) <= time) low = middle;
    else high = middle - 1;
  }
  return times[low] ?? null;
}

/** The color of a bias: bullish up, bearish down, neutral muted. */
export function biasColor(bias: string, palette: Palette): string {
  if (bias === "bullish") return palette.up;
  if (bias === "bearish") return palette.down;
  return palette.muted;
}

/** The label of a drawing, with its timeframe when it isn't the one of the analysis, eg: FVG 4h. */
export function drawingLabel(drawing: Drawing, timeframe: string): string {
  return drawing.timeframe && drawing.timeframe !== timeframe
    ? `${drawing.label} ${drawing.timeframe}`
    : drawing.label;
}

const zoneKinds: Record<string, string> = { supply: "Oferta", demand: "Demanda" };

const zoneStates: Record<string, string> = {
  [ZoneFresh]: "fresca",
  [ZoneTested]: "testada",
  [ZoneBroken]: "rompida",
};

/** The state of a zone of supply or demand, eg: testada. */
export const zoneStateLabel = (state: string) => zoneStates[state] ?? state;

/** Whether the drawing is a zone of supply or demand. */
export const isSupplyDemand = (drawing: Pick<Drawing, "kind">) =>
  drawing.kind === DrawingSupply || drawing.kind === DrawingDemand;

/**
 * The label of a zone of supply or demand on the chart: its kind, then its
 * own label, eg: Demanda · DZ 4h; a broken one told so, eg: Demanda rompida,
 * as it holds no more. The other states show in its fill, see
 * drawSupplyDemand, and in the decision.
 */
export function zoneLabel(drawing: Drawing, timeframe: string): string {
  const kind = zoneKinds[drawing.kind] ?? drawing.kind;
  const head = drawing.state === ZoneBroken ? `${kind} ${zoneStateLabel(drawing.state)}` : kind;
  const own = drawingLabel(drawing, timeframe);
  return own ? `${head} · ${own}` : head;
}

/**
 * The drawings of an analysis drawn once: a zone of supply or demand of the
 * kind, timeframe and range of one before it left out, as the agent leaves
 * them out of its analyses (see cleanDrawings in agents/llm/drawing.go).
 */
export function uniqueDrawings(drawings: Drawing[]): Drawing[] {
  const seen = new Set<string>();
  return drawings.filter((drawing) => {
    if (!isSupplyDemand(drawing)) return true;
    const key = `${drawing.kind}|${drawing.timeframe}|${drawing.low}|${drawing.high}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

const orderTypes: Record<string, string> = { market: "mercado", limit: "limit", stop: "stop" };

const intentOutcomes: Record<string, string> = {
  applied: "não executada",
  ignored: "ignorada",
  refused: "recusada",
  failed: "falhou",
};

/** The entry of an intent, eg: Compra limit. */
export function intentLabel(intent: Intent): string {
  const side = intent.side === "SELL" ? "Venda" : "Compra";
  return `${side} ${orderTypes[intent.order_type] ?? intent.order_type}`;
}

/** What came of an intent, eg: recusada. */
export const intentOutcome = (outcome: string) => intentOutcomes[outcome] ?? outcome;

/**
 * The shapes of the analysis on the chart of candles, of any timeframe: its
 * drawings snapped to the candle holding their times (a zone or a level from
 * before the first one at the first, a marker before it and the ones that
 * ended before it left out), the zones of supply and demand colored by their
 * kind, the others by their bias, and the prices of its intent from the
 * candle of the analysis, dashed, none of them an order.
 */
export function analysisShapes(
  analysis: Analysis | null | undefined,
  candles: Candle[],
  palette: Palette,
): AnalysisShapes {
  if (!analysis) return noShapes;
  const times = candles.map((c) => c.time);
  const first = times[0];
  if (first === undefined) return noShapes;
  const shapes: AnalysisShapes = { zones: [], levels: [], markers: [] };
  for (const drawing of uniqueDrawings(analysis.drawings)) {
    if (drawing.to != null && drawing.to < first) continue; // ended before the candles shown
    const from = snapTime(times, drawing.from);
    if (from === null) continue;
    const to = drawing.to != null ? snapTime(times, drawing.to) : null;
    const ended = drawing.to != null;
    const zone = drawing.kind === DrawingZone || isSupplyDemand(drawing);
    if (zone && drawing.low != null && drawing.high != null) {
      const supplyDemand = isSupplyDemand(drawing);
      const bullish = supplyDemand ? drawing.kind === DrawingDemand : drawing.bias === "bullish";
      shapes.zones.push({
        kind: drawing.kind as ZoneKind,
        state: supplyDemand ? (drawing.state ?? null) : null,
        from,
        to,
        low: drawing.low,
        high: drawing.high,
        color: supplyDemand
          ? bullish
            ? palette.up
            : palette.down
          : biasColor(drawing.bias, palette),
        label: supplyDemand
          ? zoneLabel(drawing, analysis.timeframe)
          : drawingLabel(drawing, analysis.timeframe),
        ended,
        labelAt: bullish ? "bottom" : "top",
      });
      continue;
    }
    const color = biasColor(drawing.bias, palette);
    const label = drawingLabel(drawing, analysis.timeframe);
    if (drawing.kind === "level" && drawing.price != null) {
      shapes.levels.push({ from, to, price: drawing.price, color, label, ended, intent: false });
    } else if (drawing.kind === "marker" && drawing.price != null && drawing.from >= first) {
      shapes.markers.push({ time: from, price: drawing.price, color, label });
    }
  }

  const intent = analysis.intent;
  const from = snapTime(times, analysis.candle);
  if (intent && from !== null) {
    const outcome = intentOutcome(intent.outcome);
    const prices = [
      [intent.entry, palette.accent, `Intenção: ${intentLabel(intent)} (${outcome})`],
      [intent.stop_loss, palette.down, "SL da intenção"],
      [intent.take_profit, palette.up, "TP da intenção"],
    ] as const;
    for (const [price, color, label] of prices) {
      if (price == null) continue;
      shapes.levels.push({
        from,
        to: null,
        price,
        color,
        label: `${label} ${formatChartPrice(price)}`,
        ended: false,
        intent: true,
      });
    }
  }
  return shapes;
}

/** The side and the type of the entry of an action, eg: BUY_LIMIT, empty for the others. */
function entryOf(action: string): { side: string; type: string } | null {
  switch (action) {
    case "ENTER_LONG":
      return { side: "BUY", type: "market" };
    case "ENTER_SHORT":
      return { side: "SELL", type: "market" };
    case "BUY_LIMIT":
      return { side: "BUY", type: "limit" };
    case "SELL_LIMIT":
      return { side: "SELL", type: "limit" };
    case "BUY_STOP":
      return { side: "BUY", type: "stop" };
    case "SELL_STOP":
      return { side: "SELL", type: "stop" };
  }
  return null;
}

/**
 * The analysis of a decision of the list, to see it on the chart: its
 * drawings, and the entry it proposed (at the market, pending, or the new
 * levels of a pending order) whatever came of it.
 */
export function decisionAnalysis(decision: Decision): Analysis {
  let intent: Intent | undefined;
  const action = decision.action ?? "";
  let entry = entryOf(action);
  if (
    !entry &&
    action === "MODIFY_PENDING" &&
    decision.entry != null &&
    decision.stop_loss != null
  ) {
    const side = decision.stop_loss < decision.entry ? "BUY" : "SELL";
    const above = decision.entry > decision.price;
    entry = { side, type: (side === "BUY") === above ? "stop" : "limit" };
  }
  if (entry && decision.entry != null) {
    intent = {
      action,
      side: entry.side,
      order_type: entry.type,
      entry: decision.entry,
      stop_loss: decision.stop_loss,
      take_profit: decision.take_profit,
      outcome: decision.outcome,
      error: decision.error,
    };
  }
  return {
    pair: decision.pair,
    decision_id: decision.id,
    timeframe: decision.timeframe,
    candle: decision.candle,
    decided_at: decision.decided_at,
    action,
    outcome: decision.outcome,
    drawings: decision.drawings ?? [],
    intent,
  };
}

/**
 * The caption of an analysis on the chart, eg: Análise do LLM 30m 08/10/2026
 * 14:30: 5 marcações (1 oferta, 2 demanda); compra limite aplicada: the
 * timeframe of its candles, whatever the one of the chart.
 */
export function analysisCaption(analysis: Analysis): string {
  const head = ["Análise do LLM", analysis.timeframe, formatTime(analysis.decided_at)]
    .filter(Boolean)
    .join(" ");
  const parts: string[] = [];
  const drawings = uniqueDrawings(analysis.drawings);
  if (drawings.length > 0) {
    const supply = drawings.filter((d) => d.kind === DrawingSupply).length;
    const demand = drawings.filter((d) => d.kind === DrawingDemand).length;
    const zones = [supply ? `${supply} oferta` : "", demand ? `${demand} demanda` : ""]
      .filter(Boolean)
      .join(", ");
    parts.push(
      `${drawings.length} marcaç${drawings.length === 1 ? "ão" : "ões"}${zones ? ` (${zones})` : ""}`,
    );
  }
  if (analysis.intent) {
    const intent = `${intentLabel(analysis.intent)} ${intentOutcome(analysis.intent.outcome)}`;
    parts.push(intent.charAt(0).toLowerCase() + intent.slice(1));
  }
  return parts.length > 0 ? `${head}: ${parts.join("; ")}` : head;
}

const entryTypes = ["LIMIT", "STOP_LOSS", "STOP_LOSS_LIMIT"];

/** Whether the order is a pending entry: a limit or a stop of no group, the legs being of one. */
export const isPendingEntry = (order: Order) =>
  order.group_id == null && entryTypes.includes(order.type);

const isOpen = (order: Order) => order.status === "NEW" || order.status === "PARTIALLY_FILLED";

/** How long after its entry the brokers create the legs of a pending order, in seconds. */
const LEGS_WINDOW = 5;

/**
 * The legs waiting for the fill of a pending entry, by their ID, with the
 * entry: open legs (of a group) on the other side, created with an open
 * pending entry. Once it fills, they are the legs of the position.
 */
export function pendingEntryLegs(orders: Order[]): Map<number, Order> {
  const entries = orders.filter((o) => isPendingEntry(o) && isOpen(o));
  const legs = new Map<number, Order>();
  if (entries.length === 0) return legs;
  for (const order of orders) {
    if (order.group_id == null || !isOpen(order)) continue;
    const entry = entries.find(
      (e) =>
        e.side !== order.side &&
        order.created_at >= e.created_at - 1 &&
        order.created_at <= e.created_at + LEGS_WINDOW,
    );
    if (entry) legs.set(order.id, entry);
  }
  return legs;
}

/** The title of a pending entry on the chart, eg: C LIMIT 0,01 or V STOP 0,02. */
export function pendingEntryTitle(order: Order): string {
  const side = order.side === "BUY" ? "C" : "V";
  return `${side} ${order.type === "LIMIT" ? "LIMIT" : "STOP"} ${formatCompact(order.quantity)}`;
}

/** The price of a pending entry: the one of a limit, the trigger of a stop. */
export const pendingEntryPrice = (order: Order) =>
  order.type === "LIMIT" ? order.price : (order.stop ?? order.price);

const ended = ["FILLED", "CANCELED", "EXPIRED", "REJECTED"];

/**
 * The pending entries that ended, on the chart of candles: their price while
 * they waited, from their creation to their end, solid once filled, dashed
 * and muted when canceled, expired or rejected.
 */
export function endedPendingSegments(
  orders: Order[],
  candles: Candle[],
  palette: Palette,
): { fromTime: number; toTime: number; price: number; color: string; dashed: boolean }[] {
  const times = candles.map((c) => c.time);
  const segments = [];
  for (const order of orders) {
    if (!isPendingEntry(order) || !ended.includes(order.status)) continue;
    const from = snapTime(times, order.created_at);
    const to = snapTime(times, order.updated_at);
    const price = pendingEntryPrice(order);
    if (from === null || to === null || to <= from || !price) continue;
    const filled = order.status === "FILLED";
    segments.push({
      fromTime: from,
      toTime: to,
      price,
      color: filled ? palette.accent : palette.muted,
      dashed: !filled,
    });
  }
  return segments;
}
