import { useState } from "react";
import { type DailyLimitOutcome, setDailyLimits } from "../api/client";
import type { DailyLimitPreview, DailyLimitSettings, DailyLimitsResponse } from "../api/types";
import {
  type DailyLimitForm,
  dailyLimitErrors,
  dailyLimitForm,
  dailyLimitSettings,
  resultAmount,
} from "../lib/dailyLimits";
import { Alert } from "./States";
import { Switch } from "./Switch";

interface Props {
  /** The agent whose limits these are; all the agents together when absent. */
  agent?: string;
  /** The currency of the account of the agent, told beside its amounts. */
  accountCurrency?: string;
  settings: DailyLimitSettings | undefined;
  /** Called once a change is applied: the limits saved, and the answer of the controller. */
  onSaved: (settings: DailyLimitSettings, response: DailyLimitsResponse) => void;
}

/**
 * The form of daily limits, of all the agents together (its currency, every
 * account converted to it) or of an agent (in the currency of its account):
 * on or off, the loss and the gain of the day, why. Saved, a change that
 * would trip or release the guard now comes back with what it would do, to
 * confirm before it is applied.
 */
export function DailyLimitFields({ agent, accountCurrency, settings, onSaved }: Props) {
  const global = agent === undefined;
  const [form, setForm] = useState<DailyLimitForm>(() => dailyLimitForm(settings));
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [preview, setPreview] = useState<DailyLimitPreview | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const errors = dailyLimitErrors(form, global);
  const invalid = Object.keys(errors).length > 0;
  const set = <K extends keyof DailyLimitForm>(field: K, value: DailyLimitForm[K]) => {
    setForm((current) => ({ ...current, [field]: value }));
    setPreview(null);
    setSaved(null);
  };
  const unit = global ? form.currency.trim().toUpperCase() : (accountCurrency ?? "");
  const id = agent ? `daily-${agent}` : "daily-global";

  const save = async (confirm: boolean) => {
    if (invalid) return;
    setBusy(true);
    setError(null);
    try {
      const next = dailyLimitSettings(form, global);
      const outcome: DailyLimitOutcome = await setDailyLimits(
        { settings: next, reason: reason.trim(), confirm },
        agent,
      );
      if (!outcome.applied) {
        setPreview(outcome.preview);
        return;
      }
      setPreview(null);
      setSaved(
        outcome.response.effect === "release"
          ? "Salvo: a trava de hoje foi liberada."
          : outcome.response.effect === "trip"
            ? "Salvo: o limite já atingido zera as posições e bloqueia as entradas em instantes."
            : "Salvo: vale a partir de agora.",
      );
      onSaved(next, outcome.response);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const amount = (field: "maxLoss" | "maxGain", label: string, hint: string) => (
    <div className="field">
      <label className="field-label" htmlFor={`${id}-${field}`}>
        {label}
      </label>
      <div className="protection-input">
        <input
          id={`${id}-${field}`}
          className="input"
          inputMode="decimal"
          value={form[field]}
          disabled={busy}
          placeholder="sem limite"
          aria-describedby={`${id}-${field}-hint`}
          aria-invalid={errors[field] ? true : undefined}
          onChange={(e) => set(field, e.target.value)}
        />
        {unit && <span className="protection-unit">{unit}</span>}
      </div>
      <p id={`${id}-${field}-hint`} className="field-hint">
        {hint}
      </p>
      {errors[field] && <p className="field-error">{errors[field]}</p>}
    </div>
  );

  return (
    <div className="daily-fields">
      {error && <Alert tone="error">{error}</Alert>}
      <Switch
        checked={form.enabled}
        onChange={(value) => set("enabled", value)}
        label="Ligado"
        disabled={busy}
      />
      <div className="form-grid">
        {amount(
          "maxLoss",
          "Perda máxima do dia",
          "Atingida (realizado + aberto), zera as posições e bloqueia as entradas até 00:00.",
        )}
        {amount(
          "maxGain",
          "Ganho máximo do dia",
          "Atingido, faz o mesmo: protege o lucro do dia de ser devolvido.",
        )}
        {global && (
          <div className="field">
            <label className="field-label" htmlFor={`${id}-currency`}>
              Moeda
            </label>
            <input
              id={`${id}-currency`}
              className="input"
              value={form.currency}
              maxLength={3}
              disabled={busy}
              placeholder="BRL"
              aria-invalid={errors.currency ? true : undefined}
              onChange={(e) => set("currency", e.target.value)}
            />
            <p className="field-hint">Contas em outra moeda são convertidas pela cotação do MT5.</p>
            {errors.currency && <p className="field-error">{errors.currency}</p>}
          </div>
        )}
        <div className="field">
          <label className="field-label" htmlFor={`${id}-reason`}>
            Motivo (registrado)
          </label>
          <input
            id={`${id}-reason`}
            className="input"
            value={reason}
            disabled={busy}
            placeholder="ex.: conter a perda do dia"
            onChange={(e) => setReason(e.target.value)}
          />
        </div>
      </div>
      {preview && (
        <Alert tone="warn" title="Efeito imediato">
          <p>{preview.message}</p>
          {preview.result && (
            <p className="field-hint">
              Resultado de hoje: {resultAmount(preview.result.total, preview.result.currency)}{" "}
              (realizado {resultAmount(preview.result.realized, preview.result.currency)}, aberto{" "}
              {resultAmount(preview.result.open, preview.result.currency)})
            </p>
          )}
        </Alert>
      )}
      <div className="daily-actions">
        {saved && <span className="dialog-foot-hint">{saved}</span>}
        {preview ? (
          <>
            <button type="button" className="btn" disabled={busy} onClick={() => setPreview(null)}>
              Cancelar
            </button>
            <button
              type="button"
              className="btn btn-danger"
              disabled={busy || invalid}
              onClick={() => save(true)}
            >
              {busy ? "Aplicando…" : "Confirmar e aplicar"}
            </button>
          </>
        ) : (
          <button
            type="button"
            className="btn btn-primary"
            disabled={busy || invalid}
            onClick={() => save(false)}
          >
            {busy ? "Salvando…" : "Salvar"}
          </button>
        )}
      </div>
    </div>
  );
}
