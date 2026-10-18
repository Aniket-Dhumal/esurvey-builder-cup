"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ApiError } from "./api";
import type { Paged } from "./types";

/**
 * A table's worth of rows: one page, a search box, and a true total.
 *
 * # Why the search is debounced and the page is not
 *
 * Typing is continuous and clicking is not. Firing a request per keystroke
 * would send five queries to answer one question and, worse, let an early
 * response land after a later one — so the officer types "barshi" and sees
 * the results for "bars". The debounce collapses the burst; the generation
 * check makes a late answer a no-op regardless.
 *
 * # Why editing the search resets to the first page
 *
 * An officer on page four who narrows the search is asking a new question.
 * Keeping the offset would show them page four of a three-page answer, which
 * renders as "no results" for a search that plainly has some — the kind of
 * empty state people read as a broken feature.
 */
export interface PagedResource<T, R = Paged<T>> {
  rows: T[];
  total: number;
  /** True when `total` is a floor the server stopped counting at. */
  totalIsLowerBound: boolean;
  /**
   * The whole last response.
   *
   * Some listings carry a figure alongside the page -- the worklist's overdue
   * count is over the entire list, not the fifty rows on screen -- and that
   * figure would be lost if the hook kept only the rows.
   */
  last: R | null;
  /** True until the first page settles. Distinct from `searching`. */
  loading: boolean;
  /** True while a new page or search is in flight, with rows still on screen. */
  searching: boolean;
  error: ApiError | null;

  query: string;
  setQuery: (q: string) => void;

  page: number;
  pageCount: number;
  pageSize: number;
  offset: number;
  setPage: (n: number) => void;

  hasMore: boolean;
  reload: () => void;
}

export const DEFAULT_PAGE_SIZE = 50;
const DEBOUNCE_MS = 250;

export function usePagedResource<T, R extends Paged<T> = Paged<T>>(
  fetcher: (args: { limit: number; offset: number; q: string }) => Promise<R>,
  opts: { pageSize?: number; deps?: readonly unknown[] } = {},
): PagedResource<T, R> {
  const pageSize = opts.pageSize ?? DEFAULT_PAGE_SIZE;
  const deps = opts.deps ?? [];

  const [rows, setRows] = useState<T[]>([]);
  const [last, setLast] = useState<R | null>(null);
  const [total, setTotal] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [loading, setLoading] = useState(true);

  // "Searching" is derived, not set. Setting it at the top of the effect is a
  // synchronous state update during an effect, which makes every page render
  // twice before paint -- the same defect the per-page fetch code had.
  const [settled, setSettled] = useState<string | null>(null);

  const [query, setQueryRaw] = useState("");
  const [debounced, setDebounced] = useState("");
  const [offset, setOffset] = useState(0);
  const [attempt, setAttempt] = useState(0);

  // Collapse a burst of keystrokes into one request.
  useEffect(() => {
    if (query === debounced) return;
    const id = setTimeout(() => setDebounced(query), DEBOUNCE_MS);
    return () => clearTimeout(id);
  }, [query, debounced]);

  const fetcherRef = useRef(fetcher);
  useEffect(() => {
    fetcherRef.current = fetcher;
  });

  const key = JSON.stringify([pageSize, offset, debounced, attempt, deps]);
  const searching = settled !== key;

  useEffect(() => {
    let live = true;

    fetcherRef
      .current({ limit: pageSize, offset, q: debounced })
      .then((r) => {
        if (!live) return;
        setRows(r.items ?? []);
        setLast(r);
        setTotal(r.total ?? 0);
        setHasMore(Boolean(r.has_more));
        setError(null);
        setLoading(false);
        setSettled(key);
      })
      .catch((e: unknown) => {
        if (!live) return;
        setError(e as ApiError);
        setLoading(false);
        setSettled(key);
      });

    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pageSize, offset, debounced, attempt, ...deps]);

  // A changed filter is a new question, same as a changed search.
  const depsKey = JSON.stringify(deps);
  const firstDeps = useRef(depsKey);
  useEffect(() => {
    if (firstDeps.current === depsKey) return;
    firstDeps.current = depsKey;
    setOffset(0);
  }, [depsKey]);

  const setQuery = useCallback((q: string) => {
    setQueryRaw(q);
    setOffset(0);
  }, []);

  const setPage = useCallback(
    (n: number) => setOffset(Math.max(0, n - 1) * pageSize),
    [pageSize],
  );

  const reload = useCallback(() => {
    setError(null);
    setAttempt((n) => n + 1);
  }, []);

  const pageCount = useMemo(
    () => Math.max(1, Math.ceil(total / pageSize)),
    [total, pageSize],
  );

  return {
    rows,
    last,
    total,
    totalIsLowerBound: Boolean(
      (last as { total_is_lower_bound?: boolean } | null)?.total_is_lower_bound,
    ),
    loading,
    searching,
    error,
    query,
    setQuery,
    page: Math.floor(offset / pageSize) + 1,
    pageCount,
    pageSize,
    offset,
    setPage,
    hasMore,
    reload,
  };
}
