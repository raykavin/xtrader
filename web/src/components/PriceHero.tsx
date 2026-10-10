import type { Snapshot } from "../api/types";
import { formatPercent, formatPrice, formatProfit, formatQuantity, signClass } from "../lib/format";
import type { PositionPnL } from "../lib/stats";
import { Icon } from "./Icon";

interface Props {
  snapshot: Snapshot;
  /** The buy-and-hold return of the asset over the candles, see Stats. */
  buyAndHold: number;
  /** The open position of the pair and its profit, null when flat. */
  pnl: PositionPnL | null;
}

/**
 * The quote of the pair shown, the first card of the panel: the pair, its
 * last price set large, how the asset moved over the candles loaded (buy &
 * hold), and the position held with its profit.
 */
export function PriceHero({ snapshot, buyAndHold, pnl }: Props) {
  const last = snapshot.candles[snapshot.candles.length - 1]?.close ?? 0;
  const tone = signClass(buyAndHold);
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
        <div className="hero-price">
          <span className="hero-value" title="Último preço, o fechamento do último candle">
            {last > 0 ? formatPrice(last) : "—"}
            <small>{snapshot.quote}</small>
          </span>
          <span
            className={`change-chip ${tone}`}
            title={`Buy & hold: a variação de ${snapshot.asset} do primeiro ao último dos ${snapshot.candles.length} candles carregados`}
          >
            <Icon name="trend" size={13} className={buyAndHold < 0 ? "flip" : undefined} />
            {formatPercent(buyAndHold)}
            <small>buy &amp; hold · {snapshot.candles.length} candles</small>
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
