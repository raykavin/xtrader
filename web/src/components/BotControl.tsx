import { useEffect, useRef, useState } from "react";
import { startBot, stopBot } from "../api/client";
import type { ControlsResponse } from "../api/types";
import { PermTradingOperate } from "../api/types";
import { useCan } from "./AccessContext";
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
 * The bot of the agent, an item of the state atop the panel: its stop,
 * confirmed in place, while it runs, its start while it is stopped, the
 * button telling the state, whatever the trade panel, which gates the orders
 * only. Without a bot to control, eg: of a backtest, only that it is off.
 */
export function BotControl({ agent, controls, onControlsChange }: Props) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const feedbackTimer = useRef<number | undefined>(undefined);

  // operated by a user allowed to only: the others see how the bot stands
  const operate = useCan(PermTradingOperate);
  const enabled = operate && controls?.bot_control === true;
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

  // stopped: the control itself tells it (Iniciar bot), no note
  const handleStop = () =>
    run(async () => {
      onControlsChange(await stopBot(agent));
    });

  return (
    <div className="status-item bot-control">
      <dt className="status-label">Bot</dt>
      <dd className="status-value">
        {!enabled && (
          <span className="status-chip">
            <StatusIcon />
            {controls === null ? "…" : !operate ? (running ? "Executando" : "Parado") : "Desligado"}
          </span>
        )}
        {!enabled ? null : running ? (
          confirming ? (
            <fieldset className="confirm" aria-label="Confirmar a parada do bot">
              <span className="confirm-question">Parar o bot?</span>
              <button
                type="button"
                className="btn btn-xs btn-danger"
                disabled={busy}
                onClick={handleStop}
              >
                Parar
              </button>
              <button
                type="button"
                className="btn btn-xs"
                disabled={busy}
                onClick={() => setConfirming(false)}
              >
                Voltar
              </button>
            </fieldset>
          ) : (
            <button
              type="button"
              className="btn btn-xs btn-danger-outline"
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
            className="btn btn-xs btn-success"
            disabled={busy}
            onClick={handleStart}
          >
            <StatusIcon mark="play" />
            Iniciar bot
          </button>
        )}
        {feedback && (
          <span className={`msg msg-${feedback.tone} status-note`} role="status">
            {feedback.text}
          </span>
        )}
      </dd>
    </div>
  );
}
