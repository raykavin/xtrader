import { describe, expect, it } from "vitest";
import type { BacktestBatchInfo, BacktestRunInfo, BacktestSummary } from "../api/types";
import {
  type BacktestForm,
  callFor,
  candlesIn,
  comparisonRows,
  conditionDifferences,
  defaultForm,
  degradedValidation,
  estimatedCalls,
  filterRows,
  formCalls,
  formPeriod,
  formRequest,
  hoursAssumption,
  progressOf,
  reasonLabels,
  robustnessFlags,
  runsOf,
  sortRows,
  withHours,
} from "./backtest";

function summary(partial: Partial<BacktestSummary>): BacktestSummary {
  return {
    currency: "USD",
    initial_balance: 10000,
    final_equity: 10000,
    net: 0,
    return: 0,
    open_profit: 0,
    open_positions: 0,
    trades: 50,
    wins: 25,
    losses: 25,
    win_rate: 0.5,
    average_win: 10,
    average_loss: -10,
    fees: 0,
    max_drawdown: 100,
    max_drawdown_pct: 0.01,
    months: 3,
    positive_months: 2,
    warnings: 0,
    gaps: 0,
    refused: 0,
    ...partial,
  };
}

function run(partial: Partial<BacktestRunInfo>): BacktestRunInfo {
  return {
    id: 1,
    agent: "trend",
    kind: "trend",
    segment: "fit",
    from: 0,
    to: 1,
    status: "succeeded",
    processed: 0,
    total: 0,
    pairs: ["XAUUSDm"],
    ...partial,
  };
}

describe("the robustness of a run", () => {
  it("tells a small sample, a deep drawdown, a degraded validation, an LLM and gaps", () => {
    expect(robustnessFlags(summary({}), false)).toEqual([]);
    const flags = robustnessFlags(
      summary({ trades: 12, max_drawdown_pct: 0.3, gaps: 2 }),
      true,
      true,
    );
    expect(flags.map((f) => f.text)).toEqual([
      "Amostra pequena",
      "Drawdown alto",
      "Degradou na validação",
      "LLM: não determinístico",
      "Dados com lacunas",
    ]);
  });

  it("finds a validation much worse than the fit", () => {
    expect(degradedValidation(summary({ net: 100 }), summary({ net: -1 }))).toBe(true);
    expect(
      degradedValidation(
        summary({ net: 100, return_over_drawdown: 2 }),
        summary({ net: 10, return_over_drawdown: 0.5 }),
      ),
    ).toBe(true);
    expect(
      degradedValidation(
        summary({ net: 100, return_over_drawdown: 2 }),
        summary({ net: 60, return_over_drawdown: 1.5 }),
      ),
    ).toBe(false);
  });
});

describe("the comparison", () => {
  const batch: BacktestBatchInfo = {
    id: 1,
    name: "lote",
    from: 0,
    to: 1,
    validation_share: 0.3,
    assumptions: {
      balance: 10000,
      currency: "USD",
      fee: 0,
      spread_points: 0,
      slippage_points: 0,
      execution_timeframe: "1m",
      gap_fills: true,
      conservative: true,
      end_policy: "mark_to_market",
      llm_max_calls: 300,
      llm_max_cost: 5,
    },
    created_by: "admin",
    created_at: 0,
    runs: [
      run({ id: 1, agent: "a", summary: summary({ net: 500, return_over_drawdown: 5 }) }),
      run({
        id: 2,
        agent: "a",
        segment: "validation",
        summary: summary({ net: -10, return_over_drawdown: -0.1 }),
      }),
      run({
        id: 3,
        agent: "b",
        summary: summary({ net: 100, return_over_drawdown: 1, trades: 10 }),
      }),
      run({ id: 4, agent: "c", kind: "llm", llm: true, pairs: ["BTCUSDm"], status: "running" }),
    ],
  };

  it("pairs the fit and the validation of each agent, the degradation told", () => {
    const rows = comparisonRows(batch);
    expect(rows.map((r) => r.agent)).toEqual(["a", "b", "c"]);
    expect(rows[0]?.validation?.id).toBe(2);
    expect(rows[0]?.degraded).toBe(true);
    expect(rows[1]?.degraded).toBe(false);
  });

  it("sorts by any column, the unknown last, and filters the sample and the LLMs", () => {
    const rows = comparisonRows(batch);
    expect(sortRows(rows, "net").map((r) => r.agent)).toEqual(["a", "b", "c"]);
    expect(sortRows(rows, "net", true).map((r) => r.agent)).toEqual(["b", "a", "c"]);
    expect(sortRows(rows, "agent").map((r) => r.agent)).toEqual(["a", "b", "c"]);
    expect(filterRows(rows, { minTrades: 30, showLLM: true }).map((r) => r.agent)).toEqual([
      "a",
      "c",
    ]);
    expect(filterRows(rows, { minTrades: 0, showLLM: false }).map((r) => r.agent)).toEqual([
      "a",
      "b",
    ]);
  });

  it("tells what differs between the rows", () => {
    expect(conditionDifferences(comparisonRows(batch))).toEqual([
      "ativos diferentes entre os agentes",
      "agentes LLM (não determinísticos) junto de determinísticos",
    ]);
    const coarser = {
      ...batch,
      runs: batch.runs.map((r) =>
        r.id === 3 && r.summary
          ? { ...r, summary: { ...r.summary, execution_timeframe: "5m" } }
          : r.summary
            ? { ...r, summary: { ...r.summary, execution_timeframe: "1m" } }
            : r,
      ),
    };
    expect(conditionDifferences(comparisonRows(coarser))).toContain(
      "execução em resoluções diferentes (1m, 5m)",
    );
  });
});

describe("the progress and the estimates", () => {
  it("tells the progress, unknown before the first", () => {
    expect(progressOf(run({ status: "running", processed: 25, total: 100 }))).toBe(0.25);
    expect(progressOf(run({ status: "queued" }))).toBeNull();
  });

  it("estimates the calls of an LLM by its candles, at most its ceiling", () => {
    expect(candlesIn(86400, "15m")).toBe(96);
    expect(candlesIn(86400, "1h")).toBe(24);
    const agent = {
      agent: "llm",
      kind: "llm",
      mode: "paper",
      enabled: true,
      pairs: ["XAUUSDm", "BTCUSDm"],
      timeframe: "15m",
      llm: true,
      compatible: true,
    };
    expect(estimatedCalls(agent, 86400, undefined, 1000)).toBe(192);
    expect(estimatedCalls(agent, 86400, undefined, 100)).toBe(100);
  });
});

describe("the form of a batch", () => {
  const now = new Date(2026, 9, 29, 15, 30, 20);
  const limits = { max_period_days: 366, max_runs: 40 };
  const form = (partial: Partial<BacktestForm>): BacktestForm => ({
    ...defaultForm(now),
    agents: ["a", "b"],
    ...partial,
  });

  it("starts on the last 90 days, until today, 30% of them for the validation", () => {
    const f = defaultForm(now);
    expect(f.from).toBe("2026-08-01");
    expect(f.to).toBe("2026-10-29");
    expect(f.validation).toBe("30");
  });

  it("asks until the end of the last day, now at most", () => {
    const period = formPeriod(form({ from: "2026-10-01", to: "2026-10-02" }), now);
    expect(period).toEqual({
      from: new Date(2026, 9, 1).getTime() / 1000,
      to: new Date(2026, 9, 3).getTime() / 1000,
    });
    expect(formPeriod(form({ to: "2026-10-29" }), now)?.to).toBe(
      new Date(2026, 9, 29, 15, 30).getTime() / 1000,
    );
  });

  it("builds the request: the percents as fractions, the blanks as none", () => {
    const { request, errors } = formRequest(
      form({ fee: "0,05", spread: "20", slippage: "3", dailyLoss: "200", llmMaxCost: "2.5" }),
      limits,
      now,
    );
    expect(errors).toEqual({});
    expect(request).toMatchObject({
      agents: ["a", "b"],
      validation_share: 0.3,
      balance: 10000,
      fee: 0.0005,
      spread_points: 20,
      slippage_points: 3,
      daily_loss: 200,
      llm_max_cost: 2.5,
    });
    expect(request?.pairs).toBeUndefined();
    expect(request?.daily_gain).toBeUndefined();
    expect(request?.llm_max_calls).toBeUndefined();
    expect(request?.timeframe).toBeUndefined();
  });

  it("refuses what the controller refuses", () => {
    const { request, errors } = formRequest(
      form({
        agents: [],
        validation: "60",
        balance: "0",
        fee: "abc",
        spread: "-1",
        llmMaxCalls: "1.5",
        timeframe: "2h",
      }),
      limits,
      now,
    );
    expect(request).toBeNull();
    expect(Object.keys(errors).sort()).toEqual(
      ["agents", "balance", "fee", "llmMaxCalls", "spread", "timeframe", "validation"].sort(),
    );
    expect(formRequest(form({ from: "2026-10-02", to: "2026-10-01" }), limits, now).errors.to).toBe(
      "A data final é anterior à inicial.",
    );
    expect(formRequest(form({ from: "2025-01-01" }), limits, now).errors.to).toBe(
      "Escolha até 366 dias.",
    );
    expect(formRequest(form({}), { ...limits, max_runs: 3 }, now).errors.agents).toBe(
      "O lote teria 4 execuções; o máximo é 3.",
    );
  });

  it("takes the trading hours of the agents, to change for the batch", () => {
    const hours = {
      enabled: true,
      start: "09:00",
      end: "17:30",
      flatten: true,
      cancel_pending: false,
      updated_by: "admin",
    };
    const f = withHours(form({}), hours);
    expect([f.hours, f.hoursStart, f.hoursEnd, f.hoursFlatten, f.hoursCancel]).toEqual([
      true,
      "09:00",
      "17:30",
      true,
      false,
    ]);
    expect(withHours(form({}), { ...hours, enabled: false }).hours).toBe(false);
    expect(formRequest(f, limits, now).request?.hours).toEqual({
      start: "09:00",
      end: "17:30",
      flatten: true,
      cancel_pending: false,
    });
    expect(formRequest(form({}), limits, now).request?.hours).toBeUndefined();
    expect(formRequest({ ...f, hoursEnd: "09:00" }, limits, now).errors.hoursStart).toBe(
      "O início e o fim devem ser diferentes.",
    );
    expect(formRequest({ ...f, hoursStart: "" }, limits, now).request).toBeNull();
    expect(
      formRequest({ ...f, hours: false, hoursStart: "" }, limits, now).request?.hours,
    ).toBeUndefined();
    expect(
      hoursAssumption({ start: "22:00", end: "02:00", flatten: true, cancel_pending: true }),
    ).toBe("22:00–02:00, no fim zera as posições e cancela as ordens pendentes");
    expect(hoursAssumption(undefined)).toBe("");
    expect(reasonLabels.trading_hours).toBe("Fim do horário");
  });

  it("counts the runs and the calls of the LLMs, by run", () => {
    expect(runsOf(form({}))).toBe(4);
    expect(runsOf(form({ validation: "0" }))).toBe(2);
    const agents = [
      {
        agent: "a",
        kind: "trend",
        mode: "paper",
        enabled: true,
        pairs: ["XAUUSDm"],
        compatible: true,
      },
      {
        agent: "b",
        kind: "llm",
        mode: "paper",
        enabled: true,
        pairs: ["XAUUSDm", "BTCUSDm"],
        timeframe: "1h",
        llm: true,
        compatible: true,
      },
    ];
    const f = form({ from: "2026-10-01", to: "2026-10-10", validation: "0" });
    const period = formPeriod(f, now);
    expect(formCalls(agents, f, period, 1000)).toBe(10 * 24 * 2);
    expect(formCalls(agents, { ...f, pairs: ["BTCUSDm"] }, period, 1000)).toBe(10 * 24);
    expect(formCalls(agents, f, period, 100)).toBe(100);
    expect(formCalls(agents, { ...f, validation: "50" }, period, 100)).toBe(200);
  });
});

describe("the justification of a trade of an LLM", () => {
  it("is the last call deciding its side on its pair until its opening", () => {
    const call = (at: number, pair: string, action: string) => ({
      at,
      pair,
      action,
      reason: `${pair} ${action} ${at}`,
      input_sha256: "",
      input_tokens: 0,
      output_tokens: 0,
      cost: 0,
    });
    const calls = [
      call(100, "XAUUSDm", "BUY"),
      call(200, "BTCUSDm", "BUY"),
      call(300, "XAUUSDm", "WAIT"),
      call(400, "XAUUSDm", "BUY"),
      call(500, "XAUUSDm", "BUY"),
    ];
    const trade = {
      pair: "XAUUSDm",
      side: "BUY",
      quantity: 0.01,
      opened_at: 450,
      closed_at: 900,
      entry: 1,
      exit: 2,
      gross: 1,
      fees: 0,
      net: 1,
      entry_reason: "market",
      exit_reason: "take_profit",
    };
    expect(callFor(trade, calls)?.at).toBe(400);
    expect(callFor({ ...trade, side: "SELL" }, calls)).toBeUndefined();
  });
});
