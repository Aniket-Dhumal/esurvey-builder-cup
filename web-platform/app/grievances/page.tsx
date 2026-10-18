"use client";

import Link from "next/link";
import { useState } from "react";

import { api, ApiError } from "@/lib/api";
import { DecideAppealDialog } from "@/components/DecideAppealDialog";
import type { Grievance, GrievanceState } from "@/lib/types";
import { formatDate, formatDateTime } from "@/lib/format";
import { usePagedResource } from "@/lib/usePagedResource";
import { NoMatches, TablePager, TableSearch } from "@/components/TableControls";
import { PageHeader } from "@/components/dashboard";
import { useSession } from "@/components/SessionProvider";
import {
  Button,
  Card,
  CardHeader,
  EmptyState,
  ErrorPanel,
  Skeleton,
} from "@/components/ui";

/**
 * Appeals waiting on an officer.
 *
 * Ordered by deadline rather than by filing date, because the queue's job is
 * to stop one being forgotten and the one closest to its deadline is the one
 * at risk. An appeal nobody answers is worse for a farmer than one that is
 * rejected: a rejection at least tells them where they stand.
 */

const STATE_STYLE: Record<GrievanceState, { label: string; tone: string }> = {
  open: { label: "Open", tone: "bg-sev-moderate-bg text-sev-moderate" },
  under_review: { label: "Under review", tone: "bg-muted text-muted-foreground" },
  upheld: { label: "Upheld", tone: "bg-sev-negligible-bg text-sev-negligible" },
  partly_upheld: {
    label: "Partly upheld",
    tone: "bg-sev-negligible-bg text-sev-negligible",
  },
  rejected: { label: "Rejected", tone: "bg-muted text-muted-foreground" },
  withdrawn: { label: "Withdrawn", tone: "bg-muted text-faint" },
};

const GROUND_LABEL: Record<string, string> = {
  area_wrong: "Area assessed is wrong",
  damage_percent_low: "Damage recorded is too low",
  not_inspected: "Field was never inspected",
  evidence_ignored: "Photographs were not considered",
  other: "Other",
};

function Row({ g, onChanged }: { g: Grievance; onChanged: () => void }) {
  const { can, user } = useSession();
  const style = STATE_STYLE[g.state];
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [deciding, setDeciding] = useState(false);

  const mine = g.assigned_to === user?.user_id;
  const open = g.state === "open" || g.state === "under_review";

  async function take() {
    setBusy(true);
    setError(null);
    try {
      await api.assignGrievance(g.id);
      onChanged();
    } catch (e) {
      // The one refusal worth explaining rather than swallowing: it is the
      // rule that makes this an appeal, not a bug in the button.
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
    <li className="px-4 py-4">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <Link
          href={`/verify?report=${encodeURIComponent(g.report_id)}`}
          className="text-sm font-medium text-foreground underline-offset-2 hover:underline">
          {g.reference}
        </Link>
        <span className="flex items-center gap-2">
          {g.overdue && open && (
            <span className="rounded px-1.5 py-0.5 text-xs font-semibold uppercase tracking-wide text-sev-catastrophic">
              overdue
            </span>
          )}
          <span
            className={`rounded px-1.5 py-0.5 text-xs font-medium ${style.tone}`}>
            {style.label}
          </span>
        </span>
      </div>

      <p className="mt-0.5 text-sm text-muted-foreground">
        {g.farmer_name} · {g.village_name ?? g.village_lgd_code}
        {g.report_reference && (
          <>
            {" · against "}
            <span className="mono text-xs">{g.report_reference}</span>
          </>
        )}
      </p>

      <p className="mt-1.5 text-sm font-medium text-foreground">
        {GROUND_LABEL[g.ground] ?? g.ground}
      </p>
      {/* The farmer's own words. The category alone cannot carry "they
          measured the wrong survey number", and that sentence is usually the
          whole grievance. */}
      <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
        “{g.statement}”
      </p>

      <div className="mt-2 flex flex-wrap items-baseline gap-x-5 gap-y-1 text-xs text-faint">
        {g.disputed_percent != null && (
          <span>
            disputing{" "}
            <span className="tabular-nums text-muted-foreground">
              {g.disputed_percent.toFixed(1)}%
            </span>
          </span>
        )}
        {g.claimed_percent != null && (
          <span>
            claims{" "}
            <span className="tabular-nums text-muted-foreground">
              {g.claimed_percent.toFixed(1)}%
            </span>
          </span>
        )}
        <span>filed {formatDate(g.filed_at)}</span>
        {open && <span>due {formatDate(g.due_at)}</span>}
        {g.assigned_to_name && <span>with {g.assigned_to_name}</span>}
      </div>

      {g.decision_reason && (
        <p className="mt-2 rounded-md bg-muted px-3 py-2 text-sm leading-relaxed text-muted-foreground">
          <span className="font-medium text-foreground">
            {g.decided_by_name || "Decided"}
            {g.decided_at ? ` · ${formatDateTime(g.decided_at)}` : ""}
          </span>
          <br />
          {g.decision_reason}
          {g.revised_percent != null && (
            <>
              {" "}
              Revised to{" "}
              <span className="tabular-nums font-medium text-foreground">
                {g.revised_percent.toFixed(1)}%
              </span>
              .
            </>
          )}
        </p>
      )}

      {error && (
        <p role="alert" className="mt-2 text-sm text-sev-catastrophic">
          {error}
        </p>
      )}

      <div className="mt-2.5 flex flex-wrap gap-2">
        {open && can("grievance:assign") && !mine && (
          <Button
            size="sm"
            variant="outline"
            onClick={take}
            disabled={busy}>
            {busy ? "Taking…" : "Take this appeal"}
          </Button>
        )}

        {/* The half that was missing. An officer could take an appeal and
            there was no way to answer it, so every appeal a farmer filed
            ended here -- on the one route they have to contest a decision. */}
        {open && mine && can("grievance:decide") && (
          <Button size="sm" onClick={() => setDeciding(true)}>
            Answer this appeal
          </Button>
        )}
      </div>

      {deciding && (
        <DecideAppealDialog
          grievance={g}
          onClose={() => setDeciding(false)}
          onDone={async () => {
            setDeciding(false);
            onChanged();
          }}
        />
      )}
    </li>
  );
}

export default function GrievancesPage() {
  const [openOnly, setOpenOnly] = useState(true);

  const table = usePagedResource<Grievance>(
    (a) => api.listGrievances(a, openOnly ? { open: "true" } : {}),
    { deps: [openOnly] },
  );
  const { rows, error, reload, loading, query } = table;

  return (
    <div>
      <PageHeader
        eyebrow="Claims"
        title="Grievances"
        subtitle="Appeals against a decision, closest deadline first."
      />

      {error && (
        <div className="mb-6">
          <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
        </div>
      )}

      <Card>
        <CardHeader
          title={openOnly ? "Waiting for an answer" : "All grievances"}
          subtitle="An appeal nobody answers is worse for a farmer than one that is rejected"
          action={
            <div className="flex flex-wrap items-center gap-2">
              <TableSearch
                value={table.query}
                onChange={table.setQuery}
                busy={table.searching}
                placeholder="Reference or farmer"
              />
              <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
                <input
                  type="checkbox"
                  checked={openOnly}
                  onChange={(e) => setOpenOnly(e.target.checked)}
                  className="size-4 accent-primary"
                />
                Open only
              </label>
            </div>
          }
        />

        {loading && (
          <div className="space-y-2 p-5">
            <Skeleton className="h-16 w-full" />
            <Skeleton className="h-16 w-full" />
          </div>
        )}

        {!loading && rows.length === 0 && query !== "" && (
          <NoMatches query={query} noun="grievances" />
        )}

        {!loading && rows.length === 0 && query === "" && (
          <EmptyState
            title={openOnly ? "Nothing waiting" : "No grievances"}
            body={
              openOnly
                ? "Every appeal in your area has been answered."
                : "No farmer in your area has appealed a decision."
            }
          />
        )}

        {!loading && rows.length > 0 && (
          <ul className="divide-y divide-border">
            {rows.map((g) => (
              <Row key={g.id} g={g} onChanged={reload} />
            ))}
          </ul>
        )}

        <TablePager resource={table} noun="grievances" />
      </Card>
    </div>
  );
}
