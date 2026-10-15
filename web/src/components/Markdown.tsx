import { type ReactNode, useMemo } from "react";
import { type Block, type Inline, parseMarkdown } from "../lib/markdown";

// The keys of the rendered nodes are their places: the tree is built anew
// from the text at each render, and a text being written only grows at its
// end, so a place is the identity of what is there.

/**
 * A Markdown text of the XTrader AI rendered as React elements (see
 * lib/markdown): never HTML, the links opened apart. A caret at the end of
 * the text tells one still being written.
 */
export function Markdown({ text, writing = false }: { text: string; writing?: boolean }) {
  const blocks = useMemo(() => parseMarkdown(text), [text]);
  return (
    <div className={writing ? "md writing" : "md"}>
      {blocks.map((block, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: a block is its place, see above
        <BlockNode key={i} block={block} />
      ))}
    </div>
  );
}

function BlockNode({ block }: { block: Block }): ReactNode {
  switch (block.type) {
    case "paragraph":
      return (
        <p>
          <Inlines nodes={block.children} />
        </p>
      );
    case "heading": {
      const Tag = block.level <= 1 ? "h3" : block.level === 2 ? "h4" : "h5";
      return (
        <Tag className="md-heading">
          <Inlines nodes={block.children} />
        </Tag>
      );
    }
    case "list": {
      const items = block.items.map((item, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: an item is its place, see above
        <li key={i}>
          {item.map((child, j) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: a block is its place, see above
            <BlockNode key={j} block={child} />
          ))}
        </li>
      ));
      return block.ordered ? (
        <ol start={block.start === 1 ? undefined : block.start}>{items}</ol>
      ) : (
        <ul>{items}</ul>
      );
    }
    case "code":
      return (
        <pre className="md-code">
          <code>{block.text}</code>
        </pre>
      );
    case "quote":
      return (
        <blockquote>
          {block.children.map((child, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: a block is its place, see above
            <BlockNode key={i} block={child} />
          ))}
        </blockquote>
      );
    case "table":
      return (
        <div className="md-table-wrap">
          <table className="md-table">
            <thead>
              <tr>
                {block.header.map((cell, i) => (
                  // biome-ignore lint/suspicious/noArrayIndexKey: a column is its place
                  <th key={i} style={{ textAlign: block.align[i] ?? undefined }}>
                    <Inlines nodes={cell} />
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {block.rows.map((row, i) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: a row is its place, see above
                <tr key={i}>
                  {row.map((cell, j) => (
                    // biome-ignore lint/suspicious/noArrayIndexKey: a column is its place
                    <td key={j} style={{ textAlign: block.align[j] ?? undefined }}>
                      <Inlines nodes={cell} />
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      );
    case "hr":
      return <hr />;
  }
}

function Inlines({ nodes }: { nodes: Inline[] }): ReactNode {
  return nodes.map((node, i) => (
    // biome-ignore lint/suspicious/noArrayIndexKey: a piece is its place, see above
    <InlineNode key={i} node={node} />
  ));
}

function InlineNode({ node }: { node: Inline }): ReactNode {
  switch (node.type) {
    case "text":
      return node.text;
    case "strong":
      return (
        <strong>
          <Inlines nodes={node.children} />
        </strong>
      );
    case "em":
      return (
        <em>
          <Inlines nodes={node.children} />
        </em>
      );
    case "del":
      return (
        <del>
          <Inlines nodes={node.children} />
        </del>
      );
    case "code":
      return <code className="md-inline-code">{node.text}</code>;
    case "link":
      return (
        <a href={node.href} target="_blank" rel="noopener noreferrer nofollow">
          <Inlines nodes={node.children} />
        </a>
      );
    case "br":
      return <br />;
  }
}
