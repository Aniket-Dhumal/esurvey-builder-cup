"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Camera, MapPin, Play, TriangleAlert, Video } from "lucide-react";

import { api, API_BASE } from "@/lib/api";
import { getAccessToken } from "@/lib/session";
import type { Capture } from "@/lib/types";
import { istDateTime } from "@/lib/format";
import { Card, CardHeader, Note, Skeleton } from "@/components/ui";
import { EvidenceCoverage } from "@/components/EvidenceCoverage";

/** A clip rather than a still. The content type is what the handset sent. */
function isVideo(c: Capture): boolean {
  return c.content_type.startsWith("video/");
}

const KIND_LABEL: Record<string, string> = {
  wide_shot: "The whole field",
  damage_closeup: "The damage",
  crop_closeup: "The crop",
  boundary_point: "A boundary point",
  // The clip taken standing at a directed point, panning across the field.
  // Without this line the tile read "station_video" at the officer.
  station_video: "The clip from this point",
};

/** The two failures that mean a photograph is not evidence of this loss. */
const DISQUALIFYING = new Set(["inside_parcel", "after_the_incident"]);

/**
 * The farmer's photographs, as the verifying officer sees them.
 *
 * Every capture is shown, including the ones that failed a gate. A photograph
 * taken on the bund of one's own field is outside the polygon and is not
 * fraud; the officer is given the distance and decides. What the panel does is
 * separate the two failures that mean "this is not evidence of this loss" from
 * the ones that are provenance notes, because folding them together is what
 * makes an officer either ignore all the flags or distrust all the photographs.
 */
export function EvidencePanel({ reportId }: { reportId: string }) {
  const [captures, setCaptures] = useState<Capture[] | null>(null);
  const [explanations, setExplanations] = useState<Record<string, string>>({});
  const [error, setError] = useState<string | null>(null);
  const [blobs, setBlobs] = useState<Record<string, string>>({});
  /** Clips the officer has asked for, while they are being fetched. */
  const [playing, setPlaying] = useState<Record<string, boolean>>({});

  const load = useCallback(() => {
    api
      .listCaptures(reportId)
      .then((r) => {
        setCaptures(r.captures);
        setExplanations(r.gate_explanations ?? {});
      })
      .catch(() => setError("The evidence could not be loaded."));
  }, [reportId]);

  useEffect(load, [load]);

  // Every object URL this panel makes, so all of them are revoked when it
  // goes — the clips an officer chose to watch included, which are created
  // outside the effect below and would otherwise be held until the tab closed.
  const made = useRef<string[]>([]);
  useEffect(
    () => () => {
      made.current.forEach(URL.revokeObjectURL);
      made.current = [];
    },
    [],
  );

  const fetchMedia = useCallback(
    async (id: string): Promise<string | null> => {
      try {
        const res = await fetch(
          `${API_BASE}/api/v1/reports/${reportId}/captures/${id}/media`,
          { headers: { Authorization: `Bearer ${getAccessToken() ?? ""}` } },
        );
        if (!res.ok) return null;
        const url = URL.createObjectURL(await res.blob());
        made.current.push(url);
        return url;
      } catch {
        return null;
      }
    },
    [reportId],
  );

  // The media endpoint needs the bearer token, so an <img src> cannot fetch it
  // directly — the stills are pulled here and turned into object URLs.
  //
  // Clips are not. A directed walk carries four of them, several megabytes
  // each, and pulling them all to render a grid would spend the officer's
  // connection on video nobody has asked to watch. A clip is fetched when its
  // play button is pressed.
  useEffect(() => {
    if (!captures) return;
    let cancelled = false;
    (async () => {
      for (const c of captures) {
        if (isVideo(c)) continue;
        const url = await fetchMedia(c.id);
        // One unreadable photograph should not blank the panel.
        if (url && !cancelled) setBlobs((prev) => ({ ...prev, [c.id]: url }));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [captures, fetchMedia]);

  /** Fetch one clip, because the officer pressed play on it. */
  const playClip = useCallback(
    async (id: string) => {
      setPlaying((prev) => ({ ...prev, [id]: true }));
      const url = await fetchMedia(id);
      if (url) setBlobs((prev) => ({ ...prev, [id]: url }));
      else setPlaying((prev) => ({ ...prev, [id]: false }));
    },
    [fetchMedia],
  );

  if (error) {
    return (
      <Card>
        <CardHeader title="Field evidence" />
        <div className="px-4 py-4 text-sm text-muted-foreground">{error}</div>
      </Card>
    );
  }

  if (!captures) {
    return (
      <Card>
        <CardHeader title="Field evidence" />
        <div className="space-y-2 p-5">
          <Skeleton className="h-32 w-full" />
        </div>
      </Card>
    );
  }

  const disqualified = captures.filter((c) =>
    c.gates_failed.some((g) => DISQUALIFYING.has(g)),
  ).length;

  // Counted off the captures themselves rather than the report's running
  // total, because this panel is looking at the evidence and the two should
  // be able to disagree out loud if the trigger ever drifts.
  //
  // A point counts when it has both a still and a clip — the same rule the
  // handset walks by and migration 0039 counts by. A point with only one of
  // the two is a point the farmer started and did not finish, and rounding it
  // up here would put a fuller walk on the screen than happened.
  const atPoint = new Map<number, { still: boolean; clip: boolean }>();
  for (const c of captures) {
    if (c.station_no === undefined) continue;
    const seen = atPoint.get(c.station_no) ?? { still: false, clip: false };
    if (isVideo(c)) seen.clip = true;
    else seen.still = true;
    atPoint.set(c.station_no, seen);
  }
  const whole = [...atPoint.values()].filter((p) => p.still && p.clip).length;
  const walked = atPoint.size > 0;

  return (
    <Card>
      <CardHeader
        title="Field evidence"
        subtitle="Captured by the farmer, with what the gates found"
        action={
          <span className="shrink-0 rounded-md bg-muted px-2 py-0.5 text-xs text-muted-foreground">
            {captures.length} {captures.length === 1 ? "item" : "items"}
          </span>
        }
      />

      {/* Which of the directed points this claim actually covers. Counting
          tiles does not answer it: four photographs from one corner and four
          points around the parcel are the same number of tiles and are not
          the same evidence (docs/EVIDENCE_REVALIDATION.md, V8). */}
      {walked && (
        <div className="border-b border-border px-4 py-2.5 text-xs">
          <EvidenceCoverage
            captureCount={captures.length}
            stationsCaptured={whole}
          />
        </div>
      )}

      {captures.length === 0 && (
        <div className="px-4 py-6 text-center">
          <Camera className="mx-auto size-6 text-muted-foreground" />
          <p className="mt-2 text-sm text-muted-foreground">
            Nothing was filed against this claim. Without ground imagery no
            damage figure can be stated, so this report needs a field visit.
          </p>
        </div>
      )}

      {captures.length > 0 && (
        <div className="space-y-4 px-4 py-4">
          {disqualified > 0 && (
            <Note>
              {disqualified} of these was not shown to the assessment model —
              taken outside the plot, or dated before the loss. It is still here
              for you to look at.
            </Note>
          )}

          <div className="grid gap-4 sm:grid-cols-2">
            {captures.map((c) => {
              const hard = c.gates_failed.filter((g) => DISQUALIFYING.has(g));
              const soft = c.gates_failed.filter((g) => !DISQUALIFYING.has(g));

              return (
                <figure
                  key={c.id}
                  className="overflow-hidden rounded-lg border border-border"
                >
                  {blobs[c.id] ? (
                    isVideo(c) ? (
                      /* A clip, played here rather than shown as a broken
                         image. Half the directed walk is video, and an
                         officer who cannot watch it is deciding on the
                         stills alone. */
                      <video
                        src={blobs[c.id]}
                        controls
                        autoPlay
                        preload="metadata"
                        playsInline
                        className="aspect-[4/3] w-full bg-black object-contain"
                      />
                    ) : (
                      /* eslint-disable-next-line @next/next/no-img-element */
                      <img
                        src={blobs[c.id]}
                        alt={KIND_LABEL[c.kind] ?? c.kind}
                        className="aspect-[4/3] w-full bg-muted object-cover"
                      />
                    )
                  ) : isVideo(c) ? (
                    <button
                      type="button"
                      onClick={() => playClip(c.id)}
                      disabled={playing[c.id]}
                      className="grid aspect-[4/3] w-full place-items-center gap-1.5 bg-muted hover:bg-accent disabled:opacity-60"
                    >
                      <Play className="size-6 text-muted-foreground" />
                      <span className="text-xs text-muted-foreground">
                        {playing[c.id]
                          ? "Loading the clip…"
                          : c.duration_ms !== undefined
                            ? `Watch the clip · ${(c.duration_ms / 1000).toFixed(0)} s`
                            : "Watch the clip"}
                      </span>
                    </button>
                  ) : (
                    <div className="grid aspect-[4/3] w-full place-items-center bg-muted">
                      <Camera className="size-5 text-muted-foreground" />
                    </div>
                  )}

                  <figcaption className="space-y-2 border-t border-border px-3 py-2.5">
                    <div className="flex items-baseline justify-between gap-2">
                      <span className="text-sm font-medium">
                        {/* Which point of the walk, where there was one. The
                            kind alone ("The damage") does not tell an officer
                            whether two tiles are two sides of the field or
                            two shots of the same corner. */}
                        {c.station_no !== undefined
                          ? `Point ${c.station_no} · ${KIND_LABEL[c.kind] ?? c.kind}`
                          : (KIND_LABEL[c.kind] ?? c.kind)}
                      </span>
                      <span className="text-xs text-faint">
                        {istDateTime(c.captured_at)}
                      </span>
                    </div>

                    {c.caption && (
                      <p className="text-xs leading-relaxed text-muted-foreground">
                        &ldquo;{c.caption}&rdquo;
                      </p>
                    )}

                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
                      <span
                        className={
                          "inline-flex items-center gap-1 " +
                          (c.inside_parcel === true
                            ? "text-sev-negligible"
                            : c.inside_parcel === false
                              ? "text-sev-catastrophic"
                              : "text-muted-foreground")
                        }
                      >
                        <MapPin className="size-3" />
                        {c.inside_parcel === true
                          ? "inside the plot"
                          : c.inside_parcel === false
                            ? `${Math.round(c.distance_from_parcel_m ?? 0)} m outside`
                            : "no boundary to check"}
                      </span>
                      {c.accuracy_m !== undefined && (
                        <span className="text-faint">±{Math.round(c.accuracy_m)} m</span>
                      )}
                      {/* How far from the directed point the farmer stood,
                          and how long the clip runs. Both are how a station
                          is judged to have been reached and filmed. */}
                      {c.station_offset_m !== undefined && (
                        <span className="text-faint">
                          {Math.round(c.station_offset_m)} m from the point
                        </span>
                      )}
                      {c.duration_ms !== undefined && (
                        <span className="inline-flex items-center gap-1 text-faint">
                          <Video className="size-3" />
                          {(c.duration_ms / 1000).toFixed(1)} s
                        </span>
                      )}
                    </div>

                    {hard.length > 0 && (
                      <ul className="space-y-0.5">
                        {hard.map((g) => (
                          <li
                            key={g}
                            className="flex gap-1.5 text-xs leading-snug text-sev-catastrophic"
                          >
                            <TriangleAlert className="mt-0.5 size-3 shrink-0" />
                            Not used as evidence — could not confirm that{" "}
                            {explanations[g] ?? g}
                          </li>
                        ))}
                      </ul>
                    )}

                    {soft.length > 0 && (
                      <ul className="space-y-0.5">
                        {soft.map((g) => (
                          <li key={g} className="text-xs leading-snug text-muted-foreground">
                            · Note: could not confirm that {explanations[g] ?? g}
                          </li>
                        ))}
                      </ul>
                    )}
                  </figcaption>
                </figure>
              );
            })}
          </div>
        </div>
      )}
    </Card>
  );
}
