import type { CurrencyAmount, OpenPositionsResponse } from "../api/types";
import { formatProfit } from "./format";

/** The open profit of an agent: its positions open and their profit, by the currency of their account. */
export interface AgentOpenPL {
  agent: string;
  positions: number;
  /** Whether it runs; else stopped or disabled, its positions open all the same. */
  running: boolean;
  /** The profit of its positions by the currency of their account, none converted. */
  amounts: CurrencyAmount[];
}

/** The open profit of each agent with positions open, in the order of the read (of the dashboard). */
export function openPLByAgent(read: OpenPositionsResponse): AgentOpenPL[] {
  const agents: AgentOpenPL[] = [];
  for (const position of read.positions) {
    let agent = agents.find((a) => a.agent === position.agent);
    if (!agent) {
      agent = { agent: position.agent, positions: 0, running: false, amounts: [] };
      agents.push(agent);
    }
    agent.positions += 1;
    agent.running ||= position.running;
    const amount = agent.amounts.find((a) => a.currency === position.currency);
    if (amount) amount.amount += position.profit;
    else agent.amounts.push({ currency: position.currency, amount: position.profit });
  }
  return agents;
}

/**
 * The profit of an agent as its line shows it: in the currency of its account,
 * its code told when the read has several, eg: +81,63, or +81,63 BRL · +3,20 USD.
 */
export function agentOpenPLText(agent: AgentOpenPL, read: OpenPositionsResponse): string {
  const several = read.totals.length > 1;
  return agent.amounts
    .map((a) => (several ? `${formatProfit(a.amount)} ${a.currency}` : formatProfit(a.amount)))
    .join(" · ");
}

const gapReasons: Record<string, string> = {
  unreachable: "a bridge da conta não respondeu",
  refused: "a bridge recusou o token",
  failed: "o terminal da conta respondeu com erro",
};

/** Why the open profit is incomplete, in the words of the dashboard: the agents whose account wasn't read. */
export function openPLGaps(read: OpenPositionsResponse): string[] {
  return (read.missing ?? []).map(
    (gap) => `Sem dados de ${gap.agents.join(", ")}: ${gapReasons[gap.reason] ?? gap.reason}`,
  );
}

/** The count of the positions open and of their agents, eg: 3 posições abertas em 2 agentes. */
export function openPLSummary(read: OpenPositionsResponse): string {
  const positions = read.positions.length;
  if (positions === 0) return "Sem posições abertas";
  const agents = new Set(read.positions.map((p) => p.agent)).size;
  const count = positions === 1 ? "1 posição aberta" : `${positions} posições abertas`;
  return `${count} em ${agents === 1 ? "1 agente" : `${agents} agentes`}`;
}
