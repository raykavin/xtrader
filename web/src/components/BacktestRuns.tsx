import type { BacktestBatchInfo, BacktestRunInfo } from "../api/types";
import { active, progressOf, runStatusLabels } from "../lib/backtest";
import { formatPercent, formatTime } from "../lib/format";
import { formatDuration } from "../lib/performance";
import { ConfirmButton } from "./ConfirmButton";
import { Icon } from "./Icon";
import { EmptyState } from "./States";
import { toneClass } from "./SupervisorAction";

interface Props {
  batches: BacktestBatchInfo[];
  /** The batch just queued, marked. */
  highlight: number | null;
  /** Cancels a run; none to a user who may not manage the backtests. */
  onCancel?: (run: number) => void;
  /** Deletes a batch; none to a user who may not manage the backtests. */
  onDelete?: (batch: number) => void;
  /** Shows the results of a batch. */
  onOpen: (batch: number) => void;
  /** Configures a new batch; none to a user who may not manage the backtests. */
  onNew?: () => void;
}

/** The day of a Unix time in the time of the browser, as the period was asked, eg: 01/10/2026. */
function dayLabel(unix: number): string {
  return new Date(unix * 1000).toLocaleDateString("pt-BR");
}

/** The period of a batch or a run, in days: its end is exclusive. */
export function periodLabel(from: number, to: number): string {
  return `${dayLabel(from)} a ${dayLabel(to - 1)}`;
}

/** The segment of a run in words. */
export function segmentLabel(segment: string): string {
  return segment === "validation" ? "Validação" : "Ajuste";
}

/**
 * The batches of backtests, the newest first, each with its runs: their
 * status, their progress, how long they ran, their error; a run queued or
 * running canceled, a batch ended deleted, its results opened.
 */
export function BacktestRuns({ batches, highlight, onCancel, onDelete, onOpen, onNew }: Props) {
  if (batches.length === 0) {
    return (
      <EmptyState
        compact
        icon="trend"
        title="Nenhum backtest ainda"
        action={
          onNew && (
            <button type="button" className="btn btn-sm" onClick={onNew}>
              Configurar um lote
            </button>
          )
        }
      />
    );
  }
  return (
    <div className="panel-body bt-batches">
      {batches.map((batch) => {
        const busy = batch.runs.some(active);
        return (
          <article
            key={batch.id}
            className="bt-batch"
            data-highlight={batch.id === highlight ? "" : undefined}
            aria-label={batch.name || `Lote ${batch.id}`}
          >
            <header className="bt-batch-head">
              <div className="bt-batch-title">
                <strong>{batch.name || `Lote #${batch.id}`}</strong>
                <small>
                  {periodLabel(batch.from, batch.to)}
                  {batch.validation_share > 0 &&
                    ` · validação ${formatPercent(batch.validation_share, 0).replace("+", "")}`}
                  {` · ${batch.assumptions.balance.toLocaleString("pt-BR")} ${batch.assumptions.currency}`}
                  {` · por ${batch.created_by} em ${formatTime(batch.created_at)} UTC`}
                </small>
              </div>
              <div className="bt-batch-actions">
                <button type="button" className="btn btn-sm" onClick={() => onOpen(batch.id)}>
                  <Icon name="results" size={14} />
                  Resultados
                </button>
                {onDelete && (
                  <ConfirmButton
                    label="Excluir"
                    icon="trash"
                    question="Excluir o lote e seus resultados?"
                    ariaLabel={`Excluir o lote ${batch.name || batch.id}`}
                    disabled={busy}
                    title={busy ? "Cancele as execuções em andamento para excluir" : undefined}
                    onConfirm={() => onDelete(batch.id)}
                  />
                )}
              </div>
            </header>
            <div className="table-wrap">
              <table className="table table-responsive bt-runs">
                <thead>
                  <tr>
                    <th scope="col">Agente</th>
                    <th scope="col">Segmento</th>
                    <th scope="col">Período</th>
                    <th scope="col">Status</th>
                    <th scope="col">Progresso</th>
                    <th scope="col">Duração</th>
                    <th scope="col">
                      <span className="sr-only">Ações</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {batch.runs.map((run) => (
                    <RunRow key={run.id} run={run} onCancel={onCancel} />
                  ))}
                </tbody>
              </table>
            </div>
          </article>
        );
      })}
    </div>
  );
}

/** A run: its status and progress, its error below it. */
function RunRow({ run, onCancel }: { run: BacktestRunInfo; onCancel?: (run: number) => void }) {
  const status = runStatusLabels[run.status] ?? { text: run.status, tone: "off" as const };
  const progress = progressOf(run);
  const end = run.finished_at ?? (run.started_at ? Date.now() / 1000 : undefined);
  return (
    <>
      <tr>
        <td className="strong" data-label="Agente">
          {run.agent}
          {run.llm && <span className="tag tag-live bt-inline-tag">LLM</span>}
        </td>
        <td data-label="Segmento">{segmentLabel(run.segment)}</td>
        <td className="mono" data-label="Período">
          {periodLabel(run.from, run.to)}
        </td>
        <td data-label="Status">
          <span className={toneClass(status.tone)}>{status.text}</span>
        </td>
        <td data-label="Progresso">
          {progress === null ? (
            "—"
          ) : (
            <span className="bt-progress">
              <progress max={1} value={progress} aria-label={`Progresso de ${run.agent}`} />
              <span className="mono">{formatPercent(progress, 0).replace("+", "")}</span>
            </span>
          )}
        </td>
        <td className="mono" data-label="Duração">
          {run.started_at && end ? formatDuration(end - run.started_at) : "—"}
        </td>
        <td className="num" data-label="Ações">
          {active(run) && onCancel && (
            <ConfirmButton
              label="Cancelar"
              icon="close"
              question="Cancelar a execução?"
              ariaLabel={`Cancelar a execução de ${run.agent}`}
              onConfirm={() => onCancel(run.id)}
            />
          )}
        </td>
      </tr>
      {run.error && (
        <tr className="bt-run-error">
          <td colSpan={7}>
            <p className="msg msg-error">{run.error}</p>
          </td>
        </tr>
      )}
    </>
  );
}
