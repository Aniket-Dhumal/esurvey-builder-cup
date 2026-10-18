"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { Clock, FileText, Landmark, Layers, Siren } from "lucide-react";

import { api } from "@/lib/api";
import type { AreaRollup, Overview } from "@/lib/types";
import { Card, CardHeader, Skeleton } from "@/components/ui";
import { KpiCard } from "@/components/dashboard";

/**
 * What is on this officer's patch, and what is waiting on someone.
 *
 * The same component for every role. A State Officer sees districts here and
 * a Talathi sees one village, because the server scoped the answer to their
 * jurisdiction — not because the console decided what their rank should be
 * shown. That keeps one thing true in one place: the officer's posting is the
 * only thing that determines what they can see.
 *
 * The counters are counts of *work*, not of records. "Awaiting
 * acknowledgement" is a queue with an owner and a farmer waiting at the end
 * of it; "reports filed" is trivia by comparison, so it is the smaller
 * number on the page.
 */

const STATE_LABELS: Record<string, string> = {
  draft: "Draft",
  submitted: "Submitted",
  acknowledged: "Acknowledged",
  assigned: "Allocated",
  under_verification: "Being verified",
  verified: "Verified",
  adjudicated: "Adjudicated",
  closed: "Closed",
  withdrawn: "Withdrawn",
};

/**
 * What each stage's column is coloured with.
 *
 * Blue while a report is moving, yellow while it waits on a person, green once
 * it is decided, grey for the states that are nobody's work.
 *
 * # Why these are chart fills and not the severity ramp
 *
 * "Verified" used to be `bg-sev-moderate`, which is #854d0e. Those four
 * severity colours are *text* weights, picked to be legible on their own pale
 * backgrounds — that one reads as amber in a six-word label and as a mud-brown
 * slab when it fills a bar. So the single stage an officer most needs to
 * notice, the reports sitting verified and waiting to be adjudicated, was the
 * one painted a colour that appears nowhere else in the console.
 *
 * It was also the wrong meaning. That ramp says how badly a crop was damaged.
 * A pipeline stage is not a severity, and borrowing the scale quietly claimed
 * a verified report was moderately damaged.
 */
const STATE_TONE: Record<string, string> = {
  draft: "bg-chart-inert",
  submitted: "bg-chart-moving",
  acknowledged: "bg-chart-moving",
  assigned: "bg-chart-moving",
  under_verification: "bg-chart-moving",
  verified: "bg-chart-waiting",
  adjudicated: "bg-chart-done",
  closed: "bg-chart-inert",
  withdrawn: "bg-chart-inert",
};

// The order work actually moves in. Sorting by count instead would reshuffle
// the row every time a report advanced, which makes a page nobody can scan.
const STATE_ORDER = [
  "draft",
  "submitted",
  "acknowledged",
  "assigned",
  "under_verification",
  "verified",
  "adjudicated",
  "closed",
  "withdrawn",
];

function ha(v: number): string {
  return v.toLocaleString("en-IN", {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  });
}

function AreaTable({ areas }: { areas: AreaRollup[] }) {
  const level = areas[0]?.level ?? "";
  const heading =
    level === "district"
      ? "Districts"
      : level === "sub_district"
        ? "Talukas"
        : "Villages";

  return (
    <Card>
      <CardHeader
        title={heading}
        subtitle="Sorted by what is still open. Areas with nothing filed do not appear."
      />
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b text-left text-xs text-muted-foreground">
              <th className="px-4 py-2 font-medium">{heading.slice(0, -1)}</th>
              <th className="px-4 py-2 text-right font-medium">Reports</th>
              <th className="px-4 py-2 text-right font-medium">Households</th>
              <th className="px-4 py-2 text-right font-medium">Area (ha)</th>
              <th className="px-4 py-2 text-right font-medium">Open</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {[...areas]
              .sort((a, b) => b.unresolved - a.unresolved || b.reports - a.reports)
              .map((a) => (
                <tr key={a.code}>
                  <td className="px-4 py-2.5">
                    <span className="font-medium text-foreground">{a.name}</span>
                    <span className="mono ml-2 text-xs text-faint">
                      LGD {a.code}
                    </span>
                  </td>
                  <td className="px-4 py-2.5 text-right tabular-nums text-muted-foreground">
                    {a.reports}
                  </td>
                  <td className="px-4 py-2.5 text-right tabular-nums text-muted-foreground">
                    {a.farmers}
                  </td>
                  <td className="px-4 py-2.5 text-right tabular-nums text-muted-foreground">
                    {ha(a.area_hectares)}
                  </td>
                  <td className="px-4 py-2.5 text-right tabular-nums">
                    {a.unresolved > 0 ? (
                      <span className="font-medium text-sev-moderate">
                        {a.unresolved}
                      </span>
                    ) : (
                      <span className="text-faint">—</span>
                    )}
                  </td>
                </tr>
              ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

/**
 * Where the reports are, as columns.
 *
 * # Why vertical
 *
 * The stages are a sequence — a report enters at the left and leaves at the
 * right — and a column chart puts that sequence along the axis people already
 * read time on. The horizontal bars this replaces sorted the same stages down
 * the page, which reads as a ranking, and the widths were measured against
 * the page rather than against each other.
 *
 * # Why each column carries its stage's own colour
 *
 * The bars were all one grey, so the chart said how many and never what kind.
 * Blue for the stages where a report is simply moving, yellow where it is
 * waiting on a person, green where it is done. An officer can now see from
 * across the desk whether their district is flowing or piling up in one place,
 * which is the only question this panel is for.
 *
 * # Why the tallest column sets the scale
 *
 * Not the total. With eleven of nineteen reports in one stage, scaling against
 * nineteen leaves every other column a stub two pixels tall — the shape the
 * old bars had. Scaling to the largest stage keeps the small ones legible and
 * still shows the pile.
 */
function Pipeline({ o }: { o: Overview }) {
  const present = STATE_ORDER.filter((s) => (o.reports[s] ?? 0) > 0);
  if (present.length === 0) return null;

  const peak = Math.max(...present.map((s) => o.reports[s] ?? 0), 1);

  return (
    <Card>
      <CardHeader
        title="Where the reports are"
        subtitle="In the order work moves, not by size"
      />

      <div className="p-4">
        <div className="flex items-end gap-2.5" style={{ height: 148 }}>
          {present.map((st) => {
            const n = o.reports[st] ?? 0;
            const tone = STATE_TONE[st] ?? "bg-chart-inert";
            return (
              <div
                key={st}
                className="flex min-w-0 flex-1 flex-col items-center justify-end gap-1.5"
              >
                <span className="text-xs font-semibold tabular-nums text-foreground">
                  {n}
                </span>
                <div
                  role="img"
                  aria-label={`${STATE_LABELS[st] ?? st}: ${n} report${n === 1 ? "" : "s"}`}
                  title={`${STATE_LABELS[st] ?? st} — ${n}`}
                  // The hairline is what makes the yellow column legible;
                  // see --chart-edge. Bottom border omitted: the axis rule
                  // below already draws it, and two lines there read as a
                  // gap.
                  className={`w-full max-w-12 rounded-t-sm border border-b-0 border-chart-edge ${tone}`}
                  // A floor of 6px, so a stage holding one report is still a
                  // column somebody can see and point at rather than a line.
                  style={{ height: `${Math.max((n / peak) * 116, 4)}px` }}
                />
              </div>
            );
          })}
        </div>

        {/* Labels below the axis, angled only if they would otherwise collide.
            Kept horizontal at these counts because rotated text is harder to
            read and there is room for it. */}
        <div className="mt-2 flex gap-2.5 border-t border-border pt-2">
          {present.map((st) => (
            <span
              key={st}
              className="min-w-0 flex-1 text-center text-xs leading-tight text-muted-foreground"
            >
              {STATE_LABELS[st] ?? st}
            </span>
          ))}
        </div>
      </div>
    </Card>
  );
}

export function CaseloadOverview({ eventId }: { eventId?: string }) {
  const [o, setO] = useState<Overview | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let live = true;
    api
      .overview(eventId)
      .then((r) => live && setO(r))
      // The rollup is a summary of things reachable elsewhere on their own
      // pages. If it cannot be built, the rest of the console still works, so
      // this stays quiet rather than taking the page down with an error card.
      .catch(() => live && setFailed(true));
    return () => {
      live = false;
    };
  }, [eventId]);

  if (failed) return null;

  if (!o) {
    return (
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-24" />
        ))}
      </div>
    );
  }

  const waiting = o.awaiting_acknowledgement + o.awaiting_inspection;

  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <KpiCard
          label="Awaiting action"
          value={waiting}
          icon={Clock}
          href="/reports"
          tone={waiting > 0 ? "warn" : "default"}
        />
        <KpiCard
          label="Reports filed"
          value={o.reports_total}
          icon={FileText}
          href="/reports"
        />
        <KpiCard
          label="Area claimed"
          value={ha(o.area_hectares)}
          unit="ha"
          icon={Layers}
          href="/reports"
        />
        <KpiCard
          label="Events running"
          value={o.open_events}
          icon={Siren}
          href="/events"
        />
      </div>

      {(o.awaiting_acknowledgement > 0 || o.filed_late > 0) && (
        <div className="flex flex-wrap gap-x-6 gap-y-1 px-1 text-sm text-muted-foreground">
          {o.awaiting_acknowledgement > 0 && (
            <span>
              <strong className="font-medium text-foreground">
                {o.awaiting_acknowledgement}
              </strong>{" "}
              {o.awaiting_acknowledgement === 1 ? "farmer is" : "farmers are"}{" "}
              waiting on a पोहोच पावती —{" "}
              <Link href="/reports?state=submitted" className="underline underline-offset-2">
                acknowledge
              </Link>
            </span>
          )}
          {o.filed_late > 0 && (
            <span>
              <strong className="font-medium text-foreground">{o.filed_late}</strong>{" "}
              filed outside the event window, to be adjudicated separately
            </span>
          )}
        </div>
      )}

      <div className="grid items-start gap-4 lg:grid-cols-2">
        <Pipeline o={o} />
        {o.areas.length > 0 ? (
          <AreaTable areas={o.areas} />
        ) : (
          <Card>
            <CardHeader
              title="Your posting"
              subtitle="One area, so there is nothing to break down"
            />
            <p className="flex items-center gap-2 px-4 py-3.5 text-sm text-muted-foreground">
              <Landmark className="size-4 shrink-0 text-faint" />
              {o.farmers} household{o.farmers === 1 ? "" : "s"} have filed,
              covering {ha(o.area_hectares)} ha.
            </p>
          </Card>
        )}
      </div>
    </div>
  );
}
