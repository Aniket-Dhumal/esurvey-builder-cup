"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Loader2, Search, X } from "lucide-react";

import { api } from "@/lib/api";
import type { Jurisdiction } from "@/lib/types";

/**
 * Choosing the places an officer is posted to.
 *
 * # What this replaces
 *
 * A text box asking for an LGD code. It required a Tehsildar to know village
 * 562111 by heart, gave no signal that 562112 was a different village until
 * the form was submitted, and — because a wrong code is nearly always another
 * real place — could silently post a Talathi to somebody else's village. There
 * is no error state for that: the account works, and the officer simply cannot
 * see the records they were posted to handle.
 *
 * # Why several
 *
 * Because a Talathi holds several villages, and a Krushi Sahayak more. The
 * data model has always been able to say so — a posting is a row, and there
 * may be many — and the form could say only one, which is how two officers end
 * up sharing one login.
 *
 * # Why the search is server-side
 *
 * The largest taluka in Maharashtra has 327 villages, and a district has
 * thousands. Fetching them to filter in the browser would work in Barshi and
 * fail in Pune.
 */

export interface Posting {
  jurisdiction_level: string;
  jurisdiction_code: string;
  /** For display only; the server resolves by level and code. */
  name?: string;
}

/** The level each role is posted at. A placeless role returns "". */
export const POSTED_AT: Record<string, string> = {
  state_officer: "state",
  district_magistrate: "district",
  tehsildar: "sub_district",
  mandal_officer: "circle",
  talathi: "village",
  krushi_sahayak: "village",
};

const LEVEL_NOUN: Record<string, string> = {
  state: "state",
  district: "district",
  sub_district: "taluka",
  circle: "circle",
  village: "village",
};

export function levelNoun(level: string): string {
  return LEVEL_NOUN[level] ?? level;
}

export function PostingPicker({
  level,
  value,
  onChange,
  /** Restricts the search to one subtree. Empty searches everywhere the
   *  caller is allowed to see, which the server already scopes. */
  disabled,
}: {
  level: string;
  value: Posting[];
  onChange: (next: Posting[]) => void;
  disabled?: boolean;
}) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<Jurisdiction[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);

  const chosen = useMemo(
    () => new Set(value.map((p) => p.jurisdiction_code)),
    [value],
  );

  // Changing the designation changes the level a posting is valid at, so
  // whatever was chosen for the old one no longer means anything. Cleared by
  // the form rather than silently carried, because a village left selected
  // under "Tehsildar" would be submitted and refused.
  const search = useCallback(
    async (q: string) => {
      if (!level) return;
      setLoading(true);
      try {
        const r = await api.listJurisdictions({
          level: level as "village",
          q: q.trim() || undefined,
          limit: 25,
        });
        setResults(r.items ?? r.jurisdictions ?? []);
      } catch {
        setResults([]);
      } finally {
        setLoading(false);
      }
    },
    [level],
  );

  // Debounced: a Tehsildar typing "Male" should not issue four queries, and
  // the last one is the only answer that matters.
  useEffect(() => {
    if (!open) return;
    const t = setTimeout(() => void search(query), 220);
    return () => clearTimeout(t);
  }, [query, open, search]);

  // Close on a click outside. Without it the panel stays over the rest of the
  // form and the next field cannot be reached.
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  function toggle(j: Jurisdiction) {
    if (chosen.has(j.code)) {
      onChange(value.filter((p) => p.jurisdiction_code !== j.code));
      return;
    }
    onChange([
      ...value,
      { jurisdiction_level: level, jurisdiction_code: j.code, name: j.name },
    ]);
  }

  if (!level) {
    return (
      <p className="rounded-lg border border-border bg-muted/40 px-3 py-2.5 text-sm text-muted-foreground">
        A system administrator is not posted to a place. They manage accounts
        and read the audit log, and hold nothing over a claim.
      </p>
    );
  }

  return (
    <div ref={box} className="relative">
      {/* What is chosen, above the search, so an officer adding a fourth
          village can see the three already on the form. */}
      {value.length > 0 && (
        <div className="mb-2 flex flex-wrap gap-1.5">
          {value.map((p) => (
            <button
              key={p.jurisdiction_code}
              type="button"
              disabled={disabled}
              onClick={() =>
                onChange(
                  value.filter(
                    (x) => x.jurisdiction_code !== p.jurisdiction_code,
                  ),
                )
              }
              className="inline-flex h-7 items-center gap-1.5 rounded-full bg-primary px-3 text-xs font-medium text-primary-foreground disabled:opacity-50">
              {p.name ?? p.jurisdiction_code}
              <span className="mono opacity-70">{p.jurisdiction_code}</span>
              <X className="size-3" />
            </button>
          ))}
        </div>
      )}

      <div className="relative">
        <Search
          className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-faint"
          aria-hidden
        />
        <input
          value={query}
          disabled={disabled}
          onChange={(e) => setQuery(e.target.value)}
          onFocus={() => setOpen(true)}
          placeholder={`Search ${levelNoun(level)}s by name or code`}
          aria-label={`Search ${levelNoun(level)}s`}
          className="h-9 w-full rounded-lg border border-input bg-background pl-9 pr-3 text-sm outline-none placeholder:text-faint focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:opacity-50"
        />
      </div>

      {/* In the flow, not floating over it.
          An absolutely positioned panel is the usual shape for this, and it
          was clipped by the dialog's own `overflow-y-auto` -- the results
          appeared, ran into the bottom edge of the modal and were cut in
          half. Rendering it in the flow makes the dialog scroll to it
          instead, which is the behaviour somebody reaching for a village
          three rows down actually needs. */}
      {open && (
        <div className="mt-1 max-h-60 w-full overflow-y-auto rounded-lg border border-border bg-card">
          {loading && (
            <p className="flex items-center gap-2 px-3 py-3 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" /> Searching…
            </p>
          )}
          {!loading && results !== null && results.length === 0 && (
            <p className="px-3 py-3 text-sm text-muted-foreground">
              No {levelNoun(level)} matches “{query}”.
            </p>
          )}
          {!loading &&
            (results ?? []).map((j) => (
              <button
                key={j.id}
                type="button"
                onClick={() => toggle(j)}
                className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-muted">
                <input
                  type="checkbox"
                  readOnly
                  checked={chosen.has(j.code)}
                  className="size-4 accent-primary"
                  tabIndex={-1}
                />
                <span className="flex-1 truncate">{j.name}</span>
                <span className="mono text-xs text-faint">{j.code}</span>
              </button>
            ))}
        </div>
      )}

      <p className="mt-1.5 text-xs text-faint">
        {level === "village" || level === "circle"
          ? `Several ${levelNoun(level)}s may be chosen — additional charge is routine.`
          : `One ${levelNoun(level)} is usual, but several may be chosen.`}
      </p>
    </div>
  );
}
