import { useEffect, useMemo, useRef, useState } from "react";
import { fetchAgentPeriods, fetchPairs, fetchSnapshot, resetAgentResults } from "../api/client";
import type { AgentPeriod, AgentSettings, Snapshot } from "../api/types";
import { formatMoney, formatNumber, formatPercent, formatTime, signClass } from "../lib/format";
import { type PeriodResult, parseAmount, periodResults, resetPeriods } from "../lib/periods";
import { ConfirmButton } from "./ConfirmButton";
import { DialogHeader } from "./Dialog";
import { Alert, EmptyState, Loading, Skeleton } from "./States";

interface Props {
  agent: AgentSettings;
  onClose: () => void;
}

/** The profit factor shown: ∞ without losses, — without trades. */
function factorLabel(trades: number, factor: number): string {
  if (trades === 0) return "—";
  return Number.isFinite(factor) ? formatNumber(factor) : "∞";
}

/** The window of a result: since its start, between its start and its end, or before the first reset. */
function windowLabel(result: PeriodResult, current: boolean): string {
  const { period } = result;
  if (!period) {
    return result.until === undefined
      ? "Desde o início: os resultados nunca foram redefinidos"
      : `Até ${formatTime(result.until)} UTC, antes da primeira redefinição`;
  }
  const by = period.created_by ? ` · por ${period.created_by}` : "";
  if (current || period.ended_at === undefined) {
    return `Desde ${formatTime(period.started_at)} UTC${by}`;
  }
  return `${formatTime(period.started_at)} → ${formatTime(period.ended_at)} UTC${by}`;
}

/** The pairs of a result, with their total when there are several. */
function ResultTable({ result }: { result: PeriodResult }) {
  if (result.pairs.length === 0) {
    return <EmptyState compact icon="results" title="Nenhuma operação fechada no período" />;
  }
  return (
    <div className="table-wrap">
      <table className="table table-compact">
        <thead>
          <tr>
            <th scope="col">Ativo</th>
            <th scope="col" className="num">
              Operações
            </th>
            <th scope="col" className="num">
              Acerto
            </th>
            <th scope="col" className="num">
              Lucro líquido
            </th>
            <th scope="col" className="num">
              Fator
            </th>
          </tr>
        </thead>
        <tbody>
          {result.pairs.map(({ pair, stats }) => (
            <tr key={pair}>
              <td className="mono">{pair}</td>
              <td className="num mono" title={`${stats.wins} ganhas · ${stats.losses} perdidas`}>
                {stats.trades}
              </td>
              <td className="num mono">{formatPercent(stats.winRate).replace("+", "")}</td>
              <td className={`num mono ${signClass(stats.totalProfit)}`}>
                {formatMoney(stats.totalProfit)} <small>{stats.profitCurrency}</small>
              </td>
              <td className="num mono">{factorLabel(stats.trades, stats.profitFactor)}</td>
            </tr>
          ))}
        </tbody>
        {result.pairs.length > 1 && (
          <tfoot>
            <tr>
              <th scope="row">Total</th>
              <td className="num mono" title={`${result.wins} ganhas · ${result.losses} perdidas`}>
                {result.trades}
              </td>
              <td className="num mono">
                {formatPercent(result.wins / result.trades).replace("+", "")}
              </td>
              <td className={`num mono ${signClass(result.profit ?? 0)}`}>
                {result.profit === null ? (
                  "moedas diferentes"
                ) : (
                  <>
                    {formatMoney(result.profit)} <small>{result.currency}</small>
                  </>
                )}
              </td>
              <td className="num mono">{factorLabel(result.trades, result.profitFactor)}</td>
            </tr>
          </tfoot>
        )}
      </table>
    </div>
  );
}

/** A period: its window, its deposit, its result and its balance, and its pairs. */
function PeriodSection({
  result,
  current,
  known,
}: {
  result: PeriodResult;
  current: boolean;
  /** Whether its results are known: the agent answered with its orders. */
  known: boolean;
}) {
  const { period } = result;
  return (
    <section className="results-period">
      <p className="results-window">{windowLabel(result, current)}</p>
      <dl className="meta-list">
        {period && (
          <>
            <dt>Saldo inicial</dt>
            <dd className="mono">
              {formatMoney(period.deposit)} {period.currency}
            </dd>
          </>
        )}
        {known && (
          <>
            <dt>Resultado</dt>
            <dd className={`mono ${signClass(result.profit ?? 0)}`}>
              {result.profit === null
                ? "em moedas diferentes, por ativo abaixo"
                : `${formatMoney(result.profit)} ${result.currency}`}
            </dd>
            {result.balance !== null && period && (
              <>
                <dt>{current ? "Saldo atual" : "Saldo final"}</dt>
                <dd className="mono">
                  {formatMoney(result.balance)} {period.currency}
                </dd>
              </>
            )}
          </>
        )}
      </dl>
      {known && <ResultTable result={result} />}
    </section>
  );
}

/**
 * The results of an agent by accounting period, and their reset: the cards
 * of the dashboard (trades, win rate, net profit, profit factor) of every pair
 * count, from then on, the trades closed after it, the deposit the balance the
 * new period starts with. The earlier periods are kept with their results; the
 * orders, the open positions and the pending orders don't change. The results
 * come from the orders the agent holds: unknown while it is offline.
 */
export function AgentResultsDialog({ agent, onClose }: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [periods, setPeriods] = useState<AgentPeriod[] | null>(null);
  const [snapshots, setSnapshots] = useState<Snapshot[] | null>(null);
  const [offline, setOffline] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [deposit, setDeposit] = useState("");
  const [currency, setCurrency] = useState("");
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<string | null>(null);
  const [stale, setStale] = useState<string | null>(null);

  useEffect(() => {
    const element = dialog.current;
    if (element && !element.open) element.showModal();
  }, []);

  // the periods of the controller, and the orders of the agent by pair, which
  // the results are counted from as the cards of the dashboard count them
  useEffect(() => {
    const controller = new AbortController();
    const { signal } = controller;
    fetchAgentPeriods(agent.name, signal)
      .then(setPeriods)
      .catch((e: unknown) => {
        if (!signal.aborted) setError(e instanceof Error ? e.message : String(e));
      });
    fetchPairs(agent.name, signal)
      .then((pairs) => Promise.all(pairs.map((pair) => fetchSnapshot(agent.name, pair, signal))))
      .then(setSnapshots)
      .catch(() => {
        if (!signal.aborted) {
          setOffline("O agente não respondeu: os resultados aparecem com ele executando.");
        }
      });
    return () => controller.abort();
  }, [agent.name]);

  // the currency of the deposit: the one of the current period, else the one
  // the agent counts its profits in (the account's, eg: BRL on MT5)
  useEffect(() => {
    if (currency !== "") return;
    const known =
      periods?.[0]?.currency ??
      snapshots?.find((s) => s.account_currency)?.account_currency ??
      snapshots?.[0]?.quote;
    if (known) setCurrency(known);
  }, [periods, snapshots, currency]);

  const results = useMemo(
    () => (periods ? periodResults(periods, snapshots ?? []) : null),
    [periods, snapshots],
  );
  const amount = parseAmount(deposit);
  const code = currency.trim().toUpperCase();
  const invalid =
    amount === null
      ? "Informe o depósito: um valor a partir de 0, por exemplo 1.000,00."
      : /^[A-Z]{3}$/.test(code)
        ? null
        : "Informe a moeda: um código de 3 letras, por exemplo BRL.";

  const close = () => dialog.current?.close();

  const reset = async () => {
    if (amount === null || invalid) return;
    setBusy(true);
    setError(null);
    setFeedback(null);
    setStale(null);
    try {
      const outcome = await resetPeriods(
        periods,
        () => resetAgentResults(agent.name, { deposit: amount, currency: code }),
        () => fetchAgentPeriods(agent.name),
      );
      setPeriods(outcome.periods);
      setDeposit("");
      setFeedback(
        `Resultados redefinidos às ${formatTime(outcome.period.started_at)} UTC: os cards contam as operações fechadas a partir de agora.`,
      );
      if (outcome.stale !== null) {
        setStale(
          `A redefinição foi feita, mas a lista de períodos não pôde ser atualizada (${outcome.stale}): não a repita; feche e abra de novo para conferir.`,
        );
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const [current, ...earlier] = results ?? [];
  const known = snapshots !== null;

  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc closes the dialog natively
    <dialog
      ref={dialog}
      className="dialog dialog-md"
      aria-labelledby="results-title"
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current && !busy) close(); // the backdrop
      }}
    >
      <DialogHeader
        id="results-title"
        title="Resultados"
        subtitle={agent.name}
        onClose={close}
        disabled={busy}
      />
      <div className="dialog-body">
        {error && (
          <Alert tone="error" title="Não foi possível carregar os períodos">
            {error}
          </Alert>
        )}
        {offline && <Alert tone="warn">{offline}</Alert>}
        {!results && !error && (
          <Loading label="Carregando os resultados…">
            <Skeleton height={140} />
          </Loading>
        )}

        {current && (
          <section className="dialog-section">
            <h3 className="section-title">Período atual</h3>
            <PeriodSection result={current} current known={known} />
          </section>
        )}

        <section className="dialog-section results-reset" aria-labelledby="results-reset-title">
          <h3 id="results-reset-title" className="section-title">
            Redefinir resultados
          </h3>
          <p className="field-hint">
            Zera Operações, Taxa de acerto, Lucro líquido e Fator de lucro de todos os ativos: os
            cards passam a contar só as operações fechadas a partir de agora. O período atual fica
            guardado com os resultados dele. As ordens continuam no histórico, e as posições abertas
            e as ordens pendentes não mudam; uma posição aberta conta no novo período quando fechar.
          </p>
          <div className="results-fields">
            <label className="field">
              <span className="field-label">Depósito (saldo inicial)</span>
              <input
                className="input mono"
                inputMode="decimal"
                placeholder="1.000,00"
                value={deposit}
                disabled={busy}
                aria-invalid={deposit !== "" && amount === null}
                onChange={(e) => setDeposit(e.target.value)}
              />
            </label>
            <label className="field results-currency">
              <span className="field-label">Moeda</span>
              <input
                className="input mono"
                maxLength={3}
                placeholder="BRL"
                value={currency}
                disabled={busy}
                aria-invalid={currency !== "" && !/^[A-Z]{3}$/.test(code)}
                onChange={(e) => setCurrency(e.target.value.toUpperCase())}
              />
            </label>
            <ConfirmButton
              label="Redefinir resultados"
              question="Redefinir os resultados de todos os ativos?"
              disabled={busy || invalid !== null}
              title={invalid ?? "Começa um novo período com o depósito informado"}
              onConfirm={reset}
            />
          </div>
          {feedback && <Alert tone="success">{feedback}</Alert>}
          {stale && <Alert tone="warn">{stale}</Alert>}
        </section>

        {earlier.length > 0 && (
          <section className="dialog-section">
            <h3 className="section-title">Períodos anteriores</h3>
            {earlier.map((result) => (
              <PeriodSection
                key={result.period?.id ?? "before"}
                result={result}
                current={false}
                known={known}
              />
            ))}
          </section>
        )}
      </div>
    </dialog>
  );
}
