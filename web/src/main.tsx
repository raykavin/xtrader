import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { Login } from "./components/Login";
import { PerformancePage } from "./components/PerformancePage";
import { SettingsPage } from "./components/SettingsPage";
import "./fonts.css";
import "./styles.css";

const root = document.getElementById("root");
if (!root) throw new Error("#root not found");

// the server serves the same page on /login, for the users without a session,
// on /settings and on /performance
function page() {
  const path = window.location.pathname;
  if (path.startsWith("/login")) return <Login />;
  if (path.startsWith("/settings")) return <SettingsPage />;
  if (path.startsWith("/performance")) return <PerformancePage />;
  return <App />;
}

createRoot(root).render(<StrictMode>{page()}</StrictMode>);
