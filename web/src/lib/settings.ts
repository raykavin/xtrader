// The settings of the controller: what the page of the settings shows of the
// agents, of their prompts and of the users, the form of an agent checked as
// the controller checks it, and the CSRF token of the session, which the
// requests changing something carry.

import type {
  AgentAccountRequest,
  AgentCreateRequest,
  AgentLLMRequest,
  AgentPromptStatus,
  AgentSettings,
  AgentSettingsRequest,
  DecisionPrompt,
} from "../api/types";
import type { StatusMark } from "../components/StatusIcon";

/** The CSRF token of the session in the cookies of the page, empty without one. */
export function csrfToken(cookies: string): string {
  for (const part of cookies.split(";")) {
    const [name, ...value] = part.trim().split("=");
    if (name === "csrf_token") return decodeURIComponent(value.join("="));
  }
  return "";
}

const kinds: Record<string, string> = {
  llm: "LLM",
  trend: "Tendência",
  momentum: "Momentum",
  breakout: "Rompimento",
  intraday: "Intraday",
};

/** The label of the kind of an agent. */
export function agentKindLabel(kind: string): string {
  return kinds[kind] ?? kind;
}

/** What the trade panel of an agent turns on in the dashboard: its bot starts and stops without it. */
export const tradePanelHint =
  "Ordens pela boleta e pelo gráfico e a reanálise no painel; iniciar e parar o bot não dependem dele";

/** The kinds of the agents, of the binaries the controller launches, in its order. */
export const agentKinds = Object.keys(kinds);

export type Tone = "on" | "off" | "warn";

/**
 * The state of an agent: disabled, not launched by the controller (eg: another
 * agent running trades the same positions), exited and waiting to be started
 * again, running (a restart pending), or stopped, with the mark of its chip
 * when it runs or stopped.
 */
export function agentState(agent: AgentSettings): { label: string; tone: Tone; mark?: StatusMark } {
  if (!agent.enabled) return { label: "Desativado", tone: "off" };
  if (agent.launch_error) return { label: "Não iniciado", tone: "warn", mark: "stop" };
  if (agent.running && agent.restarting)
    return { label: "Reiniciando", tone: "warn", mark: "stop" };
  if (agent.running && agent.restart_pending) return { label: "Reinício pendente", tone: "warn" };
  if (agent.running) return { label: "Executando", tone: "on", mark: "play" };
  if (!agent.launched) return { label: "Iniciado à parte", tone: "off" };
  return { label: "Parado", tone: "warn", mark: "stop" };
}

/**
 * The note of the card of an agent: why the controller didn't launch it, a
 * restart pending, why it stays stopped once exited ("Reiniciar se cair" off),
 * or how its last start exited, with the times the controller started it
 * again since its launch.
 */
export function agentNote(agent: AgentSettings): string {
  if (agent.launch_error) return `Não iniciado: ${agent.launch_error}`;
  if (agent.restart_pending)
    return "Configurações de inicialização alteradas: reinicie para aplicá-las.";
  const exit = agent.last_exit ? `Última saída: ${agent.last_exit}` : "";
  if (agent.exited) {
    const note =
      "Saiu e fica parado, iniciado com “Reiniciar se cair” desligado: reinicie para iniciá-lo de novo (um reinício do controller também o inicia).";
    return exit ? `${note} ${exit}` : note;
  }
  const restarts = agent.restarts ?? 0;
  if (restarts === 0) return exit;
  const times = restarts === 1 ? "1 vez" : `${restarts} vezes`;
  return exit ? `${exit}. Reiniciado ${times}` : `Reiniciado ${times}`;
}

/** How long ago, as the note of the outbox tells it: "12 min", "3 h", "2 dias". */
function formatAge(seconds: number): string {
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours} h`;
  return `${Math.floor(hours / 24)} dias`;
}

/**
 * The note of the outbox of an agent, as it told the controller last: the
 * records not written to the database yet, once their sending fails or the
 * oldest of them is a minute old, the ones the controller refused for good,
 * set apart in it, and the ones it failed again and again, held apart, sent
 * again now and then.
 */
export function outboxNote(agent: AgentSettings, now = Date.now()): string {
  const outbox = agent.outbox;
  if (!outbox) return "";
  const notes: string[] = [];
  const age = outbox.since ? Math.max(0, Math.floor(now / 1000) - outbox.since) : 0;
  if (outbox.pending > 0 && (outbox.failing || age >= 60)) {
    const records =
      outbox.pending === 1
        ? "1 registro ainda não gravado"
        : `${outbox.pending} registros ainda não gravados`;
    const oldest = age >= 60 ? `, o mais antigo há ${formatAge(age)}` : "";
    const failing = outbox.failing ? " (o envio está falhando: veja o log do controller)" : "";
    notes.push(`Outbox: ${records} no banco${oldest}${failing}`);
  }
  if (outbox.set_apart) {
    const records =
      outbox.set_apart === 1
        ? "1 registro recusado para sempre, separado"
        : `${outbox.set_apart} registros recusados para sempre, separados`;
    notes.push(`Outbox: ${records} no arquivo do agente (veja o log do agente)`);
  }
  if (outbox.held) {
    const records =
      outbox.held === 1
        ? "1 registro que falhou seguidamente, separado"
        : `${outbox.held} registros que falharam seguidamente, separados`;
    notes.push(
      `Outbox: ${records} no arquivo do agente e reenviado${outbox.held === 1 ? "" : "s"} de tempos em tempos (veja o log do agente)`,
    );
  }
  return notes.join(". ");
}

/** The agents of a kind, a group of the page: the LLM ones, or the ones of a strategy. */
export interface AgentGroup {
  kind: string;
  label: string;
  /** In the order of the list. */
  agents: AgentSettings[];
  /** How many of them are enabled. */
  enabled: number;
}

/**
 * The agents by kind, the kinds without agents left out: the LLM ones first,
 * then the strategies in the order of agentKinds, the unknown kinds after them
 * by name.
 */
export function agentGroups(agents: AgentSettings[]): AgentGroup[] {
  const byKind = new Map<string, AgentSettings[]>();
  for (const agent of agents) {
    const list = byKind.get(agent.kind) ?? [];
    list.push(agent);
    byKind.set(agent.kind, list);
  }
  const unknown = [...byKind.keys()].filter((kind) => !agentKinds.includes(kind)).sort();
  return [...agentKinds, ...unknown].flatMap((kind) => {
    const list = byKind.get(kind);
    if (!list) return [];
    const enabled = list.filter((agent) => agent.enabled).length;
    return [{ kind, label: agentKindLabel(kind), agents: list, enabled }];
  });
}

/**
 * What tells an agent apart on its card, of its params that have it: its
 * pairs, its timeframe, and its strategy, or the model of an LLM agent.
 */
export function agentSummary(agent: AgentSettings): { label: string; value: string }[] {
  const { pair = "", timeframe = "", strategy = "" } = agent.params;
  const pairs = pair
    .split(",")
    .map((p) => p.trim())
    .filter(Boolean);
  const parts = [
    { label: pairs.length > 1 ? "Pares" : "Par", value: pairs.join(", ") },
    { label: "Timeframe", value: timeframe.trim() },
    agent.kind === "llm"
      ? { label: "Modelo", value: agent.model?.trim() ?? "" }
      : { label: "Estratégia", value: strategy.trim() },
  ];
  return parts.filter((part) => part.value !== "");
}

/** The tabs of the page of the settings, by the hash of their URL, eg: #prompts. */
export const settingsTabs = ["agentes", "prompts", "telegram", "usuarios"] as const;

export type SettingsTab = (typeof settingsTabs)[number];

/** The tab of the hash of the URL of the settings, the agents for none or an unknown one. */
export function settingsTabFromHash(hash: string): SettingsTab {
  const name = hash.replace(/^#/, "").toLowerCase();
  return settingsTabs.find((tab) => tab === name) ?? "agentes";
}

/** The name of a prompt for the page, eg: trend_following v3, the default one embedded. */
export function promptLabel(prompt: DecisionPrompt): string {
  if (!prompt.id && prompt.name === "default") return "default (embutido)";
  return prompt.version ? `${prompt.name} v${prompt.version}` : prompt.name;
}

/** What came of the last prompt an LLM agent was asked to switch to, null for none. */
export function promptStatusLabel(status?: AgentPromptStatus): { text: string; tone: Tone } | null {
  if (!status) return null;
  const prompt = promptLabel({ id: status.prompt_id, name: status.name, version: status.version });
  if (status.applied) return { text: `${prompt}: aplicado`, tone: "on" };
  return {
    text: `${prompt}: recusado, o anterior mantido${status.error ? ` (${status.error})` : ""}`,
    tone: "warn",
  };
}

/** A Telegram user ID typed in the form, null when it isn't one. */
export function parseTelegramID(text: string): number | null {
  const trimmed = text.trim();
  if (!/^-?\d{1,16}$/.test(trimmed)) return null;
  const id = Number(trimmed);
  return Number.isSafeInteger(id) && id !== 0 ? id : null;
}

/** Why a password is refused, null when it is taken: 8 to 72 bytes, as bcrypt takes. */
export function passwordError(password: string): string | null {
  const size = new TextEncoder().encode(password).length;
  if (size < 8) return "A senha precisa de 8 caracteres ao menos.";
  if (size > 72) return "A senha pode ter 72 bytes no máximo.";
  return null;
}

/** Why the name of a new prompt is refused, as the controller refuses it, null when taken. */
export function promptNameError(name: string): string | null {
  if (!/^[a-z0-9][a-z0-9_.-]{0,63}$/.test(name)) {
    return "Use de 1 a 64 letras minúsculas, dígitos, _, . ou -, começando por letra ou dígito.";
  }
  return null;
}

/** Why a username is refused, as the controller refuses it, null when taken. */
export function usernameError(username: string): string | null {
  if (!/^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$/.test(username)) {
    return "Use de 1 a 64 letras, dígitos, ., _, @ ou -, começando por letra ou dígito.";
  }
  return null;
}

/** The modes of an agent: live, on its MT5 account, or paper, on a paper wallet. */
export const agentModes = ["live", "paper"];

/** The providers of the language models of the LLM agents. */
export const llmProviders = ["openai", "anthropic"];

/** The reasoning efforts of a language model; none, the one of the model. */
export const reasoningEfforts = ["none", "minimal", "low", "medium", "high", "xhigh"];

/** The length of the comment of the orders of an agent at most: the longest MT5 keeps. */
export const maxComment = 31;

/** The length of the value of a param at most, in bytes. */
const maxParamValue = 1024;

/** The tokens of an answer at most, the most the database keeps: an integer of 32 bits. */
const maxTokensLimit = 2 ** 31 - 1;

/**
 * The flags a field of an agent sets, and the ones of the files the database
 * replaces: never params, which the controller refuses.
 */
const reservedParams = new Set([
  "mode",
  "exchange",
  "api",
  "trade-panel",
  "telegram-control",
  "mt5-bridge",
  "mt5-token",
  "mt5-magic",
  "mt5-comment",
  "mt5-live",
  "llm-provider",
  "llm-model",
  "llm-base-url",
  "llm-reasoning",
  "llm-max-tokens",
  "prompt-path",
  "calendar-mcp",
  "db",
  "llm-decisions",
  "llm-journal",
]);

/** The prefixes of the params of a language model, the ones of an LLM agent only. */
const llmParamPrefixes = ["llm-", "calendar-", "news-"];

/** Whether text has a control character, as Go tells them (unicode.IsControl). */
function hasControl(text: string): boolean {
  for (const char of text) {
    const code = char.codePointAt(0) ?? 0;
    if (code < 0x20 || (code >= 0x7f && code <= 0x9f)) return true;
  }
  return false;
}

/** Why the name of a new agent is refused, as the controller refuses it, null when taken. */
export function agentNameError(name: string): string | null {
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(name.trim())) {
    return "Use de 1 a 64 letras, dígitos, ., _ ou -, começando por letra ou dígito.";
  }
  return null;
}

/**
 * Why the address of the API of an agent is refused, as the controller refuses
 * it, null when taken: host:port, the port 1 to 65535 and the host on the
 * loopback (localhost, 127.0.0.0/8 or [::1]), as the API of an agent answers
 * whoever reaches it.
 */
export function addressError(address: string): string | null {
  const [, host = "", port = ""] =
    /^(\[[A-Za-z0-9._:-]+\]|[A-Za-z0-9._-]+):(\d+)$/.exec(address.trim()) ?? [];
  if (!(Number(port) >= 1 && Number(port) <= 65535)) {
    return "Use host:porta, com a porta de 1 a 65535, ex.: localhost:5580.";
  }
  if (!loopbackHost(host)) {
    return "Use um host do loopback (localhost, 127.0.0.1 ou [::1]): só o controller desta máquina acessa a API do agente.";
  }
  return null;
}

/** Whether host, an IPv6 address in brackets, is localhost or on the loopback. */
function loopbackHost(host: string): boolean {
  if (host.toLowerCase() === "localhost") return true;
  if (/^127(?:\.(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/.test(host)) return true;
  if (!host.startsWith("[")) return false;
  try {
    // normalized, eg: [0:0:0:0:0:0:0:1] is [::1], [::ffff:127.0.0.1] is [::ffff:7f00:1]
    const ip = new URL(`http://${host}/`).hostname;
    return ip === "[::1]" || /^\[::ffff:7f[0-9a-f]{2}:[0-9a-f]{1,4}\]$/.test(ip);
  } catch {
    return false;
  }
}

/** Whether url parses with a host. */
function hasHost(url: string): boolean {
  try {
    return new URL(url).hostname !== "";
  } catch {
    return false;
  }
}

/**
 * Why a URL of the settings of an agent is refused, as the controller refuses
 * it, null when taken: http(s), with a host, and without credentials, which go
 * to the secrets.
 */
export function httpURLError(text: string): string | null {
  const url = text.trim();
  const authority = /^https?:\/\/([^/?#]*)/i.exec(url)?.[1];
  if (!authority || hasControl(url) || !hasHost(url)) {
    return "Use uma URL que comece com http:// ou https://.";
  }
  if (authority.includes("@")) return "Sem usuário nem senha na URL: os tokens vão nos segredos.";
  return null;
}

/**
 * A whole number above 0 typed in the form, max at most: undefined when blank,
 * NaN when it isn't one.
 */
export function parseCount(text: string, max = Number.MAX_SAFE_INTEGER): number | undefined {
  const trimmed = text.trim();
  if (trimmed === "") return undefined;
  const value = /^\d+$/.test(trimmed) ? Number(trimmed) : Number.NaN;
  return value >= 1 && value <= max ? value : Number.NaN;
}

/**
 * Why the magic number of the orders of an agent is refused, null when taken:
 * above 0, or blank for a hash of their comment.
 */
export function magicError(text: string): string | null {
  return Number.isNaN(parseCount(text))
    ? "Use um número inteiro acima de 0, ou deixe em branco."
    : null;
}

/**
 * Why the tokens of an answer at most are refused, null when taken: above 0,
 * or blank for the default of the agent.
 */
export function maxTokensError(text: string): string | null {
  return Number.isNaN(parseCount(text, maxTokensLimit))
    ? "Use um número inteiro de 1 a 2.147.483.647, ou deixe em branco."
    : null;
}

/**
 * Why the comment of the orders of an agent on MT5 is refused, as the exchange
 * refuses it, null when taken: 1 to 31 printable ASCII characters not starting
 * with [, or blank for the default of the agent (nb: and its strategy).
 */
export function commentError(text: string): string | null {
  const comment = text.trim();
  if (comment.length > maxComment) return `Use até ${maxComment} caracteres.`;
  // from the space to the tilde: the printable ASCII characters
  if (!/^[ -~]*$/.test(comment)) return "Use só caracteres ASCII imprimíveis, sem acentos.";
  if (comment.startsWith("[")) return "O comentário não pode começar com [.";
  return null;
}

/** Why the model of a language model is refused, null when taken: a line of text. */
function modelError(text: string): string | null {
  const model = text.trim();
  if (model === "") return "Informe o modelo.";
  return hasControl(model) ? "Use uma linha de texto, sem caracteres de controle." : null;
}

/**
 * Why the name of a param of an agent of kind is refused, as the controller
 * refuses it, null when taken: the name of a flag without its dash, eg: pair,
 * not one a field sets, the ones of a language model (llm-, calendar- and
 * news-) for an LLM agent only.
 */
export function paramKeyError(key: string, kind: string): string | null {
  const name = key.trim();
  if (name === "") return "Informe o nome do parâmetro.";
  const bare = name.replace(/^-+/, "");
  if (bare !== name && bare !== "") return `Sem o traço: ${bare}, não ${name}.`;
  if (!/^[a-z0-9][a-z0-9-]*$/.test(name)) {
    return "Use letras minúsculas, dígitos e -, como no nome da flag.";
  }
  if (reservedParams.has(name)) {
    return `${name} é uma configuração do próprio agente, não um parâmetro.`;
  }
  if (kind !== "llm" && llmParamPrefixes.some((prefix) => name.startsWith(prefix))) {
    return "Parâmetro do modelo de linguagem: só para agentes LLM.";
  }
  return null;
}

/**
 * Why the value of the param name is refused, as the controller refuses it,
 * null when taken: a line of text of 1024 bytes at most, trimmed, not starting
 * with the name and =, eg: "lot = 0.01" for lot, the line of the param pasted
 * whole.
 */
export function paramValueError(value: string, name = ""): string | null {
  const text = value.trim();
  if (hasControl(text)) return "Use uma linha de texto, sem caracteres de controle.";
  const unnamed = text.replace(/^-/, "");
  if (
    name !== "" &&
    unnamed.startsWith(name) &&
    unnamed.slice(name.length).trim().startsWith("=")
  ) {
    return "O valor repete o nome do parâmetro: preencha só o que vem depois do =.";
  }
  if (new TextEncoder().encode(text).length > maxParamValue) {
    return `Use até ${maxParamValue} bytes.`;
  }
  return null;
}

/** A param of the form of an agent: the name of its flag, without the dash, and its value. */
export interface ParamRow {
  /** Tells the rows apart on the page. */
  id: number;
  key: string;
  value: string;
}

/**
 * The form of an agent as typed: its numbers as text, "" for none, and the
 * prompt of an LLM agent by ID, "" for the default one embedded in it. The
 * switches of its channels, its prompt and its secrets are the ones of a new
 * agent: the page changes them apart.
 */
export interface AgentFormValues {
  name: string;
  kind: string;
  mode: string;
  apiAddress: string;
  enabled: boolean;
  launched: boolean;
  restart: boolean;
  tradePanel: boolean;
  telegramControl: boolean;
  bridgeURL: string;
  magic: string;
  comment: string;
  realAllowed: boolean;
  provider: string;
  model: string;
  baseURL: string;
  reasoningEffort: string;
  maxTokens: string;
  calendarURL: string;
  promptID: string;
  llmAPIKey: string;
  bridgeToken: string;
  calendarToken: string;
  params: ParamRow[];
}

/** A field of the form of an agent, its params apart. */
export type AgentFormField = Exclude<keyof AgentFormValues, "params">;

/** Why the form of an agent is refused, by field, and its params by the ID of their row. */
export type AgentFormErrors = Partial<Record<AgentFormField, string>> & {
  params?: Record<number, string>;
};

/**
 * The form of the agent, or of a new one: in paper, disabled, launched by the
 * controller and launched again when it exits, as the controller creates one
 * by default, its language model of OpenAI.
 */
export function agentFormValues(agent?: AgentSettings): AgentFormValues {
  const account = agent?.account;
  const llm = agent?.llm;
  const params = agent?.params ?? {};
  return {
    name: agent?.name ?? "",
    kind: agent?.kind ?? "",
    mode: agent?.mode ?? "paper",
    apiAddress: agent?.api_address ?? "",
    enabled: agent?.enabled ?? false,
    launched: agent?.launched ?? true,
    restart: agent?.restart ?? true,
    tradePanel: agent?.trade_panel ?? false,
    telegramControl: agent?.telegram_control ?? false,
    bridgeURL: account?.bridge_url ?? "",
    magic: account?.magic ? String(account.magic) : "",
    comment: account?.comment ?? "",
    realAllowed: account?.real_allowed ?? false,
    // an LLM agent without its language model picks a provider
    provider: llm?.provider ?? (agent ? "" : "openai"),
    model: llm?.model ?? "",
    baseURL: llm?.base_url ?? "",
    reasoningEffort: llm?.reasoning_effort ?? "",
    maxTokens: llm?.max_tokens ? String(llm.max_tokens) : "",
    calendarURL: llm?.calendar_url ?? "",
    promptID: agent?.prompt_id ? String(agent.prompt_id) : "",
    llmAPIKey: "",
    bridgeToken: "",
    calendarToken: "",
    params: Object.keys(params)
      .sort()
      .map((key, id) => ({ id, key, value: params[key] ?? "" })),
  };
}

/** Whether a row of the params is blank, left out. */
function blankRow(row: ParamRow): boolean {
  return row.key.trim() === "" && row.value.trim() === "";
}

/** The params of the form by name, trimmed, the blank rows left out. */
export function formParams(rows: ParamRow[]): Record<string, string> {
  return Object.fromEntries(
    rows
      .filter((row) => !blankRow(row))
      .map((row): [string, string] => [row.key.trim(), row.value.trim()]),
  );
}

/**
 * Why the form of an agent is refused, as the controller refuses it: its name
 * and its kind, of a new agent only, its settings, its account, its language
 * model, of an LLM agent, and its params, a name repeated refused. {} when it
 * is taken.
 */
export function agentFormErrors(values: AgentFormValues, creating: boolean): AgentFormErrors {
  const errors: AgentFormErrors = {};
  const check = (field: AgentFormField, error: string | null) => {
    if (error) errors[field] = error;
  };
  const blank = (text: string) => text.trim() === "";
  if (creating) {
    check("name", agentNameError(values.name));
    check("kind", agentKinds.includes(values.kind) ? null : "Escolha o tipo do agente.");
  }
  check("mode", agentModes.includes(values.mode) ? null : "Escolha o modo.");
  check(
    "apiAddress",
    blank(values.apiAddress) ? "Informe o endereço da API." : addressError(values.apiAddress),
  );
  check(
    "bridgeURL",
    blank(values.bridgeURL) ? "Informe a URL da bridge." : httpURLError(values.bridgeURL),
  );
  check("magic", magicError(values.magic));
  check("comment", commentError(values.comment));
  if (values.kind === "llm") {
    check("provider", llmProviders.includes(values.provider) ? null : "Escolha o provedor.");
    check("model", modelError(values.model));
    check("baseURL", blank(values.baseURL) ? null : httpURLError(values.baseURL));
    check("maxTokens", maxTokensError(values.maxTokens));
    check("calendarURL", blank(values.calendarURL) ? null : httpURLError(values.calendarURL));
  }
  const params: Record<number, string> = {};
  const seen = new Set<string>();
  for (const row of values.params) {
    if (blankRow(row)) continue;
    const key = row.key.trim();
    const error =
      paramKeyError(key, values.kind) ??
      paramValueError(row.value, row.key.trim()) ??
      (seen.has(key) ? "Parâmetro repetido." : null);
    if (error) params[row.id] = error;
    seen.add(key);
  }
  if (Object.keys(params).length > 0) errors.params = params;
  return errors;
}

/** text trimmed, undefined when blank: an optional field left out. */
function filled(text: string): string | undefined {
  return text.trim() || undefined;
}

/**
 * The request creating the agent of the form, taken (see agentFormErrors):
 * its optional fields left blank left out, and its language model, its prompt
 * and their secrets for an LLM agent only.
 */
export function agentCreateRequest(values: AgentFormValues): AgentCreateRequest {
  const request: AgentCreateRequest = {
    name: values.name.trim(),
    kind: values.kind,
    mode: values.mode,
    api_address: values.apiAddress.trim(),
    enabled: values.enabled,
    launched: values.launched,
    restart: values.restart,
    trade_panel: values.tradePanel,
    telegram_control: values.telegramControl,
    params: formParams(values.params),
    account: {
      bridge_url: values.bridgeURL.trim(),
      magic: parseCount(values.magic),
      comment: filled(values.comment),
      real_allowed: values.realAllowed,
    },
    bridge_token: filled(values.bridgeToken),
  };
  if (values.kind === "llm") {
    request.llm = {
      provider: values.provider,
      model: values.model.trim(),
      base_url: filled(values.baseURL),
      reasoning_effort: values.reasoningEffort || undefined,
      max_tokens: parseCount(values.maxTokens),
      calendar_url: filled(values.calendarURL),
    };
    request.prompt_id = values.promptID ? Number(values.promptID) : undefined;
    request.llm_api_key = filled(values.llmAPIKey);
    request.calendar_token = filled(values.calendarToken);
  }
  return request;
}

/** Whether two sets of params are the same. */
function sameParams(a: Record<string, string>, b: Record<string, string>): boolean {
  const keys = Object.keys(a);
  return keys.length === Object.keys(b).length && keys.every((key) => b[key] === a[key]);
}

/**
 * The request changing the agent of the form, taken (see agentFormErrors),
 * from initial, the form as it was opened: of the fields changed only, so that
 * a change made elsewhere meanwhile is kept, null when none. An optional field
 * emptied is removed ("" or 0, its default applying), and the params, one of
 * them changed, replace them all.
 */
export function agentUpdateRequest(
  initial: AgentFormValues,
  values: AgentFormValues,
): AgentSettingsRequest | null {
  const changed = (field: AgentFormField) => {
    const before = initial[field];
    const after = values[field];
    return typeof before === "string" && typeof after === "string"
      ? before.trim() !== after.trim()
      : before !== after;
  };
  const request: AgentSettingsRequest = {};
  if (changed("mode")) request.mode = values.mode;
  if (changed("apiAddress")) request.api_address = values.apiAddress.trim();
  if (changed("launched")) request.launched = values.launched;
  if (changed("restart")) request.restart = values.restart;

  const account: AgentAccountRequest = {};
  if (changed("bridgeURL")) account.bridge_url = values.bridgeURL.trim();
  if (changed("magic")) account.magic = parseCount(values.magic) ?? 0;
  if (changed("comment")) account.comment = values.comment.trim();
  if (changed("realAllowed")) account.real_allowed = values.realAllowed;
  if (Object.keys(account).length > 0) request.account = account;

  if (values.kind === "llm") {
    const llm: AgentLLMRequest = {};
    if (changed("provider")) llm.provider = values.provider;
    if (changed("model")) llm.model = values.model.trim();
    if (changed("baseURL")) llm.base_url = values.baseURL.trim();
    if (changed("reasoningEffort")) llm.reasoning_effort = values.reasoningEffort;
    if (changed("maxTokens")) llm.max_tokens = parseCount(values.maxTokens) ?? 0;
    if (changed("calendarURL")) llm.calendar_url = values.calendarURL.trim();
    if (Object.keys(llm).length > 0) request.llm = llm;
  }

  const params = formParams(values.params);
  if (!sameParams(formParams(initial.params), params)) request.params = params;
  return Object.keys(request).length > 0 ? request : null;
}
