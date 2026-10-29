import type { ReactNode } from "react";
import { formatMoney, formatNumber, formatPercent, formatQuantity, signClass } from "../lib/format";
import { formatDuration, type PerformanceSummary as Summary } from "../lib/performance";

/** A line of the summary: its label and its value, in the color of its sign when it has one. */
interface Line {
  label: string;
  value: ReactNode;
  tone?: string;
  /** Under the label, eg: what it counts. */
  hint?: string;
}

/** An amount with its currency, eg: 1.787,55 BRL. */
function money(value: number, currency: string): string {
  return `${formatMoney(value)} ${currency}`;
}

/** A ratio, eg: 20,56, ∞ without a loss, — without a value. */
function ratio(value: number | null): string {
  if (value === null) return "—";
  return Number.isFinite(value) ? formatNumber(value) : "∞";
}

/** The groups of the summary, in two columns as the Profit of Nelogica shows them. */
function groupsOf(s: Summary): { title?: string; left: Line[]; right: Line[] }[] {
  const c = s.currency;
  return [
    {
      left: [
        { label: "Saldo líquido total", value: money(s.net, c), tone: signClass(s.net) },
        { label: "Lucro bruto", value: money(s.grossProfit, c), tone: signClass(s.grossProfit) },
        {
          label: "Fator de lucro",
          value: s.trades ? ratio(s.profitFactor) : "—",
          hint: "lucro bruto ÷ prejuízo bruto",
        },
      ],
      right: [
        {
          label: "Saldo total",
          value: money(s.gross, c),
          tone: signClass(s.gross),
          hint: "antes dos custos",
        },
        { label: "Prejuízo bruto", value: money(s.grossLoss, c), tone: signClass(s.grossLoss) },
        {
          label: "Custos",
          value: money(s.costs, c),
          tone: signClass(s.costs),
          hint: "comissão, taxa e swap",
        },
      ],
    },
    {
      left: [
        { label: "Número total de operações", value: String(s.trades) },
        { label: "Operações vencedoras", value: String(s.wins) },
        { label: "Operações zeradas", value: String(s.zeroed) },
      ],
      right: [
        {
          label: "Percentual de operações vencedoras",
          value: s.trades ? formatPercent(s.winRate).replace("+", "") : "—",
        },
        { label: "Operações perdedoras", value: String(s.losses) },
      ],
    },
    {
      left: [
        {
          label: "Média de lucro/prejuízo",
          value: money(s.average, c),
          tone: signClass(s.average),
        },
        {
          label: "Média de operações vencedoras",
          value: money(s.averageWin, c),
          tone: signClass(s.averageWin),
        },
        {
          label: "Maior operação vencedora",
          value: money(s.largestWin, c),
          tone: signClass(s.largestWin),
        },
        { label: "Maior sequência vencedora", value: String(s.longestWinStreak) },
        { label: "Média de tempo em op. vencedoras", value: formatDuration(s.averageWinDuration) },
        { label: "Média de tempo em operações", value: formatDuration(s.averageDuration) },
      ],
      right: [
        { label: "Razão média lucro : média prejuízo", value: ratio(s.payoff) },
        {
          label: "Média de operações perdedoras",
          value: money(s.averageLoss, c),
          tone: signClass(s.averageLoss),
        },
        {
          label: "Maior operação perdedora",
          value: money(s.largestLoss, c),
          tone: signClass(s.largestLoss),
        },
        { label: "Maior sequência perdedora", value: String(s.longestLossStreak) },
        { label: "Média de tempo em op. perdedoras", value: formatDuration(s.averageLossDuration) },
      ],
    },
    {
      left: [
        { label: "Máximo de lotes", value: s.trades ? formatQuantity(s.maxVolume) : "—" },
        {
          label: "Retorno no capital inicial",
          value: s.returnOnCapital === null ? "—" : formatPercent(s.returnOnCapital),
          tone: s.returnOnCapital === null ? undefined : signClass(s.returnOnCapital),
          hint:
            s.capital === null
              ? "saldo inicial das contas desconhecido"
              : `sobre ${money(s.capital, c)}, o saldo das contas no início`,
        },
        {
          label: "Patrimônio máximo",
          value: money(s.peak, c),
          hint: "pico do resultado líquido acumulado",
        },
      ],
      right: [
        { label: "Capital inicial", value: s.capital === null ? "—" : money(s.capital, c) },
        {
          label: "Percentual de tempo no mercado",
          value: s.timeInMarket === null ? "—" : formatPercent(s.timeInMarket).replace("+", ""),
        },
      ],
    },
    {
      title: "Declínio máximo (topo ao fundo)",
      left: [{ label: "Valor", value: money(-s.drawdown, c), tone: signClass(-s.drawdown) }],
      right: [
        {
          label: "Drawdown sobre o patrimônio no topo",
          value: s.drawdownPct === null ? "—" : formatPercent(s.drawdownPct).replace("+", ""),
          hint: "capital inicial + pico",
        },
      ],
    },
  ];
}

function Lines({ lines }: { lines: Line[] }) {
  return (
    <dl className="perf-lines">
      {lines.map((line) => (
        <div key={line.label} className="perf-line">
          <dt>
            {line.label}
            {line.hint && <small>{line.hint}</small>}
          </dt>
          <dd className={`mono ${line.tone ?? ""}`}>{line.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/**
 * The summary of the performance of a currency, as the "Resumo de
 * Performance" of the Profit of Nelogica: the results, the trades, the
 * averages and extremes, the capital, the drawdown; in two columns, one on a
 * phone.
 */
export function PerformanceSummary({ summary }: { summary: Summary }) {
  return (
    <div className="perf-summary">
      {groupsOf(summary).map((group, i) => (
        <section key={group.title ?? i} className="perf-group" aria-label={group.title}>
          {group.title && <h3 className="perf-group-title">{group.title}</h3>}
          <div className="perf-columns">
            <Lines lines={group.left} />
            <Lines lines={group.right} />
          </div>
        </section>
      ))}
      <p className="field-hint perf-note">
        Operações vencedoras, perdedoras, lucro e prejuízo bruto contam o resultado antes dos
        custos; o saldo líquido, a média por operação, o patrimônio e o drawdown, o resultado
        líquido. Sem a leitura do caminho de cada operação, o declínio é medido entre os
        fechamentos.
      </p>
    </div>
  );
}
