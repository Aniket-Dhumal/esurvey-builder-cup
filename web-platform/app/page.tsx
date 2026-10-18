"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";

import Link from "next/link";
import {
  ClipboardCheck,
  FileBarChart,
  Layers,
  Map,
  Plus,
  TriangleAlert,
  Users,
} from "lucide-react";

import { api } from "@/lib/api";
import { useResource } from "@/lib/useResource";
import type { AssessmentSummary } from "@/lib/types";
import { confidenceLabel, damageFigure, formatDate, roleLabel } from "@/lib/format";
import {
  CardLink,
  KpiCard,
  MetricCard,
  PageHeader,
  Panel,
} from "@/components/dashboard";
import { CaseloadOverview } from "@/components/CaseloadOverview";
import { useSession } from "@/components/SessionProvider";
import type { CurrentUser } from "@/lib/session";
import {
  Button,
  EmptyState,
  ErrorPanel,
  CropPresenceBadge,
  Skeleton,
  TriageBadge,
} from "@/components/ui";

/**
 * Where this officer is posted, as they would say it themselves.
 *
 * Their grant, not a hardcoded district. The console said "Solapur district"
 * to everyone, which is wrong the moment a second district is onboarded and
 * misleading before that.
 */
function posting(user: CurrentUser | null): string {
  if (!user) return "Maharashtra";
  const named = user.grants.find((g) => g.jurisdiction_name);
  return named?.jurisdiction_name ?? "Maharashtra";
}

/**
 * The one thing this officer is most likely to have come here to do.
 *
 * Ordered by who acts soonest rather than by seniority: a Talathi with a
 * worklist is mid-task, a Collector declaring an event is starting one. An
 * officer with neither permission gets no button at all, which is better than
 * a button that 403s.
 */
function primaryAction(can: (p: string) => boolean) {
  if (can("report:verify")) {
    return (
      <Button asChild>
        <Link href="/worklist">
          <ClipboardCheck className="size-4" />
          My worklist
        </Link>
      </Button>
    );
  }
  if (can("event:create")) {
    return (
      <Button asChild>
        <Link href="/events">
          <Plus className="size-4" />
          Declare an event
        </Link>
      </Button>
    );
  }
  if (can("assessment:create")) {
    return (
      <Button asChild>
        <Link href="/assessments">
          <Plus className="size-4" />
          New assessment
        </Link>
      </Button>
    );
  }
  return null;
}

export default function DashboardPage() {
  const { user, can } = useSession();
  const router = useRouter();
  // The product administrator reads no client records; its home is the
  // platform overview.
  const productAdmin = can("platform:read") && !can("report:read");
  useEffect(() => {
    if (productAdmin) router.replace("/platform/");
  }, [productAdmin, router]);
  // Two requests, one resource. The registry listing is decoration on this
  // page -- if it fails, the assessments are still worth showing -- so only
  // the assessment call decides whether the page errored.
  const { data, error, reload } = useResource(async () => {
    // The five most recent assessments, not all of them. This panel shows a
    // handful and the counters that used to be derived by loading everything
    // are now totals the server computes.
    const items = (await api.listAssessments({ limit: 25 })).items ?? [];
    const registry = await api.registrySummary().catch(() => null);
    return { items, registry };
  });
  const items = data?.items ?? null;
  const registry = data?.registry ?? null;

  const parcelCount = registry?.parcels ?? 0;
  const withGeometry = registry?.parcels_with_boundary ?? 0;

  const escalated =
    items?.filter((a) => a.triage_tier === "RED_ANOMALY_ESCALATE").length ?? 0;
  // Fraud flags, not damage tiers: with no field imagery almost every
  // assessment carries no damage figure at all, so a severity histogram would
  // be four empty bars. What varies — and what an officer acts on — is
  // whether the record contradicts the claim.
  const flagged = items?.filter((a) => a.fraud_count > 0).length ?? 0;
  const recent = (items ?? []).slice(0, 5);

  const totalExtent = registry?.extent_hectares ?? 0;

  return (
    <div>
      <PageHeader
        eyebrow={user ? roleLabel(user.primary_role) : "Overview"}
        title={posting(user)}
        subtitle={
          <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
            <span>Kharif 2025–26</span>
            <span aria-hidden className="text-faint">·</span>
            <span>{user?.name ?? "Signed out"}</span>
          </span>
        }
        actions={primaryAction(can)}
      />

      {error && (
        <div className="mb-6">
          <ErrorPanel
            code={error.code}
            message={error.message}
            onRetry={reload}
          />
        </div>
      )}

      {/* What is on this officer's patch ------------------------------ */}
      {can("report:read") && (
        <div className="mb-4">
          <CaseloadOverview />
        </div>
      )}

      {/* Registry counters ------------------------------------------- */}
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <KpiCard
          label="Farmers"
          value={registry?.farmers ?? "—"}
          icon={Users}
          href="/farmers"
        />
        <KpiCard
          label="Parcels"
          value={registry ? parcelCount : "—"}
          icon={Map}
          href="/parcels"
        />
        <KpiCard
          label="Assessments"
          value={items?.length ?? "—"}
          icon={FileBarChart}
          href="/assessments"
        />
        <KpiCard
          label="Escalated"
          value={items ? escalated : "—"}
          icon={TriangleAlert}
          href="/assessments"
          tone={escalated > 0 ? "warn" : "default"}
        />
      </div>

      {/* Coverage --------------------------------------------------- */}
      <div className="mt-4 grid gap-4 lg:grid-cols-3">
        <MetricCard
          label="Boundaries available"
          value={registry ? `${withGeometry}/${parcelCount}` : "—"}
          icon={Layers}
          footer={
            registry && parcelCount > 0 && withGeometry < parcelCount ? (
              <span>
                {parcelCount - withGeometry} parcel
                {parcelCount - withGeometry === 1 ? "" : "s"} cannot be
                assessed — AgriStack returns no geometry.
              </span>
            ) : (
              <span>Every parcel has a boundary to analyse.</span>
            )
          }
        />
        <MetricCard
          label="Registered extent"
          value={registry ? totalExtent.toFixed(2) : "—"}
          unit="ha"
          footer={
            <span>
              Owner shares, not polygon area —{" "}
              <CardLink href="/parcels">see parcels</CardLink>
            </span>
          }
        />
        <MetricCard
          label="Crop declarations"
          value={registry ? registry.crops : "—"}
          footer={
            registry && registry.crops === 0 ? (
              <span>
                None on record. AgriStack returns an empty crop registry for
                every farmer tested.
              </span>
            ) : (
              <span>
                Across {registry?.farmers ?? 0} household
                {registry?.farmers === 1 ? "" : "s"}
              </span>
            )
          }
        />
      </div>

      {/* Recent assessments ----------------------------------------- */}
      <div className="mt-4 grid gap-4 lg:grid-cols-3">
        <Panel
          title="Recent assessments"
          subtitle="Newest first"
          className="lg:col-span-2"
          badge={
            items && items.length > 0 ? (
              <CardLink href="/assessments">View all</CardLink>
            ) : undefined
          }
        >
          {!items && (
            <div className="space-y-2">
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} className="h-14 w-full" />
              ))}
            </div>
          )}

          {items && items.length === 0 && (
            <EmptyState
              title="No assessments yet"
              body="Run one against a parcel to see its satellite series, terrain analysis and damage estimate."
              action={
                <Button asChild>
                  <Link href="/assessments">New assessment</Link>
                </Button>
              }
            />
          )}

          {recent.length > 0 && (
            <ul className="divide-y">
              {recent.map((a) => (
                <li key={a.id}>
                  <Link
                    href={`/assessment/?id=${a.id}`}
                    className="-mx-2 flex flex-wrap items-center gap-x-5 gap-y-2 rounded-md px-2 py-3 transition-colors hover:bg-muted"
                  >
                    <span className="min-w-0">
                      <span className="block text-sm font-medium">
                        Survey {a.survey_number}
                      </span>
                      <span className="mono block text-xs text-faint">
                        {a.farm_id}
                      </span>
                    </span>
                    <span className="text-sm text-muted-foreground">
                      {formatDate(a.incident_date)}
                    </span>
                    <span className="tnum text-sm font-medium">
                      {damageFigure(a.damage_percent, 2)}
                    </span>
                    <CropPresenceBadge presence={a.crop_presence} showHint />
                    <span className="ml-auto flex items-center gap-3">
                      <span className="text-xs text-faint">
                        {confidenceLabel(a.confidence_score)} confidence
                      </span>
                      <TriageBadge tier={a.triage_tier} showHint />
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </Panel>

        <Panel
          title="Signal coverage"
          subtitle="What the engine can currently see"
        >
          <ul className="space-y-3">
            <SignalRow
              name="Sentinel-2 NDVI"
              state="live"
              detail="Cloud-free optical passes over the crop cycle"
            />
            <SignalRow
              name="Copernicus DEM"
              state="live"
              detail="Elevation, slope and aspect at 30 m"
            />
            <SignalRow
              name="Ground computer vision"
              state="missing"
              detail="No capture pipeline — the specification weights this highest"
            />
            <SignalRow
              name="Sentinel-1 SAR"
              state="missing"
              detail="Optical is blind through monsoon cloud without it"
            />
          </ul>
          <p className="mt-4 border-t pt-3 text-xs leading-relaxed text-muted-foreground">
            None of these can measure damage. They establish that a crop grew
            and whether the record matches the claim; a damage percentage needs
            a field photograph, so no assessment fast-tracks and confidence is
            capped at 0.60.
          </p>
        </Panel>
      </div>

      <div className="mt-4">
        <Panel
          title="What the record shows"
          subtitle={
            items && items.length > 0
              ? `${flagged} of ${items.length} carrying a fraud flag`
              : "No assessments to summarise yet"
          }
        >
          {!items || items.length === 0 ? (
            <p className="py-6 text-center text-sm text-muted-foreground">
              Run an assessment to populate this.
            </p>
          ) : (
            <PresenceBars items={items} />
          )}
        </Panel>
      </div>
    </div>
  );
}

function SignalRow({
  name,
  state,
  detail,
}: {
  name: string;
  state: "live" | "missing";
  detail: string;
}) {
  return (
    <li className="flex items-start gap-3">
      <span
        className={`mt-1.5 size-2 shrink-0 rounded-full ${
          state === "live" ? "bg-chart-done" : "bg-chart-waiting"
        }`}
        aria-hidden
      />
      <span className="min-w-0">
        <span className="block text-sm font-medium">{name}</span>
        <span className="block text-xs leading-snug text-muted-foreground">
          {detail}
        </span>
      </span>
    </li>
  );
}

/** Crop-presence mix as proportional bars — no chart library needed. */
function PresenceBars({ items }: { items: AssessmentSummary[] }) {
  // Chart fills, not the severity ramp — see STATE_TONE in
  // CaseloadOverview.tsx for why. "No crop found" was #18181b, which is a
  // solid black bar on white and an invisible one on the dark theme's
  // #0a0a0b ground; --chart-alert inverts to near-white there instead.
  const tiers = [
    { key: "Crop confirmed", cls: "bg-chart-done", match: "confirmed" },
    { key: "Inconclusive", cls: "bg-chart-waiting", match: "inconclusive" },
    { key: "No crop found", cls: "bg-chart-alert", match: "absent" },
  ] as const;

  const counts = tiers.map((t) => ({
    ...t,
    n: items.filter((a) => a.crop_presence === t.match).length,
  }));
  const max = Math.max(...counts.map((c) => c.n), 1);

  return (
    <div className="space-y-3">
      {counts.map((c) => (
        <div key={c.key} className="flex items-center gap-4">
          <span className="w-28 shrink-0 text-sm text-muted-foreground">
            {c.key}
          </span>
          <span className="h-2.5 min-w-0 flex-1 overflow-hidden rounded-full bg-muted">
            <span
              // Same hairline as the pipeline chart's columns: these bars sit
              // on a muted track rather than on white, so the yellow one has
              // even less to distinguish it. See --chart-edge.
              className={`block h-full rounded-full border border-chart-edge ${c.cls}`}
              style={{ width: `${(c.n / max) * 100}%` }}
            />
          </span>
          <span className="tnum w-8 shrink-0 text-right text-sm font-medium">
            {c.n}
          </span>
        </div>
      ))}
    </div>
  );
}
