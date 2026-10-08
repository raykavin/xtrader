import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { type EventSourceLike, liveStream, RETRY_DELAY } from "./events";

const CONNECTING = 0;
const OPEN = 1;
const CLOSED = 2;

/** An EventSource of the test: its events are emitted by hand. */
class FakeSource implements EventSourceLike {
  readyState = CONNECTING;
  closed = false;
  private listeners = new Map<string, ((event: Event) => void)[]>();

  addEventListener(type: string, listener: (event: Event) => void): void {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }

  close(): void {
    this.closed = true;
    this.readyState = CLOSED;
  }

  open(): void {
    this.readyState = OPEN;
    this.emit("open");
  }

  /** A network error: the EventSource reconnects by itself. */
  drop(): void {
    this.readyState = CONNECTING;
    this.emit("error");
  }

  /** An HTTP error, eg: 401 or 502: the EventSource gives up. */
  fail(): void {
    this.readyState = CLOSED;
    this.emit("error");
  }

  private emit(type: string): void {
    for (const listener of this.listeners.get(type) ?? []) listener(new Event(type));
  }
}

function setup(session: (signal: AbortSignal) => Promise<boolean> = async () => true) {
  const sources: FakeSource[] = [];
  const calls = { status: [] as boolean[], resyncs: 0, loggedOut: 0, sessions: 0 };
  const close = liveStream({
    open: () => {
      const source = new FakeSource();
      sources.push(source);
      return source;
    },
    onStatus: (connected) => calls.status.push(connected),
    onResync: () => calls.resyncs++,
    session: (signal) => {
      calls.sessions++;
      return session(signal);
    },
    onLoggedOut: () => calls.loggedOut++,
  });
  const last = () => sources[sources.length - 1] as FakeSource;
  return { sources, calls, close, last };
}

describe("liveStream", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("doesn't resync on the first open", () => {
    const s = setup();
    s.last().open();
    expect(s.calls.status).toEqual([true]);
    expect(s.calls.resyncs).toBe(0);
  });

  it("resyncs once reconnected by itself after a drop", () => {
    const s = setup();
    s.last().open();
    s.last().drop();
    expect(s.calls.status).toEqual([true, false]);
    expect(s.calls.resyncs).toBe(0);
    s.last().open(); // the same EventSource, reconnected
    expect(s.sources).toHaveLength(1);
    expect(s.calls.status).toEqual([true, false, true]);
    expect(s.calls.resyncs).toBe(1);
    // an open without a drop before it changes nothing
    s.last().open();
    expect(s.calls.resyncs).toBe(1);
    expect(s.calls.sessions).toBe(0); // reconnecting by itself: nothing to check
  });

  it("opens the stream again once given up, then resyncs", async () => {
    const s = setup();
    s.last().open();
    s.last().fail(); // eg: 502 while the agent restarts
    await vi.advanceTimersByTimeAsync(0);
    expect(s.calls.sessions).toBe(1);
    expect(s.sources).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(RETRY_DELAY);
    expect(s.sources).toHaveLength(2);
    s.last().fail(); // still offline
    await vi.advanceTimersByTimeAsync(RETRY_DELAY);
    expect(s.sources).toHaveLength(3);
    s.last().open();
    expect(s.calls.resyncs).toBe(1);
    expect(s.calls.loggedOut).toBe(0);
  });

  it("resyncs when the first open follows a failure", async () => {
    // the agent was offline as the page loaded: what it loaded then failed
    const s = setup();
    s.last().fail();
    await vi.advanceTimersByTimeAsync(RETRY_DELAY);
    s.last().open();
    expect(s.calls.resyncs).toBe(1);
  });

  it("goes to the login once the session is gone", async () => {
    const s = setup(async () => false);
    s.last().open();
    s.last().fail(); // the 401 of the dashboard
    await vi.advanceTimersByTimeAsync(0);
    expect(s.calls.loggedOut).toBe(1);
    await vi.advanceTimersByTimeAsync(RETRY_DELAY * 10);
    expect(s.sources).toHaveLength(1); // never opened again
    expect(s.calls.sessions).toBe(1);
  });

  it("tries again when the session can't be checked", async () => {
    const s = setup(async () => {
      throw new Error("network");
    });
    s.last().fail();
    await vi.advanceTimersByTimeAsync(RETRY_DELAY);
    expect(s.calls.loggedOut).toBe(0);
    expect(s.sources).toHaveLength(2);
  });

  it("does nothing once closed", async () => {
    let answer: ((held: boolean) => void) | undefined;
    let aborted: AbortSignal | undefined;
    const s = setup(
      (signal) =>
        new Promise<boolean>((resolve) => {
          aborted = signal;
          answer = resolve;
        }),
    );
    s.last().fail();
    s.close();
    expect(aborted?.aborted).toBe(true);
    answer?.(false);
    await vi.advanceTimersByTimeAsync(RETRY_DELAY * 2);
    expect(s.calls.loggedOut).toBe(0);
    expect(s.sources).toHaveLength(1);
    expect(s.last().closed).toBe(true);
  });

  it("closes the stream and its retry", async () => {
    const s = setup();
    s.last().fail();
    await vi.advanceTimersByTimeAsync(0);
    s.close();
    await vi.advanceTimersByTimeAsync(RETRY_DELAY * 2);
    expect(s.sources).toHaveLength(1);
  });
});
