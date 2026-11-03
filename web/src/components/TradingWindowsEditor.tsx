import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  agentsLabel,
  agentsTitle,
  MAX_WINDOWS,
  nextWindow,
  type WindowForm,
  windowForm,
  windowLabel,
  windowsCheck,
} from "../lib/tradingHours";
import { DialogHeader } from "./Dialog";
import { Icon } from "./Icon";

interface Props {
  /** The windows, in the order of the user (sorted by their start only when sent, see windowsRequest). */
  windows: WindowForm[];
  onChange: (windows: WindowForm[]) => void;
  /** Whether one window at least is required: the hours on. */
  required: boolean;
  /**
   * The agents a window may be limited to, in the order shown; without them
   * (and none chosen) a window is for all the agents, its button hidden.
   */
  agents: string[];
  /** The tag of an agent chosen out of agents, eg: desligado. */
  unavailable?: string;
  disabled?: boolean;
}

/**
 * The windows of the trading hours, of the settings and of a batch: one row
 * by window, its start, its end, the agents that may open positions in it
 * (all of them by default, chosen in a dialog), the assets they may open them
 * on (all of them by default, typed separated by ;) and what its end does,
 * each removable; a button to add one, free of the others. The problems are told
 * as the controller tells them, each by the field or the row concerned, the
 * ones of the whole list under it.
 */
export function TradingWindowsEditor({
  windows,
  onChange,
  required,
  agents,
  unavailable = "indisponível",
  disabled,
}: Props) {
  const id = useId();
  // the key of the window whose agents are being chosen
  const [picking, setPicking] = useState<string | null>(null);
  const picked = windows.find((w) => w.key === picking);
  const check = windowsCheck(windows, required);
  const full = windows.length >= MAX_WINDOWS;
  const addRef = useRef<HTMLButtonElement>(null);
  // the field to focus once the list changed: the start of a window added,
  // the button to add after one removed
  const focus = useRef<string | null>(null);
  useEffect(() => {
    if (focus.current === null) return;
    const target =
      focus.current === "add" ? addRef.current : document.getElementById(focus.current);
    focus.current = null;
    target?.focus();
  });

  const fieldId = (w: WindowForm, field: string) => `${id}-${w.key}-${field}`;
  const update = (key: string, fields: Partial<WindowForm>) =>
    onChange(windows.map((w) => (w.key === key ? { ...w, ...fields } : w)));
  const add = () => {
    const added = windowForm(nextWindow(windows));
    focus.current = fieldId(added, "start");
    onChange([...windows, added]);
  };
  const remove = (key: string) => {
    focus.current = "add";
    onChange(windows.filter((w) => w.key !== key));
  };

  return (
    <div className="hours-windows">
      {windows.length === 0 ? (
        <p className="field-hint">Nenhuma janela ainda</p>
      ) : (
        <div className="hours-window-list">
          {windows.map((w, index) => {
            const errors = check.rows[w.key] ?? {};
            const name = windowName(w, index);
            return (
              <fieldset
                key={w.key}
                className="hours-window"
                aria-labelledby={fieldId(w, "title")}
                data-invalid={errors.window ? "" : undefined}
              >
                <div className="hours-window-head">
                  <span id={fieldId(w, "title")} className="hours-window-title">
                    Janela {index + 1}
                  </span>
                  <button
                    type="button"
                    className="btn btn-sm btn-ghost btn-icon"
                    aria-label={`Remover a janela ${name}`}
                    title="Remover"
                    disabled={disabled}
                    onClick={() => remove(w.key)}
                  >
                    <Icon name="trash" size={15} />
                  </button>
                </div>
                <TimeField
                  id={fieldId(w, "start")}
                  className="hours-window-start"
                  label="Início"
                  value={w.start}
                  error={errors.start}
                  invalid={Boolean(errors.start || errors.window)}
                  describedBy={errors.window ? fieldId(w, "error") : undefined}
                  disabled={disabled}
                  onChange={(start) => update(w.key, { start })}
                />
                <TimeField
                  id={fieldId(w, "end")}
                  className="hours-window-end"
                  label="Fim"
                  value={w.end}
                  error={errors.end}
                  invalid={Boolean(errors.end || errors.window)}
                  describedBy={errors.window ? fieldId(w, "error") : undefined}
                  disabled={disabled}
                  onChange={(end) => update(w.key, { end })}
                />
                {(agents.length > 0 || w.agents.length > 0) && (
                  <div className="field hours-window-agents">
                    <span className="field-label" aria-hidden="true">
                      Agentes
                    </span>
                    <button
                      type="button"
                      className="btn hours-window-agents-button"
                      aria-haspopup="dialog"
                      aria-label={`Agentes da janela ${name}: ${agentsLabel(w.agents)}`}
                      title={agentsTitle(w.agents)}
                      disabled={disabled}
                      onClick={() => setPicking(w.key)}
                    >
                      <Icon name="users" size={14} />
                      <span>{agentsLabel(w.agents)}</span>
                    </button>
                  </div>
                )}
                <div
                  className="field hours-window-pairs"
                  data-invalid={errors.pairs ? "" : undefined}
                >
                  <label className="field-label" htmlFor={fieldId(w, "pairs")}>
                    Ativos
                  </label>
                  <input
                    id={fieldId(w, "pairs")}
                    className="input mono"
                    value={w.pairsText}
                    placeholder="todos (ex.: XAUUSDm;BTCUSDm)"
                    autoCapitalize="none"
                    autoComplete="off"
                    spellCheck={false}
                    disabled={disabled}
                    aria-invalid={errors.pairs ? true : undefined}
                    aria-describedby={errors.pairs ? fieldId(w, "pairs-error") : undefined}
                    onChange={(e) => update(w.key, { pairsText: e.target.value })}
                  />
                  {errors.pairs && (
                    <span id={fieldId(w, "pairs-error")} className="field-error">
                      {errors.pairs}
                    </span>
                  )}
                </div>
                <fieldset className="hours-end hours-window-checks">
                  <legend className="field-label">No fim da janela</legend>
                  <label className="hours-check">
                    <input
                      type="checkbox"
                      checked={w.flatten}
                      disabled={disabled}
                      onChange={(e) => update(w.key, { flatten: e.target.checked })}
                    />
                    Zerar todas as posições abertas
                  </label>
                  <label className="hours-check">
                    <input
                      type="checkbox"
                      checked={w.cancel_pending}
                      disabled={disabled}
                      onChange={(e) => update(w.key, { cancel_pending: e.target.checked })}
                    />
                    Cancelar todas as ordens pendentes
                  </label>
                </fieldset>
                {errors.window && (
                  <span id={fieldId(w, "error")} className="field-error hours-window-error">
                    {errors.window}
                  </span>
                )}
              </fieldset>
            );
          })}
        </div>
      )}
      {check.list && <p className="field-error">{check.list}</p>}
      <div>
        <button
          ref={addRef}
          type="button"
          className="btn btn-sm"
          disabled={disabled || full}
          title={full ? `Use no máximo ${MAX_WINDOWS} janelas.` : undefined}
          onClick={add}
        >
          <Icon name="plus" size={14} />
          Adicionar janela
        </button>
      </div>
      {picked && (
        <AgentsDialog
          title={`Agentes da janela ${windowName(picked, windows.indexOf(picked))}`}
          choices={agents}
          selected={picked.agents}
          unavailable={unavailable}
          onChange={(chosen) => update(picked.key, { agents: chosen })}
          onClose={() => setPicking(null)}
        />
      )}
    </div>
  );
}

/** A window named by its times, else by its place (from 1) while they are typed. */
function windowName(w: WindowForm, index: number): string {
  return clockLike(w.start) && clockLike(w.end) ? windowLabel(w) : String(index + 1);
}

/** Whether text looks like a time HH:MM, to name a window by it. */
function clockLike(text: string): boolean {
  return /^\d{2}:\d{2}$/.test(text);
}

/** The start or the end of a window, with its own problem under it. */
function TimeField({
  id,
  className,
  label,
  value,
  error,
  invalid,
  describedBy,
  disabled,
  onChange,
}: {
  id: string;
  className: string;
  label: string;
  value: string;
  error?: string;
  invalid: boolean;
  /** The problem of the whole window, when there is one. */
  describedBy?: string;
  disabled?: boolean;
  onChange: (value: string) => void;
}) {
  const described = [error ? `${id}-error` : null, describedBy].filter(Boolean).join(" ");
  return (
    <div className={`field ${className}`} data-invalid={invalid ? "" : undefined}>
      <label className="field-label" htmlFor={id}>
        {label}
      </label>
      <input
        id={id}
        className="input mono"
        type="time"
        step={60}
        value={value}
        disabled={disabled}
        aria-invalid={invalid ? true : undefined}
        aria-describedby={described || undefined}
        onChange={(e) => onChange(e.target.value)}
      />
      {error && (
        <span id={`${id}-error`} className="field-error">
          {error}
        </span>
      )}
    </div>
  );
}

/**
 * Picks the agents of a window in a modal dialog: one checkbox by agent of
 * choices, then the ones chosen out of them (tagged, to uncheck); none checked
 * for all the agents. A change applies at once; Concluir closes it.
 */
function AgentsDialog({
  title,
  choices,
  selected,
  unavailable,
  onChange,
  onClose,
}: {
  title: string;
  choices: string[];
  selected: string[];
  unavailable: string;
  onChange: (agents: string[]) => void;
  onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const id = useId();
  const names = [...choices, ...selected.filter((name) => !choices.includes(name))];

  useEffect(() => {
    const element = dialog.current;
    if (element && !element.open) element.showModal();
  }, []);

  const close = () => dialog.current?.close();
  // the chosen in the order shown
  const toggle = (name: string, checked: boolean) => {
    const chosen = new Set(selected);
    if (checked) chosen.add(name);
    else chosen.delete(name);
    onChange(names.filter((n) => chosen.has(n)));
  };

  // outside the form of the hours: its own buttons never send it
  return createPortal(
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc closes the dialog natively
    <dialog
      ref={dialog}
      className="dialog dialog-sm"
      aria-labelledby={`${id}-title`}
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current) close(); // the backdrop
      }}
    >
      <DialogHeader id={`${id}-title`} title={title} onClose={close} />
      <div className="dialog-body window-agents-body">
        <p className="field-hint" id={`${id}-hint`}>
          Sem nenhum marcado, todos os agentes operam nesta janela.
        </p>
        <fieldset className="window-agents" aria-describedby={`${id}-hint`}>
          <legend className="sr-only">Agentes que operam nesta janela</legend>
          {names.map((name) => (
            <label key={name} className="hours-check window-agent">
              <input
                type="checkbox"
                checked={selected.includes(name)}
                onChange={(e) => toggle(name, e.target.checked)}
              />
              <span className="window-agent-name">{name}</span>
              {!choices.includes(name) && <span className="tag">{unavailable}</span>}
            </label>
          ))}
        </fieldset>
      </div>
      <div className="dialog-foot">
        <button
          type="button"
          className="btn window-agents-all"
          disabled={selected.length === 0}
          onClick={() => onChange([])}
        >
          Todos os agentes
        </button>
        <button type="button" className="btn btn-primary" onClick={close}>
          Concluir
        </button>
      </div>
    </dialog>,
    document.body,
  );
}
