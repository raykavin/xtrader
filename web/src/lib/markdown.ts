// The Markdown of the answers of the XTrader AI, parsed into a tree the
// dashboard renders as React elements: never HTML, so a text of the model is
// never markup, and its links are http and https only. The part of Markdown a
// model writes in a chat: paragraphs, headings, lists (nested too), tables,
// quotes, fenced code, rules, and in the text bold, italics, strikethrough,
// code, links and the bare addresses. A text being written (streamed) parses
// as far as it goes: an unclosed fence is code to the end, an unclosed ** is
// text until it closes.

/** A piece of the text of a block. */
export type Inline =
  | { type: "text"; text: string }
  | { type: "strong" | "em" | "del"; children: Inline[] }
  | { type: "code"; text: string }
  | { type: "link"; href: string; children: Inline[] }
  | { type: "br" };

/** The alignment of a column of a table. */
export type Align = "left" | "center" | "right" | null;

/** A block of the text. */
export type Block =
  | { type: "paragraph"; children: Inline[] }
  | { type: "heading"; level: number; children: Inline[] }
  | { type: "list"; ordered: boolean; start: number; items: Block[][] }
  | { type: "code"; lang: string; text: string }
  | { type: "quote"; children: Block[] }
  | { type: "table"; align: Align[]; header: Inline[][]; rows: Inline[][][] }
  | { type: "hr" };

const FENCE = /^ {0,3}(`{3,}|~{3,})\s*([^`\s]*)/;
const HEADING = /^ {0,3}(#{1,6})(?:\s+(.*?))?\s*#*\s*$/;
const RULE = /^ {0,3}([-*_])(?:\s*\1){2,}\s*$/;
const QUOTE = /^ {0,3}> ?/;
const ITEM = /^( *)([-*+]|\d{1,9}[.)])(?:\s+(.*))?$/;
const DELIMITER = /^\s*\|?\s*:?-+:?\s*(?:\|\s*:?-+:?\s*)*\|?\s*$/;

/** The blocks of a Markdown text. */
export function parseMarkdown(text: string): Block[] {
  return parseBlocks(text.replace(/\r\n?/g, "\n").replace(/\t/g, "    ").split("\n"));
}

/** The spaces at the start of a line. */
function indentOf(line: string): number {
  return line.length - line.trimStart().length;
}

function isBlank(line: string | undefined): boolean {
  return line === undefined || line.trim() === "";
}

/** Whether lines[i] starts a table: a row with a pipe, then its delimiter. */
function startsTable(lines: string[], i: number): boolean {
  const line = lines[i] ?? "";
  const next = lines[i + 1];
  return line.includes("|") && next?.includes("-") === true && DELIMITER.test(next);
}

/** Whether line starts a block other than a paragraph. */
function startsBlock(lines: string[], i: number): boolean {
  const line = lines[i] ?? "";
  return (
    FENCE.test(line) ||
    HEADING.test(line) ||
    RULE.test(line) ||
    QUOTE.test(line) ||
    ITEM.test(line) ||
    startsTable(lines, i)
  );
}

function parseBlocks(lines: string[]): Block[] {
  const blocks: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i] ?? "";
    if (isBlank(line)) {
      i++;
      continue;
    }
    const fence = FENCE.exec(line);
    if (fence) {
      const marker = fence[1] ?? "```";
      const body: string[] = [];
      i++;
      while (i < lines.length && !(lines[i] ?? "").trimStart().startsWith(marker)) {
        body.push(lines[i] ?? "");
        i++;
      }
      i++; // the closing fence, if any
      blocks.push({ type: "code", lang: fence[2] ?? "", text: body.join("\n") });
      continue;
    }
    const heading = HEADING.exec(line);
    if (heading) {
      blocks.push({
        type: "heading",
        level: heading[1]?.length ?? 1,
        children: parseInline(heading[2] ?? ""),
      });
      i++;
      continue;
    }
    if (RULE.test(line)) {
      blocks.push({ type: "hr" });
      i++;
      continue;
    }
    if (QUOTE.test(line)) {
      const quoted: string[] = [];
      while (i < lines.length && QUOTE.test(lines[i] ?? "")) {
        quoted.push((lines[i] ?? "").replace(QUOTE, ""));
        i++;
      }
      blocks.push({ type: "quote", children: parseBlocks(quoted) });
      continue;
    }
    if (startsTable(lines, i)) {
      const header = cells(line);
      const align = cells(lines[i + 1] ?? "").map(alignOf);
      const rows: Inline[][][] = [];
      i += 2;
      while (i < lines.length && !isBlank(lines[i]) && (lines[i] ?? "").includes("|")) {
        const row = cells(lines[i] ?? "");
        rows.push(header.map((_, column) => parseInline(row[column] ?? "")));
        i++;
      }
      blocks.push({
        type: "table",
        align: header.map((_, column) => align[column] ?? null),
        header: header.map((cell) => parseInline(cell)),
        rows,
      });
      continue;
    }
    if (ITEM.test(line)) {
      i = parseList(lines, i, blocks);
      continue;
    }
    const paragraph: string[] = [];
    while (
      i < lines.length &&
      !isBlank(lines[i]) &&
      (paragraph.length === 0 || !startsBlock(lines, i))
    ) {
      paragraph.push((lines[i] ?? "").trim());
      i++;
    }
    blocks.push({ type: "paragraph", children: parseInline(paragraph.join("\n")) });
  }
  return blocks;
}

/** Parses the list starting at lines[start] into blocks, returning where it ends. */
function parseList(lines: string[], start: number, blocks: Block[]): number {
  const first = ITEM.exec(lines[start] ?? "");
  const indent = first?.[1]?.length ?? 0;
  const ordered = /\d/.test(first?.[2] ?? "");
  const items: Block[][] = [];
  let i = start;
  while (i < lines.length) {
    const item = ITEM.exec(lines[i] ?? "");
    if (!item || (item[1]?.length ?? 0) !== indent || /\d/.test(item[2] ?? "") !== ordered) break;
    const offset = indent + (item[2]?.length ?? 1) + 1;
    const content = [item[3] ?? ""];
    i++;
    while (i < lines.length) {
      const line = lines[i] ?? "";
      if (isBlank(line)) {
        let next = i + 1;
        while (next < lines.length && isBlank(lines[next])) next++;
        if (next < lines.length && indentOf(lines[next] ?? "") > indent) {
          content.push("");
          i++;
          continue;
        }
        break;
      }
      if (indentOf(line) > indent) {
        content.push(line.slice(Math.min(indentOf(line), offset)));
        i++;
        continue;
      }
      // a line of the text of the item, not indented
      if (!startsBlock(lines, i) && !isBlank(content[content.length - 1])) {
        content.push(line.trim());
        i++;
        continue;
      }
      break;
    }
    items.push(parseBlocks(content));
  }
  const number = Number.parseInt(first?.[2] ?? "1", 10);
  blocks.push({ type: "list", ordered, start: Number.isFinite(number) ? number : 1, items });
  return i;
}

/** The cells of a row of a table, without its outer pipes. */
function cells(row: string): string[] {
  let text = row.trim();
  if (text.startsWith("|")) text = text.slice(1);
  if (text.endsWith("|") && !text.endsWith("\\|")) text = text.slice(0, -1);
  return text.split(/(?<!\\)\|/).map((cell) => cell.trim().replace(/\\\|/g, "|"));
}

/** The alignment of a cell of the delimiter of a table, eg: :---: center. */
function alignOf(cell: string): Align {
  const left = cell.startsWith(":");
  const right = cell.endsWith(":");
  if (left && right) return "center";
  if (right) return "right";
  if (left) return "left";
  return null;
}

/** The address of a link if it is one the dashboard opens: http or https. */
export function safeHref(href: string): string | null {
  try {
    const url = new URL(href);
    return url.protocol === "http:" || url.protocol === "https:" ? url.href : null;
  } catch {
    return null;
  }
}

// The pieces of the text, the first one matched at a place winning: an escape,
// code, a link, an address, bold, strikethrough, italics, a line break.
// Italics with _ only between words, never inside a name as block_entries.
// The groups are numbered across the whole: \2 of the code is its backticks.
const INLINE = new RegExp(
  [
    /\\([\\`*_{}[\]()#+\-.!~|>])/u.source,
    /(`+)([^`]|[^`][\s\S]*?[^`])\2(?!`)/u.source,
    /\[([^\]\n]+)\]\(\s*([^)\s]+)(?:\s+"[^"]*")?\s*\)/u.source,
    /(https?:\/\/[^\s<>()]*[^\s<>().,:;"'!?])/u.source,
    /\*\*(?=\S)([\s\S]*?\S)\*\*/u.source,
    /(?<![\p{L}\p{N}])__(?=\S)([\s\S]*?\S)__(?![\p{L}\p{N}])/u.source,
    /~~(?=\S)([\s\S]*?\S)~~/u.source,
    /\*(?=[^\s*])((?:[^*\n]|\*\*[^*\n]+\*\*)*?[^\s*])\*(?!\*)/u.source,
    /(?<![\p{L}\p{N}_])_(?=[^\s_])([^_\n]*?[^\s_])_(?![\p{L}\p{N}_])/u.source,
    /(\n)/u.source,
  ].join("|"),
  "gu",
);

/** The pieces of a text. */
export function parseInline(text: string): Inline[] {
  const nodes: Inline[] = [];
  const add = (node: Inline) => {
    const last = nodes[nodes.length - 1];
    if (node.type === "text" && last?.type === "text") last.text += node.text;
    else nodes.push(node);
  };
  let at = 0;
  for (const match of text.matchAll(INLINE)) {
    const index = match.index ?? 0;
    if (index > at) add({ type: "text", text: text.slice(at, index) });
    at = index + match[0].length;
    const [
      ,
      escaped,
      ,
      code,
      label,
      href,
      address,
      strong,
      strongUnderscore,
      del,
      em,
      emUnderscore,
      lineBreak,
    ] = match;
    if (escaped !== undefined) add({ type: "text", text: escaped });
    else if (code !== undefined) add({ type: "code", text: code.trim() || code });
    else if (label !== undefined && href !== undefined) {
      const safe = safeHref(href);
      if (safe) add({ type: "link", href: safe, children: parseInline(label) });
      else add({ type: "text", text: label });
    } else if (address !== undefined) {
      const safe = safeHref(address);
      add(
        safe
          ? { type: "link", href: safe, children: [{ type: "text", text: address }] }
          : { type: "text", text: address },
      );
    } else if (strong !== undefined || strongUnderscore !== undefined)
      add({ type: "strong", children: parseInline(strong ?? strongUnderscore ?? "") });
    else if (del !== undefined) add({ type: "del", children: parseInline(del) });
    else if (em !== undefined || emUnderscore !== undefined)
      add({ type: "em", children: parseInline(em ?? emUnderscore ?? "") });
    else if (lineBreak !== undefined) add({ type: "br" });
  }
  if (at < text.length) add({ type: "text", text: text.slice(at) });
  return nodes;
}
