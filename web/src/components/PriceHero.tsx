import { useMemo } from "react";
import type { Snapshot } from "../api/types";
import { formatPercent, formatPrice, signClass } from "../lib/format";
import { palettes, type Theme } from "../lib/theme";
import { Sparkline } from "./Sparkline";

interface Props {
  snapshot: Snapshot;
  /** The buy-and-hold return of the asset over the candles, see Stats. */
  buyAndHold: number;
  theme: Theme;
}

/**
 * The quote of the pair shown, the first card of the panel: the pair, its
 * last price set large, how the asset moved over the candles loaded (buy &
 * hold) and their count, and their closes as a line along the foot of the
 * card. The position held is in the card of the net profit, see StatsBar.
 */
export function PriceHero({ snapshot, buyAndHold, theme }: Props) {
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
        <span className="hero-value" title="Último preço, o fechamento do último candle">
          {last > 0 ? formatPrice(last) : "—"}
          <small>{snapshot.quote}</small>
        </span>
      </div>

      <div
        className="hero-change"
        title={`Buy & hold: a variação de ${snapshot.asset} do primeiro ao último dos ${count} candles carregados`}
      >
        <span className={`change-chip ${tone}`}>
          <span className="sr-only">Variação (buy &amp; hold): </span>
          {formatPercent(buyAndHold)}
        </span>
        <span className="hero-candles">{count} candles</span>
      </div>

      <div className="hero-chart">
        <Sparkline points={closes} color={color} width={400} height={80} />
      </div>
    </section>
  );
}
