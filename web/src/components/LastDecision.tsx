import type { Decision } from "../api/types";
import {
  decisionActionLabel,
  decisionOutcomeLabel,
  decisionReason,
  decisionTone,
  decisionTriggerLabel,
  formatConfidence,
} from "../lib/decisions";
import { formatPrice, formatTime } from "../lib/format";
import { Icon } from "./Icon";
import { SectionHead } from "./SectionHead";
import { EmptyState, Skeleton } from "./States";

interface Props {
  /** The newest decision of the pair, none yet. */
  decision?: Decision;
  /** Whether the decisions of the pair were read: until then, a block stands for the card. */
  loaded: boolean;
  error: string | null;
  /** Opens the whole decision. */
  onShow: (id: string) => void;
}

/**
 * The newest decision of the language model on the pair, under the chart
 * that draws its analysis: the verdict, its justification and confidence,
 * and the way to the whole of it; the list of them is in the tabs below.
 */
export function LastDecision({ decision, loaded, error, onShow }: Props) {
  const tone = decision ? decisionTone(decision.action) || "neutral" : "neutral";
  return (
    <section className="card decision-card" aria-labelledby="last-decision-title">
      <SectionHead
        id="last-decision-title"
        eyebrow="Análise do LLM"
        title="Última decisão"
        subtitle={
          decision &&
          `${formatTime(decision.decided_at)} UTC · ${decisionTriggerLabel(decision)} · ${decision.model}`
        }
        aside={
          decision && (
            <span className={`verdict ${tone}`}>
              <span className="verdict-label">Decisão</span>
              <strong className="verdict-value">
                {decision.action
                  ? decisionActionLabel(decision.action)
                  : decisionOutcomeLabel(decision.outcome)}
              </strong>
            </span>
          )
        }
      />
      {error && !decision ? (
        <p className="msg msg-error" role="alert">
          {error}
        </p>
      ) : !loaded && !decision ? (
        <Skeleton height={72} />
      ) : !decision ? (
        <EmptyState icon="sparkles" title="Nenhuma decisão neste par" compact>
          A primeira análise do modelo aparece aqui assim que chegar.
        </EmptyState>
      ) : (
        <>
          <p className="decision-card-reason">{decisionReason(decision)}</p>
          <div className="decision-card-foot">
            <dl className="decision-facts">
              {decision.action && (
                <div className="decision-fact">
                  <dt>Confiança</dt>
                  <dd>
                    <span className="meter" aria-hidden="true">
                      <span style={{ width: `${Math.round(decision.confidence * 100)}%` }} />
                    </span>
                    <span className="mono">{formatConfidence(decision.confidence)}</span>
                  </dd>
                </div>
              )}
              <div className="decision-fact">
                <dt>Resultado</dt>
                <dd>
                  <span className={`badge outcome ${decision.outcome}`}>
                    {decisionOutcomeLabel(decision.outcome)}
                  </span>
                </dd>
              </div>
              {decision.price > 0 && (
                <div className="decision-fact">
                  <dt>Preço</dt>
                  <dd className="mono">{formatPrice(decision.price)}</dd>
                </div>
              )}
            </dl>
            <button type="button" className="btn btn-sm" onClick={() => onShow(decision.id)}>
              <Icon name="eye" />
              Ver detalhes
            </button>
          </div>
        </>
      )}
    </section>
  );
}
