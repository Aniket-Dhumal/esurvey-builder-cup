"use client";

import { useCallback, useEffect, useState } from "react";
import type { ApiError } from "./api";

/**
 * Load something from the API, once per mount, with a retry.
 *
 * # Why this exists
 *
 * Every page in the console had written the same twenty lines: two pieces of
 * state, a `load` callback that clears the error and fires a request, a
 * `useEffect` to run it, and an `ErrorPanel` wired to run it again. Twenty
 * copies of a thing is twenty chances for one of them to be subtly different
 * — and they were: some cleared the error before refetching and some did not,
 * so a failed retry sometimes flashed the old message and sometimes did not.
 *
 * It also fixes a real defect the copies shared. Each `load` called
 * `setError(null)` synchronously, and the effect called `load` — a
 * synchronous state update during an effect, which makes React render twice
 * before paint on every page load. Here the only synchronous set happens in
 * `reload`, which runs from a click.
 *
 * # Why results are dropped after unmount
 *
 * A response that arrives after the screen has gone is discarded rather than
 * written into whatever replaced it.
 */
export interface Resource<T> {
  data: T | null;
  error: ApiError | null;
  /** True until the first attempt settles, either way. */
  loading: boolean;
  reload: () => void;
}

export function useResource<T>(
  fetcher: () => Promise<T>,
  deps: readonly unknown[] = [],
): Resource<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [loading, setLoading] = useState(true);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    // A closure flag rather than a ref. An officer moving between screens
    // faster than the network answers would otherwise get the previous
    // page's response written into the new page's state -- a bug that only
    // shows up on a slow connection, which is to say only in the field.
    let live = true;

    fetcher()
      .then((value) => {
        if (!live) return;
        setData(value);
        setError(null);
        setLoading(false);
      })
      .catch((e: unknown) => {
        if (!live) return;
        setError(e as ApiError);
        setLoading(false);
      });

    return () => {
      live = false;
    };
    // The deps array is the contract: callers pass an inline arrow, so
    // including `fetcher` would refetch on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [attempt, ...deps]);

  const reload = useCallback(() => {
    setLoading(true);
    setError(null);
    setAttempt((n) => n + 1);
  }, []);

  return { data, error, loading, reload };
}

/**
 * A clock that a component may read during render.
 *
 * `Date.now()` called while rendering is impure: the value is captured once
 * and then never changes, so a badge reading "2 days left" keeps saying that
 * on a screen left open overnight. Worse, it makes the render depend on when
 * it happened, which React is entitled to redo at any time.
 *
 * This ticks every minute, which is as often as anything measured in days
 * needs, and returns a stable value in between.
 */
export function useNow(intervalMs = 60_000): number {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);

  return now;
}
