import { type FormEvent, useCallback, useEffect, useId, useState } from "react";
import { fetchTradingHours, updateTradingHours } from "../api/client";
import type { TradingHoursResponse } from "../api/types";
import {
  crossesMidnight,
  endActionsText,
  type HoursForm,
  hoursError,
  hoursState,
  lastEndLabel,
} from "../lib/tradingHours";
import { Alert, Loading, Skeleton } from "./States";
import { Switch } from "./Switch";

/** How often the state of the hours is read again, in ms. */
const POLL = 5000;

/** The class of a status chip of tone. */
function chip(tone: string): string {
  return `status-chip ${tone === "error" ? "stopped" : tone === "off" ? "" : tone}`.trim();
}

function formOf(data: TradingHoursResponse): HoursForm {
  const { enabled, start, end, flatten, cancel_pending } = data.settings;
  return { enabled, start, end, flatten, cancel_pending };
}

/** The zone of the hours in words, eg: Belém. */
function zoneLabel(zone: string): string {
  const city = zone.split("/").pop()?.replace(/_/g, " ") ?? zone;
  return city === "Belem" ? "Belém" : city;
}

/**
 * The trading hours of all the agents, on the tab Limites: on, no agent opens
 * a position before the start nor from the end; at the end the positions of
 * the agents are zeroed and their pending orders canceled, as checked; how
 * they stand now and how the last end went. A change applies at once.
 */
export function TradingHoursCard() {
  const id = useId();
  const [data, setData] = useState<TradingHoursResponse | null>(null);
  const [form, setForm] = useState<HoursForm | null>(null);
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback((signal?: AbortSignal) => {
    fetchTradingHours(signal)
      .then((response) => {
        setData(response);
        setForm((current) => current ?? formOf(response));
      })
      .catch((e) => {
        if (!signal?.aborted) setError(e instanceof Error ? e.message : String(e));
      });
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal);
    const timer = window.setInterval(() => load(controller.signal), POLL);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, [load]);

  if (!data || !form) {
    return (
      <section className="card">
        {error ? (
          <Alert tone="error">{error}</Alert>
        ) : (
          <Loading label="Carregando o horário de operação">
            <Skeleton height={160} />
          </Loading>
        )}
      </section>
    );
  }

  const set = <K extends keyof HoursForm>(key: K, value: HoursForm[K]) => {
    setForm({ ...form, [key]: value });
    setSaved(null);
  };
  const invalid = hoursError(form);
  const status = data.status;
  const state = hoursState(status);
  const zone = zoneLabel(status.zone);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (invalid) {
      setError(invalid);
      return;
    }
    setBusy(true);
    setError(null);
    updateTradingHours({ settings: { ...form }, reason: reason.trim() || undefined })
      .then((response) => {
        setData(response);
        setForm(formOf(response));
        setReason("");
        setSaved("Salvo e aplicado aos agentes.");
      })
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setBusy(false));
  };

  return (
    <section className="card card-flush" aria-labelledby={`${id}-title`}>
      <header className="card-header">
        <div className="card-heading">
          <span className="eyebrow">Todos os agentes</span>
          <h2 id={`${id}-title`} className="card-title">
            Horário de operação
          </h2>
        </div>
        <span className={chip(state.tone)}>{state.text}</span>
      </header>
      <form className="card-body hours-body" onSubmit={submit} noValidate>
        {error && <Alert tone="error">{error}</Alert>}
        {status.last_end && (
          <p className="hours-last">
            <span className={chip(lastEndLabel(status.last_end, status.zone).tone)}>
              {lastEndLabel(status.last_end, status.zone).text}
            </span>
          </p>
        )}
        <Switch
          checked={form.enabled}
          onChange={(value) => set("enabled", value)}
          label="Ligado"
          disabled={busy}
        />
        <div className="form-grid hours-times">
          <div className="field">
            <label className="field-label" htmlFor={`${id}-start`}>
              Início
            </label>
            <input
              id={`${id}-start`}
              className="input mono"
              type="time"
              step={60}
              value={form.start}
              disabled={busy}
              onChange={(e) => set("start", e.target.value)}
            />
          </div>
          <div className="field">
            <label className="field-label" htmlFor={`${id}-end`}>
              Fim
            </label>
            <input
              id={`${id}-end`}
              className="input mono"
              type="time"
              step={60}
              value={form.end}
              disabled={busy}
              onChange={(e) => set("end", e.target.value)}
            />
          </div>
        </div>
        <p className="field-hint">
          Horário de {zone}. Fora dele, nenhum agente abre posição.
          {crossesMidnight(form) && " O horário passa da meia-noite: termina no dia seguinte."}
        </p>
        <fieldset className="hours-end">
          <legend className="field-label">No horário de fim</legend>
          <label className="hours-check">
            <input
              type="checkbox"
              checked={form.flatten}
              disabled={busy}
              onChange={(e) => set("flatten", e.target.checked)}
            />
            Zerar todas as posições em aberto
          </label>
          <label className="hours-check">
            <input
              type="checkbox"
              checked={form.cancel_pending}
              disabled={busy}
              onChange={(e) => set("cancel_pending", e.target.checked)}
            />
            Cancelar todas as ordens pendentes
          </label>
          <p className="field-hint">
            No fim, {endActionsText(form.flatten, form.cancel_pending)}. Só as posições e ordens dos
            agentes; as manuais nunca são tocadas.
          </p>
        </fieldset>
        <div className="field">
          <label className="field-label" htmlFor={`${id}-reason`}>
            Motivo (registrado)
          </label>
          <input
            id={`${id}-reason`}
            className="input"
            value={reason}
            disabled={busy}
            placeholder="opcional"
            onChange={(e) => setReason(e.target.value)}
          />
        </div>
        <div className="daily-actions">
          {saved && <span className="dialog-foot-hint">{saved}</span>}
          <button type="submit" className="btn btn-primary" disabled={busy || invalid !== null}>
            {busy ? "Salvando…" : "Salvar"}
          </button>
        </div>
      </form>
    </section>
  );
}
