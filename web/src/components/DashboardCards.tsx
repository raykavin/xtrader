import {
  type CSSProperties,
  Fragment,
  type KeyboardEvent,
  type PointerEvent,
  type ReactNode,
  type RefObject,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import {
  activityFollowsNews,
  CARD_LABELS,
  CARDS,
  type CardId,
  CHART_MAX_HEIGHT,
  CHART_MAX_WIDTH,
  CHART_MIN_HEIGHT,
  CHART_MIN_WIDTH,
  chartHeightOf,
  chartWidthOf,
  clearLayout,
  type DashboardLayout,
  type Direction,
  defaultLayout,
  isDefaultLayout,
  LAYOUT_KEY,
  loadLayout,
  moveCard,
  type Placement,
  saveLayout,
  setChartHeight,
  setChartWidth,
  setHidden,
  shownRows,
  stepCard,
} from "../lib/layout";
import { Icon } from "./Icon";

/**
 * The layout of the cards of this browser: read once, saved at each change,
 * read again when another tab changes it or removes it, eg: the settings
 * restoring the default one.
 */
export function useDashboardLayout() {
  const [layout, setLayout] = useState<DashboardLayout>(loadLayout);
  const update = useCallback((next: DashboardLayout) => {
    setLayout(next);
    saveLayout(next);
  }, []);
  const reset = useCallback(() => {
    clearLayout();
    setLayout(defaultLayout());
  }, []);
  useEffect(() => {
    const onStorage = (event: StorageEvent) => {
      if (event.key === LAYOUT_KEY || event.key === null) setLayout(loadLayout());
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);
  return { layout, update, reset };
}

/** Whether the media query matches, following it. */
function useMedia(query: string): boolean {
  const [matches, setMatches] = useState(() => window.matchMedia?.(query).matches ?? true);
  useEffect(() => {
    const list = window.matchMedia?.(query);
    if (!list) return;
    const onChange = () => setMatches(list.matches);
    onChange();
    list.addEventListener("change", onChange);
    return () => list.removeEventListener("change", onChange);
  }, [query]);
  return matches;
}

interface Props {
  layout: DashboardLayout;
  onLayout: (layout: DashboardLayout) => void;
  /** The cards of the page by id; one absent isn't on this agent, eg: the last decision without an LLM. */
  cards: Partial<Record<CardId, ReactNode>>;
  /** Whether the layout is being customized: the cards then show how to move, hide and size them. */
  editing: boolean;
  onEditingChange: (editing: boolean) => void;
  /** Restores the default layout, the saved one removed. */
  onReset: () => void;
}

/** A card being dragged, and where the pointer is. */
interface Drag {
  card: CardId;
  pointer: number;
  x: number;
  y: number;
}

/**
 * The column of each card in a row of several on a wide screen: its least
 * width (never above an equal share of the row, so that a row of many cards
 * never overflows), and its share of the rest, or its widest width.
 */
const TRACKS: Record<CardId, { min: number; grow: number; max?: number }> = {
  quote: { min: 300, grow: 4 },
  sentiment: { min: 240, grow: 3 },
  results: { min: 280, grow: 6 },
  openpl: { min: 260, grow: 3 },
  chart: { min: CHART_MIN_WIDTH, grow: 8 },
  decision: { min: 260, grow: 4 },
  activity: { min: 360, grow: 8 },
  news: { min: 320, grow: 4 },
};

/**
 * The cards of about the same height, as tall as each other in a row of them
 * alone, one per column, as the overview row of the default layout; next to a
 * taller card (the chart, the activity, or a column of several),
 * each keeps the height of its content.
 */
const EVEN: ReadonlySet<CardId> = new Set(["quote", "sentiment", "results", "openpl", "decision"]);

/**
 * The cards filling the height of their column alone in it, whatever their
 * content: the news beside the chart, as tall as its column, its list
 * scrolling, never shrunk by a filter leaving few releases.
 */
const FILL: ReadonlySet<CardId> = new Set(["news"]);

/** The column of the chart of the width the user set, see DashboardLayout.chartWidth. */
const CHART_WIDTH_TRACK = "minmax(0, var(--chart-width))";

/** The least width of a column of cards in a row of count columns, in CSS pixels, at most an equal share of width. */
function leastWidth(cards: CardId[], count: number, width: number, gap: number): number {
  const share = (width - (count - 1) * gap) / count;
  return Math.min(Math.max(0, ...cards.map((card) => TRACKS[card].min)), share);
}

/**
 * The columns of a row on a wide screen, for its columns of cards: a column
 * alone the whole row; else each at its least width at least, sharing the
 * rest, but the chart of the width the user set (chartWidth), the others
 * sharing what it leaves.
 */
export function rowColumns(columns: CardId[][], chartWidth = false): string {
  const count = columns.length;
  const share = `calc((100% - ${count - 1} * var(--sp-4)) / ${count})`;
  return columns
    .map((cards) => {
      if (chartWidth && cards.includes("chart")) return CHART_WIDTH_TRACK;
      if (count === 1) return "minmax(0, 1fr)";
      const min = Math.max(...cards.map((card) => TRACKS[card].min));
      const least = min > 0 ? `min(${min}px, ${share})` : "0";
      const widest = cards.map((card) => TRACKS[card].max);
      if (widest.every((max) => max !== undefined)) {
        return `minmax(${least}, ${Math.max(...(widest as number[]))}px)`;
      }
      return `minmax(${least}, ${Math.max(...cards.map((card) => TRACKS[card].grow))}fr)`;
    })
    .join(" ");
}

/** The step of a move of the size of the chart by the keyboard, in CSS pixels, and with Shift or a page key. */
const RESIZE_STEP = 20;
const RESIZE_BIG_STEP = 100;

/** How close to the widest the chart dragged alone in its row fills it again, the width of the responsive layout. */
const FILL_SNAP = 12;

/** The highest height of the chart the window takes, in CSS pixels. */
function chartCeiling(): number {
  return Math.max(
    CHART_MIN_HEIGHT,
    Math.min(CHART_MAX_HEIGHT, Math.round(window.innerHeight * 0.92)),
  );
}

/** What a move of a card to target says, for the readers of the screen. */
function describeMove(card: CardId, target: Placement): string {
  const label = CARD_LABELS[card];
  switch (target.kind) {
    case "beside":
      return `${label} ao lado ${target.side === "before" ? "esquerdo" : "direito"} de ${CARD_LABELS[target.card]}.`;
    case "stack":
      return `${label} ${target.side === "before" ? "acima" : "abaixo"} de ${CARD_LABELS[target.card]}.`;
    case "swap":
      return `${label} trocou de lugar com ${CARD_LABELS[target.card]}.`;
    case "row":
      return `${label} numa linha própria.`;
  }
}

/**
 * The element a card is drawn in, the same one wherever the card goes: the
 * card is rendered into it once (a portal), and the element moved into the
 * frame of its place, so that a card moved keeps its state, eg: the zoom of
 * the chart, or the tab of the activity.
 */
function CardSlot({ host }: { host: HTMLElement }) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const slot = ref.current;
    if (slot && host.parentElement !== slot) slot.appendChild(host);
  });
  return <div ref={ref} className="layout-card-body" />;
}

/**
 * The cards of the dashboard in rows of columns, as the user arranged them
 * (see lib/layout.ts): any card in any row and column, beside the others or
 * stacked with them, the rows and columns without a card shown left out.
 * Out of the customization the cards are drawn as they are, nothing added to
 * them. Customizing, each card gets a bar above it: its handle, dragged with a
 * mouse or a finger, or moved with the arrows of the keyboard, and a button
 * hiding it; the chart bars on its edges sizing it. Only those bars drag, so
 * the buttons, the fields and the chart within the cards keep working.
 */
export function DashboardCards({
  layout,
  onLayout,
  cards,
  editing,
  onEditingChange,
  onReset,
}: Props) {
  const rootRef = useRef<HTMLDivElement>(null);
  const hosts = useRef(new Map<CardId, HTMLElement>());
  const hintId = useId();
  const [drag, setDrag] = useState<Drag | null>(null);
  const [target, setTarget] = useState<Placement | null>(null);
  const [focusCard, setFocusCard] = useState<CardId | null>(null);
  const [announcement, setAnnouncement] = useState("");
  // a drag of the width of the chart going on: its column follows it before it is saved
  const [sizingWidth, setSizingWidth] = useState(false);
  const wide = useMedia("(min-width: 1200px)");
  const unstacked = useMedia("(min-width: 1024px)");

  const available = (card: CardId) => cards[card] !== undefined && cards[card] !== null;
  const shown = (card: CardId) => available(card) && !layout.hidden.includes(card);
  const rows = shownRows(layout, shown);
  const hidden = CARDS.filter((card) => available(card) && layout.hidden.includes(card));

  /** The element card is drawn in, see CardSlot. */
  const hostOf = (card: CardId) => {
    let host = hosts.current.get(card);
    if (!host) {
      host = document.createElement("div");
      host.className = "layout-card-host";
      hosts.current.set(card, host);
    }
    return host;
  };

  /**
   * The activity as tall as the news card (see activityFollowsNews): the
   * height of the news, measured as it changes (the chart sized, the last
   * decision of an LLM agent shown or not), in --news-height of the cards, the
   * fixed height of the activity else (see .activity-card).
   */
  const followsNews = activityFollowsNews(rows);
  useLayoutEffect(() => {
    const root = rootRef.current;
    const news = hosts.current.get("news")?.firstElementChild;
    if (!root) return;
    if (!followsNews || !(news instanceof HTMLElement) || typeof ResizeObserver === "undefined") {
      root.style.removeProperty("--news-height");
      return;
    }
    const measure = () => {
      const height = Math.round(news.getBoundingClientRect().height);
      if (height > 0) root.style.setProperty("--news-height", `${height}px`);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(news);
    return () => {
      observer.disconnect();
      root.style.removeProperty("--news-height");
    };
  }, [followsNews]);

  // the handle of the card moved by the keyboard keeps the focus, wherever it went
  useEffect(() => {
    if (!focusCard) return;
    rootRef.current?.querySelector<HTMLElement>(`[data-card="${focusCard}"] .layout-grip`)?.focus();
    setFocusCard(null);
  }, [focusCard]);

  // a customization left ends a drag
  useEffect(() => {
    if (!editing) {
      setDrag(null);
      setTarget(null);
    }
  }, [editing]);

  /**
   * Where the card dragged would land under the pointer at x, y: on the left
   * or the right edge of a card, a column of its own beside the card's column;
   * on its top or bottom edge, above or below it in its column; in its middle,
   * the card's place, a swap; on a band between the rows, a row of its own; on
   * the empty part of a row, a column at its end. Null over itself or out of
   * the cards.
   */
  const dropAt = useCallback((dragged: CardId, x: number, y: number): Placement | null => {
    const root = rootRef.current;
    if (!root) return null;
    for (const element of document.elementsFromPoint(x, y)) {
      if (!root.contains(element)) continue;
      const gap = element.closest<HTMLElement>("[data-row-gap]");
      if (gap) return { kind: "row", index: Number(gap.dataset.rowGap) };
      const cardElement = element.closest<HTMLElement>("[data-card]");
      if (cardElement) {
        const card = cardElement.dataset.card as CardId;
        if (card === dragged) return null; // over its own place: it stays there
        const rect = cardElement.getBoundingClientRect();
        const edgeX = Math.min(Math.max(rect.width * 0.25, 48), 160);
        const edgeY = Math.min(Math.max(rect.height * 0.3, 32), 140);
        if (x < rect.left + edgeX) return { kind: "beside", card, side: "before" };
        if (x > rect.right - edgeX) return { kind: "beside", card, side: "after" };
        if (y < rect.top + edgeY) return { kind: "stack", card, side: "before" };
        if (y > rect.bottom - edgeY) return { kind: "stack", card, side: "after" };
        return { kind: "swap", card };
      }
      const rowElement = element.closest<HTMLElement>("[data-row]");
      if (rowElement) {
        // beside the column at the left of the pointer, else before the first one
        const columns = [...rowElement.querySelectorAll<HTMLElement>(":scope > [data-cards]")];
        const left = columns.filter((column) => column.getBoundingClientRect().left < x).pop();
        const anchor = (left ?? columns[0])?.dataset.cards?.split(",")[0] as CardId | undefined;
        if (!anchor || anchor === dragged) return null;
        return { kind: "beside", card: anchor, side: left ? "after" : "before" };
      }
    }
    return null;
  }, []);

  // the drag follows the pointer over the page, scrolling it near its edges
  useEffect(() => {
    if (!drag) return;
    const move = (event: globalThis.PointerEvent) => {
      if (event.pointerId !== drag.pointer) return;
      event.preventDefault();
      setDrag((current) =>
        current ? { ...current, x: event.clientX, y: event.clientY } : current,
      );
      setTarget(dropAt(drag.card, event.clientX, event.clientY));
      const edge = 56;
      if (event.clientY < edge) window.scrollBy(0, -16);
      else if (event.clientY > window.innerHeight - edge) window.scrollBy(0, 16);
    };
    const end = (event: globalThis.PointerEvent) => {
      if (event.pointerId !== drag.pointer) return;
      const landing =
        event.type === "pointerup" ? dropAt(drag.card, event.clientX, event.clientY) : null;
      if (landing) {
        const next = moveCard(layout, drag.card, landing);
        if (next !== layout) {
          onLayout(next);
          setAnnouncement(describeMove(drag.card, landing));
        }
      }
      setDrag(null);
      setTarget(null);
    };
    const cancel = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      setDrag(null);
      setTarget(null);
      setAnnouncement("Movimento cancelado.");
    };
    window.addEventListener("pointermove", move, { passive: false });
    window.addEventListener("pointerup", end);
    window.addEventListener("pointercancel", end);
    window.addEventListener("keydown", cancel);
    return () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", end);
      window.removeEventListener("pointercancel", end);
      window.removeEventListener("keydown", cancel);
    };
  }, [drag, dropAt, layout, onLayout]);

  const startDrag = (card: CardId) => (event: PointerEvent<HTMLButtonElement>) => {
    if (event.button !== 0) return;
    event.preventDefault();
    setDrag({ card, pointer: event.pointerId, x: event.clientX, y: event.clientY });
    setAnnouncement(
      `Movendo ${CARD_LABELS[card]}: solte no lugar desejado ou tecle Esc para cancelar.`,
    );
  };

  const keyMove = (card: CardId) => (event: KeyboardEvent<HTMLButtonElement>) => {
    const directions: Record<string, Direction> = {
      ArrowUp: "up",
      ArrowDown: "down",
      ArrowLeft: "left",
      ArrowRight: "right",
    };
    const direction = directions[event.key];
    if (!direction) return;
    event.preventDefault();
    const next = stepCard(layout, card, direction, shown);
    if (next === layout) {
      setAnnouncement(`${CARD_LABELS[card]} já está na borda do painel.`);
      return;
    }
    onLayout(next);
    const after = shownRows(next, shown);
    const row = after.findIndex((r) => r.columns.some((column) => column.cards.includes(card)));
    const columns = after[row]?.columns ?? [];
    const column = columns.findIndex((c) => c.cards.includes(card));
    setAnnouncement(
      `${CARD_LABELS[card]}: linha ${row + 1} de ${after.length}, coluna ${column + 1} de ${columns.length}.`,
    );
    setFocusCard(card);
  };

  const hide = (card: CardId) => {
    onLayout(setHidden(layout, card, true));
    setAnnouncement(
      `Card ${CARD_LABELS[card]} oculto. Para mostrá-lo, use Mostrar, acima dos cards.`,
    );
  };

  // the width of the chart, set or being dragged
  const chartWidth = sizingWidth || layout.chartWidth !== null;
  const chartRow = rows.find((row) => row.columns.some((column) => column.cards.includes("chart")));
  // the rows of three columns or more wrap below the wide screens, where the width isn't the user's
  const widthApplies = unstacked && chartRow !== undefined && (wide || chartRow.columns.length < 3);

  /**
   * The card in its frame: out of the customization the frame draws no box
   * (display: contents), the card laid out as if it weren't there. grow tells
   * the card filling the rest of its column: the chart of the responsive
   * height, or a card of a row of cards of about the same height, see EVEN.
   */
  const renderCard = (card: CardId, grow: boolean) => {
    const label = CARD_LABELS[card];
    const landing =
      editing && target && target.kind !== "row" && target.card === card ? target : null;
    const className = [
      "layout-card",
      drag?.card === card && "is-dragging",
      landing?.kind === "stack" && `drop-${landing.side} drop-y`,
      landing?.kind === "swap" && "drop-swap",
    ]
      .filter(Boolean)
      .join(" ");
    return (
      <div key={card} className={className} data-card={card} data-grow={grow ? "" : undefined}>
        {editing && (
          <div key="bar" className="layout-card-bar">
            <button
              type="button"
              className="layout-grip"
              aria-label={`Mover ${label}`}
              aria-describedby={hintId}
              onPointerDown={startDrag(card)}
              onKeyDown={keyMove(card)}
            >
              <Icon name="grip" size={14} />
              <span className="layout-card-name">{label}</span>
            </button>
            <button
              type="button"
              className="btn btn-ghost btn-icon btn-xs layout-hide"
              aria-label={`Ocultar ${label}`}
              title="Ocultar o card"
              onClick={() => hide(card)}
            >
              <Icon name="close" size={14} />
            </button>
          </div>
        )}
        <CardSlot key="body" host={hostOf(card)} />
        {editing && card === "chart" && (
          <ChartSizer
            key="size"
            root={rootRef}
            layout={layout}
            onLayout={onLayout}
            onAnnounce={setAnnouncement}
            widthApplies={widthApplies}
            onSizingWidth={setSizingWidth}
          />
        )}
        {landing?.kind === "swap" && (
          <span key="swap" className="layout-swap-label" aria-hidden="true">
            Trocar de lugar
          </span>
        )}
      </div>
    );
  };

  /** A band between the rows while customizing, where a card dropped gets a row of its own before the row of index. */
  const rowGap = (index: number) =>
    editing && (
      <div
        key={`gap-${index}`}
        className={
          target?.kind === "row" && target.index === index
            ? "layout-row-gap is-target"
            : "layout-row-gap"
        }
        data-row-gap={index}
        aria-hidden="true"
      >
        {drag && <span>Soltar aqui: nova linha</span>}
      </div>
    );

  const style = {
    ...(layout.chartHeight !== null && { "--chart-height": `${layout.chartHeight}px` }),
    ...(layout.chartWidth !== null && { "--chart-width": `${layout.chartWidth}px` }),
  } as CSSProperties;

  return (
    <div
      ref={rootRef}
      className={editing ? "dashboard-cards is-editing" : "dashboard-cards"}
      data-chart-height={layout.chartHeight !== null ? "" : undefined}
      data-chart-width={layout.chartWidth !== null ? "" : undefined}
      data-dragging={drag ? "" : undefined}
      style={style}
    >
      {editing && (
        <section className="layout-bar card" aria-label="Personalização do layout">
          <div className="layout-bar-text">
            <span className="eyebrow">Personalizando o layout</span>
            <p id={hintId}>
              Arraste um card pela barra acima dele: solte na borda lateral de outro card para pô-lo
              ao lado, na de cima ou de baixo para empilhar, no meio para trocar os dois de lugar,
              ou entre as linhas para criar uma linha. Com a barra em foco, as setas do teclado
              também movem o card. O ✕ oculta o card. Redimensione o gráfico pelas bordas: a de
              baixo muda a altura, a da direita, a largura, e o canto, as duas.
            </p>
          </div>
          {hidden.length > 0 && (
            <div className="layout-hidden">
              <span className="layout-hidden-label">Ocultos:</span>
              {hidden.map((card) => (
                <button
                  key={card}
                  type="button"
                  className="btn btn-xs"
                  onClick={() => {
                    onLayout(setHidden(layout, card, false));
                    setAnnouncement(`${CARD_LABELS[card]} de volta ao painel.`);
                  }}
                >
                  <Icon name="eye" size={14} />
                  Mostrar {CARD_LABELS[card]}
                </button>
              ))}
            </div>
          )}
          <div className="layout-bar-actions">
            <button
              type="button"
              className="btn btn-sm"
              disabled={isDefaultLayout(layout)}
              onClick={onReset}
            >
              <Icon name="restart" size={14} />
              Restaurar padrão
            </button>
            <button
              type="button"
              className="btn btn-sm btn-primary"
              onClick={() => onEditingChange(false)}
            >
              Concluir
            </button>
          </div>
        </section>
      )}

      {rows.map((row) => {
        const columns = row.columns.map((column) => column.cards);
        const overview = columns.join("|") === "quote|sentiment|results";
        const hasChart = columns.some((cards) => cards.includes("chart"));
        const even = columns.every(
          (cards) => cards.length === 1 && cards[0] !== undefined && EVEN.has(cards[0]),
        );
        return (
          <Fragment key={`row-${columns[0]?.[0] ?? row.index}`}>
            {rowGap(row.index)}
            <div
              className={overview ? "dash-row overview" : "dash-row"}
              data-row={row.index}
              data-wrap={columns.length >= 3 ? "" : undefined}
              data-even={even ? "" : undefined}
              style={
                {
                  "--row-columns": rowColumns(columns, chartWidth && hasChart),
                } as CSSProperties
              }
            >
              {row.columns.map((column) => {
                const beside =
                  editing && target?.kind === "beside" && column.cards.includes(target.card)
                    ? target
                    : null;
                return (
                  <div
                    key={`column-${column.cards[0]}`}
                    className={beside ? `dash-col drop-${beside.side} drop-x` : "dash-col"}
                    data-cards={column.cards.join(",")}
                  >
                    {column.cards.map((card) =>
                      renderCard(
                        card,
                        column.cards.includes("chart")
                          ? card === "chart" && layout.chartHeight === null
                          : even || (column.cards.length === 1 && FILL.has(card)),
                      ),
                    )}
                  </div>
                );
              })}
            </div>
          </Fragment>
        );
      })}
      {rowGap(layout.rows.length)}

      {drag && (
        <div
          className="layout-drag-ghost"
          style={{ left: drag.x + 14, top: drag.y + 14 }}
          aria-hidden="true"
        >
          <Icon name="grip" size={14} />
          {CARD_LABELS[drag.card]}
        </div>
      )}
      <p className="sr-only" aria-live="polite">
        {announcement}
      </p>
      {/* the cards, rendered once into their elements, after the frames they are moved into */}
      {CARDS.filter(shown).map((card) => (
        <Fragment key={card}>{createPortal(cards[card], hostOf(card))}</Fragment>
      ))}
    </div>
  );
}

/**
 * The bars sizing the chart, while customizing: below it, its height, from
 * 280 px to what the window holds; on its right edge, its width, from 360 px
 * to what its row holds, the other columns of the row kept at their least
 * width, and alone in its row, the whole row, where it fills the row again
 * (the width of the responsive layout); at their corner, both. Dragged, or the
 * bars with the arrows of the keyboard; back to the size of the responsive
 * layout with their buttons. While dragged the size is set on the page alone,
 * saved once released. The width shows only where it applies: on a screen
 * whose rows aren't stacked.
 */
function ChartSizer({
  root,
  layout,
  onLayout,
  onAnnounce,
  widthApplies,
  onSizingWidth,
}: {
  root: RefObject<HTMLDivElement | null>;
  layout: DashboardLayout;
  onLayout: (layout: DashboardLayout) => void;
  onAnnounce: (message: string) => void;
  /** Whether the row of the chart takes a width, eg: not on a screen whose rows are stacked. */
  widthApplies: boolean;
  /** Tells a drag of the width starting and ending, the columns following it meanwhile. */
  onSizingWidth: (sizing: boolean) => void;
}) {
  const [liveHeight, setLiveHeight] = useState<number | null>(null);
  const [liveWidth, setLiveWidth] = useState<number | null>(null);
  const height = liveHeight ?? layout.chartHeight;
  const width = liveWidth ?? layout.chartWidth;

  const frame = () => root.current?.querySelector<HTMLElement>('[data-card="chart"]') ?? null;

  /** The height of the chart as drawn now. */
  const drawnHeight = () => {
    const chart = frame()?.querySelector<HTMLElement>(".price-chart");
    return Math.round(chart?.getBoundingClientRect().height ?? CHART_MIN_HEIGHT);
  };

  /** The width of the chart as drawn now: the one of its column. */
  const drawnWidth = () =>
    Math.round(
      frame()?.closest<HTMLElement>(".dash-col")?.getBoundingClientRect().width ?? CHART_MIN_WIDTH,
    );

  /**
   * The widest the chart goes in its row, and whether it is alone in it: the
   * row, but the least width of each other column and the gaps between them.
   */
  const widthCeiling = () => {
    const column = frame()?.closest<HTMLElement>(".dash-col");
    const row = column?.parentElement;
    if (!column || !row) return { max: CHART_MAX_WIDTH, alone: false };
    const columns = [...row.querySelectorAll<HTMLElement>(":scope > .dash-col")];
    const gap = Number.parseFloat(getComputedStyle(row).columnGap) || 16;
    const rowWidth = row.getBoundingClientRect().width;
    let rest = 0;
    for (const other of columns) {
      if (other === column) continue;
      const cards = (other.dataset.cards ?? "").split(",").filter(Boolean) as CardId[];
      rest += leastWidth(cards, columns.length, rowWidth, gap) + gap;
    }
    return { max: Math.round(rowWidth - rest), alone: columns.length === 1 };
  };

  const bound = (value: number, min: number, max: number) =>
    Math.round(Math.min(Math.max(value, min), Math.max(min, max)));

  /** Sets a size on the page while it is dragged, before it is saved. */
  const apply = (axis: "height" | "width", value: number) => {
    const element = root.current;
    if (!element) return;
    element.style.setProperty(axis === "height" ? "--chart-height" : "--chart-width", `${value}px`);
    if (axis === "height") element.dataset.chartHeight = "";
    else element.dataset.chartWidth = "";
  };

  const commit = (next: { height?: number | null; width?: number | null }) => {
    let updated = layout;
    const said: string[] = [];
    if (next.height !== undefined) {
      updated = setChartHeight(updated, next.height);
      said.push(
        next.height === null ? "altura automática" : `altura ${chartHeightOf(next.height)} px`,
      );
    }
    if (next.width !== undefined) {
      updated = setChartWidth(updated, next.width);
      said.push(
        next.width === null
          ? "largura automática, a linha inteira"
          : `largura ${chartWidthOf(next.width)} px`,
      );
    }
    onLayout(updated);
    onAnnounce(`Gráfico: ${said.join(", ")}.`);
  };

  /** The width to save for value: the responsive one for the chart alone in its row stretched to its end. */
  const widthOf = (value: number, ceiling: { max: number; alone: boolean }) =>
    ceiling.alone && value >= ceiling.max - FILL_SNAP ? null : value;

  /** A drag of the handle of event sizing the axes asked, captured by it until released. */
  const drag =
    (axes: { height?: boolean; width?: boolean }) => (event: PointerEvent<HTMLElement>) => {
      if (event.button !== 0 || (event.target as HTMLElement).closest("button")) return;
      event.preventDefault();
      event.stopPropagation();
      const handle = event.currentTarget;
      handle.setPointerCapture(event.pointerId);
      const fromHeight = drawnHeight();
      const fromWidth = drawnWidth();
      const startX = event.clientX;
      const startY = event.clientY;
      const heightMax = chartCeiling();
      const ceiling = widthCeiling();
      let nextHeight = fromHeight;
      let nextWidth = fromWidth;
      if (axes.width) {
        apply("width", fromWidth);
        onSizingWidth(true);
      }
      const move = (moveEvent: globalThis.PointerEvent) => {
        if (axes.height) {
          nextHeight = bound(fromHeight + moveEvent.clientY - startY, CHART_MIN_HEIGHT, heightMax);
          setLiveHeight(nextHeight);
          apply("height", nextHeight);
        }
        if (axes.width) {
          nextWidth = bound(fromWidth + moveEvent.clientX - startX, CHART_MIN_WIDTH, ceiling.max);
          setLiveWidth(nextWidth);
          apply("width", nextWidth);
        }
      };
      const end = () => {
        handle.removeEventListener("pointermove", move);
        handle.removeEventListener("pointerup", end);
        handle.removeEventListener("pointercancel", end);
        setLiveHeight(null);
        setLiveWidth(null);
        if (axes.width) onSizingWidth(false);
        commit({
          ...(axes.height && { height: nextHeight }),
          ...(axes.width && { width: widthOf(nextWidth, ceiling) }),
        });
      };
      handle.addEventListener("pointermove", move);
      handle.addEventListener("pointerup", end);
      handle.addEventListener("pointercancel", end);
    };

  /** The size of a key of the keyboard on an axis, from current, between min and max; null for another key. */
  const keyed = (
    event: KeyboardEvent<HTMLElement>,
    less: string,
    more: string,
    current: number,
    min: number,
    max: number,
  ) => {
    const step = event.shiftKey ? RESIZE_BIG_STEP : RESIZE_STEP;
    const next =
      event.key === less
        ? current - step
        : event.key === more
          ? current + step
          : event.key === "PageUp"
            ? current - RESIZE_BIG_STEP
            : event.key === "PageDown"
              ? current + RESIZE_BIG_STEP
              : event.key === "Home"
                ? min
                : event.key === "End"
                  ? max
                  : null;
    if (next === null) return null;
    event.preventDefault();
    return bound(next, min, max);
  };

  const heightKeys = (event: KeyboardEvent<HTMLDivElement>) => {
    const next = keyed(
      event,
      "ArrowUp",
      "ArrowDown",
      layout.chartHeight ?? drawnHeight(),
      CHART_MIN_HEIGHT,
      chartCeiling(),
    );
    if (next !== null) commit({ height: next });
  };

  const widthKeys = (event: KeyboardEvent<HTMLDivElement>) => {
    const ceiling = widthCeiling();
    const next = keyed(
      event,
      "ArrowLeft",
      "ArrowRight",
      layout.chartWidth ?? drawnWidth(),
      CHART_MIN_WIDTH,
      ceiling.max,
    );
    if (next !== null) commit({ width: widthOf(next, ceiling) });
  };

  return (
    <>
      {/* biome-ignore lint/a11y/useSemanticElements: a focusable separator sizing the chart, as the WAI-ARIA window splitter */}
      <div
        key="height"
        className="chart-resize"
        role="separator"
        aria-orientation="horizontal"
        aria-label="Altura do gráfico: arraste ou use as setas para cima e para baixo"
        aria-valuemin={CHART_MIN_HEIGHT}
        aria-valuemax={CHART_MAX_HEIGHT}
        aria-valuenow={height ?? undefined}
        aria-valuetext={height === null ? "automática" : `${height} pixels`}
        tabIndex={0}
        onPointerDown={drag({ height: true })}
        onKeyDown={heightKeys}
      >
        <span className="chart-resize-grip" aria-hidden="true" />
        <span className="chart-resize-label">
          {height === null ? "Altura automática" : `Altura ${height} px`}
          {widthApplies && ` · ${width === null ? "largura automática" : `largura ${width} px`}`} ·
          arraste as bordas para redimensionar
        </span>
        {layout.chartHeight !== null && (
          <button type="button" className="btn btn-xs" onClick={() => commit({ height: null })}>
            Altura automática
          </button>
        )}
        {widthApplies && layout.chartWidth !== null && (
          <button type="button" className="btn btn-xs" onClick={() => commit({ width: null })}>
            Largura automática
          </button>
        )}
      </div>
      {widthApplies && (
        <>
          {/* biome-ignore lint/a11y/useSemanticElements: a focusable separator sizing the chart, as the WAI-ARIA window splitter */}
          <div
            key="width"
            className="chart-resize-width"
            role="separator"
            aria-orientation="vertical"
            aria-label="Largura do gráfico: arraste ou use as setas para a esquerda e para a direita"
            aria-valuemin={CHART_MIN_WIDTH}
            aria-valuemax={CHART_MAX_WIDTH}
            aria-valuenow={width ?? undefined}
            aria-valuetext={width === null ? "automática" : `${width} pixels`}
            title="Arraste para mudar a largura do gráfico"
            tabIndex={0}
            onPointerDown={drag({ width: true })}
            onKeyDown={widthKeys}
          >
            <span className="chart-resize-grip" aria-hidden="true" />
          </div>
          <div
            key="corner"
            className="chart-resize-corner"
            aria-hidden="true"
            title="Arraste para mudar a largura e a altura do gráfico"
            onPointerDown={drag({ height: true, width: true })}
          />
        </>
      )}
    </>
  );
}
