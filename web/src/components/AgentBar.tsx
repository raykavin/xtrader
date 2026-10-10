import type { AgentStatus, ControlsResponse } from "../api/types";
import { agentLabel } from "../lib/agents";
import { formatPrice, formatProfit, formatQuantity, signClass } from "../lib/format";
import type { PositionPnL } from "../lib/stats";
import { timeframeLabel } from "../lib/timeframes";
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
  /** The timeframe the strategy trades on, once known. */
  timeframe?: string;
  /** The open position of the pair and its profit, null when flat, undefined while unknown. */
  pnl?: PositionPnL | null;
  /** The controls of the bot of the agent, null while unknown. */
  controls: ControlsResponse | null;
  onControlsChange: (controls: ControlsResponse) => void;
}

/**
 * The head of the panel: the agent and the pair shown, then the state of the
 * agent at a glance: its bot, started and stopped there, the position held
 * and its profit, whether the ticket sends orders, and what the strategy
 * trades on. A row on a wide screen; wrapped on a narrow one.
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
  pnl,
  controls,
  onControlsChange,
}: Props) {
  const online = agents.filter((a) => a.online).length;
  const current = agents.find((a) => a.name === agent);
  return (
    <section className="status-head" aria-label="Agente e estado">
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

      <dl className="status-list">
        {current && !current.online && (
          <div className="status-item">
            <dt className="status-label">Agente</dt>
            <dd className="status-value warn" title="A API do agente não responde">
              Offline
            </dd>
          </div>
        )}
        <BotControl agent={agent} controls={controls} onControlsChange={onControlsChange} />
        <div className="status-item">
          <dt className="status-label">Posição</dt>
          <dd className="status-value">
            {pnl === undefined ? (
              "…"
            ) : pnl === null ? (
              <span className="muted">Sem posição</span>
            ) : (
              <>
                <span className={`side-chip ${pnl.side === "BUY" ? "buy" : "sell"}`}>
                  {pnl.side === "BUY" ? "Comprado" : "Vendido"}
                </span>
                <span className="position-size">
                  {formatQuantity(pnl.quantity)} a {formatPrice(pnl.entry)}
                </span>
                <strong className={`position-pnl ${signClass(pnl.profit)}`}>
                  {formatProfit(pnl.profit)}
                  <small>{pnl.currency}</small>
                </strong>
              </>
            )}
          </dd>
        </div>
        <div className="status-item">
          <dt className="status-label">Boleta</dt>
          <dd
            className="status-value"
            title={
              controls?.enabled
                ? "O painel de trading do agente está ligado: a boleta envia ordens"
                : "O painel de trading do agente está desligado: a boleta não envia ordens"
            }
          >
            {controls === null ? (
              "…"
            ) : (
              <>
                <i className={controls.enabled ? "dot on" : "dot idle"} aria-hidden="true" />
                {controls.enabled ? "Liberada" : "Bloqueada"}
              </>
            )}
          </dd>
        </div>
        {(timeframe || llm) && (
          <div className="status-item">
            <dt className="status-label">Estratégia</dt>
            <dd
              className="status-value"
              title="O timeframe de execução muda nas configurações do agente, com reinício"
            >
              {timeframe && `Executa em ${timeframeLabel(timeframe)}`}
              {llm && (
                <span className="status-model">
                  {timeframe ? "com " : ""}
                  {llm}
                </span>
              )}
            </dd>
          </div>
        )}
      </dl>
    </section>
  );
}
