import { useId, useState } from "react";
import { deletePrompt, fetchPromptsPage } from "../api/client";
import type { PromptInfo } from "../api/types";
import { PermPromptsManage } from "../api/types";
import { formatTime } from "../lib/format";
import { searchNote } from "../lib/pagination";
import { useCan } from "./AccessContext";
import { ConfirmButton } from "./ConfirmButton";
import { Icon } from "./Icon";
import { PageHeader } from "./PageHeader";
import { ListPages } from "./Paginator";
import { PromptEditor } from "./PromptEditor";
import { SearchField } from "./SearchField";
import type { SectionIntro } from "./SettingsPage";
import { Alert, EmptyState, Loading, Skeleton } from "./States";
import { usePagedList } from "./usePagedList";

interface Props {
  /** The title of the section and what it is for. */
  intro: SectionIntro;
  /** Tells the page a prompt was created, edited or deleted, eg: the agents using each. */
  onChange: () => void;
}

/** The prompts, as a search counts them. */
const noun = { one: "prompt", many: "prompts" };

/** What the editor shows: a new prompt, or the one of an ID. */
type Editing = { id: number } | "new" | null;

/**
 * The prompts of the LLM agents, a page at a time, searched by their names,
 * descriptions and the agents using them: their version, when they changed
 * and the agents using them, edited in a dialog; an edit reaches the agents
 * using the prompt from their next analysis.
 */
export function PromptsSettings({ intro, onChange }: Props) {
  const manage = useCan(PermPromptsManage); // changed by a user allowed to only, read by the others
  const [editing, setEditing] = useState<Editing>(null);
  const [error, setError] = useState<string | null>(null);
  const list = usePagedList((params, signal) =>
    fetchPromptsPage(params, signal).then((r) => ({ rows: r.prompts, page: r })),
  );
  const listID = useId();
  const changed = () => {
    list.reload();
    onChange();
  };

  const remove = async (prompt: PromptInfo) => {
    setError(null);
    try {
      await deletePrompt(prompt.id);
      changed();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const create = manage ? (
    <button type="button" className="btn btn-primary" onClick={() => setEditing("new")}>
      <Icon name="plus" />
      Novo prompt
    </button>
  ) : undefined;

  return (
    <div className="settings-section">
      <PageHeader
        eyebrow="Configurações"
        title={intro.title}
        description={intro.description}
        actions={create}
      />
      {error && (
        <Alert tone="error" title="Não foi possível excluir o prompt">
          {error}
        </Alert>
      )}
      {list.error && (
        <Alert tone="error" title="Não foi possível carregar os prompts">
          {list.error}
        </Alert>
      )}
      <div className="card card-flush">
        {list.rows === null || list.info === null ? (
          !list.error && (
            <Loading label="Carregando…">
              <Skeleton height={220} />
            </Loading>
          )
        ) : list.info.total === 0 && list.search === "" ? (
          <EmptyState icon="prompt" title="Nenhum prompt ainda" action={create}>
            Sem prompt, os agentes LLM usam o embutido. Para os prompts de exemplo, rode o migrator
            (up).
          </EmptyState>
        ) : (
          <>
            <div className="list-toolbar">
              <SearchField
                value={list.query}
                onChange={list.setQuery}
                label="Buscar prompts"
                placeholder="Buscar por nome, descrição ou agente"
                controls={listID}
                note={searchNote(list.info.total, list.search, noun)}
              />
            </div>
            {list.rows.length > 0 && (
              <div className="table-wrap" id={listID}>
                <table className="table table-responsive">
                  <thead>
                    <tr>
                      <th scope="col">Nome</th>
                      <th scope="col">Descrição</th>
                      <th scope="col" className="num">
                        Versão
                      </th>
                      <th scope="col">Alterado em (UTC)</th>
                      <th scope="col">Usado por</th>
                      <th scope="col" className="col-actions">
                        <span className="sr-only">Ações</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {list.rows.map((prompt) => (
                      <tr key={prompt.id}>
                        <td className="mono strong" data-label="Nome">
                          {prompt.name}
                        </td>
                        <td
                          className="cell-truncate cell-fill"
                          data-label="Descrição"
                          title={prompt.description}
                        >
                          {prompt.description || "—"}
                        </td>
                        <td className="num mono" data-label="Versão">
                          v{prompt.version}
                        </td>
                        <td className="mono" data-label="Alterado em (UTC)">
                          {formatTime(prompt.updated_at)}
                        </td>
                        <td data-label="Usado por">
                          {prompt.used_by.length > 0 ? (
                            <span className="tag-list">
                              {prompt.used_by.map((name) => (
                                <span key={name} className="tag tag-mono">
                                  {name}
                                </span>
                              ))}
                            </span>
                          ) : (
                            <span className="muted">—</span>
                          )}
                        </td>
                        <td className="cell-actions">
                          <div className="row-actions">
                            <button
                              type="button"
                              className="btn btn-xs"
                              aria-label={`${manage ? "Editar" : "Ver"} ${prompt.name}`}
                              onClick={() => setEditing({ id: prompt.id })}
                            >
                              <Icon name={manage ? "edit" : "eye"} size={13} />
                              {manage ? "Editar" : "Ver"}
                            </button>
                            {manage && (
                              <ConfirmButton
                                label="Excluir"
                                ariaLabel={`Excluir ${prompt.name}`}
                                question={`Excluir ${prompt.name}?`}
                                disabled={prompt.used_by.length > 0}
                                title={
                                  prompt.used_by.length > 0
                                    ? "Em uso: troque o prompt dos agentes antes"
                                    : undefined
                                }
                                onConfirm={() => remove(prompt)}
                              />
                            )}
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            <div className="list-foot">
              <ListPages info={list.info} onPage={list.setPage} />
            </div>
          </>
        )}
      </div>
      {editing && (
        <PromptEditor
          id={editing === "new" ? null : editing.id}
          onSaved={changed}
          onClose={() => setEditing(null)}
        />
      )}
    </div>
  );
}
