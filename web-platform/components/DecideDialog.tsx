"use client";

import { useEffect, useState } from "react";
import { Loader2, Undo2, Check, TriangleAlert, Sprout, MapPinOff } from "lucide-react";

import { api } from "@/lib/api";
import type { ApprovalLevel, ApprovalRecord, ReportSummary } from "@/lib/types";
import { istDateTime, roleLabel } from "@/lib/format";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  EvidenceCoverage,
  STATIONS_EXPECTED,
  coverageIsThin,
} from "@/components/EvidenceCoverage";

/**
 * Deciding one report at one rung.
 *
 * # Why the figure is pre-filled and editable in the same box
 *
 * Any level may revise the number and the District's stands. Most approvals
 * change nothing, so the box opens holding what the level below passed up and
 * approving it unchanged is one click. Typing a different number is the
 * revision, and the reason field appears the moment it differs — because a
 * change nobody explained leaves the record unable to answer who decided what
 * a farmer was paid, which is the one question it exists to answer.
 *
 * # Why the history is on this screen and not behind a tab
 *
 * An officer being asked to approve 65% should be able to see, without
 * clicking, that the village pair said 40% and the Circle changed it. That is
 * the whole of the context for the decision they are about to make.
 */
export function DecideDialog({
  report,
  level,
  onClose,
  onDone,
}: {
  report: ReportSummary;
  level: ApprovalLevel;
  onClose: () => void;
  onDone: () => void | Promise<void>;
}) {
  // Pre-filled with what the ladder is carrying, which is what the server
  // will compare against. Seeding this from the farmer's declaration instead
  // made an unchanged approval look like a fifteen-point cut to the server and
  // like a no-op to the officer.
  const standing = report.assessed_percent ?? report.damage_percent;
  const [percent, setPercent] = useState(standing.toFixed(1));
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [history, setHistory] = useState<ApprovalRecord[] | null>(null);

  useEffect(() => {
    let live = true;
    api
      .reportApprovals(report.id)
      .then((r) => live && setHistory(r.approvals))
      .catch(() => live && setHistory([]));
    return () => {
      live = false;
    };
  }, [report.id]);

  const parsed = Number(percent);
  const valid = Number.isFinite(parsed) && parsed>= 0 && parsed <= 100;
  const changed = valid && Math.abs(parsed - standing)> 0.001;

  // The server owns both of these rules; they are mirrored here only so the
  // officer is told before pressing rather than after.
  const reasonNeeded = changed || (report.contested && level === "circle");
  const canApprove = valid && (!reasonNeeded || reason.trim().length> 0);

  async function act(kind: "approve" | "return") {
    setBusy(true);
    setError(null);
    try {
      if (kind === "return") {
        await api.returnReport(report.id, reason.trim());
      } else {
        await api.approveReport(report.id, {
          ...(changed ? { damage_percent: parsed } : {}),
          ...(reason.trim() ? { reason: reason.trim() } : {}),
        });
      }
      await onDone();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Something went wrong.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label={`Decide ${report.reference}`}
      className="fixed inset-0 z-50 flex items-center justify-center bg-foreground/30 p-4"
      onClick={(e) => e.target === e.currentTarget && onClose()}
>
      <div className="max-h-[88vh] w-full max-w-lg overflow-y-auto rounded-2xl border border-border bg-card p-6 shadow-lg">
        <div className="mb-5">
          <p className="mono text-xs text-muted-foreground">{report.reference}</p>
          <h2 className="mt-1 text-lg font-semibold text-foreground">
            {report.farmer_name}
          </h2>
          <p className="text-sm text-muted-foreground">
            {report.village_name} · {report.area_affected_ha.toFixed(2)} ha affected
            {report.assessed_percent != null &&
              Math.abs(report.assessed_percent - report.damage_percent)> 0.05 && (
                <> · farmer declared {report.damage_percent.toFixed(1)}%</>
              )}
          </p>
          {/* What the figure above is standing on. An officer revising a
              percentage should know whether it came off a whole walk or one
              photograph. */}
          <p className="mt-1.5 text-xs">
            <EvidenceCoverage
              captureCount={report.capture_count}
              stationsCaptured={report.stations_captured}
            />
          </p>
        </div>

        {coverageIsThin(report.stations_captured) && (
          <Note
            icon={MapPinOff}
            tone="muted"
            text={
              report.capture_count === 0
                ? "Nothing was filed against this claim. Deciding it means " +
                  "deciding on the declaration alone."
                : `The app directs the farmer to ${STATIONS_EXPECTED} points ` +
                  `around the parcel; ${report.stations_captured} of them were ` +
                  "filmed. The rest of the field is not in the evidence. This " +
                  "is not a reason to refuse — it is what the figure rests on."
            }
          />
        )}

        {report.contested && (
          <Note
            icon={TriangleAlert}
            tone="severe"
            text={
              "The Talathi and the Krushi Sahayak disagreed about this field by " +
              "more than 25 points. Say what you concluded and why."
            }
        />
        )}
        {report.crop_mismatch && (
          <Note
            icon={Sprout}
            tone="muted"
            text={
              `The registry records ${report.registry_crop_name || "another crop"} ` +
              "for this land, which this event does not cover. The registry is " +
              "frequently stale, so this is a discrepancy to settle rather than " +
              "a reason to refuse."
            }
        />
        )}

        <div className="mt-5 space-y-4">
          <div>
            <Label htmlFor="pct">Damage percentage</Label>
            <Input
              id="pct"
              inputMode="decimal"
              value={percent}
              onChange={(e) => setPercent(e.target.value)}
              className="mt-1.5"
            />
            <p className="mt-1.5 text-xs text-muted-foreground">
              {changed
                ? `Changing from ${standing.toFixed(1)}% to ${parsed.toFixed(1)}%.`
                : "Approving it as it stands. Change the number to revise it."}
            </p>
          </div>

          <div>
            <Label htmlFor="reason">
              Reason{reasonNeeded ? "" : " (optional)"}
            </Label>
            <Textarea id="reason" rows={3} value={reason} onChange={(e) => setReason(e.target.value)}
              placeholder={
 reasonNeeded
 ? "Required: say what you found and why the figure is what it is"
 : "Anything the next level should know"
 }
              className="mt-1.5 w-full"
            />
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
            <Button onClick={() => act("approve")} disabled={!canApprove || busy} className="h-11 flex-1">
              {busy ? <Loader2 className="size-4 animate-spin" /> : <Check className="size-4" />}
              Approve
            </Button>
            <Button
              variant="outline"
              onClick={() => act("return")}
              disabled={busy || reason.trim().length === 0}
              className="h-11"
              title="Send this back one level, with your reason"
>
              <Undo2 className="size-4" />
              Send back
            </Button>
            <Button size="field" variant="ghost" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
          </div>

          <p className="text-xs leading-relaxed text-faint">
            Sending back returns this to the level immediately below with your
            reason. They record again and it comes back up.
          </p>
        </div>

        <History rows={history} />
      </div>
    </div>
  );
}

function Note({
  icon: Icon,
  tone,
  text,
}: {
  icon: React.ComponentType<{ className?: string }>;
  tone: "severe" | "muted";
  text: string;
}) {
  const styles =
    tone === "severe"
      ? "bg-sev-severe-bg text-sev-severe"
      : "bg-accent text-foreground";
  return (
    <div className={`mt-3 flex gap-2.5 rounded-md px-3 py-2.5 text-xs leading-relaxed ${styles}`}>
      <Icon className="mt-0.5 size-4 shrink-0" />
      <span>{text}</span>
    </div>
  );
}

/** Every rung this report has passed, superseded ones included. */
function History({ rows }: { rows: ApprovalRecord[] | null }) {
  if (rows === null) {
    return <p className="mt-6 text-xs text-muted-foreground">Loading history…</p>;
  }
  if (rows.length === 0) return null;

  return (
    <div className="mt-6 border-t border-border pt-4">
      <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        How it got here
      </h3>
      <ol className="mt-3 space-y-3">
        {rows.map((a) => (
          <li
            key={a.id}
            className={`text-xs leading-relaxed ${a.superseded_at ? "opacity-50" : ""}`}
>
            <span className="font-medium text-foreground">
              {roleLabel(a.role)}
            </span>{" "}
            <span className="text-muted-foreground">
              {a.decision === "returned" ? "sent it back" : "approved"}
              {a.decision === "approved" &&
                a.percent_after != null &&
                a.percent_before != null &&
                Math.abs(a.percent_after - a.percent_before)> 0.001 &&
                ` and changed ${a.percent_before.toFixed(1)}% to ${a.percent_after.toFixed(1)}%`}
              {" · "}
              {istDateTime(a.decided_at)}
              {a.superseded_at && " · later sent back, so this no longer stands"}
            </span>
            {a.reason && <p className="mt-0.5 text-muted-foreground">“{a.reason}”</p>}
          </li>
        ))}
      </ol>
    </div>
  );
}
