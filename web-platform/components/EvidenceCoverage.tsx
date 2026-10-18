import { Camera, MapPinOff } from "lucide-react";

/**
 * How much evidence a claim rests on, in the officer's own words.
 *
 * # Why this is on the screen at all
 *
 * The app directs a farmer to four points around their parcel and asks for a
 * photograph and a clip at each. The counters for that walk were kept on the
 * report row from the first version and read by nothing: a claim shot from
 * one corner of the field and a claim walked to all four points looked
 * identical in the approval queue, in the dialog, and in the batch an officer
 * ticked through (docs/EVIDENCE_REVALIDATION.md, V8).
 *
 * # Why thin coverage is a note and not a refusal
 *
 * A farmer whose phone died on the third point, or whose fourth point sits in
 * standing water, has a real claim with three points behind it. The rule has
 * never been that four points are required to be paid; it is that an officer
 * deciding on three should know they are deciding on three. So this says what
 * is there and gets out of the way.
 */

/** The directed walk asks for four points. See survey.Stations. */
export const STATIONS_EXPECTED = 4;

export function coverageIsThin(stationsCaptured: number): boolean {
  return stationsCaptured < STATIONS_EXPECTED;
}

/** One line: points filmed, and how many pieces of evidence in all. */
export function EvidenceCoverage({
  captureCount,
  stationsCaptured,
  className = "",
}: {
  captureCount: number;
  stationsCaptured: number;
  className?: string;
}) {
  const thin = coverageIsThin(stationsCaptured);
  return (
    <span
      className={`inline-flex items-center gap-1.5 ${
        thin ? "text-sev-severe" : "text-muted-foreground"
      } ${className}`}
    >
      {thin ? (
        <MapPinOff className="size-3.5 shrink-0" />
      ) : (
        <Camera className="size-3.5 shrink-0" />
      )}
      <span>
        {stationsCaptured} of {STATIONS_EXPECTED} points filmed
        {captureCount > 0 && ` · ${captureCount} in all`}
      </span>
    </span>
  );
}

/**
 * The same fact as a queue badge, shown only when it should stop a tick.
 *
 * Nothing is drawn for a complete walk: a queue that flags every row flags
 * nothing.
 */
export function ThinEvidenceBadge({
  captureCount,
  stationsCaptured,
}: {
  captureCount: number;
  stationsCaptured: number;
}) {
  if (!coverageIsThin(stationsCaptured)) return null;
  return (
    <span className="inline-flex items-center gap-1 rounded bg-sev-severe-bg px-1.5 py-0.5 text-xs font-medium text-sev-severe">
      <MapPinOff className="size-3" />
      {stationsCaptured === 0 && captureCount === 0
        ? "No evidence on the claim"
        : `Only ${stationsCaptured} of ${STATIONS_EXPECTED} points filmed`}
    </span>
  );
}
