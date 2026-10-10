import { describe, expect, it } from "vitest";
import type { Decision } from "../api/types";
import {
  formatSentiment,
  latestSentiment,
  needleAngle,
  sentimentBand,
  sentimentChange,
} from "./sentiment";

function decision(id: string, sentiment?: number, outcome = "applied"): Decision {
  return {
    id,
    pair: "XAUUSDm",
    timeframe: "15m",
    candle: 0,
    decided_at: 0,
    trigger: "interval",
    model: "test",
    latency_ms: 0,
    price: 100,
    confidence: 0.5,
    sentiment,
    levels: [],
    notes: [],
    attachments: [],
    outcome,
    order_ids: [],
  };
}

describe("latestSentiment", () => {
  it("reads the newest decision with a sentiment, and the one before it", () => {
    const reading = latestSentiment([
      decision("d3", undefined, "skipped"),
      decision("d2", 64),
      decision("d1", undefined, "failed"),
      decision("d0", 52),
      decision("old", 10),
    ]);
    expect(reading?.value).toBe(64);
    expect(reading?.decision.id).toBe("d2");
    expect(reading?.previous).toBe(52);
  });

  it("has no previous one with a single reading", () => {
    expect(latestSentiment([decision("d0", 30)])?.previous).toBeUndefined();
  });

  it("is null without any, or with values out of the range only", () => {
    expect(latestSentiment([])).toBeNull();
    expect(latestSentiment([decision("d0"), decision("d1", 120), decision("d2", -5)])).toBeNull();
    expect(latestSentiment([decision("d0", Number.NaN)])).toBeNull();
  });
});

describe("sentimentBand", () => {
  it("splits the range in fifths, neutral around 50", () => {
    expect(sentimentBand(0)).toEqual({ label: "Muito baixista", tone: "bear-strong" });
    expect(sentimentBand(19.9).tone).toBe("bear-strong");
    expect(sentimentBand(20).tone).toBe("bear");
    expect(sentimentBand(39.9).tone).toBe("bear");
    expect(sentimentBand(40)).toEqual({ label: "Neutro", tone: "neutral" });
    expect(sentimentBand(50).tone).toBe("neutral");
    expect(sentimentBand(60).tone).toBe("neutral");
    expect(sentimentBand(60.1)).toEqual({ label: "Altista", tone: "bull" });
    expect(sentimentBand(80).tone).toBe("bull");
    expect(sentimentBand(80.1)).toEqual({ label: "Muito altista", tone: "bull-strong" });
    expect(sentimentBand(100).tone).toBe("bull-strong");
  });
});

describe("formatSentiment and sentimentChange", () => {
  it("shows whole values and signed changes", () => {
    expect(formatSentiment(62.4)).toBe("62");
    expect(formatSentiment(62.5)).toBe("63");
    const reading = { value: 64, decision: decision("d1", 64), previous: 52 };
    expect(sentimentChange(reading)).toBe("+12");
    expect(sentimentChange({ ...reading, previous: 70 })).toBe("−6");
    expect(sentimentChange({ ...reading, previous: 64.2 })).toBeNull();
    expect(sentimentChange({ ...reading, previous: undefined })).toBeNull();
  });
});

describe("needleAngle", () => {
  it("turns from the left through the top to the right", () => {
    expect(needleAngle(0)).toBe(180);
    expect(needleAngle(50)).toBe(270);
    expect(needleAngle(100)).toBe(360);
    expect(needleAngle(-10)).toBe(180);
    expect(needleAngle(150)).toBe(360);
  });
});
