import { describe, expect, it } from "vitest";
import { exposureCheck, exposureRules, MAX_ASSETS, type RuleForm, ruleForm } from "./exposure";

const rows = (...rules: [string[], string][]): RuleForm[] =>
  rules.map(([pairs, maxAgents]) => ruleForm(pairs, maxAgents));

describe("the rules of the exposure limit", () => {
  it("shows the assets of the same limit in one rule, by name, the rules by their limit", () => {
    expect(exposureRules({})).toEqual([]);
    const rules = exposureRules({
      pairs: { XAUUSDm: 2, USOILm: 4, BTCUSDm: 2, UKOILm: 4, "US500.cash": 1 },
      updated_by: "admin",
    });
    expect(rules.map(({ pairs, maxAgents }) => [pairs, maxAgents])).toEqual([
      [["US500.cash"], "1"],
      [["BTCUSDm", "XAUUSDm"], "2"],
      [["UKOILm", "USOILm"], "4"],
    ]);
    expect(new Set(rules.map((r) => r.key)).size).toBe(3);
    expect(ruleForm()).toMatchObject({ pairs: [], maxAgents: "" });
  });

  it("sends the limit of each asset of each rule, no rule for off", () => {
    expect(
      exposureCheck(rows([["XAUUSDm", "BTCUSDm"], " 2 "], [["USOILm", "UKOILm"], "4"])),
    ).toEqual({
      request: { settings: { pairs: { XAUUSDm: 2, BTCUSDm: 2, USOILm: 4, UKOILm: 4 } } },
      rows: {},
    });
    expect(exposureCheck(rows([["US500.cash", "btc#"], "100"])).request).toEqual({
      settings: { pairs: { "US500.cash": 100, "btc#": 100 } },
    });
    expect(exposureCheck([])).toEqual({ request: { settings: { pairs: {} } }, rows: {} });
  });

  it("drops a row left blank, never refusing it", () => {
    expect(exposureCheck(rows([["XAUUSDm"], "2"], [[], " "]))).toEqual({
      request: { settings: { pairs: { XAUUSDm: 2 } } },
      rows: {},
    });
  });

  it("refuses each field of a rule with why, by its row", () => {
    const rules = rows(
      [[], "2"],
      [["XAUUSDm", "XAU USDm"], "2"],
      [["BTCUSDm"], ""],
      [["USOILm"], "0"],
      [["EURUSDm"], "101"],
      [["GBPUSDm"], "1.5"],
      [["USDJPYm"], "-1"],
      [["A".repeat(33)], "1"],
    );
    const { request, rows: errors } = exposureCheck(rules);
    expect(request).toBeNull();
    expect(rules.map((r) => errors[r.key])).toEqual([
      { pairs: "Informe ao menos um ativo." },
      { pairs: "XAU USDm: use o nome do ativo no MT5, ex.: XAUUSDm." },
      { maxAgents: "Informe o máximo de agentes." },
      { maxAgents: "Use um número inteiro de 1 a 100." },
      { maxAgents: "Use um número inteiro de 1 a 100." },
      { maxAgents: "Use um número inteiro de 1 a 100." },
      { maxAgents: "Use um número inteiro de 1 a 100." },
      { pairs: `${"A".repeat(33)}: use o nome do ativo no MT5, ex.: XAUUSDm.` },
    ]);
    expect(exposureCheck(rows([["A".repeat(32)], "1"])).request).not.toBeNull();
  });

  it("refuses an asset in two rules, on the later one, the case telling them apart", () => {
    const rules = rows([["XAUUSDm"], "0"], [["BTCUSDm", "XAUUSDm"], "2"], [["xauusdm"], "1"]);
    const { request, rows: errors } = exposureCheck(rules);
    expect(request).toBeNull();
    expect(rules.map((r) => errors[r.key])).toEqual([
      { maxAgents: "Use um número inteiro de 1 a 100." },
      { pairs: "XAUUSDm já está em outra regra: deixe o ativo numa só." },
      undefined,
    ]);
  });

  it("refuses more assets than the controller takes, of all the rules", () => {
    const pairs = (from: number, count: number) =>
      Array.from({ length: count }, (_, i) => `PAIR${from + i}`);
    const many = rows([pairs(0, 30), "1"], [pairs(30, MAX_ASSETS - 29), "2"]);
    expect(exposureCheck(many)).toMatchObject({ request: null, list: "Use no máximo 50 ativos." });
    expect(exposureCheck(rows([pairs(0, MAX_ASSETS), "1"])).request).not.toBeNull();
  });
});
