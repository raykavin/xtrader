import { type FormEvent, useCallback, useEffect, useId, useState } from "react";
import { fetchNewsSettings, testNews, updateNewsSettings } from "../api/client";
import type { NewsSettingsResponse, NewsTestResponse } from "../api/types";
import { formatTime } from "../lib/format";
import { newsState, newsStatusLabel } from "../lib/news";
import { Icon } from "./Icon";
import { PageHeader } from "./PageHeader";
import { SecretField } from "./SecretField";
import { Select } from "./Select";
import type { SectionIntro } from "./SettingsPage";
import { Alert, EmptyState, Loading, Skeleton } from "./States";
import { Switch } from "./Switch";

interface Props {
  intro: SectionIntro;
}

/** The class of a status chip of tone. */
function chip(tone: "on" | "warn" | "error"): string {
  return `status-chip ${tone === "error" ? "stopped" : tone}`;
}

const importances = [
  { value: "high", label: "Alta" },
  { value: "medium", label: "Média e alta" },
  { value: "low", label: "Todas" },
];

/** The rules of the news as typed: the server and the window, in minutes. */
interface RulesForm {
  url: string;
  before: string;
  after: string;
  importance: string;
}

function rulesOf(data: NewsSettingsResponse): RulesForm {
  return {
    url: data.url ?? "",
    before: String(data.block_before),
    after: String(data.block_after),
    importance: data.block_importance || "high",
  };
}

/** Why a window in minutes is refused; null when it is taken. */
function minutesError(text: string): string | null {
  return /^\d+$/.test(text.trim()) && Number(text) <= 1440 ? null : "De 0 a 1440 minutos.";
}

/**
 * The news of all the agents, LLM or not: the feature on or off; the MCP
 * server of the calendar and its token (never shown: only whether it is set);
 * the window of the news block, inside which no agent opens a position, their
 * pending entries canceled and their positions closed as it starts; the
 * conflicts of the former settings of the agents; a test of the server; and
 * the agents. A change applies to the agents running at once, through their
 * runtime.
 */
export function NewsSettings({ intro }: Props) {
  const [data, setData] = useState<NewsSettingsResponse | null>(null);
  const [form, setForm] = useState<RulesForm | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [test, setTest] = useState<NewsTestResponse | null>(null);
  const [testing, setTesting] = useState(false);
  const id = useId();

  const accept = useCallback((response: NewsSettingsResponse) => {
    setData(response);
    setForm(rulesOf(response));
  }, []);
  const load = useCallback(
    (signal?: AbortSignal) => {
      fetchNewsSettings(signal)
        .then((response) => {
          accept(response);
          setError(null);
        })
        .catch((e) => {
          if (!signal?.aborted) setError(e instanceof Error ? e.message : String(e));
        });
    },
    [accept],
  );
  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal);
    return () => controller.abort();
  }, [load]);

  const toggle = (enabled: boolean) => {
    setBusy(true);
    setError(null);
    updateNewsSettings({ enabled })
      .then((response) => setData({ ...response })) // the rules typed kept
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };
  const saveToken = async (token: string) => {
    const response = await updateNewsSettings({ token });
    setData(response);
    setTest(null);
  };
  const errors = form
    ? {
        before: minutesError(form.before),
        after: minutesError(form.after),
        url:
          form.url.trim() === "" || /^https?:\/\/[^\s@]+$/.test(form.url.trim())
            ? null
            : "Um endereço http(s), ex.: http://mt5:22347/mcp.",
      }
    : { before: null, after: null, url: null };
  const saveRules = (event: FormEvent) => {
    event.preventDefault();
    if (!form || errors.before || errors.after || errors.url) return;
    setBusy(true);
    setError(null);
    setSaved(false);
    updateNewsSettings({
      url: form.url.trim(),
      block_before: Number(form.before),
      block_after: Number(form.after),
      block_importance: form.importance,
    })
      .then((response) => {
        accept(response);
        setSaved(true);
        setTest(null);
      })
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };
  const runTest = () => {
    setTesting(true);
    setError(null);
    testNews()
      .then(setTest)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setTesting(false));
  };

  if (!data || !form) {
    return (
      <>
        <PageHeader eyebrow="Configurações" title={intro.title} description={intro.description} />
        {error ? (
          <Alert tone="error">{error}</Alert>
        ) : (
          <Loading label="Carregando as notícias">
            <Skeleton height={220} />
          </Loading>
        )}
      </>
    );
  }
  const state = newsState(
    data.enabled,
    data.token_set,
    data.url ?? "",
    data.block_before + data.block_after,
  );
  const set = (field: keyof RulesForm) => (value: string) => {
    setForm((f) => (f ? { ...f, [field]: value } : f));
    setSaved(false);
  };

  return (
    <>
      <PageHeader eyebrow="Configurações" title={intro.title} description={intro.description} />
      {error && <Alert tone="error">{error}</Alert>}
      {data.conflict.length > 0 && (
        <Alert tone="warn" title="Tokens diferentes nos agentes">
          Os agentes {data.conflict.join(", ")} tinham tokens diferentes do servidor MCP (ou um que
          não pôde ser lido). Nenhum foi escolhido: defina abaixo o token que todos vão usar.
        </Alert>
      )}
      {data.url_conflict.length > 0 && (
        <Alert tone="warn" title="Servidores diferentes nos agentes">
          Os agentes {data.url_conflict.join(", ")} usavam servidores MCP diferentes. Nenhum foi
          escolhido: confira e salve abaixo o endereço que todos vão usar.
        </Alert>
      )}

      <section className="card card-flush" aria-labelledby="news-feature-title">
        <header className="card-header">
          <div className="card-heading">
            <span className="eyebrow">Todos os agentes</span>
            <h2 id="news-feature-title" className="card-title">
              Recurso de notícias
            </h2>
          </div>
          <span className={chip(state.tone)}>{state.text}</span>
        </header>
        <div className="card-body news-body">
          <Switch
            checked={data.enabled}
            disabled={busy}
            onChange={toggle}
            label="Consultar o calendário e bloquear por notícia"
          />
          <p className="field-hint">{state.hint}</p>
          <SecretField
            label="Token do servidor MCP"
            set={data.token_set}
            disabled={busy}
            placeholder="o token de Ferramentas › Opções › Servidor MCP"
            onSave={saveToken}
          />
          <p className="field-hint">
            O token é guardado cifrado e nunca é mostrado. Ligar, desligar ou trocar o token vale na
            hora para os agentes em execução, sem reiniciar.
          </p>
          {data.updated_at ? (
            <p className="field-hint">
              Alterado por {data.updated_by} em {formatTime(data.updated_at)} UTC.
            </p>
          ) : null}
        </div>
      </section>

      <section className="card card-flush" aria-labelledby="news-rules-title">
        <header className="card-header">
          <div className="card-heading">
            <span className="eyebrow">Para todos os agentes</span>
            <h2 id="news-rules-title" className="card-title">
              Servidor e janela da notícia
            </h2>
          </div>
        </header>
        <form className="card-body news-body" onSubmit={saveRules} noValidate>
          <div className="field" data-invalid={errors.url ? "" : undefined}>
            <label className="field-label" htmlFor={`${id}-url`}>
              Servidor MCP do calendário
            </label>
            <input
              id={`${id}-url`}
              className="input mono"
              inputMode="url"
              value={form.url}
              placeholder="http://mt5:22347/mcp"
              onChange={(e) => set("url")(e.target.value)}
            />
            <span className={errors.url ? "field-error" : "field-hint"}>
              {errors.url ?? "O servidor MCP do terminal MT5; em branco, nenhuma consulta."}
            </span>
          </div>
          <div className="form-grid news-window">
            <div className="field" data-invalid={errors.before ? "" : undefined}>
              <label className="field-label" htmlFor={`${id}-before`}>
                Minutos antes
              </label>
              <input
                id={`${id}-before`}
                className="input mono"
                inputMode="numeric"
                value={form.before}
                onChange={(e) => set("before")(e.target.value)}
              />
              {errors.before && <span className="field-error">{errors.before}</span>}
            </div>
            <div className="field" data-invalid={errors.after ? "" : undefined}>
              <label className="field-label" htmlFor={`${id}-after`}>
                Minutos depois
              </label>
              <input
                id={`${id}-after`}
                className="input mono"
                inputMode="numeric"
                value={form.after}
                onChange={(e) => set("after")(e.target.value)}
              />
              {errors.after && <span className="field-error">{errors.after}</span>}
            </div>
            <div className="field">
              <span className="field-label">Importância das divulgações</span>
              <Select
                caption="Importância das divulgações"
                value={form.importance}
                options={importances}
                onChange={set("importance")}
              />
            </div>
          </div>
          <p className="field-hint">
            Da janela antes de uma divulgação das moedas dos ativos de um agente até a janela depois
            dela, nenhum agente abre posição. Ao começar a janela, as ordens pendentes de entrada do
            ativo são canceladas e a posição dele é zerada a mercado. Os dois em 0: nenhum bloqueio.
          </p>
          <div className="news-actions">
            {saved && <span className="msg msg-ok">Salvo e aplicado aos agentes.</span>}
            <button type="submit" className="btn btn-primary btn-sm" disabled={busy}>
              Salvar
            </button>
          </div>
        </form>
      </section>

      <section className="card card-flush" aria-labelledby="news-test-title">
        <header className="card-header">
          <div className="card-heading">
            <span className="eyebrow">Conexão</span>
            <h2 id="news-test-title" className="card-title">
              Testar o servidor MCP
            </h2>
          </div>
          <button type="button" className="btn btn-sm" disabled={testing} onClick={runTest}>
            <Icon name="restart" size={14} />
            {testing ? "Testando…" : "Testar agora"}
          </button>
        </header>
        <div className="card-body news-body">
          {!test ? (
            <p className="field-hint">
              Consulta uma vez o servidor salvo com o token salvo
              {data.enabled ? "" : ", mesmo com as notícias desativadas"}, para conferir a
              credencial.
            </p>
          ) : (
            <ul className="news-results">
              {test.results.map((result) => {
                const label = newsStatusLabel(result.status);
                return (
                  <li key={result.url || "none"} className="news-result">
                    <span className={chip(label.tone)}>{label.text}</span>
                    {result.url && <span className="mono news-url">{result.url}</span>}
                    <span className="field-hint">
                      {label.hint}
                      {result.detail ? ` (${result.detail})` : ""}
                    </span>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      </section>

      <section className="card card-flush" aria-labelledby="news-agents-title">
        <header className="card-header">
          <div className="card-heading">
            <span className="eyebrow">Todos os agentes</span>
            <h2 id="news-agents-title" className="card-title">
              Quem respeita as notícias
            </h2>
          </div>
          <span className="field-hint">
            {data.agents.length === 1 ? "1 agente" : `${data.agents.length} agentes`}
          </span>
        </header>
        {data.agents.length === 0 ? (
          <EmptyState compact icon="news" title="Nenhum agente" />
        ) : (
          <div className="table-wrap news-table">
            <table className="table table-responsive">
              <thead>
                <tr>
                  <th scope="col">Agente</th>
                  <th scope="col">Tipo</th>
                  <th scope="col">Estado</th>
                </tr>
              </thead>
              <tbody>
                {data.agents.map((agent) => (
                  <tr key={agent.agent}>
                    <td className="strong" data-label="Agente">
                      {agent.agent}
                    </td>
                    <td data-label="Tipo">
                      {agent.kind === "llm" ? "LLM: calendário no contexto e bloqueio" : "bloqueio"}
                    </td>
                    <td data-label="Estado">
                      {agent.running
                        ? "executando"
                        : agent.enabled
                          ? "ligado, parado"
                          : "desligado"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </>
  );
}
