import { describe, expect, it } from "vitest";
import type { AgentSettings, AgentsParamResult } from "../api/types";
import {
  type AgentsParamForm,
  agentsParamErrors,
  agentsParamRequest,
  emptyAgentsParamForm,
  paramKeys,
  paramPreview,
  paramResultLabel,
  paramSummary,
  paramUsage,
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

const form = (fields: Partial<AgentsParamForm>): AgentsParamForm => ({
  ...emptyAgentsParamForm,
  ...fields,
});

const result = (fields: Partial<AgentsParamResult>): AgentsParamResult => ({
  agent: "gold",
  enabled: true,
  status: "updated",
  ...fields,
});

describe("the global parametrizer", () => {
  it("checks the key and the value as the params of an agent, a param of the LLM taken", () => {
    expect(agentsParamErrors(form({ key: "lot", value: "0.02" }))).toEqual({});
    expect(agentsParamErrors(form({ key: "llm-min-stop-atr", value: "2" }))).toEqual({});
    expect(agentsParamErrors(form({ key: "lot", value: "" }))).toEqual({});
    expect(agentsParamErrors(form({ key: "", value: "1" })).key).toBe(
      "Informe o nome do parâmetro.",
    );
    expect(agentsParamErrors(form({ key: "-lot", value: "1" })).key).toBe(
      "Sem o traço: lot, não -lot.",
    );
    expect(agentsParamErrors(form({ key: "Lot", value: "1" })).key).toContain("letras minúsculas");
    expect(agentsParamErrors(form({ key: "mode", value: "paper" })).key).toContain(
      "configuração do próprio agente",
    );
    expect(agentsParamErrors(form({ key: "lot", value: "lot = 0.02" })).value).toContain(
      "repete o nome",
    );
  });

  it("sends the key and the value trimmed, with both options", () => {
    expect(
      agentsParamRequest(
        form({ key: " lot ", value: " 0.02 ", addMissing: true, restartActive: false }),
      ),
    ).toEqual({ key: "lot", value: "0.02", add_missing: true, restart_active: false });
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
      unchanged: 1,
      without: 2,
      restart: 0,
    });
    expect(
      paramPreview(
        agents,
        form({ key: "lot", value: "0.02", addMissing: true, restartActive: true }),
      ),
    ).toEqual({ update: 2, add: 2, unchanged: 1, without: 0, restart: 3 });
    expect(
      paramPreview(agents, form({ key: "llm-consult-interval", value: "15m", addMissing: true })),
    ).toEqual({ update: 0, add: 1, unchanged: 0, without: 4, restart: 0 });
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

  it("sums up the results and the restarts", () => {
    const results = [
      result({ status: "updated" }),
      result({ agent: "btc", status: "updated" }),
      result({ agent: "llm", status: "added", restart: "restarted" }),
      result({ agent: "x", status: "skipped", restart: "failed" }),
      result({ agent: "y", status: "failed" }),
    ];
    expect(paramSummary(results, true)).toEqual({
      params: "2 alterados, 1 adicionado, 1 ignorado, 1 falha",
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
