import { useEffect, useRef, useState } from "react";
import { reanalyze } from "../api/client";
import type { Decision } from "../api/types";
import {
  decisionActionLabel,
  decisionOutcomeLabel,
  decisionTone,
  decisionTriggerLabel,
  formatConfidence,
  type ReanalysisState,
  reanalysisButton,
} from "../lib/decisions";
import { formatTime } from "../lib/format";
import { pageRows } from "../lib/pagination";
import { Paginator } from "./Paginator";
import { ReanalysisDialog } from "./ReanalysisDialog";

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
    <section className="card decisions" aria-label="Decisões do LLM">
      <div className="card-title">
        <span>
          Decisões do LLM <small>{model}</small>
          {feedback && (
            <span className={feedback.tone === "ok" ? "decision-feedback" : "chart-error"}>
              {feedback.text}
            </span>
          )}
        </span>
        <button
          type="button"
          className="button"
          disabled={reanalysis !== "enabled"}
          title={button.title}
          onClick={() => setAsking(true)}
        >
          {button.label}
        </button>
      </div>
      {asking && (
        <ReanalysisDialog
          pair={pair}
          onConfirm={handleReanalyze}
          onClose={() => setAsking(false)}
        />
      )}
      {error && <div className="banner error">{error}</div>}
      {decisions.length === 0 ? (
        <p className="empty">Nenhuma decisão ainda.</p>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Hora (UTC)</th>
                <th>Origem</th>
                <th>Ação</th>
                <th className="num">Confiança</th>
                <th>Resultado</th>
                <th>Justificativa</th>
                <th aria-label="Detalhes" />
              </tr>
            </thead>
            <tbody>
              {pageRows(decisions, page).map((decision) => (
                <tr key={decision.id}>
                  <td className="mono">{formatTime(decision.decided_at)}</td>
                  <td>
                    {decisionTriggerLabel(decision)}
                    {decision.request_note && (
                      <span
                        className="note-mark"
                        title={`Mensagem: ${decision.request_note}`}
                        role="img"
                        aria-label="Com mensagem do trader"
                      >
                        💬
                      </span>
                    )}
                  </td>
                  <td
                    className={
                      decisionTone(decision.action) === "buy"
                        ? "pos"
                        : decisionTone(decision.action) === "sell"
                          ? "neg"
                          : ""
                    }
                  >
                    {decisionActionLabel(decision.action)}
                  </td>
                  <td className="num mono">
                    {decision.action ? formatConfidence(decision.confidence) : "—"}
                  </td>
                  <td>
                    <span className={`badge outcome ${decision.outcome}`}>
                      {decisionOutcomeLabel(decision.outcome)}
                    </span>
                  </td>
                  <td className="decision-cell" title={decision.reason || decision.error}>
                    {decision.reason || decision.error || "—"}
                  </td>
                  <td className="num">
                    <button type="button" className="table-btn" onClick={() => onShow(decision.id)}>
                      Ver
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Paginator page={page} total={decisions.length} onPage={setPage} />
    </section>
  );
}
