import { describe, expect, it } from "vitest";
import type { ClosedProfitResponse } from "../api/types";
import {
  CLOSED_PERIOD_KEY,
  closedAmountsOf,
  closedAxis,
  closedCharts,
  closedPeriodLabel,
  closedPLSummary,
  closedRange,
  customPeriodError,
  dayOf,
  dayStart,
  loadClosedPeriod,
  saveClosedPeriod,
} from "./closedPL";

/** Unix seconds of a time of the clock of the browser. */
const at = (year: number, month: number, day: number, hours = 0) =>
  Math.floor(new Date(year, month - 1, day, hours).getTime() / 1000);

/** A storage of the tests, in memory. */
function memoryStorage(): Storage {
  const items = new Map<string, string>();
  return {
    get length() {
      return items.size;
    },
    clear: () => items.clear(),
    getItem: (key) => items.get(key) ?? null,
    key: (index) => [...items.keys()][index] ?? null,
    removeItem: (key) => void items.delete(key),
    setItem: (key, value) => void items.set(key, value),
  };
}

const read = (fields: Partial<ClosedProfitResponse> = {}): ClosedProfitResponse => ({
  from: 0,
  to: 86_400,
  totals: [{ currency: "BRL", amount: 0 }],
  agents: [],
  series: [{ currency: "BRL", points: [] }],
  complete: true,
  at: 1_760_000_000,
  ...fields,
});

describe("the period of the closed profit", () => {
  // a Wednesday afternoon
  const now = new Date(2026, 9, 14, 15, 30);

  it("takes today, the week from Monday and the month, by the clock of the browser", () => {
    expect(closedRange({ kind: "today" }, now)).toEqual({
      from: at(2026, 10, 14),
      to: at(2026, 10, 15),
    });
    expect(closedRange({ kind: "week" }, now)).toEqual({
      from: at(2026, 10, 12),
      to: at(2026, 10, 19),
    });
    expect(closedRange({ kind: "month" }, now)).toEqual({
      from: at(2026, 10, 1),
      to: at(2026, 11, 1),
    });
    // on a Sunday, the week of the Monday before; in December, the month until January
    expect(closedRange({ kind: "week" }, new Date(2026, 9, 18, 23))).toEqual({
      from: at(2026, 10, 12),
      to: at(2026, 10, 19),
    });
    expect(closedRange({ kind: "month" }, new Date(2026, 11, 31))).toEqual({
      from: at(2026, 12, 1),
      to: at(2027, 1, 1),
    });
  });

  it("takes the days of a custom period, both counted, today for days it can't take", () => {
    expect(closedRange({ kind: "custom", from: "2026-10-01", to: "2026-10-13" }, now)).toEqual({
      from: at(2026, 10, 1),
      to: at(2026, 10, 14),
    });
    expect(closedRange({ kind: "custom", from: "2026-10-14", to: "2026-10-14" }, now)).toEqual({
      from: at(2026, 10, 14),
      to: at(2026, 10, 15),
    });
    expect(closedRange({ kind: "custom", from: "2026-10-14", to: "2026-10-01" }, now)).toEqual(
      closedRange({ kind: "today" }, now),
    );
  });

  it("tells why the days of a custom period can't be taken", () => {
    expect(customPeriodError("2026-10-01", "2026-10-13")).toBeNull();
    expect(customPeriodError("", "2026-10-13")).toBe("Escolha a data inicial e a final.");
    expect(customPeriodError("2026-02-30", "2026-03-01")).toBe("Escolha a data inicial e a final.");
    expect(customPeriodError("2026-10-13", "2026-10-01")).toBe(
      "A data final é anterior à inicial.",
    );
    expect(customPeriodError("2025-01-01", "2026-10-13")).toBe("Escolha até 400 dias.");
    expect(customPeriodError("2025-09-10", "2026-10-14")).toBeNull(); // 400 days
  });

  it("reads and writes the days of a date input", () => {
    expect(dayStart("2026-10-14")?.getTime()).toBe(new Date(2026, 9, 14).getTime());
    expect(dayStart("14/10/2026")).toBeNull();
    expect(dayOf(new Date(2026, 0, 5, 23, 59))).toBe("2026-01-05");
  });

  it("labels each period, the days of a custom one", () => {
    expect(closedPeriodLabel({ kind: "today" }, now)).toBe("Hoje");
    expect(closedPeriodLabel({ kind: "week" }, now)).toBe("Semana");
    expect(closedPeriodLabel({ kind: "month" }, now)).toBe("Mês");
    expect(closedPeriodLabel({ kind: "custom", from: "2026-10-01", to: "2026-10-13" }, now)).toBe(
      "01/10 a 13/10",
    );
    expect(closedPeriodLabel({ kind: "custom", from: "2026-10-05", to: "2026-10-05" }, now)).toBe(
      "05/10",
    );
    expect(closedPeriodLabel({ kind: "custom", from: "2025-12-28", to: "2026-01-03" }, now)).toBe(
      "28/12/2025 a 03/01/2026",
    );
  });

  it("is kept in the browser, today without a valid one", () => {
    const storage = memoryStorage();
    expect(loadClosedPeriod(storage)).toEqual({ kind: "today" });
    saveClosedPeriod({ kind: "month" }, storage);
    expect(loadClosedPeriod(storage)).toEqual({ kind: "month" });
    saveClosedPeriod({ kind: "custom", from: "2026-10-01", to: "2026-10-13" }, storage);
    expect(loadClosedPeriod(storage)).toEqual({
      kind: "custom",
      from: "2026-10-01",
      to: "2026-10-13",
    });
    for (const saved of [
      "not json",
      JSON.stringify({ kind: "year" }),
      JSON.stringify({ kind: "custom", from: "2026-10-13", to: "2026-10-01" }),
      JSON.stringify(null),
    ]) {
      storage.setItem(CLOSED_PERIOD_KEY, saved);
      expect(loadClosedPeriod(storage), saved).toEqual({ kind: "today" });
    }
    expect(loadClosedPeriod(null)).toEqual({ kind: "today" });
  });
});

describe("the closed profit of the agents", () => {
  it("counts the positions closed and their agents", () => {
    expect(closedPLSummary(read())).toBe("Sem posições fechadas");
    expect(
      closedPLSummary(
        read({
          agents: [{ agent: "llm", amount: -8.4, currency: "BRL", positions: 1, running: true }],
        }),
      ),
    ).toBe("1 posição fechada em 1 agente");
    expect(
      closedPLSummary(
        read({
          agents: [
            { agent: "llm", amount: -8.4, currency: "BRL", positions: 2, running: true },
            { agent: "fibonacci", amount: -50, currency: "BRL", positions: 1, running: false },
          ],
        }),
      ),
    ).toBe("3 posições fechadas em 2 agentes");
  });

  it("keeps the profit of an agent in the currency of its account", () => {
    const several = read({
      agents: [
        { agent: "mixed", amount: -8.4, currency: "BRL", positions: 1, running: true },
        { agent: "mixed", amount: 3.2, currency: "USD", positions: 1, running: true },
      ],
    });
    expect(closedAmountsOf("mixed", several)).toEqual([
      { currency: "BRL", amount: -8.4 },
      { currency: "USD", amount: 3.2 },
    ]);
    expect(closedAmountsOf("other", several)).toEqual([]);
  });
});

describe("the evolution of the closed profit", () => {
  const series = read({
    from: 0,
    to: 86_400,
    totals: [
      { currency: "BRL", amount: 10 },
      { currency: "USD", amount: 0 },
    ],
    series: [
      {
        currency: "BRL",
        points: [
          { at: 3_600, amount: -30 },
          { at: 7_200, amount: 10 },
        ],
      },
      { currency: "USD", points: [] },
    ],
  });

  it("starts at 0, steps at each exit and holds the total until now", () => {
    expect(closedCharts(series, 10_000)).toEqual([
      {
        currency: "BRL",
        points: [
          { time: 0, value: 0 },
          { time: 3_600, value: -30 },
          { time: 7_200, value: 10 },
          { time: 10_000, value: 10 },
        ],
        exits: 2,
        total: 10,
      },
    ]);
  });

  it("holds it until the end of a period gone, never past its last exit", () => {
    expect(closedCharts(series, 90_000)[0]?.points.at(-1)).toEqual({ time: 86_400, value: 10 });
    // a clock behind the one of the broker: the last exit ends the line
    expect(closedCharts(series, 5_000)[0]?.points.at(-1)).toEqual({ time: 7_200, value: 10 });
  });

  it("draws no line without an exit", () => {
    expect(closedCharts(read(), 10_000)).toEqual([]);
    expect(closedCharts({ ...read(), series: undefined } as never, 10_000)).toEqual([]);
  });

  it("tells the ends of its time: the hours of a day, the days of a longer period", () => {
    const today = { from: at(2026, 10, 14), to: at(2026, 10, 15) };
    expect(closedAxis(today, at(2026, 10, 14, 15))).toEqual({ start: "00:00", end: "agora" });
    expect(closedAxis(today, at(2026, 10, 16))).toEqual({ start: "00:00", end: "24:00" });
    const week = { from: at(2026, 10, 13), to: at(2026, 10, 20) };
    expect(closedAxis(week, at(2026, 10, 14, 15))).toEqual({ start: "13/10", end: "hoje" });
    expect(closedAxis(week, at(2026, 10, 21))).toEqual({ start: "13/10", end: "19/10" });
    const years = { from: at(2025, 12, 29), to: at(2026, 1, 3) };
    expect(closedAxis(years, at(2026, 2, 1))).toEqual({ start: "29/12/2025", end: "02/01/2026" });
  });
});
