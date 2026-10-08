import { describe, expect, it } from "vitest";
import type { AgentPeriod, Order, Snapshot } from "../api/types";
import { type PeriodResult, parseAmount, periodResults } from "./periods";

function order(partial: Partial<Order>): Order {
  return {
    id: 1,
    exchange_id: 1,
    pair: "BTCUSDm",
    side: "SELL",
    type: "MARKET",
    status: "FILLED",
    price: 100,
    quantity: 1,
    fee: 0,
    created_at: 0,
    updated_at: 0,
    candle_time: 0,
    ref_price: 0,
    profit: 0,
    profit_value: 0,
    ...partial,
  };
}

/** A close at the time at, booked by the account in BRL. */
function close(pair: string, at: number, profit: number): Order {
  return order({
    pair,
    created_at: at,
    updated_at: at,
    profit: profit / 1000,
    profit_value: profit / 5,
    account_profit: profit,
    account_currency: "BRL",
  });
}

function snapshot(pair: string, orders: Order[]): Snapshot {
  return {
    levels: null,
    pair,
    asset: pair.slice(0, 3),
    quote: "USD",
    candles: [],
    indicators: [],
    orders,
    equity_values: [],
    asset_values: [],
    account_currency: "BRL",
  };
}

/** The results of periods on snapshots, count of them. */
function results(list: AgentPeriod[], on: Snapshot[], count: number): PeriodResult[] {
  const all = periodResults(list, on);
  expect(all).toHaveLength(count);
  return all;
}

const snapshots: Snapshot[] = [
  snapshot("BTCUSDm", [
    close("BTCUSDm", 100, -50),
    close("BTCUSDm", 250, 80),
    close("BTCUSDm", 400, 20),
  ]),
  snapshot("XAUUSDm", [close("XAUUSDm", 150, 30), close("XAUUSDm", 420, -10)]),
];

// two resets: at 200, with 1000 BRL, and at 300, with 500 BRL
const periods: AgentPeriod[] = [
  { id: 2, started_at: 300, deposit: 500, currency: "BRL", created_by: "admin" },
  { id: 1, started_at: 200, ended_at: 300, deposit: 1000, currency: "BRL" },
];

describe("periodResults", () => {
  it("counts the trades of each period, the current one first, the ones before the first reset last", () => {
    const [current, previous, before] = results(periods, snapshots, 3) as [
      PeriodResult,
      PeriodResult,
      PeriodResult,
    ];

    expect(current.period?.id).toBe(2);
    expect(current.pairs.map((p) => [p.pair, p.stats.trades, p.stats.totalProfit])).toEqual([
      ["BTCUSDm", 1, 20],
      ["XAUUSDm", 1, -10],
    ]);
    expect(current.trades).toBe(2);
    expect(current.wins).toBe(1);
    expect(current.losses).toBe(1);
    expect(current.profit).toBe(10);
    expect(current.currency).toBe("BRL");
    expect(current.profitFactor).toBe(2);
    expect(current.balance).toBe(510);

    expect(previous.period?.id).toBe(1);
    expect(previous.until).toBe(300);
    expect(previous.pairs.map((p) => p.pair)).toEqual(["BTCUSDm"]);
    expect(previous.profit).toBe(80);
    expect(previous.balance).toBe(1080);

    expect(before.period).toBeNull();
    expect(before.until).toBe(200);
    expect(before.trades).toBe(2);
    expect(before.profit).toBe(-20);
    expect(before.balance).toBeNull(); // no deposit
  });

  it("counts them all before any reset", () => {
    const [all] = results([], snapshots, 1) as [PeriodResult];
    expect(all.period).toBeNull();
    expect(all.trades).toBe(5);
    expect(all.profit).toBe(70);
  });

  it("starts the balance at the deposit, nothing closed yet", () => {
    const fresh: AgentPeriod[] = [{ id: 3, started_at: 1000, deposit: 750, currency: "BRL" }];
    const [current, before] = results(fresh, snapshots, 2) as [PeriodResult, PeriodResult];
    expect(current.trades).toBe(0);
    expect(current.pairs).toEqual([]);
    expect(current.profit).toBe(0);
    expect(current.currency).toBe("BRL");
    expect(current.balance).toBe(750);
    expect(before.trades).toBe(5);
  });

  it("leaves the sums out when the pairs count in different currencies", () => {
    const mixed: Snapshot[] = [
      snapshot("BTCUSDm", [close("BTCUSDm", 400, 20)]),
      snapshot("ETHUSDT", [
        order({ pair: "ETHUSDT", created_at: 350, profit: 0.01, profit_value: 4 }),
      ]),
    ];
    const [current] = results(periods, mixed, 2) as [PeriodResult];
    expect(current.pairs.map((p) => p.stats.profitCurrency)).toEqual(["BRL", "USD"]);
    expect(current.trades).toBe(2);
    expect(current.profit).toBeNull();
    expect(current.balance).toBeNull();
  });

  it("leaves the balance out with the profit in another currency than the deposit", () => {
    const usd: AgentPeriod[] = [{ id: 4, started_at: 300, deposit: 100, currency: "USD" }];
    const [current] = results(usd, snapshots, 2) as [PeriodResult];
    expect(current.profit).toBe(10);
    expect(current.currency).toBe("BRL");
    expect(current.balance).toBeNull();
  });
});

describe("parseAmount", () => {
  it("reads the amounts as typed in Brazil, or with a dot", () => {
    expect(parseAmount("1.234,56")).toBe(1234.56);
    expect(parseAmount("1234,5")).toBe(1234.5);
    expect(parseAmount(" 1000 ")).toBe(1000);
    expect(parseAmount("250.75")).toBe(250.75);
    expect(parseAmount("1.000")).toBe(1000);
    expect(parseAmount("10.000,00")).toBe(10000);
    expect(parseAmount("0")).toBe(0);
  });

  it("refuses what isn't an amount from 0", () => {
    for (const text of ["", " ", "-10", "abc", "1,2,3", "10 BRL", "1e5"]) {
      expect(parseAmount(text)).toBeNull();
    }
  });
});
