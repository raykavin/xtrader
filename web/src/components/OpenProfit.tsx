import { type ReactNode, useEffect, useRef, useState } from "react";
import { closeAgentPositions, fetchClosedProfit } from "../api/client";
import type {
  AgentStatus,
  ClosedProfitResponse,
  CurrencyAmount,
  OpenPositionsResponse,
} from "../api/types";
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
import { agentPairHref } from "../lib/nav";
import {
  type AgentLine,
  accountsSummary,
  agentLines,
  agentOpenState,
  amountsText,
  assetLabel,
  directionOf,
  type OpenState,
  openPLGaps,
  openPLSummary,
  profitTone,
  severalCurrencies,
  tileAssets,
} from "../lib/openPL";
import { type CloseOutcome, closeOutcome } from "../lib/supervisor";
import { palettes, type Theme } from "../lib/theme";
import { ClosedPeriodDialog } from "./ClosedPeriodDialog";
import { Icon } from "./Icon";
import { Sparkline } from "./Sparkline";
import { Skeleton } from "./States";
import { useOpenPositions } from "./useOpenPositions";

/**
 * The agents a wide card shows beside its figures; with more, they go under
 * the figures and the chart, across the card, more of them a row.
 */
const AGENTS_BESIDE = 8;

/** How often the closed profit of the period is read again, in ms: it changes as a position closes. */
const CLOSED_PL_REFRESH = 15000;

/**
 * The last read of the closed profit, of all the agents: kept as the dashboard
 * switches agents, shown at once by the next one while it reads again (the
 * open positions are kept by useOpenPositions).
 */
let lastClosed: ClosedProfitResponse | null = null;

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

/**
 * The close of every open position of an agent, from its tile: at once, no
 * confirmation asked, its button disabled until it ends (a click more does
 * nothing); its outcome, checked on the account, told a while, what remains
 * listed.
 */
function useCloseAgent(agent: string) {
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<CloseOutcome | null>(null);
  const running = useRef(false);
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);

  const tell = (next: CloseOutcome) => {
    setFeedback(next);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(
      () => setFeedback(null),
      next.tone === "ok" ? 10_000 : 30_000,
    );
  };
  const close = () => {
    if (running.current) return; // a click more while it runs
    running.current = true;
    setBusy(true);
    setFeedback(null);
    closeAgentPositions(agent)
      .then(({ action }) => tell(closeOutcome(action)))
      .catch((e) =>
        tell({ tone: "error", text: e instanceof Error ? e.message : String(e), remaining: [] }),
      )
      .finally(() => {
        running.current = false;
        setBusy(false);
      });
  };
  return { busy, feedback, close };
}

/** The agent and the pair the dashboard shows. */
export interface ShownPair {
  agent: string;
  pair: string | null;
}

/**
 * The tile of an agent: its icon, its name, its assets (the pairs it trades,
 * each with the profit open of its positions) and its closed profit, its
 * background by the sign of its open profit. An agent of the dashboard
 * (pairs given) has each of its assets a link to it on that pair, shown at
 * once in the page (see onShow); a link opened apart, eg: in a new tab, as
 * any link. One of an agent off the dashboard, eg: disabled, isn't.
 */
function AgentTile({
  line,
  open,
  several,
  label,
  pairs,
  shown,
  onShow,
}: {
  line: AgentLine;
  open: OpenPositionsResponse | null;
  several: boolean;
  label: string;
  /** The pairs of the agent, undefined for one off the dashboard. */
  pairs: string[] | undefined;
  shown: ShownPair;
  onShow: (agent: string, pair: string) => void;
}) {
  const state = agentOpenState(line, open);
  const note = openNotes[state];
  const closing = useCloseAgent(line.agent);
  const positions = line.positions === 1 ? "1 posição" : `${line.positions} posições`;
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
        <span className="openpl-agent-end">
          {line.assets.length > 1 && (
            <span
              className={`openpl-agent-total mono ${profitClass(line.open)}`}
              title="Em aberto, a soma dos ativos"
            >
              <span className="sr-only">Em aberto, a soma dos ativos: </span>
              {amountsText(line.open, several)}
            </span>
          )}
          {(line.positions > 0 || closing.busy) && (
            <button
              type="button"
              className={`openpl-close${closing.busy ? " is-busy" : ""}`}
              disabled={closing.busy}
              aria-busy={closing.busy}
              title={
                closing.busy
                  ? `Encerrando as posições de ${line.agent}…`
                  : `Encerrar já ${line.positions === 1 ? "a posição" : `as ${positions}`} de ${line.agent}, a mercado`
              }
              aria-label={
                closing.busy
                  ? `Encerrando as posições de ${line.agent}`
                  : `Encerrar as posições de ${line.agent}`
              }
              onClick={closing.close}
            >
              <Icon name={closing.busy ? "restart" : "close"} size={12} />
            </button>
          )}
        </span>
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
      {closing.feedback && (
        <div className={`openpl-close-note ${closing.feedback.tone}`} role="status">
          <p>{closing.feedback.text}</p>
          {closing.feedback.remaining.length > 0 && (
            <ul>
              {closing.feedback.remaining.map((text) => (
                <li key={text}>{text}</li>
              ))}
            </ul>
          )}
        </div>
      )}
      <dl className="openpl-assets">
        {tileAssets(line, pairs).map(({ pair, open: asset }) => {
          const direction = asset && directionOf(asset.side);
          const current = shown.agent === line.agent && shown.pair === pair;
          return (
            <div
              key={`${pair}/${asset?.side ?? ""}`}
              className={asset ? "openpl-asset" : "openpl-asset is-idle"}
              title={asset ? assetLabel(asset) : `${pair}: sem posição aberta`}
            >
              <dt>
                {asset && direction && (
                  <span
                    className={`openpl-side is-${direction.icon}`}
                    role="img"
                    aria-label={assetLabel(asset)}
                  >
                    <Icon name={direction.icon} size={11} />
                  </span>
                )}
                <span>
                  {pairs ? (
                    <a
                      className="openpl-pair"
                      href={agentPairHref(line.agent, pair, window.location.search)}
                      aria-current={current ? "page" : undefined}
                      aria-label={`Mostrar ${line.agent} em ${pair}`}
                      title={
                        current
                          ? `${line.agent} em ${pair}, mostrado`
                          : `Mostrar ${line.agent} em ${pair}`
                      }
                      onClick={(e) => {
                        // opened apart (a new tab, a window) as any link
                        if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey)
                          return;
                        e.preventDefault();
                        onShow(line.agent, pair);
                      }}
                    >
                      {pair}
                    </a>
                  ) : (
                    pair
                  )}
                  {asset && <span aria-hidden="true">:</span>}
                </span>
              </dt>
              {asset ? (
                <dd className={`openpl-profit mono ${profitClass(asset.amounts)}`}>
                  {amountsText(asset.amounts, several)}
                </dd>
              ) : (
                <dd className="openpl-profit" />
              )}
            </div>
          );
        })}
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
 * The accumulated profit of all the agents in the period, as its card tells
 * it: the closed one, the running total from 0 at the start of the period,
 * exit after exit, held until now, a plain line in steps along the time of the
 * period like the line of the quote card; a chart for each currency, none
 * converted. In a period not over, with positions open, the line goes on,
 * dashed, to the closed total plus the profit open now (see closedCharts),
 * in real time: drawn again each time the open positions are read. Its head
 * tells the two lines apart. While the period is read, a placeholder; without
 * an exit nor a position open, or on a failure, said so: never the read of
 * another period.
 */
function ClosedEvolution({
  read,
  error,
  open,
  range,
  label,
  theme,
}: {
  read: ClosedProfitResponse | null;
  error: string | null;
  open: OpenPositionsResponse | null;
  range: ClosedRange;
  label: string;
  theme: Theme;
}) {
  const now = Math.floor(Date.now() / 1000);
  const charts = read ? closedCharts(read, now, open?.totals) : [];
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
          const colorOf = (amount: number) => {
            const tone = profitTone([{ currency: chart.currency, amount }]);
            return tone === "pos" ? palette.up : tone === "neg" ? palette.down : palette.muted;
          };
          const first = chart.points[0]?.time ?? range.from;
          const end = chart.live?.time ?? chart.points[chart.points.length - 1]?.time ?? first;
          // room after now: the jump to the profit open, and its dot, inside the plot
          const last = chart.live ? end + Math.max(60, (end - first) * 0.04) : end;
          const exits = chart.exits === 1 ? "1 saída" : `${chart.exits} saídas`;
          const now =
            chart.live && chart.open !== undefined
              ? `; agora ${formatProfit(chart.live.value)} ${chart.currency} com o P/L aberto (${formatProfit(chart.open)})`
              : "";
          return (
            <figure
              key={chart.currency}
              className="openpl-chart-plot"
              aria-label={`P/L acumulado, ${label}: fechado de 0 a ${formatProfit(chart.total)} ${chart.currency} em ${exits}${now}`}
            >
              <Sparkline
                points={chart.points}
                color={colorOf(chart.total)}
                width={400}
                height={72}
                domain={[first, last]}
                step
                zero
                live={chart.live}
                liveColor={chart.live ? colorOf(chart.live.value) : undefined}
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
        <span className="openpl-label">P/L acumulado</span>
        <span className="openpl-chart-key">
          (<i className="openpl-chart-key-line is-open" aria-hidden="true" />
          aberto + <i className="openpl-chart-key-line" aria-hidden="true" />
          fechado)
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
 * past a hairline, the accumulated profit of the period under them (see
 * ClosedEvolution); apart, by agent (beside them up to AGENTS_BESIDE on a
 * wide card, under them across it with more), a tile each with its assets,
 * the profit open of each, a link each to the agent on it, and its closed
 * one, tinted by the sign of its open profit (a stopped or disabled one marked
 * so); incomplete, and why, when an account wasn't read.
 */
export function OpenProfitCard({
  theme,
  agents,
  shown,
  onShow,
}: {
  theme: Theme;
  /** The agents of the dashboard, with their pairs: the ones a tile links to. */
  agents: AgentStatus[];
  /** The agent and the pair shown, their link marked. */
  shown: ShownPair;
  /** Shows an agent on one of its pairs. */
  onShow: (agent: string, pair: string) => void;
}) {
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
  const pairsOf = new Map(agents.map((a) => [a.name, a.pairs ?? []]));

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

          <hr className="openpl-divider" />

          <ClosedEvolution
            read={closed.read}
            error={closed.error}
            open={read}
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
                    pairs={pairsOf.get(line.agent)}
                    shown={shown}
                    onShow={onShow}
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
