"use client";

import { ChevronLeft, ChevronRight, Loader2, Search, X } from "lucide-react";
import type { PagedResource } from "@/lib/usePagedResource";
import { cn } from "@/lib/utils";

/**
 * The search box and pager that sit around every table.
 *
 * One component so that every table in the console behaves identically. An
 * officer who learns that typing narrows the reports list should not have to
 * discover whether the same is true of the audit trail.
 */

export function TableSearch({
  value,
  onChange,
  placeholder,
  busy,
  className,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
  busy?: boolean;
  className?: string;
}) {
  return (
    <div className={cn("relative w-full sm:w-80 sm:shrink-0", className)}>
      <Search
        className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-faint"
        aria-hidden
      />
      <input
        type="search"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        // The placeholder names the fields, so nothing has to be guessed —
        // "search" alone leaves an officer wondering whether a mobile number
        // will work.
        aria-label={placeholder}
        className="h-9 w-full rounded-md border border-border bg-background pl-8 pr-8 text-sm outline-none placeholder:text-faint focus-visible:ring-2 focus-visible:ring-ring"
      />
      {busy ? (
        <Loader2
          className="absolute right-2.5 top-1/2 size-4 -translate-y-1/2 animate-spin text-faint"
          aria-hidden
        />
      ) : (
        value !== "" && (
          <button
            type="button"
            onClick={() => onChange("")}
            aria-label="Clear search"
            className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-0.5 text-faint hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring">
            <X className="size-3.5" />
          </button>
        )
      )}
    </div>
  );
}

function pageWindow(page: number, pageCount: number): (number | "gap")[] {
  // At most seven slots, always including the first and last. A pager for 87
  // pages that renders 87 buttons is not a pager.
  if (pageCount <= 7) {
    return Array.from({ length: pageCount }, (_, i) => i + 1);
  }
  const out: (number | "gap")[] = [1];
  const from = Math.max(2, page - 1);
  const to = Math.min(pageCount - 1, page + 1);
  if (from > 2) out.push("gap");
  for (let i = from; i <= to; i++) out.push(i);
  if (to < pageCount - 1) out.push("gap");
  out.push(pageCount);
  return out;
}

/**
 * The pager, and the count it is paging through.
 *
 * The count is the point. "Showing 1–50 of 4,312" is what tells an officer
 * whether the thing they are looking for is likely on this screen or whether
 * they should be searching instead — which is a decision the console could
 * not previously help them make, because the total it had was the page size.
 */
export function TablePager<T>({
  resource,
  noun,
  className,
}: {
  resource: PagedResource<T>;
  /** Plural, lowercase: "reports", "officers". */
  noun: string;
  className?: string;
}) {
  const { page, pageCount, pageSize, offset, total, rows, setPage } = resource;

  // A capped count is a floor, not a total. "5,000+" is the honest rendering;
  // printing 5,000 would state as fact a number the server stopped counting
  // at, and an officer deciding whether to narrow their search would be
  // deciding on it.
  const totalText =
    total.toLocaleString("en-IN") + (resource.totalIsLowerBound ? "+" : "");

  if (total === 0) return null;

  const first = offset + 1;
  const last = offset + rows.length;
  const single = pageCount <= 1;

  return (
    <div
      className={cn(
        "flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-t px-4 py-3",
        className,
      )}>
      <p className="text-xs text-muted-foreground">
        {single ? (
          <>
            {totalText} {noun}
          </>
        ) : (
          <>
            Showing{" "}
            <span className="tabular-nums text-foreground">
              {first.toLocaleString("en-IN")}–{last.toLocaleString("en-IN")}
            </span>{" "}
            of{" "}
            <span className="tabular-nums text-foreground">{totalText}</span>{" "}
            {noun}
          </>
        )}
      </p>

      {!single && (
        <nav className="flex items-center gap-1" aria-label={`${noun} pages`}>
          <PagerButton
            onClick={() => setPage(page - 1)}
            disabled={page <= 1}
            label="Previous page">
            <ChevronLeft className="size-4" />
          </PagerButton>

          {pageWindow(page, pageCount).map((slot, i) =>
            slot === "gap" ? (
              <span key={`gap-${i}`} className="px-1 text-xs text-faint">
                …
              </span>
            ) : (
              <button
                key={slot}
                type="button"
                onClick={() => setPage(slot)}
                aria-current={slot === page ? "page" : undefined}
                className={cn(
                  "min-w-8 rounded-md px-2 py-1 text-xs tabular-nums focus-visible:ring-2 focus-visible:ring-ring",
                  slot === page
                    ? "bg-foreground font-medium text-background"
                    : "text-muted-foreground hover:bg-muted",
                )}>
                {slot}
              </button>
            ),
          )}

          <PagerButton
            onClick={() => setPage(page + 1)}
            disabled={page >= pageCount}
            label="Next page">
            <ChevronRight className="size-4" />
          </PagerButton>
        </nav>
      )}

      {/* Only shown where the page size is not the whole story. */}
      {!single && (
        <p className="sr-only">
          Page {page} of {pageCount}, {pageSize} per page
        </p>
      )}
    </div>
  );
}

function PagerButton({
  children,
  onClick,
  disabled,
  label,
}: {
  children: React.ReactNode;
  onClick: () => void;
  disabled: boolean;
  label: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      className="rounded-md p-1 text-muted-foreground hover:bg-muted disabled:pointer-events-none disabled:opacity-35 focus-visible:ring-2 focus-visible:ring-ring">
      {children}
    </button>
  );
}

/**
 * What to show when a search matched nothing.
 *
 * Distinct from an empty table, and the difference matters: "no reports have
 * been filed here" and "no reports match *dhumal*" are different facts, and
 * only the second one has an obvious next action.
 */
export function NoMatches({ query, noun }: { query: string; noun: string }) {
  return (
    <div className="px-4 py-10 text-center">
      <p className="text-sm text-foreground">
        No {noun} match <span className="font-medium">“{query}”</span>
      </p>
      <p className="mt-1 text-sm text-muted-foreground">
        Check the spelling, or try part of a name, village or reference number.
      </p>
    </div>
  );
}
