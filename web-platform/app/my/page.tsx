"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  BatteryLow,
  Camera,
  CheckCircle2,
  ChevronRight,
  Clock,
  Loader2,
  MessageSquareWarning,
  Siren,
} from "lucide-react";

import { api } from "@/lib/api";
import type {
  FarmerRecord,
  Grievance,
  ReportSummary,
  SurveyEvent,
} from "@/lib/types";
import { PrintReport } from "@/components/PrintReport";
import { FileGrievance } from "@/components/FileGrievance";
import { calamityLabels, formatDate, ha, istDateTime, reportStateStyles } from "@/lib/format";
import { useBattery, useOnline } from "@/lib/sensors";
import { LINES, useVoice } from "@/lib/voice";
import {
  allDrafts,
  capturesFor,
  newLocalId,
  getBoundary,
  putDraft,
  requestPersistence,
  storageEstimate,
  type DraftReport,
  type NewDraft,
  type QueuedCapture,
} from "@/lib/offline/db";
import { SurveyWizard } from "@/components/survey/SurveyWizard";
import { SyncStatus } from "@/components/survey/SyncStatus";
import { TraceBoundary } from "@/components/survey/TraceBoundary";
import { useSession } from "@/components/SessionProvider";
import { NativeSelect } from "@/components/ui/native-select";
import { Input } from "@/components/ui/input";

/**
 * The farmer's app.
 *
 * The whole journey on one screen, in the order it happens: is anything
 * notified, report it, photograph it, see where it got to.
 *
 * Both journeys are the same journey. With a network the report reaches the
 * office as it is made; without one it is written to the handset and sent when
 * the signal returns. The farmer is told which happened and nothing else
 * changes — there is no separate offline mode to discover or turn on.
 */
export default function FarmerHome() {
  const { user } = useSession();
  const online = useOnline();
  const voice = useVoice();
  const battery = useBattery();

  const [record, setRecord] = useState<FarmerRecord | null>(null);
  const [events, setEvents] = useState<SurveyEvent[]>([]);
  const [reports, setReports] = useState<ReportSummary[]>([]);
  const [drafts, setDrafts] = useState<DraftReport[]>([]);
  const [storage, setStorage] = useState<{ usageMB: number; quotaMB: number } | null>(null);

  const [filing, setFiling] = useState(false);
  const [surveying, setSurveying] = useState<DraftReport | null>(null);
  // A latch, not state: nothing renders from it, and making it state means
  // the welcome line re-runs the render it just caused.
  const greeted = useRef(false);

  const loadLocal = useCallback(
    () =>
      Promise.all([allDrafts(), storageEstimate()]).then(([d, s]) => {
        setDrafts(d);
        setStorage(s);
      }),
    [],
  );

  const farmerId = user?.farmer_id;

  const loadRemote = useCallback(() => {
    if (!farmerId) return;
    // Every one of these is allowed to fail. Offline, the app still works from
    // what is on the handset, and a spinner that never resolves is worse than
    // a screen that quietly shows less.
    api.getFarmer(farmerId).then(setRecord).catch(() => {});
    // A farmer's own screen: the events they may file against, and their own
    // reports. Both are small by construction -- one household -- but bounded
    // anyway, because "by construction" is an assumption and a limit is not.
    api.listEvents({ limit: 50 }, "open")
      .then((r) => setEvents(r.items ?? []))
      .catch(() => {});
    api.listReports({ limit: 100 })
      .then((r) => setReports(r.items))
      .catch(() => {});
  }, [farmerId]);

  useEffect(() => {
    void loadLocal();
    loadRemote();
    // Ask the browser not to evict the queue under storage pressure. The one
    // thing this app must never do is lose a photograph someone believes they
    // have already taken.
    void requestPersistence();
  }, [loadLocal, loadRemote]);

  // Register the service worker so the app opens with no signal at all.
  useEffect(() => {
    if (!("serviceWorker" in navigator)) return;
    navigator.serviceWorker.register("/sw.js").catch(() => {});
  }, []);

  useEffect(() => {
    if (greeted.current || !user) return;
    greeted.current = true;
    voice.speak(LINES.welcome.mr, LINES.welcome.en);
  }, [user, voice]);

  if (!user) return null;

  if (surveying) {
    return (
      <SurveyWizard
        draft={surveying}
        onExit={() => setSurveying(null)}
        onFinished={async () => {
          setSurveying(null);
          await loadLocal();
          loadRemote();
          voice.speak(LINES.sentDone.mr, LINES.sentDone.en);
        }}
      />
    );
  }

  const firstName = user.name.split(" ")[0];
  const lowBattery = battery.level !== null && battery.level < 0.15 && !battery.charging;
  const lowStorage = storage !== null && storage.quotaMB - storage.usageMB < 60;

  return (
    <div className="mx-auto max-w-lg space-y-5 pb-16">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight">नमस्कार, {firstName}</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          शेतातील नुकसान कळवा
          <span className="ml-1.5">· Report crop damage</span>
        </p>
      </header>

      <SyncStatus onSynced={() => { void loadLocal(); loadRemote(); }} />

      {lowBattery && (
        <div className="flex items-center gap-2 rounded-lg bg-sev-moderate-bg px-3 py-2.5 text-sm text-sev-moderate">
          <BatteryLow className="size-4 shrink-0" />
          Battery is low. A survey takes four photographs and a position fix —
          charge before walking the field if you can.
        </div>
      )}

      {lowStorage && (
        <div className="flex items-center gap-2 rounded-lg bg-sev-moderate-bg px-3 py-2.5 text-sm text-sev-moderate">
          <Camera className="size-4 shrink-0" />
          Your phone is nearly out of space. Photographs may fail to save.
        </div>
      )}

      {/* Is anything notified ------------------------------------------ */}
      {events.length > 0 ? (
        <section className="rounded-xl border border-sev-moderate/40 bg-sev-moderate-bg p-4">
          <div className="flex items-start gap-3">
            <Siren className="mt-0.5 size-5 shrink-0 text-sev-moderate" />
            <div className="min-w-0">
              <p className="text-sm font-medium">
                {events.length === 1
                  ? "आपल्या भागात आपत्ती जाहीर झाली आहे"
                  : `${events.length} आपत्ती जाहीर झाल्या आहेत`}
              </p>
              <p className="text-xs text-muted-foreground">
                A calamity has been notified for your area
              </p>
              {events.map((e) => (
                <div key={e.id} className="mt-2">
                  <div className="text-sm">
                    {calamityLabels[e.calamity_type]?.mr}
                    <span className="ml-1.5 text-muted-foreground">
                      {calamityLabels[e.calamity_type]?.en}
                    </span>
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {formatDate(e.incident_start)} – {formatDate(e.incident_end)}
                    {" · "}report by {formatDate(e.survey_closes_at)}
                  </div>
                </div>
              ))}
            </div>
          </div>

          {!filing && record && (
            <button
              onClick={() => setFiling(true)}
              className="mt-4 w-full rounded-md bg-primary py-3.5 text-base font-medium text-primary-foreground">
              नुकसान कळवा
              <span className="ml-2 text-sm opacity-80">Report damage</span>
            </button>
          )}
        </section>
      ) : (
        <section className="rounded-xl border border-border p-4">
          <p className="text-sm font-medium">सध्या काही जाहीर झालेले नाही</p>
          <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
            {online
              ? "You can report crop damage once the district has notified a calamity for your area."
              : "No network, so this may be out of date. Anything you have already recorded is safe on your phone."}
          </p>
        </section>
      )}

      {filing && events.length > 0 && record && (
        <FileReport
          record={record}
          events={events}
          onCancel={() => setFiling(false)}
          onStarted={async (draft) => {
            setFiling(false);
            await loadLocal();
            setSurveying(draft);
          }}
        />
      )}

      {/* Surveys still on this phone ------------------------------------ */}
      {drafts.filter((d) => !d.serverId || d.status !== "sent").length > 0 && (
        <section>
          <h2 className="mb-2 text-sm font-medium text-muted-foreground">
            On this phone
          </h2>
          <div className="space-y-2">
            {drafts
              .filter((d) => !d.serverId || d.status !== "sent")
              .map((d) => (
                <LocalDraft key={d.localId} draft={d} onResume={() => setSurveying(d)} />
              ))}
          </div>
        </section>
      )}

      {/* Reports the office has ----------------------------------------- */}
      {reports.length > 0 && (
        <section>
          <h2 className="mb-2 text-sm font-medium text-muted-foreground">
            माझे अहवाल <span className="ml-1">· My reports</span>
          </h2>
          <div className="space-y-2.5">
            {reports.map((r) => (
              <ReportCard key={r.id} summary={r} />
            ))}
          </div>
        </section>
      )}

      {/* My land --------------------------------------------------------- */}
      {record && (
        <section>
          <h2 className="mb-2 text-sm font-medium text-muted-foreground">
            माझी जमीन <span className="ml-1">· My land</span>
          </h2>
          <div className="space-y-2">
            {record.land_data.map((p) => (
              <ParcelCard key={p.farm_id} parcel={p} />
            ))}
          </div>
        </section>
      )}
    </div>
  );
}

/**
 * One parcel, and whether it has a boundary yet.
 *
 * The land registry returns none for anybody this platform can reach, so for
 * most farmers this card is the prompt to walk their field — and the reason
 * the prompt is here rather than buried in the survey flow is that a boundary
 * belongs to a plot, not to a report. Walking it once serves every claim they
 * ever file on that land.
 */
function ParcelCard({ parcel }: { parcel: FarmerRecord["land_data"][number] }) {
  const [tracing, setTracing] = useState(false);
  const [known, setKnown] = useState<"unknown" | "present" | "absent">("unknown");

  useEffect(() => {
    let live = true;

    // The phone first. A farmer who walked this boundary an hour ago with no
    // signal must not be asked to walk it again, and asking the server first
    // means exactly that whenever they are still offline.
    void getBoundary(parcel.farm_id)
      .then((local) => {
        if (live && local) setKnown("present");
        return api.parcelBoundary(parcel.farm_id);
      })
      .then((r) => {
        if (live && r?.boundary) setKnown("present");
        else if (live) setKnown((k) => (k === "present" ? k : "absent"));
      })
      .catch(() => {
        // Offline. Whatever the phone knew stands; if it knew nothing, the
        // prompt appears, which is the right default for a farmer who has
        // never walked this field.
        if (live) setKnown((k) => (k === "present" ? k : "absent"));
      });

    return () => {
      live = false;
    };
  }, [parcel.farm_id]);

  if (tracing) {
    return (
      <TraceBoundary
        farmId={parcel.farm_id}
        surveyNumber={parcel.survey_number}
        registryExtentHa={Number(parcel.owner_extent) || undefined}
        onDone={() => {
          setKnown("present");
          setTracing(false);
        }}
        onCancel={() => setTracing(false)}
      />
    );
  }

  return (
    <div className="rounded-lg border border-border p-3">
      <div className="flex items-baseline justify-between gap-3">
        <span className="text-sm font-medium">
          सर्व्हे नं. {parcel.survey_number}
        </span>
        <span className="tnum text-sm text-muted-foreground">
          {parcel.owner_extent} ha
        </span>
      </div>
      <div className="mono mt-0.5 text-xs text-faint">{parcel.farm_id}</div>

      {known === "absent" && (
        <button
          type="button"
          onClick={() => setTracing(true)}
          className="mt-3 w-full rounded-lg border border-primary/40 bg-primary/5 px-3 py-2.5 text-left">
          <span className="block text-sm font-medium">
            शेताची हद्द नोंदवा
          </span>
          <span className="mt-0.5 block text-xs text-muted-foreground">
            Record this field&rsquo;s boundary — five minutes of walking, and
            it is needed once. Without it your photographs cannot be checked
            against your own plot and this field cannot be seen from satellite.
          </span>
        </button>
      )}

      {known === "present" && (
        <p className="mt-2 text-xs text-sev-negligible">
          हद्द नोंदवली आहे · Boundary recorded
        </p>
      )}
    </div>
  );
}

/* --------------------------------------------------------------------- */

function LocalDraft({
  draft,
  onResume,
}: {
  draft: DraftReport;
  onResume: () => void;
}) {
  const [captures, setCaptures] = useState<QueuedCapture[]>([]);

  useEffect(() => {
    void capturesFor(draft.localId).then(setCaptures);
  }, [draft.localId]);

  const sent = captures.filter((c) => c.status === "sent").length;

  return (
    <button
      onClick={onResume}
      className="flex w-full items-center gap-3 rounded-lg border border-border p-3 text-left">
      <Clock className="size-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium">
          {draft.reference ?? "Not sent yet"}
        </div>
        <div className="text-xs text-muted-foreground">
          {captures.length} photograph{captures.length === 1 ? "" : "s"}
          {sent > 0 && ` · ${sent} sent`}
          {draft.lastError && ` · ${draft.lastError}`}
        </div>
      </div>
      <ChevronRight className="size-4 shrink-0 text-muted-foreground" />
    </button>
  );
}

/**
 * States in which there is a decision to appeal.
 *
 * Kept in step with the server, which refuses a grievance against a report
 * still being verified -- offering the button earlier would be offering a
 * form that comes back with an error the farmer cannot act on.
 */
const APPEALABLE = new Set(["verified", "adjudicated", "closed"]);

function ReportCard({ summary }: { summary: ReportSummary }) {
  const [open, setOpen] = useState(false);
  const [appealing, setAppealing] = useState(false);
  const [filed, setFiled] = useState<Grievance | null>(null);
  const state = reportStateStyles[summary.state];
  const canAppeal = APPEALABLE.has(summary.state);

  return (
    <div className="overflow-hidden rounded-xl border border-border">
      <button
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-3 p-3.5 text-left">
        <div className="min-w-0 flex-1">
          <div className="mono text-sm font-medium">{summary.reference}</div>
          <div className="mt-0.5 text-xs text-muted-foreground">
            {calamityLabels[summary.calamity_type]?.mr} ·{" "}
            {ha(summary.area_affected_ha)} ha
          </div>
        </div>
        <span className={`shrink-0 rounded px-2 py-0.5 text-xs font-medium ${state?.bg} ${state?.text}`}>
          {state?.label}
        </span>
        <ChevronRight
          className={"size-4 shrink-0 text-muted-foreground transition-transform " + (open ? "rotate-90" : "")}
        />
      </button>

      {open && (
        <div className="space-y-3 border-t border-border p-3.5">
          <p className="rounded-md bg-muted px-3 py-2.5 text-sm leading-relaxed">
            {progressFor(summary.state)}
          </p>
          <div className="text-xs text-muted-foreground">
            Reported {istDateTime(summary.reported_at)}
          </div>
          <PrintReport
            reportId={summary.id}
            reference={summary.reference}
            label="पोहोच पावती · My receipt"
            className="w-full justify-center rounded-md border border-border py-2.5"
          />

          {/* The appeal. Offered only once there is a decision to appeal,
              and replaced by its own reference after filing so the farmer
              has something to quote on the phone. */}
          {filed ? (
            <div className="rounded-md bg-sev-negligible-bg px-3 py-2.5">
              <p className="text-sm font-medium text-sev-negligible">
                हरकत नोंदवली · {filed.reference}
              </p>
              <p className="mt-0.5 text-xs text-muted-foreground">
                १५ दिवसांत अधिकारी उत्तर देतील.
              </p>
            </div>
          ) : appealing ? (
            <FileGrievance
              reportId={summary.id}
              reportReference={summary.reference}
              onFiled={(g) => {
                setFiled(g);
                setAppealing(false);
              }}
              onCancel={() => setAppealing(false)}
            />
          ) : canAppeal ? (
            <button
              type="button"
              onClick={() => setAppealing(true)}
              className="flex w-full items-center justify-center gap-2 rounded-md border border-border py-2.5 text-sm">
              <MessageSquareWarning className="size-4" />
              हरकत नोंदवा{" "}
              <span className="text-muted-foreground">· Disagree with this</span>
            </button>
          ) : null}
        </div>
      )}
    </div>
  );
}

/** Where a report has got to, said the way a person would say it. */
function progressFor(state: string): string {
  switch (state) {
    // Nobody visits. The photographs and the boundary you sent are what gets
    // examined, and copy that promises a visit leaves a farmer waiting at
    // home for one that was never going to happen.
    case "submitted":
      return "Sent. Two village officers will be asked to examine what you sent.";
    case "acknowledged":
      return "Received by the office. Your photographs are being routed for review.";
    case "assigned":
      return "The Talathi and the Krushi Sahayak have been asked to review your photographs.";
    case "under_verification":
      return "One of the two officers has recorded their finding. Waiting for the other.";
    case "verified":
      return "Both officers have recorded a finding. A decision is being made.";
    case "adjudicated":
      return "A decision has been recorded on your report.";
    case "closed":
      return "This report is closed.";
    case "withdrawn":
      return "You withdrew this report.";
    default:
      return "Not sent yet.";
  }
}

function FileReport({
  record,
  events,
  onCancel,
  onStarted,
}: {
  record: FarmerRecord;
  events: SurveyEvent[];
  onCancel: () => void;
  onStarted: (d: DraftReport) => void;
}) {
  const online = useOnline();
  const [eventId, setEventId] = useState(events[0].id);
  const [farmId, setFarmId] = useState(record.land_data[0]?.farm_id ?? "");
  const [occurred, setOccurred] = useState("");
  const [crop, setCrop] = useState(record.crop_sown_data[0]?.crop_name ?? "");
  const [sown, setSown] = useState(record.land_data[0]?.owner_extent ?? "");
  const [affected, setAffected] = useState("");
  const [damage, setDamage] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const event = events.find((e) => e.id === eventId)!;
  const parcel = record.land_data.find((p) => p.farm_id === farmId);

  async function start(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);

    const draft: NewDraft = {
      localId: newLocalId(),
      surveyEventId: eventId,
      farmerId: record.FarmerData.farmer_id,
      season: "kharif",
      cropYear: event.incident_start.slice(0, 4),
      lossCategory: "standing_crop",
      calamityType: event.calamity_type,
      occurredAt: new Date(occurred).toISOString(),
      farmId,
      surveyNumber: parcel?.survey_number ?? "",
      cropName: crop,
      areaSownHa: Number(sown),
      areaAffectedHa: Number(affected),
      damagePercent: Number(damage),
      declarationAccepted: true,
      signatureKind: "thumb",
      status: "pending",
      attempts: 0,
      // createdAt and updatedAt are stamped by putDraft, at the moment of the
      // write. Reading the clock here would be reading it during render.
    };

    try {
      // On the handset first, always. Whether there is a network decides only
      // when it leaves, never whether it was kept.
      onStarted(await putDraft(draft));
    } catch {
      setError("Could not save on this phone. It may be out of space.");
      setBusy(false);
    }
  }

  return (
    <form onSubmit={start} className="space-y-4 rounded-xl border border-border p-4">
      <div className="flex items-center justify-between">
        <h2 className="text-base font-semibold">नुकसानाची माहिती</h2>
        <button type="button" onClick={onCancel} className="text-sm text-muted-foreground">
          रद्द करा
        </button>
      </div>

      {events.length > 1 && (
        <Field label="कोणती आपत्ती" en="Which calamity">
          <NativeSelect selectSize="field" value={eventId} onChange={(e) => setEventId(e.target.value)}
            className="w-full">
            {events.map((e) => (
              <option key={e.id} value={e.id}>
                {calamityLabels[e.calamity_type]?.mr} — {formatDate(e.incident_start)}
              </option>
            ))}
          </NativeSelect>
        </Field>
      )}

      {record.land_data.length > 1 && (
        <Field label="कोणते शेत" en="Which field">
          <NativeSelect selectSize="field" value={farmId} onChange={(e) => setFarmId(e.target.value)}
            className="w-full">
            {record.land_data.map((p) => (
              <option key={p.farm_id} value={p.farm_id}>
                सर्व्हे नं. {p.survey_number} — {p.owner_extent} ha
              </option>
            ))}
          </NativeSelect>
        </Field>
      )}

      <Field
        label="कधी झाले"
        en="When did it happen"
        hint={`${formatDate(event.incident_start)} – ${formatDate(event.incident_end)}`}>
        <input
          type="datetime-local"
          value={occurred}
          onChange={(e) => setOccurred(e.target.value)}
          required
          className="h-12 w-full rounded-md border border-input bg-transparent px-3 text-base"
        />
      </Field>

      <Field label="कोणते पीक" en="Which crop">
        <input
          value={crop}
          onChange={(e) => setCrop(e.target.value)}
          required
          className="h-12 w-full rounded-md border border-input bg-transparent px-3 text-base"
        />
      </Field>

      <div className="grid grid-cols-3 gap-2.5">
        <Field label="पेरणी" en="Sown (ha)">
          <Input inputSize="field" type="number" step="0.01" min="0" value={sown} required
            onChange={(e) => setSown(e.target.value)} />
        </Field>
        <Field label="नुकसान" en="Damaged (ha)">
          <Input inputSize="field" type="number" step="0.01" min="0" value={affected} required
            onChange={(e) => setAffected(e.target.value)} />
        </Field>
        <Field label="किती %" en="How much">
          <Input inputSize="field" type="number" step="1" min="0" max="100" value={damage} required
            onChange={(e) => setDamage(e.target.value)} />
        </Field>
      </div>

      <p className="rounded-md bg-muted px-3 py-3 text-sm leading-relaxed">
        पाहणी होईपर्यंत मी नुकसान झालेले पीक कापणार नाही किंवा बदलणार नाही, आणि
        मागितल्यास ७/१२ उतारा देईन.
        <span className="mt-1.5 block text-xs text-muted-foreground">
          I will not cut or change the damaged crop until an officer has
          inspected it, and I will produce my 7/12 extract when asked.
        </span>
      </p>

      {!online && (
        <p className="rounded-md bg-muted px-3 py-2.5 text-xs leading-relaxed text-muted-foreground">
          नेटवर्क नाही. माहिती फोनमध्ये सुरक्षित राहील आणि नेटवर्क आल्यावर
          आपोआप पाठवली जाईल.
        </p>
      )}

      {error && (
        <p role="alert" className="rounded-md bg-sev-catastrophic-bg px-3 py-2 text-sm text-sev-catastrophic">
          {error}
        </p>
      )}

      <button
        type="submit"
        disabled={busy}
        className="flex w-full items-center justify-center gap-2 rounded-md bg-primary py-3.5 text-base font-medium text-primary-foreground disabled:opacity-50">
        {busy ? <Loader2 className="size-5 animate-spin" /> : <CheckCircle2 className="size-5" />}
        पुढे — फोटो घ्या
        <span className="text-sm opacity-80">Next — photographs</span>
      </button>
    </form>
  );
}

function Field({
  label,
  en,
  hint,
  children,
}: {
  label: string;
  en: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <label className="block">
      <span className="mb-1.5 block text-sm font-medium">
        {label}
        <span className="ml-1.5 text-xs font-normal text-muted-foreground">{en}</span>
      </span>
      {children}
      {hint && <span className="mt-1 block text-xs text-muted-foreground">{hint}</span>}
    </label>
  );
}
