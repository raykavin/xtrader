import { useSyncExternalStore } from "react";

// Whether the amounts of money are hidden, on every page: the balances, the
// profits and the limits show a mask in place of their digits (see
// formatMoney and formatProfit), the prices and the quantities as they are.
// Kept in the browser (localStorage), the same in all its tabs; shown without
// one saved.

/** The key of the choice in localStorage. */
const STORAGE_KEY = "xtrader.hideValues";

/** What an amount hidden shows. */
export const HIDDEN_AMOUNT = "•••••";

function load(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === "1";
  } catch {
    return false; // storage unavailable
  }
}

let hidden = load();
const listeners = new Set<() => void>();

function changed(next: boolean) {
  hidden = next;
  for (const listener of listeners) listener();
}

// hidden or shown in another tab
if (typeof window !== "undefined") {
  window.addEventListener("storage", (e) => {
    if (e.key === STORAGE_KEY && (e.newValue === "1") !== hidden) changed(e.newValue === "1");
  });
}

/** Whether the amounts are hidden now. */
export function valuesHidden(): boolean {
  return hidden;
}

/** Hides or shows the amounts, on every page, kept in the browser. */
export function setValuesHidden(next: boolean): void {
  if (next === hidden) return;
  try {
    if (next) localStorage.setItem(STORAGE_KEY, "1");
    else localStorage.removeItem(STORAGE_KEY);
  } catch {
    // storage unavailable: hidden in this page only
  }
  changed(next);
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** Whether the amounts are hidden, the component drawn again as they are hidden or shown. */
export function useValuesHidden(): boolean {
  return useSyncExternalStore(subscribe, valuesHidden);
}
