"use client";

import Link from "next/link";
import { api } from "@/lib/api";
import { usePagedResource } from "@/lib/usePagedResource";
import { NoMatches, TablePager, TableSearch } from "@/components/TableControls";
import type { AdjudicationEntry } from "@/lib/types";
import { calamityLabel, formatDate } from "@/lib/format";
import { PageHeader } from "@/components/dashboard";
import {
  Card,
  CardHeader,
  EmptyState,
  ErrorPanel,
  Skeleton,
} from "@/components/ui";

/**
 * Reports where the two verifiers did not agree.
 *
 * The platform could already adjudicate a report, and could show one once you
 * knew its reference. What it could not do was find them — so a report whose
 * verifiers diverged sat in `verified` indefinitely and the farmer heard
 * nothing. An arbitration path nobody can navigate to is an arbitration path
 * that does not run.
 *
 * Ordered by how long the farmer has been waiting, not by how far apart the
 * officers are. The widest disagreement is the most interesting case; the
 * oldest one is the one someone is owed an answer on.
 */

const ROUTE_COPY: Record<string, { label: string; tone: string; why: string }> = {
  escalation: {
    label: "Escalate",
    tone: "text-sev-catastrophic",
    why: "The two findings are far enough apart that neither can stand on its own.",
  },
  arbitration: {
    label: "Decide",
    tone: "text-sev-severe",
    why: "The findings differ by more than the pair can be said to agree on.",
  },
  consensus: {
    label: "Waiting",
    tone: "text-muted-foreground",
    why: "Not yet yours: the second verifier has still to file.",
  },
};

function pct(v: number | null): string {
  return v === null ? "—" : `${v.toFixed(1)}%`;
}

function Row({ e }: { e: AdjudicationEntry }) {
  const route = ROUTE_COPY[e.route] ?? ROUTE_COPY.consensus;
  const mine = e.route !== "consensus";

  return (
    <li className="px-4 py-4">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <Link
          href={`/verify?report=${encodeURIComponent(e.report_id)}`}
          className="text-sm font-medium text-foreground underline-offset-2 hover:underline"
        >
          {e.reference}
        </Link>
        <span className={`text-xs font-medium ${route.tone}`}>{route.label}</span>
      </div>

      <p className="mt-0.5 text-sm text-muted-foreground">
        {e.farmer_name} · {e.village_name}{" "}
        <span className="mono text-xs text-faint">LGD {e.village_lgd_code}</span>
      </p>

      <p className="mt-1 text-xs text-faint">
        {calamityLabel(e.calamity_type)} on {formatDate(e.occurred_at)}
      </p>

      <div className="mt-2 flex flex-wrap items-baseline gap-x-5 gap-y-1 text-sm">
        <span className="text-muted-foreground">
          Findings{" "}
          <span className="tabular-nums text-foreground">
            {pct(e.first_percent)}
          </span>
          {e.findings_filed >= 2 && (
            <>
              {" and "}
              <span className="tabular-nums text-foreground">
                {pct(e.second_percent)}
              </span>
            </>
          )}
        </span>

        {e.divergence !== null && (
          <span className="text-muted-foreground">
            apart by{" "}
            <span className="tabular-nums font-medium text-foreground">
              {e.divergence.toFixed(1)} points
            </span>
          </span>
        )}

        {e.rejected > 0 && (
          <span className="font-medium text-sev-severe">
            {e.rejected === 1
              ? "One officer rejected the claim"
              : `${e.rejected} officers rejected the claim`}
          </span>
        )}

        <span className="ml-auto text-xs text-faint">
          waiting {e.waiting_days} day{e.waiting_days === 1 ? "" : "s"}
        </span>
      </div>

      <p className="mt-1.5 text-xs leading-relaxed text-faint">
        {route.why}
        {e.rejected > 0 && mine && (
          <> A rejection is never averaged away — it needs a person.</>
        )}
      </p>
    </li>
  );
}

export default function AdjudicationPage() {
  const table = usePagedResource<AdjudicationEntry>((a) => api.adjudicationQueue(a));
  const { rows: items, error, reload, query } = table;

  // Split rather than filtered: an officer should be able to see what is
  // coming toward them, not only what has already landed. Hiding the second
  // group would make a quiet queue look like an empty one.
  const mine = (items ?? []).filter((e) => e.route !== "consensus");
  const coming = (items ?? []).filter((e) => e.route === "consensus");

  return (
    <div>
      <PageHeader
        eyebrow="Claims"
        title="Adjudication"
        subtitle="Reports whose verifiers did not agree, oldest first"
      />

      {error && (
        <div className="mb-6">
          <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
        </div>
      )}

      {!items && !error && <Skeleton className="h-48" />}

      {items && (
        <div className="space-y-4">
          <Card>
            <CardHeader
              title="Waiting on you"
              subtitle={mine.length === 1 ? "1 report" : `${mine.length} reports`}
              action={
                <TableSearch
                  value={table.query}
                  onChange={table.setQuery}
                  busy={table.searching}
                  placeholder="Reference or farmer"
                />
              }
            />
            {mine.length === 0 && query !== "" ? (
              <NoMatches query={query} noun="reports" />
            ) : mine.length === 0 ? (
              <EmptyState
                title="Nothing to decide"
                body="Every verified report in your jurisdiction has findings that agree."
              />
            ) : (
              <ul className="divide-y divide-border">
                {mine.map((e) => (
                  <Row key={e.report_id} e={e} />
                ))}
              </ul>
            )}
          </Card>

          {coming.length > 0 && (
            <Card>
              <CardHeader
                title="Still being verified"
                subtitle="One finding filed; these reach you only if the pair disagree"
              />
              <ul className="divide-y divide-border">
                {coming.map((e) => (
                  <Row key={e.report_id} e={e} />
                ))}
              </ul>
            </Card>
          )}
        <TablePager resource={table} noun="reports" />
        </div>
      )}
    </div>
  );
}
