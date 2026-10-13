// The layout of the cards of the dashboard, as the user arranged it: the
// cards of each area of the page in their order, the ones hidden, and the
// height of the chart. Kept in the browser (localStorage); without a valid one
// saved, the default layout, the one of the page as designed.

/** A card of the dashboard. */
export type CardId =
  | "quote"
  | "sentiment"
  | "results"
  | "chart"
  | "decision"
  | "ticket"
  | "activity";

/**
 * An area of the page holding cards: the overview row at the top, the main
 * column and the side column of the workspace, and the bottom, full width.
 */
export type ZoneId = "overview" | "main" | "side" | "bottom";

/** The areas in the order of the page. */
export const ZONES: readonly ZoneId[] = ["overview", "main", "side", "bottom"];

/** Every card, in the order of the default layout. */
export const CARDS: readonly CardId[] = [
  "quote",
  "sentiment",
  "results",
  "chart",
  "decision",
  "ticket",
  "activity",
];

/** The names of the cards, as the page shows them. */
export const CARD_LABELS: Record<CardId, string> = {
  quote: "Cotação",
  sentiment: "Sentimento do mercado",
  results: "Resultados",
  chart: "Gráfico",
  decision: "Última decisão do LLM",
  ticket: "Boleta",
  activity: "Atividade do agente",
};

/** The names of the areas, as the page tells them. */
export const ZONE_LABELS: Record<ZoneId, string> = {
  overview: "faixa do topo",
  main: "coluna principal",
  side: "coluna lateral",
  bottom: "área de baixo",
};

export interface DashboardLayout {
  /** The cards of each area, in their order, the hidden ones included. */
  zones: Record<ZoneId, CardId[]>;
  /** The cards left out of the page, kept in their place to come back to it. */
  hidden: CardId[];
  /** The height of the chart in CSS pixels, null for the one of the responsive layout. */
  chartHeight: number | null;
  /**
   * The width of the chart in CSS pixels, null for the one of the responsive
   * layout: the width of its column, beside the other column of the workspace,
   * or its own in an area of a single column; never on a screen whose areas are
   * stacked.
   */
  chartWidth: number | null;
}

/** The layout of the page as designed. */
export function defaultLayout(): DashboardLayout {
  return {
    zones: {
      overview: ["quote", "sentiment", "results"],
      main: ["chart", "decision"],
      side: ["ticket"],
      bottom: ["activity"],
    },
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

/** The version of the saved layout: another one is left aside, the default used. */
const LAYOUT_VERSION = 1;

const isCard = (value: unknown): value is CardId =>
  typeof value === "string" && (CARDS as string[]).includes(value);

/** The area of card in the default layout. */
function defaultZone(card: CardId): ZoneId {
  const zones = defaultLayout().zones;
  return ZONES.find((zone) => zones[zone].includes(card)) ?? "bottom";
}

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
 * The layout read from what was saved, whatever it holds: the cards unknown or
 * repeated left out, the ones missing (eg: a card added to the page since)
 * back at the end of their default area; the default layout for anything else
 * than a layout.
 */
export function normalizeLayout(raw: unknown): DashboardLayout {
  if (typeof raw !== "object" || raw === null) return defaultLayout();
  const saved = raw as {
    version?: unknown;
    zones?: unknown;
    hidden?: unknown;
    chartHeight?: unknown;
    chartWidth?: unknown;
  };
  if (saved.version !== undefined && saved.version !== LAYOUT_VERSION) return defaultLayout();
  if (typeof saved.zones !== "object" || saved.zones === null) return defaultLayout();
  const zonesIn = saved.zones as Record<string, unknown>;

  const seen = new Set<CardId>();
  const zones = { overview: [], main: [], side: [], bottom: [] } as Record<ZoneId, CardId[]>;
  for (const zone of ZONES) {
    const cards = zonesIn[zone];
    if (!Array.isArray(cards)) continue;
    for (const card of cards) {
      if (isCard(card) && !seen.has(card)) {
        seen.add(card);
        zones[zone].push(card);
      }
    }
  }
  for (const card of CARDS) {
    if (!seen.has(card)) zones[defaultZone(card)].push(card);
  }
  const hidden = Array.isArray(saved.hidden)
    ? CARDS.filter((card) => (saved.hidden as unknown[]).includes(card))
    : [];
  return {
    zones,
    hidden,
    chartHeight: chartHeightOf(saved.chartHeight),
    chartWidth: chartWidthOf(saved.chartWidth),
  };
}

/** Whether layout is the default one, nothing moved, hidden or resized. */
export function isDefaultLayout(layout: DashboardLayout): boolean {
  const fallback = defaultLayout();
  return (
    layout.hidden.length === 0 &&
    layout.chartHeight === null &&
    layout.chartWidth === null &&
    ZONES.every((zone) => layout.zones[zone].join() === fallback.zones[zone].join())
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

/** The area of card in layout. */
export function zoneOf(layout: DashboardLayout, card: CardId): ZoneId {
  return ZONES.find((zone) => layout.zones[zone].includes(card)) ?? defaultZone(card);
}

/**
 * The layout with card moved to area zone at index, a position among the
 * cards of the area as they are before the move (the hidden ones included).
 */
export function moveCard(
  layout: DashboardLayout,
  card: CardId,
  zone: ZoneId,
  index: number,
): DashboardLayout {
  const from = zoneOf(layout, card);
  const position = layout.zones[from].indexOf(card);
  let at = Math.min(Math.max(index, 0), layout.zones[zone].length);
  if (from === zone && position < at) at -= 1;
  const zones = { ...layout.zones, [from]: layout.zones[from].filter((c) => c !== card) };
  zones[zone] = [...zones[zone].slice(0, at), card, ...zones[zone].slice(at)];
  return { ...layout, zones };
}

/**
 * The layout with card moved one step, as the keyboard moves it: before the
 * card shown before it in its area (delta -1) or after the one shown after it
 * (+1); at the edge of its area, to the end of the area before it or to the
 * start of the one after it. Shown tells the cards on the page.
 */
export function stepCard(
  layout: DashboardLayout,
  card: CardId,
  delta: -1 | 1,
  shown: (card: CardId) => boolean,
): DashboardLayout {
  const zone = zoneOf(layout, card);
  const cards = layout.zones[zone];
  const position = cards.indexOf(card);
  const neighbors = delta < 0 ? cards.slice(0, position).reverse() : cards.slice(position + 1);
  const neighbor = neighbors.find(shown);
  if (neighbor) {
    const at = cards.indexOf(neighbor);
    return moveCard(layout, card, zone, delta < 0 ? at : at + 1);
  }
  const next = ZONES[ZONES.indexOf(zone) + delta];
  if (!next) return layout;
  return moveCard(layout, card, next, delta < 0 ? layout.zones[next].length : 0);
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
