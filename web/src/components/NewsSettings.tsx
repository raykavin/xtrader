import { useCallback, useEffect, useState } from "react";
import { fetchNewsSettings, testNews, updateNewsSettings } from "../api/client";
import type { NewsSettingsResponse, NewsTestResponse } from "../api/types";
import { formatTime } from "../lib/format";
import { newsState, newsStatusLabel } from "../lib/news";
import { Icon } from "./Icon";
import { PageHeader } from "./PageHeader";
import { SecretField } from "./SecretField";
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

/**
 * The news of all the agents: the feature on or off, the token of the MCP
 * server of the calendar shared by the LLM agents (never shown: only whether
 * it is set), the conflict of the tokens the agents kept each, a test of the
 * servers, and the LLM agents with their server and their news block. A
 * change applies to the agents running at once, through their runtime.
 */
export function NewsSettings({ intro }: Props) {
  const [data, setData] = useState<NewsSettingsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [test, setTest] = useState<NewsTestResponse | null>(null);
  const [testing, setTesting] = useState(false);

  const load = useCallback((signal?: AbortSignal) => {
    fetchNewsSettings(signal)
      .then((response) => {
        setData(response);
        setError(null);
      })
      .catch((e) => {
        if (!signal?.aborted) setError(e instanceof Error ? e.message : String(e));
      });
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal);
    return () => controller.abort();
  }, [load]);

  const toggle = (enabled: boolean) => {
    setBusy(true);
    setError(null);
    updateNewsSettings({ enabled })
      .then(setData)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };
  const saveToken = async (token: string) => {
    setData(await updateNewsSettings({ token }));
    setTest(null);
  };
  const runTest = () => {
    setTesting(true);
    setError(null);
    testNews()
      .then(setTest)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setTesting(false));
  };

  if (!data) {
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
  const state = newsState(data.enabled, data.token_set);
  const withCalendar = data.agents.filter((a) => a.calendar_url);

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
            label="Consultar o calendário econômico"
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
            O token é guardado cifrado e nunca é mostrado. Ele vale para todos os agentes LLM; o
            endereço do servidor e o bloqueio por notícia continuam no card de cada agente. Ligar,
            desligar ou trocar o token vale na hora para os agentes em execução, sem reiniciar.
          </p>
          {data.updated_at ? (
            <p className="field-hint">
              Alterado por {data.updated_by} em {formatTime(data.updated_at)} UTC.
            </p>
          ) : null}
        </div>
      </section>

      <section className="card card-flush" aria-labelledby="news-test-title">
        <header className="card-header">
          <div className="card-heading">
            <span className="eyebrow">Conexão</span>
            <h2 id="news-test-title" className="card-title">
              Testar o servidor MCP
            </h2>
          </div>
          <button
            type="button"
            className="btn btn-sm"
            disabled={testing || withCalendar.length === 0}
            onClick={runTest}
          >
            <Icon name="restart" size={14} />
            {testing ? "Testando…" : "Testar agora"}
          </button>
        </header>
        <div className="card-body news-body">
          {withCalendar.length === 0 ? (
            <p className="field-hint">
              Nenhum agente LLM tem o endereço do servidor do calendário (no card do agente).
            </p>
          ) : !test ? (
            <p className="field-hint">
              Consulta uma vez o servidor de cada agente com o token salvo
              {data.enabled ? "" : ", mesmo com as notícias desativadas"}, para conferir a
              credencial.
            </p>
          ) : (
            <ul className="news-results">
              {test.results.map((result) => {
                const label = newsStatusLabel(result.status);
                return (
                  <li key={result.url} className="news-result">
                    <span className={chip(label.tone)}>{label.text}</span>
                    <span className="mono news-url">{result.url}</span>
                    <span className="field-hint">
                      {result.agents.join(", ")} · {label.hint}
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
            <span className="eyebrow">Agentes LLM</span>
            <h2 id="news-agents-title" className="card-title">
              Quem consulta
            </h2>
          </div>
        </header>
        {data.agents.length === 0 ? (
          <EmptyState compact icon="news" title="Nenhum agente LLM" />
        ) : (
          <div className="table-wrap news-table">
            <table className="table table-responsive">
              <thead>
                <tr>
                  <th scope="col">Agente</th>
                  <th scope="col">Servidor do calendário</th>
                  <th scope="col">Bloqueio por notícia</th>
                  <th scope="col">Estado</th>
                </tr>
              </thead>
              <tbody>
                {data.agents.map((agent) => (
                  <tr key={agent.agent}>
                    <td className="strong" data-label="Agente">
                      {agent.agent}
                    </td>
                    <td className="mono" data-label="Servidor">
                      {agent.calendar_url || <span className="muted">sem calendário</span>}
                    </td>
                    <td data-label="Bloqueio">
                      {agent.block_before || agent.block_after
                        ? `${agent.block_before || "0"} antes · ${agent.block_after || "0"} depois`
                        : "padrão do agente"}
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
