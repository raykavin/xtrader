// How the price is drawn on the dashboard: as candles, or as a line of the
// closes over an area fading downwards. Kept in the browser (localStorage):
// the page reloaded shows it as left; the candles without one saved.

import { browserStorage } from "./layout";

export type ChartType = "candles" | "line";

/** The key of the type in localStorage. */
export const CHART_TYPE_KEY = "xtrader.chart.type";

/** The type of the chart of value, null for something else. */
export function chartTypeOf(value: unknown): ChartType | null {
  return value === "line" || value === "candles" ? value : null;
}

/** The type saved in storage, the candles without one. */
export function loadChartType(storage: Storage | null = browserStorage()): ChartType {
  try {
    return storage?.getItem(CHART_TYPE_KEY) === "line" ? "line" : "candles";
  } catch {
    return "candles";
  }
}

/** Saves the type in storage, when it can be reached. */
export function saveChartType(type: ChartType, storage: Storage | null = browserStorage()) {
  try {
    storage?.setItem(CHART_TYPE_KEY, type);
  } catch {
    // no storage (private mode, quota): the candles next time
  }
}
