import { type FormEvent, useCallback, useEffect, useId, useState } from "react";
import { fetchAgentSettings, fetchTradingHours, updateTradingHours } from "../api/client";
import type { TradingHoursResponse } from "../api/types";
import { PermLimitsManage } from "../api/types";
import {
  type HoursForm,
  hoursError,
  hoursSettings,
  hoursState,
  windowsForm,
  windowsOf,
} from "../lib/tradingHours";
import { useCan } from "./AccessContext";
import { Alert, Loading, Skeleton } from "./States";
import { Switch } from "./Switch";
import { TradingWindowsEditor } from "./TradingWindowsEditor";

/** How often the state of the hours is read again, in ms. */
const POLL = 5000;

/** The class of a status chip of tone. */
function chip(tone: string): string {
  return `status-chip ${tone === "error" ? "stopped" : tone === "off" ? "" : tone}`.trim();
}

function formOf(data: TradingHoursResponse): HoursForm {
  return { enabled: data.settings.enabled, windows: windowsForm(windowsOf(data.settings)) };
}

/** The zone of the hours in words, eg: Belém. */
function zoneLabel(zone: string): string {
  const city = zone.split("/").pop()?.replace(/_/g, " ") ?? zone;
  return city === "Belem" ? "Belém" : city;
}

/**
 * The trading hours of all the agents, on the tab Limites: on, no agent opens
 * a position outside their windows (nor in a window limited to other agents,
 * chosen among the ones on); at the end of each window the positions of the
 * agents are zeroed and their pending orders canceled, as it checks; how they
 * stand now and how the last end went. A change applies at once.
 */
export function TradingHoursCard() {
  const manage = useCan(PermLimitsManage); // changed by a user allowed to only, seen by the others
  const id = useId();
  const [data, setData] = useState<TradingHoursResponse | null>(null);
  const [form, setForm] = useState<HoursForm | null>(null);
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // the agents on, by name: the ones a window may be limited to
  const [agents, setAgents] = useState<string[]>([]);
  useEffect(() => {
    const controller = new AbortController();
    fetchAgentSettings(controller.signal)
      .then((list) =>
        setAgents(
          list
            .filter((a) => a.enabled)
            .map((a) => a.name)
            .sort((a, b) => a.localeCompare(b, "pt-BR")),
        ),
      )
      .catch(() => {
        // without them, a window keeps its agents and the button only shows them
      });
    return () => controller.abort();
  }, []);

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
    updateTradingHours({ settings: hoursSettings(form), reason: reason.trim() || undefined })
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
        <fieldset className="fieldset-contents" disabled={!manage}>
          {error && <Alert tone="error">{error}</Alert>}
          <Switch
            checked={form.enabled}
            onChange={(value) => set("enabled", value)}
            label="Ligado"
            disabled={busy}
          />
          <p className="field-hint">
            Horário de {zone}. Fora das janelas, nenhum agente abre posição. Cada janela começa e
            termina no mesmo dia.
          </p>
          <TradingWindowsEditor
            windows={form.windows}
            onChange={(windows) => set("windows", windows)}
            required={form.enabled}
            agents={agents}
            unavailable="desligado"
            disabled={busy}
          />
          <p className="field-hint">
            Agentes e ativos de uma janela: sem nenhum, valem todos. No fim de cada janela, vale o
            que estiver marcado nela, para todos os agentes e ativos; sem nada marcado, só bloqueia
            novas entradas. Só as posições e ordens dos agentes: as manuais não são afetadas.
          </p>
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
          {manage && (
            <div className="daily-actions">
              {saved && <span className="dialog-foot-hint">{saved}</span>}
              <button type="submit" className="btn btn-primary" disabled={busy || invalid !== null}>
                {busy ? "Salvando…" : "Salvar"}
              </button>
            </div>
          )}
        </fieldset>
      </form>
    </section>
  );
}
