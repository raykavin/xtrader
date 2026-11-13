// The layout of the cards of the dashboard, as the user arranged it: rows of
// columns, each column the cards stacked in it, the cards hidden, and the size
// of the chart. Any card goes in any row and column, beside the others or
// stacked with them; a row or a column left without cards is gone, nothing
// kept for them. Kept in the browser (localStorage); without a valid one saved,
// the default layout, the one of the page as designed.

/** A card of the dashboard. */
export type CardId =
  | "quote"
  | "sentiment"
  | "results"
  | "openpl"
  | "chart"
  | "decision"
  | "activity"
  | "news";

/** Every card, in the order of the default layout. */
export const CARDS: readonly CardId[] = [
  "openpl",
  "quote",
  "sentiment",
  "results",
  "chart",
  "decision",
  "activity",
  "news",
];

/** The names of the cards, as the page shows them. */
export const CARD_LABELS: Record<CardId, string> = {
  quote: "Cotação",
  sentiment: "Sentimento do mercado",
  results: "Resultados",
  openpl: "P/L em aberto",
  chart: "Gráfico",
  decision: "Última decisão do LLM",
  activity: "Atividade do agente",
  news: "Notícias",
};

/** The cards of a column, top to bottom. */
export type Column = CardId[];

/** The columns of a row, left to right. */
export type Row = Column[];

export interface DashboardLayout {
  /** The rows of the page, top to bottom, the hidden cards included in their place. */
  rows: Row[];
  /** The cards left out of the page, kept in their place to come back to it. */
  hidden: CardId[];
  /** The height of the chart in CSS pixels, null for the one of the responsive layout. */
  chartHeight: number | null;
  /**
   * The width of the chart in CSS pixels, null for the one of the responsive
   * layout: the width of its column, the other columns of its row sharing the
   * rest; never on a screen whose rows are stacked.
   */
  chartWidth: number | null;
}

/** The layout of the page as designed. */
export function defaultLayout(): DashboardLayout {
  return {
    rows: [
      [["openpl"]],
      [["quote"], ["sentiment"], ["results"]],
      [["chart", "decision"]],
      [["activity"], ["news"]],
    ],
    hidden: [],
    chartHeight: null,
    chartWidth: null,
  };
}

/** The bounds of the height of the chart, in CSS pixels. */
export const CHART_MIN_HEIGHT = 280;
export const CHART_MAX_HEIGHT = 1600;

/** The bounds of the width of the chart, in CSS pixels. */
export const CHART_MIN_WIDTH = 360;
export const CHART_MAX_WIDTH = 4000;

/** The key of the layout in localStorage. */
export const LAYOUT_KEY = "xtrader.dashboard.layout";

/**
 * The version of the saved layout: 2 for rows, 1 for the areas of before
 * (the overview row, the main and side columns, the bottom), read into rows;
 * another one is left aside, the default used.
 */
const LAYOUT_VERSION = 2;

const isCard = (value: unknown): value is CardId =>
  typeof value === "string" && (CARDS as string[]).includes(value);

/** The value within min and max, rounded; null for none or what isn't a number. */
function bounded(value: unknown, min: number, max: number): number | null {
  if (typeof value !== "number" || !Number.isFinite(value)) return null;
  return Math.round(Math.min(Math.max(value, min), max));
}

/** The height of the chart within its bounds, rounded; null for none or what isn't one. */
export function chartHeightOf(value: unknown): number | null {
  return bounded(value, CHART_MIN_HEIGHT, CHART_MAX_HEIGHT);
}

/** The width of the chart within its bounds, rounded; null for none or what isn't one. */
export function chartWidthOf(value: unknown): number | null {
  return bounded(value, CHART_MIN_WIDTH, CHART_MAX_WIDTH);
}

/**
 * The rows of a layout of version 1: the overview a row of a column per card,
 * the main and side columns a row, the bottom a column.
 */
function rowsOfZones(zones: Record<string, unknown>): unknown[] {
  const list = (zone: string) => (Array.isArray(zones[zone]) ? (zones[zone] as unknown[]) : []);
  return [list("overview").map((card) => [card]), [list("main"), list("side")], [list("bottom")]];
}

/**
 * The layout read from what was saved, whatever it holds: the cards unknown or
 * repeated left out, the columns and rows left empty gone, the cards missing
 * (eg: a card added to the page since) in rows of their own at the end; a
 * layout of version 1 read into rows; the default layout for anything else
 * than a layout.
 */
export function normalizeLayout(raw: unknown): DashboardLayout {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) return defaultLayout();
  const saved = raw as {
    version?: unknown;
    rows?: unknown;
    zones?: unknown;
    hidden?: unknown;
    chartHeight?: unknown;
    chartWidth?: unknown;
  };
  let rawRows: unknown[];
  if (
    (saved.version === undefined || saved.version === LAYOUT_VERSION) &&
    Array.isArray(saved.rows)
  ) {
    rawRows = saved.rows;
  } else if (
    (saved.version === undefined || saved.version === 1) &&
    typeof saved.zones === "object" &&
    saved.zones !== null &&
    !Array.isArray(saved.zones)
  ) {
    rawRows = rowsOfZones(saved.zones as Record<string, unknown>);
  } else {
    return defaultLayout();
  }

  const seen = new Set<CardId>();
  const rows: Row[] = [];
  for (const rawRow of rawRows) {
    if (!Array.isArray(rawRow)) continue;
    const row: Row = [];
    for (const rawColumn of rawRow) {
      if (!Array.isArray(rawColumn)) continue;
      const column: Column = [];
      for (const card of rawColumn) {
        if (isCard(card) && !seen.has(card)) {
          seen.add(card);
          column.push(card);
        }
      }
      if (column.length > 0) row.push(column);
    }
    if (row.length > 0) rows.push(row);
  }
  if (rows.length === 0) return defaultLayout();
  for (const card of CARDS) {
    if (!seen.has(card)) rows.push([[card]]);
  }
  const hidden = Array.isArray(saved.hidden)
    ? CARDS.filter((card) => (saved.hidden as unknown[]).includes(card))
    : [];
  return {
    rows,
    hidden,
    chartHeight: chartHeightOf(saved.chartHeight),
    chartWidth: chartWidthOf(saved.chartWidth),
  };
}

/** Whether the cards of layout are where the default layout has them, hidden or not. */
export function isDefaultArrangement(layout: DashboardLayout): boolean {
  return JSON.stringify(layout.rows) === JSON.stringify(defaultLayout().rows);
}

/** Whether layout is the default one, nothing moved, hidden or resized. */
export function isDefaultLayout(layout: DashboardLayout): boolean {
  return (
    layout.hidden.length === 0 &&
    layout.chartHeight === null &&
    layout.chartWidth === null &&
    isDefaultArrangement(layout)
  );
}

/** The storage of the browser, null where it can't be reached, eg: blocked. */
export function browserStorage(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

/** The layout saved in storage, the default one without a valid one. */
export function loadLayout(storage: Storage | null = browserStorage()): DashboardLayout {
  try {
    const saved = storage?.getItem(LAYOUT_KEY);
    return saved ? normalizeLayout(JSON.parse(saved)) : defaultLayout();
  } catch {
    return defaultLayout(); // unreadable, or not JSON
  }
}

/** Whether a layout is saved in storage. */
export function hasSavedLayout(storage: Storage | null = browserStorage()): boolean {
  try {
    return storage?.getItem(LAYOUT_KEY) != null;
  } catch {
    return false;
  }
}

/** Saves layout in storage; nothing where it can't be written, the layout kept for the page. */
export function saveLayout(
  layout: DashboardLayout,
  storage: Storage | null = browserStorage(),
): void {
  try {
    storage?.setItem(LAYOUT_KEY, JSON.stringify({ version: LAYOUT_VERSION, ...layout }));
  } catch {
    // no storage, or full: the layout lasts as long as the page
  }
}

/** Removes the layout saved in storage, and only it: the default one next. */
export function clearLayout(storage: Storage | null = browserStorage()): void {
  try {
    storage?.removeItem(LAYOUT_KEY);
  } catch {
    // no storage: nothing saved
  }
}

/** Where a card is: its row, its column in the row and its place in the column. */
export interface CardPlace {
  row: number;
  column: number;
  index: number;
}

/** The place of card in rows, null when it isn't there. */
function placeIn(rows: Row[], card: CardId): CardPlace | null {
  for (const [row, columns] of rows.entries()) {
    for (const [column, cards] of columns.entries()) {
      const index = cards.indexOf(card);
      if (index >= 0) return { row, column, index };
    }
  }
  return null;
}

/** The place of card in layout, null when it isn't there. */
export function placeOf(layout: DashboardLayout, card: CardId): CardPlace | null {
  return placeIn(layout.rows, card);
}

const cloneRows = (rows: Row[]): Row[] => rows.map((row) => row.map((column) => [...column]));

/**
 * Where a card is moved to, see moveCard: a column of its own beside the
 * column of another card, before (at its left) or after it; in the column of
 * another card, above it (before) or below it; the place of another card,
 * which takes its place (a swap); or a row of its own before the row of index
 * index, the end for the count of the rows.
 */
export type Placement =
  | { kind: "beside"; card: CardId; side: "before" | "after" }
  | { kind: "stack"; card: CardId; side: "before" | "after" }
  | { kind: "swap"; card: CardId }
  | { kind: "row"; index: number };

/**
 * The layout with card moved to target, see Placement: its column, and then
 * its row, gone when left without cards. Unchanged for a card that isn't
 * there, or a target on itself.
 */
export function moveCard(
  layout: DashboardLayout,
  card: CardId,
  target: Placement,
): DashboardLayout {
  if (target.kind !== "row" && target.card === card) return layout;
  const from = placeOf(layout, card);
  if (!from) return layout;
  const rows = cloneRows(layout.rows);
  const fromRow = rows[from.row];
  const fromColumn = fromRow?.[from.column];
  if (!fromRow || !fromColumn) return layout;

  if (target.kind === "swap") {
    const to = placeIn(rows, target.card);
    const toColumn = to ? rows[to.row]?.[to.column] : undefined;
    if (!to || !toColumn) return layout;
    fromColumn[from.index] = target.card;
    toColumn[to.index] = card;
    return { ...layout, rows };
  }

  // the card out of its place, its column and its row gone when left empty
  fromColumn.splice(from.index, 1);
  let rowGone = false;
  if (fromColumn.length === 0) {
    fromRow.splice(from.column, 1);
    if (fromRow.length === 0) {
      rows.splice(from.row, 1);
      rowGone = true;
    }
  }

  if (target.kind === "row") {
    let index = Math.min(Math.max(target.index, 0), layout.rows.length);
    if (rowGone && from.row < index) index -= 1;
    rows.splice(index, 0, [[card]]);
    return { ...layout, rows };
  }
  const to = placeIn(rows, target.card);
  const toRow = to ? rows[to.row] : undefined;
  const toColumn = to ? toRow?.[to.column] : undefined;
  if (!to || !toRow || !toColumn) return layout;
  const after = target.side === "after" ? 1 : 0;
  if (target.kind === "beside") toRow.splice(to.column + after, 0, [card]);
  else toColumn.splice(to.index + after, 0, card);
  return { ...layout, rows };
}

/** A direction of a move of a card by the keyboard. */
export type Direction = "up" | "down" | "left" | "right";

/**
 * The layout with card moved one step in direction, as the keyboard moves it,
 * shown telling the cards on the page (the hidden ones passed over):
 * - up and down: above the card shown above it in its column, or below the one
 *   below it; at the top or the bottom of its column, out into a row of its own
 *   above or below its row, or, alone in its row, its row swapped with the row
 *   shown above or below;
 * - left and right: out of its column into a column of its own at that side,
 *   or, alone in its column, its column swapped with the column shown at that
 *   side.
 * Unchanged at the edges of the page.
 */
export function stepCard(
  layout: DashboardLayout,
  card: CardId,
  direction: Direction,
  shown: (card: CardId) => boolean,
): DashboardLayout {
  const at = placeOf(layout, card);
  if (!at) return layout;
  const back = direction === "up" || direction === "left";
  const row = layout.rows[at.row];
  const column = row?.[at.column];
  if (!row || !column) return layout;
  const inColumn = column.filter(shown);
  const position = inColumn.indexOf(card);
  const columns = row
    .map((cards, index) => ({ index, shown: cards.some(shown) }))
    .filter((column) => column.shown)
    .map((column) => column.index);

  if (direction === "up" || direction === "down") {
    const neighbor = inColumn[position + (back ? -1 : 1)];
    if (neighbor) {
      return moveCard(layout, card, {
        kind: "stack",
        card: neighbor,
        side: back ? "before" : "after",
      });
    }
    if (columns.length > 1 || inColumn.length > 1) {
      return moveCard(layout, card, { kind: "row", index: back ? at.row : at.row + 1 });
    }
    const rows = layout.rows
      .map((columns, index) => ({ index, shown: columns.some((cards) => cards.some(shown)) }))
      .filter((r) => r.shown)
      .map((r) => r.index);
    const other = rows[rows.indexOf(at.row) + (back ? -1 : 1)];
    if (other === undefined) return layout;
    // its row before the row above, or after the row below
    const next = cloneRows(layout.rows);
    const [moved] = next.splice(at.row, 1);
    if (moved) next.splice(other, 0, moved);
    return { ...layout, rows: next };
  }

  const next = cloneRows(layout.rows);
  const nextRow = next[at.row];
  if (!nextRow) return layout;
  if (inColumn.length > 1) {
    nextRow[at.column]?.splice(at.index, 1);
    nextRow.splice(at.column + (back ? 0 : 1), 0, [card]);
    return { ...layout, rows: next };
  }
  const other = columns[columns.indexOf(at.column) + (back ? -1 : 1)];
  const mine = nextRow[at.column];
  const theirs = other === undefined ? undefined : nextRow[other];
  if (other === undefined || !mine || !theirs) return layout;
  nextRow[at.column] = theirs;
  nextRow[other] = mine;
  return { ...layout, rows: next };
}

/** A row as the page shows it: its index in the layout, and its columns holding cards shown. */
export interface ShownRow {
  index: number;
  columns: { index: number; cards: CardId[] }[];
}

/**
 * The rows of layout as the page shows them, shown telling the cards on it:
 * the columns without one left out, and the rows without such a column.
 */
export function shownRows(layout: DashboardLayout, shown: (card: CardId) => boolean): ShownRow[] {
  const rows: ShownRow[] = [];
  for (const [index, row] of layout.rows.entries()) {
    const columns = row
      .map((cards, column) => ({ index: column, cards: cards.filter(shown) }))
      .filter((column) => column.cards.length > 0);
    if (columns.length > 0) rows.push({ index, columns });
  }
  return rows;
}

/** The layout with card hidden, or shown again in its place. */
export function setHidden(layout: DashboardLayout, card: CardId, hidden: boolean): DashboardLayout {
  const rest = layout.hidden.filter((c) => c !== card);
  return { ...layout, hidden: hidden ? CARDS.filter((c) => c === card || rest.includes(c)) : rest };
}

/** The layout with the chart height, null for the one of the responsive layout. */
export function setChartHeight(layout: DashboardLayout, height: number | null): DashboardLayout {
  return { ...layout, chartHeight: height === null ? null : chartHeightOf(height) };
}

/** The layout with the chart width, null for the one of the responsive layout. */
export function setChartWidth(layout: DashboardLayout, width: number | null): DashboardLayout {
  return { ...layout, chartWidth: width === null ? null : chartWidthOf(width) };
}
