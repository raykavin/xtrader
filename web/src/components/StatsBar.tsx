import type { Snapshot } from "../api/types";
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
  /** The open position and its profit, null when flat, see positionPnL. */
  pnl?: PositionPnL | null;
  /** The start of the accounting period of the agent the trades count from, see AgentPeriod. */
  since?: number;
}

interface Tile {
  label: string;
  value: string;
  unit?: string;
  tone?: string;
  hint?: string;
}

export function StatsBar({ stats, snapshot, theme, pnl = null, since }: Props) {
  const hasEquity = snapshot.equity_values.length > 0;
  const palette = palettes[theme];

  // The hero answers the one question a backtest is run for: did it pay off?
  // Without a paper wallet there is no equity, so buy & hold takes its place.
  const hero: Tile = hasEquity
    ? {
        label: "Retorno da estratégia",
        value: formatPercent(stats.returnPct),
        tone: signClass(stats.returnPct),
        hint: `${formatNumber(stats.initialEquity)} → ${formatNumber(stats.finalEquity)} ${snapshot.quote}`,
      }
    : {
        label: "Buy & hold",
        value: formatPercent(stats.buyAndHold),
        tone: signClass(stats.buyAndHold),
        hint: `${snapshot.asset} em ${snapshot.candles.length} candles`,
      };

  // The open position, next to the hero: what the bot holds now.
  const position: Tile = {
    label: "P&L",
    value: pnl ? formatProfit(pnl.profit) : "—",
    unit: pnl ? pnl.currency : undefined,
    tone: pnl ? signClass(pnl.profit) : "",
    hint: pnl
      ? `${pnl.side === "BUY" ? "Compra" : "Venda"} ${formatQuantity(pnl.quantity)} a ${formatPrice(pnl.entry)}`
      : "sem posição aberta",
  };

  const tiles: Tile[] = [
    ...(hasEquity
      ? [
          {
            label: "Buy & hold",
            value: formatPercent(stats.buyAndHold),
            tone: signClass(stats.buyAndHold),
            hint: `${snapshot.asset} no período`,
          },
          {
            label: "Drawdown máximo",
            value: formatPercent(-stats.maxDrawdown),
            tone: stats.maxDrawdown > 0 ? "neg" : "",
          },
        ]
      : []),
    {
      label: "Operações",
      value: String(stats.trades),
      hint:
        `${stats.wins} ganhas · ${stats.losses} perdidas` +
        (since !== undefined ? ` · desde ${formatTime(since)} UTC` : ""),
    },
    {
      label: "Taxa de acerto",
      value: stats.trades ? formatPercent(stats.winRate).replace("+", "") : "—",
      tone: stats.trades ? (stats.winRate >= 0.5 ? "pos" : "neg") : "",
    },
    {
      label: "Lucro líquido",
      value: formatNumber(stats.totalProfit),
      unit: stats.profitCurrency,
      tone: signClass(stats.totalProfit),
      hint: stats.trades ? `média de ${formatPercent(stats.avgReturn)} por operação` : undefined,
    },
    {
      label: "Fator de lucro",
      value: stats.trades
        ? Number.isFinite(stats.profitFactor)
          ? formatNumber(stats.profitFactor)
          : "∞"
        : "—",
      tone: stats.trades ? (stats.profitFactor >= 1 ? "pos" : "neg") : "",
    },
  ];

  return (
    <section className="stats" aria-label="Desempenho">
      <div className="stat-heroes">
        <div className="stat-hero">
          <div className="stat">
            <span className="stat-label">{hero.label}</span>
            <span className={`stat-value ${hero.tone ?? ""}`}>{hero.value}</span>
          </div>
          {hero.hint && <span className="stat-hint">{hero.hint}</span>}
          {hasEquity && (
            <Sparkline
              className="sparkline"
              points={snapshot.equity_values}
              color={stats.returnPct < 0 ? palette.down : palette.accent}
            />
          )}
        </div>
        <div className="stat-hero">
          <div className="stat">
            <span className="stat-label">{position.label}</span>
            <span className={`stat-value ${position.tone ?? ""}`}>
              {position.value}
              {position.unit && <span className="unit">{position.unit}</span>}
            </span>
          </div>
          {position.hint && <span className="stat-hint">{position.hint}</span>}
        </div>
      </div>
      <div className="stat-strip">
        {tiles.map((tile) => (
          <div key={tile.label} className="stat">
            <span className="stat-label">{tile.label}</span>
            <span className={`stat-value ${tile.tone ?? ""}`}>
              {tile.value}
              {tile.unit && <span className="unit">{tile.unit}</span>}
            </span>
            {tile.hint && <span className="stat-hint">{tile.hint}</span>}
          </div>
        ))}
      </div>
    </section>
  );
}
