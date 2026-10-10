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

interface Readout {
  label: string;
  value: string;
  unit?: string;
  tone?: string;
  hint?: string;
}

/**
 * The results of the strategy as a row of readouts, a label over each value:
 * the first one answers whether it paid off, the trend of the equity beside
 * it; the open position is in the head of the panel.
 */
export function StatsBar({ stats, snapshot, theme, since }: Props) {
  const hasEquity = snapshot.equity_values.length > 0;
  const palette = palettes[theme];

  // Without a paper wallet there is no equity, so buy & hold comes first.
  const first: Readout = hasEquity
    ? {
        label: "Retorno da estratégia",
        value: formatPercent(stats.returnPct),
        tone: signClass(stats.returnPct),
        hint: `De ${formatNumber(stats.initialEquity)} para ${formatNumber(stats.finalEquity)} ${snapshot.quote}`,
      }
    : {
        label: "Buy & hold",
        value: formatPercent(stats.buyAndHold),
        tone: signClass(stats.buyAndHold),
        hint: `${snapshot.asset} em ${snapshot.candles.length} candles`,
      };

  const readouts: Readout[] = [
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
        `${stats.wins} ganhas e ${stats.losses} perdidas` +
        (since !== undefined ? ` desde ${formatTime(since)} UTC` : ""),
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
    },
  ];

  return (
    <section className="readouts" aria-label="Desempenho">
      <div className="readout readout-first">
        <span className="readout-label">{first.label}</span>
        <span className="readout-line">
          <span className={`readout-value ${first.tone ?? ""}`}>{first.value}</span>
          {hasEquity && (
            <Sparkline
              className="readout-spark"
              points={snapshot.equity_values}
              color={stats.returnPct < 0 ? palette.down : palette.up}
              width={96}
              height={28}
              area={false}
            />
          )}
        </span>
        {first.hint && <span className="readout-hint">{first.hint}</span>}
      </div>
      {readouts.map((readout) => (
        <div key={readout.label} className="readout">
          <span className="readout-label">{readout.label}</span>
          <span className={`readout-value ${readout.tone ?? ""}`}>
            {readout.value}
            {readout.unit && <small>{readout.unit}</small>}
          </span>
          {readout.hint && <span className="readout-hint">{readout.hint}</span>}
        </div>
      ))}
    </section>
  );
}
