import { useEffect, useRef } from "react";
import type { AgentSettings } from "../api/types";
import { DailyLimitFields } from "./DailyLimitFields";
import { DialogHeader } from "./Dialog";

interface Props {
  agent: AgentSettings;
  onChange: (agent: AgentSettings) => void;
  onClose: () => void;
}

/**
 * The daily limits of an agent, apart from the ones of all the agents
 * together: its loss and gain of the day (realized plus open) in the currency
 * of its account; reached, its positions are zeroed, its pending entries
 * canceled and its entries blocked until 00:00. Saved, it applies at once.
 */
export function DailyLimitDialog({ agent, onChange, onClose }: Props) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const element = dialog.current;
    if (element && !element.open) element.showModal();
  }, []);
  const close = () => dialog.current?.close();
  const paper = agent.mode !== "live";

  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc closes the dialog natively
    <dialog
      ref={dialog}
      className="dialog dialog-md"
      aria-labelledby="daily-limit-title"
      onClose={onClose}
      onClick={(e) => {
        if (e.target === dialog.current) close(); // the backdrop
      }}
    >
      <DialogHeader
        id="daily-limit-title"
        title="Limite diário do agente"
        subtitle={agent.name}
        onClose={close}
      />
      <div className="dialog-body protection-body">
        <p className="field-hint">
          O resultado do dia deste agente (o realizado desde 00:00, com swap, comissão e taxas, mais
          o P/L das posições abertas agora), na moeda da conta dele. Atingida a perda ou o ganho, as
          posições dele são zeradas, as ordens pendentes de entrada canceladas e as entradas
          bloqueadas até 00:00 (Belém). Vale junto com o limite de todos os agentes: uma entrada só
          ocorre se os dois permitirem.
        </p>
        {paper && (
          <p className="msg msg-warn">
            Agente em paper: o limite fica guardado, mas só vale em live (o resultado vem da conta
            MT5).
          </p>
        )}
        <DailyLimitFields
          agent={agent.name}
          settings={agent.daily_limit}
          onSaved={(settings) => onChange({ ...agent, daily_limit: settings })}
        />
      </div>
      <footer className="dialog-foot">
        <button type="button" className="btn" onClick={close}>
          Fechar
        </button>
      </footer>
    </dialog>
  );
}
