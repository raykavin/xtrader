import { describe, expect, it } from "vitest";
import type { BacktestRunInfo, BacktestSummary } from "../api/types";
import {
  assetRefusals,
  type BacktestForm,
  batchParams,
  callFor,
  candlesIn,
  defaultForm,
  estimatedCalls,
  formCalls,
  formPeriod,
  formRequest,
  hoursAssumption,
  outsideWindows,
  pairsTested,
  progressOf,
  reasonLabels,
  robustnessFlags,
  runsOf,
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
    expect(request?.asset_agents, "the agents per asset followed by default").toBe(true);
    expect(
      formRequest(form({ assetAgents: false }), limits, now).request?.asset_agents,
    ).toBeUndefined();
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
      "O período pode ter até 366 dias.",
    );
    expect(formRequest(form({}), { ...limits, max_runs: 3 }, now).errors.agents).toBe(
      "O lote teria 4 execuções; o máximo é 3.",
    );
  });

  it("takes the trading hours of the agents, to change for the batch", () => {
    const morning = { start: "08:00", end: "10:00", flatten: true, cancel_pending: false };
    const noon = { start: "12:00", end: "14:00", flatten: false, cancel_pending: true };
    const hours = {
      enabled: true,
      windows: [morning, noon],
      start: "08:00",
      end: "10:00",
      flatten: true,
      cancel_pending: false,
      updated_by: "admin",
    };
    const f = withHours(form({}), hours);
    expect(f.hours).toBe(true);
    expect(f.hoursWindows.map(({ key: _, ...w }) => w)).toEqual([
      { ...morning, agents: [], pairs: [] },
      { ...noon, agents: [], pairs: [] },
    ]);
    expect(withHours(form({}), { ...hours, enabled: false }).hours).toBe(false);
    // the hours of a single window, of before
    expect(
      withHours(form({}), { ...hours, windows: [] }).hoursWindows.map(({ key: _, ...w }) => w),
    ).toEqual([{ ...morning, agents: [], pairs: [] }]);
    // the windows sent in the order of the day, whatever the order typed
    const reversed = { ...f, hoursWindows: [...f.hoursWindows].reverse() };
    expect(formRequest(reversed, limits, now).request?.hours).toEqual({
      windows: [morning, noon],
      flatten: false,
      cancel_pending: false,
    });
    expect(formRequest(form({}), limits, now).request?.hours).toBeUndefined();
    const [first, second] = f.hoursWindows;
    if (!first || !second) throw new Error("two windows");
    expect(
      formRequest({ ...f, hoursWindows: [{ ...first, end: "08:00" }, second] }, limits, now).errors
        .hoursWindows,
    ).toBe(
      "A janela 08:00–08:00 deve começar antes de terminar: uma janela não atravessa a meia-noite.",
    );
    expect(
      formRequest({ ...f, hoursWindows: [first, { ...second, start: "10:00" }] }, limits, now)
        .errors.hoursWindows,
    ).toBe(
      "As janelas 08:00–10:00 e 10:00–14:00 se sobrepõem ou se encostam: junte-as numa só ou deixe um intervalo entre elas.",
    );
    expect(
      formRequest({ ...f, hoursWindows: [{ ...first, start: "" }] }, limits, now).request,
    ).toBeNull();
    expect(formRequest({ ...f, hoursWindows: [] }, limits, now).errors.hoursWindows).toBe(
      "Adicione ao menos uma janela.",
    );
    expect(
      formRequest({ ...f, hours: false, hoursWindows: [{ ...first, start: "" }] }, limits, now)
        .request?.hours,
    ).toBeUndefined();
    // a batch of before: its start to its end, crossing midnight
    expect(
      hoursAssumption({ start: "22:00", end: "02:00", flatten: true, cancel_pending: true }),
    ).toBe("22:00–02:00, no fim zera as posições e cancela as ordens pendentes");
    expect(hoursAssumption({ windows: [morning], flatten: false, cancel_pending: false })).toBe(
      "08:00–10:00, no fim zera as posições",
    );
    expect(
      hoursAssumption({
        windows: [morning, { ...noon, flatten: true, cancel_pending: false }],
        flatten: false,
        cancel_pending: false,
      }),
    ).toBe("08:00–10:00, 12:00–14:00, no fim de cada janela zera as posições");
    expect(
      hoursAssumption({ windows: [morning, noon], flatten: false, cancel_pending: false }),
    ).toBe("08:00–10:00, no fim zera as posições; 12:00–14:00, no fim cancela as ordens pendentes");
    expect(hoursAssumption(undefined)).toBe("");
    expect(hoursAssumption({ windows: [], flatten: false, cancel_pending: false })).toBe("");
    expect(reasonLabels.trading_hours).toBe("Fim do horário");
  });

  it("limits a window of a batch to some of its agents, each agent in a window at least", () => {
    const morning = { start: "08:00", end: "10:00", flatten: true, cancel_pending: false };
    const noon = { start: "12:00", end: "14:00", flatten: false, cancel_pending: true };
    const hours = {
      enabled: true,
      windows: [{ ...morning, agents: ["a"] }, noon],
      start: "08:00",
      end: "10:00",
      flatten: true,
      cancel_pending: false,
    };
    // the agents of the windows kept from the settings
    const f = withHours(form({}), hours);
    expect(f.hoursWindows.map((w) => w.agents)).toEqual([["a"], []]);
    expect(outsideWindows(f)).toEqual([]);
    expect(formRequest(f, limits, now).request?.hours).toEqual({
      windows: [{ ...morning, agents: ["a"] }, noon],
      flatten: false,
      cancel_pending: false,
    });
    // b of the batch in no window
    const onlyA = { ...f, hoursWindows: f.hoursWindows.map((w) => ({ ...w, agents: ["a"] })) };
    const refused =
      "O agente b não está em nenhuma janela do horário: inclua-o numa janela ou tire-o do lote.";
    expect(outsideWindows(onlyA)).toEqual([refused]);
    expect(formRequest(onlyA, limits, now).errors.hoursWindows).toBe(refused);
    expect(formRequest(onlyA, limits, now).request).toBeNull();
    expect(outsideWindows({ ...onlyA, agents: ["b", "c", "a"] })).toEqual([
      refused,
      refused.replace("agente b", "agente c"),
    ]);
    // off, or without windows (one asked for first): no agent out of them
    expect(outsideWindows({ ...onlyA, hours: false })).toEqual([]);
    expect(formRequest({ ...onlyA, hours: false }, limits, now).request).not.toBeNull();
    expect(outsideWindows({ ...onlyA, hoursWindows: [] })).toEqual([]);
    // a problem of the windows told first
    const [first] = onlyA.hoursWindows;
    if (!first) throw new Error("a window");
    expect(
      formRequest({ ...onlyA, hoursWindows: [{ ...first, end: "08:00" }] }, limits, now).errors
        .hoursWindows,
    ).toBe(
      "A janela 08:00–08:00 deve começar antes de terminar: uma janela não atravessa a meia-noite.",
    );
    expect(formRequest({ ...onlyA, agents: ["a"] }, limits, now).request?.hours).toEqual({
      windows: [
        { ...morning, agents: ["a"] },
        { ...noon, agents: ["a"] },
      ],
      flatten: false,
      cancel_pending: false,
    });
    // the agents of a window in the assumptions
    expect(
      hoursAssumption({
        windows: [{ ...morning, agents: ["highlow", "matrend"] }],
        flatten: false,
        cancel_pending: false,
      }),
    ).toBe("08:00–10:00 (agentes: highlow, matrend), no fim zera as posições");
    expect(
      hoursAssumption({
        windows: [
          { ...morning, agents: ["highlow"] },
          { ...noon, flatten: true, cancel_pending: false },
        ],
        flatten: false,
        cancel_pending: false,
      }),
    ).toBe("08:00–10:00 (agente: highlow), 12:00–14:00, no fim de cada janela zera as posições");
    expect(
      hoursAssumption({
        windows: [{ ...morning, agents: ["highlow"] }, noon],
        flatten: false,
        cancel_pending: false,
      }),
    ).toBe(
      "08:00–10:00 (agente: highlow), no fim zera as posições; 12:00–14:00, no fim cancela as ordens pendentes",
    );
  });

  it("limits a window of a batch to some assets, kept from the settings and sent as a list", () => {
    const morning = { start: "08:00", end: "10:00", flatten: true, cancel_pending: false };
    const hours = {
      enabled: true,
      windows: [{ ...morning, agents: ["a"], pairs: ["XAUUSDm", "BTCUSDm"] }],
      start: "08:00",
      end: "10:00",
      flatten: true,
      cancel_pending: false,
    };
    const f = withHours(form({ agents: ["a"] }), hours);
    expect(f.hoursWindows[0]?.pairs).toEqual(["XAUUSDm", "BTCUSDm"]);
    expect(formRequest(f, limits, now).request?.hours).toEqual({
      windows: [{ ...morning, agents: ["a"], pairs: ["XAUUSDm", "BTCUSDm"] }],
      flatten: false,
      cancel_pending: false,
    });
    const [first] = f.hoursWindows;
    if (!first) throw new Error("a window");
    const typed = (pairs: string[]) =>
      formRequest({ ...f, hoursWindows: [{ ...first, pairs }] }, limits, now);
    expect(typed(["USOILm"]).request?.hours?.windows?.[0]?.pairs).toEqual(["USOILm"]);
    expect(typed([]).request?.hours?.windows?.[0]).not.toHaveProperty("pairs");
    expect(typed(["USOILm", "USOILm"]).errors.hoursWindows).toBe(
      "A janela 08:00–10:00 repete o ativo USOILm.",
    );
    expect(typed(["USOILm", "USOILm"]).request).toBeNull();
    expect(
      hoursAssumption({
        windows: [{ ...morning, agents: ["highlow"], pairs: ["XAUUSDm", "BTCUSDm"] }],
        flatten: false,
        cancel_pending: false,
      }),
    ).toBe("08:00–10:00 (agente: highlow; ativos: XAUUSDm, BTCUSDm), no fim zera as posições");
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
    // the agents per asset followed: b only on BTCUSDm
    const rules = [{ pairs: ["XAUUSDm"], agents: ["a"] }];
    expect(formCalls(agents, f, period, 1000, rules)).toBe(10 * 24);
    expect(formCalls(agents, { ...f, assetAgents: false }, period, 1000, rules)).toBe(10 * 24 * 2);
  });

  it("follows the agents per asset of the tab Limites, as the controller does", () => {
    const a = {
      agent: "a",
      kind: "trend",
      mode: "live",
      enabled: true,
      pairs: ["XAUUSDm"],
      compatible: true,
    };
    const b = { ...a, agent: "b", pairs: ["XAUUSDm", "BTCUSDm"] };
    const agents = [a, b];
    const rules = [{ pairs: ["XAUUSDm"], agents: ["b"] }];
    const f = form({});
    expect(pairsTested(b, f, rules)).toEqual(["XAUUSDm", "BTCUSDm"]);
    expect(pairsTested(a, f, rules), "a left without XAUUSDm").toEqual([]);
    expect(pairsTested(a, { ...f, assetAgents: false }, rules)).toEqual(["XAUUSDm"]);
    expect(assetRefusals(f, agents, rules)).toEqual([
      "O agente a não pode operar nenhum dos seus ativos pelos agentes por ativo de Configurações › Limites: tire-o do lote ou desmarque a opção.",
    ]);
    expect(assetRefusals({ ...f, assetAgents: false }, agents, rules)).toEqual([]);
    const refused = formRequest(f, { ...limits, agents }, now, rules);
    expect(refused.request).toBeNull();
    expect(refused.errors.assetAgents).toContain("O agente a não pode operar");
    expect(formRequest(f, { ...limits, agents }, now, []).request?.asset_agents).toBe(true);
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

describe("the params of a batch", () => {
  it("reads name=value pairs, the dashes of a flag left out", () => {
    expect(
      batchParams("regime=trend; -equity-curve=20 ;intermarket-pairs=XAUUSDm;BTCUSDm"),
    ).toEqual({
      error: "BTCUSDm: use nome=valor.",
      params: null,
    });
    expect(batchParams("regime=trend; -equity-curve=20")).toEqual({
      params: { regime: "trend", "equity-curve": "20" },
    });
    expect(batchParams("  ")).toEqual({ params: null });
  });

  it("refuses the params the backtest sets and the ones of a language model", () => {
    expect(batchParams("pair=XAUUSDm").error).toContain("próprio backtest");
    expect(batchParams("mt5-bridge=x").error).toContain("próprio backtest");
    expect(batchParams("llm-timeout=1m").error).toContain("LLM");
    expect(batchParams("Regime=trend").error).toContain("letras minúsculas, dígitos e -");
  });
});
