import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import { fetchExposure, updateExposure } from "../api/client";
import { exposureCheck, exposureRules, type RuleForm, ruleForm } from "../lib/exposure";
import { isSymbol } from "../lib/symbols";
import { ChipInput } from "./ChipInput";
import { Icon } from "./Icon";
import { Alert, Loading, Skeleton } from "./States";

/**
 * The exposure limit of all the agents, on the tab Limites, by rules: a row by
 * rule, its assets (each a chip) and the most agents with a position or a
 * pending entry on each of them in the same direction, each removable, and a
 * button to add one, as the windows of the trading hours. An asset without a
 * rule has no limit; no rule, the limit off. Settings only: how many hold each
 * asset now isn't told here. A change holds for the next entries.
 */
export function ExposureCard() {
  const id = useId();
  const [rules, setRules] = useState<RuleForm[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const addRef = useRef<HTMLButtonElement>(null);
  // the field to focus once the list changed: the assets of a rule added, the
  // button to add after one removed
  const focus = useRef<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    fetchExposure(controller.signal)
      .then((response) => setRules(exposureRules(response.settings)))
      .catch((e) => {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
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
          <Loading label="Carregando a exposição por ativo">
            <Skeleton height={160} />
          </Loading>
        )}
      </section>
    );
  }

  const fieldId = (rule: RuleForm, field: string) => `${id}-${rule.key}-${field}`;
  const change = (next: RuleForm[]) => {
    setRules(next);
    setSaved(null);
  };
  const update = (key: string, fields: Partial<RuleForm>) =>
    change(rules.map((rule) => (rule.key === key ? { ...rule, ...fields } : rule)));
  const add = () => {
    const added = ruleForm();
    focus.current = fieldId(added, "pairs");
    change([...rules, added]);
  };
  const remove = (key: string) => {
    focus.current = "add";
    change(rules.filter((rule) => rule.key !== key));
  };
  const check = exposureCheck(rules);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!check.request) return;
    setBusy(true);
    setError(null);
    updateExposure(check.request)
      .then((response) => {
        setRules(exposureRules(response.settings));
        setSaved("Salvo. Vale para as próximas entradas.");
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
            Exposição por ativo
          </h2>
        </div>
      </header>
      <form className="card-body exposure-body" onSubmit={submit} noValidate>
        {error && <Alert tone="error">{error}</Alert>}
        <p className="field-hint">
          Em cada ativo da regra, no máximo essa quantidade de agentes com posição ou ordem pendente
          de entrada nele na mesma direção: compras e vendas contam separadas. Um ativo sem regra
          não tem limite. Saídas, stops, alvos e ordens manuais nunca são limitados; não vale em
          paper nem no backtest.
        </p>
        {rules.length === 0 ? (
          <p className="field-hint">Nenhuma regra: nenhum ativo tem limite.</p>
        ) : (
          <div className="exposure-rule-list">
            {rules.map((rule, index) => {
              const errors = check.rows[rule.key] ?? {};
              const name = rule.pairs.length > 0 ? rule.pairs.join(", ") : String(index + 1);
              return (
                <fieldset
                  key={rule.key}
                  className="hours-window exposure-rule"
                  aria-labelledby={fieldId(rule, "title")}
                  data-invalid={errors.pairs || errors.maxAgents ? "" : undefined}
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
                    className="field exposure-rule-pairs"
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
                    className="field exposure-rule-max"
                    data-invalid={errors.maxAgents ? "" : undefined}
                  >
                    <label className="field-label" htmlFor={fieldId(rule, "max")}>
                      Máximo de agentes
                    </label>
                    <input
                      id={fieldId(rule, "max")}
                      className="input mono"
                      inputMode="numeric"
                      autoComplete="off"
                      value={rule.maxAgents}
                      placeholder="ex.: 2"
                      disabled={busy}
                      aria-invalid={errors.maxAgents ? true : undefined}
                      aria-describedby={errors.maxAgents ? fieldId(rule, "max-error") : undefined}
                      onChange={(e) => update(rule.key, { maxAgents: e.target.value })}
                    />
                    {errors.maxAgents && (
                      <span id={fieldId(rule, "max-error")} className="field-error">
                        {errors.maxAgents}
                      </span>
                    )}
                  </div>
                </fieldset>
              );
            })}
          </div>
        )}
        {check.list && <p className="field-error">{check.list}</p>}
        <div>
          <button ref={addRef} type="button" className="btn btn-sm" disabled={busy} onClick={add}>
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
      </form>
    </section>
  );
}
