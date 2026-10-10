import type { CSSProperties, ReactNode } from "react";
import type { Point, Snapshot } from "../api/types";
import {
  formatNumber,
  formatPercent,
  formatPrice,
  formatProfit,
  formatQuantity,
  formatTime,
  signClass,
} from "../lib/format";
import type { PositionPnL, Stats } from "../lib/stats";
import { palettes, type Theme } from "../lib/theme";
import { Sparkline } from "./Sparkline";

interface Props {
  stats: Stats;
  snapshot: Snapshot;
  theme: Theme;
  /** The start of the accounting period of the agent the trades count from, see AgentPeriod. */
  since?: number;
  /** The open position of the pair and its profit, null when flat. */
  pnl: PositionPnL | null;
}

interface Kpi {
  label: string;
  value: string;
  unit?: string;
  tone?: string;
  hint?: string;
  /** Its course, beside it, eg: the equity of the return; in the color of its tone. */
  curve?: Point[];
  /** Under the hint, eg: the position held under the net profit. */
  footer?: ReactNode;
}

/**
 * The results of the strategy as a grid of cards, a label over each value:
 * with a paper wallet, the return first, with the equity beside it, and the
 * drawdown; then the trades, the win rate, the net profit and the profit
 * factor each with its course over the trades beside it, and the position
 * held under the net profit. The buy & hold of the asset is in the card of
 * the quote.
 */
export function StatsBar({ stats, snapshot, theme, since, pnl }: Props) {
  const hasEquity = snapshot.equity_values.length > 0;
  const palette = palettes[theme];
  const curve = (points: Point[]) => (stats.trades ? points : undefined);

  const kpis: Kpi[] = [
    ...(hasEquity
      ? [
          {
            label: "Retorno da estratégia",
            value: formatPercent(stats.returnPct),
            tone: signClass(stats.returnPct),
            hint: `De ${formatNumber(stats.initialEquity)} para ${formatNumber(stats.finalEquity)} ${snapshot.quote}`,
            curve: snapshot.equity_values,
          },
          {
            label: "Drawdown máximo",
            value: formatPercent(-stats.maxDrawdown),
            tone: stats.maxDrawdown > 0 ? "neg" : "",
            hint: "Maior queda do patrimônio",
          },
        ]
      : []),
    {
      label: "Operações",
      value: String(stats.trades),
      hint:
        `${stats.wins} ganhas e ${stats.losses} perdidas` +
        (since !== undefined ? ` desde ${formatTime(since)} UTC` : ""),
    },
    {
      label: "Taxa de acerto",
      value: stats.trades ? formatPercent(stats.winRate).replace("+", "") : "—",
      tone: stats.trades ? (stats.winRate >= 0.5 ? "pos" : "neg") : "",
      hint: stats.trades ? "Operações com lucro" : "Sem operações fechadas",
      curve: curve(stats.curves.winRate),
    },
    {
      label: "Lucro líquido",
      value: formatNumber(stats.totalProfit),
      unit: stats.profitCurrency,
      tone: signClass(stats.totalProfit),
      hint: stats.trades ? `Média de ${formatPercent(stats.avgReturn)} por operação` : undefined,
      curve: curve(stats.curves.profit),
      footer: <Position pnl={pnl} />,
    },
    {
      label: "Fator de lucro",
      value: stats.trades
        ? Number.isFinite(stats.profitFactor)
          ? formatNumber(stats.profitFactor)
          : "∞"
        : "—",
      tone: stats.trades ? (stats.profitFactor >= 1 ? "pos" : "neg") : "",
      hint: stats.trades ? "Ganhos ÷ perdas" : undefined,
      curve: curve(stats.curves.profitFactor),
    },
  ];
  const colors: Record<string, string> = { pos: palette.up, neg: palette.down };

  return (
    <section className={`kpis kpis-${kpis.length}`} aria-label="Desempenho da estratégia">
      {kpis.map((kpi) => (
        <div key={kpi.label} className="kpi">
          <span className="kpi-label">{kpi.label}</span>
          <span className="kpi-line">
            <span
              className={`kpi-value ${kpi.tone ?? ""}`}
              title={kpi.unit ? `${kpi.value} ${kpi.unit}` : kpi.value}
              style={{ "--kpi-chars": kpi.value.length + (kpi.unit ? 2 : 0) } as CSSProperties}
            >
              {kpi.value}
              {kpi.unit && <small>{kpi.unit}</small>}
            </span>
            {kpi.curve && (
              <Sparkline
                className="kpi-spark"
                points={kpi.curve}
                color={colors[kpi.tone ?? ""] ?? palette.muted}
                width={96}
                height={28}
              />
            )}
          </span>
          {kpi.hint && <span className="kpi-hint">{kpi.hint}</span>}
          {kpi.footer}
        </div>
      ))}
    </section>
  );
}

/** The position held, under the net profit: its side, size and entry, and its profit now. */
function Position({ pnl }: { pnl: PositionPnL | null }) {
  return (
    <p className="kpi-position">
      <span className="sr-only">Posição: </span>
      {pnl === null ? (
        <span className="position-flat">Sem posição aberta</span>
      ) : (
        <>
          <span className={`side-chip ${pnl.side === "BUY" ? "buy" : "sell"}`}>
            {pnl.side === "BUY" ? "Comprado" : "Vendido"}
          </span>
          <span className="position-size">
            {formatQuantity(pnl.quantity)} a {formatPrice(pnl.entry)}
          </span>
          <strong className={`position-pnl ${signClass(pnl.profit)}`}>
            {formatProfit(pnl.profit)}
            <small>{pnl.currency}</small>
          </strong>
        </>
      )}
    </p>
  );
}
