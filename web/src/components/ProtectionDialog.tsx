import { useEffect, useRef, useState } from "react";
import { updateAgentSettings } from "../api/client";
import type { AgentSettings } from "../api/types";
import {
  type ProtectionForm,
  protectionErrors,
  protectionForm,
  protectionSettings,
  unitLabel,
} from "../lib/protection";
import { DialogHeader } from "./Dialog";
import { Alert } from "./States";
import { Switch } from "./Switch";

interface Props {
  agent: AgentSettings;
  onChange: (agent: AgentSettings) => void;
  onClose: () => void;
}

/**
 * The protection of the open positions of an agent: its breakeven and its
 * trailing stop, each on or off, its distances in ATR, and whether the XTrader
 * AI may arm it on a position, its trigger kept. Saved, it applies at once,
 * without a restart.
 */
export function ProtectionDialog({ agent, onChange, onClose }: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [form, setForm] = useState<ProtectionForm>(() => protectionForm(agent.protection));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    const element = dialog.current;
    if (element && !element.open) element.showModal();
  }, []);

  const close = () => dialog.current?.close();
  const errors = protectionErrors(form);
  const invalid = Object.keys(errors).length > 0;
  const set = <K extends keyof ProtectionForm>(field: K, value: ProtectionForm[K]) => {
    setForm((current) => ({ ...current, [field]: value }));
    setSaved(false);
  };
  const isLLM = agent.kind === "llm";

  const save = async () => {
    if (invalid) return;
    setBusy(true);
    setError(null);
    try {
      onChange(await updateAgentSettings(agent.name, { protection: protectionSettings(form) }));
      setSaved(true);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const distance = (field: keyof ProtectionForm, label: string, hint: string) => {
    const id = `protection-${field}`;
    return (
      <div className="field">
        <label className="field-label" htmlFor={id}>
          {label}
        </label>
        <div className="protection-input">
          <input
            id={id}
            className="input"
            inputMode="decimal"
            value={form[field] as string}
            disabled={busy}
            aria-describedby={`${id}-hint`}
            aria-invalid={errors[field] ? true : undefined}
            onChange={(e) => set(field, e.target.value)}
          />
          <span className="protection-unit">{unitLabel}</span>
        </div>
        <p id={`${id}-hint`} className="field-hint">
          {hint}
        </p>
        {errors[field] && <p className="field-error">{errors[field]}</p>}
      </div>
    );
  };

  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc closes the dialog natively
    <dialog
      ref={dialog}
      className="dialog dialog-md"
      aria-labelledby="protection-title"
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current && !busy) close(); // the backdrop
      }}
    >
      <DialogHeader
        id="protection-title"
        title="Proteção de posições"
        subtitle={agent.name}
        onClose={close}
        disabled={busy}
      />
      <div className="dialog-body protection-body">
        <p className="field-hint">
          Distâncias em múltiplos do ATR(14) dos candles fechados do timeframe do agente, em preço
          de cada ativo. O stop só aperta: nunca afrouxa um stop mais protetor e nunca é removido se
          uma atualização falhar. Vale na hora, sem reiniciar, enquanto o agente estiver rodando.
        </p>
        {error && <Alert tone="error">{error}</Alert>}

        <section
          className="dialog-section protection-feature"
          aria-labelledby="protection-breakeven"
        >
          <h3 id="protection-breakeven" className="section-title">
            Breakeven
          </h3>
          <div className="protection-switches">
            <Switch
              checked={form.breakevenEnabled}
              onChange={(value) => set("breakevenEnabled", value)}
              label="Ligado"
              disabled={busy}
            />
            <Switch
              checked={form.breakevenSupervisor}
              onChange={(value) => set("breakevenSupervisor", value)}
              label="XTrader AI pode acionar"
              disabled={busy}
              title="O XTrader AI arma o breakeven numa posição; o gatilho abaixo continua valendo"
            />
          </div>
          <div className="form-grid">
            {distance(
              "breakevenTrigger",
              "Gatilho",
              "Avanço a favor desde a entrada (pelo bid na compra, pelo ask na venda).",
            )}
            {distance(
              "breakevenMargin",
              "Margem",
              "Além da entrada, somada aos custos conhecidos.",
            )}
          </div>
          <p className="field-hint">
            O stop vai para a entrada + margem + custos conhecidos (comissão e swap negativo). Isso
            não garante resultado líquido zero: spread e slippage na saída, comissão e swap futuros
            podem deixar o resultado negativo.
          </p>
        </section>

        <section
          className="dialog-section protection-feature"
          aria-labelledby="protection-trailing"
        >
          <h3 id="protection-trailing" className="section-title">
            Trailing stop
          </h3>
          <div className="protection-switches">
            <Switch
              checked={form.trailingEnabled}
              onChange={(value) => set("trailingEnabled", value)}
              label="Ligado"
              disabled={busy}
            />
            <Switch
              checked={form.trailingSupervisor}
              onChange={(value) => set("trailingSupervisor", value)}
              label="XTrader AI pode acionar"
              disabled={busy}
              title="O XTrader AI arma o trailing numa posição; o gatilho abaixo continua valendo"
            />
          </div>
          <div className="form-grid">
            {distance("trailingTrigger", "Gatilho", "Avanço a favor desde a entrada para ativar.")}
            {distance("trailingDistance", "Distância", "Atrás do preço de saída.")}
            {distance("trailingStep", "Passo", "Melhora mínima do stop para enviar à corretora.")}
          </div>
        </section>

        <ul className="protection-notes">
          <li>
            Vale o stop mais protetor entre o da estratégia, o breakeven, o trailing e o do XTrader
            AI.
          </li>
          <li>
            Uma posição sem stop recebe um quando o gatilho é atingido; o ajuste manual pelo painel
            continua livre.
          </li>
          {isLLM && (
            <li>
              Com a proteção ligada, o modelo nunca afrouxa o stop, mesmo com{" "}
              <code>llm-widen-stop</code>.
            </li>
          )}
        </ul>
      </div>
      <footer className="dialog-foot">
        {saved && <span className="dialog-foot-hint">Salvo: vale a partir do próximo preço.</span>}
        <button type="button" className="btn" onClick={close} disabled={busy}>
          Fechar
        </button>
        <button type="button" className="btn btn-primary" onClick={save} disabled={busy || invalid}>
          {busy ? "Salvando…" : "Salvar"}
        </button>
      </footer>
    </dialog>
  );
}
