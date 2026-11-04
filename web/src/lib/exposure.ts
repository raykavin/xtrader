// The exposure limit of all the agents on the dashboard, by rules: for each
// asset of a rule, at most so many agents on it in the same direction at
// once; an asset without a rule without a limit. The form sets them a row by
// rule, its assets (each a chip) and its limit, checked as the controller
// checks them; the controller keeps the limit of each asset.

import type { ExposureRequest, ExposureSettings } from "../api/types";
import { isSymbol } from "./symbols";

/** The largest limit of a rule. */
export const MAX_EXPOSURE = 100;

/** The most assets limited, of all the rules (as the controller takes them). */
export const MAX_ASSETS = 50;

/** A rule as a row of the form: its assets, its limit as typed, and a key of its own. */
export interface RuleForm {
  key: string;
  pairs: string[];
  maxAgents: string;
}

let lastKey = 0;

/** A row of a rule, blank by default, with a key of its own. */
export function ruleForm(pairs: string[] = [], maxAgents = ""): RuleForm {
  lastKey += 1;
  return { key: `r${lastKey}`, pairs, maxAgents };
}

/**
 * The rules of settings as rows: the assets of the same limit in one rule,
 * by their name, the rules by their limit.
 */
export function exposureRules(settings: ExposureSettings): RuleForm[] {
  const byLimit = new Map<number, string[]>();
  for (const [pair, limit] of Object.entries(settings.pairs ?? {})) {
    byLimit.set(limit, [...(byLimit.get(limit) ?? []), pair]);
  }
  return [...byLimit.entries()]
    .sort(([a], [b]) => a - b)
    .map(([limit, pairs]) =>
      ruleForm(
        pairs.sort((a, b) => a.localeCompare(b)),
        String(limit),
      ),
    );
}

/** Why the fields of a rule are refused. */
export interface RuleErrors {
  pairs?: string;
  maxAgents?: string;
}

/**
 * The request of the rules, or why they are refused, as the controller would
 * refuse them (it checks them again): each rule with an asset at least, each
 * as MT5 names it (the case kept, MT5 telling them apart) and in one rule only;
 * each limit an integer from 1 to MAX_EXPOSURE; at most MAX_ASSETS assets. A
 * row left blank is dropped; no rule turns the limit off.
 */
export function exposureCheck(rules: RuleForm[]): {
  request: ExposureRequest | null;
  rows: Record<string, RuleErrors>;
  list?: string;
} {
  const rows: Record<string, RuleErrors> = {};
  const pairs: Record<string, number> = {};
  const taken = rules.filter((rule) => rule.pairs.length > 0 || rule.maxAgents.trim() !== "");
  const seen = new Set<string>();
  let assets = 0;
  for (const rule of taken) {
    const errors: RuleErrors = {};
    const invalid = rule.pairs.find((pair) => !isSymbol(pair));
    const repeated = rule.pairs.find((pair, at) => seen.has(pair) || rule.pairs.indexOf(pair) < at);
    if (rule.pairs.length === 0) errors.pairs = "Informe ao menos um ativo.";
    else if (invalid !== undefined) {
      errors.pairs = `${invalid}: use o nome do ativo no MT5, ex.: XAUUSDm.`;
    } else if (repeated !== undefined) {
      errors.pairs = `${repeated} já está em outra regra: deixe o ativo numa só.`;
    }
    for (const pair of rule.pairs) seen.add(pair);
    assets += rule.pairs.length;
    const typed = rule.maxAgents.trim();
    const limit = /^\d+$/.test(typed) ? Number(typed) : Number.NaN;
    if (typed === "") errors.maxAgents = "Informe o máximo de agentes.";
    else if (!(limit >= 1 && limit <= MAX_EXPOSURE)) {
      errors.maxAgents = `Use um número inteiro de 1 a ${MAX_EXPOSURE}.`;
    }
    if (errors.pairs || errors.maxAgents) rows[rule.key] = errors;
    else for (const pair of rule.pairs) pairs[pair] = limit;
  }
  const list = assets > MAX_ASSETS ? `Use no máximo ${MAX_ASSETS} ativos.` : undefined;
  if (list || Object.keys(rows).length > 0) return { request: null, rows, list };
  return { request: { settings: { pairs } }, rows };
}
