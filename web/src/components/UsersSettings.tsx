import { type FormEvent, useState } from "react";
import { createUser, deleteUser, updateUser } from "../api/client";
import type { UserInfo } from "../api/types";
import { formatTime } from "../lib/format";
import { passwordError, usernameError } from "../lib/settings";
import { ConfirmButton } from "./ConfirmButton";
import { Switch } from "./Switch";

interface Props {
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
export function UsersSettings({ users, current, onChange }: Props) {
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
    <div className="card settings-users">
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Usuário</th>
              <th>Ativo</th>
              <th>Último login</th>
              <th>Criado em</th>
              <th>
                <span className="sr-only">Ações</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {users.map((user) => (
              <tr key={user.id}>
                <td>
                  {user.username}
                  {user.username === current && <small className="settings-you"> (você)</small>}
                </td>
                <td>
                  <Switch
                    label={user.enabled ? "Sim" : "Não"}
                    checked={user.enabled}
                    disabled={busy}
                    onChange={(enabled) => run(() => updateUser(user.id, { enabled }))}
                  />
                </td>
                <td className="mono">
                  {user.last_login_at ? formatTime(user.last_login_at) : "—"}
                </td>
                <td className="mono">{formatTime(user.created_at)}</td>
                <td>
                  <div className="settings-actions">
                    {changing === user.id ? (
                      <form className="secret-form" onSubmit={(e) => savePassword(e, user)}>
                        <input
                          type="password"
                          className="settings-input"
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
                          className="button primary"
                          disabled={busy || newPassword === ""}
                        >
                          Salvar
                        </button>
                        <button
                          type="button"
                          className="button"
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
                          className="mini-btn"
                          disabled={busy}
                          onClick={() => {
                            setChanging(user.id);
                            setNewPassword("");
                            setError(null);
                          }}
                        >
                          Trocar senha
                        </button>
                        <ConfirmButton
                          label="Excluir"
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
      <form className="settings-form" onSubmit={create}>
        <input
          className="settings-input"
          aria-label="Usuário"
          placeholder="Usuário"
          autoCapitalize="none"
          autoComplete="off"
          spellCheck={false}
          value={username}
          disabled={busy}
          onChange={(e) => setUsername(e.target.value.trim())}
        />
        <input
          type="password"
          className="settings-input"
          aria-label="Senha"
          placeholder="Senha (8 caracteres ao menos)"
          autoComplete="new-password"
          value={password}
          disabled={busy}
          onChange={(e) => setPassword(e.target.value)}
        />
        <button
          type="submit"
          className="button"
          disabled={busy || username === "" || password === ""}
        >
          Criar usuário
        </button>
      </form>
      {error && (
        <p className="control-feedback error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
