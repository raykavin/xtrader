// The pending orders of an agent on the chart: their lines (a limit order at
// its price, a stop loss or take profit at its level), moved by dragging them
// where the brokers move them, and the prices set rounded to their step.

import type { Order, Snapshot } from "../api/types";

/** The step of the prices set on the chart: 5 significant digits, eg: 0,1 at 2.400. */
export function priceStep(price: number): number {
  if (!Number.isFinite(price) || price <= 0) return 0.00001;
  return 10 ** (Math.floor(Math.log10(price)) - 4);
}

/** price rounded to the step at reference, the price of the market. */
export function roundPrice(price: number, reference = price): number {
  const step = priceStep(Math.abs(reference));
  const decimals = Math.max(0, -Math.round(Math.log10(step)));
  return Number((Math.round(price / step) * step).toFixed(decimals));
}

/**
 * A pending order drawn on the chart: a limit order at its price, a stop loss
 * or take profit at its level. Movable when the brokers move it: a limit
 * order, or a leg of a group.
 */
export interface PendingLine {
  order: Order;
  kind: "limit" | "stop" | "profit";
  price: number;
  movable: boolean;
}

export function pendingLines(orders: Order[]): PendingLine[] {
  const lines: PendingLine[] = [];
  for (const order of orders) {
    if (order.status !== "NEW") continue;
    const leg = order.group_id != null;
    switch (order.type) {
      case "LIMIT":
        lines.push({ order, kind: "limit", price: order.price, movable: true });
        break;
      case "LIMIT_MAKER":
        lines.push({ order, kind: "profit", price: order.price, movable: leg });
        break;
      case "STOP_LOSS":
        lines.push({ order, kind: "stop", price: order.stop ?? order.price, movable: leg });
        break;
    }
  }
  return lines;
}

/**
 * Applies an order returned by the controls of the agent (moved or canceled),
 * unless the order events told its outcome meanwhile: it replaces a pending
 * order only.
 */
export function applyPendingOrder(snapshot: Snapshot, order: Order): Snapshot {
  const index = snapshot.orders.findIndex((o) => o.id === order.id);
  if (order.pair !== snapshot.pair || index < 0 || snapshot.orders[index]?.status !== "NEW") {
    return snapshot;
  }
  const orders = [...snapshot.orders];
  orders[index] = order;
  return { ...snapshot, orders };
}
