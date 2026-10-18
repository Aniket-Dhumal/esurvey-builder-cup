"use client";

import { useState } from "react";
import { Check, Loader2 } from "lucide-react";

import { api, ApiError } from "@/lib/api";
import type { Grievance } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";

/**
 * Answering a farmer's appeal.
 *
 * # Why this exists
 *
 * It did not. An officer could take an appeal and there was no way to decide
 * one: `api.decideGrievance` was implemented, wired to a route, tested on the
 * server, and reachable from no screen. So every appeal a farmer filed could
 * be assigned and then never answered — the one route a farmer has to contest
 * a decision, ending nowhere.
 *
 * # Why the three outcomes are not a dropdown of states
 *
 * "Partly upheld" and "upheld" both change the figure and the server refuses
 * them without one; "rejected" leaves it alone. Presenting them as three
 * buttons with the figure appearing when it is needed is the difference
 * between a form an officer completes and one they are refused by.
 */
export function DecideAppealDialog({
  grievance,
  onClose,
  onDone,
}: {
  grievance: Grievance;
  onClose: () => void;
  onDone: () => void | Promise<void>;
}) {
  const [state, setState] = useState<
    "upheld" | "partly_upheld" | "rejected" | null
  >(null);
  const [percent, setPercent] = useState(
    grievance.claimed_percent != null ? String(grievance.claimed_percent) : "",
  );
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // The server's rules, mirrored so the officer is told before pressing
  // rather than after. It enforces them either way.
  const changesFigure = state === "upheld" || state === "partly_upheld";
  const parsed = Number(percent);
  const figureOk =
    !changesFigure || (Number.isFinite(parsed) && parsed >= 0 && parsed <= 100);
  const canDecide = state !== null && reason.trim().length >= 10 && figureOk;

  async function decide() {
    if (!canDecide || state === null) return;
    setBusy(true);
    setError(null);
    try {
      await api.decideGrievance(grievance.id, {
        state,
        reason: reason.trim(),
        revised_percent: changesFigure ? parsed : null,
      });
      await onDone();
    } catch (e) {
      const err = e as ApiError;
      setError(
        err.code === "SELF_REVIEW"
          ? "You decided this report, so you cannot answer the appeal against it."
          : err.message,
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label={`Decide appeal ${grievance.reference}`}
      className="fixed inset-0 z-50 flex items-center justify-center bg-foreground/30 p-4"
      onClick={(e) => e.target === e.currentTarget && onClose()}
    >
      <div className="max-h-[88vh] w-full max-w-lg overflow-y-auto rounded-2xl border border-border bg-card p-6 shadow-lg">
        <p className="mono text-xs text-muted-foreground">
          {grievance.reference}
        </p>
        <h2 className="mt-1 text-lg font-semibold">{grievance.farmer_name}</h2>

        <div className="mt-3 rounded-md bg-accent px-3 py-2.5 text-xs leading-relaxed">
          <p className="font-medium">What the farmer says</p>
          <p className="mt-1 text-muted-foreground">
            &ldquo;{grievance.statement}&rdquo;
          </p>
          {grievance.disputed_percent != null && (
            <p className="mt-1.5 text-muted-foreground">
              Decided at {grievance.disputed_percent.toFixed(1)}%
              {grievance.claimed_percent != null && (
                <> · they say {grievance.claimed_percent.toFixed(1)}%</>
              )}
            </p>
          )}
        </div>

        <div className="mt-5 space-y-4">
          <div>
            <Label>Your answer</Label>
            <div className="mt-1.5 flex flex-wrap gap-2">
              {(
                [
                  ["upheld", "Upheld"],
                  ["partly_upheld", "Partly upheld"],
                  ["rejected", "Rejected"],
                ] as const
              ).map(([value, label]) => (
                <Button
                  key={value}
                  type="button"
                  variant={state === value ? "default" : "outline"}
                  className="h-10"
                  onClick={() => setState(value)}
                >
                  {label}
                </Button>
              ))}
            </div>
          </div>

          {/* Only where it is needed. Upholding an appeal without changing the
              figure is not upholding it, and the server refuses it. */}
          {changesFigure && (
            <div>
              <Label htmlFor="revised">Revised damage percentage</Label>
              <Input
                id="revised"
                inputMode="decimal"
                value={percent}
                onChange={(e) => setPercent(e.target.value)}
                className="mt-1.5"
              />
              <p className="mt-1.5 text-xs text-muted-foreground">
                This replaces the figure the appeal is against.
              </p>
            </div>
          )}

          <div>
            <Label htmlFor="appeal-reason">Reason</Label>
            <Textarea
              id="appeal-reason"
              rows={4}
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              className="mt-1.5 w-full"
              placeholder="What you looked at, and what you concluded"
            />
            <p className="mt-1.5 text-xs text-muted-foreground">
              The farmer reads this. An appeal answered with nothing but an
              outcome is one they cannot take any further.
            </p>
          </div>

          {error && (
            <p
              role="alert"
              className="rounded-md bg-sev-catastrophic-bg px-3 py-2 text-sm text-sev-catastrophic"
            >
              {error}
            </p>
          )}

          <div className="flex items-center gap-3">
            <Button
              onClick={decide}
              disabled={!canDecide || busy}
              className="h-11 flex-1"
            >
              {busy ? (
                <Loader2 className="size-4 animate-spin" />
              ) : (
                <Check className="size-4" />
              )}
              Record this decision
            </Button>
            <Button
              size="field"
              variant="ghost"
              onClick={onClose}
              disabled={busy}
            >
              Cancel
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}
