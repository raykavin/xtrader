import { describe, expect, it } from "vitest";
import type { AgentSettings, AgentsParamResult } from "../api/types";
import {
  type AgentsParamForm,
  agentsParamErrors,
  agentsParamRequest,
  emptyAgentsParamForm,
  filtering,
  inFilter,
  paramKeyLabel,
  paramKeys,
  paramPreview,
  paramResultLabel,
  paramRow,
  paramSummary,
  paramUsage,
  refused,
  restartLabel,
  sortedResults,
} from "./agentsParam";

const agent = (fields: Partial<AgentSettings>): AgentSettings => ({
  name: "trend",
  kind: "trend",
  mode: "live",
  api_address: "localhost:5580",
  enabled: true,
  launched: true,
  restart: true,
  trade_panel: false,
  telegram_control: false,
  reanalysis: false,
  running: true,
  restart_pending: false,
  params: {},
  secrets: { llm_api_key: false, bridge_token: false },
  daily_limit: { enabled: false },
  protection: {
    unit: "atr",
    breakeven: { enabled: false, supervisor: false, trigger: 0, margin: 0 },
    trailing: { enabled: false, supervisor: false, trigger: 0, distance: 0, step: 0 },
  },
  ...fields,
});

const agents = [
  agent({ name: "gold", params: { pair: "XAUUSDm", lot: "0.01" } }),
  agent({ name: "btc", params: { pair: "BTCUSDm", lot: "0.01" } }),
  agent({ name: "silver", enabled: false, params: { pair: "XAGUSDm", lot: "0.02" } }),
  agent({ name: "apart", launched: false, params: { pair: "XAUUSDm" } }),
  agent({ name: "llm", kind: "llm", params: { pair: "XAUUSDm", "llm-min-stop-atr": "1" } }),
];

/** The fields of a form: a single param by key and value, or several by params. */
type Fields = Partial<Omit<AgentsParamForm, "rows">> & {
  key?: string;
  value?: string;
  params?: [string, string][];
};

const form = ({ key, value, params, ...fields }: Fields): AgentsParamForm => ({
  ...emptyAgentsParamForm(),
  rows: params ? params.map(([k, v]) => paramRow(k, v)) : [paramRow(key ?? "", value ?? "")],
  ...fields,
});

/** Why each param of the form of fields is refused, in their order ({} when taken), and the whole list. */
const errorsOf = (fields: Fields) => {
  const f = form(fields);
  const errors = agentsParamErrors(f);
  return { rows: f.rows.map((row) => errors.rows[row.id] ?? {}), list: errors.list };
};

const result = (fields: Partial<AgentsParamResult>): AgentsParamResult => ({
  agent: "gold",
  enabled: true,
  status: "updated",
  ...fields,
});

describe("the global parametrizer", () => {
  it("checks each key and value as the params of an agent, a param of the LLM taken", () => {
    const first = (fields: Fields) => errorsOf(fields).rows[0] ?? {};
    expect(first({ key: "lot", value: "0.02" })).toEqual({});
    expect(first({ key: "llm-min-stop-atr", value: "2" })).toEqual({});
    expect(first({ key: "lot", value: "" })).toEqual({});
    expect(first({ key: "", value: "1" }).key).toBe("Informe o nome do parâmetro.");
    expect(first({ key: "", value: "" }).key).toBe("Informe o nome do parâmetro.");
    expect(first({ key: "-lot", value: "1" }).key).toBe("Sem o traço: lot, não -lot.");
    expect(first({ key: "Lot", value: "1" }).key).toContain("letras minúsculas");
    expect(first({ key: "mode", value: "paper" }).key).toContain("configuração do próprio agente");
    expect(first({ key: "lot", value: "lot = 0.02" }).value).toContain("repete o nome");
    expect(refused(agentsParamErrors(form({ key: "lot", value: "1" })))).toBe(false);
    expect(refused(agentsParamErrors(form({ key: "Lot", value: "1" })))).toBe(true);
  });

  it("checks several params: a key once, a row left blank dropped, at most 20", () => {
    expect(
      errorsOf({
        params: [
          ["pair", "XAUUSDm;BTCUSDm"],
          ["lot", "XAUUSDm=0.01;BTCUSDm=0.02"],
          ["", ""],
        ],
      }),
    ).toEqual({ rows: [{}, {}, {}], list: undefined });
    expect(
      errorsOf({
        params: [
          ["lot", "1"],
          [" lot ", "2"],
        ],
      }).rows,
    ).toEqual([{}, { key: "Chave repetida: deixe uma só." }]);
    const many = Array.from({ length: 21 }, (_, i): [string, string] => [`p${i}`, "1"]);
    expect(errorsOf({ params: many }).list).toBe("Use no máximo 20 parâmetros de uma vez.");
  });

  it("sends the params typed, trimmed, with the options and the filters", () => {
    expect(
      agentsParamRequest(
        form({
          params: [
            [" pair ", " XAUUSDm;BTCUSDm "],
            ["", ""],
            ["lot", "XAUUSDm=0.01;BTCUSDm=0.02"],
          ],
          addMissing: true,
          restartActive: false,
          llmOnly: true,
        }),
      ),
    ).toEqual({
      params: [
        { key: "pair", value: "XAUUSDm;BTCUSDm" },
        { key: "lot", value: "XAUUSDm=0.01;BTCUSDm=0.02" },
      ],
      add_missing: true,
      restart_active: false,
      llm_only: true,
      enabled_only: false,
      remove: false,
    });
  });

  it("removes the params by their keys, a value only where the agents hold it, never adding them", () => {
    expect(
      agentsParamRequest(form({ key: " lot ", value: "", remove: true, addMissing: true })),
    ).toEqual({
      params: [{ key: "lot", value: "" }],
      add_missing: false,
      restart_active: false,
      llm_only: false,
      enabled_only: false,
      remove: true,
    });
    // any value: the 3 agents holding lot; 0.01 only: gold and btc, silver holding another one
    expect(
      paramPreview(agents, form({ key: "lot", remove: true, restartActive: true })),
    ).toMatchObject({
      remove: 3,
      other: 0,
      without: 2,
      restart: 3,
    });
    expect(paramPreview(agents, form({ key: "lot", value: "0.01", remove: true }))).toMatchObject({
      remove: 2,
      other: 1,
      without: 2,
      update: 0,
      add: 0,
    });
    expect(
      paramPreview(agents, form({ key: "lot", value: "0.01", remove: true, enabledOnly: true })),
    ).toMatchObject({ remove: 2, other: 0, without: 2, excluded: 1 });
    expect(agentsParamErrors(form({ key: "lot", value: "", remove: true })).rows).toEqual({});
  });

  it("filters the LLM agents, the enabled ones, or the LLM ones enabled", () => {
    const names = (f: AgentsParamForm) => agents.filter((a) => inFilter(a, f)).map((a) => a.name);
    expect(names(form({}))).toEqual(["gold", "btc", "silver", "apart", "llm"]);
    expect(names(form({ llmOnly: true }))).toEqual(["llm"]);
    expect(names(form({ enabledOnly: true }))).toEqual(["gold", "btc", "apart", "llm"]);
    expect(names(form({ llmOnly: true, enabledOnly: true }))).toEqual(["llm"]);
    expect(filtering(form({}))).toBe(false);
    expect(filtering(form({ enabledOnly: true }))).toBe(true);
  });

  it("suggests the keys of the agents and tells how a key stands", () => {
    expect(paramKeys(agents)).toEqual(["llm-min-stop-atr", "lot", "pair"]);
    expect(paramUsage(agents, " lot ")).toEqual({
      holding: 3,
      values: [
        { value: "0.01", count: 2 },
        { value: "0.02", count: 1 },
      ],
    });
    expect(paramUsage(agents, "nothing")).toEqual({ holding: 0, values: [] });
  });

  it("previews the agents changed, added, left, and the ones enabled restarted", () => {
    expect(paramPreview(agents, form({ key: "lot", value: "0.02" }))).toEqual({
      update: 2,
      add: 0,
      remove: 0,
      other: 0,
      unchanged: 1,
      without: 2,
      excluded: 0,
      restart: 0,
    });
    expect(
      paramPreview(
        agents,
        form({ key: "lot", value: "0.02", addMissing: true, restartActive: true }),
      ),
    ).toEqual({
      update: 2,
      add: 2,
      remove: 0,
      other: 0,
      unchanged: 1,
      without: 0,
      excluded: 0,
      restart: 3,
    });
    expect(
      paramPreview(agents, form({ key: "llm-consult-interval", value: "15m", addMissing: true })),
    ).toEqual({
      update: 0,
      add: 1,
      remove: 0,
      other: 0,
      unchanged: 0,
      without: 4,
      excluded: 0,
      restart: 0,
    });
    // the agents out of the filter left out, never restarted
    expect(
      paramPreview(
        agents,
        form({
          key: "lot",
          value: "0.02",
          addMissing: true,
          restartActive: true,
          enabledOnly: true,
        }),
      ),
    ).toEqual({
      update: 2,
      add: 2,
      remove: 0,
      other: 0,
      unchanged: 0,
      without: 0,
      excluded: 1,
      restart: 3,
    });
    expect(
      paramPreview(agents, form({ key: "lot", value: "0.03", restartActive: true, llmOnly: true })),
    ).toEqual({
      update: 0,
      add: 0,
      remove: 0,
      other: 0,
      unchanged: 0,
      without: 1,
      excluded: 4,
      restart: 1,
    });
  });

  it("previews each agent once across several params", () => {
    // silver changes its lot; the others get the new key only, their lot as it is or added
    expect(
      paramPreview(
        agents,
        form({
          params: [
            ["lot", "0.01"],
            ["timeframe", "15m"],
          ],
          addMissing: true,
        }),
      ),
    ).toEqual({
      update: 1,
      add: 4,
      remove: 0,
      other: 0,
      unchanged: 0,
      without: 0,
      excluded: 0,
      restart: 0,
    });
    expect(
      paramPreview(
        agents,
        form({
          params: [
            ["lot", "0.01"],
            ["pair", "XAUUSDm"],
          ],
        }),
      ),
    ).toEqual({
      update: 2,
      add: 0,
      remove: 0,
      other: 0,
      unchanged: 3,
      without: 0,
      excluded: 0,
      restart: 0,
    });
  });

  it("labels each param of an agent when there are several", () => {
    const several = result({
      status: "updated",
      params: [
        { key: "lot", status: "updated", previous: "0.01" },
        { key: "pair", status: "added" },
        { key: "timeframe", status: "unchanged" },
        { key: "llm-x", status: "skipped", detail: "parâmetro só para agentes LLM" },
      ],
    });
    expect(paramResultLabel(several)).toEqual({ label: "Alterado", tone: "on" });
    expect(several.params?.map(paramKeyLabel)).toEqual([
      "lot: alterado (antes 0.01)",
      "pair: adicionado",
      "timeframe: já tinha o valor",
      "llm-x: ignorado (parâmetro só para agentes LLM)",
    ]);
    expect(
      paramResultLabel(
        result({
          status: "unchanged",
          params: [
            { key: "a", status: "unchanged" },
            { key: "b", status: "unchanged" },
          ],
        }),
      ).label,
    ).toBe("Já tinha os valores");
    expect(paramKeyLabel({ key: "lot", status: "updated" })).toBe("lot: alterado (antes vazio)");
  });

  it("labels what was done on each agent", () => {
    expect(paramResultLabel(result({ status: "updated", previous: "0.01" }))).toEqual({
      label: "Alterado",
      tone: "on",
      detail: "Antes: 0.01",
    });
    expect(paramResultLabel(result({ status: "updated" })).detail).toBe("Antes: vazio");
    expect(paramResultLabel(result({ status: "added" }))).toEqual({
      label: "Adicionado",
      tone: "on",
    });
    expect(paramResultLabel(result({ status: "excluded" }))).toEqual({
      label: "Fora do filtro",
      tone: "off",
    });
    expect(paramResultLabel(result({ status: "unchanged" })).label).toBe("Já tinha o valor");
    expect(paramResultLabel(result({ status: "skipped", detail: "sem o parâmetro" }))).toEqual({
      label: "Ignorado",
      tone: "off",
      detail: "Sem o parâmetro",
    });
    expect(paramResultLabel(result({ status: "failed", detail: "O agente x já opera…" }))).toEqual({
      label: "Falhou",
      tone: "error",
      detail: "O agente x já opera…",
    });
  });

  it("labels the restarts, the disabled agents told only when asked", () => {
    expect(restartLabel(result({ restart: "restarted" }), true)).toEqual({
      label: "Reiniciado",
      tone: "on",
    });
    expect(restartLabel(result({ restart: "failed", restart_detail: "Erro." }), true)).toEqual({
      label: "Não reiniciado",
      tone: "error",
      detail: "Erro.",
    });
    expect(
      restartLabel(result({ restart: "skipped", restart_detail: "roda fora do controller" }), true)
        ?.detail,
    ).toBe("Roda fora do controller");
    expect(restartLabel(result({ enabled: false }), true)).toEqual({
      label: "Desligado",
      tone: "off",
    });
    expect(restartLabel(result({ enabled: false }), false)).toBeNull();
    expect(restartLabel(result({}), false)).toBeNull();
  });

  it("labels the params removed", () => {
    expect(paramResultLabel(result({ status: "removed", previous: "30m" }))).toEqual({
      label: "Removido",
      tone: "on",
      detail: "Valor: 30m",
    });
    expect(paramKeyLabel({ key: "timeframe", status: "removed", previous: "30m" })).toBe(
      "timeframe: removido (era 30m)",
    );
    expect(
      paramSummary(
        [result({ status: "removed" }), result({ agent: "btc", status: "skipped" })],
        false,
      ).params,
    ).toBe("1 removido, 1 ignorado");
    expect(
      sortedResults([
        result({ agent: "a", status: "skipped" }),
        result({ agent: "b", status: "removed" }),
      ]).map((r) => r.agent),
    ).toEqual(["b", "a"]);
  });

  it("sums up the results and the restarts", () => {
    const results = [
      result({ status: "updated" }),
      result({ agent: "btc", status: "updated" }),
      result({ agent: "llm", status: "added", restart: "restarted" }),
      result({ agent: "x", status: "skipped", restart: "failed" }),
      result({ agent: "y", status: "failed" }),
      result({ agent: "z1", status: "excluded" }),
      result({ agent: "z2", status: "excluded" }),
    ];
    expect(paramSummary(results, true)).toEqual({
      params: "2 alterados, 1 adicionado, 1 ignorado, 1 falha, 2 fora do filtro",
      restarts: "1 reiniciado, 1 não reiniciado",
    });
    expect(paramSummary([result({ status: "unchanged" })], false)).toEqual({
      params: "1 já tinha o valor",
      restarts: null,
    });
    expect(paramSummary([], true)).toEqual({
      params: "Nenhum agente",
      restarts: "Nenhum agente ligado para reiniciar",
    });
  });

  it("shows the failures first, then the changes, then the others, in their order", () => {
    const results = [
      result({ agent: "a", status: "skipped" }),
      result({ agent: "b", status: "updated" }),
      result({ agent: "c", status: "failed" }),
      result({ agent: "d", status: "unchanged", restart: "failed" }),
      result({ agent: "e", status: "added" }),
    ];
    expect(sortedResults(results).map((r) => r.agent)).toEqual(["c", "d", "b", "e", "a"]);
  });
});
