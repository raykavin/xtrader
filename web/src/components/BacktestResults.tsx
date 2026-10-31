import { useId, useMemo, useState } from "react";
import type { BacktestBatchInfo, BacktestRunInfo, BacktestSummary } from "../api/types";
import {
  type ComparisonRow,
  comparisonRows,
  conditionDifferences,
  filterRows,
  hoursAssumption,
  MIN_SAMPLE,
  robustnessFlags,
  runStatusLabels,
  type SortKey,
  sortRows,
} from "../lib/backtest";
import { formatMoney, formatNumber, formatPercent, formatProfit, signClass } from "../lib/format";
import type { Theme } from "../lib/theme";
import { timeframeLabel } from "../lib/timeframes";
import { BacktestRunDetail } from "./BacktestRunDetail";
import { periodLabel } from "./BacktestRuns";
import { Select } from "./Select";
import { Alert, EmptyState } from "./States";
import { toneClass } from "./SupervisorAction";
import { Switch } from "./Switch";

interface Props {
  batches: BacktestBatchInfo[];
  /** The batch shown; the newest when null. */
  batch: number | null;
  onBatch: (batch: number) => void;
  theme: Theme;
  onNew: () => void;
}

/** A share without its sign, eg: 42,5%. */
function share(value: number, digits = 1): string {
  return formatPercent(value, digits).replace("+", "");
}

/** The columns of the comparison: their title, what they mean on hover. */
const columns: { key: SortKey; label: string; title: string }[] = [
  { key: "agent", label: "Agente", title: "O agente, a sua estratégia e os seus ativos" },
  {
    key: "ratio",
    label: "Ret./DD",
    title: "O resultado líquido sobre o drawdown máximo: quanto rendeu por unidade de queda",
  },
  { key: "net", label: "Líquido", title: "O resultado das operações fechadas, após os custos" },
  { key: "return", label: "Retorno", title: "O resultado líquido sobre o saldo inicial" },
  { key: "drawdown", label: "DD máx.", title: "A maior queda do patrimônio desde um topo" },
  { key: "factor", label: "Fator de lucro", title: "Os ganhos somados sobre as perdas somadas" },
  { key: "winrate", label: "Acerto", title: "As operações vencedoras sobre as fechadas" },
  { key: "trades", label: "Operações", title: "As operações fechadas: a amostra" },
  {
    key: "months",
    label: "Meses +",
    title: "Os meses com resultado positivo sobre os meses com operações: a consistência",
  },
  {
    key: "validation",
    label: "Validação",
    title: "O retorno/DD e o líquido do período de validação, fora da amostra do ajuste",
  },
];

/**
 * The results of a batch compared: a row by agent with the metrics of its
 * fit (and of its validation), sortable by any of them (by return over
 * drawdown first: never by the profit alone), filtered by the sample, with
 * the flags of its robustness and the conditions that differ between the
 * rows; a row opened shows the run.
 */
export function BacktestResults({ batches, batch, onBatch, theme, onNew }: Props) {
  const current = batches.find((b) => b.id === batch) ?? batches[0];
  const [sort, setSort] = useState<{ key: SortKey; ascending?: boolean }>({ key: "ratio" });
  const [minTrades, setMinTrades] = useState("0");
  const [showLLM, setShowLLM] = useState(true);
  const [open, setOpen] = useState<{ agent: string; segment: string } | null>(null);
  const id = useId();

  const rows = useMemo(() => (current ? comparisonRows(current) : []), [current]);
  const shown = sortRows(
    filterRows(rows, { minTrades: Number(minTrades) || 0, showLLM }),
    sort.key,
    sort.ascending,
  );

  if (!current) {
    return (
      <EmptyState
        compact
        icon="results"
        title="Nenhum resultado ainda"
        action={
          <button type="button" className="btn btn-sm" onClick={onNew}>
            Configurar um lote
          </button>
        }
      >
        Os resultados aparecem quando as execuções de um lote terminam.
      </EmptyState>
    );
  }

  const a = current.assumptions;
  const differences = conditionDifferences(rows);
  const hidden = rows.length - shown.length;
  const opened = open && rows.find((r) => r.agent === open.agent);
  const openedRun = opened && (open.segment === "validation" ? opened.validation : opened.fit);
  const toggle = (key: SortKey) =>
    setSort((s) =>
      s.key === key ? { key, ascending: !(s.ascending ?? key === "agent") } : { key },
    );

  return (
    <div className="panel-body bt-results">
      <div className="perf-filters bt-filters">
        <div className="perf-filter">
          <span className="field-label">Lote</span>
          <Select
            caption="Lote"
            value={String(current.id)}
            onChange={(value) => {
              onBatch(Number(value));
              setOpen(null);
            }}
            options={batches.map((b) => ({
              value: String(b.id),
              label: b.name || `Lote #${b.id}`,
              hint: periodLabel(b.from, b.to),
            }))}
          />
        </div>
        <div className="perf-filter">
          <label className="field-label" htmlFor={`${id}-min`}>
            Mín. de operações
          </label>
          <input
            id={`${id}-min`}
            className="input mono bt-min"
            inputMode="numeric"
            value={minTrades}
            onChange={(e) => setMinTrades(e.target.value.replace(/\D/g, ""))}
          />
        </div>
        <div className="perf-filter bt-filter-switch">
          <Switch checked={showLLM} onChange={setShowLLM} label="Mostrar agentes LLM" />
        </div>
      </div>

      <p className="panel-summary">
        {periodLabel(current.from, current.to)}
        {current.validation_share > 0 &&
          ` (os últimos ${share(current.validation_share, 0)} como validação)`}
        {" · saldo "}
        <strong>
          {formatMoney(a.balance)} {a.currency}
        </strong>
        {` · taxa ${share(a.fee, 3)} · spread de reserva ${formatNumber(a.spread_points)} pts · slippage ${formatNumber(a.slippage_points)} pts`}
        {` · execução em ${timeframeLabel(a.execution_timeframe)} quando a corretora tem os candles (senão mais grosso, avisado), stop antes do alvo no mesmo candle`}
        {a.daily_loss || a.daily_gain
          ? ` · limites diários ${a.daily_loss ? `-${formatMoney(a.daily_loss)}` : "—"}/${a.daily_gain ? `+${formatMoney(a.daily_gain)}` : "—"} ${a.currency}`
          : " · sem limites diários"}
        {a.hours ? ` · horário ${hoursAssumption(a.hours)}` : ""}
        {" · abertas no fim: a mercado, à parte"}
      </p>

      {differences.length > 0 && (
        <Alert tone="warn" title="Condições diferentes entre as linhas">
          {differences.join("; ")}. Compare com cuidado.
        </Alert>
      )}
      {hidden > 0 && (
        <p className="field-hint">
          {hidden === 1 ? "1 agente oculto" : `${hidden} agentes ocultos`} pelos filtros.
        </p>
      )}

      <div className="table-wrap">
        <table className="table table-responsive bt-compare">
          <thead>
            <tr>
              {columns.map((c) => (
                <th
                  key={c.key}
                  scope="col"
                  className={c.key === "agent" ? undefined : "num"}
                  aria-sort={
                    sort.key === c.key
                      ? (sort.ascending ?? c.key === "agent")
                        ? "ascending"
                        : "descending"
                      : undefined
                  }
                >
                  <button
                    type="button"
                    className="bt-sort"
                    title={c.title}
                    onClick={() => toggle(c.key)}
                  >
                    {c.label}
                    {sort.key === c.key && (
                      <span aria-hidden="true">
                        {(sort.ascending ?? c.key === "agent") ? " ↑" : " ↓"}
                      </span>
                    )}
                  </button>
                </th>
              ))}
              <th scope="col">Robustez</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((row) => (
              <CompareRow
                key={row.agent}
                row={row}
                selected={open?.agent === row.agent}
                onOpen={(segment) => setOpen({ agent: row.agent, segment })}
              />
            ))}
          </tbody>
        </table>
      </div>
      <p className="field-hint">
        Nenhuma linha é "a melhor": compare o retorno pelo risco, a amostra ({MIN_SAMPLE}+
        operações), a consistência e a validação. "—" é uma métrica indefinida (ex.: fator de lucro
        sem perdas); passe o mouse para ver o motivo. Clique num agente para ver a execução.
      </p>

      {opened && openedRun && (
        <BacktestRunDetail
          key={openedRun.id}
          run={openedRun}
          other={open.segment === "validation" ? opened.fit : opened.validation}
          onSegment={(segment) => setOpen({ agent: opened.agent, segment })}
          onClose={() => setOpen(null)}
          theme={theme}
        />
      )}
    </div>
  );
}

/** A metric, or "—" with why it is undefined on hover. */
function Metric({
  value,
  text,
  why,
  tone,
  label,
}: {
  value: number | undefined | null;
  text?: (v: number) => string;
  why: string;
  tone?: boolean;
  label: string;
}) {
  if (value === undefined || value === null || !Number.isFinite(value)) {
    return (
      <td className="num mono muted" data-label={label} title={why}>
        —
      </td>
    );
  }
  return (
    <td className={`num mono ${tone ? signClass(value) : ""}`} data-label={label}>
      {text ? text(value) : formatNumber(value)}
    </td>
  );
}

/** Why the profit factor of s is undefined. */
function factorWhy(s: BacktestSummary): string {
  if (s.trades === 0) return "Sem operações fechadas";
  return s.losses === 0 ? "Sem perdas: o fator seria infinito" : "Sem ganhos";
}

/** A row of the comparison: an agent, the metrics of its fit and its validation. */
function CompareRow({
  row,
  selected,
  onOpen,
}: {
  row: ComparisonRow;
  selected: boolean;
  onOpen: (segment: string) => void;
}) {
  const fit = row.fit;
  const s = fit?.summary;
  const v = row.validation?.summary;
  const flags = s ? robustnessFlags(s, row.llm, row.degraded) : [];
  return (
    <tr className="bt-row" data-selected={selected ? "" : undefined}>
      <td className="strong" data-label="Agente">
        <button type="button" className="bt-row-open" disabled={!fit} onClick={() => onOpen("fit")}>
          {row.agent}
        </button>
        <small className="bt-row-sub">
          {[row.strategy ?? row.kind, row.pairs.join(", "), fit?.timeframe]
            .filter(Boolean)
            .join(" · ")}
        </small>
        {fit && fit.status !== "succeeded" && <RunStatus run={fit} />}
      </td>
      {!s ? (
        <td colSpan={columns.length - 1} className="muted" data-label="Resultado">
          {fit ? `${statusText(fit)}: sem resultado para comparar` : "—"}
        </td>
      ) : (
        <>
          <Metric
            label="Ret./DD"
            value={s.return_over_drawdown}
            text={(x) => formatNumber(Math.round(x * 100) / 100)}
            why="Sem drawdown: a razão não se define"
            tone
          />
          <td className={`num mono strong ${signClass(s.net)}`} data-label="Líquido">
            {formatProfit(s.net)} <small>{s.currency}</small>
          </td>
          <Metric label="Retorno" value={s.return} text={(x) => formatPercent(x)} why="" tone />
          <td className="num mono" data-label="DD máx.">
            {share(s.max_drawdown_pct)}
            <small> {formatMoney(s.max_drawdown)}</small>
          </td>
          <Metric
            label="Fator de lucro"
            value={s.profit_factor}
            text={(x) => formatNumber(Math.round(x * 100) / 100)}
            why={factorWhy(s)}
          />
          <Metric
            label="Acerto"
            value={s.trades > 0 ? s.win_rate : undefined}
            text={(x) => share(x)}
            why="Sem operações fechadas"
          />
          <td className="num mono" data-label="Operações">
            {s.trades}
            {s.open_positions > 0 && (
              <small title="Abertas no fim, avaliadas a mercado à parte">
                {" "}
                +{s.open_positions} ({formatProfit(s.open_profit)})
              </small>
            )}
          </td>
          <Metric
            label="Meses +"
            value={s.months > 0 ? s.positive_months : undefined}
            text={(x) => `${x}/${s.months}`}
            why="Sem meses com operações"
          />
          <td className="num mono" data-label="Validação">
            {v ? (
              <button
                type="button"
                className={`bt-row-open ${signClass(v.net)}`}
                onClick={() => onOpen("validation")}
                title="Ver a execução de validação"
              >
                {v.return_over_drawdown === undefined
                  ? "—"
                  : formatNumber(Math.round(v.return_over_drawdown * 100) / 100)}
                <small> {formatProfit(v.net)}</small>
              </button>
            ) : row.validation ? (
              statusText(row.validation)
            ) : (
              <span className="muted">sem</span>
            )}
          </td>
        </>
      )}
      <td data-label="Robustez">
        <span className="tag-list">
          {flags.map((f) => (
            <span key={f.text} className={`bt-flag ${f.tone}`} title={f.title}>
              {f.text}
            </span>
          ))}
          {s && s.refused > 0 && (
            <span className="bt-flag warn" title="Ordens fora do passo ou do mínimo de lote">
              {s.refused} recusada(s)
            </span>
          )}
          {s?.llm_cost !== undefined && (
            <span className="bt-flag info" title={`${s.llm_calls ?? 0} chamadas do modelo`}>
              LLM {formatMoney(s.llm_cost)} USD
            </span>
          )}
        </span>
      </td>
    </tr>
  );
}

/** The status of run in words. */
function statusText(run: BacktestRunInfo): string {
  return runStatusLabels[run.status]?.text ?? run.status;
}

/** The status of a run that didn't end as asked, under its agent. */
function RunStatus({ run }: { run: BacktestRunInfo }) {
  const status = runStatusLabels[run.status] ?? { text: run.status, tone: "off" as const };
  return <span className={`${toneClass(status.tone)} bt-row-status`}>{status.text}</span>;
}
