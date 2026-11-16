import { describe, expect, it } from "vitest";
import type { PositionProtection, ProtectionFeature } from "../api/types";
import {
  featureLabel,
  featureState,
  moveLabel,
  parseDistance,
  protectionErrors,
  protectionForm,
  protectionSettings,
  shownFeatures,
} from "./protection";

const off: ProtectionFeature = { state: "off" };

describe("the protection of the positions", () => {
  it("tells the state of a feature on a position", () => {
    expect(featureState({ state: "waiting", remaining: 0.4 })).toEqual({
      text: "aguardando o gatilho, faltam 0,4 ATR",
      tone: "info",
    });
    expect(featureState({ state: "active", level: 4161.5 }).tone).toBe("on");
    expect(featureState({ state: "active", level: 4161.5 }).text).toContain("stop em");
    expect(featureState(off)).toEqual({ text: "desligado", tone: "off" });
    expect(featureState({ state: "unavailable", detail: "aguardando o ATR(14)" }).text).toBe(
      "aguardando o ATR(14)",
    );
  });

  it("never shows a stop applied before the broker confirmed it", () => {
    expect(moveLabel({ status: "requested", previous: 98, requested: 100.1, at: 1 }).text).toMatch(
      /^solicitado/,
    );
    expect(
      moveLabel({ status: "confirmed", previous: 98, requested: 100.1, confirmed: 100.1, at: 1 })
        .text,
    ).toMatch(/^confirmado pela corretora/);
    const rejected = moveLabel({ status: "rejected", previous: 98, requested: 100.1, at: 1 });
    expect(rejected.tone).toBe("error");
    expect(rejected.text).toContain("stop mantido em");
    expect(moveLabel({ status: "unknown", previous: 98, requested: 100.1, at: 1 }).tone).toBe(
      "warn",
    );
  });

  it("shows the features on, or armed", () => {
    const protection = {
      breakeven: off,
      trailing: { state: "waiting", remaining: 1 },
    } as PositionProtection;
    expect(shownFeatures(protection).map((f) => f.name)).toEqual(["trailing"]);
    expect(shownFeatures(undefined)).toEqual([]);
    expect(
      shownFeatures({ ...protection, breakeven: { state: "off", armed: true } }).map((f) => f.name),
    ).toEqual(["breakeven", "trailing"]);
    const daily = {
      state: "active",
      level: 4153,
      detail: "limite de perda diária de todos os agentes",
    };
    expect(
      shownFeatures({ ...protection, daily }).map((f) => f.name),
      "the stop of the limits",
    ).toEqual(["trailing", "daily"]);
    expect(featureLabel("daily")).toBe("Limite diário");
  });
});

describe("the form of the protection", () => {
  const settings = {
    unit: "atr",
    breakeven: { enabled: true, supervisor: false, trigger: 1, margin: 0.1 },
    trailing: { enabled: false, supervisor: true, trigger: 2, distance: 1, step: 0.25 },
  };

  it("goes from the settings to the form and back, a comma taken as a dot", () => {
    const form = protectionForm(settings);
    expect(protectionSettings(form)).toEqual(settings);
    expect(parseDistance("0,5")).toBe(0.5);
    expect(parseDistance("")).toBe(0);
    expect(Number.isNaN(parseDistance("1e3"))).toBe(true);
    expect(protectionErrors(form)).toEqual({});
  });

  it("refuses what the controller refuses", () => {
    const form = protectionForm(settings);
    expect(protectionErrors({ ...form, breakevenMargin: "1" })).toEqual({
      breakevenMargin: "Deve ser menor que o gatilho.",
    });
    expect(protectionErrors({ ...form, breakevenTrigger: "" })).toEqual({
      breakevenTrigger: "Defina o gatilho.",
    });
    expect(protectionErrors({ ...form, trailingStep: "2" })).toEqual({
      trailingStep: "No máximo a distância.",
    });
    expect(protectionErrors({ ...form, trailingDistance: "51" }).trailingDistance).toBe(
      "No máximo 50 ATR.",
    );
    expect(protectionErrors({ ...form, trailingTrigger: "abc" }).trailingTrigger).toBe(
      "Digite um número, ex.: 0,5.",
    );
    // off and not allowed: the distances left as they are
    expect(
      protectionErrors({
        ...form,
        breakevenEnabled: false,
        breakevenTrigger: "",
        breakevenMargin: "",
      }),
    ).toEqual({});
  });
});
