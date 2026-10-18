"use client";

import { useCallback, useState } from "react";
import { useRouter } from "next/navigation";
import { api, ApiError } from "@/lib/api";
import type { SurveyEvent } from "@/lib/types";
import { formatDate } from "@/lib/format";
import { Button, ErrorPanel, Spinner } from "./ui";
import { EventPicker } from "@/components/EventPicker";
import { FarmerPicker } from "@/components/FarmerPicker";
import { ParcelPicker, type ParcelChoice } from "@/components/ParcelPicker";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

/**
 * Queues an assessment.
 *
 * Parcels without a boundary are listed but disabled rather than hidden: an
 * officer looking for a parcel that AgriStack cannot supply geometry for
 * should be told why, not left wondering where it went.
 */
export function NewAssessment({ onDone }: { onDone?: () => void }) {
  const router = useRouter();

  // The calamity this assessment is about, chosen before anything else.
  //
  // It is what dates the work: the incident date has to be a day inside a
  // window somebody declared, not a day an officer remembered. Assessments
  // are not stored against an event -- the API takes a date -- so what this
  // buys is a date that cannot be typed wrong.
  const [event, setEvent] = useState<SurveyEvent | null>(null);
  const [farmerId, setFarmerId] = useState("");
  const [farmId, setFarmId] = useState("");
  // The parcel as the picker handed it over: the registry's own row, with the
  // crop declaration where it carries one.
  const [selected, setSelected] = useState<ParcelChoice | null>(null);
  const [incidentDate, setIncidentDate] = useState("");
  const [sowingDate, setSowingDate] = useState("");

  const [submitting, setSubmitting] = useState(false);
  // Where the request has got to: waiting its turn, being produced, or taking
  // longer than this page is willing to watch.
  const [waiting, setWaiting] = useState<string | null>(null);
  const [error, setError] = useState<ApiError | null>(null);

  // Clearing the old farmer's parcels belongs to the act of choosing a new
  // one, not to an effect that notices afterwards. An effect leaves a frame
  // in which one farmer's land is on screen under another farmer's name.
  const chooseFarmer = useCallback((id: string) => {
    setFarmerId(id);
    setFarmId("");
    setSelected(null);
  }, []);

  const canSubmit =
    !!event &&
    !!farmerId &&
    !!farmId &&
    !!incidentDate &&
    !!selected?.parcel.plot_geometry;

  /**
   * Ask for the assessment, then wait for the queue to produce it.
   *
   * The request itself returns at once -- producing an assessment is minutes
   * of satellite and model work, and the engine takes a handful at a time.
   * What is on screen while that happens is a position in a queue, not a
   * frozen button: an officer can leave the page and the work carries on.
   */
  async function submit() {
    setSubmitting(true);
    setError(null);
    setWaiting(null);
    try {
      const job = await api.requestAssessment({
        farmer_id: farmerId,
        farm_id: farmId,
        incident_date: incidentDate,
        // The incident chosen above. Asking for it and then dropping it made
        // the first field on the form decorative.
        ...(event ? { survey_event_id: event.id } : {}),
        ...(sowingDate ? { sowing_date: sowingDate } : {}),
      });
      setWaiting(job.state);
      await poll(job.id);
    } catch (e) {
      setError(e as ApiError);
      setSubmitting(false);
      setWaiting(null);
    }
  }

  /** Polls a queued assessment until it is produced or refused. */
  async function poll(jobId: string) {
    // Roughly twenty minutes at three seconds: longer than the pipeline's own
    // ceiling plus a wait behind other work. Past that the job is still being
    // worked -- it is this page that stops watching, and it says so.
    for (let i = 0; i < 400; i++) {
      await new Promise((r) => setTimeout(r, 3000));
      let job;
      try {
        job = await api.getAssessmentJob(jobId);
      } catch (e) {
        setError(e as ApiError);
        setSubmitting(false);
        setWaiting(null);
        return;
      }
      if (job.state === "done" && job.assessment_id) {
        onDone?.();
        router.push(`/assessment/?id=${job.assessment_id}`);
        return;
      }
      if (job.state === "failed") {
        setError({
          code: "ASSESSMENT_FAILED",
          message: job.error || "the assessment could not be produced",
        } as ApiError);
        setSubmitting(false);
        setWaiting(null);
        return;
      }
      setWaiting(job.state);
    }
    setWaiting("slow");
    setSubmitting(false);
  }

  const today = new Date().toISOString().slice(0, 10);
  const day = (iso?: string) => (iso ? iso.slice(0, 10) : undefined);
  // The window the incident date must sit in, and never past today: a
  // satellite cannot answer for a day that has not happened.
  const windowStart = day(event?.incident_start);
  const windowEnd = day(event?.incident_end);
  const latest = windowEnd && windowEnd < today ? windowEnd : today;

  return (
    <div className="space-y-4 px-4 py-4">
      <div className="grid gap-1.5">
        <Label htmlFor="incident-event">Incident</Label>
        <EventPicker
          id="incident-event"
          value={event?.id ?? ""}
          onChange={(e) => {
            setEvent(e);
            // The window moved, so a date chosen under the old one is no
            // longer a date inside this calamity.
            setIncidentDate("");
          }}
          emptyNote="No survey event is open in your area. An assessment is dated by a declared calamity, so one has to be declared first."
        />
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <div className="grid gap-1.5">
          <Label htmlFor="farmer">Farmer</Label>
          <FarmerPicker id="farmer" value={farmerId} onChange={chooseFarmer} />
        </div>

        <div className="grid gap-1.5">
          <Label htmlFor="parcel">Parcel</Label>
          <ParcelPicker
            id="parcel"
            farmerId={farmerId}
            value={farmId}
            onChange={(c) => {
              setFarmId(c?.parcel.farm_id ?? "");
              setSelected(c ?? null);
            }}
          />
          {selected && !selected.crop && (
            <p className="text-xs text-muted-foreground">
              No crop declaration on this parcel — a sowing date is required.
            </p>
          )}
        </div>

        <div className="grid gap-1.5">
          <Label htmlFor="incident">Incident date</Label>
          <Input
            id="incident"
            type="date"
            value={incidentDate}
            min={windowStart}
            max={latest}
            disabled={!event}
            onChange={(e) => setIncidentDate(e.target.value)}
          />
          <p className="text-xs text-faint">
            {event
              ? `Inside ${formatDate(event.incident_start)} → ${formatDate(event.incident_end)}, and in the past.`
              : "Choose the incident above first."}
          </p>
        </div>

        <div className="grid gap-1.5">
          <Label htmlFor="sowing">
            Sowing date{" "}
            <span className="font-normal text-faint">(optional)</span>
          </Label>
          <Input
            id="sowing"
            type="date"
            value={sowingDate}
            max={today}
            onChange={(e) => setSowingDate(e.target.value)}
          />
          <p className="text-xs text-faint">
            {selected?.crop
              ? `Defaults to ${selected.crop.sowing_date} from the ${selected.crop.crop_name} declaration.`
              : "No crop declaration on this parcel — a sowing date is required."}
          </p>
        </div>
      </div>

      {selected && !selected.parcel.plot_geometry && (
        <ErrorPanel
          code="NO_GEOMETRY"
          message="This parcel has no boundary, so it cannot be assessed."
        />
      )}

      {error && (
        <ErrorPanel
          code={error.code}
          message={error.message}
          onRetry={() => setError(null)}
        />
      )}

      <div className="flex items-center gap-3">
        <Button onClick={submit} disabled={!canSubmit || submitting}>
          {submitting && <Spinner />}
          {submitting ? (waiting === "queued" ? "Queued…" : "Analysing…") : "Run assessment"}
        </Button>
        <span className="text-xs text-faint">
          {waiting === "queued"
            ? "Queued. Satellite analysis runs a few at a time; this page will open the assessment when it is ready."
            : waiting === "running"
              ? "Being analysed: Sentinel-2 NDVI, Copernicus DEM and the assessment panel."
              : waiting === "slow"
                ? "Still being worked. It will appear under Assessments when it is done; you can leave this page."
                : "Queries Sentinel-2 NDVI and Copernicus DEM for the parcel boundary."}
        </span>
      </div>
    </div>
  );
}
