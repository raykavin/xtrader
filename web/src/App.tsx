import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  cancelOrder,
  fetchAgents,
  fetchCandles,
  fetchControls,
  fetchDecisions,
  fetchPairs,
  fetchSnapshot,
  moveOrder,
  subscribeEvents,
} from "./api/client";
import type {
  AgentStatus,
  Analysis,
  ControlsResponse,
  Decision,
  Order,
  Snapshot,
} from "./api/types";
import { AgentBar } from "./components/AgentBar";
import { AppShell } from "./components/AppShell";
import { ControlPanel } from "./components/ControlPanel";
import { DailyGuardBanner } from "./components/DailyGuardStrip";
import { DashboardCards, useDashboardLayout } from "./components/DashboardCards";
import { DecisionDialog } from "./components/DecisionDialog";
import { DecisionsPanel } from "./components/DecisionsPanel";
import { EquityChart } from "./components/EquityChart";
import { Icon } from "./components/Icon";
import { LastDecision } from "./components/LastDecision";
import { OpenProfitCard } from "./components/OpenProfit";
import { OrdersTable } from "./components/OrdersTable";
import { PriceChart } from "./components/PriceChart";
import { PriceHero } from "./components/PriceHero";
import { SentimentMeter } from "./components/SentimentMeter";
import { Alert, EmptyState, Loading, Skeleton } from "./components/States";
import { StatsBar } from "./components/StatsBar";
import { type TabItem, Tabs, tabIds } from "./components/Tabs";
import { pickAgent } from "./lib/agents";
import {
  applyAnalysisEvent,
  applyDecisionEvent,
  DECISIONS,
  linkOrders,
  reanalysisState,
} from "./lib/decisions";
import type { CardId } from "./lib/layout";
import { rememberDashboard, sessionStore, settingsHref } from "./lib/nav";
import { decisionsLoad, keepsDecisions, noResyncs, resynced } from "./lib/resync";
import { latestSentiment } from "./lib/sentiment";
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
import {
  applyViewPrice,
  freshGap,
  mergeView,
  shownTimeframe,
  timeframeLabel,
  type ViewCandles,
  viewKey,
  viewOf,
  viewSnapshot,
  viewTimeframes,
} from "./lib/timeframes";

function pairFromURL(): string | null {
  return new URLSearchParams(window.location.search).get("pair");
}

function setPairInURL(pair: string) {
  const url = new URL(window.location.href);
  url.searchParams.set("pair", pair);
  window.history.replaceState(null, "", url);
}

function timeframeFromURL(): string | null {
  return new URLSearchParams(window.location.search).get("tf");
}

function setTimeframeInURL(timeframe: string | null) {
  const url = new URL(window.location.href);
  if (timeframe) url.searchParams.set("tf", timeframe);
  else url.searchParams.delete("tf");
  window.history.replaceState(null, "", url);
}

/** How often the candles of a timeframe shown are read again, in ms. */
const VIEW_REFRESH = 15000;
/** The candles read again, the last ones, merged into the ones shown. */
const VIEW_REFRESH_CANDLES = 60;
/** The time between two reads of the candles shown asked by the prices, in ms. */
const VIEW_STALE_WAIT = 3000;

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
      <AppShell page="dashboard" title="Painel" theme={theme} onThemeToggle={toggleTheme}>
        {error ? (
          <Alert tone="error" title="Não foi possível carregar os agentes">
            {error}
          </Alert>
        ) : loaded && agents.length === 0 ? (
          // the dashboard serves the agents turned on only
          <EmptyState
            icon="bot"
            title="Nenhum agente ligado"
            action={
              <a className="btn btn-primary" href={settingsHref("agentes")}>
                <Icon name="settings" />
                Ir para Agentes
              </a>
            }
          >
            O painel mostra os agentes ativos. Crie um agente ou ative um existente nas
            configurações.
          </EmptyState>
        ) : (
          <DashboardSkeleton label="Carregando agentes…" />
        )}
      </AppShell>
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
  // whether the decisions shown are the ones of the pair, read once
  const [decisionsLoaded, setDecisionsLoaded] = useState(false);
  const [shownDecision, setShownDecision] = useState<string | null>(null);
  // the analysis of a decision of the list shown on the chart of its pair, in
  // place of the last one, until the trader goes back to it
  const [shownAnalysis, setShownAnalysis] = useState<{ pair: string; analysis: Analysis } | null>(
    null,
  );
  const [reloads, setReloads] = useState(0);
  // bumped as the events are back after a drop: what they missed is loaded again
  const [resyncs, setResyncs] = useState(noResyncs);
  // The timeframe the chart is shown on, as asked (null: the one of the
  // strategy), and the candles of one other than the one of the strategy:
  // read from the agent, and kept up to date with its prices. The trading of
  // the strategy stays on its own timeframe whatever the one shown.
  const [askedTimeframe, setAskedTimeframe] = useState<string | null>(timeframeFromURL);
  const [view, setView] = useState<ViewCandles | null>(null);
  const [viewError, setViewError] = useState<string | null>(null);
  const [viewReloads, setViewReloads] = useState(0);
  const shown = snapshot ? shownTimeframe(snapshot, askedTimeframe) : "";
  const execution = snapshot?.timeframe ?? "";
  // the key of the candles shown, null for the ones of the strategy: an
  // answer of another key, eg: late, of the timeframe shown before, is left out
  const shownKey =
    pair && snapshot?.pair === pair && shown && execution && shown !== execution
      ? viewKey(agent, pair, shown)
      : null;
  const shownKeyRef = useRef(shownKey);
  shownKeyRef.current = shownKey;
  const viewRef = useRef(view);
  viewRef.current = view;
  const viewReadAt = useRef(0);
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
  // the strategy change, eg: its prompt switched, and as the events are back
  // after a drop.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reloaded as the indicators change or the events are back
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
  }, [agent, pair, reloads, resyncs.snapshot]);

  useEffect(() => setTimeframeInURL(askedTimeframe), [askedTimeframe]);

  // The candles of the timeframe shown, other than the one of the strategy:
  // read whole as it is chosen, and again as the events are back after a drop
  // or after a gap; the ones of another timeframe never shown meanwhile.
  // biome-ignore lint/correctness/useExhaustiveDependencies: read again as the events are back
  useEffect(() => {
    setView((current) => (current?.key === shownKey ? current : null));
    setViewError(null);
    if (!shownKey || !pair) return;
    const key = shownKey;
    const controller = new AbortController();
    fetchCandles(agent, pair, shown, 0, controller.signal)
      .then((response) => {
        if (shownKeyRef.current !== key) return;
        viewReadAt.current = Date.now();
        setView(viewOf(key, response));
        setViewError(null);
      })
      .catch((e) => {
        if (controller.signal.aborted || shownKeyRef.current !== key) return;
        setViewError(e instanceof Error ? e.message : String(e));
      });
    return () => controller.abort();
  }, [agent, pair, shown, shownKey, viewReloads, resyncs.snapshot]);

  // The last candles of the timeframe shown read again, merged into the ones
  // shown: the ones complete at the broker in place of the ones in progress.
  const refreshView = useRef<() => void>(() => {});
  useEffect(() => {
    if (!shownKey || !pair) {
      refreshView.current = () => {};
      return;
    }
    const key = shownKey;
    const controller = new AbortController();
    const refresh = () => {
      const current = viewRef.current;
      if (!current || current.key !== key) return;
      viewReadAt.current = Date.now();
      fetchCandles(agent, pair, shown, VIEW_REFRESH_CANDLES, controller.signal)
        .then((response) => {
          if (shownKeyRef.current !== key) return;
          const before = viewRef.current;
          if (before && before.key === key && freshGap(before, response.candles)) {
            setViewReloads((n) => n + 1); // missed some: read whole again
            return;
          }
          setView((v) => mergeView(v, key, response.candles));
        })
        .catch(() => {
          // the candles shown kept; read again on the next turn
        });
    };
    refreshView.current = refresh;
    const timer = window.setInterval(refresh, VIEW_REFRESH);
    return () => {
      controller.abort();
      window.clearInterval(timer);
      refreshView.current = () => {};
    };
  }, [agent, pair, shown, shownKey]);

  // The decisions of the language model of the strategy, when it keeps them:
  // loaded with the pair, then followed on the events, and loaded again as
  // the events are back after a drop, the ones shown kept meanwhile.
  const keeps = keepsDecisions(snapshot, pair);
  const decisionsFor = useRef<string | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: reloaded as the events are back
  useEffect(() => {
    const run = decisionsLoad(decisionsFor.current, agent, pair, keeps);
    if (run.clear) {
      decisionsFor.current = run.key;
      setDecisions([]);
      setDecisionsError(null);
      setDecisionsLoaded(false);
    }
    if (!pair || !run.load) return;
    const controller = new AbortController();
    fetchDecisions(agent, pair, DECISIONS, controller.signal)
      .then((list) => {
        setDecisions(list);
        setDecisionsError(null);
        setDecisionsLoaded(true);
      })
      .catch((e) => {
        if (!controller.signal.aborted)
          setDecisionsError(e instanceof Error ? e.message : String(e));
      });
    return () => controller.abort();
  }, [agent, pair, keeps, resyncs.decisions]);

  // Bot control state: SSE pushes changes made from this dashboard, polling
  // catches changes made elsewhere (Telegram, another browser), loaded at once
  // as the events are back after a drop.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reloaded as the events are back
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
  }, [agent, resyncs.controls]);

  // Live updates.
  useEffect(() => {
    return subscribeEvents(agent, {
      onStatus: setConnected,
      onResync: () => setResyncs(resynced),
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
        // the price in the candle of the timeframe shown, read again once
        // its period is over
        const shownView = viewRef.current;
        if (shownView && shownView.pair === event.pair) {
          const now = Math.max(Date.now() / 1000, event.candle.time);
          const { stale } = applyViewPrice(shownView, event.candle.close, now);
          if (stale) {
            if (Date.now() - viewReadAt.current >= VIEW_STALE_WAIT) refreshView.current();
          } else {
            setView((v) =>
              v && v.key === shownView.key ? applyViewPrice(v, event.candle.close, now).view : v,
            );
          }
        }
      },
      onOrder: (event) => {
        setSnapshot((s) => (s ? applyOrderEvent(s, event) : s));
      },
      onControls: setControls,
      onDecision: (event) => {
        setDecisions((list) => applyDecisionEvent(list, event, pairRef.current));
        setSnapshot((s) => (s ? applyAnalysisEvent(linkOrders(s, event.decision), event) : s));
      },
    });
  }, [agent]);

  // the trades closed since the last reset of the results of the agent count
  const agentStatus = agents.find((a) => a.name === agent);
  const since = agentStatus?.period?.started_at;
  // the kind of the agent, known before its snapshot: the blocks of its cards while it loads
  const kind = agentStatus?.kind;
  const stats = useMemo(
    () => (snapshot ? computeStats(snapshot, { since }) : null),
    [snapshot, since],
  );
  const pnl = useMemo(() => (snapshot ? positionPnL(snapshot) : null), [snapshot]);
  // the reading of the market of the newest decision of the pair with one
  const sentiment = useMemo(() => latestSentiment(decisions), [decisions]);
  // the snapshot drawn on the chart: on the timeframe shown, its candles, none
  // while they load (never the ones of another timeframe)
  const chartSnapshot = useMemo(() => {
    if (!snapshot || !shownKey) return snapshot;
    if (view && view.key === shownKey) return viewSnapshot(snapshot, view);
    return { ...snapshot, candles: [], indicators: [] };
  }, [snapshot, shownKey, view]);
  const timeframeNote = !shownKey
    ? null
    : viewError
      ? { text: `Candles de ${timeframeLabel(shown)}: ${viewError}`, error: true }
      : view?.key !== shownKey
        ? { text: `Carregando candles de ${timeframeLabel(shown)}…` }
        : null;
  const interactive = controls?.enabled === true;
  const ticket = useMemo(() => ({ levels, onLevel: setLevel }), [levels, setLevel]);

  // the query of the panel, kept for the way back from the settings
  // biome-ignore lint/correctness/useExhaustiveDependencies: kept as the agent, the pair or the timeframe change the URL
  useEffect(() => {
    rememberDashboard(window.location.search, sessionStore());
  }, [agent, pair, askedTimeframe]);

  // The cards and the lists of the strategy only: the sentiment, the last
  // decision and the decisions of an LLM agent, its decisions kept, none of
  // another agent, which the layout keeps their place for (absent, not hidden).
  const hasDecisions = snapshot?.llm?.decisions === true;
  const hasEquity = (snapshot?.equity_values.length ?? 0) > 0;
  const [tab, setTab] = useState<DashboardTab>(loadDashboardTab);
  // the layout of the cards, as arranged in this browser, and whether it is being customized
  const { layout, update: setLayout, reset: resetLayout } = useDashboardLayout();
  const [customizing, setCustomizing] = useState(false);
  const shownTab: DashboardTab =
    (tab === "decisions" && !hasDecisions) || (tab === "equity" && !hasEquity) ? "orders" : tab;
  const chooseTab = (next: DashboardTab) => {
    setTab(next);
    saveDashboardTab(next);
  };
  const tabs: TabItem<DashboardTab>[] = [
    { id: "orders", label: "Ordens", icon: "list", count: snapshot?.orders.length ?? 0 },
    ...(hasDecisions
      ? [
          {
            id: "decisions" as const,
            label: "Decisões do LLM",
            icon: "sparkles" as const,
            count: decisions.length,
          },
        ]
      : []),
    ...(hasEquity ? [{ id: "equity" as const, label: "Patrimônio", icon: "trend" as const }] : []),
  ];
  const panel = (id: DashboardTab) => ({
    role: "tabpanel",
    id: tabIds("lists", id).panel,
    "aria-labelledby": tabIds("lists", id).tab,
    hidden: shownTab !== id,
    className: "tab-panel",
  });

  return (
    <AppShell
      page="dashboard"
      title="Painel"
      theme={theme}
      onThemeToggle={onThemeToggle}
      connected={connected}
      actions={
        snapshot && (
          <button
            type="button"
            className={customizing ? "btn btn-icon btn-primary" : "btn btn-ghost btn-icon"}
            aria-pressed={customizing}
            aria-label="Personalizar o layout do painel"
            title={
              customizing
                ? "Concluir a personalização do layout"
                : "Personalizar o layout: mover, ocultar e redimensionar os cards"
            }
            onClick={() => setCustomizing(!customizing)}
          >
            <Icon name="layout" />
          </button>
        )
      }
    >
      <AgentBar
        agents={agents}
        agent={agent}
        onAgentChange={onAgentChange}
        pairs={pairs}
        pair={pair}
        onPairChange={setPair}
        llm={snapshot?.llm?.model}
        timeframe={snapshot?.timeframe}
        controls={controls}
        onControlsChange={setControls}
      />
      <DailyGuardBanner />
      {error && (
        <Alert tone="error" title="Não foi possível carregar os dados do agente">
          {error}
        </Alert>
      )}
      {!snapshot && !error && (
        <DashboardSkeleton
          label={pairs.length === 0 ? "Aguardando o primeiro candle…" : "Carregando…"}
          waiting={pairs.length === 0}
          meter={kind === "llm"}
        />
      )}
      {snapshot && chartSnapshot && stats && (
        <div className={loading ? "content loading" : "content"} aria-busy={loading}>
          <DashboardCards
            layout={layout}
            onLayout={setLayout}
            editing={customizing}
            onEditingChange={setCustomizing}
            onReset={resetLayout}
            cards={
              {
                quote: (
                  <PriceHero snapshot={snapshot} buyAndHold={stats.buyAndHold} theme={theme} />
                ),
                sentiment: hasDecisions ? (
                  <SentimentMeter
                    reading={sentiment}
                    loaded={decisionsLoaded}
                    error={decisionsError}
                  />
                ) : undefined,
                // of all the agents, whichever is shown
                openpl: <OpenProfitCard theme={theme} />,
                results: (
                  <StatsBar
                    stats={stats}
                    snapshot={snapshot}
                    theme={theme}
                    since={since}
                    pnl={pnl}
                  />
                ),
                chart: (
                  <PriceChart
                    snapshot={chartSnapshot}
                    theme={theme}
                    layoutKey={agent ?? ""}
                    ticket={interactive ? ticket : undefined}
                    onMoveOrder={interactive ? handleMoveOrder : undefined}
                    position={pnl}
                    timeframe={shown}
                    timeframes={viewTimeframes(snapshot)}
                    onTimeframeChange={setAskedTimeframe}
                    timeframeNote={timeframeNote}
                    analysis={chartAnalysis(
                      snapshot.pair,
                      shownAnalysis?.pair === snapshot.pair
                        ? shownAnalysis.analysis
                        : snapshot.analysis,
                    )}
                    onAnalysisReset={
                      shownAnalysis?.pair === snapshot.pair
                        ? () => setShownAnalysis(null)
                        : undefined
                    }
                  />
                ),
                decision: hasDecisions ? (
                  <LastDecision
                    decision={decisions[0]}
                    loaded={decisionsLoaded}
                    error={decisionsError}
                    onShow={setShownDecision}
                  />
                ) : undefined,
                ticket: (
                  <ControlPanel
                    agent={agent}
                    pair={snapshot.pair}
                    asset={snapshot.asset}
                    quote={snapshot.quote}
                    accountCurrency={snapshot.account_currency}
                    pointValue={snapshot.point_value}
                    simulated={snapshot.simulated}
                    lastPrice={snapshot.candles[snapshot.candles.length - 1]?.close ?? 0}
                    controls={controls}
                    levels={levels}
                    onLevelsChange={setLevels}
                  />
                ),
                activity: (
                  <section className="card panel lists" aria-label="Ordens, decisões e patrimônio">
                    <div className="lists-head">
                      <span className="eyebrow">Atividade do agente</span>
                      <Tabs
                        label="Listas do agente"
                        idPrefix="lists"
                        items={tabs}
                        value={shownTab}
                        onChange={chooseTab}
                      />
                    </div>
                    <div {...panel("orders")}>
                      <OrdersTable
                        key={`orders-${snapshot.pair}`} // the first page of each pair
                        agent={agent}
                        pair={snapshot.pair}
                        quote={snapshot.quote}
                        accountCurrency={snapshot.account_currency}
                        simulated={snapshot.simulated}
                        orders={snapshot.orders}
                        onCancel={interactive ? handleCancelOrder : undefined}
                        onShowDecision={hasDecisions ? setShownDecision : undefined}
                      />
                    </div>
                    {snapshot.llm?.decisions && (
                      <div {...panel("decisions")}>
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
                      </div>
                    )}
                    {hasEquity && (
                      <div {...panel("equity")}>
                        {/* drawn once shown: a chart sizes itself on a visible box */}
                        {shownTab === "equity" && <EquityChart snapshot={snapshot} theme={theme} />}
                      </div>
                    )}
                  </section>
                ),
              } satisfies Partial<Record<CardId, ReactNode>>
            }
          />

          {shownDecision && (
            <DecisionDialog
              agent={agent}
              decisionId={shownDecision}
              quote={snapshot.quote}
              onClose={() => setShownDecision(null)}
              onShowOnChart={(analysis) => setShownAnalysis({ pair: snapshot.pair, analysis })}
            />
          )}
        </div>
      )}
    </AppShell>
  );
}

/** The lists under the chart, a tab each. */
type DashboardTab = "orders" | "decisions" | "equity";

const TAB_KEY = "xtrader.dashboard.tab";

/** The tab of the lists shown last in this browser. */
function loadDashboardTab(): DashboardTab {
  try {
    const saved = window.localStorage.getItem(TAB_KEY);
    if (saved === "orders" || saved === "decisions" || saved === "equity") return saved;
  } catch {
    // no storage: the first tab
  }
  return "orders";
}

function saveDashboardTab(tab: DashboardTab) {
  try {
    window.localStorage.setItem(TAB_KEY, tab);
  } catch {
    // no storage: the first tab next time
  }
}

/**
 * The blocks of the panel while its data loads: the open profit of the agents,
 * the quote, the sentiment of an LLM agent (meter), the results, the chart and
 * the ticket.
 */
function DashboardSkeleton({
  label,
  waiting = false,
  meter = true,
}: {
  label: string;
  waiting?: boolean;
  meter?: boolean;
}) {
  return (
    <Loading label={label}>
      {waiting && (
        <Alert tone="info" title="Aguardando dados">
          O agente ainda não recebeu o primeiro candle. O painel aparece assim que ele chegar.
        </Alert>
      )}
      <Skeleton className="skeleton-openpl" />
      <div className={meter ? "overview" : "overview no-meter"}>
        <Skeleton className="skeleton-hero" />
        {meter && <Skeleton className="skeleton-meter" />}
        <Skeleton className="skeleton-kpis" />
      </div>
      <div className="workspace">
        <Skeleton className="skeleton-chart" />
        <Skeleton className="skeleton-ticket" />
      </div>
    </Loading>
  );
}

/** The analysis drawn on the chart of pair: none of another pair. */
function chartAnalysis(pair: string, analysis: Analysis | null | undefined): Analysis | null {
  if (!analysis || (analysis.pair && analysis.pair !== pair)) return null;
  return analysis;
}
