import { describe, expect, it, vi } from "vitest";
import {
  type ConfigFile,
  ConfigFormat,
  type ConfigPreview,
  ConfigSectionAgents,
  ConfigSectionDashboard,
  ConfigSectionPrompts,
  ConfigVersion,
  MaxConfigFile,
} from "../api/types";
import { CHART_TYPE_KEY } from "./chartType";
import { CLOSED_PERIOD_KEY, PERFORMANCE_PERIOD_KEY } from "./closedPL";
import {
  applyBrowserPrefs,
  type BrowserItem,
  browserPreview,
  type ConfigTreeSection,
  configFileName,
  configFileText,
  defaultImportSelection,
  exportPasswordError,
  exportTree,
  hasServerItems,
  importTree,
  keepImportable,
  mergeRefusals,
  pickedCount,
  pickedSecrets,
  pickItem,
  pickSection,
  readConfigFile,
  selectAll,
  serverSelection,
  statusCounts,
  statusLabel,
  withBrowserPrefs,
} from "./configFile";
import { defaultLayout, LAYOUT_KEY } from "./layout";

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

/** A configuration file of the tests, of fields. */
function configFile(fields: Partial<ConfigFile> = {}): ConfigFile {
  return {
    format: ConfigFormat,
    version: ConfigVersion,
    app_schema: 48,
    exported_at: 1_762_800_000,
    exported_by: "admin",
    ...fields,
  };
}

/** The preferences of this browser of the tests, as they stand. */
const prefs: Record<BrowserItem, unknown> = {
  layout: defaultLayout(),
  theme: "dark",
  chart_type: "candles",
  hide_values: false,
  closed_period: { kind: "today" },
  performance_period: { kind: "month" },
};

const catalog = {
  sections: [
    {
      key: ConfigSectionPrompts,
      label: "Prompts",
      items: [{ key: "smc_ict", label: "smc_ict" }],
    },
    {
      key: ConfigSectionAgents,
      label: "Agentes",
      items: [
        { key: "llm", label: "llm" },
        { key: "matrend", label: "matrend" },
      ],
      secrets: true,
    },
  ],
};

/** A preview of the controller of the tests: an item of each state. */
const preview: ConfigPreview = {
  secrets: "none",
  sections: [
    {
      key: ConfigSectionAgents,
      label: "Agentes",
      items: [
        { key: "llm", label: "llm", status: "new", warnings: ["Entra desligado."] },
        { key: "matrend", label: "matrend", status: "changed", changes: ["parâmetros (lot)"] },
        { key: "orb", label: "orb", status: "same" },
        {
          key: "gold",
          label: "gold",
          status: "invalid",
          error: "O agente já existe com o tipo trend.",
        },
      ],
    },
  ],
};

describe("the export", () => {
  it("lists the sections of the catalog, then the preferences of this browser, all picked", () => {
    const tree = exportTree(catalog);
    expect(tree.map((s) => s.key)).toEqual([
      ConfigSectionPrompts,
      ConfigSectionAgents,
      ConfigSectionDashboard,
    ]);
    const selection = selectAll(tree);
    expect(selection[ConfigSectionAgents]).toEqual(["llm", "matrend"]);
    expect(selection[ConfigSectionDashboard]).toHaveLength(6);
    expect(pickedCount(selection)).toBe(9);
    expect(serverSelection(selection)).toEqual({
      [ConfigSectionPrompts]: ["smc_ict"],
      [ConfigSectionAgents]: ["llm", "matrend"],
    });
  });

  it("picks an item, or a whole section, in the order of the section", () => {
    const tree = exportTree(catalog);
    const agents = tree[1] as ConfigTreeSection;
    let selection = pickItem({}, agents, "matrend", true);
    selection = pickItem(selection, agents, "llm", true);
    expect(selection[ConfigSectionAgents]).toEqual(["llm", "matrend"]);
    selection = pickItem(selection, agents, "llm", false);
    expect(selection[ConfigSectionAgents]).toEqual(["matrend"]);
    selection = pickItem(selection, agents, "matrend", false);
    expect(ConfigSectionAgents in selection, "a section without items left out").toBe(false);
    expect(pickSection({}, agents, true)[ConfigSectionAgents]).toEqual(["llm", "matrend"]);
    expect(pickSection(selectAll(tree), agents, false)[ConfigSectionAgents]).toBeUndefined();
  });

  it("offers the secrets while a section having them has an item picked", () => {
    const tree = exportTree(catalog);
    expect(pickedSecrets(tree, selectAll(tree))).toBe(true);
    expect(pickedSecrets(tree, { [ConfigSectionPrompts]: ["smc_ict"] })).toBe(false);
  });

  it("checks the password of the secrets", () => {
    expect(exportPasswordError("curta", "curta")).toBe("A senha deve ter ao menos 8 caracteres.");
    expect(exportPasswordError("uma senha longa", "outra senha")).toBe(
      "As duas senhas são diferentes.",
    );
    expect(exportPasswordError("uma senha longa", "uma senha longa")).toBeNull();
    expect(exportPasswordError("çãõéíóú", "çãõéíóú"), "7 characters, more bytes").not.toBeNull();
  });

  it("adds the preferences of this browser picked to the file of the controller", () => {
    const file = withBrowserPrefs(
      configFile(),
      { [ConfigSectionDashboard]: ["theme", "chart_type"] },
      prefs,
    );
    expect(file.dashboard).toEqual({ theme: "dark", chart_type: "candles" });
    expect(withBrowserPrefs(configFile(), {}, prefs).dashboard, "none picked").toBeUndefined();
  });

  it("names and writes the file", () => {
    expect(configFileName(new Date(2026, 0, 5, 23, 59))).toBe("xtrader-config-2026-01-05.json");
    const text = configFileText(configFile());
    expect(text.endsWith("}\n")).toBe(true);
    expect(JSON.parse(text).format).toBe(ConfigFormat);
  });
});

describe("the import", () => {
  it("reads a configuration file of a version known only", () => {
    const ok = JSON.stringify(configFile());
    expect(readConfigFile(ok, ok.length)).toEqual({ file: configFile() });
    expect(readConfigFile("{nope", 5)).toEqual({
      error: "O arquivo não é um JSON válido: escolha um arquivo exportado por esta tela.",
    });
    expect(readConfigFile("[]", 2)).toHaveProperty("error");
    expect(readConfigFile(JSON.stringify({ format: "profit", version: 1 }), 30)).toEqual({
      error: "O arquivo não é um arquivo de configuração do XTrader.",
    });
    const newer = JSON.stringify(configFile({ version: ConfigVersion + 1 }));
    expect(readConfigFile(newer, newer.length)).toEqual({
      error: `O arquivo é da versão ${ConfigVersion + 1} do formato, e esta instalação lê até a versão ${ConfigVersion}: atualize o XTrader antes de importá-lo.`,
    });
    expect(readConfigFile(ok, MaxConfigFile + 1)).toHaveProperty("error");
  });

  it("tells whether a file has anything for the controller", () => {
    expect(hasServerItems(configFile({ dashboard: { theme: "dark" } }))).toBe(false);
    expect(hasServerItems(configFile({ agents: [] }))).toBe(false);
    expect(hasServerItems(configFile({ news: { enabled: true } as ConfigFile["news"] }))).toBe(
      true,
    );
  });

  it("compares the preferences of this browser of a file with the ones of now", () => {
    const section = browserPreview(
      configFile({
        dashboard: {
          theme: "light",
          chart_type: "candles",
          hide_values: "yes",
          layout: ["not", "a", "layout"],
          closed_period: { kind: "custom", from: "2026-11-01", to: "2026-11-10" },
          unknown: 1,
        },
      }),
      prefs,
    );
    expect(section?.key).toBe(ConfigSectionDashboard);
    expect(section?.items.map((it) => [it.key, it.preview?.status])).toEqual([
      ["layout", "invalid"],
      ["theme", "changed"],
      ["chart_type", "same"],
      ["hide_values", "invalid"],
      ["closed_period", "changed"],
    ]);
    expect(browserPreview(configFile(), prefs), "none in the file").toBeNull();
  });

  it("picks the new and the changed items at first, never the refused ones", () => {
    const tree = importTree(preview, null);
    const selection = defaultImportSelection(tree);
    expect(selection).toEqual({ [ConfigSectionAgents]: ["llm", "matrend"] });
    expect(pickSection({}, tree[0] as ConfigTreeSection, true)[ConfigSectionAgents]).toEqual([
      "llm",
      "matrend",
      "orb",
    ]);
    expect(statusCounts(tree)).toEqual({ new: 1, changed: 1, same: 1, invalid: 1 });
  });

  it("tells the items an import refused, and unpicks them", () => {
    const tree = importTree(preview, null);
    const selection = defaultImportSelection(tree);
    const answer: ConfigPreview = {
      secrets: "none",
      sections: [
        {
          key: ConfigSectionAgents,
          label: "Agentes",
          items: [
            { key: "llm", label: "llm", status: "invalid", error: "O prompt novo não existe." },
            { key: "matrend", label: "matrend", status: "changed" },
          ],
        },
      ],
    };
    const merged = mergeRefusals(tree, answer);
    const llm = merged[0]?.items.find((it) => it.key === "llm");
    expect(llm?.preview?.error).toBe("O prompt novo não existe.");
    expect(merged[0]?.items.find((it) => it.key === "matrend")?.preview?.changes, "kept").toEqual([
      "parâmetros (lot)",
    ]);
    expect(keepImportable(selection, merged)).toEqual({ [ConfigSectionAgents]: ["matrend"] });
  });

  it("saves the preferences of this browser picked, as it keeps them", () => {
    const storage = memoryStorage();
    const theme = vi.fn();
    const file = configFile({
      dashboard: {
        layout: { rows: [[["chart"]]], hidden: [] },
        theme: "light",
        chart_type: "line",
        closed_period: { kind: "week" },
        performance_period: { kind: "custom", from: "2026-11-01", to: "2026-11-10" },
        hide_values: "nope",
      },
    });
    const saved = applyBrowserPrefs(
      file,
      {
        [ConfigSectionDashboard]: [
          "layout",
          "theme",
          "chart_type",
          "closed_period",
          "performance_period",
          "hide_values",
        ],
      },
      { theme },
      storage,
    );
    expect(saved).toEqual(["layout", "theme", "chart_type", "closed_period", "performance_period"]);
    expect(theme).toHaveBeenCalledWith("light");
    expect(storage.getItem(CHART_TYPE_KEY)).toBe("line");
    expect(JSON.parse(storage.getItem(CLOSED_PERIOD_KEY) ?? "null")).toEqual({ kind: "week" });
    expect(JSON.parse(storage.getItem(PERFORMANCE_PERIOD_KEY) ?? "null")).toEqual({
      kind: "custom",
      from: "2026-11-01",
      to: "2026-11-10",
    });
    const layout = JSON.parse(storage.getItem(LAYOUT_KEY) ?? "null");
    expect(layout.rows[0]).toEqual([["chart"]]);
    expect(layout.rows.length, "the cards missing added").toBeGreaterThan(1);

    const none = memoryStorage();
    expect(applyBrowserPrefs(file, { [ConfigSectionDashboard]: ["chart_type"] }, {}, none)).toEqual(
      ["chart_type"],
    );
    expect(none.getItem(LAYOUT_KEY), "only the ones picked").toBeNull();
  });

  it("labels the states of the items", () => {
    expect(statusLabel("new")).toBe("Novo");
    expect(statusLabel("new", true)).toBe("Criado");
    expect(statusLabel("changed")).toBe("Alterado");
    expect(statusLabel("same")).toBe("Igual");
    expect(statusLabel("invalid")).toBe("Inválido");
  });
});
