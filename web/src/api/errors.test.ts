import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchOrOffline, NetworkError, OFFLINE_MESSAGE } from "./errors";

describe("a request that can't reach the server", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fails in Portuguese, the error of the browser kept as its cause", async () => {
    const browser = new TypeError("Failed to fetch");
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(browser));
    const failure = await fetchOrOffline("/api/agents").catch((e: unknown) => e);
    expect(failure).toBeInstanceOf(NetworkError);
    expect((failure as Error).message).toBe(OFFLINE_MESSAGE);
    expect((failure as Error).cause).toBe(browser);
  });

  it("leaves an abort as it is", async () => {
    const controller = new AbortController();
    controller.abort();
    const abort = new DOMException("The operation was aborted.", "AbortError");
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(abort));
    await expect(fetchOrOffline("/api/agents", { signal: controller.signal })).rejects.toBe(abort);
  });

  it("answers as fetch does when the server answers, an error status included", async () => {
    const answer = new Response("{}", { status: 500 });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(answer));
    await expect(fetchOrOffline("/api/agents")).resolves.toBe(answer);
  });
});
