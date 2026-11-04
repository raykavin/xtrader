import { describe, expect, it } from "vitest";
import {
  type ExposureForm,
  exposureForm,
  exposureRequest,
  pairLimitsText,
  parsePairLimits,
} from "./exposure";

const form = (fields: Partial<ExposureForm> = {}): ExposureForm => ({
  maxAgents: "2",
  pairsText: "",
  ...fields,
});

describe("the exposure limit", () => {
  it("fills the form from the settings, no limit as a blank", () => {
    expect(exposureForm({ max_agents: 0 })).toEqual({ maxAgents: "", pairsText: "" });
    expect(
      exposureForm({ max_agents: 2, pairs: { XAUUSDm: 2, BTCUSDm: 3 }, updated_by: "admin" }),
    ).toEqual({ maxAgents: "2", pairsText: "XAUUSDm=2;BTCUSDm=3" });
    expect(pairLimitsText(undefined)).toBe("");
    expect(pairLimitsText({ USOILm: 0 })).toBe("USOILm=0");
  });

  it("takes the limits of the assets typed as ATIVO=número separated by ;, the case kept", () => {
    expect(parsePairLimits("")).toEqual({ pairs: {}, errors: [] });
    expect(parsePairLimits(" ; ;")).toEqual({ pairs: {}, errors: [] });
    expect(parsePairLimits(" XAUUSDm = 2 ;BTCUSDm=3; USOILm=0;")).toEqual({
      pairs: { XAUUSDm: 2, BTCUSDm: 3, USOILm: 0 },
      errors: [],
    });
    expect(parsePairLimits("xauusdm=1;XAUUSDm=2").pairs).toEqual({ xauusdm: 1, XAUUSDm: 2 });
    expect(parsePairLimits("US30.cash=1;GER40#=2;BRENT_X-1=100;XAUUSDm=02").errors).toEqual([]);
  });

  it("tells why each entry is refused, once each", () => {
    expect(parsePairLimits("XAUUSDm2;=2;BTCUSDm=").errors).toEqual([
      "XAUUSDm2: use ATIVO=número, ex.: XAUUSDm=2.",
      "=2: use ATIVO=número, ex.: XAUUSDm=2.",
      "BTCUSDm=: use ATIVO=número, ex.: XAUUSDm=2.",
    ]);
    expect(parsePairLimits("XAU USD=2;.XAU=1").errors).toEqual([
      "XAU USD: use o nome do ativo no MT5, ex.: XAUUSDm.",
      ".XAU: use o nome do ativo no MT5, ex.: XAUUSDm.",
    ]);
    expect(parsePairLimits(`${"A".repeat(33)}=1`).errors).toHaveLength(1);
    expect(parsePairLimits(`${"A".repeat(32)}=1`).errors).toEqual([]);
    expect(parsePairLimits("XAUUSDm=101;BTCUSDm=-1;USOILm=1.5;US30=2=3").errors).toEqual([
      "XAUUSDm: use um número inteiro de 0 a 100.",
      "BTCUSDm: use um número inteiro de 0 a 100.",
      "USOILm: use um número inteiro de 0 a 100.",
      "US30: use um número inteiro de 0 a 100.",
    ]);
    expect(parsePairLimits("XAUUSDm=2;XAUUSDm=3;XAUUSDm=1")).toEqual({
      pairs: { XAUUSDm: 2 },
      errors: ["XAUUSDm: ativo repetido, deixe um só."],
    });
  });

  it("checks the form as the controller does and sends the assets only when some", () => {
    expect(exposureRequest(form())).toEqual({
      request: { settings: { max_agents: 2 } },
      errors: { pairs: [] },
    });
    expect(exposureRequest(form({ maxAgents: " " })).request).toEqual({
      settings: { max_agents: 0 },
    });
    expect(
      exposureRequest(form({ maxAgents: "0", pairsText: "XAUUSDm=2;BTCUSDm=0" })).request,
    ).toEqual({ settings: { max_agents: 0, pairs: { XAUUSDm: 2, BTCUSDm: 0 } } });
    expect(exposureRequest(form({ maxAgents: "100" })).request?.settings.max_agents).toBe(100);
    expect(exposureRequest(form()).request?.settings).not.toHaveProperty("pairs");
    for (const typed of ["101", "-1", "1.5", "dois"]) {
      expect(exposureRequest(form({ maxAgents: typed }))).toEqual({
        request: null,
        errors: { maxAgents: "Use um número inteiro de 0 a 100.", pairs: [] },
      });
    }
    expect(exposureRequest(form({ pairsText: "XAUUSDm=2;XAUUSDm=3" }))).toEqual({
      request: null,
      errors: { pairs: ["XAUUSDm: ativo repetido, deixe um só."] },
    });
  });
});
