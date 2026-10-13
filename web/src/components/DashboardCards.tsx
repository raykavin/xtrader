import {
  type CSSProperties,
  type KeyboardEvent,
  type PointerEvent,
  type ReactNode,
  type RefObject,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import {
  CARD_LABELS,
  CARDS,
  type CardId,
  CHART_MAX_HEIGHT,
  CHART_MIN_HEIGHT,
  chartHeightOf,
  clearLayout,
  type DashboardLayout,
  defaultLayout,
  isDefaultLayout,
  LAYOUT_KEY,
  loadLayout,
  moveCard,
  saveLayout,
  setChartHeight,
  setHidden,
  stepCard,
  ZONE_LABELS,
  type ZoneId,
  zoneOf,
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

/** Where a card dragged would go: before the card index of an area, eg: its end for none. */
interface DropTarget {
  zone: ZoneId;
  index: number;
  /** The card it lands beside, and on which of its sides along axis; none on an empty spot of the area. */
  card?: CardId;
  side?: "before" | "after";
  axis?: "x" | "y";
}

/** A card being dragged, and where the pointer is. */
interface Drag {
  card: CardId;
  pointer: number;
  x: number;
  y: number;
}

/** The columns of the overview row for cards other than its own: the share and the least width of each. */
const OVERVIEW_TRACKS: Record<CardId, string> = {
  quote: "minmax(300px, 4fr)",
  sentiment: "minmax(240px, 3fr)",
  results: "minmax(0, 6fr)",
  chart: "minmax(0, 8fr)",
  decision: "minmax(260px, 4fr)",
  ticket: "minmax(300px, 4fr)",
  activity: "minmax(0, 8fr)",
};

/**
 * The columns of the overview row holding cards: none for its own cards in
 * their order (the ones of the stylesheet), a column each for up to three,
 * else as many columns of 300 px at least as fit, the cards wrapping.
 */
export function overviewColumns(cards: CardId[]): string | undefined {
  if (cards.join() === "quote,sentiment,results") return undefined;
  if (cards.length > 3) return "repeat(auto-fit, minmax(300px, 1fr))";
  return cards.map((card) => OVERVIEW_TRACKS[card]).join(" ");
}

/** The step of a move of the height of the chart by the keyboard, in CSS pixels, and with Shift or a page key. */
const RESIZE_STEP = 20;
const RESIZE_BIG_STEP = 100;

/** The highest height of the chart the window takes, in CSS pixels. */
function chartCeiling(): number {
  return Math.max(
    CHART_MIN_HEIGHT,
    Math.min(CHART_MAX_HEIGHT, Math.round(window.innerHeight * 0.92)),
  );
}

/**
 * The cards of the dashboard in the areas of the page, as the user arranged
 * them (see lib/layout.ts). Out of the customization they are drawn as they
 * are, nothing added to them. Customizing, each card gets a bar above it: its
 * handle, dragged with a mouse or a finger, or moved with the arrows of the
 * keyboard, and a button hiding it; the chart a bar below it sizing its height.
 * Only those bars drag, so the buttons, the fields and the chart within the
 * cards keep working.
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
  const hintId = useId();
  const [drag, setDrag] = useState<Drag | null>(null);
  const [target, setTarget] = useState<DropTarget | null>(null);
  const [focusCard, setFocusCard] = useState<CardId | null>(null);
  const [announcement, setAnnouncement] = useState("");

  const available = (card: CardId) => cards[card] !== undefined && cards[card] !== null;
  const shown = (card: CardId) => available(card) && !layout.hidden.includes(card);
  const zoneCards = (zone: ZoneId) => layout.zones[zone].filter(shown);
  const hidden = CARDS.filter((card) => available(card) && layout.hidden.includes(card));

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

  /** Where the card dragged would land under the pointer at x, y; null over itself or out of the areas. */
  const dropAt = useCallback(
    (dragged: CardId, x: number, y: number): DropTarget | null => {
      const root = rootRef.current;
      if (!root) return null;
      for (const element of document.elementsFromPoint(x, y)) {
        if (!root.contains(element)) continue;
        const cardElement = element.closest<HTMLElement>("[data-card]");
        if (cardElement) {
          const card = cardElement.dataset.card as CardId;
          if (card === dragged) return null; // over its own place: it stays there
          const zone = cardElement.dataset.zone as ZoneId;
          const rect = cardElement.getBoundingClientRect();
          const area = cardElement.parentElement?.getBoundingClientRect() ?? rect;
          // beside the others of a row, or above and below them in a column
          const axis = rect.width < area.width * 0.75 ? "x" : "y";
          const before =
            axis === "x" ? x < rect.left + rect.width / 2 : y < rect.top + rect.height / 2;
          const index = layout.zones[zone].indexOf(card) + (before ? 0 : 1);
          return { zone, index, card, side: before ? "before" : "after", axis };
        }
        const areaElement = element.closest<HTMLElement>("[data-zone-area]");
        if (areaElement) {
          const zone = areaElement.dataset.zoneArea as ZoneId;
          return { zone, index: layout.zones[zone].length };
        }
      }
      return null;
    },
    [layout],
  );

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
        onLayout(moveCard(layout, drag.card, landing.zone, landing.index));
        setAnnouncement(`${CARD_LABELS[drag.card]} movido para a ${ZONE_LABELS[landing.zone]}.`);
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
      `Movendo ${CARD_LABELS[card]}: solte onde ele deve ficar, ou Esc para cancelar.`,
    );
  };

  const keyMove = (card: CardId) => (event: KeyboardEvent<HTMLButtonElement>) => {
    const delta =
      event.key === "ArrowUp" || event.key === "ArrowLeft"
        ? -1
        : event.key === "ArrowDown" || event.key === "ArrowRight"
          ? 1
          : 0;
    if (delta === 0) return;
    event.preventDefault();
    const next = stepCard(layout, card, delta, shown);
    if (next === layout) {
      setAnnouncement(
        `${CARD_LABELS[card]} já está ${delta < 0 ? "no início" : "no fim"} do painel.`,
      );
      return;
    }
    onLayout(next);
    const zone = zoneOf(next, card);
    const position = next.zones[zone].filter(shown).indexOf(card) + 1;
    const count = next.zones[zone].filter(shown).length;
    setAnnouncement(
      `${CARD_LABELS[card]}: posição ${position} de ${count} na ${ZONE_LABELS[zone]}.`,
    );
    setFocusCard(card);
  };

  const hide = (card: CardId) => {
    onLayout(setHidden(layout, card, true));
    setAnnouncement(`${CARD_LABELS[card]} oculto. Ele volta pelo botão Mostrar, acima dos cards.`);
  };

  /**
   * The card in its frame: out of the customization the frame draws no box
   * (display: contents), the card laid out as if it weren't there; the same
   * elements either way, so that the cards keep their state, eg: the zoom of
   * the chart or what is typed in the ticket, as the customization starts and
   * ends.
   */
  const renderCard = (card: CardId, zone: ZoneId) => {
    const node = cards[card];
    const label = CARD_LABELS[card];
    const landing = editing && target?.card === card ? target : null;
    const className = [
      "layout-card",
      drag?.card === card && "is-dragging",
      landing && `drop-${landing.side} drop-${landing.axis}`,
    ]
      .filter(Boolean)
      .join(" ");
    return (
      <div key={card} className={className} data-card={card} data-zone={zone}>
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
        <div key="body" className="layout-card-body">
          {node}
        </div>
        {editing && card === "chart" && (
          <ChartResize
            key="resize"
            root={rootRef}
            layout={layout}
            onLayout={onLayout}
            onAnnounce={setAnnouncement}
          />
        )}
      </div>
    );
  };

  /** The cards of an area, and customizing, a place to drop one when it holds none. */
  const renderZone = (zone: ZoneId) => {
    const list = zoneCards(zone);
    if (list.length > 0 || !editing) return list.map((card) => renderCard(card, zone));
    return (
      <div
        className={target?.zone === zone && !target.card ? "layout-drop is-target" : "layout-drop"}
      >
        Arraste um card para a {ZONE_LABELS[zone]}
      </div>
    );
  };

  const overview = zoneCards("overview");
  const main = zoneCards("main");
  const side = zoneCards("side");
  const bottom = zoneCards("bottom");
  const columns = overviewColumns(overview);
  const areaClass = (base: string, zone: ZoneId) =>
    editing && target?.zone === zone && !target.card ? `${base} is-target` : base;
  const style = {
    ...(layout.chartHeight !== null && { "--chart-height": `${layout.chartHeight}px` }),
  } as CSSProperties;

  return (
    <div
      ref={rootRef}
      className={editing ? "dashboard-cards is-editing" : "dashboard-cards"}
      data-chart-height={layout.chartHeight !== null ? "" : undefined}
      data-dragging={drag ? "" : undefined}
      style={style}
    >
      {editing && (
        <section className="layout-bar card" aria-label="Personalização do layout">
          <div className="layout-bar-text">
            <span className="eyebrow">Personalizando o layout</span>
            <p id={hintId}>
              Arraste um card pela barra acima dele, ou use as setas do teclado com ela em foco.
              Oculte um card no ✕ e ajuste a altura do gráfico pela barra abaixo dele.
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

      {(overview.length > 0 || editing) && (
        <div
          className={areaClass("overview", "overview")}
          data-zone-area="overview"
          style={columns ? ({ "--overview-columns": columns } as CSSProperties) : undefined}
        >
          {renderZone("overview")}
        </div>
      )}

      {(main.length > 0 || side.length > 0 || editing) && (
        <div
          className={
            !editing && (main.length === 0 || side.length === 0)
              ? "workspace workspace-single"
              : "workspace"
          }
        >
          {(main.length > 0 || editing) && (
            <div className={areaClass("workspace-main", "main")} data-zone-area="main">
              {renderZone("main")}
            </div>
          )}
          {(side.length > 0 || editing) && (
            <div className={areaClass("workspace-side", "side")} data-zone-area="side">
              {renderZone("side")}
            </div>
          )}
        </div>
      )}

      {(bottom.length > 0 || editing) && (
        <div className={areaClass("dashboard-bottom", "bottom")} data-zone-area="bottom">
          {renderZone("bottom")}
        </div>
      )}

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
    </div>
  );
}

/**
 * The bar below the chart sizing its height: dragged, or with the arrows of
 * the keyboard, from 280 px to what the window holds; back to the height of
 * the responsive layout with its button. While dragged the height is set on
 * the page alone, saved once released.
 */
function ChartResize({
  root,
  layout,
  onLayout,
  onAnnounce,
}: {
  root: RefObject<HTMLDivElement | null>;
  layout: DashboardLayout;
  onLayout: (layout: DashboardLayout) => void;
  onAnnounce: (message: string) => void;
}) {
  const [live, setLive] = useState<number | null>(null);
  const height = live ?? layout.chartHeight;

  /** The height of the chart as drawn now. */
  const drawn = () => {
    const chart = root.current?.querySelector<HTMLElement>('[data-card="chart"] .price-chart');
    return Math.round(chart?.getBoundingClientRect().height ?? CHART_MIN_HEIGHT);
  };

  /** Sets the height on the page while it is dragged, before it is saved. */
  const apply = (value: number) => {
    const element = root.current;
    if (!element) return;
    element.style.setProperty("--chart-height", `${value}px`);
    element.dataset.chartHeight = "";
  };

  const commit = (value: number | null) => {
    onLayout(setChartHeight(layout, value));
    onAnnounce(
      value === null
        ? "Altura do gráfico automática."
        : `Altura do gráfico: ${chartHeightOf(value)} px.`,
    );
  };

  const start = (event: PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0 || (event.target as HTMLElement).closest("button")) return;
    event.preventDefault();
    const handle = event.currentTarget;
    handle.setPointerCapture(event.pointerId);
    const from = drawn();
    const startY = event.clientY;
    const ceiling = chartCeiling();
    let value = from;
    const move = (moveEvent: globalThis.PointerEvent) => {
      value = Math.round(
        Math.min(Math.max(from + moveEvent.clientY - startY, CHART_MIN_HEIGHT), ceiling),
      );
      setLive(value);
      apply(value);
    };
    const end = () => {
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", end);
      handle.removeEventListener("pointercancel", end);
      setLive(null);
      commit(value);
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", end);
    handle.addEventListener("pointercancel", end);
  };

  const keys = (event: KeyboardEvent<HTMLDivElement>) => {
    const step = event.shiftKey ? RESIZE_BIG_STEP : RESIZE_STEP;
    const current = layout.chartHeight ?? drawn();
    const next =
      event.key === "ArrowUp"
        ? current - step
        : event.key === "ArrowDown"
          ? current + step
          : event.key === "PageUp"
            ? current - RESIZE_BIG_STEP
            : event.key === "PageDown"
              ? current + RESIZE_BIG_STEP
              : event.key === "Home"
                ? CHART_MIN_HEIGHT
                : event.key === "End"
                  ? chartCeiling()
                  : null;
    if (next === null) return;
    event.preventDefault();
    commit(Math.min(Math.max(next, CHART_MIN_HEIGHT), chartCeiling()));
  };

  return (
    // biome-ignore lint/a11y/useSemanticElements: a focusable separator sizing the chart, as the WAI-ARIA window splitter
    <div
      className="chart-resize"
      role="separator"
      aria-orientation="horizontal"
      aria-label="Altura do gráfico: arraste, ou use as setas para cima e para baixo"
      aria-valuemin={CHART_MIN_HEIGHT}
      aria-valuemax={CHART_MAX_HEIGHT}
      aria-valuenow={height ?? undefined}
      aria-valuetext={height === null ? "automática" : `${height} pixels`}
      tabIndex={0}
      onPointerDown={start}
      onKeyDown={keys}
    >
      <span className="chart-resize-grip" aria-hidden="true" />
      <span className="chart-resize-label">
        {height === null ? "Altura automática" : `${height} px`} · arraste para redimensionar
      </span>
      {layout.chartHeight !== null && (
        <button type="button" className="btn btn-xs" onClick={() => commit(null)}>
          Altura automática
        </button>
      )}
    </div>
  );
}
