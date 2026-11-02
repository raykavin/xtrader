import { useEffect, useMemo, useState } from "react";
import { ordersCSVUrl } from "../api/client";
import type { Order } from "../api/types";
import {
  formatNumber,
  formatPercent,
  formatPrice,
  formatProfit,
  formatQuantity,
  signClass,
} from "../lib/format";
import { orderStatusLabel, orderTypeLabel, sideLabel } from "../lib/labels";
import { pageRows } from "../lib/pagination";
import { Icon } from "./Icon";
import { Paginator } from "./Paginator";
import { Alert, EmptyState } from "./States";
import { TimeCell } from "./TimeCell";

interface Props {
  agent: string;
  pair: string;
  quote: string;
  /** The currency of the account, when the broker reports it, eg: MT5. */
  accountCurrency?: string;
  /** A paper wallet: its money simulated, in the quote. */
  simulated?: boolean;
  orders: Order[];
  /** With the control panel: cancels a pending order. */
  onCancel?: (order: Order) => Promise<void>;
  /**
   * A strategy of a language model keeping its decisions: shows the
   * justification of the decision that placed an order.
   */
  onShowDecision?: (id: string) => void;
}

export function OrdersTable({
  agent,
  pair,
  quote,
  accountCurrency,
  simulated,
  orders,
  onCancel,
  onShowDecision,
}: Props) {
  // on an account, the profits as it books them, in its currency, the ones it
  // didn't book yet awaited, never the core's in the quote
  const booked = orders.find((o) => o.account_currency)?.account_currency ?? accountCurrency;
  const account = booked !== undefined;
  const profitCurrency = booked ?? (simulated ? `${quote}, simulação` : quote);
  const [page, setPage] = useState(0);
  const [confirmId, setConfirmId] = useState<number | null>(null);
  const [cancelError, setCancelError] = useState<string | null>(null);

  useEffect(() => {
    if (!cancelError) return;
    const timer = window.setTimeout(() => setCancelError(null), 8000);
    return () => window.clearTimeout(timer);
  }, [cancelError]);

  const cancel = (order: Order) => {
    setConfirmId(null);
    onCancel?.(order).catch((e: unknown) =>
      setCancelError(e instanceof Error ? e.message : String(e)),
    );
  };
  const sorted = useMemo(
    () => [...orders].sort((a, b) => b.updated_at - a.updated_at || b.id - a.id),
    [orders],
  );
  const visible = pageRows(sorted, page);

  return (
    <div className="panel-body">
      <div className="panel-toolbar">
        <p className="panel-summary">
          {orders.length === 1 ? "1 ordem" : `${orders.length} ordens`} em <strong>{pair}</strong>,
          com os resultados em {profitCurrency}
        </p>
        <a className="btn btn-sm" href={ordersCSVUrl(agent, pair)} download>
          <Icon name="download" size={14} />
          Baixar CSV
        </a>
      </div>
      {cancelError && (
        <Alert tone="error" title="Não foi possível cancelar a ordem">
          {cancelError}
        </Alert>
      )}
      {orders.length === 0 ? (
        <EmptyState compact icon="list" title="Nenhuma ordem ainda" />
      ) : (
        <div className="table-wrap">
          <table className="table table-responsive">
            <thead>
              <tr>
                <th scope="col">Hora (UTC)</th>
                <th scope="col">ID</th>
                <th scope="col">Lado</th>
                <th scope="col">Tipo</th>
                <th scope="col">Status</th>
                <th scope="col" className="num">
                  Preço
                </th>
                <th scope="col" className="num">
                  Quantidade
                </th>
                {!account && (
                  <>
                    <th scope="col" className="num">
                      Total ({quote})
                    </th>
                    <th scope="col" className="num">
                      Taxa
                    </th>
                  </>
                )}
                <th scope="col" className="num">
                  P/L ({profitCurrency})
                </th>
                {onShowDecision && <th scope="col">Justificativa</th>}
                {onCancel && (
                  <th scope="col" className="col-actions">
                    <span className="sr-only">Ações</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {visible.map((order) => (
                <tr key={order.id}>
                  <td className="mono" data-label="Hora (UTC)">
                    <TimeCell unix={order.updated_at} />
                  </td>
                  <td className="mono" data-label="ID">
                    {order.id}
                  </td>
                  <td data-label="Lado">
                    <span className={`side-chip ${order.side === "BUY" ? "buy" : "sell"}`}>
                      {sideLabel(order.side)}
                    </span>
                  </td>
                  <td data-label="Tipo">{orderTypeLabel(order.type)}</td>
                  <td data-label="Status">
                    <span className={`badge ${order.status.toLowerCase()}`}>
                      {orderStatusLabel(order.status)}
                    </span>
                  </td>
                  <td className="num mono" data-label="Preço">
                    {formatPrice(order.price)}
                  </td>
                  <td className="num mono" data-label="Quantidade">
                    {formatQuantity(order.quantity)}
                  </td>
                  {!account && (
                    <>
                      <td className="num mono" data-label={`Total (${quote})`}>
                        {formatNumber(order.price * order.quantity)}
                      </td>
                      <td className="num mono" data-label="Taxa">
                        {order.fee ? formatNumber(order.fee) : "—"}
                      </td>
                    </>
                  )}
                  <td
                    className={`num mono ${signClass(account ? (order.account_profit ?? 0) : order.profit)}`}
                    data-label={`P/L (${profitCurrency})`}
                  >
                    {profitCell(order, account)}
                  </td>
                  {onShowDecision && (
                    <td data-label="Justificativa">
                      {order.decision_id ? (
                        <button
                          type="button"
                          className="btn btn-xs"
                          aria-label={`Ver a justificativa da ordem ${order.id}`}
                          onClick={() => onShowDecision(order.decision_id ?? "")}
                        >
                          <Icon name="eye" size={13} />
                          Ver
                        </button>
                      ) : (
                        "—"
                      )}
                    </td>
                  )}
                  {onCancel && (
                    <td className="num cell-actions">
                      {order.status === "NEW" &&
                        (confirmId === order.id ? (
                          <fieldset className="confirm" aria-label="Cancelar a ordem?">
                            <button
                              type="button"
                              className="btn btn-xs btn-danger"
                              onClick={() => cancel(order)}
                            >
                              Confirmar
                            </button>
                            <button
                              type="button"
                              className="btn btn-xs"
                              // biome-ignore lint/a11y/noAutofocus: the button asked from leaves, the safe answer takes its focus
                              autoFocus
                              onClick={() => setConfirmId(null)}
                            >
                              Voltar
                            </button>
                          </fieldset>
                        ) : (
                          <button
                            type="button"
                            className="btn btn-xs btn-danger-outline"
                            aria-label={`Cancelar a ordem ${order.id}`}
                            onClick={() => setConfirmId(order.id)}
                          >
                            Cancelar
                          </button>
                        ))}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Paginator page={page} total={sorted.length} onPage={setPage} />
    </div>
  );
}

/**
 * The profit of an order, a close: its percentage and its value, on an
 * account the one it booked, awaited until it does; elsewhere the core's.
 */
function profitCell(order: Order, account: boolean): string {
  if (order.profit === 0 && order.profit_value === 0) return "—";
  if (!account) return `${formatPercent(order.profit)} (${formatProfit(order.profit_value)})`;
  if (order.account_profit === undefined) {
    return `${formatPercent(order.profit)} (aguardando a corretora)`;
  }
  return `${formatPercent(order.profit)} (${formatProfit(order.account_profit)})`;
}
