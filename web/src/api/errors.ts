/** An error answered by the API, with its HTTP status and, if told, the seconds to wait before trying again. */
export class APIError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly retryAfter?: number,
    /** The body of the answer, eg: the preview of a change refused (409). */
    readonly body?: string,
  ) {
    super(message);
  }
}

/** The seconds of a Retry-After header, undefined for none or a date. */
export function retryAfterSeconds(header: string | null): number | undefined {
  if (header === null || !/^\d+$/.test(header.trim())) return undefined;
  return Number(header.trim());
}

/** What a request that never reached the server tells, eg: the network down. */
export const OFFLINE_MESSAGE = "Sem conexão com o XTrader. Verifique a rede e tente novamente.";

/**
 * A request that never reached the server (the network down, the server off),
 * in place of the error of the browser in English, eg: Failed to fetch; that
 * one kept as its cause.
 */
export class NetworkError extends Error {
  constructor(cause: unknown) {
    super(OFFLINE_MESSAGE, { cause });
    this.name = "NetworkError";
  }
}

/** fetch, a request that can't reach the server failing with a NetworkError; an abort as it is. */
export async function fetchOrOffline(input: string, init?: RequestInit): Promise<Response> {
  try {
    return await fetch(input, init);
  } catch (e) {
    if (init?.signal?.aborted) throw e; // canceled: the callers tell it apart
    throw new NetworkError(e);
  }
}
