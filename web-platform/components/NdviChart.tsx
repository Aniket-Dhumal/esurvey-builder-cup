"use client";

import { useMemo, useState } from "react";
import type { NdviPoint } from "@/lib/types";
import { daysBetween, formatDate } from "@/lib/format";

/**
 * NDVI time series, drawn as inline SVG.
 *
 * Hand-rolled rather than pulled from a charting library because this chart
 * has to say two things no general-purpose chart says: where the cloud gaps
 * are, and which observation was taken as the baseline. Both are load-bearing
 * for reading the assessment — a long gap means the series may have missed the
 * canopy peak entirely, and the baseline is the number every loss figure is
 * measured against.
 *
 * Points are spaced by actual date, not by index, so a two-month gap looks
 * like a two-month gap.
 */
/** Chart margins. A layout constant, so it is not a dependency of anything. */
const pad = { top: 16, right: 16, bottom: 30, left: 40 };

export function NdviChart({
  series,
  baselineDate,
  sowingDate,
  incidentDate,
  gapThresholdDays = 30,
}: {
  series: NdviPoint[];
  baselineDate?: string;
  sowingDate?: string;
  incidentDate?: string;
  gapThresholdDays?: number;
}) {
  const [hover, setHover] = useState<number | null>(null);

  const W = 720;
  const H = 240;

  const geom = useMemo(() => {
    if (series.length === 0) return null;

    const t0 = Date.parse(sowingDate ?? series[0].date);
    const t1 = Date.parse(incidentDate ?? series[series.length - 1].date);
    const span = Math.max(t1 - t0, 1);

    // NDVI is bounded [-1, 1] but crops live in roughly [0, 1]. Fit to the
    // data with headroom rather than always drawing the full range, which
    // would flatten every real series into a hairline.
    const values = series.map((p) => p.ndvi);
    const lo = Math.max(0, Math.min(...values) - 0.08);
    const hi = Math.min(1, Math.max(...values) + 0.08);
    const range = Math.max(hi - lo, 0.1);

    const x = (date: string) =>
      pad.left +
      ((Date.parse(date) - t0) / span) * (W - pad.left - pad.right);
    const y = (v: number) =>
      pad.top + (1 - (v - lo) / range) * (H - pad.top - pad.bottom);

    const pts = series.map((p) => ({ ...p, cx: x(p.date), cy: y(p.ndvi) }));

    // Runs longer than the threshold are drawn dashed: the line between two
    // distant passes is interpolation, not observation, and should not look
    // like data.
    const segments = pts.slice(1).map((p, i) => {
      const prev = pts[i];
      const gap = daysBetween(prev.date, p.date) ?? 0;
      return { from: prev, to: p, gap, wide: gap > gapThresholdDays };
    });

    const ticks = [lo, lo + range / 2, hi].map((v) => ({
      v,
      y: y(v),
    }));

    return { pts, segments, ticks, x, y, lo, hi };
  }, [series, sowingDate, incidentDate, gapThresholdDays]);

  if (!geom) {
    return (
      <div className="flex h-[240px] items-center justify-center text-sm text-muted-foreground">
        No cloud-free observations in this window.
      </div>
    );
  }

  const { pts, segments, ticks } = geom;
  const active = hover !== null ? pts[hover] : null;

  return (
    <div className="relative">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        className="w-full"
        role="img"
        aria-label={`NDVI time series, ${series.length} observations`}
      >
        {/* horizontal guides */}
        {ticks.map((t) => (
          <g key={t.v}>
            <line
              x1={pad.left}
              x2={W - pad.right}
              y1={t.y}
              y2={t.y}
              stroke="var(--border)"
              strokeWidth="1"
            />
            <text
              x={pad.left - 8}
              y={t.y + 3.5}
              textAnchor="end"
              className="tnum"
              fontSize="10"
              fill="var(--faint)"
            >
              {t.v.toFixed(2)}
            </text>
          </g>
        ))}

        {/* connecting segments; dashed across a long blind stretch */}
        {segments.map((s, i) => (
          <line
            key={i}
            x1={s.from.cx}
            y1={s.from.cy}
            x2={s.to.cx}
            y2={s.to.cy}
            stroke="var(--primary)"
            strokeWidth="2"
            strokeLinecap="round"
            strokeDasharray={s.wide ? "3 4" : undefined}
            opacity={s.wide ? 0.45 : 0.9}
          />
        ))}

        {/* gap annotation on the widest blind stretch */}
        {segments
          .filter((s) => s.wide)
          .map((s, i) => (
            <text
              key={`gap-${i}`}
              x={(s.from.cx + s.to.cx) / 2}
              y={Math.min(s.from.cy, s.to.cy) - 8}
              textAnchor="middle"
              fontSize="9.5"
              fill="var(--sev-moderate)"
            >
              {s.gap}d gap
            </text>
          ))}

        {/* observations */}
        {pts.map((p, i) => {
          const isBaseline = baselineDate && p.date === baselineDate;
          return (
            <g key={p.date + i}>
              {isBaseline && (
                <circle
                  cx={p.cx}
                  cy={p.cy}
                  r="8"
                  fill="none"
                  stroke="var(--primary)"
                  strokeWidth="1.25"
                  opacity="0.5"
                />
              )}
              <circle
                cx={p.cx}
                cy={p.cy}
                r={hover === i ? 5 : 3.5}
                fill="var(--card)"
                stroke="var(--primary)"
                strokeWidth="2"
              />
              {/* generous hit area — the dots are small */}
              <circle
                cx={p.cx}
                cy={p.cy}
                r="14"
                fill="transparent"
                onMouseEnter={() => setHover(i)}
                onMouseLeave={() => setHover(null)}
                style={{ cursor: "pointer" }}
              />
            </g>
          );
        })}

        {/* date axis: first and last only, to avoid crowding */}
        <text
          x={pad.left}
          y={H - 8}
          fontSize="10"
          fill="var(--faint)"
        >
          {formatDate(pts[0].date)}
        </text>
        <text
          x={W - pad.right}
          y={H - 8}
          textAnchor="end"
          fontSize="10"
          fill="var(--faint)"
        >
          {formatDate(pts[pts.length - 1].date)}
        </text>
      </svg>

      {active && (
        <div
          className="pointer-events-none absolute z-10 -translate-x-1/2 -translate-y-full rounded-md border border-border bg-card px-2.5 py-1.5 shadow-lg"
          style={{
            left: `${(active.cx / W) * 100}%`,
            top: `${(active.cy / H) * 100}%`,
          }}
        >
          <div className="tnum text-sm font-semibold text-foreground">
            {active.ndvi.toFixed(4)}
          </div>
          <div className="text-xs text-muted-foreground">
            {formatDate(active.date)}
          </div>
        </div>
      )}

      <div className="mt-1 flex flex-wrap items-center gap-4 text-xs text-muted-foreground">
        <span className="inline-flex items-center gap-1.5">
          <span className="inline-block size-2 rounded-full border-2 border-primary" />
          Observation
        </span>
        {baselineDate && (
          <span className="inline-flex items-center gap-1.5">
            <span className="inline-block size-2.5 rounded-full border border-primary opacity-50" />
            Baseline (peak observed)
          </span>
        )}
        <span className="inline-flex items-center gap-1.5">
          <svg width="18" height="2" aria-hidden>
            <line
              x1="0"
              y1="1"
              x2="18"
              y2="1"
              stroke="var(--primary)"
              strokeWidth="2"
              strokeDasharray="3 4"
              opacity="0.45"
            />
          </svg>
          Interpolated across a cloud gap
        </span>
      </div>
    </div>
  );
}
