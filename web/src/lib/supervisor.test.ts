import { describe, expect, it } from "vitest";
import type {
  SupervisorActionInfo,
  SupervisorMessageInfo,
  SupervisorResultItem,
} from "../api/types";
import {
  actionLabel,
  actionNote,
  actionOrigin,
  actionStatusLabel,
  actorLabel,
  awaitingDecision,
  byActor,
  closeOutcome,
  formatInterval,
  intervalError,
  mergeMessages,
  scopeProfit,
  scopeText,
  stageLabel,
  supervisionBadges,
  withAction,
} from "./supervisor";

const close: SupervisorActionInfo = {
  id: 7,
  source: "supervisor",
  origin: "supervisor",
  action: "close_positions",
  scope: {
    agents: ["emacross", "fibonacci"],
    positions: [
      {
        agent: "fibonacci",
        ticket: 1,
        pair: "XAUUSDm",
        side: "BUY",
        volume: 0.01,
        stop_loss: 4150,
        take_profit: 4180,
        profit: 12.5,
        currency: "BRL",
        running: true,
      },
      {
        agent: "emacross",
        ticket: 3,
        pair: "XAUUSDm",
        side: "SELL",
        volume: 0.01,
        stop_loss: 0,
        take_profit: 0,
        profit: -2,
        currency: "BRL",
        running: false,
      },
    ],
  },
  options: { cancel_pending: false, keep_blocked: true },
  reason: "fechar tudo",
  evidence: [],
  status: "proposed",
  created_at: 100,
  expires_at: 700,
};

describe("the actions of the supervisor", () => {
  it("names each action apart, the stops by their mode", () => {
    expect(actionLabel("stop_agent")).toBe("Parar o agente quando zerado");
    expect(actionLabel("stop_agent", "now")).toBe("Parar o agente agora");
    expect(actionLabel("block_entries")).toBe("Bloquear novas entradas");
    expect(actionNote("block_entries")).toContain("Nada é fechado nem cancelado");
    expect(actionNote("stop_agent")).toContain("Nenhuma posição é liquidada");
    expect(actionNote("close_positions")).toContain("só as posições dos agentes da aplicação");
  });

  it("writes the scope of a close out: the positions, the pendings, the entries, the agents stopped", () => {
    const text = scopeText(close);
    expect(text).toContain("Fechar 2 posição(ões) de 2 agente(s)");
    expect(text).toContain("só dos agentes da aplicação");
    expect(text).toContain("não são canceladas");
    expect(text).toContain("ficam bloqueadas");
    expect(text).toContain("1 posição(ões) de agentes que não estão rodando");
    expect(scopeProfit(close)).toBe("+10,50 BRL");
  });

  it("tells a proposal waiting from one expired or decided", () => {
    expect(awaitingDecision(close, 600)).toBe(true);
    expect(awaitingDecision(close, 700)).toBe(false);
    expect(awaitingDecision({ ...close, status: "succeeded" }, 600)).toBe(false);
    expect(actionStatusLabel("unknown")).toEqual({ text: "Resultado desconhecido", tone: "warn" });
  });
});

describe("the badges of an agent supervised", () => {
  it("shows its stop and its entries blocked, by whom, with the reason", () => {
    expect(supervisionBadges(undefined)).toEqual([]);
    const badges = supervisionBadges({
      entries_blocked: true,
      entries_reason: "loop de reinício",
      entries_by: "supervisor",
      stop_state: "stopped",
      stop_reason: "loop de reinício",
      stop_by: "admin",
    });
    expect(badges.map((b) => [b.scope, b.text])).toEqual([
      ["agent", "Parado por admin"],
      ["entries", "Bloqueadas pelo XTrader AI"],
    ]);
    expect(badges[0]?.title).toBe("Motivo: loop de reinício");
    expect(badges[1]?.title).toBe("Motivo: loop de reinício");
  });

  it("leaves the daily limit to its card", () => {
    expect(
      supervisionBadges({
        entries_blocked: false,
        stop_state: "none",
        daily_blocked: true,
        daily_reason: "perda diária atingida",
      }),
    ).toEqual([]);
  });
});

describe("the origin of an action", () => {
  it("tells XTrader AI, a user, or a rule applied by itself", () => {
    expect(actionOrigin(close)).toBe("XTrader AI");
    expect(actionOrigin({ ...close, confirmed_by: "admin" })).toBe("XTrader AI");
    expect(
      actionOrigin({
        ...close,
        origin: "rule",
        requested_by: "limite diário",
        confirmed_by: "limite diário",
      }),
    ).toBe("Limite diário (automático)");
    expect(actionOrigin({ ...close, origin: "rule", requested_by: "horário de operação" })).toBe(
      "Horário de operação (automático)",
    );
    expect(actionOrigin({ ...close, source: "user", origin: "user", requested_by: "admin" })).toBe(
      "Usuário (admin)",
    );
  });

  it("names who did something after a verb", () => {
    expect(byActor("supervisor")).toBe("pelo XTrader AI");
    expect(byActor("admin")).toBe("por admin");
    expect(byActor(undefined)).toBe("");
  });
});

describe("the interval of the analyses", () => {
  it("is written in minutes or hours, and kept in its bounds", () => {
    expect(formatInterval(900)).toBe("15 min");
    expect(formatInterval(7200)).toBe("2 h");
    expect(intervalError(15, 300, 86400)).toBeNull();
    expect(intervalError(2, 300, 86400)).toBe("De 5 min a 24 h.");
    expect(intervalError(1.5, 300, 86400)).toBe("Informe um número inteiro de minutos.");
  });
});

describe("the conversation", () => {
  const message = (id: number, action?: SupervisorActionInfo): SupervisorMessageInfo => ({
    id,
    role: "assistant",
    kind: action ? "proposal" : "answer",
    content: String(id),
    action,
    created_at: id,
  });

  it("merges the messages by ID, in their order, and replaces an action decided", () => {
    const merged = mergeMessages([message(1), message(3, close)], [message(2), message(3, close)]);
    expect(merged.map((m) => m.id)).toEqual([1, 2, 3]);
    const decided = withAction(merged, { ...close, status: "succeeded" });
    expect(decided[2]?.action?.status).toBe("succeeded");
  });
});

describe("an answer streaming", () => {
  it("tells its stage while the text is awaited", () => {
    expect(stageLabel(null)).toBe("Enviando");
    expect(stageLabel({ stage: "reading" })).toBe("Lendo os agentes");
    expect(stageLabel({ stage: "thinking" })).toBe("Analisando");
    expect(stageLabel({ stage: "tool", tool: "get_agent" })).toBe(
      "Consultando os detalhes do agente",
    );
    expect(stageLabel({ stage: "tool", tool: "propose_action" })).toBe("Montando a proposta");
  });

  it("names the supervisor XTrader AI, the users as they are", () => {
    expect(actorLabel("supervisor")).toBe("XTrader AI");
    expect(actorLabel("admin")).toBe("admin");
  });
});

describe("the outcome of a close of positions", () => {
  const item = (
    kind: string,
    status: string,
    fields: Partial<SupervisorResultItem> = {},
  ): SupervisorResultItem => ({ agent: "", kind, status, ...fields });
  const done = (items: SupervisorResultItem[], status: string): SupervisorActionInfo => ({
    ...close,
    status,
    result: { items, checked_at: 1_760_000_000 },
  });

  it("tells the positions closed and the orders canceled, checked on the accounts", () => {
    const outcome = closeOutcome(
      done(
        [
          item("position", "done", { agent: "fibonacci", ticket: 1, pair: "XAUUSDm" }),
          item("position", "done", { agent: "vwap", pair: "XAUUSDm" }),
          item("order", "done", { agent: "vwap", ticket: 21, pair: "XAUUSDm" }),
          item("order", "already", { agent: "emacross", ticket: 31 }),
          item("orders", "already", { agent: "matrend" }),
        ],
        "succeeded",
      ),
    );
    expect(outcome).toEqual({
      tone: "ok",
      text: "2 posições fechadas · 1 ordem pendente cancelada.",
      remaining: [],
    });
    expect(closeOutcome(done([item("orders", "already")], "succeeded")).text).toBe(
      "Nada a encerrar.",
    );
  });

  it("tells a partial close as it went, never a success, what remains listed", () => {
    const outcome = closeOutcome(
      done(
        [
          item("position", "done", { agent: "fibonacci", ticket: 1, pair: "XAUUSDm" }),
          item("position", "unknown", {
            agent: "vwap",
            ticket: 2,
            pair: "XAUUSDm",
            detail: "resultado desconhecido",
          }),
          item("position", "failed", {
            agent: "emacross",
            ticket: 3,
            pair: "XAUUSDm",
            detail: "Market closed",
          }),
          item("order", "failed", { agent: "emacross", ticket: 31, pair: "XAUUSDm" }),
        ],
        "partial",
      ),
    );
    expect(outcome.tone).toBe("warn");
    expect(outcome.text).toBe(
      "Parcial: 1 posição fechada · 2 falhas · 1 resultado desconhecido. Confira na conta: nada é reenviado.",
    );
    expect(outcome.remaining).toEqual([
      "vwap · Posição 2 (XAUUSDm): resultado desconhecido",
      "emacross · Posição 3 (XAUUSDm): Market closed",
      "emacross · Ordem 31 (XAUUSDm)",
    ]);

    const failed = closeOutcome(
      done([item("positions", "failed", { detail: "as contas não puderam ser lidas" })], "failed"),
    );
    expect(failed.tone).toBe("error");
    expect(failed.text).toBe("Falhou: 1 falha. Confira na conta.");
    expect(failed.remaining).toEqual(["Posições: as contas não puderam ser lidas"]);
  });
});
