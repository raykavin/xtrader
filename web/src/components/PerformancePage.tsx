import { useCallback, useEffect, useMemo, useState } from "react";
import { fetchExcursions, fetchPerformance } from "../api/client";
import type { PerformanceExcursionsResponse, PerformanceResponse } from "../api/types";
import {
  type ClosedPeriod,
  closedPeriodLabel,
  closedRange,
  dayOf,
  loadClosedPeriod,
  saveClosedPeriod,
} from "../lib/closedPL";
import { formatMoney, formatPercent, signClass } from "../lib/format";
import { openPLGaps } from "../lib/openPL";
import {
  capitalOf,
  currenciesOf,
  excursionsByTrade,
  filterTrades,
  type PerformanceFilter,
  summarize,
  summarizeEfficiency,
} from "../lib/performance";
import { loadTheme, saveTheme, type Theme } from "../lib/theme";
import { AppShell } from "./AppShell";
import { ClosedPeriodDialog } from "./ClosedPeriodDialog";
import { Icon } from "./Icon";
import { PageHeader } from "./PageHeader";
import { PerformanceCharts } from "./PerformanceCharts";
import { PerformanceSummary } from "./PerformanceSummary";
import { PerformanceTrades } from "./PerformanceTrades";
import { Select } from "./Select";
import { Alert, EmptyState, Loading, Skeleton } from "./States";
import { Tabs, tabIds } from "./Tabs";

/** The key of the period of the page in localStorage, apart from the one of the card of the accounts. */
const PERIOD_KEY = "xtrader.performance.period";

/** How often the trades are read again while the page is open, in ms. */
const POLL = 60_000;

/** "Todos" in the selects of the filters. */
const ALL = "";

type View = "summary" | "charts" | "trades";

const views: { id: View; label: string; icon: "results" | "chart" | "list" }[] = [
  { id: "summary", label: "Resumo", icon: "results" },
  { id: "charts", label: "Gráficos", icon: "chart" },
  { id: "trades", label: "Operações", icon: "list" },
];

/** The trades of period, read at once, again every POLL and on a refresh. */
function usePerformance(period: ClosedPeriod) {
  const [read, setRead] = useState<PerformanceResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [version, setVersion] = useState(0);
  const { from, to } = closedRange(period);

  // biome-ignore lint/correctness/useExhaustiveDependencies: version reads again on a refresh
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setRead((current) => (current && current.from === from && current.to === to ? current : null));
    const load = () =>
      fetchPerformance(from, to, controller.signal)
        .then((response) => {
          setRead(response);
          setError(null);
        })
        .catch((e) => {
          if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
        })
        .finally(() => {
          if (!controller.signal.aborted) setLoading(false);
        });
    load();
    const timer = window.setInterval(load, POLL);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, [from, to, version]);

  return { read, error, loading, refresh: useCallback(() => setVersion((v) => v + 1), []) };
}

/**
 * The excursions of the trades of read (see PerformanceExcursionsResponse),
 * read again with each new read of the trades: the first read of a range is
 * slow, the candles of each trade read at the bridges; then kept.
 */
function useExcursions(read: PerformanceResponse | null) {
  const [excursions, setExcursions] = useState<PerformanceExcursionsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const from = read?.from;
  const to = read?.to;
  const at = read?.at;

  // biome-ignore lint/correctness/useExhaustiveDependencies: at reads them again with the trades
  useEffect(() => {
    if (from === undefined || to === undefined) return;
    const controller = new AbortController();
    setExcursions((current) =>
      current && current.from === from && current.to === to ? current : null,
    );
    fetchExcursions(from, to, controller.signal)
      .then((response) => {
        setExcursions(response);
        setError(null);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      });
    return () => controller.abort();
  }, [from, to, at]);

  return { excursions, error };
}

/**
 * The page Performance: the trades of the agents trading live closed in a
 * period, as their MT5 accounts booked them, filtered by agent and asset,
 * summed apart by the currency of the accounts (none converted): the
 * highlights, then the summary, the charts or the list of the trades.
 */
export function PerformancePage() {
  const [theme, setTheme] = useState<Theme>(loadTheme);
  const [period, setPeriod] = useState<ClosedPeriod>(() => loadClosedPeriod(undefined, PERIOD_KEY));
  const [picking, setPicking] = useState(false);
  const [filter, setFilter] = useState<PerformanceFilter>({});
  const [currency, setCurrency] = useState<string | null>(null);
  const [view, setView] = useState<View>("summary");
  const { read, error, loading, refresh } = usePerformance(period);
  const paths = useExcursions(read);
  const byTrade = useMemo(
    () => excursionsByTrade(paths.excursions?.excursions ?? []),
    [paths.excursions],
  );

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    saveTheme(theme);
  }, [theme]);

  const trades = read?.trades ?? [];
  const accounts = read?.accounts ?? [];
  const currencies = currenciesOf(trades, accounts);
  const shown = currency && currencies.includes(currency) ? currency : (currencies[0] ?? null);
  const agents = [
    ...new Set([...trades.map((t) => t.agent), ...accounts.flatMap((a) => a.agents)]),
  ].sort();
  const symbols = [...new Set(trades.map((t) => t.symbol))].sort();
  const filtered = useMemo(
    () => filterTrades(trades, filter).filter((t) => t.currency === shown),
    [trades, filter, shown],
  );
  const range = read ? { from: read.from, to: read.to } : closedRange(period);
  const summary = shown
    ? summarize(filtered, shown, capitalOf(accounts, shown, filter.agent), range, Date.now() / 1000)
    : null;
  const gaps = read ? openPLGaps(read) : [];
  const efficiency = paths.excursions ? summarizeEfficiency(filtered, byTrade) : null;
  const efficiencyState = {
    summary: efficiency,
    excursions: byTrade,
    loading: paths.excursions === null && paths.error === null,
    error: paths.error,
  };

  const apply = (next: ClosedPeriod) => {
    saveClosedPeriod(next, undefined, PERIOD_KEY);
    setPeriod(next);
  };
  const ids = tabIds("perf", view);
  const fileName = `performance-${dayOf(new Date(range.from * 1000))}${filter.agent ? `-${filter.agent}` : ""}${filter.symbol ? `-${filter.symbol}` : ""}-${shown ?? ""}.csv`;

  return (
    <AppShell
      page="performance"
      title="Performance"
      theme={theme}
      onThemeToggle={() => setTheme((t) => (t === "dark" ? "light" : "dark"))}
    >
      <PageHeader
        eyebrow="Operação"
        title="Performance"
        description="O resultado das operações dos agentes em live, como as contas MT5 registraram, na moeda de cada conta, sem conversão."
      />

      <section className="card perf-filters" aria-label="Filtros da performance">
        <div className="perf-filter">
          <span className="field-label">Período</span>
          <button type="button" className="btn" onClick={() => setPicking(true)}>
            <Icon name="results" size={14} />
            {closedPeriodLabel(period)}
          </button>
        </div>
        <div className="perf-filter">
          <span className="field-label">Agente</span>
          <Select
            caption="Agente"
            value={filter.agent ?? ALL}
            onChange={(value) =>
              setFilter((f) => ({ ...f, agent: value === ALL ? undefined : value }))
            }
            options={[
              { value: ALL, label: "Todos" },
              ...agents.map((a) => ({ value: a, label: a })),
            ]}
          />
        </div>
        <div className="perf-filter">
          <span className="field-label">Ativo</span>
          <Select
            caption="Ativo"
            value={filter.symbol ?? ALL}
            onChange={(value) =>
              setFilter((f) => ({ ...f, symbol: value === ALL ? undefined : value }))
            }
            options={[
              { value: ALL, label: "Todos" },
              ...symbols.map((s) => ({ value: s, label: s })),
            ]}
          />
        </div>
        {currencies.length > 1 && shown && (
          <div className="perf-filter">
            <span className="field-label">Moeda da conta</span>
            <Tabs
              label="Moeda das contas"
              idPrefix="perf-currency"
              items={currencies.map((c) => ({ id: c, label: c }))}
              value={shown}
              onChange={setCurrency}
            />
          </div>
        )}
        <button
          type="button"
          className="btn btn-sm btn-ghost perf-refresh"
          onClick={refresh}
          disabled={loading}
          aria-label="Atualizar"
          title="Ler as contas de novo"
        >
          <Icon name="restart" size={14} />
          {loading ? "Lendo…" : "Atualizar"}
        </button>
      </section>

      {error && (
        <Alert tone="error" title="Não foi possível ler a performance">
          {error}
        </Alert>
      )}
      {gaps.length > 0 && (
        <Alert tone="warn" title="Dados incompletos">
          {gaps.join("; ")}
        </Alert>
      )}

      {!read ? (
        !error && (
          <Loading label="Lendo as operações das contas">
            <Skeleton height={320} />
          </Loading>
        )
      ) : !summary || !shown ? (
        <EmptyState icon="results" title="Nenhuma conta em live">
          A performance aparece com agentes em live numa conta MT5.
        </EmptyState>
      ) : (
        <>
          <section className="perf-highlights" aria-label="Destaques">
            {[
              { label: "Resultado líquido", value: summary.net, signed: true },
              { label: "Lucro bruto", value: summary.grossProfit, signed: true },
              { label: "Prejuízo bruto", value: summary.grossLoss, signed: true },
              { label: "Custos", value: summary.costs, signed: true },
            ].map((h) => (
              <div key={h.label} className="perf-highlight">
                <span className="perf-highlight-label">{h.label}</span>
                <strong className={`mono ${h.signed ? signClass(h.value) : ""}`}>
                  {formatMoney(h.value)} <small>{shown}</small>
                </strong>
              </div>
            ))}
            <div className="perf-highlight">
              <span className="perf-highlight-label">Operações</span>
              <strong className="mono">{summary.trades}</strong>
            </div>
            <div className="perf-highlight">
              <span className="perf-highlight-label">Vencedoras</span>
              <strong className="mono">
                {summary.trades ? formatPercent(summary.winRate).replace("+", "") : "—"}
              </strong>
            </div>
          </section>

          <section className="card perf-card" aria-label="Performance">
            <Tabs
              label="Vistas da performance"
              idPrefix="perf"
              items={views.map((v) => ({
                ...v,
                count: v.id === "trades" ? filtered.length : undefined,
              }))}
              value={view}
              onChange={setView}
            />
            <div id={ids.panel} role="tabpanel" aria-labelledby={ids.tab} className="perf-panel">
              {filtered.length === 0 && view !== "trades" ? (
                <EmptyState compact icon="results" title="Nenhuma operação fechada no período">
                  Escolha outro período ou tire os filtros.
                </EmptyState>
              ) : view === "summary" ? (
                <PerformanceSummary summary={summary} efficiency={efficiencyState} />
              ) : view === "charts" ? (
                <PerformanceCharts
                  trades={filtered}
                  currency={shown}
                  range={range}
                  theme={theme}
                  efficiency={efficiencyState}
                />
              ) : (
                <PerformanceTrades
                  trades={filtered}
                  currency={shown}
                  fileName={fileName}
                  excursions={byTrade}
                />
              )}
            </div>
          </section>
        </>
      )}

      {picking && (
        <ClosedPeriodDialog
          period={period}
          onApply={apply}
          onClose={() => setPicking(false)}
          title="Período da performance"
        />
      )}
    </AppShell>
  );
}
