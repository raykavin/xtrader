import { type MouseEvent, type ReactNode, useEffect, useRef, useState } from "react";
import { fetchSessionInfo } from "../api/client";
import { dashboardHref, sessionStore, settingsHref } from "../lib/nav";
import type { SettingsTab } from "../lib/settings";
import type { Theme } from "../lib/theme";
import { Footer } from "./Footer";
import { Header } from "./Header";
import { Icon, type IconName } from "./Icon";
import { Logo } from "./Logo";
import { SupervisorChat } from "./SupervisorChat";

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
  /** The title of the page, read by the screen readers. */
  title: string;
  theme: Theme;
  onThemeToggle: () => void;
  /** The actions of the page, in the bar before the theme, eg: the layout of the dashboard. */
  actions?: ReactNode;
  /** Whether the updates of the agent shown are live. */
  connected?: boolean;
  children: ReactNode;
}

const sections: { tab: SettingsTab; label: string; icon: IconName }[] = [
  { tab: "agentes", label: "Agentes", icon: "bot" },
  { tab: "limites", label: "Limites", icon: "alert" },
  { tab: "prompts", label: "Prompts", icon: "prompt" },
  { tab: "supervisor", label: "XTrader AI", icon: "shield" },
  { tab: "telegram", label: "Telegram", icon: "send" },
  { tab: "usuarios", label: "Usuários", icon: "users" },
  { tab: "layout", label: "Layout", icon: "layout" },
];

/** The width the bar holds the navigation from: below it, the navigation is a drawer. */
const DRAWER_BELOW = "(max-width: 767px)";

/**
 * The frame of the pages: the bar atop them, with the brand, the navigation
 * as a segmented control (a drawer on a phone) and the actions of the
 * session; then the page and its foot.
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
  actions,
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
  // navigation in the bar.
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

  const navigate = (tab: SettingsTab | null, event: MouseEvent) => {
    setMenuOpen(false);
    if (tab && onSection) {
      event.preventDefault();
      onSection(tab);
    }
  };

  return (
    <div className="shell">
      <a className="skip-link" href="#main">
        Pular para o conteúdo
      </a>
      <Header
        theme={theme}
        onThemeToggle={onThemeToggle}
        connected={connected}
        actions={actions}
        user={user}
        onMenu={() => setMenuOpen(true)}
        menuOpen={menuOpen}
        nav={
          <Navigation
            page={page}
            section={section}
            hints={hints}
            onNavigate={navigate}
            where="bar"
          />
        }
      />
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
        <Navigation
          page={page}
          section={section}
          hints={hints}
          onNavigate={navigate}
          where="drawer"
        />
      </dialog>

      <main id="main" className="page" tabIndex={-1}>
        <h1 className="sr-only">{title}</h1>
        {children}
      </main>
      <Footer />
      <SupervisorChat />
    </div>
  );
}

/** The links of the navigation, the page shown marked: a segmented bar, or the list of the drawer. */
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
  where: "bar" | "drawer";
}) {
  const dashboard = (
    <a
      className="nav-link"
      href={dashboardHref(sessionStore())}
      aria-current={page === "dashboard" ? "page" : undefined}
      onClick={(e) => onNavigate(null, e)}
    >
      <Icon name="dashboard" size={where === "bar" ? 15 : 18} className="nav-icon" />
      <span className="nav-label">Painel</span>
    </a>
  );
  const links = sections.map(({ tab, label, icon }) => {
    const hint = hints?.[tab];
    return (
      <a
        key={tab}
        className="nav-link"
        href={settingsHref(tab)}
        aria-current={page === "settings" && section === tab ? "page" : undefined}
        onClick={(e) => onNavigate(tab, e)}
      >
        <Icon name={icon} size={where === "bar" ? 15 : 18} className="nav-icon" />
        <span className="nav-label">{label}</span>
        {hint && (
          <span className={hint.tone ? `nav-hint ${hint.tone}` : "nav-hint"}>{hint.text}</span>
        )}
      </a>
    );
  });

  if (where === "bar") {
    return (
      <nav className="appnav" aria-label="Páginas">
        {dashboard}
        <span className="appnav-divider" aria-hidden="true" />
        {links}
      </nav>
    );
  }
  return (
    <nav className="nav-groups">
      <div className="nav-group">
        <span className="nav-heading">Operação</span>
        {dashboard}
      </div>
      <div className="nav-group">
        <span className="nav-heading">Configurações</span>
        {links}
      </div>
    </nav>
  );
}
