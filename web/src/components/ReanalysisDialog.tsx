import { type FormEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";
import { MaxReanalysisNote } from "../api/types";

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
      className="decision-dialog reanalysis-dialog"
      aria-labelledby="reanalysis-title"
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current && !busy) close(); // the backdrop
      }}
    >
      <header className="decision-head">
        <h2 id="reanalysis-title">Reanalisar {pair}</h2>
        <button type="button" className="mini-btn" onClick={close} aria-label="Fechar">
          ✕
        </button>
      </header>
      <form className="decision-body" onSubmit={submit}>
        <label className="reanalysis-label" htmlFor="reanalysis-note">
          Mensagem para o agente (Opcional)
        </label>
        <textarea
          id="reanalysis-note"
          ref={field}
          className="reanalysis-note"
          rows={4}
          value={note}
          maxLength={MaxReanalysisNote}
          placeholder="Ex.: encerre a posição ou proteja o lucro"
          disabled={busy}
          onChange={(e) => setNote(e.target.value)}
          onKeyDown={onKey}
        />
        <p className="reanalysis-hint">
          O modelo considera a mensagem, mas decide pela estratégia e pelas regras.{" "}
          <span className="mono">
            {note.length}/{MaxReanalysisNote}
          </span>
        </p>
        {error && <div className="banner error">{error}</div>}
        <div className="reanalysis-actions">
          <button type="button" className="button" onClick={close} disabled={busy}>
            Cancelar
          </button>
          <button type="submit" className="button primary" disabled={busy}>
            {busy ? "Solicitando…" : "Reanalisar"}
          </button>
        </div>
      </form>
    </dialog>
  );
}
