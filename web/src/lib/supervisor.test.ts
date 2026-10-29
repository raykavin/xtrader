import { describe, expect, it } from "vitest";
import type { SupervisorActionInfo, SupervisorMessageInfo } from "../api/types";
import {
  actionLabel,
  actionNote,
  actionStatusLabel,
  actorLabel,
  awaitingDecision,
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
  it("shows its stop and its entries blocked, with the reason", () => {
    expect(supervisionBadges(undefined)).toEqual([]);
    expect(
      supervisionBadges({
        entries_blocked: false,
        stop_state: "none",
        daily_blocked: true,
        daily_reason: "perda diária atingida",
      }),
    ).toEqual([
      {
        text: "Limite diário: entradas bloqueadas",
        tone: "error",
        title: "perda diária atingida · liberado às 00:00",
      },
    ]);
    const badges = supervisionBadges({
      entries_blocked: true,
      entries_reason: "loop de reinício",
      entries_by: "supervisor",
      stop_state: "stopped",
      stop_reason: "loop de reinício",
    });
    expect(badges.map((b) => b.text)).toEqual([
      "Interrompido pelo XTrader AI",
      "Entradas bloqueadas",
    ]);
    expect(badges[0]?.title).toBe("Motivo: loop de reinício");
    expect(badges[1]?.title).toBe("Motivo: loop de reinício · Por: XTrader AI");
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
