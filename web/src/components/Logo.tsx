import { useId } from "react";

interface MarkProps {
  /** The side of the mark, in px. */
  size?: number;
}

/**
 * The X of XTrader: two candles crossed, a bullish one rising over a bearish
 * one falling, in the colors of the chart; the bearish one cut around the
 * other, a gap on any background. public/favicon.svg draws the same.
 */
export function LogoMark({ size = 22 }: MarkProps) {
  const cut = `logo-cut-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
  return (
    <svg
      className="logo-mark"
      width={size}
      height={size}
      viewBox="0 0 32 32"
      aria-hidden="true"
      focusable="false"
    >
      <defs>
        <mask id={cut} maskUnits="userSpaceOnUse" x="0" y="0" width="32" height="32">
          <rect width="32" height="32" fill="#fff" />
          <rect
            x="11.6"
            y="4.6"
            width="8.8"
            height="22.8"
            rx="2.4"
            transform="rotate(45 16 16)"
            fill="#000"
          />
        </mask>
      </defs>
      <g mask={`url(#${cut})`}>
        <g className="logo-bear" transform="rotate(-45 16 16)">
          <line x1="16" y1="1.5" x2="16" y2="30.5" />
          <rect x="13" y="6" width="6" height="20" rx="1.4" />
        </g>
      </g>
      <g className="logo-bull" transform="rotate(45 16 16)">
        <line x1="16" y1="1.5" x2="16" y2="30.5" />
        <rect x="13" y="6" width="6" height="20" rx="1.4" />
      </g>
    </svg>
  );
}

interface LogoProps {
  /** The side of the mark, in px; the word follows it. */
  size?: number;
  className?: string;
}

/** The logo: the mark as the X of the word, then "Trader". */
export function Logo({ size = 24, className = "" }: LogoProps) {
  return (
    <span className={`logo ${className}`.trim()} role="img" aria-label="XTrader">
      <LogoMark size={size} />
      <span className="logo-word" aria-hidden="true">
        Trader
      </span>
    </span>
  );
}
