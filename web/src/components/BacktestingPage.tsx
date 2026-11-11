import { useCallback, useEffect, useRef, useState } from "react";
import { cancelBacktestRun, deleteBacktest, fetchBacktestsPage } from "../api/client";
import type { BacktestBatchInfo, BacktestsResponse } from "../api/types";
import { PermBacktestsManage } from "../api/types";
import { loadTheme, saveTheme, type Theme } from "../lib/theme";
import { useCan } from "./AccessContext";
import { AppShell } from "./AppShell";
import { BacktestConfigure } from "./BacktestConfigure";
import { BacktestResults } from "./BacktestResults";
import { BacktestRuns } from "./BacktestRuns";
import { PageHeader } from "./PageHeader";
import { ListPages } from "./Paginator";
import { Alert, Loading, Skeleton } from "./States";
import { Tabs, tabIds } from "./Tabs";

/** How often the batches are read again while some run, and while none does, in ms. */
const POLL_ACTIVE = 3_000;
const POLL_IDLE = 30_000;

/** The batches of a page of the runs: each one with the table of its runs. */
const BATCHES_PAGE = 5;

type View = "configure" | "runs" | "results";

const views: { id: View; label: string; icon: "settings" | "list" | "results" }[] = [
  { id: "configure", label: "Configurar", icon: "settings" },
  { id: "runs", label: "Execuções", icon: "list" },
  { id: "results", label: "Resultados", icon: "results" },
];

/**
 * The batches of backtests of a page (from 1), BATCHES_PAGE each, with every
 * batch as a choice and the agents, read at once and again: every POLL_ACTIVE
 * while some run of any batch is queued or running, every POLL_IDLE
 * otherwise, and on a refresh or another page. The runs go on in the
 * controller: a reload of the page only reads them.
 */
function useBacktests(page: number) {
  const [read, setRead] = useState<BacktestsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [version, setVersion] = useState(0);
  const busy = useRef(false);

  // biome-ignore lint/correctness/useExhaustiveDependencies: version reads again on a refresh
  useEffect(() => {
    const controller = new AbortController();
    let timer = 0;
    const load = () =>
      fetchBacktestsPage({ page, perPage: BATCHES_PAGE }, controller.signal)
        .then((response) => {
          setRead(response);
          setError(null);
          busy.current = response.running > 0;
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
  }, [version, page]);

  return { read, error, refresh: useCallback(() => setVersion((v) => v + 1), []) };
}

/**
 * The page Backtesting: the strategies of the agents run on the history of
 * their pairs, several at once, apart from the agents trading (never an
 * order, never a change to them), to compare them under the same conditions:
 * a batch configured, its runs followed, their results compared.
 */
export function BacktestingPage() {
  // started, canceled and deleted by a user allowed to only, followed by the others
  const manage = useCan(PermBacktestsManage);
  const [theme, setTheme] = useState<Theme>(loadTheme);
  const [view, setView] = useState<View>(manage ? "configure" : "runs");
  const shownViews = manage ? views : views.filter((v) => v.id !== "configure");
  const configure = manage ? () => setView("configure") : undefined;
  const [batch, setBatch] = useState<number | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [page, setPage] = useState(1);
  const { read, error, refresh } = useBacktests(page);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    saveTheme(theme);
  }, [theme]);

  // the page the controller answered, on a new reading only (the one of an older page is
  // canceled): the last one for a page past it, eg: its batches deleted
  const asked = useRef(page);
  asked.current = page;
  useEffect(() => {
    if (read && read.total_pages > 0 && read.page !== asked.current) setPage(read.page);
  }, [read]);

  const batches = read?.batches ?? [];
  const running = read?.running ?? 0;
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
    setPage(1); // the newest first
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
        description="Teste as estratégias dos agentes no histórico dos ativos e compare-as nas mesmas condições."
      />

      <Alert tone="info" title="Simulação" className="bt-notice">
        Nenhuma execução envia ordens nem altera os agentes. Os resultados ficam na moeda de cotação
        do ativo e não garantem o resultado em live. Para ligar um agente, use Configurações ›
        Agentes.
      </Alert>

      {error && (
        <Alert tone="error" title="Não foi possível carregar os backtests">
          {error}
        </Alert>
      )}
      {failure && (
        <Alert tone="error" title="Não foi possível concluir a ação">
          {failure}
        </Alert>
      )}

      {!read ? (
        !error && (
          <Loading label="Carregando os backtests">
            <Skeleton height={320} />
          </Loading>
        )
      ) : (
        <section className="card perf-card" aria-label="Backtesting">
          <Tabs
            label="Seções do backtesting"
            idPrefix="bt"
            items={shownViews.map((v) => ({
              ...v,
              count: v.id === "runs" && running > 0 ? running : undefined,
            }))}
            value={view}
            onChange={setView}
          />
          <div id={ids.panel} role="tabpanel" aria-labelledby={ids.tab} className="perf-panel">
            {view === "configure" && manage ? (
              <BacktestConfigure read={read} onCreated={created} />
            ) : view === "runs" ? (
              <>
                <BacktestRuns
                  batches={batches}
                  highlight={batch}
                  onCancel={manage ? (id) => act(() => cancelBacktestRun(id)) : undefined}
                  onDelete={manage ? (id) => act(() => deleteBacktest(id)) : undefined}
                  onOpen={open}
                  onNew={configure}
                />
                <div className="panel-body bt-pages">
                  <ListPages info={read} onPage={setPage} />
                </div>
              </>
            ) : (
              <BacktestResults
                choices={read.choices}
                batch={batch}
                onBatch={setBatch}
                theme={theme}
                onNew={configure}
                refreshed={read}
              />
            )}
          </div>
        </section>
      )}
    </AppShell>
  );
}
