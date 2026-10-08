import type { CandleEvent, Order, OrderEvent, Point, Snapshot } from "../api/types";

export interface Stats {
  /** Closed trades (orders that carry a profit). */
  trades: number;
  wins: number;
  losses: number;
  /** 0..1 */
  winRate: number;
  /** Sum of the profits, in profitCurrency. */
  totalProfit: number;
  /**
   * The currency of the profits: the account's when the broker booked all of
   * them (account_profit), or reports the account without any trade yet (eg:
   * MT5), else the quote (profit_value, the core's).
   */
  profitCurrency: string;
  /** Gross profit / gross loss; Infinity when there are no losses. */
  profitFactor: number;
  /** The sums of the wins and of the losses (positive), in profitCurrency. */
  grossProfit: number;
  grossLoss: number;
  /** Average profit per closed trade (fraction, e.g. 0.012). */
  avgReturn: number;
  initialEquity: number;
  finalEquity: number;
  /** (final - initial) / initial, or 0 without equity data. */
  returnPct: number;
  /** Largest peak-to-trough decline of the equity curve (fraction). */
  maxDrawdown: number;
  /** Buy-and-hold return of the asset over the same candles (fraction). */
  buyAndHold: number;
}

export function isClosedTrade(order: Order): boolean {
  return order.status === "FILLED" && (order.profit !== 0 || order.profit_value !== 0);
}

/** When the order filled, in Unix seconds: its last update, else its creation. */
export function closedAt(order: Order): number {
  return order.updated_at || order.created_at;
}

/**
 * The trades counted, by their close: from since (the start of the accounting
 * period of the agent, see AgentPeriod) until before until, each optional.
 */
export interface StatsWindow {
  since?: number;
  until?: number;
}

/** Whether the trade closed within window. */
function within(order: Order, window: StatsWindow): boolean {
  const at = closedAt(order);
  return (
    (window.since === undefined || at >= window.since) &&
    (window.until === undefined || at < window.until)
  );
}

export function maxDrawdown(points: Point[]): number {
  let peak = Number.NEGATIVE_INFINITY;
  let worst = 0;
  for (const p of points) {
    if (p.value > peak) peak = p.value;
    if (peak > 0) {
      const dd = (peak - p.value) / peak;
      if (dd > worst) worst = dd;
    }
  }
  return worst;
}

/**
 * The stats of the snapshot: of the trades closed within window, all of them
 * by default; the equity and buy & hold over the candles, whatever it.
 */
export function computeStats(snapshot: Snapshot, window: StatsWindow = {}): Stats {
  const closed = snapshot.orders.filter((o) => isClosedTrade(o) && within(o, window));
  // as the account booked them when it did for all, else the core's
  const booked = closed.every((o) => o.account_profit !== undefined);
  const value = (o: Order) => (booked ? (o.account_profit ?? 0) : o.profit_value);
  const profitCurrency = booked
    ? (closed[0]?.account_currency ?? snapshot.account_currency ?? snapshot.quote)
    : snapshot.quote;
  const wins = closed.filter((o) => value(o) > 0 || (value(o) === 0 && o.profit > 0));
  const losses = closed.filter((o) => value(o) < 0 || (value(o) === 0 && o.profit < 0));
  const grossProfit = wins.reduce((sum, o) => sum + value(o), 0);
  const grossLoss = Math.abs(losses.reduce((sum, o) => sum + value(o), 0));
  const totalProfit = closed.reduce((sum, o) => sum + value(o), 0);
  const avgReturn = closed.length
    ? closed.reduce((sum, o) => sum + o.profit, 0) / closed.length
    : 0;

  const equity = snapshot.equity_values;
  const initialEquity = equity[0]?.value ?? 0;
  const finalEquity = equity[equity.length - 1]?.value ?? 0;
  const returnPct = initialEquity > 0 ? (finalEquity - initialEquity) / initialEquity : 0;

  const firstClose = snapshot.candles[0]?.close ?? 0;
  const lastClose = snapshot.candles[snapshot.candles.length - 1]?.close ?? 0;
  const buyAndHold = firstClose > 0 ? (lastClose - firstClose) / firstClose : 0;

  // The API reports the drawdown as a negative fraction (e.g. -0.117).
  const drawdown =
    snapshot.max_drawdown && snapshot.max_drawdown.value !== 0
      ? Math.abs(snapshot.max_drawdown.value)
      : maxDrawdown(equity);

  return {
    trades: closed.length,
    wins: wins.length,
    losses: losses.length,
    winRate: closed.length ? wins.length / closed.length : 0,
    totalProfit,
    profitCurrency,
    profitFactor:
      grossLoss > 0 ? grossProfit / grossLoss : grossProfit > 0 ? Number.POSITIVE_INFINITY : 0,
    grossProfit,
    grossLoss,
    avgReturn,
    initialEquity,
    finalEquity,
    returnPct,
    maxDrawdown: drawdown,
    buyAndHold,
  };
}

/** Appends a point when its time is newer, or replaces the last one when equal. */
export function upsertPoint(points: Point[], point: Point): Point[] {
  const last = points[points.length - 1];
  if (!last || point.time > last.time) return [...points, point];
  if (point.time === last.time) return [...points.slice(0, -1), point];
  return points;
}

/** Applies an SSE candle event to a snapshot immutably. */
export function applyCandleEvent(snapshot: Snapshot, event: CandleEvent): Snapshot {
  if (event.pair !== snapshot.pair) return snapshot;
  const last = snapshot.candles[snapshot.candles.length - 1];
  if (last && event.candle.time < last.time) return snapshot;

  // The candle in progress is sent again as it changes, then at its close:
  // the one of the time of the last candle replaces it.
  const candles =
    last && event.candle.time === last.time
      ? [...snapshot.candles.slice(0, -1), event.candle]
      : [...snapshot.candles, event.candle];
  // the levels of the strategy come with the candles, as it analyzes them
  const levels = event.levels ?? snapshot.levels;
  const position = event.position ?? snapshot.position;
  if (event.partial) return { ...snapshot, candles, levels, position };

  const updates = new Map(event.indicators.map((i) => [i.name, i.metrics]));
  const indicators = snapshot.indicators.map((indicator) => {
    const metricUpdates = updates.get(indicator.name);
    if (!metricUpdates) return indicator;
    return {
      ...indicator,
      metrics: indicator.metrics.map((metric) => {
        const update = metricUpdates.find((m) => m.name === metric.name);
        return update ? { ...metric, points: upsertPoint(metric.points, update.point) } : metric;
      }),
    };
  });

  return {
    ...snapshot,
    candles,
    levels,
    position,
    indicators,
    equity_values: event.equity
      ? upsertPoint(snapshot.equity_values, event.equity)
      : snapshot.equity_values,
    asset_values: event.asset
      ? upsertPoint(snapshot.asset_values, event.asset)
      : snapshot.asset_values,
  };
}

/**
 * The indicators a candle event sends, by group and metric, sorted: its key.
 * Two events of the same indicators have the same key.
 */
export function indicatorsKey(event: CandleEvent): string {
  const keys = event.indicators.flatMap((i) => i.metrics.map((m) => `${i.name}\u0000${m.name}`));
  return keys.sort().join("\n");
}

/**
 * Whether a closed candle sends other indicators than the ones of the
 * snapshot, eg: the prompt of the strategy switched: the snapshot is then
 * loaded again. The metrics of the snapshot without points, which the events
 * don't send, are left out.
 */
export function indicatorsChanged(snapshot: Snapshot, event: CandleEvent): boolean {
  if (event.partial || event.pair !== snapshot.pair) return false;
  const shown = snapshot.indicators.flatMap((i) =>
    i.metrics.filter((m) => m.points.length > 0).map((m) => `${i.name}\u0000${m.name}`),
  );
  return shown.sort().join("\n") !== indicatorsKey(event);
}

/** Applies an SSE order event to a snapshot immutably (insert or replace by id). */
export function applyOrderEvent(snapshot: Snapshot, event: OrderEvent): Snapshot {
  if (event.pair !== snapshot.pair) return snapshot;
  const index = snapshot.orders.findIndex((o) => o.id === event.order.id);
  const orders = [...snapshot.orders];
  const previous = orders[index];
  if (previous) {
    // the decision of the order, linked by a decision event sent before an
    // order event computed before the link, is kept
    orders[index] = {
      ...event.order,
      decision_id: event.order.decision_id ?? previous.decision_id,
    };
  } else orders.push(event.order);
  return { ...snapshot, orders };
}

/** The position the filled orders of a pair leave open, see openPosition. */
export interface OpenPosition {
  side: "BUY" | "SELL";
  quantity: number;
  /** Average price of the fills still open. */
  entry: number;
}

/**
 * Returns the position the filled orders leave open, null when flat: their
 * net quantity, and the average price of the fills since the position was
 * last flat (a reduction keeps it, a reversal starts at its price). The orders
 * are the whole history of the bot, kept across its restarts.
 */
export function openPosition(orders: Order[]): OpenPosition | null {
  const fills = orders
    .filter((o) => o.status === "FILLED" && o.quantity > 0 && o.price > 0)
    .sort((a, b) => a.updated_at - b.updated_at || a.id - b.id);
  let net = 0; // signed: positive long
  let entry = 0;
  for (const fill of fills) {
    const signed = fill.side === "BUY" ? fill.quantity : -fill.quantity;
    if (net === 0 || Math.sign(net) === Math.sign(signed)) {
      entry =
        (entry * Math.abs(net) + fill.price * Math.abs(signed)) /
        (Math.abs(net) + Math.abs(signed));
      net += signed;
    } else if (Math.abs(signed) <= Math.abs(net)) {
      net += signed; // a reduction: the entry of the rest unchanged
    } else {
      net += signed; // a reversal: the rest opened at its price
      entry = fill.price;
    }
    if (Math.abs(net) < 1e-9) {
      net = 0;
      entry = 0;
    }
  }
  if (net === 0) return null;
  return { side: net > 0 ? "BUY" : "SELL", quantity: Math.abs(net), entry };
}

/** The profit of the open position at price, in the quote currency. */
export function positionProfit(position: OpenPosition, price: number): number {
  const sign = position.side === "BUY" ? 1 : -1;
  return sign * (price - position.entry) * position.quantity;
}

/** The open position of a snapshot and its profit, see positionPnL. */
export interface PositionPnL {
  side: string;
  quantity: number;
  entry: number;
  profit: number;
  currency: string;
}

/**
 * Returns the open position of the snapshot and its profit: as the broker
 * reports it when it does (MT5: the profit of the terminal, in the currency of
 * the account, the swap included), else left by the filled orders and valued
 * at the last price, in the quote. Null when flat.
 */
export function positionPnL(snapshot: Snapshot): PositionPnL | null {
  const broker = snapshot.position;
  if (broker) {
    if (!broker.open || !broker.entry || !broker.quantity) return null;
    return {
      side: broker.side ?? "BUY",
      quantity: broker.quantity,
      entry: broker.entry,
      profit: broker.profit,
      currency: broker.currency ?? snapshot.quote,
    };
  }
  const position = openPosition(snapshot.orders);
  const last = snapshot.candles[snapshot.candles.length - 1]?.close ?? 0;
  if (!position || last <= 0) return null;
  return { ...position, profit: positionProfit(position, last), currency: snapshot.quote };
}
