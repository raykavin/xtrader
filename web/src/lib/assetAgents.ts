// The agents allowed on assets, of all the agents, on the dashboard, by rules:
// on the assets of a rule only its agents open positions; an asset without a
// rule free to every agent. The form sets them a row by rule, its assets (each
// a chip) and its agents, checked as the controller checks them.

import type { AssetAgentRule, AssetAgentsRequest, AssetAgentsSettings } from "../api/types";
import { isSymbol } from "./symbols";

/** The most rules, as the controller takes them. */
export const MAX_ASSET_RULES = 50;

/** A rule as a row of the form: its assets, its agents, and a key of its own. */
export interface AssetRuleForm {
  key: string;
  pairs: string[];
  agents: string[];
}

let lastKey = 0;

/** A row of a rule, blank by default, with a key of its own. */
export function assetRuleForm(pairs: string[] = [], agents: string[] = []): AssetRuleForm {
  lastKey += 1;
  return { key: `a${lastKey}`, pairs, agents };
}

/** The rules of settings as rows, in their order. */
export function assetRules(settings: AssetAgentsSettings): AssetRuleForm[] {
  return (settings.rules ?? []).map((rule) => assetRuleForm([...rule.pairs], [...rule.agents]));
}

/** Why the fields of a rule are refused. */
export interface AssetRuleErrors {
  pairs?: string;
  agents?: string;
}

/**
 * The request of the rules, or why they are refused, as the controller would
 * refuse them (it checks them again, the agents too): each rule with an asset
 * at least, each as MT5 names it (the case kept) and in one rule only, and an
 * agent at least; at most MAX_ASSET_RULES rules. A row left blank is dropped;
 * no rule frees every asset.
 */
export function assetAgentsCheck(rules: AssetRuleForm[]): {
  request: AssetAgentsRequest | null;
  rows: Record<string, AssetRuleErrors>;
  list?: string;
} {
  const rows: Record<string, AssetRuleErrors> = {};
  const kept: AssetAgentRule[] = [];
  const taken = rules.filter((rule) => rule.pairs.length > 0 || rule.agents.length > 0);
  const seen = new Set<string>();
  for (const rule of taken) {
    const errors: AssetRuleErrors = {};
    const invalid = rule.pairs.find((pair) => !isSymbol(pair));
    const repeated = rule.pairs.find((pair, at) => seen.has(pair) || rule.pairs.indexOf(pair) < at);
    if (rule.pairs.length === 0) errors.pairs = "Informe ao menos um ativo.";
    else if (invalid !== undefined) {
      errors.pairs = `${invalid}: use o nome do ativo no MT5, ex.: XAUUSDm.`;
    } else if (repeated !== undefined) {
      errors.pairs = `${repeated} já está em outra regra: deixe o ativo numa só.`;
    }
    for (const pair of rule.pairs) seen.add(pair);
    if (rule.agents.length === 0) errors.agents = "Escolha quem pode operar estes ativos.";
    if (errors.pairs || errors.agents) rows[rule.key] = errors;
    else kept.push({ pairs: rule.pairs, agents: rule.agents });
  }
  const list =
    taken.length > MAX_ASSET_RULES ? `Use no máximo ${MAX_ASSET_RULES} regras.` : undefined;
  if (list || Object.keys(rows).length > 0) return { request: null, rows, list };
  return { request: { settings: { rules: kept } }, rows };
}

/** The agents of a rule, as the label of its button, eg: "orb, trend" or "5 agentes". */
export function ruleAgentsLabel(agents: string[]): string {
  if (agents.length === 0) return "Escolher agentes";
  return agents.length <= 2 ? agents.join(", ") : `${agents.length} agentes`;
}

/**
 * The assets an agent of pairs may open positions on by rules, and the ones
 * left out (of a rule without it), as the controller narrows them.
 */
export function allowedPairs(
  rules: AssetAgentRule[],
  name: string,
  pairs: string[],
): { allowed: string[]; excluded: string[] } {
  const allowed: string[] = [];
  const excluded: string[] = [];
  for (const pair of pairs) {
    const rule = rules.find((r) => r.pairs.includes(pair));
    if (rule && !rule.agents.includes(name)) excluded.push(pair);
    else allowed.push(pair);
  }
  return { allowed, excluded };
}
