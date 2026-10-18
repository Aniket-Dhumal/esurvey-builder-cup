"use client";

import { useCallback, useEffect, useState } from "react";
import { CircleCheck, CircleX, TriangleAlert } from "lucide-react";

import { api, API_BASE, ApiError } from "@/lib/api";
import { PageHeader, Panel } from "@/components/dashboard";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

type State = "live" | "degraded" | "unavailable";

interface Source {
  name: string;
  provider: string;
  state: State;
  role: string;
  note: string;
  /** The key under /readyz's checks that says whether this is connected. */
  check?: string;
}

/**
 * A standing account of which evidence the platform actually has.
 *
 * What each source is and what its absence costs is written here, because it
 * is an argument and not a fact about a server. Whether it is *connected* is
 * read from the deployment on every load, because that is a fact about a
 * server and this page had it wrong for months: it called Sentinel-1 and
 * ground vision "not integrated" long after both were wired, and an officer
 * reading it was told the assessment rested on less than it did.
 */
const SOURCES: Source[] = [
  {
    name: "Sentinel-2 MSI",
    provider: "Farm Analytics Engine · Google Earth Engine",
    state: "live",
    check: "satellite",
    role: "Vegetation index",
    note: "Cloud-free NDVI over the crop cycle, plus two colourised overlays clipped to the parcel boundary.",
  },
  {
    name: "Copernicus DEM GLO-30",
    provider: "Farm Analytics Engine · Google Earth Engine",
    state: "live",
    check: "satellite",
    role: "Terrain",
    note: "Elevation, slope and aspect at 30 m. Slope stands in for the specification's Topographic Wetness Index.",
  },
  {
    name: "AgriStack — Farmer Registry",
    provider: "agristack.agribid.ai bridge",
    state: "degraded",
    check: "registry",
    role: "Identity and land records",
    note: "Demographic and RoR land data return correctly. Geometry and crops came back empty on every deployment tested for a long time; the records loaded here carry both, and a parcel that arrives without an outline still has to be walked before it can be fenced or assessed.",
  },
  {
    name: "Sentinel-1 SAR",
    provider: "Radar module · Google Earth Engine",
    check: "radar",
    state: "live",
    role: "All-weather backscatter",
    note: "Optical NDVI cannot see through monsoon cloud, and a damage event inside a cloudy stretch is invisible without radar — the reference parcel had a 60-day blind gap. Backscatter and soil moisture, over the same parcel boundary.",
  },
  {
    name: "Ground computer vision",
    provider: "Assessment model · keyframes from the survey walk",
    check: "ground_vision",
    state: "live",
    role: "Field evidence",
    note: "Stem lodging angle, submerged and necrotic share, read from the walk by the assessment model. The specification weights this signal most heavily of the three, and the reading is capped below the confidence a validated instrument would earn until it has been measured against labelled footage.",
  },
];

const STATE_META: Record<
  State,
  { label: string; cls: string; Icon: typeof CircleCheck }
> = {
  live: {
    label: "Live",
    cls: "bg-sev-negligible-bg text-sev-negligible",
    Icon: CircleCheck,
  },
  degraded: {
    label: "Partial",
    cls: "bg-sev-moderate-bg text-sev-moderate",
    Icon: TriangleAlert,
  },
  unavailable: {
    label: "Not integrated",
    cls: "bg-sev-catastrophic-bg text-sev-catastrophic",
    Icon: CircleX,
  },
};

/** What the deployment said about one module, mapped onto this page. */
function stateFromCheck(
  checks: Record<string, unknown> | undefined,
  key: string | undefined,
  fallback: State,
): State {
  if (!key || !checks) return fallback;
  const entry = checks[key] as { status?: string } | undefined;
  switch (entry?.status) {
    case undefined:
      // A deployment too old to report this module. Saying nothing is
      // honest; asserting either answer is not.
      return fallback;
    case "up":
    case "ok":
      return "live";
    case "not_configured":
      return "unavailable";
    case "degraded":
    case "empty":
      return "degraded";
    default:
      // unreachable, down, or anything a later version invents.
      return "degraded";
  }
}

export default function SourcesPage() {
  const [online, setOnline] = useState<boolean | null>(null);
  const [farmers, setFarmers] = useState<number | null>(null);
  const [checks, setChecks] = useState<Record<string, unknown> | undefined>();
  const [, setError] = useState<ApiError | null>(null);

  const load = useCallback(() => {
    // readyz rather than healthz: this page is about whether the things the
    // platform depends on are actually reachable, which is exactly what the
    // readiness probe reports and what liveness deliberately does not.
    api
      .ready()
      .then((r) => {
        setOnline(r.status === "ready");
        setChecks(r.checks as Record<string, unknown> | undefined);
        const registry = r.checks?.registry as { farmers?: number } | undefined;
        setFarmers(registry?.farmers ?? null);
      })
      .catch((e) => {
        setOnline(false);
        setError(e as ApiError);
      });
  }, []);

  useEffect(load, [load]);

  // The state on each row is what this deployment reports, not what the list
  // above assumes.
  const sources = SOURCES.map((s) => ({
    ...s,
    state: stateFromCheck(checks, s.check, s.state),
  }));
  const live = sources.filter((s) => s.state === "live").length;

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Analysis"
        title="Data sources"
        subtitle={
          checks
            ? `${live} of ${sources.length} signals available on this deployment.`
            : "Reading what this deployment is connected to…"
        }
      />

      <Card className="gap-0 p-5">
        <div className="flex flex-wrap items-center gap-x-8 gap-y-3">
          <div>
            <div className="text-xs font-semibold tracking-[0.1em] text-muted-foreground uppercase">
              Assessment API
            </div>
            <div className="mono mt-1 text-sm">
              {API_BASE.replace(/^https?:\/\//, "")}
            </div>
          </div>
          <Badge
            variant="secondary"
            className={cn(
              "border-transparent",
              online === false
                ? "bg-sev-catastrophic-bg text-sev-catastrophic"
                : "bg-sev-negligible-bg text-sev-negligible",
            )}
          >
            {online === null
              ? "checking…"
              : online
                ? "reachable"
                : "unreachable"}
          </Badge>
          {farmers !== null && (
            <div className="text-sm text-muted-foreground">
              {farmers.toLocaleString("en-IN")} farmer record
              {farmers === 1 ? "" : "s"} loaded
            </div>
          )}
        </div>
      </Card>

      <Panel
        title="Signals"
        subtitle="What each source contributes, and what its absence costs"
      >
        <ul className="divide-y">
          {sources.map((s) => {
            const meta = STATE_META[s.state];
            return (
              <li
                key={s.name}
                className="flex flex-col gap-2 py-4 first:pt-0 last:pb-0 sm:flex-row sm:gap-5"
              >
                <div className="sm:w-64 sm:shrink-0">
                  <div className="flex items-center gap-2">
                    <meta.Icon
                      className={cn(
                        "size-4",
                        s.state === "live"
                          ? "text-sev-negligible"
                          : s.state === "degraded"
                            ? "text-sev-moderate"
                            : "text-sev-catastrophic",
                      )}
                    />
                    <span className="text-sm font-medium">{s.name}</span>
                  </div>
                  <div className="mt-1 pl-6 text-xs text-muted-foreground">
                    {s.provider}
                  </div>
                </div>

                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge
                      variant="secondary"
                      className={cn("border-transparent", meta.cls)}
                    >
                      {meta.label}
                    </Badge>
                    <span className="text-xs text-muted-foreground">
                      {s.role}
                    </span>
                  </div>
                  <p className="mt-1.5 text-sm leading-relaxed text-muted-foreground">
                    {s.note}
                  </p>
                </div>
              </li>
            );
          })}
        </ul>
      </Panel>
    </div>
  );
}
