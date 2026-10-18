"use client";

import { useState } from "react";
import { Loader2, MinusCircle, Pause, Play, PlusCircle } from "lucide-react";

import { api, ApiError } from "@/lib/api";
import type { SurveyEvent } from "@/lib/types";
import { Button } from "@/components/ui";
import { Label } from "@/components/ui/label";
import { Modal } from "@/components/ui/modal";
import { Textarea } from "@/components/ui/textarea";
import { AreaPicker } from "@/components/AreaPicker";
import { useSession } from "@/components/SessionProvider";

/**
 * Editing a survey event that is already running.
 *
 * # Why this exists
 *
 * An event's coverage used to be fixed the moment it was declared, and there
 * was no way to suspend filing short of closing it. Both are wrong about how a
 * calamity actually unfolds: a Collector learns on day three that two more
 * circles were hit, and a district whose roads are impassable wants collection
 * stopped for two days without ending the window.
 *
 * The workarounds were declaring a second event over the same storm — which
 * splits one calamity's reports across two references and two deadlines — and
 * closing the event, which puts "filing ended" into its permanent history for
 * something that was a two-day pause.
 *
 * # What is still refused
 *
 * A circle farmers have already filed from cannot be removed, and the server
 * says how many reports hold it. Removing it would leave those claims outside
 * the event they were filed against; withdrawing them instead would let one
 * edit destroy a farmer's claim. Neither is a power a coverage correction
 * should carry, so the answer is no and the reason is given.
 */
export function ManageEvent({
  event: e,
  open,
  onClose,
  onChanged,
}: {
  event: SurveyEvent;
  open: boolean;
  onClose: () => void;
  onChanged: () => void;
}) {
  const { can, user } = useSession();
  const districtId =
    user?.grants?.find((g) => g.jurisdiction_level === "district")
      ?.jurisdiction_id ?? "";

  const [adding, setAdding] = useState<string[]>([]);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const running = e.state === "open" || e.state === "paused";
  const covered = new Set(e.areas.map((a) => a.code));

  async function run(what: string, fn: () => Promise<unknown>) {
    setBusy(what);
    setError(null);
    try {
      await fn();
      setAdding([]);
      setReason("");
      onChanged();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Something went wrong.");
    } finally {
      setBusy(null);
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={`Manage — ${e.reference}`}
      description={e.title}
      size="lg"
      footer={
        <Button variant="outline" onClick={onClose}>
          Close
        </Button>
      }>
      <div className="space-y-6">
        {/* --- pause and resume ------------------------------------- */}
        {can("event:close") && running && (
          <section>
            <h3 className="text-sm font-semibold">Filing</h3>
            <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
              {e.state === "paused" ? (
                <>
                  Filing is paused. Farmers cannot submit new reports, and the
                  deadline of{" "}
                  <span className="font-medium text-foreground">
                    {new Date(e.survey_closes_at).toLocaleDateString("en-IN", {
                      day: "numeric",
                      month: "long",
                      year: "numeric",
                    })}
                  </span>{" "}
                  has not moved. Everything already filed keeps moving up the
                  ladder.
                </>
              ) : (
                <>
                  Pausing stops new reports without ending the window. Use it
                  when collection has to stop for a few days — the deadline
                  stays where it is, and reports already filed are unaffected.
                </>
              )}
            </p>
            <Button
              className="mt-3"
              size="sm"
              variant={e.state === "paused" ? "default" : "outline"}
              disabled={busy !== null}
              onClick={() =>
                void run("state", () =>
                  api.transitionEvent(
                    e.id,
                    e.state === "paused" ? "open" : "paused",
                    reason.trim() || undefined,
                  ),
                )
              }>
              {busy === "state" ? (
                <Loader2 className="size-4 animate-spin" />
              ) : e.state === "paused" ? (
                <Play className="size-4" />
              ) : (
                <Pause className="size-4" />
              )}
              {e.state === "paused" ? "Resume filing" : "Pause filing"}
            </Button>
          </section>
        )}

        {/* --- coverage --------------------------------------------- */}
        {can("event:create") && running && (
          <section className="border-t border-border pt-5">
            <h3 className="text-sm font-semibold">Areas covered</h3>
            <p className="mt-1 text-sm text-muted-foreground">
              Only farmers whose land sits inside one of these may file against
              this event.
            </p>

            <ul className="mt-3 divide-y divide-border rounded-lg border border-border">
              {e.areas.map((a) => (
                <li
                  key={a.jurisdiction_id}
                  className="flex items-center justify-between gap-3 px-3 py-2.5">
                  <span className="min-w-0 text-sm">
                    <span className="font-medium">{a.name}</span>{" "}
                    <span className="text-xs text-muted-foreground">
                      {a.level === "circle" ? "circle" : "taluka"} ·{" "}
                      {a.village_count} villages
                    </span>
                  </span>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={busy !== null || e.areas.length <= 1}
                    title={
                      e.areas.length <= 1
                        ? "The only area. An event covering nothing covers nobody — cancel it instead."
                        : undefined
                    }
                    onClick={() =>
                      void run(`rm-${a.code}`, () =>
                        api.editEventAreas(
                          e.id,
                          "remove",
                          [a.code],
                          reason.trim() || undefined,
                        ),
                      )
                    }>
                    {busy === `rm-${a.code}` ? (
                      <Loader2 className="size-3.5 animate-spin" />
                    ) : (
                      <MinusCircle className="size-3.5" />
                    )}
                    Remove
                  </Button>
                </li>
              ))}
            </ul>

            {districtId && (
              <div className="mt-4 space-y-2">
                <Label>Add talukas or circles</Label>
                <AreaPicker
                  districtId={districtId}
                  value={adding}
                  onChange={setAdding}
                />
                <Button
                  size="sm"
                  disabled={busy !== null || adding.length === 0}
                  onClick={() =>
                    void run("add", () =>
                      api.editEventAreas(
                        e.id,
                        "add",
                        // Anything already covered is dropped rather than sent:
                        // the server would ignore it, and the confirmation
                        // would claim to have added something it did not.
                        adding.filter((c) => !covered.has(c)),
                        reason.trim() || undefined,
                      ),
                    )
                  }>
                  {busy === "add" ? (
                    <Loader2 className="size-4 animate-spin" />
                  ) : (
                    <PlusCircle className="size-4" />
                  )}
                  Add to this event
                </Button>
              </div>
            )}
          </section>
        )}

        {!running && (
          <p className="text-sm leading-relaxed text-muted-foreground">
            This event is {e.state}. Its coverage and its filing window are
            fixed — an event that farmers can no longer file against is a
            record, not a form.
          </p>
        )}

        {running && (
          <div className="space-y-1.5 border-t border-border pt-5">
            <Label htmlFor="mg-reason">Reason (optional)</Label>
            <Textarea
              id="mg-reason"
              rows={2}
              value={reason}
              onChange={(e2) => setReason(e2.target.value)}
              placeholder="Two more circles reported hit on the morning of 3 September"
            />
            <p className="text-xs text-faint">
              Recorded in the audit trail against whichever change you make
              next.
            </p>
          </div>
        )}

        {error && (
          <p
            role="alert"
            className="rounded-lg bg-destructive-bg px-3 py-2 text-sm text-on-destructive-bg">
            {error}
          </p>
        )}
      </div>
    </Modal>
  );
}
