// The configuration files of the controller: their sections and items picked
// to export or to import, the preferences of this browser (the dashboard
// section, kept in localStorage and handled here, the controller never
// seeing them), and the reading of a file chosen to import.

import {
  type ConfigCatalog,
  type ConfigFile,
  ConfigFormat,
  type ConfigPreview,
  type ConfigPreviewItem,
  ConfigSectionDashboard,
  ConfigStatusChanged,
  ConfigStatusInvalid,
  ConfigStatusNew,
  ConfigStatusSame,
  ConfigVersion,
  MaxConfigFile,
  MinConfigPassword,
  PermAgentsManage,
  PermAgentsView,
  PermDashboardView,
  PermLimitsManage,
  PermLimitsView,
  PermMT5AccountsManage,
  PermMT5AccountsView,
  PermNewsManage,
  PermNewsView,
  PermPromptsManage,
  PermPromptsView,
  PermSupervisorManage,
  PermSupervisorView,
  PermTelegramManage,
  PermTelegramView,
  PermUsersManage,
  PermUsersView,
} from "../api/types";
import { chartTypeOf, loadChartType, saveChartType } from "./chartType";
import {
  CLOSED_PERIOD_KEY,
  loadClosedPeriod,
  normalizeClosedPeriod,
  PERFORMANCE_PERIOD_KEY,
  saveClosedPeriod,
} from "./closedPL";
import { setValuesHidden, valuesHidden } from "./hiddenValues";
import { browserStorage, loadLayout, normalizeLayout, saveLayout } from "./layout";
import { loadTheme, saveTheme, type Theme } from "./theme";

/** The items picked of each section, by their keys. */
export type ConfigSelection = Record<string, string[]>;

/** An item of a section in the tree of the export or of the import. */
export interface ConfigTreeItem {
  key: string;
  label: string;
  /** Of an import: its state, the fields that change, its warnings and why it is refused. */
  preview?: ConfigPreviewItem;
}

/** A section of the tree of the export or of the import, its items. */
export interface ConfigTreeSection {
  key: string;
  label: string;
  items: ConfigTreeItem[];
  /** Of an export: whether its items have secrets, exported when asked. */
  secrets?: boolean;
}

/** The preferences of this browser in a configuration file, by key, in their order. */
export const browserItems = [
  { key: "layout", label: "Layout do painel" },
  { key: "theme", label: "Tema" },
  { key: "chart_type", label: "Tipo de gráfico" },
  { key: "hide_values", label: "Ocultar valores" },
  { key: "closed_period", label: "Período do P/L fechado" },
  { key: "performance_period", label: "Período do Desempenho" },
] as const;

export type BrowserItem = (typeof browserItems)[number]["key"];

/** The label of the section of the preferences of this browser. */
export const BROWSER_SECTION_LABEL = "Preferências deste navegador";

/** The preferences of this browser as they stand, by key. */
export function browserPrefs(
  storage: Storage | null = browserStorage(),
): Record<BrowserItem, unknown> {
  return {
    layout: loadLayout(storage),
    theme: loadTheme(),
    chart_type: loadChartType(storage),
    hide_values: valuesHidden(),
    closed_period: loadClosedPeriod(storage),
    performance_period: loadClosedPeriod(storage, PERFORMANCE_PERIOD_KEY),
  };
}

/** The preference of key read from a file, as this browser keeps it; null for something it doesn't take. */
export function browserPrefOf(key: string, value: unknown): unknown {
  switch (key) {
    case "layout":
      // a layout of a file that isn't one is refused, not read as the default one
      return typeof value === "object" && value !== null && !Array.isArray(value)
        ? normalizeLayout(value)
        : null;
    case "theme":
      return value === "dark" || value === "light" ? value : null;
    case "chart_type":
      return chartTypeOf(value);
    case "hide_values":
      return typeof value === "boolean" ? value : null;
    case "closed_period":
    case "performance_period":
      return normalizeClosedPeriod(value);
  }
  return null;
}

/** The section of the preferences of this browser in the tree of the export: all of them. */
export function browserSection(): ConfigTreeSection {
  return {
    key: ConfigSectionDashboard,
    label: BROWSER_SECTION_LABEL,
    items: browserItems.map(({ key, label }) => ({ key, label })),
  };
}

/** The sections of the tree of the export: the ones of the catalog, then the preferences of this browser. */
export function exportTree(catalog: ConfigCatalog): ConfigTreeSection[] {
  return [
    ...catalog.sections.map((s) => ({
      key: s.key,
      label: s.label,
      items: s.items.map((it) => ({ key: it.key, label: it.label })),
      secrets: s.secrets,
    })),
    browserSection(),
  ];
}

/** The permissions of the imports of the controller: the change of an area, any of them. */
export const configManagePermissions = [
  PermPromptsManage,
  PermMT5AccountsManage,
  PermAgentsManage,
  PermUsersManage,
  PermLimitsManage,
  PermNewsManage,
  PermSupervisorManage,
  PermTelegramManage,
];

/** The permissions of the exports: the panel (the preferences of this browser) or the view of an area, any of them. */
export const configViewPermissions = [
  PermDashboardView,
  PermPromptsView,
  PermMT5AccountsView,
  PermAgentsView,
  PermUsersView,
  PermLimitsView,
  PermNewsView,
  PermSupervisorView,
  PermTelegramView,
];

/** Every item of the sections of tree picked. */
export function selectAll(tree: ConfigTreeSection[]): ConfigSelection {
  const selection: ConfigSelection = {};
  for (const section of tree) {
    const keys = section.items.filter((it) => importable(it)).map((it) => it.key);
    if (keys.length > 0) selection[section.key] = keys;
  }
  return selection;
}

/** Whether an item may be picked: not one refused by the preview. */
export function importable(item: ConfigTreeItem): boolean {
  return item.preview?.status !== ConfigStatusInvalid;
}

/** The items of an import picked at first: the new and the changed ones. */
export function defaultImportSelection(tree: ConfigTreeSection[]): ConfigSelection {
  const selection: ConfigSelection = {};
  for (const section of tree) {
    const keys = section.items
      .filter(
        (it) =>
          it.preview?.status === ConfigStatusNew || it.preview?.status === ConfigStatusChanged,
      )
      .map((it) => it.key);
    if (keys.length > 0) selection[section.key] = keys;
  }
  return selection;
}

/** The selection with the item of key of section picked (on) or not, the items in the order of the section. */
export function pickItem(
  selection: ConfigSelection,
  section: ConfigTreeSection,
  key: string,
  on: boolean,
): ConfigSelection {
  const picked = new Set(selection[section.key] ?? []);
  if (on) picked.add(key);
  else picked.delete(key);
  return pickItems(selection, section, [...picked]);
}

/** The selection with every item of section that may be picked picked (on), or none. */
export function pickSection(
  selection: ConfigSelection,
  section: ConfigTreeSection,
  on: boolean,
): ConfigSelection {
  return pickItems(
    selection,
    section,
    on ? section.items.filter(importable).map((it) => it.key) : [],
  );
}

function pickItems(
  selection: ConfigSelection,
  section: ConfigTreeSection,
  keys: string[],
): ConfigSelection {
  const next = { ...selection };
  const ordered = section.items.map((it) => it.key).filter((key) => keys.includes(key));
  if (ordered.length > 0) next[section.key] = ordered;
  else delete next[section.key];
  return next;
}

/** How many items are picked. */
export function pickedCount(selection: ConfigSelection): number {
  return Object.values(selection).reduce((sum, keys) => sum + keys.length, 0);
}

/** The selection without the preferences of this browser: what the controller exports and imports. */
export function serverSelection(selection: ConfigSelection): ConfigSelection {
  const { [ConfigSectionDashboard]: _browser, ...rest } = selection;
  return rest;
}

/** Whether a section with secrets has an item picked. */
export function pickedSecrets(tree: ConfigTreeSection[], selection: ConfigSelection): boolean {
  return tree.some((s) => s.secrets && (selection[s.key]?.length ?? 0) > 0);
}

/** Why the password of the secrets of an export is refused, null when it is taken. */
export function exportPasswordError(password: string, confirm: string): string | null {
  if ([...password].length < MinConfigPassword) {
    return `A senha deve ter ao menos ${MinConfigPassword} caracteres.`;
  }
  if (password !== confirm) return "As duas senhas são diferentes.";
  return null;
}

/** The file of the export: the one of the controller, the preferences of this browser picked added. */
export function withBrowserPrefs(
  file: ConfigFile,
  selection: ConfigSelection,
  prefs: Record<BrowserItem, unknown>,
): ConfigFile {
  const keys = selection[ConfigSectionDashboard] ?? [];
  if (keys.length === 0) return file;
  const dashboard: Record<string, unknown> = {};
  for (const key of keys) {
    if (key in prefs) dashboard[key] = prefs[key as BrowserItem];
  }
  return { ...file, dashboard };
}

/** The name of the file of an export made on day, eg: xtrader-config-2026-11-11.json. */
export function configFileName(day: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `xtrader-config-${day.getFullYear()}-${pad(day.getMonth() + 1)}-${pad(day.getDate())}.json`;
}

/** The text of the file of an export, indented. */
export function configFileText(file: ConfigFile): string {
  return `${JSON.stringify(file, null, 2)}\n`;
}

/** A file chosen to import: the configuration it holds, or why it is refused. */
export type ReadFile = { file: ConfigFile } | { error: string };

/** Reads the text of a file chosen to import, its size in bytes: a configuration file of a version known only. */
export function readConfigFile(text: string, size: number): ReadFile {
  if (size > MaxConfigFile) {
    return { error: "O arquivo passa de 8 MB: não é um arquivo de configuração do XTrader." };
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return { error: "O arquivo não é um JSON válido: escolha um arquivo exportado por esta tela." };
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    return { error: "O arquivo não é um arquivo de configuração do XTrader." };
  }
  const file = parsed as Partial<ConfigFile>;
  if (file.format !== ConfigFormat) {
    return { error: "O arquivo não é um arquivo de configuração do XTrader." };
  }
  if (typeof file.version !== "number" || file.version < 1 || file.version > ConfigVersion) {
    return {
      error: `O arquivo é da versão ${String(file.version)} do formato, e esta instalação lê até a versão ${ConfigVersion}: atualize o XTrader antes de importá-lo.`,
    };
  }
  return { file: file as ConfigFile };
}

/** Whether a file holds anything for the controller, beyond the preferences of a browser. */
export function hasServerItems(file: ConfigFile): boolean {
  return Boolean(
    file.prompts?.length ||
      file.mt5_accounts?.length ||
      file.agents?.length ||
      file.roles?.length ||
      file.limits ||
      file.news ||
      file.supervisor ||
      file.telegram,
  );
}

/** The preferences of this browser in file, each with its state against the ones of now (never new). */
export function browserPreview(
  file: ConfigFile,
  prefs: Record<BrowserItem, unknown> = browserPrefs(),
): ConfigTreeSection | null {
  const dashboard = file.dashboard;
  if (!dashboard || typeof dashboard !== "object") return null;
  const items: ConfigTreeItem[] = [];
  for (const { key, label } of browserItems) {
    if (!(key in dashboard)) continue;
    const value = browserPrefOf(key, dashboard[key]);
    let preview: ConfigPreviewItem;
    if (value === null) {
      preview = {
        key,
        label,
        status: ConfigStatusInvalid,
        error: "Valor que este navegador não aceita.",
      };
    } else if (JSON.stringify(value) === JSON.stringify(prefs[key])) {
      preview = { key, label, status: ConfigStatusSame };
    } else {
      preview = { key, label, status: ConfigStatusChanged };
    }
    items.push({ key, label, preview });
  }
  return items.length > 0
    ? { key: ConfigSectionDashboard, label: BROWSER_SECTION_LABEL, items }
    : null;
}

/** The sections of the tree of an import: the ones of the preview of the controller, then the preferences of this browser. */
export function importTree(
  preview: ConfigPreview | null,
  browser: ConfigTreeSection | null,
): ConfigTreeSection[] {
  const sections: ConfigTreeSection[] = (preview?.sections ?? []).map((s) => ({
    key: s.key,
    label: s.label,
    items: s.items.map((it) => ({ key: it.key, label: it.label, preview: it })),
  }));
  return browser ? [...sections, browser] : sections;
}

/** The tree with the items an import refused (nothing applied) told as the import tells them. */
export function mergeRefusals(
  tree: ConfigTreeSection[],
  answer: ConfigPreview,
): ConfigTreeSection[] {
  return tree.map((section) => {
    const told = answer.sections.find((s) => s.key === section.key);
    if (!told) return section;
    return {
      ...section,
      items: section.items.map((item) => {
        const refused = told.items.find(
          (it) => it.key === item.key && it.status === ConfigStatusInvalid,
        );
        return refused ? { ...item, preview: refused } : item;
      }),
    };
  });
}

/** The selection without the items of tree that can't be picked any more. */
export function keepImportable(
  selection: ConfigSelection,
  tree: ConfigTreeSection[],
): ConfigSelection {
  let next = selection;
  for (const section of tree) {
    const keys = section.items.filter(importable).map((it) => it.key);
    next = pickItems(
      next,
      section,
      (selection[section.key] ?? []).filter((key) => keys.includes(key)),
    );
  }
  return next;
}

/** What applies a preference of this browser imported to the page open, eg: its theme. */
export interface BrowserAppliers {
  theme?: (theme: Theme) => void;
}

/** Saves the preferences of this browser of file picked, read as this browser keeps them; returns the keys saved. */
export function applyBrowserPrefs(
  file: ConfigFile,
  selection: ConfigSelection,
  appliers: BrowserAppliers = {},
  storage: Storage | null = browserStorage(),
): string[] {
  const dashboard = file.dashboard ?? {};
  const saved: string[] = [];
  for (const key of selection[ConfigSectionDashboard] ?? []) {
    const value = browserPrefOf(key, dashboard[key]);
    if (value === null) continue;
    switch (key) {
      case "layout":
        saveLayout(value as ReturnType<typeof normalizeLayout>, storage);
        break;
      case "theme":
        saveTheme(value as Theme);
        appliers.theme?.(value as Theme);
        break;
      case "chart_type":
        saveChartType(value as "line" | "candles", storage);
        break;
      case "hide_values":
        setValuesHidden(value as boolean);
        break;
      case "closed_period":
        saveClosedPeriod(
          value as NonNullable<ReturnType<typeof normalizeClosedPeriod>>,
          storage,
          CLOSED_PERIOD_KEY,
        );
        break;
      case "performance_period":
        saveClosedPeriod(
          value as NonNullable<ReturnType<typeof normalizeClosedPeriod>>,
          storage,
          PERFORMANCE_PERIOD_KEY,
        );
        break;
      default:
        continue;
    }
    saved.push(key);
  }
  return saved;
}

/** The label of the state of an item of a preview, or of an import once applied. */
export function statusLabel(status: string, applied = false): string {
  switch (status) {
    case ConfigStatusNew:
      return applied ? "Criado" : "Novo";
    case ConfigStatusChanged:
      return "Alterado";
    case ConfigStatusSame:
      return "Igual";
    case ConfigStatusInvalid:
      return applied ? "Recusado" : "Inválido";
  }
  return status;
}

/** How many items of tree have each state. */
export function statusCounts(tree: ConfigTreeSection[]): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const section of tree) {
    for (const item of section.items) {
      const status = item.preview?.status ?? "";
      counts[status] = (counts[status] ?? 0) + 1;
    }
  }
  return counts;
}
