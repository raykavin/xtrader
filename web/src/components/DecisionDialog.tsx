import { useEffect, useRef, useState } from "react";
import { decisionAttachmentUrl, fetchDecision } from "../api/client";
import type { Analysis, Decision } from "../api/types";
import { decisionAnalysis } from "../lib/analysis";
import {
  biasLabel,
  decisionActionLabel,
  decisionOutcomeLabel,
  decisionReason,
  decisionTone,
  decisionTriggerLabel,
  drawingKindLabel,
  drawingPlace,
  formatConfidence,
  isImage,
  isSkipped,
  levelKindLabel,
  noteKindLabel,
} from "../lib/decisions";
import { formatNumber, formatPrice, formatTime } from "../lib/format";
import { formatSentiment, sentimentBand } from "../lib/sentiment";
import { promptLabel } from "../lib/settings";
import { DialogHeader } from "./Dialog";
import { Icon } from "./Icon";
import { Alert, Loading, Skeleton } from "./States";

interface Props {
  agent: string;
  /** The decision shown, read from the API. */
  decisionId: string;
  quote: string;
  onClose: () => void;
  /** Draws the analysis of the decision on the chart, in place of the last one. */
  onShowOnChart?: (analysis: Analysis) => void;
}

/**
 * The justification of a decision of the language model of the strategy, its
 * annotations and the images it returned, in a modal dialog.
 */
export function DecisionDialog({ agent, decisionId, quote, onClose, onShowOnChart }: Props) {
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
      className="dialog dialog-md"
      aria-labelledby="decision-title"
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current) close(); // the backdrop
      }}
    >
      <DialogHeader
        id="decision-title"
        title="Justificativa da decisão"
        subtitle="LLM"
        onClose={close}
      />
      {error && (
        <div className="dialog-body">
          <Alert tone="error" title="Não foi possível carregar a decisão">
            {error}
          </Alert>
        </div>
      )}
      {!decision && !error && (
        <div className="dialog-body">
          <Loading label="Carregando a decisão…">
            <Skeleton height={32} />
            <Skeleton height={140} />
            <Skeleton height={96} />
          </Loading>
        </div>
      )}
      {decision && (
        <DecisionDetails
          agent={agent}
          decision={decision}
          quote={quote}
          onShowOnChart={
            onShowOnChart
              ? (analysis) => {
                  onShowOnChart(analysis);
                  close();
                }
              : undefined
          }
        />
      )}
    </dialog>
  );
}

function DecisionDetails({
  agent,
  decision,
  quote,
  onShowOnChart,
}: {
  agent: string;
  decision: Decision;
  quote: string;
  onShowOnChart?: (analysis: Analysis) => void;
}) {
  const drawings = decision.drawings ?? [];
  const drawable = drawings.length > 0 || (decision.entry != null && decision.action !== undefined);
  const tone = decisionTone(decision.action);
  const levels = [
    { name: "Entrada", value: decision.entry },
    { name: "Stop loss", value: decision.stop_loss },
    { name: "Take profit", value: decision.take_profit },
  ].filter((level) => level.value !== undefined && level.value !== null);

  return (
    <div className="dialog-body">
      <div className="decision-summary">
        <span className={`decision-action ${tone}`}>{decisionActionLabel(decision.action)}</span>
        {decision.action && (
          <span className="decision-confidence">
            confiança {formatConfidence(decision.confidence)}
          </span>
        )}
        {decision.sentiment !== undefined && (
          <span
            className="decision-confidence"
            title="A leitura do mercado pelo modelo, de 0 (muito baixista) a 100 (muito altista); não opera"
          >
            sentimento {formatSentiment(decision.sentiment)}{" "}
            <span className={`meter-band ${sentimentBand(decision.sentiment).tone}`}>
              {sentimentBand(decision.sentiment).label}
            </span>
          </span>
        )}
        <span className={`badge outcome ${decision.outcome}`}>
          {decisionOutcomeLabel(decision.outcome)}
        </span>
        {onShowOnChart && drawable && (
          <button
            type="button"
            className="btn btn-sm btn-primary decision-show"
            title="Desenhar as marcações e os níveis desta análise no gráfico"
            onClick={() => onShowOnChart(decisionAnalysis(decision))}
          >
            <Icon name="chart" size={14} />
            Mostrar no gráfico
          </button>
        )}
      </div>

      <dl className="meta-list">
        <dt>{isSkipped(decision) ? "Analisada em" : "Decidida em"}</dt>
        <dd className="mono">{formatTime(decision.decided_at)} UTC</dd>
        <dt>Ativo</dt>
        <dd>{decision.pair}</dd>
        <dt>Timeframe</dt>
        <dd>{decision.timeframe}</dd>
        <dt>Candle</dt>
        <dd className="mono">{formatTime(decision.candle)} UTC</dd>
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
        <section className="dialog-section">
          <h3 className="section-title">Mensagem do trader</h3>
          <p className="decision-reason">{decision.request_note}</p>
        </section>
      )}

      {isSkipped(decision) ? (
        <section className="dialog-section">
          <h3 className="section-title">Por que o modelo não foi consultado</h3>
          <p className="decision-reason">{decisionReason(decision)}</p>
          {decision.error && <p className="decision-detail">{decision.error}</p>}
        </section>
      ) : (
        <section className="dialog-section">
          <h3 className="section-title">Justificativa</h3>
          {decision.reason ? (
            <p className="decision-reason">{decision.reason}</p>
          ) : (
            <p className="empty">—</p>
          )}
          {decision.error && <p className="decision-error">{decision.error}</p>}
        </section>
      )}

      {levels.length > 0 && (
        <section className="dialog-section">
          <h3 className="section-title">Níveis da decisão</h3>
          <ul className="kv-list">
            {levels.map((level) => (
              <li key={level.name}>
                <span>{level.name}</span>
                <span className="mono">{formatPrice(level.value ?? 0)}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {drawings.length > 0 && (
        <section className="dialog-section">
          <h3 className="section-title">Marcações no gráfico</h3>
          <ul className="kv-list">
            {drawings.map((drawing) => (
              <li key={drawing.id}>
                <span>
                  {drawingKindLabel(drawing.kind)}: {drawing.label}{" "}
                  <small>
                    ({drawing.timeframe},{" "}
                    {drawing.bias === "neutral"
                      ? "viés neutro"
                      : `viés de ${biasLabel(drawing.bias)}`}
                    )
                  </small>
                </span>
                <span className="tabular">{drawingPlace(drawing)}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {(decision.levels.length > 0 || decision.notes.length > 0) && (
        <section className="dialog-section">
          <h3 className="section-title">Anotações</h3>
          <ul className="kv-list">
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
        <section className="dialog-section">
          <h3 className="section-title">Imagens e arquivos</h3>
          <div className="decision-attachments">
            {decision.attachments.map((attachment) => {
              const url = decisionAttachmentUrl(agent, decision.id, attachment.index);
              return isImage(attachment.media_type) ? (
                <a key={attachment.index} href={url} target="_blank" rel="noreferrer noopener">
                  <img src={url} alt={`Imagem ${attachment.index + 1} da decisão`} loading="lazy" />
                </a>
              ) : (
                <a key={attachment.index} className="btn btn-sm" href={url} download>
                  <Icon name="download" size={14} />
                  Arquivo {attachment.index + 1} ({attachment.media_type},{" "}
                  {formatNumber(attachment.size / 1024)} KB)
                </a>
              );
            })}
          </div>
        </section>
      )}

      <section className="dialog-section">
        <h3 className="section-title">Ordens</h3>
        <p className="mono">
          {decision.order_ids.length > 0
            ? decision.order_ids.map((id) => `#${id}`).join(", ")
            : "nenhuma"}
        </p>
      </section>
    </div>
  );
}
