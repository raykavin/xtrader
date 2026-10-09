import { describe, expect, it } from "vitest";
import type { Candle, CandlesResponse, Order, Snapshot } from "../api/types";
import {
  anchorOrders,
  applyViewPrice,
  candleUpdates,
  freshGap,
  mergeView,
  shownTimeframe,
  timeframeLabel,
  timeframeSeconds,
  viewKey,
  viewOf,
  viewSnapshot,
  viewTimeframes,
} from "./timeframes";

const requested = ["1m", "5m", "15m", "30m", "4h", "1d"];

function candle(time: number, close = 100): Candle {
  return { time, open: close, high: close + 1, low: close - 1, close, volume: 1 };
}

function snapshot(partial: Partial<Snapshot>): Snapshot {
  return {
    pair: "BTCUSDm",
    candles: [candle(1790000000)],
    indicators: [{ name: "EMA", overlay: true, metrics: [] }],
    orders: [],
    equity_values: [],
    asset_values: [],
    ...partial,
  } as Snapshot;
}

function response(timeframe: string, candles: Candle[]): CandlesResponse {
  return {
    pair: "BTCUSDm",
    timeframe,
    execution_timeframe: "30m",
    source: "broker",
    candles,
  };
}

const views = requested.map((timeframe) => ({
  timeframe,
  execution: timeframe === "30m",
  available: true,
}));

describe("the timeframes of the chart", () => {
  it("names and measures every timeframe asked for", () => {
    expect(requested.map(timeframeLabel)).toEqual(["1m", "5m", "15m", "30m", "4h", "D"]);
    expect(requested.map(timeframeSeconds)).toEqual([60, 300, 900, 1800, 14400, 86400]);
    expect(timeframeSeconds("1M")).toBe(0); // the months, of no fixed length
  });

  it("shows the timeframe asked when available, else the one of the strategy", () => {
    const s = snapshot({ timeframe: "30m", timeframes: views });
    for (const timeframe of requested) expect(shownTimeframe(s, timeframe)).toBe(timeframe);
    expect(shownTimeframe(s, null)).toBe("30m");
    expect(shownTimeframe(s, "2h")).toBe("30m");
    const below = snapshot({
      timeframe: "30m",
      timeframes: views.map((v) => ({
        ...v,
        available: !["1m", "5m", "15m"].includes(v.timeframe),
      })),
    });
    expect(shownTimeframe(below, "5m")).toBe("30m"); // no source: below it unavailable
    // an agent of a version before: the one of the strategy alone
    expect(viewTimeframes(snapshot({ timeframe: "15m" }))).toEqual([
      { timeframe: "15m", execution: true, available: true },
    ]);
    expect(shownTimeframe(snapshot({}), "4h")).toBe("");
  });

  it("keys the candles by agent, pair and timeframe", () => {
    const keys = new Set(requested.map((tf) => viewKey("llm", "BTCUSDm", tf)));
    expect(keys.size).toBe(requested.length);
    expect(viewKey("llm", "BTCUSDm", "4h")).not.toBe(viewKey("llm", "XAUUSDm", "4h"));
    expect(viewKey("llm", "BTCUSDm", "4h")).not.toBe(viewKey("smc", "BTCUSDm", "4h"));
  });
});

describe("the candles of the timeframe shown", () => {
  const key = viewKey("llm", "BTCUSDm", "5m");
  const view = viewOf(key, response("5m", [candle(1790000100), candle(1790000400)]));

  it("merges the candles read again, a late answer of another timeframe left out", () => {
    const merged = mergeView(view, key, [candle(1790000400, 105), candle(1790000700, 106)]);
    expect(merged?.candles.map((c) => [c.time, c.close])).toEqual([
      [1790000100, 100],
      [1790000400, 105],
      [1790000700, 106],
    ]);
    const other = viewKey("llm", "BTCUSDm", "4h");
    expect(mergeView(view, other, [candle(1790000700, 1)])).toBe(view);
    expect(mergeView(null, key, [candle(1790000700)])).toBeNull();
    expect(freshGap(view, [candle(1790000400)])).toBe(false);
    expect(freshGap(view, [candle(1790001000)])).toBe(true);
  });

  it("takes the prices of the strategy in the candle in progress, until its period ends", () => {
    const up = applyViewPrice(view, 103, 1790000500);
    expect(up.stale).toBe(false);
    expect(up.view.candles[1]).toMatchObject({ close: 103, high: 103, low: 99 });
    const down = applyViewPrice(up.view, 97, 1790000600);
    expect(down.view.candles[1]).toMatchObject({ close: 97, high: 103, low: 97 });
    expect(applyViewPrice(view, 100, 1790000500).view).toBe(view); // the same close
    expect(applyViewPrice(view, 103, 1790000700)).toEqual({ view, stale: true }); // the next 5m
    expect(applyViewPrice(view, Number.NaN, 1790000500).view).toBe(view);
  });

  it("anchors the orders to the candles shown", () => {
    const orders = [
      { id: 1, status: "FILLED", candle_time: 1790000000, updated_at: 1790000500 },
      { id: 2, status: "FILLED", candle_time: 1789000000, updated_at: 1789000100 },
    ] as Order[];
    const anchored = anchorOrders(orders, view.candles);
    expect(anchored[0]?.candle_time).toBe(1790000400); // the 5m that holds its fill
    expect(anchored[1]).toBe(orders[1]); // before the candles shown: as it is
  });

  it("draws the snapshot on the candles shown, without the indicators of the strategy", () => {
    const s = snapshot({ timeframe: "30m" });
    const shown = viewSnapshot(s, view);
    expect(shown.candles).toBe(view.candles);
    expect(shown.indicators).toEqual([]);
    expect(shown.pair).toBe("BTCUSDm");
    expect(viewSnapshot(s, null)).toBe(s);
    expect(viewSnapshot(snapshot({ pair: "XAUUSDm" }), view).candles).not.toBe(view.candles);
  });

  it("redraws the chart whole on another timeframe, else the candles changed", () => {
    const drawn = [candle(1), candle(2), candle(3)];
    expect(candleUpdates(drawn, [candle(1), candle(2), candle(3, 101)])).toEqual([
      { index: 2, historical: false },
    ]);
    expect(candleUpdates(drawn, [candle(1, 99), candle(2), candle(3), candle(4)])).toEqual([
      { index: 0, historical: true },
      { index: 2, historical: false },
      { index: 3, historical: false },
    ]);
    // the candles of another timeframe, of as many
    expect(candleUpdates(drawn, [candle(10), candle(20), candle(30)])).toBeNull();
    expect(candleUpdates(drawn, [candle(1)])).toBeNull();
    expect(
      candleUpdates(drawn, [...drawn, ...[4, 5, 6, 7, 8, 9].map((t) => candle(t))]),
    ).toBeNull();
    expect(candleUpdates([], [candle(1)])).toEqual([{ index: 0, historical: false }]);
  });
});
