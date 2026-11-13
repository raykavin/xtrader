import { describe, expect, it } from "vitest";
import type { NewsCalendarResponse, NewsEvent } from "../api/types";
import {
  blockingNow,
  customNewsError,
  DEFAULT_NEWS_PERIOD,
  filterNews,
  impactOf,
  importanceLabel,
  importanceLevel,
  loadNewsImpacts,
  loadNewsPeriod,
  NEWS_IMPACTS_KEY,
  NEWS_PERIOD_KEY,
  newsDayLabel,
  newsDays,
  newsPast,
  newsPeriodLabel,
  newsRange,
  newsStatusMessage,
  newsTime,
  newsValue,
  newsWindowText,
  nextNews,
  normalizeNewsImpacts,
  normalizeNewsPeriod,
  saveNewsImpacts,
  saveNewsPeriod,
  toggleImpact,
} from "./newsCalendar";

/** A storage in memory, as localStorage. */
function memoryStorage(initial: Record<string, string> = {}): Storage {
  const items = new Map(Object.entries(initial));
  return {
    get length() {
      return items.size;
    },
    clear: () => items.clear(),
    getItem: (key) => items.get(key) ?? null,
    key: (index) => [...items.keys()][index] ?? null,
    removeItem: (key) => {
      items.delete(key);
    },
    setItem: (key, value) => {
      items.set(key, String(value));
    },
  };
}

// a Friday, 13/11/2026 at 10:30 by the clock of the browser
const now = new Date(2026, 10, 13, 10, 30);
const seconds = (date: Date) => Math.floor(date.getTime() / 1000);
const at = (day: number, hour: number, minute = 0) =>
  seconds(new Date(2026, 10, day, hour, minute));

const event = (fields: Partial<NewsEvent>): NewsEvent => ({
  at: at(13, 12),
  timed: true,
  currency: "USD",
  name: "CPI",
  importance: "high",
  ...fields,
});

describe("the period of the news", () => {
  it("takes the range of each period by the clock of the browser", () => {
    const day = (d: number) => seconds(new Date(2026, 10, d));
    expect(newsRange({ kind: "today" }, now)).toEqual({ from: day(13), to: day(14) });
    expect(newsRange({ kind: "tomorrow" }, now)).toEqual({ from: day(14), to: day(15) });
    expect(newsRange({ kind: "two" }, now)).toEqual({ from: day(13), to: day(15) });
    expect(newsRange({ kind: "next24" }, now)).toEqual({
      from: seconds(now),
      to: seconds(now) + 86_400,
    });
    expect(newsRange({ kind: "week" }, now), "from Monday").toEqual({ from: day(9), to: day(16) });
    expect(newsRange({ kind: "nextweek" }, now)).toEqual({ from: day(16), to: day(23) });
    expect(newsRange({ kind: "custom", from: "2026-11-20", to: "2026-11-22" }, now)).toEqual({
      from: day(20),
      to: day(23),
    });
    expect(
      newsRange({ kind: "custom", from: "2026-11-22", to: "2026-11-20" }, now),
      "refused",
    ).toBeNull();
  });

  it("checks the days of a custom period", () => {
    expect(customNewsError("2026-11-13", "2026-11-13")).toBeNull();
    expect(customNewsError("2026-11-13", "")).toBe("Escolha o primeiro e o último dia.");
    expect(customNewsError("2026-11-14", "2026-11-13")).toBe(
      "O último dia vem depois do primeiro.",
    );
    expect(customNewsError("2026-11-01", "2026-12-02")).toBe("O período vai até 31 dias.");
    expect(customNewsError("2026-11-01", "2026-12-01"), "31 days").toBeNull();
    expect(customNewsError("2026-02-30", "2026-03-01"), "no such day").not.toBeNull();
  });

  it("labels the periods, a custom one by its days", () => {
    expect(newsPeriodLabel({ kind: "two" })).toBe("Hoje e amanhã");
    expect(newsPeriodLabel({ kind: "custom", from: "2026-11-20", to: "2026-11-22" })).toBe(
      "20/11 a 22/11",
    );
    expect(newsPeriodLabel({ kind: "custom", from: "2026-11-20", to: "2026-11-20" })).toBe("20/11");
  });

  it("keeps the period and the impacts in the browser, the defaults without valid ones", () => {
    const storage = memoryStorage();
    expect(loadNewsPeriod(storage)).toEqual(DEFAULT_NEWS_PERIOD);
    expect(loadNewsImpacts(storage), "all of them at first").toEqual(["low", "medium", "high"]);
    saveNewsPeriod({ kind: "custom", from: "2026-11-20", to: "2026-11-22" }, storage);
    saveNewsImpacts(["high"], storage);
    expect(loadNewsPeriod(storage)).toEqual({
      kind: "custom",
      from: "2026-11-20",
      to: "2026-11-22",
    });
    expect(loadNewsImpacts(storage)).toEqual(["high"]);

    const broken = memoryStorage({ [NEWS_PERIOD_KEY]: "{nope", [NEWS_IMPACTS_KEY]: '["huge"]' });
    expect(loadNewsPeriod(broken)).toEqual(DEFAULT_NEWS_PERIOD);
    expect(loadNewsImpacts(broken)).toEqual(["low", "medium", "high"]);
    expect(normalizeNewsPeriod({ kind: "yearly" })).toBeNull();
    expect(normalizeNewsPeriod({ kind: "custom", from: "x", to: "y" })).toBeNull();
    expect(normalizeNewsImpacts(["high", "low", "x"]), "in their order").toEqual(["low", "high"]);
    expect(normalizeNewsImpacts([])).toBeNull();
  });
});

describe("the impact of the news", () => {
  it("filters by impact, the releases of no impact with the low ones, never none", () => {
    expect(impactOf("none")).toBe("low");
    expect(importanceLabel("none")).toBe("Sem impacto");
    expect(importanceLabel("medium")).toBe("Médio");
    expect([importanceLevel("none"), importanceLevel("low"), importanceLevel("high")]).toEqual([
      0, 1, 3,
    ]);
    const events = [
      event({ name: "a", importance: "low" }),
      event({ name: "b", importance: "none" }),
      event({ name: "c", importance: "medium" }),
      event({ name: "d", importance: "high" }),
    ];
    expect(filterNews(events, ["high"]).map((e) => e.name)).toEqual(["d"]);
    expect(filterNews(events, ["low", "high"]).map((e) => e.name)).toEqual(["a", "b", "d"]);
    expect(toggleImpact(["low", "high"], "medium")).toEqual(["low", "medium", "high"]);
    expect(toggleImpact(["low", "high"], "low")).toEqual(["high"]);
    expect(toggleImpact(["high"], "high"), "the last one kept").toEqual(["high"]);
  });
});

describe("the releases of the card", () => {
  it("groups them by day, labelled near today", () => {
    const days = newsDays(
      [
        event({ at: at(13, 9) }),
        event({ at: at(13, 15) }),
        event({ at: at(14, 3) }),
        event({ at: at(18, 9) }),
      ],
      now,
    );
    expect(days.map((d) => [d.day, d.events.length])).toEqual([
      ["2026-11-13", 2],
      ["2026-11-14", 1],
      ["2026-11-18", 1],
    ]);
    expect(days[0]?.label).toBe("Hoje, sex. 13/11");
    expect(days[1]?.label).toBe("Amanhã, sáb. 14/11");
    expect(days[2]?.label).toBe("qua. 18/11");
    expect(newsDayLabel(new Date(2026, 10, 12, 23), now)).toBe("Ontem, qui. 12/11");
  });

  it("tells the time, the values with their unit, the ones over and the next one", () => {
    expect(newsTime(event({ at: at(13, 9, 5) }))).toBe("09:05");
    expect(newsTime(event({ timed: false }))).toBe("Dia todo");
    expect(newsValue(150, "K")).toBe("150K");
    expect(newsValue(3.25, "%")).toBe("3,25%");
    expect(newsValue(undefined, "%")).toBeNull();
    const events = [
      event({ at: at(13, 9) }),
      event({ at: at(13, 0), timed: false }),
      event({ at: at(13, 11) }),
    ];
    expect(newsPast(events[0] as NewsEvent, now)).toBe(true);
    expect(newsPast(events[1] as NewsEvent, now), "its day not over").toBe(false);
    expect(nextNews(events, now), "the release of the hours ahead, before the day-long one").toBe(
      2,
    );
    expect(nextNews([event({ at: at(13, 8) })], now)).toBe(-1);
  });

  it("takes a release of a day by its date in the calendar, never the evening before in Belém", () => {
    // 14/11 at 00:00 UTC: 21:00 of 13/11 in Belém, a holiday of 14/11
    const holiday = event({ at: Date.UTC(2026, 10, 14) / 1000, timed: false, name: "Holiday" });
    const days = newsDays([event({ at: at(13, 22) }), holiday, event({ at: at(14, 9) })], now);
    expect(days.map((d) => [d.day, d.events.map((e) => e.name)])).toEqual([
      ["2026-11-13", ["CPI"]],
      ["2026-11-14", ["Holiday", "CPI"]],
    ]);
    expect(newsPast(holiday, now)).toBe(false);
    expect(newsPast(holiday, new Date(2026, 10, 15, 0, 1)), "over at the end of its day").toBe(
      true,
    );
    const today = newsRange({ kind: "today" }, now) as { from: number; to: number };
    const tomorrow = newsRange({ kind: "tomorrow" }, now) as { from: number; to: number };
    expect(filterNews([holiday], ["low", "medium", "high"], today), "not of today").toEqual([]);
    expect(filterNews([holiday], ["low", "medium", "high"], tomorrow)).toEqual([holiday]);
    const next24 = newsRange({ kind: "next24" }, now) as { from: number; to: number };
    expect(
      filterNews([holiday], ["low", "medium", "high"], next24),
      "its day starts within",
    ).toEqual([holiday]);
    // the next one: the next release of the hours of the day before a holiday of today
    const todayOff = event({ at: Date.UTC(2026, 10, 13) / 1000, timed: false, name: "Today off" });
    expect(nextNews([todayOff, event({ at: at(13, 11), name: "PPI" })], now)).toBe(1);
    expect(
      nextNews([todayOff, event({ at: at(13, 9) })], now),
      "the holiday when nothing is ahead",
    ).toBe(0);
  });

  it("tells the releases blocking the entries now, in the window around them", () => {
    const read: NewsCalendarResponse = {
      from: 0,
      to: 0,
      currencies: ["USD"],
      status: "ok",
      events: [],
      at: 0,
      window: { before: 10, after: 15, importance: "high" },
    };
    expect(
      blockingNow(event({ at: at(13, 10, 35), blocks: true }), read, now),
      "5 min before",
    ).toBe(true);
    expect(
      blockingNow(event({ at: at(13, 10, 20), blocks: true }), read, now),
      "10 min after",
    ).toBe(true);
    expect(blockingNow(event({ at: at(13, 10, 41), blocks: true }), read, now)).toBe(false);
    expect(blockingNow(event({ at: at(13, 10, 35) }), read, now), "not blocking").toBe(false);
    expect(newsWindowText(read)).toBe(
      "Os agentes não abrem posição de 10 min antes a 15 min depois das de impacto alto.",
    );
    expect(newsWindowText({ ...read, window: undefined })).toBeNull();
  });

  it("tells why the calendar wasn't read", () => {
    expect(newsStatusMessage("disabled")).toBe(
      "As notícias estão desligadas em Configurações › Notícias.",
    );
    expect(newsStatusMessage("unauthorized")).toContain("recusou o token");
    expect(newsStatusMessage("anything")).toContain("não pôde ser lido");
  });
});
