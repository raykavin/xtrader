import { useEffect, useState } from "react";
import { fetchOpenPositions } from "../api/client";
import type { OpenPositionsResponse } from "../api/types";

/** How often the open positions of the agents are read again, in ms. */
const REFRESH = 5000;

/** The state of the read shared by every component showing it. */
interface OpenState {
  read: OpenPositionsResponse | null;
  error: string | null;
}

/**
 * The last read, kept as the dashboard switches agents, and the components
 * following it: one read at a time for all of them, every REFRESH while any
 * is shown.
 */
let state: OpenState = { read: null, error: null };
const listeners = new Set<(next: OpenState) => void>();
let timer: number | undefined;
let controller: AbortController | undefined;

function load() {
  const signal = controller?.signal;
  fetchOpenPositions(signal)
    .then((read) => publish({ read, error: null }))
    .catch((e) => {
      if (!signal?.aborted)
        publish({ ...state, error: e instanceof Error ? e.message : String(e) });
    });
}

function publish(next: OpenState) {
  state = next;
  for (const listener of listeners) listener(next);
}

/** The open positions of the agents, read at once and every REFRESH, the last one kept on a failure. */
export function useOpenPositions(): OpenState {
  const [current, setCurrent] = useState<OpenState>(state);
  useEffect(() => {
    listeners.add(setCurrent);
    if (listeners.size === 1) {
      controller = new AbortController();
      load();
      timer = window.setInterval(load, REFRESH);
    }
    return () => {
      listeners.delete(setCurrent);
      if (listeners.size === 0) {
        controller?.abort();
        window.clearInterval(timer);
      }
    };
  }, []);
  return current;
}
