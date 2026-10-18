"use client";

import Link from "next/link";
import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { api } from "@/lib/api";
import { useResource } from "@/lib/useResource";
import type { Assessment } from "@/lib/types";
import { PrintDocument } from "@/components/PrintReport";
import {
  confidenceLabel,
  cropPresenceStyles,
  damageFigure,
  daysBetween,
  formatDate,
  formatDateTime,
  triageStyles,
} from "@/lib/format";
import {
  BackLink,
  Card,
  CardHeader,
  CropPresenceBadge,
  ErrorPanel,
  Field,
  FraudList,
  GuardrailList,
  SeverityBadge,
  Skeleton,
  Stat,
  TriageBadge,
} from "@/components/ui";
import { Grounding } from "@/components/Grounding";
import { PanelFindings } from "@/components/PanelFindings";
import { NdviChart } from "@/components/NdviChart";
import { ParcelMap, polygonAreaHectares } from "@/components/ParcelMap";

function AssessmentView() {
  const params = { id: useSearchParams().get("id") ?? "" };

  const { data, error, reload } = useResource(async () => {
    const a = await api.getAssessment(params.id);
    // The farmer record decorates the page. Losing it should not lose the
    // assessment, which is the thing an officer came here to read.
    const rec = await api.getFarmer(a.farmer_id).catch(() => null);
    return { a, rec };
  }, [params.id]);
  const a = data?.a ?? null;
  const rec = data?.rec ?? null;

  if (error) {
    return (
      <div className="space-y-5">
        <BackLink href="/assessments">Assessments</BackLink>
        <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
      </div>
    );
  }

  if (!a) {
    return (
      <div className="space-y-5">
        <BackLink href="/assessments">Assessments</BackLink>
        <Skeleton className="h-32 w-full" />
        <div className="grid gap-4 lg:grid-cols-2">
          <Skeleton className="h-80 w-full" />
          <Skeleton className="h-80 w-full" />
        </div>
      </div>
    );
  }

  const v = a.assessment;
  const sat = a.satellite;
  const parcel = rec?.land_data.find((p) => p.farm_id === a.farm_id) ?? null;
  const farmer = rec?.FarmerData;
  const polyArea = polygonAreaHectares(parcel?.plot_geometry ?? null);
  const windowDays = daysBetween(a.sowing_date, a.incident_date);
  const fraud = v.fraud_indicators ?? [];

  return (
    <div className="space-y-6">
      <BackLink href="/assessments">Assessments</BackLink>

      {/* Header ------------------------------------------------------- */}
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold tracking-tight text-foreground">
            Survey {a.survey_number}
            {a.crop_name && (
              <span className="ml-2 text-base font-normal text-muted-foreground">
                · {a.crop_name}
              </span>
            )}
          </h1>
          <div className="mono mt-1 flex flex-wrap gap-x-4 text-xs text-faint">
            <span>{a.farm_id}</span>
            {farmer && (
              <Link
                href={`/farmer/?id=${a.farmer_id}`}
                className="hover:text-primary"
              >
                {farmer.farmer_name}
              </Link>
            )}
            <span>{formatDateTime(a.created_at)}</span>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <CropPresenceBadge presence={v.crop_presence} showHint />
          <SeverityBadge severity={v.severity} />
          <TriageBadge tier={a.triage_tier} showHint />
          {/* The assessment as a paper document. Not the same paper as the
              farmer's filing: this is what the evidence and the officers made
              of it, laid out by the server rather than by printing this
              page. */}
          <PrintDocument
            fetchHtml={() => api.assessmentDossierHtml(a.id)}
            what={`Print the assessment of survey ${a.survey_number}`}
            noun="assessment"
            label="Print"
            className="border border-border"
          />
        </div>
      </div>

      {/* Verdict ------------------------------------------------------ */}
      <Card className="px-4 py-5">
        <dl className="grid grid-cols-2 gap-6 lg:grid-cols-5">
          <Stat
            label="Damage"
            value={damageFigure(v.damage_percent, 2)}
            tone={v.damage_percent === null ? "muted" : "default"}
            hint={
              v.damage_percent === null
                ? "Withheld — needs a field photograph"
                : "From ground imagery"
            }
          />
          <Stat
            label="Confidence"
            value={v.confidence.toFixed(2)}
            hint={confidenceLabel(v.confidence)}
          />
          <Stat
            label="Owner extent"
            value={a.owner_extent_hectares.toFixed(2)}
            unit="ha"
            hint="The share loss would be apportioned over."
          />
          <Stat
            label="Observations"
            value={sat.observation_count}
            hint={`Largest gap ${sat.largest_gap_days} days`}
          />
          <Stat
            label="Window"
            value={windowDays ?? "—"}
            unit="days"
            hint={`${formatDate(a.sowing_date)} → ${formatDate(a.incident_date)}`}
          />
        </dl>

        {/*
          The refusal is the headline, not a footnote. An officer reading this
          screen has to leave knowing whether a number exists and why.
        */}
        {v.insufficient_evidence && (
          <div className="mt-5 rounded-md border border-dashed border-border px-4 py-3">
            <p className="text-sm font-medium text-foreground">
              No damage figure was produced.
            </p>
            <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
              Sentinel-2 at 10 m establishes that a crop grew and roughly when.
              It cannot measure how badly one was damaged, so a percentage only
              survives verification when a field photograph backed it. This
              assessment carries evidence for a field visit, not a payout.
            </p>
          </div>
        )}

        <div
          className={`mt-4 rounded-md px-4 py-3 ${triageStyles[a.triage_tier].bg}`}
        >
          <p className="text-sm text-foreground">
            <span className="font-medium">
              {triageStyles[a.triage_tier].label}.
            </span>{" "}
            {triageStyles[a.triage_tier].hint}
          </p>
        </div>
      </Card>

      {/* Findings ----------------------------------------------------- */}
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader
            title="Crop presence"
            subtitle={cropPresenceStyles[v.crop_presence].label}
            action={<CropPresenceBadge presence={v.crop_presence} />}
          />
          <div className="space-y-3 px-4 py-4">
            <p className="text-sm leading-relaxed text-muted-foreground">
              {v.crop_presence_reasoning || "—"}
            </p>
            {(v.contradictions?.length ?? 0) > 0 && (
              <div>
                <h4 className="text-xs font-medium text-muted-foreground">
                  Contradictions in the evidence
                </h4>
                <ul className="mt-1.5 space-y-1">
                  {v.contradictions?.map((c, i) => (
                    <li
                      key={i}
                      className="text-sm leading-relaxed text-foreground"
                    >
                      · {c}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        </Card>

        <Card>
          <CardHeader
            title="Fraud indicators"
            subtitle="Discrepancies between the claim and the observed record"
            action={
              <span className="shrink-0 rounded-md bg-muted px-2 py-0.5 text-xs text-muted-foreground">
                {fraud.length} {fraud.length === 1 ? "flag" : "flags"}
              </span>
            }
          />
          <FraudList indicators={fraud} />
        </Card>
      </div>

      {/* Reasoning ---------------------------------------------------- */}
      <Card>
        <CardHeader
          title="Assessment"
          subtitle={`${v.model || "model"} · prompt ${v.prompt_version || "—"}`}
        />
        <div className="space-y-4 px-4 py-4">
          <p className="whitespace-pre-line text-sm leading-relaxed text-foreground">
            {v.reasoning || "—"}
          </p>
          {(v.evidence_cited?.length ?? 0) > 0 && (
            <div>
              <h4 className="text-xs font-medium text-muted-foreground">
                Evidence cited
              </h4>
              <ul className="mt-1.5 space-y-1">
                {v.evidence_cited?.map((e, i) => (
                  <li
                    key={i}
                    className="text-sm leading-relaxed text-muted-foreground"
                  >
                    · {e}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      </Card>

      {/* What the figure stands on ------------------------------------ */}
      {/* Before the panel's own reasoning, because an officer needs to know
          whether the percentage rests on anything outside the claim before
          they read an argument for it. */}
      <Grounding packet={a.baseline} incidentDate={a.incident_date} />

      {/* Panel -------------------------------------------------------- */}
      <PanelFindings
        specialists={v.specialists}
        degraded={v.degraded}
        degradedReason={v.degraded_reason}
      />

      {/* Panes -------------------------------------------------------- */}
      <div className="grid gap-4 lg:grid-cols-2">
        {/* Pane 1 — cadastral */}
        <Card>
          <CardHeader
            title="Cadastral boundary"
            subtitle="AgriStack parcel geometry"
          />
          <div className="grid gap-4 px-4 py-4 sm:grid-cols-[180px_1fr]">
            <ParcelMap
              geometry={parcel?.plot_geometry ?? null}
              className="h-[180px] w-full"
            />
            <dl className="grid grid-cols-2 gap-x-5">
              <Field label="Survey number" value={a.survey_number} mono />
              <Field
                label="Sub-survey"
                value={
                  parcel?.sub_survey_number === "*"
                    ? "none"
                    : (parcel?.sub_survey_number ?? "—")
                }
                mono
              />
              <Field
                label="Polygon area"
                value={polyArea !== null ? `${polyArea.toFixed(2)} ha` : "—"}
              />
              <Field
                label="Owner extent"
                value={`${a.owner_extent_hectares.toFixed(2)} ha`}
              />
              <Field label="Khata" value={parcel?.khata_number ?? "—"} mono />
              <Field label="ULPIN" value={parcel?.ulpin ?? "—"} mono />
            </dl>
          </div>
        </Card>

        {/* Pane 2 — terrain */}
        <Card>
          <CardHeader
            title="Terrain"
            subtitle="Copernicus DEM GLO-30"
            action={
              <span className="shrink-0 rounded-md bg-muted px-2 py-0.5 text-xs text-muted-foreground">
                {a.terrain.waterlogging_risk} waterlogging risk
              </span>
            }
          />
          <div className="px-4 py-4">
            <dl className="grid grid-cols-2 gap-5">
              <Stat
                label="Mean slope"
                value={a.terrain.mean_slope_degrees.toFixed(2)}
                unit="°"
                hint="Susceptibility, not damage — flat ground under a thriving crop is not a loss."
              />
              <Stat
                label="Classification"
                value={a.terrain.classification}
                tone="muted"
              />
            </dl>
            <div className="mt-4 grid grid-cols-2 gap-3">
              <ImageTile src={a.images?.terrain_slope} label="Slope" />
              <ImageTile src={a.images?.terrain_aspect} label="Aspect" />
            </div>
          </div>
        </Card>
      </div>

      {/* Pane 3 — NDVI */}
      <Card>
        <CardHeader
          title="Vegetation index"
          subtitle="Sentinel-2, cloud-free passes only. Evidence that something grew — not a damage measurement."
        />
        <div className="px-4 py-4">
          <dl className="mb-5 grid grid-cols-2 gap-5 sm:grid-cols-4">
            <Stat
              label="Peak NDVI"
              value={sat.baseline_ndvi.toFixed(4)}
              hint={formatDate(sat.baseline_date)}
            />
            <Stat
              label="Latest NDVI"
              value={sat.incident_ndvi.toFixed(4)}
              hint={formatDate(sat.incident_date)}
            />
            <Stat label="ΔNDVI" value={sat.delta_ndvi.toFixed(4)} />
            <Stat
              label="Relative loss"
              value={sat.rrvl_percent.toFixed(2)}
              unit="%"
              hint="How far the canopy fell toward bare soil at 0.12"
            />
          </dl>

          <NdviChart
            series={a.ndvi_series ?? buildSeries(a)}
            baselineDate={sat.baseline_date}
            sowingDate={a.sowing_date}
            incidentDate={a.incident_date}
          />

          <div className="mt-5 grid grid-cols-2 gap-3">
            <ImageTile
              src={a.images?.ndvi_mid_date?.base64}
              label="Mid-window"
              caption={
                a.images?.ndvi_mid_date?.date
                  ? formatDate(a.images.ndvi_mid_date.date)
                  : undefined
              }
            />
            <ImageTile
              src={a.images?.ndvi_last_visit?.base64}
              label="Last visit"
              caption={
                a.images?.ndvi_last_visit?.date
                  ? formatDate(a.images.ndvi_last_visit.date)
                  : undefined
              }
            />
          </div>
        </div>
      </Card>

      {/* Guardrails --------------------------------------------------- */}
      <Card>
        <CardHeader
          title="Guardrails applied"
          subtitle="Where the system overrode or withheld part of the answer"
        />
        <GuardrailList
          agent={v.agent_guardrails}
          backend={v.backend_guardrails}
        />
      </Card>
    </div>
  );
}

/**
 * Fallback for assessments stored before the API carried the raw series:
 * draws the two anchors so the panel still says something.
 */
function buildSeries(a: Assessment) {
  const s = a.satellite;
  const pts = [
    { date: s.baseline_date, ndvi: s.baseline_ndvi },
    { date: s.incident_date, ndvi: s.incident_ndvi },
  ].filter((p) => p.date);

  // A single distinct date means peak and latest are the same pass.
  const unique = pts.filter(
    (p, i, arr) => arr.findIndex((q) => q.date === p.date) === i,
  );
  return unique.sort((x, y) => x.date.localeCompare(y.date));
}

function ImageTile({
  src,
  label,
  caption,
}: {
  src?: string | null;
  label: string;
  caption?: string;
}) {
  if (!src) {
    return (
      <div className="flex aspect-square items-center justify-center rounded-md border border-dashed border-border bg-muted">
        <span className="text-xs text-faint">{label} unavailable</span>
      </div>
    );
  }
  return (
    <figure className="overflow-hidden rounded-md border border-border bg-muted">
      {/* Data URIs from the analytics engine; next/image would only add a
          loader in front of an already-inline payload. */}
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img
        src={src}
        alt={`${label} overlay`}
        className="aspect-square w-full object-contain"
      />
      <figcaption className="flex items-center justify-between border-t border-border px-2.5 py-1.5">
        <span className="text-xs font-medium text-foreground">{label}</span>
        {caption && <span className="text-xs text-faint">{caption}</span>}
      </figcaption>
    </figure>
  );
}

/**
 * useSearchParams suspends during prerender, so the route needs a boundary for
 * the static export to build.
 */
export default function AssessmentPage() {
  return (
    <Suspense fallback={<Skeleton className="h-64 w-full" />}>
      <AssessmentView />
    </Suspense>
  );
}
