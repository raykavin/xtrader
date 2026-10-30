import {
  type AreaData,
  AreaSeries,
  type CandlestickData,
  CandlestickSeries,
  ColorType,
  CrosshairMode,
  createChart,
  createSeriesMarkers,
  type HistogramData,
  HistogramSeries,
  type IChartApi,
  type IPaneApi,
  type IPriceLine,
  type ISeriesApi,
  type ISeriesMarkersPluginApi,
  type LineData,
  LineSeries,
  LineStyle,
  type SeriesMarker,
  type SeriesType,
  type Time,
  type UTCTimestamp,
} from "lightweight-charts";
import { type CSSProperties, useEffect, useMemo, useRef, useState } from "react";
import type {
  Analysis,
  Candle,
  Indicator,
  Metric,
  Order,
  Snapshot,
  ViewTimeframe,
} from "../api/types";
import { AnalysisPrimitive } from "../chart/analysisDrawings";
import { type DraggableLine, LineDragger } from "../chart/dragLines";
import {
  type LegendItem,
  type LegendStyle,
  PaneLegendPrimitive,
  withAlpha,
} from "../chart/paneLegend";
import { type TradeLine, TradeLinesPrimitive } from "../chart/tradeLines";
import {
  analysisCaption,
  analysisShapes,
  endedPendingSegments,
  isPendingEntry,
  pendingEntryLegs,
  pendingEntryTitle,
} from "../lib/analysis";
import {
  formatChartPrice,
  formatCompact,
  formatPercent,
  formatProfit,
  LOCALE,
} from "../lib/format";
import { shownFeatures } from "../lib/protection";
import { type PositionPnL, positionPnL } from "../lib/stats";
import { type Palette, palettes, seriesColor, type Theme } from "../lib/theme";
import { pendingLines, roundPrice, type TicketLevels } from "../lib/ticket";
import { candleUpdates, timeframeLabel } from "../lib/timeframes";
import { Icon } from "./Icon";
import { PositionProtection } from "./PositionProtection";

interface Props {
  snapshot: Snapshot;
  theme: Theme;
  /** Names the layout of the chart kept in the browser, eg: the agent. */
  layoutKey?: string;
  /** With the control panel: the levels of the order ticket, set on the chart too. */
  ticket?: {
    levels: TicketLevels;
    onLevel: (field: keyof TicketLevels, price: number) => void;
  };
  /** With the control panel: moves a pending order dropped at price. */
  onMoveOrder?: (order: Order, price: number) => Promise<void>;
  /** The analysis of the language model drawn: the last one, or one of the list shown. */
  analysis?: Analysis | null;
  /** Back to the last analysis, when one of the list is shown. */
  onAnalysisReset?: () => void;
  /**
   * The timeframe of the candles of snapshot, the one of the strategy (see
   * Snapshot.timeframe) or another one shown, see timeframes.
   */
  timeframe?: string;
  /** The timeframes the chart may be shown on, with the selector, see onTimeframeChange. */
  timeframes?: ViewTimeframe[];
  /** Shows the chart on another timeframe: the trading of the strategy as it is. */
  onTimeframeChange?: (timeframe: string) => void;
  /** How the candles of the timeframe shown stand, eg: loading, or why they failed. */
  timeframeNote?: { text: string; error?: boolean } | null;
  /**
   * The open position, of the strategy: its line kept whatever the candles
   * shown; from the last candle of snapshot when absent.
   */
  position?: PositionPnL | null;
}

/** A price line of the chart: a pending order, or a level of the order ticket. */
interface LineSpec {
  price: number;
  color: string;
  title: string;
  dashed: boolean;
  dotted?: boolean; // a level of the analysis of the strategy, see Snapshot.levels
  largeDashed?: boolean; // a leg of a pending entry, waiting for its fill
  onDrag?: (price: number) => void;
  onDrop?: (price: number) => Promise<void> | void;
}

interface MetricSeries {
  series: ISeriesApi<SeriesType>;
  count: number;
}

interface ChartState {
  chart: IChartApi;
  candles: ISeriesApi<"Candlestick">;
  line: ISeriesApi<"Area">; // the closes, shown in the line chart, see applyChartType
  volume: ISeriesApi<"Histogram">;
  markers: ISeriesMarkersPluginApi<Time>;
  tradeLines: TradeLinesPrimitive;
  analysis: AnalysisPrimitive;
  priceLines: Map<string, IPriceLine>;
  dragger: LineDragger;
  metrics: Map<string, MetricSeries>;
  paneByIndicator: Map<string, number>;
  legends: WeakMap<IPaneApi<Time>, PaneLegendPrimitive>; // the legend of each pane, see paneLegends
  pair: string;
  timeframe: string;
  drawn: Candle[]; // the candles drawn, see candleUpdates
  paneCount: number;
  indicators: string; // the names of the indicators drawn
}

const t = (unix: number) => unix as UTCTimestamp;

/** The look of the legends of the panes in palette. */
function legendStyle(palette: Palette): LegendStyle {
  return {
    fontFamily: palette.fontFamily,
    text: palette.text,
    background: withAlpha(palette.background, 0.82),
  };
}

/** The label of a metric of an indicator, eg: EMA 20, or MACD signal. */
function metricLabel(indicator: Indicator, metric: Metric): string {
  return metric.name ? `${indicator.name} ${metric.name}` : indicator.name;
}

/**
 * The legends of the panes of the chart: in each one, the indicators drawn
 * in it, the ones over the candles in the first after the pair, each other
 * one in its own.
 */
function paneLegends(state: ChartState, indicators: Indicator[], palette: Palette, pair: string) {
  const items = new Map<number, LegendItem[]>([[0, [{ label: pair, color: "", title: true }]]]);
  for (const indicator of indicators) {
    const pane = indicator.overlay ? 0 : state.paneByIndicator.get(indicator.name);
    if (pane === undefined) continue;
    const list = items.get(pane) ?? [];
    for (const metric of indicator.metrics) {
      list.push({
        label: metricLabel(indicator, metric),
        color: seriesColor(metric.color, palette.accent),
      });
    }
    items.set(pane, list);
  }
  for (const [index, pane] of state.chart.panes().entries()) {
    let legend = state.legends.get(pane);
    if (!legend) {
      legend = new PaneLegendPrimitive(legendStyle(palette));
      pane.attachPrimitive(legend);
      state.legends.set(pane, legend);
    }
    legend.setItems(items.get(index) ?? []);
  }
}

// The heights of the panes, resized by the user, are kept in the browser by
// layout and pair: the page reloaded shows them as they were left.
const paneKey = (layout: string, pair: string) => `xtrader.panes:${layout}:${pair}`;

function loadPaneFactors(key: string, count: number): number[] | null {
  try {
    const saved: unknown = JSON.parse(window.localStorage.getItem(key) ?? "null");
    return Array.isArray(saved) &&
      saved.length === count &&
      saved.every((f) => typeof f === "number" && f > 0)
      ? saved
      : null;
  } catch {
    return null;
  }
}

function savePaneFactors(key: string, factors: number[]) {
  try {
    window.localStorage.setItem(key, JSON.stringify(factors));
  } catch {
    // no storage (private mode, quota): the default heights next time
  }
}

/**
 * How the price is drawn: as candles, or as a line of the closes over an area
 * fading downwards. Kept in the browser: the page reloaded shows it as left.
 */
type ChartType = "candles" | "line";

const CHART_TYPE_KEY = "xtrader.chart.type";

function loadChartType(): ChartType {
  try {
    return window.localStorage.getItem(CHART_TYPE_KEY) === "line" ? "line" : "candles";
  } catch {
    return "candles";
  }
}

function saveChartType(type: ChartType) {
  try {
    window.localStorage.setItem(CHART_TYPE_KEY, type);
  } catch {
    // no storage (private mode, quota): the candles next time
  }
}

const CLEAR = "rgba(0, 0, 0, 0)";

/**
 * The price drawn as type: in the line chart, the line of the closes shown
 * and the candles kept but clear, the orders, the markers, the lines dragged
 * and the analysis on them as on the candles, and the price scale the one of
 * the candles: switching moves nothing.
 */
function applyChartType(state: ChartState, type: ChartType, palette: Palette) {
  const line = type === "line";
  state.candles.applyOptions({
    upColor: line ? CLEAR : palette.up,
    downColor: line ? CLEAR : palette.down,
    wickUpColor: line ? CLEAR : palette.up,
    wickDownColor: line ? CLEAR : palette.down,
    priceLineVisible: !line,
    lastValueVisible: !line,
  });
  state.line.applyOptions({
    visible: line,
    lineColor: palette.price,
    topColor: withAlpha(palette.price, 0.28),
    bottomColor: withAlpha(palette.price, 0.02),
    priceLineColor: palette.price,
  });
}

function toClose(c: Candle): AreaData<Time> {
  return { time: t(c.time), value: c.close };
}

function toCandle(c: Candle): CandlestickData<Time> {
  return { time: t(c.time), open: c.open, high: c.high, low: c.low, close: c.close };
}

function toVolume(c: Candle, palette: Palette): HistogramData<Time> {
  return {
    time: t(c.time),
    value: c.volume,
    color: c.close >= c.open ? palette.volumeUp : palette.volumeDown,
  };
}

function metricKey(indicator: Indicator, metric: Metric, index: number): string {
  return `${indicator.name}::${metric.name || index}`;
}

function metricData(metric: Metric): (LineData<Time> | HistogramData<Time>)[] {
  const out: (LineData<Time> | HistogramData<Time>)[] = [];
  let lastTime = Number.NEGATIVE_INFINITY;
  for (const p of metric.points) {
    if (!Number.isFinite(p.value) || p.time <= lastTime) continue;
    lastTime = p.time;
    out.push({ time: t(p.time), value: p.value });
  }
  return out;
}

// The formats of the series, each its own: a formatter of the localization
// of the chart would take the place of all of them. The chart sizes its
// scales for the label of the crosshair of any value, the panes all as wide
// as the widest: the prices as the broker quotes them (see formatChartPrice),
// the values of the indicators in short, eg: 12.000 or 13,57, never wider.
const priceFormat = { type: "custom" as const, formatter: formatChartPrice };
const indicatorFormat = { type: "custom" as const, formatter: formatCompact };

function createMetricSeries(
  chart: IChartApi,
  metric: Metric,
  paneIndex: number,
  palette: Palette,
): ISeriesApi<SeriesType> {
  const color = seriesColor(metric.color, palette.accent);
  switch (metric.style) {
    case "bar":
    case "histogram":
    case "waterfall":
      return chart.addSeries(
        HistogramSeries,
        {
          color,
          priceFormat: indicatorFormat,
          priceLineVisible: false,
          lastValueVisible: false,
          title: metric.name,
        },
        paneIndex,
      );
    case "scatter":
      return chart.addSeries(
        LineSeries,
        {
          color,
          priceFormat: indicatorFormat,
          lineVisible: false,
          pointMarkersVisible: true,
          pointMarkersRadius: 2,
          priceLineVisible: false,
          lastValueVisible: false,
          title: metric.name,
        },
        paneIndex,
      );
    default:
      return chart.addSeries(
        LineSeries,
        {
          color,
          priceFormat: indicatorFormat,
          lineWidth: 1,
          priceLineVisible: false,
          lastValueVisible: false,
          crosshairMarkerVisible: false,
          title: metric.name,
        },
        paneIndex,
      );
  }
}

function buildMarkers(orders: Order[], candles: Candle[], palette: Palette): SeriesMarker<Time>[] {
  const times = new Set(candles.map((c) => c.time));
  const markers: SeriesMarker<Time>[] = [];
  for (const order of orders) {
    if (order.status !== "FILLED" || !times.has(order.candle_time)) continue;
    const buy = order.side === "BUY";
    const profit = order.profit !== 0 ? ` ${formatPercent(order.profit, 1)}` : "";
    markers.push({
      time: t(order.candle_time),
      position: buy ? "belowBar" : "aboveBar",
      shape: buy ? "arrowUp" : "arrowDown",
      color: buy ? palette.up : palette.down,
      text: `${buy ? "C" : "V"} ${formatCompact(order.quantity)}${profit}`,
    });
  }
  markers.sort((a, b) => (a.time as number) - (b.time as number));
  return markers;
}

/**
 * OCO orders (stop loss / limit maker) are drawn as a line from the price
 * where they were created (ref_price) to their trigger price, over the time
 * they were open. Green for take-profit targets, red for stop losses. The
 * pending entries that ended are drawn at their price over the time they
 * waited, see endedPendingSegments.
 */
function buildTradeLines(orders: Order[], candles: Candle[], palette: Palette): TradeLine[] {
  if (candles.length === 0) return [];
  const first = candles[0]?.time ?? 0;
  const last = candles[candles.length - 1]?.time ?? 0;
  const snap = (time: number) => {
    let result = first;
    for (const c of candles) {
      if (c.time > time) break;
      result = c.time;
    }
    return Math.min(Math.max(result, first), last);
  };

  const lines: TradeLine[] = [];
  for (const order of orders) {
    if (order.type !== "STOP_LOSS" && order.type !== "LIMIT_MAKER") continue;
    if (!order.ref_price || !order.price) continue;
    const isStop = order.type === "STOP_LOSS";
    lines.push({
      fromTime: snap(order.created_at),
      fromPrice: order.ref_price,
      toTime: snap(order.updated_at),
      toPrice: order.price,
      color: isStop ? palette.down : palette.up,
      dashed: order.status !== "FILLED",
    });
  }
  for (const segment of endedPendingSegments(orders, candles, palette)) {
    lines.push({
      fromTime: segment.fromTime,
      fromPrice: segment.price,
      toTime: segment.toTime,
      toPrice: segment.price,
      color: segment.color,
      dashed: segment.dashed,
    });
  }
  return lines;
}

// The colors of the chart, applied again when the theme changes: never its
// position (the scroll, the zoom, the panes), which the user moves.
function themeOptions(palette: Palette) {
  return {
    layout: {
      // transparent: drawn on the glass of its card
      background: { type: ColorType.Solid, color: "transparent" },
      textColor: palette.text,
      panes: { separatorColor: palette.border, separatorHoverColor: palette.accent },
    },
    grid: { vertLines: { color: palette.grid }, horzLines: { color: palette.grid } },
    crosshair: {
      vertLine: { color: palette.crosshair, labelBackgroundColor: palette.crosshair },
      horzLine: { color: palette.crosshair, labelBackgroundColor: palette.crosshair },
    },
    rightPriceScale: { borderColor: palette.border },
    timeScale: { borderColor: palette.border },
  };
}

// The options of the chart when created.
function chartOptions(palette: Palette) {
  return {
    autoSize: true,
    layout: {
      // transparent: drawn on the glass of its card
      background: { type: ColorType.Solid, color: "transparent" },
      textColor: palette.text,
      fontFamily: palette.fontFamily,
      fontSize: 11,
      attributionLogo: false,
      panes: {
        separatorColor: palette.border,
        separatorHoverColor: palette.accent,
        enableResize: true,
      },
    },
    grid: { vertLines: { color: palette.grid }, horzLines: { color: palette.grid } },
    crosshair: {
      mode: CrosshairMode.Normal,
      vertLine: { color: palette.crosshair, labelBackgroundColor: palette.crosshair },
      horzLine: { color: palette.crosshair, labelBackgroundColor: palette.crosshair },
    },
    localization: { locale: LOCALE },
    // Keep the candles above the volume histogram (bottom ~18% of the pane).
    rightPriceScale: { borderColor: palette.border, scaleMargins: { top: 0.08, bottom: 0.22 } },
    timeScale: {
      borderColor: palette.border,
      timeVisible: true,
      secondsVisible: false,
      rightOffset: 5,
    },
  };
}

// The price scales of every pane fit to their data again, as when the chart
// was created: a scale the user dragged or zoomed keeps its range otherwise,
// eg: the one of the pair shown before.
function autoScale(chart: IChartApi) {
  chart.priceScale("volume").applyOptions({ autoScale: true });
  for (const [index] of chart.panes().entries()) {
    chart.priceScale("right", index).applyOptions({ autoScale: true });
  }
}

/**
 * How the chart is shown alone: in the fullscreen of the browser, or over the
 * page where the browser has none (eg: the Safari of the iPhone).
 */
type Fullscreen = "browser" | "page" | null;

export function PriceChart({
  snapshot,
  theme,
  layoutKey = "",
  ticket,
  onMoveOrder,
  analysis,
  onAnalysisReset,
  timeframe = "",
  timeframes = [],
  onTimeframeChange,
  timeframeNote,
  position,
}: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const cardRef = useRef<HTMLElement>(null);
  const stateRef = useRef<ChartState | null>(null);
  const [fullscreen, setFullscreen] = useState<Fullscreen>(null);
  const [chartType, setChartType] = useState<ChartType>(loadChartType);
  const lastClose = useRef(0);
  lastClose.current = snapshot.candles[snapshot.candles.length - 1]?.close ?? 0;
  const ownPnL = useMemo(() => positionPnL(snapshot), [snapshot]);
  const pnl = position === undefined ? ownPnL : position;
  const execution = timeframes.find((v) => v.execution)?.timeframe ?? snapshot.timeframe ?? "";
  const otherView = timeframe !== "" && execution !== "" && timeframe !== execution;
  const snapshotRef = useRef(snapshot);
  snapshotRef.current = snapshot;
  const layoutRef = useRef(layoutKey);
  layoutRef.current = layoutKey;
  const [moveError, setMoveError] = useState<string | null>(null);
  // the width of the price scale, the button of the current price beside it
  const [scaleWidth, setScaleWidth] = useState(0);

  // Create / destroy the chart with the container.
  // biome-ignore lint/correctness/useExhaustiveDependencies: the chart is created once; theme and data are applied by the effects below
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const palette = palettes[theme];
    const chart = createChart(container, chartOptions(palette));

    // the line chart under the candles: the orders and the analysis on them over it
    const line = chart.addSeries(AreaSeries, {
      priceFormat,
      lineWidth: 2,
      priceLineStyle: LineStyle.Dotted,
      crosshairMarkerRadius: 4,
      visible: false,
    });
    const candles = chart.addSeries(CandlestickSeries, {
      priceFormat,
      upColor: palette.up,
      downColor: palette.down,
      borderVisible: false,
      wickUpColor: palette.up,
      wickDownColor: palette.down,
    });
    const volume = chart.addSeries(HistogramSeries, {
      priceFormat: { type: "volume" },
      priceScaleId: "volume",
      priceLineVisible: false,
      lastValueVisible: false,
    });
    chart.priceScale("volume").applyOptions({ scaleMargins: { top: 0.82, bottom: 0 } });

    const tradeLines = new TradeLinesPrimitive();
    candles.attachPrimitive(tradeLines);
    const analysisDrawings = new AnalysisPrimitive(palette.fontFamily, palette.background);
    candles.attachPrimitive(analysisDrawings);
    const markers = createSeriesMarkers(candles, []);
    const dragger = new LineDragger(container, candles, (price) =>
      roundPrice(price, lastClose.current || price),
    );

    // the heights of the panes, once resized: kept for the next load
    const savePanes = () => {
      const panes = chart.panes();
      if (panes.length < 2) return;
      savePaneFactors(
        paneKey(layoutRef.current, snapshotRef.current.pair),
        panes.map((pane) => pane.getStretchFactor()),
      );
    };
    container.addEventListener("pointerup", savePanes);

    stateRef.current = {
      chart,
      candles,
      line,
      volume,
      markers,
      tradeLines,
      analysis: analysisDrawings,
      priceLines: new Map(),
      dragger,
      metrics: new Map(),
      paneByIndicator: new Map(),
      legends: new WeakMap(),
      pair: "",
      timeframe: "",
      drawn: [],
      paneCount: 0,
      indicators: "",
    };

    return () => {
      container.removeEventListener("pointerup", savePanes);
      dragger.destroy();
      chart.remove();
      stateRef.current = null;
    };
  }, []);

  // The pending orders and the levels of the order ticket, as price lines: the
  // movable ones are dragged to move them.
  const levels = ticket?.levels;
  const onLevel = ticket?.onLevel;
  useEffect(() => {
    const state = stateRef.current;
    if (!state) return;
    const palette = palettes[theme];
    const specs = new Map<string, LineSpec>();

    // the pending entries, a stop one included, apart from the legs: the ones
    // waiting for the fill of an entry (pend.) apart from the ones of a position
    const waiting = pendingEntryLegs(snapshot.orders);
    for (const { order, kind, price, movable } of pendingLines(snapshot.orders)) {
      const entry = isPendingEntry(order);
      const pending = waiting.has(order.id);
      specs.set(`order:${order.id}`, {
        price,
        color: entry ? palette.accent : kind === "stop" ? palette.down : palette.up,
        title: entry
          ? pendingEntryTitle(order)
          : `${kind === "stop" ? "SL" : "TP"}${pending ? " pend." : ""}`,
        dashed: false,
        largeDashed: pending,
        onDrop:
          movable && onMoveOrder
            ? (to) =>
                onMoveOrder(order, to).catch((e: unknown) => {
                  setMoveError(e instanceof Error ? e.message : String(e));
                  throw e;
                })
            : undefined,
      });
    }

    // The open position and its profit, updated with every tick, see
    // positionPnL: the one of the broker (MT5) when it reports it.
    const open = pnl;
    if (open) {
      const side = open.side === "BUY" ? "C" : "V";
      const profit =
        open.profit === null
          ? "—"
          : `${formatProfit(open.profit)} ${open.currency}${open.simulated ? " (sim.)" : ""}`;
      specs.set("position", {
        price: open.entry,
        color: (open.profit ?? 0) >= 0 ? palette.up : palette.down,
        title: `${side} ${formatCompact(open.quantity)} P&L Aberto: ${profit}`,
        dashed: false,
      });
    }

    // The supports and resistances of the last analysis of the strategy: what
    // the model saw, dotted apart from the orders and the ticket.
    for (const [index, level] of (snapshot.levels ?? []).entries()) {
      const support = level.kind === "support";
      specs.set(`level:${index}`, {
        price: level.price,
        color: support ? palette.up : palette.down,
        title: support ? "S" : "R",
        dashed: false,
        dotted: true,
      });
    }

    if (levels && onLevel) {
      const fields = [
        ["entry", "Entrada", palette.accent],
        ["stop", "SL", palette.down],
        ["profit", "TP", palette.up],
      ] as const;
      for (const [field, title, color] of fields) {
        const price = levels[field];
        if (price === null) continue;
        const set = (to: number) => onLevel(field, to);
        specs.set(`ticket:${field}`, {
          price,
          color,
          title,
          dashed: true,
          onDrag: set,
          onDrop: set,
        });
      }
    }

    for (const [key, line] of state.priceLines) {
      if (!specs.has(key)) {
        state.candles.removePriceLine(line);
        state.priceLines.delete(key);
      }
    }
    const draggable: DraggableLine[] = [];
    for (const [key, spec] of specs) {
      const dragged = state.dragger.dragging(key);
      const existing = state.priceLines.get(key);
      const price = dragged && existing ? existing.options().price : spec.price;
      const options = {
        price,
        color: spec.color,
        title: spec.title,
        lineWidth: 1 as const,
        lineStyle: spec.dotted
          ? LineStyle.Dotted
          : spec.largeDashed
            ? LineStyle.LargeDashed
            : spec.dashed
              ? LineStyle.Dashed
              : LineStyle.Solid,
        axisLabelVisible: true,
      };
      const line = existing ?? state.candles.createPriceLine(options);
      if (existing) line.applyOptions(options);
      state.priceLines.set(key, line);
      if (spec.onDrop) {
        draggable.push({ key, line, price, onDrag: spec.onDrag, onDrop: spec.onDrop });
      }
    }
    state.dragger.setLines(draggable);
  }, [snapshot.orders, snapshot.levels, pnl, theme, levels, onLevel, onMoveOrder]);

  // The analysis of the language model: its zones, levels and markers, and
  // the prices of its intent, drawn behind the candles, see AnalysisPrimitive.
  useEffect(() => {
    const state = stateRef.current;
    if (!state) return;
    const palette = palettes[theme];
    state.analysis.background = palette.background;
    state.analysis.setShapes(analysisShapes(analysis, snapshot.candles, palette));
  }, [analysis, snapshot.candles, theme]);

  // A move refused shows for a while.
  useEffect(() => {
    if (!moveError) return;
    const timer = window.setTimeout(() => setMoveError(null), 8000);
    return () => window.clearTimeout(timer);
  }, [moveError]);

  // Theme changes only: the data, which changes every second with the candle
  // in progress, is drawn by the effect below, without moving the chart.
  useEffect(() => {
    const state = stateRef.current;
    if (!state) return;
    const palette = palettes[theme];
    const { candles, orders } = snapshotRef.current;
    state.chart.applyOptions(themeOptions(palette));
    state.volume.setData(candles.map((c) => toVolume(c, palette)));
    state.markers.setMarkers(buildMarkers(orders, candles, palette));
    state.tradeLines.setLines(buildTradeLines(orders, candles, palette));
    for (const pane of state.chart.panes()) state.legends.get(pane)?.setStyle(legendStyle(palette));
  }, [theme]);

  // Data changes: drawn again whole, the chart fit to them, on another pair,
  // timeframe or indicators; else the candles changed only, see candleUpdates.
  useEffect(() => {
    const state = stateRef.current;
    if (!state) return;
    const palette = palettes[theme];
    // other indicators, eg: the prompt of the strategy switched, are drawn
    // again whole, their panes with them
    const indicators = snapshot.indicators.map((i) => i.name).join("\n");
    const same =
      state.pair === snapshot.pair &&
      state.timeframe === timeframe &&
      state.indicators === indicators;
    const updates = same ? candleUpdates(state.drawn, snapshot.candles) : null;
    const incremental = updates !== null;

    if (!updates) {
      state.candles.setData(snapshot.candles.map(toCandle));
      state.line.setData(snapshot.candles.map(toClose));
      state.volume.setData(snapshot.candles.map((c) => toVolume(c, palette)));
    } else {
      // from the last candle drawn: the one in progress changes until its
      // close; before it, the ones read again changed, eg: a candle of a
      // timeframe below the one of the strategy built from its prices
      for (const { index, historical } of updates) {
        const c = snapshot.candles[index];
        if (!c) continue;
        state.candles.update(toCandle(c), historical);
        state.line.update(toClose(c), historical);
        state.volume.update(toVolume(c, palette), historical);
      }
    }

    // Indicators: create missing series, update existing ones.
    const seen = new Set<string>();
    let nextPane =
      Math.max(1, ...state.paneByIndicator.values()) + (state.paneByIndicator.size ? 1 : 0);
    if (!same) {
      nextPane = 1;
      for (const { series } of state.metrics.values()) state.chart.removeSeries(series);
      state.metrics.clear();
      state.paneByIndicator.clear();
    }

    for (const indicator of snapshot.indicators) {
      let paneIndex = 0;
      if (!indicator.overlay) {
        const existing = state.paneByIndicator.get(indicator.name);
        if (existing === undefined) {
          paneIndex = nextPane++;
          state.paneByIndicator.set(indicator.name, paneIndex);
        } else {
          paneIndex = existing;
        }
      }

      for (const [index, metric] of indicator.metrics.entries()) {
        const key = metricKey(indicator, metric, index);
        seen.add(key);
        const data = metricData(metric);
        let entry = state.metrics.get(key);
        if (!entry) {
          entry = { series: createMetricSeries(state.chart, metric, paneIndex, palette), count: 0 };
          state.metrics.set(key, entry);
          entry.series.setData(data);
          entry.count = data.length;
          continue;
        }
        if (incremental && data.length >= entry.count && data.length - entry.count <= 5) {
          for (const point of data.slice(entry.count)) entry.series.update(point);
        } else {
          entry.series.setData(data);
        }
        entry.count = data.length;
      }
    }

    for (const [key, entry] of state.metrics) {
      if (!seen.has(key)) {
        state.chart.removeSeries(entry.series);
        state.metrics.delete(key);
      }
    }

    // Give indicator panes a sensible share of the height, when they come or
    // go: the user may resize them in between.
    const panes = state.chart.panes();
    if (panes.length !== state.paneCount || !same) {
      const saved = loadPaneFactors(paneKey(layoutKey, snapshot.pair), panes.length);
      for (const [index, pane] of panes.entries()) {
        pane.setStretchFactor(saved?.[index] ?? (index === 0 ? 3 : 1));
      }
      state.paneCount = panes.length;
    }
    paneLegends(state, snapshot.indicators, palette, snapshot.pair);

    state.markers.setMarkers(buildMarkers(snapshot.orders, snapshot.candles, palette));
    state.tradeLines.setLines(buildTradeLines(snapshot.orders, snapshot.candles, palette));

    if (!same || (state.drawn.length === 0 && snapshot.candles.length > 0)) {
      // the prices of this pair, whatever the scale of the one before; the
      // candles of a timeframe once loaded
      autoScale(state.chart);
      state.chart.timeScale().fitContent();
    }
    // drawn on the next frame: the width of its price scale known then
    const frame = window.requestAnimationFrame(() => {
      const width = stateRef.current?.chart.priceScale("right").width() ?? 0;
      setScaleWidth((previous) => (previous === width ? previous : width));
    });
    state.pair = snapshot.pair;
    state.timeframe = timeframe;
    state.drawn = snapshot.candles;
    state.indicators = indicators;
    return () => window.cancelAnimationFrame(frame);
  }, [snapshot, theme, layoutKey, timeframe]);

  // The fullscreen of the browser left, eg: with Esc, followed.
  useEffect(() => {
    const changed = () =>
      setFullscreen((current) =>
        document.fullscreenElement && document.fullscreenElement === cardRef.current
          ? "browser"
          : current === "browser"
            ? null
            : current,
      );
    document.addEventListener("fullscreenchange", changed);
    return () => document.removeEventListener("fullscreenchange", changed);
  }, []);

  // Over the page: the page under it kept still, and Esc leaves it.
  useEffect(() => {
    if (fullscreen !== "page") return;
    const leave = (event: KeyboardEvent) => {
      if (event.key === "Escape") setFullscreen(null);
    };
    document.documentElement.classList.add("chart-fullscreen-open");
    window.addEventListener("keydown", leave);
    return () => {
      document.documentElement.classList.remove("chart-fullscreen-open");
      window.removeEventListener("keydown", leave);
    };
  }, [fullscreen]);

  // The chart alone on the screen, its card in the fullscreen of the
  // browser, or over the page where it has none; the same button leaves it.
  const toggleFullscreen = () => {
    const card = cardRef.current;
    if (!card) return;
    if (fullscreen === "browser") {
      void document.exitFullscreen().catch(() => setFullscreen(null));
      return;
    }
    if (fullscreen === "page") {
      setFullscreen(null);
      return;
    }
    if (document.fullscreenEnabled && typeof card.requestFullscreen === "function") {
      card.requestFullscreen().catch(() => setFullscreen("page"));
    } else {
      setFullscreen("page");
    }
  };

  // The price as candles or as a line, in the colors of the theme.
  useEffect(() => {
    const state = stateRef.current;
    if (state) applyChartType(state, chartType, palettes[theme]);
  }, [chartType, theme]);

  const toggleChartType = () => {
    const next = chartType === "line" ? "candles" : "line";
    setChartType(next);
    saveChartType(next);
  };

  // The last candle at the right edge, the price scales fit to the data.
  const goLive = () => {
    const state = stateRef.current;
    if (!state) return;
    autoScale(state.chart);
    state.chart.timeScale().scrollToRealTime();
  };

  const legend = snapshot.indicators.flatMap((indicator) =>
    indicator.metrics.map((metric, index) => ({
      key: metricKey(indicator, metric, index),
      label: metricLabel(indicator, metric),
    })),
  );
  const hasProtection = shownFeatures(snapshot.protection).length > 0;

  return (
    <section
      ref={cardRef}
      className={fullscreen ? "card chart-card chart-fullscreen" : "card chart-card"}
      aria-label={`Gráfico de ${snapshot.pair}`}
    >
      {(analysis || hasProtection) && (
        <div className="chart-meta">
          {analysis && (
            <span className="analysis-chip" title={analysisCaption(analysis)}>
              <Icon name="sparkles" size={13} />
              <span className="analysis-chip-text">{analysisCaption(analysis)}</span>
              {onAnalysisReset && (
                <button type="button" className="btn btn-xs" onClick={onAnalysisReset}>
                  Voltar à análise atual
                </button>
              )}
            </span>
          )}
          <PositionProtection protection={snapshot.protection} />
        </div>
      )}

      {/* the legends are drawn in the panes of the chart, see paneLegends: listed here for the screen readers */}
      {legend.length > 0 && (
        <ul className="sr-only" aria-label="Indicadores">
          {legend.map((item) => (
            <li key={item.key}>{item.label}</li>
          ))}
        </ul>
      )}

      {(moveError || timeframeNote || (otherView && !timeframeNote)) && (
        <div className="chart-notes">
          {moveError && (
            <p className="msg msg-error" role="alert">
              {moveError}
            </p>
          )}
          {otherView && !timeframeNote && (
            <p className="msg">
              Visualização em {timeframeLabel(timeframe)}: ordens, análise e indicadores do{" "}
              {timeframeLabel(execution)} alinhados aos candles.
            </p>
          )}
          {timeframeNote && (
            <p
              className={timeframeNote.error ? "msg msg-error" : "msg"}
              role={timeframeNote.error ? "alert" : "status"}
            >
              {timeframeNote.text}
            </p>
          )}
        </div>
      )}

      <div className="chart-frame">
        <div ref={containerRef} className="chart price-chart" />
        <div
          className="chart-toolbar"
          style={{ "--chart-scale": `${scaleWidth}px` } as CSSProperties}
        >
          {timeframes.length > 0 ? (
            <fieldset className="seg timeframes chart-timeframes">
              <legend>Timeframe do gráfico</legend>
              {timeframes.map((view) => {
                const label = timeframeLabel(view.timeframe);
                const title = !view.available
                  ? `${label}: indisponível neste agente (sem fonte de candles abaixo do timeframe de execução)`
                  : view.execution
                    ? `${label}: timeframe de execução, o da estratégia e das análises do LLM (muda só nas configurações do agente, com reinício)`
                    : `${label}: só a visualização; a estratégia segue no ${timeframeLabel(execution)}`;
                return (
                  <button
                    key={view.timeframe}
                    type="button"
                    className={
                      view.execution ? "seg-item timeframe execution" : "seg-item timeframe"
                    }
                    aria-pressed={view.timeframe === timeframe}
                    aria-label={view.execution ? `${label} (execução)` : label}
                    disabled={!view.available || !onTimeframeChange}
                    title={title}
                    onClick={() => onTimeframeChange?.(view.timeframe)}
                  >
                    {label}
                  </button>
                );
              })}
            </fieldset>
          ) : (
            (timeframe || execution) && (
              <span className="chart-timeframe-tag">{timeframeLabel(timeframe || execution)}</span>
            )
          )}
          <span className="chart-toolbar-buttons">
            <button
              type="button"
              className="btn btn-icon chart-overlay-btn"
              title={fullscreen ? "Sair da tela cheia (Esc)" : "Gráfico em tela cheia"}
              aria-label={fullscreen ? "Sair da tela cheia" : "Gráfico em tela cheia"}
              aria-pressed={fullscreen !== null}
              onClick={toggleFullscreen}
            >
              <Icon name={fullscreen ? "shrink" : "expand"} size={15} />
            </button>
            <button
              type="button"
              className="btn btn-icon chart-overlay-btn"
              title={chartType === "line" ? "Mostrar em candles" : "Mostrar em linha"}
              aria-label={chartType === "line" ? "Mostrar em candles" : "Mostrar em linha"}
              onClick={toggleChartType}
            >
              <Icon name={chartType === "line" ? "candles" : "area"} size={15} />
            </button>
            <button
              type="button"
              className="btn btn-icon chart-overlay-btn"
              title="Ir para o preço atual"
              aria-label="Ir para o preço atual"
              onClick={goLive}
            >
              <Icon name="crosshair" size={15} />
            </button>
          </span>
        </div>
      </div>
    </section>
  );
}
