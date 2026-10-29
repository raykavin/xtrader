import { useCallback, useEffect, useRef, useState } from "react";
import { cancelBacktestRun, deleteBacktest, fetchBacktests } from "../api/client";
import type { BacktestBatchInfo, BacktestsResponse } from "../api/types";
import { active } from "../lib/backtest";
import { loadTheme, saveTheme, type Theme } from "../lib/theme";
import { AppShell } from "./AppShell";
import { BacktestConfigure } from "./BacktestConfigure";
import { BacktestResults } from "./BacktestResults";
import { BacktestRuns } from "./BacktestRuns";
import { PageHeader } from "./PageHeader";
import { Alert, Loading, Skeleton } from "./States";
import { Tabs, tabIds } from "./Tabs";

/** How often the batches are read again while some run, and while none does, in ms. */
const POLL_ACTIVE = 3_000;
const POLL_IDLE = 30_000;

type View = "configure" | "runs" | "results";

const views: { id: View; label: string; icon: "settings" | "list" | "results" }[] = [
  { id: "configure", label: "Configurar", icon: "settings" },
  { id: "runs", label: "Execuções", icon: "list" },
  { id: "results", label: "Resultados", icon: "results" },
];

/**
 * The batches of backtests, read at once and again: every POLL_ACTIVE while
 * some run is queued or running, every POLL_IDLE otherwise, and on a refresh.
 * The runs go on in the controller: a reload of the page only reads them.
 */
function useBacktests() {
  const [read, setRead] = useState<BacktestsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [version, setVersion] = useState(0);
  const busy = useRef(false);

  // biome-ignore lint/correctness/useExhaustiveDependencies: version reads again on a refresh
  useEffect(() => {
    const controller = new AbortController();
    let timer = 0;
    const load = () =>
      fetchBacktests(controller.signal)
        .then((response) => {
          setRead(response);
          setError(null);
          busy.current = response.batches.some((b) => b.runs.some(active));
        })
        .catch((e) => {
          if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
        })
        .finally(() => {
          if (!controller.signal.aborted) {
            timer = window.setTimeout(load, busy.current ? POLL_ACTIVE : POLL_IDLE);
          }
        });
    load();
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [version]);

  return { read, error, refresh: useCallback(() => setVersion((v) => v + 1), []) };
}

/**
 * The page Backtesting: the strategies of the agents run on the history of
 * their pairs, several at once, apart from the agents trading (never an
 * order, never a change to them), to compare them under the same conditions:
 * a batch configured, its runs followed, their results compared.
 */
export function BacktestingPage() {
  const [theme, setTheme] = useState<Theme>(loadTheme);
  const [view, setView] = useState<View>("configure");
  const [batch, setBatch] = useState<number | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const { read, error, refresh } = useBacktests();

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    saveTheme(theme);
  }, [theme]);

  const batches = read?.batches ?? [];
  const running = batches.reduce((n, b) => n + b.runs.filter(active).length, 0);
  const ids = tabIds("bt", view);

  const act = (action: () => Promise<void>) => {
    setFailure(null);
    action()
      .catch((e) => setFailure(e instanceof Error ? e.message : String(e)))
      .finally(refresh);
  };
  const created = (info: BacktestBatchInfo) => {
    setBatch(info.id);
    setView("runs");
    refresh();
  };
  const open = (id: number) => {
    setBatch(id);
    setView("results");
  };

  return (
    <AppShell
      page="backtesting"
      title="Backtesting"
      theme={theme}
      onThemeToggle={() => setTheme((t) => (t === "dark" ? "light" : "dark"))}
    >
      <PageHeader
        eyebrow="Operação"
        title="Backtesting"
        description="As estratégias dos agentes sobre o histórico dos seus ativos, várias ao mesmo tempo, comparadas nas mesmas condições."
      />

      <Alert tone="info" title="Simulação" className="bt-notice">
        Nenhuma execução envia ordens nem altera os agentes, seus saldos ou históricos. Os
        resultados ficam na moeda de cotação do par e não garantem o resultado em live. Ativar um
        agente continua sendo feito em Configurações › Agentes.
      </Alert>

      {error && (
        <Alert tone="error" title="Não foi possível ler os backtests">
          {error}
        </Alert>
      )}
      {failure && (
        <Alert tone="error" title="A ação não foi feita">
          {failure}
        </Alert>
      )}

      {!read ? (
        !error && (
          <Loading label="Lendo os backtests">
            <Skeleton height={320} />
          </Loading>
        )
      ) : (
        <section className="card perf-card" aria-label="Backtesting">
          <Tabs
            label="Áreas do backtesting"
            idPrefix="bt"
            items={views.map((v) => ({
              ...v,
              count: v.id === "runs" && running > 0 ? running : undefined,
            }))}
            value={view}
            onChange={setView}
          />
          <div id={ids.panel} role="tabpanel" aria-labelledby={ids.tab} className="perf-panel">
            {view === "configure" ? (
              <BacktestConfigure read={read} onCreated={created} />
            ) : view === "runs" ? (
              <BacktestRuns
                batches={batches}
                highlight={batch}
                onCancel={(id) => act(() => cancelBacktestRun(id))}
                onDelete={(id) => act(() => deleteBacktest(id))}
                onOpen={open}
                onNew={() => setView("configure")}
              />
            ) : (
              <BacktestResults
                batches={batches}
                batch={batch}
                onBatch={setBatch}
                theme={theme}
                onNew={() => setView("configure")}
              />
            )}
          </div>
        </section>
      )}
    </AppShell>
  );
}
