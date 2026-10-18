"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { ChevronRight, Loader2, Search, X } from "lucide-react";

import { api } from "@/lib/api";
import type { Jurisdiction } from "@/lib/types";

/**
 * Choosing the talukas and circles a survey event covers.
 *
 * # What replaced what
 *
 * A text box taking "4246, 4247". It asked a Collector to know LGD codes by
 * heart, gave no signal when one was mistyped until the form was submitted,
 * and — because a wrong code is usually another real place — could declare a
 * calamity over the wrong taluka without anything looking odd.
 *
 * # Why selecting a taluka does not expand into its circles
 *
 * It does not need to. Coverage is resolved through the jurisdiction tree, so
 * one row naming a taluka already covers every circle and village beneath it.
 * Storing the children instead would be the same statement, longer, and would
 * go stale the moment a circle was added.
 *
 * The expansion happens only when it has to: unticking one circle under a
 * chosen taluka. "This taluka except Vairag" cannot be said with a taluka row,
 * so at that moment the selection becomes the remaining circles, named
 * individually. That is the one case where the longer form carries information
 * the shorter one cannot.
 *
 * # Why most talukas will not open
 *
 * A circle is a revenue unit and LGD assigns it no code, so the village-to-
 * circle mapping is not in any national directory — see reference_data. One
 * circle is loaded for the whole state. Until a district's circles are
 * recorded, a taluka has nothing to expand into, and the picker says so rather
 * than offering an arrow that opens onto nothing.
 */

export interface AreaSelection {
  /** The codes to submit: taluka codes, and circle codes where narrowed. */
  codes: string[];
}

type Node = Jurisdiction;

export function AreaPicker({
  districtId,
  value,
  onChange,
}: {
  districtId: string;
  value: string[];
  onChange: (codes: string[]) => void;
}) {
  const [talukas, setTalukas] = useState<Node[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  // Circles per taluka, fetched on first expand. Undefined means "not asked
  // yet", an empty array means "asked, and this taluka has none recorded" --
  // and those must not look alike, or a taluka with no circles would show a
  // spinner for ever.
  const [circles, setCircles] = useState<Record<string, Node[]>>({});
  const [loadingCircles, setLoadingCircles] = useState<Set<string>>(new Set());
  const [open, setOpen] = useState<Set<string>>(new Set());

  useEffect(() => {
    if (!districtId) return;
    let live = true;
    api
      .listJurisdictions({ parent_id: districtId, level: "sub_district", limit: 200 })
      .then((r) => live && setTalukas(r.items))
      .catch((e) => live && setError(e instanceof Error ? e.message : "Could not load talukas."));
    return () => {
      live = false;
    };
  }, [districtId]);

  const selected = useMemo(() => new Set(value), [value]);

  const loadCircles = useCallback(
    async (t: Node) => {
      if (circles[t.id] !== undefined) return circles[t.id];
      setLoadingCircles((s) => new Set(s).add(t.id));
      try {
        const r = await api.listJurisdictions({
          parent_id: t.id,
          level: "circle",
          limit: 200,
        });
        setCircles((c) => ({ ...c, [t.id]: r.items }));
        return r.items;
      } catch {
        setCircles((c) => ({ ...c, [t.id]: [] }));
        return [];
      } finally {
        setLoadingCircles((s) => {
          const n = new Set(s);
          n.delete(t.id);
          return n;
        });
      }
    },
    [circles],
  );

  async function toggleOpen(t: Node) {
    setOpen((s) => {
      const n = new Set(s);
      if (n.has(t.id)) n.delete(t.id);
      else n.add(t.id);
      return n;
    });
    await loadCircles(t);
  }

  /** A taluka is on when its own code is chosen, or any of its circles are. */
  function stateOf(t: Node): "on" | "partial" | "off" {
    if (selected.has(t.code)) return "on";
    const kids = circles[t.id] ?? [];
    const chosen = kids.filter((c) => selected.has(c.code)).length;
    if (chosen === 0) return "off";
    return chosen === kids.length && kids.length > 0 ? "on" : "partial";
  }

  function toggleTaluka(t: Node) {
    const kids = circles[t.id] ?? [];
    const next = value.filter((c) => c !== t.code && !kids.some((k) => k.code === c));
    if (stateOf(t) === "off") next.push(t.code);
    onChange(next);
  }

  async function toggleCircle(t: Node, circle: Node) {
    const kids = (await loadCircles(t)) ?? [];

    // The taluka is currently chosen as a whole. Unticking one circle means
    // "all of it except this", which a taluka row cannot say — so the
    // selection becomes the siblings, named individually.
    if (selected.has(t.code)) {
      const rest = kids.filter((k) => k.code !== circle.code).map((k) => k.code);
      onChange([...value.filter((c) => c !== t.code), ...rest]);
      return;
    }

    const next = selected.has(circle.code)
      ? value.filter((c) => c !== circle.code)
      : [...value, circle.code];

    // Every circle ticked is the taluka, said the short way.
    const chosen = kids.filter((k) => next.includes(k.code));
    if (kids.length > 0 && chosen.length === kids.length) {
      onChange([...next.filter((c) => !kids.some((k) => k.code === c)), t.code]);
      return;
    }
    onChange(next);
  }

  const shown = useMemo(() => {
    if (!talukas) return [];
    const q = query.trim().toLowerCase();
    if (!q) return talukas;
    return talukas.filter(
      (t) => t.name.toLowerCase().includes(q) || t.code.toLowerCase().includes(q),
    );
  }, [talukas, query]);

  if (error) {
    return <p className="text-sm text-sev-catastrophic">{error}</p>;
  }

  return (
    <div className="rounded-lg border border-border">
      {/* What is chosen, above the list, because a Collector scrolled to the
          bottom of eleven talukas should not have to scroll back to see it. */}
      <div className="flex flex-wrap items-center gap-1.5 border-b border-border px-3 py-2.5">
        {value.length === 0 ? (
          <span className="text-xs text-muted-foreground">
            Nothing chosen yet. An event must cover at least one taluka or circle.
          </span>
        ) : (
          value.map((code) => (
            <button
              key={code}
              type="button"
              onClick={() => onChange(value.filter((c) => c !== code))}
              className="inline-flex h-7 items-center gap-1 rounded-full bg-primary px-3 text-xs font-medium text-primary-foreground">
              {labelFor(code, talukas, circles)}
              <X className="size-3" />
            </button>
          ))
        )}
      </div>

      <div className="relative border-b border-border">
        <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search talukas by name or LGD code"
          aria-label="Search talukas"
          className="w-full bg-transparent py-2.5 pl-9 pr-3 text-sm outline-none"
      />
      </div>

      <div className="max-h-72 overflow-y-auto">
        {talukas === null ? (
          <p className="flex items-center gap-2 px-3 py-4 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" /> Loading talukas…
          </p>
        ) : shown.length === 0 ? (
          <p className="px-3 py-4 text-sm text-muted-foreground">
            No taluka in your district matches “{query}”.
          </p>
        ) : (
          shown.map((t) => {
            const st = stateOf(t);
            const kids = circles[t.id];
            const isOpen = open.has(t.id);
            return (
              <div key={t.id} className="border-b border-border/50 last:border-0">
                <div className="flex items-center gap-2 px-3 py-2">
                  <button
                    type="button"
                    onClick={() => toggleOpen(t)}
                    aria-label={`Show circles in ${t.name}`}
                    aria-expanded={isOpen}
                    className="grid size-5 place-items-center rounded text-muted-foreground hover:text-foreground">
                    <ChevronRight
                      className={`size-4 transition-transform ${isOpen ? "rotate-90" : ""}`}
                  />
                  </button>

                  <label className="flex flex-1 cursor-pointer items-center gap-2.5">
                    <input
                      type="checkbox"
                      checked={st === "on"}
                      ref={(el) => {
                        if (el) el.indeterminate = st === "partial";
                      }}
                      onChange={() => toggleTaluka(t)}
                      className="size-4 accent-primary"
                  />
                    <span className="text-sm text-foreground">{t.name}</span>
                    <span className="mono text-xs text-muted-foreground">{t.code}</span>
                    {st === "partial" && (
                      <span className="text-xs text-muted-foreground">
                        some circles
                      </span>
                    )}
                  </label>
                </div>

                {isOpen && (
                  <div className="pb-2 pl-11 pr-3">
                    {loadingCircles.has(t.id) || kids === undefined ? (
                      <p className="py-1 text-xs text-muted-foreground">Loading…</p>
                    ) : kids.length === 0 ? (
                      <p className="py-1 text-xs leading-relaxed text-muted-foreground">
                        No circles are recorded for {t.name}. Choosing the taluka
                        covers all of its villages.
                      </p>
                    ) : (
                      kids.map((c) => (
                        <label
                          key={c.id}
                          className="flex cursor-pointer items-center gap-2.5 py-1">
                          <input
                            type="checkbox"
                            checked={selected.has(t.code) || selected.has(c.code)}
                            onChange={() => toggleCircle(t, c)}
                            className="size-4 accent-primary"
                        />
                          <span className="text-sm text-foreground">{c.name}</span>
                          <span className="mono text-xs text-muted-foreground">
                            {c.code}
                          </span>
                        </label>
                      ))
                    )}
                  </div>
                )}
              </div>
            );
          })
        )}
      </div>
    </div>
  );
}

/** The name behind a code, for the chips. Falls back to the code itself. */
function labelFor(
  code: string,
  talukas: Node[] | null,
  circles: Record<string, Node[]>,
): string {
  const t = talukas?.find((x) => x.code === code);
  if (t) return t.name;
  for (const list of Object.values(circles)) {
    const c = list.find((x) => x.code === code);
    if (c) return c.name;
  }
  return code;
}
