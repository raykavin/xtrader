import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import { createBacktest, fetchTradingHours } from "../api/client";
import type { BacktestBatchInfo, BacktestCompatibility, BacktestsResponse } from "../api/types";
import {
  type BacktestForm,
  backtestTimeframes,
  batchHours,
  defaultForm,
  type FormErrors,
  formCalls,
  formPeriod,
  formRequest,
  hoursAssumption,
  outsideWindows,
  pairsTested,
  runsOf,
  withHours,
} from "../lib/backtest";
import { formatMoney, formatNumber } from "../lib/format";
import { timeframeLabel } from "../lib/timeframes";
import { windowsOf } from "../lib/tradingHours";
import { Icon } from "./Icon";
import { Select } from "./Select";
import { Alert } from "./States";
import { TradingWindowsEditor } from "./TradingWindowsEditor";

interface Props {
  read: BacktestsResponse;
  /** The batch queued. */
  onCreated: (batch: BacktestBatchInfo) => void;
}

/** "Do agente" in the select of the timeframe. */
const AGENTS = "";

/** The days of a period of seconds, in words. */
function daysOf(seconds: number): string {
  const days = Math.round(seconds / 86400);
  return days === 1 ? "1 dia" : `${days} dias`;
}

/**
 * The configuration of a batch: the agents (the ones that can't run greyed,
 * with why), the pairs, the period and its validation, the money, the costs,
 * the daily limits and the ceilings of the language models; then the
 * assumptions of the batch, before it is queued.
 */
export function BacktestConfigure({ read, onCreated }: Props) {
  const [form, setForm] = useState<BacktestForm>(() => defaultForm());
  const [errors, setErrors] = useState<FormErrors>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const id = useId();

  const set = <K extends keyof BacktestForm>(field: K, value: BacktestForm[K]) => {
    if (field.startsWith("hours")) touchedHours.current = true;
    setForm((f) => ({ ...f, [field]: value }));
    setErrors((e) => ({ ...e, [field]: undefined, hoursWindows: undefined }));
  };

  // the trading hours of the agents (the tab Limites) as the hours of the
  // batch, unless changed meanwhile
  const touchedHours = useRef(false);
  useEffect(() => {
    const controller = new AbortController();
    fetchTradingHours(controller.signal)
      .then(({ settings }) => {
        if (!touchedHours.current && windowsOf(settings).length > 0) {
          setForm((f) => withHours(f, settings));
        }
      })
      .catch(() => {
        // without them, the hours of the batch start off
      });
    return () => controller.abort();
  }, []);
  const toggle = (list: string[], value: string) =>
    list.includes(value) ? list.filter((v) => v !== value) : [...list, value];

  const chosen = read.agents.filter((a) => form.agents.includes(a.agent));
  const pairs = [...new Set(chosen.flatMap((a) => a.pairs))].sort();
  const llm = chosen.some((a) => a.llm);
  const period = formPeriod(form);
  const maxCalls = Number(form.llmMaxCalls) || read.llm_max_calls;
  const maxCost = Number(form.llmMaxCost.replace(",", ".")) || read.llm_max_cost;
  const calls = formCalls(read.agents, form, period, maxCalls);
  const llmRuns = chosen.filter((a) => a.llm).length * (runsOf(form) / (form.agents.length || 1));
  const idle = chosen.filter((a) => pairsTested(a, form).length === 0);
  // the agents a window may be limited to: the ones of the batch, else the
  // ones that can run
  const windowAgents =
    form.agents.length > 0
      ? form.agents
      : read.agents.filter((a) => a.compatible).map((a) => a.agent);
  const outside = outsideWindows(form);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const { request, errors: refused } = formRequest(form, read);
    setErrors(refused);
    if (!request) return;
    setSending(true);
    setFailure(null);
    createBacktest(request)
      .then(onCreated)
      .catch((err) => setFailure(err instanceof Error ? err.message : String(err)))
      .finally(() => setSending(false));
  };

  const field = (
    name: keyof BacktestForm,
    label: string,
    hint?: string,
    placeholder?: string,
    suffix?: string,
  ) => (
    <div className="field" data-invalid={errors[name] ? "" : undefined}>
      <label className="field-label" htmlFor={`${id}-${name}`}>
        {label}
      </label>
      <div className={suffix ? "bt-input-suffix" : undefined}>
        <input
          id={`${id}-${name}`}
          className="input mono"
          inputMode="decimal"
          autoComplete="off"
          value={form[name] as string}
          placeholder={placeholder}
          aria-invalid={errors[name] ? true : undefined}
          aria-describedby={hint || errors[name] ? `${id}-${name}-hint` : undefined}
          onChange={(e) => set(name, e.target.value)}
        />
        {suffix && <span aria-hidden="true">{suffix}</span>}
      </div>
      {(errors[name] || hint) && (
        <span id={`${id}-${name}-hint`} className={errors[name] ? "field-error" : "field-hint"}>
          {errors[name] ?? hint}
        </span>
      )}
    </div>
  );

  return (
    <form className="panel-body bt-configure" onSubmit={submit} noValidate>
      <fieldset className="form-section">
        <legend>Agentes</legend>
        {errors.agents && <p className="field-error">{errors.agents}</p>}
        {read.agents.length === 0 ? (
          <p className="field-hint">Nenhum agente cadastrado ainda.</p>
        ) : (
          <div className="bt-agents">
            {read.agents.map((a) => (
              <AgentOption
                key={a.agent}
                agent={a}
                checked={form.agents.includes(a.agent)}
                onChange={() => set("agents", toggle(form.agents, a.agent))}
              />
            ))}
          </div>
        )}
        <p className="field-hint">
          Cada agente roda com uma cópia da configuração atual (estratégia, parâmetros, proteção).
          Ligados ou desligados, todos podem ser testados; nada neles muda.
        </p>
      </fieldset>

      {pairs.length > 1 && (
        <fieldset className="form-section">
          <legend>Ativos</legend>
          <div className="bt-chips">
            {pairs.map((p) => (
              <label key={p} className="bt-chip">
                <input
                  type="checkbox"
                  checked={form.pairs.includes(p)}
                  onChange={() => set("pairs", toggle(form.pairs, p))}
                />
                <span className="mono">{p}</span>
              </label>
            ))}
          </div>
          <p className="field-hint">
            Nenhum marcado: cada agente testa todos os seus ativos. Marcados: só esses, nos agentes
            que os operam.
          </p>
          {idle.length > 0 && (
            <p className="field-error">
              {idle.map((a) => a.agent).join(", ")} não opera(m) nenhum dos ativos marcados.
            </p>
          )}
        </fieldset>
      )}

      <fieldset className="form-section">
        <legend>Período</legend>
        <div className="form-grid bt-grid">
          <div className="field" data-invalid={errors.from ? "" : undefined}>
            <label className="field-label" htmlFor={`${id}-from`}>
              De
            </label>
            <input
              id={`${id}-from`}
              className="input"
              type="date"
              value={form.from}
              max={form.to || undefined}
              onChange={(e) => set("from", e.target.value)}
            />
            {errors.from && <span className="field-error">{errors.from}</span>}
          </div>
          <div className="field" data-invalid={errors.to ? "" : undefined}>
            <label className="field-label" htmlFor={`${id}-to`}>
              Até
            </label>
            <input
              id={`${id}-to`}
              className="input"
              type="date"
              value={form.to}
              min={form.from || undefined}
              onChange={(e) => set("to", e.target.value)}
            />
            <span className={errors.to ? "field-error" : "field-hint"}>
              {errors.to ?? `No fuso deste navegador, até ${read.max_period_days} dias.`}
            </span>
          </div>
          {field(
            "validation",
            "Validação fora da amostra",
            "Parte final do período, rodada à parte para conferir o resultado. 0 para não validar.",
            "30",
            "%",
          )}
          <div className="field" data-invalid={errors.timeframe ? "" : undefined}>
            <span className="field-label">Timeframe</span>
            <Select
              caption="Timeframe"
              value={form.timeframe}
              onChange={(value) => set("timeframe", value)}
              options={[
                { value: AGENTS, label: "O de cada agente" },
                ...backtestTimeframes.map((t) => ({ value: t, label: timeframeLabel(t) })),
              ]}
            />
            <span className={errors.timeframe ? "field-error" : "field-hint"}>
              {errors.timeframe ?? "Outro timeframe testa uma variação, não o agente como está."}
            </span>
          </div>
        </div>
      </fieldset>

      <fieldset className="form-section">
        <legend>Dinheiro e custos</legend>
        <div className="form-grid bt-grid">
          {field(
            "balance",
            "Saldo inicial",
            "Na moeda de cotação do ativo (ex.: USD em XAUUSD).",
            "10000",
          )}
          {field("fee", "Taxa por ordem", "Percentual do valor de cada ordem executada.", "0", "%")}
          {field(
            "spread",
            "Spread de reserva",
            "Usado só nos candles sem o spread da corretora.",
            "0",
            "pts",
          )}
          {field(
            "slippage",
            "Slippage",
            "Aplicado contra você nas ordens a mercado e nos stops.",
            "0",
            "pts",
          )}
        </div>
      </fieldset>

      <fieldset className="form-section">
        <legend>Limites diários</legend>
        <div className="form-grid bt-grid">
          {field("dailyLoss", "Perda máxima no dia", "Em branco, sem limite.", "", "")}
          {field("dailyGain", "Ganho máximo no dia", "Em branco, sem limite.", "", "")}
        </div>
        <p className="field-hint">
          Na moeda de cotação, por execução, no dia de America/Belem: ao atingir, a posição é
          fechada e as entradas param até a meia-noite. Os limites diários dos agentes e o global
          não valem no backtest.
        </p>
      </fieldset>

      <fieldset className="form-section">
        <legend>Horário de operação</legend>
        <label className="hours-check">
          <input
            type="checkbox"
            checked={form.hours}
            onChange={(e) => set("hours", e.target.checked)}
          />
          Aplicar o horário de operação
        </label>
        {form.hours && (
          <TradingWindowsEditor
            windows={form.hoursWindows}
            onChange={(windows) => set("hoursWindows", windows)}
            required
            agents={windowAgents}
            unavailable={form.agents.length > 0 ? "fora do lote" : undefined}
          />
        )}
        {outside.map((problem) => (
          <p key={problem} className="field-error">
            {problem}
          </p>
        ))}
        <p className="field-hint">
          {form.hours
            ? "Horário de America/Belem: fora das janelas, nenhuma entrada; no fim de cada janela, vale o que estiver marcado nela."
            : "Desligado, as execuções operam o dia todo."}{" "}
          Vem de Configurações › Limites; mudar aqui vale só para este lote.
        </p>
      </fieldset>

      <fieldset className="form-section">
        <legend>Parâmetros dos agentes</legend>
        <div className="field" data-invalid={errors.params ? "" : undefined}>
          <label className="field-label" htmlFor={`${id}-params`}>
            Parâmetros extras para todos os agentes do lote
          </label>
          <input
            id={`${id}-params`}
            className="input mono"
            autoComplete="off"
            spellCheck={false}
            value={form.params}
            placeholder="regime=trend; equity-curve=20"
            aria-invalid={errors.params ? true : undefined}
            aria-describedby={`${id}-params-hint`}
            onChange={(e) => set("params", e.target.value)}
          />
          <p className="field-hint" id={`${id}-params-hint`}>
            {errors.params ??
              "nome=valor separados por ;, ex.: filtros de entrada (regime, equity-curve, intermarket-symbol). Listas com vírgula: intermarket-pairs=XAUUSDm,BTCUSDm. Substitui o parâmetro de mesmo nome do agente só neste lote. Não vale para agentes LLM."}
          </p>
        </div>
      </fieldset>

      {llm && (
        <fieldset className="form-section">
          <legend>Agentes LLM</legend>
          <div className="form-grid bt-grid">
            {field(
              "llmMaxCalls",
              "Teto de chamadas por execução",
              undefined,
              String(read.llm_max_calls),
            )}
            {field(
              "llmMaxCost",
              "Teto de custo por execução",
              undefined,
              formatMoney(read.llm_max_cost),
              "USD",
            )}
          </div>
          <Alert tone="warn" title="Custo real no provedor">
            Cada análise chama o LLM com a chave do agente: até ~{formatNumber(calls)} chamada(s) em{" "}
            {formatNumber(llmRuns)} execução(ões), até {formatMoney(maxCost * llmRuns)} USD pelos
            tetos. Ao atingir o teto, a execução para e fica marcada. O resultado varia a cada
            execução: o LLM pode ter visto o período no treino e roda com contexto reduzido (sem
            timeframes superiores, correlatos, calendário nem memória de operações).
          </Alert>
        </fieldset>
      )}

      <fieldset className="form-section">
        <legend>Premissas</legend>
        <ul className="kv-list bt-assumptions">
          <li>
            <span>Período</span>
            <span>
              {period && period.to > period.from ? daysOf(period.to - period.from) : "—"}
              {Number(form.validation) > 0 && `, os últimos ${form.validation}% como validação`}
            </span>
          </li>
          <li>
            <span>Execuções</span>
            <span>
              {runsOf(form)} (máximo {read.max_runs}), em fila no controller; continuam com a página
              fechada
            </span>
          </li>
          <li>
            <span>Horário</span>
            <span>
              {form.hours
                ? hoursAssumption(batchHours(form.hoursWindows)) || "nenhuma janela"
                : "o dia todo"}
            </span>
          </li>
          <li>
            <span>Dados</span>
            <span>
              Candles bid do MT5 pela bridge de cada agente, com o spread de cada candle; compras no
              ask. Lacunas são avisadas, nunca preenchidas.
            </span>
          </li>
          <li>
            <span>Execução</span>
            <span>
              Na resolução mais fina que o período permite (M1 quando cabe). Stop loss e take profit
              no mesmo candle: vale o stop. Entrada e stop no mesmo candle: estopada. Stop com gap:
              na abertura.
            </span>
          </li>
          <li>
            <span>Fim do período</span>
            <span>Posições abertas avaliadas a mercado, mostradas à parte do realizado.</span>
          </li>
          <li>
            <span>Moeda</span>
            <span>A de cotação do ativo, sem conversão para a moeda da conta.</span>
          </li>
        </ul>
      </fieldset>

      <div className="field">
        <label className="field-label" htmlFor={`${id}-name`}>
          Nome do lote
        </label>
        <input
          id={`${id}-name`}
          className="input"
          value={form.name}
          maxLength={80}
          placeholder="Opcional, ex.: tendência XAU 2026"
          onChange={(e) => set("name", e.target.value)}
        />
      </div>

      {failure && (
        <Alert tone="error" title="Não foi possível criar o lote">
          {failure}
        </Alert>
      )}
      <div className="bt-submit">
        <button
          type="submit"
          className="btn btn-primary"
          disabled={sending || form.agents.length === 0}
        >
          <Icon name="trend" size={14} />
          {sending ? "Enviando…" : "Iniciar backtest"}
        </button>
      </div>
    </form>
  );
}

/** An agent to choose: what it trades, or why it can't run. */
function AgentOption({
  agent,
  checked,
  onChange,
}: {
  agent: BacktestCompatibility;
  checked: boolean;
  onChange: () => void;
}) {
  return (
    <label
      className="bt-agent"
      data-disabled={agent.compatible ? undefined : ""}
      title={agent.compatible ? undefined : agent.reason}
    >
      <input type="checkbox" checked={checked} disabled={!agent.compatible} onChange={onChange} />
      <span className="bt-agent-text">
        <strong>
          {agent.agent}
          {agent.llm && <span className="tag tag-live">LLM · custo</span>}
          {!agent.enabled && <span className="tag">desligado</span>}
        </strong>
        <small>
          {agent.compatible
            ? [
                agent.strategy ?? agent.kind,
                agent.pairs.join(", "),
                agent.timeframe ? timeframeLabel(agent.timeframe) : null,
              ]
                .filter(Boolean)
                .join(" · ")
            : `Incompatível: ${agent.reason}`}
        </small>
      </span>
    </label>
  );
}
