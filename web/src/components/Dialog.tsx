import type { ReactNode } from "react";
import { Icon } from "./Icon";

interface Props {
  /** The ID the dialog is labelled by. */
  id: string;
  title: ReactNode;
  /** Muted after the title, eg: the agent of the results. */
  subtitle?: ReactNode;
  onClose: () => void;
  /** Whether the close button is off, eg: while the form is sent. */
  disabled?: boolean;
}

/**
 * The top of a dialog, the same in all of them: its title, and its close
 * button, kept in view as the body scrolls.
 */
export function DialogHeader({ id, title, subtitle, onClose, disabled = false }: Props) {
  return (
    <header className="dialog-head">
      <h2 id={id} className="dialog-title">
        {title}
        {subtitle && <small className="dialog-subtitle">{subtitle}</small>}
      </h2>
      <button
        type="button"
        className="btn btn-ghost btn-icon"
        onClick={onClose}
        disabled={disabled}
        aria-label="Fechar"
        title="Fechar (Esc)"
      >
        <Icon name="close" />
      </button>
    </header>
  );
}
