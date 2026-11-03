import { describe, expect, it } from "vitest";
import type { DailyLimitsStatus } from "../api/types";
import {
  blockNote,
  dailyLimitErrors,
  dailyLimitForm,
  dailyLimitSettings,
  dailyStateLabel,
  flattenLabel,
  globalUnit,
  guardedAgents,
  limitProgress,
  limitsLabel,
  parseLimit,
  scopeResults,
  stripHeadline,
  stripTone,
  timeIn,
} from "./dailyLimits";

describe("the daily limits on the dashboard", () => {
  it("tells what a guard blocks once: the limit, when, until when; incomplete, why", () => {
    const guard = {
      id: 1,
      kind: "limit",
      trigger: "gain",
      result: 205.87,
      limit: 200,
      currency: "USD",
      reason: "Ganho diário de todos os agentes atingido: 205,87 USD (limite 200,00 USD)",
      trips: 1,
      tripped_at: Date.UTC(2026, 10, 3, 13, 35) / 1000,
    };
    expect(blockNote({ state: "blocked", limits: { enabled: true }, guard }, "America/Belem")).toBe(
      "Limite de 200,00 USD, atingido às 10:35 · entradas bloqueadas até 00:00",
    );
    expect(
      blockNote(
        {
          state: "incomplete",
          limits: { enabled: true },
          guard: {
            ...guard,
            reason: "resultado do dia incompleto (a conta de trend não respondeu)",
          },
        },
        "America/Belem",
      ),
    ).toBe("Resultado do dia incompleto (a conta de trend não respondeu)");
    expect(blockNote({ state: "free", limits: { enabled: true } }, "America/Belem")).toBe("");
  });

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
    expect(blocked).toEqual({ text: "Ganho máximo atingido", tone: "error" });
    expect(dailyStateLabel({ state: "incomplete", limits: { enabled: true } })).toEqual({
      text: "Resultado incompleto",
      tone: "warn",
    });
    expect(flattenLabel("done")).toEqual({ text: "RISCO: POSIÇÕES ZERADAS", tone: "error" });
    expect(flattenLabel("failed")).toEqual({ text: "RISCO: ZERAGEM FALHOU", tone: "error" });
    expect(flattenLabel(undefined)).toBeNull();
  });

  it("tells the limits and how far the result went toward them", () => {
    const limits = { enabled: true, max_loss: 150, max_gain: 300 };
    expect(limitsLabel(limits, "BRL")).toBe("perda máx. 150,00 · ganho máx. 300,00 BRL");
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

  it("tells the results of all the agents by currency, none converted", () => {
    const brl = { currency: "BRL", realized: -50, open: 10, total: -40, complete: true };
    const usd = { currency: "USD", realized: -30, open: 0, total: -30, complete: true };
    const global = { state: "free", limits: { enabled: true }, result: brl, results: [brl, usd] };
    expect(scopeResults(global)).toEqual([brl, usd]);
    expect(scopeResults({ state: "free", limits: { enabled: true }, result: brl })).toEqual([brl]);
    expect(scopeResults(undefined)).toEqual([]);
    expect(globalUnit([brl])).toBe("BRL");
    expect(globalUnit([brl, usd])).toBe("em cada moeda");
    expect(globalUnit([])).toBe("");
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
    const settings = { enabled: true, max_loss: 150.5, max_gain: 300 };
    expect(dailyLimitSettings(dailyLimitForm(settings))).toEqual(settings);
    expect(parseLimit("150,50")).toBe(150.5);
    expect(parseLimit("1.500,25")).toBe(1500.25);
    expect(parseLimit("")).toBeNull();
    expect(Number.isNaN(parseLimit("abc") as number)).toBe(true);
    expect(dailyLimitSettings({ enabled: true, maxLoss: "40", maxGain: "" })).toEqual({
      enabled: true,
      max_loss: 40,
    });
  });

  it("refuses what the controller refuses", () => {
    const form = { enabled: true, maxLoss: "150", maxGain: "" };
    expect(dailyLimitErrors(form)).toEqual({});
    expect(dailyLimitErrors({ ...form, maxLoss: "" }).maxLoss).toBe(
      "Defina a perda, o ganho ou os dois.",
    );
    expect(dailyLimitErrors({ ...form, maxLoss: "0" }).maxLoss).toBe("Use um valor acima de zero.");
    expect(dailyLimitErrors({ ...form, enabled: false, maxLoss: "" })).toEqual({});
  });
});

describe("the strip of the daily limits", () => {
  const limits = { enabled: true, max_loss: 150, max_gain: 300 };
  const guard = (trigger: string) => ({
    id: 1,
    kind: "limit",
    trigger,
    result: 0,
    limit: 0,
    currency: "BRL",
    reason: "",
    trips: 1,
    tripped_at: 0,
  });

  it("takes the color of the limit reached", () => {
    expect(stripTone({ limits, state: "blocked", guard: guard("gain") })).toBe("gain");
    expect(stripTone({ limits, state: "blocked", guard: guard("loss") })).toBe("loss");
    expect(stripTone({ limits, state: "incomplete" })).toBe("incomplete");
    expect(stripTone({ limits, state: "free" })).toBe("free");
    expect(stripTone(undefined)).toBe("off");
    expect(stripHeadline({ limits, state: "blocked", guard: guard("gain") })).toBe(
      "Ganho diário atingido",
    );
    expect(stripHeadline({ limits, state: "blocked", guard: guard("loss") })).toBe(
      "Perda diária atingida",
    );
  });

  it("tells when a limit was reached in the zone of the limits", () => {
    expect(timeIn(Date.UTC(2026, 9, 29, 17, 32) / 1000, "America/Belem")).toBe("14:32");
    expect(timeIn(0, "America/Belem")).toBe("");
  });
});
