import type { AgentStatus, ControlsResponse } from "../api/types";
import { agentLabel } from "../lib/agents";
import { BotControl } from "./BotControl";
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
  /** The controls of the bot of the agent, null while unknown. */
  controls: ControlsResponse | null;
  onControlsChange: (controls: ControlsResponse) => void;
}

/**
 * The card atop the dashboard: the agent and the pair shown, the language
 * model of the strategy, and the bot of the agent, started and stopped there. A row on a wide screen, two columns on a narrow one;
 * whether the updates are live, the version and the theme are in the Header.
 */
export function AgentBar({
  agents,
  agent,
  onAgentChange,
  pairs,
  pair,
  onPairChange,
  llm,
  controls,
  onControlsChange,
}: Props) {
  const online = agents.filter((a) => a.online).length;
  return (
    <section className="card agent-bar" aria-label="Agente e par">
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
      {llm && (
        <div className="field field-wide">
          <span className="field-label">Modelo de IA</span>
          <span className="llm-chip" title={`Estratégia com LLM: ${llm}`}>
            {llm}
          </span>
        </div>
      )}
      <BotControl agent={agent} controls={controls} onControlsChange={onControlsChange} />
    </section>
  );
}
