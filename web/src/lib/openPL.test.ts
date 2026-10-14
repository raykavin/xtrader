import { describe, expect, it } from "vitest";
import type { OpenPosition, OpenPositionsResponse } from "../api/types";
import { agentOpenPLText, openPLByAgent, openPLGaps, openPLSummary } from "./openPL";

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
  complete: true,
  positions: [],
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

  it("keeps each account in its currency, none converted, its code told with several", () => {
    const single = read({ totals: [{ currency: "USD", amount: -6.5 }] });
    const [dollar] = openPLByAgent({
      ...single,
      positions: [position("dollar", 1, -6.5, { currency: "USD" })],
    });
    expect(dollar && agentOpenPLText(dollar, single)).toBe("−6,50");

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
    expect(mixed && agentOpenPLText(mixed, several)).toBe("+1,00 BRL · +3,20 KWD");
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
});
