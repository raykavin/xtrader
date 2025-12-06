// The agents of the dashboard: the bots of its configuration.
import type { AgentStatus } from "../api/types";

/**
 * The agent to show: the wanted one while configured, even offline, else the
 * first online, else the first.
 */
export function pickAgent(agents: AgentStatus[], wanted: string | null): string | null {
  if (wanted && agents.some((a) => a.name === wanted)) return wanted;
  return (agents.find((a) => a.online) ?? agents[0])?.name ?? null;
}

/** The label of an agent in the selector. */
export function agentLabel(agent: AgentStatus): string {
  return agent.online ? agent.name : `${agent.name} (offline)`;
}
