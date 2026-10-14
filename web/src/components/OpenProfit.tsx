import { useEffect, useState } from "react";
import { fetchOpenPositions } from "../api/client";
import type { OpenPositionsResponse } from "../api/types";
import { formatProfit, signClass } from "../lib/format";
import { agentOpenPLText, openPLByAgent, openPLGaps, openPLSummary } from "../lib/openPL";
import { SectionHead } from "./SectionHead";
import { Skeleton } from "./States";

/** How often the open profit of the agents is read again, in ms, as the market moves it. */
const OPEN_PL_REFRESH = 5000;

/**
 * The last read of the open profit, of all the agents: kept as the dashboard
 * switches agents, shown at once by the next one while it reads again.
 */
let lastRead: OpenPositionsResponse | null = null;

/** The open profit of the agents, read at once and every OPEN_PL_REFRESH, the last one kept on a failure. */
function useOpenPositions() {
  const [read, setRead] = useState<OpenPositionsResponse | null>(lastRead);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    const load = () => {
      fetchOpenPositions(controller.signal)
        .then((response) => {
          lastRead = response;
          setRead(response);
          setError(null);
        })
        .catch((e) => {
          if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
        });
    };
    load();
    const timer = window.setInterval(load, OPEN_PL_REFRESH);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, []);
  return { read, error };
}

/** The time of a read in UTC, as the cards show it, eg: 21:04:05. */
function readTime(unixSeconds: number): string {
  return new Date(unixSeconds * 1000).toISOString().slice(11, 19);
}

/**
 * The open profit of all the agents, whichever the dashboard shows: the profit
 * and the swap of the positions they hold open now on their accounts, as the
 * broker reports them, summed by the currency of each account (none
 * converted: a total for each currency), by agent below it (a stopped or
 * disabled one marked so); incomplete, and why, when an account wasn't read.
 * Not realized: the closed trades are the net profit of each agent, and the
 * daily limits of the prompts count apart.
 */
export function OpenProfitCard() {
  const { read, error } = useOpenPositions();
  const agents = read ? openPLByAgent(read) : [];
  const gaps = read ? openPLGaps(read) : [];
  return (
    <section className="card openpl-card" aria-labelledby="openpl-title">
      <div className="openpl-layout">
        <div className="openpl-lead">
          <SectionHead id="openpl-title" eyebrow="Todos os agentes" title="P/L em aberto" />
          {read && (
            <div className="openpl-main">
              <div className="openpl-total">
                {read.totals.length === 0 ? (
                  <span className="openpl-value" title="Nenhuma conta de agente em live lida">
                    —
                  </span>
                ) : (
                  read.totals.map((total) => (
                    <span
                      key={total.currency}
                      className={`openpl-value ${signClass(total.amount)}`}
                      title={`${formatProfit(total.amount)} ${total.currency}, na moeda da conta${read.complete ? "" : " (incompleto)"}`}
                    >
                      {formatProfit(total.amount)}
                      <small>{total.currency}</small>
                    </span>
                  ))
                )}
                {!read.complete && (
                  <span className="badge outcome ignored" title={gaps.join("\n")}>
                    Incompleto
                  </span>
                )}
              </div>
              <p className="openpl-summary">
                {openPLSummary(read)} ·{" "}
                <time
                  dateTime={new Date(read.at * 1000).toISOString()}
                  title="Lido da corretora neste horário (UTC)"
                >
                  {readTime(read.at)} UTC
                </time>
              </p>
            </div>
          )}
        </div>
        {!read ? (
          error ? (
            <p className="openpl-note error">Não foi possível ler as posições abertas: {error}</p>
          ) : (
            <Skeleton className="openpl-skeleton" />
          )
        ) : (
          <>
            {agents.length > 0 && (
              <ul className="openpl-agents" aria-label="Por agente">
                {agents.map((agent) => (
                  <li
                    key={agent.agent}
                    title={`${agent.agent}: ${agent.positions === 1 ? "1 posição aberta" : `${agent.positions} posições abertas`}${agent.running ? "" : ", o agente parado"}`}
                  >
                    <span className="openpl-agent">{agent.agent}</span>
                    {!agent.running && <span className="openpl-stopped">parado</span>}
                    <span
                      className={`openpl-profit mono ${agent.amounts.length === 1 ? signClass(agent.amounts[0]?.amount ?? 0) : ""}`}
                    >
                      {agentOpenPLText(agent, read)}
                    </span>
                  </li>
                ))}
              </ul>
            )}
            {gaps.length > 0 && (
              <ul className="openpl-gaps">
                {gaps.map((gap) => (
                  <li key={gap}>{gap}</li>
                ))}
              </ul>
            )}
            {error && <p className="openpl-note error">Não foi possível atualizar: {error}</p>}
            <p
              className="openpl-hint"
              title="O lucro e o swap das posições abertas agora, como a corretora mostra, de todos os agentes em live, rodando ou não. O lucro líquido de cada agente conta as operações encerradas; os limites diários de ganho e perda dos prompts são outra conta."
            >
              Não realizado: posições abertas agora. Fora daqui: operações encerradas, ordens
              pendentes e limites diários.
            </p>
          </>
        )}
      </div>
    </section>
  );
}
