import { describe, expect, it } from "vitest";
import { APIError } from "../api/errors";
import { LOGIN_CLOSED, loginError, nextPage } from "./auth";

describe("nextPage", () => {
  it("goes back to a page of the dashboard only", () => {
    expect(nextPage("")).toBe("/");
    expect(nextPage("?next=%2F%3Fagent%3Dllm%26pair%3DXAUUSDm")).toBe("/?agent=llm&pair=XAUUSDm");
    expect(nextPage("?next=%2F%2Fevil.example")).toBe("/");
    expect(nextPage("?next=https%3A%2F%2Fevil.example")).toBe("/");
    expect(nextPage("?next=%2F%5Cevil.example")).toBe("/");
    expect(nextPage("?next=%2Flogin")).toBe("/");
  });
});

describe("loginError", () => {
  it("never tells which credential is wrong", () => {
    expect(loginError(new APIError("invalid username or password", 401))).toBe(
      "Usuário ou senha inválidos.",
    );
    expect(loginError(new APIError("no credentials", 503))).toBe(LOGIN_CLOSED);
    expect(loginError(new APIError("invalid JSON payload", 400))).toBe(
      "Não foi possível entrar. Tente novamente.",
    );
    expect(loginError(new TypeError("Failed to fetch"))).toContain("Sem conexão");
  });
});
