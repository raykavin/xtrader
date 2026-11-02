import {
  BaselineSeries,
  ColorType,
  CrosshairMode,
  createChart,
  type IChartApi,
  type ISeriesApi,
  type UTCTimestamp,
} from "lightweight-charts";
import { type RefObject, useEffect, useMemo, useRef, useState } from "react";
import type { PerformanceTrade, Point } from "../api/types";
import { formatMoney, formatPercent, formatProfit, LOCALE, signClass } from "../lib/format";
import { useValuesHidden } from "../lib/hiddenValues";
import {
  bucketOf,
  type EfficiencyState,
  efficiencyOf,
  equityCurve,
  groupBars,
  type PerformanceBar,
  periodBars,
  tradeKey,
} from "../lib/performance";
import { palettes, type Theme } from "../lib/theme";
import { Tabs, tabIds } from "./Tabs";

type ChartId = "equity" | "trades" | "efficiency" | "period" | "symbol" | "agent";

interface Props {
  trades: PerformanceTrade[];
  currency: string;
  /** The period shown, Unix seconds. */
  range: { from: number; to: number };
  theme: Theme;
  efficiency: EfficiencyState;
}

/** The colors of the chart of the equity in theme. */
function equityStyle(theme: Theme) {
  const p = palettes[theme];
  return {
    layout: {
      background: { type: ColorType.Solid, color: "transparent" },
      textColor: p.text,
    },
    grid: { vertLines: { color: p.grid }, horzLines: { color: p.grid } },
    rightPriceScale: { borderColor: p.border },
    timeScale: { borderColor: p.border },
    series: {
      topLineColor: p.up,
      topFillColor1: `${p.up}55`,
      topFillColor2: `${p.up}08`,
      bottomLineColor: p.down,
      bottomFillColor1: `${p.down}08`,
      bottomFillColor2: `${p.down}55`,
    },
  };
}

/**
 * The equity of the period: the net results summed trade after trade, green
 * above base (0 by default), red below.
 */
export function EquityArea({
  points,
  theme,
  base = 0,
}: {
  points: Point[];
  theme: Theme;
  base?: number;
}) {
  const container = useRef<HTMLDivElement>(null);
  const state = useRef<{ chart: IChartApi; series: ISeriesApi<"Baseline"> } | null>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: created once; the theme and the points are applied below
  useEffect(() => {
    const element = container.current;
    if (!element) return;
    const s = equityStyle(theme);
    const chart = createChart(element, {
      autoSize: true,
      layout: {
        ...s.layout,
        fontFamily: palettes[theme].fontFamily,
        fontSize: 11,
        attributionLogo: false,
      },
      grid: s.grid,
      crosshair: { mode: CrosshairMode.Magnet },
      rightPriceScale: s.rightPriceScale,
      timeScale: { ...s.timeScale, timeVisible: true, secondsVisible: false },
      localization: { locale: LOCALE, priceFormatter: formatMoney },
      handleScroll: false,
      handleScale: false,
    });
    const series = chart.addSeries(BaselineSeries, {
      baseValue: { type: "price", price: 0 },
      lineWidth: 2,
      priceLineVisible: false,
      lastValueVisible: true,
      ...s.series,
    });
    state.current = { chart, series };
    return () => {
      chart.remove();
      state.current = null;
    };
  }, []);

  useEffect(() => {
    const current = state.current;
    if (!current) return;
    const s = equityStyle(theme);
    current.chart.applyOptions({
      layout: s.layout,
      grid: s.grid,
      rightPriceScale: s.rightPriceScale,
      timeScale: s.timeScale,
    });
    current.series.applyOptions(s.series);
  }, [theme]);

  const hidden = useValuesHidden();
  // biome-ignore lint/correctness/useExhaustiveDependencies: the scale drawn again as the amounts are hidden or shown, formatMoney masking them
  useEffect(() => {
    state.current?.chart.applyOptions({
      localization: { locale: LOCALE, priceFormatter: (value: number) => formatMoney(value) },
    });
  }, [hidden]);

  useEffect(() => {
    const current = state.current;
    if (!current) return;
    current.series.applyOptions({ baseValue: { type: "price", price: base } });
    current.series.setData(points.map((p) => ({ time: p.time as UTCTimestamp, value: p.value })));
    current.chart.timeScale().fitContent();
  }, [points, base]);

  return <div ref={container} className="chart perf-equity" />;
}

/**
 * The scale of values from min to max, 0 among them: about 4 round steps,
 * its ends the ticks around them.
 */
function scaleOf(min: number, max: number): { ticks: number[]; bottom: number; top: number } {
  const span = max - min || 1;
  const raw = span / 4;
  const power = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * power).find((s) => s >= raw) ?? raw;
  const bottom = Math.floor(min / step) * step;
  const top = Math.ceil(max / step) * step;
  const ticks: number[] = [];
  for (let i = 0; bottom + i * step <= top + step / 1e6; i++) {
    const v = bottom + i * step;
    ticks.push(Math.abs(v) < step / 1e6 ? 0 : v);
  }
  return { ticks, bottom, top: top === bottom ? top + step : top };
}

/** The width of element, followed as it resizes; 0 before it is laid out. */
function useWidth(element: RefObject<HTMLElement | null>): number {
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const target = element.current;
    if (!target) return;
    setWidth(target.clientWidth);
    const observer = new ResizeObserver(([entry]) => setWidth(entry?.contentRect.width ?? 0));
    observer.observe(target);
    return () => observer.disconnect();
  }, [element]);
  return width;
}

/**
 * Vertical bars from 0, green up and red down, a tooltip on the hovered
 * one: the results of each trade, or of each bucket of the period.
 */
function Columns({
  bars,
  currency,
  caption,
}: {
  bars: PerformanceBar[];
  currency: string;
  caption: string;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const figure = useRef<HTMLElement>(null);
  const width = Math.max(useWidth(figure), 280);
  const height = 300;
  const pad = { top: 12, right: 12, bottom: 28, left: 72 };
  const max = Math.max(0, ...bars.map((b) => b.net));
  const min = Math.min(0, ...bars.map((b) => b.net));
  const { ticks, bottom, top } = scaleOf(min, max);
  const y = (v: number) =>
    pad.top + ((top - v) / (top - bottom || 1)) * (height - pad.top - pad.bottom);
  const slot = (width - pad.left - pad.right) / Math.max(bars.length, 1);
  const barWidth = Math.max(2, Math.min(36, slot - 2)); // a 2px gap between the bars
  const every = Math.max(1, Math.ceil(bars.length / Math.max(2, Math.floor(width / 70)))); // labels 70px apart
  const shown = hover === null ? undefined : bars[hover];

  return (
    <figure ref={figure} className="perf-columns-chart">
      <svg width={width} height={height} role="img" aria-label={caption}>
        {ticks.map((t) => (
          <g key={t}>
            <line
              className={t === 0 ? "perf-zero" : "perf-grid"}
              x1={pad.left}
              x2={width - pad.right}
              y1={y(t)}
              y2={y(t)}
            />
            <text className="perf-axis" x={pad.left - 8} y={y(t)} dy="0.32em" textAnchor="end">
              {formatMoney(t)}
            </text>
          </g>
        ))}
        {bars.map((bar, i) => {
          const x = pad.left + i * slot + (slot - barWidth) / 2;
          const from = y(Math.max(bar.net, 0));
          const to = y(Math.min(bar.net, 0));
          return (
            // biome-ignore lint/a11y/noStaticElementInteractions: the tooltip of the bar; the list of the operations is its table
            <g
              key={bar.key}
              onMouseEnter={() => setHover(i)}
              onMouseLeave={() => setHover(null)}
              className={hover === i ? "perf-bar is-hover" : "perf-bar"}
            >
              <rect
                className="perf-hit"
                x={pad.left + i * slot}
                y={pad.top}
                width={slot}
                height={height - pad.top - pad.bottom}
              />
              <rect
                className={bar.net >= 0 ? "perf-up" : "perf-down"}
                x={x}
                y={from}
                width={barWidth}
                height={Math.max(1, to - from)}
                rx={Math.min(4, barWidth / 2)}
              />
              {i % every === 0 && (
                <text className="perf-axis" x={x + barWidth / 2} y={height - 8} textAnchor="middle">
                  {bar.label}
                </text>
              )}
            </g>
          );
        })}
      </svg>
      {shown && (
        <figcaption className="perf-tooltip" role="status">
          <strong>{shown.label}</strong>
          <span className={`mono ${signClass(shown.net)}`}>
            {formatProfit(shown.net)} {currency}
          </span>
          <span className="field-hint">
            {shown.trades === 1 ? "1 operação" : `${shown.trades} operações`}, {shown.wins}{" "}
            vencedora
            {shown.wins === 1 ? "" : "s"}
          </span>
        </figcaption>
      )}
    </figure>
  );
}

/** A share in the tooltip, eg: 62%, — without one. */
function share(value: number | null): string {
  return value === null ? "—" : formatPercent(value, 0).replace("+", "");
}

/**
 * The efficiency of each trade, in the order of the closes: a thin neutral
 * range from its MAE below 0 to its MFE above, and a mark at its result
 * before the costs, green or red; a tooltip with its efficiencies.
 */
function ExcursionColumns({
  trades,
  efficiency,
  currency,
}: {
  trades: PerformanceTrade[];
  efficiency: EfficiencyState;
  currency: string;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const figure = useRef<HTMLElement>(null);
  const width = Math.max(useWidth(figure), 280);
  const height = 300;
  const pad = { top: 12, right: 12, bottom: 28, left: 72 };
  const rows = [...trades]
    .sort((a, b) => a.closed_at - b.closed_at || a.position - b.position)
    .map((t, i) => ({ t, i: i + 1, x: efficiency.excursions.get(tradeKey(t)) }))
    .filter((r) => r.x !== undefined);
  const max = Math.max(0, ...rows.map((r) => Math.max(r.x?.mfe ?? 0, r.t.gross)));
  const min = Math.min(0, ...rows.map((r) => Math.min(-(r.x?.mae ?? 0), r.t.gross)));
  const { ticks, bottom, top } = scaleOf(min, max);
  const y = (v: number) =>
    pad.top + ((top - v) / (top - bottom || 1)) * (height - pad.top - pad.bottom);
  const slot = (width - pad.left - pad.right) / Math.max(rows.length, 1);
  const rangeWidth = Math.max(2, Math.min(10, slot * 0.35));
  const every = Math.max(1, Math.ceil(rows.length / Math.max(2, Math.floor(width / 70))));
  const shown = hover === null ? undefined : rows[hover];
  const e = shown ? efficiencyOf(shown.t, shown.x) : null;
  const timeframes = [...new Set(rows.map((r) => r.x?.timeframe).filter(Boolean))].join(", ");

  return (
    <>
      <p className="field-hint">
        Cada faixa vai do MAE (máximo contra) ao MFE (máximo a favor) da operação; o ponto é o
        resultado antes dos custos, em {currency}. Candles da corretora
        {timeframes ? ` (${timeframes})` : ""}.
        {efficiency.summary?.missing
          ? ` ${efficiency.summary.missing === 1 ? "1 operação fora" : `${efficiency.summary.missing} operações fora`} do gráfico: sem abertura conhecida ou sem candles.`
          : ""}
      </p>
      {rows.length === 0 ? (
        <p className="field-hint">Nenhuma operação com candles para mostrar.</p>
      ) : (
        <figure ref={figure} className="perf-columns-chart">
          <svg
            width={width}
            height={height}
            role="img"
            aria-label="MFE, MAE e resultado de cada operação"
          >
            {ticks.map((t) => (
              <g key={t}>
                <line
                  className={t === 0 ? "perf-zero" : "perf-grid"}
                  x1={pad.left}
                  x2={width - pad.right}
                  y1={y(t)}
                  y2={y(t)}
                />
                <text className="perf-axis" x={pad.left - 8} y={y(t)} dy="0.32em" textAnchor="end">
                  {formatMoney(t)}
                </text>
              </g>
            ))}
            {rows.map((row, i) => {
              const center = pad.left + i * slot + slot / 2;
              const up = y(row.x?.mfe ?? 0);
              const down = y(-(row.x?.mae ?? 0));
              return (
                // biome-ignore lint/a11y/noStaticElementInteractions: the tooltip of the trade; the list of the operations is its table
                <g
                  key={tradeKey(row.t)}
                  onMouseEnter={() => setHover(i)}
                  onMouseLeave={() => setHover(null)}
                  className={hover === i ? "perf-bar is-hover" : "perf-bar"}
                >
                  <rect
                    className="perf-hit"
                    x={pad.left + i * slot}
                    y={pad.top}
                    width={slot}
                    height={height - pad.top - pad.bottom}
                  />
                  <rect
                    className="perf-range"
                    x={center - rangeWidth / 2}
                    y={up}
                    width={rangeWidth}
                    height={Math.max(1, down - up)}
                    rx={Math.min(4, rangeWidth / 2)}
                  />
                  <circle
                    className={row.t.gross >= 0 ? "perf-mark perf-up" : "perf-mark perf-down"}
                    cx={center}
                    cy={y(row.t.gross)}
                    r={4.5}
                  />
                  {i % every === 0 && (
                    <text className="perf-axis" x={center} y={height - 8} textAnchor="middle">
                      {row.i}
                    </text>
                  )}
                </g>
              );
            })}
          </svg>
          {shown && e && (
            <figcaption className="perf-tooltip" role="status">
              <strong>
                {shown.i}. {shown.t.symbol} · {shown.t.agent}
              </strong>
              <span className="mono">
                MFE {formatMoney(shown.x?.mfe ?? 0)} · MAE {formatMoney(-(shown.x?.mae ?? 0))}
              </span>
              <span className={`mono ${signClass(shown.t.gross)}`}>
                resultado {formatProfit(shown.t.gross)} {currency}
              </span>
              <span className="field-hint">
                eficiência: entrada {share(e.entry)} · saída {share(e.exit)} · total{" "}
                {share(e.total)}
              </span>
            </figcaption>
          )}
        </figure>
      )}
    </>
  );
}

/** Horizontal bars from 0, the best first, each labelled with its result: by asset or by agent. */
function Ranking({
  bars,
  currency,
  caption,
}: {
  bars: PerformanceBar[];
  currency: string;
  caption: string;
}) {
  const extent = Math.max(1e-9, ...bars.map((b) => Math.abs(b.net)));
  const negatives = bars.some((b) => b.net < 0);
  const positives = bars.some((b) => b.net > 0);
  // the zero in the middle with both signs, else at the edge
  const zero = negatives && positives ? 50 : negatives ? 100 : 0;
  const span = negatives && positives ? 50 : 100;
  return (
    <ul className="perf-ranking" aria-label={caption}>
      {bars.map((bar) => {
        const size = (Math.abs(bar.net) / extent) * span;
        const left = bar.net >= 0 ? zero : zero - size;
        return (
          <li key={bar.key} className="perf-rank">
            <span className="perf-rank-label" title={bar.label}>
              {bar.label}
            </span>
            <span className="perf-rank-track">
              <span
                className={`perf-rank-bar ${bar.net >= 0 ? "perf-up" : "perf-down"}`}
                style={{ left: `${left}%`, width: `${Math.max(size, 0.6)}%` }}
              />
            </span>
            <span className={`perf-rank-value mono ${signClass(bar.net)}`}>
              {formatProfit(bar.net)} {currency}
              <small>
                {bar.trades} op. · {bar.trades ? Math.round((bar.wins / bar.trades) * 100) : 0}%
                venc.
              </small>
            </span>
          </li>
        );
      })}
    </ul>
  );
}

const charts: { id: ChartId; label: string }[] = [
  { id: "equity", label: "Patrimônio" },
  { id: "trades", label: "Operações" },
  { id: "efficiency", label: "Eficiência" },
  { id: "period", label: "Por período" },
  { id: "symbol", label: "Por ativo" },
  { id: "agent", label: "Por agente" },
];

const bucketNames = { day: "dia", week: "semana", month: "mês" } as const;

/**
 * The charts of the performance of a currency: the equity of the period, the
 * result of each trade, by bucket of the period (days, weeks or months after
 * its length), by asset and by agent.
 */
export function PerformanceCharts({ trades, currency, range, theme, efficiency }: Props) {
  const [shown, setShown] = useState<ChartId>("equity");
  const equity = useMemo(() => equityCurve(trades, range.from), [trades, range.from]);
  const bucket = bucketOf(Math.min(range.to, Date.now() / 1000) - range.from);
  const perTrade = useMemo(
    () =>
      [...trades]
        .sort((a, b) => a.closed_at - b.closed_at || a.position - b.position)
        .map((t, i) => ({
          key: `${t.account}/${t.position}`,
          label: String(i + 1),
          net: t.net,
          trades: 1,
          wins: t.gross > 0 ? 1 : 0,
        })),
    [trades],
  );
  const ids = tabIds("perf-charts", shown);

  return (
    <div className="perf-charts">
      <Tabs
        label="Gráficos da performance"
        idPrefix="perf-charts"
        items={charts}
        value={shown}
        onChange={setShown}
      />
      <div id={ids.panel} role="tabpanel" aria-labelledby={ids.tab} className="perf-chart-panel">
        {trades.length === 0 ? (
          <p className="field-hint">Nenhuma operação no período.</p>
        ) : shown === "equity" ? (
          <>
            <p className="field-hint">
              Resultado líquido acumulado a cada fechamento, em {currency}, a partir de zero.
            </p>
            <EquityArea points={equity} theme={theme} />
          </>
        ) : shown === "trades" ? (
          <Columns
            bars={perTrade}
            currency={currency}
            caption="Resultado líquido de cada operação, na ordem dos fechamentos"
          />
        ) : shown === "efficiency" ? (
          efficiency.error ? (
            <p className="field-hint">
              Não foi possível carregar os candles das operações: {efficiency.error}
            </p>
          ) : efficiency.loading ? (
            <p className="field-hint">Carregando os candles das operações…</p>
          ) : (
            <ExcursionColumns trades={trades} efficiency={efficiency} currency={currency} />
          )
        ) : shown === "period" ? (
          <>
            <p className="field-hint">Resultado líquido por {bucketNames[bucket]} de fechamento.</p>
            <Columns
              bars={periodBars(trades, bucket)}
              currency={currency}
              caption={`Resultado líquido por ${bucketNames[bucket]}`}
            />
          </>
        ) : (
          <Ranking
            bars={groupBars(trades, shown)}
            currency={currency}
            caption={
              shown === "symbol" ? "Resultado líquido por ativo" : "Resultado líquido por agente"
            }
          />
        )}
      </div>
    </div>
  );
}
