import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { Login } from "./components/Login";
import { SettingsPage } from "./components/SettingsPage";
import "./fonts.css";
import "./styles.css";

const root = document.getElementById("root");
if (!root) throw new Error("#root not found");

// the server serves the same page on /login, for the users without a session,
// and on /settings
function page() {
  const path = window.location.pathname;
  if (path.startsWith("/login")) return <Login />;
  if (path.startsWith("/settings")) return <SettingsPage />;
  return <App />;
}

createRoot(root).render(<StrictMode>{page()}</StrictMode>);
