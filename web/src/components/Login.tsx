import { type FormEvent, useEffect, useRef, useState } from "react";
import { APIError, fetchSession, login } from "../api/client";
import { LOGIN_CLOSED, loginError, nextPage } from "../lib/auth";
import { loadTheme } from "../lib/theme";
import { Footer } from "./Footer";
import { Icon } from "./Icon";
import { LogoMark } from "./Logo";
import { Alert } from "./States";

/** Checking the session, the form ready, or the dashboard without credentials. */
type State = "checking" | "ready" | "closed";

/**
 * The candles drawn behind the card, faded: the direction and the height of
 * each, as a chart of the day.
 */
const CANDLES: [up: boolean, body: number, wick: number][] = [
  [true, 18, 30],
  [true, 26, 38],
  [false, 14, 24],
  [true, 30, 44],
  [true, 22, 30],
  [false, 34, 46],
  [false, 20, 28],
  [true, 16, 26],
  [true, 38, 52],
  [false, 12, 22],
  [true, 28, 36],
  [true, 44, 58],
  [false, 24, 34],
  [true, 20, 32],
  [false, 30, 40],
  [true, 36, 50],
  [true, 26, 34],
  [false, 18, 30],
  [true, 42, 56],
  [true, 22, 30],
  [false, 16, 26],
  [true, 32, 44],
  [false, 28, 38],
  [true, 48, 62],
];

/**
 * The circuit drawn above the candles, faded: the traces of the automation,
 * the signals running along them, and their nodes, in a box of 1440 by 900.
 */
const TRACES = [
  "M0 140 H220 L260 180 H420 L460 140 H600",
  "M1440 110 H1250 L1210 150 H1030 L990 110 H860",
  "M0 330 H120 L160 290 H300",
  "M1440 360 H1320 L1280 320 H1120",
  "M140 0 V60 L180 100 H340",
  "M1300 0 V70 L1260 110 H1120",
  "M720 0 V90",
  "M0 520 H90 L130 480 H210",
  "M1440 560 H1360 L1320 520 H1240",
];

const NODES: [x: number, y: number][] = [
  [600, 140],
  [860, 110],
  [300, 290],
  [1120, 320],
  [340, 100],
  [1120, 110],
  [720, 90],
  [210, 480],
  [1240, 520],
  [260, 180],
  [1210, 150],
  [160, 290],
  [1280, 320],
];

/** The outline of a gear of teeth teeth around cx, cy, from inner to outer. */
function gear(cx: number, cy: number, outer: number, inner: number, teeth: number): string {
  const step = (2 * Math.PI) / teeth;
  const point = (r: number, a: number) =>
    `${(cx + r * Math.cos(a)).toFixed(1)} ${(cy + r * Math.sin(a)).toFixed(1)}`;
  let path = "";
  for (let i = 0; i < teeth; i++) {
    const a = i * step;
    path += `${i === 0 ? "M" : "L"}${point(inner, a)} L${point(outer, a + step * 0.15)} L${point(outer, a + step * 0.45)} L${point(inner, a + step * 0.6)} `;
  }
  return `${path}Z`;
}

const GEARS = [
  { d: gear(1180, 232, 50, 40, 12), hub: [1180, 232, 13] as const, className: "cw" },
  { d: gear(1263, 296, 33, 25, 8), hub: [1263, 296, 9] as const, className: "ccw" },
];

/** The page of the login: the credentials of the controller open a session. */
export function Login() {
  const [state, setState] = useState<State>("checking");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const user = useRef<HTMLInputElement>(null);

  useEffect(() => {
    document.documentElement.dataset.theme = loadTheme();
  }, []);

  // the username focused once the form is ready
  useEffect(() => {
    if (state === "ready") user.current?.focus();
  }, [state]);

  // a session open already goes on to the dashboard
  useEffect(() => {
    const controller = new AbortController();
    fetchSession(controller.signal)
      .then((authenticated) => {
        if (authenticated)
          window.location.replace(nextPage(window.location.search, window.location.origin));
        else setState("ready");
      })
      .catch((e) => {
        if (controller.signal.aborted) return;
        setState(e instanceof APIError && e.status === 503 ? "closed" : "ready");
      });
    return () => controller.abort();
  }, []);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      await login(username, password);
      window.location.replace(nextPage(window.location.search, window.location.origin)); // busy until it leaves
    } catch (e) {
      if (e instanceof APIError && e.status === 503) setState("closed");
      setError(loginError(e));
      setPassword("");
      setBusy(false);
    }
  };

  const disabled = busy || state !== "ready";
  return (
    <main className="login">
      <svg
        className="login-backdrop"
        viewBox="0 0 480 120"
        preserveAspectRatio="none"
        aria-hidden="true"
      >
        {CANDLES.map(([up, body, wick], i) => {
          const x = 10 + i * 19.6;
          const base = 100 - i * 2.4;
          return (
            // biome-ignore lint/suspicious/noArrayIndexKey: a fixed drawing
            <g key={i} className={up ? "up" : "down"}>
              <line x1={x} x2={x} y1={base - wick} y2={base} />
              <rect x={x - 4} y={base - (wick + body) / 2} width={8} height={body} rx={1.5} />
            </g>
          );
        })}
      </svg>
      <svg
        className="login-circuit"
        viewBox="0 0 1440 900"
        preserveAspectRatio="xMidYMid slice"
        aria-hidden="true"
      >
        {TRACES.map((d) => (
          <path key={d} className="trace" d={d} />
        ))}
        {TRACES.map((d, i) => (
          <path
            key={`pulse-${d}`}
            className="pulse"
            d={d}
            pathLength={1000}
            style={{ animationDelay: `${(i * 0.9) % 6}s` }}
          />
        ))}
        {NODES.map(([x, y], i) => (
          <circle
            key={`${x}-${y}`}
            className={i % 3 === 0 ? "node blink" : "node"}
            cx={x}
            cy={y}
            r={i < 9 ? 4 : 2.5}
            style={i % 3 === 0 ? { animationDelay: `${i * 0.4}s` } : undefined}
          />
        ))}
        {GEARS.map(({ d, hub: [cx, cy, r], className }) => (
          <g key={d} className={`gear ${className}`} style={{ transformOrigin: `${cx}px ${cy}px` }}>
            <path d={d} />
            <circle cx={cx} cy={cy} r={r} />
          </g>
        ))}
      </svg>

      <section className="login-card" aria-labelledby="login-title">
        <span className="login-mark" role="img" aria-label="XTrader">
          <LogoMark size={40} />
        </span>
        <h1 id="login-title" className="login-title">
          Trader Cockpit
        </h1>
        <p className="login-subtitle">
          Entre para acompanhar e controlar suas operações automatizadas
        </p>

        <form className="login-form" onSubmit={submit} noValidate>
          <label className="field-label" htmlFor="login-username">
            Usuário
          </label>
          <span className="input-icon">
            <Icon name="user" />
            <input
              id="login-username"
              className="input input-lg"
              name="username"
              autoComplete="username"
              autoCapitalize="none"
              spellCheck={false}
              ref={user}
              value={username}
              disabled={disabled}
              onChange={(e) => setUsername(e.target.value)}
            />
          </span>
          <label className="field-label" htmlFor="login-password">
            Senha
          </label>
          <span className="input-icon">
            <Icon name="key" />
            <input
              id="login-password"
              className="input input-lg"
              type="password"
              name="password"
              autoComplete="current-password"
              value={password}
              disabled={disabled}
              onChange={(e) => setPassword(e.target.value)}
            />
          </span>
          {error && (
            <Alert tone="error" className="login-error">
              {error}
            </Alert>
          )}
          {state === "closed" && !error && (
            <Alert tone="warn" className="login-error">
              {LOGIN_CLOSED}
            </Alert>
          )}
          <button
            type="submit"
            className="btn btn-primary btn-lg login-submit"
            disabled={disabled || username === "" || password === ""}
          >
            {busy ? "Entrando…" : state === "checking" ? "Verificando sessão…" : "Entrar"}
          </button>
        </form>
      </section>
      <Footer className="login-footer" />
    </main>
  );
}
