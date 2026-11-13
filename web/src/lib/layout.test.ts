import { describe, expect, it } from "vitest";
import {
  type CardId,
  CHART_MAX_HEIGHT,
  CHART_MAX_WIDTH,
  CHART_MIN_HEIGHT,
  CHART_MIN_WIDTH,
  clearLayout,
  type DashboardLayout,
  defaultLayout,
  hasSavedLayout,
  isDefaultArrangement,
  isDefaultLayout,
  LAYOUT_KEY,
  loadLayout,
  moveCard,
  normalizeLayout,
  type Row,
  saveLayout,
  setChartHeight,
  setChartWidth,
  setHidden,
  shownRows,
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

/** The default layout with rows instead of its own. */
const withRows = (rows: Row[]): DashboardLayout => ({ ...defaultLayout(), rows });

/** The default layout of before the news, without their card: the moves of the tests made on it. */
const classic = (): DashboardLayout =>
  withRows([
    [["openpl"]],
    [["quote"], ["sentiment"], ["results"]],
    [["chart", "decision"]],
    [["activity"]],
  ]);

describe("normalizeLayout", () => {
  it("keeps a valid layout as it is", () => {
    const layout = setChartWidth(
      moveCard(defaultLayout(), "activity", { kind: "row", index: 0 }),
      900,
    );
    expect(normalizeLayout({ version: 2, ...layout })).toEqual(layout);
    expect(normalizeLayout(layout)).toEqual(layout);
  });

  it("takes the default for anything else than a layout", () => {
    for (const raw of [
      null,
      undefined,
      3,
      "x",
      [],
      { rows: 1 },
      { rows: [] },
      { rows: [[], [[]], "chart"] },
      { version: 3, rows: [[["chart"]]] },
      { version: 2, zones: { overview: ["chart"] } },
      { zones: [] },
    ]) {
      expect(normalizeLayout(raw)).toEqual(defaultLayout());
    }
  });

  it("leaves out the unknown and repeated cards and the empty columns and rows, the missing cards beside their card or in rows at the end", () => {
    const layout = normalizeLayout({
      version: 2,
      rows: [
        [["chart", "nothing", 7, "chart"], [], ["quote"]],
        [[]],
        "results",
        [["decision"], "activity"],
      ],
      hidden: ["quote", "bogus", "quote"],
      chartHeight: 500,
    });
    expect(layout.rows).toEqual([
      [["chart"], ["news"], ["quote"]],
      [["decision"]],
      [["openpl"]],
      [["sentiment"]],
      [["results"]],
      [["activity"]],
    ]);
    expect(layout.hidden).toEqual(["quote"]);
    expect(layout.chartHeight).toBe(500);
  });

  it("reads a layout of the areas of before into rows, the ticket removed since left out", () => {
    // the default one of version 1, the open profit added since at the end
    expect(
      normalizeLayout({
        version: 1,
        zones: {
          overview: ["quote", "sentiment", "results"],
          main: ["chart", "decision"],
          side: ["ticket"],
          bottom: ["activity"],
        },
        hidden: [],
        chartHeight: null,
      }),
    ).toEqual({
      ...defaultLayout(),
      rows: [
        [["quote"], ["sentiment"], ["results"]],
        [["chart", "decision"], ["news"]],
        [["activity"]],
        [["openpl"]],
      ],
    });
    // the ticket in the overview, the side column empty, the activity at the top
    const layout = normalizeLayout({
      version: 1,
      zones: {
        overview: ["activity", "ticket"],
        main: ["chart"],
        side: [],
        bottom: ["quote", "sentiment", "results", "decision"],
      },
      hidden: ["sentiment"],
      chartHeight: 640,
      chartWidth: 900,
    });
    expect(layout).toEqual({
      rows: [
        [["activity"]],
        [["chart"], ["news"]],
        [["quote", "sentiment", "results", "decision"]],
        [["openpl"]],
      ],
      hidden: ["sentiment"],
      chartHeight: 640,
      chartWidth: 900,
    });
  });

  it("adds the open profit, a card added since, in a row of its own at the end of a layout saved before it", () => {
    const saved = {
      version: 2,
      rows: [[["chart", "decision"]], [["results"], ["quote"], ["sentiment"]], [["activity"]]],
      hidden: ["sentiment"],
      chartHeight: 520,
      chartWidth: null,
    };
    const layout = normalizeLayout(saved);
    // the news beside the chart, where the ticket of the orders was; the open profit in a row of its own
    expect(layout.rows).toEqual([
      [["chart", "decision"], ["news"]],
      [["results"], ["quote"], ["sentiment"]],
      [["activity"]],
      [["openpl"]],
    ]);
    expect(layout.hidden, "the cards hidden kept").toEqual(["sentiment"]);
    expect(layout.chartHeight).toBe(520);
    expect(isDefaultArrangement(layout)).toBe(false);
    // the default one has it first, a row of its own across the page
    expect(defaultLayout().rows[0]).toEqual([["openpl"]]);
  });

  it("keeps the size of the chart within its bounds", () => {
    const rows = [[["chart"]]];
    expect(normalizeLayout({ rows, chartHeight: 10 }).chartHeight).toBe(CHART_MIN_HEIGHT);
    expect(normalizeLayout({ rows, chartHeight: 1e9 }).chartHeight).toBe(CHART_MAX_HEIGHT);
    expect(normalizeLayout({ rows, chartHeight: Number.NaN }).chartHeight).toBeNull();
    expect(normalizeLayout({ rows, chartHeight: "600" }).chartHeight).toBeNull();
    expect(normalizeLayout({ rows, chartHeight: 612.6 }).chartHeight).toBe(613);
    expect(normalizeLayout({ rows, chartWidth: 10 }).chartWidth).toBe(CHART_MIN_WIDTH);
    expect(normalizeLayout({ rows, chartWidth: 1e9 }).chartWidth).toBe(CHART_MAX_WIDTH);
    expect(normalizeLayout({ rows, chartWidth: "900" }).chartWidth).toBeNull();
    expect(normalizeLayout({ rows }).chartWidth).toBeNull();
  });
});

describe("moveCard", () => {
  it("puts a card in a column of its own beside another card's column", () => {
    expect(
      moveCard(classic(), "activity", { kind: "beside", card: "quote", side: "before" }).rows,
    ).toEqual([
      [["openpl"]],
      [["activity"], ["quote"], ["sentiment"], ["results"]],
      [["chart", "decision"]],
    ]);
    // beside the decision, a column after the one of the chart and the decision
    expect(
      moveCard(classic(), "activity", { kind: "beside", card: "decision", side: "after" }).rows,
    ).toEqual([
      [["openpl"]],
      [["quote"], ["sentiment"], ["results"]],
      [["chart", "decision"], ["activity"]],
    ]);
  });

  it("stacks a card above or below another in its column", () => {
    expect(
      moveCard(classic(), "activity", { kind: "stack", card: "chart", side: "before" }).rows,
    ).toEqual([
      [["openpl"]],
      [["quote"], ["sentiment"], ["results"]],
      [["activity", "chart", "decision"]],
    ]);
    expect(
      moveCard(classic(), "quote", { kind: "stack", card: "activity", side: "after" }).rows,
    ).toEqual([
      [["openpl"]],
      [["sentiment"], ["results"]],
      [["chart", "decision"]],
      [["activity", "quote"]],
    ]);
  });

  it("swaps two cards, wherever they are", () => {
    expect(moveCard(classic(), "activity", { kind: "swap", card: "results" }).rows).toEqual([
      [["openpl"]],
      [["quote"], ["sentiment"], ["activity"]],
      [["chart", "decision"]],
      [["results"]],
    ]);
    expect(moveCard(classic(), "chart", { kind: "swap", card: "activity" }).rows).toEqual([
      [["openpl"]],
      [["quote"], ["sentiment"], ["results"]],
      [["activity", "decision"]],
      [["chart"]],
    ]);
  });

  it("puts a card in a row of its own", () => {
    expect(moveCard(classic(), "chart", { kind: "row", index: 0 }).rows).toEqual([
      [["chart"]],
      [["openpl"]],
      [["quote"], ["sentiment"], ["results"]],
      [["decision"]],
      [["activity"]],
    ]);
    expect(moveCard(classic(), "decision", { kind: "row", index: 4 }).rows).toEqual([
      [["openpl"]],
      [["quote"], ["sentiment"], ["results"]],
      [["chart"]],
      [["activity"]],
      [["decision"]],
    ]);
    // the row it leaves gone, the rows after it one up
    expect(moveCard(classic(), "activity", { kind: "row", index: 4 }).rows).toEqual(classic().rows);
    const lone = moveCard(classic(), "activity", { kind: "row", index: 2 }).rows;
    expect(lone).toEqual([
      [["openpl"]],
      [["quote"], ["sentiment"], ["results"]],
      [["activity"]],
      [["chart", "decision"]],
    ]);
    expect(moveCard(withRows(lone), "activity", { kind: "row", index: 4 }).rows).toEqual(
      classic().rows,
    );
  });

  it("leaves no column nor row empty, nothing kept for the card moved", () => {
    const layout = moveCard(
      moveCard(classic(), "results", { kind: "stack", card: "decision", side: "after" }),
      "activity",
      { kind: "beside", card: "sentiment", side: "after" },
    );
    expect(layout.rows).toEqual([
      [["openpl"]],
      [["quote"], ["sentiment"], ["activity"]],
      [["chart", "decision", "results"]],
    ]);
  });

  it("leaves the layout as it is for a card on itself or not there", () => {
    const layout = classic();
    expect(moveCard(layout, "chart", { kind: "swap", card: "chart" })).toBe(layout);
    expect(moveCard(layout, "chart", { kind: "beside", card: "chart", side: "after" })).toBe(
      layout,
    );
    expect(moveCard(withRows([[["chart"]]]), "activity", { kind: "row", index: 0 }).rows).toEqual([
      [["chart"]],
    ]);
  });

  it("never changes the layout it is given", () => {
    const layout = classic();
    moveCard(layout, "chart", { kind: "row", index: 3 });
    moveCard(layout, "activity", { kind: "swap", card: "quote" });
    expect(layout).toEqual(classic());
  });
});

describe("stepCard", () => {
  it("moves a card up and down in its column", () => {
    expect(stepCard(classic(), "decision", "up", all).rows[2]).toEqual([["decision", "chart"]]);
    expect(stepCard(classic(), "chart", "down", all).rows[2]).toEqual([["decision", "chart"]]);
  });

  it("takes a card at the edge of its column out into a row of its own", () => {
    const apart = [
      [["openpl"]],
      [["quote"], ["sentiment"], ["results"]],
      [["chart"]],
      [["decision"]],
      [["activity"]],
    ];
    expect(stepCard(classic(), "chart", "up", all).rows).toEqual(apart);
    expect(stepCard(classic(), "decision", "down", all).rows).toEqual(apart);
  });

  it("swaps the row of a card alone in it with the row beside", () => {
    expect(stepCard(classic(), "activity", "up", all).rows).toEqual([
      [["openpl"]],
      [["quote"], ["sentiment"], ["results"]],
      [["activity"]],
      [["chart", "decision"]],
    ]);
    const top = withRows([[["activity"]], ...classic().rows.slice(0, 3)]);
    expect(stepCard(top, "activity", "down", all).rows).toEqual([
      [["openpl"]],
      [["activity"]],
      [["quote"], ["sentiment"], ["results"]],
      [["chart", "decision"]],
    ]);
  });

  it("swaps the column of a card alone in it with the column beside", () => {
    expect(stepCard(classic(), "results", "left", all).rows[1]).toEqual([
      ["quote"],
      ["results"],
      ["sentiment"],
    ]);
    expect(stepCard(classic(), "quote", "right", all).rows[1]).toEqual([
      ["sentiment"],
      ["quote"],
      ["results"],
    ]);
  });

  it("takes a card out of a column of several into a column of its own", () => {
    expect(stepCard(classic(), "decision", "right", all).rows[2]).toEqual([
      ["chart"],
      ["decision"],
    ]);
    expect(stepCard(classic(), "chart", "left", all).rows[2]).toEqual([["chart"], ["decision"]]);
  });

  it("passes over the hidden cards, and stops at the edges of the page", () => {
    const shown = (card: CardId) => card !== "sentiment";
    // the results swap with the quote, the column shown at their left
    expect(stepCard(classic(), "results", "left", shown).rows[1]).toEqual([
      ["results"],
      ["sentiment"],
      ["quote"],
    ]);
    // the decision hidden, the chart alone in its column: its column swaps
    const noDecision = (card: CardId) => card !== "decision";
    const beside = withRows([[["chart", "decision"], ["activity"]]]);
    expect(stepCard(beside, "chart", "right", noDecision).rows[0]).toEqual([
      ["activity"],
      ["chart", "decision"],
    ]);
    expect(stepCard(classic(), "quote", "left", all)).toEqual(classic());
    expect(stepCard(classic(), "openpl", "up", all)).toEqual(classic());
    expect(stepCard(classic(), "activity", "down", all)).toEqual(classic());
    expect(stepCard(withRows([[["quote"]], [["chart"]]]), "quote", "up", all).rows).toEqual([
      [["quote"]],
      [["chart"]],
    ]);
  });
});

describe("shownRows", () => {
  it("leaves out the columns and the rows without a card shown, keeping their indexes", () => {
    const shown = (card: CardId) => !["quote", "sentiment", "results"].includes(card);
    expect(shownRows(classic(), shown)).toEqual([
      { index: 0, columns: [{ index: 0, cards: ["openpl"] }] },
      { index: 2, columns: [{ index: 0, cards: ["chart", "decision"] }] },
      { index: 3, columns: [{ index: 0, cards: ["activity"] }] },
    ]);
  });
});

describe("setHidden and the size of the chart", () => {
  it("hides a card in its place, and shows it again there", () => {
    const hidden = setHidden(setHidden(defaultLayout(), "activity", true), "quote", true);
    expect(hidden.hidden).toEqual(["quote", "activity"]);
    expect(hidden.rows).toEqual(defaultLayout().rows);
    expect(setHidden(hidden, "activity", false).hidden).toEqual(["quote"]);
    expect(setHidden(hidden, "activity", true).hidden).toEqual(["quote", "activity"]);
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
  });

  it("tells the default layout apart", () => {
    expect(isDefaultLayout(defaultLayout())).toBe(true);
    expect(isDefaultLayout(setHidden(defaultLayout(), "quote", true))).toBe(false);
    expect(isDefaultArrangement(setHidden(defaultLayout(), "quote", true))).toBe(true);
    expect(isDefaultLayout(setChartHeight(defaultLayout(), 500))).toBe(false);
    expect(isDefaultLayout(setChartWidth(defaultLayout(), 900))).toBe(false);
    const moved = moveCard(defaultLayout(), "chart", { kind: "row", index: 0 });
    expect(isDefaultLayout(moved)).toBe(false);
    expect(isDefaultArrangement(moved)).toBe(false);
  });
});

describe("the layout in storage", () => {
  it("is saved, read back and removed alone", () => {
    const storage = memoryStorage({ "xtrader.theme": "dark" });
    expect(loadLayout(storage)).toEqual(defaultLayout());
    expect(hasSavedLayout(storage)).toBe(false);

    const layout = setChartWidth(
      setChartHeight(
        setHidden(
          moveCard(defaultLayout(), "activity", { kind: "row", index: 0 }),
          "sentiment",
          true,
        ),
        700,
      ),
      880,
    );
    saveLayout(layout, storage);
    expect(hasSavedLayout(storage)).toBe(true);
    expect(JSON.parse(storage.getItem(LAYOUT_KEY) ?? "{}").version).toBe(2);
    expect(loadLayout(storage)).toEqual(layout);

    clearLayout(storage);
    expect(hasSavedLayout(storage)).toBe(false);
    expect(loadLayout(storage)).toEqual(defaultLayout());
    expect(storage.getItem("xtrader.theme")).toBe("dark");
  });

  it("reads a layout saved before the rows, the ticket removed since left out", () => {
    const storage = memoryStorage({
      [LAYOUT_KEY]: JSON.stringify({
        version: 1,
        zones: {
          overview: ["quote", "sentiment", "results"],
          main: ["chart", "decision"],
          side: [],
          bottom: ["activity"],
        },
        hidden: ["ticket"],
        chartHeight: null,
        chartWidth: 1000,
      }),
    });
    const layout = loadLayout(storage);
    expect(layout.rows).toEqual([
      [["quote"], ["sentiment"], ["results"]],
      [["chart", "decision"], ["news"]],
      [["activity"]],
      [["openpl"]],
    ]);
    expect(layout.hidden, "the ticket hidden left out").toEqual([]);
    expect(layout.chartWidth).toBe(1000);
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
