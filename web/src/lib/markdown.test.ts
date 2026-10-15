import { describe, expect, it } from "vitest";
import { parseInline, parseMarkdown, safeHref } from "./markdown";

const text = (value: string) => ({ type: "text", text: value });

describe("the Markdown of the answers", () => {
  it("reads bold, italics, strikethrough, code and line breaks", () => {
    expect(parseInline("**forte** e *leve*, ~~não~~ `XAUUSDm`\nfim")).toEqual([
      { type: "strong", children: [text("forte")] },
      text(" e "),
      { type: "em", children: [text("leve")] },
      text(", "),
      { type: "del", children: [text("não")] },
      text(" "),
      { type: "code", text: "XAUUSDm" },
      { type: "br" },
      text("fim"),
    ]);
  });

  it("never takes the underscores of a name for italics, nor the stars of an unclosed bold", () => {
    expect(parseInline("chame block_entries e stop_when_flat")).toEqual([
      text("chame block_entries e stop_when_flat"),
    ]);
    expect(parseInline("**escrevendo")).toEqual([text("**escrevendo")]);
    expect(parseInline("_leve_ e \\*literal\\*")).toEqual([
      { type: "em", children: [text("leve")] },
      text(" e *literal*"),
    ]);
  });

  it("links http and https only, the bare addresses too", () => {
    expect(parseInline("[docs](https://x.test/a) e https://y.test/b.")).toEqual([
      { type: "link", href: "https://x.test/a", children: [text("docs")] },
      text(" e "),
      { type: "link", href: "https://y.test/b", children: [text("https://y.test/b")] },
      text("."),
    ]);
    expect(safeHref("javascript:alert(1)")).toBeNull();
    expect(safeHref("data:text/html,x")).toBeNull();
    expect(safeHref("https://ok.test")).toBe("https://ok.test/");
  });

  it("never links a script address", () => {
    const nodes = parseInline("[clique](javascript:alert(1))");
    expect(nodes.some((node) => node.type === "link")).toBe(false);
    expect(nodes.map((node) => (node.type === "text" ? node.text : "")).join("")).toContain(
      "clique",
    );
  });

  it("reads paragraphs, headings, rules, quotes and code", () => {
    expect(
      parseMarkdown('## Resumo\n\nTudo **ok**.\n\n---\n\n> atenção\n\n```json\n{"a": 1}\n```'),
    ).toEqual([
      { type: "heading", level: 2, children: [text("Resumo")] },
      {
        type: "paragraph",
        children: [text("Tudo "), { type: "strong", children: [text("ok")] }, text(".")],
      },
      { type: "hr" },
      { type: "quote", children: [{ type: "paragraph", children: [text("atenção")] }] },
      { type: "code", lang: "json", text: '{"a": 1}' },
    ]);
  });

  it("reads lists, ordered and nested, an item continued on the next line", () => {
    expect(parseMarkdown("1. vwap\n2. orb\n   - parado\n   - sem posição\ncontinua")).toEqual([
      {
        type: "list",
        ordered: true,
        start: 1,
        items: [
          [{ type: "paragraph", children: [text("vwap")] }],
          [
            { type: "paragraph", children: [text("orb")] },
            {
              type: "list",
              ordered: false,
              start: 1,
              items: [
                [{ type: "paragraph", children: [text("parado")] }],
                [
                  {
                    type: "paragraph",
                    children: [text("sem posição"), { type: "br" }, text("continua")],
                  },
                ],
              ],
            },
          ],
        ],
      },
    ]);
  });

  it("reads a table with its alignments, a row short of cells filled", () => {
    expect(
      parseMarkdown("| Agente | P/L |\n| :--- | ---: |\n| vwap | -41,98 BRL |\n| orb |"),
    ).toEqual([
      {
        type: "table",
        align: ["left", "right"],
        header: [[text("Agente")], [text("P/L")]],
        rows: [
          [[text("vwap")], [text("-41,98 BRL")]],
          [[text("orb")], []],
        ],
      },
    ]);
  });

  it("parses a text being written as far as it goes", () => {
    expect(parseMarkdown("Veja:\n\n```\nlinha")).toEqual([
      { type: "paragraph", children: [text("Veja:")] },
      { type: "code", lang: "", text: "linha" },
    ]);
    expect(parseMarkdown("| a | b |\n| --")).toEqual([
      { type: "table", align: [null, null], header: [[text("a")], [text("b")]], rows: [] },
    ]);
    expect(parseMarkdown("| a | b |")).toEqual([
      { type: "paragraph", children: [text("| a | b |")] },
    ]);
  });
});
