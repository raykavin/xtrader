// Server-sent events read from the body of a fetch, as an EventSource reads
// them: an EventSource asks with GET only, and a message to the XTrader AI is
// a POST with the CSRF token of the session.

/** An event of a stream: its name ("message" without one) and its data. */
export interface StreamEvent {
  event: string;
  data: string;
}

/**
 * A parser of server-sent events fed the text of a stream in pieces cut
 * anywhere, even in the middle of a line: each event ends at a blank line,
 * its data lines joined by a newline; comments (":") and the fields it
 * doesn't use (id, retry) are skipped.
 */
export function createSSEParser(onEvent: (event: StreamEvent) => void) {
  let buffer = "";
  let name = "";
  let data: string[] = [];

  const line = (text: string) => {
    if (text === "") {
      if (data.length > 0) onEvent({ event: name || "message", data: data.join("\n") });
      name = "";
      data = [];
      return;
    }
    if (text.startsWith(":")) return;
    const colon = text.indexOf(":");
    const field = colon < 0 ? text : text.slice(0, colon);
    let value = colon < 0 ? "" : text.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") name = value;
    else if (field === "data") data.push(value);
  };

  return {
    /** Feeds a piece of the stream. */
    push(chunk: string) {
      buffer += chunk;
      let end = buffer.search(/\r\n|\r|\n/);
      while (end >= 0) {
        const breakLength = buffer.startsWith("\r\n", end) ? 2 : 1;
        // a lone \r at the end of a piece may be the start of \r\n
        if (buffer[end] === "\r" && end === buffer.length - 1) break;
        line(buffer.slice(0, end));
        buffer = buffer.slice(end + breakLength);
        end = buffer.search(/\r\n|\r|\n/);
      }
    },
    /** The stream ended: an event without its blank line still counts. */
    end() {
      if (buffer !== "") line(buffer);
      buffer = "";
      line("");
    },
  };
}
