import { useState } from "react";

interface Props {
  label: string;
  /** Asked before the action, eg: Excluir o prompt trend? */
  question: string;
  onConfirm: () => unknown;
  disabled?: boolean;
  /** Why it is disabled, or what it does, on hover. */
  title?: string;
}

/**
 * A button of an action that can't be undone, confirmed in place before it
 * runs: outlined in red, its confirmation filled.
 */
export function ConfirmButton({ label, question, onConfirm, disabled = false, title }: Props) {
  const [asking, setAsking] = useState(false);
  if (!asking) {
    return (
      <button
        type="button"
        className="mini-btn danger-outline"
        disabled={disabled}
        title={title}
        onClick={() => setAsking(true)}
      >
        {label}
      </button>
    );
  }
  return (
    <span className="confirm">
      <span className="confirm-question">{question}</span>
      <button
        type="button"
        className="mini-btn danger"
        onClick={() => {
          setAsking(false);
          void onConfirm();
        }}
      >
        Sim
      </button>
      <button type="button" className="mini-btn" onClick={() => setAsking(false)}>
        Não
      </button>
    </span>
  );
}
