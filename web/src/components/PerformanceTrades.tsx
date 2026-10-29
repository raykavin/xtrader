import { useMemo, useState } from "react";
import type { PerformanceTrade } from "../api/types";
import { formatMoney, formatPrice, formatProfit, formatQuantity, signClass } from "../lib/format";
import { sideLabel } from "../lib/labels";
import { clampPage, pageRows } from "../lib/pagination";
import { formatDuration, listTrades, tradesCSV } from "../lib/performance";
import { Icon } from "./Icon";
import { Paginator } from "./Paginator";
import { TimeCell } from "./TimeCell";

interface Props {
  trades: PerformanceTrade[];
  currency: string;
  /** Names the file of the CSV, eg: the period and the filters. */
  fileName: string;
}

/** Downloads text as a CSV file named name. */
function download(text: string, name: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/csv;charset=utf-8" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/**
 * The trades of a currency, the latest first, each with its result before
 * and after the costs and the net results summed until it; downloaded as a
 * CSV with the filters applied.
 */
export function PerformanceTrades({ trades, currency, fileName }: Props) {
  const listed = useMemo(() => listTrades(trades), [trades]);
  const [page, setPage] = useState(0);
  const visible = pageRows(listed, clampPage(page, listed.length));

  return (
    <div className="panel-body">
      <div className="panel-toolbar">
        <p className="panel-summary">
          {listed.length === 1 ? "1 operação" : `${listed.length} operações`}, com os resultados em{" "}
          {currency}, a mais recente primeiro
        </p>
        <button
          type="button"
          className="btn btn-sm"
          disabled={listed.length === 0}
          onClick={() => download(tradesCSV(trades), fileName)}
        >
          <Icon name="download" size={14} />
          Baixar CSV
        </button>
      </div>
      <div className="table-wrap">
        <table className="table table-responsive perf-table">
          <thead>
            <tr>
              <th scope="col">Ativo</th>
              <th scope="col">Agente</th>
              <th scope="col">Abertura (UTC)</th>
              <th scope="col">Fechamento (UTC)</th>
              <th scope="col">Duração</th>
              <th scope="col">Lado</th>
              <th scope="col" className="num">
                Lotes
              </th>
              <th scope="col" className="num">
                Entrada
              </th>
              <th scope="col" className="num">
                Saída
              </th>
              <th scope="col" className="num">
                Bruto
              </th>
              <th scope="col" className="num">
                Custos
              </th>
              <th scope="col" className="num">
                Líquido
              </th>
              <th scope="col" className="num">
                Acumulado
              </th>
            </tr>
          </thead>
          <tbody>
            {visible.map((t) => (
              <tr key={`${t.account}/${t.position}`}>
                <td className="strong" data-label="Ativo">
                  {t.symbol}
                </td>
                <td data-label="Agente">{t.agent}</td>
                <td className="mono" data-label="Abertura (UTC)">
                  {t.opened_at ? <TimeCell unix={t.opened_at} /> : "—"}
                </td>
                <td className="mono" data-label="Fechamento (UTC)">
                  <TimeCell unix={t.closed_at} />
                </td>
                <td className="mono" data-label="Duração">
                  {formatDuration(t.opened_at ? t.closed_at - t.opened_at : null)}
                </td>
                <td data-label="Lado">
                  <span className={`side-chip ${t.side === "BUY" ? "buy" : "sell"}`}>
                    {sideLabel(t.side)}
                  </span>
                </td>
                <td className="num mono" data-label="Lotes">
                  {formatQuantity(t.volume)}
                </td>
                <td className="num mono" data-label="Entrada">
                  {t.entry ? formatPrice(t.entry) : "—"}
                </td>
                <td className="num mono" data-label="Saída">
                  {formatPrice(t.exit)}
                </td>
                <td className={`num mono ${signClass(t.gross)}`} data-label="Bruto">
                  {formatProfit(t.gross)}
                </td>
                <td className="num mono" data-label="Custos">
                  {t.costs ? formatMoney(t.costs) : "—"}
                </td>
                <td className={`num mono strong ${signClass(t.net)}`} data-label="Líquido">
                  {formatProfit(t.net)}
                </td>
                <td className={`num mono ${signClass(t.cumulative)}`} data-label="Acumulado">
                  {formatProfit(t.cumulative)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <Paginator page={clampPage(page, listed.length)} total={listed.length} onPage={setPage} />
    </div>
  );
}
