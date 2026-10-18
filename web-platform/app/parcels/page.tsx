"use client";
import { useState } from "react";

import Link from "next/link";

import { api } from "@/lib/api";
import type { ParcelRow } from "@/lib/types";
import { usePagedResource } from "@/lib/usePagedResource";
import { NativeSelect } from "@/components/ui/native-select";
import { NoMatches, TablePager, TableSearch } from "@/components/TableControls";
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
import { Badge } from "@/components/ui/badge";

/**
 * Land records, one page at a time.
 *
 * This page used to fetch every farmer's whole record and join parcels to
 * owners in the browser -- one request per farmer, and a district holds
 * several times more parcels than farmers, so it was the worse of the two
 * N+1s. The join and the jurisdiction filter both happen on the server now.
 */
export default function ParcelsPage() {
  // A parcel with no boundary has no polygon to run satellite or radar
  // against, so it cannot be assessed at all. "without" is the working list
  // for chasing AgriStack over missing geometry.
  const [boundary, setBoundary] = useState("");
  const [crop, setCrop] = useState("");

  const table = usePagedResource<ParcelRow>(
    (a) =>
      api.listParcels(a, {
        ...(boundary ? { boundary } : {}),
        ...(crop ? { crop } : {}),
      }),
    { deps: [boundary, crop] },
  );
  const { rows, error, reload, loading, query } = table;

  const withoutBoundary = rows.filter((r) => !r.has_boundary).length;

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Registry"
        title="Parcels"
        subtitle="Land records from AgriStack, keyed by survey number."
      />

      {error && (
        <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
      )}

      <Card>
        <CardHeader
          title="All parcels"
          subtitle="By village, then survey number"
          action={
            <div className="flex flex-wrap items-center justify-end gap-2">
              <TableSearch
                value={table.query}
                onChange={table.setQuery}
                busy={table.searching}
                placeholder="Survey number, owner or village"
              />
              <div className="w-40 shrink-0">
                <NativeSelect
                  selectSize="lg"
                  aria-label="Filter by boundary"
                  value={boundary}
                  onChange={(e) => setBoundary(e.target.value)}>
                  <option value="">All parcels</option>
                  <option value="without">No boundary</option>
                  <option value="with">Has a boundary</option>
                </NativeSelect>
              </div>
              <div className="w-40 shrink-0">
                <NativeSelect
                  selectSize="lg"
                  aria-label="Filter by crop"
                  value={crop}
                  onChange={(e) => setCrop(e.target.value)}>
                  <option value="">Any crop</option>
                  <option value="undeclared">No crop declared</option>
                </NativeSelect>
              </div>
            </div>
          }
        />

        {loading && (
          <div className="space-y-3 p-5">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-12 w-full" />
            ))}
          </div>
        )}

        {!loading && rows.length === 0 && query !== "" && (
          <NoMatches query={query} noun="parcels" />
        )}

        {!loading && rows.length === 0 && query === "" && (
          <EmptyState
            title="No parcels"
            body="No land records are loaded for the villages you are posted to."
          />
        )}

        {!loading && rows.length > 0 && (
          <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                {[
                  "Survey",
                  "Farm ID",
                  "Owner",
                  "Village",
                  "Owner extent",
                  "Crop",
                  "Boundary",
                ].map((h) => (
                  <TableHead key={h} className="text-xs">
                    {h}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((p) => (
                // Farmer and farm together, because a farm id is not unique
                // here. This listing crosses farmers, and a survey plot held
                // on one khata appears once per owner -- in the first real
                // village loaded, 873 parcels over 436 distinct farm ids.
                // Keyed on the farm id alone, React saw the joint owners as
                // one row and dropped the rest.
                <TableRow key={`${p.farmer_id}::${p.farm_id}`}>
                  <TableCell className="font-medium">
                    {p.survey_number}
                  </TableCell>
                  <TableCell className="mono text-xs text-muted-foreground">
                    {p.farm_id}
                  </TableCell>
                  <TableCell>
                    <Link
                      href={`/farmer/?id=${encodeURIComponent(p.farmer_id)}`}
                      className="hover:text-primary"
                    >
                      {p.farmer_name}
                    </Link>
                    {p.joint_owners > 1 && (
                      <span className="ml-1.5 text-xs text-faint">
                        +{p.joint_owners - 1} joint
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="mono text-xs text-muted-foreground">
                    {p.village_lgd_code}
                  </TableCell>
                  <TableCell className="tnum">
                    {p.owner_extent_hectares.toFixed(2)} ha
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {p.crop_name ?? "—"}
                  </TableCell>
                  <TableCell>
                    {p.has_boundary ? (
                      <Badge
                        variant="secondary"
                        className="border-transparent bg-sev-negligible-bg text-sev-negligible"
                      >
                        Available
                      </Badge>
                    ) : (
                      <Badge
                        variant="secondary"
                        className="border-transparent bg-sev-moderate-bg text-sev-moderate"
                      >
                        Missing
                      </Badge>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          </div>
        )}

        <TablePager resource={table} noun="parcels" />
      </Card>

      {withoutBoundary > 0 && (
        <p className="text-xs leading-relaxed text-muted-foreground">
          Parcels marked <span className="font-medium">Missing</span> cannot be
          assessed. AgriStack returns <span className="mono">plot_geometry</span>{" "}
          as null on every deployment tested, so a boundary has to be traced in
          the field before the spatial pipeline can run.
        </p>
      )}
    </div>
  );
}
