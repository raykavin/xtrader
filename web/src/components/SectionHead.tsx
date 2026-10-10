import type { ReactNode } from "react";

interface Props {
  /** The ID of the title, the section labelled by it. */
  id?: string;
  /** A short line over the title, the kind of the section, eg: Operar. */
  eyebrow: ReactNode;
  title: ReactNode;
  /** Muted beside the title, eg: the pair of the ticket. */
  tag?: ReactNode;
  /** Below the title, eg: when the decision was taken. */
  subtitle?: ReactNode;
  /** At the right of the head, eg: the timeframes of the chart. */
  aside?: ReactNode;
  className?: string;
}

/**
 * The head of a card of the panel, the same in all of them: the eyebrow
 * marked in the accent, the title and its tag, a line under it, and the
 * controls of the card at its right.
 */
export function SectionHead({ id, eyebrow, title, tag, subtitle, aside, className = "" }: Props) {
  return (
    <header className={`section-head ${className}`.trim()}>
      <div className="section-heading">
        <span className="eyebrow">{eyebrow}</span>
        <h2 id={id} className="section-name">
          {title}
          {tag && <span className="section-tag">{tag}</span>}
        </h2>
        {subtitle && <p className="section-sub">{subtitle}</p>}
      </div>
      {aside && <div className="section-aside">{aside}</div>}
    </header>
  );
}
