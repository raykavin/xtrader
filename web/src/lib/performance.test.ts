import { describe, expect, it } from "vitest";
import type { PerformanceAccount, PerformanceTrade } from "../api/types";
import {
  bucketOf,
  capitalOf,
  currenciesOf,
  efficiencyOf,
  equityCurve,
  excursionsByTrade,
  filterTrades,
  formatDuration,
  groupBars,
  listTrades,
  periodBars,
  summarize,
  summarizeEfficiency,
  tradesCSV,
} from "./performance";

function trade(partial: Partial<PerformanceTrade>): PerformanceTrade {
  return {
    position: 1,
    account: "198974329",
    agent: "llm",
    symbol: "XAUUSDm",
    side: "BUY",
    volume: 0.01,
    closed_at: 0,
    entry: 4000,
    exit: 4010,
    gross: 0,
    costs: 0,
    net: 0,
    currency: "BRL",
    ...partial,
  };
}

// a hand-made day: a win, a loss and a win overlapping it, a zeroed one
// without its opening
const trades: PerformanceTrade[] = [
  trade({
    position: 1,
    opened_at: 1,
    closed_at: 3600,
    gross: 100,
    costs: -2,
    net: 98,
    volume: 0.02,
  }),
  trade({
    position: 2,
    opened_at: 7200,
    closed_at: 9000,
    gross: -40,
    costs: -1,
    net: -41,
    symbol: "BTCUSDm",
    agent: "orb",
  }),
  trade({ position: 3, opened_at: 8000, closed_at: 10800, gross: 60, costs: 0, net: 60 }),
  trade({ position: 4, closed_at: 12000, gross: 0, costs: -1, net: -1, volume: 0.05 }),
];
const day = { from: 0, to: 86400 };

describe("the summary of the performance", () => {
  it("sums the results, the averages, the streaks and the drawdown as by hand", () => {
    const s = summarize(trades, "BRL", 1000, day, 86400);
    expect(s.net).toBe(116);
    expect(s.gross).toBe(120);
    expect(s.grossProfit).toBe(160);
    expect(s.grossLoss).toBe(-40);
    expect(s.costs).toBe(-4);
    expect(s.profitFactor).toBe(4);
    expect([s.trades, s.wins, s.losses, s.zeroed]).toEqual([4, 2, 1, 1]);
    expect(s.winRate).toBe(0.5);
    expect(s.average).toBe(29);
    expect(s.averageWin).toBe(80);
    expect(s.averageLoss).toBe(-40);
    expect(s.payoff).toBe(2);
    expect(s.largestWin).toBe(100);
    expect(s.largestLoss).toBe(-40);
    expect([s.longestWinStreak, s.longestLossStreak]).toEqual([1, 1]);
    expect(s.averageDuration).toBeCloseTo((3599 + 1800 + 2800) / 3, 9);
    expect(s.averageWinDuration).toBe((3599 + 2800) / 2);
    expect(s.averageLossDuration).toBe(1800);
    expect(s.maxVolume).toBe(0.05);
    expect(s.returnOnCapital).toBeCloseTo(0.116, 9);
    expect(s.peak).toBe(117);
    expect(s.drawdown).toBe(41);
    expect(s.drawdownPct).toBeCloseTo(41 / 1098, 9);
    // [1, 3600] and [7200, 10800] open: 3599 + 3600 of the day
    expect(s.timeInMarket).toBeCloseTo(7199 / 86400, 9);
  });

  it("tells the unknown apart from zero", () => {
    const s = summarize([], "BRL", null, day, 86400);
    expect(s.trades).toBe(0);
    expect(s.profitFactor).toBe(0);
    expect(s.payoff).toBeNull();
    expect(s.returnOnCapital).toBeNull();
    expect(s.averageDuration).toBeNull();
    expect(s.timeInMarket).toBeNull();
    expect(s.drawdownPct).toBeNull();
    const wins = summarize([trades[0] as PerformanceTrade], "BRL", 0, day, 86400);
    expect(wins.profitFactor).toBe(Number.POSITIVE_INFINITY);
    expect(wins.returnOnCapital, "no capital above 0").toBeNull();
  });

  it("counts the streaks in the order of the closes", () => {
    const streak = [1, 2, 3, -1, -2, 4].map((g, i) =>
      trade({ position: i, closed_at: 100 - i, gross: g, net: g }),
    );
    // by their close: 4, -2, -1, 3, 2, 1
    const s = summarize(streak, "BRL", null, day, 86400);
    expect(s.longestWinStreak).toBe(3);
    expect(s.longestLossStreak).toBe(2);
  });
});

describe("the filters, the currencies and the capital", () => {
  const accounts: PerformanceAccount[] = [
    { account: "1", currency: "BRL", agents: ["llm", "orb"], balance: 1500, start_balance: 1000 },
    { account: "2", currency: "USD", agents: ["usd"], balance: 300, start_balance: 250 },
    { account: "3", currency: "BRL", agents: ["fib"], balance: 600, start_balance: 500 },
  ];

  it("keeps the trades of an agent and of an asset", () => {
    expect(filterTrades(trades, {}).length).toBe(4);
    expect(filterTrades(trades, { agent: "orb" }).map((t) => t.position)).toEqual([2]);
    expect(filterTrades(trades, { symbol: "XAUUSDm" }).map((t) => t.position)).toEqual([1, 3, 4]);
  });

  it("sums apart the currencies, the capital of the accounts of each", () => {
    expect(currenciesOf(trades, accounts)).toEqual(["BRL", "USD"]);
    expect(capitalOf(accounts, "BRL")).toBe(1500);
    expect(capitalOf(accounts, "BRL", "orb")).toBe(1000);
    expect(capitalOf(accounts, "USD", "orb")).toBeNull();
  });
});

describe("the series of the charts", () => {
  it("sums the net results trade after trade from 0", () => {
    expect(equityCurve(trades, 0)).toEqual([
      { time: 0, value: 0 },
      { time: 3600, value: 98 },
      { time: 9000, value: 57 },
      { time: 10800, value: 117 },
      { time: 12000, value: 116 },
    ]);
    expect(equityCurve([], 0)).toEqual([]);
  });

  it("buckets by day, week or month after the length of the period", () => {
    expect(bucketOf(86400)).toBe("day");
    expect(bucketOf(90 * 86400)).toBe("week");
    expect(bucketOf(365 * 86400)).toBe("month");
    const noon = (y: number, m: number, d: number) => new Date(y, m - 1, d, 12).getTime() / 1000;
    const spread = [
      trade({ position: 1, closed_at: noon(2026, 10, 26), gross: 10, net: 10 }),
      trade({ position: 2, closed_at: noon(2026, 10, 28), gross: -4, net: -4 }),
      trade({ position: 3, closed_at: noon(2026, 11, 2), gross: 5, net: 5 }),
    ];
    expect(periodBars(spread, "day").map((b) => [b.label, b.net])).toEqual([
      ["26/10", 10],
      ["28/10", -4],
      ["02/11", 5],
    ]);
    expect(periodBars(spread, "week").map((b) => [b.label, b.net, b.trades])).toEqual([
      ["sem. 26/10", 6, 2],
      ["sem. 02/11", 5, 1],
    ]);
    expect(periodBars(spread, "month").map((b) => [b.label, b.net])).toEqual([
      ["10/2026", 6],
      ["11/2026", 5],
    ]);
  });

  it("groups by asset and by agent, the best first", () => {
    expect(groupBars(trades, "symbol").map((b) => [b.key, b.net, b.trades, b.wins])).toEqual([
      ["XAUUSDm", 157, 3, 2],
      ["BTCUSDm", -41, 1, 0],
    ]);
    expect(groupBars(trades, "agent").map((b) => b.key)).toEqual(["llm", "orb"]);
  });
});

describe("the efficiency of the trades", () => {
  it("measures the entry, the exit and the move over the range of the price", () => {
    // a long from 4000 to 4010, the price from 3990 to 4030
    const long = trade({ side: "BUY", entry: 4000, exit: 4010 });
    const path = { account: "198974329", position: 1, high: 4030, low: 3990, mfe: 150, mae: 50 };
    expect(efficiencyOf(long, path)).toEqual({ entry: 0.75, exit: 0.5, total: 0.25 });
    // a short from 4100 to 4090, the ask from 4085 to 4105
    const short = trade({ side: "SELL", entry: 4100, exit: 4090 });
    const down = { account: "198974329", position: 2, high: 4105, low: 4085, mfe: 75, mae: 25 };
    expect(efficiencyOf(short, down)).toEqual({ entry: 0.75, exit: 0.75, total: 0.5 });
    // a loss: a negative total
    expect(efficiencyOf(trade({ entry: 4000, exit: 3995 }), path).total).toBe(-0.125);
    expect(efficiencyOf(long, undefined)).toEqual({ entry: null, exit: null, total: null });
    expect(efficiencyOf(long, { ...path, high: 4000, low: 4000 }).total, "no range").toBeNull();
  });

  it("averages them, with what the wins gave back and the worst of the wins", () => {
    const win = trade({ position: 1, side: "BUY", entry: 4000, exit: 4010, gross: 50, net: 50 });
    const loss = trade({ position: 2, side: "BUY", entry: 4000, exit: 3990, gross: -50, net: -50 });
    const unread = trade({ position: 3, gross: 10, net: 10 });
    const paths = excursionsByTrade([
      { account: "198974329", position: 1, high: 4030, low: 3990, mfe: 150, mae: 50 },
      { account: "198974329", position: 2, high: 4010, low: 3980, mfe: 50, mae: 100 },
      { account: "198974329", position: 3, mfe: 0, mae: 0, error: "abertura desconhecida" },
    ]);
    const e = summarizeEfficiency([win, loss, unread], paths);
    expect(e.trades).toBe(2);
    expect(e.missing).toBe(1);
    expect(e.averageMFE).toBe(100);
    expect(e.averageMAE).toBe(75);
    expect(e.leftOnTable).toBe(100);
    expect(e.largestWinMAE).toBe(50);
    // entries 0.75 and 1/3, exits 0.5 and 1/3, totals 0.25 and -1/3
    expect(e.entry).toBeCloseTo((0.75 + 1 / 3) / 2, 9);
    expect(e.exit).toBeCloseTo((0.5 + 1 / 3) / 2, 9);
    expect(e.total).toBeCloseTo((0.25 - 1 / 3) / 2, 9);
    expect(summarizeEfficiency([unread], paths).averageMFE).toBeNull();
  });
});

describe("the list and its CSV", () => {
  it("lists the latest first with the running sum", () => {
    expect(listTrades(trades).map((t) => [t.position, t.cumulative])).toEqual([
      [4, 116],
      [3, 117],
      [2, 57],
      [1, 98],
    ]);
  });

  it("writes the CSV oldest first, separated by semicolons", () => {
    const lines = tradesCSV(trades.slice(0, 2)).trim().split("\n");
    expect(lines).toHaveLength(3);
    expect(lines[0]).toContain("ativo;agente;conta;posicao;lado;lotes");
    expect(lines[1]).toBe(
      "XAUUSDm;llm;198974329;1;BUY;0.02;1970-01-01 00:00:01;1970-01-01 01:00:00;3599;4000;4010;100;-2;98;98;BRL;;;;;",
    );
    expect(lines[2]?.endsWith(";-41;57;BRL;;;;;")).toBe(true);
    // with its excursion: MFE, MAE and the efficiencies
    const x = { account: "198974329", position: 1, high: 4020, low: 3990, mfe: 200, mae: 100 };
    const withPath = tradesCSV(trades.slice(0, 1), excursionsByTrade([x]))
      .trim()
      .split("\n");
    expect(withPath[1]?.endsWith(";BRL;200;100;0.6667;0.6667;0.3333")).toBe(true);
  });

  it("writes the durations as the Profit does", () => {
    expect(formatDuration(9554)).toBe("2h39min14s");
    expect(formatDuration(47)).toBe("47s");
    expect(formatDuration(87)).toBe("1min27s");
    expect(formatDuration(3 * 86400 + 4 * 3600)).toBe("3d4h");
    expect(formatDuration(null)).toBe("—");
  });
});
