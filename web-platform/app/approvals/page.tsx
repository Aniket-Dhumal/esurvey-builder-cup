"use client";

import Link from "next/link";
import { useCallback, useMemo, useState } from "react";
import { CheckCheck, Clock, TriangleAlert, Undo2, Sprout } from "lucide-react";

import { api } from "@/lib/api";
import type { ApprovalLevel, BulkApprovalResult, Paged, ReportSummary } from "@/lib/types";
import { usePagedResource } from "@/lib/usePagedResource";
import { NoMatches, TablePager, TableSearch } from "@/components/TableControls";
import { calamityLabel, istDateTime } from "@/lib/format";
import {
  Card,
  CardHeader,
  EmptyState,
  ErrorPanel,
  Skeleton,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui";
import { Button } from "@/components/ui/button";
import { PageHeader } from "@/components/dashboard";
import { useSession } from "@/components/SessionProvider";
import { DecideDialog } from "@/components/DecideDialog";
import { ThinEvidenceBadge, coverageIsThin } from "@/components/EvidenceCoverage";
import { WaitingTooLong } from "@/components/WaitingTooLong";

/**
 * The claims one officer has been asked to settle.
 *
 * # What is in this queue
 *
 * Only disagreements. The village pair decide a claim between them, and where
 * they agree that is the decision -- nothing above the village approves it.
 * A claim reaches this page because the Talathi and the Krushi Sahayak did not
 * agree, and exactly one officer was asked to answer: the Circle Officer where
 * one is posted over that village, the Tehsildar where none is.
 *
 * So there is nothing to batch. Every row is a claim two officers standing in
 * the same field could not agree about, and the server refuses a bulk action
 * on one; this says so before the click rather than after.
 *
 * # Why the level is not a filter the officer chooses
 *
 * It is derived from their role. A Circle Officer has exactly one queue, and
 * offering them a dropdown would be offering a control that can only ever
 * produce an error.
 *
 * The District Magistrate has no queue at all. They declare the calamity,
 * staff the district, read what its officers decided, and hear appeals -- a
 * better position to hear one from than having signed the decision under
 * appeal.
 */

const LEVEL_FOR_ROLE: Record<string, ApprovalLevel> = {
  mandal_officer: "circle",
  tehsildar: "tehsil",
};

const LEVEL_LABEL: Record<ApprovalLevel, string> = {
  village_pair: "Village",
  circle: "Circle",
  tehsil: "Tehsil",
  district: "District",
};

export default function ApprovalsPage() {
  const { user } = useSession();
  const role = user?.primary_role ?? "";
  const level = LEVEL_FOR_ROLE[role];

  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<BulkApprovalResult | null>(null);
  const [deciding, setDeciding] = useState<ReportSummary | null>(null);

  const table = usePagedResource<ReportSummary, Paged<ReportSummary>>(
    (a) => api.listReports(a, level ? { awaiting: level } : {}),
    { deps: [level] },
  );
  const { rows, error, reload, loading } = table;

  // Only uncontested rows may be swept. Kept as a derived set rather than a
  // flag on each row so "select all" and the button count cannot disagree.
  const selectable = useMemo(
    () => rows.filter((r) => !r.contested).map((r) => r.id),
    [rows],
  );
  const allPicked = selectable.length> 0 && selectable.every((id) => picked.has(id));

  const toggle = useCallback((id: string) => {
    setPicked((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const toggleAll = useCallback(() => {
    setPicked((prev) =>
      selectable.every((id) => prev.has(id)) ? new Set() : new Set(selectable),
    );
  }, [selectable]);

  async function approveSelected() {
    if (picked.size === 0) return;
    setBusy(true);
    setOutcome(null);
    try {
      setOutcome(await api.bulkApprove([...picked]));
      setPicked(new Set());
      await reload();
    } catch (e) {
      setOutcome({
        selected: picked.size,
        approved: 0,
        refused: picked.size,
        results: [
          {
            report_id: "",
            approved: false,
            reason: e instanceof Error ? e.message : "Something went wrong.",
          },
        ],
      });
    } finally {
      setBusy(false);
    }
  }

  if (!level) {
    return (
      <div className="space-y-6">
        <PageHeader title="Approvals" />
        <EmptyState
          title="Your role does not settle claims"
          body={
            "The village pair decide a claim between them. Where they disagree " +
            "it goes to one officer to settle — the Circle Officer, or the " +
            "Tehsildar where no Circle Officer is posted. A District " +
            "Magistrate declares the calamity and hears appeals; the state " +
            "ministry reads aggregates."
          }
      />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={`${LEVEL_LABEL[level]} approvals`}
        subtitle={`Reports waiting for your sign-off. Contested ones are listed first and have to be opened.`}
    />

      {outcome && <BulkResult result={outcome} onDismiss={() => setOutcome(null)} />}

      {/* Above the queue, because a report that is late is not simply one more
          row in it. The queue is sorted by contested-then-age and a report
          three days past its allowance sits in the middle of it looking like
          every other. */}
      <WaitingTooLong />

      <Card>
        <CardHeader
          title={`Waiting at ${LEVEL_LABEL[level].toLowerCase()}`}
          subtitle={`${table.total} report${table.total === 1 ? "" : "s"} · contested first`}
          action={
            <div className="flex items-center gap-3">
              <TableSearch
                value={table.query}
                onChange={table.setQuery}
                busy={table.searching}
                placeholder="Reference or farmer"
            />
              <Button size="lg"
        onClick={approveSelected}
        disabled={picked.size === 0 || busy}
>
                <CheckCheck className="size-4" />
                {busy
                  ? "Approving…"
                  : picked.size === 0
                    ? "Approve selected"
                    : `Approve ${picked.size}`}
              </Button>
            </div>
          }
      />

        {error ? (
          <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
        ) : loading ? (
          <Skeleton className="h-64" />
        ) : rows.length === 0 ? (
          table.query ? (
            <NoMatches query={table.query} noun="reports" />
          ) : (
            <EmptyState
              title="Nothing is waiting"
              body="Every report at your level has been dealt with."
          />
          )
        ) : (
          <>
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-10">
                    <input
                      type="checkbox"
                      aria-label="Select every report that may be approved in a batch"
                      checked={allPicked}
                      onChange={toggleAll}
                      disabled={selectable.length === 0}
                      className="size-4 accent-primary"
                  />
                  </TableHead>
                  {[
                    "Reference",
                    "Farmer",
                    "Village",
                    "Calamity",
                    "Assessed",
                    "Filed",
                    "",
                  ].map((h, i) => (
                    <TableHead key={h || i} className="text-xs">
                      {h}
                    </TableHead>
                  ))}
                </TableRow>
              </TableHeader>

              <TableBody>
                {rows.map((r) => (
                  <TableRow key={r.id} className={r.contested ? "bg-sev-severe-bg/40" : undefined}>
                    <TableCell>
                      <input
                        type="checkbox"
                        aria-label={
                          r.contested
                            ? `${r.reference} is contested and has to be opened`
                            : `Select ${r.reference}`
                        }
                        checked={picked.has(r.id)}
                        onChange={() => toggle(r.id)}
                        disabled={r.contested}
                        className="size-4 accent-primary disabled:opacity-40"
                    />
                    </TableCell>

                    <TableCell>
                      <Link
                        href={`/reports?q=${encodeURIComponent(r.reference)}`}
                        className="mono text-xs font-medium text-primary hover:underline"
>
                        {r.reference}
                      </Link>
                      <Flags report={r} />
                    </TableCell>

                    <TableCell>{r.farmer_name}</TableCell>
                    <TableCell className="text-muted-foreground">
                      {r.village_name}
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {calamityLabel(r.calamity_type)}
                    </TableCell>
                    {/* The assessed figure, not the declared one. This is
                        the number the officer is being asked to approve. */}
                    <TableCell className="tabular-nums">
                      {(r.assessed_percent ?? r.damage_percent).toFixed(1)}%
                      {r.assessed_percent != null &&
                        Math.abs(r.assessed_percent - r.damage_percent)> 0.05 && (
                          <span className="ml-1.5 text-xs font-normal text-muted-foreground">
                            declared {r.damage_percent.toFixed(1)}%
                          </span>
                        )}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {istDateTime(r.reported_at)}
                    </TableCell>

                    <TableCell>
                      <Button
                        variant="outline"
                        className="h-8 text-xs"
                        onClick={() => setDeciding(r)}
>
                        Open
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            <TablePager resource={table} noun="reports" />
          </>
        )}
      </Card>

      {deciding && (
        <DecideDialog
          report={deciding}
          level={level}
          onClose={() => setDeciding(null)}
          onDone={async () => {
            setDeciding(null);
            await reload();
          }}
      />
      )}
    </div>
  );
}

/** The things about a report that change how it may be handled. */
function Flags({ report }: { report: ReportSummary }) {
  if (
    !report.contested &&
    !report.crop_mismatch &&
    report.returned_count === 0 &&
    !report.arrived_after_close &&
    !coverageIsThin(report.stations_captured)
  ) {
    return null;
  }
  return (
    <div className="mt-1 flex flex-wrap items-center gap-2">
      {report.contested && (
        <span className="inline-flex items-center gap-1 rounded bg-sev-severe-bg px-1.5 py-0.5 text-xs font-medium text-sev-severe">
          <TriangleAlert className="size-3" />
          The two village officers disagreed
        </span>
      )}
      {report.crop_mismatch && (
        <span className="inline-flex items-center gap-1 rounded bg-accent px-1.5 py-0.5 text-xs text-foreground">
          <Sprout className="size-3" />
          Registry says {report.registry_crop_name || "another crop"}
        </span>
      )}
      {report.returned_count> 0 && (
        <span className="inline-flex items-center gap-1 rounded bg-accent px-1.5 py-0.5 text-xs text-muted-foreground">
          <Undo2 className="size-3" />
          Sent back {report.returned_count}×
        </span>
      )}
      {report.arrived_after_close && (
        <span className="inline-flex items-center gap-1 rounded bg-sev-moderate-bg px-1.5 py-0.5 text-xs font-medium text-sev-moderate">
          <Clock className="size-3" />
          Arrived after the event closed
        </span>
      )}
      {/* Thin evidence does not block a tick, but it is on the row before
          one: a batch is approved without opening anything. */}
      <ThinEvidenceBadge
        captureCount={report.capture_count}
        stationsCaptured={report.stations_captured}
      />
    </div>
  );
}

/**
 * What a batch actually did.
 *
 * Per item, because partial success is the expected outcome rather than an
 * edge case, and "197 of 200 approved" with no word on the other three is a
 * report an officer cannot act on.
 */
function BulkResult({
  result,
  onDismiss,
}: {
  result: BulkApprovalResult;
  onDismiss: () => void;
}) {
  const refused = result.results.filter((r) => !r.approved);
  return (
    <Card className="border-border/70 p-5">
      <div className="flex items-start justify-between gap-4">
        <div>
          <p className="text-sm font-medium text-foreground">
            {result.approved} of {result.selected} approved
            {result.refused> 0 && `, ${result.refused} left for you to open`}
          </p>
          {refused.length> 0 && (
            <ul className="mt-3 space-y-1.5">
              {refused.map((r, i) => (
                <li key={r.report_id || i} className="text-xs text-muted-foreground">
                  <span className="mono">{r.report_id.slice(0, 8) || "—"}</span> · {r.reason}
                </li>
              ))}
            </ul>
          )}
        </div>
        <button
          onClick={onDismiss}
          className="text-xs font-medium text-muted-foreground underline underline-offset-2"
>
          Dismiss
        </button>
      </div>
    </Card>
  );
}
