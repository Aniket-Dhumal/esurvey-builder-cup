"use client";

import { useEffect, useState } from "react";

import { api, ApiError } from "@/lib/api";
import type { SurveyEvent } from "@/lib/types";
import { formatDate } from "@/lib/format";
import { NativeSelect } from "@/components/ui/native-select";
import { Note } from "@/components/ui";

/**
 * Choosing the calamity a piece of work belongs to.
 *
 * # Why this is asked first
 *
 * Everything else is dated by it. A claim may only be filed against a declared
 * event -- that is what stops retrospective claiming -- and an assessment is
 * asked about a window that has to be the window somebody declared, not a date
 * an officer typed from memory. Asking for the event first turns a free date
 * field into a choice inside a window.
 *
 * # Why a component
 *
 * Because it is asked in both places, and was written in one of them: the
 * claim form had this select and the assessment form had a bare date box.
 */
export function EventPicker({
  id,
  value,
  onChange,
  /** Shown when the officer's area has no open event. */
  emptyNote,
}: {
  id?: string;
  value: string;
  onChange: (event: SurveyEvent | null) => void;
  emptyNote?: string;
}) {
  const [events, setEvents] = useState<SurveyEvent[]>([]);
  const [error, setError] = useState<ApiError | null>(null);

  useEffect(() => {
    let live = true;
    // Open events in this officer's own area. More than two hundred open at
    // once is a different problem than this list.
    api
      .listEvents({ limit: 200 }, "open")
      .then((r) => {
        if (!live) return;
        const items = r.items ?? [];
        setEvents(items);
        // One open event is the ordinary case after a calamity, and choosing
        // it for the officer saves a click without hiding anything: the field
        // still shows which event the work is against.
        if (items.length === 1) onChange(items[0]);
      })
      .catch((e) => live && setError(e as ApiError));
    return () => {
      live = false;
    };
    // Once: the list is of open events, and an event declared while this form
    // is on screen does not move the work already being described.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const chosen = events.find((e) => e.id === value) ?? null;

  if (!error && events.length === 0) {
    return (
      <Note>
        {emptyNote ??
          "No survey event is open in your area. Work is recorded against a declared calamity, which is what dates it."}
      </Note>
    );
  }

  return (
    <>
      <NativeSelect
        selectSize="lg"
        id={id}
        value={value}
        className="w-full"
        onChange={(e) =>
          onChange(events.find((x) => x.id === e.target.value) ?? null)
        }
        required
      >
        <option value="">Select the calamity…</option>
        {events.map((e) => (
          <option key={e.id} value={e.id}>
            {e.reference} — {e.title}
          </option>
        ))}
      </NativeSelect>

      {chosen && (
        <p className="text-xs text-faint">
          Incident {formatDate(chosen.incident_start)} →{" "}
          {formatDate(chosen.incident_end)}
        </p>
      )}
      {error && <p className="text-xs text-destructive">{error.message}</p>}
    </>
  );
}
