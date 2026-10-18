"use client";

import Link from "next/link";
import { useState } from "react";
import { api } from "@/lib/api";
import type { AssessmentSummary } from "@/lib/types";
import { usePagedResource } from "@/lib/usePagedResource";
import { NativeSelect } from "@/components/ui/native-select";
import { NoMatches, TablePager, TableSearch } from "@/components/TableControls";
import {
  formatDate,
  formatDateTime,
  confidenceLabel,
  damageFigure,
  triageStyles,
} from "@/lib/format";
import {
  Button,
  Card,
  CardHeader,
  EmptyState,
  ErrorPanel,
  CropPresenceBadge,
  Skeleton,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  TriageBadge,
} from "@/components/ui";
import { NewAssessment } from "@/components/NewAssessment";
import { useSession } from "@/components/SessionProvider";
import { PageHeader } from "@/components/dashboard";
import { Plus } from "lucide-react";

export default function AssessmentsPage() {
  const [composing, setComposing] = useState(false);

  // Triage is the one an officer actually filters on: "show me what was
  // escalated" is the whole reason this page is opened on a busy morning. The
  // server has accepted the parameter all along.
  const [triage, setTriage] = useState("");

  // Requesting an assessment is assessment:create, which the officers who
  // work a claim hold and the ranks above them, who read the result, do not.
  const { can } = useSession();
  const canRun = can("assessment:create");

  const table = usePagedResource<AssessmentSummary>(
    (a) => api.listAssessments(a, triage ? { triage } : {}),
    { deps: [triage] },
  );
  const { rows: items, error, reload, loading, query } = table;

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Assessments"
        title="Damage assessments"
        subtitle="Estimates awaiting dual-officer review."
        actions={
          // Only for somebody who may actually run one.
          //
          // A State Officer reads assessments and does not request them, so
          // this button used to be an invitation to a FORBIDDEN: the form
          // opened, the parcel was chosen, the date was typed, and the server
          // refused at the end of it. Offering an action nobody can complete
          // is worse than not offering it -- the refusal arrives after the
          // work, and reads as a fault rather than as a rule.
          canRun ? (
            <Button
              onClick={() => setComposing((v) => !v)}
              variant={composing ? "outline" : "default"}
            >
              {composing ? "Cancel" : <><Plus className="size-4" /> New assessment</>}
            </Button>
          ) : null
        }
      />

      {composing && canRun && (
        <Card>
          <CardHeader
            title="Run an assessment"
            subtitle="Select a parcel and the date of the damage event."
          />
          <NewAssessment onDone={reload} />
        </Card>
      )}

      {error && (
        <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
      )}

      <Card>
        <CardHeader
          title="All assessments"
          subtitle="Newest first"
          action={
            <div className="flex flex-wrap items-center justify-end gap-2">
              <TableSearch
                value={table.query}
                onChange={table.setQuery}
                busy={table.searching}
                placeholder="Farm id, survey number or crop"
              />
              <div className="w-44 shrink-0">
                <NativeSelect
                  selectSize="lg"
                  aria-label="Filter by triage tier"
                  value={triage}
                  onChange={(e) => setTriage(e.target.value)}>
                  <option value="">All triage tiers</option>
                  {Object.entries(triageStyles).map(([k, v]) => (
                    <option key={k} value={k}>
                      {v.label}
                    </option>
                  ))}
                </NativeSelect>
              </div>
            </div>
          }
        />

        {loading && (
          <div className="space-y-3 px-4 py-5">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-12 w-full" />
            ))}
          </div>
        )}

        {!loading && items.length === 0 && query !== "" && (
          <NoMatches query={query} noun="assessments" />
        )}

        {!loading && items.length === 0 && query === "" && (
          <EmptyState
            title="No assessments yet"
            body="Run one against a parcel to see the satellite series, terrain analysis and damage estimate."
            action={
              <Button onClick={() => setComposing(true)}>
                New assessment
              </Button>
            }
          />
        )}

        {!loading && items.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow>
                {[
                  "Parcel",
                  "Farmer",
                  "Incident",
                  "Damage",
                  "Crop",
                  "Flags",
                  "Confidence",
                  "Triage",
                  "Created",
                ].map((h) => (
                  <TableHead key={h} className="text-xs">
                    {h}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((a) => (
                <TableRow key={a.id}>
                  <TableCell>
                    <Link
                      href={`/assessment/?id=${a.id}`}
                      className="font-medium hover:text-primary"
                    >
                      Survey {a.survey_number}
                    </Link>
                    <div className="mono text-xs text-faint">
                      {a.farm_id}
                    </div>
                  </TableCell>
                  <TableCell className="mono text-xs text-muted-foreground">
                    {a.farmer_id}
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {formatDate(a.incident_date)}
                  </TableCell>
                  <TableCell className="tnum font-medium">
                    <span
                      className={
                        a.damage_percent === null ? "text-faint" : undefined
                      }
                    >
                      {damageFigure(a.damage_percent, 2)}
                    </span>
                  </TableCell>
                  <TableCell>
                    <CropPresenceBadge presence={a.crop_presence} showHint />
                  </TableCell>
                  <TableCell>
                    {a.fraud_count === 0 ? (
                      <span className="text-xs text-faint">none</span>
                    ) : (
                      <span
                        className={
                          a.critical_fraud
                            ? "text-sm font-medium text-sev-catastrophic"
                            : "text-sm text-sev-moderate"
                        }
                      >
                        {a.fraud_count}
                        {a.critical_fraud && " critical"}
                      </span>
                    )}
                  </TableCell>
                  <TableCell>
                    <span className="tnum">{a.confidence_score.toFixed(2)}</span>
                    <span className="ml-1.5 text-xs text-faint">
                      {confidenceLabel(a.confidence_score)}
                    </span>
                  </TableCell>
                  <TableCell>
                    <TriageBadge tier={a.triage_tier} showHint />
                  </TableCell>
                  <TableCell className="text-xs text-faint">
                    {formatDateTime(a.created_at)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}

        <TablePager resource={table} noun="assessments" />
      </Card>
    </div>
  );
}
