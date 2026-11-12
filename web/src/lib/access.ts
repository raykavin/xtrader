// The access of the user of the dashboard: the permissions of its role, the
// implied ones included (the controller adds them, see ui.Effective), as the
// session tells them. The controller refuses on its own whatever the user may
// not do; the dashboard only hides it.

import {
  PermAgentsView,
  PermBacktestsView,
  PermDashboardView,
  PermLimitsView,
  PermMT5AccountsView,
  PermNewsView,
  PermPerformanceView,
  PermPromptsView,
  PermSupervisorView,
  PermTelegramView,
  PermUsersView,
  type RoleRef,
  type SessionResponse,
} from "../api/types";
import { configViewPermissions } from "./configFile";
import { type SettingsTab, settingsTabs } from "./settings";

/** What the user of the session may see and do. */
export interface Access {
  username?: string;
  role?: RoleRef;
  permissions: ReadonlySet<string>;
}

/** No access: nothing allowed, eg: before the session is read. */
export const NO_ACCESS: Access = { permissions: new Set() };

/** The access a session tells. */
export function accessOf(session: SessionResponse): Access {
  return {
    username: session.username || undefined,
    role: session.role,
    permissions: new Set(session.permissions ?? []),
  };
}

/** Whether access grants any of perms. */
export function can(access: Access, ...perms: string[]): boolean {
  return perms.some((p) => access.permissions.has(p));
}

/** The pages of the dashboard. */
export type Page = "dashboard" | "performance" | "backtesting" | "settings";

/** The permissions each section of the settings needs to be shown, any one of them enough. */
export const tabPermissions: Record<SettingsTab, readonly string[]> = {
  agentes: [PermAgentsView],
  contas: [PermMT5AccountsView],
  limites: [PermLimitsView],
  noticias: [PermNewsView],
  prompts: [PermPromptsView],
  supervisor: [PermSupervisorView],
  telegram: [PermTelegramView],
  usuarios: [PermUsersView],
  layout: [PermDashboardView], // the layout of the panel
  arquivo: configViewPermissions, // each section of the file by its own permission
};

/** The sections of the settings access shows, in their order. */
export function allowedTabs(access: Access): SettingsTab[] {
  return settingsTabs.filter((tab) => can(access, ...tabPermissions[tab]));
}

/** The section of the settings to show: tab when allowed, else the first allowed one, null for none. */
export function shownTab(access: Access, tab: SettingsTab): SettingsTab | null {
  const allowed = allowedTabs(access);
  return allowed.includes(tab) ? tab : (allowed[0] ?? null);
}

/** Whether access shows the page. */
export function canSee(access: Access, page: Page): boolean {
  switch (page) {
    case "dashboard":
      return can(access, PermDashboardView);
    case "performance":
      return can(access, PermPerformanceView);
    case "backtesting":
      return can(access, PermBacktestsView);
    case "settings":
      return allowedTabs(access).length > 0;
  }
}

/** The page of a path of the dashboard. */
export function pageOf(path: string): Page {
  if (path.startsWith("/settings")) return "settings";
  if (path.startsWith("/performance")) return "performance";
  if (path.startsWith("/backtesting")) return "backtesting";
  return "dashboard";
}

const pagePaths: Record<Page, string> = {
  dashboard: "/",
  performance: "/performance",
  backtesting: "/backtesting",
  settings: "/settings",
};

/** The first page access shows, in the order of the navigation: null for none. */
export function landingPage(access: Access): string | null {
  const page = (["dashboard", "performance", "backtesting", "settings"] as const).find((p) =>
    canSee(access, p),
  );
  return page ? pagePaths[page] : null;
}
