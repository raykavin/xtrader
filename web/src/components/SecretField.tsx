import { type FormEvent, useState } from "react";
import { ConfirmButton } from "./ConfirmButton";

interface Props {
  label: string;
  /** Whether the secret is set: its value is never shown, nor read. */
  set: boolean;
  /** Saves a new value, or removes it for "": rejects with why not. */
  onSave: (value: string) => Promise<void>;
  disabled?: boolean;
  placeholder?: string;
}

/**
 * A secret written from the page, never read back: whether it is set, a field
 * to replace it, and its removal.
 */
export function SecretField({ label, set, onSave, disabled = false, placeholder }: Props) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = async (next: string) => {
    setBusy(true);
    setError(null);
    try {
      await onSave(next);
      setEditing(false);
      setValue("");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (value.trim() !== "") void save(value.trim());
  };

  return (
    <div className="secret-field">
      <div className="secret-head">
        <span className="secret-label">{label}</span>
        <span className={set ? "badge filled" : "badge"}>{set ? "definido" : "não definido"}</span>
        {!editing && (
          <span className="secret-actions">
            <button
              type="button"
              className="mini-btn"
              disabled={disabled || busy}
              onClick={() => setEditing(true)}
            >
              {set ? "Trocar" : "Definir"}
            </button>
            {set && (
              <ConfirmButton
                label="Remover"
                question={`Remover ${label.toLowerCase()}?`}
                disabled={disabled || busy}
                onConfirm={() => save("")}
              />
            )}
          </span>
        )}
      </div>
      {editing && (
        <form className="secret-form" onSubmit={submit}>
          <input
            type="password"
            className="settings-input"
            autoComplete="off"
            aria-label={label}
            placeholder={placeholder ?? "Novo valor"}
            value={value}
            disabled={busy}
            onChange={(e) => setValue(e.target.value)}
            // biome-ignore lint/a11y/noAutofocus: opened by the user to type it
            autoFocus
          />
          <button type="submit" className="button primary" disabled={busy || value.trim() === ""}>
            {busy ? "Salvando…" : "Salvar"}
          </button>
          <button
            type="button"
            className="button"
            disabled={busy}
            onClick={() => {
              setEditing(false);
              setValue("");
              setError(null);
            }}
          >
            Cancelar
          </button>
        </form>
      )}
      {error && (
        <p className="control-feedback error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
