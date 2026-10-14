import { describe, expect, it } from "vitest";
import type { Order, Snapshot } from "../api/types";
import {
  applyCandleEvent,
  applyOrderEvent,
  computeStats,
  indicatorsChanged,
  indicatorsKey,
  maxDrawdown,
  openPosition,
  positionPnL,
  positionProfit,
  upsertPoint,
} from "./stats";

function order(partial: Partial<Order>): Order {
  return {
    id: 1,
    exchange_id: 1,
    pair: "BTCUSDT",
    side: "BUY",
    type: "MARKET",
    status: "FILLED",
    price: 100,
    quantity: 1,
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

function snapshot(partial: Partial<Snapshot> = {}): Snapshot {
  return {
    levels: null,
    pair: "BTCUSDT",
    asset: "BTC",
    quote: "USDT",
    candles: [],
    indicators: [],
    orders: [],
    equity_values: [],
    asset_values: [],
    ...partial,
  };
}

describe("computeStats within a period", () => {
  // closed at 100 (-10), 200 (+30, opened at 50) and 300 (+5); 150 an entry, without a profit
  const orders = [
    order({ id: 1, profit: -0.01, profit_value: -10, created_at: 90, updated_at: 100 }),
    order({ id: 2, created_at: 150, updated_at: 150 }),
    order({ id: 3, profit: 0.03, profit_value: 30, created_at: 50, updated_at: 200 }),
    order({ id: 4, profit: 0.005, profit_value: 5, created_at: 300, updated_at: 0 }),
  ];

  it("counts the trades closed from the start of the period, by their close", () => {
    const stats = computeStats(snapshot({ orders }), { since: 200 });
    expect(stats.trades).toBe(2);
    expect(stats.wins).toBe(2);
    expect(stats.losses).toBe(0);
    expect(stats.totalProfit).toBe(35);
    expect(stats.profitFactor).toBe(Number.POSITIVE_INFINITY);
    expect(stats.grossProfit).toBe(35);
    expect(stats.grossLoss).toBe(0);
  });

  it("counts the ones of a period ended, before the next one", () => {
    const stats = computeStats(snapshot({ orders }), { since: 100, until: 200 });
    expect(stats.trades).toBe(1);
    expect(stats.totalProfit).toBe(-10);
    expect(stats.grossLoss).toBe(10);
  });

  it("counts them all without a period", () => {
    const stats = computeStats(snapshot({ orders }));
    expect(stats.trades).toBe(3);
    expect(stats.totalProfit).toBe(25);
    expect(stats.profitFactor).toBe(3.5);
  });
});

describe("maxDrawdown", () => {
  it("returns 0 for flat or rising curves", () => {
    expect(maxDrawdown([])).toBe(0);
    expect(
      maxDrawdown([
        { time: 1, value: 100 },
        { time: 2, value: 110 },
      ]),
    ).toBe(0);
  });

  it("finds the largest peak to trough", () => {
    const dd = maxDrawdown([
      { time: 1, value: 100 },
      { time: 2, value: 80 },
      { time: 3, value: 120 },
      { time: 4, value: 90 },
      { time: 5, value: 130 },
    ]);
    expect(dd).toBeCloseTo(0.25);
  });
});

describe("computeStats", () => {
  it("handles an empty snapshot", () => {
    const s = computeStats(snapshot());
    expect(s.trades).toBe(0);
    expect(s.winRate).toBe(0);
    expect(s.profitFactor).toBe(0);
    expect(s.returnPct).toBe(0);
    expect(s.maxDrawdown).toBe(0);
  });

  it("aggregates closed trades and equity", () => {
    const s = computeStats(
      snapshot({
        candles: [
          { time: 1, open: 1, high: 1, low: 1, close: 100, volume: 1 },
          { time: 2, open: 1, high: 1, low: 1, close: 150, volume: 1 },
        ],
        orders: [
          order({ id: 1 }), // opening order, no profit
          order({ id: 2, side: "SELL", profit: 0.1, profit_value: 10 }),
          order({ id: 3, side: "SELL", profit: -0.05, profit_value: -5 }),
          order({ id: 4, side: "SELL", profit: 0.2, profit_value: 20 }),
          order({ id: 5, side: "SELL", status: "CANCELED", profit: 1, profit_value: 100 }),
        ],
        equity_values: [
          { time: 1, value: 1000 },
          { time: 2, value: 900 },
          { time: 3, value: 1200 },
        ],
        max_drawdown: { value: -0.1, start: 1, end: 2 },
      }),
    );
    expect(s.trades).toBe(3);
    expect(s.wins).toBe(2);
    expect(s.losses).toBe(1);
    expect(s.winRate).toBeCloseTo(2 / 3);
    expect(s.totalProfit).toBe(25);
    expect(s.profitFactor).toBeCloseTo(6);
    expect(s.avgReturn).toBeCloseTo(0.25 / 3);
    expect(s.initialEquity).toBe(1000);
    expect(s.finalEquity).toBe(1200);
    expect(s.returnPct).toBeCloseTo(0.2);
    expect(s.maxDrawdown).toBeCloseTo(0.1);
    expect(s.buyAndHold).toBeCloseTo(0.5);
  });

  it("follows the results trade by trade, by their close, up to the ones of all", () => {
    const s = computeStats(
      snapshot({
        orders: [
          order({ id: 1, profit: 0.2, profit_value: 20, updated_at: 30 }),
          order({ id: 2, profit: 0.1, profit_value: 10, updated_at: 10 }),
          order({ id: 3, profit: -0.05, profit_value: -5, updated_at: 20 }),
          order({ id: 4, created_at: 5 }), // an entry, no trade
        ],
      }),
    );
    const values = (points: { value: number }[]) => points.map((p) => p.value);
    expect(values(s.curves.profit)).toEqual([0, 10, 5, 25]);
    expect(s.curves.profit.map((p) => p.time)).toEqual([10, 10, 20, 30]);
    expect(values(s.curves.winRate)).toEqual([1, 0.5, 2 / 3]);
    // infinite before the first loss: from it only
    expect(values(s.curves.profitFactor)).toEqual([2, 6]);
    expect(s.curves.profit.at(-1)?.value).toBe(s.totalProfit);
    expect(s.curves.winRate.at(-1)?.value).toBe(s.winRate);
    expect(s.curves.profitFactor.at(-1)?.value).toBe(s.profitFactor);
  });

  it("has no curves without trades", () => {
    expect(computeStats(snapshot()).curves).toEqual({ profit: [], winRate: [], profitFactor: [] });
  });

  it("falls back to the equity curve when the API has no drawdown", () => {
    const s = computeStats(
      snapshot({
        equity_values: [
          { time: 1, value: 100 },
          { time: 2, value: 50 },
        ],
      }),
    );
    expect(s.maxDrawdown).toBeCloseTo(0.5);
  });
});

describe("upsertPoint", () => {
  it("appends, replaces or ignores by time", () => {
    const base = [{ time: 1, value: 1 }];
    expect(upsertPoint(base, { time: 2, value: 2 })).toEqual([
      { time: 1, value: 1 },
      { time: 2, value: 2 },
    ]);
    expect(upsertPoint(base, { time: 1, value: 9 })).toEqual([{ time: 1, value: 9 }]);
    expect(upsertPoint(base, { time: 0, value: 9 })).toBe(base);
    expect(upsertPoint([], { time: 0, value: 9 })).toEqual([{ time: 0, value: 9 }]);
  });
});

describe("applyCandleEvent", () => {
  const base = snapshot({
    candles: [{ time: 10, open: 1, high: 1, low: 1, close: 1, volume: 1 }],
    indicators: [
      {
        name: "EMA",
        overlay: true,
        metrics: [{ name: "ema", color: "red", style: "line", points: [{ time: 10, value: 1 }] }],
      },
    ],
    equity_values: [{ time: 10, value: 100 }],
  });

  it("ignores other pairs and stale candles", () => {
    const candle = { time: 20, open: 1, high: 1, low: 1, close: 2, volume: 1 };
    expect(applyCandleEvent(base, { pair: "ETHUSDT", candle, indicators: [], levels: null })).toBe(
      base,
    );
    expect(
      applyCandleEvent(base, {
        pair: "BTCUSDT",
        candle: { ...candle, time: 5 },
        indicators: [],
        levels: null,
      }),
    ).toBe(base);
  });

  it("keeps the levels of the strategy up to date", () => {
    const candle = { time: 20, open: 1, high: 1, low: 1, close: 2, volume: 1 };
    const drawn = applyCandleEvent(base, {
      pair: "BTCUSDT",
      candle,
      indicators: [],
      partial: true,
      levels: [{ kind: "support", price: 0.9 }],
    });
    expect(drawn.levels).toEqual([{ kind: "support", price: 0.9 }]);
    // a strategy without levels sends null: the lines stay
    const kept = applyCandleEvent(drawn, { pair: "BTCUSDT", candle, indicators: [], levels: null });
    expect(kept.levels).toEqual([{ kind: "support", price: 0.9 }]);
    // none seen: cleared
    const cleared = applyCandleEvent(kept, { pair: "BTCUSDT", candle, indicators: [], levels: [] });
    expect(cleared.levels).toEqual([]);
  });

  it("follows the candle in progress, then its close", () => {
    const forming = applyCandleEvent(base, {
      pair: "BTCUSDT",
      candle: { time: 20, open: 1, high: 3, low: 1, close: 2, volume: 1 },
      indicators: [],
      partial: true,
      levels: null,
    });
    expect(forming.candles).toHaveLength(2);
    expect(forming.indicators).toBe(base.indicators);
    expect(forming.equity_values).toBe(base.equity_values);

    const moved = applyCandleEvent(forming, {
      pair: "BTCUSDT",
      candle: { time: 20, open: 1, high: 4, low: 1, close: 3.5, volume: 2 },
      indicators: [],
      partial: true,
      levels: null,
    });
    expect(moved.candles).toHaveLength(2);
    expect(moved.candles[1]?.close).toBe(3.5);

    const closed = applyCandleEvent(moved, {
      pair: "BTCUSDT",
      candle: { time: 20, open: 1, high: 4, low: 1, close: 3, volume: 3 },
      indicators: [{ name: "EMA", metrics: [{ name: "ema", point: { time: 20, value: 2 } }] }],
      equity: { time: 20, value: 105 },
      levels: null,
    });
    expect(closed.candles).toHaveLength(2);
    expect(closed.candles[1]?.close).toBe(3);
    expect(closed.indicators[0]?.metrics[0]?.points).toEqual([
      { time: 10, value: 1 },
      { time: 20, value: 2 },
    ]);
    expect(closed.equity_values).toEqual([
      { time: 10, value: 100 },
      { time: 20, value: 105 },
    ]);
  });

  it("appends candle, indicator points and equity", () => {
    const next = applyCandleEvent(base, {
      pair: "BTCUSDT",
      candle: { time: 20, open: 1, high: 1, low: 1, close: 2, volume: 1 },
      indicators: [{ name: "EMA", metrics: [{ name: "ema", point: { time: 20, value: 1.5 } }] }],
      equity: { time: 20, value: 110 },
      levels: null,
    });
    expect(next.candles).toHaveLength(2);
    expect(next.indicators[0]?.metrics[0]?.points).toEqual([
      { time: 10, value: 1 },
      { time: 20, value: 1.5 },
    ]);
    expect(next.equity_values).toHaveLength(2);
    expect(next.asset_values).toHaveLength(0);
    expect(base.candles).toHaveLength(1); // immutable
  });
});

describe("applyOrderEvent", () => {
  it("inserts new orders and replaces existing ones", () => {
    const base = snapshot({ orders: [order({ id: 1, status: "NEW" })] });
    const inserted = applyOrderEvent(base, { pair: "BTCUSDT", order: order({ id: 2 }) });
    expect(inserted.orders.map((o) => o.id)).toEqual([1, 2]);

    const replaced = applyOrderEvent(inserted, {
      pair: "BTCUSDT",
      order: order({ id: 1, status: "FILLED" }),
    });
    expect(replaced.orders[0]?.status).toBe("FILLED");
    expect(replaced.orders).toHaveLength(2);

    expect(applyOrderEvent(base, { pair: "ETHUSDT", order: order({ id: 3 }) })).toBe(base);
  });
});

describe("openPosition", () => {
  const fill = (id: number, side: string, quantity: number, price: number, at = id) =>
    order({ id, side, quantity, price, updated_at: at, status: "FILLED" });

  it("is null when flat", () => {
    expect(openPosition([])).toBeNull();
    expect(openPosition([fill(1, "BUY", 0.1, 100), fill(2, "SELL", 0.1, 104)])).toBeNull();
  });

  it("follows the entry left open, its legs pending", () => {
    const position = openPosition([
      fill(1, "BUY", 0.1, 84001.49),
      order({
        id: 2,
        side: "SELL",
        type: "LIMIT_MAKER",
        status: "NEW",
        quantity: 0.1,
        price: 84260,
      }),
      order({ id: 3, side: "SELL", type: "STOP_LOSS", status: "NEW", quantity: 0.1, price: 83907 }),
    ]);
    expect(position).toEqual({ side: "BUY", quantity: 0.1, entry: 84001.49 });
    if (!position) return;
    expect(positionProfit(position, 84011.49)).toBeCloseTo(1, 9);
    expect(positionProfit({ ...position, side: "SELL" }, 84011.49)).toBeCloseTo(-1, 9);
  });

  it("averages the adds, keeps the entry on a reduction, restarts on a reversal", () => {
    expect(openPosition([fill(1, "BUY", 1, 100), fill(2, "BUY", 1, 110)])).toEqual({
      side: "BUY",
      quantity: 2,
      entry: 105,
    });
    expect(openPosition([fill(1, "BUY", 2, 100), fill(2, "SELL", 1, 120)])).toEqual({
      side: "BUY",
      quantity: 1,
      entry: 100,
    });
    expect(openPosition([fill(1, "BUY", 1, 100), fill(2, "SELL", 3, 90)])).toEqual({
      side: "SELL",
      quantity: 2,
      entry: 90,
    });
  });

  it("orders the fills by their time", () => {
    // the stop filled after the entry, though listed first
    expect(openPosition([fill(5, "SELL", 1, 95, 20), fill(4, "BUY", 1, 100, 10)])).toBeNull();
  });
});

describe("positionPnL", () => {
  const entry = order({ id: 1, side: "BUY", quantity: 0.1, price: 84000, status: "FILLED" });
  const candles = [{ time: 10, open: 1, high: 1, low: 1, close: 84010, volume: 1 }];

  it("takes the profit of the broker when it reports it", () => {
    const withBroker = snapshot({
      orders: [entry],
      candles,
      position: {
        open: true,
        side: "BUY",
        quantity: 0.1,
        entry: 84001.49,
        profit: 11.92,
        currency: "BRL",
      },
    });
    expect(positionPnL(withBroker)).toEqual({
      side: "BUY",
      quantity: 0.1,
      entry: 84001.49,
      profit: 11.92,
      currency: "BRL",
    });
    expect(positionPnL({ ...withBroker, position: { open: false, profit: 0 } })).toBeNull();
  });

  it("values the filled orders at the last price without a broker", () => {
    const pnl = positionPnL(snapshot({ orders: [entry], candles }));
    expect(pnl?.profit).toBeCloseTo(1, 9);
    expect(pnl?.currency).toBe("USDT");
  });
});

describe("computeStats with the profits the account booked", () => {
  const close = (id: number, profit: number, booked?: number) =>
    order({
      id,
      status: "FILLED",
      profit: profit / 8400,
      profit_value: profit,
      ...(booked === undefined ? {} : { account_profit: booked, account_currency: "BRL" }),
    });

  it("sums them in the currency of the account when it booked them all", () => {
    const stats = computeStats(
      snapshot({ orders: [close(1, -14.49, -75.1), close(2, 12.74, 66.02)] }),
    );
    expect(stats.profitCurrency).toBe("BRL");
    expect(stats.totalProfit).toBeCloseTo(-9.08, 9);
    expect(stats.wins).toBe(1);
    expect(stats.losses).toBe(1);
  });

  it("keeps the ones of the core, in the quote, when some are unknown", () => {
    const stats = computeStats(snapshot({ orders: [close(1, -14.49, -75.1), close(2, 12.74)] }));
    expect(stats.profitCurrency).toBe("USDT");
    expect(stats.totalProfit).toBeCloseTo(-1.75, 9);
    expect(stats.unbooked, "the account not told: nothing awaited from the broker").toBe(0);
    const told = computeStats(
      snapshot({ orders: [close(1, -14.49, -75.1), close(2, 12.74)], account_currency: "BRL" }),
    );
    expect(told.profitCurrency).toBe("USDT");
    expect(told.unbooked).toBe(1);
    expect(
      computeStats(snapshot({ orders: [close(1, -14.49, -75.1)], account_currency: "BRL" }))
        .unbooked,
    ).toBe(0);
  });

  it("shows the currency of the account before any trade", () => {
    const stats = computeStats(snapshot({ orders: [], account_currency: "BRL" }));
    expect(stats.profitCurrency).toBe("BRL");
    expect(stats.totalProfit).toBe(0);
    expect(computeStats(snapshot({ orders: [] })).profitCurrency).toBe("USDT");
  });
});

describe("indicatorsChanged", () => {
  const base = snapshot({
    indicators: [
      {
        name: "LLM",
        overlay: true,
        metrics: [
          { name: "EMA 9", color: "red", style: "line", points: [{ time: 10, value: 1 }] },
          { name: "SMA 200", color: "blue", style: "line", points: [] },
        ],
      },
      {
        name: "ATR 14",
        overlay: false,
        metrics: [{ name: "", color: "red", style: "line", points: [{ time: 10, value: 1 }] }],
      },
    ],
  });
  const candle = { time: 20, open: 1, high: 1, low: 1, close: 2, volume: 1 };
  const event = (indicators: { name: string; metrics: string[] }[], partial = false) => ({
    pair: "BTCUSDT",
    candle,
    levels: null,
    partial,
    indicators: indicators.map((i) => ({
      name: i.name,
      metrics: i.metrics.map((name) => ({ name, point: { time: 20, value: 1 } })),
    })),
  });

  it("tells a closed candle of other indicators", () => {
    const same = event([
      { name: "ATR 14", metrics: [""] },
      { name: "LLM", metrics: ["EMA 9"] },
    ]);
    expect(indicatorsChanged(base, same)).toBe(false);
    const switched = event([
      { name: "LLM", metrics: ["ema_10", "ema_30"] },
      { name: "ATR 14", metrics: [""] },
    ]);
    expect(indicatorsChanged(base, switched)).toBe(true);
    expect(indicatorsChanged(base, event([{ name: "LLM", metrics: ["EMA 9"] }]))).toBe(true);
    expect(indicatorsChanged(base, { ...switched, partial: true })).toBe(false);
    expect(indicatorsChanged(base, { ...switched, pair: "ETHUSDT" })).toBe(false);
  });

  it("keys the indicators of an event whatever their order", () => {
    expect(
      indicatorsKey(
        event([
          { name: "B", metrics: ["y", "x"] },
          { name: "A", metrics: ["z"] },
        ]),
      ),
    ).toBe(
      indicatorsKey(
        event([
          { name: "A", metrics: ["z"] },
          { name: "B", metrics: ["x", "y"] },
        ]),
      ),
    );
  });
});
