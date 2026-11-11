import { describe, expect, it } from "vitest";
import {
  allowedPairs,
  assetAgentsCheck,
  assetRuleForm,
  assetRules,
  ruleAgentsLabel,
} from "./assetAgents";

describe("the agents per asset", () => {
  it("reads the rules of the settings as rows", () => {
    const rows = assetRules({
      rules: [{ pairs: ["XAUUSDm"], agents: ["orb", "trend"] }],
    });
    expect(rows.map((r) => [r.pairs, r.agents])).toEqual([[["XAUUSDm"], ["orb", "trend"]]]);
    expect(assetRules({ rules: [] })).toEqual([]);
  });

  it("checks the rules as the controller does", () => {
    const blank = assetRuleForm();
    const gold = assetRuleForm(["XAUUSDm", "XAGUSDm"], ["orb"]);
    expect(assetAgentsCheck([blank, gold])).toEqual({
      request: { settings: { rules: [{ pairs: ["XAUUSDm", "XAGUSDm"], agents: ["orb"] }] } },
      rows: {},
    });
    expect(assetAgentsCheck([])).toEqual({ request: { settings: { rules: [] } }, rows: {} });

    const noAgent = assetRuleForm(["BTCUSDm"]);
    const noPair = assetRuleForm([], ["orb"]);
    const repeated = assetRuleForm(["XAUUSDm"], ["trend"]);
    const invalid = assetRuleForm(["XAU USD"], ["orb"]);
    const check = assetAgentsCheck([gold, noAgent, noPair, repeated, invalid]);
    expect(check.request).toBeNull();
    expect(check.rows[noAgent.key]).toEqual({ agents: "Escolha quem pode operar estes ativos." });
    expect(check.rows[noPair.key]).toEqual({ pairs: "Informe ao menos um ativo." });
    expect(check.rows[repeated.key]?.pairs).toContain("já está em outra regra");
    expect(check.rows[invalid.key]?.pairs).toContain("use o nome do ativo no MT5");
    expect(check.rows[gold.key]).toBeUndefined();
  });

  it("tells the agents of a rule", () => {
    expect(ruleAgentsLabel([])).toBe("Escolher agentes");
    expect(ruleAgentsLabel(["orb", "trend"])).toBe("orb, trend");
    expect(ruleAgentsLabel(["a", "b", "c"])).toBe("3 agentes");
  });

  it("narrows the assets of an agent as the controller does", () => {
    const rules = [{ pairs: ["XAUUSDm"], agents: ["orb"] }];
    expect(allowedPairs(rules, "trend", ["XAUUSDm", "BTCUSDm"])).toEqual({
      allowed: ["BTCUSDm"],
      excluded: ["XAUUSDm"],
    });
    expect(allowedPairs(rules, "orb", ["XAUUSDm"])).toEqual({ allowed: ["XAUUSDm"], excluded: [] });
    expect(allowedPairs([], "trend", ["XAUUSDm"])).toEqual({ allowed: ["XAUUSDm"], excluded: [] });
  });
});
