import { type FormEvent, useEffect, useId, useState } from "react";
import { fetchExposure, updateExposure } from "../api/client";
import { type ExposureForm, exposureForm, exposureRequest } from "../lib/exposure";
import { Alert, Loading, Skeleton } from "./States";

/**
 * The exposure limit of all the agents, on the tab Limites: at most so many
 * agents with a position or a pending entry on an asset in the same
 * direction, for every asset and for some apart. Settings only: how many hold
 * each asset now isn't told here. A change holds for the next entries.
 */
export function ExposureCard() {
  const id = useId();
  const [form, setForm] = useState<ExposureForm | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    fetchExposure(controller.signal)
      .then((response) => setForm(exposureForm(response.settings)))
      .catch((e) => {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      });
    return () => controller.abort();
  }, []);

  if (!form) {
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

  const set = (key: keyof ExposureForm, value: string) => {
    setForm({ ...form, [key]: value });
    setSaved(null);
  };
  const { request, errors } = exposureRequest(form);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!request) return;
    setBusy(true);
    setError(null);
    updateExposure(request)
      .then((response) => {
        setForm(exposureForm(response.settings));
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
          Conta os agentes com posição ou ordem pendente de entrada no ativo, compras e vendas à
          parte. Saídas, stops, alvos e ordens manuais nunca são limitados; não vale em paper nem no
          backtest.
        </p>
        <div className="form-grid">
          <div className="field" data-invalid={errors.maxAgents ? "" : undefined}>
            <label className="field-label" htmlFor={`${id}-max`}>
              Máximo de agentes na mesma direção
            </label>
            <input
              id={`${id}-max`}
              className="input mono"
              inputMode="numeric"
              autoComplete="off"
              value={form.maxAgents}
              placeholder="sem limite"
              disabled={busy}
              aria-invalid={errors.maxAgents ? true : undefined}
              aria-describedby={`${id}-max-hint`}
              onChange={(e) => set("maxAgents", e.target.value)}
            />
            <span id={`${id}-max-hint`} className={errors.maxAgents ? "field-error" : "field-hint"}>
              {errors.maxAgents ?? "Por ativo e direção. Em branco ou 0, sem limite."}
            </span>
          </div>
          <div className="field" data-invalid={errors.pairs.length > 0 ? "" : undefined}>
            <label className="field-label" htmlFor={`${id}-pairs`}>
              Por ativo (opcional)
            </label>
            <input
              id={`${id}-pairs`}
              className="input mono"
              autoCapitalize="none"
              autoComplete="off"
              spellCheck={false}
              value={form.pairsText}
              placeholder="XAUUSDm=2;BTCUSDm=3"
              disabled={busy}
              aria-invalid={errors.pairs.length > 0 ? true : undefined}
              aria-describedby={`${id}-pairs-hint`}
              onChange={(e) => set("pairsText", e.target.value)}
            />
            <div id={`${id}-pairs-hint`} className="exposure-hints">
              {errors.pairs.length > 0 ? (
                errors.pairs.map((problem) => (
                  <span key={problem} className="field-error">
                    {problem}
                  </span>
                ))
              ) : (
                <span className="field-hint">
                  Vale no lugar do máximo geral nesses ativos; 0 tira o limite do ativo.
                </span>
              )}
            </div>
          </div>
        </div>
        <div className="daily-actions">
          {saved && <span className="dialog-foot-hint">{saved}</span>}
          <button type="submit" className="btn btn-primary" disabled={busy || request === null}>
            {busy ? "Salvando…" : "Salvar"}
          </button>
        </div>
      </form>
    </section>
  );
}
