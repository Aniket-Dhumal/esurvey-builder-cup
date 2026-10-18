"use client";

import { useState } from "react";
import { Loader2 } from "lucide-react";

import { api, ApiError } from "@/lib/api";
import type { SurveyEvent } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Modal } from "@/components/ui/modal";
import { formatDate } from "@/lib/format";
import { Textarea } from "@/components/ui/textarea";

/**
 * Confirming a change to an event's state.
 *
 * # Why a click was not enough
 *
 * "Close filing" sat next to the event as a plain button and fired on the
 * first click. Pressing it stops every farmer in the district filing against
 * that event, and there was nothing between an accidental click and that
 * happening — no confirmation, and no record of why anyone did it.
 *
 * Closing is reversible: the state machine allows closed → open, and the
 * comment beside it calls reopening "a real act". That makes an accidental
 * close recoverable, not harmless. Between the mistake and the fix, farmers
 * are told the window has shut.
 *
 * # Why closing asks for a note and declaring does not
 *
 * The API has always accepted a reason on a transition and written it to the
 * audit log; the console simply never sent one. Requiring it everywhere would
 * teach officers to type a full stop, so it is asked for where it is actually
 * read: closing early is the decision somebody will later ask about, and
 * "closed on the 14th" with no reason is not an answer.
 */

const COPY: Record<
  string,
  { title: string; body: string; confirm: string; needsNote: boolean }
> = {
  declared: {
    title: "Declare this event?",
    body:
      "The notification becomes visible to officers in the district. Farmers " +
      "cannot file until it is opened for survey.",
    confirm: "Declare",
    needsNote: false,
  },
  open: {
    title: "Open this event for survey?",
    body:
      "Farmers in the covered talukas will be able to file against it from " +
      "now until the filing deadline.",
    confirm: "Open for survey",
    needsNote: false,
  },
  closed: {
    title: "Close filing on this event?",
    body:
      "Every farmer in the covered area stops being able to file against it, " +
      "immediately. Reports already filed continue through review unaffected.",
    confirm: "Close filing",
    needsNote: true,
  },
  complete: {
    title: "Mark this event complete?",
    body: "The event is finished and moves out of the working list.",
    confirm: "Mark complete",
    needsNote: true,
  },
};

export function ConfirmTransition({
  event,
  to,
  onClose,
  onDone,
}: {
  event: SurveyEvent;
  to: string;
  onClose: () => void;
  onDone: () => void | Promise<void>;
}) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const copy = COPY[to] ?? {
    title: `Move this event to ${to}?`,
    body: "",
    confirm: "Confirm",
    needsNote: true,
  };
  const ready = !copy.needsNote || reason.trim().length> 0;

  async function go() {
    setBusy(true);
    setError(null);
    try {
      await api.transitionEvent(event.id, to, reason.trim() || undefined);
      await onDone();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Something went wrong.");
      setBusy(false);
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      size="sm"
      title={copy.title}
      description={`${event.reference} · ${event.title}`}
      footer={
        <>
          <Button size="lg" variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button size="lg"
      onClick={go}
      disabled={busy || !ready}
      variant={to === "closed" ? "destructive" : "default"}
>
            {busy && <Loader2 className="size-4 animate-spin" />}
            {copy.confirm}
          </Button>
        </>
      }
>
      <p className="text-sm leading-relaxed text-foreground">{copy.body}</p>

      {to === "closed" && (
        <p className="mt-3 text-sm leading-relaxed text-muted-foreground">
          The filing deadline on this event is{" "}
          <span className="font-medium text-foreground">
            {formatDate(event.survey_closes_at)}
          </span>
          . Closing now ends filing before then.
        </p>
      )}

      {copy.needsNote && (
        <div className="mt-4">
          <Label htmlFor="why">Reason</Label>
          <Textarea id="why" rows={3} value={reason} onChange={(e) => setReason(e.target.value)}
            placeholder="Why is this happening now? Recorded against the event in the audit log."
            className="mt-1.5 w-full"
          />
        </div>
      )}

      {error && (
        <p
          role="alert"
          className="mt-3 rounded-md bg-sev-catastrophic-bg px-3 py-2 text-sm text-sev-catastrophic"
>
          {error}
        </p>
      )}
    </Modal>
  );
}
