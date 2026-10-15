import type { AgentStatus, ControlsResponse } from "../api/types";
import { agentLabel } from "../lib/agents";
import { supervisionBadges } from "../lib/supervisor";
import { timeframeLabel } from "../lib/timeframes";
import { BotControl } from "./BotControl";
import { Select } from "./Select";
import { toneClass } from "./SupervisorAction";

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

/** The pairs shown as segments, side by side; more than these go in a list. */
const PAIR_SEGMENTS = 6;

/**
 * The context of the panel, a bar atop it: the agent and the pair shown, then
 * the state of the agent at a glance: its bot, started and stopped there, and
 * what the strategy trades on (whether the ticket sends orders is on the
 * ticket). A row on a wide screen; wrapped on a narrow one.
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
    <section className="context-bar" aria-label="Agente e estado">
      <div className="context-pick">
        <div className="field context-agent">
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
        <div className="field context-pair">
          <span className="field-label" aria-hidden="true">
            Par
          </span>
          {pairs.length > 1 && pairs.length <= PAIR_SEGMENTS ? (
            <fieldset className="seg seg-fill">
              <legend>Par</legend>
              {pairs.map((p) => (
                <button
                  key={p}
                  type="button"
                  className="seg-item"
                  aria-pressed={p === pair}
                  onClick={() => p !== pair && onPairChange(p)}
                >
                  {p}
                </button>
              ))}
            </fieldset>
          ) : pairs.length === 1 ? (
            <span className="context-static" title="O único par deste agente">
              <span className="sr-only">Par: </span>
              {pairs[0]}
            </span>
          ) : (
            <Select
              caption="Par"
              value={pair}
              onChange={onPairChange}
              placeholder="aguardando dados…"
              options={pairs.map((p) => ({ value: p, label: p }))}
            />
          )}
        </div>
      </div>

      <dl className="status-list">
        {current && !current.online && (
          <div className="status-item">
            <dt className="status-label">Agente</dt>
            <dd className="status-value">
              <span className="status-chip warn" title="A API do agente não responde">
                <i aria-hidden="true" />
                Offline
              </span>
            </dd>
          </div>
        )}
        {supervisionBadges(current?.supervision).length > 0 && (
          <div className="status-item">
            <dt className="status-label">XTrader AI</dt>
            <dd className="status-value">
              {supervisionBadges(current?.supervision).map((badge) => (
                <span key={badge.text} className={toneClass(badge.tone)} title={badge.title}>
                  {badge.text}
                </span>
              ))}
            </dd>
          </div>
        )}
        <BotControl agent={agent} controls={controls} onControlsChange={onControlsChange} />
        {(timeframe || llm) && (
          <div className="status-item">
            <dt className="status-label">Estratégia</dt>
            <dd
              className="status-value"
              title="O timeframe de execução muda nas configurações do agente, com reinício"
            >
              {timeframe && <span className="tag">Executa em {timeframeLabel(timeframe)}</span>}
              {llm && <span className="tag tag-model">{llm}</span>}
            </dd>
          </div>
        )}
      </dl>
    </section>
  );
}
