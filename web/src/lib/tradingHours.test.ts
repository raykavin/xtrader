import { describe, expect, it } from "vitest";
import type { TradingHoursStatus } from "../api/types";
import {
  crossesMidnight,
  endActionsText,
  hoursError,
  hoursState,
  hoursWindow,
  lastEndLabel,
} from "./tradingHours";

const zone = "America/Belem";
// 2026-10-30 17:30 and 2026-10-31 09:00 in Belém (UTC−3)
const end = Date.UTC(2026, 9, 30, 20, 30) / 1000;
const start = Date.UTC(2026, 9, 31, 12, 0) / 1000;

const status = (fields: Partial<TradingHoursStatus> = {}): TradingHoursStatus => ({
  enabled: true,
  start: "09:00",
  end: "17:30",
  zone,
  open: true,
  blocked: false,
  next_at: end,
  ...fields,
});

describe("the trading hours", () => {
  it("tells how they stand: off, open until the end, closed until the start", () => {
    expect(hoursState(status({ enabled: false }))).toEqual({ tone: "off", text: "Desligado" });
    expect(hoursState(status())).toEqual({ tone: "on", text: "Aberto · fecha às 17:30" });
    expect(hoursState(status({ open: false, blocked: true, next_at: start }))).toEqual({
      tone: "warn",
      text: "Fora do horário · abre às 09:00",
    });
    expect(hoursWindow(status())).toBe("09:00–17:30");
    expect(hoursWindow(status({ enabled: false }))).toBe("");
  });

  it("tells what the end does and how the last one went", () => {
    expect(endActionsText(true, true)).toBe("zera as posições e cancela as ordens pendentes");
    expect(endActionsText(false, true)).toBe("cancela as ordens pendentes");
    expect(endActionsText(false, false)).toBe("só bloqueia novas entradas");
    expect(
      lastEndLabel({ at: end, flatten: true, cancel_pending: true, status: "succeeded" }, zone),
    ).toEqual({
      tone: "on",
      text: "Fim às 17:30: posições zeradas e ordens pendentes canceladas · Concluída",
    });
    expect(
      lastEndLabel({ at: end, flatten: true, cancel_pending: false, status: "partial" }, zone),
    ).toEqual({ tone: "warn", text: "Fim às 17:30: posições zeradas · Parcial" });
    expect(
      lastEndLabel({ at: end, flatten: false, cancel_pending: true, status: "executing" }, zone),
    ).toEqual({ tone: "info", text: "Fim às 17:30: ordens pendentes canceladas · executando" });
  });

  it("checks the form as the controller does", () => {
    const form = {
      enabled: true,
      start: "09:00",
      end: "17:30",
      flatten: false,
      cancel_pending: false,
    };
    expect(hoursError(form)).toBeNull();
    expect(hoursError({ ...form, start: "" })).toBe("Informe o início e o fim.");
    expect(hoursError({ ...form, enabled: false, start: "" })).toBeNull();
    expect(hoursError({ ...form, end: "09:00" })).toBe("O início e o fim devem ser diferentes.");
    expect(hoursError({ ...form, start: "24:00" })).toBe(
      "O início deve ser um horário de 00:00 a 23:59.",
    );
    expect(crossesMidnight({ start: "22:00", end: "02:00" })).toBe(true);
    expect(crossesMidnight(form)).toBe(false);
  });
});
