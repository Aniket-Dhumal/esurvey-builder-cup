"use client";

import Link from "next/link";
import { useState } from "react";

import { api } from "@/lib/api";
import { useResource } from "@/lib/useResource";
import type { Statewide } from "@/lib/types";
import { calamityLabel, istDateTime } from "@/lib/format";
import { PageHeader } from "@/components/dashboard";
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

/**
 * Oversight above a district.
 *
 * # What this replaces
 *
 * Nothing, which was the problem. A State Officer signed in and got the
 * district dashboard scoped upward: every number on it a statewide total,
 * which is the one figure they already know. There was no comparison between
 * districts, no cut by crop or by event, and no movement over time — which is
 * most of what oversight at that level consists of.
 *
 * # Why every number is dated
 *
 * They come from the rollup tables, which a sweeper keeps current on a minute
 * ticker, because a live aggregate over the whole state is a full scan of the
 * largest table in the database on every page load. So a figure here is as old
 * as the last sweep, and the page says so. An aggregate that implies it is
 * live and is not is worse than one that dates itself — a Collector reading a
 * number during an event needs to know whether it includes this morning.
 *
 * # Why districts are ordered by area and not by claim count
 *
 * A district with four thousand small claims and one with eight hundred large
 * ones are different problems, and the second is the larger one. Area affected
 * is the figure a relief conversation actually turns on.
 */
export default function StatewidePage() {
  const [eventId, setEventId] = useState("");
  const { data, error, loading, reload } = useResource(
    () => api.statewide(eventId || undefined),
    [eventId],
  );

  return (
    <div className="space-y-6">
      <PageHeader
        title="Statewide"
        subtitle="Districts side by side, the same claims by crop and by event, and what has been filed over the last eight weeks."
      />

      {error ? (
        <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
      ) : loading ? (
        <Skeleton className="h-96" />
      ) : !data || data.totals.reports === 0 ? (
        <EmptyState
          title="Nothing has been filed in your jurisdiction"
          body="This view fills as claims arrive. It reads the rollup tables, so a claim filed in the last minute may not be counted yet."
        />
      ) : (
        <>
          <Totals data={data} />

          {data.events.length > 0 && (
            <EventFilter
              data={data}
              eventId={eventId}
              onPick={setEventId}
            />
          )}

          <FilingTrend data={data} />
          <Districts data={data} />
          <Crops data={data} />

          <Freshness data={data} />
        </>
      )}
    </div>
  );
}

function Totals({ data }: { data: Statewide }) {
  const t = data.totals;
  return (
    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <Stat label="Claims" value={n(t.reports)} sub={`${n(t.farmers)} household${t.farmers === 1 ? "" : "s"}`} />
      <Stat
        label="Still open"
        value={n(t.unresolved)}
        sub={`${n(t.adjudicated)} decided`}
        warn={t.unresolved > 0}
      />
      <Stat label="Area affected" value={`${ha(t.area_affected_ha)} ha`} sub="as claimed" />
      <Stat
        label="Decided loss"
        value={t.adjudicated_percent > 0 ? pct(t.adjudicated_percent) : "—"}
        // Spelled out because the two percentages are routinely confused, and
        // the gap between them is the single most interesting number here.
        sub={
          t.adjudicated_percent > 0
            ? `farmers declared ${pct(t.declared_percent)}`
            : "nothing adjudicated yet"
        }
      />
    </div>
  );
}

function Stat({
  label,
  value,
  sub,
  warn,
}: {
  label: string;
  value: string;
  sub?: string;
  warn?: boolean;
}) {
  return (
    <Card className="px-4 py-4">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p
        className={
          "tnum mt-1 text-2xl font-semibold " +
          (warn ? "text-sev-severe" : "text-foreground")
        }>
        {value}
      </p>
      {sub && <p className="mt-0.5 text-xs text-faint">{sub}</p>}
    </Card>
  );
}

/**
 * Which notified calamity, if any.
 *
 * A dropdown and not a set of tabs: a bad season carries a dozen events, and
 * tabs would wrap into three rows of them.
 */
function EventFilter({
  data,
  eventId,
  onPick,
}: {
  data: Statewide;
  eventId: string;
  onPick: (id: string) => void;
}) {
  return (
    <Card className="px-4 py-3">
      <label className="flex flex-wrap items-center gap-3 text-sm">
        <span className="text-muted-foreground">Event</span>
        <select
          value={eventId}
          onChange={(e) => onPick(e.target.value)}
          className="rounded-md border border-border bg-card px-3 py-1.5 text-sm">
          <option value="">Every event</option>
          {data.events.map((e) => (
            <option key={e.id} value={e.id}>
              {e.reference} · {e.title} · {calamityLabel(e.calamity_type)}
              {e.district_name ? ` · ${e.district_name}` : ""}
            </option>
          ))}
        </select>
        {eventId && (
          <button
            type="button"
            onClick={() => onPick("")}
            className="text-xs text-primary hover:underline">
            Clear
          </button>
        )}
      </label>
    </Card>
  );
}

/**
 * Eight weeks of filings.
 *
 * Bars rather than a line, and no library: this is one number per day over a
 * fixed window, and what a reader takes from it is the shape — when the event
 * landed and whether filing has stopped. A charting dependency for that is a
 * hundred kilobytes to draw fifty-six rectangles.
 */
function FilingTrend({ data }: { data: Statewide }) {
  const peak = Math.max(1, ...data.filed.map((d) => d.reports));
  const total = data.filed.reduce((t, d) => t + d.reports, 0);

  return (
    <Card>
      <CardHeader
        title="Filed over the last eight weeks"
        subtitle={`${n(total)} claim${total === 1 ? "" : "s"} · busiest day: ${n(peak)}`}
      />
      <div className="px-4 py-4">
        <div className="flex h-24 items-end gap-[2px]" role="img"
          aria-label={`Daily filings over ${data.filed.length} days, peaking at ${peak}`}>
          {data.filed.map((d) => (
            <div
              key={d.day}
              title={`${d.day}: ${d.reports}`}
              className="flex-1 rounded-t-sm bg-chart-moving"
              // A day with nothing filed keeps a hairline rather than
              // vanishing, or the axis reads as a gap in the record.
              style={{ height: `${Math.max(2, (d.reports / peak) * 100)}%` }}
            />
          ))}
        </div>
        <div className="mt-2 flex justify-between text-xs text-faint">
          <span>{data.filed[0]?.day}</span>
          <span>{data.filed[data.filed.length - 1]?.day}</span>
        </div>
      </div>
    </Card>
  );
}

function Districts({ data }: { data: Statewide }) {
  return (
    <Card>
      <CardHeader
        title="Districts"
        subtitle="Ordered by area affected. A district with eight hundred large claims is a bigger problem than one with four thousand small ones."
      />
      <div className="overflow-x-auto">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>District</TableHead>
              <TableHead className="text-right">Claims</TableHead>
              <TableHead className="text-right">Open</TableHead>
              <TableHead className="text-right">Contested</TableHead>
              <TableHead className="text-right">Late</TableHead>
              <TableHead className="text-right">Area (ha)</TableHead>
              <TableHead className="text-right">Declared</TableHead>
              <TableHead className="text-right">Decided</TableHead>
              <TableHead className="text-right">Villages</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.districts.map((d) => (
              <TableRow key={d.code}>
                <TableCell className="font-medium">{d.name}</TableCell>
                <TableCell className="tnum text-right">{n(d.reports)}</TableCell>
                <TableCell
                  className={
                    "tnum text-right " + (d.unresolved > 0 ? "text-sev-severe" : "")
                  }>
                  {n(d.unresolved)}
                </TableCell>
                <TableCell className="tnum text-right">{n(d.contested)}</TableCell>
                <TableCell className="tnum text-right">{n(d.filed_late)}</TableCell>
                <TableCell className="tnum text-right">{ha(d.area_affected_ha)}</TableCell>
                <TableCell className="tnum text-right">{pct(d.declared_percent)}</TableCell>
                {/* An em dash, not 0%. Nothing decided and no damage found are
                    different answers and must not look alike. */}
                <TableCell className="tnum text-right">
                  {d.adjudicated_percent > 0 ? pct(d.adjudicated_percent) : "—"}
                </TableCell>
                <TableCell className="tnum text-right">{n(d.villages)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </Card>
  );
}

function Crops({ data }: { data: Statewide }) {
  const peak = Math.max(1, ...data.crops.map((c) => c.area_affected_ha));
  return (
    <Card>
      <CardHeader
        title="Crops"
        subtitle="The same claims, cut by what was growing."
      />
      <div className="divide-y divide-border">
        {data.crops.map((c) => (
          <div key={c.crop} className="px-4 py-3">
            <div className="flex items-baseline justify-between gap-4 text-sm">
              <span className="font-medium">{c.crop}</span>
              <span className="tnum text-muted-foreground">
                {ha(c.area_affected_ha)} ha · {n(c.reports)} claims ·{" "}
                {pct(c.declared_percent)} declared
              </span>
            </div>
            <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted">
              <div
                className="h-full rounded-full bg-chart-moving"
                style={{ width: `${(c.area_affected_ha / peak) * 100}%` }}
              />
            </div>
          </div>
        ))}
      </div>
    </Card>
  );
}

function Freshness({ data }: { data: Statewide }) {
  return (
    <p className="text-xs leading-relaxed text-faint">
      Read from the rollup tables, refreshed about once a minute.
      {data.refreshed_at && <> Oldest figure here: {istDateTime(data.refreshed_at)}.</>}
      {data.pending_refresh > 0 && (
        <>
          {" "}
          {n(data.pending_refresh)} village
          {data.pending_refresh === 1 ? "" : "s"} waiting to be recounted, so
          the most recent filings may not be included.
        </>
      )}{" "}
      <Link href="/reports/" className="text-primary hover:underline">
        The claims themselves
      </Link>{" "}
      are always current.
    </p>
  );
}

function n(v: number): string {
  return v.toLocaleString("en-IN");
}

function ha(v: number): string {
  return v.toLocaleString("en-IN", {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  });
}

function pct(v: number): string {
  return `${v.toFixed(1)}%`;
}
