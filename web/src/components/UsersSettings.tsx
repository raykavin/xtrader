import { type FormEvent, type ReactNode, useEffect, useId, useState } from "react";
import { createUser, deleteUser, fetchUsersPage, updateUser } from "../api/client";
import { PermUsersManage, type RoleInfo, type UserInfo } from "../api/types";
import { formatTime } from "../lib/format";
import { searchNote } from "../lib/pagination";
import { passwordError, usernameError } from "../lib/settings";
import { useCan } from "./AccessContext";
import { ConfirmButton } from "./ConfirmButton";
import { Icon } from "./Icon";
import { PageHeader } from "./PageHeader";
import { ListPages } from "./Paginator";
import { SearchField } from "./SearchField";
import { Select } from "./Select";
import type { SectionIntro } from "./SettingsPage";
import { Alert, Loading, Skeleton } from "./States";
import { Switch } from "./Switch";
import { usePagedList } from "./usePagedList";

interface Props {
  /** The title of the section and what it is for. */
  intro: SectionIntro;
  /** The roles a user may be given, all of them. */
  roles: RoleInfo[];
  /** The user of the session, told apart. */
  current: string;
  /** Tells the page a user changed, eg: the users of each role. */
  onChange: () => void;
  /** Changes of the users or of the roles, read again as it grows. */
  version: number;
  /** The roles, after the users, eg: the card of the roles. */
  children?: ReactNode;
}

/** The users, as a search counts them. */
const noun = { one: "usuário", many: "usuários" };

/** The value of the option without a role, the user then refused at the login. */
const NO_ROLE = "0";

/**
 * The users of the dashboard, a page at a time, searched by their usernames
 * and roles: their roles, enabled or not, their last login, a new password,
 * and new ones, each with its role. The passwords are kept as bcrypt hashes;
 * a user disabled, deleted, given a new password or left without a role is
 * logged out. Changed by a user allowed to only.
 */
export function UsersSettings({ intro, roles, current, onChange, version, children }: Props) {
  const manage = useCan(PermUsersManage);
  const list = usePagedList((params, signal) =>
    fetchUsersPage(params, signal).then((r) => ({ rows: r.users, page: r })),
  );
  const listID = useId();
  const { reload } = list;
  useEffect(() => {
    if (version > 0) reload();
  }, [version, reload]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [roleID, setRoleID] = useState<string | null>(null);
  const roleOptions = roles.map((role) => ({
    value: String(role.id),
    label: role.name,
    hint: role.fixed ? "todas as permissões" : undefined,
  }));
  const [changing, setChanging] = useState<number | null>(null);
  const [newPassword, setNewPassword] = useState("");

  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await action();
      onChange();
      return true;
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return false;
    } finally {
      setBusy(false);
    }
  };
  const create = async (event: FormEvent) => {
    event.preventDefault();
    const invalid =
      usernameError(username) ??
      passwordError(password) ??
      (roleID === null ? "Escolha o papel do usuário: sem um papel ele não entra." : null);
    if (invalid) {
      setError(invalid);
      return;
    }
    if (await run(() => createUser({ username, password, role_id: Number(roleID) }))) {
      setUsername("");
      setPassword("");
      setRoleID(null);
    }
  };
  /** Gives user the role of value: none logs it out, the user of the session then sent to the login. */
  const changeRole = async (user: UserInfo, value: string) => {
    if (await run(() => updateUser(user.id, { role_id: Number(value) }))) {
      if (user.username === current) {
        window.location.assign(value === NO_ROLE ? "/login" : window.location.href);
      }
    }
  };
  const savePassword = async (event: FormEvent, user: UserInfo) => {
    event.preventDefault();
    const invalid = passwordError(newPassword);
    if (invalid) {
      setError(invalid);
      return;
    }
    if (await run(() => updateUser(user.id, { password: newPassword }))) {
      setChanging(null);
      setNewPassword("");
      if (user.username === current) window.location.assign("/login"); // its sessions closed
    }
  };

  return (
    <div className="settings-section">
      <PageHeader eyebrow="Configurações" title={intro.title} description={intro.description} />
      {error && (
        <Alert tone="error" title="Não foi possível salvar a alteração">
          {error}
        </Alert>
      )}
      {list.error && (
        <Alert tone="error" title="Não foi possível carregar os usuários">
          {list.error}
        </Alert>
      )}
      <section className="card card-flush" aria-labelledby="users-title">
        <header className="card-header">
          <div className="card-heading">
            <span className="eyebrow">Acesso</span>
            <h3 id="users-title" className="card-title">
              Usuários do painel
              {list.info && list.search === "" && (
                <span className="card-count">{list.info.total}</span>
              )}
            </h3>
          </div>
        </header>
        <div className="list-toolbar">
          <SearchField
            value={list.query}
            onChange={list.setQuery}
            label="Buscar usuários do painel"
            placeholder="Buscar por usuário ou papel"
            controls={listID}
            note={list.info && searchNote(list.info.total, list.search, noun)}
          />
        </div>
        {list.rows === null
          ? !list.error && (
              <Loading label="Carregando…">
                <Skeleton height={160} />
              </Loading>
            )
          : list.rows.length > 0 && (
              <div className="table-wrap" id={listID}>
                <table className="table table-responsive">
                  <thead>
                    <tr>
                      <th scope="col">Usuário</th>
                      <th scope="col">Papel</th>
                      <th scope="col">Ativo</th>
                      <th scope="col">Último login (UTC)</th>
                      <th scope="col">Criado em (UTC)</th>
                      {manage && (
                        <th scope="col" className="col-actions">
                          <span className="sr-only">Ações</span>
                        </th>
                      )}
                    </tr>
                  </thead>
                  <tbody>
                    {list.rows.map((user) => (
                      <tr key={user.id}>
                        <td className="strong" data-label="Usuário">
                          <span className="user-cell">
                            <span className="user-avatar" aria-hidden="true">
                              {user.username.slice(0, 1).toUpperCase()}
                            </span>
                            {user.username}
                            {user.username === current && <span className="tag">você</span>}
                          </span>
                        </td>
                        <td data-label="Papel">
                          {manage ? (
                            <Select
                              caption={`Papel de ${user.username}`}
                              value={user.role ? String(user.role.id) : NO_ROLE}
                              options={[
                                ...roleOptions,
                                { value: NO_ROLE, label: "Sem papel", hint: "não entra" },
                              ]}
                              onChange={(value) => changeRole(user, value)}
                            />
                          ) : (
                            (user.role?.name ?? <span className="muted">Sem papel</span>)
                          )}
                        </td>
                        <td data-label="Ativo">
                          <Switch
                            label={user.enabled ? "Sim" : "Não"}
                            checked={user.enabled}
                            disabled={busy || !manage}
                            title={`${user.enabled ? "Desativar" : "Ativar"} ${user.username}`}
                            onChange={(enabled) => run(() => updateUser(user.id, { enabled }))}
                          />
                        </td>
                        <td className="mono" data-label="Último login (UTC)">
                          {user.last_login_at ? formatTime(user.last_login_at) : "—"}
                        </td>
                        <td className="mono" data-label="Criado em (UTC)">
                          {formatTime(user.created_at)}
                        </td>
                        {manage && (
                          <td className="cell-actions">
                            <div className="row-actions">
                              {changing === user.id ? (
                                <form
                                  className="inline-form"
                                  onSubmit={(e) => savePassword(e, user)}
                                >
                                  <input
                                    type="password"
                                    className="input"
                                    autoComplete="new-password"
                                    aria-label={`Nova senha de ${user.username}`}
                                    placeholder="Nova senha"
                                    value={newPassword}
                                    disabled={busy}
                                    onChange={(e) => setNewPassword(e.target.value)}
                                    // biome-ignore lint/a11y/noAutofocus: opened by the user to type it
                                    autoFocus
                                  />
                                  <button
                                    type="submit"
                                    className="btn btn-sm btn-primary"
                                    disabled={busy || newPassword === ""}
                                  >
                                    Salvar
                                  </button>
                                  <button
                                    type="button"
                                    className="btn btn-sm"
                                    onClick={() => {
                                      setChanging(null);
                                      setNewPassword("");
                                    }}
                                  >
                                    Cancelar
                                  </button>
                                </form>
                              ) : (
                                <>
                                  <button
                                    type="button"
                                    className="btn btn-xs"
                                    aria-label={`Trocar a senha de ${user.username}`}
                                    disabled={busy}
                                    onClick={() => {
                                      setChanging(user.id);
                                      setNewPassword("");
                                      setError(null);
                                    }}
                                  >
                                    <Icon name="key" size={13} />
                                    Trocar senha
                                  </button>
                                  <ConfirmButton
                                    label="Excluir"
                                    ariaLabel={`Excluir ${user.username}`}
                                    question={`Excluir ${user.username}?`}
                                    disabled={busy}
                                    onConfirm={() => run(() => deleteUser(user.id))}
                                  />
                                </>
                              )}
                            </div>
                          </td>
                        )}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
        <div className="list-foot">
          <ListPages info={list.info} onPage={list.setPage} />
        </div>
        {manage && (
          <form className="card-footer inline-form" onSubmit={create} aria-label="Novo usuário">
            <div className="field">
              <label className="field-label" htmlFor="new-user-name">
                Novo usuário
              </label>
              <input
                id="new-user-name"
                className="input"
                placeholder="nome de usuário"
                autoCapitalize="none"
                autoComplete="off"
                spellCheck={false}
                value={username}
                disabled={busy}
                onChange={(e) => setUsername(e.target.value.trim())}
              />
            </div>
            <div className="field">
              <label className="field-label" htmlFor="new-user-password">
                Senha
              </label>
              <input
                id="new-user-password"
                type="password"
                className="input"
                placeholder="mínimo de 8 caracteres"
                autoComplete="new-password"
                value={password}
                disabled={busy}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
            <div className="field">
              <span className="field-label">Papel</span>
              <Select
                caption="Papel do novo usuário"
                value={roleID}
                options={roleOptions}
                placeholder="Escolha o papel"
                onChange={setRoleID}
              />
            </div>
            <button
              type="submit"
              className="btn btn-primary"
              disabled={busy || username === "" || password === "" || roleID === null}
            >
              <Icon name="plus" />
              Criar usuário
            </button>
          </form>
        )}
      </section>
      {children}
    </div>
  );
}
