import { type FormEvent, useId, useState } from "react";
import {
  addTelegramUser,
  deleteTelegramUser,
  fetchTelegramUsersPage,
  updateTelegramSettings,
} from "../api/client";
import type { TelegramSettingsResponse } from "../api/types";
import { PermTelegramManage } from "../api/types";
import { searchNote } from "../lib/pagination";
import { parseTelegramID } from "../lib/settings";
import { useCan } from "./AccessContext";
import { ConfirmButton } from "./ConfirmButton";
import { Icon } from "./Icon";
import { PageHeader } from "./PageHeader";
import { ListPages } from "./Paginator";
import { SearchField } from "./SearchField";
import { SecretField } from "./SecretField";
import type { SectionIntro } from "./SettingsPage";
import { Alert, EmptyState, Loading, Skeleton } from "./States";
import { StatusIcon } from "./StatusIcon";
import { Switch } from "./Switch";
import { usePagedList } from "./usePagedList";

interface Props {
  /** The title of the section and what it is for. */
  intro: SectionIntro;
  telegram: TelegramSettingsResponse;
  onChange: (telegram: TelegramSettingsResponse) => void;
}

/** The users of Telegram, as a search counts them. */
const noun = { one: "usuário", many: "usuários" };

/**
 * The Telegram bot of the controller: on or off, its token (written, never
 * read), and the users it answers, the only ones who reach the agents and the
 * prompts from Telegram, a page at a time, searched by their IDs and names.
 */
export function TelegramSettings({ intro, telegram, onChange }: Props) {
  const manage = useCan(PermTelegramManage); // changed by a user allowed to only, seen by the others
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [userID, setUserID] = useState("");
  const [name, setName] = useState("");
  const list = usePagedList((params, signal) =>
    fetchTelegramUsersPage(params, signal).then((r) => ({ rows: r.users, page: r })),
  );
  const listID = useId();

  const run = async (action: () => Promise<TelegramSettingsResponse>) => {
    setBusy(true);
    setError(null);
    try {
      onChange(await action());
      list.reload(); // the users authorized, of the page
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
              disabled={busy || !manage}
              onChange={(enabled) => run(() => updateTelegramSettings({ enabled }))}
            />
            {telegram.enabled && !telegram.has_token && (
              <p className="msg msg-warn">Defina o token para o bot rodar.</p>
            )}
            <div className="secret-list">
              <SecretField
                label="Token do bot"
                set={telegram.has_token}
                disabled={busy || !manage}
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
                {list.info && list.search === "" && (
                  <span className="card-count">{list.info.total}</span>
                )}
              </h3>
            </div>
          </header>
          {list.error && (
            <Alert tone="error" title="Não foi possível carregar os usuários">
              {list.error}
            </Alert>
          )}
          {list.rows === null || list.info === null ? (
            !list.error && (
              <Loading label="Carregando…">
                <Skeleton height={120} />
              </Loading>
            )
          ) : list.info.total === 0 && list.search === "" ? (
            <EmptyState compact icon="users" title="Nenhum usuário autorizado ainda">
              O bot só responde aos IDs autorizados abaixo.
            </EmptyState>
          ) : (
            <>
              <div className="list-toolbar">
                <SearchField
                  value={list.query}
                  onChange={list.setQuery}
                  label="Buscar usuários autorizados"
                  placeholder="Buscar por ID no Telegram ou nome"
                  controls={listID}
                  note={searchNote(list.info.total, list.search, noun)}
                />
              </div>
              {list.rows.length > 0 && (
                <div className="table-wrap" id={listID}>
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
                      {list.rows.map((user) => (
                        <tr key={user.id}>
                          <td className="mono" data-label="ID no Telegram">
                            {user.telegram_id}
                          </td>
                          <td data-label="Nome">{user.name || "—"}</td>
                          <td className="cell-actions">
                            <div className="row-actions">
                              {manage && (
                                <ConfirmButton
                                  label="Remover"
                                  ariaLabel={`Remover ${user.name || user.telegram_id}`}
                                  question={`Remover ${user.name || user.telegram_id}?`}
                                  disabled={busy}
                                  onConfirm={() => run(() => deleteTelegramUser(user.id))}
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
          {manage && (
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
          )}
        </section>
      </div>
    </div>
  );
}
