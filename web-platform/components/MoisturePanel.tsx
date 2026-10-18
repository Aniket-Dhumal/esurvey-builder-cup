"use client";

import { useEffect, useRef, useState } from "react";
import { Droplets } from "lucide-react";

import type { MoistureReading } from "@/lib/types";

/**
 * Leaf water and soil moisture, as the officer reads them.
 *
 * # What leads, and why
 *
 * The relevance line, before any number. NDMI is strong evidence for a dry
 * spell and weak evidence for a hailstorm, and an officer shown "canopy water
 * stress" on a hail claim with no framing will reasonably take it as bearing
 * on breakage. The service computes that sentence per calamity; this puts it
 * first.
 *
 * # Why the chart is the field against its neighbours and not NDMI alone
 *
 * An NDMI value has no meaning on its own — 0.2 is a stressed maize canopy and
 * a healthy sorghum one. What carries meaning is the field against the cropland
 * around it on the same days, and against its own normal. So both lines are
 * drawn, on one axis, with the prior-season normal as a reference, and the
 * reader sees the gap open (or not) rather than being told about it.
 */
export function MoisturePanel({
  reading,
  incidentDate,
}: {
  reading?: MoistureReading;
  incidentDate?: string;
}) {
  if (!reading) return null;

  const findings = reading.findings ?? [];
  const lead = findings[0]?.code;
  const ring =
    lead === "canopy_water_stress"
      ? "border-sev-severe/40"
      : lead === "no_canopy_water_change"
        ? "border-sev-negligible/40"
        : "border-border";

  const c = reading.canopy;
  const s = reading.soil;

  return (
    // min-w-0: a grid item defaults to its content's minimum width, and an SVG
    // chart has a wide one, so without this the panel pushes past its column.
    <div className={`min-w-0 rounded-lg border ${ring} bg-muted/40 px-4 py-3 sm:col-span-2`}>
      <div className="mb-1.5 flex items-center gap-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        <Droplets className="size-3.5" aria-hidden />
        Leaf water and soil moisture
      </div>

      <div className="space-y-2 text-sm">
        {reading.synthetic && (
          <p className="rounded bg-sev-catastrophic-bg px-2 py-1 text-xs font-semibold text-on-destructive-bg">
            Generated for testing — not a satellite observation. Carries no weight.
          </p>
        )}

        {reading.relevance && (
          <p className="text-xs leading-relaxed text-muted-foreground">
            <span className="font-medium text-foreground">Bearing on this calamity: </span>
            {reading.relevance}
          </p>
        )}

        {findings.map((f) => (
          <div key={f.code} className="space-y-1">
            <p className="font-medium">
              {label(f.code)}
              <span className="ml-1 font-normal text-muted-foreground">({f.confidence})</span>
            </p>
            {f.detail && <Note>{f.detail}</Note>}
            {f.caveats?.map((x) => <Note key={x}>{x}</Note>)}
          </div>
        ))}

        {c?.season && c.season.length > 0 && (
          <SeasonChart
            season={c.season}
            normal={c.prior_seasons_ndmi}
            incidentDate={incidentDate}
          />
        )}

        <dl className="grid grid-cols-2 gap-x-6 gap-y-2 pt-1 sm:grid-cols-4">
          <Figure label="NDMI near the event" value={num(c?.near_incident_ndmi)} />
          <Figure
            label={`Own normal (${c?.prior_seasons_observed ?? 0} season${
              c?.prior_seasons_observed === 1 ? "" : "s"
            })`}
            value={num(c?.prior_seasons_ndmi)}
          />
          <Figure label="Against its normal" value={signed(c?.anomaly_vs_prior_seasons)} />
          <Figure label="Against neighbours" value={signed(c?.gap_vs_neighbours)} />
          {s?.index_near_incident !== undefined && (
            <Figure
              label={`Soil index · ${s.index_date ?? ""}`}
              value={`${Math.round(s.index_near_incident * 100)}%`}
              hint={s.canopy_obscured ? "dense canopy — weak guide" : undefined}
            />
          )}
        </dl>

        {reading.caveats?.map((x) => <Note key={x}>{x}</Note>)}
        <Note>
          Leaf water, not loss. Relative to this field&rsquo;s own seasons and the
          cropland within 1.5&nbsp;km; it does not enter the damage figure.
        </Note>
      </div>
    </div>
  );
}

/* ------------------------------------------------------------------ */

type SeasonPass = NonNullable<NonNullable<MoistureReading["canopy"]>["season"]>[number];

/**
 * The season as two lines on one NDMI axis.
 *
 * Inline SVG: a handful of clear passes over three months does not justify a
 * charting library. One y-axis only — both series are NDMI. The prior-season
 * normal is a dashed neutral rule, not a third coloured series, because it is
 * a reference and not an entity. The incident is a vertical rule.
 */
function SeasonChart({
  season,
  normal,
  incidentDate,
}: {
  season: SeasonPass[];
  normal?: number;
  incidentDate?: string;
}) {
  const [hover, setHover] = useState<number | null>(null);

  // Drawn at the width it is shown at, in pixels, rather than scaled from a
  // fixed viewBox. A scaled viewBox scales the text with it: labels meant to be
  // ten pixels rendered at nearly twenty on a wide screen and at five on a
  // phone. Measuring keeps one SVG unit equal to one pixel at every width.
  const box = useRef<HTMLDivElement>(null);
  const [W, setW] = useState(560);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const measure = () => setW(Math.max(240, Math.round(el.getBoundingClientRect().width)));
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const H = 150;
  const pad = { l: 36, r: 12, t: 12, b: 22 };

  const DAY = 86_400_000;
  const times = season.map((p) => Date.parse(p.date));
  const inc = incidentDate ? Date.parse(incidentDate) : NaN;
  // Padded by a few days either side, so a season with a single clear pass --
  // the ordinary monsoon case -- does not pin its only point to the frame
  // edge beside the event rule.
  const tMin = Math.min(...times, Number.isFinite(inc) ? inc : Infinity) - 4 * DAY;
  const tMax = Math.max(...times, Number.isFinite(inc) ? inc : -Infinity) + 4 * DAY;
  const span = Math.max(DAY, tMax - tMin);

  const values = season.flatMap((p) => [p.ndmi, p.ring_ndmi ?? p.ndmi]);
  if (normal !== undefined) values.push(normal);
  const vMin = Math.floor((Math.min(...values) - 0.03) * 20) / 20;
  const vMax = Math.ceil((Math.max(...values) + 0.03) * 20) / 20;
  const vSpan = Math.max(0.05, vMax - vMin);

  const x = (t: number) => pad.l + ((t - tMin) / span) * (W - pad.l - pad.r);
  const y = (v: number) => pad.t + (1 - (v - vMin) / vSpan) * (H - pad.t - pad.b);

  const fieldPts = season.map((p, i) => [x(times[i]), y(p.ndmi)] as const);
  const peerPts = season
    .map((p, i) => (p.ring_ndmi === undefined ? null : ([x(times[i]), y(p.ring_ndmi)] as const)))
    .filter((p): p is readonly [number, number] => p !== null);

  const path = (pts: readonly (readonly [number, number])[]) =>
    pts.map(([px, py], i) => `${i ? "L" : "M"}${px.toFixed(1)},${py.toFixed(1)}`).join("");

  // On round 0.05 steps, which the domain is already snapped to. A midpoint
  // tick would label a 0.275 gridline "0.28".
  const ticks: number[] = [];
  for (let v = vMin; v <= vMax + 1e-9; v += 0.05) ticks.push(Math.round(v * 100) / 100);
  const hovered = hover === null ? null : season[hover];

  return (
    <figure className="pt-1">
      {/* Legend: always present for two series, so identity is never colour alone. */}
      <figcaption className="mb-1 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <span className="text-foreground">
          {season.length} clear pass{season.length === 1 ? "" : "es"} this season
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className="inline-block h-0.5 w-4 rounded bg-chart-field" /> This field
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className="inline-block h-0.5 w-4 rounded bg-chart-peer" /> Cropland within 1.5 km
        </span>
        {normal !== undefined && (
          <span className="inline-flex items-center gap-1.5">
            <span className="inline-block w-4 border-t-2 border-dashed border-faint" /> Its normal
          </span>
        )}
      </figcaption>

      <div className="relative" ref={box}>
        <svg
          width={W}
          height={H}
          viewBox={`0 0 ${W} ${H}`}
          className="block max-w-full overflow-visible"
          role="img"
          aria-label={`NDMI over the season: ${season.length} clear passes for this field and the surrounding cropland`}
          onMouseLeave={() => setHover(null)}>
          {/* Recessive grid and axis. */}
          {ticks.map((v) => (
            <g key={v}>
              <line x1={pad.l} x2={W - pad.r} y1={y(v)} y2={y(v)}
                className="stroke-border" strokeWidth={1} />
              <text x={pad.l - 6} y={y(v) + 3} textAnchor="end"
                className="fill-muted-foreground text-[10px] tabular-nums">
                {v.toFixed(2)}
              </text>
            </g>
          ))}

          {normal !== undefined && (
            <line x1={pad.l} x2={W - pad.r} y1={y(normal)} y2={y(normal)}
              className="stroke-faint" strokeWidth={1.5} strokeDasharray="4 3" />
          )}

          {Number.isFinite(inc) && (
            <g>
              <line x1={x(inc)} x2={x(inc)} y1={pad.t} y2={H - pad.b}
                className="stroke-muted-foreground" strokeWidth={1} />
              <text x={x(inc) + 4} y={pad.t + 9}
                className="fill-muted-foreground text-[10px]">event</text>
            </g>
          )}

          {peerPts.length > 1 && (
            <path d={path(peerPts)} fill="none" className="stroke-chart-peer" strokeWidth={2}
              strokeLinejoin="round" strokeLinecap="round" />
          )}
          {fieldPts.length > 1 && (
            <path d={path(fieldPts)} fill="none" className="stroke-chart-field" strokeWidth={2}
              strokeLinejoin="round" strokeLinecap="round" />
          )}

          {season.map((p, i) => (
            <g key={p.date}>
              {p.ring_ndmi !== undefined && (
                <circle cx={x(times[i])} cy={y(p.ring_ndmi)} r={4}
                  className="fill-chart-peer stroke-card" strokeWidth={2} />
              )}
              <circle cx={x(times[i])} cy={y(p.ndmi)} r={4}
                className="fill-chart-field stroke-card" strokeWidth={2} />
              {/* Hit target wider than the mark. */}
              <rect x={x(times[i]) - 12} y={pad.t} width={24} height={H - pad.t - pad.b}
                fill="transparent" onMouseEnter={() => setHover(i)}
                onFocus={() => setHover(i)} tabIndex={0}
                aria-label={`${p.date}: field ${p.ndmi.toFixed(2)}${
                  p.ring_ndmi !== undefined ? `, neighbours ${p.ring_ndmi.toFixed(2)}` : ""
                }`} />
            </g>
          ))}

          {hovered && hover !== null && (
            <line x1={x(times[hover])} x2={x(times[hover])} y1={pad.t} y2={H - pad.b}
              className="stroke-foreground/30" strokeWidth={1} />
          )}
        </svg>

        {hovered && hover !== null && (
          <div
            className="pointer-events-none absolute top-0 z-10 rounded-md border border-border bg-popover px-2.5 py-1.5 text-xs shadow-sm"
            style={{
              left: `${(x(times[hover]) / W) * 100}%`,
              transform: x(times[hover]) > W / 2 ? "translateX(-105%)" : "translateX(8px)",
            }}>
            <p className="font-medium text-foreground">{hovered.date}</p>
            <p className="tabular-nums text-muted-foreground">
              This field <span className="text-foreground">{hovered.ndmi.toFixed(3)}</span>
            </p>
            {hovered.ring_ndmi !== undefined && (
              <p className="tabular-nums text-muted-foreground">
                Neighbours <span className="text-foreground">{hovered.ring_ndmi.toFixed(3)}</span>
              </p>
            )}
            <p className="tabular-nums text-muted-foreground">
              {Math.round(hovered.clear_fraction * 100)}% of the field clear
            </p>
          </div>
        )}
      </div>
    </figure>
  );
}

function Figure({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div>
      <dt className="text-xs text-faint">{label}</dt>
      <dd className="tabular-nums text-sm font-medium text-foreground">{value}</dd>
      {hint && <dd className="text-xs text-muted-foreground">{hint}</dd>}
    </div>
  );
}

function Note({ children }: { children: React.ReactNode }) {
  return <p className="text-xs leading-relaxed text-muted-foreground">{children}</p>;
}

function label(code?: string): string {
  switch (code) {
    case "canopy_water_stress":
      return "Canopy water stress";
    case "locality_dry":
      return "Whole locality dry";
    case "wetter_than_neighbours":
      return "Wetter than surrounding fields";
    case "no_canopy_water_change":
      return "Leaf water at its usual level";
    case "surface_wet":
      return "Soil near its wettest";
    case "surface_dry":
      return "Soil near its driest";
    default:
      return (code ?? "").replaceAll("_", " ");
  }
}

function num(v?: number): string {
  return v === undefined ? "—" : v.toFixed(2);
}

function signed(v?: number): string {
  return v === undefined ? "—" : `${v > 0 ? "+" : ""}${v.toFixed(2)}`;
}
