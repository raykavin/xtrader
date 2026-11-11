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

// The lists paged by the API (see PageInfo): a page asked with its search,
// the controller reading only its rows.

/** A page of a list asked of the API: from 1, its size and its search. */
export interface ListParams {
  page: number;
  perPage: number;
  q?: string;
}

/** A value of a query string, left out when undefined or blank. */
type QueryValue = string | number | boolean | undefined;

/**
 * The query string of the page of params and of more (eg: a filter, a sort),
 * eg: ?page=2&per_page=10&q=ouro, the blank ones left out.
 */
export function listQuery(params: ListParams, more: Record<string, QueryValue> = {}): string {
  const query = new URLSearchParams({
    page: String(params.page),
    per_page: String(params.perPage),
  });
  const q = params.q?.trim();
  if (q) query.set("q", q);
  for (const [key, value] of Object.entries(more)) {
    if (value !== undefined && value !== "") query.set(key, String(value));
  }
  return `?${query.toString()}`;
}

/** What a list holds, in words: one, many, and whether the word is feminine. */
export interface Noun {
  one: string;
  many: string;
  feminine?: boolean;
}

/**
 * The note of a search: the rows found of every page, or none for query, eg:
 * "3 contas encontradas.", "Nenhum prompt encontrado para “ouro”."; null
 * without a search.
 */
export function searchNote(total: number, query: string, noun: Noun): string | null {
  const q = query.trim();
  if (q === "") return null;
  const found = noun.feminine ? "encontrada" : "encontrado";
  if (total === 0)
    return `${noun.feminine ? "Nenhuma" : "Nenhum"} ${noun.one} ${found} para “${q}”.`;
  if (total === 1) return `1 ${noun.one} ${found}.`;
  return `${total} ${noun.many} ${found}s.`;
}
