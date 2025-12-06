import { useEffect, useMemo, useState } from "react";
import { ordersCSVUrl } from "../api/client";
import type { Order } from "../api/types";
import {
  formatNumber,
  formatPercent,
  formatPrice,
  formatProfit,
  formatQuantity,
  formatTime,
  signClass,
} from "../lib/format";
import { orderStatusLabel, orderTypeLabel, sideLabel } from "../lib/labels";
import { pageRows } from "../lib/pagination";
import { Paginator } from "./Paginator";

interface Props {
  agent: string;
  pair: string;
  quote: string;
  /** The currency of the account, when the broker reports it, eg: MT5. */
  accountCurrency?: string;
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
  orders,
  onCancel,
  onShowDecision,
}: Props) {
  // the profits as the account books them, in its currency, when it does
  const profitCurrency =
    orders.find((o) => o.account_currency)?.account_currency ?? accountCurrency ?? quote;
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
    <section className="card">
      <div className="card-title">
        <span>
          Ordens <small>{orders.length}</small>
          {cancelError && <span className="chart-error">{cancelError}</span>}
        </span>
        <a className="button" href={ordersCSVUrl(agent, pair)} download>
          Baixar CSV
        </a>
      </div>
      {orders.length === 0 ? (
        <p className="empty">Nenhuma ordem ainda.</p>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Hora (UTC)</th>
                <th>ID</th>
                <th>Lado</th>
                <th>Tipo</th>
                <th>Status</th>
                <th className="num">Preço</th>
                <th className="num">Quantidade</th>
                <th className="num">Total ({quote})</th>
                <th className="num">Taxa</th>
                <th className="num">Lucro/Perda ({profitCurrency})</th>
                {onShowDecision && <th>Justificativa (LLM)</th>}
                {onCancel && <th aria-label="Ações" />}
              </tr>
            </thead>
            <tbody>
              {visible.map((order) => (
                <tr key={order.id}>
                  <td className="mono">{formatTime(order.updated_at)}</td>
                  <td className="mono">{order.id}</td>
                  <td className={order.side === "BUY" ? "pos" : "neg"}>{sideLabel(order.side)}</td>
                  <td>{orderTypeLabel(order.type)}</td>
                  <td>
                    <span className={`badge ${order.status.toLowerCase()}`}>
                      {orderStatusLabel(order.status)}
                    </span>
                  </td>
                  <td className="num mono">{formatPrice(order.price)}</td>
                  <td className="num mono">{formatQuantity(order.quantity)}</td>
                  <td className="num mono">{formatNumber(order.price * order.quantity)}</td>
                  <td className="num mono">{order.fee ? formatNumber(order.fee) : "—"}</td>
                  <td className={`num mono ${signClass(order.account_profit ?? order.profit)}`}>
                    {order.profit !== 0 || order.profit_value !== 0
                      ? `${formatPercent(order.profit)} (${formatProfit(order.account_profit ?? order.profit_value)}${order.account_profit === undefined && profitCurrency !== quote ? ` ${quote}` : ""})`
                      : "—"}
                  </td>
                  {onShowDecision && (
                    <td>
                      {order.decision_id ? (
                        <button
                          type="button"
                          className="table-btn"
                          onClick={() => onShowDecision(order.decision_id ?? "")}
                        >
                          Ver
                        </button>
                      ) : (
                        "—"
                      )}
                    </td>
                  )}
                  {onCancel && (
                    <td className="num">
                      {order.status === "NEW" &&
                        (confirmId === order.id ? (
                          <button
                            type="button"
                            className="table-btn danger"
                            onClick={() => cancel(order)}
                          >
                            Confirmar
                          </button>
                        ) : (
                          <button
                            type="button"
                            className="table-btn cancel"
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
    </section>
  );
}
