import { useState } from "react";
import { logout } from "../api/client";
import type { Theme } from "../lib/theme";
import { Icon } from "./Icon";
import { LogoMark } from "./Logo";

interface Props {
  /** The title of the page, eg: Painel, or the section of the settings. */
  title: string;
  theme: Theme;
  onThemeToggle: () => void;
  /** Whether the updates of the agent are live: none without an agent. */
  connected?: boolean;
  /** The user of the session, once known. */
  user?: string;
  /** Opens the navigation, on a screen too narrow for the sidebar. */
  onMenu: () => void;
  /** Whether the navigation is open, of the menu button. */
  menuOpen: boolean;
}

/**
 * The bar atop the pages: the menu on a narrow screen, the title of the
 * page, whether the updates are live, the theme, the user and the logout.
 */
export function Header({ title, theme, onThemeToggle, connected, user, onMenu, menuOpen }: Props) {
  const [leaving, setLeaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const leave = async () => {
    setLeaving(true);
    setError(null);
    try {
      await logout();
      window.location.assign("/login");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setLeaving(false);
    }
  };

  return (
    <header className="topbar">
      <button
        type="button"
        className="btn btn-ghost btn-icon topbar-menu"
        onClick={onMenu}
        aria-label="Abrir menu"
        aria-expanded={menuOpen}
        aria-controls="nav-drawer"
      >
        <Icon name="menu" size={18} />
      </button>
      <span className="topbar-mark" aria-hidden="true">
        <LogoMark size={20} />
      </span>
      <h1 className="topbar-title">{title}</h1>

      <div className="topbar-actions">
        {error && (
          <span className="topbar-error" role="alert" title={error}>
            Não foi possível sair
          </span>
        )}
        {connected !== undefined && (
          <span
            className={connected ? "live on" : "live off"}
            role="status"
            title={
              connected ? "Atualizações em tempo real conectadas" : "Sem conexão: reconectando…"
            }
          >
            <i aria-hidden="true" />
            <span className="live-label">{connected ? "Ao vivo" : "Reconectando…"}</span>
          </span>
        )}
        <button
          type="button"
          className="btn btn-ghost btn-icon"
          onClick={onThemeToggle}
          title={theme === "dark" ? "Usar tema claro" : "Usar tema escuro"}
          aria-label={theme === "dark" ? "Usar tema claro" : "Usar tema escuro"}
        >
          <Icon name={theme === "dark" ? "sun" : "moon"} />
        </button>
        <span className="topbar-divider" aria-hidden="true" />
        {user && (
          <span className="user" title={`Sessão de ${user}`}>
            <span className="user-avatar" aria-hidden="true">
              {user.slice(0, 1).toUpperCase()}
            </span>
            <span className="user-name">{user}</span>
          </span>
        )}
        <button
          type="button"
          className="btn btn-ghost btn-icon"
          onClick={leave}
          disabled={leaving}
          title={leaving ? "Saindo…" : "Sair"}
          aria-label="Sair"
        >
          <Icon name="logout" />
        </button>
      </div>
    </header>
  );
}
