import { useEffect, useState } from "react";
import { fetchDailyGuard } from "../api/client";
import type { DailyLimitsStatus } from "../api/types";
import {
  dailyStateLabel,
  flattenLabel,
  guardedAgents,
  limitsLabel,
  resultAmount,
} from "../lib/dailyLimits";

/** How often the state of the daily limits is read again, in ms. */
const POLL = 5000;

/** The last read, kept as the dashboard switches agents. */
let lastStatus: DailyLimitsStatus | null = null;

/** The state of the daily limits, read at once and every POLL, the last one kept on a failure. */
export function useDailyGuard(): DailyLimitsStatus | null {
  const [status, setStatus] = useState<DailyLimitsStatus | null>(lastStatus);
  useEffect(() => {
    const controller = new AbortController();
    const load = () => {
      fetchDailyGuard(controller.signal)
        .then((next) => {
          lastStatus = next;
          setStatus(next);
        })
        .catch(() => {
          // kept: the limits are applied by the controller whatever the dashboard reads
        });
    };
    load();
    const timer = window.setInterval(load, POLL);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, []);
  return status;
}

interface Props {
  status: DailyLimitsStatus | null;
  /** A line only, eg: in the panel of the XTrader AI. */
  compact?: boolean;
}

/**
 * The daily limit of all the agents now, as the controller weighs it: the
 * result of the day (realized plus open), the limits, the state (free,
 * blocked and why, incomplete) and how the positions were zeroed; the agents
 * blocked by their own limits. Nothing while no limit is set and none blocks.
 */
export function DailyGuardStrip({ status, compact = false }: Props) {
  if (!status) return null;
  const global = status.global;
  const guarded = Object.values(guardedAgents(status));
  if (global.state === "off" && guarded.length === 0) return null;
  const state = dailyStateLabel(global);
  const result = global.result;
  const flatten = flattenLabel(global.guard?.flatten_status);
  return (
    <section
      className={`daily-strip ${global.state}${compact ? " compact" : ""}`}
      aria-label="Limite diário"
    >
      <span className="daily-strip-label">Limite diário</span>
      {global.state !== "off" && <span className={`status-chip ${state.tone}`}>{state.text}</span>}
      {result && global.state !== "off" && (
        <span
          className="daily-strip-result"
          title={`Hoje (${status.day}, desde 00:00 ${status.zone}): realizado ${resultAmount(result.realized, result.currency)}, aberto ${resultAmount(result.open, result.currency)}${result.complete ? "" : "; incompleto"}`}
        >
          <span className="field-hint">resultado do dia</span>{" "}
          <strong className="mono">{resultAmount(result.total, result.currency)}</strong>
          {!compact && (
            <span className="field-hint">
              {" "}
              (realizado {resultAmount(result.realized, result.currency)} · aberto{" "}
              {resultAmount(result.open, result.currency)})
            </span>
          )}
        </span>
      )}
      {global.state !== "off" && (
        <span className="field-hint daily-strip-limits">{limitsLabel(global.limits)}</span>
      )}
      {flatten && <span className={`status-chip ${flatten.tone}`}>{flatten.text}</span>}
      {!compact && global.guard && <p className="daily-strip-reason">{global.guard.reason}</p>}
      {guarded.length > 0 && (
        <span className="field-hint daily-strip-agents">
          {guarded.length === 1 ? "1 agente bloqueado" : `${guarded.length} agentes bloqueados`}{" "}
          pelo próprio limite: {guarded.map((scope) => scope.agent).join(", ")}
        </span>
      )}
    </section>
  );
}
