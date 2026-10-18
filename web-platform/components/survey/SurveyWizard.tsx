"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Camera,
  Check,
  ChevronLeft,
  CloudOff,
  Loader2,
  MapPin,
  RotateCcw,
  Volume2,
  VolumeX,
} from "lucide-react";

import {
  accuracyLabel,
  compassPoint,
  tiltGuidance,
  useOnline,
  useOrientation,
  usePosition,
} from "@/lib/sensors";
import { LINES, useVoice } from "@/lib/voice";
import {
  newLocalId,
  putCapture,
  type DraftReport,
  type QueuedCapture,
} from "@/lib/offline/db";
import { sync } from "@/lib/offline/sync";
import { STAGES, stageLine, type Stage } from "./stages";

/**
 * The guided survey.
 *
 * One stage on screen at a time, each with a spoken instruction, live sensor
 * guidance, and a photograph. Nothing waits for the network: a capture is
 * written to the handset before anything else happens, and the upload is a
 * background concern that catches up.
 *
 * The design rule throughout is that the farmer is standing in a field holding
 * the phone in one hand. One decision per screen, targets big enough for a
 * thumb, and never a state where it is unclear whether the last photograph was
 * kept.
 */
export function SurveyWizard({
  draft,
  onFinished,
  onExit,
}: {
  draft: DraftReport;
  onFinished: () => void;
  onExit: () => void;
}) {
  const [index, setIndex] = useState(0);
  const [taken, setTaken] = useState<Record<string, QueuedCapture>>({});
  const [finishing, setFinishing] = useState(false);

  const online = useOnline();
  const voice = useVoice();
  const { state: fixState, best } = usePosition(true);
  const orientation = useOrientation(true);

  const stage = STAGES[index];
  const done = Object.keys(taken).length;
  const required = STAGES.filter((s) => !s.optional).length;

  // Ask for motion permission once, from the first tap. iOS only grants it
  // from a gesture, and a permission sheet appearing over the camera is the
  // worst possible moment for it.
  const askedMotion = useRef(false);
  const primeSensors = useCallback(() => {
    if (askedMotion.current) return;
    askedMotion.current = true;
    void orientation.request();
  }, [orientation]);

  // Speak the stage as it opens.
  useEffect(() => {
    const line = stageLine(stage);
    voice.speak(line.mr, line.en);
  }, [stage, voice]);

  const onCaptured = useCallback(
    (capture: QueuedCapture) => {
      setTaken((prev) => ({ ...prev, [stage.id]: capture }));
      voice.speak(LINES.captured.mr, LINES.captured.en);
      if (!online) {
        window.setTimeout(
          () => voice.speak(LINES.savedOffline.mr, LINES.savedOffline.en),
          1800,
        );
      }
    },
    [stage.id, voice, online],
  );

  async function finish() {
    setFinishing(true);
    voice.speak(LINES.allDone.mr, LINES.allDone.en);
    // Best effort. Everything is already on the handset, so a failure here is
    // a delay rather than a loss.
    await sync().catch(() => {});
    onFinished();
  }

  const readyToFinish = STAGES.filter((s) => !s.optional).every((s) => taken[s.id]);

  return (
    <div className="mx-auto max-w-lg pb-24">
      {/* Progress -------------------------------------------------------- */}
      <div className="sticky top-0 z-10 -mx-4 border-b bg-card px-4 py-3">
        <div className="flex items-center gap-3">
          <button
            onClick={onExit}
            className="rounded-md p-1.5 text-muted-foreground hover:bg-muted"
            aria-label="Back"
          >
            <ChevronLeft className="size-5" />
          </button>

          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-1.5">
              {STAGES.map((s, i) => (
                <div
                  key={s.id}
                  className={
                    "h-1.5 flex-1 rounded-full transition-colors " +
                    (taken[s.id]
                      ? "bg-primary"
                      : i === index
                        ? "bg-primary/40"
                        : "bg-muted")
                  }
                />
              ))}
            </div>
            <div className="mt-1 text-xs text-muted-foreground">
              {done} of {required} photographs
            </div>
          </div>

          <button
            onClick={voice.toggle}
            className="rounded-md p-1.5 text-muted-foreground hover:bg-muted"
            aria-label={voice.enabled ? "Turn voice off" : "Turn voice on"}
            aria-pressed={voice.enabled}
          >
            {voice.enabled && voice.available ? (
              <Volume2 className={"size-5 " + (voice.speaking ? "text-primary" : "")} />
            ) : (
              <VolumeX className="size-5" />
            )}
          </button>
        </div>
      </div>

      {/* Conditions ------------------------------------------------------ */}
      <div className="mt-4 space-y-2">
        <PositionBar state={fixState} best={best} voice={voice} />
        {!online && (
          <div className="flex items-center gap-2 rounded-lg bg-muted px-3 py-2 text-sm">
            <CloudOff className="size-4 shrink-0 text-muted-foreground" />
            <span className="text-muted-foreground">
              No network. Photographs are saved on your phone and sent later.
            </span>
          </div>
        )}
      </div>

      {/* Stage ----------------------------------------------------------- */}
      <div className="mt-5">
        <div className="flex items-baseline gap-2">
          <h2 className="text-xl font-semibold tracking-tight">{stage.titleMr}</h2>
          <span className="text-sm text-muted-foreground">{stage.titleEn}</span>
          {stage.optional && (
            <span className="ml-auto rounded bg-muted px-2 py-0.5 text-xs text-muted-foreground">
              optional
            </span>
          )}
        </div>

        <p className="mt-2 text-base leading-relaxed">{stage.instructionMr}</p>
        <p className="mt-0.5 text-sm leading-relaxed text-muted-foreground">
          {stage.instructionEn}
        </p>

        <ul className="mt-3 space-y-1.5">
          {stage.tips.map((t, i) => (
            <li key={i} className="flex gap-2 text-sm">
              <span aria-hidden className="mt-1.5 size-1.5 shrink-0 rounded-full bg-primary/60" />
              <span>
                {t.mr}
                <span className="ml-1.5 text-xs text-muted-foreground">{t.en}</span>
              </span>
            </li>
          ))}
        </ul>
      </div>

      {/* Capture --------------------------------------------------------- */}
      <div className="mt-5">
        <StageCapture
          stage={stage}
          draftLocalId={draft.localId}
          fix={best}
          orientation={orientation}
          existing={taken[stage.id]}
          voice={voice}
          onPrime={primeSensors}
          onCaptured={onCaptured}
        />
      </div>

      {/* Move on --------------------------------------------------------- */}
      <div className="fixed inset-x-0 bottom-0 border-t bg-card px-4 py-3">
        <div className="mx-auto flex max-w-lg gap-3">
          {index > 0 && (
            <button
              onClick={() => setIndex((i) => i - 1)}
              className="rounded-md border border-border px-4 py-3 text-sm"
            >
              Back
            </button>
          )}

          {index < STAGES.length - 1 ? (
            <button
              onClick={() => {
                primeSensors();
                setIndex((i) => i + 1);
              }}
              disabled={!taken[stage.id] && !stage.optional}
              className="flex-1 rounded-md bg-primary py-3 text-base font-medium text-primary-foreground disabled:opacity-40"
            >
              {taken[stage.id] ? "Next" : stage.optional ? "Skip" : "Take the photograph first"}
            </button>
          ) : (
            <button
              onClick={finish}
              disabled={!readyToFinish || finishing}
              className="flex flex-1 items-center justify-center gap-2 rounded-md bg-primary py-3 text-base font-medium text-primary-foreground disabled:opacity-40"
            >
              {finishing ? <Loader2 className="size-5 animate-spin" /> : <Check className="size-5" />}
              Finish
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

/* --------------------------------------------------------------------- */

function PositionBar({
  state,
  best,
  voice,
}: {
  state: ReturnType<typeof usePosition>["state"];
  best: ReturnType<typeof usePosition>["best"];
  voice: ReturnType<typeof useVoice>;
}) {
  // Say it once, when the fix first becomes usable or is clearly bad.
  const announced = useRef<string>("");
  useEffect(() => {
    if (state.status === "locating" && announced.current !== "locating") {
      announced.current = "locating";
      voice.speak(LINES.standInField.mr, LINES.standInField.en);
    }
    if (best && announced.current !== "ready" && best.accuracyM <= 40) {
      announced.current = "ready";
      voice.speak(LINES.positionGood.mr, LINES.positionGood.en);
    }
    if (best && announced.current === "ready" && best.accuracyM > 80) {
      announced.current = "poor";
      voice.speak(LINES.positionPoor.mr, LINES.positionPoor.en);
    }
  }, [state.status, best, voice]);

  if (state.status === "denied") {
    return (
      <div className="rounded-lg bg-sev-catastrophic-bg px-3 py-2.5 text-sm text-sev-catastrophic">
        Location permission was refused. Without it a photograph cannot be
        matched to your field. Allow it in your browser settings and reload.
      </div>
    );
  }

  if (!best) {
    return (
      <div className="flex items-center gap-2 rounded-lg bg-muted px-3 py-2.5 text-sm">
        <Loader2 className="size-4 shrink-0 animate-spin text-muted-foreground" />
        <span className="text-muted-foreground">
          Finding your position — stand still for a moment
        </span>
      </div>
    );
  }

  const acc = accuracyLabel(best.accuracyM);
  const tone =
    acc.tone === "good"
      ? "bg-sev-negligible-bg text-sev-negligible"
      : acc.tone === "fair"
        ? "bg-muted text-muted-foreground"
        : "bg-sev-moderate-bg text-sev-moderate";

  return (
    <div className={`flex items-center gap-2 rounded-lg px-3 py-2.5 text-sm ${tone}`}>
      <MapPin className="size-4 shrink-0" />
      <span>
        Position {acc.label}
        <span className="ml-1.5 opacity-70">±{Math.round(best.accuracyM)} m</span>
      </span>
      {acc.tone === "poor" && (
        <span className="ml-auto text-xs">Move into the open</span>
      )}
    </div>
  );
}

function StageCapture({
  stage,
  draftLocalId,
  fix,
  orientation,
  existing,
  voice,
  onPrime,
  onCaptured,
}: {
  stage: Stage;
  draftLocalId: string;
  fix: ReturnType<typeof usePosition>["best"];
  orientation: ReturnType<typeof useOrientation>;
  existing?: QueuedCapture;
  voice: ReturnType<typeof useVoice>;
  onPrime: () => void;
  onCaptured: (c: QueuedCapture) => void;
}) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const guidance = useMemo(
    () => tiltGuidance(orientation.tiltDeg, stage.tilt),
    [orientation.tiltDeg, stage.tilt],
  );

  // Nudge, spoken, when the phone is clearly held wrong.
  const lastHint = useRef("");
  useEffect(() => {
    if (guidance.ok || !guidance.hint || guidance.hint === lastHint.current) return;
    lastHint.current = guidance.hint;
    if (guidance.hint.includes("down")) voice.speak(LINES.tiltDown.mr, LINES.tiltDown.en);
    else if (guidance.hint.includes("Lift") || guidance.hint.includes("upright")) {
      voice.speak(LINES.tiltUp.mr, LINES.tiltUp.en);
    }
  }, [guidance, voice]);

  async function handleFile(file: File) {
    if (!fix) {
      setError("Waiting for your position. Stand still for a moment and try again.");
      return;
    }
    setSaving(true);
    setError(null);

    try {
      const capture: QueuedCapture = {
        localId: newLocalId(),
        draftLocalId,
        kind: stage.kind,
        caption: "",
        blob: file,
        contentType: file.type || "image/jpeg",
        capturedAt: new Date().toISOString(),
        lat: fix.lat,
        lon: fix.lon,
        accuracyM: fix.accuracyM,
        headingDeg: orientation.headingDeg ?? undefined,
        tiltDeg: orientation.tiltDeg ?? undefined,
        device: navigator.userAgent.slice(0, 120),
        status: "pending",
        attempts: 0,
        createdAt: Date.now(),
      };

      // On the handset before anything else. If the app is killed on the next
      // line, the photograph still exists.
      await putCapture(capture);
      onCaptured(capture);
    } catch {
      setError("Could not save the photograph on this phone. It may be out of space.");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div>
      <input
        ref={inputRef}
        type="file"
        accept="image/*"
        capture="environment"
        className="sr-only"
        onChange={(e) => {
          const f = e.target.files?.[0];
          if (f) void handleFile(f);
          e.target.value = "";
        }}
      />

      {existing ? (
        <div className="overflow-hidden rounded-xl border border-border">
          <div className="relative">
            {/* The object URL is owned by the element that uses it. React 19
                runs the cleanup a ref callback returns when the element goes
                away, which releases the blob at exactly the right moment --
                a photograph held open is a photograph the browser cannot
                evict, and this app asks it to keep several.

                next/image is not an option: the source is a blob taken on
                this handset, there is no loader to optimise it, and the app
                is a static export that must work with no network at all. */}
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img
              ref={(el) => {
                if (!el) return;
                const url = URL.createObjectURL(existing.blob);
                el.src = url;
                return () => URL.revokeObjectURL(url);
              }}
              alt={stage.titleEn}
              className="w-full"
            />
            <div className="absolute right-2 top-2 flex items-center gap-1 rounded-full bg-sev-negligible px-2.5 py-1 text-xs font-medium text-white">
              <Check className="size-3.5" />
              Saved
            </div>
          </div>
          <button
            onClick={() => {
              onPrime();
              inputRef.current?.click();
            }}
            className="flex w-full items-center justify-center gap-2 border-t border-border py-3 text-sm text-muted-foreground"
          >
            <RotateCcw className="size-4" />
            Take it again
          </button>
        </div>
      ) : (
        <button
          onClick={() => {
            onPrime();
            inputRef.current?.click();
          }}
          disabled={saving || !fix}
          className="flex w-full flex-col items-center justify-center gap-2 rounded-xl border-2 border-dashed border-border py-12 disabled:opacity-50"
        >
          {saving ? (
            <Loader2 className="size-8 animate-spin text-muted-foreground" />
          ) : (
            <Camera className="size-8 text-muted-foreground" />
          )}
          <span className="text-base font-medium">
            {saving ? "Saving…" : "Take the photograph"}
          </span>
          {!fix && (
            <span className="text-xs text-muted-foreground">
              Waiting for your position
            </span>
          )}
        </button>
      )}

      {/* Sensor guidance ------------------------------------------------- */}
      {(orientation.supported || orientation.headingDeg !== null) && (
        <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
          {stage.tilt !== "any" && guidance.hint && (
            <span className={guidance.ok ? "text-sev-negligible" : "text-sev-moderate"}>
              {guidance.hint}
            </span>
          )}
          {orientation.headingDeg !== null && (
            <span>
              Facing {compassPoint(orientation.headingDeg)}
              <span className="ml-1 opacity-60">
                {Math.round(orientation.headingDeg)}°
              </span>
            </span>
          )}
        </div>
      )}

      {error && (
        <p role="alert" className="mt-2 rounded-md bg-sev-catastrophic-bg px-3 py-2 text-sm text-sev-catastrophic">
          {error}
        </p>
      )}
    </div>
  );
}
