import { type FormEvent, useEffect, useRef, useState } from "react";
import { APIError, createPrompt, fetchPrompt, updatePrompt } from "../api/client";
import type { PromptDetail } from "../api/types";
import { promptNameError } from "../lib/settings";
import { DialogHeader } from "./Dialog";
import { Alert } from "./States";

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
          "O prompt mudou em outro lugar depois de aberto. Feche e abra de novo para editar a versão atual.",
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
      className="dialog dialog-lg"
      aria-labelledby="prompt-title"
      onClose={onClose}
      onCancel={(e) => {
        if (busy) e.preventDefault(); // Esc waits for the answer
      }}
      onClick={(e) => {
        if (e.target === dialog.current && !busy) close(); // the backdrop
      }}
    >
      <DialogHeader
        id="prompt-title"
        title={title}
        subtitle={prompt ? `v${prompt.version}` : undefined}
        onClose={close}
        disabled={busy}
      />
      <form className="dialog-form" onSubmit={submit}>
        <div className="dialog-body">
          {id === null && (
            <div className="field" data-invalid={nameError ? "" : undefined}>
              <label className="field-label" htmlFor="prompt-name">
                Nome
              </label>
              <input
                id="prompt-name"
                className="input mono"
                value={name}
                autoCapitalize="none"
                spellCheck={false}
                placeholder="ex.: trend_following"
                disabled={busy}
                aria-invalid={nameError ? true : undefined}
                aria-describedby={nameError ? "prompt-name-error" : "prompt-name-hint"}
                onChange={(e) => setName(e.target.value.trim())}
              />
              {nameError ? (
                <span id="prompt-name-error" className="field-error">
                  {nameError}
                </span>
              ) : (
                <span id="prompt-name-hint" className="field-hint">
                  Não muda depois de criado.
                </span>
              )}
            </div>
          )}
          <div className="field">
            <label className="field-label" htmlFor="prompt-description">
              Descrição
            </label>
            <input
              id="prompt-description"
              className="input"
              value={description}
              disabled={busy || loading}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
          <div className="field">
            <label className="field-label" htmlFor="prompt-content">
              Conteúdo
            </label>
            <textarea
              id="prompt-content"
              className="textarea mono prompt-content"
              value={content}
              rows={18}
              spellCheck={false}
              placeholder={loading ? "Carregando…" : PLACEHOLDER}
              disabled={busy || loading}
              aria-describedby="prompt-content-hint"
              onChange={(e) => setContent(e.target.value)}
            />
            <span id="prompt-content-hint" className="field-hint">
              As linhas do cabeçalho (<code># Indicators:</code>, <code># Take profit:</code>,{" "}
              <code># Pending orders:</code>, <code># Drawings:</code>, <code># Context:</code>)
              configuram o agente que usa o prompt.
            </span>
          </div>
          {prompt && prompt.used_by.length > 0 && (
            <Alert tone="info">
              Usado por {prompt.used_by.join(", ")}: a nova versão vale a partir da próxima análise
              de cada um.
            </Alert>
          )}
          {error && (
            <Alert tone="error" title="Não foi possível salvar o prompt">
              {error}
            </Alert>
          )}
        </div>
        <div className="dialog-foot">
          <button type="button" className="btn" onClick={close} disabled={busy}>
            Cancelar
          </button>
          <button
            type="submit"
            className="btn btn-primary"
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
