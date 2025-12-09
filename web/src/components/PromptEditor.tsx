import { type FormEvent, useEffect, useRef, useState } from "react";
import { APIError, createPrompt, fetchPrompt, updatePrompt } from "../api/client";
import type { PromptDetail } from "../api/types";
import { promptNameError } from "../lib/settings";

interface Props {
  /** The prompt edited, null for a new one. */
  id: number | null;
  onSaved: () => void;
  onClose: () => void;
}

const PLACEHOLDER = `# nome: o que a estratégia opera
# Indicators: ema 20 50, atr
# Take profit: optional

Setup: …`;

/**
 * Creates or edits a prompt in a modal dialog: its description and its
 * content, the header lines included. An edit is made on a version, refused
 * when the prompt changed since, eg: from Telegram.
 */
export function PromptEditor({ id, onSaved, onClose }: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [prompt, setPrompt] = useState<PromptDetail | null>(null);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [content, setContent] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const element = dialog.current;
    if (element && !element.open) element.showModal();
  }, []);

  useEffect(() => {
    if (id === null) return;
    const controller = new AbortController();
    fetchPrompt(id, controller.signal)
      .then((p) => {
        setPrompt(p);
        setDescription(p.description);
        setContent(p.content);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      });
    return () => controller.abort();
  }, [id]);

  const close = () => dialog.current?.close();
  const loading = id !== null && prompt === null;
  const nameError = id === null && name !== "" ? promptNameError(name) : null;

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy || loading) return;
    setBusy(true);
    setError(null);
    try {
      if (id === null) {
        await createPrompt({ name, description, content });
      } else if (prompt) {
        await updatePrompt(id, { description, content, version: prompt.version });
      }
      onSaved();
      close();
    } catch (e) {
      if (e instanceof APIError && e.status === 409 && id !== null) {
        setError(
          "O prompt foi alterado em outro lugar desde que foi aberto: feche e abra de novo para editar a versão atual.",
        );
      } else {
        setError(e instanceof Error ? e.message : String(e));
      }
      setBusy(false);
    }
  };

  const title = id === null ? "Novo prompt" : `Editar ${prompt?.name ?? "prompt"}`;
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc closes the dialog natively
    <dialog
      ref={dialog}
      className="decision-dialog prompt-dialog"
      aria-labelledby="prompt-title"
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current && !busy) close(); // the backdrop
      }}
    >
      <header className="decision-head">
        <h2 id="prompt-title">
          {title}
          {prompt && <small className="prompt-version"> v{prompt.version}</small>}
        </h2>
        <button type="button" className="mini-btn" onClick={close} aria-label="Fechar">
          ✕
        </button>
      </header>
      <form className="decision-body" onSubmit={submit}>
        {id === null && (
          <label className="settings-label">
            Nome
            <input
              className="settings-input mono"
              value={name}
              autoCapitalize="none"
              spellCheck={false}
              placeholder="ex.: trend_following"
              disabled={busy}
              onChange={(e) => setName(e.target.value.trim())}
            />
            {nameError && <span className="control-feedback error">{nameError}</span>}
          </label>
        )}
        <label className="settings-label">
          Descrição
          <input
            className="settings-input"
            value={description}
            disabled={busy || loading}
            onChange={(e) => setDescription(e.target.value)}
          />
        </label>
        <label className="settings-label">
          Conteúdo
          <textarea
            className="reanalysis-note prompt-content mono"
            value={content}
            rows={18}
            spellCheck={false}
            placeholder={loading ? "Carregando…" : PLACEHOLDER}
            disabled={busy || loading}
            onChange={(e) => setContent(e.target.value)}
          />
        </label>
        {prompt && prompt.used_by.length > 0 && (
          <p className="settings-hint">
            Usado por {prompt.used_by.join(", ")}: a nova versão vale a partir da próxima análise de
            cada um.
          </p>
        )}
        {error && (
          <p className="control-feedback error" role="alert">
            {error}
          </p>
        )}
        <div className="reanalysis-actions">
          <button type="button" className="button" onClick={close} disabled={busy}>
            Cancelar
          </button>
          <button
            type="submit"
            className="button primary"
            disabled={
              busy ||
              loading ||
              content.trim() === "" ||
              (id === null && (name === "" || nameError !== null))
            }
          >
            {busy ? "Salvando…" : "Salvar"}
          </button>
        </div>
      </form>
    </dialog>
  );
}
