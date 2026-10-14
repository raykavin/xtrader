import { useEffect, useState } from "react";
import { fetchClosedProfit, fetchOpenPositions } from "../api/client";
import type { ClosedProfitResponse, CurrencyAmount, OpenPositionsResponse } from "../api/types";
import {
  type ClosedPeriod,
  closedPeriodLabel,
  closedPLSummary,
  closedRange,
  loadClosedPeriod,
  saveClosedPeriod,
} from "../lib/closedPL";
import { formatMoney, formatProfit, signClass } from "../lib/format";
import {
  accountsSummary,
  agentLines,
  amountsText,
  openPLGaps,
  openPLSummary,
  severalCurrencies,
} from "../lib/openPL";
import { ClosedPeriodDialog } from "./ClosedPeriodDialog";
import { SectionHead } from "./SectionHead";
import { Skeleton } from "./States";

/** How often the open profit and the balances of the agents are read again, in ms, as the market moves them. */
const OPEN_PL_REFRESH = 5000;

/** How often the closed profit of the period is read again, in ms: it changes as a position closes. */
const CLOSED_PL_REFRESH = 15000;

/**
 * The last reads, of all the agents: kept as the dashboard switches agents,
 * shown at once by the next one while it reads again.
 */
let lastRead: OpenPositionsResponse | null = null;
let lastClosed: ClosedProfitResponse | null = null;

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

/**
 * The closed profit of the agents in period, read at once, as the period
 * changes and every CLOSED_PL_REFRESH, its range taken again each time (today
 * follows midnight); the read of another range not shown, the last one of the
 * range kept on a failure.
 */
function useClosedProfit(period: ClosedPeriod) {
  const [read, setRead] = useState<ClosedProfitResponse | null>(lastClosed);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    const load = () => {
      const range = closedRange(period);
      fetchClosedProfit(range.from, range.to, controller.signal)
        .then((response) => {
          lastClosed = response;
          setRead(response);
          setError(null);
        })
        .catch((e) => {
          if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
        });
    };
    setError(null);
    load();
    const timer = window.setInterval(load, CLOSED_PL_REFRESH);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, [period]);
  const range = closedRange(period);
  const current = read && read.from === range.from && read.to === range.to ? read : null;
  return { read: current, error };
}

/** The time of a read in UTC, as the cards show it, eg: 21:04:05. */
function readTime(unixSeconds: number): string {
  return new Date(unixSeconds * 1000).toISOString().slice(11, 19);
}

/** A day and time of the browser, eg: 14/10 00:00, as the tooltip of the period tells its range. */
function localTime(unixSeconds: number): string {
  return new Date(unixSeconds * 1000).toLocaleString("pt-BR", {
    day: "2-digit",
    month: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** Amounts set large, each in its currency, incomplete told beside them; — without any. */
function Amounts({
  amounts,
  complete,
  gaps,
  signed,
  empty,
  what,
}: {
  amounts: CurrencyAmount[];
  complete: boolean;
  /** Why it is incomplete, the tooltip of its badge. */
  gaps: string[];
  /** Whether they are profits, signed and colored, else balances. */
  signed: boolean;
  /** The tooltip of —, eg: no account read. */
  empty: string;
  what: string;
}) {
  return (
    <div className="openpl-total">
      {amounts.length === 0 ? (
        <span className="openpl-value" title={empty}>
          —
        </span>
      ) : (
        amounts.map((total) => {
          const text = signed ? formatProfit(total.amount) : formatMoney(total.amount);
          return (
            <span
              key={total.currency}
              className={`openpl-value ${signed ? signClass(total.amount) : ""}`}
              title={`${what}: ${text} ${total.currency}, na moeda da conta${complete ? "" : " (incompleto)"}`}
            >
              {text}
              <small>{total.currency}</small>
            </span>
          );
        })
      )}
      {!complete && (
        <span className="badge outcome ignored" title={gaps.join("\n")}>
          Incompleto
        </span>
      )}
    </div>
  );
}

/**
 * The accounts of all the agents, whichever the dashboard shows: the profit
 * and the swap of the positions they hold open now, the profit their
 * positions closed in the period (picked by a double click on its title:
 * today, this week, this month or custom days) and the balances of their
 * accounts, as the broker reports them, by the currency of each account (none
 * converted: a total for each currency); by agent below them (a stopped or
 * disabled one marked so); incomplete, and why, when an account wasn't read.
 */
export function OpenProfitCard() {
  const { read, error } = useOpenPositions();
  const [period, setPeriod] = useState<ClosedPeriod>(loadClosedPeriod);
  const [picking, setPicking] = useState(false);
  const closed = useClosedProfit(period);
  const lines = read || closed.read ? agentLines(read, closed.read) : [];
  const several = severalCurrencies(read, closed.read);
  const gaps = [
    ...new Set([
      ...(read ? openPLGaps(read) : []),
      ...(closed.read ? openPLGaps(closed.read) : []),
    ]),
  ];
  const range = closedRange(period);
  const label = closedPeriodLabel(period);

  const apply = (next: ClosedPeriod) => {
    saveClosedPeriod(next);
    setPeriod(next);
  };

  return (
    <section className="card openpl-card" aria-labelledby="openpl-title">
      <div className="openpl-layout">
        <div className="openpl-metric openpl-open">
          <SectionHead id="openpl-title" eyebrow="Todos os agentes" title="P/L em aberto" />
          {read ? (
            <div className="openpl-main">
              <Amounts
                amounts={read.totals}
                complete={read.complete}
                gaps={openPLGaps(read)}
                signed
                empty="Nenhuma conta de agente em live lida"
                what="Em aberto"
              />
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
          ) : error ? (
            <p className="openpl-note error">Não foi possível ler as posições abertas: {error}</p>
          ) : (
            <Skeleton className="openpl-skeleton" />
          )}
        </div>

        <div className="openpl-metric openpl-closed">
          <SectionHead
            eyebrow={label}
            title={
              <button
                type="button"
                className="openpl-period"
                title={`De ${localTime(range.from)} até antes de ${localTime(range.to)} (horário deste navegador). Clique duas vezes para escolher o período.`}
                aria-haspopup="dialog"
                aria-label={`P/L fechado, ${label}: clique duas vezes para escolher o período`}
                onDoubleClick={() => setPicking(true)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault();
                    setPicking(true);
                  }
                }}
              >
                P/L fechado
              </button>
            }
          />
          {closed.read ? (
            <div className="openpl-main">
              <Amounts
                amounts={closed.read.totals}
                complete={closed.read.complete}
                gaps={openPLGaps(closed.read)}
                signed
                empty="Nenhuma conta de agente em live lida"
                what={`Fechado (${label})`}
              />
              <p className="openpl-summary">{closedPLSummary(closed.read)}</p>
            </div>
          ) : closed.error ? (
            <p className="openpl-note error">Não foi possível ler o P/L fechado: {closed.error}</p>
          ) : (
            <Skeleton className="openpl-skeleton" />
          )}
        </div>

        <div className="openpl-metric openpl-balance">
          <SectionHead eyebrow="Contas" title="Saldo" />
          {read ? (
            <div className="openpl-main">
              <Amounts
                amounts={read.balances}
                complete={read.complete}
                gaps={openPLGaps(read)}
                signed={false}
                empty="Nenhuma conta de agente em live lida"
                what="Saldo"
              />
              <p className="openpl-summary">{accountsSummary(read)}</p>
            </div>
          ) : error ? null : (
            <Skeleton className="openpl-skeleton" />
          )}
        </div>

        {lines.length > 0 && (
          <ul className="openpl-agents" aria-label="Por agente">
            {lines.map((line) => (
              <li
                key={line.agent}
                title={[
                  line.agent,
                  line.positions > 0 &&
                    (line.positions === 1
                      ? "1 posição aberta"
                      : `${line.positions} posições abertas`),
                  line.closedPositions > 0 &&
                    `${line.closedPositions === 1 ? "1 posição fechada" : `${line.closedPositions} posições fechadas`} (${label})`,
                  !line.running && "o agente parado",
                ]
                  .filter(Boolean)
                  .join(", ")}
              >
                <span className="openpl-agent">{line.agent}</span>
                {!line.running && <span className="openpl-stopped">parado</span>}
                {line.open.length > 0 && (
                  <span className="openpl-part">
                    <small>aberto</small>
                    <span
                      className={`openpl-profit mono ${line.open.length === 1 ? signClass(line.open[0]?.amount ?? 0) : ""}`}
                    >
                      {amountsText(line.open, several)}
                    </span>
                  </span>
                )}
                {line.closed.length > 0 && (
                  <span className="openpl-part">
                    <small>fechado</small>
                    <span
                      className={`openpl-profit mono ${line.closed.length === 1 ? signClass(line.closed[0]?.amount ?? 0) : ""}`}
                    >
                      {amountsText(line.closed, several)}
                    </span>
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
        {(read || closed.read) && (
          <>
            {gaps.length > 0 && (
              <ul className="openpl-gaps">
                {gaps.map((gap) => (
                  <li key={gap}>{gap}</li>
                ))}
              </ul>
            )}
            {read && error && (
              <p className="openpl-note error">Não foi possível atualizar: {error}</p>
            )}
            {closed.read && closed.error && (
              <p className="openpl-note error">
                Não foi possível atualizar o P/L fechado: {closed.error}
              </p>
            )}
          </>
        )}
      </div>
      {picking && (
        <ClosedPeriodDialog period={period} onApply={apply} onClose={() => setPicking(false)} />
      )}
    </section>
  );
}
