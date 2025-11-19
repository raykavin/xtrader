// Numbers as in Brazil: 1.234,56
export const LOCALE = "pt-BR";

const numberFormat = new Intl.NumberFormat(LOCALE, { maximumFractionDigits: 2 });
const priceFormat = new Intl.NumberFormat(LOCALE, { maximumFractionDigits: 8 });
const compactFormat = new Intl.NumberFormat(LOCALE, { maximumSignificantDigits: 4 });

export function formatNumber(value: number): string {
  return numberFormat.format(value);
}

const moneyFormat = new Intl.NumberFormat(LOCALE, {
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
});

/** A profit with its sign and two decimals, eg: +11,92 or −3,40. */
export function formatProfit(value: number): string {
  return `${value >= 0 ? "+" : "−"}${moneyFormat.format(Math.abs(value))}`;
}

export function formatPrice(value: number): string {
  if (Math.abs(value) >= 1000) return numberFormat.format(value);
  return priceFormat.format(value);
}

export function formatPercent(fraction: number, digits = 2): string {
  if (!Number.isFinite(fraction)) return "∞";
  const sign = fraction > 0 ? "+" : "";
  const percent = new Intl.NumberFormat(LOCALE, {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  });
  // || 0 drops the sign of a negative zero, eg: no drawdown
  return `${sign}${percent.format(fraction * 100 || 0)}%`;
}

export function formatQuantity(value: number): string {
  return priceFormat.format(value);
}

/** Short form for chart labels, e.g. 0,4485 or 1.234. */
export function formatCompact(value: number): string {
  return compactFormat.format(value);
}

/** Day and time in UTC, as the charts: 26/09/2021 20:00. */
export function formatTime(unixSeconds: number): string {
  const iso = new Date(unixSeconds * 1000).toISOString();
  return `${iso.slice(8, 10)}/${iso.slice(5, 7)}/${iso.slice(0, 4)} ${iso.slice(11, 16)}`;
}

export function signClass(value: number): string {
  if (value > 0) return "pos";
  if (value < 0) return "neg";
  return "";
}
