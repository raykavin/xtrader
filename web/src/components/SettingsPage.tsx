import { type ReactNode, useCallback, useEffect, useState } from "react";
import {
  fetchAgentSettings,
  fetchMT5Accounts,
  fetchPrompts,
  fetchRoles,
  fetchSupervisorSettings,
  fetchTelegramSettings,
  fetchUsers,
} from "../api/client";
import {
  type AgentSettings,
  type MT5AccountInfo,
  PermAgentsManage,
  PermAgentsView,
  PermMT5AccountsView,
  PermPromptsView,
  PermSupervisorView,
  PermTelegramView,
  PermUsersManage,
  PermUsersView,
  type PromptInfo,
  type RolesResponse,
  type SupervisorSettingsResponse,
  type TelegramSettingsResponse,
  type UserInfo,
} from "../api/types";
import { allowedTabs, can, shownTab } from "../lib/access";
import { terminalsOf } from "../lib/mt5Accounts";
import { type SettingsTab, settingsTabFromHash } from "../lib/settings";
import { loadTheme, saveTheme, type Theme } from "../lib/theme";
import { useAccess } from "./AccessContext";
import { AgentsSettings } from "./AgentsSettings";
import { AppShell, type NavHint } from "./AppShell";
import { DailyLimitsSettings } from "./DailyLimitsSettings";
import { LayoutSettings } from "./LayoutSettings";
import { MT5AccountsSettings } from "./MT5AccountsSettings";
import { NewsSettings } from "./NewsSettings";
import { PageHeader } from "./PageHeader";
import { PromptsSettings } from "./PromptsSettings";
import { RolesSettings } from "./RolesSettings";
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
      "Num agente em execução, “Ligado”, “Painel de trading”, “Controle via Telegram”, “Permitir reanálise” e o prompt valem na hora; o resto, depois de reiniciar.",
  },
  contas: {
    title: "Contas MT5",
    description:
      "Um terminal fica numa conta por vez. Trocar a conta dele pede confirmação: os agentes param, as posições são fechadas e as ordens pendentes, canceladas.",
  },
  limites: {
    title: "Limites",
    description:
      "Perda e ganho máximos do dia, no total e por agente, as janelas do horário de operação e a exposição por ativo. Atingido um limite, o XTrader zera as posições, cancela as ordens pendentes de entrada e bloqueia as entradas até 00:00; fora das janelas, nenhum agente abre posição.",
  },
  noticias: {
    title: "Notícias",
    description:
      "Calendário econômico do servidor MCP do MetaTrader 5, para todos os agentes: nenhum agente opera na janela definida em torno de cada divulgação. As mudanças valem na hora, sem reiniciar.",
  },
  prompts: {
    title: "Prompts",
    description: (
      <>
        Instruções dos agentes LLM. As linhas do cabeçalho (<code># Indicators:</code>,{" "}
        <code># Take profit:</code> e outras) configuram o agente; uma edição vale a partir da
        próxima análise.
      </>
    ),
  },
  supervisor: {
    title: "XTrader AI",
    description:
      "Um LLM que acompanha todos os agentes: analisa periodicamente, bloqueia entradas ou para um agente dentro da autonomia definida aqui e propõe o resto para você confirmar na conversa.",
  },
  telegram: {
    title: "Telegram",
    description: "Só os IDs autorizados controlam os agentes e os prompts pelo bot.",
  },
  usuarios: {
    title: "Usuários e papéis",
    description:
      "Quem pode entrar no painel e o que cada um vê e faz, pelo papel. Um usuário sem papel não entra. Desativar, excluir, trocar a senha ou tirar o papel de um usuário encerra as sessões dele.",
  },
  layout: {
    title: "Layout",
    description: "A disposição dos cards do painel, salva neste navegador.",
  },
};

/**
 * The page of the settings of the controller, kept in its database, a
 * section at a time: the agents, the prompts of the LLM agents, the Telegram
 * bot and the users of the dashboard. The section shown is the hash of the
 * URL, eg: #prompts, chosen in the navigation of the page; only the sections
 * the role of the user shows, and only their data read.
 */
export function SettingsPage() {
  const access = useAccess();
  const [theme, setTheme] = useState<Theme>(loadTheme);
  const [wanted, setTab] = useState<SettingsTab>(() => settingsTabFromHash(window.location.hash));
  const tab = shownTab(access, wanted) ?? wanted;
  const shown = allowedTabs(access);
  const seesAgents = can(access, PermAgentsView);
  const seesTelegram = can(access, PermTelegramView);
  const seesSupervisor = can(access, PermSupervisorView);
  const seesAccounts = can(access, PermMT5AccountsView);
  const seesPrompts = can(access, PermPromptsView, PermAgentsManage); // the prompt of an agent
  const seesUsers = can(access, PermUsersView);
  const current = access.username ?? "";
  const [agents, setAgents] = useState<AgentSettings[] | null>(null);
  const [prompts, setPrompts] = useState<PromptInfo[] | null>(null);
  const [telegram, setTelegram] = useState<TelegramSettingsResponse | null>(null);
  const [supervisor, setSupervisor] = useState<SupervisorSettingsResponse | null>(null);
  const [users, setUsers] = useState<UserInfo[] | null>(null);
  const [roles, setRoles] = useState<RolesResponse | null>(null);
  const [accounts, setAccounts] = useState<MT5AccountInfo[] | null>(null);
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
    if (seesPrompts) fetchPrompts().then(setPrompts).catch(fail);
  }, [fail, seesPrompts]);
  const loadUsers = useCallback(() => {
    if (!seesUsers) return;
    fetchUsers().then(setUsers).catch(fail);
    fetchRoles().then(setRoles).catch(fail); // the users of each role
  }, [fail, seesUsers]);

  useEffect(() => {
    const controller = new AbortController();
    const failed = (e: unknown) => {
      if (!controller.signal.aborted) fail(e);
    };
    const poll = () => {
      if (seesAgents) {
        fetchAgentSettings(controller.signal)
          .then((list) => {
            setAgents(list);
            setError(null);
          })
          .catch(failed);
      }
      if (seesTelegram) fetchTelegramSettings(controller.signal).then(setTelegram).catch(failed);
      if (seesSupervisor)
        fetchSupervisorSettings(controller.signal).then(setSupervisor).catch(failed);
      if (seesAccounts) {
        fetchMT5Accounts(controller.signal)
          .then((response) => setAccounts(response.accounts))
          .catch(() => {
            // the count only: the tab tells its own error
          });
      }
    };
    poll();
    const timer = window.setInterval(poll, POLL);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, [fail, seesAgents, seesTelegram, seesSupervisor, seesAccounts]);
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
  const terminals = accounts && terminalsOf(accounts);
  const hints: Partial<Record<SettingsTab, NavHint>> = {
    ...(agents && {
      agentes: { text: `${running}/${agents.length}`, tone: running > 0 ? "on" : undefined },
    }),
    ...(terminals && {
      contas: {
        text: `${terminals.active}/${terminals.total}`,
        tone: terminals.active > 0 ? "on" : undefined,
      },
    }),
    ...(prompts && { prompts: { text: String(prompts.length) } }),
    ...(supervisor && {
      supervisor: supervisor.enabled
        ? { text: "ligado", tone: "on" }
        : { text: "desligado", tone: "off" },
    }),
    ...(telegram && {
      telegram: telegram.running
        ? { text: "executando", tone: "on" }
        : { text: "parado", tone: "off" },
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

      <Panel tab="agentes" shown={tab} allowed={shown}>
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
      <Panel tab="contas" shown={tab} allowed={shown}>
        <MT5AccountsSettings intro={intros.contas} />
      </Panel>
      <Panel tab="limites" shown={tab} allowed={shown}>
        <DailyLimitsSettings intro={intros.limites} />
      </Panel>
      <Panel tab="noticias" shown={tab} allowed={shown}>
        <NewsSettings intro={intros.noticias} />
      </Panel>
      <Panel tab="prompts" shown={tab} allowed={shown}>
        {prompts === null ? (
          <SectionLoading intro={intros.prompts} />
        ) : (
          <PromptsSettings intro={intros.prompts} prompts={prompts} onChange={loadPrompts} />
        )}
      </Panel>
      <Panel tab="supervisor" shown={tab} allowed={shown}>
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
      <Panel tab="telegram" shown={tab} allowed={shown}>
        {telegram === null ? (
          <SectionLoading intro={intros.telegram} />
        ) : (
          <TelegramSettings intro={intros.telegram} telegram={telegram} onChange={setTelegram} />
        )}
      </Panel>
      <Panel tab="usuarios" shown={tab} allowed={shown}>
        {users === null || roles === null ? (
          <SectionLoading intro={intros.usuarios} />
        ) : (
          <UsersSettings
            intro={intros.usuarios}
            users={users}
            roles={roles.roles}
            current={current}
            onChange={loadUsers}
          >
            <RolesSettings
              roles={roles.roles}
              areas={roles.areas}
              manage={can(access, PermUsersManage)}
              onChange={loadUsers}
            />
          </UsersSettings>
        )}
      </Panel>
      <Panel tab="layout" shown={tab} allowed={shown}>
        <LayoutSettings intro={intros.layout} />
      </Panel>
    </AppShell>
  );
}

/**
 * The panel of a section, kept while hidden: what is typed in it survives a
 * switch; not drawn at all when the role of the user doesn't show it.
 */
function Panel({
  tab,
  shown,
  allowed,
  children,
}: {
  tab: SettingsTab;
  shown: SettingsTab;
  allowed: SettingsTab[];
  children: ReactNode;
}) {
  if (!allowed.includes(tab)) return null;
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
      <Loading label="Carregando…">
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
