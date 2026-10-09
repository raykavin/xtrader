import type { ReactNode } from "react";
import { Icon, type IconName } from "./Icon";

export type AlertTone = "error" | "warn" | "success" | "info";

const alertIcons: Record<AlertTone, IconName> = {
  error: "error",
  warn: "alert",
  success: "success",
  info: "info",
};

interface AlertProps {
  tone?: AlertTone;
  /** A line in bold before the text. */
  title?: ReactNode;
  children: ReactNode;
  /** An action at its end, eg: a button to try again. */
  action?: ReactNode;
  className?: string;
}

/**
 * A message of the page in a box of its tone, with its icon: an error read
 * at once by the screen readers, the others politely.
 */
export function Alert({ tone = "info", title, children, action, className = "" }: AlertProps) {
  return (
    <div
      className={`alert alert-${tone} ${className}`.trim()}
      role={tone === "error" ? "alert" : "status"}
    >
      <Icon name={alertIcons[tone]} className="alert-icon" />
      <div className="alert-text">
        {title && <strong className="alert-title">{title}</strong>}
        <span>{children}</span>
      </div>
      {action && <div className="alert-action">{action}</div>}
    </div>
  );
}

interface EmptyProps {
  icon?: IconName;
  title: ReactNode;
  children?: ReactNode;
  /** What to do about it, eg: a button creating the first item. */
  action?: ReactNode;
  /** Smaller, inside a card. */
  compact?: boolean;
}

/** What a list or a page shows without anything in it, and what to do about it. */
export function EmptyState({
  icon = "inbox",
  title,
  children,
  action,
  compact = false,
}: EmptyProps) {
  return (
    <div className={compact ? "empty-state compact" : "empty-state"}>
      <span className="empty-icon" aria-hidden="true">
        <Icon name={icon} size={compact ? 18 : 22} />
      </span>
      <p className="empty-title">{title}</p>
      {children && <p className="empty-text">{children}</p>}
      {action && <div className="empty-action">{action}</div>}
    </div>
  );
}

/** A block standing for content loading, eg: a card, as tall as given. */
export function Skeleton({ height, className = "" }: { height?: number; className?: string }) {
  return (
    <span
      className={`skeleton ${className}`.trim()}
      style={height ? { height } : undefined}
      aria-hidden="true"
    />
  );
}

/**
 * Loading, told to the screen readers once, the blocks of the layout
 * coming drawn meanwhile.
 */
export function Loading({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="loading-block" role="status" aria-live="polite">
      <span className="sr-only">{label}</span>
      {children}
    </div>
  );
}
