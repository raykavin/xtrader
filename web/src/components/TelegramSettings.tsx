import { type FormEvent, useState } from "react";
import { addTelegramUser, deleteTelegramUser, updateTelegramSettings } from "../api/client";
import type { TelegramSettingsResponse } from "../api/types";
import { parseTelegramID } from "../lib/settings";
import { ConfirmButton } from "./ConfirmButton";
import { Icon } from "./Icon";
import { PageHeader } from "./PageHeader";
import { SecretField } from "./SecretField";
import type { SectionIntro } from "./SettingsPage";
import { Alert, EmptyState } from "./States";
import { StatusIcon } from "./StatusIcon";
import { Switch } from "./Switch";

interface Props {
  /** The title of the section and what it is for. */
  intro: SectionIntro;
  telegram: TelegramSettingsResponse;
  onChange: (telegram: TelegramSettingsResponse) => void;
}

/**
 * The Telegram bot of the controller: on or off, its token (written, never
 * read), and the users it answers, the only ones who reach the agents and the
 * prompts from Telegram.
 */
export function TelegramSettings({ intro, telegram, onChange }: Props) {
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
    <div className="settings-section">
      <PageHeader eyebrow="Configurações" title={intro.title} description={intro.description} />
      {error && (
        <Alert tone="error" title="Não foi possível salvar a alteração">
          {error}
        </Alert>
      )}
      <div className="settings-columns">
        <section className="card card-flush" aria-labelledby="telegram-bot-title">
          <header className="card-header">
            <div className="card-heading">
              <span className="eyebrow">Telegram</span>
              <h3 id="telegram-bot-title" className="card-title">
                Bot
              </h3>
            </div>
            <span className={`status-chip ${telegram.running ? "on" : "off"}`}>
              <StatusIcon mark={telegram.running ? "play" : "stop"} />
              {telegram.running ? "Executando" : "Parado"}
            </span>
          </header>
          <div className="card-body stack">
            <Switch
              label="Bot ligado"
              checked={telegram.enabled}
              disabled={busy}
              onChange={(enabled) => run(() => updateTelegramSettings({ enabled }))}
            />
            {telegram.enabled && !telegram.has_token && (
              <p className="msg msg-warn">Defina o token para o bot rodar.</p>
            )}
            <div className="secret-list">
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
          </div>
        </section>

        <section className="card card-flush" aria-labelledby="telegram-users-title">
          <header className="card-header">
            <div className="card-heading">
              <span className="eyebrow">Acesso</span>
              <h3 id="telegram-users-title" className="card-title">
                Usuários autorizados
                <span className="card-count">{telegram.users.length}</span>
              </h3>
            </div>
          </header>
          {telegram.users.length === 0 ? (
            <EmptyState compact icon="users" title="Nenhum usuário autorizado ainda">
              O bot só responde aos IDs autorizados abaixo.
            </EmptyState>
          ) : (
            <div className="table-wrap">
              <table className="table table-responsive">
                <thead>
                  <tr>
                    <th scope="col">ID no Telegram</th>
                    <th scope="col">Nome</th>
                    <th scope="col" className="col-actions">
                      <span className="sr-only">Ações</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {telegram.users.map((user) => (
                    <tr key={user.id}>
                      <td className="mono" data-label="ID no Telegram">
                        {user.telegram_id}
                      </td>
                      <td data-label="Nome">{user.name || "—"}</td>
                      <td className="cell-actions">
                        <div className="row-actions">
                          <ConfirmButton
                            label="Remover"
                            ariaLabel={`Remover ${user.name || user.telegram_id}`}
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
          <form className="card-footer inline-form" onSubmit={add} aria-label="Autorizar usuário">
            <div className="field">
              <label className="field-label" htmlFor="telegram-user-id">
                ID no Telegram
              </label>
              <input
                id="telegram-user-id"
                className="input mono"
                inputMode="numeric"
                placeholder="ex.: 123456789"
                value={userID}
                disabled={busy}
                onChange={(e) => setUserID(e.target.value)}
              />
            </div>
            <div className="field">
              <label className="field-label" htmlFor="telegram-user-name">
                Nome <span className="optional">(opcional)</span>
              </label>
              <input
                id="telegram-user-name"
                className="input"
                placeholder="ex.: Maria"
                value={name}
                disabled={busy}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <button type="submit" className="btn btn-primary" disabled={busy || id === null}>
              <Icon name="plus" />
              Autorizar
            </button>
          </form>
        </section>
      </div>
    </div>
  );
}
