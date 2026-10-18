"use client";

import { useMemo } from "react";
import type { Geometry } from "@/lib/types";

type Ring = [number, number][];

/**
 * Pulls the exterior rings out of a GeoJSON Polygon or MultiPolygon.
 *
 * AgriStack nests MultiPolygon one level deeper than Polygon, and its own
 * inline example in the spec gets that nesting wrong — so this narrows the
 * shape at runtime rather than trusting the declared type.
 */
function ringsOf(geometry: Geometry): Ring[] {
  const c = geometry.coordinates;
  if (!Array.isArray(c)) return [];

  const isPosition = (v: unknown): v is [number, number] =>
    Array.isArray(v) &&
    v.length >= 2 &&
    typeof v[0] === "number" &&
    typeof v[1] === "number";

  const isRing = (v: unknown): v is Ring =>
    Array.isArray(v) && v.length > 0 && isPosition(v[0]);

  if (geometry.type === "Polygon") {
    return (c as unknown[]).filter(isRing);
  }
  // MultiPolygon: [ polygon ][ ring ][ position ]
  const out: Ring[] = [];
  for (const polygon of c as unknown[]) {
    if (!Array.isArray(polygon)) continue;
    for (const ring of polygon) {
      if (isRing(ring)) out.push(ring);
    }
  }
  return out;
}

/**
 * Renders a parcel boundary as inline SVG.
 *
 * No mapping library: there is no basemap to fetch and no pan or zoom to
 * support — this is one field, drawn to fit. A tile-based map would add a
 * network dependency and a licence question for a shape we can project
 * ourselves in a dozen lines.
 *
 * Longitude is scaled by cos(latitude) so the parcel keeps its true aspect
 * ratio instead of being stretched east-west.
 */
export function ParcelMap({
  geometry,
  className = "",
  showVertices = true,
}: {
  geometry: Geometry | null;
  className?: string;
  showVertices?: boolean;
}) {
  const model = useMemo(() => {
    if (!geometry) return null;
    const rings = ringsOf(geometry);
    if (rings.length === 0) return null;

    const all = rings.flat();
    const lons = all.map((p) => p[0]);
    const lats = all.map((p) => p[1]);

    const minLon = Math.min(...lons);
    const maxLon = Math.max(...lons);
    const minLat = Math.min(...lats);
    const maxLat = Math.max(...lats);

    const midLat = (minLat + maxLat) / 2;
    const kx = Math.cos((midLat * Math.PI) / 180);

    const w = Math.max((maxLon - minLon) * kx, 1e-9);
    const h = Math.max(maxLat - minLat, 1e-9);

    const VB = 100;
    const padding = 8;
    const scale = (VB - padding * 2) / Math.max(w, h);

    // Centre the shape in the square viewBox.
    const offX = (VB - w * scale) / 2;
    const offY = (VB - h * scale) / 2;

    const project = ([lon, lat]: [number, number]) => ({
      x: offX + (lon - minLon) * kx * scale,
      // SVG y grows downward; latitude grows north.
      y: offY + (maxLat - lat) * scale,
    });

    const paths = rings.map((ring) => {
      const pts = ring.map(project);
      return {
        d:
          pts
            .map((p, i) => `${i === 0 ? "M" : "L"}${p.x.toFixed(3)},${p.y.toFixed(3)}`)
            .join(" ") + " Z",
        pts,
      };
    });

    const centroid = {
      lat: midLat,
      lon: (minLon + maxLon) / 2,
    };

    return { paths, centroid, vertexCount: all.length };
  }, [geometry]);

  if (!model) {
    return (
      <div
        className={`flex items-center justify-center rounded-md border border-dashed border-border bg-muted ${className}`}
      >
        <div className="px-6 py-10 text-center">
          <p className="text-sm font-medium text-foreground">No boundary</p>
          <p className="mt-1 max-w-xs text-xs leading-relaxed text-muted-foreground">
            AgriStack returns <span className="mono">plot_geometry</span> as
            null for this parcel. Nothing spatial can be computed without one.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className={className}>
      <svg
        viewBox="0 0 100 100"
        className="h-full w-full"
        role="img"
        aria-label="Parcel boundary"
      >
        <defs>
          <pattern
            id="parcel-hatch"
            width="4"
            height="4"
            patternUnits="userSpaceOnUse"
            patternTransform="rotate(45)"
          >
            <line
              x1="0"
              y1="0"
              x2="0"
              y2="4"
              stroke="var(--primary)"
              strokeWidth="0.6"
              opacity="0.25"
            />
          </pattern>
        </defs>

        {model.paths.map((p, i) => (
          <path
            key={i}
            d={p.d}
            fill="url(#parcel-hatch)"
            stroke="var(--primary)"
            strokeWidth="0.9"
            strokeLinejoin="round"
          />
        ))}

        {showVertices &&
          model.paths.flatMap((p, ri) =>
            p.pts.map((pt, i) => (
              <circle
                key={`${ri}-${i}`}
                cx={pt.x}
                cy={pt.y}
                r="0.9"
                fill="var(--card)"
                stroke="var(--primary)"
                strokeWidth="0.6"
              />
            )),
          )}
      </svg>

      <p className="mono mt-2 text-center text-xs text-faint">
        {model.centroid.lat.toFixed(6)}, {model.centroid.lon.toFixed(6)} ·{" "}
        {model.vertexCount} vertices
      </p>
    </div>
  );
}

/** Shoelace area in hectares, for the true polygon extent. */
export function polygonAreaHectares(geometry: Geometry | null): number | null {
  if (!geometry) return null;
  const rings = ringsOf(geometry);
  if (rings.length === 0) return null;

  const all = rings.flat();
  const midLat =
    all.reduce((s, p) => s + p[1], 0) / Math.max(all.length, 1);
  const mx = 111_320 * Math.cos((midLat * Math.PI) / 180);
  const my = 110_574;

  let total = 0;
  for (const ring of rings) {
    const closed =
      ring.length > 1 &&
      (ring[0][0] !== ring[ring.length - 1][0] ||
        ring[0][1] !== ring[ring.length - 1][1])
        ? [...ring, ring[0]]
        : ring;

    let s = 0;
    for (let i = 0; i < closed.length - 1; i++) {
      const [x1, y1] = [closed[i][0] * mx, closed[i][1] * my];
      const [x2, y2] = [closed[i + 1][0] * mx, closed[i + 1][1] * my];
      s += x1 * y2 - x2 * y1;
    }
    total += Math.abs(s) / 2;
  }
  return total / 10_000;
}
