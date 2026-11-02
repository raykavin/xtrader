import { type FormEvent, useEffect, useRef, useState } from "react";
import { agentNameError } from "../lib/settings";
import { DialogHeader } from "./Dialog";
import { Icon } from "./Icon";
import { Alert } from "./States";

interface Props {
  /** The name of the agent, disabled and stopped. */
  name: string;
  /** Renames the agent; rejects with why not. */
  onConfirm: (name: string) => Promise<void>;
  onClose: () => void;
}

/**
 * Renames an agent in a modal dialog: only its name changes, all it has
 * staying its own; it stays open with the name on a refusal.
 */
export function RenameAgentDialog({ name, onConfirm, onClose }: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  const field = useRef<HTMLInputElement>(null);
  const [value, setValue] = useState(name);
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const invalid = agentNameError(value);
  const unchanged = value.trim() === name;

  useEffect(() => {
    const element = dialog.current;
    if (element && !element.open) element.showModal();
    field.current?.select(); // the dialog focuses its first button
  }, []);

  const close = () => dialog.current?.close();

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setTouched(true);
    if (busy || invalid || unchanged) return;
    setBusy(true);
    setError(null);
    try {
      await onConfirm(value.trim());
      close();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc closes the dialog natively
    <dialog
      ref={dialog}
      className="dialog dialog-sm"
      aria-labelledby="rename-title"
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current && !busy) close(); // the backdrop
      }}
    >
      <DialogHeader id="rename-title" title={`Renomear ${name}`} onClose={close} disabled={busy} />
      <form className="dialog-form" onSubmit={submit} noValidate>
        <div className="dialog-body">
          <p className="dialog-lead">
            Só o nome muda: o agente mantém a conta, o comentário e o magic (as posições continuam
            dele), as ordens, as decisões, os resultados e os registros ainda não enviados.
          </p>
          <div className="field" data-invalid={touched && invalid ? "" : undefined}>
            <label className="field-label" htmlFor="rename-name">
              Novo nome
            </label>
            <input
              id="rename-name"
              ref={field}
              className="input mono"
              aria-invalid={touched && invalid !== null}
              aria-describedby="rename-hint"
              value={value}
              maxLength={64}
              autoComplete="off"
              spellCheck={false}
              disabled={busy}
              onChange={(e) => setValue(e.target.value)}
              onBlur={() => setTouched(true)}
            />
            <p className={touched && invalid ? "field-error" : "field-hint"} id="rename-hint">
              {touched && invalid
                ? invalid
                : "O novo nome vale no painel, no Telegram (/agent) e nos endereços."}
            </p>
          </div>
          {error && (
            <Alert tone="error" title="Não foi possível renomear o agente">
              {error}
            </Alert>
          )}
        </div>
        <div className="dialog-foot">
          <button type="button" className="btn" onClick={close} disabled={busy}>
            Cancelar
          </button>
          <button type="submit" className="btn btn-primary" disabled={busy || unchanged}>
            <Icon name="rename" size={14} />
            {busy ? "Renomeando…" : "Renomear"}
          </button>
        </div>
      </form>
    </dialog>
  );
}
