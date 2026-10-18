"use client";
import { useState } from "react";

import Link from "next/link";

import { api } from "@/lib/api";
import type { FarmerSummary } from "@/lib/types";
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

/**
 * The farmers in this officer's villages.
 *
 * This page used to fetch every farmer id and then request each record in
 * turn — one round trip per farmer, and every parcel, crop and joint owner
 * in the registry pulled into the browser to render a name and a village. It
 * was also unfiltered, so a Talathi posted to one village received the whole
 * registry. Both are fixed on the server; what changed here is that the page
 * asks for a page.
 *
 * A table rather than the card grid it was. Cards are for a handful of things
 * to compare; this is a list to search and page through, and at a district's
 * worth of households the cards were the wrong shape long before the loading
 * was.
 */
export default function FarmersPage() {
  // "Which of my farmers cannot be assessed" is the question this page is
  // opened to answer, and until now the only way to answer it was to read
  // every row.
  const [boundary, setBoundary] = useState("");

  const table = usePagedResource<FarmerSummary>(
    (a) => api.listFarmers(a, boundary ? { boundary } : {}),
    { deps: [boundary] },
  );
  const { rows, error, reload, loading, query } = table;

  return (
    <div>
      <PageHeader
        eyebrow="Registry"
        title="Farmers"
        subtitle="Records from the AgriStack source, limited to the villages you are posted to."
      />

      {error && (
        <div className="mb-6">
          <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
        </div>
      )}

      <Card>
        <CardHeader
          title="Registry records"
          subtitle="By name"
          action={
            <div className="flex flex-wrap items-center justify-end gap-2">
              <TableSearch
                value={table.query}
                onChange={table.setQuery}
                busy={table.searching}
                placeholder="Name, farmer id, village or mobile"
              />
              <div className="w-48 shrink-0">
                <NativeSelect
                  selectSize="lg"
                  aria-label="Filter by boundary coverage"
                  value={boundary}
                  onChange={(e) => setBoundary(e.target.value)}>
                  <option value="">All farmers</option>
                  <option value="missing">Missing a boundary</option>
                  <option value="complete">All parcels mapped</option>
                </NativeSelect>
              </div>
            </div>
          }
        />

        {loading && (
          <div className="space-y-2 p-5">
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-9 w-full" />
          </div>
        )}

        {!loading && rows.length === 0 && query !== "" && (
          <NoMatches query={query} noun="farmers" />
        )}

        {!loading && rows.length === 0 && query === "" && (
          <EmptyState
            title="No farmer records"
            body="Either the registry directory is empty, or no farmer is recorded in the villages you are posted to."
          />
        )}

        {!loading && rows.length > 0 && (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  {["Farmer", "Farmer ID", "Village LGD", "Parcels", "Extent", "Crops", ""].map(
                    (h, i) => (
                      <TableHead key={i} className="text-xs">
                        {h}
                      </TableHead>
                    ),
                  )}
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((f) => (
                  <TableRow key={f.farmer_id}>
                    <TableCell className="font-medium text-foreground">
                      {f.farmer_name}
                    </TableCell>
                    <TableCell className="mono text-xs text-muted-foreground">
                      {f.farmer_id}
                    </TableCell>
                    <TableCell className="mono text-xs text-muted-foreground">
                      {f.village_lgd_code}
                    </TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      {f.parcels}
                      {/* A parcel without a boundary cannot be assessed at
                          all, so the gap belongs in the row rather than one
                          click further in. */}
                      {f.parcels_with_boundary < f.parcels && (
                        <span className="ml-1.5 text-xs text-sev-moderate">
                          {f.parcels - f.parcels_with_boundary} without boundary
                        </span>
                      )}
                    </TableCell>
                    <TableCell className="text-sm tabular-nums text-muted-foreground">
                      {f.extent_hectares.toFixed(2)} ha
                    </TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      {f.crops}
                    </TableCell>
                    <TableCell className="text-right">
                      <Link
                        href={`/farmer/?id=${encodeURIComponent(f.farmer_id)}`}
                        className="text-xs font-medium text-primary hover:underline"
                      >
                        Open
                      </Link>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}

        <TablePager resource={table} noun="farmers" />
      </Card>
    </div>
  );
}
