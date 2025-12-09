import { type FormEvent, useState } from "react";
import { addTelegramUser, deleteTelegramUser, updateTelegramSettings } from "../api/client";
import type { TelegramSettingsResponse } from "../api/types";
import { parseTelegramID } from "../lib/settings";
import { ConfirmButton } from "./ConfirmButton";
import { SecretField } from "./SecretField";
import { StatusIcon } from "./StatusIcon";
import { Switch } from "./Switch";

interface Props {
  telegram: TelegramSettingsResponse;
  onChange: (telegram: TelegramSettingsResponse) => void;
}

/**
 * The Telegram bot of the controller: on or off, its token (written, never
 * read), and the users it answers, the only ones who reach the agents and the
 * prompts from Telegram.
 */
export function TelegramSettings({ telegram, onChange }: Props) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [userID, setUserID] = useState("");
  const [name, setName] = useState("");

  const run = async (action: () => Promise<TelegramSettingsResponse>) => {
    setBusy(true);
    setError(null);
    try {
      onChange(await action());
      return true;
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return false;
    } finally {
      setBusy(false);
    }
  };
  const id = parseTelegramID(userID);
  const add = async (event: FormEvent) => {
    event.preventDefault();
    if (id === null) return;
    if (await run(() => addTelegramUser({ telegram_id: id, name: name.trim() }))) {
      setUserID("");
      setName("");
    }
  };

  return (
    <div className="card settings-telegram">
      <div className="settings-switches">
        <Switch
          label="Bot ativo"
          checked={telegram.enabled}
          disabled={busy}
          onChange={(enabled) => run(() => updateTelegramSettings({ enabled }))}
        />
        <span className={`status-chip ${telegram.running ? "on" : "off"}`}>
          <StatusIcon mark={telegram.running ? "play" : "stop"} />
          {telegram.running ? "Executando" : "Parado"}
        </span>
        {telegram.enabled && !telegram.has_token && (
          <span className="settings-status warn">Defina o token do bot para ele rodar.</span>
        )}
      </div>
      <div className="settings-secrets">
        <SecretField
          label="Token do bot"
          set={telegram.has_token}
          disabled={busy}
          placeholder="123456:ABC…"
          onSave={async (token) => {
            onChange(await updateTelegramSettings({ token }));
          }}
        />
      </div>

      <h3 className="settings-subtitle">Usuários autorizados</h3>
      {telegram.users.length === 0 ? (
        <p className="empty">Nenhum: o bot não responde a ninguém.</p>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>ID no Telegram</th>
                <th>Nome</th>
                <th>
                  <span className="sr-only">Ações</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {telegram.users.map((user) => (
                <tr key={user.id}>
                  <td className="mono">{user.telegram_id}</td>
                  <td>{user.name || "—"}</td>
                  <td>
                    <div className="settings-actions">
                      <ConfirmButton
                        label="Remover"
                        question={`Remover ${user.name || user.telegram_id}?`}
                        disabled={busy}
                        onConfirm={() => run(() => deleteTelegramUser(user.id))}
                      />
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <form className="settings-form" onSubmit={add}>
        <input
          className="settings-input mono"
          inputMode="numeric"
          aria-label="ID no Telegram"
          placeholder="ID no Telegram"
          value={userID}
          disabled={busy}
          onChange={(e) => setUserID(e.target.value)}
        />
        <input
          className="settings-input"
          aria-label="Nome"
          placeholder="Nome (opcional)"
          value={name}
          disabled={busy}
          onChange={(e) => setName(e.target.value)}
        />
        <button type="submit" className="button" disabled={busy || id === null}>
          Autorizar
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
