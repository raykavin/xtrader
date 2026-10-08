/** An error answered by the API, with its HTTP status and, if told, the seconds to wait before trying again. */
export class APIError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly retryAfter?: number,
  ) {
    super(message);
  }
}

/** The seconds of a Retry-After header, undefined for none or a date. */
export function retryAfterSeconds(header: string | null): number | undefined {
  if (header === null || !/^\d+$/.test(header.trim())) return undefined;
  return Number(header.trim());
}
