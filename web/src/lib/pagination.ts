// The pages of the lists of the dashboard, eg: the orders and the decisions of
// the language model: PAGE_SIZE rows each, the size fixed.

/** The rows of a page. */
export const PAGE_SIZE = 10;

/** The pages of total rows, one at least. */
export function pageCount(total: number, size = PAGE_SIZE): number {
  return Math.max(1, Math.ceil(total / size));
}

/** The page shown, from 0: page kept within the pages of total rows. */
export function clampPage(page: number, total: number, size = PAGE_SIZE): number {
  return Math.min(Math.max(0, page), pageCount(total, size) - 1);
}

/** The rows of page, from 0. */
export function pageRows<T>(rows: T[], page: number, size = PAGE_SIZE): T[] {
  const start = clampPage(page, rows.length, size) * size;
  return rows.slice(start, start + size);
}

/**
 * The pages offered around page, from 0, of count pages: all of them when they
 * are few, else the first, the last and the ones next to page, a gap of pages
 * as null (an ellipsis), eg: 0 null 4 5 6 null 11.
 */
export function pageList(page: number, count: number): (number | null)[] {
  if (count <= 7) return Array.from({ length: count }, (_, i) => i);
  const shown = [...new Set([0, page - 1, page, page + 1, count - 1])]
    .filter((p) => p >= 0 && p < count)
    .sort((a, b) => a - b);
  const list: (number | null)[] = [];
  for (const [i, p] of shown.entries()) {
    const before = shown[i - 1];
    if (before !== undefined && p - before === 2)
      list.push(before + 1); // a gap of one page: the page
    else if (before !== undefined && p - before > 2) list.push(null);
    list.push(p);
  }
  return list;
}
