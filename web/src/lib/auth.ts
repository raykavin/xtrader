// The login of the dashboard: where to go once logged in, and what to tell of
// a login refused.

import { APIError } from "../api/errors";

/**
 * Whether a character of a URL is one the browsers drop (a control
 * character, eg: a tab or a newline), a space, or a backslash, which they
 * read as a slash: "/\t/evil.example" goes to evil.example.
 */
function unsafeURLChar(c: string): boolean {
  const code = c.charCodeAt(0);
  return code <= 0x20 || code === 0x7f || c === "\\";
}

/**
 * The page to go to once logged in: the next of the query when it is a page
 * of the dashboard at origin (the one of the page), else its root, never
 * another site nor the login again.
 */
export function nextPage(search: string, origin: string): string {
  const next = new URLSearchParams(search).get("next") ?? "";
  if (!next.startsWith("/") || next.startsWith("//") || [...next].some(unsafeURLChar)) {
    return "/";
  }
  let url: URL;
  try {
    url = new URL(next, origin);
  } catch {
    return "/";
  }
  // The dot segments are gone from the pathname: "/.//evil.example" is
  // "//evil.example", which a browser reads as another site.
  if (url.origin !== origin || url.pathname.includes("//") || url.pathname.startsWith("/login")) {
    return "/";
  }
  return url.pathname + url.search + url.hash;
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
    if (error.status === 429) {
      const wait = error.retryAfter ?? 0;
      return wait > 1
        ? `Muitas tentativas de login. Aguarde ${formatWait(wait)} e tente novamente.`
        : "Muitas tentativas de login. Aguarde um instante e tente novamente.";
    }
    return "Não foi possível entrar. Tente novamente.";
  }
  return "Sem conexão com o painel. Verifique a rede e tente novamente.";
}

/** A wait of seconds, as the login tells it: "45 s", "3 min". */
function formatWait(seconds: number): string {
  return seconds < 60 ? `${seconds} s` : `${Math.ceil(seconds / 60)} min`;
}
