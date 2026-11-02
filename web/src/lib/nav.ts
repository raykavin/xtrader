// The navigation between the pages of the dashboard: the panel of the agents
// and the sections of the settings, served by the controller on / and
// /settings (each section a hash of it, eg: /settings#prompts).

import type { SettingsTab } from "./settings";

const DASHBOARD_KEY = "xtrader.dashboard.search";

/**
 * Keeps the query of the panel, its agent, pair and timeframe, for the
 * link back to it from the settings: this tab of the browser only.
 */
export function rememberDashboard(search: string, storage: Pick<Storage, "setItem"> | null) {
  try {
    storage?.setItem(DASHBOARD_KEY, search);
  } catch {
    // no storage (private mode, quota): the link goes to the first agent
  }
}

/** The link to the panel: as it was left in this tab, else its default. */
export function dashboardHref(storage: Pick<Storage, "getItem"> | null): string {
  try {
    const search = storage?.getItem(DASHBOARD_KEY) ?? "";
    if (/^\?[\w%.=&-]*$/.test(search) && search.length > 1) return `/${search}`;
  } catch {
    // no storage: the default
  }
  return "/";
}

/** The link to the dashboard of an agent on one of its pairs, the rest of search (eg: the timeframe) kept. */
export function agentPairHref(agent: string, pair: string, search = ""): string {
  const params = new URLSearchParams(search);
  params.set("agent", agent);
  params.set("pair", pair);
  return `/?${params.toString()}`;
}

/** The session storage of the page, null where the browser refuses it. */
export function sessionStore(): Storage | null {
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

/** The link to a section of the settings. */
export const settingsHref = (tab: SettingsTab) => `/settings#${tab}`;
