import { describe, expect, it } from "vitest";
import {
  type CardId,
  CHART_MAX_HEIGHT,
  CHART_MAX_WIDTH,
  CHART_MIN_HEIGHT,
  CHART_MIN_WIDTH,
  clearLayout,
  defaultLayout,
  hasSavedLayout,
  isDefaultLayout,
  LAYOUT_KEY,
  loadLayout,
  moveCard,
  normalizeLayout,
  saveLayout,
  setChartHeight,
  setChartWidth,
  setHidden,
  stepCard,
} from "./layout";

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

/** A storage whose every call throws, as a blocked one. */
const brokenStorage = new Proxy({} as Storage, {
  get: () => () => {
    throw new Error("SecurityError");
  },
});

const all = () => true;

describe("normalizeLayout", () => {
  it("keeps a valid layout as it is", () => {
    const layout = moveCard(defaultLayout(), "activity", "overview", 0);
    expect(normalizeLayout({ version: 1, ...layout })).toEqual(layout);
  });

  it("takes the default for anything else than a layout", () => {
    for (const raw of [null, undefined, 3, "x", [], { zones: 1 }, { version: 2, zones: {} }]) {
      expect(normalizeLayout(raw)).toEqual(defaultLayout());
    }
  });

  it("leaves the unknown and repeated cards out, and puts the missing ones back in their area", () => {
    const layout = normalizeLayout({
      zones: { overview: ["chart", "nothing", 7, "chart"], main: ["quote"], side: "ticket" },
      hidden: ["quote", "bogus", "quote"],
      chartHeight: 500,
    });
    expect(layout.zones).toEqual({
      overview: ["chart", "sentiment", "results"],
      main: ["quote", "decision"],
      side: ["ticket"],
      bottom: ["activity"],
    });
    expect(layout.hidden).toEqual(["quote"]);
    expect(layout.chartHeight).toBe(500);
  });

  it("keeps the height of the chart within its bounds", () => {
    expect(normalizeLayout({ zones: {}, chartHeight: 10 }).chartHeight).toBe(CHART_MIN_HEIGHT);
    expect(normalizeLayout({ zones: {}, chartHeight: 1e9 }).chartHeight).toBe(CHART_MAX_HEIGHT);
    expect(normalizeLayout({ zones: {}, chartHeight: Number.NaN }).chartHeight).toBeNull();
    expect(normalizeLayout({ zones: {}, chartHeight: "600" }).chartHeight).toBeNull();
    expect(normalizeLayout({ zones: {}, chartHeight: 612.6 }).chartHeight).toBe(613);
  });
});

describe("moveCard", () => {
  it("moves a card within its area, before the card at the index", () => {
    const layout = moveCard(defaultLayout(), "results", "overview", 0);
    expect(layout.zones.overview).toEqual(["results", "quote", "sentiment"]);
    expect(moveCard(defaultLayout(), "quote", "overview", 3).zones.overview).toEqual([
      "sentiment",
      "results",
      "quote",
    ]);
    expect(moveCard(defaultLayout(), "quote", "overview", 2).zones.overview).toEqual([
      "sentiment",
      "quote",
      "results",
    ]);
  });

  it("moves a card to another area", () => {
    const layout = moveCard(defaultLayout(), "ticket", "overview", 1);
    expect(layout.zones.overview).toEqual(["quote", "ticket", "sentiment", "results"]);
    expect(layout.zones.side).toEqual([]);
    expect(moveCard(layout, "ticket", "bottom", 99).zones.bottom).toEqual(["activity", "ticket"]);
  });

  it("never changes the layout it is given", () => {
    const layout = defaultLayout();
    moveCard(layout, "chart", "bottom", 0);
    expect(layout).toEqual(defaultLayout());
  });
});

describe("stepCard", () => {
  it("swaps a card with the one shown beside it", () => {
    expect(stepCard(defaultLayout(), "sentiment", -1, all).zones.overview).toEqual([
      "sentiment",
      "quote",
      "results",
    ]);
    expect(stepCard(defaultLayout(), "sentiment", 1, all).zones.overview).toEqual([
      "quote",
      "results",
      "sentiment",
    ]);
  });

  it("passes over the cards not shown", () => {
    const shown = (card: CardId) => card !== "sentiment";
    expect(stepCard(defaultLayout(), "results", -1, shown).zones.overview).toEqual([
      "results",
      "quote",
      "sentiment",
    ]);
  });

  it("goes to the next area at the edge of its own, and stops at the edges of the page", () => {
    const down = stepCard(defaultLayout(), "results", 1, all);
    expect(down.zones.overview).toEqual(["quote", "sentiment"]);
    expect(down.zones.main).toEqual(["results", "chart", "decision"]);
    const up = stepCard(defaultLayout(), "chart", -1, all);
    expect(up.zones.overview).toEqual(["quote", "sentiment", "results", "chart"]);
    expect(stepCard(defaultLayout(), "quote", -1, all)).toEqual(defaultLayout());
    expect(stepCard(defaultLayout(), "activity", 1, all)).toEqual(defaultLayout());
  });

  it("reaches an empty area", () => {
    const empty = moveCard(defaultLayout(), "ticket", "main", 2);
    const back = stepCard(empty, "ticket", 1, all);
    expect(back.zones.side).toEqual(["ticket"]);
  });
});

describe("setHidden and setChartHeight", () => {
  it("hides a card in its place, and shows it again there", () => {
    const hidden = setHidden(setHidden(defaultLayout(), "ticket", true), "quote", true);
    expect(hidden.hidden).toEqual(["quote", "ticket"]);
    expect(hidden.zones).toEqual(defaultLayout().zones);
    expect(setHidden(hidden, "ticket", false).hidden).toEqual(["quote"]);
    expect(setHidden(hidden, "ticket", true).hidden).toEqual(["quote", "ticket"]);
  });

  it("bounds the height of the chart, null for the responsive one", () => {
    expect(setChartHeight(defaultLayout(), 100).chartHeight).toBe(CHART_MIN_HEIGHT);
    expect(setChartHeight(defaultLayout(), 640).chartHeight).toBe(640);
    expect(setChartHeight(setChartHeight(defaultLayout(), 640), null).chartHeight).toBeNull();
  });

  it("bounds the width of the chart, null for the responsive one", () => {
    expect(setChartWidth(defaultLayout(), 10).chartWidth).toBe(CHART_MIN_WIDTH);
    expect(setChartWidth(defaultLayout(), 1e9).chartWidth).toBe(CHART_MAX_WIDTH);
    expect(setChartWidth(defaultLayout(), 900.4).chartWidth).toBe(900);
    expect(setChartWidth(setChartWidth(defaultLayout(), 900), null).chartWidth).toBeNull();
    expect(normalizeLayout({ zones: {}, chartWidth: "900" }).chartWidth).toBeNull();
    expect(normalizeLayout({ zones: {}, chartWidth: 900 }).chartWidth).toBe(900);
    expect(normalizeLayout({ zones: {} }).chartWidth).toBeNull(); // a layout saved before the width
  });

  it("tells the default layout apart", () => {
    expect(isDefaultLayout(defaultLayout())).toBe(true);
    expect(isDefaultLayout(setHidden(defaultLayout(), "quote", true))).toBe(false);
    expect(isDefaultLayout(setChartHeight(defaultLayout(), 500))).toBe(false);
    expect(isDefaultLayout(setChartWidth(defaultLayout(), 900))).toBe(false);
    expect(isDefaultLayout(moveCard(defaultLayout(), "chart", "main", 2))).toBe(false);
  });
});

describe("the layout in storage", () => {
  it("is saved, read back and removed alone", () => {
    const storage = memoryStorage({ "xtrader.theme": "dark" });
    expect(loadLayout(storage)).toEqual(defaultLayout());
    expect(hasSavedLayout(storage)).toBe(false);

    const layout = setChartWidth(
      setChartHeight(
        setHidden(moveCard(defaultLayout(), "ticket", "overview", 0), "sentiment", true),
        700,
      ),
      880,
    );
    saveLayout(layout, storage);
    expect(hasSavedLayout(storage)).toBe(true);
    expect(loadLayout(storage)).toEqual(layout);

    clearLayout(storage);
    expect(hasSavedLayout(storage)).toBe(false);
    expect(loadLayout(storage)).toEqual(defaultLayout());
    expect(storage.getItem("xtrader.theme")).toBe("dark");
  });

  it("falls back to the default when what is saved can't be read", () => {
    expect(loadLayout(memoryStorage({ [LAYOUT_KEY]: "{not json" }))).toEqual(defaultLayout());
    expect(loadLayout(memoryStorage({ [LAYOUT_KEY]: "[1,2]" }))).toEqual(defaultLayout());
    expect(loadLayout(null)).toEqual(defaultLayout());
  });

  it("never throws without a storage, or with one that refuses", () => {
    expect(loadLayout(brokenStorage)).toEqual(defaultLayout());
    expect(hasSavedLayout(brokenStorage)).toBe(false);
    expect(() => saveLayout(defaultLayout(), brokenStorage)).not.toThrow();
    expect(() => clearLayout(brokenStorage)).not.toThrow();
    expect(() => saveLayout(defaultLayout(), null)).not.toThrow();
  });
});
