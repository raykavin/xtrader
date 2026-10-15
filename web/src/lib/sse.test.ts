import { describe, expect, it } from "vitest";
import { createSSEParser, type StreamEvent } from "./sse";

function parse(...chunks: string[]): StreamEvent[] {
  const events: StreamEvent[] = [];
  const parser = createSSEParser((event) => events.push(event));
  for (const chunk of chunks) parser.push(chunk);
  parser.end();
  return events;
}

describe("the server-sent events of a fetch", () => {
  it("reads the events, their names and data, the comments skipped", () => {
    expect(
      parse(
        ': connected\n\nevent: text\ndata: {"text":"Oi"}\n\n: ping\n\nevent: done\ndata: {}\n\n',
      ),
    ).toEqual([
      { event: "text", data: '{"text":"Oi"}' },
      { event: "done", data: "{}" },
    ]);
  });

  it("puts together an event cut anywhere, even in a line or a CRLF", () => {
    expect(parse("event: te", "xt\r", '\ndata: {"text":', '"a b"}\r\n', "\r\n")).toEqual([
      { event: "text", data: '{"text":"a b"}' },
    ]);
  });

  it("joins the lines of a data, names an event without a name message, keeps the last one", () => {
    expect(parse("data: one\ndata: two\n\nevent: done\ndata: x")).toEqual([
      { event: "message", data: "one\ntwo" },
      { event: "done", data: "x" },
    ]);
  });
});
