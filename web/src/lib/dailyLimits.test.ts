import { describe, expect, it } from "vitest";
import type { DailyLimitsStatus } from "../api/types";
import {
  dailyLimitErrors,
  dailyLimitForm,
  dailyLimitSettings,
  dailyStateLabel,
  flattenLabel,
  guardedAgents,
  limitProgress,
  limitsLabel,
  parseLimit,
} from "./dailyLimits";

describe("the daily limits on the dashboard", () => {
  it("tells the state of a scope, the side reached", () => {
    expect(dailyStateLabel({ state: "free", limits: { enabled: true } }).tone).toBe("on");
    expect(dailyStateLabel(undefined).text).toBe("Sem limite");
    const blocked = dailyStateLabel({
      state: "blocked",
      limits: { enabled: true },
      guard: {
        id: 1,
        kind: "limit",
        trigger: "gain",
        result: 310,
        limit: 300,
        currency: "BRL",
        reason: "",
        trips: 1,
        tripped_at: 1,
      },
    });
    expect(blocked).toEqual({ text: "Ganho atingido: bloqueado", tone: "error" });
    expect(dailyStateLabel({ state: "incomplete", limits: { enabled: true } }).tone).toBe("warn");
    expect(flattenLabel("done")?.tone).toBe("on");
    expect(flattenLabel("failed")?.text).toContain("confira a conta");
    expect(flattenLabel(undefined)).toBeNull();
  });

  it("tells the limits and how far the result went toward them", () => {
    const limits = { enabled: true, currency: "BRL", max_loss: 150, max_gain: 300 };
    expect(limitsLabel(limits)).toBe("perda máx. 150,00 · ganho máx. 300,00 BRL");
    expect(limitsLabel({ enabled: false })).toBe("sem limite");
    expect(
      limitProgress(
        { currency: "BRL", realized: -50, open: -25, total: -75, complete: true },
        limits,
      ),
    ).toEqual({
      loss: 0.5,
      gain: 0,
    });
    expect(limitProgress(undefined, { enabled: false }).loss).toBeNull();
  });

  it("finds the agents blocked or incomplete", () => {
    const status = {
      day: "2026-10-29",
      zone: "America/Belem",
      evaluated_at: 1,
      global: { state: "free", limits: { enabled: true } },
      agents: [
        { agent: "orb", state: "blocked", limits: { enabled: true } },
        { agent: "trend", state: "free", limits: { enabled: true } },
      ],
    } as DailyLimitsStatus;
    expect(Object.keys(guardedAgents(status))).toEqual(["orb"]);
    expect(guardedAgents(null)).toEqual({});
  });
});

describe("the form of the daily limits", () => {
  it("goes from the settings to the form and back, a comma taken as the decimals", () => {
    const settings = { enabled: true, currency: "BRL", max_loss: 150.5, max_gain: 300 };
    expect(dailyLimitSettings(dailyLimitForm(settings), true)).toEqual(settings);
    expect(parseLimit("150,50")).toBe(150.5);
    expect(parseLimit("1.500,25")).toBe(1500.25);
    expect(parseLimit("")).toBeNull();
    expect(Number.isNaN(parseLimit("abc") as number)).toBe(true);
    expect(
      dailyLimitSettings({ enabled: true, currency: "brl", maxLoss: "40", maxGain: "" }, false),
    ).toEqual({
      enabled: true,
      max_loss: 40,
    });
  });

  it("refuses what the controller refuses", () => {
    const form = { enabled: true, currency: "BRL", maxLoss: "150", maxGain: "" };
    expect(dailyLimitErrors(form, true)).toEqual({});
    expect(dailyLimitErrors({ ...form, maxLoss: "" }, true).maxLoss).toBe(
      "Defina a perda, o ganho ou os dois.",
    );
    expect(dailyLimitErrors({ ...form, maxLoss: "0" }, true).maxLoss).toBe("Acima de zero.");
    expect(dailyLimitErrors({ ...form, currency: "" }, true).currency).toBe("Defina a moeda.");
    expect(dailyLimitErrors({ ...form, currency: "BRLX" }, true).currency).toContain("3 letras");
    // an agent: the currency of its account
    expect(dailyLimitErrors({ ...form, currency: "" }, false)).toEqual({});
    expect(dailyLimitErrors({ ...form, enabled: false, maxLoss: "" }, true)).toEqual({});
  });
});
