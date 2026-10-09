import { formatTime } from "../lib/format";

/** A time of a row of a table: the hour over its date, narrower than both in a line. */
export function TimeCell({ unix }: { unix: number }) {
  const text = formatTime(unix);
  const split = text.lastIndexOf(" ");
  if (split < 0) return <>{text}</>;
  return (
    <span className="time-cell">
      <span>{text.slice(split + 1)}</span>
      <small>{text.slice(0, split)}</small>
    </span>
  );
}
