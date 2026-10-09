import { useEffect, useRef, useState } from "react";
import { startBot, stopBot } from "../api/client";
import type { ControlsResponse } from "../api/types";
import { botStatusLabel } from "../lib/labels";
import { StatusIcon } from "./StatusIcon";

interface Props {
  /** The agent whose bot it starts and stops. */
  agent: string;
  /** Null while unknown. */
  controls: ControlsResponse | null;
  onControlsChange: (controls: ControlsResponse) => void;
}

type Feedback = { tone: "ok" | "error"; text: string };

/**
 * The bot of the agent, at the end of the bar atop the panel: whether it
 * runs, and its start, or its stop confirmed in place, whatever the trade
 * panel, which gates the orders only. Without a bot to control, eg: of a
 * backtest, only that it is off.
 */
export function BotControl({ agent, controls, onControlsChange }: Props) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const feedbackTimer = useRef<number | undefined>(undefined);

  const enabled = controls?.bot_control === true;
  const running = controls?.status === "running";

  // Another agent starts over.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reset on agent change only
  useEffect(() => {
    setConfirming(false);
    setFeedback(null);
  }, [agent]);

  useEffect(() => {
    return () => window.clearTimeout(feedbackTimer.current);
  }, []);

  const showFeedback = (next: Feedback) => {
    setFeedback(next);
    window.clearTimeout(feedbackTimer.current);
    feedbackTimer.current = window.setTimeout(() => setFeedback(null), 8000);
  };

  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    try {
      await action();
    } catch (e) {
      showFeedback({ tone: "error", text: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  };

  const handleStart = () =>
    run(async () => {
      onControlsChange(await startBot(agent));
      showFeedback({ tone: "ok", text: "Bot iniciado." });
    });

  const handleStop = () =>
    run(async () => {
      onControlsChange(await stopBot(agent));
      showFeedback({
        tone: "ok",
        text: "Bot parado. Novas ordens, inclusive os stops de proteção, são rejeitadas até ele ser iniciado.",
      });
    });

  return (
    <div className="bot-control">
      <div className="bot-control-row">
        <span className="bot-control-label">Bot</span>
        <span className={`status-chip ${!enabled ? "" : running ? "running" : "stopped"}`}>
          <StatusIcon mark={!enabled ? undefined : running ? "play" : "stop"} />
          {controls === null ? "…" : enabled ? botStatusLabel(controls.status) : "desativado"}
        </span>
        {!enabled ? null : running ? (
          confirming ? (
            <fieldset className="confirm" aria-label="Confirmar a parada do bot">
              <span className="confirm-question">Parar o bot?</span>
              <button
                type="button"
                className="btn btn-sm btn-danger"
                disabled={busy}
                onClick={handleStop}
              >
                Parar
              </button>
              <button
                type="button"
                className="btn btn-sm"
                disabled={busy}
                onClick={() => setConfirming(false)}
              >
                Voltar
              </button>
            </fieldset>
          ) : (
            <button
              type="button"
              className="btn btn-sm btn-danger-outline"
              disabled={busy}
              onClick={() => setConfirming(true)}
            >
              <StatusIcon mark="stop" />
              Parar bot
            </button>
          )
        ) : (
          <button
            type="button"
            className="btn btn-sm btn-success"
            disabled={busy}
            onClick={handleStart}
          >
            <StatusIcon mark="play" />
            Iniciar bot
          </button>
        )}
      </div>
      {feedback && (
        <p className={`msg msg-${feedback.tone}`} role="status">
          {feedback.text}
        </p>
      )}
    </div>
  );
}
