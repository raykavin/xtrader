import type { ControlOrderResponse, Order, Snapshot } from "../api/types";
import { formatQuantity } from "./format";

export type Side = "buy" | "sell";

/**
 * The levels of the order ticket drawn on the chart: the price of a limit
 * order, the stop loss and the take profit, null when unset.
 */
export interface TicketLevels {
  entry: number | null;
  stop: number | null;
  profit: number | null;
}

export const noLevels: TicketLevels = { entry: null, stop: null, profit: null };

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

const sign = (side: Side) => (side === "buy" ? 1 : -1);

/** A stop loss 0,5% from price, on the losing side of the order. */
export function defaultStop(side: Side, price: number): number {
  return roundPrice(price * (1 - sign(side) * 0.005), price);
}

/** A take profit 1% from price, on the winning side of the order. */
export function defaultProfit(side: Side, price: number): number {
  return roundPrice(price * (1 + sign(side) * 0.01), price);
}

/**
 * Why the stop loss and take profit of an order of side entering at entry
 * would be refused, as the brokers check them, or null.
 */
export function levelsError(
  side: Side,
  entry: number,
  stop: number | null,
  profit: number | null,
): string | null {
  const order = side === "buy" ? "uma compra" : "uma venda";
  if (stop !== null && sign(side) * stop >= sign(side) * entry) {
    return `O stop loss de ${order} deve ficar ${side === "buy" ? "abaixo" : "acima"} da entrada`;
  }
  if (profit !== null && sign(side) * profit <= sign(side) * entry) {
    return `O take profit de ${order} deve ficar ${side === "buy" ? "acima" : "abaixo"} da entrada`;
  }
  return null;
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
 * Applies an order returned by the control panel, unless the order events told
 * its outcome meanwhile: it replaces a pending order only.
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

/** What the ticket tells of an order created: "warn" when it was created short of what was asked. */
export interface OrderFeedback {
  tone: "ok" | "warn";
  text: string;
}

/**
 * The feedback of an order created by the ticket: a warning when it was
 * created short of what was asked, eg: the entry filled but its stop loss or
 * take profit failing, the position left without them, which another order
 * would double rather than protect.
 */
export function orderFeedback(
  order: ControlOrderResponse,
  side: Side,
  asset: string,
): OrderFeedback {
  const text = `Ordem de ${side === "buy" ? "compra" : "venda"} #${order.id} criada — ${formatQuantity(
    order.quantity,
  )} ${asset}`;
  if (!order.warning) return { tone: "ok", text };
  return {
    tone: "warn",
    text: `${text}, mas o stop loss ou o take profit pedido não foi criado. Confira a posição: ela pode estar desprotegida (${order.warning})`,
  };
}
