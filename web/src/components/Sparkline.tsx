import { useId } from "react";
import type { Point } from "../api/types";

interface Props {
  points: Point[];
  /** CSS color of the line and glow. */
  color: string;
  width?: number;
  height?: number;
  className?: string;
  /** Whether the area under the line is filled, fading out. */
  area?: boolean;
  /** The times of the ends of the x axis, the points placed by their times; else evenly, one after another. */
  domain?: [number, number];
  /** Whether the value holds until the next point, eg: a running total, drawn in steps. */
  step?: boolean;
  /** Whether 0 is marked by a dashed line, when within the values. */
  zero?: boolean;
}

/** Downsamples to at most `max` points, keeping the first and last. */
function sample(points: Point[], max: number): Point[] {
  if (points.length <= max) return points;
  const step = (points.length - 1) / (max - 1);
  const out: Point[] = [];
  for (let i = 0; i < max; i++) {
    const p = points[Math.round(i * step)];
    if (p) out.push(p);
  }
  return out;
}

/** Tiny inline SVG line chart used as the signature of the hero stat. */
export function Sparkline({
  points,
  color,
  width = 320,
  height = 64,
  className,
  area: filled = true,
  domain,
  step = false,
  zero = false,
}: Props) {
  const id = useId();
  const data = sample(points, 160);
  if (data.length < 2) return null;

  let min = Number.POSITIVE_INFINITY;
  let max = Number.NEGATIVE_INFINITY;
  for (const p of data) {
    if (p.value < min) min = p.value;
    if (p.value > max) max = p.value;
  }
  const span = max - min || 1;
  const pad = 4;
  const innerH = height - pad * 2;
  const yOf = (value: number) => pad + innerH - ((value - min) / span) * innerH;
  const [start, end] = domain ?? [0, 0];
  const xOf = (p: Point, i: number) =>
    domain && end > start
      ? ((p.time - start) / (end - start)) * width
      : (i / (data.length - 1)) * width;
  const coords: (readonly [number, number])[] = [];
  data.forEach((p, i) => {
    const x = xOf(p, i);
    const y = yOf(p.value);
    const previous = coords[coords.length - 1];
    if (step && previous) coords.push([x, previous[1]] as const);
    coords.push([x, y] as const);
  });
  const line = coords
    .map(([x, y], i) => `${i === 0 ? "M" : "L"}${x.toFixed(1)} ${y.toFixed(1)}`)
    .join(" ");
  const first = coords[0]?.[0] ?? 0;
  const last = coords[coords.length - 1]?.[0] ?? width;
  const area = `${line} L${last.toFixed(1)} ${height} L${first.toFixed(1)} ${height} Z`;

  return (
    <svg
      className={className}
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      aria-hidden="true"
      focusable="false"
    >
      {filled && (
        <>
          <defs>
            <linearGradient id={`${id}-fill`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={color} stopOpacity="0.28" />
              <stop offset="100%" stopColor={color} stopOpacity="0" />
            </linearGradient>
          </defs>
          <path d={area} fill={`url(#${id}-fill)`} />
        </>
      )}
      {zero && min < 0 && max > 0 && (
        <line
          x1="0"
          x2={width}
          y1={yOf(0)}
          y2={yOf(0)}
          stroke="currentColor"
          strokeOpacity="0.35"
          strokeDasharray="3 3"
          vectorEffect="non-scaling-stroke"
        />
      )}
      <path
        d={line}
        fill="none"
        stroke={color}
        strokeWidth="1.5"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}
