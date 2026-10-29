import { type ReactNode, useEffect, useState } from "react";
import { fetchClosedProfit, fetchOpenPositions } from "../api/client";
import type { ClosedProfitResponse, CurrencyAmount, OpenPositionsResponse } from "../api/types";
import {
  type ClosedPeriod,
  type ClosedRange,
  closedAxis,
  closedCharts,
  closedPeriodLabel,
  closedPLSummary,
  closedRange,
  loadClosedPeriod,
  saveClosedPeriod,
} from "../lib/closedPL";
import { formatMoney, formatProfit, signClass } from "../lib/format";
import {
  type AgentLine,
  accountsSummary,
  agentLines,
  agentOpenState,
  amountsText,
  type OpenState,
  openPLGaps,
  openPLSummary,
  profitTone,
  severalCurrencies,
} from "../lib/openPL";
import { palettes, type Theme } from "../lib/theme";
import { ClosedPeriodDialog } from "./ClosedPeriodDialog";
import { DailyGuardStrip, useDailyGuard } from "./DailyGuardStrip";
import { Icon } from "./Icon";
import { Sparkline } from "./Sparkline";
import { Skeleton } from "./States";

/** How often the open profit and the balances of the agents are read again, in ms, as the market moves them. */
const OPEN_PL_REFRESH = 5000;

/**
 * The agents a wide card shows beside its figures; with more, they go under
 * the figures and the chart, across the card, more of them a row.
 */
const AGENTS_BESIDE = 8;

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

/** The color of a profit by its sign in cents, as its value tells it; none for zero or signs mixed in several currencies. */
function profitClass(amounts: CurrencyAmount[]): string {
  const tone = profitTone(amounts);
  return tone === "pos" || tone === "neg" ? tone : "";
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

/** What the tile of an agent tells of its open profit beside its assets, by its state. */
const openNotes: Partial<Record<OpenState, { text: string; title: string }>> = {
  flat: { text: "Sem posição aberta", title: "O agente não tem posição aberta agora" },
  unknown: {
    text: "Em aberto: sem dados",
    title: "As posições abertas da conta do agente não puderam ser lidas: nada é contado como zero",
  },
};

/** The tile of an agent: its icon, its name, the profit open of each of its assets and its closed one, its background by the sign of its open profit. */
function AgentTile({
  line,
  open,
  several,
  label,
}: {
  line: AgentLine;
  open: OpenPositionsResponse | null;
  several: boolean;
  label: string;
}) {
  const state = agentOpenState(line, open);
  const note = openNotes[state];
  return (
    <li
      className={`openpl-tile is-${state}`}
      title={[
        line.agent,
        line.positions > 0 &&
          (line.positions === 1 ? "1 posição aberta" : `${line.positions} posições abertas`),
        line.closedPositions > 0 &&
          `${line.closedPositions === 1 ? "1 posição fechada" : `${line.closedPositions} posições fechadas`} (${label})`,
        !line.running && "o agente parado",
      ]
        .filter(Boolean)
        .join(", ")}
    >
      <span className="openpl-agent-head">
        <span className="openpl-agent-icon">
          <Icon name="bot" size={15} />
        </span>
        {line.assets.length > 1 && (
          <span
            className={`openpl-agent-total mono ${profitClass(line.open)}`}
            title="Em aberto, a soma dos ativos"
          >
            <span className="sr-only">Em aberto, a soma dos ativos: </span>
            {amountsText(line.open, several)}
          </span>
        )}
      </span>
      <span className="openpl-agent-name">
        <span className="openpl-agent">{line.agent}</span>
        {!line.running && <span className="openpl-stopped">parado</span>}
      </span>
      {note && (
        <p className="openpl-tile-note" title={note.title}>
          {note.text}
        </p>
      )}
      <dl className="openpl-assets">
        {line.assets.map((asset) => (
          <div
            key={asset.pair}
            className="openpl-asset"
            title={`${asset.pair}: ${asset.positions === 1 ? "1 posição aberta" : `${asset.positions} posições abertas`}`}
          >
            <dt>{asset.pair}:</dt>
            <dd className={`openpl-profit mono ${profitClass(asset.amounts)}`}>
              {amountsText(asset.amounts, several)}
            </dd>
          </div>
        ))}
        {line.closed.length > 0 && (
          <div
            className="openpl-asset openpl-asset-closed"
            title={`Fechado (${label}): ${line.closedPositions === 1 ? "1 posição" : `${line.closedPositions} posições`}`}
          >
            <dt>Fechado:</dt>
            <dd className={`openpl-profit mono ${profitClass(line.closed)}`}>
              {amountsText(line.closed, several)}
            </dd>
          </div>
        )}
      </dl>
    </li>
  );
}

/**
 * The evolution of the closed profit of all the agents in the period, as its
 * card tells it: the running total from 0 at the start of the period, exit
 * after exit, held until now, a line in steps along the time of the period
 * like the line of the quote card; a chart for each currency, none
 * converted. While the period is read, a placeholder; without an exit, or on
 * a failure, said so: never the read of another period.
 */
function ClosedEvolution({
  read,
  error,
  range,
  label,
  theme,
}: {
  read: ClosedProfitResponse | null;
  error: string | null;
  range: ClosedRange;
  label: string;
  theme: Theme;
}) {
  const now = Math.floor(Date.now() / 1000);
  const charts = read ? closedCharts(read, now) : [];
  const axis = closedAxis(range, now);
  const palette = palettes[theme];
  let body: ReactNode;
  if (!read) {
    body = error ? (
      <p className="openpl-chart-empty error">Não foi possível ler o P/L fechado deste período.</p>
    ) : (
      <Skeleton className="openpl-chart-skeleton" />
    );
  } else if (charts.length === 0) {
    body = <p className="openpl-chart-empty">Nenhuma posição fechada no período.</p>;
  } else {
    body = (
      <>
        {charts.map((chart) => {
          const tone = profitTone([{ currency: chart.currency, amount: chart.total }]);
          const color = tone === "pos" ? palette.up : tone === "neg" ? palette.down : palette.muted;
          const first = chart.points[0]?.time ?? range.from;
          const last = chart.points[chart.points.length - 1]?.time ?? first;
          return (
            <figure
              key={chart.currency}
              className="openpl-chart-plot"
              aria-label={`P/L fechado acumulado, ${label}: de 0 a ${formatProfit(chart.total)} ${chart.currency} em ${chart.exits === 1 ? "1 saída" : `${chart.exits} saídas`}`}
            >
              <Sparkline
                points={chart.points}
                color={color}
                width={400}
                height={72}
                domain={[first, last]}
                step
                zero
              />
              {charts.length > 1 && <figcaption>{chart.currency}</figcaption>}
            </figure>
          );
        })}
        <div className="openpl-chart-axis" aria-hidden="true">
          <span>{axis.start}</span>
          <span>{axis.end}</span>
        </div>
      </>
    );
  }
  return (
    <div className="openpl-chart">
      <div className="openpl-chart-head">
        <span className="openpl-label">P/L fechado acumulado</span>
        <span className="openpl-chart-note">
          {label} · saída a saída, de todos os agentes
          {read && !read.complete && " · incompleto"}
        </span>
      </div>
      {body}
    </div>
  );
}

/**
 * The accounts of all the agents, whichever the dashboard shows: the profit
 * and the swap of the positions they hold open now, the profit their
 * positions closed in the period (picked by a double click on its label:
 * today, this week, this month or custom days) and the balances of their
 * accounts, as the broker reports them, by the currency of each account (none
 * converted: a total for each currency), each labelled, their values aligned;
 * the evolution of the closed profit in the period under them; apart, by
 * agent (beside them up to AGENTS_BESIDE on a wide card, under them across it
 * with more), a tile each with the profit open of each of its assets and its closed
 * one, tinted by the sign of its open profit (a stopped or disabled one marked
 * so); incomplete, and why, when an account wasn't read.
 */
export function OpenProfitCard({ theme }: { theme: Theme }) {
  const { read, error } = useOpenPositions();
  const dailyGuard = useDailyGuard();
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
      <div className={lines.length > AGENTS_BESIDE ? "openpl-layout is-crowded" : "openpl-layout"}>
        <div className="openpl-finance">
          <span className="eyebrow">Todos os agentes</span>
          <dl className="openpl-stats">
            <div className="openpl-stat openpl-open">
              <dt className="openpl-label" id="openpl-title">
                P/L em aberto
              </dt>
              <dd className="openpl-stat-value">
                {read ? (
                  <Amounts
                    amounts={read.totals}
                    complete={read.complete}
                    gaps={openPLGaps(read)}
                    signed
                    empty="Nenhuma conta de agente em live lida"
                    what="Em aberto"
                  />
                ) : error ? (
                  <span className="openpl-value" title="Não lido">
                    —
                  </span>
                ) : (
                  <Skeleton className="openpl-skeleton" />
                )}
              </dd>
              <dd className="openpl-summary">
                {read ? (
                  <>
                    {openPLSummary(read)} ·{" "}
                    <time
                      dateTime={new Date(read.at * 1000).toISOString()}
                      title="Lido da corretora neste horário (UTC)"
                    >
                      {readTime(read.at)} UTC
                    </time>
                  </>
                ) : (
                  error && (
                    <span className="openpl-note error">
                      Não foi possível ler as posições abertas: {error}
                    </span>
                  )
                )}
              </dd>
            </div>

            <div className="openpl-stat openpl-closed">
              <dt className="openpl-label">
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
                <span className="openpl-period-name">{label}</span>
              </dt>
              <dd className="openpl-stat-value">
                {closed.read ? (
                  <Amounts
                    amounts={closed.read.totals}
                    complete={closed.read.complete}
                    gaps={openPLGaps(closed.read)}
                    signed
                    empty="Nenhuma conta de agente em live lida"
                    what={`Fechado (${label})`}
                  />
                ) : closed.error ? (
                  <span className="openpl-value" title="Não lido">
                    —
                  </span>
                ) : (
                  <Skeleton className="openpl-skeleton" />
                )}
              </dd>
              <dd className="openpl-summary">
                {closed.read
                  ? closedPLSummary(closed.read)
                  : closed.error && (
                      <span className="openpl-note error">
                        Não foi possível ler o P/L fechado: {closed.error}
                      </span>
                    )}
              </dd>
            </div>

            <div className="openpl-stat openpl-balance">
              <dt className="openpl-label">Saldo</dt>
              <dd className="openpl-stat-value">
                {read ? (
                  <Amounts
                    amounts={read.balances}
                    complete={read.complete}
                    gaps={openPLGaps(read)}
                    signed={false}
                    empty="Nenhuma conta de agente em live lida"
                    what="Saldo"
                  />
                ) : error ? (
                  <span className="openpl-value" title="Não lido">
                    —
                  </span>
                ) : (
                  <Skeleton className="openpl-skeleton" />
                )}
              </dd>
              <dd className="openpl-summary">{read && accountsSummary(read)}</dd>
            </div>
          </dl>

          <DailyGuardStrip status={dailyGuard} />

          <ClosedEvolution
            read={closed.read}
            error={closed.error}
            range={range}
            label={label}
            theme={theme}
          />
        </div>

        {lines.length > 0 && (
          <>
            <hr className="openpl-separator" />
            <div className="openpl-people">
              <span className="openpl-label" id="openpl-agents-title">
                Por agente
              </span>
              <ul className="openpl-agents" aria-labelledby="openpl-agents-title">
                {lines.map((line) => (
                  <AgentTile
                    key={line.agent}
                    line={line}
                    open={read}
                    several={several}
                    label={label}
                  />
                ))}
              </ul>
            </div>
          </>
        )}
        {(read || closed.read) &&
          (gaps.length > 0 || (read && error) || (closed.read && closed.error)) && (
            <div className="openpl-notes">
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
            </div>
          )}
      </div>
      {picking && (
        <ClosedPeriodDialog period={period} onApply={apply} onClose={() => setPicking(false)} />
      )}
    </section>
  );
}
