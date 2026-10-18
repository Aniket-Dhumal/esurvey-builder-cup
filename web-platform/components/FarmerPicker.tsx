"use client";

import { useEffect, useId, useRef, useState } from "react";
import { ChevronDown, Search, X } from "lucide-react";

import { api, ApiError } from "@/lib/api";
import type { FarmerSummary } from "@/lib/types";
import { cn } from "@/lib/utils";

/**
 * Choosing one farmer out of however many this officer can reach.
 *
 * # Why not a dropdown
 *
 * It was one: the first two hundred farmer ids, listed as bare numbers. That
 * works for a trial directory of one record and fails for a village. Malegaon
 * alone is 613 farmers, so the list was both truncated -- the farmer an
 * officer wanted was frequently not in it -- and unreadable, because an
 * eleven-digit AgriStack id says nothing about who it belongs to.
 *
 * # What it shows, and what it searches
 *
 * The name first, since that is what an officer has been told, with the id and
 * the village under it, since those are what disambiguate two people called
 * Pawar. The search runs on the server, over the same fields: name, id,
 * village and mobile. Scoping is the server's too -- this asks for farmers and
 * gets the ones inside the officer's own posting.
 */
export function FarmerPicker({
  id,
  value,
  onChange,
  disabled,
}: {
  id?: string;
  value: string;
  onChange: (farmerId: string) => void;
  disabled?: boolean;
}) {
  const listId = useId();
  const box = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);

  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<FarmerSummary[]>([]);
  const [chosen, setChosen] = useState<FarmerSummary | null>(null);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [active, setActive] = useState(0);

  // The search, debounced. A keystroke per request would put 613 rows through
  // a village connection several times a second.
  useEffect(() => {
    if (!open) return;
    let live = true;
    // The spinner is raised inside the timer rather than on the way into the
    // effect: setting state as an effect begins is a second render before the
    // first is on screen, and this one runs on every keystroke.
    const t = window.setTimeout(() => {
      if (!live) return;
      setLoading(true);
      api
        .listFarmers({ q: query.trim() || undefined, limit: 20 })
        .then((r) => {
          if (!live) return;
          setResults(r.items ?? []);
          setTotal(r.total ?? 0);
          setActive(0);
          setError(null);
        })
        .catch((e) => live && setError(e as ApiError))
        .finally(() => live && setLoading(false));
    }, 250);

    return () => {
      live = false;
      window.clearTimeout(t);
    };
  }, [query, open]);

  // Clicking away is a decision not to choose, and leaves what was chosen.
  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (!box.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  }, [open]);

  const choose = (f: FarmerSummary) => {
    setChosen(f);
    setQuery("");
    setOpen(false);
    onChange(f.farmer_id);
  };

  const clear = () => {
    setChosen(null);
    setQuery("");
    onChange("");
    input.current?.focus();
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!open) return setOpen(true);
      setActive((i) => {
        const next = e.key === "ArrowDown" ? i + 1 : i - 1;
        return Math.max(0, Math.min(results.length - 1, next));
      });
      return;
    }
    if (e.key === "Enter" && open && results[active]) {
      e.preventDefault();
      choose(results[active]);
      return;
    }
    if (e.key === "Escape" && open) {
      e.preventDefault();
      setOpen(false);
    }
  };

  // What the closed field reads. A chosen farmer shows as themselves; a value
  // arriving from elsewhere shows as the id it is, rather than as nothing.
  const label = chosen
    ? `${chosen.farmer_name} · ${chosen.farmer_id}`
    : value || "";

  return (
    <div ref={box} className="relative">
      <div
        className={cn(
          "flex h-10 items-center gap-2 rounded-md border border-input bg-transparent px-3",
          "focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/50",
          disabled && "pointer-events-none opacity-50",
        )}
      >
        <Search className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <input
          id={id}
          ref={input}
          role="combobox"
          aria-expanded={open}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={
            open && results[active] ? `${listId}-${active}` : undefined
          }
          autoComplete="off"
          disabled={disabled}
          className="w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
          placeholder={chosen ? "" : "Search by name, id, village or mobile…"}
          value={open ? query : label}
          onFocus={() => setOpen(true)}
          onChange={(e) => {
            setQuery(e.target.value);
            setOpen(true);
          }}
          onKeyDown={onKeyDown}
        />
        {value ? (
          <button
            type="button"
            onClick={clear}
            aria-label="Clear the chosen farmer"
            className="grid size-5 shrink-0 place-items-center rounded text-muted-foreground hover:text-foreground"
          >
            <X className="size-4" />
          </button>
        ) : (
          <ChevronDown
            className="size-4 shrink-0 text-muted-foreground"
            aria-hidden
          />
        )}
      </div>

      {open && (
        <div className="absolute z-50 mt-1 w-full overflow-hidden rounded-md border bg-card shadow-lg">
          <ul
            id={listId}
            role="listbox"
            aria-label="Farmers"
            className="max-h-72 overflow-y-auto py-1"
          >
            {results.map((f, i) => (
              <li
                key={f.farmer_id}
                id={`${listId}-${i}`}
                role="option"
                aria-selected={f.farmer_id === value}
                onMouseEnter={() => setActive(i)}
                onMouseDown={(e) => {
                  // Before blur, so the click is not lost to the field
                  // closing under it.
                  e.preventDefault();
                  choose(f);
                }}
                className={cn(
                  "cursor-pointer px-3 py-2 text-sm",
                  i === active ? "bg-muted" : "",
                )}
              >
                <div className="flex items-baseline justify-between gap-3">
                  <span className="truncate font-medium">{f.farmer_name}</span>
                  <span className="mono shrink-0 text-xs text-muted-foreground">
                    {f.farmer_id}
                  </span>
                </div>
                <div className="mt-0.5 text-xs text-muted-foreground">
                  Village {f.village_lgd_code} · {f.parcels} parcel
                  {f.parcels === 1 ? "" : "s"}
                  {f.parcels_with_boundary < f.parcels && (
                    // The reason a parcel cannot be assessed, said before the
                    // officer picks the farmer and finds out.
                    <span className="text-sev-moderate">
                      {" "}
                      · {f.parcels - f.parcels_with_boundary} unmapped
                    </span>
                  )}
                </div>
              </li>
            ))}

            {!loading && results.length === 0 && (
              <li className="px-3 py-3 text-sm text-muted-foreground">
                {error
                  ? error.message
                  : query
                    ? `No farmer in your area matches “${query}”.`
                    : "No farmers are loaded for your area."}
              </li>
            )}
            {loading && results.length === 0 && (
              <li className="px-3 py-3 text-sm text-muted-foreground">
                Searching…
              </li>
            )}
          </ul>

          {total > results.length && (
            // Said plainly: the list is the first twenty of more, and typing
            // is how the rest are reached.
            <div className="border-t px-3 py-1.5 text-xs text-muted-foreground">
              Showing {results.length} of {total.toLocaleString("en-IN")} — keep
              typing to narrow it.
            </div>
          )}
        </div>
      )}
    </div>
  );
}
