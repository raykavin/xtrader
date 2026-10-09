import { type MouseEvent, type ReactNode, useEffect, useRef, useState } from "react";
import { fetchSessionInfo, UI_VERSION } from "../api/client";
import { dashboardHref, sessionStore, settingsHref } from "../lib/nav";
import type { SettingsTab } from "../lib/settings";
import type { Theme } from "../lib/theme";
import { Footer } from "./Footer";
import { Header } from "./Header";
import { Icon, type IconName } from "./Icon";
import { Logo } from "./Logo";

/** What a section of the settings shows in the navigation once known, eg: how many agents. */
export interface NavHint {
  text: string;
  tone?: "on" | "off";
}

interface Props {
  page: "dashboard" | "settings";
  /** The section of the settings shown. */
  section?: SettingsTab;
  /** On the settings: shows another section without loading the page again. */
  onSection?: (tab: SettingsTab) => void;
  hints?: Partial<Record<SettingsTab, NavHint>>;
  /** The title of the page in its top bar. */
  title: string;
  theme: Theme;
  onThemeToggle: () => void;
  /** Whether the updates of the agent shown are live. */
  connected?: boolean;
  children: ReactNode;
}

const sections: { tab: SettingsTab; label: string; icon: IconName }[] = [
  { tab: "agentes", label: "Agentes", icon: "bot" },
  { tab: "prompts", label: "Prompts", icon: "prompt" },
  { tab: "telegram", label: "Telegram", icon: "send" },
  { tab: "usuarios", label: "Usuários", icon: "users" },
];

/** The width the sidebar shows from: below it, the navigation is a drawer. */
const DRAWER_BELOW = "(max-width: 767px)";

/**
 * The frame of the pages: the navigation in a sidebar (a rail of icons on a
 * tablet, a drawer on a phone), the top bar, the page and its foot.
 */
export function AppShell({
  page,
  section,
  onSection,
  hints,
  title,
  theme,
  onThemeToggle,
  connected,
  children,
}: Props) {
  const drawer = useRef<HTMLDialogElement>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [user, setUser] = useState<string | undefined>(undefined);

  useEffect(() => {
    const controller = new AbortController();
    fetchSessionInfo(controller.signal)
      .then((s) => setUser(s.username || undefined))
      .catch(() => {
        // the bar shows without the user
      });
    return () => controller.abort();
  }, []);

  // The drawer, a modal dialog: Esc and the backdrop close it, the focus
  // kept inside while open; closed once the window is wide enough for the
  // sidebar.
  useEffect(() => {
    const element = drawer.current;
    if (!element) return;
    if (menuOpen && !element.open) element.showModal();
    if (!menuOpen && element.open) element.close();
  }, [menuOpen]);
  useEffect(() => {
    const query = window.matchMedia?.(DRAWER_BELOW);
    if (!query) return;
    const changed = () => {
      if (!query.matches) setMenuOpen(false);
    };
    query.addEventListener("change", changed);
    return () => query.removeEventListener("change", changed);
  }, []);

  const nav = (where: "sidebar" | "drawer") => (
    <Navigation
      page={page}
      section={section}
      hints={hints}
      onNavigate={(tab, event) => {
        setMenuOpen(false);
        if (tab && onSection) {
          event.preventDefault();
          onSection(tab);
        }
      }}
      where={where}
    />
  );

  return (
    <div className="shell">
      <a className="skip-link" href="#main">
        Pular para o conteúdo
      </a>
      <aside className="sidebar" aria-label="Navegação principal">
        {nav("sidebar")}
      </aside>
      {/* biome-ignore lint/a11y/useKeyWithClickEvents: Esc closes the dialog natively */}
      <dialog
        ref={drawer}
        id="nav-drawer"
        className="drawer"
        aria-label="Navegação principal"
        onClose={() => setMenuOpen(false)}
        onClick={(e) => {
          if (e.target === drawer.current) setMenuOpen(false); // the backdrop
        }}
      >
        <div className="drawer-head">
          <Logo size={22} />
          <button
            type="button"
            className="btn btn-ghost btn-icon"
            onClick={() => setMenuOpen(false)}
            aria-label="Fechar menu"
          >
            <Icon name="close" />
          </button>
        </div>
        {nav("drawer")}
      </dialog>

      <div className="shell-main">
        <Header
          title={title}
          theme={theme}
          onThemeToggle={onThemeToggle}
          connected={connected}
          user={user}
          onMenu={() => setMenuOpen(true)}
          menuOpen={menuOpen}
        />
        <main id="main" className="page" tabIndex={-1}>
          {children}
        </main>
        <Footer />
      </div>
    </div>
  );
}

/** The links of the navigation, the page shown marked. */
function Navigation({
  page,
  section,
  hints,
  onNavigate,
  where,
}: {
  page: Props["page"];
  section?: SettingsTab;
  hints?: Props["hints"];
  /** A link followed: with the section of the settings it shows, if any. */
  onNavigate: (tab: SettingsTab | null, event: MouseEvent) => void;
  where: "sidebar" | "drawer";
}) {
  return (
    <div className="nav">
      {where === "sidebar" && (
        <a
          className="sidebar-brand"
          href={dashboardHref(sessionStore())}
          aria-label="XTrader: painel"
        >
          <Logo size={24} />
        </a>
      )}
      <nav className="nav-groups" aria-label={where === "drawer" ? undefined : "Páginas"}>
        <div className="nav-group">
          <span className="nav-heading">Operação</span>
          <a
            className="nav-link"
            href={dashboardHref(sessionStore())}
            aria-current={page === "dashboard" ? "page" : undefined}
            title="Painel"
            onClick={(e) => onNavigate(null, e)}
          >
            <Icon name="dashboard" size={18} className="nav-icon" />
            <span className="nav-label">Painel</span>
          </a>
        </div>
        <div className="nav-group">
          <span className="nav-heading">Configurações</span>
          {sections.map(({ tab, label, icon }) => {
            const hint = hints?.[tab];
            return (
              <a
                key={tab}
                className="nav-link"
                href={settingsHref(tab)}
                aria-current={page === "settings" && section === tab ? "page" : undefined}
                title={label}
                onClick={(e) => onNavigate(tab, e)}
              >
                <Icon name={icon} size={18} className="nav-icon" />
                <span className="nav-label">{label}</span>
                {hint && (
                  <span className={hint.tone ? `nav-hint ${hint.tone}` : "nav-hint"}>
                    {hint.text}
                  </span>
                )}
              </a>
            );
          })}
        </div>
      </nav>
      <div className="nav-foot">
        <span className="version" title="Versão do painel">
          {/^\d/.test(UI_VERSION) ? `v${UI_VERSION}` : UI_VERSION}
        </span>
      </div>
    </div>
  );
}
