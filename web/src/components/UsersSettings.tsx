import { type FormEvent, useState } from "react";
import { createUser, deleteUser, updateUser } from "../api/client";
import type { UserInfo } from "../api/types";
import { formatTime } from "../lib/format";
import { passwordError, usernameError } from "../lib/settings";
import { ConfirmButton } from "./ConfirmButton";
import { Icon } from "./Icon";
import { PageHeader } from "./PageHeader";
import type { SectionIntro } from "./SettingsPage";
import { Alert } from "./States";
import { Switch } from "./Switch";

interface Props {
  /** The title of the section and what it is for. */
  intro: SectionIntro;
  users: UserInfo[];
  /** The user of the session, told apart. */
  current: string;
  /** Loads the users again, one changed. */
  onChange: () => void;
}

/**
 * The users of the dashboard: enabled or not, their last login, a new
 * password, and new ones. The passwords are kept as bcrypt hashes; a user
 * disabled, deleted or given a new password is logged out.
 */
export function UsersSettings({ intro, users, current, onChange }: Props) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
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
    const invalid = usernameError(username) ?? passwordError(password);
    if (invalid) {
      setError(invalid);
      return;
    }
    if (await run(() => createUser({ username, password }))) {
      setUsername("");
      setPassword("");
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
      <PageHeader title={intro.title} description={intro.description} />
      {error && (
        <Alert tone="error" title="A alteração não foi feita">
          {error}
        </Alert>
      )}
      <section className="card card-flush" aria-labelledby="users-title">
        <header className="card-header">
          <h3 id="users-title" className="card-title">
            Usuários do painel
            <span className="card-count">{users.length}</span>
          </h3>
        </header>
        <div className="table-wrap">
          <table className="table table-responsive">
            <thead>
              <tr>
                <th scope="col">Usuário</th>
                <th scope="col">Ativo</th>
                <th scope="col">Último login (UTC)</th>
                <th scope="col">Criado em (UTC)</th>
                <th scope="col" className="col-actions">
                  <span className="sr-only">Ações</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {users.map((user) => (
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
                  <td data-label="Ativo">
                    <Switch
                      label={user.enabled ? "Sim" : "Não"}
                      checked={user.enabled}
                      disabled={busy}
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
                  <td className="cell-actions">
                    <div className="row-actions">
                      {changing === user.id ? (
                        <form className="inline-form" onSubmit={(e) => savePassword(e, user)}>
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
                </tr>
              ))}
            </tbody>
          </table>
        </div>
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
              placeholder="8 caracteres ao menos"
              autoComplete="new-password"
              value={password}
              disabled={busy}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          <button
            type="submit"
            className="btn btn-primary"
            disabled={busy || username === "" || password === ""}
          >
            <Icon name="plus" />
            Criar usuário
          </button>
        </form>
      </section>
    </div>
  );
}
