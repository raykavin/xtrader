import { useState } from "react";
import { Icon, type IconName } from "./Icon";

interface Props {
  label: string;
  /** Asked before the action, eg: Excluir o prompt trend? */
  question: string;
  onConfirm: () => unknown;
  disabled?: boolean;
  /** Why it is disabled, or what it does, on hover. */
  title?: string;
  /** Before the label, eg: a trash can. */
  icon?: IconName;
  /** Names the button for the screen readers, eg: with what it deletes. */
  ariaLabel?: string;
}

/**
 * A button of an action that can't be undone, confirmed in place before it
 * runs: outlined in red, its confirmation filled.
 */
export function ConfirmButton({
  label,
  question,
  onConfirm,
  disabled = false,
  title,
  icon,
  ariaLabel,
}: Props) {
  const [asking, setAsking] = useState(false);
  if (!asking) {
    return (
      <button
        type="button"
        className="btn btn-sm btn-danger-outline"
        disabled={disabled}
        title={title}
        aria-label={ariaLabel}
        onClick={() => setAsking(true)}
      >
        {icon && <Icon name={icon} size={14} />}
        {label}
      </button>
    );
  }
  return (
    <fieldset className="confirm" aria-label={question}>
      <span className="confirm-question">{question}</span>
      <button
        type="button"
        className="btn btn-sm btn-danger"
        onClick={() => {
          setAsking(false);
          void onConfirm();
        }}
      >
        Sim
      </button>
      <button
        type="button"
        className="btn btn-sm"
        // biome-ignore lint/a11y/noAutofocus: the button asked from leaves, the safe answer takes its focus
        autoFocus
        onClick={() => setAsking(false)}
      >
        Não
      </button>
    </fieldset>
  );
}
