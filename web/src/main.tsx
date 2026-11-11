import { type ReactNode, StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { AccessProvider, useAccessState } from "./components/AccessContext";
import { BacktestingPage } from "./components/BacktestingPage";
import { Login } from "./components/Login";
import { NoAccess } from "./components/NoAccess";
import { PerformancePage } from "./components/PerformancePage";
import { SettingsPage } from "./components/SettingsPage";
import { canSee, landingPage, pageOf } from "./lib/access";
import { useValuesHidden } from "./lib/hiddenValues";
import "./fonts.css";
import "./styles.css";

const root = document.getElementById("root");
if (!root) throw new Error("#root not found");

// the server serves the same page on /login, for the users without a session,
// on /settings, on /performance and on /backtesting
function page() {
  const path = window.location.pathname;
  if (path.startsWith("/settings")) return <SettingsPage />;
  if (path.startsWith("/performance")) return <PerformancePage />;
  if (path.startsWith("/backtesting")) return <BacktestingPage />;
  return <App />;
}

/**
 * The page once the access of the user is known: a page its role doesn't
 * show leads to the first one it does, or to the page without access.
 */
function Allowed({ children }: { children: ReactNode }) {
  const access = useAccessState();
  if (access === null) {
    return (
      <div className="loading-block" role="status" aria-live="polite">
        <span className="sr-only">Carregando…</span>
      </div>
    );
  }
  if (!canSee(access, pageOf(window.location.pathname))) {
    const landing = landingPage(access);
    if (landing) {
      window.location.replace(landing);
      return null;
    }
    return <NoAccess role={access.role?.name} />;
  }
  return children;
}

/** The page, drawn again whole as the amounts are hidden or shown, see useValuesHidden. */
function Root() {
  useValuesHidden();
  if (window.location.pathname.startsWith("/login")) return <Login />;
  return (
    <AccessProvider>
      <Allowed>{page()}</Allowed>
    </AccessProvider>
  );
}

createRoot(root).render(
  <StrictMode>
    <Root />
  </StrictMode>,
);
