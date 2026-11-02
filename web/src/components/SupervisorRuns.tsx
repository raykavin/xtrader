import type { SupervisorRunInfo } from "../api/types";
import { categoryLabel, originLabel, runStatusLabel, severityLabel } from "../lib/supervisor";
import { EmptyState } from "./States";
import { dayClock, toneClass } from "./SupervisorAction";

/**
 * The analyses of the supervisor, the newest first: when, who asked, how it
 * went, its summary and its findings, by agent, category and severity, with
 * what they rest on and what they recommend.
 */
export function SupervisorRuns({
  runs,
  compact = false,
}: {
  runs: SupervisorRunInfo[];
  compact?: boolean;
}) {
  if (runs.length === 0) {
    return (
      <EmptyState compact icon="shield" title="Nenhuma análise ainda">
        Ligue o XTrader AI ou use Analisar agora.
      </EmptyState>
    );
  }
  return (
    <ol className={compact ? "sup-runs compact" : "sup-runs"}>
      {runs.map((run) => {
        const status = runStatusLabel(run.status);
        return (
          <li key={run.id} className="sup-run">
            <header className="sup-run-head">
              <span className={toneClass(status.tone)}>{status.text}</span>
              <span className="sup-run-meta">
                {originLabel(run.origin, run.requested_by)} · {dayClock(run.started_at)}
                {run.model ? ` · ${run.model}` : ""}
              </span>
            </header>
            {run.summary && <p className="sup-run-summary">{run.summary}</p>}
            {run.error && <p className="msg msg-error">{run.error}</p>}
            {run.findings.length > 0 && (
              <ul className="sup-findings">
                {run.findings.map((finding, i) => {
                  const severity = severityLabel(finding.severity);
                  return (
                    // biome-ignore lint/suspicious/noArrayIndexKey: the findings keep the order of the model
                    <li key={i} className="sup-finding">
                      <div className="sup-finding-head">
                        <span className={toneClass(severity.tone)}>{severity.text}</span>
                        <span className="tag">{categoryLabel(finding.category)}</span>
                        {finding.agent && <strong>{finding.agent}</strong>}
                      </div>
                      <p>{finding.summary}</p>
                      {finding.evidence && (
                        <p className="sup-finding-evidence">{finding.evidence}</p>
                      )}
                      {finding.recommendation && finding.recommendation !== "none" && (
                        <p className="sup-finding-evidence">
                          Recomendação: {recommendationLabel(finding.recommendation)}
                        </p>
                      )}
                    </li>
                  );
                })}
              </ul>
            )}
            {run.action_ids && run.action_ids.length > 0 && (
              <p className="sup-run-actions">
                Ações desta análise: {run.action_ids.map((id) => `#${id}`).join(", ")}
              </p>
            )}
            {run.protection_ids && run.protection_ids.length > 0 && (
              <p className="sup-run-actions">
                Proteções armadas: {run.protection_ids.map((id) => `#${id}`).join(", ")} (veja em
                Configurações › XTrader AI)
              </p>
            )}
          </li>
        );
      })}
    </ol>
  );
}

/** A recommendation of a finding, in words. */
function recommendationLabel(recommendation: string): string {
  switch (recommendation) {
    case "watch":
      return "acompanhar";
    case "block_entries":
      return "bloquear novas entradas";
    case "stop":
      return "parar o agente";
    case "cancel_pending":
      return "cancelar as ordens pendentes de entrada";
    case "close_positions":
      return "fechar posições (decisão do usuário)";
  }
  return recommendation;
}
