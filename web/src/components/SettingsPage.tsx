import { type ReactNode, useCallback, useEffect, useState } from "react";
import {
  fetchAgentSettings,
  fetchPrompts,
  fetchSessionInfo,
  fetchSupervisorSettings,
  fetchTelegramSettings,
  fetchUsers,
} from "../api/client";
import type {
  AgentSettings,
  PromptInfo,
  SupervisorSettingsResponse,
  TelegramSettingsResponse,
  UserInfo,
} from "../api/types";
import { type SettingsTab, settingsTabFromHash } from "../lib/settings";
import { loadTheme, saveTheme, type Theme } from "../lib/theme";
import { AgentsSettings } from "./AgentsSettings";
import { AppShell, type NavHint } from "./AppShell";
import { DailyLimitsSettings } from "./DailyLimitsSettings";
import { LayoutSettings } from "./LayoutSettings";
import { NewsSettings } from "./NewsSettings";
import { PageHeader } from "./PageHeader";
import { PromptsSettings } from "./PromptsSettings";
import { Alert, Loading, Skeleton } from "./States";
import { SupervisorSettings } from "./SupervisorSettings";
import { TelegramSettings } from "./TelegramSettings";
import { UsersSettings } from "./UsersSettings";

/** How often the state of the agents and of the bot is read again. */
const POLL = 5000;

/** The title of each section and what it is for. */
export interface SectionIntro {
  title: string;
  description: ReactNode;
}

const intros: Record<SettingsTab, SectionIntro> = {
  agentes: {
    title: "Agentes",
    description:
      "Ligue, configure e reinicie cada agente. Num agente em execução, Ativo, Painel de trading, Controle via Telegram e o prompt mudam na hora; o resto vale depois de reiniciar.",
  },
  limites: {
    title: "Limites diários",
    description:
      "A perda e o ganho máximos do dia, de todos os agentes somados e de cada agente. Atingido um limite, a aplicação zera as posições, cancela as ordens pendentes de entrada e bloqueia as entradas até 00:00.",
  },
  noticias: {
    title: "Notícias",
    description:
      "O calendário econômico que os agentes LLM consultam pelo servidor MCP do MetaTrader 5: ligue ou desligue para todos os agentes e defina o token, compartilhado por todos. As mudanças valem na hora para os agentes em execução, sem reiniciar.",
  },
  prompts: {
    title: "Prompts",
    description: (
      <>
        As instruções dos agentes LLM. As linhas do cabeçalho (<code># Indicators:</code>,{" "}
        <code># Take profit:</code> e outras) configuram o agente; uma edição vale a partir da
        próxima análise.
      </>
    ),
  },
  supervisor: {
    title: "XTrader AI",
    description:
      "O supervisor dos agentes, um modelo de linguagem que acompanha todos eles: analisa periodicamente, bloqueia entradas ou para um agente dentro da autonomia definida aqui, e propõe o resto para você confirmar na conversa.",
  },
  telegram: {
    title: "Telegram",
    description:
      "O bot do Telegram e quem ele atende: só os IDs autorizados controlam os agentes e os prompts por lá.",
  },
  usuarios: {
    title: "Usuários",
    description:
      "Quem pode entrar no painel. Desativar, excluir ou trocar a senha de um usuário encerra as sessões dele.",
  },
  layout: {
    title: "Layout",
    description:
      "A disposição dos cards do painel neste navegador: a ordem, os ocultos e o tamanho do gráfico.",
  },
};

/**
 * The page of the settings of the controller, kept in its database, a
 * section at a time: the agents, the prompts of the LLM agents, the Telegram
 * bot and the users of the dashboard. The section shown is the hash of the
 * URL, eg: #prompts, chosen in the navigation of the page.
 */
export function SettingsPage() {
  const [theme, setTheme] = useState<Theme>(loadTheme);
  const [tab, setTab] = useState<SettingsTab>(() => settingsTabFromHash(window.location.hash));
  const [agents, setAgents] = useState<AgentSettings[] | null>(null);
  const [prompts, setPrompts] = useState<PromptInfo[] | null>(null);
  const [telegram, setTelegram] = useState<TelegramSettingsResponse | null>(null);
  const [supervisor, setSupervisor] = useState<SupervisorSettingsResponse | null>(null);
  const [users, setUsers] = useState<UserInfo[] | null>(null);
  const [current, setCurrent] = useState("");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    saveTheme(theme);
  }, [theme]);

  // the section of a hash changed on the page, eg: a link to #prompts, or one typed
  useEffect(() => {
    const onHash = () => setTab(settingsTabFromHash(window.location.hash));
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

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
      fetchSupervisorSettings(controller.signal)
        .then(setSupervisor)
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

  /** Shows a section, in the hash of the URL without a new entry: Back leaves the settings. */
  const select = (next: SettingsTab) => {
    setTab(next);
    window.history.replaceState(window.history.state, "", `#${next}`);
    document.getElementById("main")?.focus({ preventScroll: true });
    window.scrollTo({ top: 0 });
  };

  /** What each section holds, once loaded: how many, or whether the bot runs. */
  const running = agents?.filter((a) => a.running).length ?? 0;
  const hints: Partial<Record<SettingsTab, NavHint>> = {
    ...(agents && {
      agentes: { text: `${running}/${agents.length}`, tone: running > 0 ? "on" : undefined },
    }),
    ...(prompts && { prompts: { text: String(prompts.length) } }),
    ...(supervisor && {
      supervisor: supervisor.enabled
        ? { text: "ativo", tone: "on" }
        : { text: "desligado", tone: "off" },
    }),
    ...(telegram && {
      telegram: telegram.running ? { text: "ativo", tone: "on" } : { text: "parado", tone: "off" },
    }),
    ...(users && { usuarios: { text: String(users.length) } }),
  };

  return (
    <AppShell
      page="settings"
      section={tab}
      onSection={select}
      hints={hints}
      title="Configurações"
      theme={theme}
      onThemeToggle={() => setTheme((t) => (t === "dark" ? "light" : "dark"))}
    >
      {error && (
        <Alert tone="error" title="Não foi possível carregar as configurações">
          {error}
        </Alert>
      )}

      <Panel tab="agentes" shown={tab}>
        {agents === null ? (
          <SectionLoading intro={intros.agentes} cards />
        ) : (
          <AgentsSettings
            intro={intros.agentes}
            agents={agents}
            prompts={prompts ?? []}
            onChange={replaceAgent}
            onReload={loadAgents}
          />
        )}
      </Panel>
      <Panel tab="limites" shown={tab}>
        <DailyLimitsSettings intro={intros.limites} />
      </Panel>
      <Panel tab="noticias" shown={tab}>
        <NewsSettings intro={intros.noticias} />
      </Panel>
      <Panel tab="prompts" shown={tab}>
        {prompts === null ? (
          <SectionLoading intro={intros.prompts} />
        ) : (
          <PromptsSettings intro={intros.prompts} prompts={prompts} onChange={loadPrompts} />
        )}
      </Panel>
      <Panel tab="supervisor" shown={tab}>
        {supervisor === null ? (
          <SectionLoading intro={intros.supervisor} />
        ) : (
          <SupervisorSettings
            intro={intros.supervisor}
            settings={supervisor}
            onChange={setSupervisor}
          />
        )}
      </Panel>
      <Panel tab="telegram" shown={tab}>
        {telegram === null ? (
          <SectionLoading intro={intros.telegram} />
        ) : (
          <TelegramSettings intro={intros.telegram} telegram={telegram} onChange={setTelegram} />
        )}
      </Panel>
      <Panel tab="usuarios" shown={tab}>
        {users === null ? (
          <SectionLoading intro={intros.usuarios} />
        ) : (
          <UsersSettings
            intro={intros.usuarios}
            users={users}
            current={current}
            onChange={loadUsers}
          />
        )}
      </Panel>
      <Panel tab="layout" shown={tab}>
        <LayoutSettings intro={intros.layout} />
      </Panel>
    </AppShell>
  );
}

/** The panel of a section, kept while hidden: what is typed in it survives a switch. */
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
    <section className="settings-panel" aria-label={intros[tab].title} hidden={tab !== shown}>
      {children}
    </section>
  );
}

/** A section while it loads: its head, and blocks in place of its content. */
function SectionLoading({ intro, cards = false }: { intro: SectionIntro; cards?: boolean }) {
  return (
    <>
      <PageHeader eyebrow="Configurações" title={intro.title} description={intro.description} />
      <Loading label={`Carregando ${intro.title.toLowerCase()}…`}>
        {cards ? (
          <div className="agent-grid">
            <Skeleton height={300} />
            <Skeleton height={300} />
            <Skeleton height={300} />
          </div>
        ) : (
          <Skeleton height={220} />
        )}
      </Loading>
    </>
  );
}
