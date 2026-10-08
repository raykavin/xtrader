import {
  type CandlestickData,
  CandlestickSeries,
  ColorType,
  CrosshairMode,
  createChart,
  createSeriesMarkers,
  type HistogramData,
  HistogramSeries,
  type IChartApi,
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
import { useEffect, useMemo, useRef, useState } from "react";
import type { Candle, Indicator, Metric, Order, Snapshot } from "../api/types";
import { type DraggableLine, LineDragger } from "../chart/dragLines";
import { type TradeLine, TradeLinesPrimitive } from "../chart/tradeLines";
import {
  formatChartPrice,
  formatCompact,
  formatPercent,
  formatProfit,
  LOCALE,
} from "../lib/format";
import { positionPnL } from "../lib/stats";
import { type Palette, palettes, seriesColor, type Theme } from "../lib/theme";
import { pendingLines, roundPrice, type TicketLevels } from "../lib/ticket";

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
}

/** A price line of the chart: a pending order, or a level of the order ticket. */
interface LineSpec {
  price: number;
  color: string;
  title: string;
  dashed: boolean;
  dotted?: boolean; // a level of the analysis of the strategy, see Snapshot.levels
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
  volume: ISeriesApi<"Histogram">;
  markers: ISeriesMarkersPluginApi<Time>;
  tradeLines: TradeLinesPrimitive;
  priceLines: Map<string, IPriceLine>;
  dragger: LineDragger;
  metrics: Map<string, MetricSeries>;
  paneByIndicator: Map<string, number>;
  pair: string;
  candleCount: number;
  paneCount: number;
  indicators: string; // the names of the indicators drawn
}

const t = (unix: number) => unix as UTCTimestamp;

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
 * they were open. Green for take-profit targets, red for stop losses.
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
  return lines;
}

// The colors of the chart, applied again when the theme changes: never its
// position (the scroll, the zoom, the panes), which the user moves.
function themeOptions(palette: Palette) {
  return {
    layout: {
      background: { type: ColorType.Solid, color: palette.background },
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
      background: { type: ColorType.Solid, color: palette.background },
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

/** A crosshair, of the button that brings the chart to the current price. */
function CrosshairIcon() {
  return (
    <svg
      width="15"
      height="15"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      aria-hidden="true"
      focusable="false"
    >
      <circle cx="12" cy="12" r="7" />
      <path d="M12 2v5M12 17v5M2 12h5M17 12h5" />
    </svg>
  );
}

export function PriceChart({ snapshot, theme, layoutKey = "", ticket, onMoveOrder }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const stateRef = useRef<ChartState | null>(null);
  const lastClose = useRef(0);
  lastClose.current = snapshot.candles[snapshot.candles.length - 1]?.close ?? 0;
  const pnl = useMemo(() => positionPnL(snapshot), [snapshot]);
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
      volume,
      markers,
      tradeLines,
      priceLines: new Map(),
      dragger,
      metrics: new Map(),
      paneByIndicator: new Map(),
      pair: "",
      candleCount: 0,
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

    for (const { order, kind, price, movable } of pendingLines(snapshot.orders)) {
      const side = order.side === "BUY" ? "C" : "V";
      specs.set(`order:${order.id}`, {
        price,
        color: kind === "stop" ? palette.down : kind === "profit" ? palette.up : palette.accent,
        title:
          kind === "limit"
            ? `${side} ${formatCompact(order.quantity)}`
            : kind === "stop"
              ? "SL"
              : "TP",
        dashed: false,
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
      specs.set("position", {
        price: open.entry,
        color: open.profit >= 0 ? palette.up : palette.down,
        title: `${side} ${formatCompact(open.quantity)} P&L Aberto: ${formatProfit(open.profit)} ${open.currency}`,
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
    state.candles.applyOptions({
      upColor: palette.up,
      downColor: palette.down,
      wickUpColor: palette.up,
      wickDownColor: palette.down,
    });
    state.volume.setData(candles.map((c) => toVolume(c, palette)));
    state.markers.setMarkers(buildMarkers(orders, candles, palette));
    state.tradeLines.setLines(buildTradeLines(orders, candles, palette));
  }, [theme]);

  // Data changes: full reload when the pair changes, incremental otherwise.
  useEffect(() => {
    const state = stateRef.current;
    if (!state) return;
    const palette = palettes[theme];
    // other indicators, eg: the prompt of the strategy switched, are drawn
    // again whole, their panes with them
    const indicators = snapshot.indicators.map((i) => i.name).join("\n");
    const incremental =
      state.pair === snapshot.pair &&
      state.indicators === indicators &&
      snapshot.candles.length >= state.candleCount &&
      snapshot.candles.length - state.candleCount <= 5;

    if (!incremental) {
      state.candles.setData(snapshot.candles.map(toCandle));
      state.volume.setData(snapshot.candles.map((c) => toVolume(c, palette)));
    } else {
      // from the last candle drawn: the one in progress changes until its close
      for (const c of snapshot.candles.slice(Math.max(0, state.candleCount - 1))) {
        state.candles.update(toCandle(c));
        state.volume.update(toVolume(c, palette));
      }
    }

    // Indicators: create missing series, update existing ones.
    const seen = new Set<string>();
    let nextPane =
      Math.max(1, ...state.paneByIndicator.values()) + (state.paneByIndicator.size ? 1 : 0);
    if (!incremental) {
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
    if (panes.length !== state.paneCount || !incremental) {
      const saved = loadPaneFactors(paneKey(layoutKey, snapshot.pair), panes.length);
      for (const [index, pane] of panes.entries()) {
        pane.setStretchFactor(saved?.[index] ?? (index === 0 ? 3 : 1));
      }
      state.paneCount = panes.length;
    }

    state.markers.setMarkers(buildMarkers(snapshot.orders, snapshot.candles, palette));
    state.tradeLines.setLines(buildTradeLines(snapshot.orders, snapshot.candles, palette));

    if (!incremental) {
      autoScale(state.chart); // the prices of this pair, whatever the scale of the one before
      state.chart.timeScale().fitContent();
    }
    // drawn on the next frame: the width of its price scale known then
    const frame = window.requestAnimationFrame(() => {
      const width = stateRef.current?.chart.priceScale("right").width() ?? 0;
      setScaleWidth((previous) => (previous === width ? previous : width));
    });
    state.pair = snapshot.pair;
    state.candleCount = snapshot.candles.length;
    state.indicators = indicators;
    return () => window.cancelAnimationFrame(frame);
  }, [snapshot, theme, layoutKey]);

  // The last candle at the right edge, the price scales fit to the data.
  const goLive = () => {
    const state = stateRef.current;
    if (!state) return;
    autoScale(state.chart);
    state.chart.timeScale().scrollToRealTime();
  };

  return (
    <section className="card chart-card">
      <div className="card-title">
        <span>
          {snapshot.pair} <small>preço · volume · indicadores</small>
          {moveError && <span className="chart-error">{moveError}</span>}
        </span>
        <span className="legend">
          {snapshot.indicators.map((indicator) => (
            <span key={indicator.name} className="legend-item">
              {indicator.metrics.map((metric, index) => (
                <span key={metricKey(indicator, metric, index)}>
                  <i style={{ background: seriesColor(metric.color, "var(--accent)") }} />
                  {metric.name ? `${indicator.name} ${metric.name}` : indicator.name}
                </span>
              ))}
            </span>
          ))}
        </span>
      </div>
      <div className="chart-frame">
        <div ref={containerRef} className="chart price-chart" />
        <button
          type="button"
          className="icon-button chart-live"
          style={{ right: scaleWidth + 8 }}
          title="Ir para o preço atual"
          aria-label="Ir para o preço atual"
          onClick={goLive}
        >
          <CrosshairIcon />
        </button>
      </div>
    </section>
  );
}
