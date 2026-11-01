import { describe, expect, it } from "vitest";
import type { MT5AccountInfo } from "../api/types";
import {
  accountCreateRequest,
  accountErrors,
  accountForm,
  accountUpdateRequest,
  kindLabel,
  movesTheTerminal,
  sessionLabel,
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
});
