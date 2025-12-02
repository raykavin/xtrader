/** The mark of a status: a play when it runs, a stop when it stopped, a dot for the others. */
export type StatusMark = "play" | "stop";

/** What a status chip shows before its label, in the color of its tone. */
export function StatusIcon({ mark }: { mark?: StatusMark }) {
  if (!mark) return <i />;
  return (
    <svg className="status-icon" viewBox="0 0 10 10" aria-hidden="true" focusable="false">
      {mark === "play" ? (
        <path d="M2.5 1.2v7.6L8.8 5z" />
      ) : (
        <rect x="1.8" y="1.8" width="6.4" height="6.4" rx="1" />
      )}
    </svg>
  );
}
