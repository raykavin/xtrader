import { useMemo } from "react";
import type { Snapshot } from "../api/types";
import { formatPercent, formatPrice, formatProfit, formatQuantity, signClass } from "../lib/format";
import type { PositionPnL } from "../lib/stats";
import { palettes, type Theme } from "../lib/theme";
import { Sparkline } from "./Sparkline";

interface Props {
  snapshot: Snapshot;
  /** The buy-and-hold return of the asset over the candles, see Stats. */
  buyAndHold: number;
  /** The open position of the pair and its profit, null when flat. */
  pnl: PositionPnL | null;
  theme: Theme;
}

/**
 * The quote of the pair shown, the first card of the panel: the pair, its
 * last price set large, how the asset moved over the candles loaded (buy &
 * hold) at its right, its closes as a line over the change and the count of
 * the candles, and the position held with its profit.
 */
export function PriceHero({ snapshot, buyAndHold, pnl, theme }: Props) {
  const last = snapshot.candles[snapshot.candles.length - 1]?.close ?? 0;
  const tone = signClass(buyAndHold);
  const closes = useMemo(
    () => snapshot.candles.map((c) => ({ time: c.time, value: c.close })),
    [snapshot.candles],
  );
  const palette = palettes[theme];
  const color = buyAndHold > 0 ? palette.up : buyAndHold < 0 ? palette.down : palette.muted;
  const count = snapshot.candles.length;
  return (
    <section className="card hero" aria-labelledby="hero-pair">
      <div className="hero-quote">
        <div className="hero-top">
          <div className="hero-id">
            <span className="asset-badge" aria-hidden="true">
              {snapshot.asset.slice(0, 3)}
            </span>
            <div className="hero-names">
              <span className="hero-pair" id="hero-pair">
                {snapshot.pair}
              </span>
              <span className="hero-symbols">
                {snapshot.asset} / {snapshot.quote}
              </span>
            </div>
          </div>
          <Sparkline
            className="hero-spark"
            points={closes}
            color={color}
            width={112}
            height={36}
            dot
          />
        </div>
        <div className="hero-price">
          <span className="hero-value" title="Último preço, o fechamento do último candle">
            {last > 0 ? formatPrice(last) : "—"}
            <small>{snapshot.quote}</small>
          </span>
          <span
            className="hero-change"
            title={`Buy & hold: a variação de ${snapshot.asset} do primeiro ao último dos ${count} candles carregados`}
          >
            <span className={`change-chip ${tone}`}>
              <span className="sr-only">Variação (buy &amp; hold): </span>
              {formatPercent(buyAndHold)}
            </span>
            <span className="hero-candles">{count} candles</span>
          </span>
        </div>
      </div>

      <dl className="hero-position">
        <dt className="kpi-label">Posição</dt>
        <dd>
          {pnl === null ? (
            <span className="hero-flat">Sem posição aberta</span>
          ) : (
            <>
              <span className="hero-position-line">
                <span className={`side-chip ${pnl.side === "BUY" ? "buy" : "sell"}`}>
                  {pnl.side === "BUY" ? "Comprado" : "Vendido"}
                </span>
                <span className="position-size">
                  {formatQuantity(pnl.quantity)} a {formatPrice(pnl.entry)}
                </span>
              </span>
              <strong className={`position-pnl ${signClass(pnl.profit)}`}>
                {formatProfit(pnl.profit)}
                <small>{pnl.currency}</small>
              </strong>
            </>
          )}
        </dd>
      </dl>
    </section>
  );
}
