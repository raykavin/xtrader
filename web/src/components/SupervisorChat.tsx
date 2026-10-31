import {
  type FormEvent,
  type KeyboardEvent,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  fetchSupervisorMessages,
  fetchSupervisorRuns,
  fetchSupervisorSettings,
  runSupervisor,
  type SupervisorDecision,
  streamSupervisorMessage,
} from "../api/client";
import type {
  SupervisorMessageInfo,
  SupervisorRunInfo,
  SupervisorSettingsResponse,
  SupervisorStreamStage,
} from "../api/types";
import { settingsHref } from "../lib/nav";
import { awaitingDecision, mergeMessages, stageLabel, withAction } from "../lib/supervisor";
import { Icon } from "./Icon";
import { Markdown } from "./Markdown";
import { Alert } from "./States";
import { clock, SupervisorActionCard, toneClass } from "./SupervisorAction";
import { SupervisorRuns } from "./SupervisorRuns";

type PanelTab = "conversa" | "analises";

/** How often the open panel reads the analyses again while one runs, in ms. */
const RUNS_POLL = 3000;

/** The longest message the supervisor takes, as the server. */
const MAX_MESSAGE = 4000;

/**
 * The XTrader AI, the supervisor of the agents, at hand on every page: a
 * button opens a panel beside the page (the whole screen on a phone) with the
 * conversation of the user with it, kept on the server, and its analyses. Its
 * answers stream as the model writes them, in Markdown, an animation telling
 * what it does until the text comes; an action it proposes shows as a card
 * the user confirms or rejects; the result of one confirmed comes back
 * checked on the account.
 */
export function SupervisorChat() {
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<PanelTab>("conversa");
  const [settings, setSettings] = useState<SupervisorSettingsResponse | null>(null);
  const [messages, setMessages] = useState<SupervisorMessageInfo[] | null>(null);
  const [more, setMore] = useState(false);
  const [runs, setRuns] = useState<SupervisorRunInfo[] | null>(null);
  const [text, setText] = useState("");
  const [sending, setSending] = useState<string | null>(null);
  // the answer streaming: its question kept, its stage, the text of the stage
  const [asked, setAsked] = useState(false);
  const [stage, setStage] = useState<SupervisorStreamStage | null>(null);
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);
  const streaming = useRef<AbortController | null>(null);
  const panel = useRef<HTMLElement>(null);
  const input = useRef<HTMLTextAreaElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const toggle = useRef<HTMLButtonElement>(null);

  const fail = useCallback(
    (e: unknown) => setError(e instanceof Error ? e.message : String(e)),
    [],
  );
  const loadMessages = useCallback(() => {
    fetchSupervisorMessages()
      .then((response) => {
        setMessages(response.messages);
        setMore(Boolean(response.more));
      })
      .catch(fail);
  }, [fail]);
  const loadRuns = useCallback(() => {
    fetchSupervisorRuns()
      .then((response) => setRuns(response.runs))
      .catch(fail);
  }, [fail]);

  // opened: the state of the supervisor and the conversation, the focus in it
  useEffect(() => {
    if (!open) return;
    fetchSupervisorSettings().then(setSettings).catch(fail);
    loadMessages();
    // the focus in the message where a keyboard is at hand: on a phone it would open its keyboard
    if (window.matchMedia?.("(pointer: fine)").matches)
      window.setTimeout(() => input.current?.focus(), 0);
  }, [open, fail, loadMessages]);
  useEffect(() => {
    if (open && tab === "analises") loadRuns();
  }, [open, tab, loadRuns]);
  // an analysis running: read again until it ends
  const running = runs?.some((run) => run.status === "running") || settings?.analyzing;
  useEffect(() => {
    if (!open || tab !== "analises" || !running) return;
    const timer = window.setInterval(() => {
      loadRuns();
      fetchSupervisorSettings().then(setSettings).catch(fail);
    }, RUNS_POLL);
    return () => window.clearInterval(timer);
  }, [open, tab, running, loadRuns, fail]);

  // the last message in view, the text streaming too
  const count = (messages?.length ?? 0) + (sending ? 1 : 0);
  useEffect(() => {
    if (count > 0 || draft) list.current?.scrollTo({ top: list.current.scrollHeight });
  }, [count, draft]);
  // a page left: the stream stops, the server keeps the answer
  useEffect(() => () => streaming.current?.abort(), []);

  const close = () => {
    setOpen(false);
    toggle.current?.focus();
  };
  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key === "Escape") close();
  };

  const send = async (event?: FormEvent) => {
    event?.preventDefault();
    const message = text.trim();
    if (!message || sending) return;
    setSending(message);
    setAsked(false);
    setStage(null);
    setDraft("");
    setText("");
    setError(null);
    const controller = new AbortController();
    streaming.current = controller;
    const keep = (kept: SupervisorMessageInfo[]) =>
      setMessages((current) => mergeMessages(current ?? [], kept));
    try {
      const response = await streamSupervisorMessage(
        message,
        {
          onMessage: (m) => {
            if (m.kind === "question") setAsked(true);
            keep([m]);
          },
          onStage: (next) => {
            setStage(next);
            setDraft(""); // the text before a tool isn't the answer
          },
          onText: (piece) => setDraft((current) => current + piece),
        },
        controller.signal,
      );
      keep(response.messages);
    } catch (e) {
      if (!controller.signal.aborted) {
        fail(e);
        loadMessages(); // what the server kept, eg: the question, the answer after a drop of the network
      }
    } finally {
      streaming.current = null;
      setSending(null);
      setStage(null);
      setDraft("");
      input.current?.focus();
    }
  };
  const onComposerKey = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      void send();
    }
  };
  const decided = (decision: SupervisorDecision) => {
    setMessages((current) => {
      const updated = withAction(current ?? [], decision.action);
      return decision.message ? mergeMessages(updated, [decision.message]) : updated;
    });
  };
  const older = () => {
    const first = messages?.[0];
    if (!first) return;
    fetchSupervisorMessages(first.id)
      .then((response) => {
        setMessages((current) => mergeMessages(current ?? [], response.messages));
        setMore(Boolean(response.more));
      })
      .catch(fail);
  };
  const analyze = async () => {
    setError(null);
    try {
      await runSupervisor();
      setSettings((s) => (s ? { ...s, analyzing: true } : s));
      window.setTimeout(loadRuns, 500);
    } catch (e) {
      fail(e);
    }
  };

  const now = Math.floor(Date.now() / 1000);
  const waiting = messages?.filter((m) => m.action && awaitingDecision(m.action, now)).length ?? 0;
  const ready = settings
    ? settings.configured && settings.api_key_set && Boolean(settings.model)
    : true;

  return (
    <>
      <button
        ref={toggle}
        type="button"
        className="supervisor-fab"
        aria-expanded={open}
        aria-controls="supervisor-panel"
        aria-label={open ? "Fechar o XTrader AI" : "Abrir o XTrader AI"}
        title="XTrader AI, o supervisor dos agentes"
        onClick={() => (open ? close() : setOpen(true))}
      >
        <Icon name={open ? "close" : "shield"} size={20} />
        <span className="supervisor-fab-label">XTrader AI</span>
        {waiting > 0 && !open && (
          <span className="supervisor-fab-badge" title={`${waiting} proposta(s) aguardando`}>
            {waiting}
          </span>
        )}
      </button>
      {open && (
        <section
          ref={panel}
          id="supervisor-panel"
          className="supervisor-panel"
          role="dialog"
          aria-label="XTrader AI"
          onKeyDown={onKeyDown}
        >
          <header className="supervisor-head">
            <div className="supervisor-title">
              <Icon name="shield" size={18} />
              <div>
                <strong>XTrader AI</strong>
                <span className="supervisor-sub">
                  {settings === null
                    ? "carregando…"
                    : !ready
                      ? "sem modelo configurado"
                      : settings.enabled
                        ? `análises a cada ${Math.round(settings.interval_seconds / 60)} min`
                        : "análises periódicas desligadas"}
                </span>
              </div>
            </div>
            <a
              className="btn btn-ghost btn-icon"
              href={settingsHref("supervisor")}
              title="Configurações do XTrader AI"
            >
              <Icon name="settings" size={16} />
            </a>
            <button
              type="button"
              className="btn btn-ghost btn-icon"
              onClick={close}
              aria-label="Fechar"
            >
              <Icon name="close" size={16} />
            </button>
          </header>
          <div className="supervisor-tabs" role="tablist" aria-label="Seções do XTrader AI">
            {(
              [
                ["conversa", "Conversa"],
                ["analises", "Análises"],
              ] as const
            ).map(([id, label]) => (
              <button
                key={id}
                type="button"
                role="tab"
                aria-selected={tab === id}
                className={tab === id ? "supervisor-tab active" : "supervisor-tab"}
                onClick={() => setTab(id)}
              >
                {label}
                {id === "conversa" && waiting > 0 && <span className="nav-hint on">{waiting}</span>}
              </button>
            ))}
          </div>
          {error && (
            <Alert tone="error" className="supervisor-alert">
              {error}
            </Alert>
          )}
          {!ready && (
            <Alert tone="warn" className="supervisor-alert">
              Defina o provedor, o modelo e a chave em{" "}
              <a href={settingsHref("supervisor")}>Configurações → XTrader AI</a>.
            </Alert>
          )}

          {tab === "conversa" ? (
            <>
              <div ref={list} className="supervisor-messages" aria-live="polite">
                {more && (
                  <button
                    type="button"
                    className="btn btn-sm btn-ghost supervisor-older"
                    onClick={older}
                  >
                    Mensagens anteriores
                  </button>
                )}
                {messages === null ? (
                  <p className="supervisor-empty">Carregando a conversa…</p>
                ) : messages.length === 0 && !sending ? (
                  <div className="supervisor-empty">
                    <p>Pergunte sobre os agentes, peça uma análise ou uma ação de controle.</p>
                    <p className="field-hint">
                      Ex.: "Quais agentes estão perdendo hoje?", "Bloqueie as entradas do orb",
                      "Encerre todas as posições". Toda ação passa pela sua confirmação.
                    </p>
                  </div>
                ) : (
                  messages.map((m) => <Message key={m.id} message={m} onDecided={decided} />)
                )}
                {sending && (
                  <>
                    {!asked && <div className="sup-bubble question pending">{sending}</div>}
                    {draft ? (
                      <div className="sup-bubble answer streaming" aria-busy="true">
                        <span className="sup-answer-label">Análise</span>
                        <Markdown text={draft} writing />
                      </div>
                    ) : (
                      <Thinking label={stageLabel(stage)} />
                    )}
                  </>
                )}
              </div>
              <form className="supervisor-composer" onSubmit={send}>
                <textarea
                  ref={input}
                  className="textarea"
                  rows={2}
                  maxLength={MAX_MESSAGE}
                  placeholder="Escreva para o XTrader AI…"
                  aria-label="Mensagem para o XTrader AI"
                  value={text}
                  disabled={sending !== null}
                  onChange={(e) => setText(e.target.value)}
                  onKeyDown={onComposerKey}
                />
                <button
                  type="submit"
                  className="btn btn-primary btn-icon"
                  disabled={sending !== null || !text.trim()}
                  aria-label="Enviar"
                >
                  <Icon name="send" size={16} />
                </button>
              </form>
            </>
          ) : (
            <div className="supervisor-runs">
              <div className="supervisor-runs-head">
                <button
                  type="button"
                  className="btn btn-sm btn-primary"
                  disabled={!ready || Boolean(settings?.analyzing)}
                  onClick={analyze}
                >
                  <Icon name="restart" size={14} />
                  {settings?.analyzing ? "Analisando…" : "Analisar agora"}
                </button>
                {settings?.next_run_at && (
                  <span className="field-hint">próxima às {clock(settings.next_run_at)}</span>
                )}
                {settings && settings.failures_in_row > 0 && (
                  <span className={toneClass("error")}>
                    {settings.failures_in_row} falha(s) seguida(s)
                  </span>
                )}
              </div>
              {runs === null ? (
                <p className="supervisor-empty">Carregando…</p>
              ) : (
                <SupervisorRuns runs={runs} compact />
              )}
            </div>
          )}
        </section>
      )}
    </>
  );
}

/**
 * A message of the conversation: a question of the user, an answer of the
 * supervisor (an analysis, with the time of its data), a proposal or the
 * result of an action, or an error.
 */
function Message({
  message,
  onDecided,
}: {
  message: SupervisorMessageInfo;
  onDecided: (decision: SupervisorDecision) => void;
}) {
  switch (message.kind) {
    case "question":
      return <div className="sup-bubble question">{message.content}</div>;
    case "proposal":
      return (
        <div className="sup-message proposal">
          {message.action ? (
            <SupervisorActionCard action={message.action} onDecided={onDecided} />
          ) : (
            <p className="sup-result-text">{message.content}</p>
          )}
        </div>
      );
    case "result":
      // the items of the result are on the card of the proposal, above
      return (
        <div className="sup-message result">
          <Icon name="info" size={14} />
          <p className="sup-result-text">
            {message.action ? `Ação #${message.action.id}: ` : ""}
            {message.content}
          </p>
        </div>
      );
    case "error":
      return (
        <div className="sup-bubble error" role="status">
          <Icon name="error" size={14} />
          {message.content}
        </div>
      );
  }
  return (
    <div className="sup-bubble answer">
      <span className="sup-answer-label">Análise</span>
      <Markdown text={message.content} />
      {message.data_at && <span className="sup-data-at">dados de {clock(message.data_at)}</span>}
    </div>
  );
}

/**
 * The XTrader AI at work, until the text of its answer comes: a pulsing
 * mark, three dots and what it does, eg: Lendo os agentes. Still with the
 * motion reduced, the words telling it.
 */
function Thinking({ label }: { label: string }) {
  return (
    <div className="sup-bubble answer thinking" role="status" aria-busy="true">
      <span className="sup-thinking-mark" aria-hidden="true">
        <Icon name="shield" size={14} />
      </span>
      <span className="sup-thinking-label">{label}</span>
      <span className="sup-thinking-dots" aria-hidden="true">
        <i />
        <i />
        <i />
      </span>
    </div>
  );
}
