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
  CHART_MAX_WIDTH,
  CHART_MIN_HEIGHT,
  CHART_MIN_WIDTH,
  chartHeightOf,
  chartWidthOf,
  clearLayout,
  type DashboardLayout,
  defaultLayout,
  isDefaultLayout,
  LAYOUT_KEY,
  loadLayout,
  moveCard,
  saveLayout,
  setChartHeight,
  setChartWidth,
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

/** The least width of the column beside the chart in the workspace: the side one, the main one. */
const SIDE_MIN_WIDTH = 300;
const MAIN_MIN_WIDTH = 360;

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
export function overviewColumns(cards: CardId[], chartWidth = false): string | undefined {
  if (cards.join() === "quote,sentiment,results") return undefined;
  if (cards.length > 3) return "repeat(auto-fit, minmax(300px, 1fr))";
  return cards
    .map((card) => (card === "chart" && chartWidth ? CHART_WIDTH_TRACK : OVERVIEW_TRACKS[card]))
    .join(" ");
}

/** The column of the chart of the width the user set, see DashboardLayout.chartWidth. */
const CHART_WIDTH_TRACK = "minmax(0, var(--chart-width))";

/**
 * The columns of the workspace holding the chart of the width the user set
 * beside another column: the chart's column of that width, the other taking
 * the rest, at its least width; none for the ones of the stylesheet.
 */
export function workspaceColumns(
  chartZone: ZoneId,
  withMain: boolean,
  withSide: boolean,
): string | undefined {
  if (chartZone === "main" && withSide)
    return `${CHART_WIDTH_TRACK} minmax(${SIDE_MIN_WIDTH}px, 1fr)`;
  if (chartZone === "side" && withMain)
    return `minmax(${MAIN_MIN_WIDTH}px, 1fr) ${CHART_WIDTH_TRACK}`;
  return undefined;
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
  // a drag of the width of the chart going on: its columns follow it before it is saved
  const [sizingWidth, setSizingWidth] = useState(false);

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
          <ChartSizer
            key="size"
            root={rootRef}
            layout={layout}
            onLayout={onLayout}
            onAnnounce={setAnnouncement}
            widthApplies={!(zone === "overview" && zoneCards("overview").length > 3)}
            onSizingWidth={setSizingWidth}
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
  // the width of the chart, set or being dragged, and the area of the chart
  const chartWidth = sizingWidth || layout.chartWidth !== null;
  const chartZone = zoneOf(layout, "chart");
  const chartShown = shown("chart");
  const columns = overviewColumns(overview, chartWidth && chartShown);
  const workspaceStyle =
    chartWidth && chartShown
      ? workspaceColumns(chartZone, main.length > 0 || editing, side.length > 0 || editing)
      : undefined;
  const areaClass = (base: string, zone: ZoneId) =>
    editing && target?.zone === zone && !target.card ? `${base} is-target` : base;
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
              Arraste um card pela barra acima dele, ou use as setas do teclado com ela em foco.
              Oculte um card no ✕ e ajuste o tamanho do gráfico pelas bordas dele: a barra de baixo
              muda a altura, a da direita a largura, e o canto, as duas.
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
          style={
            workspaceStyle
              ? ({ "--workspace-columns": workspaceStyle } as CSSProperties)
              : undefined
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
 * The bars sizing the chart, while customizing: below it, its height, from
 * 280 px to what the window holds; on its right edge, its width, from 360 px
 * to what its area holds (the other column of the workspace kept at its least
 * width); at their corner, both. Dragged, or the bars with the arrows of the
 * keyboard; back to the size of the responsive layout with their buttons.
 * While dragged the size is set on the page alone, saved once released. The
 * width shows only where it applies: on a screen whose areas aren't stacked.
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
  /** Whether the area of the chart takes a width, eg: not the overview row wrapping more than three cards. */
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

  /** The width of the chart as drawn now: the one of its frame, its column. */
  const drawnWidth = () => Math.round(frame()?.getBoundingClientRect().width ?? CHART_MIN_WIDTH);

  /** The widest the chart goes in its area: the area, but the least width of the other column. */
  const widthCeiling = () => {
    const card = frame();
    const area = card?.parentElement;
    if (!card || !area) return CHART_MAX_WIDTH;
    const zone = card.dataset.zone as ZoneId;
    const workspace = area.parentElement;
    if ((zone === "main" || zone === "side") && workspace?.classList.contains("workspace")) {
      const other = workspace.querySelector(
        zone === "main" ? ".workspace-side" : ".workspace-main",
      );
      if (other) {
        const gap = Number.parseFloat(getComputedStyle(workspace).columnGap) || 16;
        const least = zone === "main" ? SIDE_MIN_WIDTH : MAIN_MIN_WIDTH;
        return Math.round(workspace.getBoundingClientRect().width - gap - least);
      }
    }
    return Math.round(area.getBoundingClientRect().width);
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
        next.width === null ? "largura automática" : `largura ${chartWidthOf(next.width)} px`,
      );
    }
    onLayout(updated);
    onAnnounce(`Gráfico: ${said.join(", ")}.`);
  };

  /** A drag of the handle of event sizing the axes asked, captured by it until released. */
  const drag =
    (axes: { height?: boolean; width?: boolean }) => (event: PointerEvent<HTMLElement>) => {
      if (event.button !== 0 || (event.target as HTMLElement).closest("button")) return;
      event.preventDefault();
      const handle = event.currentTarget;
      handle.setPointerCapture(event.pointerId);
      const fromHeight = drawnHeight();
      const fromWidth = drawnWidth();
      const startX = event.clientX;
      const startY = event.clientY;
      const heightMax = chartCeiling();
      const widthMax = widthCeiling();
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
          nextWidth = bound(fromWidth + moveEvent.clientX - startX, CHART_MIN_WIDTH, widthMax);
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
          ...(axes.width && { width: nextWidth }),
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
    const next = keyed(
      event,
      "ArrowLeft",
      "ArrowRight",
      layout.chartWidth ?? drawnWidth(),
      CHART_MIN_WIDTH,
      widthCeiling(),
    );
    if (next !== null) commit({ width: next });
  };

  return (
    <>
      {/* biome-ignore lint/a11y/useSemanticElements: a focusable separator sizing the chart, as the WAI-ARIA window splitter */}
      <div
        key="height"
        className="chart-resize"
        role="separator"
        aria-orientation="horizontal"
        aria-label="Altura do gráfico: arraste, ou use as setas para cima e para baixo"
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
            aria-label="Largura do gráfico: arraste, ou use as setas para a esquerda e para a direita"
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
