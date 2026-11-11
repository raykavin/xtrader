// The roles of the users of the dashboard on its settings: the permissions of
// a role as a matrix of the areas of the catalog (see PermissionArea), seen
// and/or changed, and what the dashboard tells of a role.

import {
  PermDashboardView,
  type PermissionArea,
  PermSupervisorChat,
  PermTradingOperate,
} from "../api/types";

/** What a role grants in an area: to see it, to change or operate it. */
export interface AreaChoice {
  view: boolean;
  manage: boolean;
}

/** The choices of each area, by its key, of the permissions granted. */
export function choicesOf(
  permissions: readonly string[],
  areas: readonly PermissionArea[],
): Record<string, AreaChoice> {
  const granted = new Set(permissions);
  const choices: Record<string, AreaChoice> = {};
  for (const area of areas) {
    choices[area.key] = {
      view: !!area.view && granted.has(area.view),
      manage: !!area.manage && granted.has(area.manage),
    };
  }
  return choices;
}

/**
 * The choices with one changed: managing an area sees it too, and no longer
 * seeing it no longer manages it.
 */
export function setChoice(
  choices: Record<string, AreaChoice>,
  area: PermissionArea,
  which: keyof AreaChoice,
  on: boolean,
): Record<string, AreaChoice> {
  const current = choices[area.key] ?? { view: false, manage: false };
  const next = { ...current, [which]: on };
  if (which === "manage" && on && area.view) next.view = true;
  if (which === "view" && !on) next.manage = false;
  return { ...choices, [area.key]: next };
}

/** The permissions of the choices, in the order of the catalog. */
export function permissionsOf(
  choices: Record<string, AreaChoice>,
  areas: readonly PermissionArea[],
): string[] {
  const permissions: string[] = [];
  for (const area of areas) {
    const choice = choices[area.key];
    if (!choice) continue;
    if (area.view && (choice.view || (choice.manage && area.manage))) permissions.push(area.view);
    if (area.manage && choice.manage) permissions.push(area.manage);
  }
  return permissions;
}

/**
 * The label of the area granting a permission the choices imply without
 * granting it, eg: the panel seen by who operates; undefined for none.
 */
export function impliedBy(
  permission: string,
  choices: Record<string, AreaChoice>,
  areas: readonly PermissionArea[],
): string | undefined {
  if (permission !== PermDashboardView) return undefined;
  const granted = new Set(permissionsOf(choices, areas));
  const by = areas.find(
    (area) =>
      (area.manage === PermTradingOperate && granted.has(PermTradingOperate)) ||
      (area.view === PermSupervisorChat && granted.has(PermSupervisorChat)),
  );
  return by?.label;
}

/** What a role grants, area by area, eg: "Painel: ver · Agentes: gerenciar"; "Nenhuma permissão" for none. */
export function roleSummary(
  permissions: readonly string[],
  areas: readonly PermissionArea[],
): string {
  const granted = new Set(permissions);
  const parts: string[] = [];
  for (const area of areas) {
    const manage = !!area.manage && granted.has(area.manage);
    const view = !!area.view && granted.has(area.view);
    if (manage) {
      parts.push(area.view ? `${area.label}: ${area.manage_label?.toLowerCase()}` : area.label);
    } else if (view) {
      parts.push(area.manage ? `${area.label}: ${area.view_label?.toLowerCase()}` : area.label);
    }
  }
  return parts.length > 0 ? parts.join(" · ") : "Nenhuma permissão";
}

/** Why the name of a role is refused, null when it is valid: as the controller checks it. */
export function roleNameError(name: string): string | null {
  const length = [...name.trim()].length;
  if (length === 0) return "Informe o nome do papel.";
  if (length > 64) return "Use até 64 caracteres no nome.";
  return null;
}
