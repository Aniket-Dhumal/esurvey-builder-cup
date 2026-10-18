"use client";

import Link from "next/link";
import { Clock } from "lucide-react";

import { api } from "@/lib/api";
import { useResource } from "@/lib/useResource";
import type { WaitingEntry } from "@/lib/types";
import { roleLabel } from "@/lib/format";
import {
  Card,
  CardHeader,
  Skeleton,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui";

/**
 * Reports that have sat at a rung longer than that rung allows.
 *
 * # Why this is visible to the level above as well as the level that is late
 *
 * Both see it, and that is the whole point. Showing an officer their own late
 * reports is what the worklist already did, and it fails in exactly the case
 * that matters: an officer who has stopped opening the console is precisely
 * the officer whose reports are late, and nobody above them could tell.
 *
 * The scope does the rest — a Tehsildar sees their own and the Circle's, a
 * Collector sees the district's — so this component takes no filter.
 *
 * # Why nothing here is a button
 *
 * Because nothing moves. A report that escalated on a timer would arrive at
 * the next rung with no finding behind it and would reward the officer who
 * ignored it. Being late is a flag and two notifications; the remedy is
 * somebody opening the report, which is what the reference links to.
 *
 * # Why it renders nothing when nothing is late
 *
 * An empty "nothing is overdue" card on every page view teaches an officer to
 * scroll past this position, which is exactly where the one overdue report
 * will appear.
 */
export function WaitingTooLong() {
  const { data, error, loading } = useResource(() => api.overdue());

  // A failure here is not worth a panel. The queue below is the page's job;
  // this is a warning that sits above it, and an error box in its place would
  // read as though the queue had failed.
  if (error) return null;
  if (loading) return <Skeleton className="h-24" />;

  const rungs = data?.rungs ?? [];
  if (rungs.length === 0) return null;

  return (
    <Card className="border-sev-severe/40">
      <CardHeader
        title="Waiting longer than allowed"
        subtitle={
          `${rungs.length} report${rungs.length === 1 ? " has" : "s have"} passed ` +
          "the time their level is given. Nothing moves on its own."
        }
        action={
          <Clock className="size-4 text-sev-severe" aria-hidden />
        }
      />

      <div className="overflow-x-auto">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Reference</TableHead>
              <TableHead>Farmer</TableHead>
              <TableHead>Village</TableHead>
              <TableHead>Waiting at</TableHead>
              <TableHead className="text-right">Overdue</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rungs.map((r) => (
              <Row key={r.report_id} entry={r} />
            ))}
          </TableBody>
        </Table>
      </div>
    </Card>
  );
}

function Row({ entry: r }: { entry: WaitingEntry }) {
  return (
    <TableRow>
      <TableCell className="mono">
        <Link
          href={`/verify/?id=${encodeURIComponent(r.report_id)}`}
          className="hover:text-primary hover:underline">
          {r.reference}
        </Link>
        {/* A contested report was already the harder one. Being late with it
            is worse, and the row says so rather than leaving a supervisor to
            open each one to find out. */}
        {r.contested && (
          <span className="ml-2 rounded bg-sev-severe-bg px-1.5 py-0.5 text-xs font-medium text-sev-severe">
            contested
          </span>
        )}
      </TableCell>
      <TableCell>{r.farmer_name}</TableCell>
      <TableCell>{r.village_name}</TableCell>
      <TableCell className="text-sm text-muted-foreground">
        {roleLabel(r.role)}
      </TableCell>
      <TableCell className="tnum text-right text-sm font-medium text-sev-severe">
        {r.days_overdue === 0
          ? "today"
          : `${r.days_overdue} day${r.days_overdue === 1 ? "" : "s"}`}
      </TableCell>
    </TableRow>
  );
}
