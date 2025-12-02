import { type ReactNode, useState } from "react";
import { logout } from "../api/client";
import type { Theme } from "../lib/theme";
import { Logo } from "./Logo";

interface Props {
  theme: Theme;
  onThemeToggle: () => void;
  version: string;
  /** Whether the updates of the agent are live: no dot without an agent. */
  connected?: boolean;
  /** The page shown, which the header links away from. */
  page?: "dashboard" | "settings";
}

/**
 * The top of the dashboard: the logo with whether the updates are live, the
 * version, the settings (or back to the dashboard), the theme and the logout.
 * The agent and the pair are in the AgentBar.
 */
export function Header({ theme, onThemeToggle, version, connected, page = "dashboard" }: Props) {
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
    <header className="header">
      <div className="brand">
        <Logo />
        {connected !== undefined && (
          <span
            className={`live-dot ${connected ? "on" : "off"}`}
            role="status"
            aria-label={connected ? "Ao vivo" : "Offline"}
            title={connected ? "Atualizações em tempo real conectadas" : "Offline: reconectando…"}
          >
            <i />
          </span>
        )}
      </div>
      <div className="header-actions">
        {error && (
          <span className="header-error" role="alert" title={error}>
            Não foi possível sair
          </span>
        )}
        <span className="version" title="Versão do painel">
          {version}
        </span>
        {page === "settings" ? (
          <a className="button icon-button" href="/" title="Voltar ao painel" aria-label="Painel">
            <ChartIcon />
          </a>
        ) : (
          <a
            className="button icon-button"
            href="/settings"
            title="Configurações"
            aria-label="Configurações"
          >
            <GearIcon />
          </a>
        )}
        <button
          type="button"
          className="icon-button"
          onClick={onThemeToggle}
          title="Alternar tema"
          aria-label="Alternar tema"
        >
          {theme === "dark" ? <SunIcon /> : <MoonIcon />}
        </button>
        <button
          type="button"
          className="icon-button"
          onClick={leave}
          disabled={leaving}
          title={leaving ? "Saindo…" : "Sair"}
          aria-label="Sair"
        >
          <LogoutIcon />
        </button>
      </div>
    </header>
  );
}

/** An icon of the header buttons: 15px, drawn with the color of the text. */
function Icon({ children }: { children: ReactNode }) {
  return (
    <svg
      width="15"
      height="15"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {children}
    </svg>
  );
}

/** A gear, of the settings. */
function GearIcon() {
  return (
    <Icon>
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" />
    </Icon>
  );
}

/** A chart of candles, of the dashboard. */
function ChartIcon() {
  return (
    <Icon>
      <path d="M3 3v18h18" />
      <path d="M8 8v7" />
      <path d="M13 5v9" />
      <path d="M18 9v6" />
    </Icon>
  );
}

/** A sun, of the light theme the button switches to. */
function SunIcon() {
  return (
    <Icon>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41" />
    </Icon>
  );
}

/** A crescent moon, of the dark theme the button switches to. */
function MoonIcon() {
  return (
    <Icon>
      <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
    </Icon>
  );
}

/** A door with an arrow leaving it. */
function LogoutIcon() {
  return (
    <Icon>
      <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
      <path d="M16 17l5-5-5-5" />
      <path d="M21 12H9" />
    </Icon>
  );
}
