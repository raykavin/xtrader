import type { ReactNode } from "react";

interface Props {
  /** The ID of the title, the section labelled by it. */
  id?: string;
  /** A short line over the title, the kind of the section, eg: Operar. */
  eyebrow: ReactNode;
  title: ReactNode;
  /** Muted beside the title, eg: the pair shown. */
  tag?: ReactNode;
  /** Below the title, eg: when the decision was taken. */
  subtitle?: ReactNode;
  /** On the line of the title, after it, eg: the timeframes of the chart. */
  inline?: ReactNode;
  /** At the right of the head, eg: the verdict of a decision. */
  aside?: ReactNode;
  className?: string;
}

/**
 * The head of a card of the panel, the same in all of them: the eyebrow
 * marked in the accent, the title and its tag, the controls on its line (eg:
 * the timeframes of the chart), a line under it, and the controls of the card
 * at its right.
 */
export function SectionHead({
  id,
  eyebrow,
  title,
  tag,
  subtitle,
  inline,
  aside,
  className = "",
}: Props) {
  return (
    <header className={`section-head ${className}`.trim()}>
      <div className="section-heading">
        <span className="eyebrow">{eyebrow}</span>
        {inline ? (
          <div className="section-line">
            <h2 id={id} className="section-name">
              {title}
              {tag && <span className="section-tag">{tag}</span>}
            </h2>
            {inline}
          </div>
        ) : (
          <h2 id={id} className="section-name">
            {title}
            {tag && <span className="section-tag">{tag}</span>}
          </h2>
        )}
        {subtitle && <p className="section-sub">{subtitle}</p>}
      </div>
      {aside && <div className="section-aside">{aside}</div>}
    </header>
  );
}
