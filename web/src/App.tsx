import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  cancelOrder,
  fetchAgents,
  fetchControls,
  fetchDecisions,
  fetchPairs,
  fetchSnapshot,
  moveOrder,
  subscribeEvents,
  UI_VERSION,
} from "./api/client";
import type { AgentStatus, ControlsResponse, Decision, Order, Snapshot } from "./api/types";
import { AgentBar } from "./components/AgentBar";
import { ControlPanel } from "./components/ControlPanel";
import { DecisionDialog } from "./components/DecisionDialog";
import { DecisionsPanel } from "./components/DecisionsPanel";
import { EquityChart } from "./components/EquityChart";
import { Footer } from "./components/Footer";
import { Header } from "./components/Header";
import { OrdersTable } from "./components/OrdersTable";
import { PriceChart } from "./components/PriceChart";
import { StatsBar } from "./components/StatsBar";
import { pickAgent } from "./lib/agents";
import { applyDecisionEvent, DECISIONS, linkOrders, reanalysisState } from "./lib/decisions";
import {
  applyCandleEvent,
  applyOrderEvent,
  computeStats,
  indicatorsChanged,
  indicatorsKey,
  positionPnL,
} from "./lib/stats";
import { loadTheme, saveTheme, type Theme } from "./lib/theme";
import { applyPendingOrder, noLevels, type TicketLevels } from "./lib/ticket";

function pairFromURL(): string | null {
  return new URLSearchParams(window.location.search).get("pair");
}

function setPairInURL(pair: string) {
  const url = new URL(window.location.href);
  url.searchParams.set("pair", pair);
  window.history.replaceState(null, "", url);
}

function agentFromURL(): string | null {
  return new URLSearchParams(window.location.search).get("agent");
}

function setAgentInURL(agent: string) {
  const url = new URL(window.location.href);
  url.searchParams.set("agent", agent);
  window.history.replaceState(null, "", url);
}

export function App() {
  const [theme, setTheme] = useState<Theme>(loadTheme);
  const [agents, setAgents] = useState<AgentStatus[]>([]);
  const [agent, setAgent] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    saveTheme(theme);
  }, [theme]);

  useEffect(() => {
    if (agent) setAgentInURL(agent);
  }, [agent]);

  // The agents of the configuration, polled for the ones online.
  useEffect(() => {
    const controller = new AbortController();
    const load = () => {
      fetchAgents(controller.signal)
        .then((list) => {
          setAgents(list);
          setLoaded(true);
          setError(null);
          setAgent((current) => pickAgent(list, current ?? agentFromURL()));
        })
        .catch((e) => {
          if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
        });
    };
    load();
    const timer = window.setInterval(load, 10000);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, []);

  const toggleTheme = useCallback(() => setTheme((t) => (t === "dark" ? "light" : "dark")), []);

  if (!agent) {
    return (
      <div className="app">
        <Header theme={theme} onThemeToggle={toggleTheme} version={UI_VERSION} />
        <main>
          {error ? (
            <div className="banner error">{error}</div>
          ) : loaded && agents.length === 0 ? (
            // the dashboard serves the agents turned on only
            <div className="banner">
              Nenhum agente ligado: crie ou ligue um em <a href="/settings">Configurações</a>.
            </div>
          ) : (
            <div className="banner">Carregando agentes…</div>
          )}
        </main>
        <Footer />
      </div>
    );
  }
  // Each agent gets a dashboard of its own, its state starting over.
  return (
    <Dashboard
      key={agent}
      agent={agent}
      agents={agents}
      onAgentChange={setAgent}
      theme={theme}
      onThemeToggle={toggleTheme}
    />
  );
}

interface DashboardProps {
  agent: string;
  agents: AgentStatus[];
  onAgentChange: (agent: string) => void;
  theme: Theme;
  onThemeToggle: () => void;
}

function Dashboard({ agent, agents, onAgentChange, theme, onThemeToggle }: DashboardProps) {
  const [pairs, setPairs] = useState<string[]>([]);
  const [pair, setPair] = useState<string | null>(null);
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [connected, setConnected] = useState(false);
  const [controls, setControls] = useState<ControlsResponse | null>(null);
  const [levels, setLevels] = useState<TicketLevels>(noLevels);
  const [decisions, setDecisions] = useState<Decision[]>([]);
  const [decisionsError, setDecisionsError] = useState<string | null>(null);
  const [shownDecision, setShownDecision] = useState<string | null>(null);
  const [reloads, setReloads] = useState(0);
  const pairRef = useRef<string | null>(pair);
  pairRef.current = pair;
  const snapshotRef = useRef<Snapshot | null>(snapshot);
  snapshotRef.current = snapshot;
  const reloadedFor = useRef<string | null>(null);

  // The order ticket belongs to a pair.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reset on pair change only
  useEffect(() => setLevels(noLevels), [pair]);

  const setLevel = useCallback(
    (field: keyof TicketLevels, price: number) => setLevels((l) => ({ ...l, [field]: price })),
    [],
  );
  const handleMoveOrder = useCallback(
    async (order: Order, price: number) => {
      const moved = await moveOrder(agent, order.id, price);
      setSnapshot((s) => (s ? applyPendingOrder(s, moved) : s));
    },
    [agent],
  );
  const handleCancelOrder = useCallback(
    async (order: Order) => {
      const canceling = await cancelOrder(agent, order.id);
      setSnapshot((s) => (s ? applyPendingOrder(s, canceling) : s));
    },
    [agent],
  );

  // Load the pair list; keep polling while the bot has no data yet. The pair
  // of the URL, of the agent shown before, is kept when this one trades it.
  useEffect(() => {
    const controller = new AbortController();
    let timer: number | undefined;
    const load = async () => {
      try {
        const list = await fetchPairs(agent, controller.signal);
        setPairs(list);
        if (list.length === 0) {
          timer = window.setTimeout(load, 3000);
          return;
        }
        setPair((current) => {
          const wanted = current ?? pairFromURL();
          return wanted && list.includes(wanted) ? wanted : (list[0] ?? null);
        });
      } catch (e) {
        if (controller.signal.aborted) return;
        setError(e instanceof Error ? e.message : String(e));
        timer = window.setTimeout(load, 5000);
      }
    };
    load();
    return () => {
      controller.abort();
      if (timer) window.clearTimeout(timer);
    };
  }, [agent]);

  // Load the snapshot of the selected pair, and again as the indicators of
  // the strategy change, eg: its prompt switched.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reloaded as the indicators change
  useEffect(() => {
    if (!pair) return;
    setPairInURL(pair);
    const controller = new AbortController();
    setLoading(true);
    fetchSnapshot(agent, pair, controller.signal)
      .then((s) => {
        setSnapshot(s);
        setError(null);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [agent, pair, reloads]);

  // The decisions of the language model of the strategy, when it keeps them:
  // loaded with the pair, then followed on the events.
  const keepsDecisions = snapshot?.llm?.decisions === true && snapshot.pair === pair;
  useEffect(() => {
    setDecisions([]);
    setDecisionsError(null);
    if (!pair || !keepsDecisions) return;
    const controller = new AbortController();
    fetchDecisions(agent, pair, DECISIONS, controller.signal)
      .then(setDecisions)
      .catch((e) => {
        if (!controller.signal.aborted)
          setDecisionsError(e instanceof Error ? e.message : String(e));
      });
    return () => controller.abort();
  }, [agent, pair, keepsDecisions]);

  // Bot control state: SSE pushes changes made from this dashboard, polling
  // catches changes made elsewhere (Telegram, another browser).
  useEffect(() => {
    const controller = new AbortController();
    const load = () => {
      fetchControls(agent, controller.signal)
        .then(setControls)
        .catch(() => {
          // leave the last known state; polling retries
        });
    };
    load();
    const timer = window.setInterval(load, 10000);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, [agent]);

  // Live updates.
  useEffect(() => {
    return subscribeEvents(agent, {
      onStatus: setConnected,
      onCandle: (event) => {
        setPairs((list) => (list.includes(event.pair) ? list : [...list, event.pair].sort()));
        const current = snapshotRef.current;
        if (current && indicatorsChanged(current, event)) {
          // once for the indicators of the event: a snapshot showing others
          // isn't loaded over and over
          const key = indicatorsKey(event);
          if (reloadedFor.current !== key) {
            reloadedFor.current = key;
            setReloads((n) => n + 1);
          }
        }
        setSnapshot((s) => (s ? applyCandleEvent(s, event) : s));
      },
      onOrder: (event) => {
        setSnapshot((s) => (s ? applyOrderEvent(s, event) : s));
      },
      onControls: setControls,
      onDecision: (event) => {
        setDecisions((list) => applyDecisionEvent(list, event, pairRef.current));
        setSnapshot((s) => (s ? linkOrders(s, event.decision) : s));
      },
    });
  }, [agent]);

  const stats = useMemo(() => (snapshot ? computeStats(snapshot) : null), [snapshot]);
  const pnl = useMemo(() => (snapshot ? positionPnL(snapshot) : null), [snapshot]);
  const interactive = controls?.enabled === true;
  const ticket = useMemo(() => ({ levels, onLevel: setLevel }), [levels, setLevel]);

  return (
    <div className="app">
      <Header
        theme={theme}
        onThemeToggle={onThemeToggle}
        version={UI_VERSION}
        connected={connected}
      />
      <main>
        <AgentBar
          agents={agents}
          agent={agent}
          onAgentChange={onAgentChange}
          pairs={pairs}
          pair={pair}
          onPairChange={setPair}
          llm={snapshot?.llm?.model}
          controls={controls}
          onControlsChange={setControls}
        />
        {error && <div className="banner error">{error}</div>}
        {!snapshot && !error && (
          <div className="banner">
            {pairs.length === 0 ? "Aguardando o primeiro candle…" : "Carregando…"}
          </div>
        )}
        {snapshot && stats && (
          <div className={loading ? "content loading" : "content"}>
            <StatsBar stats={stats} snapshot={snapshot} theme={theme} pnl={pnl} />
            <div className="trade-area">
              <PriceChart
                snapshot={snapshot}
                theme={theme}
                layoutKey={agent ?? ""}
                ticket={interactive ? ticket : undefined}
                onMoveOrder={interactive ? handleMoveOrder : undefined}
              />
              <ControlPanel
                agent={agent}
                pair={snapshot.pair}
                asset={snapshot.asset}
                quote={snapshot.quote}
                lastPrice={snapshot.candles[snapshot.candles.length - 1]?.close ?? 0}
                controls={controls ?? { enabled: false, status: "" }}
                levels={levels}
                onLevelsChange={setLevels}
              />
            </div>
            {snapshot.equity_values.length > 0 && <EquityChart snapshot={snapshot} theme={theme} />}
            {snapshot.llm?.decisions && (
              <DecisionsPanel
                key={`decisions-${snapshot.pair}`} // the first page of each pair
                agent={agent}
                pair={snapshot.pair}
                model={snapshot.llm.model}
                decisions={decisions}
                error={decisionsError}
                reanalysis={reanalysisState(controls)}
                onShow={setShownDecision}
              />
            )}
            <OrdersTable
              key={`orders-${snapshot.pair}`} // the first page of each pair
              agent={agent}
              pair={snapshot.pair}
              quote={snapshot.quote}
              accountCurrency={snapshot.account_currency}
              orders={snapshot.orders}
              onCancel={interactive ? handleCancelOrder : undefined}
              onShowDecision={snapshot.llm?.decisions ? setShownDecision : undefined}
            />
            {shownDecision && (
              <DecisionDialog
                agent={agent}
                decisionId={shownDecision}
                quote={snapshot.quote}
                onClose={() => setShownDecision(null)}
              />
            )}
          </div>
        )}
      </main>
      <Footer />
    </div>
  );
}
