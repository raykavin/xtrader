import { useState } from "react";
import { type DailyLimitOutcome, setDailyLimits } from "../api/client";
import type { DailyLimitPreview, DailyLimitSettings, DailyLimitsResponse } from "../api/types";
import { PermLimitsManage } from "../api/types";
import {
  type DailyLimitForm,
  dailyLimitErrors,
  dailyLimitForm,
  dailyLimitSettings,
  resultAmount,
} from "../lib/dailyLimits";
import { useCan } from "./AccessContext";
import { Alert } from "./States";
import { Switch } from "./Switch";

interface Props {
  /** The agent whose limits these are; all the agents together when absent. */
  agent?: string;
  /**
   * The currency told beside the amounts: of the account of the agent, or of
   * the accounts of all the agents ("em cada moeda" when several).
   */
  unit?: string;
  settings: DailyLimitSettings | undefined;
  /** Called once a change is applied: the limits saved, and the answer of the controller. */
  onSaved: (settings: DailyLimitSettings, response: DailyLimitsResponse) => void;
}

/**
 * The form of daily limits, of all the agents together or of an agent, in the
 * currency of the accounts (with accounts in several, the same amounts apply
 * to each apart, none converted): on or off, the loss and the gain of the
 * day, why. Saved, a change that would trip or release the guard now comes
 * back with what it would do, to confirm before it is applied.
 */
export function DailyLimitFields({ agent, unit, settings, onSaved }: Props) {
  const manage = useCan(PermLimitsManage); // changed by a user allowed to only, seen by the others
  const global = agent === undefined;
  const [form, setForm] = useState<DailyLimitForm>(() => dailyLimitForm(settings));
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [preview, setPreview] = useState<DailyLimitPreview | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const errors = dailyLimitErrors(form);
  const invalid = Object.keys(errors).length > 0;
  const set = <K extends keyof DailyLimitForm>(field: K, value: DailyLimitForm[K]) => {
    setForm((current) => ({ ...current, [field]: value }));
    setPreview(null);
    setSaved(null);
  };
  const id = agent ? `daily-${agent}` : "daily-global";

  const save = async (confirm: boolean) => {
    if (invalid) return;
    setBusy(true);
    setError(null);
    try {
      const next = dailyLimitSettings(form);
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
          ? "Salvo: o bloqueio de hoje foi liberado."
          : outcome.response.effect === "trip"
            ? "Salvo: o limite já foi atingido. As posições serão zeradas e as entradas bloqueadas em instantes."
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
    <fieldset className="daily-fields fieldset-plain" disabled={!manage}>
      {error && <Alert tone="error">{error}</Alert>}
      <Switch
        checked={form.enabled}
        onChange={(value) => set("enabled", value)}
        label="Ligado"
        disabled={busy}
      />
      {global && (
        <p className="field-hint">
          Na moeda de cada conta, sem conversão: com contas em moedas diferentes, o mesmo valor vale
          para cada moeda separadamente.
        </p>
      )}
      <div className="form-grid">
        {amount(
          "maxLoss",
          "Perda máxima do dia",
          "Atingida (realizado + aberto), zera as posições e bloqueia as entradas até 00:00.",
        )}
        {amount("maxGain", "Ganho máximo do dia", "Atingido, faz o mesmo: protege o lucro do dia.")}
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
          {(preview.results ?? (preview.result ? [preview.result] : [])).map((result) => (
            <p key={result.currency || "none"} className="field-hint">
              Resultado de hoje: {resultAmount(result.total, result.currency)} (realizado{" "}
              {resultAmount(result.realized, result.currency)}, aberto{" "}
              {resultAmount(result.open, result.currency)})
            </p>
          ))}
        </Alert>
      )}
      {manage && (
        <div className="daily-actions">
          {saved && <span className="dialog-foot-hint">{saved}</span>}
          {preview ? (
            <>
              <button
                type="button"
                className="btn"
                disabled={busy}
                onClick={() => setPreview(null)}
              >
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
      )}
    </fieldset>
  );
}
