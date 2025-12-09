import {
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  fetchAgentSettings,
  fetchPrompts,
  fetchSessionInfo,
  fetchTelegramSettings,
  fetchUsers,
  UI_VERSION,
} from "../api/client";
import type { AgentSettings, PromptInfo, TelegramSettingsResponse, UserInfo } from "../api/types";
import { type SettingsTab, settingsTabFromHash, settingsTabs } from "../lib/settings";
import { loadTheme, saveTheme, type Theme } from "../lib/theme";
import { AgentsSettings } from "./AgentsSettings";
import { Footer } from "./Footer";
import { Header } from "./Header";
import { PromptsSettings } from "./PromptsSettings";
import { StatusIcon } from "./StatusIcon";
import { TelegramSettings } from "./TelegramSettings";
import { UsersSettings } from "./UsersSettings";

/** How often the state of the agents and of the bot is read again. */
const POLL = 5000;

const tabLabels: Record<SettingsTab, string> = {
  agentes: "Agentes",
  prompts: "Prompts",
  telegram: "Telegram",
  usuarios: "Usuários",
};

/** The IDs of a tab and of its panel, apart from the hashes: the page doesn't scroll to them. */
const tabId = (tab: SettingsTab) => `settings-tab-${tab}`;
const panelId = (tab: SettingsTab) => `settings-panel-${tab}`;

/**
 * The page of the settings of the controller, kept in its database, in tabs:
 * the agents, the prompts of the LLM agents, the Telegram bot and the users of
 * the dashboard. The tab shown is the hash of the URL, eg: #prompts.
 */
export function SettingsPage() {
  const [theme, setTheme] = useState<Theme>(loadTheme);
  const [tab, setTab] = useState<SettingsTab>(() => settingsTabFromHash(window.location.hash));
  const [agents, setAgents] = useState<AgentSettings[] | null>(null);
  const [prompts, setPrompts] = useState<PromptInfo[] | null>(null);
  const [telegram, setTelegram] = useState<TelegramSettingsResponse | null>(null);
  const [users, setUsers] = useState<UserInfo[] | null>(null);
  const [current, setCurrent] = useState("");
  const [error, setError] = useState<string | null>(null);
  const bar = useRef<HTMLDivElement>(null);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    saveTheme(theme);
  }, [theme]);

  // the tab of a hash changed on the page, eg: a link to #prompts, or one typed
  useEffect(() => {
    const onHash = () => setTab(settingsTabFromHash(window.location.hash));
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  // the tab shown in view in the bar, which scrolls sideways when the tabs don't
  // fit: again as they resize, eg: their hints loaded
  useEffect(() => {
    const list = bar.current;
    const shown = document.getElementById(tabId(tab));
    if (!list || !shown) return;
    const reveal = () => {
      const end = shown.offsetLeft + shown.offsetWidth;
      if (shown.offsetLeft < list.scrollLeft) list.scrollLeft = shown.offsetLeft;
      else if (end > list.scrollLeft + list.clientWidth) list.scrollLeft = end - list.clientWidth;
    };
    const observer = new ResizeObserver(reveal);
    for (const element of list.children) observer.observe(element);
    return () => observer.disconnect();
  }, [tab]);

  const fail = useCallback(
    (e: unknown) => setError(e instanceof Error ? e.message : String(e)),
    [],
  );
  const loadPrompts = useCallback(() => {
    fetchPrompts().then(setPrompts).catch(fail);
  }, [fail]);
  const loadUsers = useCallback(() => {
    fetchUsers().then(setUsers).catch(fail);
  }, [fail]);

  useEffect(() => {
    const controller = new AbortController();
    const poll = () => {
      fetchAgentSettings(controller.signal)
        .then((list) => {
          setAgents(list);
          setError(null);
        })
        .catch((e) => {
          if (!controller.signal.aborted) fail(e);
        });
      fetchTelegramSettings(controller.signal)
        .then(setTelegram)
        .catch((e) => {
          if (!controller.signal.aborted) fail(e);
        });
    };
    poll();
    const timer = window.setInterval(poll, POLL);
    fetchSessionInfo(controller.signal)
      .then((s) => setCurrent(s.username ?? ""))
      .catch(() => {
        // the users are told apart without it
      });
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, [fail]);
  useEffect(() => loadPrompts(), [loadPrompts]);
  useEffect(() => loadUsers(), [loadUsers]);

  const replaceAgent = useCallback(
    (agent: AgentSettings) => {
      setAgents((list) => list?.map((a) => (a.name === agent.name ? agent : a)) ?? list);
      loadPrompts(); // the agents using each prompt
    },
    [loadPrompts],
  );
  const loadAgents = useCallback(() => {
    fetchAgentSettings()
      .then((list) => {
        setAgents(list);
        setError(null);
      })
      .catch(fail);
    loadPrompts(); // the agents using each prompt
  }, [fail, loadPrompts]);

  /** Shows a tab, in the hash of the URL without a new entry: Back leaves the settings. */
  const select = (next: SettingsTab) => {
    setTab(next);
    window.history.replaceState(window.history.state, "", `#${next}`);
  };

  // the arrows, Home and End move between the tabs, showing them
  const onTabKey = (e: KeyboardEvent) => {
    const index = settingsTabs.indexOf(tab);
    const last = settingsTabs.length - 1;
    let target: number;
    switch (e.key) {
      case "ArrowLeft":
        target = index === 0 ? last : index - 1;
        break;
      case "ArrowRight":
        target = index === last ? 0 : index + 1;
        break;
      case "Home":
        target = 0;
        break;
      case "End":
        target = last;
        break;
      default:
        return;
    }
    const next = settingsTabs[target];
    if (!next) return;
    e.preventDefault();
    select(next);
    document.getElementById(tabId(next))?.focus();
  };

  /** What each tab holds, once loaded: how many, or whether the bot runs. */
  const hints: Record<SettingsTab, string | undefined> = {
    agentes: agents?.length.toString(),
    prompts: prompts?.length.toString(),
    telegram: telegram ? (telegram.running ? "executando" : "parado") : undefined,
    usuarios: users?.length.toString(),
  };

  return (
    <div className="app">
      <Header
        theme={theme}
        onThemeToggle={() => setTheme((t) => (t === "dark" ? "light" : "dark"))}
        version={UI_VERSION}
        page="settings"
      />
      <main className="settings">
        <div className="settings-head">
          <h1 className="settings-title">Configurações</h1>
          <div className="settings-tabs" role="tablist" aria-label="Configurações" ref={bar}>
            {settingsTabs.map((t) => (
              <button
                key={t}
                type="button"
                role="tab"
                id={tabId(t)}
                className="settings-tab"
                aria-selected={t === tab}
                aria-controls={panelId(t)}
                tabIndex={t === tab ? 0 : -1}
                onClick={() => select(t)}
                onKeyDown={onTabKey}
              >
                {tabLabels[t]}
                {hints[t] && (
                  <span
                    className={
                      t === "telegram" && telegram?.running
                        ? "settings-tab-hint on"
                        : "settings-tab-hint"
                    }
                  >
                    {t === "telegram" && telegram && (
                      <StatusIcon mark={telegram.running ? "play" : "stop"} />
                    )}
                    {hints[t]}
                  </span>
                )}
              </button>
            ))}
          </div>
        </div>
        {error && (
          <div className="banner error" role="alert">
            {error}
          </div>
        )}

        <Panel tab="agentes" shown={tab}>
          {agents === null ? (
            <div className="banner">Carregando agentes…</div>
          ) : (
            <AgentsSettings
              agents={agents}
              prompts={prompts ?? []}
              onChange={replaceAgent}
              onReload={loadAgents}
            />
          )}
        </Panel>
        <Panel tab="prompts" shown={tab}>
          {prompts === null ? (
            <div className="banner">Carregando prompts…</div>
          ) : (
            <PromptsSettings prompts={prompts} onChange={loadPrompts} />
          )}
        </Panel>
        <Panel tab="telegram" shown={tab}>
          {telegram === null ? (
            <div className="banner">Carregando…</div>
          ) : (
            <TelegramSettings telegram={telegram} onChange={setTelegram} />
          )}
        </Panel>
        <Panel tab="usuarios" shown={tab}>
          {users === null ? (
            <div className="banner">Carregando usuários…</div>
          ) : (
            <UsersSettings users={users} current={current} onChange={loadUsers} />
          )}
        </Panel>
      </main>
      <Footer />
    </div>
  );
}

/** The panel of a tab, kept while hidden: what is typed in it survives a switch. */
function Panel({
  tab,
  shown,
  children,
}: {
  tab: SettingsTab;
  shown: SettingsTab;
  children: ReactNode;
}) {
  return (
    <section
      id={panelId(tab)}
      className="settings-panel"
      role="tabpanel"
      aria-labelledby={tabId(tab)}
      hidden={tab !== shown}
    >
      {children}
    </section>
  );
}
