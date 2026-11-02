import { type ReactNode, useState } from "react";
import { logout } from "../api/client";
import { setValuesHidden, useValuesHidden } from "../lib/hiddenValues";
import { dashboardHref, sessionStore } from "../lib/nav";
import type { Theme } from "../lib/theme";
import { Icon } from "./Icon";
import { LogoMark } from "./Logo";

interface Props {
  theme: Theme;
  onThemeToggle: () => void;
  /** Whether the updates of the agent are live: none without an agent. */
  connected?: boolean;
  /** The user of the session, once known. */
  user?: string;
  /** Opens the navigation, on a screen too narrow for it in the bar. */
  onMenu: () => void;
  /** Whether the navigation is open, of the menu button. */
  menuOpen: boolean;
  /** The navigation of the pages, in the middle of the bar. */
  nav: ReactNode;
  /** The actions of the page shown, before the theme, eg: the layout of the dashboard. */
  actions?: ReactNode;
}

/**
 * The bar atop the pages: the brand, the navigation (behind the menu on a
 * phone), whether the updates are live, the amounts hidden or shown, the
 * theme, the user and the logout.
 */
export function Header({
  theme,
  onThemeToggle,
  connected,
  user,
  onMenu,
  menuOpen,
  nav,
  actions,
}: Props) {
  const [leaving, setLeaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const hidden = useValuesHidden();

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
    <header className="appbar">
      <div className="appbar-inner">
        <button
          type="button"
          className="btn btn-ghost btn-icon appbar-menu"
          onClick={onMenu}
          aria-label="Abrir menu"
          aria-expanded={menuOpen}
          aria-controls="nav-drawer"
        >
          <Icon name="menu" size={18} />
        </button>
        <a className="brand" href={dashboardHref(sessionStore())} title="Ir para o painel">
          <span className="brand-mark" aria-hidden="true">
            <LogoMark size={22} />
          </span>
          <span className="brand-text">
            <span className="brand-name">XTrader</span>
            <span className="brand-sub">Trading automatizado</span>
          </span>
        </a>

        <div className="appbar-nav">{nav}</div>

        <div className="appbar-actions">
          {error && (
            <span className="appbar-error" role="alert" title={error}>
              Não foi possível sair
            </span>
          )}
          {connected !== undefined && (
            <span
              className={connected ? "live on" : "live off"}
              role="status"
              title={
                connected ? "Recebendo atualizações em tempo real" : "Sem conexão: reconectando…"
              }
            >
              <i aria-hidden="true" />
              <span className="live-label">{connected ? "Ao vivo" : "Reconectando…"}</span>
            </span>
          )}
          {actions}
          <button
            type="button"
            className="btn btn-ghost btn-icon"
            aria-pressed={hidden}
            onClick={() => setValuesHidden(!hidden)}
            title={
              hidden
                ? "Mostrar valores em dinheiro"
                : "Ocultar valores em dinheiro em todas as páginas"
            }
            aria-label={hidden ? "Mostrar valores" : "Ocultar valores"}
          >
            <Icon name={hidden ? "eyeOff" : "eye"} />
          </button>
          <button
            type="button"
            className="btn btn-ghost btn-icon"
            onClick={onThemeToggle}
            title={theme === "dark" ? "Usar tema claro" : "Usar tema escuro"}
            aria-label={theme === "dark" ? "Usar tema claro" : "Usar tema escuro"}
          >
            <Icon name={theme === "dark" ? "sun" : "moon"} />
          </button>
          <span className="appbar-divider" aria-hidden="true" />
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
      </div>
    </header>
  );
}
