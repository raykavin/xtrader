import { useCallback, useEffect, useRef, useState } from "react";
import type { PageInfo } from "../api/types";
import { type ListParams, PAGE_SIZE } from "../lib/pagination";

/** How long the typing of a search waits before it is asked, in ms. */
export const SEARCH_DELAY = 300;

/** A page of a list as the API answers it: its rows, where they stand and more of the answer. */
export interface PagedRows<T, X = undefined> {
  rows: T[];
  page: PageInfo;
  extra?: X;
}

interface Options {
  /** The rows of a page, PAGE_SIZE by default. */
  size?: number;
  /** How often the page is read again, in ms; never by default. */
  poll?: number;
  /**
   * The filters and the sort of the list, as a key, eg: "ratio:desc": a
   * change of it goes back to the first page.
   */
  filters?: string;
  /** Whether to read the list at all, eg: the user may see it. */
  enabled?: boolean;
}

/**
 * A list read a page at a time from the API: the page asked (from 1), its
 * search (asked SEARCH_DELAY after the typing stops, and the first page with
 * it, as with a change of the filters), the page the API answered (the last
 * one for a page past it, eg: its last row deleted), read again by reload,
 * eg: after an action, and every poll ms when set. The rows shown are kept
 * while the next ones load; a request outdated is canceled.
 */
export function usePagedList<T, X = undefined>(
  read: (params: ListParams, signal: AbortSignal) => Promise<PagedRows<T, X>>,
  { size = PAGE_SIZE, poll, filters = "", enabled = true }: Options = {},
) {
  const [page, setPage] = useState(1);
  const [query, setQuery] = useState("");
  const [search, setSearch] = useState("");
  const [data, setData] = useState<PagedRows<T, X> | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [version, setVersion] = useState(0);
  const latest = useRef(read);
  latest.current = read;
  // the page the API just answered in place of the one asked: not asked again
  const answered = useRef<number | null>(null);

  // the search typed, asked once the typing stops (cleared at once), from its first page
  useEffect(() => {
    const wanted = query.trim();
    if (wanted === search) return;
    const timer = window.setTimeout(
      () => {
        setSearch(wanted);
        setPage(1);
      },
      wanted === "" ? 0 : SEARCH_DELAY,
    );
    return () => window.clearTimeout(timer);
  }, [query, search]);

  // the filters or the sort changed: the first page
  const shownFilters = useRef(filters);
  useEffect(() => {
    if (shownFilters.current === filters) return;
    shownFilters.current = filters;
    setPage(1);
  }, [filters]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: version and filters read the page again
  useEffect(() => {
    if (!enabled) return;
    if (answered.current === page) {
      answered.current = null;
      return;
    }
    const controller = new AbortController();
    setLoading(true);
    latest
      .current({ page, perPage: size, q: search }, controller.signal)
      .then((result) => {
        if (controller.signal.aborted) return;
        setData(result);
        setError(null);
        if (result.page.page !== page && result.page.page >= 1) {
          answered.current = result.page.page;
          setPage(result.page.page);
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [page, size, search, filters, version, enabled]);

  useEffect(() => {
    if (!poll || !enabled) return;
    const timer = window.setInterval(() => setVersion((v) => v + 1), poll);
    return () => window.clearInterval(timer);
  }, [poll, enabled]);

  return {
    /** The rows of the page, null until the first one is read. */
    rows: data?.rows ?? null,
    /** Where the page stands, null until the first one is read. */
    info: data?.page ?? null,
    /** More of the answer of the page, eg: what the filters hid. */
    extra: data?.extra,
    page,
    setPage,
    /** The search as typed. */
    query,
    setQuery,
    /** The search the rows answer, once asked. */
    search,
    error,
    loading,
    reload: useCallback(() => setVersion((v) => v + 1), []),
  };
}
