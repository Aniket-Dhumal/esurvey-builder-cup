"use client";

import Link from "next/link";
import { useState } from "react";
import { ClipboardCheck, TriangleAlert } from "lucide-react";

import { api } from "@/lib/api";
import type { Paged, WorklistEntry } from "@/lib/types";
import { usePagedResource } from "@/lib/usePagedResource";
import { NoMatches, TablePager, TableSearch } from "@/components/TableControls";
import { calamityLabel, istDateTime, roleLabel } from "@/lib/format";
import {
  Card,
  CardHeader,
  DueBadge,
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
import { PageHeader } from "@/components/dashboard";
import { useSession } from "@/components/SessionProvider";

/**
 * The officer's home: what they have been asked to verify.
 *
 * Ordered by deadline, oldest first, because that is the order the work should
 * be done in and any other default invites the easy ones to be picked off.
 */
export default function WorklistPage() {
  const { user } = useSession();
  const [showDone, setShowDone] = useState(false);

  const table = usePagedResource<WorklistEntry, Paged<WorklistEntry> & { overdue: number }>(
    (a) => api.worklist(a, showDone),
    { deps: [showDone] },
  );
  const { rows: items, error, reload, loading, query } = table;
  // Across the whole list, not this page: an officer told "3 overdue" while
  // looking at page one, with nine more on page four, has been misled.
  const overdue = table.last?.overdue ?? 0;

  return (
    <div>
      <PageHeader
        eyebrow="Verification"
        title="My worklist"
        subtitle={
          user
            ? `${roleLabel(user.primary_role)}${
                user.jurisdiction_codes.length
                  ? ` · ${user.jurisdiction_codes.join(", ")}`
                  : ""
              }`
            : undefined
        }
        actions={
          <label className="flex cursor-pointer items-center gap-2 text-sm text-muted-foreground">
            <input
              type="checkbox"
              checked={showDone}
              onChange={(e) => setShowDone(e.target.checked)}
              className="size-4 accent-primary"
            />
            Show completed
          </label>
        }
      />

      {error && (
        <div className="mb-6">
          <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
        </div>
      )}

      {overdue > 0 && (
        <div className="mb-4 flex items-center gap-2 rounded-md bg-sev-catastrophic-bg px-4 py-2.5 text-sm text-sev-catastrophic">
          <TriangleAlert className="size-4 shrink-0" />
          {overdue} {overdue === 1 ? "report is" : "reports are"} past the
          seven-day verification deadline.
        </div>
      )}

      <Card>
        <CardHeader
          title="Assigned to you"
          subtitle="Unfinished first, then by deadline"
          action={
            <TableSearch
              value={table.query}
              onChange={table.setQuery}
              busy={table.searching}
              placeholder="Reference or farmer"
            />
          }
        />

        {loading && (
          <div className="space-y-2 p-5">
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-9 w-full" />
          </div>
        )}

        {!loading && items.length === 0 && query !== "" && (
          <NoMatches query={query} noun="assignments" />
        )}

        {!loading && items.length === 0 && query === "" && (
          <EmptyState
            title="Nothing assigned"
            body={
              showDone
                ? "You have no assignments at all yet."
                : "No reports are waiting on you. A report is allocated to the Talathi and Krushi Sahayak posted to the village its land sits in."
            }
          />
        )}

        {!loading && items.length > 0 && (
          <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                {[
                  "Report",
                  "Farmer",
                  "Village",
                  "Cause",
                  "Occurred",
                  "Colleague",
                  "Due",
                ].map((h) => (
                  <TableHead key={h} className="text-xs">
                    {h}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((a) => (
                <TableRow key={a.id} className={a.completed_at ? "opacity-60" : undefined}>
                  <TableCell>
                    <Link
                      href={`/verify/?id=${a.report_id}`}
                      className="mono font-medium hover:text-primary">
                      {a.reference}
                    </Link>
                    <div className="text-xs text-faint">
                      {roleLabel(a.role)}
                    </div>
                  </TableCell>
                  <TableCell>{a.farmer_name}</TableCell>
                  <TableCell className="text-muted-foreground">
                    {a.village_name}
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {calamityLabel(a.calamity_type)}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {istDateTime(a.occurred_at)}
                  </TableCell>
                  <TableCell className="text-xs">
                    {/* That they have been, never what they concluded. */}
                    {a.peer_recorded ? (
                      <span className="inline-flex items-center gap-1.5 text-muted-foreground">
                        <ClipboardCheck className="size-3.5" />
                        has recorded
                      </span>
                    ) : (
                      <span className="text-faint">not yet</span>
                    )}
                  </TableCell>
                  <TableCell>
                    {a.completed_at ? (
                      <span className="text-xs text-faint">done</span>
                    ) : (
                      <DueBadge dueAt={a.due_at} overdue={a.overdue} />
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          </div>
        )}
        <TablePager resource={table} noun="assignments" />
      </Card>

      <p className="mt-3 text-xs leading-relaxed text-faint">
        Your colleague&rsquo;s finding is not shown until you have recorded your
        own. The two of you verify different things — the land record and the
        crop — and the agreement between you is the only independent measure
        this system has.
      </p>
    </div>
  );
}
