import type { AgentStatus, ControlsResponse } from "../api/types";
import { agentLabel } from "../lib/agents";
import { timeframeLabel } from "../lib/timeframes";
import { BotControl } from "./BotControl";
import { Icon } from "./Icon";
import { Select } from "./Select";

interface Props {
  agents: AgentStatus[];
  agent: string;
  onAgentChange: (agent: string) => void;
  pairs: string[];
  pair: string | null;
  onPairChange: (pair: string) => void;
  /** The language model of the strategy of the agent, when it trades with one. */
  llm?: string;
  /** The timeframe the strategy trades on, once known. */
  timeframe?: string;
  /** The controls of the bot of the agent, null while unknown. */
  controls: ControlsResponse | null;
  onControlsChange: (controls: ControlsResponse) => void;
}

/**
 * The bar atop the panel: the agent and the pair shown, what the strategy
 * trades on (its timeframe, its language model), and its bot, started and
 * stopped there. A row on a wide screen; wrapped on a narrow one.
 */
export function AgentBar({
  agents,
  agent,
  onAgentChange,
  pairs,
  pair,
  onPairChange,
  llm,
  timeframe,
  controls,
  onControlsChange,
}: Props) {
  const online = agents.filter((a) => a.online).length;
  const current = agents.find((a) => a.name === agent);
  return (
    <section className="context-bar" aria-label="Agente e par">
      <div className="context-selects">
        <div className="field">
          <span className="field-label" aria-hidden="true">
            Agente
          </span>
          <Select
            caption="Agente"
            value={agent}
            onChange={onAgentChange}
            summary={`${online} de ${agents.length} online`}
            options={agents.map((a) => ({
              value: a.name,
              label: a.name,
              hint: a.address,
              status: a.online ? "on" : "off",
              description: agentLabel(a),
            }))}
          />
        </div>
        <div className="field">
          <span className="field-label" aria-hidden="true">
            Par
          </span>
          <Select
            caption="Par"
            value={pair}
            onChange={onPairChange}
            placeholder="aguardando dados…"
            options={pairs.map((p) => ({ value: p, label: p }))}
          />
        </div>
      </div>

      <div className="context-meta">
        {current && !current.online && (
          <span className="meta-chip warn" title="A API do agente não responde">
            <Icon name="alert" size={13} />
            Offline
          </span>
        )}
        {timeframe && (
          <span
            className="meta-chip"
            title="Timeframe de execução: o da estratégia e das análises (muda nas configurações do agente)"
          >
            <span className="meta-chip-label">Execução</span>
            {timeframeLabel(timeframe)}
          </span>
        )}
        {llm && (
          <span className="meta-chip accent" title={`Estratégia com LLM: ${llm}`}>
            <Icon name="sparkles" size={13} />
            <span className="meta-chip-text">{llm}</span>
          </span>
        )}
      </div>

      <BotControl agent={agent} controls={controls} onControlsChange={onControlsChange} />
    </section>
  );
}
