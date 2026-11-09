// The global parametrizer of the agents: a param set by its key on every
// agent of the filter holding it (all of them, the LLM ones, the enabled
// ones, or the LLM ones enabled), on the ones without it when asked, then the
// agents of the filter enabled restarted when asked; checked as the params of
// an agent are, and what it did on each agent told.

import type { AgentSettings, AgentsParamRequest, AgentsParamResult } from "../api/types";
import { llmParam, paramKeyError, paramValueError, type Tone } from "./settings";

/** The form of the global parametrizer, as typed. */
export interface AgentsParamForm {
  key: string;
  value: string;
  /** Adicionar para agentes sem o parâmetro. */
  addMissing: boolean;
  /** Reiniciar os agentes ativos (of the filter). */
  restartActive: boolean;
  /** Somente para agentes de LLM. */
  llmOnly: boolean;
  /** Somente para os agentes ativos. */
  enabledOnly: boolean;
}

/** An empty form: the switches off. */
export const emptyAgentsParamForm: AgentsParamForm = {
  key: "",
  value: "",
  addMissing: false,
  restartActive: false,
  llmOnly: false,
  enabledOnly: false,
};

/** Whether agent is in the filter of form: an LLM one when llmOnly, enabled when enabledOnly. */
export function inFilter(agent: AgentSettings, form: AgentsParamForm): boolean {
  return (!form.llmOnly || agent.kind === "llm") && (!form.enabledOnly || agent.enabled);
}

/** Whether form filters the agents: the LLM ones or the enabled ones only. */
export function filtering(form: AgentsParamForm): boolean {
  return form.llmOnly || form.enabledOnly;
}

/**
 * Why the key and the value are refused, as the controller refuses them, by
 * field; {} when taken. A param of a language model is taken, set on the LLM
 * agents only.
 */
export function agentsParamErrors(form: AgentsParamForm): { key?: string; value?: string } {
  const errors: { key?: string; value?: string } = {};
  const key = paramKeyError(form.key, "llm");
  if (key) errors.key = key;
  const value = paramValueError(form.value, form.key.trim());
  if (value) errors.value = value;
  return errors;
}

/** The request of form, its key and value trimmed. */
export function agentsParamRequest(form: AgentsParamForm): AgentsParamRequest {
  return {
    key: form.key.trim(),
    value: form.value.trim(),
    add_missing: form.addMissing,
    restart_active: form.restartActive,
    llm_only: form.llmOnly,
    enabled_only: form.enabledOnly,
  };
}

/** The keys of the params of agents, by name, each once: the suggestions of the key. */
export function paramKeys(agents: AgentSettings[]): string[] {
  const keys = new Set<string>();
  for (const agent of agents) for (const key of Object.keys(agent.params)) keys.add(key);
  return [...keys].sort((a, b) => a.localeCompare(b));
}

/** A value of a param and how many agents hold it. */
export interface ParamValueCount {
  value: string;
  count: number;
}

/**
 * How the param key stands on agents: how many hold it, and its values by
 * how many agents hold each, the most held first (then by value).
 */
export function paramUsage(
  agents: AgentSettings[],
  key: string,
): { holding: number; values: ParamValueCount[] } {
  const name = key.trim();
  const counts = new Map<string, number>();
  for (const agent of agents) {
    if (!Object.hasOwn(agent.params, name)) continue;
    const value = agent.params[name] ?? "";
    counts.set(value, (counts.get(value) ?? 0) + 1);
  }
  const values = [...counts]
    .map(([value, count]) => ({ value, count }))
    .sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
  return { holding: values.reduce((sum, v) => sum + v.count, 0), values };
}

/**
 * What the parametrizer would do on agents as the page has them: how many
 * would have their value changed, the param added, be left with the value
 * they have already, be left without it (not added, or a param of a language
 * model on an agent of another kind), be out of the filter; and how many
 * agents of the filter enabled the controller launches would restart. The
 * controller decides on each agent as it is then.
 */
export function paramPreview(
  agents: AgentSettings[],
  form: AgentsParamForm,
): {
  update: number;
  add: number;
  unchanged: number;
  without: number;
  excluded: number;
  restart: number;
} {
  const key = form.key.trim();
  const value = form.value.trim();
  const preview = { update: 0, add: 0, unchanged: 0, without: 0, excluded: 0, restart: 0 };
  for (const agent of agents) {
    if (!inFilter(agent, form)) {
      preview.excluded++;
      continue;
    }
    if (Object.hasOwn(agent.params, key)) {
      if (agent.params[key] === value) preview.unchanged++;
      else preview.update++;
    } else if (form.addMissing && (!llmParam(key) || agent.kind === "llm")) {
      preview.add++;
    } else {
      preview.without++;
    }
    if (form.restartActive && agent.enabled && agent.launched) preview.restart++;
  }
  return preview;
}

/** "1 agente" or "n agentes", of count. */
export function agentsCount(count: number): string {
  return count === 1 ? "1 agente" : `${count} agentes`;
}

/** How a result shows: its label, its tone, and what it adds, if anything. */
export interface ResultLabel {
  label: string;
  tone: Tone | "info" | "error";
  detail?: string;
}

/** The first letter of text up, for a detail told alone. */
function capitalized(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/** How the change of the param on an agent shows, see AgentsParamResult. */
export function paramResultLabel(result: AgentsParamResult): ResultLabel {
  switch (result.status) {
    case "updated":
      return {
        label: "Alterado",
        tone: "on",
        detail: `Antes: ${result.previous ? result.previous : "vazio"}`,
      };
    case "added":
      return { label: "Adicionado", tone: "on" };
    case "unchanged":
      return { label: "Já tinha o valor", tone: "info" };
    case "excluded":
      return { label: "Fora do filtro", tone: "off" };
    case "skipped":
      return {
        label: "Ignorado",
        tone: "off",
        ...(result.detail ? { detail: capitalized(result.detail) } : {}),
      };
    default:
      return {
        label: "Falhou",
        tone: "error",
        ...(result.detail ? { detail: result.detail } : {}),
      };
  }
}

/**
 * How the restart of an agent shows, see AgentsParamResult; null when it
 * wasn't asked or the agent is disabled (left stopped, told only when asked).
 */
export function restartLabel(result: AgentsParamResult, asked: boolean): ResultLabel | null {
  switch (result.restart) {
    case "restarted":
      return { label: "Reiniciado", tone: "on" };
    case "failed":
      return {
        label: "Não reiniciado",
        tone: "error",
        ...(result.restart_detail ? { detail: result.restart_detail } : {}),
      };
    case "skipped":
      return {
        label: "Não reiniciado",
        tone: "warn",
        ...(result.restart_detail ? { detail: capitalized(result.restart_detail) } : {}),
      };
  }
  return asked && !result.enabled ? { label: "Desligado", tone: "off" } : null;
}

/** "n palavra" in the singular or the plural, of count. */
function counted(count: number, one: string, many: string): string {
  return `${count} ${count === 1 ? one : many}`;
}

/**
 * The summary of results, of the outcomes there are, eg: "2 alterados, 1
 * adicionado, 4 ignorados, 1 falha, 40 fora do filtro"; and of the restarts
 * when asked, eg: "3 reiniciados, 1 não reiniciado".
 */
export function paramSummary(
  results: AgentsParamResult[],
  asked: boolean,
): { params: string; restarts: string | null } {
  const count = (status: string) => results.filter((r) => r.status === status).length;
  const parts = [
    [count("updated"), "alterado", "alterados"],
    [count("added"), "adicionado", "adicionados"],
    [count("unchanged"), "já tinha o valor", "já tinham o valor"],
    [count("skipped"), "ignorado", "ignorados"],
    [count("failed"), "falha", "falhas"],
    [count("excluded"), "fora do filtro", "fora do filtro"],
  ] as const;
  const params =
    parts
      .filter(([n]) => n > 0)
      .map(([n, one, many]) => counted(n, one, many))
      .join(", ") || "Nenhum agente";
  if (!asked) return { params, restarts: null };
  const restarted = results.filter((r) => r.restart === "restarted").length;
  const left = results.filter((r) => r.restart === "failed" || r.restart === "skipped").length;
  const restarts =
    restarted + left === 0
      ? "Nenhum agente ligado para reiniciar"
      : [
          restarted > 0 ? counted(restarted, "reiniciado", "reiniciados") : "",
          left > 0 ? counted(left, "não reiniciado", "não reiniciados") : "",
        ]
          .filter(Boolean)
          .join(", ");
  return { params, restarts };
}

/** The order results show in: the failures first, then the changes, then the others, each in the order given. */
export function sortedResults(results: AgentsParamResult[]): AgentsParamResult[] {
  const rank = (r: AgentsParamResult) => {
    if (r.status === "failed" || r.restart === "failed") return 0;
    if (r.status === "updated" || r.status === "added") return 1;
    return 2;
  };
  return results
    .map((result, index) => ({ result, index }))
    .sort((a, b) => rank(a.result) - rank(b.result) || a.index - b.index)
    .map(({ result }) => result);
}
