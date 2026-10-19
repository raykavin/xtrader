import { liveStream } from "../lib/events";
import { csrfToken } from "../lib/settings";
import { createSSEParser } from "../lib/sse";
import { APIError, retryAfterSeconds } from "./errors";
import type {
  AgentCreateRequest,
  AgentPeriod,
  AgentPeriodRequest,
  AgentPeriodsResponse,
  AgentRenameRequest,
  AgentSettings,
  AgentSettingsRequest,
  AgentSettingsResponse,
  AgentStatus,
  AgentsResponse,
  CandleEvent,
  CandlesResponse,
  ClosedProfitResponse,
  ControlMoveRequest,
  ControlOrderRequest,
  ControlOrderResponse,
  ControlsResponse,
  Decision,
  DecisionEvent,
  DecisionsResponse,
  LoginRequest,
  OpenPositionsResponse,
  Order,
  OrderEvent,
  PairsResponse,
  PromptDetail,
  PromptInfo,
  PromptRequest,
  PromptsResponse,
  ReanalysisRequest,
  ReanalysisResponse,
  SessionResponse,
  Snapshot,
  SupervisorActionInfo,
  SupervisorActionsResponse,
  SupervisorEntriesRequest,
  SupervisorMessageInfo,
  SupervisorMessageRequest,
  SupervisorMessagesResponse,
  SupervisorProtectionInfo,
  SupervisorProtectionsResponse,
  SupervisorRunResponse,
  SupervisorRunsResponse,
  SupervisorSettingsRequest,
  SupervisorSettingsResponse,
  SupervisorSnapshot,
  SupervisorStreamError,
  SupervisorStreamStage,
  SupervisorStreamText,
  TelegramSettingsRequest,
  TelegramSettingsResponse,
  TelegramUserRequest,
  UserInfo,
  UserRequest,
  UsersResponse,
} from "./types";

export { APIError };

declare global {
  interface Window {
    __XTRADER__?: { version?: string; api?: string };
  }
}

export const API_BASE = window.__XTRADER__?.api ?? "/api";

/** The page of the login, back to the page of now once logged in. */
export function loginPath(): string {
  const here = window.location.pathname + window.location.search;
  return here.startsWith("/login") ? "/login" : `/login?next=${encodeURIComponent(here)}`;
}

/** The methods that change something, which carry the CSRF token of the session. */
const unsafeMethods = ["POST", "PUT", "PATCH", "DELETE"];

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  const token = csrfToken(document.cookie);
  if (token && unsafeMethods.includes((init?.method ?? "GET").toUpperCase())) {
    headers.set("X-CSRF-Token", token);
  }
  const response = await fetch(`${API_BASE}${path}`, { ...init, headers });
  if (response.status === 401 && !path.startsWith("/auth/")) {
    // the session expired, or was closed: back to the login
    window.location.assign(loginPath());
  }
  const text = await response.text();
  if (!response.ok) throw failureOf(path, response, text);
  return (text ? JSON.parse(text) : undefined) as T; // none for 204
}

/** The error of an answer of the API refused: its message, else the status. */
function failureOf(path: string, response: Response, text: string): APIError {
  let message = `${path}: ${response.status} ${response.statusText}`;
  try {
    const parsed = JSON.parse(text) as { error?: string };
    if (parsed.error) message = parsed.error;
  } catch {
    // not JSON; keep the generic message
  }
  return new APIError(
    message,
    response.status,
    retryAfterSeconds(response.headers.get("Retry-After")),
  );
}

function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  return request<T>(path, { signal });
}

function sendJSON<T>(method: string, path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

function postJSON<T>(path: string, body?: unknown): Promise<T> {
  return sendJSON<T>("POST", path, body);
}

/** Whether the browser holds a session of the dashboard; 503 without users. */
export function fetchSession(signal?: AbortSignal): Promise<boolean> {
  return getJSON<SessionResponse>("/auth/session", signal).then((r) => r.authenticated);
}

/** The session of the browser, with its user. */
export function fetchSessionInfo(signal?: AbortSignal): Promise<SessionResponse> {
  return getJSON<SessionResponse>("/auth/session", signal);
}

/** Logs in with the credentials of the controller: the session is a cookie. */
export function login(username: string, password: string): Promise<SessionResponse> {
  const request: LoginRequest = { username, password };
  return postJSON<SessionResponse>("/auth/login", request);
}

/** Closes the session of the browser. */
export function logout(): Promise<SessionResponse> {
  return postJSON<SessionResponse>("/auth/logout");
}

/** The agents of the dashboard, with whether their API answers. */
export function fetchAgents(signal?: AbortSignal): Promise<AgentStatus[]> {
  return getJSON<AgentsResponse>("/agents", signal).then((r) => r.agents);
}

/**
 * The open profit of all the agents, the positions they hold open now on their
 * accounts, whichever agent the dashboard shows.
 */
export function fetchOpenPositions(signal?: AbortSignal): Promise<OpenPositionsResponse> {
  return getJSON<OpenPositionsResponse>("/positions/open", signal);
}

/**
 * The closed profit of all the agents from until before to (Unix seconds):
 * what their accounts booked for the positions they closed then.
 */
export function fetchClosedProfit(
  from: number,
  to: number,
  signal?: AbortSignal,
): Promise<ClosedProfitResponse> {
  return getJSON<ClosedProfitResponse>(`/positions/closed?from=${from}&to=${to}`, signal);
}

/** The path of the API of an agent, which the dashboard proxies. */
function agentPath(agent: string, path: string): string {
  return `/agents/${encodeURIComponent(agent)}${path}`;
}

export function fetchPairs(agent: string, signal?: AbortSignal): Promise<string[]> {
  return getJSON<PairsResponse>(agentPath(agent, "/pairs"), signal).then((r) => r.pairs);
}

export function fetchSnapshot(
  agent: string,
  pair: string,
  signal?: AbortSignal,
): Promise<Snapshot> {
  return getJSON<Snapshot>(agentPath(agent, `/${encodeURIComponent(pair)}/snapshot`), signal);
}

/**
 * Fetches the candles of pair on timeframe, the last limit of them (the
 * agent's default when 0): the ones of the strategy on its own timeframe,
 * else read or built by the agent for the chart alone.
 */
export function fetchCandles(
  agent: string,
  pair: string,
  timeframe: string,
  limit = 0,
  signal?: AbortSignal,
): Promise<CandlesResponse> {
  const query = new URLSearchParams({ timeframe });
  if (limit > 0) query.set("limit", String(limit));
  return getJSON<CandlesResponse>(
    agentPath(agent, `/${encodeURIComponent(pair)}/candles?${query}`),
    signal,
  );
}

export function ordersCSVUrl(agent: string, pair: string): string {
  return `${API_BASE}${agentPath(agent, `/${encodeURIComponent(pair)}/orders.csv`)}`;
}

export function fetchControls(agent: string, signal?: AbortSignal): Promise<ControlsResponse> {
  return getJSON<ControlsResponse>(agentPath(agent, "/controls"), signal);
}

export function startBot(agent: string): Promise<ControlsResponse> {
  return postJSON<ControlsResponse>(agentPath(agent, "/controls/start"));
}

export function stopBot(agent: string): Promise<ControlsResponse> {
  return postJSON<ControlsResponse>(agentPath(agent, "/controls/stop"));
}

/** Creates an order of the ticket: its warning tells of one created short of what was asked. */
export function createOrder(
  agent: string,
  request: ControlOrderRequest,
): Promise<ControlOrderResponse> {
  return postJSON<ControlOrderResponse>(agentPath(agent, "/controls/order"), request);
}

/** Moves a pending order: the price of a limit order, or a stop loss or take profit. */
export function moveOrder(agent: string, id: number, price: number): Promise<Order> {
  const request: ControlMoveRequest = { price };
  return postJSON<Order>(agentPath(agent, `/controls/orders/${id}/move`), request);
}

/** Cancels a pending order, with the legs of its group. */
export function cancelOrder(agent: string, id: number): Promise<Order> {
  return postJSON<Order>(agentPath(agent, `/controls/orders/${id}/cancel`));
}

/** The last decisions of the language model of the strategy on pair, newest first. */
export function fetchDecisions(
  agent: string,
  pair: string,
  limit: number,
  signal?: AbortSignal,
): Promise<Decision[]> {
  const query = `?pair=${encodeURIComponent(pair)}&limit=${limit}`;
  return getJSON<DecisionsResponse>(agentPath(agent, `/decisions${query}`), signal).then(
    (r) => r.decisions,
  );
}

/** A decision of the language model, with its justification. */
export function fetchDecision(agent: string, id: string, signal?: AbortSignal): Promise<Decision> {
  return getJSON<Decision>(agentPath(agent, `/decisions/${encodeURIComponent(id)}`), signal);
}

/** The address of the content of an attachment of a decision, eg: an image. */
export function decisionAttachmentUrl(agent: string, id: string, index: number): string {
  return `${API_BASE}${agentPath(agent, `/decisions/${encodeURIComponent(id)}/attachments/${index}`)}`;
}

/**
 * Asks the language model of the strategy of the agent again about pair now,
 * with the note of the trader for it, if any: blank, none is sent.
 */
export function reanalyze(agent: string, pair: string, note = ""): Promise<ReanalysisResponse> {
  const request: ReanalysisRequest = { pair };
  const text = note.trim();
  if (text) request.note = text;
  return postJSON<ReanalysisResponse>(agentPath(agent, "/controls/reanalyze"), request);
}

export interface EventHandlers {
  onCandle?: (event: CandleEvent) => void;
  onOrder?: (event: OrderEvent) => void;
  onControls?: (event: ControlsResponse) => void;
  onDecision?: (event: DecisionEvent) => void;
  onStatus?: (connected: boolean) => void;
  /** Connected again after a drop: what the events of the meantime changed is to be loaded. */
  onResync?: () => void;
}

/**
 * Subscribes to the Server-Sent Events stream of an agent, opened again when
 * it gives up (see liveStream): the login follows once the session is gone.
 * The returned function closes the connection.
 */
export function subscribeEvents(agent: string, handlers: EventHandlers): () => void {
  return liveStream({
    open: () => {
      const source = new EventSource(`${API_BASE}${agentPath(agent, "/events")}`);
      source.addEventListener("candle", (e) => {
        handlers.onCandle?.(JSON.parse((e as MessageEvent<string>).data) as CandleEvent);
      });
      source.addEventListener("order", (e) => {
        handlers.onOrder?.(JSON.parse((e as MessageEvent<string>).data) as OrderEvent);
      });
      source.addEventListener("controls", (e) => {
        handlers.onControls?.(JSON.parse((e as MessageEvent<string>).data) as ControlsResponse);
      });
      source.addEventListener("decision", (e) => {
        handlers.onDecision?.(JSON.parse((e as MessageEvent<string>).data) as DecisionEvent);
      });
      return source;
    },
    onStatus: handlers.onStatus,
    onResync: handlers.onResync,
    session: fetchSession,
    onLoggedOut: () => window.location.assign(loginPath()),
  });
}

/** Every agent of the controller in its settings, the disabled ones included. */
export function fetchAgentSettings(signal?: AbortSignal): Promise<AgentSettings[]> {
  return getJSON<AgentSettingsResponse>("/settings/agents", signal).then((r) => r.agents);
}

/** Creates an agent, launched at once when enabled: 409 when its name or address is taken. */
export function createAgent(request: AgentCreateRequest): Promise<AgentSettings> {
  return postJSON<AgentSettings>("/settings/agents", request);
}

/**
 * Changes an agent, of the fields set: its switches, its settings, its account,
 * its language model, its params, its prompt and its secrets (write only).
 */
export function updateAgentSettings(
  name: string,
  request: AgentSettingsRequest,
): Promise<AgentSettings> {
  return sendJSON<AgentSettings>("PATCH", `/settings/agents/${encodeURIComponent(name)}`, request);
}

/** Deletes an agent disabled and stopped: its orders and its decisions are kept. */
export function deleteAgent(name: string): Promise<void> {
  return sendJSON<void>("DELETE", `/settings/agents/${encodeURIComponent(name)}`);
}

/**
 * Renames an agent disabled and stopped: its ID, its account, its orders and
 * its decisions stay its own, its outbox renamed with it.
 */
export function renameAgent(name: string, newName: string): Promise<AgentSettings> {
  const request: AgentRenameRequest = { name: newName };
  return postJSON<AgentSettings>(`/settings/agents/${encodeURIComponent(name)}/rename`, request);
}

/** Restarts an agent, eg: to apply the settings waiting for it. */
export function restartAgent(name: string): Promise<AgentSettings> {
  return postJSON<AgentSettings>(`/settings/agents/${encodeURIComponent(name)}/restart`);
}

/** The accounting periods of an agent, the current one first: none before its first reset. */
export function fetchAgentPeriods(name: string, signal?: AbortSignal): Promise<AgentPeriod[]> {
  return getJSON<AgentPeriodsResponse>(
    `/settings/agents/${encodeURIComponent(name)}/periods`,
    signal,
  ).then((r) => r.periods);
}

/**
 * Resets the results of every pair of the agent: a period starting now, its
 * deposit the balance it starts with. The orders and the positions don't change.
 */
export function resetAgentResults(name: string, request: AgentPeriodRequest): Promise<AgentPeriod> {
  return postJSON<AgentPeriod>(`/settings/agents/${encodeURIComponent(name)}/periods`, request);
}

/** The prompts of the LLM agents, without their content, by name. */
export function fetchPrompts(signal?: AbortSignal): Promise<PromptInfo[]> {
  return getJSON<PromptsResponse>("/prompts", signal).then((r) => r.prompts);
}

/** A prompt with its content. */
export function fetchPrompt(id: number, signal?: AbortSignal): Promise<PromptDetail> {
  return getJSON<PromptDetail>(`/prompts/${id}`, signal);
}

export function createPrompt(request: PromptRequest): Promise<PromptDetail> {
  return postJSON<PromptDetail>("/prompts", request);
}

/** Edits a prompt, refused (409) when it was edited since request.version. */
export function updatePrompt(id: number, request: PromptRequest): Promise<PromptDetail> {
  return sendJSON<PromptDetail>("PUT", `/prompts/${id}`, request);
}

/** Deletes a prompt no agent uses. */
export function deletePrompt(id: number): Promise<void> {
  return sendJSON<void>("DELETE", `/prompts/${id}`);
}

export function fetchTelegramSettings(signal?: AbortSignal): Promise<TelegramSettingsResponse> {
  return getJSON<TelegramSettingsResponse>("/settings/telegram", signal);
}

/** Changes the Telegram bot: on or off, and its token (write only). */
export function updateTelegramSettings(
  request: TelegramSettingsRequest,
): Promise<TelegramSettingsResponse> {
  return sendJSON<TelegramSettingsResponse>("PATCH", "/settings/telegram", request);
}

/** Authorizes a Telegram user. */
export function addTelegramUser(request: TelegramUserRequest): Promise<TelegramSettingsResponse> {
  return postJSON<TelegramSettingsResponse>("/settings/telegram/users", request);
}

/** Stops answering a Telegram user, of the ID of its record. */
export function deleteTelegramUser(id: number): Promise<TelegramSettingsResponse> {
  return sendJSON<TelegramSettingsResponse>("DELETE", `/settings/telegram/users/${id}`);
}

/** The users of the dashboard, by username. */
export function fetchUsers(signal?: AbortSignal): Promise<UserInfo[]> {
  return getJSON<UsersResponse>("/users", signal).then((r) => r.users);
}

export function createUser(request: UserRequest): Promise<UserInfo> {
  return postJSON<UserInfo>("/users", request);
}

/** Changes a user: its password, or enabled. */
export function updateUser(id: number, request: UserRequest): Promise<UserInfo> {
  return sendJSON<UserInfo>("PATCH", `/users/${id}`, request);
}

/** Deletes a user, never the last one enabled. */
export function deleteUser(id: number): Promise<void> {
  return sendJSON<void>("DELETE", `/users/${id}`);
}

/** The settings of the supervisor of the agents and its state, never its key. */
export function fetchSupervisorSettings(signal?: AbortSignal): Promise<SupervisorSettingsResponse> {
  return getJSON<SupervisorSettingsResponse>("/settings/supervisor", signal);
}

/** Changes the settings of the supervisor, the fields set. */
export function updateSupervisorSettings(
  request: SupervisorSettingsRequest,
): Promise<SupervisorSettingsResponse> {
  return sendJSON<SupervisorSettingsResponse>("PATCH", "/settings/supervisor", request);
}

/** What the supervisor knows of the agents now, as its model gets it. */
export function fetchSupervisorOverview(signal?: AbortSignal): Promise<SupervisorSnapshot> {
  return getJSON<SupervisorSnapshot>("/supervisor/overview", signal);
}

/** Starts an analysis of the supervisor now: the list of the analyses tells how it went. */
export function runSupervisor(): Promise<{ started: boolean }> {
  return postJSON<{ started: boolean }>("/supervisor/run");
}

/** The last analyses of the supervisor, the newest first. */
export function fetchSupervisorRuns(signal?: AbortSignal): Promise<SupervisorRunsResponse> {
  return getJSON<SupervisorRunsResponse>("/supervisor/runs", signal);
}

/** An analysis of the supervisor and the data sent to its model. */
export function fetchSupervisorRun(
  id: number,
  signal?: AbortSignal,
): Promise<SupervisorRunResponse> {
  return getJSON<SupervisorRunResponse>(`/supervisor/runs/${id}`, signal);
}

/** The last actions of the supervision on the agents, the newest first. */
export function fetchSupervisorActions(signal?: AbortSignal): Promise<SupervisorActionInfo[]> {
  return getJSON<SupervisorActionsResponse>("/supervisor/actions", signal).then((r) => r.actions);
}

/** The protections of positions the XTrader AI armed, the newest first. */
export function fetchSupervisorProtections(
  signal?: AbortSignal,
): Promise<SupervisorProtectionInfo[]> {
  return getJSON<SupervisorProtectionsResponse>("/supervisor/protections", signal).then(
    (r) => r.protections,
  );
}

/** The answer of a confirmation or a rejection: the action and the message telling it. */
export interface SupervisorDecision {
  action: SupervisorActionInfo;
  message?: SupervisorMessageInfo;
}

/** Executes a proposal of the supervisor, confirmed by the user: its result, checked. */
export function confirmSupervisorAction(id: number): Promise<SupervisorDecision> {
  return postJSON<SupervisorDecision>(`/supervisor/actions/${id}/confirm`);
}

/** Rejects a proposal of the supervisor. */
export function rejectSupervisorAction(id: number): Promise<SupervisorDecision> {
  return postJSON<SupervisorDecision>(`/supervisor/actions/${id}/reject`);
}

/** The conversation of the user with the supervisor, the last messages, or the ones before. */
export function fetchSupervisorMessages(
  before?: number,
  signal?: AbortSignal,
): Promise<SupervisorMessagesResponse> {
  const query = before ? `?before=${before}` : "";
  return getJSON<SupervisorMessagesResponse>(`/supervisor/messages${query}`, signal);
}

/** Sends a message to the supervisor: the messages it added, its answer and proposals. */
export function sendSupervisorMessage(text: string): Promise<SupervisorMessagesResponse> {
  const request: SupervisorMessageRequest = { text };
  return postJSON<SupervisorMessagesResponse>("/supervisor/messages", request);
}

/** What an answer of the XTrader AI tells as it streams. */
export interface SupervisorStreamHandlers {
  /** A message kept: the question, then each proposal as the model makes it. */
  onMessage?: (message: SupervisorMessageInfo) => void;
  /** Where the answer is; the text of the stage before is dropped. */
  onStage?: (stage: SupervisorStreamStage) => void;
  /** A piece of the text the model writes, in Markdown. */
  onText?: (piece: string) => void;
}

/**
 * Sends a message to the XTrader AI and follows its answer as the server
 * streams it (POST /supervisor/messages/stream), telling handlers what comes:
 * the messages it added once done, as sendSupervisorMessage answers them. A
 * stream cut before its end fails: what the server kept is read again.
 */
export async function streamSupervisorMessage(
  text: string,
  handlers: SupervisorStreamHandlers,
  signal?: AbortSignal,
): Promise<SupervisorMessagesResponse> {
  const path = "/supervisor/messages/stream";
  const headers = new Headers({ "Content-Type": "application/json", Accept: "text/event-stream" });
  const token = csrfToken(document.cookie);
  if (token) headers.set("X-CSRF-Token", token);
  const request: SupervisorMessageRequest = { text };
  const response = await fetch(`${API_BASE}${path}`, {
    method: "POST",
    headers,
    body: JSON.stringify(request),
    signal,
  });
  if (response.status === 401) window.location.assign(loginPath());
  if (!response.ok || !response.body) throw failureOf(path, response, await response.text());

  let done: SupervisorMessagesResponse | null = null;
  let failure: string | null = null;
  const parser = createSSEParser(({ event, data }) => {
    let payload: unknown;
    try {
      payload = JSON.parse(data);
    } catch {
      return; // not an event of the answer
    }
    switch (event) {
      case "message":
        handlers.onMessage?.(payload as SupervisorMessageInfo);
        break;
      case "stage":
        handlers.onStage?.(payload as SupervisorStreamStage);
        break;
      case "text":
        handlers.onText?.((payload as SupervisorStreamText).text);
        break;
      case "error":
        failure = (payload as SupervisorStreamError).error;
        break;
      case "done":
        done = payload as SupervisorMessagesResponse;
        break;
    }
  });
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  for (;;) {
    const { value, done: ended } = await reader.read();
    if (ended) break;
    parser.push(decoder.decode(value, { stream: true }));
  }
  parser.push(decoder.decode());
  parser.end();
  if (failure !== null) throw new APIError(failure, response.status);
  if (done === null)
    throw new APIError("A resposta foi interrompida antes do fim.", response.status);
  return done;
}

/** Blocks the entries of an agent, or allows them again, by the user. */
export function setAgentEntries(
  name: string,
  request: SupervisorEntriesRequest,
): Promise<SupervisorDecision> {
  return postJSON<SupervisorDecision>(
    `/supervisor/agents/${encodeURIComponent(name)}/entries`,
    request,
  );
}
