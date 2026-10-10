import { formatTime } from "../lib/format";
import {
  formatSentiment,
  needleAngle,
  SENTIMENT_BANDS,
  type SentimentReading,
  type SentimentTone,
  sentimentBand,
  sentimentChange,
  sentimentDelta,
} from "../lib/sentiment";
import { Icon } from "./Icon";
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

/** The room between the bands on the arc, in hundredths of it. */
const BAND_GAP = 1.6;

/**
 * The sentiment of the market of the pair as the language model reads it,
 * the one of its newest decision with one: on a gauge from very bearish to
 * very bullish, an arc of its bands with the current one lit, its value and
 * its band inside it, and the reading before it to compare. Off for an agent
 * without a language model; it informs only: it doesn't trade.
 */
export function SentimentMeter({ available, reading, loaded, error }: Props) {
  const band = reading ? sentimentBand(reading.value) : null;
  const previous = reading?.previous !== undefined ? sentimentBand(reading.previous) : null;
  const delta = reading ? sentimentDelta(reading) : null;
  const state = !available ? "is-off" : reading ? "is-on" : "is-empty";
  return (
    <section
      className={`card meter-card ${state}`}
      aria-labelledby="sentiment-title"
      aria-disabled={!available || undefined}
    >
      <SectionHead
        id="sentiment-title"
        eyebrow="Análise do LLM"
        title="Sentimento do mercado"
        aside={
          reading && (
            <time
              className="meter-time"
              dateTime={new Date(reading.decision.decided_at * 1000).toISOString()}
              title={`Leitura de ${formatTime(reading.decision.decided_at)} UTC`}
            >
              {readingTime(reading.decision.decided_at)} UTC
            </time>
          )
        }
      />

      {available && !loaded && !reading ? (
        <Skeleton className="meter-skeleton" />
      ) : (
        <div className="meter-body">
          <div className="meter-dial">
            <span className={mark("neutral", band?.tone)} aria-hidden="true">
              Neutro
            </span>
            <div className="meter-gauge">
              <Gauge
                value={reading?.value ?? null}
                previous={reading?.previous}
                tone={band?.tone}
              />
              {reading && band && (
                <div className="meter-readout">
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
                  <span className={`meter-band ${band.tone}`} aria-hidden="true">
                    {band.label}
                  </span>
                </div>
              )}
            </div>
            <div className="meter-ends" aria-hidden="true">
              <span className={mark("bear-strong", band?.tone)}>Muito baixista</span>
              <span className={mark("bull-strong", band?.tone)}>Muito altista</span>
            </div>
          </div>

          {!reading && (
            <p className="meter-note">
              {!available
                ? "Disponível só em agentes LLM: esta estratégia não usa um modelo de linguagem."
                : error
                  ? error
                  : "Sem leitura ainda: aparece na próxima análise do modelo."}
            </p>
          )}

          {reading && previous && reading.previous !== undefined && (
            <dl className="meter-compare">
              <div className="meter-compare-item">
                <dt>
                  <i className="meter-ring" aria-hidden="true" />
                  Leitura anterior
                </dt>
                <dd>
                  <span className="meter-previous mono">{formatSentiment(reading.previous)}</span>
                  <span className={`meter-band small ${previous.tone}`}>{previous.label}</span>
                </dd>
              </div>
              <div className="meter-compare-item end">
                <dt>Variação</dt>
                <dd
                  className={
                    delta === null ? "meter-delta" : `meter-delta ${delta > 0 ? "up" : "down"}`
                  }
                  title={
                    delta === null
                      ? "Igual à leitura anterior"
                      : delta > 0
                        ? "Mais altista que a leitura anterior"
                        : "Mais baixista que a leitura anterior"
                  }
                >
                  {delta !== null && (
                    <Icon name="trend" size={15} className={delta < 0 ? "flip" : undefined} />
                  )}
                  <span className="mono">{sentimentChange(reading) ?? "0"}</span>
                </dd>
              </div>
            </dl>
          )}
        </div>
      )}
    </section>
  );
}

/** The class of the name of a band on the scale, lit for the band of the reading. */
function mark(tone: SentimentTone, current?: SentimentTone): string {
  return tone === current ? `meter-mark ${tone} current` : `meter-mark ${tone}`;
}

/** When the reading was taken, short: its time on a day of today (UTC), else its day before it. */
function readingTime(unixSeconds: number): string {
  const time = formatTime(unixSeconds); // 10/10/2026 19:31
  const today = formatTime(Date.now() / 1000).slice(0, 10);
  return time.startsWith(today) ? time.slice(11) : `${time.slice(0, 5)} ${time.slice(11)}`;
}

/**
 * The gauge: a half ring from 0 at the left to 100 at the right, a band of the
 * sentiment each, the one of the value lit, its knob at the value and a dot at
 * the reading before it; all the bands dim, without a knob, without a value.
 */
function Gauge({
  value,
  previous,
  tone,
}: {
  value: number | null;
  previous?: number;
  tone?: SentimentTone;
}) {
  const arc = "M 20 100 A 80 80 0 0 1 180 100";
  const span = 100 / SENTIMENT_BANDS.length;
  const at = (point: number) => {
    const angle = (needleAngle(point) * Math.PI) / 180;
    return { x: 100 + 80 * Math.cos(angle), y: 100 + 80 * Math.sin(angle) };
  };
  const dot = value !== null && previous !== undefined ? at(previous) : null;
  return (
    <svg className="gauge" viewBox="0 12 200 98" aria-hidden="true" focusable="false">
      {SENTIMENT_BANDS.map((band, i) => {
        const start = i === 0 ? 0 : i * span + BAND_GAP / 2;
        const end = i === SENTIMENT_BANDS.length - 1 ? 100 : (i + 1) * span - BAND_GAP / 2;
        return (
          <path
            key={band.tone}
            className={
              band.tone === tone ? `gauge-band ${band.tone} current` : `gauge-band ${band.tone}`
            }
            d={arc}
            pathLength={100}
            strokeDasharray={`0 ${start} ${end - start} 100`}
          />
        );
      })}
      {dot && <circle className="gauge-previous" cx={dot.x} cy={dot.y} r="4.5" />}
      {value !== null && (
        <g
          className={`gauge-knob ${tone ?? ""}`}
          style={{ transform: `rotate(${needleAngle(value)}deg)` }}
        >
          <circle cx="180" cy="100" r="8.5" />
        </g>
      )}
    </svg>
  );
}
