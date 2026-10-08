import { useEffect, useRef, useState } from "react";
import { decisionAttachmentUrl, fetchDecision } from "../api/client";
import type { Decision } from "../api/types";
import {
  decisionActionLabel,
  decisionOutcomeLabel,
  decisionReason,
  decisionTone,
  decisionTriggerLabel,
  formatConfidence,
  isImage,
  isSkipped,
  levelKindLabel,
  noteKindLabel,
} from "../lib/decisions";
import { formatNumber, formatPrice, formatTime } from "../lib/format";
import { promptLabel } from "../lib/settings";

interface Props {
  agent: string;
  /** The decision shown, read from the API. */
  decisionId: string;
  quote: string;
  onClose: () => void;
}

/**
 * The justification of a decision of the language model of the strategy, its
 * annotations and the images it returned, in a modal dialog.
 */
export function DecisionDialog({ agent, decisionId, quote, onClose }: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [decision, setDecision] = useState<Decision | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const element = dialog.current;
    if (element && !element.open) element.showModal();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    setDecision(null);
    setError(null);
    fetchDecision(agent, decisionId, controller.signal)
      .then(setDecision)
      .catch((e) => {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      });
    return () => controller.abort();
  }, [agent, decisionId]);

  const close = () => dialog.current?.close();

  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc closes the dialog natively
    <dialog
      ref={dialog}
      className="decision-dialog"
      aria-labelledby="decision-title"
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current) close(); // the backdrop
      }}
    >
      <header className="decision-head">
        <h2 id="decision-title">Justificativa (LLM)</h2>
        <button type="button" className="mini-btn" onClick={close} aria-label="Fechar">
          ✕
        </button>
      </header>
      {error && <div className="banner error">{error}</div>}
      {!decision && !error && <p className="empty">Carregando…</p>}
      {decision && <DecisionDetails agent={agent} decision={decision} quote={quote} />}
    </dialog>
  );
}

function DecisionDetails({
  agent,
  decision,
  quote,
}: {
  agent: string;
  decision: Decision;
  quote: string;
}) {
  const tone = decisionTone(decision.action);
  const levels = [
    { name: "Entrada", value: decision.entry },
    { name: "Stop loss", value: decision.stop_loss },
    { name: "Take profit", value: decision.take_profit },
  ].filter((level) => level.value !== undefined && level.value !== null);

  return (
    <div className="decision-body">
      <div className="decision-summary">
        <span className={`decision-action ${tone}`}>{decisionActionLabel(decision.action)}</span>
        {decision.action && (
          <span className="decision-confidence">
            confiança {formatConfidence(decision.confidence)}
          </span>
        )}
        <span className={`badge outcome ${decision.outcome}`}>
          {decisionOutcomeLabel(decision.outcome)}
        </span>
      </div>

      <dl className="decision-meta">
        <dt>{isSkipped(decision) ? "Analisada em" : "Decidida em"}</dt>
        <dd className="mono">{formatTime(decision.decided_at)} UTC</dd>
        <dt>Candle</dt>
        <dd className="mono">
          {formatTime(decision.candle)} · {decision.timeframe}
        </dd>
        <dt>Origem</dt>
        <dd>{decisionTriggerLabel(decision)}</dd>
        <dt>Modelo</dt>
        <dd>
          {isSkipped(decision) ? (
            "não consultado"
          ) : (
            <>
              {decision.model} <small>({formatNumber(decision.latency_ms / 1000)} s)</small>
            </>
          )}
        </dd>
        {decision.prompt && (
          <>
            <dt>Prompt</dt>
            <dd className="mono">{promptLabel(decision.prompt)}</dd>
          </>
        )}
        <dt>Preço</dt>
        <dd className="mono">
          {formatPrice(decision.price)} {quote}
        </dd>
        <dt>Posição</dt>
        <dd>
          {decision.position === "LONG"
            ? "Comprada"
            : decision.position === "SHORT"
              ? "Vendida"
              : "Sem posição"}
        </dd>
      </dl>

      {decision.request_note && (
        <section className="decision-section">
          <h3>Mensagem do trader</h3>
          <p className="decision-reason">{decision.request_note}</p>
        </section>
      )}

      {isSkipped(decision) ? (
        <section className="decision-section">
          <h3>Por que o modelo não foi consultado</h3>
          <p className="decision-reason">{decisionReason(decision)}</p>
          {decision.error && <p className="decision-detail">{decision.error}</p>}
        </section>
      ) : (
        <section className="decision-section">
          <h3>Justificativa</h3>
          {decision.reason ? (
            <p className="decision-reason">{decision.reason}</p>
          ) : (
            <p className="empty">—</p>
          )}
          {decision.error && <p className="decision-error">{decision.error}</p>}
        </section>
      )}

      {levels.length > 0 && (
        <section className="decision-section">
          <h3>Níveis da decisão</h3>
          <ul className="decision-list">
            {levels.map((level) => (
              <li key={level.name}>
                <span>{level.name}</span>
                <span className="mono">{formatPrice(level.value ?? 0)}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {(decision.levels.length > 0 || decision.notes.length > 0) && (
        <section className="decision-section">
          <h3>Anotações</h3>
          <ul className="decision-list">
            {decision.levels.map((level) => (
              <li key={`${level.kind}-${level.price}`}>
                <span>{levelKindLabel(level.kind)}</span>
                <span className="mono">{formatPrice(level.price)}</span>
              </li>
            ))}
            {decision.notes.map((note, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: the notes of a decision never change
              <li key={i} className="decision-note">
                <span>{noteKindLabel(note.kind)}</span>
                <span>
                  {note.text}
                  {note.url && (
                    <a href={note.url} target="_blank" rel="noreferrer noopener">
                      {note.title || note.url}
                    </a>
                  )}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {decision.attachments.length > 0 && (
        <section className="decision-section">
          <h3>Imagens e arquivos</h3>
          <div className="decision-attachments">
            {decision.attachments.map((attachment) => {
              const url = decisionAttachmentUrl(agent, decision.id, attachment.index);
              return isImage(attachment.media_type) ? (
                <a key={attachment.index} href={url} target="_blank" rel="noreferrer noopener">
                  <img src={url} alt={`Imagem ${attachment.index + 1} da decisão`} loading="lazy" />
                </a>
              ) : (
                <a key={attachment.index} className="button" href={url} download>
                  Arquivo {attachment.index + 1} ({attachment.media_type},{" "}
                  {formatNumber(attachment.size / 1024)} KB)
                </a>
              );
            })}
          </div>
        </section>
      )}

      <section className="decision-section">
        <h3>Ordens</h3>
        <p className="mono">
          {decision.order_ids.length > 0
            ? decision.order_ids.map((id) => `#${id}`).join(", ")
            : "nenhuma"}
        </p>
      </section>
    </div>
  );
}
