import { describe, expect, it } from "vitest";
import type { Order, Snapshot } from "../api/types";
import {
  applyPendingOrder,
  defaultProfit,
  defaultStop,
  levelsError,
  pendingLines,
  priceStep,
  roundPrice,
} from "./ticket";

function order(partial: Partial<Order>): Order {
  return {
    id: 1,
    exchange_id: 1,
    pair: "GOLD",
    side: "BUY",
    type: "LIMIT",
    status: "NEW",
    price: 2400,
    quantity: 0.01,
    fee: 0,
    created_at: 0,
    updated_at: 0,
    candle_time: 0,
    ref_price: 0,
    profit: 0,
    profit_value: 0,
    ...partial,
  };
}

describe("ticket", () => {
  it("rounds the prices to 5 significant digits", () => {
    expect(priceStep(2400)).toBeCloseTo(0.1);
    expect(priceStep(84500)).toBeCloseTo(1);
    expect(priceStep(1.0854)).toBeCloseTo(0.0001);
    expect(roundPrice(2401.2367)).toBe(2401.2);
    expect(roundPrice(84512.7)).toBe(84513);
    expect(roundPrice(1.085432)).toBe(1.0854);
    expect(roundPrice(2399.96, 2400)).toBe(2400);
  });

  it("places the default levels on the side of the order", () => {
    expect(defaultStop("buy", 2400)).toBe(2388);
    expect(defaultProfit("buy", 2400)).toBe(2424);
    expect(defaultStop("sell", 2400)).toBe(2412);
    expect(defaultProfit("sell", 2400)).toBe(2376);
  });

  it("checks the levels as the brokers do", () => {
    expect(levelsError("buy", 2400, 2388, 2424)).toBeNull();
    expect(levelsError("buy", 2400, null, null)).toBeNull();
    expect(levelsError("buy", 2400, 2400, null)).toContain("stop loss de uma compra fica abaixo");
    expect(levelsError("buy", 2400, null, 2390)).toContain("take profit de uma compra fica acima");
    expect(levelsError("sell", 2400, 2412, 2376)).toBeNull();
    expect(levelsError("sell", 2400, 2390, null)).toContain("stop loss de uma venda fica acima");
    expect(levelsError("sell", 2400, null, 2410)).toContain("take profit de uma venda fica abaixo");
  });

  it("draws the pending orders, movable when the brokers move them", () => {
    const lines = pendingLines([
      order({ id: 1, type: "LIMIT", price: 2390 }),
      order({ id: 2, type: "LIMIT_MAKER", price: 2450, group_id: 9 }),
      order({ id: 3, type: "STOP_LOSS", price: 2379, stop: 2380, group_id: 9 }),
      order({ id: 4, type: "STOP_LOSS", price: 2370 }), // a plain stop order
      order({ id: 5, type: "LIMIT", status: "FILLED" }),
      order({ id: 6, type: "MARKET" }),
    ]);
    expect(lines.map((l) => [l.order.id, l.kind, l.price, l.movable])).toEqual([
      [1, "limit", 2390, true],
      [2, "profit", 2450, true],
      [3, "stop", 2380, true],
      [4, "stop", 2370, false],
    ]);
  });

  it("applies an order of the panel over a pending one only", () => {
    const snapshot = {
      pair: "GOLD",
      orders: [order({ id: 1 }), order({ id: 2, status: "CANCELED" })],
    } as Snapshot;
    const moved = applyPendingOrder(snapshot, order({ id: 1, price: 2395 }));
    expect(moved.orders[0]?.price).toBe(2395);
    expect(applyPendingOrder(snapshot, order({ id: 2, status: "PENDING_CANCEL" }))).toBe(snapshot);
    expect(applyPendingOrder(snapshot, order({ id: 3 }))).toBe(snapshot);
    expect(applyPendingOrder(snapshot, order({ id: 1, pair: "BTCUSD" }))).toBe(snapshot);
  });
});
