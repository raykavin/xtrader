import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import { fetchAgentSettings, fetchAssetAgents, updateAssetAgents } from "../api/client";
import { PermLimitsManage } from "../api/types";
import {
  type AssetRuleForm,
  assetAgentsCheck,
  assetRuleForm,
  assetRules,
  ruleAgentsLabel,
} from "../lib/assetAgents";
import { isSymbol } from "../lib/symbols";
import { useCan } from "./AccessContext";
import { ChipInput } from "./ChipInput";
import { Icon } from "./Icon";
import { Alert, Loading, Skeleton } from "./States";
import { AgentsDialog } from "./TradingWindowsEditor";

/**
 * The agents allowed on assets, of all the agents, on the tab Limites, by
 * rules: a row by rule, its assets (each a chip) and the agents that may open
 * positions on them (picked in a dialog), each removable, and a button to add
 * one, as the exposure limit. An asset without a rule is free to every agent.
 * A change reaches the agents at once, live and in paper.
 */
export function AssetAgentsCard() {
  const manage = useCan(PermLimitsManage); // changed by a user allowed to only, seen by the others
  const id = useId();
  const [rules, setRules] = useState<AssetRuleForm[] | null>(null);
  const [agents, setAgents] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [picking, setPicking] = useState<string | null>(null);
  const addRef = useRef<HTMLButtonElement>(null);
  // the field to focus once the list changed: the assets of a rule added, the
  // button to add after one removed
  const focus = useRef<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    fetchAssetAgents(controller.signal)
      .then((response) => setRules(assetRules(response.settings)))
      .catch((e) => {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      });
    // every agent, by name: a rule may name a disabled one, enabled later
    fetchAgentSettings(controller.signal)
      .then((list) =>
        setAgents(list.map((a) => a.name).sort((a, b) => a.localeCompare(b, "pt-BR"))),
      )
      .catch(() => {
        // without them, a rule keeps its agents and the dialog only shows them
      });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    if (focus.current === null) return;
    const target =
      focus.current === "add" ? addRef.current : document.getElementById(focus.current);
    focus.current = null;
    target?.focus();
  });

  if (!rules) {
    return (
      <section className="card">
        {error ? (
          <Alert tone="error">{error}</Alert>
        ) : (
          <Loading label="Carregando os agentes por ativo">
            <Skeleton height={160} />
          </Loading>
        )}
      </section>
    );
  }

  const fieldId = (rule: AssetRuleForm, field: string) => `${id}-${rule.key}-${field}`;
  const change = (next: AssetRuleForm[]) => {
    setRules(next);
    setSaved(null);
  };
  const update = (key: string, fields: Partial<AssetRuleForm>) =>
    change(rules.map((rule) => (rule.key === key ? { ...rule, ...fields } : rule)));
  const add = () => {
    const added = assetRuleForm();
    focus.current = fieldId(added, "pairs");
    change([...rules, added]);
  };
  const remove = (key: string) => {
    focus.current = "add";
    change(rules.filter((rule) => rule.key !== key));
  };
  const check = assetAgentsCheck(rules);
  const picked = rules.find((rule) => rule.key === picking);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!check.request) return;
    setBusy(true);
    setError(null);
    updateAssetAgents(check.request)
      .then((response) => {
        setRules(assetRules(response.settings));
        setSaved("Salvo. Vale na hora para as próximas entradas.");
      })
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setBusy(false));
  };

  return (
    <section className="card card-flush" aria-labelledby={`${id}-title`}>
      <header className="card-header">
        <div className="card-heading">
          <span className="eyebrow">Todos os agentes</span>
          <h2 id={`${id}-title`} className="card-title">
            Agentes por ativo
          </h2>
        </div>
      </header>
      <form className="card-body exposure-body" onSubmit={submit} noValidate>
        <fieldset className="fieldset-contents" disabled={!manage}>
          {error && <Alert tone="error">{error}</Alert>}
          <p className="field-hint">
            Nos ativos de uma regra, só os agentes dela abrem posição, em live e em paper; um ativo
            sem regra fica livre para todos. Um agente sem nenhum dos seus ativos permitido fica com
            as entradas bloqueadas. Saídas, stops, alvos e posições abertas nunca são afetados. No
            backtest, vale quando o lote marca a opção.
          </p>
          {rules.length === 0 ? (
            <p className="field-hint">
              Nenhuma regra: todos os agentes operam todos os seus ativos.
            </p>
          ) : (
            <div className="exposure-rule-list">
              {rules.map((rule, index) => {
                const errors = check.rows[rule.key] ?? {};
                const name = rule.pairs.length > 0 ? rule.pairs.join(", ") : String(index + 1);
                return (
                  <fieldset
                    key={rule.key}
                    className="hours-window asset-rule"
                    aria-labelledby={fieldId(rule, "title")}
                    data-invalid={errors.pairs || errors.agents ? "" : undefined}
                  >
                    <div className="hours-window-head">
                      <span id={fieldId(rule, "title")} className="hours-window-title">
                        Regra {index + 1}
                      </span>
                      <button
                        type="button"
                        className="btn btn-sm btn-ghost btn-icon"
                        aria-label={`Remover a regra de ${name}`}
                        title="Remover"
                        disabled={busy}
                        onClick={() => remove(rule.key)}
                      >
                        <Icon name="trash" size={15} />
                      </button>
                    </div>
                    <div
                      className="field asset-rule-pairs"
                      data-invalid={errors.pairs ? "" : undefined}
                    >
                      <label className="field-label" htmlFor={fieldId(rule, "pairs")}>
                        Ativos
                      </label>
                      <ChipInput
                        id={fieldId(rule, "pairs")}
                        values={rule.pairs}
                        placeholder="digite um ativo e tecle Enter"
                        disabled={busy}
                        invalid={Boolean(errors.pairs)}
                        describedBy={errors.pairs ? fieldId(rule, "pairs-error") : undefined}
                        refused={(pair) => !isSymbol(pair)}
                        onChange={(pairs) => update(rule.key, { pairs })}
                      />
                      {errors.pairs && (
                        <span id={fieldId(rule, "pairs-error")} className="field-error">
                          {errors.pairs}
                        </span>
                      )}
                    </div>
                    <div
                      className="field asset-rule-agents"
                      data-invalid={errors.agents ? "" : undefined}
                    >
                      <span className="field-label" aria-hidden="true">
                        Agentes que podem operar
                      </span>
                      <button
                        type="button"
                        className="btn hours-window-agents-button"
                        aria-haspopup="dialog"
                        aria-label={`Agentes da regra ${name}: ${ruleAgentsLabel(rule.agents)}`}
                        aria-invalid={errors.agents ? true : undefined}
                        aria-describedby={errors.agents ? fieldId(rule, "agents-error") : undefined}
                        title={rule.agents.length > 0 ? rule.agents.join(", ") : undefined}
                        disabled={busy}
                        onClick={() => setPicking(rule.key)}
                      >
                        <Icon name="users" size={14} />
                        <span>{ruleAgentsLabel(rule.agents)}</span>
                      </button>
                      {errors.agents && (
                        <span id={fieldId(rule, "agents-error")} className="field-error">
                          {errors.agents}
                        </span>
                      )}
                    </div>
                  </fieldset>
                );
              })}
            </div>
          )}
          {check.list && <p className="field-error">{check.list}</p>}
          {manage && (
            <>
              <div>
                <button
                  ref={addRef}
                  type="button"
                  className="btn btn-sm"
                  disabled={busy}
                  onClick={add}
                >
                  <Icon name="plus" size={14} />
                  Adicionar regra
                </button>
              </div>
              <div className="daily-actions">
                {saved && <span className="dialog-foot-hint">{saved}</span>}
                <button type="submit" className="btn btn-primary" disabled={busy || !check.request}>
                  {busy ? "Salvando…" : "Salvar"}
                </button>
              </div>
            </>
          )}
        </fieldset>
      </form>
      {picked && (
        <AgentsDialog
          title={`Agentes de ${picked.pairs.length > 0 ? picked.pairs.join(", ") : `regra ${rules.indexOf(picked) + 1}`}`}
          choices={agents}
          selected={picked.agents}
          unavailable="excluído"
          hint="Só os agentes marcados abrem posição nos ativos desta regra."
          legend="Agentes que podem operar os ativos desta regra"
          allowAll={false}
          onChange={(chosen) => update(picked.key, { agents: chosen })}
          onClose={() => setPicking(null)}
        />
      )}
    </section>
  );
}
