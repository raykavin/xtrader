import type { CSSProperties } from "react";
import type { Snapshot } from "../api/types";
import { formatNumber, formatPercent, formatTime, signClass } from "../lib/format";
import type { Stats } from "../lib/stats";
import { palettes, type Theme } from "../lib/theme";
import { Sparkline } from "./Sparkline";

interface Props {
  stats: Stats;
  snapshot: Snapshot;
  theme: Theme;
  /** The start of the accounting period of the agent the trades count from, see AgentPeriod. */
  since?: number;
}

interface Kpi {
  label: string;
  value: string;
  unit?: string;
  tone?: string;
  hint?: string;
}

/**
 * The results of the strategy as a grid of cards, a label over each value:
 * with a paper wallet, the return first, the trend of the equity in its
 * card, and the drawdown; then the trades. The buy & hold of the asset is in
 * the card of the quote.
 */
export function StatsBar({ stats, snapshot, theme, since }: Props) {
  const hasEquity = snapshot.equity_values.length > 0;
  const palette = palettes[theme];

  const kpis: Kpi[] = [
    ...(hasEquity
      ? [
          {
            label: "Retorno da estratégia",
            value: formatPercent(stats.returnPct),
            tone: signClass(stats.returnPct),
            hint: `De ${formatNumber(stats.initialEquity)} para ${formatNumber(stats.finalEquity)} ${snapshot.quote}`,
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
    },
    {
      label: "Lucro líquido",
      value: formatNumber(stats.totalProfit),
      unit: stats.profitCurrency,
      tone: signClass(stats.totalProfit),
      hint: stats.trades ? `Média de ${formatPercent(stats.avgReturn)} por operação` : undefined,
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
    },
  ];

  return (
    <section className={`kpis kpis-${kpis.length}`} aria-label="Desempenho da estratégia">
      {kpis.map((kpi, index) => (
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
            {hasEquity && index === 0 && (
              <Sparkline
                className="kpi-spark"
                points={snapshot.equity_values}
                color={stats.returnPct < 0 ? palette.down : palette.up}
                width={96}
                height={28}
              />
            )}
          </span>
          {kpi.hint && <span className="kpi-hint">{kpi.hint}</span>}
        </div>
      ))}
    </section>
  );
}
