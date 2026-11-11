import { type FormEvent, useId, useState } from "react";
import {
  createMT5Account,
  deleteMT5Account,
  fetchMT5AccountsPage,
  fetchMT5Switch,
  switchMT5Account,
  switchRefusalOf,
  testMT5Account,
  updateMT5Account,
} from "../api/client";
import type { MT5AccountInfo, MT5AccountTest, MT5SwitchPreview } from "../api/types";
import { PermMT5AccountsManage } from "../api/types";
import { formatTime } from "../lib/format";
import {
  type AccountField,
  type AccountForm,
  type AccountLabel,
  accountCreateRequest,
  accountErrors,
  accountForm,
  accountState,
  accountUpdateRequest,
  agentsLabel,
  kindLabel,
  moveAgentsLabel,
  movesTheTerminal,
  sessionLabel,
  switchQuestion,
} from "../lib/mt5Accounts";
import { searchNote } from "../lib/pagination";
import { useCan } from "./AccessContext";
import { ConfirmButton } from "./ConfirmButton";
import { Icon } from "./Icon";
import { PageHeader } from "./PageHeader";
import { ListPages } from "./Paginator";
import { SearchField } from "./SearchField";
import { SecretField } from "./SecretField";
import type { SectionIntro } from "./SettingsPage";
import { Alert, EmptyState, Loading, Skeleton } from "./States";
import { Switch } from "./Switch";
import { usePagedList } from "./usePagedList";

/** How often the accounts and their terminals are read again, in ms. */
const POLL = 10000;

interface Props {
  intro: SectionIntro;
}

/** The accounts, as a search counts them. */
const noun = { one: "conta", many: "contas", feminine: true };

/** The class of a status chip of tone. */
function chip(label: AccountLabel): string {
  return `status-chip ${label.tone === "error" ? "stopped" : label.tone === "off" ? "" : label.tone}`.trim();
}

/** A field of the form of an account. */
function Field({
  id,
  label,
  value,
  onChange,
  error,
  hint,
  type = "text",
  mono = false,
  inputMode,
  placeholder,
  disabled,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  error?: string;
  hint?: string;
  type?: string;
  mono?: boolean;
  inputMode?: "numeric" | "url";
  placeholder?: string;
  disabled?: boolean;
}) {
  return (
    <div className="field" data-invalid={error ? "" : undefined}>
      <label className="field-label" htmlFor={id}>
        {label}
      </label>
      <input
        id={id}
        className={mono ? "input mono" : "input"}
        type={type}
        value={value}
        inputMode={inputMode}
        placeholder={placeholder}
        disabled={disabled}
        autoComplete={type === "password" ? "new-password" : "off"}
        aria-invalid={error ? true : undefined}
        onChange={(e) => onChange(e.target.value)}
      />
      {(error || hint) && (
        <span className={error ? "field-error" : "field-hint"}>{error ?? hint}</span>
      )}
    </div>
  );
}

/**
 * The form of an account, new or changed: its login, server and bridge, and
 * whether the bridge may trade it real or trade at all; a new one with its
 * password and the token of its bridge (an account kept changes them on its
 * card, never read back).
 */
function AccountEditor({
  account,
  onDone,
  onCancel,
}: {
  account?: MT5AccountInfo;
  onDone: (account: MT5AccountInfo) => void;
  onCancel: () => void;
}) {
  const id = useId();
  const creating = !account;
  const [initial] = useState(() => accountForm(account));
  const [form, setForm] = useState(initial);
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const errors = accountErrors(form, creating);
  const set = <K extends AccountField>(key: K, value: AccountForm[K]) =>
    setForm((f) => ({ ...f, [key]: value }));
  const shown = (field: AccountField) => (touched ? errors[field] : undefined);
  const update = account ? accountUpdateRequest(initial, form) : null;
  const moving = account?.active && update !== null && movesTheTerminal(update);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (Object.keys(errors).length > 0) return;
    setBusy(true);
    setError(null);
    const request = account ? update : accountCreateRequest(form);
    if (!request) {
      onCancel();
      return;
    }
    (account ? updateMT5Account(account.id, request) : createMT5Account(request))
      .then(onDone)
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setBusy(false));
  };

  return (
    <form className="mt5-editor" onSubmit={submit} noValidate>
      {error && <Alert tone="error">{error}</Alert>}
      <div className="form-grid">
        <Field
          id={`${id}-name`}
          label="Nome"
          value={form.name}
          onChange={(v) => set("name", v)}
          error={shown("name")}
          placeholder="ex.: Exness real"
          disabled={busy}
        />
        <Field
          id={`${id}-login`}
          label="Login"
          value={form.login}
          onChange={(v) => set("login", v)}
          error={shown("login")}
          mono
          inputMode="numeric"
          placeholder="ex.: 87654321"
          disabled={busy}
        />
        <Field
          id={`${id}-server`}
          label="Servidor"
          value={form.server}
          onChange={(v) => set("server", v)}
          error={shown("server")}
          mono
          placeholder="ex.: Exness-MT5Real11"
          disabled={busy}
        />
        <Field
          id={`${id}-bridge`}
          label="Bridge do terminal"
          value={form.bridgeURL}
          onChange={(v) => set("bridgeURL", v)}
          error={shown("bridgeURL")}
          hint="O terminal MT5 que serve a conta."
          mono
          inputMode="url"
          disabled={busy}
        />
        {creating && (
          <>
            <Field
              id={`${id}-password`}
              label="Senha"
              value={form.password}
              onChange={(v) => set("password", v)}
              error={shown("password")}
              type="password"
              hint="Guardada cifrada e nunca exibida; enviada ao terminal ao ativar a conta."
              disabled={busy}
            />
            <Field
              id={`${id}-token`}
              label="Token da bridge"
              value={form.token}
              onChange={(v) => set("token", v)}
              type="password"
              hint="O MT5_BRIDGE_TOKEN do terminal."
              disabled={busy}
            />
          </>
        )}
      </div>
      <div className="mt5-switches">
        <div className={form.allowReal ? "agent-real on" : "agent-real"}>
          <Switch
            label="Permitir conta real"
            checked={form.allowReal}
            onChange={(v) => set("allowReal", v)}
            disabled={busy}
          />
          <p className="field-hint">
            {form.allowReal
              ? "Atenção: o terminal poderá operar esta conta real, com dinheiro de verdade (e só os agentes com “Permitir conta real”)."
              : "Desligado, o terminal recusa operar se a conta for real."}
          </p>
        </div>
        <Switch
          label="Somente leitura"
          checked={form.readOnly}
          onChange={(v) => set("readOnly", v)}
          disabled={busy}
        />
        <p className="field-hint">Ligado, o terminal lê a conta e recusa toda ordem.</p>
      </div>
      <Field
        id={`${id}-reason`}
        label="Motivo (registrado)"
        value={form.reason}
        onChange={(v) => set("reason", v)}
        placeholder="opcional"
        disabled={busy}
      />
      {moving && (
        <Alert tone="warn" title="Conta em uso">
          Mudar o login, o servidor ou a bridge da conta ativa troca a conta do terminal: é recusado
          com agentes em execução nele ou com posições ou ordens dos agentes abertas.
        </Alert>
      )}
      <p className="field-hint">
        O nome e as opções valem na hora; a senha, no próximo login do terminal; a associação de um
        agente, no próximo início dele.
      </p>
      <div className="daily-actions">
        <button type="button" className="btn" disabled={busy} onClick={onCancel}>
          Cancelar
        </button>
        <button type="submit" className="btn btn-primary" disabled={busy}>
          {busy ? "Salvando…" : creating ? "Adicionar conta" : "Salvar"}
        </button>
      </div>
    </form>
  );
}

/** The result of the test of an account, never a credential. */
function TestResult({ test }: { test: MT5AccountTest }) {
  const tone = test.error ? "error" : test.current ? "info" : "warn";
  const kind = kindLabel(test.kind);
  return (
    <Alert
      tone={tone}
      title={
        test.error ? "O teste falhou" : test.current ? "Conta confirmada" : "Conta não confirmada"
      }
    >
      {test.error && <p>{test.error}</p>}
      {test.current && (
        <p>
          {kind.text} · {test.currency} · {test.company} · {test.server}
        </p>
      )}
      {!test.current && test.logged_login ? (
        <p>O terminal está na conta {test.logged_login}.</p>
      ) : null}
      {test.detail && <p className="field-hint">{test.detail}</p>}
    </Alert>
  );
}

/** The positions and the pending orders open on an account, the first ones. */
function OpenList({ items }: { items: string[] }) {
  const shown = items.slice(0, 5);
  return (
    <ul className="mt5-open">
      {shown.map((item) => (
        <li key={item}>{item}</li>
      ))}
      {items.length > shown.length && <li>e mais {items.length - shown.length}</li>}
    </ul>
  );
}

/** The card of an account: how it stands, its agents, and its actions. */
function AccountCard({ account, onChange }: { account: MT5AccountInfo; onChange: () => void }) {
  const manage = useCan(PermMT5AccountsManage); // changed, switched and tested by a user allowed to only
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [test, setTest] = useState<MT5AccountTest | null>(null);
  // the switch turned: what it does read, then asked before (see switchQuestion)
  const [turning, setTurning] = useState<boolean | null>(null);
  const [preview, setPreview] = useState<MT5SwitchPreview | null>(null);
  const [pending, setPending] = useState<string[]>([]);
  const [move, setMove] = useState(true); // the agents of the account leaving the terminal moved to this one
  const session = sessionLabel(account);
  const kind = kindLabel(account.kind);
  const state = accountState(account);

  const turn = (active: boolean) => {
    setTurning(active);
    setPreview(null);
    setMove(true);
    setError(null);
    setPending([]);
    fetchMT5Switch(account.id, active)
      .then((read) => {
        if (read.error) {
          setTurning(null);
          setError(read.error);
          return;
        }
        setPreview(read);
      })
      .catch((err) => {
        setTurning(null);
        setError(err instanceof Error ? err.message : String(err));
      });
  };
  const confirm = () => {
    if (turning === null || preview === null) return;
    const active = turning;
    const close = preview.exposure.length > 0;
    setPreview(null);
    setBusy(true);
    setError(null);
    switchMT5Account(account.id, active, {
      close_positions: close,
      move_agents: active && move && (preview.linked ?? []).length > 0,
    })
      .then(() => {
        setTurning(null);
        onChange();
      })
      .catch((err) => {
        const refusal = switchRefusalOf(err);
        if (refusal?.confirm) {
          turn(active); // something opened meanwhile: asked again with it
          return;
        }
        setTurning(null);
        setError(refusal?.error ?? (err instanceof Error ? err.message : String(err)));
        setPending(refusal?.pending ?? []);
        onChange();
      })
      .finally(() => setBusy(false));
  };

  const run = (action: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    action()
      .then(onChange)
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setBusy(false));
  };
  const saveSecret = (field: "password" | "bridge_token") => async (value: string) => {
    await updateMT5Account(account.id, { [field]: value });
    onChange();
  };

  return (
    <article className={`card mt5-card ${state.tone}`} aria-label={`Conta ${account.name}`}>
      <header className="mt5-card-head">
        <div className="mt5-card-title">
          <h3 className="card-title">{account.name}</h3>
          <span className="mono field-hint">
            {account.login} · {account.server}
          </span>
        </div>
        <div className="mt5-card-chips">
          <span className={chip(kind)}>{kind.text}</span>
          {account.active && (
            <span className={chip(session)} title={session.text}>
              {session.text}
            </span>
          )}
        </div>
      </header>
      <div className="mt5-active">
        <Switch
          label="Ativa no terminal"
          checked={turning ?? account.active}
          disabled={busy || turning !== null || !manage}
          title="Ligada, o terminal desta bridge fica logado nela e os agentes dela são iniciados"
          onChange={turn}
        />
        {busy && turning !== null && (
          <span className="field-hint" role="status">
            {turning
              ? "Trocando de conta: parando os agentes, fechando o que estiver aberto e logando o terminal…"
              : "Desativando: parando os agentes e fechando o que estiver aberto…"}
          </span>
        )}
        {turning !== null && !busy && preview === null && (
          <span className="field-hint" role="status">
            Conferindo o terminal…
          </span>
        )}
        {preview && (
          <fieldset className="confirm mt5-confirm" aria-label="Confirmar">
            <span className="confirm-question">{switchQuestion(account, preview)}</span>
            {preview.exposure.length > 0 && <OpenList items={preview.exposure} />}
            {moveAgentsLabel(preview) && (
              <label className="hours-check">
                <input type="checkbox" checked={move} onChange={(e) => setMove(e.target.checked)} />
                {moveAgentsLabel(preview)}
              </label>
            )}
            <div className="mt5-confirm-actions">
              <button type="button" className="btn btn-sm btn-danger" onClick={confirm}>
                Sim
              </button>
              <button
                type="button"
                className="btn btn-sm"
                // biome-ignore lint/a11y/noAutofocus: the switch asked from leaves, the safe answer takes its focus
                autoFocus
                onClick={() => {
                  setTurning(null);
                  setPreview(null);
                }}
              >
                Não
              </button>
            </div>
          </fieldset>
        )}
      </div>
      {error && (
        <Alert tone="error">
          {error}
          {pending.length > 0 && <OpenList items={pending} />}
        </Alert>
      )}
      {test && <TestResult test={test} />}
      {editing ? (
        <AccountEditor
          account={account}
          onCancel={() => setEditing(false)}
          onDone={() => {
            setEditing(false);
            onChange();
          }}
        />
      ) : (
        <>
          <dl className="mt5-facts">
            <div>
              <dt>Terminal</dt>
              <dd className="mono">
                {account.bridge_url}
                {account.instance ? ` · instância ${account.instance}` : ""}
              </dd>
            </div>
            <div>
              <dt>Opções</dt>
              <dd>
                {[
                  account.allow_real ? "permite conta real" : "recusa conta real",
                  account.read_only ? "somente leitura" : "",
                ]
                  .filter(Boolean)
                  .join(" · ")}
              </dd>
            </div>
            <div>
              <dt>Agentes</dt>
              <dd>{agentsLabel(account.agents.length)}</dd>
            </div>
            <div>
              <dt>Confirmada</dt>
              <dd>
                {account.checked_at
                  ? `${formatTime(account.checked_at)}${account.currency ? ` · ${account.currency}` : ""}`
                  : "ainda não testada"}
                {account.check_error ? ` · ${account.check_error}` : ""}
              </dd>
            </div>
          </dl>
          <div className="mt5-secrets">
            <SecretField
              label="Senha da conta"
              set={account.password_set}
              onSave={saveSecret("password")}
              disabled={busy || !manage}
            />
            <SecretField
              label="Token da bridge"
              set={account.token_set}
              onSave={saveSecret("bridge_token")}
              disabled={busy || !manage}
            />
          </div>
          {manage && (
            <div className="mt5-actions">
              <button
                type="button"
                className="btn btn-sm"
                disabled={busy}
                onClick={() => {
                  setBusy(true);
                  setError(null);
                  testMT5Account(account.id)
                    .then((result) => {
                      setTest(result);
                      onChange();
                    })
                    .catch((err) => setError(err instanceof Error ? err.message : String(err)))
                    .finally(() => setBusy(false));
                }}
              >
                <Icon name="success" size={14} /> Testar conexão
              </button>
              <button
                type="button"
                className="btn btn-sm"
                disabled={busy}
                onClick={() => setEditing(true)}
              >
                <Icon name="edit" size={14} /> Editar
              </button>
              {!account.active && (
                <ConfirmButton
                  label="Excluir"
                  question="Excluir a conta? O histórico dela é mantido."
                  disabled={busy || account.agents.length > 0}
                  title={
                    account.agents.length > 0 ? "Associe os agentes a outra conta antes" : undefined
                  }
                  onConfirm={() => run(() => deleteMT5Account(account.id))}
                />
              )}
            </div>
          )}
        </>
      )}
    </article>
  );
}

/**
 * The accounts of MetaTrader 5: each with its login, server and password
 * (encrypted, never shown), the terminal serving it through its bridge, and
 * whether it is the account of its terminal; how its terminal stands, what the
 * bridge confirmed of it, and the agents trading it. A terminal is logged in
 * one account at a time: activating another one is refused while it is in use.
 */
export function MT5AccountsSettings({ intro }: Props) {
  const manage = useCan(PermMT5AccountsManage);
  const [adding, setAdding] = useState(false);
  const list = usePagedList(
    (params, signal) =>
      fetchMT5AccountsPage(params, signal).then((r) => ({ rows: r.accounts, page: r })),
    { poll: POLL },
  );
  const listID = useId();
  const load = list.reload;

  return (
    <>
      <PageHeader
        eyebrow="Configurações"
        title={intro.title}
        description={intro.description}
        actions={
          manage &&
          !adding && (
            <button type="button" className="btn btn-primary" onClick={() => setAdding(true)}>
              <Icon name="plus" size={16} /> Nova conta
            </button>
          )
        }
      />
      {list.error && <Alert tone="error">Não foi possível carregar as contas: {list.error}</Alert>}
      {adding && (
        <section className="card" aria-label="Nova conta">
          <h2 className="card-title">Nova conta</h2>
          <AccountEditor
            onCancel={() => setAdding(false)}
            onDone={() => {
              setAdding(false);
              load();
            }}
          />
        </section>
      )}
      {!list.rows || !list.info ? (
        list.error ? null : (
          <Loading label="Carregando…">
            <Skeleton height={180} />
          </Loading>
        )
      ) : list.info.total === 0 && list.search === "" ? (
        <EmptyState icon="key" title="Nenhuma conta MT5 ainda">
          Adicione a conta do terminal e associe os agentes a ela.
        </EmptyState>
      ) : (
        <div className="list-stack">
          <SearchField
            value={list.query}
            onChange={list.setQuery}
            label="Buscar contas MT5"
            placeholder="Buscar por nome, login, servidor, moeda ou agente"
            controls={listID}
            note={searchNote(list.info.total, list.search, noun)}
          />
          {list.rows.length > 0 && (
            <div className="mt5-list" id={listID}>
              {list.rows.map((account) => (
                <AccountCard key={account.id} account={account} onChange={load} />
              ))}
            </div>
          )}
          <ListPages info={list.info} onPage={list.setPage} />
        </div>
      )}
    </>
  );
}
