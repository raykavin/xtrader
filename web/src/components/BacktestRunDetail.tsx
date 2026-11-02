import { useEffect, useMemo, useState } from "react";
import { fetchBacktestRun } from "../api/client";
import type { BacktestRunInfo } from "../api/types";
import {
  type BacktestTrade,
  callFor,
  type BacktestRunDetail as Detail,
  hoursAssumption,
  type RecordedCall,
  reasonLabels,
} from "../lib/backtest";
import {
  formatMoney,
  formatNumber,
  formatPrice,
  formatProfit,
  formatQuantity,
  LOCALE,
  signClass,
} from "../lib/format";
import { sideLabel } from "../lib/labels";
import { clampPage, pageRows } from "../lib/pagination";
import { formatDuration } from "../lib/performance";
import type { Theme } from "../lib/theme";
import { timeframeLabel } from "../lib/timeframes";
import { periodLabel, segmentLabel } from "./BacktestRuns";
import { Icon } from "./Icon";
import { Paginator } from "./Paginator";
import { EquityArea } from "./PerformanceCharts";
import { SectionHead } from "./SectionHead";
import { Alert, EmptyState, Loading, Skeleton } from "./States";
import { Tabs, tabIds } from "./Tabs";
import { TimeCell } from "./TimeCell";

type View = "equity" | "trades" | "open" | "warnings" | "config" | "llm";

interface Props {
  run: BacktestRunInfo;
  /** The other segment of the agent, the fit of a validation or the reverse. */
  other?: BacktestRunInfo;
  onSegment: (segment: string) => void;
  onClose: () => void;
  theme: Theme;
}

/** The reason of an entry or an exit in words. */
function reasonOf(reason: string): string {
  return reasonLabels[reason] ?? reason;
}

/**
 * A run of a batch: its equity, its trades with why they opened and closed
 * (the justification of the model for an LLM), the positions open at its
 * end, the warnings of its data, the config and the assumptions it ran
 * with, and the calls of its model.
 */
export function BacktestRunDetail({ run, other, onSegment, onClose, theme }: Props) {
  const [detail, setDetail] = useState<Detail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [view, setView] = useState<View>("equity");

  useEffect(() => {
    const controller = new AbortController();
    fetchBacktestRun(run.id, controller.signal)
      .then(setDetail)
      .catch((e) => {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      });
    return () => controller.abort();
  }, [run.id]);

  const report = detail?.report;
  const extra = report?.extra;
  const trades = report?.trades ?? [];
  const open = report?.open ?? [];
  const warnings = [...(report?.warnings ?? []), ...(detail?.config.notes ?? [])];
  const items: { id: View; label: string; count?: number }[] = [
    { id: "equity", label: "Curva de capital" },
    { id: "trades", label: "Operações", count: trades.length },
    { id: "open", label: "Abertas no fim", count: open.length },
    { id: "warnings", label: "Avisos", count: warnings.length },
    { id: "config", label: "Configuração" },
  ];
  if (extra) items.push({ id: "llm", label: "LLM", count: extra.calls });
  const ids = tabIds(`bt-run-${run.id}`, view);
  const s = run.summary;

  return (
    <section className="bt-detail" aria-labelledby={`bt-run-${run.id}-title`}>
      <SectionHead
        id={`bt-run-${run.id}-title`}
        eyebrow={`Execução #${run.id} · ${segmentLabel(run.segment)}`}
        title={run.agent}
        tag={periodLabel(run.from, run.to)}
        subtitle={
          s &&
          `Realizado ${formatProfit(s.net)} ${s.currency} · aberto no fim ${formatProfit(s.open_profit)} ${s.currency} (à parte) · patrimônio final ${formatMoney(s.final_equity)} ${s.currency}`
        }
        aside={
          <div className="bt-detail-actions">
            {other && (
              <button type="button" className="btn btn-sm" onClick={() => onSegment(other.segment)}>
                Ver {segmentLabel(other.segment).toLowerCase()}
              </button>
            )}
            <button
              type="button"
              className="btn btn-sm btn-ghost"
              onClick={onClose}
              aria-label="Fechar"
            >
              <Icon name="close" size={14} />
            </button>
          </div>
        }
      />
      {run.llm && (
        <Alert tone="info" title="Não determinístico">
          Um LLM decidiu as entradas: outra execução pode dar outro resultado, o LLM pode ter visto
          o período no treino e o contexto foi reduzido.
        </Alert>
      )}
      {error && (
        <Alert tone="error" title="Não foi possível carregar a execução">
          {error}
        </Alert>
      )}
      {!detail ? (
        !error && (
          <Loading label="Carregando a execução">
            <Skeleton height={240} />
          </Loading>
        )
      ) : (
        <>
          <Tabs
            label="Visualizações da execução"
            idPrefix={`bt-run-${run.id}`}
            items={items}
            value={view}
            onChange={setView}
          />
          <div id={ids.panel} role="tabpanel" aria-labelledby={ids.tab} className="bt-detail-panel">
            {!report ? (
              <EmptyState compact icon="results" title="Sem relatório">
                {run.error ?? "A execução não terminou."}
              </EmptyState>
            ) : view === "equity" ? (
              (report.equity ?? []).length < 2 ? (
                <EmptyState compact icon="chart" title="Sem curva de capital">
                  Poucos pontos no período.
                </EmptyState>
              ) : (
                <>
                  <EquityArea
                    points={report.equity ?? []}
                    theme={theme}
                    base={report.summary.initial_balance}
                  />
                  <p className="field-hint">
                    Patrimônio (realizado + aberto) a cada candle do timeframe do agente, em{" "}
                    {report.currency}; verde acima do saldo inicial, vermelho abaixo.{" "}
                    {report.summary.protection_moves > 0 &&
                      `${report.summary.protection_moves} movimento(s) de breakeven/trailing. `}
                    {report.summary.refused > 0 &&
                      `${report.summary.refused} ordem(ns) recusada(s) por volume abaixo do mínimo.`}
                  </p>
                </>
              )
            ) : view === "trades" ? (
              <Trades trades={trades} calls={extra?.recorded ?? []} currency={report.currency} />
            ) : view === "open" ? (
              open.length === 0 ? (
                <EmptyState compact icon="results" title="Nenhuma posição aberta no fim" />
              ) : (
                <div className="table-wrap">
                  <table className="table table-responsive">
                    <thead>
                      <tr>
                        <th scope="col">Ativo</th>
                        <th scope="col">Lado</th>
                        <th scope="col">Abertura (UTC)</th>
                        <th scope="col" className="num">
                          Quantidade
                        </th>
                        <th scope="col" className="num">
                          Entrada
                        </th>
                        <th scope="col" className="num">
                          Avaliação
                        </th>
                        <th scope="col" className="num">
                          Resultado ({report.currency})
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {open.map((p) => (
                        <tr key={`${p.pair}-${p.opened_at}`}>
                          <td className="strong" data-label="Ativo">
                            {p.pair}
                          </td>
                          <td data-label="Lado">
                            <span className={`side-chip ${p.side === "BUY" ? "buy" : "sell"}`}>
                              {sideLabel(p.side)}
                            </span>
                          </td>
                          <td className="mono" data-label="Abertura (UTC)">
                            <TimeCell unix={p.opened_at} />
                          </td>
                          <td className="num mono" data-label="Quantidade">
                            {formatQuantity(p.quantity)}
                          </td>
                          <td className="num mono" data-label="Entrada">
                            {formatPrice(p.entry)}
                          </td>
                          <td className="num mono" data-label="Avaliação">
                            {formatPrice(p.mark)}
                          </td>
                          <td className={`num mono ${signClass(p.profit)}`} data-label="Resultado">
                            {formatProfit(p.profit)}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )
            ) : view === "warnings" ? (
              warnings.length === 0 ? (
                <EmptyState compact icon="success" title="Nenhum aviso" />
              ) : (
                <ul className="bt-warnings">
                  {warnings.map((w) => (
                    <li key={w}>{w}</li>
                  ))}
                </ul>
              )
            ) : view === "config" ? (
              <Config detail={detail} />
            ) : (
              extra && <Calls extra={extra} />
            )}
          </div>
        </>
      )}
    </section>
  );
}

/** The trades of a run, the latest first, with why they opened and closed. */
function Trades({
  trades,
  calls,
  currency,
}: {
  trades: BacktestTrade[];
  calls: RecordedCall[];
  currency: string;
}) {
  const listed = useMemo(() => [...trades].reverse(), [trades]);
  const [page, setPage] = useState(0);
  if (trades.length === 0) {
    return <EmptyState compact icon="list" title="Nenhuma operação fechada" />;
  }
  const visible = pageRows(listed, clampPage(page, listed.length));
  return (
    <>
      <div className="table-wrap">
        <table className="table table-responsive bt-trades">
          <thead>
            <tr>
              <th scope="col">Ativo</th>
              <th scope="col">Lado</th>
              <th scope="col">Abertura (UTC)</th>
              <th scope="col">Duração</th>
              <th scope="col" className="num">
                Qtd.
              </th>
              <th scope="col" className="num">
                Entrada
              </th>
              <th scope="col" className="num">
                Saída
              </th>
              <th scope="col" className="num">
                Custos
              </th>
              <th scope="col" className="num">
                Líquido ({currency})
              </th>
              <th scope="col">Entrada por</th>
              <th scope="col">Saída por</th>
            </tr>
          </thead>
          <tbody>
            {visible.map((t) => {
              const call = calls.length > 0 ? callFor(t, calls) : undefined;
              return (
                <tr key={`${t.pair}-${t.opened_at}-${t.closed_at}-${t.entry}`}>
                  <td className="strong" data-label="Ativo">
                    {t.pair}
                  </td>
                  <td data-label="Lado">
                    <span className={`side-chip ${t.side === "BUY" ? "buy" : "sell"}`}>
                      {sideLabel(t.side)}
                    </span>
                  </td>
                  <td className="mono" data-label="Abertura (UTC)">
                    <TimeCell unix={t.opened_at} />
                  </td>
                  <td className="mono" data-label="Duração">
                    {formatDuration(t.closed_at - t.opened_at)}
                  </td>
                  <td className="num mono" data-label="Qtd.">
                    {formatQuantity(t.quantity)}
                  </td>
                  <td className="num mono" data-label="Entrada">
                    {formatPrice(t.entry)}
                  </td>
                  <td className="num mono" data-label="Saída">
                    {formatPrice(t.exit)}
                  </td>
                  <td className="num mono" data-label="Custos">
                    {t.fees ? formatMoney(-t.fees) : "—"}
                  </td>
                  <td className={`num mono strong ${signClass(t.net)}`} data-label="Líquido">
                    {formatProfit(t.net)}
                  </td>
                  <td data-label="Entrada por">
                    {reasonOf(t.entry_reason)}
                    {call?.reason && <small className="bt-reason">{call.reason}</small>}
                  </td>
                  <td data-label="Saída por">{reasonOf(t.exit_reason)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <Paginator page={clampPage(page, listed.length)} total={listed.length} onPage={setPage} />
    </>
  );
}

/** The config a run ran with, copied, and the assumptions of its batch. */
function Config({ detail }: { detail: Detail }) {
  const c = detail.config;
  const a = c.assumptions;
  const p = c.protection;
  const report = detail.report;
  const execution = detail.summary?.execution_timeframe || a.execution_timeframe;
  return (
    <ul className="kv-list">
      <li>
        <span>Agente</span>
        <span>
          {c.agent} ({c.kind}
          {c.strategy ? ` · ${c.strategy}` : ""}), em {c.mode} quando testado
        </span>
      </li>
      <li>
        <span>Ativos e timeframe</span>
        <span>
          {c.pairs.join(", ")} · {c.timeframe ? timeframeLabel(c.timeframe) : "o da estratégia"}
        </span>
      </li>
      {report && (
        <li>
          <span>Dados</span>
          <span>
            {formatNumber(report.candles)} candles de {timeframeLabel(execution)}
            {report.data_from && report.data_to
              ? `, de ${periodLabel(report.data_from, report.data_to + 1)}`
              : ""}
            , da bridge do agente
          </span>
        </li>
      )}
      <li>
        <span>Dinheiro e custos</span>
        <span>
          {formatMoney(a.balance)} {a.currency} · taxa {formatNumber(a.fee * 100)}% · spread de
          reserva {formatNumber(a.spread_points)} pts · slippage {formatNumber(a.slippage_points)}{" "}
          pts
        </span>
      </li>
      <li>
        <span>Execução</span>
        <span>
          {timeframeLabel(execution)} · stop com gap na abertura · stop antes do take profit no
          mesmo candle · posições abertas no fim a mercado, à parte
        </span>
      </li>
      <li>
        <span>Limites diários</span>
        <span>
          {a.daily_loss || a.daily_gain
            ? `perda ${a.daily_loss ? formatMoney(a.daily_loss) : "—"} · ganho ${a.daily_gain ? formatMoney(a.daily_gain) : "—"} ${a.currency}`
            : "nenhum"}
        </span>
      </li>
      <li>
        <span>Horário</span>
        <span>{a.hours ? hoursAssumption(a.hours) : "o dia todo"}</span>
      </li>
      <li>
        <span>Proteção</span>
        <span>
          {!p || (!p.breakeven.enabled && !p.trailing.enabled)
            ? "nenhuma"
            : [
                p.breakeven.enabled &&
                  `breakeven a ${formatNumber(p.breakeven.trigger)} ${p.unit} (+${formatNumber(p.breakeven.margin)})`,
                p.trailing.enabled && `trailing a ${formatNumber(p.trailing.distance)} ${p.unit}`,
              ]
                .filter(Boolean)
                .join(" · ")}
        </span>
      </li>
      {c.llm && (
        <li>
          <span>Modelo</span>
          <span>
            {c.llm.provider} {c.llm.model} · teto {c.llm.max_calls} chamadas /{" "}
            {formatMoney(c.llm.max_cost)} USD
            {c.llm.prices
              ? ` · ${formatNumber(c.llm.prices.input)}/${formatNumber(c.llm.prices.output)} USD por 1M tokens`
              : " · sem preço configurado: custo não estimado"}
          </span>
        </li>
      )}
      {c.prompt && (
        <li>
          <span>Prompt</span>
          <span>
            {c.prompt.name} v{c.prompt.version} <small className="mono">{c.prompt.sha256}</small>
          </span>
        </li>
      )}
      <li>
        <span>Versão do código</span>
        <span className="mono">{c.code}</span>
      </li>
      <li>
        <span>Parâmetros</span>
        <span className="mono bt-args">{c.args.join(" ")}</span>
      </li>
    </ul>
  );
}

/** The calls of the model of a run: their totals, then each, the latest first. */
function Calls({ extra }: { extra: NonNullable<NonNullable<Detail["report"]>["extra"]> }) {
  const listed = useMemo(() => [...extra.recorded].reverse(), [extra.recorded]);
  const [page, setPage] = useState(0);
  const visible = pageRows(listed, clampPage(page, listed.length));
  return (
    <>
      <p className="panel-summary">
        <strong>{extra.model}</strong> · {extra.calls} chamada(s) de até {extra.max_calls} ·{" "}
        {formatNumber(extra.input_tokens)} tokens de entrada, {formatNumber(extra.output_tokens)} de
        saída ·{" "}
        {extra.cost === undefined
          ? "custo não estimado (sem preço do modelo)"
          : `~${formatMoney(extra.cost)} USD de até ${formatMoney(extra.max_cost)}`}
        {extra.exhausted && " · teto atingido: a execução parou antes do fim"}
      </p>
      {extra.context.length > 0 && (
        <p className="field-hint">Contexto reduzido: {extra.context.join("; ")}.</p>
      )}
      <div className="table-wrap">
        <table className="table table-responsive bt-calls">
          <thead>
            <tr>
              <th scope="col">Instante simulado (UTC)</th>
              <th scope="col">Ativo</th>
              <th scope="col">Decisão</th>
              <th scope="col">Justificativa</th>
              <th scope="col" className="num">
                Tokens
              </th>
              <th scope="col" className="num">
                Custo (USD)
              </th>
            </tr>
          </thead>
          <tbody>
            {visible.map((call) => (
              <tr key={`${call.at}-${call.pair}-${call.input_sha256}`}>
                <td className="mono" data-label="Instante">
                  <TimeCell unix={call.at} />
                </td>
                <td data-label="Ativo">{call.pair}</td>
                <td data-label="Decisão">
                  {call.error ? (
                    <span className="msg-error">erro</span>
                  ) : (
                    `${call.action ?? "—"}${call.confidence ? ` (${formatNumber(Math.round(call.confidence * 100))}%)` : ""}`
                  )}
                </td>
                <td data-label="Justificativa" className="bt-reason-cell" title={call.input_sha256}>
                  {call.error ?? call.reason ?? "—"}
                </td>
                <td className="num mono" data-label="Tokens">
                  {formatNumber(call.input_tokens + call.output_tokens)}
                </td>
                <td className="num mono" data-label="Custo">
                  {call.cost
                    ? call.cost.toLocaleString(LOCALE, {
                        minimumFractionDigits: 4,
                        maximumFractionDigits: 4,
                      })
                    : "—"}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <Paginator page={clampPage(page, listed.length)} total={listed.length} onPage={setPage} />
    </>
  );
}
