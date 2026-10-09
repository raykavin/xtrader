interface Props {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: string;
  disabled?: boolean;
  /** Why it is disabled, or what it does, on hover. */
  title?: string;
}

/** A switch turning something on: a stop loss of the ticket, an agent, the Telegram bot. */
export function Switch({ checked, onChange, label, disabled = false, title }: Props) {
  return (
    <label className="switch" title={title}>
      <input
        type="checkbox"
        role="switch"
        aria-checked={checked}
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className="switch-track" aria-hidden="true" />
      <span className="switch-label">{label}</span>
    </label>
  );
}
