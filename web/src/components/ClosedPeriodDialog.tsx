import { type FormEvent, useEffect, useRef, useState } from "react";
import {
  type ClosedPeriod,
  type ClosedPeriodKind,
  closedPeriodNames,
  customPeriodError,
  dayOf,
} from "../lib/closedPL";
import { DialogHeader } from "./Dialog";

interface Props {
  /** The period shown now. */
  period: ClosedPeriod;
  onApply: (period: ClosedPeriod) => void;
  onClose: () => void;
  /** The title of the dialog, of the closed profit by default. */
  title?: string;
}

/** What each kind of period counts, by the clock of the browser. */
const hints: Record<ClosedPeriodKind, string> = {
  today: "Desde 00:00 de hoje",
  week: "Desde segunda-feira, 00:00",
  month: "Desde o dia 1, 00:00",
  custom: "Do primeiro ao último dia escolhido, os dois inclusive",
};

const kinds: ClosedPeriodKind[] = ["today", "week", "month", "custom"];

/**
 * Picks the period of the closed profit of all the agents in a modal dialog:
 * today, this week, this month, or the days the user chooses, by the clock of
 * the browser.
 */
export function ClosedPeriodDialog({
  period,
  onApply,
  onClose,
  title = "Período do P/L fechado",
}: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  const today = dayOf(new Date());
  const [kind, setKind] = useState<ClosedPeriodKind>(period.kind);
  const [from, setFrom] = useState(period.from ?? today);
  const [to, setTo] = useState(period.to ?? today);
  const invalid = kind === "custom" ? customPeriodError(from, to) : null;

  useEffect(() => {
    const element = dialog.current;
    if (element && !element.open) element.showModal();
  }, []);

  const close = () => dialog.current?.close();

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (invalid) return;
    onApply(kind === "custom" ? { kind, from, to } : { kind });
    close();
  };

  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc closes the dialog natively
    <dialog
      ref={dialog}
      className="dialog dialog-sm"
      aria-labelledby="closed-period-title"
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current) close(); // the backdrop
      }}
    >
      <DialogHeader id="closed-period-title" title={title} onClose={close} />
      <form className="dialog-form" onSubmit={submit} noValidate>
        <div className="dialog-body">
          <fieldset className="period-options">
            <legend className="field-label">Contar as posições fechadas</legend>
            {kinds.map((option) => (
              <label key={option} className="period-option">
                <input
                  type="radio"
                  name="closed-period"
                  value={option}
                  checked={kind === option}
                  onChange={() => setKind(option)}
                />
                <span>
                  <strong>{closedPeriodNames[option]}</strong>
                  <small>{hints[option]}</small>
                </span>
              </label>
            ))}
          </fieldset>
          {kind === "custom" && (
            <div className="period-range" data-invalid={invalid ? "" : undefined}>
              <div className="field">
                <label className="field-label" htmlFor="closed-period-from">
                  De
                </label>
                <input
                  id="closed-period-from"
                  className="input"
                  type="date"
                  value={from}
                  max={to || undefined}
                  aria-invalid={invalid !== null}
                  aria-describedby="closed-period-hint"
                  onChange={(e) => setFrom(e.target.value)}
                />
              </div>
              <div className="field">
                <label className="field-label" htmlFor="closed-period-to">
                  Até
                </label>
                <input
                  id="closed-period-to"
                  className="input"
                  type="date"
                  value={to}
                  min={from || undefined}
                  aria-invalid={invalid !== null}
                  aria-describedby="closed-period-hint"
                  onChange={(e) => setTo(e.target.value)}
                />
              </div>
              <p className={invalid ? "field-error" : "field-hint"} id="closed-period-hint">
                {invalid ?? "Os dias pelo horário deste navegador."}
              </p>
            </div>
          )}
        </div>
        <div className="dialog-foot">
          <button type="button" className="btn" onClick={close}>
            Cancelar
          </button>
          <button type="submit" className="btn btn-primary" disabled={invalid !== null}>
            Aplicar
          </button>
        </div>
      </form>
    </dialog>
  );
}
