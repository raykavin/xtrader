import type { ClosedProfitResponse, CurrencyAmount, OpenPositionsResponse } from "../api/types";
import { formatProfit } from "./format";

/** The open profit of an agent: its positions open and their profit, by the currency of their account. */
export interface AgentOpenPL {
  agent: string;
  positions: number;
  /** Whether it runs; else stopped or disabled, its positions open all the same. */
  running: boolean;
  /** The profit of its positions by the currency of their account, none converted. */
  amounts: CurrencyAmount[];
  /** Its positions by asset, in the order of the read. */
  assets: AssetOpenPL[];
}

/** The positions open of an agent on an asset, eg: XAUUSDm: their count and their profit by the currency of their account. */
export interface AssetOpenPL {
  pair: string;
  positions: number;
  amounts: CurrencyAmount[];
}

/** Adds profit in currency to amounts, one per currency. */
function addAmount(amounts: CurrencyAmount[], currency: string, profit: number): void {
  const amount = amounts.find((a) => a.currency === currency);
  if (amount) amount.amount += profit;
  else amounts.push({ currency, amount: profit });
}

/** The open profit of each agent with positions open, in the order of the read (of the dashboard). */
export function openPLByAgent(read: OpenPositionsResponse): AgentOpenPL[] {
  const agents: AgentOpenPL[] = [];
  for (const position of read.positions) {
    let agent = agents.find((a) => a.agent === position.agent);
    if (!agent) {
      agent = { agent: position.agent, positions: 0, running: false, amounts: [], assets: [] };
      agents.push(agent);
    }
    agent.positions += 1;
    agent.running ||= position.running;
    addAmount(agent.amounts, position.currency, position.profit);
    let asset = agent.assets.find((a) => a.pair === position.pair);
    if (!asset) {
      asset = { pair: position.pair, positions: 0, amounts: [] };
      agent.assets.push(asset);
    }
    asset.positions += 1;
    addAmount(asset.amounts, position.currency, position.profit);
  }
  return agents;
}

/**
 * Amounts as a line shows them, in the currency of their account, their codes
 * told when the band shows several, eg: +81,63, or +81,63 BRL · +3,20 USD.
 */
export function amountsText(amounts: CurrencyAmount[], several: boolean): string {
  return amounts
    .map((a) => (several ? `${formatProfit(a.amount)} ${a.currency}` : formatProfit(a.amount)))
    .join(" · ");
}

const gapReasons: Record<string, string> = {
  unreachable: "a bridge da conta não respondeu",
  refused: "a bridge recusou o token",
  failed: "o terminal da conta respondeu com erro",
};

/** Why a read of the band is incomplete, in the words of the dashboard: the agents whose account wasn't read. */
export function openPLGaps(read: Pick<OpenPositionsResponse, "missing">): string[] {
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

/** The count of the accounts of the balances, eg: 2 contas. */
export function accountsSummary(read: OpenPositionsResponse): string {
  if (read.accounts === 0) return "Nenhuma conta lida";
  return read.accounts === 1 ? "1 conta" : `${read.accounts} contas`;
}

/** An agent on the band: its profit open now and closed in the period, by the currency of its account. */
export interface AgentLine {
  agent: string;
  /** Whether it runs; else stopped or disabled. */
  running: boolean;
  /** Its positions open, and their profit, in all and by asset; none without one. */
  positions: number;
  open: CurrencyAmount[];
  assets: AssetOpenPL[];
  /** The positions it closed in the period, and their profit; none without one. */
  closedPositions: number;
  closed: CurrencyAmount[];
}

/**
 * The agents with positions open or closed in the period: the ones with
 * positions open first, in the order of the read, then the ones that only
 * closed some, in the order of theirs.
 */
export function agentLines(
  open: OpenPositionsResponse | null,
  closed: ClosedProfitResponse | null,
): AgentLine[] {
  const lines: AgentLine[] = (open ? openPLByAgent(open) : []).map((agent) => ({
    agent: agent.agent,
    running: agent.running,
    positions: agent.positions,
    open: agent.amounts,
    assets: agent.assets,
    closedPositions: 0,
    closed: [],
  }));
  for (const agent of closed?.agents ?? []) {
    let line = lines.find((l) => l.agent === agent.agent);
    if (!line) {
      line = {
        agent: agent.agent,
        running: agent.running,
        positions: 0,
        open: [],
        assets: [],
        closedPositions: 0,
        closed: [],
      };
      lines.push(line);
    }
    line.running ||= agent.running;
    line.closedPositions += agent.positions;
    line.closed.push({ currency: agent.currency, amount: agent.amount });
  }
  return lines;
}

/** Whether the band shows several currencies, each amount then with its code. */
export function severalCurrencies(
  open: OpenPositionsResponse | null,
  closed: ClosedProfitResponse | null,
): boolean {
  const currencies = new Set(
    [...(open?.totals ?? []), ...(open?.balances ?? []), ...(closed?.totals ?? [])].map(
      (amount) => amount.currency,
    ),
  );
  return currencies.size > 1;
}

/** The sign of amounts by their cents, as their values tell it: positive, negative, zero, or mixed in several currencies. */
export type ProfitTone = "pos" | "neg" | "zero" | "mixed";

export function profitTone(amounts: CurrencyAmount[]): ProfitTone {
  const cents = amounts.map((a) => Math.round(a.amount * 100));
  const up = cents.some((c) => c > 0);
  const down = cents.some((c) => c < 0);
  if (up && down) return "mixed";
  if (up) return "pos";
  if (down) return "neg";
  return "zero";
}

/**
 * The open profit of an agent on the band: its sign; flat without a position
 * open; unknown when its account wasn't read, or the positions weren't, never
 * taken as 0.
 */
export type OpenState = ProfitTone | "flat" | "unknown";

export function agentOpenState(line: AgentLine, open: OpenPositionsResponse | null): OpenState {
  if (!open || (open.missing ?? []).some((gap) => gap.agents.includes(line.agent))) {
    return "unknown";
  }
  if (line.positions === 0) return "flat";
  return profitTone(line.open);
}
