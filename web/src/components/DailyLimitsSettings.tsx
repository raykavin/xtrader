import { useCallback, useEffect, useState } from "react";
import { fetchDailyAgentsPage, fetchDailyLimits } from "../api/client";
import type { DailyLimitsResponse, DailyScopeStatus, DailyStopInfo } from "../api/types";
import {
  blockNote,
  dailyStateLabel,
  flattenLabel,
  globalUnit,
  limitsLabel,
  resultAmount,
  scopeResults,
  stopMoveLabel,
  stopScope,
  stopSide,
} from "../lib/dailyLimits";
import { formatMoney, formatPrice, formatProfit } from "../lib/format";
import { AssetAgentsCard } from "./AssetAgentsCard";
import { DailyLimitFields } from "./DailyLimitFields";
import { ExposureCard } from "./ExposureCard";
import { PageHeader } from "./PageHeader";
import { ListPages } from "./Paginator";
import type { SectionIntro } from "./SettingsPage";
import { Alert, Loading, Skeleton } from "./States";
import { TradingHoursCard } from "./TradingHoursCard";
import { usePagedList } from "./usePagedList";

/** How often the state of the daily limits is read again, in ms. */
const POLL = 5000;

interface Props {
  intro: SectionIntro;
}

/**
 * A scope of the daily limits now, each thing once: its state with how its
 * positions were zeroed; its result of the day (of all the agents, one by
 * currency of the accounts), realized and open; and what its guard blocks,
 * until when (see blockNote).
 */
function ScopeState({ scope, zone }: { scope: DailyScopeStatus; zone: string }) {
  const state = dailyStateLabel(scope);
  const flatten = flattenLabel(scope.guard?.flatten_status);
  const results = scopeResults(scope);
  const note = blockNote(scope, zone);
  const missing = results.find((r) => !r.complete && (r.missing?.length ?? 0) > 0)?.missing;
  return (
    <div className="daily-state">
      <div className="daily-state-head">
        <span className={`status-chip ${state.tone}`}>{state.text}</span>
        {flatten && <span className={`status-chip ${flatten.tone}`}>{flatten.text}</span>}
      </div>
      {results.map((result) => (
        <div key={result.currency || "none"} className="daily-result">
          <strong className="mono">{resultAmount(result.total, result.currency)}</strong>
          <span className="field-hint">
            realizado {formatProfit(result.realized)} · aberto {formatProfit(result.open)}
            {!result.complete && " · incompleto"}
          </span>
        </div>
      ))}
      {note && <p className="field-hint daily-reason">{note}</p>}
      {missing && <p className="field-hint">Falta: {missing.join("; ")}</p>}
    </div>
  );
}

/**
 * The stops the daily loss limits ask of the open positions now: each one's
 * agent, asset and side, the level asked, the limit it comes from with the
 * share of the loss left of the position, and its last move at the broker.
 */
function DailyStops({ stops }: { stops: DailyStopInfo[] }) {
  return (
    <section className="daily-stops" aria-labelledby="daily-stops-title">
      <h3 id="daily-stops-title" className="daily-stops-title">
        Stops na corretora agora
      </h3>
      <div className="table-wrap">
        <table className="table table-responsive">
          <thead>
            <tr>
              <th scope="col">Agente</th>
              <th scope="col">Ativo</th>
              <th scope="col">Lado</th>
              <th scope="col" className="num">
                Stop pedido
              </th>
              <th scope="col">Limite</th>
              <th scope="col">Situação</th>
            </tr>
          </thead>
          <tbody>
            {stops.map((stop) => {
              const move = stopMoveLabel(stop);
              return (
                <tr key={`${stop.agent}-${stop.pair}-${stop.tickets.join(",")}`}>
                  <td data-label="Agente">{stop.agent}</td>
                  <td data-label="Ativo" className="mono">
                    {stop.pair}
                  </td>
                  <td data-label="Lado">{stopSide(stop)}</td>
                  <td data-label="Stop pedido" className="num mono">
                    {formatPrice(stop.level)}
                  </td>
                  <td data-label="Limite">
                    {stopScope(stop)}
                    <span className="field-hint daily-stop-share">
                      {" "}
                      · {formatMoney(stop.share)} de {formatMoney(stop.headroom)} {stop.currency}
                    </span>
                  </td>
                  <td data-label="Situação" className="daily-cell">
                    <span className={`status-chip ${move.tone}`}>{move.text}</span>
                    {move.detail && (
                      <span className="field-hint daily-stop-detail">{move.detail}</span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}

/**
 * The daily limits of the application: the loss and the gain of the day of
 * all the agents together (their result realized plus open, in the currency of
 * the accounts, the limits applying to each currency apart, none converted),
 * how they stand now, and the agents with limits of their own, a page at a
 * time; reached, the positions zeroed, the pending entries canceled and the
 * entries blocked until 00:00.
 */
export function DailyLimitsSettings({ intro }: Props) {
  const [data, setData] = useState<DailyLimitsResponse | null>(null);
  const agents = usePagedList(
    (params, signal) =>
      fetchDailyAgentsPage(params, signal).then((r) => ({ rows: r.agents, page: r })),
    { poll: POLL },
  );
  const [error, setError] = useState<string | null>(null);
  const [version, setVersion] = useState(0); // the form remounted with the limits saved

  const load = useCallback((signal?: AbortSignal) => {
    fetchDailyLimits(signal)
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
    const timer = window.setInterval(() => load(controller.signal), POLL);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, [load]);

  if (!data) {
    return (
      <>
        <PageHeader eyebrow="Configurações" title={intro.title} description={intro.description} />
        {error ? (
          <Alert tone="error">{error}</Alert>
        ) : (
          <Loading label="Carregando os limites diários">
            <Skeleton height={220} />
          </Loading>
        )}
      </>
    );
  }
  const status = data.status;
  return (
    <>
      <PageHeader eyebrow="Configurações" title={intro.title} description={intro.description} />
      {error && <Alert tone="error">Não foi possível atualizar: {error}</Alert>}

      <section className="card card-flush" aria-labelledby="daily-global-title">
        <header className="card-header">
          <div className="card-heading">
            <span className="eyebrow">Todos os agentes somados</span>
            <h2 id="daily-global-title" className="card-title">
              Limite diário global
            </h2>
          </div>
        </header>
        <div className="card-body daily-body">
          <DailyLimitFields
            key={version}
            unit={globalUnit(scopeResults(status.global))}
            settings={data.settings}
            onSaved={(_, response) => {
              setData({ settings: response.settings, status: response.status });
              setVersion((v) => v + 1);
            }}
          />
          {(status.stops?.length ?? 0) > 0 && <DailyStops stops={status.stops ?? []} />}
        </div>
      </section>

      <TradingHoursCard />

      <ExposureCard />

      <AssetAgentsCard />

      <section className="card card-flush" aria-labelledby="daily-agents-title">
        <header className="card-header">
          <div className="card-heading">
            <span className="eyebrow">Cada agente</span>
            <h2 id="daily-agents-title" className="card-title">
              Limites por agente
            </h2>
          </div>
          <span className="field-hint">
            Configure em Configurações › Agentes, no botão Limite diário do agente.
          </span>
        </header>
        {agents.error && (
          <Alert tone="error">Não foi possível carregar os agentes: {agents.error}</Alert>
        )}
        {agents.rows === null ? (
          !agents.error && (
            <Loading label="Carregando…">
              <Skeleton height={120} />
            </Loading>
          )
        ) : agents.rows.length === 0 ? (
          <p className="card-body field-hint">Nenhum agente com limite diário próprio.</p>
        ) : (
          <div className="table-wrap">
            <table className="table table-responsive">
              <thead>
                <tr>
                  <th scope="col">Agente</th>
                  <th scope="col">Limites</th>
                  <th scope="col">Hoje</th>
                </tr>
              </thead>
              <tbody>
                {agents.rows.map((scope) => (
                  <tr key={scope.agent}>
                    <td data-label="Agente">{scope.agent}</td>
                    <td data-label="Limites">
                      {limitsLabel(scope.limits, scope.result?.currency)}
                    </td>
                    <td data-label="Hoje" className="daily-cell">
                      <ScopeState scope={scope} zone={status.zone} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <div className="list-foot">
          <ListPages info={agents.info} onPage={agents.setPage} />
        </div>
      </section>
    </>
  );
}
