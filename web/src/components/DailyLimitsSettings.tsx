import { useCallback, useEffect, useState } from "react";
import { fetchDailyLimits } from "../api/client";
import type { DailyLimitsResponse, DailyScopeStatus } from "../api/types";
import {
  blockNote,
  dailyStateLabel,
  flattenLabel,
  globalUnit,
  limitsLabel,
  resultAmount,
  scopeResults,
} from "../lib/dailyLimits";
import { formatProfit } from "../lib/format";
import { DailyLimitFields } from "./DailyLimitFields";
import { PageHeader } from "./PageHeader";
import type { SectionIntro } from "./SettingsPage";
import { Alert, Loading, Skeleton } from "./States";
import { TradingHoursCard } from "./TradingHoursCard";

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
 * The daily limits of the application: the loss and the gain of the day of
 * all the agents together (their result realized plus open, in the currency of
 * the accounts, the limits applying to each currency apart, none converted),
 * how they stand now, the agents
 * with limits of their own, and how the limits act: reached, the positions
 * zeroed, the pending entries canceled and the entries blocked until 00:00.
 */
export function DailyLimitsSettings({ intro }: Props) {
  const [data, setData] = useState<DailyLimitsResponse | null>(null);
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
        </div>
      </section>

      <TradingHoursCard />

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
        {status.agents.length === 0 ? (
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
                {status.agents.map((scope) => (
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
      </section>

      <section className="card" aria-labelledby="daily-how-title">
        <h2 id="daily-how-title" className="card-title">
          Como funciona
        </h2>
        <ul className="daily-notes">
          <li>
            O resultado do dia é o realizado desde 00:00 ({status.zone}), com swap, comissão e
            taxas, mais o P/L das posições abertas, lido das contas MT5 a cada 5 segundos. Cada
            operação conta uma vez, mesmo com vários agentes na mesma conta; posições manuais não
            contam.
          </li>
          <li>
            Atingida a perda ou o ganho em duas leituras seguidas, as posições são zeradas, as
            ordens pendentes de entrada canceladas e as entradas bloqueadas até 00:00: de todos os
            agentes no limite global, só do agente no limite dele. Stop loss, trailing stop e
            breakeven continuam valendo.
          </li>
          <li>
            Reiniciar o XTrader, religar ou liberar um agente ou redefinir os resultados dele não
            desfaz o bloqueio. Só a virada do dia ou uma mudança no limite o libera.
          </li>
          <li>
            Tudo na moeda de cada conta, sem conversão de câmbio: com contas em moedas diferentes, o
            limite global vale para o resultado de cada moeda separadamente e, atingido em qualquer
            uma, zera e bloqueia todos os agentes.
          </li>
          <li>
            Se uma conta não puder ser lida, as entradas ficam bloqueadas (sem zerar nada) até a
            leitura voltar.
          </li>
          {(status.paper?.length ?? 0) > 0 && (
            <li>
              Os agentes em paper ({status.paper?.length}) não contam no resultado, mas são
              bloqueados e zerados junto com todos.
            </li>
          )}
          <li>
            Horário de operação: fora das janelas, nenhum agente abre posição; numa janela com
            agentes ou ativos definidos, só eles abrem (saídas, stops e alvos sempre passam; a
            Boleta não segue os ativos da janela). No fim de cada janela, zera as posições e cancela
            as ordens pendentes de todos os agentes e ativos, conforme marcado nela; sem zerar, as
            posições abertas ficam como estão, com os stops. Se o XTrader estiver parado no fim de
            uma janela, faz isso ao voltar.
          </li>
          <li>
            Limitações: uma ordem já enviada antes do bloqueio pode ser executada (é zerada em
            seguida); o histórico do MT5 atrasa alguns segundos; entre a leitura e o fechamento o
            preço se move, então o resultado final pode passar um pouco do limite.
          </li>
        </ul>
      </section>
    </>
  );
}
