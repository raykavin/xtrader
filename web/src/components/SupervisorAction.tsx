import { useEffect, useState } from "react";
import {
  confirmSupervisorAction,
  rejectSupervisorAction,
  type SupervisorDecision,
} from "../api/client";
import type { SupervisorActionInfo, SupervisorResultItem } from "../api/types";
import { formatPrice, formatProfit, formatQuantity, signClass } from "../lib/format";
import { sideLabel } from "../lib/labels";
import {
  actionLabel,
  actionNote,
  actionOrigin,
  actionStatusLabel,
  actorLabel,
  awaitingDecision,
  categoryLabel,
  itemStatusLabel,
  itemSubject,
  type SupervisorTone,
  scopeProfit,
  scopeText,
} from "../lib/supervisor";
import { ConfirmButton } from "./ConfirmButton";
import { Icon } from "./Icon";

/** The class of a chip of a tone of the supervisor. */
export function toneClass(tone: SupervisorTone): string {
  switch (tone) {
    case "on":
      return "status-chip on";
    case "error":
      return "status-chip stopped";
    case "warn":
      return "status-chip warn";
    case "info":
      return "status-chip info";
  }
  return "status-chip";
}

/** The time of a Unix second, eg: 14:05. */
export function clock(unix: number): string {
  return new Date(unix * 1000).toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
}

/** The day and time of a Unix second, eg: 15/10 14:05. */
export function dayClock(unix: number): string {
  return new Date(unix * 1000).toLocaleString("pt-BR", {
    day: "2-digit",
    month: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** The actions confirmed in two steps: they act on positions or stop an agent at once. */
function weighty(action: SupervisorActionInfo): boolean {
  return (
    action.action === "close_positions" || (action.action === "stop_agent" && action.mode === "now")
  );
}

/**
 * An action of the supervision: what it does, to whom, why; a proposal waits
 * for the confirmation of the user, written out with the positions and the
 * orders it acts on, and the result of one executed tells each item as it
 * was checked on the account.
 */
export function SupervisorActionCard({
  action,
  onDecided,
}: {
  action: SupervisorActionInfo;
  /** A decision of the user, with the action as it ended. */
  onDecided?: (decision: SupervisorDecision) => void;
}) {
  const [busy, setBusy] = useState<"confirm" | "reject" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000));
  const status = actionStatusLabel(action.status);
  const waiting = awaitingDecision(action, now);

  // a proposal expires while shown
  useEffect(() => {
    if (action.status !== "proposed" || !action.expires_at) return;
    const timer = window.setInterval(() => setNow(Math.floor(Date.now() / 1000)), 15000);
    return () => window.clearInterval(timer);
  }, [action.status, action.expires_at]);

  const decide = async (kind: "confirm" | "reject") => {
    setBusy(kind);
    setError(null);
    try {
      const decision =
        kind === "confirm"
          ? await confirmSupervisorAction(action.id)
          : await rejectSupervisorAction(action.id);
      onDecided?.(decision);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  const positions = action.scope.positions ?? [];
  const orders = action.scope.orders ?? [];
  const profit = scopeProfit(action);
  const confirmLabel =
    action.action === "close_positions"
      ? `Confirmar: fechar ${positions.length === 1 ? "1 posição" : `${positions.length} posições`}`
      : `Confirmar: ${actionLabel(action.action, action.mode).toLowerCase()}`;

  return (
    <article
      className="sup-action"
      aria-label={`Ação ${action.id}: ${actionLabel(action.action, action.mode)}`}
    >
      <header className="sup-action-head">
        <div className="sup-action-title">
          <span className="eyebrow">
            {action.status === "proposed" ? "Proposta" : "Ação"} #{action.id}
            {action.category ? ` · ${categoryLabel(action.category)}` : ""}
          </span>
          <strong>{actionLabel(action.action, action.mode)}</strong>
        </div>
        <span className={toneClass(status.tone)}>{status.text}</span>
      </header>
      <p className="sup-action-scope">{scopeText(action)}</p>
      <p className="sup-action-note">{actionNote(action.action, action.mode)}</p>
      {action.reason && (
        <p className="sup-action-reason">
          <span className="field-label">Motivo</span> {action.reason}
        </p>
      )}
      {action.evidence.length > 0 && (
        <ul className="sup-evidence">
          {action.evidence.map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      )}

      {positions.length > 0 && (
        <div className="sup-list-wrap">
          <ul className="sup-list" aria-label="Posições afetadas">
            {positions.map((p) => (
              <li key={`${p.agent}-${p.ticket || p.pair}`} className="sup-row">
                <div className="sup-row-main">
                  <strong>{p.agent}</strong>
                  {!p.running && !p.paper && (
                    <span
                      className="sup-flag"
                      title="O agente não está rodando: fechada direto na conta"
                    >
                      parado
                    </span>
                  )}
                  {p.paper && <span className="sup-flag">paper</span>}
                  <span className="mono">{p.pair}</span>
                  <span>
                    {sideLabel(p.side)} {formatQuantity(p.volume)}
                  </span>
                </div>
                <div className="sup-row-sub mono">
                  {p.ticket ? `#${p.ticket}` : "simulada"}
                  {!p.paper &&
                    ` · SL ${p.stop_loss ? formatPrice(p.stop_loss) : "sem"} · TP ${
                      p.take_profit ? formatPrice(p.take_profit) : "sem"
                    }`}
                </div>
                {!p.paper && (
                  <span className={`sup-row-value mono ${signClass(p.profit)}`}>
                    {formatProfit(p.profit)} {p.currency ?? ""}
                  </span>
                )}
              </li>
            ))}
          </ul>
          {profit && <p className="sup-total">Em aberto: {profit}</p>}
        </div>
      )}
      {orders.length > 0 && (
        <ul className="sup-list" aria-label="Ordens pendentes afetadas">
          {orders.map((o) => (
            <li key={`${o.agent}-${o.ticket}`} className="sup-row">
              <div className="sup-row-main">
                <strong>{o.agent}</strong>
                <span className="mono">{o.pair}</span>
                <span>
                  {sideLabel(o.side)} {o.type.toLowerCase()}
                </span>
              </div>
              <div className="sup-row-sub mono">#{o.ticket}</div>
              <span className="sup-row-value mono">{formatPrice(o.price)}</span>
            </li>
          ))}
        </ul>
      )}

      {waiting && onDecided && (
        <div className="sup-action-buttons">
          {weighty(action) ? (
            <ConfirmButton
              label={confirmLabel}
              icon="success"
              question="Executar agora?"
              disabled={busy !== null}
              onConfirm={() => decide("confirm")}
            />
          ) : (
            <button
              type="button"
              className="btn btn-sm btn-primary"
              disabled={busy !== null}
              onClick={() => decide("confirm")}
            >
              <Icon name="success" size={14} />
              {busy === "confirm" ? "Executando…" : confirmLabel}
            </button>
          )}
          <button
            type="button"
            className="btn btn-sm"
            disabled={busy !== null}
            onClick={() => decide("reject")}
          >
            <Icon name="close" size={14} />
            Recusar
          </button>
          {action.expires_at && (
            <span className="sup-expiry">expira às {clock(action.expires_at)}</span>
          )}
        </div>
      )}
      {busy === "confirm" && weighty(action) && (
        <p className="msg">Executando e conferindo na conta…</p>
      )}
      {error && (
        <p className="msg msg-error" role="alert">
          {error}
        </p>
      )}
      {action.result && <ResultItems items={action.result.items} error={action.result.error} />}
      <footer className="sup-action-foot">
        {action.origin === "rule" ? (
          <span>{actionOrigin(action)}</span>
        ) : (
          action.requested_by && <span>Pedida por {actorLabel(action.requested_by)}</span>
        )}
        {action.origin !== "rule" && action.confirmed_by && action.status !== "proposed" && (
          <span>
            {action.status === "rejected" ? "Recusada" : "Confirmada"} por {action.confirmed_by}
          </span>
        )}
        <span>{dayClock(action.created_at)}</span>
      </footer>
    </article>
  );
}

/** The items of the result of an action, as checked. */
export function ResultItems({ items, error }: { items: SupervisorResultItem[]; error?: string }) {
  if (items.length === 0 && !error) return null;
  return (
    <ul className="sup-items" aria-label="Resultado conferido">
      {error && <li className="msg msg-error">{error}</li>}
      {items.map((item, i) => {
        const status = itemStatusLabel(item);
        return (
          // biome-ignore lint/suspicious/noArrayIndexKey: the items have no ID, their order is the one checked
          <li key={i} className="sup-item">
            <span className={toneClass(status.tone)}>{status.text}</span>
            <span className="sup-item-subject">
              {item.agent && <strong>{item.agent}</strong>} {itemSubject(item)}
            </span>
            {item.detail && <span className="sup-item-detail">{item.detail}</span>}
          </li>
        );
      })}
    </ul>
  );
}
