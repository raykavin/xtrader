import { describe, expect, it } from "vitest";
import {
  PermAgentsManage,
  PermAgentsView,
  PermDashboardView,
  type PermissionArea,
  PermPerformanceView,
  PermSupervisorChat,
  PermTradingOperate,
} from "../api/types";
import {
  choicesOf,
  impliedBy,
  permissionsOf,
  roleNameError,
  roleSummary,
  setChoice,
} from "./roles";

const areas: PermissionArea[] = [
  { key: "dashboard", label: "Painel", hint: "", view: PermDashboardView, view_label: "Ver" },
  {
    key: "trading",
    label: "Operação",
    hint: "",
    manage: PermTradingOperate,
    manage_label: "Operar",
  },
  {
    key: "chat",
    label: "Chat do XTrader AI",
    hint: "",
    view: PermSupervisorChat,
    view_label: "Usar",
  },
  {
    key: "performance",
    label: "Desempenho",
    hint: "",
    view: PermPerformanceView,
    view_label: "Ver",
  },
  {
    key: "agents",
    label: "Agentes",
    hint: "",
    view: PermAgentsView,
    view_label: "Ver",
    manage: PermAgentsManage,
    manage_label: "Gerenciar",
  },
];

describe("roles", () => {
  it("reads and writes the permissions of the matrix, in the order of the catalog", () => {
    const choices = choicesOf([PermAgentsManage, PermDashboardView, "made:up"], areas);
    expect(choices.agents).toEqual({ view: false, manage: true });
    expect(choices.dashboard).toEqual({ view: true, manage: false });
    expect(permissionsOf(choices, areas)).toEqual([
      PermDashboardView,
      PermAgentsView,
      PermAgentsManage,
    ]);
  });

  it("manages an area seen, and no longer manages one no longer seen", () => {
    const agents = areas[4] as PermissionArea;
    let choices = choicesOf([], areas);
    choices = setChoice(choices, agents, "manage", true);
    expect(choices.agents).toEqual({ view: true, manage: true });
    choices = setChoice(choices, agents, "view", false);
    expect(choices.agents).toEqual({ view: false, manage: false });
    const trading = areas[1] as PermissionArea;
    choices = setChoice(choices, trading, "manage", true);
    expect(permissionsOf(choices, areas)).toEqual([PermTradingOperate]);
  });

  it("tells the panel seen by who operates or chats", () => {
    expect(impliedBy(PermDashboardView, choicesOf([PermTradingOperate], areas), areas)).toBe(
      "Operação",
    );
    expect(impliedBy(PermDashboardView, choicesOf([PermSupervisorChat], areas), areas)).toBe(
      "Chat do XTrader AI",
    );
    expect(impliedBy(PermDashboardView, choicesOf([PermAgentsView], areas), areas)).toBeUndefined();
    expect(
      impliedBy(PermAgentsView, choicesOf([PermTradingOperate], areas), areas),
    ).toBeUndefined();
  });

  it("sums a role up", () => {
    expect(roleSummary([PermDashboardView, PermTradingOperate, PermAgentsView], areas)).toBe(
      "Painel · Operação · Agentes: ver",
    );
    expect(roleSummary([PermAgentsView, PermAgentsManage], areas)).toBe("Agentes: gerenciar");
    expect(roleSummary([], areas)).toBe("Nenhuma permissão");
  });

  it("checks the name of a role", () => {
    expect(roleNameError("  ")).toBe("Informe o nome do papel.");
    expect(roleNameError("é".repeat(65))).toBe("Use até 64 caracteres no nome.");
    expect(roleNameError("Observador")).toBeNull();
  });
});
