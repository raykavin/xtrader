export type Theme = "dark" | "light";

const STORAGE_KEY = "xtrader.theme";

export function loadTheme(): Theme {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored === "dark" || stored === "light") return stored;
  } catch {
    // storage unavailable
  }
  return window.matchMedia?.("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

export function saveTheme(theme: Theme): void {
  try {
    localStorage.setItem(STORAGE_KEY, theme);
  } catch {
    // storage unavailable
  }
}

/** Colors handed to lightweight-charts; must mirror the CSS tokens in styles.css. */
export interface Palette {
  background: string;
  text: string;
  muted: string;
  grid: string;
  border: string;
  up: string;
  down: string;
  volumeUp: string;
  volumeDown: string;
  accent: string;
  /** The line of the price in the line chart, and the area under it. */
  price: string;
  equity: string;
  asset: string;
  crosshair: string;
  fontFamily: string;
}

const fontFamily =
  '"JetBrains Mono Variable", "JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, monospace';

export const palettes: Record<Theme, Palette> = {
  dark: {
    background: "#0f1723",
    text: "#a9b8c8",
    muted: "#8da0b5",
    grid: "rgba(128, 149, 171, 0.07)",
    border: "#1f2c3d",
    up: "#10b981",
    down: "#ef4444",
    volumeUp: "rgba(16, 185, 129, 0.26)",
    volumeDown: "rgba(239, 68, 68, 0.26)",
    accent: "#f68b1f",
    price: "#3aa0dc",
    equity: "#3aa0dc",
    asset: "#a78bfa",
    crosshair: "rgba(141, 160, 181, 0.6)",
    fontFamily,
  },
  light: {
    background: "#ffffff",
    text: "#3a4a5e",
    muted: "#4a5a70",
    grid: "rgba(11, 21, 36, 0.05)",
    border: "#e1e7ef",
    up: "#059669",
    down: "#dc2626",
    volumeUp: "rgba(5, 150, 105, 0.24)",
    volumeDown: "rgba(220, 38, 38, 0.22)",
    accent: "#e5780b",
    price: "#1f7fbf",
    equity: "#1f7fbf",
    asset: "#7c3aed",
    crosshair: "rgba(74, 90, 112, 0.55)",
    fontFamily,
  },
};

/**
 * Strategies pass CSS color names ("red", "blue"). Pure named colors clash
 * with the palette, so the most common ones are mapped to softer tones with
 * the same hue. Anything else (hex, rgb…) is used verbatim.
 */
const namedColors: Record<string, string> = {
  red: "#f87171",
  blue: "#60a5fa",
  green: "#4ade80",
  purple: "#c084fc",
  orange: "#fb923c",
  yellow: "#facc15",
  pink: "#f472b6",
  cyan: "#22d3ee",
  teal: "#2dd4bf",
  gray: "#9ca3af",
  grey: "#9ca3af",
  white: "#e5e7eb",
  black: "#111827",
  brown: "#a16207",
  magenta: "#e879f9",
  lime: "#a3e635",
  navy: "#3b82f6",
  gold: "#eab308",
};

export function seriesColor(color: string, fallback: string): string {
  if (!color) return fallback;
  return namedColors[color.trim().toLowerCase()] ?? color;
}
