import { describe, expect, it } from "vitest";
import { agentPairHref, dashboardHref, rememberDashboard, settingsHref } from "./nav";

/** A storage of a map, as the session storage of the browser. */
function memory(): Pick<Storage, "getItem" | "setItem"> {
  const values = new Map<string, string>();
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => {
      values.set(key, value);
    },
  };
}

describe("dashboardHref", () => {
  it("goes back to the panel as it was left", () => {
    const storage = memory();
    rememberDashboard("?agent=llm-smc&pair=XAUUSDm&tf=5m", storage);
    expect(dashboardHref(storage)).toBe("/?agent=llm-smc&pair=XAUUSDm&tf=5m");
  });

  it("goes to the default panel without a query kept", () => {
    expect(dashboardHref(memory())).toBe("/");
    expect(dashboardHref(null)).toBe("/");
    const storage = memory();
    rememberDashboard("", storage);
    expect(dashboardHref(storage)).toBe("/");
  });

  it("never follows a query that isn't one of the panel", () => {
    const storage = memory();
    rememberDashboard("//evil.example/x", storage);
    expect(dashboardHref(storage)).toBe("/");
    rememberDashboard("?a=<script>", storage);
    expect(dashboardHref(storage)).toBe("/");
  });

  it("survives a storage refusing to be used", () => {
    const broken = {
      getItem: () => {
        throw new Error("denied");
      },
      setItem: () => {
        throw new Error("denied");
      },
    };
    expect(() => rememberDashboard("?agent=x", broken)).not.toThrow();
    expect(dashboardHref(broken)).toBe("/");
  });
});

describe("settingsHref", () => {
  it("links to a section of the settings by its hash", () => {
    expect(settingsHref("prompts")).toBe("/settings#prompts");
  });
});

describe("agentPairHref", () => {
  it("links to an agent on a pair, the rest of the search kept", () => {
    expect(agentPairHref("highlow", "BTCUSDm")).toBe("/?agent=highlow&pair=BTCUSDm");
    expect(agentPairHref("highlow", "BTCUSDm", "?agent=matrend&pair=XAUUSDm&tf=15m")).toBe(
      "/?agent=highlow&pair=BTCUSDm&tf=15m",
    );
    expect(agentPairHref("a b", "X&Y")).toBe("/?agent=a+b&pair=X%26Y");
  });
});
