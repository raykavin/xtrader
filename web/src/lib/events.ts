// The live stream of an agent: an EventSource opened again once it gives up,
// which tells when it is back after a drop, so what came meanwhile is loaded
// again, and goes to the login once the session is gone.

/** The part of an EventSource the stream uses. */
export interface EventSourceLike {
  readonly readyState: number;
  addEventListener(type: string, listener: (event: Event) => void): void;
  close(): void;
}

/** EventSource.CLOSED: given up, never reconnecting by itself. */
const CLOSED = 2;

/** How long a stream given up waits before it is opened again, in ms. */
export const RETRY_DELAY = 5000;

export interface LiveStreamOptions {
  /** Opens the stream, its events listened to. */
  open: () => EventSourceLike;
  /** Whether the stream is connected. */
  onStatus?: (connected: boolean) => void;
  /**
   * The stream is open again after a drop: the events sent meanwhile were
   * missed, what they change has to be loaded again.
   */
  onResync?: () => void;
  /**
   * Whether the browser still holds a session, asked once the stream gives
   * up: the API answers 401 without one, which an EventSource can't tell.
   */
  session: (signal: AbortSignal) => Promise<boolean>;
  /** The session is gone: the stream is closed for good. */
  onLoggedOut: () => void;
  /** How long to wait before opening the stream again, in ms. */
  retryDelay?: number;
}

/**
 * Opens a live stream. An EventSource reconnects by itself after a network
 * error, but gives up on an HTTP error, such as the 502 of the dashboard while
 * the agent is offline or its 401 once the session expired: then the session
 * is checked, and the stream is opened again after a while, or the login
 * follows without one. A session that can't be checked counts as held: the
 * stream is tried again. The returned function closes the stream.
 */
export function liveStream(options: LiveStreamOptions): () => void {
  const delay = options.retryDelay ?? RETRY_DELAY;
  let source: EventSourceLike | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let checking: AbortController | undefined;
  let closed = false;
  // dropped since the stream was last open, or never open yet: its events
  // of the meantime were missed
  let dropped = false;

  const retry = () => {
    if (!closed) timer = setTimeout(connect, delay);
  };

  const connect = () => {
    if (closed) return;
    const opened = options.open();
    source = opened;
    opened.addEventListener("open", () => {
      if (closed || source !== opened) return;
      options.onStatus?.(true);
      if (dropped) {
        dropped = false;
        options.onResync?.();
      }
    });
    opened.addEventListener("error", () => {
      if (closed || source !== opened) return;
      dropped = true;
      options.onStatus?.(false);
      if (opened.readyState !== CLOSED) return; // reconnecting by itself
      const check = new AbortController();
      checking = check;
      options.session(check.signal).then(
        (held) => {
          if (closed || check.signal.aborted) return;
          if (held) {
            retry();
            return;
          }
          closed = true;
          options.onLoggedOut();
        },
        () => {
          // can't tell, eg: the dashboard is restarting: try again
          if (!closed && !check.signal.aborted) retry();
        },
      );
    });
  };

  connect();
  return () => {
    closed = true;
    clearTimeout(timer);
    checking?.abort();
    source?.close();
  };
}
