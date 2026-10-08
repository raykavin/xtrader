import { describe, expect, it } from "vitest";
import type { Snapshot } from "../api/types";
import { decisionsLoad, keepsDecisions, noResyncs, resynced } from "./resync";

function snapshot(pair: string, decisions?: boolean): Snapshot {
  return {
    pair,
    ...(decisions === undefined ? {} : { llm: { model: "openai gpt-5-mini", decisions } }),
  } as Snapshot;
}

describe("resynced", () => {
  it("loads the snapshot, the decisions and the controls again", () => {
    expect(resynced(noResyncs)).toEqual({ snapshot: 1, decisions: 1, controls: 1 });
    expect(resynced(resynced(noResyncs))).toEqual({ snapshot: 2, decisions: 2, controls: 2 });
    expect(noResyncs).toEqual({ snapshot: 0, decisions: 0, controls: 0 });
  });
});

describe("keepsDecisions", () => {
  it("tells a strategy keeping the decisions of a language model, for the pair shown", () => {
    expect(keepsDecisions(snapshot("BTCUSDm", true), "BTCUSDm")).toBe(true);
    expect(keepsDecisions(snapshot("BTCUSDm", false), "BTCUSDm")).toBe(false);
    expect(keepsDecisions(snapshot("BTCUSDm"), "BTCUSDm")).toBe(false);
    expect(keepsDecisions(snapshot("BTCUSDm", true), "XAUUSDm")).toBe(false); // the snapshot of the pair before
    expect(keepsDecisions(null, "BTCUSDm")).toBe(false);
  });
});

describe("decisionsLoad", () => {
  it("clears the list and loads it for a pair first shown", () => {
    expect(decisionsLoad(null, "llm", "BTCUSDm", true)).toEqual({
      key: "llm\nBTCUSDm\ntrue",
      clear: true,
      load: true,
    });
  });

  it("keeps the list shown on a resync, loading it again", () => {
    const shown = decisionsLoad(null, "llm", "BTCUSDm", true).key;
    expect(decisionsLoad(shown, "llm", "BTCUSDm", true)).toEqual({
      key: shown,
      clear: false,
      load: true,
    });
  });

  it("clears the list as the agent, the pair or the strategy change", () => {
    const shown = decisionsLoad(null, "llm", "BTCUSDm", true).key;
    expect(decisionsLoad(shown, "other", "BTCUSDm", true)).toMatchObject({
      clear: true,
      load: true,
    });
    expect(decisionsLoad(shown, "llm", "XAUUSDm", true)).toMatchObject({ clear: true, load: true });
    expect(decisionsLoad(shown, "llm", "BTCUSDm", false)).toMatchObject({
      clear: true,
      load: false,
    });
  });

  it("loads nothing without a pair or for a strategy keeping none", () => {
    expect(decisionsLoad(null, "llm", null, true)).toMatchObject({ clear: true, load: false });
    expect(decisionsLoad(null, "llm", "", true)).toMatchObject({ load: false });
    const none = decisionsLoad(null, "trend", "BTCUSDm", false);
    expect(none).toMatchObject({ clear: true, load: false });
    expect(decisionsLoad(none.key, "trend", "BTCUSDm", false)).toMatchObject({
      clear: false,
      load: false,
    });
  });
});
