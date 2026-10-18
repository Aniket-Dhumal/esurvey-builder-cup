"use client";

import { useCallback, useMemo, useState } from "react";
import { Check, MapPin, RotateCcw, Undo2 } from "lucide-react";

import { putBoundary } from "@/lib/offline/db";
import { sync } from "@/lib/offline/sync";
import { accuracyLabel, usePosition } from "@/lib/sensors";
import { LINES, useVoice } from "@/lib/voice";

/**
 * Walking the edge of a plot.
 *
 * # Why a farmer is asked to do this at all
 *
 * The land registry returns no boundary for their field. Without one the app
 * cannot check that a photograph was taken inside their own plot, the
 * satellite cannot find the field to look at, and no assessment can be made
 * at all. Five minutes of walking replaces a record that does not exist.
 *
 * # What the screen has to get right
 *
 * The farmer is outdoors, holding a phone in one hand, and cannot read a
 * paragraph. So: one instruction at a time, spoken as well as shown, a big
 * target to press at each corner, and the enclosed area in the units they
 * think in — guntha as well as hectares, because a farmer in Maharashtra says
 * guntha.
 *
 * The position accuracy is on screen the whole time, because a corner dropped
 * on a poor fix is the one that puts their boundary on a neighbour's field,
 * and they can fix it by waiting a moment.
 */

interface Corner {
  lat: number;
  lon: number;
  accuracy_m: number;
  at: string;
}

export function TraceBoundary({
  farmId,
  surveyNumber,
  registryExtentHa,
  onDone,
  onCancel,
}: {
  farmId: string;
  surveyNumber: string;
  registryExtentHa?: number;
  onDone?: () => void;
  onCancel?: () => void;
}) {
  const { state, retry } = usePosition(true);
  const voice = useVoice();

  const [corners, setCorners] = useState<Corner[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);

  const fix = state.status === "ready" ? state.fix : null;
  const quality = fix ? accuracyLabel(fix.accuracyM) : null;

  // Held back rather than merely discouraged: a corner dropped on a 60 m fix
  // is worse than no corner, because it silently moves the whole edge.
  const canDrop = !!fix && fix.accuracyM <= 50;

  const drop = useCallback(() => {
    if (!fix) return;
    setCorners((c) => [
      ...c,
      {
        lat: fix.lat,
        lon: fix.lon,
        accuracy_m: Math.round(fix.accuracyM * 10) / 10,
        at: new Date(fix.at).toISOString(),
      },
    ]);
    setError(null);
    voice.speak(
      "कोपरा नोंदवला. पुढच्या कोपऱ्यावर जा.",
      "Corner recorded. Walk to the next corner.",
    );
  }, [fix, voice]);

  const undo = useCallback(() => setCorners((c) => c.slice(0, -1)), []);
  const restart = useCallback(() => {
    setCorners([]);
    setError(null);
  }, []);

  // The area as they walk, so a wrong turn is obvious before they finish.
  const areaHa = useMemo(() => shoelaceHectares(corners), [corners]);
  const worst = corners.reduce((m, c) => Math.max(m, c.accuracy_m), 0);

  async function finish() {
    setBusy(true);
    setError(null);
    try {
      // Written to the phone first, always.
      //
      // A farmer walks their boundary standing in the field, which is exactly
      // where there is no signal. Sending it at this moment and reporting a
      // failure -- as the first version did -- throws away five minutes of
      // somebody pacing a field edge in the sun, and they have no way to know
      // it was lost until they walk it again.
      await putBoundary({
        farmId,
        surveyNumber,
        points: corners,
        areaHa: areaHa,
        worstAccuracyM: worst,
        status: "pending",
        attempts: 0,
      });

      setDone(
        "हद्द या फोनवर सुरक्षित आहे. नेटवर्क आल्यावर ती आपोआप पाठवली जाईल.",
      );
      voice.speak(
        "शेताची हद्द नोंदवली गेली आहे.",
        "Your field boundary has been recorded.",
      );
      onDone?.();

      // Then sent, if anything can get out. A failure here changes nothing:
      // it is already queued and the sync engine will keep trying.
      void sync().catch(() => {});
    } catch (e) {
      setError(
        e instanceof Error
          ? e.message
          : "The boundary could not be saved on this phone.",
      );
    } finally {
      setBusy(false);
    }
  }

  if (done) {
    return (
      <div className="rounded-xl border border-sev-negligible/40 bg-sev-negligible-bg p-4">
        <div className="mb-2 flex items-center gap-2 font-medium text-sev-negligible">
          <Check className="size-5" />
          हद्द नोंदवली · Boundary recorded
        </div>
        <p className="text-sm">{done}</p>
        <p className="mt-1 text-xs text-muted-foreground">
          Saved on this phone. It goes to the office by itself when there is a
          signal — you do not need to wait here.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-4 rounded-xl border border-border p-4">
      <header>
        <h3 className="text-base font-medium">
          शेताची हद्द नोंदवा
          <span className="ml-2 text-sm text-muted-foreground">
            Record your field boundary
          </span>
        </h3>
        <p className="mt-1 text-sm text-muted-foreground">
          सर्व्हे नं. {surveyNumber} — शेताच्या कडेने चाला. प्रत्येक कोपऱ्यावर थांबून
          खालील बटण दाबा.
        </p>
        <p className="text-xs text-faint">
          Walk the edge of the field. Stop at each corner and press the button.
        </p>
      </header>

      {/* The fix, always visible: this is what decides whether the boundary
          lands on their field or the next one. */}
      <div
        className={
          "flex items-center justify-between rounded-lg px-3 py-2.5 text-sm " +
          (quality?.tone === "good"
            ? "bg-sev-negligible-bg text-sev-negligible"
            : quality?.tone === "fair"
              ? "bg-sev-moderate-bg text-sev-moderate"
              : "bg-sev-severe-bg text-sev-severe")
        }
      >
        <span className="flex items-center gap-2">
          <MapPin className="size-4" />
          {state.status === "ready" && fix
            ? `ठिकाण ${quality?.label === "good" ? "चांगले" : quality?.label === "usable" ? "ठीक" : "कमजोर"} · ±${Math.round(fix.accuracyM)} m`
            : state.status === "locating"
              ? "ठिकाण शोधत आहे… · Finding your position"
              : state.status === "denied"
                ? "ठिकाणाची परवानगी नाही · Location permission is off"
                : "ठिकाण मिळत नाही · No position"}
        </span>
        {state.status !== "ready" && (
          <button type="button" onClick={retry} className="underline">
            पुन्हा · Retry
          </button>
        )}
      </div>

      {!canDrop && state.status === "ready" && (
        <p className="text-sm text-sev-severe">
          ठिकाण नीट मिळत नाही. मोकळ्या जागी जा आणि थोडा वेळ थांबा.
          <span className="mt-0.5 block text-xs text-muted-foreground">
            The position is too uncertain to mark a corner. Move into the open
            and wait a moment — a corner marked now could put your boundary on
            a neighbour&rsquo;s field.
          </span>
        </p>
      )}

      <button
        type="button"
        onClick={drop}
        disabled={!canDrop}
        className="w-full rounded-xl bg-primary px-4 py-6 text-lg font-medium text-primary-foreground disabled:opacity-40"
      >
        हा कोपरा नोंदवा
        <span className="mt-0.5 block text-sm font-normal opacity-80">
          Mark this corner ({corners.length})
        </span>
      </button>

      {corners.length > 0 && (
        <div className="space-y-2 rounded-lg bg-muted/40 px-3 py-3 text-sm">
          <div className="flex items-baseline justify-between">
            <span>{corners.length} कोपरे · corners</span>
            {areaHa > 0 && (
              <span className="tnum font-medium">
                ≈ {areaHa.toFixed(2)} ha · {(areaHa * 40).toFixed(0)} गुंठे
              </span>
            )}
          </div>
          {registryExtentHa ? (
            <p className="text-xs text-muted-foreground">
              नोंदीनुसार क्षेत्र {registryExtentHa} ha · the registry records{" "}
              {registryExtentHa} ha for this survey number
            </p>
          ) : null}
          {worst > 0 && (
            <p className="text-xs text-muted-foreground">
              Worst corner ±{Math.round(worst)} m
            </p>
          )}

          <div className="flex gap-2 pt-1">
            <button
              type="button"
              onClick={undo}
              className="flex flex-1 items-center justify-center gap-1.5 rounded-lg border border-border px-3 py-2"
            >
              <Undo2 className="size-4" />
              मागे · Undo
            </button>
            <button
              type="button"
              onClick={restart}
              className="flex flex-1 items-center justify-center gap-1.5 rounded-lg border border-border px-3 py-2"
            >
              <RotateCcw className="size-4" />
              पुन्हा · Start again
            </button>
          </div>
        </div>
      )}

      {error && (
        <p className="rounded-lg bg-sev-severe-bg px-3 py-2.5 text-sm text-sev-severe">
          {error}
        </p>
      )}

      <div className="flex gap-2">
        {onCancel && (
          <button
            type="button"
            onClick={onCancel}
            className="flex-1 rounded-lg border border-border px-4 py-3"
          >
            रद्द · Cancel
          </button>
        )}
        <button
          type="button"
          onClick={finish}
          disabled={corners.length < 3 || busy}
          className="flex-[2] rounded-lg bg-primary px-4 py-3 font-medium text-primary-foreground disabled:opacity-40"
        >
          {busy
            ? "नोंदवत आहे…"
            : corners.length < 3
              ? "किमान ३ कोपरे हवेत · At least 3 corners"
              : "हद्द पूर्ण करा · Finish the boundary"}
        </button>
      </div>

      <p className="text-xs leading-relaxed text-faint">
        {LINES.welcome ? "" : ""}
        ही हद्द तुम्ही स्वतः नोंदवली आहे. अधिकारी पाहणीच्या वेळी ती तपासतील.
        <span className="mt-0.5 block">
          You drew this boundary yourself. An officer will check it when they
          visit, and every figure taken from it says so until they do.
        </span>
      </p>
    </div>
  );
}

/**
 * The enclosed area, for the farmer to sanity-check as they walk.
 *
 * A local flat-earth approximation: at field scale the error is far below the
 * GPS uncertainty already on screen, and the authoritative figure is computed
 * by PostGIS on the server. This one exists so a wrong turn is obvious before
 * somebody walks another kilometre.
 */
function shoelaceHectares(corners: Corner[]): number {
  if (corners.length < 3) return 0;

  const lat0 = (corners.reduce((s, c) => s + c.lat, 0) / corners.length) * (Math.PI / 180);
  const mPerLat = 111_132;
  const mPerLon = 111_320 * Math.cos(lat0);

  let twice = 0;
  for (let i = 0; i < corners.length; i++) {
    const a = corners[i];
    const b = corners[(i + 1) % corners.length];
    twice += (a.lon * mPerLon) * (b.lat * mPerLat) - (b.lon * mPerLon) * (a.lat * mPerLat);
  }
  return Math.abs(twice / 2) / 10_000;
}
