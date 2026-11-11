import { type FormEvent, useEffect, useRef, useState } from "react";
import { createRole, deleteRole, updateRole } from "../api/client";
import type { PermissionArea, RoleInfo, RoleRequest } from "../api/types";
import {
  type AreaChoice,
  choicesOf,
  impliedBy,
  permissionsOf,
  roleNameError,
  roleSummary,
  setChoice,
} from "../lib/roles";
import { ConfirmButton } from "./ConfirmButton";
import { DialogHeader } from "./Dialog";
import { Icon } from "./Icon";
import { Alert } from "./States";

interface Props {
  roles: RoleInfo[];
  /** The catalog of the permissions, by area. */
  areas: PermissionArea[];
  /** Whether the user may change the roles. */
  manage: boolean;
  /** Loads the roles again, one changed. */
  onChange: () => void;
}

/**
 * The roles of the users of the dashboard: what each grants, area by area, and
 * how many users hold it; created, changed and deleted by a user allowed to.
 * The fixed role of the administrators grants everything and never changes;
 * a role held isn't deleted.
 */
export function RolesSettings({ roles, areas, manage, onChange }: Props) {
  const [editing, setEditing] = useState<RoleInfo | "new" | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const remove = async (role: RoleInfo) => {
    setBusy(true);
    setError(null);
    try {
      await deleteRole(role.id);
      onChange();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const save = async (request: RoleRequest) => {
    if (editing === "new") await createRole(request);
    else if (editing) await updateRole(editing.id, request);
    onChange();
  };

  return (
    <section className="card card-flush" aria-labelledby="roles-title">
      <header className="card-header">
        <div className="card-heading">
          <span className="eyebrow">Permissões</span>
          <h3 id="roles-title" className="card-title">
            Papéis
            <span className="card-count">{roles.length}</span>
          </h3>
        </div>
        {manage && (
          <button
            type="button"
            className="btn btn-sm btn-primary"
            onClick={() => {
              setError(null);
              setEditing("new");
            }}
          >
            <Icon name="plus" size={14} />
            Novo papel
          </button>
        )}
      </header>
      {error && (
        <Alert tone="error" title="Não foi possível excluir o papel">
          {error}
        </Alert>
      )}
      <div className="table-wrap">
        <table className="table table-responsive">
          <thead>
            <tr>
              <th scope="col">Papel</th>
              <th scope="col">Permissões</th>
              <th scope="col" className="num">
                Usuários
              </th>
              {manage && (
                <th scope="col" className="col-actions">
                  <span className="sr-only">Ações</span>
                </th>
              )}
            </tr>
          </thead>
          <tbody>
            {roles.map((role) => (
              <tr key={role.id}>
                <td className="strong" data-label="Papel">
                  <span className="role-cell">
                    <span>
                      {role.name}
                      {role.fixed && (
                        <span
                          className="tag"
                          title="Tem todas as permissões e não pode ser alterado"
                        >
                          Fixo
                        </span>
                      )}
                    </span>
                    {role.description && <span className="role-desc">{role.description}</span>}
                  </span>
                </td>
                <td data-label="Permissões" className="role-summary">
                  {role.fixed ? "Todas as permissões" : roleSummary(role.permissions, areas)}
                </td>
                <td className="num mono" data-label="Usuários">
                  {role.users}
                </td>
                {manage && (
                  <td className="cell-actions">
                    {!role.fixed && (
                      <div className="row-actions">
                        <button
                          type="button"
                          className="btn btn-xs"
                          aria-label={`Editar o papel ${role.name}`}
                          disabled={busy}
                          onClick={() => {
                            setError(null);
                            setEditing(role);
                          }}
                        >
                          <Icon name="edit" size={13} />
                          Editar
                        </button>
                        <ConfirmButton
                          label="Excluir"
                          ariaLabel={`Excluir o papel ${role.name}`}
                          question={`Excluir ${role.name}?`}
                          disabled={busy || role.users > 0}
                          title={
                            role.users > 0
                              ? "Troque o papel dos usuários dele antes de excluí-lo"
                              : undefined
                          }
                          onConfirm={() => remove(role)}
                        />
                      </div>
                    )}
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {editing && (
        <RoleDialog
          role={editing === "new" ? null : editing}
          areas={areas}
          onSave={save}
          onClose={() => setEditing(null)}
        />
      )}
    </section>
  );
}

/**
 * A role created or changed in a modal dialog: its name, its description and
 * its permissions as a matrix of the areas; it stays open with what was typed
 * on a refusal.
 */
function RoleDialog({
  role,
  areas,
  onSave,
  onClose,
}: {
  role: RoleInfo | null;
  areas: PermissionArea[];
  onSave: (request: RoleRequest) => Promise<void>;
  onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const field = useRef<HTMLInputElement>(null);
  const [name, setName] = useState(role?.name ?? "");
  const [description, setDescription] = useState(role?.description ?? "");
  const [choices, setChoices] = useState<Record<string, AreaChoice>>(() =>
    choicesOf(role?.permissions ?? [], areas),
  );
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const invalid = roleNameError(name);

  useEffect(() => {
    const element = dialog.current;
    if (element && !element.open) element.showModal();
    field.current?.focus();
  }, []);

  const close = () => dialog.current?.close();
  const choose = (area: PermissionArea, which: keyof AreaChoice, on: boolean) =>
    setChoices((current) => setChoice(current, area, which, on));

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setTouched(true);
    if (busy || invalid) return;
    setBusy(true);
    setError(null);
    try {
      await onSave({
        name: name.trim(),
        description: description.trim(),
        permissions: permissionsOf(choices, areas),
      });
      close();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  const title = role ? `Editar ${role.name}` : "Novo papel";
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc closes the dialog natively
    <dialog
      ref={dialog}
      className="dialog"
      aria-labelledby="role-title"
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current && !busy) close(); // the backdrop
      }}
    >
      <DialogHeader id="role-title" title={title} onClose={close} disabled={busy} />
      <form className="dialog-form" onSubmit={submit} noValidate>
        <div className="dialog-body">
          <div className="field" data-invalid={touched && invalid ? "" : undefined}>
            <label className="field-label" htmlFor="role-name">
              Nome
            </label>
            <input
              id="role-name"
              ref={field}
              className="input"
              aria-invalid={touched && invalid !== null}
              aria-describedby="role-name-hint"
              value={name}
              maxLength={64}
              autoComplete="off"
              disabled={busy}
              onChange={(e) => setName(e.target.value)}
              onBlur={() => setTouched(true)}
            />
            <p className={touched && invalid ? "field-error" : "field-hint"} id="role-name-hint">
              {touched && invalid ? invalid : "Ex.: Observador, Operador."}
            </p>
          </div>
          <div className="field">
            <label className="field-label" htmlFor="role-description">
              Descrição
            </label>
            <input
              id="role-description"
              className="input"
              value={description}
              maxLength={280}
              autoComplete="off"
              disabled={busy}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
          <fieldset className="role-matrix-field">
            <legend className="field-label">Permissões</legend>
            <p className="field-hint">
              Gerenciar inclui ver. Operar e usar o chat incluem ver o painel. O que não estiver
              marcado fica escondido e é recusado pelo servidor.
            </p>
            <div className="table-wrap">
              <table className="table role-matrix">
                <thead>
                  <tr>
                    <th scope="col">Área</th>
                    <th scope="col">Ver</th>
                    <th scope="col">Gerenciar</th>
                  </tr>
                </thead>
                <tbody>
                  {areas.map((area) => {
                    const choice = choices[area.key] ?? { view: false, manage: false };
                    const implied = area.view ? impliedBy(area.view, choices, areas) : undefined;
                    return (
                      <tr key={area.key}>
                        <th scope="row">
                          <span className="role-area">{area.label}</span>
                          <span className="role-desc">{area.hint}</span>
                        </th>
                        <td>
                          {area.view && (
                            <label
                              className="hours-check"
                              title={implied ? `Incluído por ${implied}` : undefined}
                            >
                              <input
                                type="checkbox"
                                checked={choice.view || !!implied}
                                disabled={busy || !!implied}
                                aria-label={`${area.label}: ${area.view_label}`}
                                onChange={(e) => choose(area, "view", e.target.checked)}
                              />
                              {area.view_label}
                            </label>
                          )}
                        </td>
                        <td>
                          {area.manage && (
                            <label className="hours-check">
                              <input
                                type="checkbox"
                                checked={choice.manage}
                                disabled={busy}
                                aria-label={`${area.label}: ${area.manage_label}`}
                                onChange={(e) => choose(area, "manage", e.target.checked)}
                              />
                              {area.manage_label}
                            </label>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </fieldset>
          {error && (
            <Alert tone="error" title="Não foi possível salvar o papel">
              {error}
            </Alert>
          )}
        </div>
        <div className="dialog-foot">
          <button type="button" className="btn" onClick={close} disabled={busy}>
            Cancelar
          </button>
          <button type="submit" className="btn btn-primary" disabled={busy}>
            <Icon name="success" size={14} />
            {busy ? "Salvando…" : "Salvar"}
          </button>
        </div>
      </form>
    </dialog>
  );
}
