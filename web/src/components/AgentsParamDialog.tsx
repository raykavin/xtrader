import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import { setAgentsParam } from "../api/client";
import { type AgentSettings, type AgentsParamResponse, MaxAgentsParams } from "../api/types";
import {
  type AgentsParamForm,
  agentsCount,
  agentsParamErrors,
  agentsParamRequest,
  emptyAgentsParamForm,
  filtering,
  inFilter,
  type ParamRowForm,
  paramKeyLabel,
  paramKeys,
  paramPreview,
  paramResultLabel,
  paramRow,
  paramSummary,
  paramUsage,
  type ResultLabel,
  refused,
  restartLabel,
  sortedResults,
  typedParams,
} from "../lib/agentsParam";
import { DialogHeader } from "./Dialog";
import { Icon } from "./Icon";
import { Alert } from "./States";

interface Props {
  /** The agents as the page has them: the suggestions of the keys and the preview. */
  agents: AgentSettings[];
  /** Tells the page the agents changed, once the parametrizer ran. */
  onApplied: () => void;
  onClose: () => void;
}

/** The values of a param shown in its hint, at most. */
const shownValues = 4;

/**
 * The global parametrizer of the agents, in a modal dialog: params set by
 * their keys, a row each (added by +), on every agent of the filter holding
 * them (all of them, the LLM ones, the enabled ones, or both), on the ones
 * without them too when asked, all at once on each agent, then the agents of
 * the filter enabled restarted when asked. Before: the keys suggested among
 * the ones of the agents of the filter, how many hold each and with which
 * values, and what would change; after: what was done on each agent of the
 * filter, with each param, its failures first, the others counted.
 */
export function AgentsParamDialog({ agents, onApplied, onClose }: Props) {
  const id = useId();
  const dialog = useRef<HTMLDialogElement>(null);
  const addRef = useRef<HTMLButtonElement>(null);
  const [form, setForm] = useState<AgentsParamForm>(emptyAgentsParamForm);
  // the fields left once, and all of them once applying was tried
  const [touched, setTouched] = useState<Set<string>>(new Set());
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [response, setResponse] = useState<AgentsParamResponse | null>(null);
  // the restart as asked for the results shown
  const [asked, setAsked] = useState(false);
  // the field to focus once the rows changed: the key of a row added, the
  // button to add after one removed
  const focus = useRef<string | null>(null);

  useEffect(() => {
    const element = dialog.current;
    if (element && !element.open) element.showModal();
    element?.querySelector<HTMLInputElement>("input.input")?.focus(); // the first key, not the first button
  }, []);

  useEffect(() => {
    if (focus.current === null) return;
    const target =
      focus.current === "add" ? addRef.current : document.getElementById(focus.current);
    focus.current = null;
    target?.focus();
  });

  const close = () => dialog.current?.close();
  const set = <K extends keyof AgentsParamForm>(field: K, value: AgentsParamForm[K]) =>
    setForm((f) => ({ ...f, [field]: value }));
  const updateRow = (rowId: number, change: Partial<ParamRowForm>) =>
    setForm((f) => ({
      ...f,
      rows: f.rows.map((row) => (row.id === rowId ? { ...row, ...change } : row)),
    }));
  const addRow = () => {
    const row = paramRow();
    focus.current = `${id}-${row.id}-key`;
    setForm((f) => ({ ...f, rows: [...f.rows, row] }));
  };
  const removeRow = (rowId: number) => {
    focus.current = "add";
    setForm((f) => ({ ...f, rows: f.rows.filter((row) => row.id !== rowId) }));
  };
  const leave = (field: string) => setTouched((t) => new Set(t).add(field));
  const shown = (field: string) => tried || touched.has(field);

  const errors = agentsParamErrors(form);
  const scoped = agents.filter((a) => inFilter(a, form));
  const keys = paramKeys(scoped);
  const preview = paramPreview(agents, form);
  const nothing = preview.update + preview.add === 0 && !form.restartActive;
  const enabled = scoped.filter((a) => a.enabled && a.launched).length;
  const ofFilter = filtering(form) ? " do filtro" : "";
  const typed = typedParams(form);
  const full = form.rows.length >= MaxAgentsParams;

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setTried(true);
    if (busy || refused(errors) || nothing) return;
    setBusy(true);
    setError(null);
    try {
      const done = await setAgentsParam(agentsParamRequest(form));
      setAsked(form.restartActive);
      setResponse(done);
      onApplied();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const keyHint = (key: string): string => {
    if (key.trim() === "") return "O nome da flag, sem o traço, como nos parâmetros do agente.";
    const usage = paramUsage(scoped, key);
    if (usage.holding === 0) return `Nenhum agente${ofFilter} tem essa chave.`;
    return `Presente em ${usage.holding} de ${agentsCount(scoped.length)}${ofFilter}.`;
  };
  const valueHint = (key: string): string => {
    const usage = paramUsage(scoped, key);
    if (usage.values.length === 0) return "O valor que os agentes passam a usar.";
    const values = usage.values
      .slice(0, shownValues)
      .map((v) => `${v.value === "" ? "vazio" : v.value} (${v.count})`)
      .join(", ");
    return `Valores atuais: ${values}${usage.values.length > shownValues ? ", …" : ""}.`;
  };
  const subtitle = (done: AgentsParamResponse): string => {
    const params = done.params ?? [{ key: done.key, value: done.value }];
    const [first] = params;
    if (params.length === 1 && first) return `${first.key} = ${first.value || "vazio"}`;
    return params.map((p) => p.key).join(", ");
  };

  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc closes the dialog natively
    <dialog
      ref={dialog}
      className="dialog dialog-md"
      aria-labelledby={`${id}-title`}
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current && !busy) close(); // the backdrop
      }}
    >
      <DialogHeader
        id={`${id}-title`}
        title="Parametrizador global"
        subtitle={response ? subtitle(response) : undefined}
        onClose={close}
        disabled={busy}
      />
      {response ? (
        <Results response={response} asked={asked} onClose={close} />
      ) : (
        <form className="dialog-form" onSubmit={submit} noValidate>
          <div className="dialog-body">
            <p className="dialog-lead">
              Altera parâmetros em todos os agentes, pela chave, todos de uma vez em cada agente. Os
              outros parâmetros e as configurações de cada agente não mudam.
            </p>
            <datalist id={`${id}-keys`}>
              {keys.map((key) => (
                <option key={key} value={key} />
              ))}
            </datalist>
            <div className="agents-param-rows">
              {form.rows.map((row, index) => {
                const problems = errors.rows[row.id] ?? {};
                const keyField = `${id}-${row.id}-key`;
                const valueField = `${id}-${row.id}-value`;
                const keyError = shown(keyField) ? problems.key : undefined;
                const valueError = shown(valueField) ? problems.value : undefined;
                const name = row.key.trim() || String(index + 1);
                return (
                  <fieldset
                    key={row.id}
                    className="agents-param-row"
                    aria-label={`Parâmetro ${index + 1}`}
                  >
                    <div className="field" data-invalid={keyError ? "" : undefined}>
                      <label className="field-label" htmlFor={keyField}>
                        Chave do parâmetro
                      </label>
                      <input
                        id={keyField}
                        className="input mono"
                        list={`${id}-keys`}
                        value={row.key}
                        placeholder="ex.: lot"
                        autoCapitalize="none"
                        autoComplete="off"
                        spellCheck={false}
                        disabled={busy}
                        aria-invalid={keyError ? true : undefined}
                        aria-describedby={`${keyField}-hint`}
                        onChange={(e) => updateRow(row.id, { key: e.target.value })}
                        onBlur={() => leave(keyField)}
                      />
                      <span
                        id={`${keyField}-hint`}
                        className={keyError ? "field-error" : "field-hint"}
                      >
                        {keyError ?? keyHint(row.key)}
                      </span>
                    </div>
                    <div className="field" data-invalid={valueError ? "" : undefined}>
                      <label className="field-label" htmlFor={valueField}>
                        Novo valor
                      </label>
                      <input
                        id={valueField}
                        className="input mono"
                        value={row.value}
                        placeholder="ex.: 0.02"
                        autoCapitalize="none"
                        autoComplete="off"
                        spellCheck={false}
                        disabled={busy}
                        aria-invalid={valueError ? true : undefined}
                        aria-describedby={`${valueField}-hint`}
                        onChange={(e) => updateRow(row.id, { value: e.target.value })}
                        onBlur={() => leave(valueField)}
                      />
                      <span
                        id={`${valueField}-hint`}
                        className={valueError ? "field-error" : "field-hint"}
                      >
                        {valueError ?? valueHint(row.key)}
                      </span>
                    </div>
                    <button
                      type="button"
                      className="btn btn-sm btn-ghost btn-icon agents-param-remove"
                      aria-label={`Remover o parâmetro ${name}`}
                      title="Remover"
                      disabled={busy || form.rows.length === 1}
                      onClick={() => removeRow(row.id)}
                    >
                      <Icon name="trash" size={15} />
                    </button>
                  </fieldset>
                );
              })}
            </div>
            {errors.list && <p className="field-error">{errors.list}</p>}
            <div>
              <button
                ref={addRef}
                type="button"
                className="btn btn-sm"
                disabled={busy || full}
                title={full ? `Use no máximo ${MaxAgentsParams} parâmetros de uma vez.` : undefined}
                onClick={addRow}
              >
                <Icon name="plus" size={14} />
                Adicionar parâmetro
              </button>
            </div>
            <fieldset className="agents-param-options">
              <legend className="sr-only">Opções</legend>
              <div className="agents-param-option">
                <label className="hours-check">
                  <input
                    type="checkbox"
                    checked={form.llmOnly}
                    disabled={busy}
                    aria-describedby={`${id}-llm-hint`}
                    onChange={(e) => set("llmOnly", e.target.checked)}
                  />
                  Somente para agentes de LLM
                </label>
                <span id={`${id}-llm-hint`} className="field-hint">
                  Os agentes de outros tipos ficam de fora: não mudam nem reiniciam.
                </span>
              </div>
              <div className="agents-param-option">
                <label className="hours-check">
                  <input
                    type="checkbox"
                    checked={form.enabledOnly}
                    disabled={busy}
                    aria-describedby={`${id}-enabled-hint`}
                    onChange={(e) => set("enabledOnly", e.target.checked)}
                  />
                  Somente para os agentes ativos
                </label>
                <span id={`${id}-enabled-hint`} className="field-hint">
                  Os agentes desligados ficam de fora e não mudam.
                </span>
              </div>
              <div className="agents-param-option">
                <label className="hours-check">
                  <input
                    type="checkbox"
                    checked={form.addMissing}
                    disabled={busy}
                    aria-describedby={`${id}-add-hint`}
                    onChange={(e) => set("addMissing", e.target.checked)}
                  />
                  Adicionar para agentes sem o parâmetro
                </label>
                <span id={`${id}-add-hint`} className="field-hint">
                  Desmarcada, só os agentes que já têm a chave mudam.
                </span>
              </div>
              <div className="agents-param-option">
                <label className="hours-check">
                  <input
                    type="checkbox"
                    checked={form.restartActive}
                    disabled={busy}
                    aria-describedby={`${id}-restart-hint`}
                    onChange={(e) => set("restartActive", e.target.checked)}
                  />
                  Reiniciar os agentes ativos
                </label>
                <span id={`${id}-restart-hint`} className="field-hint">
                  {`Depois de salvar, reinicia ${enabled === 1 ? `o agente ligado${ofFilter}` : `os ${enabled} agentes ligados${ofFilter}`} para aplicar os valores; os desligados continuam desligados. Desmarcada, os ligados aplicam os valores no próximo reinício.`}
                </span>
              </div>
            </fieldset>
            {typed.some((param) => param.key !== "") && !refused(errors) && (
              <p className="agents-param-preview" aria-live="polite">
                {previewText(preview)}
              </p>
            )}
            {error && (
              <Alert tone="error" title="Os parâmetros não foram aplicados">
                {error}
              </Alert>
            )}
          </div>
          <div className="dialog-foot">
            {busy && (
              <span className="dialog-foot-hint">
                {form.restartActive ? "Aplicando e reiniciando os agentes…" : "Aplicando…"}
              </span>
            )}
            <button type="button" className="btn" onClick={close} disabled={busy}>
              Cancelar
            </button>
            <button
              type="submit"
              className="btn btn-primary"
              disabled={busy || nothing}
              title={nothing ? "Nenhum agente seria alterado" : undefined}
            >
              <Icon name="success" size={14} />
              {busy ? "Aplicando…" : "Aplicar"}
            </button>
          </div>
        </form>
      )}
    </dialog>
  );
}

/** What the preview tells before applying, eg: "Altera 40 agentes e adiciona em 2; 3 já têm os valores." */
function previewText(preview: ReturnType<typeof paramPreview>): string {
  const changes = [
    preview.update > 0 ? `altera ${agentsCount(preview.update)}` : "",
    preview.add > 0 ? `adiciona em ${agentsCount(preview.add)}` : "",
  ].filter(Boolean);
  const head = changes.length > 0 ? changes.join(" e ") : "não altera nenhum agente";
  const rest = [
    preview.unchanged > 0
      ? `${agentsCount(preview.unchanged)} já ${preview.unchanged === 1 ? "tem" : "têm"} os valores`
      : "",
    preview.without > 0
      ? `${agentsCount(preview.without)} ${preview.without === 1 ? "fica" : "ficam"} sem os parâmetros`
      : "",
    preview.excluded > 0
      ? `${agentsCount(preview.excluded)} ${preview.excluded === 1 ? "fica" : "ficam"} fora do filtro`
      : "",
    preview.restart > 0
      ? `${preview.restart === 1 ? "1 agente ligado reinicia" : `${preview.restart} agentes ligados reiniciam`}`
      : "",
  ].filter(Boolean);
  const text = `${head.charAt(0).toUpperCase()}${head.slice(1)}`;
  return rest.length > 0 ? `${text}; ${rest.join("; ")}.` : `${text}.`;
}

/** What was done on each agent, its failures first, with the summary on top. */
function Results({
  response,
  asked,
  onClose,
}: {
  response: AgentsParamResponse;
  asked: boolean;
  onClose: () => void;
}) {
  const summary = paramSummary(response.results, asked);
  const failed = response.results.some((r) => r.status === "failed" || r.restart === "failed");
  return (
    <div className="dialog-form">
      <div className="dialog-body">
        <Alert tone={failed ? "warn" : "success"} title={summary.params}>
          {summary.restarts ??
            "Os agentes ligados que mudaram aplicam os valores no próximo reinício (Reinício pendente)."}
        </Alert>
        <ul className="agents-param-results" aria-label="Resultado por agente">
          {sortedResults(response.results.filter((r) => r.status !== "excluded")).map((result) => {
            const change = paramResultLabel(result);
            const restart = restartLabel(result, asked);
            const several = (result.params?.length ?? 0) > 1;
            return (
              <li key={result.agent} className="agents-param-result">
                <span className="agents-param-agent mono" title={result.agent}>
                  {result.agent}
                </span>
                <span className="agents-param-change">
                  <Outcome outcome={change} />
                  {several && (
                    <ul className="agents-param-keys">
                      {result.params?.map((param) => (
                        <li key={param.key}>{paramKeyLabel(param)}</li>
                      ))}
                    </ul>
                  )}
                </span>
                {restart && <Outcome outcome={restart} />}
              </li>
            );
          })}
        </ul>
      </div>
      <div className="dialog-foot">
        <button type="button" className="btn btn-primary" onClick={onClose}>
          Concluir
        </button>
      </div>
    </div>
  );
}

/** An outcome of an agent: its chip, and what it adds after it. */
function Outcome({ outcome }: { outcome: ResultLabel }) {
  return (
    <span className="agents-param-outcome">
      <span className={`status-chip ${outcome.tone}`}>{outcome.label}</span>
      {outcome.detail && <span className="agents-param-detail">{outcome.detail}</span>}
    </span>
  );
}
