import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import { setAgentsParam } from "../api/client";
import type { AgentSettings, AgentsParamResponse } from "../api/types";
import {
  type AgentsParamForm,
  agentsCount,
  agentsParamErrors,
  agentsParamRequest,
  emptyAgentsParamForm,
  paramKeys,
  paramPreview,
  paramResultLabel,
  paramSummary,
  paramUsage,
  type ResultLabel,
  restartLabel,
  sortedResults,
} from "../lib/agentsParam";
import { DialogHeader } from "./Dialog";
import { Icon } from "./Icon";
import { Alert } from "./States";

interface Props {
  /** The agents as the page has them: the suggestions of the key and the preview. */
  agents: AgentSettings[];
  /** Tells the page the agents changed, once the parametrizer ran. */
  onApplied: () => void;
  onClose: () => void;
}

/** The values of a param shown in its hint, at most. */
const shownValues = 4;

/**
 * The global parametrizer of the agents, in a modal dialog: a param set by its
 * key on every agent holding it, on the ones without it too when asked, then
 * the agents enabled restarted when asked. Before: the key suggested among the
 * ones of the agents, how many hold it and with which values, and what would
 * change; after: what was done on each agent, its failures first.
 */
export function AgentsParamDialog({ agents, onApplied, onClose }: Props) {
  const id = useId();
  const dialog = useRef<HTMLDialogElement>(null);
  const keyField = useRef<HTMLInputElement>(null);
  const [form, setForm] = useState<AgentsParamForm>(emptyAgentsParamForm);
  const [touched, setTouched] = useState<{ key?: boolean; value?: boolean }>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [response, setResponse] = useState<AgentsParamResponse | null>(null);
  // the restart as asked for the results shown
  const [asked, setAsked] = useState(false);

  useEffect(() => {
    const element = dialog.current;
    if (element && !element.open) element.showModal();
    keyField.current?.focus(); // the dialog focuses its first button
  }, []);

  const close = () => dialog.current?.close();
  const set = <K extends keyof AgentsParamForm>(field: K, value: AgentsParamForm[K]) =>
    setForm((f) => ({ ...f, [field]: value }));

  const errors = agentsParamErrors(form);
  const usage = paramUsage(agents, form.key);
  const preview = paramPreview(agents, form);
  const nothing = preview.update + preview.add === 0 && !form.restartActive;
  const enabled = agents.filter((a) => a.enabled && a.launched).length;

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setTouched({ key: true, value: true });
    if (busy || errors.key || errors.value || nothing) return;
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

  const keyHint = (): string => {
    if (form.key.trim() === "")
      return "O nome da flag, sem o traço, como nos parâmetros do agente.";
    if (usage.holding === 0) return "Nenhum agente tem essa chave.";
    return `Presente em ${usage.holding} de ${agentsCount(agents.length)}.`;
  };
  const valueHint = (): string => {
    if (usage.values.length === 0) return "O valor que os agentes passam a usar.";
    const values = usage.values
      .slice(0, shownValues)
      .map((v) => `${v.value === "" ? "vazio" : v.value} (${v.count})`)
      .join(", ");
    return `Valores atuais: ${values}${usage.values.length > shownValues ? ", …" : ""}.`;
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
        subtitle={response ? `${response.key} = ${response.value || "vazio"}` : undefined}
        onClose={close}
        disabled={busy}
      />
      {response ? (
        <Results response={response} asked={asked} onClose={close} />
      ) : (
        <form className="dialog-form" onSubmit={submit} noValidate>
          <div className="dialog-body">
            <p className="dialog-lead">
              Altera um parâmetro em todos os agentes, pela chave. Os outros parâmetros e as
              configurações de cada agente não mudam.
            </p>
            <div className="form-grid">
              <div className="field" data-invalid={touched.key && errors.key ? "" : undefined}>
                <label className="field-label" htmlFor={`${id}-key`}>
                  Chave do parâmetro
                </label>
                <input
                  id={`${id}-key`}
                  ref={keyField}
                  className="input mono"
                  list={`${id}-keys`}
                  value={form.key}
                  placeholder="ex.: lot"
                  autoCapitalize="none"
                  autoComplete="off"
                  spellCheck={false}
                  disabled={busy}
                  aria-invalid={touched.key && errors.key ? true : undefined}
                  aria-describedby={`${id}-key-hint`}
                  onChange={(e) => set("key", e.target.value)}
                  onBlur={() => setTouched((t) => ({ ...t, key: true }))}
                />
                <datalist id={`${id}-keys`}>
                  {paramKeys(agents).map((key) => (
                    <option key={key} value={key} />
                  ))}
                </datalist>
                <span
                  id={`${id}-key-hint`}
                  className={touched.key && errors.key ? "field-error" : "field-hint"}
                >
                  {touched.key && errors.key ? errors.key : keyHint()}
                </span>
              </div>
              <div className="field" data-invalid={touched.value && errors.value ? "" : undefined}>
                <label className="field-label" htmlFor={`${id}-value`}>
                  Novo valor
                </label>
                <input
                  id={`${id}-value`}
                  className="input mono"
                  value={form.value}
                  placeholder="ex.: 0.02"
                  autoCapitalize="none"
                  autoComplete="off"
                  spellCheck={false}
                  disabled={busy}
                  aria-invalid={touched.value && errors.value ? true : undefined}
                  aria-describedby={`${id}-value-hint`}
                  onChange={(e) => set("value", e.target.value)}
                  onBlur={() => setTouched((t) => ({ ...t, value: true }))}
                />
                <span
                  id={`${id}-value-hint`}
                  className={touched.value && errors.value ? "field-error" : "field-hint"}
                >
                  {touched.value && errors.value ? errors.value : valueHint()}
                </span>
              </div>
            </div>
            <fieldset className="agents-param-options">
              <legend className="sr-only">Opções</legend>
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
                  Desmarcada, só os agentes que já têm a chave mudam. Um parâmetro do LLM (llm-,
                  calendar-, news-) só entra nos agentes LLM.
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
                  Reiniciar todos os agentes ativos
                </label>
                <span id={`${id}-restart-hint`} className="field-hint">
                  {`Depois de salvar, reinicia ${enabled === 1 ? "o agente ligado" : `os ${enabled} agentes ligados`} para aplicar o valor; os desligados continuam desligados. Desmarcada, os ligados aplicam o valor no próximo reinício.`}
                </span>
              </div>
            </fieldset>
            {form.key.trim() !== "" && !errors.key && !errors.value && (
              <p className="agents-param-preview" aria-live="polite">
                {previewText(preview)}
              </p>
            )}
            {error && (
              <Alert tone="error" title="O parâmetro não foi aplicado">
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

/** What the preview tells before applying, eg: "Altera 40 agentes e adiciona em 2; 3 já têm esse valor." */
function previewText(preview: ReturnType<typeof paramPreview>): string {
  const changes = [
    preview.update > 0 ? `altera ${agentsCount(preview.update)}` : "",
    preview.add > 0 ? `adiciona em ${agentsCount(preview.add)}` : "",
  ].filter(Boolean);
  const head = changes.length > 0 ? changes.join(" e ") : "não altera nenhum agente";
  const rest = [
    preview.unchanged > 0
      ? `${agentsCount(preview.unchanged)} já ${preview.unchanged === 1 ? "tem" : "têm"} esse valor`
      : "",
    preview.without > 0
      ? `${agentsCount(preview.without)} ${preview.without === 1 ? "fica" : "ficam"} sem o parâmetro`
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
            "Os agentes ligados que mudaram aplicam o valor no próximo reinício (Reinício pendente)."}
        </Alert>
        <ul className="agents-param-results" aria-label="Resultado por agente">
          {sortedResults(response.results).map((result) => {
            const change = paramResultLabel(result);
            const restart = restartLabel(result, asked);
            return (
              <li key={result.agent} className="agents-param-result">
                <span className="agents-param-agent mono" title={result.agent}>
                  {result.agent}
                </span>
                <Outcome outcome={change} />
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
