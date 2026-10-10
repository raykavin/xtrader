// The sentiment of the market as the language model of a strategy reads it,
// see Decision.sentiment: from 0, extremely bearish, to 100, extremely
// bullish, 50 neutral. It only informs: it doesn't trade.

import type { Decision } from "../api/types";

export const SENTIMENT_MIN = 0;
export const SENTIMENT_MAX = 100;

/** The tone of a band of the sentiment, for its color. */
export type SentimentTone = "bear-strong" | "bear" | "neutral" | "bull" | "bull-strong";

/** The newest reading of the sentiment, its decision, and the reading before it, if any. */
export interface SentimentReading {
  value: number;
  decision: Decision;
  previous?: number;
}

/** Whether value is a sentiment: a number of the range. */
function isSentiment(value: number | undefined): value is number {
  return (
    typeof value === "number" &&
    Number.isFinite(value) &&
    value >= SENTIMENT_MIN &&
    value <= SENTIMENT_MAX
  );
}

/**
 * The newest reading of the sentiment of decisions, newest first: the one of
 * the newest decision with one (a skipped or failed analysis has none), and
 * the one before it; null without any.
 */
export function latestSentiment(decisions: Decision[]): SentimentReading | null {
  let reading: SentimentReading | null = null;
  for (const decision of decisions) {
    if (!isSentiment(decision.sentiment)) continue;
    if (!reading) {
      reading = { value: decision.sentiment, decision };
      continue;
    }
    reading.previous = decision.sentiment;
    break;
  }
  return reading;
}

/**
 * The band of a sentiment, by fifths of its range: very bearish under 20,
 * bearish under 40, neutral from 40 to 60, bullish up to 80, very bullish
 * above.
 */
export function sentimentBand(value: number): { label: string; tone: SentimentTone } {
  if (value < 20) return { label: "Muito baixista", tone: "bear-strong" };
  if (value < 40) return { label: "Baixista", tone: "bear" };
  if (value <= 60) return { label: "Neutro", tone: "neutral" };
  if (value <= 80) return { label: "Altista", tone: "bull" };
  return { label: "Muito altista", tone: "bull-strong" };
}

/** A sentiment as shown: whole, eg: 62. */
export function formatSentiment(value: number): string {
  return String(Math.round(value));
}

/**
 * The change from the previous reading, whole and signed, eg: +8; null without
 * one or for a change under 1.
 */
export function sentimentChange(reading: SentimentReading): string | null {
  if (reading.previous === undefined) return null;
  const change = Math.round(reading.value) - Math.round(reading.previous);
  if (change === 0) return null;
  return change > 0 ? `+${change}` : `−${Math.abs(change)}`;
}

/**
 * The angle of the needle of the gauge, in degrees clockwise from pointing
 * right: 180 (left) at 0, 270 (up) at 50, 360 (right) at 100; the value
 * kept in the range.
 */
export function needleAngle(value: number): number {
  const kept = Math.min(SENTIMENT_MAX, Math.max(SENTIMENT_MIN, value));
  return 180 + (kept / SENTIMENT_MAX) * 180;
}
