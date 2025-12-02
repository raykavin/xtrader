import { type KeyboardEvent, useEffect, useId, useLayoutEffect, useRef, useState } from "react";

export interface SelectOption {
  value: string;
  label: string;
  /** Muted after the label, eg: the address of an agent. */
  hint?: string;
  /** A status dot before the label: online or offline. */
  status?: "on" | "off";
  /** Read instead of the label, eg: with the status the dot shows. */
  description?: string;
}

/** The room between a list and its trigger, and between it and the edges of the window. */
const GAP = 6;
const MARGIN = 8;
/** The height of a list at most. */
const MAX_HEIGHT = 420;

interface Props {
  caption: string;
  value: string | null;
  options: SelectOption[];
  onChange: (value: string) => void;
  /** Shown while there are no options. */
  placeholder?: string;
  /** A line above the options, eg: how many agents are online. */
  summary?: string;
}

/**
 * A select in the style of the dashboard: a pill opening a list of options,
 * which the native one draws in the colors of the system. The list is driven
 * with the arrows, Home, End, Enter and Escape, and closes on a click outside.
 * It opens in the top layer of the page, above the cards and the dialogs that
 * would cover or clip it, below its trigger, or above it when the window has
 * more room there.
 */
export function Select({ caption, value, options, onChange, placeholder, summary }: Props) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const id = useId();
  const selected = options.find((o) => o.value === value);

  const close = (focus = true) => {
    setOpen(false);
    if (focus) trigger.current?.focus();
  };
  const choose = (option: SelectOption) => {
    close();
    if (option.value !== value) onChange(option.value);
  };

  // Open: the list in the top layer (the popover API) at its trigger, placed
  // again as the page scrolls or resizes; an older browser keeps it in the flow.
  useLayoutEffect(() => {
    const menu = list.current;
    if (!open || !menu || !("showPopover" in menu)) return;
    menu.showPopover();
    const place = () => {
      const t = trigger.current?.getBoundingClientRect();
      if (!t) return;
      const below = window.innerHeight - t.bottom - GAP - MARGIN;
      const above = t.top - GAP - MARGIN;
      const down = below >= Math.min(menu.scrollHeight, MAX_HEIGHT) || below >= above;
      menu.style.minWidth = `${t.width}px`;
      menu.style.maxHeight = `${Math.max(0, Math.min(MAX_HEIGHT, down ? below : above))}px`;
      menu.style.top = down ? `${t.bottom + GAP}px` : "auto";
      menu.style.bottom = down ? "auto" : `${window.innerHeight - t.top + GAP}px`;
      const right = window.innerWidth - MARGIN - menu.offsetWidth;
      menu.style.left = `${Math.max(MARGIN, Math.min(t.left, right))}px`;
    };
    place();
    let frame = 0;
    const follow = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(place);
    };
    window.addEventListener("resize", follow);
    document.addEventListener("scroll", follow, true);
    return () => {
      cancelAnimationFrame(frame);
      window.removeEventListener("resize", follow);
      document.removeEventListener("scroll", follow, true);
      if (menu.matches(":popover-open")) menu.hidePopover();
    };
  }, [open]);

  // Open: focus the selected option, and close on a click outside.
  useEffect(() => {
    if (!open) return;
    const current =
      list.current?.querySelector<HTMLElement>('[aria-selected="true"]') ??
      list.current?.querySelector<HTMLElement>('[role="option"]');
    current?.focus();
    current?.scrollIntoView({ block: "nearest" });

    const onPointer = (e: PointerEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", onPointer);
    return () => document.removeEventListener("pointerdown", onPointer);
  }, [open]);

  const onTriggerKey = (e: KeyboardEvent) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      setOpen(true);
    }
  };

  const onListKey = (e: KeyboardEvent) => {
    const items = Array.from(list.current?.querySelectorAll<HTMLElement>('[role="option"]') ?? []);
    const current = items.indexOf(document.activeElement as HTMLElement);
    const focus = (index: number) => items[Math.max(0, Math.min(items.length - 1, index))]?.focus();
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        focus(current + 1);
        break;
      case "ArrowUp":
        e.preventDefault();
        focus(current - 1);
        break;
      case "Home":
        e.preventDefault();
        focus(0);
        break;
      case "End":
        e.preventDefault();
        focus(items.length - 1);
        break;
      case "Escape":
        e.preventDefault();
        close();
        break;
      case "Tab":
        close(false);
        break;
    }
  };

  return (
    <div className={open ? "select open" : "select"} ref={root}>
      <button
        type="button"
        ref={trigger}
        className="select-trigger"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? `${id}-list` : undefined}
        disabled={options.length === 0}
        onClick={() => (open ? close() : setOpen(true))}
        onKeyDown={onTriggerKey}
      >
        <span className="select-caption">{caption}</span>
        {selected?.status && <i className={`dot ${selected.status}`} aria-hidden="true" />}
        <span className="select-value">{selected?.label ?? placeholder}</span>
        <span className="chevron" aria-hidden="true" />
      </button>
      {open && (
        <div
          className="select-menu"
          popover="manual"
          id={`${id}-list`}
          role="listbox"
          aria-label={caption}
          tabIndex={-1}
          ref={list}
          onKeyDown={onListKey}
        >
          {summary && <div className="select-summary">{summary}</div>}
          {options.map((option) => (
            <button
              type="button"
              key={option.value}
              role="option"
              aria-selected={option.value === value}
              aria-label={option.description}
              tabIndex={-1}
              className="select-option"
              onClick={() => choose(option)}
            >
              {option.status && <i className={`dot ${option.status}`} aria-hidden="true" />}
              <span className="select-label">{option.label}</span>
              {option.hint && <span className="select-hint">{option.hint}</span>}
              <span className="select-check" aria-hidden="true" />
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
