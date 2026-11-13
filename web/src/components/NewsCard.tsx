import { useEffect, useId, useRef, useState } from "react";
import { fetchNewsCalendar } from "../api/client";
import { type NewsCalendarResponse, PermNewsView } from "../api/types";
import { settingsHref } from "../lib/nav";
import {
  blockingNow,
  customNewsError,
  dayOf,
  filterNews,
  importanceLabel,
  importanceLevel,
  loadNewsImpacts,
  loadNewsPeriod,
  MAJOR_CURRENCIES,
  NEWS_IMPACTS,
  NEWS_PERIOD_NAMES,
  type NewsImpact,
  type NewsPeriod,
  type NewsPeriodKind,
  newsDays,
  newsPast,
  newsPeriodLabel,
  newsRange,
  newsStatusMessage,
  newsTime,
  newsValue,
  newsWindowText,
  nextNews,
  saveNewsImpacts,
  saveNewsPeriod,
  toggleImpact,
} from "../lib/newsCalendar";
import { useCan } from "./AccessContext";
import { Select } from "./Select";
import { Alert, Skeleton } from "./States";

/** How often the calendar is read again, in ms: the values published as they come. */
const NEWS_REFRESH = 60_000;

/**
 * The card of the news on the dashboard: the releases of the economic
 * calendar of the main currencies in the period picked (today and tomorrow by
 * default, or a custom one), filtered by their impact, by day, the next one
 * marked and the ones blocking the entries of the agents told, with the
 * actual value once published, the forecast and the previous one. The period
 * and the filter are kept in the browser.
 */
export function NewsCard() {
  const id = useId();
  const seesSettings = useCan(PermNewsView);
  const [period, setPeriod] = useState<NewsPeriod>(() => loadNewsPeriod());
  const [impacts, setImpacts] = useState<NewsImpact[]>(() => loadNewsImpacts());
  const [draft, setDraft] = useState(() => ({
    from: period.from ?? dayOf(new Date()),
    to: period.to ?? dayOf(new Date()),
  }));
  const [read, setRead] = useState<NewsCalendarResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [now, setNow] = useState(() => new Date());
  const list = useRef<HTMLElement>(null);
  const scrolled = useRef<string | null>(null); // the range read the list was scrolled to its next release for

  // the period read at once, as it changes, and every minute, its range taken
  // again each time (today follows midnight)
  useEffect(() => {
    const controller = new AbortController();
    const load = () => {
      const at = new Date();
      setNow(at);
      const range = newsRange(period, at);
      if (!range) return;
      fetchNewsCalendar(range.from, range.to, MAJOR_CURRENCIES, controller.signal)
        .then((response) => {
          setRead(response);
          setError(null);
        })
        .catch((e) => {
          if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
        });
    };
    load();
    const timer = window.setInterval(load, NEWS_REFRESH);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, [period]);

  const events = read ? filterNews(read.events, impacts, read) : [];
  const next = nextNews(events, now);
  const nextEvent = next >= 0 ? events[next] : undefined;
  const days = newsDays(events, now);
  const readKey = read ? `${read.from}-${read.to}` : null;

  // the list scrolled to the next release once per range read (a period
  // picked, today after midnight), never as the user scrolls
  useEffect(() => {
    if (!readKey || scrolled.current === readKey || !list.current) return;
    scrolled.current = readKey;
    const row = list.current.querySelector<HTMLElement>("[data-next]");
    list.current.scrollTop = row ? Math.max(row.offsetTop - list.current.offsetTop - 40, 0) : 0;
  });

  const choose = (kind: string) => {
    const next: NewsPeriod =
      kind === "custom" ? { kind: "custom", ...draft } : { kind: kind as NewsPeriodKind };
    if (next.kind === "custom" && customNewsError(draft.from, draft.to)) {
      setPeriod(next); // shown with why, nothing read
      return;
    }
    saveNewsPeriod(next);
    setPeriod(next);
  };
  const editDraft = (field: "from" | "to", value: string) => {
    const changed = { ...draft, [field]: value };
    setDraft(changed);
    const next: NewsPeriod = { kind: "custom", ...changed };
    setPeriod(next);
    if (!customNewsError(changed.from, changed.to)) saveNewsPeriod(next);
  };
  const toggle = (impact: NewsImpact) =>
    setImpacts((current) => {
      const changed = toggleImpact(current, impact);
      saveNewsImpacts(changed);
      return changed;
    });
  const customError = period.kind === "custom" ? customNewsError(draft.from, draft.to) : null;
  const windowText = read ? newsWindowText(read) : null;

  return (
    <section className="card news-card" aria-labelledby={`${id}-title`}>
      <header className="news-head">
        <div className="card-heading">
          <span className="eyebrow">Calendário econômico</span>
          <h2 id={`${id}-title`} className="card-title">
            Notícias
          </h2>
        </div>
        <div className="news-period">
          <Select
            caption="Período das notícias"
            value={period.kind}
            summary={newsPeriodLabel(period)}
            options={Object.entries(NEWS_PERIOD_NAMES).map(([value, label]) => ({ value, label }))}
            onChange={choose}
          />
        </div>
      </header>
      {period.kind === "custom" && (
        <fieldset className="news-custom" aria-label="Dias do período personalizado">
          <label className="news-custom-day">
            <span className="field-label">De</span>
            <input
              type="date"
              className="input"
              value={draft.from}
              onChange={(e) => editDraft("from", e.target.value)}
            />
          </label>
          <label className="news-custom-day">
            <span className="field-label">Até</span>
            <input
              type="date"
              className="input"
              value={draft.to}
              onChange={(e) => editDraft("to", e.target.value)}
            />
          </label>
          {customError && <span className="field-error news-custom-error">{customError}</span>}
        </fieldset>
      )}
      <fieldset className="seg news-impacts">
        <legend className="sr-only">Impacto das notícias mostradas</legend>
        {NEWS_IMPACTS.map(({ impact, label }) => (
          <button
            key={impact}
            type="button"
            className={`seg-item news-impact-${impact}`}
            aria-pressed={impacts.includes(impact)}
            title={
              impacts.length === 1 && impacts.includes(impact)
                ? "Ao menos um impacto fica marcado"
                : undefined
            }
            onClick={() => toggle(impact)}
          >
            <ImpactBars level={importanceLevel(impact)} />
            {label}
          </button>
        ))}
      </fieldset>

      {error && !read && <Alert tone="error">{error}</Alert>}
      {read && read.status !== "ok" && (
        <Alert tone={read.status === "disabled" ? "info" : "warn"}>
          {newsStatusMessage(read.status)}{" "}
          {seesSettings && read.status !== "unavailable" && (
            <a href={settingsHref("noticias")}>Abrir Notícias</a>
          )}
        </Alert>
      )}
      {!read && !error && <Skeleton height={220} />}
      {read && read.status === "ok" && (
        <section ref={list} className="news-list" aria-label="Divulgações do calendário econômico">
          {days.length === 0 ? (
            <p className="field-hint news-empty">
              Nenhuma divulgação dos impactos marcados no período.
            </p>
          ) : (
            days.map((day) => (
              <section key={day.day} className="news-day" aria-label={day.label}>
                <h3 className="news-day-title">{day.label}</h3>
                <ul className="news-rows">
                  {day.events.map((event) => {
                    const past = newsPast(event, now);
                    const isNext = event === nextEvent;
                    const blocking = blockingNow(event, read, now);
                    const values = [
                      ["Atual", newsValue(event.actual, event.unit)],
                      ["Previsão", newsValue(event.forecast, event.unit)],
                      ["Anterior", newsValue(event.previous, event.unit)],
                    ].filter(([, value]) => value !== null) as [string, string][];
                    return (
                      <li
                        key={`${event.at}-${event.currency}-${event.code ?? event.name}`}
                        className="news-row"
                        data-past={past ? "" : undefined}
                        data-next={isNext ? "" : undefined}
                        data-impact={event.importance}
                      >
                        <span className="news-time">{newsTime(event)}</span>
                        <span className="news-currency">{event.currency}</span>
                        <span
                          className="news-importance"
                          title={`Impacto: ${importanceLabel(event.importance)}`}
                        >
                          <ImpactBars level={importanceLevel(event.importance)} />
                          <span className="sr-only">
                            Impacto {importanceLabel(event.importance)}
                          </span>
                        </span>
                        <span className="news-name">
                          {event.name}
                          {isNext && <span className="tag news-next-tag">próxima</span>}
                          {event.blocks && (
                            <span
                              className={
                                blocking ? "tag news-block-tag is-now" : "tag news-block-tag"
                              }
                              title={windowText ?? undefined}
                            >
                              {blocking ? "entradas bloqueadas agora" : "bloqueia entradas"}
                            </span>
                          )}
                        </span>
                        {values.length > 0 && (
                          <span className="news-values">
                            {values.map(([label, value]) => (
                              <span key={label} className="news-value">
                                <span className="news-value-label">{label}</span> {value}
                              </span>
                            ))}
                          </span>
                        )}
                      </li>
                    );
                  })}
                </ul>
              </section>
            ))
          )}
        </section>
      )}
      {read && read.status === "ok" && (
        <footer className="news-foot">
          <span>
            {events.length === read.events.length
              ? `${read.events.length} ${read.events.length === 1 ? "divulgação" : "divulgações"}`
              : `${events.length} de ${read.events.length} divulgações`}{" "}
            · {MAJOR_CURRENCIES.join(", ")}
          </span>
          {windowText && <span className="news-window">{windowText}</span>}
        </footer>
      )}
    </section>
  );
}

/** The impact of a release in 3 bars, level of them lit (0 for none). */
function ImpactBars({ level }: { level: number }) {
  return (
    <span className="news-bars" data-level={level} aria-hidden="true">
      <i />
      <i />
      <i />
    </span>
  );
}
