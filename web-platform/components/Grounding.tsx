"use client";

import {
  AlertTriangle,
  CheckCircle2,
  CloudRain,
  Radar,
  Ruler,
  Sprout,
  TrendingDown,
  TriangleAlert,
} from "lucide-react";

import { Card, CardHeader } from "@/components/ui";
import type { EvidencePacket } from "@/lib/types";
import { MoisturePanel } from "@/components/MoisturePanel";

/**
 * What the figure was measured against.
 *
 * # Why this is the first thing on the screen
 *
 * A damage percentage reads as a measurement. Until this existed it was a
 * judgement, because nothing in the record said what sixty per cent was sixty
 * per cent *of*. An officer deciding whether to accept the model's figure
 * needs to know, before anything else, whether it rests on the parcel's own
 * history and its neighbours' fields or on the claim and the photographs
 * alone.
 *
 * # Why the absences are as prominent as the findings
 *
 * A screen that showed only what was gathered would look identical whether
 * six sources answered or none did. The gaps are rendered in the same weight
 * as everything else, and when nothing was gathered the panel says plainly
 * that the percentage should be read as a judgement.
 */
export function Grounding({
  packet,
  incidentDate,
}: {
  packet?: EvidencePacket;
  /** Drawn on the moisture chart, so the season reads against the event. */
  incidentDate?: string;
}) {
  if (!packet) return null;

  const d = packet.denominators;
  const grounded =
    !!d?.threshold_yield ||
    (!!d?.parcel_history && d.parcel_history.verdict !== "unknown") ||
    (!!d?.neighbourhood && d.neighbourhood.verdict !== "inconclusive") ||
    (packet.radar?.findings?.length ?? 0) > 0 ||
    !!packet.field_measurements?.supports_a_figure;

  return (
    <Card>
      <CardHeader
        title="What this figure was measured against"
        subtitle={
          packet.elapsed
            ? `Evidence gathered in ${packet.elapsed}`
            : "Supporting evidence"
        }
      />

      {!grounded && (
        <div className="mx-5 mt-4 flex gap-3 rounded-lg bg-sev-severe-bg px-4 py-3 text-sm text-sev-severe">
          <TriangleAlert className="mt-0.5 size-4 shrink-0" />
          <p>
            Nothing was available to measure this figure against — no baseline
            for this parcel, no comparison with the surrounding fields, no
            radar and no field measurements.{" "}
            <strong>Read the percentage as a judgement.</strong>
          </p>
        </div>
      )}

      <div className="grid gap-3 px-4 py-4 sm:grid-cols-2">
        <PriorSeasons history={d?.parcel_history} />
        <Neighbours peers={d?.neighbourhood} />
        <ThresholdYield yield_={d?.threshold_yield} />
        <Stage packet={packet} />
        <RadarPanel packet={packet} />
        <MoisturePanel reading={packet.moisture} incidentDate={incidentDate} />
        <Weather packet={packet} />
        <Measurements packet={packet} />
        <SowingCheck packet={packet} />
      </div>

      <Signals packet={packet} />
      <Absent absent={packet.absent} />
    </Card>
  );
}

/* ------------------------------------------------------------------ */

function Panel({
  icon,
  title,
  tone = "neutral",
  children,
}: {
  icon: React.ReactNode;
  title: string;
  tone?: "neutral" | "good" | "warn" | "bad";
  children: React.ReactNode;
}) {
  const ring =
    tone === "good"
      ? "border-sev-negligible/40"
      : tone === "warn"
        ? "border-sev-moderate/40"
        : tone === "bad"
          ? "border-sev-severe/40"
          : "border-border";

  return (
    <div className={`rounded-lg border ${ring} bg-muted/40 px-4 py-3`}>
      <div className="mb-1.5 flex items-center gap-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {icon}
        {title}
      </div>
      <div className="space-y-1.5 text-sm">{children}</div>
    </div>
  );
}

function Note({ children }: { children: React.ReactNode }) {
  return <p className="text-xs leading-relaxed text-muted-foreground">{children}</p>;
}

function PriorSeasons({
  history,
}: {
  history?: NonNullable<EvidencePacket["denominators"]>["parcel_history"];
}) {
  if (!history) return null;

  const tone =
    history.verdict === "below_normal"
      ? "bad"
      : history.verdict === "above_normal"
        ? "warn"
        : "neutral";

  const words: Record<string, string> = {
    below_normal: "Below what this land normally reaches",
    above_normal: "Better than this land normally reaches",
    normal: "About what this land normally reaches",
    unknown: "No prior season could be read",
  };

  return (
    <Panel
      icon={<TrendingDown className="size-3.5" />}
      title="This parcel's own history"
      tone={tone}
    >
      <p className="font-medium">{words[history.verdict ?? ""] ?? "—"}</p>
      {history.mean_prior_peak !== undefined && (
        <p className="tabular-nums">
          This season peaked at {fmt(history.this_season_peak)} NDVI against a
          prior-season average of {fmt(history.mean_prior_peak)}
          {history.prior_seasons?.length
            ? ` over ${history.prior_seasons.length} season${history.prior_seasons.length === 1 ? "" : "s"}`
            : ""}
          .
        </p>
      )}
      {history.caveats?.map((c) => <Note key={c}>{c}</Note>)}
    </Panel>
  );
}

function Neighbours({
  peers,
}: {
  peers?: NonNullable<EvidencePacket["denominators"]>["neighbourhood"];
}) {
  if (!peers) return null;

  const tone =
    peers.verdict === "regional_event"
      ? "good"
      : peers.verdict === "local_only"
        ? "warn"
        : "neutral";

  const words: Record<string, string> = {
    regional_event: "The surrounding fields fell too",
    local_only: "The surrounding fields did not fall",
    not_visible_on_this_parcel: "The neighbours fell and this parcel did not",
    no_loss_visible_anywhere: "No fall visible here or around it",
    inconclusive: "Too little cloud-free imagery to say",
  };

  return (
    <Panel
      icon={peers.verdict === "regional_event" ? <CheckCircle2 className="size-3.5" /> : <AlertTriangle className="size-3.5" />}
      title="The fields around it"
      tone={tone}
    >
      <p className="font-medium">{words[peers.verdict ?? ""] ?? "—"}</p>
      {peers.reason && <Note>{peers.reason}</Note>}
      {peers.caveats?.map((c) => <Note key={c}>{c}</Note>)}
    </Panel>
  );
}

function ThresholdYield({
  yield_,
}: {
  yield_?: NonNullable<EvidencePacket["denominators"]>["threshold_yield"];
}) {
  if (!yield_) return null;
  return (
    <Panel icon={<Sprout className="size-3.5" />} title="Threshold yield" tone="good">
      <p className="font-medium tabular-nums">
        {yield_.yield_kg_per_ha} kg/ha
        {yield_.unit_name ? ` for ${yield_.unit_name}` : ""}
      </p>
      {yield_.basis && <Note>Basis: {yield_.basis}</Note>}
      {yield_.source && <Note>Issued by {yield_.source}</Note>}
    </Panel>
  );
}

function Stage({ packet }: { packet: EvidencePacket }) {
  const t = packet.thermal;
  const s = packet.susceptibility;
  if (!t && !s) return null;

  const tone =
    s?.level === "severe" ? "bad" : s?.level === "high" ? "warn" : "neutral";

  return (
    <Panel icon={<Sprout className="size-3.5" />} title="Where the crop stood" tone={tone}>
      {t && (
        <p className="font-medium">
          {t.stage_mr ? `${t.stage_mr} · ` : ""}
          {t.stage_en ?? t.stage ?? "—"}
          {t.percent_of_cycle !== undefined && (
            <span className="ml-1 font-normal text-muted-foreground tabular-nums">
              ({t.percent_of_cycle}% of its cycle)
            </span>
          )}
        </p>
      )}
      {t?.accumulated_gdd !== undefined && (
        <Note>
          {t.accumulated_gdd} growing degree days accumulated from sowing, not
          a day count.
        </Note>
      )}
      {t && t.recoverable === false && (
        <p className="text-xs font-medium text-sev-severe">
          Damage at this stage is not recoverable — the plant cannot make it up.
        </p>
      )}
      {s?.reason && <Note>{s.reason}</Note>}
      {t?.caveats?.map((c) => <Note key={c}>{c}</Note>)}
    </Panel>
  );
}

function RadarPanel({ packet }: { packet: EvidencePacket }) {
  const r = packet.radar;
  if (!r) return null;

  const first = r.findings?.[0];
  const tone =
    first?.code === "inundation" || first?.code === "lodging"
      ? "bad"
      : first?.code === "no_material_change"
        ? "good"
        : "neutral";

  return (
    <Panel icon={<Radar className="size-3.5" />} title="Radar, through cloud" tone={tone}>
      {packet.radar_passes_in_optical_gap ? (
        <p className="font-medium">
          Observed this field {packet.radar_passes_in_optical_gap} time
          {packet.radar_passes_in_optical_gap === 1 ? "" : "s"} while the
          optical record was blank.
        </p>
      ) : null}
      {r.findings?.map((f) => (
        <div key={f.code} className="space-y-1">
          <p className="font-medium capitalize">
            {(f.code ?? "").replaceAll("_", " ")}
            <span className="ml-1 font-normal text-muted-foreground">({f.confidence})</span>
          </p>
          {f.detail && <Note>{f.detail}</Note>}
          {f.caveats?.map((c) => <Note key={c}>{c}</Note>)}
        </div>
      ))}
      {r.orbits_matched === false && (
        <Note>
          The passes either side came from different orbit directions, which
          shifts backscatter over an unchanged field.
        </Note>
      )}
    </Panel>
  );
}

function Weather({ packet }: { packet: EvidencePacket }) {
  const w = packet.weather;
  if (!w?.season) return null;
  const s = w.season;

  return (
    <Panel icon={<CloudRain className="size-3.5" />} title="The season's weather">
      <p className="tabular-nums">
        {s.rain_mm ?? 0} mm over {s.days ?? 0} days · longest dry spell{" "}
        {s.longest_dry_spell_days ?? 0} days · hottest {s.hottest_c ?? 0}°C
      </p>
      {packet.stress?.heat_days_at_flowering ? (
        <p className="text-xs font-medium text-sev-moderate">
          {packet.stress.heat_days_at_flowering} day
          {packet.stress.heat_days_at_flowering === 1 ? "" : "s"} above the
          crop&rsquo;s heat threshold while it was flowering.
        </p>
      ) : null}
      {w.notes?.map((n) => <Note key={n}>{n}</Note>)}
    </Panel>
  );
}

function Measurements({ packet }: { packet: EvidencePacket }) {
  const m = packet.field_measurements;
  if (!m || !m.quadrats) return null;

  return (
    <Panel
      icon={<Ruler className="size-3.5" />}
      title="What an officer counted"
      tone={m.supports_a_figure ? "good" : "neutral"}
    >
      <p className="tabular-nums">
        {m.quadrats} quadrat{m.quadrats === 1 ? "" : "s"}
        {m.mean_stand_loss_percent !== undefined &&
          ` · stand loss ${m.mean_stand_loss_percent}%`}
        {m.mean_organ_damage_percent !== undefined &&
          ` · organ damage ${m.mean_organ_damage_percent}%`}
      </p>
      {m.sampling_fraction && <Note>{m.sampling_fraction}</Note>}
      {m.caveats?.map((c) => <Note key={c}>{c}</Note>)}
    </Panel>
  );
}

function SowingCheck({ packet }: { packet: EvidencePacket }) {
  const c = packet.sowing_check;
  if (!c) return null;

  return (
    <Panel
      icon={<Sprout className="size-3.5" />}
      title="The declared sowing date"
      tone={c.agrees ? "good" : "warn"}
    >
      <p className="font-medium">
        {c.agrees ? "Consistent with observed emergence" : "Does not match observed emergence"}
      </p>
      <p className="tabular-nums text-xs text-muted-foreground">
        Declared {c.declared_date} · the canopy began rising {c.observed_greenup_date}
      </p>
      {c.note && <Note>{c.note}</Note>}
    </Panel>
  );
}

function Signals({ packet }: { packet: EvidencePacket }) {
  const signals = packet.claim_history?.signals ?? [];
  if (!signals.length) return null;

  return (
    <div className="border-t border-border px-4 py-4">
      <h4 className="mb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        What has been claimed here before
      </h4>
      <ul className="space-y-3">
        {signals.map((s) => (
          <li key={s.code} className="text-sm">
            <p className={s.severity === "warning" ? "font-medium text-sev-moderate" : "font-medium"}>
              {s.detail}
            </p>
            {/* The ordinary explanation is not a footnote. A flag read without
                it becomes a mark against a farmer, and the flags are right
                often enough that the habit sticks. */}
            {s.innocent_explanation && (
              <p className="mt-0.5 text-xs leading-relaxed text-muted-foreground">
                {s.innocent_explanation}
              </p>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

function Absent({ absent }: { absent?: string[] }) {
  if (!absent?.length) return null;
  return (
    <div className="border-t border-border px-4 py-4">
      <h4 className="mb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        Could not be gathered
      </h4>
      <ul className="space-y-1.5">
        {absent.map((a) => (
          <li key={a} className="text-xs leading-relaxed text-muted-foreground">
            {a}
          </li>
        ))}
      </ul>
    </div>
  );
}

function fmt(v?: number) {
  return v === undefined ? "—" : v.toFixed(2);
}
