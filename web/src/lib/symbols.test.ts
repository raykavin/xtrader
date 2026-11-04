import { describe, expect, it } from "vitest";
import { isSymbol, splitSymbols, withSymbols } from "./symbols";

describe("the assets typed as chips", () => {
  it("tells an asset as MT5 names it", () => {
    for (const ok of ["XAUUSDm", "US500.cash", "GER40#", "BRENT_X-1", "A".repeat(32)]) {
      expect(isSymbol(ok)).toBe(true);
    }
    for (const bad of ["", "XAU USD", ".XAU", "A".repeat(33), "XAUUSDm;BTCUSDm"]) {
      expect(isSymbol(bad)).toBe(false);
    }
  });

  it("splits what is typed or pasted by ;, by , and by blanks, the case kept", () => {
    expect(splitSymbols("")).toEqual([]);
    expect(splitSymbols(" ; ,")).toEqual([]);
    expect(splitSymbols(" XAUUSDm ")).toEqual(["XAUUSDm"]);
    expect(splitSymbols("XAUUSDm;BTCUSDm, USOILm\nxauusdm")).toEqual([
      "XAUUSDm",
      "BTCUSDm",
      "USOILm",
      "xauusdm",
    ]);
  });

  it("adds the assets typed after the chips, each once", () => {
    expect(withSymbols([], "XAUUSDm")).toEqual(["XAUUSDm"]);
    expect(withSymbols(["XAUUSDm"], "BTCUSDm;XAUUSDm;BTCUSDm")).toEqual(["XAUUSDm", "BTCUSDm"]);
    expect(withSymbols(["XAUUSDm"], "  ")).toEqual(["XAUUSDm"]);
    expect(withSymbols(["XAUUSDm"], "xauusdm")).toEqual(["XAUUSDm", "xauusdm"]);
  });
});
