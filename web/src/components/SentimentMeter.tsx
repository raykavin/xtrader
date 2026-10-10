import { useId } from "react";
import { formatTime } from "../lib/format";
import {
  formatSentiment,
  needleAngle,
  type SentimentReading,
  sentimentBand,
  sentimentChange,
} from "../lib/sentiment";
import { SectionHead } from "./SectionHead";
import { Skeleton } from "./States";

interface Props {
  /** Whether the agent reads the sentiment: an LLM one, its decisions kept. */
  available: boolean;
  /** The newest reading of the pair, null without any. */
  reading: SentimentReading | null;
  /** Whether the decisions of the pair were read: until then, a block stands for the reading. */
  loaded: boolean;
  error: string | null;
}

/** The ticks of the scale of the gauge, every 10. */
const TICKS = Array.from({ length: 11 }, (_, i) => i * 10);

/**
 * The sentiment of the market of the pair as the language model reads it,
 * the one of its newest decision with one, on a gauge from very bearish to
 * very bullish. Off for an agent without a language model; it informs only:
 * it doesn't trade.
 */
export function SentimentMeter({ available, reading, loaded, error }: Props) {
  const band = reading ? sentimentBand(reading.value) : null;
  const change = reading ? sentimentChange(reading) : null;
  const state = !available ? "off" : reading ? "on" : "empty";
  return (
    <section
      className={`card meter-card ${state}`}
      aria-labelledby="sentiment-title"
      aria-disabled={!available || undefined}
    >
      <SectionHead
        id="sentiment-title"
        eyebrow="Sentimento"
        title="Leitura do LLM"
        aside={
          reading && (
            <span className="meter-time" title="A decisão de onde vem a leitura">
              {formatTime(reading.decision.decided_at)} UTC
            </span>
          )
        }
      />

      {available && !loaded && !reading ? (
        <Skeleton className="meter-skeleton" />
      ) : (
        <div className="meter-body">
          <Gauge value={reading?.value ?? null} tone={band?.tone} />
          {reading && band ? (
            <>
              <meter
                className="sr-only"
                min={0}
                max={100}
                low={40}
                high={60}
                optimum={50}
                value={reading.value}
                aria-label={`Sentimento do mercado: ${formatSentiment(reading.value)} de 100, ${band.label.toLowerCase()}`}
              />
              <p className={`meter-value ${band.tone}`} aria-hidden="true">
                {formatSentiment(reading.value)}
                <small>/100</small>
              </p>
              <p className={`meter-band ${band.tone}`}>
                {band.label}
                {change && (
                  <span className="meter-change" title="Desde a leitura anterior">
                    {change}
                  </span>
                )}
              </p>
            </>
          ) : (
            <p className="meter-note">
              {!available
                ? "Disponível só em agentes LLM: esta estratégia não usa um modelo de linguagem."
                : error
                  ? error
                  : "Sem leitura ainda: aparece na próxima análise do modelo."}
            </p>
          )}
          <p className="meter-scale" aria-hidden="true">
            Muito baixista · Neutro · Muito altista
          </p>
        </div>
      )}
    </section>
  );
}

/**
 * The gauge: a half ring from 0 at the left to 100 at the right, filled up to
 * the value in the colors of the bands, its needle at the value; empty and
 * without a needle without a value.
 */
function Gauge({ value, tone }: { value: number | null; tone?: string }) {
  const gradient = `sentiment-${useId().replace(/[^a-zA-Z0-9_-]/g, "")}`;
  const arc = "M 20 100 A 80 80 0 0 1 180 100";
  return (
    <svg className="gauge" viewBox="0 0 200 118" aria-hidden="true" focusable="false">
      <defs>
        <linearGradient id={gradient} gradientUnits="userSpaceOnUse" x1="20" y1="0" x2="180" y2="0">
          <stop offset="0%" className="gauge-stop-bear" />
          <stop offset="50%" className="gauge-stop-neutral" />
          <stop offset="100%" className="gauge-stop-bull" />
        </linearGradient>
      </defs>
      <path className="gauge-track" d={arc} pathLength={100} />
      {value !== null && (
        <path
          className="gauge-fill"
          d={arc}
          pathLength={100}
          stroke={`url(#${gradient})`}
          strokeDasharray={`${value} 100`}
        />
      )}
      {TICKS.map((tick) => {
        const angle = (needleAngle(tick) * Math.PI) / 180;
        const inner = tick % 50 === 0 ? 58 : 62;
        return (
          <line
            key={tick}
            className="gauge-tick"
            x1={100 + inner * Math.cos(angle)}
            y1={100 + inner * Math.sin(angle)}
            x2={100 + 67 * Math.cos(angle)}
            y2={100 + 67 * Math.sin(angle)}
          />
        );
      })}
      <text className="gauge-end" x="20" y="116" textAnchor="middle">
        0
      </text>
      <text className="gauge-end" x="180" y="116" textAnchor="middle">
        100
      </text>
      {value !== null && (
        <g
          className={`gauge-needle ${tone ?? ""}`}
          style={{ transform: `rotate(${needleAngle(value)}deg)` }}
        >
          <line x1="100" y1="100" x2="164" y2="100" />
          <circle cx="100" cy="100" r="6" />
        </g>
      )}
    </svg>
  );
}
