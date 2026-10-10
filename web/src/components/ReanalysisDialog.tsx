import { type FormEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";
import { MaxReanalysisNote } from "../api/types";
import { DialogHeader } from "./Dialog";
import { Icon } from "./Icon";
import { Alert } from "./States";

interface Props {
  pair: string;
  /** Asks the model again with the note, blank for none; rejects with why not. */
  onConfirm: (note: string) => Promise<void>;
  onClose: () => void;
}

/**
 * Asks the language model of the strategy again about the pair, with an
 * optional note of the trader for it, in a modal dialog: sent with Ctrl+Enter
 * or its button, it stays open with the note on a refusal.
 */
export function ReanalysisDialog({ pair, onConfirm, onClose }: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  const field = useRef<HTMLTextAreaElement>(null);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const element = dialog.current;
    if (element && !element.open) element.showModal();
    field.current?.focus(); // the dialog focuses its first button
  }, []);

  const close = () => dialog.current?.close();

  const submit = async (event?: FormEvent) => {
    event?.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      await onConfirm(note);
      close();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  const onKey = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      void submit();
    }
  };

  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc closes the dialog natively
    <dialog
      ref={dialog}
      className="dialog dialog-sm"
      aria-labelledby="reanalysis-title"
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current && !busy) close(); // the backdrop
      }}
    >
      <DialogHeader
        id="reanalysis-title"
        title={`Reanalisar ${pair}`}
        onClose={close}
        disabled={busy}
      />
      <form className="dialog-form" onSubmit={submit}>
        <div className="dialog-body">
          <p className="dialog-lead">
            Pede ao modelo uma nova análise de <strong>{pair}</strong> agora, fora do intervalo de
            consultas.
          </p>
          <div className="field">
            <label className="field-label" htmlFor="reanalysis-note">
              Mensagem para o agente <span className="optional">(opcional)</span>
            </label>
            <textarea
              id="reanalysis-note"
              ref={field}
              className="textarea"
              aria-describedby="reanalysis-hint"
              rows={4}
              value={note}
              maxLength={MaxReanalysisNote}
              placeholder="Ex.: encerre a posição ou proteja o lucro"
              disabled={busy}
              onChange={(e) => setNote(e.target.value)}
              onKeyDown={onKey}
            />
            <p className="field-hint split" id="reanalysis-hint">
              <span>O modelo considera a mensagem, mas decide pela estratégia e pelas regras.</span>
              <span className="mono">
                {note.length}/{MaxReanalysisNote}
              </span>
            </p>
          </div>
          {error && (
            <Alert tone="error" title="A reanálise foi recusada">
              {error}
            </Alert>
          )}
        </div>
        <div className="dialog-foot">
          <span className="dialog-foot-hint kbd-hint">Ctrl+Enter envia</span>
          <button type="button" className="btn" onClick={close} disabled={busy}>
            Cancelar
          </button>
          <button type="submit" className="btn btn-primary" disabled={busy}>
            <Icon name="sparkles" size={14} />
            {busy ? "Solicitando…" : "Reanalisar"}
          </button>
        </div>
      </form>
    </dialog>
  );
}
