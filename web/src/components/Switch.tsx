interface Props {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: string;
  disabled?: boolean;
  /** Why it is disabled, or what it does, on hover. */
  title?: string;
}

/** A switch turning something on: an agent, the Telegram bot, a setting. */
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
