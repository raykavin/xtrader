import { describe, expect, it } from "vitest";
import type { Decision, Order, Snapshot } from "../api/types";
import {
  applyDecisionEvent,
  DECISIONS,
  decisionActionLabel,
  decisionOutcomeLabel,
  decisionTone,
  decisionTriggerLabel,
  formatConfidence,
  isImage,
  linkOrders,
  reanalysisButton,
  reanalysisState,
} from "./decisions";
import { applyOrderEvent } from "./stats";

function decision(partial: Partial<Decision>): Decision {
  return {
    id: "20260927T130000.000000-BTCUSDm",
    pair: "BTCUSDm",
    timeframe: "15m",
    candle: 0,
    decided_at: 100,
    trigger: "candle",
    model: "openai gpt-5-mini",
    latency_ms: 900,
    price: 84000,
    confidence: 0.7,
    levels: [],
    notes: [],
    attachments: [],
    outcome: "applied",
    order_ids: [],
    ...partial,
  };
}

function order(id: number, decision_id?: string): Order {
  return {
    id,
    exchange_id: id,
    pair: "BTCUSDm",
    side: "BUY",
    type: "MARKET",
    status: "FILLED",
    price: 1,
    quantity: 1,
    fee: 0,
    created_at: 0,
    updated_at: 0,
    candle_time: 0,
    ref_price: 0,
    profit: 0,
    profit_value: 0,
    decision_id,
  };
}

describe("labels", () => {
  it("names the actions, the outcomes and the triggers in Portuguese", () => {
    expect(decisionActionLabel("ENTER_LONG")).toBe("Comprar");
    expect(decisionActionLabel(undefined)).toBe("—");
    expect(decisionOutcomeLabel("refused")).toBe("Recusada pelas regras");
    expect(
      decisionTriggerLabel(decision({ trigger: "reanalysis", requested_by: "telegram" })),
    ).toBe("Reanálise (via Telegram)");
    expect(decisionTriggerLabel(decision({ trigger: "interval" }))).toBe("Intervalo de consulta");
    expect(decisionTriggerLabel(decision({ trigger: "candle", requested_by: "web" }))).toBe(
      "Fechamento do candle (via painel)",
    );
    expect(decisionTriggerLabel(decision({ trigger: "start" }))).toBe("Início do bot");
  });

  it("shows the confidence without a sign", () => {
    expect(formatConfidence(0.72)).toBe("72%");
    expect(formatConfidence(1)).toBe("100%");
  });

  it("tells the state of the reanalysis on its button", () => {
    expect(reanalysisState({ enabled: true, status: "running", reanalysis: true })).toBe("enabled");
    expect(reanalysisState({ enabled: false, status: "" })).toBe("disabled");
    expect(reanalysisState(null)).toBe("disabled");
    expect(reanalysisState({ enabled: true, status: "running" })).toBe("unavailable");
    expect(reanalysisButton("enabled").label).toBe("Reanalisar agora");
    expect(reanalysisButton("disabled").label).toBe("Reanalisar agora (DESATIVADO)");
    expect(reanalysisButton("disabled").title).toContain(
      "painel de trading do agente em Configurações",
    );
    expect(reanalysisButton("unavailable").label).toBe("Reanalisar agora (INDISPONÍVEL)");
  });

  it("colors the entries and the exits", () => {
    expect(decisionTone("ENTER_LONG")).toBe("buy");
    expect(decisionTone("EXIT")).toBe("sell");
    expect(decisionTone("WAIT")).toBe("");
  });

  it("shows the images only", () => {
    expect(isImage("image/PNG")).toBe(true);
    expect(isImage("image/svg+xml")).toBe(false);
  });
});

describe("applyDecisionEvent", () => {
  it("keeps the newest first, each decision once", () => {
    const first = decision({ id: "a", decided_at: 100 });
    const second = decision({ id: "b", decided_at: 200 });
    let list = applyDecisionEvent([], { pair: "BTCUSDm", decision: first }, "BTCUSDm");
    list = applyDecisionEvent(list, { pair: "BTCUSDm", decision: second }, "BTCUSDm");
    expect(list.map((d) => d.id)).toEqual(["b", "a"]);

    const linked = { ...first, order_ids: [7] };
    list = applyDecisionEvent(list, { pair: "BTCUSDm", decision: linked }, "BTCUSDm");
    expect(list.map((d) => d.id)).toEqual(["b", "a"]);
    expect(list[1]?.order_ids).toEqual([7]);
  });

  it("ignores the other pairs", () => {
    const list = [decision({ id: "a" })];
    expect(
      applyDecisionEvent(list, { pair: "XAUUSDm", decision: decision({ id: "x" }) }, "BTCUSDm"),
    ).toBe(list);
  });

  it("keeps the newest DECISIONS, the most the API answers", () => {
    expect(DECISIONS).toBe(500);
    const list = Array.from({ length: DECISIONS }, (_, i) =>
      decision({ id: `d${i}`, decided_at: DECISIONS - i }),
    );
    const newest = decision({ id: "new", decided_at: DECISIONS + 1 });
    const next = applyDecisionEvent(list, { pair: "BTCUSDm", decision: newest }, "BTCUSDm");
    expect(next).toHaveLength(DECISIONS);
    expect(next[0]?.id).toBe("new");
    expect(next[DECISIONS - 1]?.id, "the oldest dropped").toBe(`d${DECISIONS - 2}`);
  });
});

describe("linkOrders", () => {
  it("links the orders the decision placed, once", () => {
    const snapshot = {
      pair: "BTCUSDm",
      orders: [order(1), order(2, "older"), order(3)],
    } as Snapshot;
    const next = linkOrders(snapshot, decision({ id: "d", order_ids: [1, 2] }));
    expect(next.orders.map((o) => o.decision_id)).toEqual(["d", "older", undefined]);
    expect(linkOrders(next, decision({ id: "d", order_ids: [1, 2] }))).toBe(next);
    expect(linkOrders(snapshot, decision({ pair: "XAUUSDm", order_ids: [1] }))).toBe(snapshot);
  });
});

describe("applyOrderEvent", () => {
  it("keeps the decision linked to an order", () => {
    const snapshot = { pair: "BTCUSDm", orders: [order(1, "d")] } as Snapshot;
    const next = applyOrderEvent(snapshot, {
      pair: "BTCUSDm",
      order: { ...order(1), status: "CANCELED" },
    });
    expect(next.orders[0]?.status).toBe("CANCELED");
    expect(next.orders[0]?.decision_id).toBe("d");
  });
});
