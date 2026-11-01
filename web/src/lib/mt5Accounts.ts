// The accounts of MetaTrader 5 on the dashboard (Configurações > Contas MT5):
// how each stands, what the bridge confirmed of it, and the form that sets it.

import type { MT5AccountInfo, MT5AccountRequest } from "../api/types";
import { httpURLError } from "./settings";

/** A label and its tone, as the chips of the dashboard show them. */
export interface AccountLabel {
  tone: "on" | "off" | "warn" | "error" | "info";
  text: string;
}

/** The kind of an account as the bridge confirmed it: real, demo or contest; unconfirmed until tested. */
export function kindLabel(kind: string | undefined): AccountLabel {
  switch (kind) {
    case "real":
      return { tone: "error", text: "Real" };
    case "demo":
      return { tone: "info", text: "Demo" };
    case "contest":
      return { tone: "info", text: "Concurso" };
  }
  return { tone: "off", text: "Tipo não confirmado" };
}

/**
 * How the terminal of an account stands: inactive (not the one of its
 * terminal); connected in it; waiting for its password or for the account;
 * the bridge unreachable or failing; not read yet.
 */
export function sessionLabel(account: MT5AccountInfo): AccountLabel {
  if (!account.active) return { tone: "off", text: "Inativa" };
  const session = account.session;
  if (!session) return { tone: "off", text: "Ainda não lida" };
  if (session.error) return { tone: "error", text: session.error };
  if (session.current) return { tone: "on", text: "Conectada" };
  if (session.waiting === "password") return { tone: "warn", text: "O terminal pede a senha" };
  if (session.waiting === "session") return { tone: "warn", text: "O terminal aguarda a conta" };
  if (session.logged_login) {
    return { tone: "warn", text: `O terminal está na conta ${session.logged_login}` };
  }
  return { tone: "warn", text: "O terminal não está conectado" };
}

/** The form of an account of MT5, as typed: its secrets apart (written only). */
export interface AccountForm {
  name: string;
  login: string;
  server: string;
  bridgeURL: string;
  allowReal: boolean;
  readOnly: boolean;
  password: string;
  token: string;
  reason: string;
}

/** The fields of the form of an account. */
export type AccountField = keyof AccountForm;

/** The bridge of the terminal of the compose, the default of a new account. */
export const defaultBridge = "http://mt5:18812";

/** The form of account, or of a new one on the terminal of the compose. */
export function accountForm(account?: MT5AccountInfo): AccountForm {
  return {
    name: account?.name ?? "",
    login: account ? String(account.login) : "",
    server: account?.server ?? "",
    bridgeURL: account?.bridge_url ?? defaultBridge,
    allowReal: account?.allow_real ?? false,
    readOnly: account?.read_only ?? false,
    password: "",
    token: "",
    reason: "",
  };
}

/** Why the form of an account is refused, by field, as the controller checks it; {} when taken. */
export function accountErrors(
  form: AccountForm,
  creating: boolean,
): Partial<Record<AccountField, string>> {
  const errors: Partial<Record<AccountField, string>> = {};
  const name = form.name.trim();
  if (name === "" || name.length > 64) errors.name = "Um nome de 1 a 64 caracteres.";
  if (!/^\d+$/.test(form.login.trim()) || Number(form.login) <= 0) {
    errors.login = "O login, um número maior que zero.";
  }
  if (form.server.trim() === "") errors.server = "O servidor, ex.: Exness-MT5Real11.";
  const bridge = form.bridgeURL.trim();
  const bridgeError = bridge === "" ? "Informe a URL da bridge." : httpURLError(bridge);
  if (bridgeError) errors.bridgeURL = bridgeError;
  if (creating && form.password.length > 256) errors.password = "Até 256 caracteres.";
  return errors;
}

/** The request creating the account of the form, taken: its secrets only when typed. */
export function accountCreateRequest(form: AccountForm): MT5AccountRequest {
  return {
    name: form.name.trim(),
    login: Number(form.login.trim()),
    server: form.server.trim(),
    bridge_url: form.bridgeURL.trim(),
    allow_real: form.allowReal,
    read_only: form.readOnly,
    password: form.password === "" ? undefined : form.password,
    bridge_token: form.token.trim() === "" ? undefined : form.token.trim(),
    reason: form.reason.trim() || undefined,
  };
}

/** The request changing the account of the form from initial: the fields changed only, null for none. */
export function accountUpdateRequest(
  initial: AccountForm,
  form: AccountForm,
): MT5AccountRequest | null {
  const request: MT5AccountRequest = {};
  if (form.name.trim() !== initial.name.trim()) request.name = form.name.trim();
  if (form.login.trim() !== initial.login.trim()) request.login = Number(form.login.trim());
  if (form.server.trim() !== initial.server.trim()) request.server = form.server.trim();
  if (form.bridgeURL.trim() !== initial.bridgeURL.trim())
    request.bridge_url = form.bridgeURL.trim();
  if (form.allowReal !== initial.allowReal) request.allow_real = form.allowReal;
  if (form.readOnly !== initial.readOnly) request.read_only = form.readOnly;
  if (Object.keys(request).length === 0) return null;
  if (form.reason.trim()) request.reason = form.reason.trim();
  return request;
}

/** Whether a change of request moves the terminal of an account: refused while it is in use. */
export function movesTheTerminal(request: MT5AccountRequest): boolean {
  return (
    request.login !== undefined || request.server !== undefined || request.bridge_url !== undefined
  );
}

/** The kinds of the events of an account in words. */
export const eventLabels: Record<string, string> = {
  created: "Criada",
  changed: "Alterada",
  activated: "Ativada",
  deactivated: "Desativada",
  deleted: "Excluída",
  imported: "Importada da bridge",
  linked: "Agente associado",
  unlinked: "Agente desassociado",
  tested: "Testada",
  session: "Sessão enviada ao terminal",
};
