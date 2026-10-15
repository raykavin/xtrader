import { type FormEvent, useCallback, useEffect, useState } from "react";
import {
  fetchSupervisorActions,
  fetchSupervisorRuns,
  runSupervisor,
  updateSupervisorSettings,
} from "../api/client";
import type {
  SupervisorActionInfo,
  SupervisorRunInfo,
  SupervisorSettingsRequest,
  SupervisorSettingsResponse,
} from "../api/types";
import {
  actionLabel,
  actionStatusLabel,
  formatInterval,
  intervalError,
  intervalOptions,
  originLabel,
} from "../lib/supervisor";
import { Icon } from "./Icon";
import { PageHeader } from "./PageHeader";
import { SecretField } from "./SecretField";
import { Select } from "./Select";
import type { SectionIntro } from "./SettingsPage";
import { Alert, EmptyState } from "./States";
import { clock, dayClock, ResultItems, toneClass } from "./SupervisorAction";
import { SupervisorRuns } from "./SupervisorRuns";
import { Switch } from "./Switch";

interface Props {
  /** The title of the section and what it is for. */
  intro: SectionIntro;
  settings: SupervisorSettingsResponse;
  onChange: (settings: SupervisorSettingsResponse) => void;
}

/** The reasoning efforts the models take, as the LLM agents. */
const efforts = ["", "none", "minimal", "low", "medium", "high", "xhigh"];

/** The form of the settings, as typed. */
interface Form {
  intervalMinutes: string;
  timeoutSeconds: string;
  provider: string;
  model: string;
  baseURL: string;
  reasoning: string;
  maxTokens: string;
  instructions: string;
  maxActions: string;
}

function formOf(settings: SupervisorSettingsResponse): Form {
  return {
    intervalMinutes: String(Math.round(settings.interval_seconds / 60)),
    timeoutSeconds: String(settings.timeout_seconds),
    provider: settings.provider ?? "",
    model: settings.model ?? "",
    baseURL: settings.base_url ?? "",
    reasoning: settings.reasoning_effort ?? "",
    maxTokens: settings.max_tokens ? String(settings.max_tokens) : "",
    instructions: settings.instructions,
    maxActions: String(settings.autonomy.max_actions_per_run),
  };
}

/**
 * The supervisor of the agents: on or off, how often it analyzes them, its
 * language model and its key (written, never read), its criteria, what it
 * does alone; its analyses and the record of the actions of the supervision.
 */
export function SupervisorSettings({ intro, settings, onChange }: Props) {
  const [form, setForm] = useState<Form>(() => formOf(settings));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [runs, setRuns] = useState<SupervisorRunInfo[] | null>(null);
  const [actions, setActions] = useState<SupervisorActionInfo[] | null>(null);
  const [open, setOpen] = useState<number | null>(null);

  const load = useCallback(() => {
    fetchSupervisorRuns()
      .then((response) => setRuns(response.runs))
      .catch(() => setRuns([]));
    fetchSupervisorActions()
      .then(setActions)
      .catch(() => setActions([]));
  }, []);
  useEffect(() => load(), [load]);
  useEffect(() => {
    if (!settings.analyzing) return;
    const timer = window.setInterval(load, 3000);
    return () => window.clearInterval(timer);
  }, [settings.analyzing, load]);

  const run = async (request: SupervisorSettingsRequest) => {
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      const updated = await updateSupervisorSettings(request);
      onChange(updated);
      setForm(formOf(updated));
      setSaved(true);
      return true;
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return false;
    } finally {
      setBusy(false);
    }
  };

  const minutes = Number(form.intervalMinutes);
  const intervalProblem = intervalError(minutes, settings.min_interval, settings.max_interval);
  const timeout = Number(form.timeoutSeconds);
  const timeoutProblem =
    !Number.isInteger(timeout) || timeout < settings.min_timeout || timeout > settings.max_timeout
      ? `De ${settings.min_timeout} a ${settings.max_timeout} segundos.`
      : null;
  const maxActions = Number(form.maxActions);
  const actionsProblem =
    !Number.isInteger(maxActions) || maxActions < 0 || maxActions > settings.max_actions
      ? `De 0 a ${settings.max_actions}.`
      : null;
  const tokens = form.maxTokens.trim() === "" ? 0 : Number(form.maxTokens);
  const tokensProblem =
    !Number.isInteger(tokens) || tokens < 0 ? "Um número inteiro positivo." : null;
  const invalid = Boolean(intervalProblem || timeoutProblem || actionsProblem || tokensProblem);
  const ready = settings.api_key_set && Boolean(settings.model) && Boolean(settings.provider);

  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (invalid) return;
    await run({
      interval_seconds: minutes * 60,
      timeout_seconds: timeout,
      provider: form.provider,
      model: form.model,
      base_url: form.baseURL,
      reasoning_effort: form.reasoning,
      max_tokens: tokens,
      instructions: form.instructions,
      autonomy: { ...settings.autonomy, max_actions_per_run: maxActions },
    });
  };
  const analyze = async () => {
    setError(null);
    try {
      await runSupervisor();
      onChange({ ...settings, analyzing: true });
      window.setTimeout(load, 500);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };
  const field = (name: keyof Form) => ({
    value: form[name],
    disabled: busy,
    onChange: (e: { target: { value: string } }) =>
      setForm((f) => ({ ...f, [name]: e.target.value })),
  });

  if (!settings.configured) {
    return (
      <div className="settings-section">
        <PageHeader eyebrow="Configurações" title={intro.title} description={intro.description} />
        <Alert tone="warn" title="O XTrader AI ainda não está no banco">
          Rode o migrator (<code>migrator up</code>), que cria as tabelas e a configuração padrão
          dele.
        </Alert>
      </div>
    );
  }
  return (
    <div className="settings-section">
      <PageHeader
        eyebrow="Configurações"
        title={intro.title}
        description={intro.description}
        actions={
          <button
            type="button"
            className="btn btn-primary"
            disabled={!ready || settings.analyzing}
            onClick={analyze}
          >
            <Icon name="restart" />
            {settings.analyzing ? "Analisando…" : "Analisar agora"}
          </button>
        }
      />
      {error && (
        <Alert tone="error" title="A alteração não foi feita">
          {error}
        </Alert>
      )}
      <div className="settings-columns">
        <section className="card card-flush" aria-labelledby="supervisor-state-title">
          <header className="card-header">
            <div className="card-heading">
              <span className="eyebrow">XTrader AI</span>
              <h3 id="supervisor-state-title" className="card-title">
                Análises periódicas
              </h3>
            </div>
            <span className={toneClass(settings.enabled ? "on" : "off")}>
              {settings.enabled ? "Ligado" : "Desligado"}
            </span>
          </header>
          <div className="card-body stack">
            <Switch
              label="XTrader AI ativo"
              checked={settings.enabled}
              disabled={busy || (!settings.enabled && !ready)}
              title={ready ? undefined : "Defina o provedor, o modelo e a chave antes"}
              onChange={(enabled) => run({ enabled })}
            />
            {!ready && (
              <p className="msg msg-warn">
                Defina o provedor, o modelo e a chave para ligar o XTrader AI.
              </p>
            )}
            <p className="field-hint">
              {settings.enabled && settings.next_run_at
                ? `Próxima análise às ${clock(settings.next_run_at)}, depois a cada ${formatInterval(settings.interval_seconds)}.`
                : "Desligado, o XTrader AI não analisa sozinho; a conversa e Analisar agora continuam disponíveis."}
              {settings.failures_in_row > 0 &&
                ` ${settings.failures_in_row} análise(s) seguida(s) falharam: os agentes não foram afetados.`}
            </p>
            <h4 className="section-title">
              <Icon name="shield" size={13} />O que ele faz sozinho
            </h4>
            <Switch
              label="Bloquear novas entradas"
              checked={settings.autonomy.block_entries}
              disabled={busy}
              title="O agente não abre nem aumenta posições; continua gerenciando as que tem"
              onChange={(block_entries) =>
                run({ autonomy: { ...settings.autonomy, block_entries } })
              }
            />
            <Switch
              label="Parar o agente quando zerado"
              checked={settings.autonomy.stop_when_flat}
              disabled={busy}
              title="Exige duas análises seguidas; o agente é desligado quando ficar sem posição e sem pendente"
              onChange={(stop_when_flat) =>
                run({ autonomy: { ...settings.autonomy, stop_when_flat } })
              }
            />
            <Switch
              label="Cancelar pendentes de entrada"
              checked={settings.autonomy.cancel_pending}
              disabled={busy}
              onChange={(cancel_pending) =>
                run({ autonomy: { ...settings.autonomy, cancel_pending } })
              }
            />
            <p className="field-hint">
              Fora disso ele só propõe, e você confirma na conversa. Encerrar posições sempre pede a
              sua confirmação, e ele nunca reativa um agente que parou.
            </p>
          </div>
        </section>

        <section className="card card-flush" aria-labelledby="supervisor-model-title">
          <header className="card-header">
            <div className="card-heading">
              <span className="eyebrow">Modelo</span>
              <h3 id="supervisor-model-title" className="card-title">
                Linguagem e critérios
              </h3>
            </div>
          </header>
          <form
            className="card-body stack"
            onSubmit={save}
            aria-label="Configurações do XTrader AI"
          >
            <div className="form-grid">
              <div className="field">
                <label className="field-label" htmlFor="supervisor-interval">
                  Intervalo (minutos)
                </label>
                <input
                  id="supervisor-interval"
                  className="input"
                  inputMode="numeric"
                  list="supervisor-intervals"
                  {...field("intervalMinutes")}
                />
                <datalist id="supervisor-intervals">
                  {intervalOptions.map((seconds) => (
                    <option key={seconds} value={seconds / 60}>
                      {formatInterval(seconds)}
                    </option>
                  ))}
                </datalist>
                {intervalProblem && <p className="field-error">{intervalProblem}</p>}
              </div>
              <div className="field">
                <label className="field-label" htmlFor="supervisor-timeout">
                  Tempo máximo de resposta (s)
                </label>
                <input
                  id="supervisor-timeout"
                  className="input"
                  inputMode="numeric"
                  {...field("timeoutSeconds")}
                />
                {timeoutProblem && <p className="field-error">{timeoutProblem}</p>}
              </div>
              <div className="field">
                <span className="field-label">Provedor</span>
                <Select
                  caption="Provedor"
                  value={form.provider || null}
                  placeholder="Escolha"
                  options={[
                    { value: "openai", label: "OpenAI" },
                    { value: "anthropic", label: "Anthropic" },
                  ]}
                  onChange={(provider) => setForm((f) => ({ ...f, provider }))}
                />
              </div>
              <div className="field">
                <label className="field-label" htmlFor="supervisor-model">
                  Modelo
                </label>
                <input
                  id="supervisor-model"
                  className="input mono"
                  placeholder="ex.: gpt-5"
                  {...field("model")}
                />
              </div>
              <div className="field">
                <span className="field-label">Raciocínio</span>
                <Select
                  caption="Raciocínio"
                  value={form.reasoning}
                  options={efforts.map((effort) => ({
                    value: effort,
                    label: effort || "padrão do modelo",
                  }))}
                  onChange={(reasoning) => setForm((f) => ({ ...f, reasoning }))}
                />
              </div>
              <div className="field">
                <label className="field-label" htmlFor="supervisor-tokens">
                  Máximo de tokens <span className="optional">(opcional)</span>
                </label>
                <input
                  id="supervisor-tokens"
                  className="input"
                  inputMode="numeric"
                  placeholder={`padrão: ${settings.default_max_tokens}`}
                  aria-describedby="supervisor-tokens-hint"
                  {...field("maxTokens")}
                />
                {tokensProblem && <p className="field-error">{tokensProblem}</p>}
                <p id="supervisor-tokens-hint" className="field-hint">
                  O raciocínio conta no limite: um modelo que raciocina (gpt-5, o3, Claude com
                  raciocínio) pode gastar {settings.default_max_tokens} tokens só pensando e não
                  responder. Para eles, use 16000 ou mais, com um tempo de resposta maior.
                </p>
              </div>
              <div className="field form-wide">
                <label className="field-label" htmlFor="supervisor-base-url">
                  URL base <span className="optional">(opcional)</span>
                </label>
                <input
                  id="supervisor-base-url"
                  className="input mono"
                  placeholder="padrão do provedor"
                  {...field("baseURL")}
                />
              </div>
              <div className="field">
                <label className="field-label" htmlFor="supervisor-actions">
                  Ações sozinho por análise
                </label>
                <input
                  id="supervisor-actions"
                  className="input"
                  inputMode="numeric"
                  {...field("maxActions")}
                />
                {actionsProblem && <p className="field-error">{actionsProblem}</p>}
              </div>
            </div>
            <div className="field">
              <label className="field-label" htmlFor="supervisor-instructions">
                Instruções e critérios
              </label>
              <textarea
                id="supervisor-instructions"
                className="textarea"
                rows={12}
                {...field("instructions")}
              />
              <p className="field-hint">
                Separe problemas operacionais, de risco e de desempenho. Não há limites financeiros
                fixos: o XTrader AI compara com os limites configurados em cada agente.
              </p>
            </div>
            <div className="form-actions">
              {saved && <span className="msg msg-ok">Salvo.</span>}
              <button type="submit" className="btn btn-primary" disabled={busy || invalid}>
                Salvar
              </button>
            </div>
          </form>
          <div className="card-footer secret-list">
            <SecretField
              label="Chave da API"
              set={settings.api_key_set}
              disabled={busy}
              onSave={async (api_key) => {
                await run({ api_key });
              }}
            />
          </div>
        </section>
      </div>

      <section className="card card-flush" aria-labelledby="supervisor-runs-title">
        <header className="card-header">
          <div className="card-heading">
            <span className="eyebrow">Histórico</span>
            <h3 id="supervisor-runs-title" className="card-title">
              Análises recentes
            </h3>
          </div>
        </header>
        <div className="card-body">
          {runs === null ? <p>Carregando…</p> : <SupervisorRuns runs={runs} />}
        </div>
      </section>

      <section className="card card-flush" aria-labelledby="supervisor-actions-title">
        <header className="card-header">
          <div className="card-heading">
            <span className="eyebrow">Registro</span>
            <h3 id="supervisor-actions-title" className="card-title">
              Ações da supervisão
              {actions && <span className="card-count">{actions.length}</span>}
            </h3>
          </div>
        </header>
        {actions === null ? (
          <p className="card-body">Carregando…</p>
        ) : actions.length === 0 ? (
          <EmptyState compact icon="list" title="Nenhuma ação ainda">
            Os bloqueios, as paradas, os cancelamentos e os encerramentos aparecem aqui, com quem
            pediu e o resultado.
          </EmptyState>
        ) : (
          <div className="table-wrap">
            <table className="table table-responsive">
              <thead>
                <tr>
                  <th scope="col">Quando</th>
                  <th scope="col">Ação</th>
                  <th scope="col">Agentes</th>
                  <th scope="col">Origem</th>
                  <th scope="col">Situação</th>
                  <th scope="col">Motivo</th>
                </tr>
              </thead>
              <tbody>
                {actions.map((action) => {
                  const status = actionStatusLabel(action.status);
                  return (
                    <tr key={action.id}>
                      <td data-label="Quando" className="mono">
                        {dayClock(action.created_at)}
                      </td>
                      <td data-label="Ação">
                        #{action.id} {actionLabel(action.action, action.mode)}
                      </td>
                      <td data-label="Agentes">{action.scope.agents.join(", ") || "—"}</td>
                      <td data-label="Origem">
                        {originLabel(action.source, action.confirmed_by || action.requested_by)}
                      </td>
                      <td data-label="Situação">
                        <button
                          type="button"
                          className="link-btn"
                          aria-expanded={open === action.id}
                          onClick={() => setOpen(open === action.id ? null : action.id)}
                        >
                          <span className={toneClass(status.tone)}>{status.text}</span>
                        </button>
                        {open === action.id && action.result && (
                          <ResultItems items={action.result.items} error={action.result.error} />
                        )}
                      </td>
                      <td data-label="Motivo">{action.reason}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}
