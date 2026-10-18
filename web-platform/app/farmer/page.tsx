"use client";

import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { api } from "@/lib/api";
import { useResource } from "@/lib/useResource";
import { damageFigure, formatDate, maskedAadhaar } from "@/lib/format";
import {
  BackLink,
  Card,
  CardHeader,
  ErrorPanel,
  Field,
  CropPresenceBadge,
  Skeleton,
  TriageBadge,
} from "@/components/ui";
import { ParcelMap, polygonAreaHectares } from "@/components/ParcelMap";
import Link from "next/link";

function FarmerDetailView() {
  const farmerId = useSearchParams().get("id") ?? "";


  const { data, error, reload } = useResource(async () => {
    // The record is the page; the history is an extra. A farmer whose
    // assessments cannot be listed still has a record worth showing, so only
    // the first failure is an error.
    const rec = await api.getFarmer(farmerId);
    const history = await api
      .listAssessments({ limit: 50 }, farmerId)
      .then((r) => r.items ?? [])
      .catch(() => []);
    return { rec, history };
  }, [farmerId]);
  const rec = data?.rec ?? null;
  const history = data?.history ?? [];

  if (error) {
    return (
      <div className="space-y-5">
        <BackLink href="/farmers">Farmers</BackLink>
        <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
      </div>
    );
  }

  if (!rec) {
    return (
      <div className="space-y-5">
        <BackLink href="/farmers">Farmers</BackLink>
        <Skeleton className="h-56 w-full" />
        <Skeleton className="h-72 w-full" />
      </div>
    );
  }

  const f = rec.FarmerData;

  return (
    <div className="space-y-6">
      <BackLink href="/farmers">Farmers</BackLink>

      <div>
        <h1 className="text-xl font-semibold tracking-tight text-foreground">
          {f.farmer_name}
        </h1>
        <p className="mono mt-1 text-sm text-muted-foreground">{f.farmer_id}</p>
      </div>

      <Card>
        <CardHeader title="Registry record" subtitle="AgriStack Farmer Registry" />
        <dl className="grid grid-cols-2 gap-x-6 px-4 py-3 md:grid-cols-4">
          <Field label="Date of birth" value={f.dob} />
          <Field label="Gender" value={f.gender} />
          <Field label="Mobile" value={f.mobile_no} mono />
          <Field label="Aadhaar" value={maskedAadhaar(f.aadhaar)} mono />
          <Field label="Category" value={f.farmer_category} />
          <Field label="Caste category" value={f.caste_category} />
          <Field
            label="Identifier"
            value={`${f.identifier_type} ${f.identifier_name}`}
          />
          <Field label="Address" value={f.address} />
          <Field label="State LGD" value={f.state_lgd_code} mono />
          <Field label="District LGD" value={f.district_lgd_code} mono />
          <Field label="Sub-district LGD" value={f.sub_district_lgd_code} mono />
          <Field label="Village LGD" value={f.village_lgd_code} mono />
        </dl>
      </Card>

      <div>
        <h2 className="mb-3 text-sm font-semibold tracking-tight text-foreground">
          Parcels ({rec.land_data.length})
        </h2>
        <div className="grid gap-4 lg:grid-cols-2">
          {rec.land_data.map((p) => {
            const crops = rec.crop_sown_data.filter(
              (c) => c.farm_id === p.farm_id,
            );
            const polyArea = polygonAreaHectares(p.plot_geometry);
            const owner = Number(p.owner_extent);

            return (
              <Card key={p.farm_id}>
                <CardHeader
                  title={`Survey ${p.survey_number}${
                    p.sub_survey_number && p.sub_survey_number !== "*"
                      ? ` / ${p.sub_survey_number}`
                      : ""
                  }`}
                  subtitle={p.farm_id}
                />
                <div className="grid gap-4 px-4 py-4 sm:grid-cols-[160px_1fr]">
                  <ParcelMap
                    geometry={p.plot_geometry}
                    className="h-[160px] w-full"
                  />
                  <dl className="grid grid-cols-2 gap-x-5">
                    <Field
                      label="Owner extent"
                      value={`${p.owner_extent} ${p.area_unit}`}
                    />
                    <Field
                      label="Polygon area"
                      value={
                        polyArea !== null
                          ? `${polyArea.toFixed(2)} Hectare`
                          : "—"
                      }
                    />
                    <Field label="Khata" value={p.khata_number ?? "—"} mono />
                    <Field label="ULPIN" value={p.ulpin ?? "—"} mono />
                    <Field label="Owner (RoR)" value={p.owner_name_ror} />
                    <Field
                      label="Joint owners"
                      value={String(p.joint_owners?.length ?? 0)}
                    />
                  </dl>
                </div>

                {/* The polygon covers the whole survey number; the extent is
                    this owner's share. Conflating them overpays a jointly
                    held parcel. */}
                {polyArea !== null &&
                  Number.isFinite(owner) &&
                  owner > 0 &&
                  polyArea > owner * 1.25 && (
                    <div className="border-t border-border bg-sev-moderate-bg px-4 py-2.5">
                      <p className="text-xs leading-relaxed text-foreground">
                        Polygon is {(polyArea / owner).toFixed(1)}× the owner
                        extent. The boundary covers the whole survey number;
                        loss is apportioned by the {p.owner_extent}{" "}
                        {p.area_unit.toLowerCase()} share.
                      </p>
                    </div>
                  )}

                {crops.length > 0 && (
                  <div className="border-t border-border px-4 py-3">
                    {crops.map((c, i) => (
                      <div
                        key={i}
                        className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs"
                      >
                        <span className="font-medium text-foreground">
                          {c.crop_name}
                        </span>
                        <span className="text-muted-foreground">
                          {c.season} {c.year}
                        </span>
                        <span className="text-muted-foreground">
                          Sown {c.sowing_date}
                        </span>
                        <span className="text-muted-foreground">
                          {c.sown_area_integer}.{c.sown_area_decimal}{" "}
                          {c.sown_area_unit.toLowerCase()}
                        </span>
                        <span className="text-muted-foreground">
                          {c.irrigation_type}
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </Card>
            );
          })}
        </div>
      </div>

      {history.length > 0 && (
        <Card>
          <CardHeader title="Assessment history" />
          <div className="divide-y divide-border">
            {history.map((a) => (
              <Link
                key={a.id}
                href={`/assessment/?id=${a.id}`}
                className="flex flex-wrap items-center gap-x-5 gap-y-2 px-4 py-3 transition-colors hover:bg-muted"
              >
                <span className="text-sm font-medium text-foreground">
                  Survey {a.survey_number}
                </span>
                <span className="text-xs text-muted-foreground">
                  {formatDate(a.incident_date)}
                </span>
                <span className="tnum text-sm text-foreground">
                  {damageFigure(a.damage_percent, 2)}
                </span>
                <CropPresenceBadge presence={a.crop_presence} />
                <TriageBadge tier={a.triage_tier} />
              </Link>
            ))}
          </div>
        </Card>
      )}
    </div>
  );
}

/**
 * useSearchParams suspends during prerender, so the route needs a boundary for
 * the static export to build.
 */
export default function FarmerDetailPage() {
  return (
    <Suspense fallback={<Skeleton className="h-64 w-full" />}>
      <FarmerDetailView />
    </Suspense>
  );
}
