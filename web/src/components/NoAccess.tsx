import { useState } from "react";
import { logout } from "../api/client";
import { Footer } from "./Footer";
import { Icon } from "./Icon";
import { LogoMark } from "./Logo";
import { Alert } from "./States";

/**
 * The page of a user whose role shows no area of the dashboard: what to do
 * about it, and the logout.
 */
export function NoAccess({ role }: { role?: string }) {
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
    <main className="login">
      <section className="login-card" aria-labelledby="no-access-title">
        <span className="login-mark" role="img" aria-label="XTrader">
          <LogoMark size={40} />
        </span>
        <h1 id="no-access-title" className="login-title">
          Sem acesso
        </h1>
        <p className="login-subtitle">
          {role ? `O papel ${role} não dá acesso` : "Seu papel não dá acesso"} a nenhuma área do
          painel. Peça acesso a um administrador.
        </p>
        {error && (
          <Alert tone="error" className="login-error">
            {error}
          </Alert>
        )}
        <button type="button" className="btn btn-primary" onClick={leave} disabled={leaving}>
          <Icon name="logout" />
          {leaving ? "Saindo…" : "Sair"}
        </button>
      </section>
      <Footer className="login-footer" />
    </main>
  );
}
