"use client";

import { useState } from "react";
import { ShieldAlert } from "lucide-react";

import { api } from "@/lib/api";
import type { AuditEntry } from "@/lib/types";
import { usePagedResource } from "@/lib/usePagedResource";
import { NoMatches, TablePager, TableSearch } from "@/components/TableControls";

import { formatDateTime, roleLabel } from "@/lib/format";
import {
  Card,
  CardHeader,
  EmptyState,
  ErrorPanel,
  Note,
  Skeleton,
} from "@/components/ui";
import { Input } from "@/components/ui/input";
import { PageHeader } from "@/components/dashboard";
import { NativeSelect } from "@/components/ui/native-select";

const OUTCOME_STYLE: Record<string, string> = {
  allow: "bg-sev-negligible-bg text-sev-negligible",
  deny: "bg-sev-catastrophic-bg text-sev-catastrophic",
  error: "bg-sev-moderate-bg text-sev-moderate",
};

/**
 * The audit trail.
 *
 * An audit log that needs a database console is one nobody checks. The table
 * refuses UPDATE and DELETE at the database, so this is a window on evidence
 * rather than on a mutable log.
 */
export default function AuditPage() {
  const [action, setAction] = useState("");
  const [outcome, setOutcome] = useState("");

  const table = usePagedResource<AuditEntry>(
    (a) =>
      api.audit(a, {
        action: action || undefined,
        outcome: outcome || undefined,
      }),
    { deps: [action, outcome] },
  );
  const { error, reload, loading, query } = table;
  const entries = table.rows;

  // Across this page only, and labelled as such below. A denial count over
  // the whole trail would be a different query, and a number that silently
  // meant one thing or the other would be worse than none.
  const denials = entries.filter((e) => e.outcome === "deny").length;

  return (
    <div>
      <PageHeader
        eyebrow="Administration"
        title="Audit trail"
        subtitle="Every sign-in, refusal, grant and record opened. Append-only at the database."
      />

      {error && (
        <div className="mb-6">
          <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
        </div>
      )}

      <div className="mb-4 flex flex-wrap items-center gap-3">
        <Input
          value={action}
          onChange={(e) => setAction(e.target.value)}
          placeholder="Filter by action, e.g. report.verified"
          className="max-w-xs"
        />
        <NativeSelect selectSize="lg" value={outcome} onChange={(e) => setOutcome(e.target.value)}>
          <option value="">All outcomes</option>
          <option value="allow">Allowed</option>
          <option value="deny">Refused</option>
          <option value="error">Error</option>
        </NativeSelect>
        {denials > 0 && (
          <span className="inline-flex items-center gap-1.5 text-xs text-sev-catastrophic">
            <ShieldAlert className="size-3.5" />
            {denials} refusals in view
          </span>
        )}
      </div>

      <Card>
        <CardHeader
          title="Recent activity"
          subtitle="Newest first"
          action={
            <TableSearch
              value={table.query}
              onChange={table.setQuery}
              busy={table.searching}
              placeholder="Action, officer or record id"
            />
          }
        />

        {loading && (
          <div className="space-y-2 p-5">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
          </div>
        )}

        {!loading && entries.length === 0 && query !== "" && (
          <NoMatches query={query} noun="entries" />
        )}

        {!loading && entries.length === 0 && query === "" && (
          <EmptyState title="Nothing matches" body="No entries for that filter." />
        )}

        {!loading && entries.length > 0 && (
          <ul className="divide-y divide-border">
            {entries.map((e) => (
              <li key={e.id} className="px-4 py-3">
                <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
                  <span className={`rounded px-1.5 py-0.5 text-xs font-semibold uppercase tracking-wide ${OUTCOME_STYLE[e.outcome]}`}>
                    {e.outcome}
                  </span>
                  <span className="mono text-sm font-medium">{e.action}</span>
                  {e.actor_label && (
                    <span className="text-sm text-muted-foreground">
                      {e.actor_label}
                      {e.actor_role && (
                        <span className="text-faint"> · {roleLabel(e.actor_role)}</span>
                      )}
                    </span>
                  )}
                  {e.jurisdiction_code && (
                    <span className="mono text-xs text-faint">
                      {e.jurisdiction_code}
                    </span>
                  )}
                  <span className="ml-auto text-xs text-faint">
                    {formatDateTime(e.at)}
                  </span>
                </div>

                {e.resource_type && (
                  <div className="mono mt-0.5 text-xs text-faint">
                    {e.resource_type} {e.resource_id}
                  </div>
                )}

                {Object.keys(e.detail ?? {}).length > 0 && (
                  <dl className="mt-1.5 flex flex-wrap gap-x-4 gap-y-0.5">
                    {Object.entries(e.detail).map(([k, v]) => (
                      <div key={k} className="text-xs">
                        <span className="text-faint">{k}</span>{" "}
                        <span className="text-muted-foreground">
                          {typeof v === "object" ? JSON.stringify(v) : String(v)}
                        </span>
                      </div>
                    ))}
                  </dl>
                )}
              </li>
            ))}
          </ul>
        )}

        <TablePager resource={table} noun="entries" />
      </Card>

      <div className="mt-3">
        <Note>
          Refusals are recorded, not merely counted. &ldquo;Who tried to verify
          a report outside their village&rdquo; is a question that gets asked,
          and it cannot be answered later from a metric.
        </Note>
      </div>
    </div>
  );
}
