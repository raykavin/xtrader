import { describe, expect, it } from "vitest";
import { APIError, retryAfterSeconds } from "../api/errors";
import { LOGIN_CLOSED, loginError, NO_ROLE, nextPage } from "./auth";

const ORIGIN = "https://xtrader.example";

describe("nextPage", () => {
  it("goes back to a page of the dashboard only", () => {
    const next = (search: string) => nextPage(search, ORIGIN);
    expect(next("")).toBe("/");
    expect(next("?next=%2F%3Fagent%3Dllm%26pair%3DXAUUSDm")).toBe("/?agent=llm&pair=XAUUSDm");
    expect(next("?next=%2Fsettings%23prompts")).toBe("/settings#prompts");
    expect(next("?next=%2F%2Fevil.example")).toBe("/");
    expect(next("?next=https%3A%2F%2Fevil.example")).toBe("/");
    expect(next("?next=%2F%5Cevil.example")).toBe("/");
    expect(next("?next=%2Flogin")).toBe("/");
    expect(next("?next=%2Flogin%3Fnext%3D%252F")).toBe("/");
  });

  it("refuses what the browsers drop of a URL or read as a slash", () => {
    const next = (value: string) => nextPage(`?${new URLSearchParams({ next: value })}`, ORIGIN);
    for (const value of [
      "/\t/evil.example",
      "/\n/evil.example",
      "/\r/evil.example",
      "\t//evil.example",
      "/ /evil.example",
      "/\u0000/evil.example",
      "/\u007f/evil.example",
      "/a\\b",
      "/\\\\evil.example",
      "///evil.example",
      "http:/evil.example",
      "https:evil.example",
      "//user@evil.example/",
      "javascript:alert(1)",
      "https://xtrader.example/settings",
    ]) {
      expect(next(value), JSON.stringify(value)).toBe("/");
    }
    expect(next("/%09/x")).toBe("/%09/x"); // a path, escaped
  });

  it("refuses a path that is another site once its dot segments are gone", () => {
    const next = (value: string) => nextPage(`?${new URLSearchParams({ next: value })}`, ORIGIN);
    for (const value of [
      "/.//evil.example",
      "/a/..//evil.example/x",
      "/%2e//evil.example",
      "/%2E%2E//evil.example",
      "/a/b/../..//evil.example",
      "/a//b",
    ]) {
      expect(next(value), JSON.stringify(value)).toBe("/");
    }
    expect(next("/./settings")).toBe("/settings");
    expect(next("/./login")).toBe("/");
    expect(next("/?next=//evil.example")).toBe("/?next=//evil.example");
  });
});

describe("loginError", () => {
  it("never tells which credential is wrong", () => {
    expect(loginError(new APIError("invalid username or password", 401))).toBe(
      "Usuário ou senha inválidos.",
    );
    expect(loginError(new APIError("no credentials", 503))).toBe(LOGIN_CLOSED);
    expect(loginError(new APIError("no role", 403))).toBe(NO_ROLE);
    expect(loginError(new APIError("invalid JSON payload", 400))).toBe(
      "Não foi possível entrar. Tente novamente.",
    );
    expect(loginError(new TypeError("Failed to fetch"))).toContain("Sem conexão");
  });

  it("tells how long to wait after too many logins", () => {
    const refused = (seconds?: number) =>
      loginError(new APIError("too many failed logins: try again later", 429, seconds));
    expect(refused(1)).toBe("Muitas tentativas de login. Aguarde um instante e tente novamente.");
    expect(refused(undefined)).toBe(refused(1));
    expect(refused(45)).toBe("Muitas tentativas de login. Aguarde 45 s e tente novamente.");
    expect(refused(300)).toBe("Muitas tentativas de login. Aguarde 5 min e tente novamente.");
  });
});

describe("retryAfterSeconds", () => {
  it("reads the seconds of Retry-After", () => {
    expect(retryAfterSeconds("30")).toBe(30);
    expect(retryAfterSeconds(" 2 ")).toBe(2);
    expect(retryAfterSeconds(null)).toBeUndefined();
    expect(retryAfterSeconds("Wed, 21 Oct 2026 07:28:00 GMT")).toBeUndefined();
  });
});
