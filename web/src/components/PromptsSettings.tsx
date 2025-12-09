import { useState } from "react";
import { deletePrompt } from "../api/client";
import type { PromptInfo } from "../api/types";
import { formatTime } from "../lib/format";
import { ConfirmButton } from "./ConfirmButton";
import { PromptEditor } from "./PromptEditor";

interface Props {
  prompts: PromptInfo[];
  /** Loads the prompts again, one created, edited or deleted. */
  onChange: () => void;
}

/** What the editor shows: a new prompt, or the one of an ID. */
type Editing = { id: number } | "new" | null;

/**
 * The prompts of the LLM agents: their version, when they changed and the
 * agents using them, edited in a dialog; an edit reaches the agents using the
 * prompt from their next analysis.
 */
export function PromptsSettings({ prompts, onChange }: Props) {
  const [editing, setEditing] = useState<Editing>(null);
  const [error, setError] = useState<string | null>(null);

  const remove = async (prompt: PromptInfo) => {
    setError(null);
    try {
      await deletePrompt(prompt.id);
      onChange();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <>
      <div className="settings-toolbar">
        <p className="settings-hint">
          O cabeçalho do prompt (<code># Indicators:</code>, <code># Take profit:</code>) define os
          indicadores e o take profit do agente.
        </p>
        <button type="button" className="button primary" onClick={() => setEditing("new")}>
          Novo prompt
        </button>
      </div>
      {error && (
        <p className="control-feedback error" role="alert">
          {error}
        </p>
      )}
      <div className="card">
        {prompts.length === 0 ? (
          <p className="empty">Nenhum prompt: rode o migrator (up) para os prompts de exemplo.</p>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Nome</th>
                  <th>Descrição</th>
                  <th className="num">Versão</th>
                  <th>Alterado em</th>
                  <th>Usado por</th>
                  <th>
                    <span className="sr-only">Ações</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {prompts.map((prompt) => (
                  <tr key={prompt.id}>
                    <td className="mono">{prompt.name}</td>
                    <td className="settings-description" title={prompt.description}>
                      {prompt.description || "—"}
                    </td>
                    <td className="num mono">v{prompt.version}</td>
                    <td className="mono">{formatTime(prompt.updated_at)}</td>
                    <td>{prompt.used_by.length > 0 ? prompt.used_by.join(", ") : "—"}</td>
                    <td>
                      <div className="settings-actions">
                        <button
                          type="button"
                          className="mini-btn"
                          onClick={() => setEditing({ id: prompt.id })}
                        >
                          Editar
                        </button>
                        <ConfirmButton
                          label="Excluir"
                          question={`Excluir ${prompt.name}?`}
                          disabled={prompt.used_by.length > 0}
                          title={
                            prompt.used_by.length > 0
                              ? "Em uso: troque o prompt dos agentes antes"
                              : undefined
                          }
                          onConfirm={() => remove(prompt)}
                        />
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      {editing && (
        <PromptEditor
          id={editing === "new" ? null : editing.id}
          onSaved={onChange}
          onClose={() => setEditing(null)}
        />
      )}
    </>
  );
}
