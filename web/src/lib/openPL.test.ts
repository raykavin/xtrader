import { describe, expect, it } from "vitest";
import type { ClosedProfitResponse, OpenPosition, OpenPositionsResponse } from "../api/types";
import {
  accountsSummary,
  agentLines,
  agentOpenState,
  amountsText,
  assetLabel,
  directionOf,
  hasExposure,
  openPLByAgent,
  openPLGaps,
  openPLSummary,
  profitTone,
  severalCurrencies,
} from "./openPL";

const position = (
  agent: string,
  ticket: number,
  profit: number,
  fields: Partial<OpenPosition> = {},
): OpenPosition => ({
  agent,
  pair: "XAUUSDm",
  side: "BUY",
  volume: 0.03,
  entry: 4160,
  price: 4161,
  profit,
  currency: "BRL",
  ticket,
  running: true,
  ...fields,
});

const read = (fields: Partial<OpenPositionsResponse> = {}): OpenPositionsResponse => ({
  totals: [{ currency: "BRL", amount: 0 }],
  balances: [{ currency: "BRL", amount: 10_000 }],
  accounts: 1,
  complete: true,
  positions: [],
  pending_orders: 0,
  at: 1_760_000_000,
  ...fields,
});

describe("the open profit of the agents", () => {
  it("sums the positions of each agent, in the order of the read", () => {
    const agents = openPLByAgent(
      read({
        positions: [
          position("llm-trend", 1, 80.43),
          position("llm-trend", 4, -10),
          position("matrend", 2, -6.5),
          position("fibonacci", 3, 24.6, { running: false }),
        ],
      }),
    );
    expect(agents.map((a) => [a.agent, a.positions, a.running])).toEqual([
      ["llm-trend", 2, true],
      ["matrend", 1, true],
      ["fibonacci", 1, false],
    ]);
    expect(agents[0]?.amounts[0]?.amount).toBeCloseTo(70.43, 9);
    expect(agents[1]?.amounts).toEqual([{ currency: "BRL", amount: -6.5 }]);
  });

  it("sums the positions of each agent by asset, in the order of the read", () => {
    const [agent] = openPLByAgent(
      read({
        positions: [
          position("llm-smc", 1, 22.6),
          position("llm-smc", 2, -4.1, { pair: "BTCUSDm", side: "SELL" }),
          position("llm-smc", 3, -1),
        ],
      }),
    );
    expect(agent?.assets.map((a) => [a.pair, a.positions])).toEqual([
      ["XAUUSDm", 2],
      ["BTCUSDm", 1],
    ]);
    expect(agent?.assets[0]?.amounts[0]?.amount).toBeCloseTo(21.6, 9);
    expect(agent?.assets[1]?.amounts).toEqual([{ currency: "BRL", amount: -4.1 }]);
    expect(agent?.assets[1] && amountsText(agent.assets[1].amounts, false)).toBe("−4,10");
  });

  it("tells the buys and the sells of each asset apart, never a direction for the agent", () => {
    const [agent, other] = openPLByAgent(
      read({
        positions: [
          position("llm-smc", 1, 10),
          position("llm-smc", 2, -3, { side: "SELL" }),
          position("llm-smc", 3, 4, { pair: "BTCUSDm", side: "SELL" }),
          position("llm-smc", 4, 2),
          position("matrend", 5, 1, { pair: "BTCUSDm" }),
        ],
      }),
    );
    expect(agent?.assets.map((a) => [a.pair, a.side, a.positions])).toEqual([
      ["XAUUSDm", "BUY", 2],
      ["XAUUSDm", "SELL", 1],
      ["BTCUSDm", "SELL", 1],
    ]);
    expect(agent?.assets.map((a) => a.amounts[0]?.amount)).toEqual([12, -3, 4]);
    expect(agent?.assets.map((a) => directionOf(a.side).icon)).toEqual(["up", "down", "down"]);
    expect(agent?.assets.map(assetLabel)).toEqual([
      "Compra em XAUUSDm, 2 posições abertas",
      "Venda em XAUUSDm, 1 posição aberta",
      "Venda em BTCUSDm, 1 posição aberta",
    ]);
    expect(other?.assets.map((a) => [a.pair, a.side])).toEqual([["BTCUSDm", "BUY"]]);
    expect(directionOf("sell")).toEqual({ icon: "down", text: "Venda" });
    expect(directionOf("BUY")).toEqual({ icon: "up", text: "Compra" });
  });

  it("tells whether there is something to zero: positions, pending orders, or a read unknown", () => {
    expect(hasExposure(null)).toBe(true);
    expect(hasExposure(read())).toBe(false);
    expect(hasExposure(read({ positions: [position("matrend", 1, 2)] }))).toBe(true);
    expect(hasExposure(read({ pending_orders: 2 }))).toBe(true);
    expect(
      hasExposure(read({ complete: false, missing: [{ agents: ["orb"], reason: "unreachable" }] })),
    ).toBe(true);
  });

  it("keeps each account in its currency, none converted, its code told with several", () => {
    const single = read({ totals: [{ currency: "USD", amount: -6.5 }] });
    const [dollar] = openPLByAgent({
      ...single,
      positions: [position("dollar", 1, -6.5, { currency: "USD" })],
    });
    expect(dollar && amountsText(dollar.amounts, false)).toBe("−6,50");

    const several = read({
      totals: [
        { currency: "BRL", amount: 1 },
        { currency: "KWD", amount: 3.2 },
      ],
      positions: [position("mixed", 1, 1), position("mixed", 2, 3.2, { currency: "KWD" })],
    });
    const [mixed] = openPLByAgent(several);
    expect(mixed?.amounts).toEqual([
      { currency: "BRL", amount: 1 },
      { currency: "KWD", amount: 3.2 },
    ]);
    expect(mixed && amountsText(mixed.amounts, true)).toBe("+1,00 BRL · +3,20 KWD");
  });

  it("tells why it is incomplete: the accounts not read", () => {
    expect(openPLGaps(read())).toEqual([]);
    expect(
      openPLGaps(
        read({
          complete: false,
          missing: [
            { agents: ["matrend", "fibonacci"], reason: "unreachable" },
            { agents: ["wrong"], reason: "refused" },
            { agents: ["llm"], reason: "failed" },
          ],
        }),
      ),
    ).toEqual([
      "Sem dados de matrend, fibonacci: a bridge da conta não respondeu",
      "Sem dados de wrong: a bridge recusou o token",
      "Sem dados de llm: o terminal da conta respondeu com erro",
    ]);
  });

  it("counts the positions and their agents", () => {
    expect(openPLSummary(read())).toBe("Sem posições abertas");
    expect(openPLSummary(read({ positions: [position("llm", 1, 1)] }))).toBe(
      "1 posição aberta em 1 agente",
    );
    expect(
      openPLSummary(
        read({
          positions: [position("llm", 1, 1), position("llm", 2, 1), position("matrend", 3, 1)],
        }),
      ),
    ).toBe("3 posições abertas em 2 agentes");
  });

  it("counts the accounts of the balances", () => {
    expect(accountsSummary(read())).toBe("1 conta");
    expect(accountsSummary(read({ accounts: 2 }))).toBe("2 contas");
    expect(accountsSummary(read({ accounts: 0, balances: [] }))).toBe("Nenhuma conta lida");
  });
});

const closedRead = (fields: Partial<ClosedProfitResponse> = {}): ClosedProfitResponse => ({
  from: 0,
  to: 86_400,
  totals: [{ currency: "BRL", amount: 0 }],
  agents: [],
  series: [{ currency: "BRL", points: [] }],
  complete: true,
  at: 1_760_000_000,
  ...fields,
});

describe("the agents on the band", () => {
  it("joins the profit open and closed of each agent, the ones with positions open first", () => {
    const lines = agentLines(
      read({ positions: [position("llm-trend", 1, 12.3), position("matrend", 2, -1)] }),
      closedRead({
        agents: [
          { agent: "llm-trend", amount: -8.42, currency: "BRL", positions: 2, running: true },
          { agent: "fibonacci", amount: -50.05, currency: "BRL", positions: 1, running: false },
        ],
      }),
    );
    expect(lines).toEqual([
      {
        agent: "llm-trend",
        running: true,
        positions: 1,
        open: [{ currency: "BRL", amount: 12.3 }],
        assets: [
          {
            pair: "XAUUSDm",
            side: "BUY",
            positions: 1,
            amounts: [{ currency: "BRL", amount: 12.3 }],
          },
        ],
        closedPositions: 2,
        closed: [{ currency: "BRL", amount: -8.42 }],
      },
      {
        agent: "matrend",
        running: true,
        positions: 1,
        open: [{ currency: "BRL", amount: -1 }],
        assets: [
          {
            pair: "XAUUSDm",
            side: "BUY",
            positions: 1,
            amounts: [{ currency: "BRL", amount: -1 }],
          },
        ],
        closedPositions: 0,
        closed: [],
      },
      {
        agent: "fibonacci",
        running: false,
        positions: 0,
        open: [],
        assets: [],
        closedPositions: 1,
        closed: [{ currency: "BRL", amount: -50.05 }],
      },
    ]);
    expect(agentLines(null, null)).toEqual([]);
    expect(agentLines(read(), null)).toEqual([]);
  });

  it("tells the codes of the currencies when the band shows several", () => {
    expect(severalCurrencies(read(), closedRead())).toBe(false);
    expect(
      severalCurrencies(read(), closedRead({ totals: [{ currency: "USD", amount: 1 }] })),
    ).toBe(true);
    expect(severalCurrencies(read({ balances: [{ currency: "KWD", amount: 1 }] }), null)).toBe(
      true,
    );
    expect(severalCurrencies(null, null)).toBe(false);
  });
});

describe("the tone of an agent on the band", () => {
  it("takes the sign of the profit by its cents", () => {
    const amounts = (...values: number[]) => values.map((amount) => ({ currency: "BRL", amount }));
    expect(profitTone(amounts(12.3))).toBe("pos");
    expect(profitTone(amounts(-0.006))).toBe("neg");
    expect(profitTone(amounts(0.004))).toBe("zero");
    expect(profitTone(amounts(-0.004))).toBe("zero");
    expect(profitTone([])).toBe("zero");
    expect(profitTone(amounts(5, 0))).toBe("pos");
    expect(profitTone(amounts(5, -1))).toBe("mixed");
  });

  it("tells an agent flat, or its account not read, never as zero", () => {
    const open = read({
      positions: [position("up", 1, 12.3), position("down", 2, -4), position("even", 3, 0)],
      missing: [{ agents: ["unread"], reason: "unreachable" }],
    });
    const closed = closedRead({
      agents: [
        { agent: "flat", amount: 5, currency: "BRL", positions: 1, running: true },
        { agent: "unread", amount: -2, currency: "BRL", positions: 1, running: true },
      ],
    });
    const states = Object.fromEntries(
      agentLines(open, closed).map((line) => [line.agent, agentOpenState(line, open)]),
    );
    expect(states).toEqual({
      up: "pos",
      down: "neg",
      even: "zero",
      flat: "flat",
      unread: "unknown",
    });
    // the positions not read at all: every agent unknown
    const [line] = agentLines(null, closed);
    expect(line && agentOpenState(line, null)).toBe("unknown");
  });
});
