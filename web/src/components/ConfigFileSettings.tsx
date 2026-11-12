import { type ChangeEvent, type FormEvent, useEffect, useId, useRef, useState } from "react";
import { exportConfig, fetchConfigCatalog, importConfig, previewConfigImport } from "../api/client";
import {
  type ConfigFile,
  type ConfigPreview,
  ConfigSecretsLocked,
  ConfigSecretsNone,
  ConfigSecretsUnlocked,
  ConfigSectionAgents,
  ConfigStatusInvalid,
  ConfigStatusSame,
  MaxConfigFile,
} from "../api/types";
import { can } from "../lib/access";
import {
  applyBrowserPrefs,
  browserPrefs,
  browserPreview,
  browserSection,
  type ConfigSelection,
  type ConfigTreeItem,
  type ConfigTreeSection,
  configFileName,
  configFileText,
  configManagePermissions,
  defaultImportSelection,
  exportPasswordError,
  exportTree,
  hasServerItems,
  importable,
  importTree,
  keepImportable,
  mergeRefusals,
  pickedCount,
  pickedSecrets,
  pickItem,
  pickSection,
  readConfigFile,
  selectAll,
  serverSelection,
  statusCounts,
  statusLabel,
  withBrowserPrefs,
} from "../lib/configFile";
import type { Theme } from "../lib/theme";
import { useAccess } from "./AccessContext";
import { Icon } from "./Icon";
import { PageHeader } from "./PageHeader";
import type { SectionIntro } from "./SettingsPage";
import { Alert, Loading, Skeleton } from "./States";

interface Props {
  /** The title of the section and what it is for. */
  intro: SectionIntro;
  /** Applies a theme imported to the page open. */
  onTheme: (theme: Theme) => void;
  /** Something imported: the other sections read again. */
  onImported: () => void;
}

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** The count of the items, eg: 1 item, 3 itens. */
const itemsText = (count: number) => (count === 1 ? "1 item" : `${count} itens`);

/** What an import did, in a sentence: how many items, and of the agents what follows. */
function importedText(tree: ConfigTreeSection[]): string {
  const count = tree.reduce((sum, section) => sum + section.items.length, 0);
  const agents = tree.some((section) => section.key === ConfigSectionAgents);
  return `${itemsText(count)} ${count === 1 ? "aplicado" : "aplicados"}.${
    agents
      ? " Os agentes ligados no arquivo já foram iniciados; os que precisam reiniciar aparecem assim na aba Agentes."
      : ""
  }`;
}

/** Downloads text as the JSON file name. */
function download(text: string, name: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "application/json;charset=utf-8" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/**
 * The configuration in a file, as the Profit does it: what to export picked
 * from the sections the role of the user shows (the secrets only when asked,
 * sealed with a password), and a file imported item by item, its preview
 * telling what each one changes before anything is applied.
 */
export function ConfigFileSettings({ intro, onTheme, onImported }: Props) {
  return (
    <div className="settings-section">
      <PageHeader eyebrow="Configurações" title={intro.title} description={intro.description} />
      <ExportCard />
      <ImportCard onTheme={onTheme} onImported={onImported} />
    </div>
  );
}

/** The export: the tree of the sections and their items, the secrets with a password, the file downloaded. */
function ExportCard() {
  const id = useId();
  const [tree, setTree] = useState<ConfigTreeSection[] | null>(null);
  const [selection, setSelection] = useState<ConfigSelection>({});
  const [withSecrets, setWithSecrets] = useState(false);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    const show = (sections: ConfigTreeSection[]) => {
      setTree(sections);
      setSelection(selectAll(sections));
    };
    fetchConfigCatalog(controller.signal)
      .then((catalog) => show(exportTree(catalog)))
      .catch((e) => {
        if (controller.signal.aborted) return;
        setError(message(e));
        show([browserSection()]); // the preferences of this browser still
      });
    return () => controller.abort();
  }, []);

  if (!tree) {
    return (
      <section className="card">
        <Loading label="Carregando o que pode ser exportado">
          <Skeleton height={220} />
        </Loading>
      </section>
    );
  }

  const count = pickedCount(selection);
  const secretsShown = pickedSecrets(tree, selection);
  const secrets = withSecrets && secretsShown;
  const passwordError = secrets ? exportPasswordError(password, confirm) : null;
  // told once something is typed: the length first, then the confirmation
  const shownError =
    passwordError && password !== "" && (confirm !== "" || passwordError.startsWith("A senha"))
      ? passwordError
      : null;
  const change = (next: ConfigSelection) => {
    setSelection(next);
    setDone(null);
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (count === 0 || passwordError) return;
    setBusy(true);
    setError(null);
    setDone(null);
    try {
      const file = await exportConfig({
        selection: serverSelection(selection),
        include_secrets: secrets,
        password: secrets ? password : undefined,
      });
      const full = withBrowserPrefs(file, selection, browserPrefs());
      const name = configFileName(new Date());
      download(configFileText(full), name);
      setDone(
        `${name} baixado, com ${itemsText(count)}${full.secrets ? " e os segredos cifrados com a senha" : ""}.`,
      );
      setPassword("");
      setConfirm("");
    } catch (err) {
      setError(message(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="card card-flush" aria-labelledby={`${id}-title`}>
      <header className="card-header">
        <div className="card-heading">
          <span className="eyebrow">Arquivo</span>
          <h2 id={`${id}-title`} className="card-title">
            Exportar
          </h2>
        </div>
        <span className="status-chip off">{itemsText(count)}</span>
      </header>
      <form className="card-body config-body" onSubmit={submit} noValidate>
        {error && <Alert tone="error">{error}</Alert>}
        <p className="field-hint">
          Marque o que vai no arquivo. O histórico (ordens, decisões, backtests) e os usuários do
          painel ficam de fora; os segredos só vão com a opção abaixo, cifrados.
        </p>
        <ConfigTree
          tree={tree}
          selection={selection}
          onChange={change}
          disabled={busy}
          mode="export"
        />
        {secretsShown && (
          <div className="config-secrets">
            <label className="hours-check">
              <input
                type="checkbox"
                checked={withSecrets}
                disabled={busy}
                onChange={(e) => setWithSecrets(e.target.checked)}
              />
              <span>
                Incluir os segredos (chaves de API, senhas e tokens), cifrados com uma senha
              </span>
            </label>
            {withSecrets && (
              <div className="config-password">
                <div className="field" data-invalid={shownError ? "" : undefined}>
                  <label className="field-label" htmlFor={`${id}-password`}>
                    Senha do arquivo
                  </label>
                  <input
                    id={`${id}-password`}
                    type="password"
                    className="input"
                    autoComplete="new-password"
                    placeholder="mínimo de 8 caracteres"
                    value={password}
                    disabled={busy}
                    aria-describedby={`${id}-password-hint`}
                    onChange={(e) => setPassword(e.target.value)}
                  />
                </div>
                <div className="field" data-invalid={shownError ? "" : undefined}>
                  <label className="field-label" htmlFor={`${id}-confirm`}>
                    Repita a senha
                  </label>
                  <input
                    id={`${id}-confirm`}
                    type="password"
                    className="input"
                    autoComplete="new-password"
                    value={confirm}
                    disabled={busy}
                    aria-describedby={shownError ? `${id}-password-error` : undefined}
                    onChange={(e) => setConfirm(e.target.value)}
                  />
                </div>
                <p id={`${id}-password-hint`} className="field-hint config-password-hint">
                  A importação pede esta senha para abrir os segredos. Sem ela, ninguém os abre:
                  guarde-a fora do arquivo.
                </p>
                {shownError && (
                  <span id={`${id}-password-error`} className="field-error config-password-hint">
                    {shownError}
                  </span>
                )}
              </div>
            )}
          </div>
        )}
        <div className="daily-actions">
          {done && (
            <span className="dialog-foot-hint" role="status">
              {done}
            </span>
          )}
          <button
            type="submit"
            className="btn btn-primary"
            disabled={busy || count === 0 || passwordError !== null}
          >
            <Icon name="download" size={14} />
            {busy ? "Exportando…" : `Exportar ${itemsText(count)}`}
          </button>
        </div>
      </form>
    </section>
  );
}

/**
 * The import: a file chosen, its preview (each item new, changed with its
 * fields, the same, or refused with why; its secrets opened with the password
 * of the file), the items picked imported all together, then what was done.
 */
function ImportCard({
  onTheme,
  onImported,
}: {
  onTheme: (theme: Theme) => void;
  onImported: () => void;
}) {
  const id = useId();
  const access = useAccess();
  const manages = can(access, ...configManagePermissions);
  const input = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<ConfigFile | null>(null);
  const [fileName, setFileName] = useState("");
  const [preview, setPreview] = useState<ConfigPreview | null>(null);
  const [tree, setTree] = useState<ConfigTreeSection[]>([]);
  const [selection, setSelection] = useState<ConfigSelection>({});
  const [password, setPassword] = useState("");
  const [showSame, setShowSame] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<ConfigTreeSection[] | null>(null);

  const reset = () => {
    setFile(null);
    setFileName("");
    setPreview(null);
    setTree([]);
    setSelection({});
    setPassword("");
    setShowSame(false);
    setConfirming(false);
    setError(null);
    setResult(null);
  };

  const runPreview = async (chosen: ConfigFile, secret: string) => {
    setBusy(true);
    setError(null);
    setConfirming(false);
    try {
      const server =
        manages && hasServerItems(chosen)
          ? await previewConfigImport({ file: chosen, password: secret || undefined })
          : null;
      const next = importTree(server, browserPreview(chosen));
      setPreview(server);
      setTree(next);
      setSelection(defaultImportSelection(next));
    } catch (err) {
      setError(message(err));
    } finally {
      setBusy(false);
    }
  };

  const choose = async (e: ChangeEvent<HTMLInputElement>) => {
    const chosen = e.target.files?.[0];
    e.target.value = ""; // the same file chosen again is read again
    if (!chosen) return;
    reset();
    const read = readConfigFile(
      chosen.size > MaxConfigFile ? "" : await chosen.text(),
      chosen.size,
    );
    if ("error" in read) {
      setError(read.error);
      return;
    }
    setFile(read.file);
    setFileName(chosen.name);
    await runPreview(read.file, "");
  };

  const run = async () => {
    if (!file) return;
    setConfirming(false);
    setBusy(true);
    setError(null);
    try {
      const server = serverSelection(selection);
      let answer: ConfigPreview | null = null;
      if (pickedCount(server) > 0) {
        answer = await importConfig({ file, password: password || undefined, selection: server });
      }
      const applied = answer === null || answer.applied === true;
      if (!applied && answer) {
        // nothing applied: the items refused told in the preview, to unpick or fix
        const next = mergeRefusals(tree, answer);
        setTree(next);
        setSelection((current) => keepImportable(current, next));
        setError(
          "Nada foi importado: um item marcado foi recusado. Desmarque-o, ou corrija o arquivo, e importe de novo.",
        );
        return;
      }
      const saved = applyBrowserPrefs(file, selection, { theme: onTheme });
      const browser = tree
        .filter((s) => s.key in selection && !(answer?.sections ?? []).some((a) => a.key === s.key))
        .map((s) => ({ ...s, items: s.items.filter((it) => saved.includes(it.key)) }))
        .filter((s) => s.items.length > 0);
      setResult([...importTree(answer, null), ...browser]);
      if (answer) onImported();
    } catch (err) {
      setError(message(err));
    } finally {
      setBusy(false);
    }
  };

  const count = pickedCount(selection);
  const counts = statusCounts(tree);
  const same = counts[ConfigStatusSame] ?? 0;
  const refused = counts[ConfigStatusInvalid] ?? 0;
  const locked = preview?.secrets === ConfigSecretsLocked;
  const leftOut = file !== null && !manages && hasServerItems(file);

  return (
    <section className="card card-flush" aria-labelledby={`${id}-title`}>
      <header className="card-header">
        <div className="card-heading">
          <span className="eyebrow">Arquivo</span>
          <h2 id={`${id}-title`} className="card-title">
            Importar
          </h2>
        </div>
        {preview?.secrets === ConfigSecretsUnlocked && (
          <span className="status-chip on">segredos abertos</span>
        )}
      </header>
      <div className="card-body config-body">
        {error && <Alert tone="error">{error}</Alert>}
        <div className="config-file-row">
          <input
            ref={input}
            id={`${id}-file`}
            type="file"
            accept="application/json,.json"
            className="sr-only"
            disabled={busy}
            onChange={(e) => void choose(e)}
          />
          <button
            type="button"
            className="btn"
            disabled={busy}
            onClick={() => input.current?.click()}
          >
            <Icon name="upload" size={14} />
            {file ? "Escolher outro arquivo" : "Escolher arquivo"}
          </button>
          {fileName ? (
            <span className="config-file-name" title={fileName}>
              {fileName}
              {file?.exported_by && ` · exportado por ${file.exported_by}`}
            </span>
          ) : (
            <span className="field-hint">
              Um arquivo exportado nesta tela, desta ou de outra instalação. Nada muda antes de você
              conferir e confirmar.
            </span>
          )}
        </div>
        {busy && !result && tree.length === 0 && (
          <Loading label="Conferindo o arquivo">
            <Skeleton height={160} />
          </Loading>
        )}

        {result ? (
          <>
            <Alert tone="success" title="Importado">
              {importedText(result)}
            </Alert>
            <ConfigTree tree={result} selection={{}} onChange={() => {}} mode="result" />
            <div className="daily-actions">
              <button type="button" className="btn" onClick={reset}>
                Concluir
              </button>
            </div>
          </>
        ) : (
          file &&
          tree.length > 0 && (
            <>
              {leftOut && (
                <Alert tone="info">
                  Os itens do XTrader no arquivo pedem permissão para alterar as áreas deles: este
                  usuário importa só as preferências deste navegador.
                </Alert>
              )}
              {preview && preview.secrets !== ConfigSecretsNone && (
                <form
                  className="config-unlock"
                  onSubmit={(e) => {
                    e.preventDefault();
                    if (password !== "") void runPreview(file, password);
                  }}
                >
                  <div className="field" data-invalid={preview.secrets_error ? "" : undefined}>
                    <label className="field-label" htmlFor={`${id}-secret`}>
                      Senha do arquivo
                    </label>
                    <input
                      id={`${id}-secret`}
                      type="password"
                      className="input"
                      autoComplete="off"
                      value={password}
                      disabled={busy}
                      aria-describedby={`${id}-secret-hint`}
                      onChange={(e) => setPassword(e.target.value)}
                    />
                  </div>
                  <button type="submit" className="btn" disabled={busy || password === ""}>
                    {locked ? "Abrir os segredos" : "Conferir de novo"}
                  </button>
                  <span
                    id={`${id}-secret-hint`}
                    className={preview.secrets_error ? "field-error" : "field-hint"}
                  >
                    {preview.secrets_error ??
                      (locked
                        ? "O arquivo traz segredos cifrados. Sem a senha, eles ficam como estão aqui e o resto é importado."
                        : "Os segredos dos itens marcados são gravados com eles.")}
                  </span>
                </form>
              )}
              <div className="config-summary">
                <span>
                  {[
                    counts.new && `${counts.new} novos`,
                    counts.changed && `${counts.changed} alterados`,
                    same && `${same} iguais`,
                    refused && `${refused} inválidos`,
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                </span>
                {same > 0 && (
                  <label className="hours-check">
                    <input
                      type="checkbox"
                      checked={showSame}
                      onChange={(e) => setShowSame(e.target.checked)}
                    />
                    <span>Mostrar os iguais</span>
                  </label>
                )}
              </div>
              <ConfigTree
                tree={tree}
                selection={selection}
                onChange={(next) => {
                  setSelection(next);
                  setConfirming(false);
                }}
                disabled={busy}
                mode="preview"
                showSame={showSame}
              />
              <div className="daily-actions">
                {confirming ? (
                  <fieldset className="confirm" aria-label={`Importar ${itemsText(count)}?`}>
                    <span className="confirm-question">
                      Importar {itemsText(count)}? Os marcados substituem os atuais.
                    </span>
                    <button
                      type="button"
                      className="btn btn-sm btn-primary"
                      onClick={() => void run()}
                    >
                      Importar
                    </button>
                    <button
                      type="button"
                      className="btn btn-sm"
                      // biome-ignore lint/a11y/noAutofocus: the button asked from leaves, the safe answer takes its focus
                      autoFocus
                      onClick={() => setConfirming(false)}
                    >
                      Cancelar
                    </button>
                  </fieldset>
                ) : (
                  <button
                    type="button"
                    className="btn btn-primary"
                    disabled={busy || count === 0}
                    onClick={() => setConfirming(true)}
                  >
                    <Icon name="upload" size={14} />
                    {busy ? "Importando…" : `Importar ${itemsText(count)}`}
                  </button>
                )}
              </div>
            </>
          )
        )}
      </div>
    </section>
  );
}

type TreeMode = "export" | "preview" | "result";

/**
 * The sections of a configuration file and their items: checked to export
 * (a grid of names) or to import (a row each, its state, the fields that
 * change, its warnings and why it is refused), or as imported (no checks).
 */
function ConfigTree({
  tree,
  selection,
  onChange,
  disabled = false,
  mode,
  showSame = true,
}: {
  tree: ConfigTreeSection[];
  selection: ConfigSelection;
  onChange: (selection: ConfigSelection) => void;
  disabled?: boolean;
  mode: TreeMode;
  showSame?: boolean;
}) {
  return (
    <div className="config-tree">
      {tree.map((section) => {
        const items = section.items.filter(
          (it) => showSame || it.preview?.status !== ConfigStatusSame,
        );
        if (items.length === 0) return null;
        return (
          <TreeSection
            key={section.key}
            section={section}
            items={items}
            selection={selection}
            onChange={onChange}
            disabled={disabled}
            mode={mode}
          />
        );
      })}
    </div>
  );
}

function TreeSection({
  section,
  items,
  selection,
  onChange,
  disabled,
  mode,
}: {
  section: ConfigTreeSection;
  items: ConfigTreeItem[];
  selection: ConfigSelection;
  onChange: (selection: ConfigSelection) => void;
  disabled: boolean;
  mode: TreeMode;
}) {
  const id = useId();
  const all = useRef<HTMLInputElement>(null);
  const pickable = section.items.filter(importable);
  const picked = selection[section.key] ?? [];
  const whole = pickable.length > 0 && picked.length === pickable.length;
  useEffect(() => {
    if (all.current) all.current.indeterminate = picked.length > 0 && !whole;
  });

  return (
    <fieldset className="config-section" disabled={disabled}>
      <legend className="sr-only">{section.label}</legend>
      <div className="config-section-head">
        {mode === "result" ? (
          <span className="config-section-title">{section.label}</span>
        ) : (
          <label className="hours-check config-section-title">
            <input
              ref={all}
              type="checkbox"
              checked={whole}
              disabled={pickable.length === 0}
              aria-label={`Marcar todos de ${section.label}`}
              onChange={(e) => onChange(pickSection(selection, section, e.target.checked))}
            />
            <span>{section.label}</span>
          </label>
        )}
        <span className="config-count">
          {mode === "result"
            ? itemsText(section.items.length)
            : `${picked.length} de ${section.items.length}`}
        </span>
      </div>
      {mode === "export" ? (
        <div className="config-items">
          {items.map((item) => (
            <label key={item.key} className="hours-check config-item">
              <input
                type="checkbox"
                checked={picked.includes(item.key)}
                onChange={(e) => onChange(pickItem(selection, section, item.key, e.target.checked))}
              />
              <span className="config-name" title={item.label}>
                {item.label}
              </span>
            </label>
          ))}
        </div>
      ) : (
        <ul className={`config-rows ${mode}`}>
          {items.map((item) => (
            <PreviewRow
              key={item.key}
              id={`${id}-${item.key}`}
              item={item}
              mode={mode}
              checked={picked.includes(item.key)}
              onCheck={(on) => onChange(pickItem(selection, section, item.key, on))}
            />
          ))}
        </ul>
      )}
    </fieldset>
  );
}

/** An item of a preview or of an import: its state, what changes, its warnings and why it is refused. */
function PreviewRow({
  id,
  item,
  mode,
  checked,
  onCheck,
}: {
  id: string;
  item: ConfigTreeItem;
  mode: TreeMode;
  checked: boolean;
  onCheck: (on: boolean) => void;
}) {
  const preview = item.preview;
  const status = preview?.status ?? "";
  return (
    <li className="config-row" data-status={status}>
      <div className="config-row-head">
        <span className="config-row-title">
          {mode === "preview" && (
            <input
              id={id}
              type="checkbox"
              className="config-check"
              checked={checked}
              disabled={!importable(item)}
              onChange={(e) => onCheck(e.target.checked)}
            />
          )}
          {mode === "preview" ? (
            <label htmlFor={id} className="config-name">
              {item.label}
            </label>
          ) : (
            <span className="config-name">{item.label}</span>
          )}
        </span>
        <span className={`badge config-status ${status}`}>
          {statusLabel(status, mode === "result")}
        </span>
        {preview?.secrets && <span className="tag">com segredos</span>}
      </div>
      {preview?.changes && preview.changes.length > 0 && (
        <p className="config-detail">Muda: {preview.changes.join(", ")}</p>
      )}
      {mode === "preview" &&
        preview?.warnings?.map((warning) => (
          <p key={warning} className="config-detail warn">
            {warning}
          </p>
        ))}
      {preview?.error && <p className="config-detail error">{preview.error}</p>}
    </li>
  );
}
