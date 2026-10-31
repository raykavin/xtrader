import { useEffect, useRef, useState } from "react";
import { closeAllPositions, fetchDailyGuard } from "../api/client";
import type { DailyLimitsStatus } from "../api/types";
import {
  type DailyTone,
  flattenLabel,
  globalUnit,
  guardedAgents,
  resultAmount,
  scopeResults,
  stripHeadline,
  stripTone,
  timeIn,
} from "../lib/dailyLimits";
import { formatMoney, signClass } from "../lib/format";
import { type CloseOutcome, closeOutcome } from "../lib/supervisor";
import { Icon } from "./Icon";

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
}

/**
 * The daily limit of all the agents atop the panel, across it, below the bar
 * of the agent: read by itself, whichever agent is shown.
 */
export function DailyGuardBanner() {
  const status = useDailyGuard();
  return <DailyGuardStrip status={status} wide />;
}

/**
 * Zeroing every position of the agents and canceling their pending entries,
 * from the card of the daily limit: apart from the close of an agent on its
 * tile, it asks to be confirmed, names what it does and what it never
 * touches; its button disabled until it ends; its outcome, checked on the
 * accounts, kept until dismissed: what was closed, canceled, and what remains.
 */
function CloseAll() {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<CloseOutcome | null>(null);
  const running = useRef(false);

  const run = () => {
    if (running.current) return; // a click more while it runs
    running.current = true;
    setBusy(true);
    setOutcome(null);
    closeAllPositions()
      .then(({ action }) => setOutcome(closeOutcome(action)))
      .catch((e) =>
        setOutcome({
          tone: "error",
          text: e instanceof Error ? e.message : String(e),
          remaining: [],
        }),
      )
      .finally(() => {
        running.current = false;
        setBusy(false);
        setConfirming(false);
      });
  };

  return (
    <div className="daily-zero">
      {!confirming && !busy && (
        <button
          type="button"
          className="btn btn-xs btn-danger daily-zero-button"
          onClick={() => setConfirming(true)}
        >
          <Icon name="alert" size={13} />
          Zerar todas as posições e cancelar pendentes
        </button>
      )}
      {(confirming || busy) && (
        <fieldset
          className="daily-zero-confirm"
          aria-label="Zerar todas as posições e cancelar pendentes"
        >
          <p>
            <strong>Todos os agentes:</strong> cancela as ordens pendentes de entrada e fecha a
            mercado as posições abertas. Posições e ordens fora dos agentes do XTrader não são
            tocadas; as entradas ficam retidas durante a execução; a habilitação e os limites não
            mudam.
          </p>
          <span className="daily-zero-actions">
            <button
              type="button"
              className="btn btn-xs btn-danger"
              disabled={busy}
              aria-busy={busy}
              onClick={run}
            >
              {busy ? "Zerando…" : "Zerar tudo"}
            </button>
            <button
              type="button"
              className="btn btn-xs"
              disabled={busy}
              onClick={() => setConfirming(false)}
            >
              Voltar
            </button>
          </span>
        </fieldset>
      )}
      {outcome && (
        <div className={`daily-zero-outcome ${outcome.tone}`} role="status">
          <p>
            <strong>Zerar tudo:</strong> {outcome.text}
          </p>
          {outcome.remaining.length > 0 && (
            <>
              <span className="daily-zero-caption">O que resta</span>
              <ul>
                {outcome.remaining.map((text) => (
                  <li key={text}>{text}</li>
                ))}
              </ul>
            </>
          )}
          <button
            type="button"
            className="btn btn-ghost btn-icon daily-zero-dismiss"
            aria-label="Dispensar o resultado"
            onClick={() => setOutcome(null)}
          >
            <Icon name="close" size={12} />
          </button>
        </div>
      )}
    </div>
  );
}

/** The class of a status chip of tone. */
function chipClass(tone: DailyTone): string {
  return `status-chip ${tone === "error" ? "stopped" : tone === "off" ? "" : tone}`.trim();
}

/**
 * The state of the strip as a padlock in a chip of its tone: closed while the
 * entries are blocked (a limit reached, a result incomplete), open while free;
 * the state in words for the screen readers and on hover.
 */
function StateLock({ tone, text, blocked }: { tone: DailyTone; text: string; blocked: boolean }) {
  return (
    <span
      className={`${chipClass(tone)} daily-strip-lock`}
      title={text}
      role="img"
      aria-label={text}
    >
      <Icon name={blocked ? "lock" : "unlock"} size={14} />
    </span>
  );
}

/** The zone of the limits in words, eg: Belém. */
function zoneLabel(zone: string): string {
  const city = zone.split("/").pop()?.replace(/_/g, " ") ?? zone;
  return city === "Belem" ? "Belém" : city;
}

/**
 * The daily limit of all the agents now, as the controller weighs it: its
 * state atop (the limit reached in the color of a gain or a loss, its result
 * unknown, or free) with how the positions were zeroed; the result of the day
 * (realized plus open) and the limits; until when the entries stay blocked;
 * the agents blocked by their own limits; and the zeroing of every position
 * of the agents. While no limit is set and none blocks, its label and the
 * zeroing only.
 */
export function DailyGuardStrip({
  status,
  wide = false,
}: Props & {
  /** Across the panel, atop it: the figures more spaced. */
  wide?: boolean;
}) {
  if (!status) return null;
  const global = status.global;
  const guarded = Object.values(guardedAgents(status));
  const tone = stripTone(global);
  const results = scopeResults(global);
  const unit = globalUnit(results);
  const flatten = flattenLabel(global.guard?.flatten_status);
  const headTone: DailyTone =
    tone === "gain" || tone === "free" ? "on" : tone === "loss" ? "error" : "warn";
  const reached = tone === "gain" || tone === "loss";
  const at = timeIn(global.guard?.tripped_at, status.zone);
  const agents = guarded.length > 0 && (
    <p className="daily-strip-agents">
      {guarded.length === 1 ? "1 agente bloqueado" : `${guarded.length} agentes bloqueados`} pelo
      próprio limite: {guarded.map((scope) => scope.agent).join(", ")}
    </p>
  );

  return (
    <section className={`daily-strip is-${tone}${wide ? " wide" : ""}`} aria-label="Limite diário">
      <div className="daily-strip-head">
        <span className="daily-strip-label">Limite diário</span>
        {tone !== "off" && (
          <StateLock tone={headTone} text={stripHeadline(global)} blocked={tone !== "free"} />
        )}
        {tone === "off" && guarded.length === 0 && (
          <span className="daily-strip-off">Sem limite definido</span>
        )}
        {flatten && (
          <span className={`${chipClass(flatten.tone)} daily-strip-flatten`}>{flatten.text}</span>
        )}
        <CloseAll />
      </div>
      {tone !== "off" && (
        <div className="daily-strip-body">
          {results.map((result) => (
            <div
              key={result.currency || "none"}
              className="daily-strip-figure"
              title={`Hoje (${status.day}, desde 00:00 ${status.zone})${result.complete ? "" : ": incompleto"}`}
            >
              <span className="daily-strip-caption">
                Resultado do dia{results.length > 1 ? ` · ${result.currency}` : ""}
              </span>
              <strong className={`mono ${signClass(result.total)}`}>
                {resultAmount(result.total, result.currency)}
              </strong>
              <span className="daily-strip-split">
                realizado {resultAmount(result.realized, result.currency)} · aberto{" "}
                {resultAmount(result.open, result.currency)}
              </span>
            </div>
          ))}
          {global.limits.enabled && (
            <dl className="daily-strip-limits">
              {global.limits.max_loss != null && (
                <div>
                  <dt>Perda máx.</dt>
                  <dd className="mono">
                    {formatMoney(global.limits.max_loss)} {unit}
                  </dd>
                </div>
              )}
              {global.limits.max_gain != null && (
                <div>
                  <dt>Ganho máx.</dt>
                  <dd className="mono">
                    {formatMoney(global.limits.max_gain)} {unit}
                  </dd>
                </div>
              )}
            </dl>
          )}
        </div>
      )}
      {reached && (
        <p className="daily-strip-note">
          Entradas bloqueadas até 00:00 ({zoneLabel(status.zone)}){at ? ` · atingido às ${at}` : ""}
        </p>
      )}
      {tone === "incomplete" && global.guard && (
        <p className="daily-strip-note">
          {global.guard.reason.charAt(0).toUpperCase() + global.guard.reason.slice(1)}
        </p>
      )}
      {agents}
    </section>
  );
}
