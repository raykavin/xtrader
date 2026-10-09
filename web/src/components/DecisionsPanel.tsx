import { useEffect, useRef, useState } from "react";
import { reanalyze } from "../api/client";
import type { Decision } from "../api/types";
import {
  decisionActionLabel,
  decisionOutcomeLabel,
  decisionReason,
  decisionTone,
  decisionTriggerLabel,
  formatConfidence,
  isSkipped,
  type ReanalysisState,
  reanalysisButton,
} from "../lib/decisions";
import { formatTime } from "../lib/format";
import { pageRows } from "../lib/pagination";
import { Icon } from "./Icon";
import { Paginator } from "./Paginator";
import { ReanalysisDialog } from "./ReanalysisDialog";
import { Alert, EmptyState } from "./States";
import { TimeCell } from "./TimeCell";

interface Props {
  agent: string;
  pair: string;
  /** The language model of the strategy, eg: openai gpt-5-mini. */
  model: string;
  decisions: Decision[];
  error: string | null;
  /** Whether the model may be asked again from the dashboard, see reanalysisState. */
  reanalysis: ReanalysisState;
  onShow: (id: string) => void;
}

type Feedback = { tone: "ok" | "error"; text: string };

/**
 * The last decisions of the language model of the strategy, with their
 * justification, the ones without an order included; and its reanalysis.
 */
export function DecisionsPanel({
  agent,
  pair,
  model,
  decisions,
  error,
  reanalysis,
  onShow,
}: Props) {
  const [asking, setAsking] = useState(false);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [page, setPage] = useState(0);
  const timer = useRef<number | undefined>(undefined);
  const button = reanalysisButton(reanalysis);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  const show = (next: Feedback) => {
    setFeedback(next);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setFeedback(null), 10000);
  };

  // a refusal is shown in the dialog, the note kept to send again
  const handleReanalyze = async (note: string) => {
    const response = await reanalyze(agent, pair, note);
    show({ tone: "ok", text: `${response.message} A decisão aparece aqui ao chegar.` });
  };

  return (
    <div className="panel-body decisions">
      <div className="panel-toolbar">
        <p className="panel-summary">
          {decisions.length === 1 ? "1 decisão" : `${decisions.length} decisões`} em{" "}
          <span className="mono">{pair}</span> · modelo <span className="mono">{model}</span>
        </p>
        <button
          type="button"
          className="btn btn-sm btn-primary"
          disabled={reanalysis !== "enabled"}
          title={button.title}
          onClick={() => setAsking(true)}
        >
          <Icon name="sparkles" size={14} />
          {button.label}
        </button>
      </div>
      {feedback && (
        <Alert tone={feedback.tone === "ok" ? "success" : "error"}>{feedback.text}</Alert>
      )}
      {asking && (
        <ReanalysisDialog
          pair={pair}
          onConfirm={handleReanalyze}
          onClose={() => setAsking(false)}
        />
      )}
      {error && (
        <Alert tone="error" title="Não foi possível carregar as decisões">
          {error}
        </Alert>
      )}
      {decisions.length === 0 ? (
        <EmptyState compact icon="sparkles" title="Nenhuma decisão ainda">
          Cada análise do modelo neste par aparece aqui, com a justificativa, inclusive as que não
          geraram ordem.
        </EmptyState>
      ) : (
        <div className="table-wrap">
          <table className="table table-responsive">
            <thead>
              <tr>
                <th scope="col">Hora (UTC)</th>
                <th scope="col">Origem</th>
                <th scope="col">Ação</th>
                <th scope="col" className="num">
                  Confiança
                </th>
                <th scope="col">Resultado</th>
                <th scope="col">Justificativa</th>
                <th scope="col" className="col-actions">
                  <span className="sr-only">Detalhes</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {pageRows(decisions, page).map((decision) => {
                const tone = decisionTone(decision.action);
                return (
                  <tr key={decision.id}>
                    <td className="mono" data-label="Hora (UTC)">
                      <TimeCell unix={decision.decided_at} />
                    </td>
                    <td data-label="Origem">
                      {decisionTriggerLabel(decision)}
                      {decision.request_note && (
                        <span
                          className="note-mark"
                          title={`Mensagem: ${decision.request_note}`}
                          role="img"
                          aria-label="Com mensagem do trader"
                        >
                          <Icon name="message" size={13} />
                        </span>
                      )}
                    </td>
                    <td data-label="Ação">
                      {tone ? (
                        <span className={`side-chip ${tone}`}>
                          {decisionActionLabel(decision.action)}
                        </span>
                      ) : (
                        decisionActionLabel(decision.action)
                      )}
                    </td>
                    <td className="num mono" data-label="Confiança">
                      {decision.action ? formatConfidence(decision.confidence) : "—"}
                    </td>
                    <td data-label="Resultado">
                      <span className={`badge outcome ${decision.outcome}`}>
                        {decisionOutcomeLabel(decision.outcome)}
                      </span>
                    </td>
                    <td
                      className="cell-truncate cell-fill decision-cell"
                      data-label="Justificativa"
                      title={
                        isSkipped(decision) ? decision.error : decision.reason || decision.error
                      }
                    >
                      {decisionReason(decision)}
                    </td>
                    <td className="num cell-actions">
                      <button
                        type="button"
                        className="btn btn-xs"
                        aria-label={`Ver a decisão de ${formatTime(decision.decided_at)}`}
                        onClick={() => onShow(decision.id)}
                      >
                        <Icon name="eye" size={13} />
                        Ver
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <Paginator page={page} total={decisions.length} onPage={setPage} />
    </div>
  );
}
