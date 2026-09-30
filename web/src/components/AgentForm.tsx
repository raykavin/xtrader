import { type FormEvent, type ReactNode, useEffect, useId, useRef, useState } from "react";
import { createAgent, updateAgentSettings } from "../api/client";
import type { AgentSettings, PromptInfo } from "../api/types";
import {
  type AgentFormField,
  type AgentFormValues,
  agentCreateRequest,
  agentFormErrors,
  agentFormValues,
  agentKindLabel,
  agentKinds,
  agentUpdateRequest,
  llmProviders,
  maxComment,
  type ParamRow,
  reasoningEfforts,
  tradePanelHint,
} from "../lib/settings";
import { Select, type SelectOption } from "./Select";
import { Switch } from "./Switch";

interface Props {
  /** The agent edited, null for a new one. */
  agent: AgentSettings | null;
  prompts: PromptInfo[];
  /** Called with the agent saved, created or changed. */
  onSaved: (agent: AgentSettings) => void;
  onClose: () => void;
}

/** A field of the form typed as text. */
type TextFieldName = {
  [K in AgentFormField]: AgentFormValues[K] extends string ? K : never;
}[AgentFormField];

const kindOptions: SelectOption[] = agentKinds.map((kind) => ({
  value: kind,
  label: agentKindLabel(kind),
  hint: kind,
}));

const modeOptions: SelectOption[] = [
  { value: "live", label: "live", hint: "na conta MT5" },
  { value: "paper", label: "paper", hint: "carteira simulada" },
];

const providerLabels: Record<string, string> = { openai: "OpenAI", anthropic: "Anthropic" };

const providerOptions: SelectOption[] = llmProviders.map((provider) => ({
  value: provider,
  label: providerLabels[provider] ?? provider,
}));

const effortOptions: SelectOption[] = [
  { value: "", label: "—", hint: "o do modelo" },
  ...reasoningEfforts.map((effort) => ({ value: effort, label: effort })),
];

/** A model of each provider, the example of the field of the model. */
const modelExamples: Record<string, string> = { openai: "gpt-5", anthropic: "claude-sonnet-5" };

/**
 * Creates or edits an agent in a modal dialog: its settings, its MT5 account,
 * the language model of an LLM agent and its params, and for a new one its
 * switches, its prompt and its secrets, which its card changes afterwards.
 * The form is checked as the controller checks it before it is sent, the
 * refusals of the controller shown; an edit sends the fields changed only,
 * and applies to a running agent once it is restarted.
 */
export function AgentForm({ agent, prompts, onSaved, onClose }: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  const formId = useId();
  const creating = agent === null;
  const [initial] = useState(() => agentFormValues(agent ?? undefined));
  const [values, setValues] = useState(initial);
  const [touched, setTouched] = useState<ReadonlySet<string>>(() => new Set());
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const nextRow = useRef(initial.params.length);

  useEffect(() => {
    const element = dialog.current;
    if (!element || element.open) return;
    element.showModal();
    // a new agent starts by its name; an edit by the close button, as the dialog does
    if (creating) element.querySelector("input")?.focus();
  }, [creating]);

  const close = () => dialog.current?.close();
  const errors = agentFormErrors(values, creating);
  const invalid = Object.keys(errors).length > 0;
  const llm = values.kind === "llm";
  const paper = values.mode === "paper";

  const set = <K extends keyof AgentFormValues>(field: K, value: AgentFormValues[K]) =>
    setValues((v) => ({ ...v, [field]: value }));
  // a press in progress: the field it leaves shows its error once the press
  // ends, as the layout moving under the pointer would lose its click, eg: of a
  // select below the field, or of the submit button
  const pressing = useRef(false);
  useEffect(() => {
    const down = () => {
      pressing.current = true;
    };
    const up = () => {
      pressing.current = false;
    };
    document.addEventListener("pointerdown", down, true);
    document.addEventListener("pointerup", up, true);
    document.addEventListener("pointercancel", up, true);
    return () => {
      document.removeEventListener("pointerdown", down, true);
      document.removeEventListener("pointerup", up, true);
      document.removeEventListener("pointercancel", up, true);
    };
  }, []);
  const touch = (name: string) => () => {
    const mark = () => setTouched((t) => (t.has(name) ? t : new Set(t).add(name)));
    if (!pressing.current) {
      mark();
      return;
    }
    // after the click of the press, which follows its pointerup
    const ended = new AbortController();
    const later = () => {
      ended.abort();
      window.setTimeout(mark, 0);
    };
    document.addEventListener("pointerup", later, { signal: ended.signal });
    document.addEventListener("pointercancel", later, { signal: ended.signal });
  };
  /** The error of a field, shown once it is left or the form sent. */
  const errorOf = (field: AgentFormField) =>
    tried || touched.has(field) ? errors[field] : undefined;
  /** The props of the text field of field. */
  const text = (field: TextFieldName) => ({
    value: values[field],
    error: errorOf(field),
    onChange: (value: string) => set(field, value),
    onBlur: touch(field),
  });

  const paramId = (row: ParamRow) => `${formId}-param-${row.id}`;
  const setRow = (id: number, change: Partial<ParamRow>) =>
    setValues((v) => ({
      ...v,
      params: v.params.map((row) => (row.id === id ? { ...row, ...change } : row)),
    }));
  const addRow = () => {
    const row = { id: nextRow.current++, key: "", value: "" };
    setValues((v) => ({ ...v, params: [...v.params, row] }));
    requestAnimationFrame(() => document.getElementById(paramId(row))?.focus());
  };
  const removeRow = (id: number) =>
    setValues((v) => ({ ...v, params: v.params.filter((row) => row.id !== id) }));

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy) return;
    setError(null);
    if (invalid) {
      setTried(true);
      const form = event.currentTarget;
      // the first field refused, once its error is shown
      requestAnimationFrame(() =>
        form.querySelector<HTMLElement>("[data-invalid] input, [data-invalid] button")?.focus(),
      );
      return;
    }
    const update = agent ? agentUpdateRequest(initial, values) : null;
    if (agent && update === null) {
      close(); // nothing changed
      return;
    }
    setBusy(true);
    try {
      const saved =
        agent && update
          ? await updateAgentSettings(agent.name, update)
          : await createAgent(agentCreateRequest(values));
      onSaved(saved);
      close();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  const promptOptions: SelectOption[] = [
    { value: "", label: "default (embutido)" },
    ...prompts.map((p) => ({ value: String(p.id), label: p.name, hint: `v${p.version}` })),
  ];
  const channelsTitle = paper
    ? "No modo paper, o painel de controle fica ativo em todos os canais"
    : undefined;
  const addressMoved = agent?.running && values.apiAddress.trim() !== initial.apiAddress.trim();
  const action = creating ? "Criar agente" : "Salvar";

  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc closes the dialog natively
    <dialog
      ref={dialog}
      className="decision-dialog agent-dialog"
      aria-labelledby={`${formId}-title`}
      onClose={onClose}
      onCancel={(e) => {
        if (busy) e.preventDefault(); // Esc waits for the answer
      }}
      onClick={(e) => {
        // the backdrop, unless something was typed
        if (e.target !== dialog.current || busy) return;
        if (JSON.stringify(values) === JSON.stringify(initial)) close();
      }}
    >
      <header className="decision-head">
        <h2 id={`${formId}-title`}>
          {agent ? `Editar ${agent.name}` : "Novo agente"}
          {agent && <small>{agentKindLabel(agent.kind)}</small>}
        </h2>
        <button type="button" className="mini-btn" onClick={close} aria-label="Fechar">
          ✕
        </button>
      </header>
      <form className="decision-body" onSubmit={submit} noValidate>
        {agent?.running && (
          <p className="settings-status warn">
            O agente está em execução: as alterações valem quando ele for reiniciado, pelo botão
            Reiniciar do card; desligar Iniciado pelo controller o para na hora.
          </p>
        )}

        <Section title="Geral" disabled={busy}>
          <div className="agent-form-grid">
            {creating && (
              <>
                <TextField
                  label="Nome"
                  {...text("name")}
                  onChange={(name) => set("name", name.trim())}
                  mono
                  placeholder="ex.: gold-trend"
                  hint="Não muda depois de criado."
                />
                <SelectField
                  label="Tipo"
                  value={values.kind}
                  options={kindOptions}
                  error={errorOf("kind")}
                  hint="Não muda depois: o binário que o controller roda."
                  onChange={(kind) => set("kind", kind)}
                />
              </>
            )}
            <SelectField
              label="Modo"
              value={values.mode}
              options={modeOptions}
              error={errorOf("mode")}
              onChange={(mode) => set("mode", mode)}
            />
            <TextField
              label="Endereço da API"
              {...text("apiAddress")}
              mono
              placeholder="ex.: localhost:5580"
              hint={
                addressMoved ? (
                  <span className="settings-status warn">
                    O painel passa a usar o endereço novo já; o agente só o escuta depois de
                    reiniciado.
                  </span>
                ) : (
                  "host:porta onde o agente serve a API, único entre os agentes."
                )
              }
            />
          </div>
          <div className="settings-switches">
            {creating && (
              <Switch
                label="Ativo"
                checked={values.enabled}
                title="Ligado, o controller inicia o agente assim que ele for criado"
                onChange={(enabled) => set("enabled", enabled)}
              />
            )}
            <Switch
              label="Iniciado pelo controller"
              checked={values.launched}
              title="Ligado, o controller roda o processo do agente; desligado, ele roda à parte"
              onChange={(launched) => set("launched", launched)}
            />
            <Switch
              label="Reiniciar se cair"
              checked={values.restart}
              title="Ligado, o controller inicia o agente de novo quando o processo sai"
              onChange={(restart) => set("restart", restart)}
            />
            {creating && (
              <>
                <Switch
                  label="Painel de trading"
                  checked={paper || values.tradePanel}
                  disabled={paper}
                  title={channelsTitle ?? tradePanelHint}
                  onChange={(tradePanel) => set("tradePanel", tradePanel)}
                />
                <Switch
                  label="Controle via Telegram"
                  checked={paper || values.telegramControl}
                  disabled={paper}
                  title={channelsTitle}
                  onChange={(telegramControl) => set("telegramControl", telegramControl)}
                />
              </>
            )}
          </div>
          {creating &&
            values.enabled &&
            values.launched &&
            (paper ? (
              <p className="settings-hint">Ativo, o agente é iniciado assim que for criado.</p>
            ) : (
              <p className="settings-status warn">
                Ativo e em live, o agente é iniciado assim que for criado e já opera a conta MT5.
              </p>
            ))}
        </Section>

        <Section
          title="Conta MT5"
          disabled={busy}
          hint="Os agentes de uma mesma conta precisam de comentários (ou magics) diferentes: cada um só opera as posições do próprio magic."
        >
          <div className="agent-form-grid">
            <TextField
              label="URL da bridge"
              {...text("bridgeURL")}
              mono
              inputMode="url"
              placeholder="http://mt5:18812"
            />
            {creating && (
              <TextField
                label="Token da bridge"
                {...text("bridgeToken")}
                type="password"
                placeholder="opcional"
                hint="Opcional: dá para definir depois, no card."
              />
            )}
            <TextField
              label="Magic"
              {...text("magic")}
              mono
              inputMode="numeric"
              placeholder="opcional"
              hint="Em branco, um hash do comentário."
            />
            <TextField
              label="Comentário"
              {...text("comment")}
              mono
              maxLength={maxComment}
              placeholder="opcional"
              hint={`Até ${maxComment} caracteres; em branco, nb: e a estratégia (ex.: nb:llm).`}
            />
          </div>
          <div className={values.realAllowed ? "agent-real on" : "agent-real"}>
            <Switch
              label="Permitir conta real"
              checked={values.realAllowed}
              onChange={(realAllowed) => set("realAllowed", realAllowed)}
            />
            <p className="settings-hint">
              {values.realAllowed
                ? "Atenção: o agente poderá operar uma conta real, com dinheiro de verdade."
                : "Desligado, o agente só opera conta demo: numa conta real, ele se recusa a iniciar."}
            </p>
          </div>
        </Section>

        {llm && (
          <Section title="Modelo" disabled={busy}>
            <div className="agent-form-grid">
              <SelectField
                label="Provedor"
                value={values.provider}
                options={providerOptions}
                error={errorOf("provider")}
                onChange={(provider) => set("provider", provider)}
              />
              <TextField
                label="Modelo"
                {...text("model")}
                mono
                placeholder={`ex.: ${modelExamples[values.provider] ?? modelExamples.openai}`}
              />
              <TextField
                label="URL base"
                {...text("baseURL")}
                mono
                inputMode="url"
                placeholder="opcional"
                hint="Ex.: um proxy; em branco, a API do provedor."
              />
              <SelectField
                label="Esforço de raciocínio"
                value={values.reasoningEffort}
                options={effortOptions}
                hint={values.provider === "anthropic" ? "Ignorado pela Anthropic." : undefined}
                onChange={(effort) => set("reasoningEffort", effort)}
              />
              <TextField
                label="Máximo de tokens"
                {...text("maxTokens")}
                mono
                inputMode="numeric"
                placeholder="opcional"
                hint="Por resposta, o raciocínio incluído; em branco, 4096."
              />
              <TextField
                label="URL do calendário (MCP)"
                {...text("calendarURL")}
                mono
                inputMode="url"
                placeholder="opcional"
                hint="O servidor MCP do terminal MT5, com o calendário econômico; em branco, nenhum."
              />
              {creating && (
                <>
                  <SelectField
                    label="Prompt"
                    value={values.promptID}
                    options={promptOptions}
                    hint="Dá para trocar depois, no card, sem reiniciar."
                    onChange={(promptID) => set("promptID", promptID)}
                  />
                  <TextField
                    label="Chave da API do LLM"
                    {...text("llmAPIKey")}
                    type="password"
                    placeholder="opcional"
                    hint="Opcional: dá para definir depois, no card."
                  />
                  <TextField
                    label="Token do calendário (MCP)"
                    {...text("calendarToken")}
                    type="password"
                    placeholder="opcional"
                    hint="Opcional: dá para definir depois, no card."
                  />
                </>
              )}
            </div>
          </Section>
        )}

        <Section
          title="Parâmetros"
          disabled={busy}
          hint={
            <>
              As flags do agente sem campo próprio, pelo nome sem o traço, ex.:{" "}
              <code>pair = XAUUSDm</code>, <code>lot = 0.01</code>, <code>timeframe = 15m</code>.
              Com vários pares, o lote de cada um: <code>lot = BTCUSDm=0.03,XAUUSDm=0.01</code>.
            </>
          }
        >
          {values.params.length > 0 && (
            <div className="agent-params">
              {values.params.map((row, index) => {
                const rowError =
                  tried || touched.has(paramId(row)) ? errors.params?.[row.id] : undefined;
                return (
                  <div
                    key={row.id}
                    className="agent-param"
                    data-invalid={rowError ? "" : undefined}
                  >
                    <input
                      id={paramId(row)}
                      className="settings-input mono"
                      aria-label={`Nome do parâmetro ${index + 1}`}
                      aria-invalid={rowError ? true : undefined}
                      aria-describedby={rowError ? `${paramId(row)}-error` : undefined}
                      placeholder="nome"
                      value={row.key}
                      autoCapitalize="none"
                      autoComplete="off"
                      spellCheck={false}
                      onChange={(e) => setRow(row.id, { key: e.target.value })}
                      onBlur={touch(paramId(row))}
                    />
                    <span className="agent-param-eq" aria-hidden="true">
                      =
                    </span>
                    <input
                      className="settings-input mono"
                      aria-label={`Valor do parâmetro ${index + 1}`}
                      aria-invalid={rowError ? true : undefined}
                      aria-describedby={rowError ? `${paramId(row)}-error` : undefined}
                      placeholder="valor"
                      value={row.value}
                      autoCapitalize="none"
                      autoComplete="off"
                      spellCheck={false}
                      onChange={(e) => setRow(row.id, { value: e.target.value })}
                      onBlur={touch(paramId(row))}
                    />
                    <button
                      type="button"
                      className="mini-btn"
                      aria-label={`Remover o parâmetro ${row.key.trim() || index + 1}`}
                      onClick={() => removeRow(row.id)}
                    >
                      Remover
                    </button>
                    {rowError && (
                      <span id={`${paramId(row)}-error`} className="control-feedback error">
                        {rowError}
                      </span>
                    )}
                  </div>
                );
              })}
            </div>
          )}
          <div>
            <button type="button" className="mini-btn" onClick={addRow}>
              Adicionar parâmetro
            </button>
          </div>
        </Section>

        {tried && invalid && (
          <p className="control-feedback error" role="alert">
            Corrija os campos destacados.
          </p>
        )}
        {error && (
          <p className="control-feedback error" role="alert">
            {error}
          </p>
        )}
        <div className="reanalysis-actions">
          <button type="button" className="button" onClick={close} disabled={busy}>
            Cancelar
          </button>
          <button type="submit" className="button primary" disabled={busy}>
            {busy ? "Salvando…" : action}
          </button>
        </div>
      </form>
    </dialog>
  );
}

/** A section of the form, disabled while it is sent. */
function Section({
  title,
  hint,
  disabled,
  children,
}: {
  title: string;
  hint?: ReactNode;
  disabled: boolean;
  children: ReactNode;
}) {
  return (
    <fieldset className="agent-form-section" disabled={disabled}>
      <legend>{title}</legend>
      {hint && <p className="settings-hint">{hint}</p>}
      {children}
    </fieldset>
  );
}

interface TextFieldProps {
  label: string;
  value: string;
  onChange: (value: string) => void;
  onBlur?: () => void;
  /** Why the value is refused, shown below it. */
  error?: string;
  /** Below the field, eg: what it means left blank. */
  hint?: ReactNode;
  mono?: boolean;
  type?: "text" | "password";
  placeholder?: string;
  inputMode?: "text" | "numeric" | "url";
  maxLength?: number;
}

/** A field of text with its label, its hint and its error, which describe it. */
function TextField({
  label,
  value,
  onChange,
  onBlur,
  error,
  hint,
  mono = false,
  type = "text",
  placeholder,
  inputMode,
  maxLength,
}: TextFieldProps) {
  const id = useId();
  const described = [hint ? `${id}-hint` : "", error ? `${id}-error` : ""].filter(Boolean);
  return (
    <div className="field" data-invalid={error ? "" : undefined}>
      <label className="field-label" htmlFor={id}>
        {label}
      </label>
      <input
        id={id}
        type={type}
        className={mono ? "settings-input mono" : "settings-input"}
        value={value}
        placeholder={placeholder}
        inputMode={inputMode}
        maxLength={maxLength}
        autoComplete="off"
        autoCapitalize="none"
        spellCheck={false}
        aria-invalid={error ? true : undefined}
        aria-describedby={described.length > 0 ? described.join(" ") : undefined}
        onChange={(e) => onChange(e.target.value)}
        onBlur={onBlur}
      />
      {hint && (
        <span id={`${id}-hint`} className="settings-hint">
          {hint}
        </span>
      )}
      {error && (
        <span id={`${id}-error`} className="control-feedback error">
          {error}
        </span>
      )}
    </div>
  );
}

/** A select with its label, its hint and its error. */
function SelectField({
  label,
  value,
  options,
  onChange,
  error,
  hint,
}: {
  label: string;
  value: string;
  options: SelectOption[];
  onChange: (value: string) => void;
  error?: string;
  hint?: ReactNode;
}) {
  return (
    <div className="field" data-invalid={error ? "" : undefined}>
      <span className="field-label">{label}</span>
      <Select
        caption={label}
        value={value}
        options={options}
        placeholder="Escolha"
        onChange={onChange}
      />
      {hint && <span className="settings-hint">{hint}</span>}
      {error && <span className="control-feedback error">{error}</span>}
    </div>
  );
}
