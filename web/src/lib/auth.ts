// The login of the dashboard: where to go once logged in, and what to tell of
// a login refused.

import { APIError } from "../api/errors";

/**
 * The page to go to once logged in: the next of the query when it is a page
 * of the dashboard, else its root, never another site nor the login again.
 */
export function nextPage(search: string): string {
  const next = new URLSearchParams(search).get("next") ?? "";
  if (
    !next.startsWith("/") ||
    next.startsWith("//") ||
    next.startsWith("/\\") ||
    next.startsWith("/login")
  ) {
    return "/";
  }
  return next;
}

/** The message of the dashboard without a user to log in with. */
export const LOGIN_CLOSED =
  "O painel não tem usuários: defina bootstrap.username e bootstrap.password no config.yml do controller, ou importe os da versão anterior com o migrator.";

/**
 * The message of a login refused: the same for any wrong credential, never
 * telling which one.
 */
export function loginError(error: unknown): string {
  if (error instanceof APIError) {
    if (error.status === 401) return "Usuário ou senha inválidos.";
    if (error.status === 503) return LOGIN_CLOSED;
    return "Não foi possível entrar. Tente novamente.";
  }
  return "Sem conexão com o painel. Verifique a rede e tente novamente.";
}
