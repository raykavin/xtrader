import { describe, expect, it } from "vitest";
import type { MT5AccountInfo } from "../api/types";
import {
  accountCreateRequest,
  accountErrors,
  accountForm,
  accountState,
  accountUpdateRequest,
  agentsLabel,
  kindLabel,
  movesTheTerminal,
  sessionLabel,
  switchQuestion,
  terminalsOf,
} from "./mt5Accounts";

const account = (fields: Partial<MT5AccountInfo> = {}): MT5AccountInfo => ({
  id: 1,
  name: "Real",
  login: 478378599,
  server: "Exness-MT5Real11",
  bridge_url: "http://mt5:18812",
  allow_real: true,
  read_only: false,
  active: true,
  password_set: true,
  token_set: true,
  agents: ["trend"],
  updated_at: 1,
  terminal: "mt5:18812",
  ...fields,
});

describe("the accounts of MT5", () => {
  it("tell their kind as confirmed by the bridge, unconfirmed until then", () => {
    expect(kindLabel("real")).toEqual({ tone: "error", text: "Real" });
    expect(kindLabel("demo").text).toBe("Demo");
    expect(kindLabel(undefined).text).toBe("Tipo não confirmado");
  });

  it("tell how their terminal stands", () => {
    expect(sessionLabel(account({ active: false })).text).toBe("Inativa");
    expect(sessionLabel(account()).text).toBe("Ainda não lida");
    const at = 1;
    expect(
      sessionLabel(account({ session: { reachable: true, connected: true, current: true, at } }))
        .text,
    ).toBe("Conectada");
    expect(
      sessionLabel(
        account({
          session: { reachable: true, connected: false, current: false, waiting: "password", at },
        }),
      ),
    ).toEqual({ tone: "warn", text: "O terminal pede a senha" });
    expect(
      sessionLabel(
        account({
          session: { reachable: true, connected: true, current: false, logged_login: 5, at },
        }),
      ).text,
    ).toBe("O terminal está na conta 5");
    expect(
      sessionLabel(
        account({
          session: {
            reachable: false,
            connected: false,
            current: false,
            error: "a bridge não respondeu",
            at,
          },
        }),
      ),
    ).toEqual({ tone: "error", text: "a bridge não respondeu" });
  });

  it("check the form as the controller does, a new one on the terminal of the compose", () => {
    const form = accountForm();
    expect(form.bridgeURL).toBe("http://mt5:18812");
    expect(Object.keys(accountErrors(form, true)).sort()).toEqual(["login", "name", "server"]);
    const filled = {
      ...form,
      name: " Demo ",
      login: "87654321",
      server: "Exness-MT5Trial11",
      password: "pw",
    };
    expect(accountErrors(filled, true)).toEqual({});
    expect(accountErrors({ ...filled, login: "-1" }, true).login).toBeDefined();
    expect(
      accountErrors({ ...filled, bridgeURL: "http://u:p@mt5:18812" }, true).bridgeURL,
    ).toContain("Sem usuário");
    expect(accountCreateRequest(filled)).toEqual({
      name: "Demo",
      login: 87654321,
      server: "Exness-MT5Trial11",
      bridge_url: "http://mt5:18812",
      allow_real: false,
      read_only: false,
      password: "pw",
      bridge_token: undefined,
      reason: undefined,
    });
  });

  it("change the fields changed only, telling a move of the terminal", () => {
    const initial = accountForm(account());
    expect(accountUpdateRequest(initial, initial)).toBeNull();
    const named = accountUpdateRequest(initial, {
      ...initial,
      name: "Real Exness",
      readOnly: true,
    });
    expect(named).toEqual({ name: "Real Exness", read_only: true });
    expect(named && movesTheTerminal(named)).toBe(false);
    const moved = accountUpdateRequest(initial, { ...initial, login: "5", reason: "outra" });
    expect(moved).toEqual({ login: 5, reason: "outra" });
    expect(moved && movesTheTerminal(moved)).toBe(true);
  });

  it("tell whether they are active as the agents tell whether they run", () => {
    const at = 1;
    expect(accountState(account({ active: false }))).toEqual({ label: "Inativa", tone: "off" });
    expect(
      accountState(account({ session: { reachable: true, connected: true, current: true, at } })),
    ).toEqual({ label: "Ativa", tone: "on", mark: "play" });
    expect(accountState(account()).tone).toBe("warn");
    expect(agentsLabel(0)).toBe("nenhum");
    expect(agentsLabel(1)).toBe("1 agente");
    expect(agentsLabel(46)).toBe("46 agentes");
  });

  it("count their terminals, many on a host, two URLs of one terminal once", () => {
    const at = 1;
    const served = { reachable: true, connected: true, current: true, at };
    expect(
      terminalsOf([
        account({ id: 1, session: served, instance: "a" }),
        account({ id: 2, active: false, instance: "a" }),
        account({ id: 3, terminal: "mt5:18813", active: false }),
        account({
          id: 4,
          terminal: "other:18812",
          instance: "a",
          bridge_url: "http://other:18812",
        }),
      ]),
    ).toEqual({ active: 1, total: 2 });
    expect(terminalsOf([])).toEqual({ active: 0, total: 0 });
  });

  it("ask before a switch what it does", () => {
    const demo = account({ id: 2, name: "Demo", login: 87654321, active: false });
    const preview = { active: true, current: ["Real"], running: ["trend", "orb"], exposure: [] };
    expect(switchQuestion(demo, preview)).toBe(
      "Logar o terminal na conta 87654321 no lugar da conta Real? Os 2 agentes em execução no terminal serão parados.",
    );
    expect(switchQuestion(demo, { ...preview, exposure: ["posição de trend em XAUUSDm #1"] })).toBe(
      "Existem posições abertas ou ordens pendentes na conta Real. Ao trocar de conta, todas as posições serão encerradas e as ordens pendentes serão canceladas. Deseja continuar?",
    );
    expect(
      switchQuestion(account(), {
        active: false,
        current: ["Real"],
        running: ["trend"],
        exposure: [],
      }),
    ).toBe(
      "Desativar a conta Real? O terminal deixa de servi-la. O agente trend, em execução no terminal, será parado.",
    );
    expect(switchQuestion(demo, { active: true, current: [], running: [], exposure: [] })).toBe(
      "Logar o terminal na conta 87654321?",
    );
  });
});
