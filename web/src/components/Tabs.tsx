import type { KeyboardEvent, ReactNode } from "react";
import { Icon, type IconName } from "./Icon";

export interface TabItem<T extends string> {
  id: T;
  label: string;
  icon?: IconName;
  /** After the label, eg: how many rows the tab holds. */
  count?: number | string;
}

interface Props<T extends string> {
  /** Names the tabs for the screen readers. */
  label: string;
  /** Prefixes the IDs of the tabs and of their panels, see tabIds. */
  idPrefix: string;
  items: TabItem<T>[];
  value: T;
  onChange: (value: T) => void;
  /** At the end of the bar, eg: the action of the tab shown. */
  aside?: ReactNode;
}

/** The IDs of a tab and of its panel, which the panel labels itself with. */
export function tabIds(idPrefix: string, id: string) {
  return { tab: `${idPrefix}-tab-${id}`, panel: `${idPrefix}-panel-${id}` };
}

/**
 * A bar of tabs, the arrows, Home and End moving between them: the panels
 * are the parent's, see tabIds.
 */
export function Tabs<T extends string>({
  label,
  idPrefix,
  items,
  value,
  onChange,
  aside,
}: Props<T>) {
  const onKey = (e: KeyboardEvent) => {
    const index = items.findIndex((item) => item.id === value);
    const last = items.length - 1;
    let target: number;
    switch (e.key) {
      case "ArrowLeft":
        target = index <= 0 ? last : index - 1;
        break;
      case "ArrowRight":
        target = index === last ? 0 : index + 1;
        break;
      case "Home":
        target = 0;
        break;
      case "End":
        target = last;
        break;
      default:
        return;
    }
    const next = items[target];
    if (!next) return;
    e.preventDefault();
    onChange(next.id);
    document.getElementById(tabIds(idPrefix, next.id).tab)?.focus();
  };

  return (
    <div className="tabs-bar">
      <div className="tabs" role="tablist" aria-label={label}>
        {items.map((item) => {
          const ids = tabIds(idPrefix, item.id);
          const selected = item.id === value;
          return (
            <button
              key={item.id}
              type="button"
              role="tab"
              id={ids.tab}
              className="tab"
              aria-selected={selected}
              aria-controls={ids.panel}
              tabIndex={selected ? 0 : -1}
              onClick={() => onChange(item.id)}
              onKeyDown={onKey}
            >
              {item.icon && <Icon name={item.icon} />}
              {item.label}
              {item.count !== undefined && <span className="tab-count">{item.count}</span>}
            </button>
          );
        })}
      </div>
      {aside && <div className="tabs-aside">{aside}</div>}
    </div>
  );
}
