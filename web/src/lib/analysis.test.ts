import { describe, expect, it } from "vitest";
import type { Analysis, Candle, Decision, DecisionEvent, Order, Snapshot } from "../api/types";
import {
  analysisCaption,
  analysisShapes,
  decisionAnalysis,
  drawingLabel,
  endedPendingSegments,
  isPendingEntry,
  noShapes,
  pendingEntryLegs,
  pendingEntryPrice,
  pendingEntryTitle,
  snapTime,
  uniqueDrawings,
  zoneLabel,
} from "./analysis";
import { applyAnalysisEvent, decisionActionLabel, decisionTone, drawingPlace } from "./decisions";
import { palettes } from "./theme";

const palette = palettes.dark;

const supply = {
  id: "sz",
  kind: "supply",
  label: "SZ",
  bias: "bearish",
  timeframe: "30m",
  from: 1790001800,
  low: 102,
  high: 103,
  state: "fresh",
};

const demand = {
  id: "dz",
  kind: "demand",
  label: "DZ",
  bias: "bullish",
  timeframe: "4h",
  from: 1789990000,
  low: 97,
  high: 98,
  state: "tested",
};

// candles of 30 minutes from 1790000000
const candles: Candle[] = [0, 1, 2, 3, 4].map((i) => ({
  time: 1790000000 + i * 1800,
  open: 100,
  high: 101,
  low: 99,
  close: 100,
  volume: 10,
}));

function analysis(partial: Partial<Analysis>): Analysis {
  return {
    pair: "BTCUSDm",
    decision_id: "d1",
    timeframe: "30m",
    candle: 1790003600,
    decided_at: 1790005500,
    action: "BUY_LIMIT",
    outcome: "applied",
    drawings: [],
    ...partial,
  };
}

function order(partial: Partial<Order>): Order {
  return {
    id: 1,
    exchange_id: 1,
    pair: "BTCUSDm",
    side: "BUY",
    type: "LIMIT",
    status: "NEW",
    price: 98.5,
    quantity: 0.01,
    fee: 0,
    created_at: 1790001000,
    updated_at: 1790001000,
    candle_time: 0,
    ref_price: 0,
    profit: 0,
    profit_value: 0,
    ...partial,
  };
}

function decision(partial: Partial<Decision>): Decision {
  return {
    id: "d1",
    pair: "BTCUSDm",
    timeframe: "30m",
    candle: 1790003600,
    decided_at: 1790005500,
    trigger: "candle",
    model: "openai gpt-test",
    latency_ms: 1200,
    price: 100,
    confidence: 0.8,
    levels: [],
    notes: [],
    attachments: [],
    outcome: "applied",
    order_ids: [],
    ...partial,
  };
}

describe("analysis on the chart", () => {
  it("snaps a time to the candle that holds it", () => {
    const times = candles.map((c) => c.time);
    expect(snapTime(times, 1790000000)).toBe(1790000000);
    expect(snapTime(times, 1790002000)).toBe(1790001800);
    expect(snapTime(times, 1789000000)).toBe(1790000000); // before the first: the first
    expect(snapTime(times, 1799000000)).toBe(1790007200); // after the last: the last
    expect(snapTime([], 1790000000)).toBeNull();
  });

  it("draws the zones, the levels and the markers by their kind and their bias", () => {
    const shapes = analysisShapes(
      analysis({
        drawings: [
          {
            id: "fvg",
            kind: "zone",
            label: "FVG",
            bias: "bullish",
            timeframe: "4h",
            from: 1790001000,
            low: 98.4,
            high: 98.9,
          },
          {
            id: "ob",
            kind: "zone",
            label: "OB",
            bias: "bearish",
            timeframe: "30m",
            from: 1789000000,
            to: 1790003600,
            low: 101,
            high: 102,
          },
          {
            id: "bsl",
            kind: "level",
            label: "BSL",
            bias: "neutral",
            timeframe: "30m",
            from: 1790000000,
            price: 103,
          },
          {
            id: "choch",
            kind: "marker",
            label: "CHoCH",
            bias: "bullish",
            timeframe: "30m",
            from: 1790005400,
            price: 99.6,
          },
          { id: "bad", kind: "level", label: "x", bias: "bullish", timeframe: "30m", from: 1 },
        ],
      }),
      candles,
      palette,
    );
    expect(shapes.zones).toEqual([
      {
        kind: "zone",
        state: null,
        from: 1790000000,
        to: null,
        low: 98.4,
        high: 98.9,
        color: palette.up,
        label: "FVG 4h",
        ended: false,
        labelAt: "bottom",
      },
      {
        kind: "zone",
        state: null,
        from: 1790000000,
        to: 1790003600,
        low: 101,
        high: 102,
        color: palette.down,
        label: "OB",
        ended: true,
        labelAt: "top",
      },
    ]);
    expect(shapes.levels).toEqual([
      {
        from: 1790000000,
        to: null,
        price: 103,
        color: palette.muted,
        label: "BSL",
        ended: false,
        intent: false,
      },
    ]);
    expect(shapes.markers).toEqual([
      { time: 1790005400, price: 99.6, color: palette.up, label: "CHoCH" },
    ]);
    expect(analysisShapes(null, candles, palette)).toBe(noShapes);
  });

  it("draws the intent dashed, from the candle of the analysis, never as an order", () => {
    const shapes = analysisShapes(
      analysis({
        outcome: "refused",
        intent: {
          action: "BUY_LIMIT",
          side: "BUY",
          order_type: "limit",
          entry: 100.5,
          stop_loss: 99,
          take_profit: 103,
          outcome: "refused",
          error: "must be below the ask",
        },
      }),
      candles,
      palette,
    );
    expect(shapes.levels.map((l) => [l.from, l.price, l.intent, l.color, l.label])).toEqual([
      [1790003600, 100.5, true, palette.accent, "Intenção: Compra limit (recusada) 100,5"],
      [1790003600, 99, true, palette.down, "SL da intenção 99"],
      [1790003600, 103, true, palette.up, "TP da intenção 103"],
    ]);
  });

  it("names the drawings with their timeframe when not the one of the analysis", () => {
    const drawing = {
      id: "x",
      kind: "zone",
      label: "FVG",
      bias: "bullish",
      timeframe: "4h",
      from: 0,
    };
    expect(drawingLabel(drawing, "30m")).toBe("FVG 4h");
    expect(drawingLabel(drawing, "4h")).toBe("FVG");
  });

  it("tells which analysis is drawn", () => {
    expect(analysisCaption(analysis({}))).toBe("Análise do LLM 30m 21/09/2026 15:45");
    expect(
      analysisCaption(
        analysis({
          drawings: [
            { id: "a", kind: "level", label: "EQ", bias: "neutral", timeframe: "30m", from: 0 },
          ],
          intent: { action: "SELL_STOP", side: "SELL", order_type: "stop", outcome: "ignored" },
        }),
      ),
    ).toBe("Análise do LLM 30m 21/09/2026 15:45 · 1 marcação · Venda stop ignorada");
    expect(
      analysisCaption(
        analysis({ drawings: [supply, { ...supply, id: "s2", low: 104, high: 105 }, demand] }),
      ),
    ).toBe("Análise do LLM 30m 21/09/2026 15:45 · 3 marcações (2 oferta, 1 demanda)");
  });

  it("draws the zones of supply and demand by their kind and state, whatever their bias", () => {
    const broken = {
      ...demand,
      id: "d2",
      low: 95,
      high: 96,
      state: "broken",
      to: 1790005400,
      bias: "bearish",
    };
    const shapes = analysisShapes(
      analysis({ drawings: [supply, demand, broken] }),
      candles,
      palette,
    );
    expect(shapes.zones).toEqual([
      {
        kind: "supply",
        state: "fresh",
        from: 1790001800,
        to: null,
        low: 102,
        high: 103,
        color: palette.down,
        label: "Oferta · SZ",
        ended: false,
        labelAt: "top",
      },
      {
        kind: "demand",
        state: "tested",
        from: 1790000000,
        to: null,
        low: 97,
        high: 98,
        color: palette.up,
        label: "Demanda · DZ 4h",
        ended: false,
        labelAt: "bottom",
      },
      {
        kind: "demand",
        state: "broken",
        from: 1790000000,
        to: 1790005400,
        low: 95,
        high: 96,
        color: palette.up,
        label: "Demanda rompida · DZ 4h",
        ended: true,
        labelAt: "bottom",
      },
    ]);
    expect(zoneLabel({ ...supply, label: "", state: undefined }, "30m")).toBe("Oferta");
  });

  it("draws a zone of supply or demand once", () => {
    const again = { ...demand, id: "other", label: "DZ 2" };
    expect(uniqueDrawings([demand, again, supply]).map((d) => d.id)).toEqual(["dz", "sz"]);
    const other = { ...demand, id: "dz15", timeframe: "15m" };
    expect(uniqueDrawings([demand, other])).toHaveLength(2); // of another timeframe
    const fvg = { ...supply, id: "fvg", kind: "zone" };
    expect(uniqueDrawings([fvg, { ...fvg, id: "fvg2" }])).toHaveLength(2); // the structures as they are
    expect(
      analysisShapes(analysis({ drawings: [demand, again] }), candles, palette).zones,
    ).toHaveLength(1);
  });

  it("aligns the drawings to the candles of any timeframe", () => {
    // the same analysis on candles of 4 hours, of 5 minutes and of a day
    const h4: Candle[] = [0, 1].map(
      (i) => ({ ...candles[0], time: 1789992000 + i * 14400 }) as Candle,
    );
    const zones = (list: Candle[]) =>
      analysisShapes(analysis({ drawings: [supply, demand] }), list, palette).zones.map((z) => [
        z.from,
        z.to,
      ]);
    // 1790001800 and 1790000000 in the 4h of 1789992000
    expect(zones(h4)).toEqual([
      [1789992000, null],
      [1789992000, null],
    ]);
    const m5: Candle[] = [0, 1, 2, 3, 4, 5, 6].map(
      (i) => ({ ...candles[0], time: 1790001500 + i * 300 }) as Candle,
    );
    // the supply at its own candle; the demand from before them, at the first
    expect(zones(m5)).toEqual([
      [1790001800, null],
      [1790001500, null],
    ]);
    // a marker before the candles shown and a zone ended before them, left out
    const shapes = analysisShapes(
      analysis({
        drawings: [
          {
            id: "m",
            kind: "marker",
            label: "BOS",
            bias: "bullish",
            timeframe: "30m",
            from: 1790000000,
            price: 99,
          },
          { ...demand, id: "old", state: "broken", to: 1790001200 },
        ],
      }),
      m5,
      palette,
    );
    expect(shapes.markers).toEqual([]);
    expect(shapes.zones).toEqual([]);
    expect(analysisShapes(analysis({ drawings: [supply] }), [], palette)).toBe(noShapes);
  });

  it("shows a decision of the list with the levels it proposed, whatever came of them", () => {
    const shown = decisionAnalysis(
      decision({
        action: "MODIFY_PENDING",
        entry: 98,
        stop_loss: 97,
        take_profit: 102,
        outcome: "refused",
        drawings: [
          { id: "a", kind: "level", label: "EQ", bias: "neutral", timeframe: "30m", from: 0 },
        ],
      }),
    );
    expect(shown.decision_id).toBe("d1");
    expect(shown.drawings).toHaveLength(1);
    expect(shown.intent).toMatchObject({ side: "BUY", order_type: "limit", entry: 98 });
    expect(
      decisionAnalysis(decision({ action: "BUY_STOP", entry: 101, stop_loss: 99 })).intent,
    ).toMatchObject({ side: "BUY", order_type: "stop" });
    expect(decisionAnalysis(decision({ action: "WAIT" })).intent).toBeUndefined();
    expect(decisionAnalysis(decision({ action: "WAIT" })).drawings).toEqual([]);
  });
});

describe("pending orders on the chart", () => {
  const entry = order({ id: 10 });
  const stopEntry = order({ id: 20, side: "SELL", type: "STOP_LOSS", price: 97, stop: 96.5 });
  const profit = order({ id: 11, side: "SELL", type: "LIMIT_MAKER", price: 102, group_id: 5 });
  const stop = order({
    id: 12,
    side: "SELL",
    type: "STOP_LOSS",
    price: 97.5,
    stop: 97.5,
    group_id: 5,
    created_at: 1790001002,
  });
  // a leg of a position, long before
  const held = order({
    id: 3,
    side: "SELL",
    type: "STOP_LOSS",
    price: 95,
    group_id: 1,
    created_at: 1789000000,
  });

  it("tells the entries from the legs", () => {
    expect(isPendingEntry(entry)).toBe(true);
    expect(isPendingEntry(stopEntry)).toBe(true);
    expect(isPendingEntry(stop)).toBe(false);
    expect(pendingEntryTitle(entry)).toBe("C LIMIT 0,01");
    expect(pendingEntryTitle(stopEntry)).toBe("V STOP 0,01");
    expect(pendingEntryPrice(stopEntry)).toBe(96.5);
  });

  it("finds the legs waiting for the fill of a pending entry", () => {
    const legs = pendingEntryLegs([entry, profit, stop, held]);
    expect([...legs.keys()].sort()).toEqual([11, 12]);
    expect(legs.get(11)?.id).toBe(10);
    // filled: the legs of the position
    expect(pendingEntryLegs([{ ...entry, status: "FILLED" }, profit, stop]).size).toBe(0);
  });

  it("draws the pending entries ended over the time they waited", () => {
    const segments = endedPendingSegments(
      [
        { ...entry, status: "FILLED", updated_at: 1790005400 },
        { ...stopEntry, status: "EXPIRED", updated_at: 1790007000 },
        { ...entry, id: 30, status: "CANCELED", updated_at: 1790001100 }, // within its candle
        entry, // waiting: a price line
      ],
      candles,
      palette,
    );
    expect(segments).toEqual([
      {
        fromTime: 1790000000,
        toTime: 1790005400,
        price: 98.5,
        color: palette.accent,
        dashed: false,
      },
      { fromTime: 1790000000, toTime: 1790005400, price: 96.5, color: palette.muted, dashed: true },
    ]);
  });
});

describe("the decisions of the pending orders", () => {
  it("labels and colors them", () => {
    expect(decisionActionLabel("BUY_LIMIT")).toBe("Compra limite");
    expect(decisionActionLabel("CANCEL_PENDING")).toBe("Cancelar pendente");
    expect(decisionTone("BUY_STOP")).toBe("buy");
    expect(decisionTone("SELL_LIMIT")).toBe("sell");
    expect(decisionTone("MODIFY_PENDING")).toBe("");
  });

  it("places a drawing", () => {
    expect(
      drawingPlace({
        id: "a",
        kind: "zone",
        label: "FVG",
        bias: "bullish",
        timeframe: "30m",
        from: 1790000000,
        to: 1790003600,
        low: 98.4,
        high: 98.9,
      }),
    ).toBe("98,4 – 98,9 · desde 21/09/2026 14:13 até 21/09/2026 15:13");
  });

  it("takes the analysis of an event of the pair, the snapshot kept when it is the same", () => {
    const snapshot = { pair: "BTCUSDm", analysis: analysis({}) } as Snapshot;
    const event = (a?: Analysis): DecisionEvent => ({
      pair: "BTCUSDm",
      decision: decision({}),
      analysis: a,
    });
    expect(applyAnalysisEvent(snapshot, event(analysis({})))).toBe(snapshot);
    expect(applyAnalysisEvent(snapshot, event())).toBe(snapshot);
    expect(
      applyAnalysisEvent(snapshot, { ...event(analysis({ decision_id: "d2" })), pair: "X" }),
    ).toBe(snapshot);
    expect(
      applyAnalysisEvent(snapshot, event(analysis({ decision_id: "d2" }))).analysis?.decision_id,
    ).toBe("d2");
  });
});
