import { describe, expect, it } from "vitest";
import {
  PermAgentsManage,
  PermAgentsView,
  PermBacktestsView,
  PermDashboardView,
  PermPerformanceView,
  PermTradingOperate,
  PermUsersView,
} from "../api/types";
import {
  accessOf,
  allowedTabs,
  can,
  canSee,
  landingPage,
  NO_ACCESS,
  pageOf,
  shownTab,
} from "./access";

const access = (...permissions: string[]) =>
  accessOf({ authenticated: true, username: "maria", permissions });

describe("access", () => {
  it("reads the session", () => {
    const a = accessOf({
      authenticated: true,
      username: "maria",
      role: { id: 2, name: "Operador" },
      permissions: [PermDashboardView, PermTradingOperate],
    });
    expect(a.username).toBe("maria");
    expect(a.role?.name).toBe("Operador");
    expect(can(a, PermTradingOperate)).toBe(true);
    expect(can(a, PermAgentsManage, PermDashboardView)).toBe(true);
    expect(can(a, PermAgentsManage)).toBe(false);
    expect(accessOf({ authenticated: false }).permissions.size).toBe(0);
    expect(can(NO_ACCESS, PermDashboardView)).toBe(false);
  });

  it("shows the sections of the settings allowed, in their order", () => {
    expect(allowedTabs(access(PermUsersView, PermAgentsView))).toEqual([
      "agentes",
      "usuarios",
      "arquivo",
    ]);
    expect(allowedTabs(access(PermDashboardView))).toEqual(["layout", "arquivo"]);
    expect(allowedTabs(access(PermPerformanceView)), "nothing of a file").toEqual([]);
    expect(allowedTabs(access())).toEqual([]);
    const agents = access(PermAgentsView, PermUsersView);
    expect(shownTab(agents, "usuarios")).toBe("usuarios");
    expect(shownTab(agents, "telegram")).toBe("agentes");
    expect(shownTab(access(), "agentes")).toBeNull();
  });

  it("shows the pages allowed, landing on the first one", () => {
    const viewer = access(PermPerformanceView, PermBacktestsView);
    expect(canSee(viewer, "dashboard")).toBe(false);
    expect(canSee(viewer, "performance")).toBe(true);
    expect(canSee(viewer, "settings")).toBe(false);
    expect(landingPage(viewer)).toBe("/performance");
    expect(landingPage(access(PermDashboardView))).toBe("/");
    expect(landingPage(access(PermUsersView))).toBe("/settings");
    expect(landingPage(access())).toBeNull();
    expect(pageOf("/settings")).toBe("settings");
    expect(pageOf("/backtesting")).toBe("backtesting");
    expect(pageOf("/performance")).toBe("performance");
    expect(pageOf("/")).toBe("dashboard");
  });
});
