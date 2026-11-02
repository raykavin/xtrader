import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { BacktestingPage } from "./components/BacktestingPage";
import { Login } from "./components/Login";
import { PerformancePage } from "./components/PerformancePage";
import { SettingsPage } from "./components/SettingsPage";
import { useValuesHidden } from "./lib/hiddenValues";
import "./fonts.css";
import "./styles.css";

const root = document.getElementById("root");
if (!root) throw new Error("#root not found");

// the server serves the same page on /login, for the users without a session,
// on /settings, on /performance and on /backtesting
function page() {
  const path = window.location.pathname;
  if (path.startsWith("/login")) return <Login />;
  if (path.startsWith("/settings")) return <SettingsPage />;
  if (path.startsWith("/performance")) return <PerformancePage />;
  if (path.startsWith("/backtesting")) return <BacktestingPage />;
  return <App />;
}

/** The page, drawn again whole as the amounts are hidden or shown, see useValuesHidden. */
function Root() {
  useValuesHidden();
  return page();
}

createRoot(root).render(
  <StrictMode>
    <Root />
  </StrictMode>,
);
