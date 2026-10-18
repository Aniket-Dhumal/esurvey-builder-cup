"use client";

import Link from "next/link";
import { Suspense, useState } from "react";
import { useSearchParams } from "next/navigation";
import {
  EyeOff,
  Loader2,
  ShieldCheck,
} from "lucide-react";

import { AssessmentReport } from "@/components/AssessmentReport";
import { api, ApiError } from "@/lib/api";
import { useResource } from "@/lib/useResource";
import type { Consensus, Finding, Outcome } from "@/lib/types";
import {
  calamityLabel,
  ha,
  istDateTime,
  lossCategoryLabels,
  outcomeLabels,
  reportStateStyles,
  roleLabel,
  routeStyles,
  seasonLabels,
} from "@/lib/format";
import {
  BackLink,
  Button,
  Card,
  CardHeader,
  ErrorPanel,
  Field,
  Figure,
  Note,
  Skeleton,
  StateBadge,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { PrintReport } from "@/components/PrintReport";
import { useSession } from "@/components/SessionProvider";
import { EvidencePanel } from "@/components/survey/EvidencePanel";
import { ClaimProgress } from "@/components/ClaimProgress";

function VerifyView() {
  const id = useSearchParams().get("id") ?? "";
  const { can, user } = useSession();


  const { data: brief, error, reload } = useResource(
    () => api.verificationBrief(id),
    [id],
  );

  if (error) {
    return (
      <div className="space-y-5">
        <BackLink href="/worklist">Worklist</BackLink>
        <ErrorPanel code={error.code} message={error.message} onRetry={reload} />
      </div>
    );
  }
  if (!brief) {
    return (
      <div className="space-y-5">
        <BackLink href="/worklist">Worklist</BackLink>
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  const r = brief.report;
  const canVerify = brief.assigned && !brief.already_recorded && can("report:verify");
  const consensus = brief.consensus;

  return (
    <div className="space-y-6">
      <BackLink href="/worklist">Worklist</BackLink>

      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="mono text-xl font-semibold tracking-tight">
            {r.reference}
          </h1>
          <div className="mt-1 flex flex-wrap gap-x-4 text-xs text-faint">
            <span>{r.farmer_name}</span>
            <span>
              {r.place.village_name} · {r.place.sub_district_name} ·{" "}
              {r.place.district_name}
            </span>
            {r.survey_event_reference && (
              <Link
                href={`/events/?ref=${r.survey_event_reference}`}
                className="mono hover:text-primary"
              >
                {r.survey_event_reference}
              </Link>
            )}
          </div>
        </div>
        <div className="flex items-center gap-2">
          <StateBadge styles={reportStateStyles} state={r.state} />
          <PrintReport
            reportId={r.id}
            reference={r.reference}
            label="Print"
            className="h-8 border border-border px-3"
          />
        </div>
      </div>

      {/* Where it has got to ------------------------------------------- */}
      <ClaimProgress reportId={r.id} />

      {/* What the farmer reported ------------------------------------- */}
      <Card>
        <CardHeader
          title="Reported loss"
          subtitle="As filed. Your finding replaces this, it does not have to agree with it."
        />
        <div className="px-4 py-4">
          <dl className="mb-4 grid grid-cols-2 gap-5 sm:grid-cols-4">
            <Field
              label="Cause"
              value={`${calamityLabel(r.calamity_type)} · ${
                lossCategoryLabels[r.loss_category]?.en ?? r.loss_category
              }`}
            />
            <Field label="Occurred" value={istDateTime(r.occurred_at)} />
            <Field
              label="Season"
              value={`${seasonLabels[r.season] ?? r.season} ${r.crop_year}`}
            />
            <Field
              label="Revenue circle"
              value={r.place.circle_name || "not mapped"}
            />
          </dl>

          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                {[
                  "Survey no.",
                  "Crop",
                  "Sown (ha)",
                  // Whose numbers these are. They are the farmer's
                  // declaration, and an officer reading "Damage" next to an
                  // assessment's own figure has no way to tell them apart --
                  // which is how a claimed 85% was read as a finding.
                  "Affected (ha), declared",
                  "Damage, declared",
                ].map((h) => (
                  <TableHead key={h} className="text-xs">{h}</TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {r.items.map((i) => (
                <TableRow key={i.line_no}>
                  <TableCell className="mono">{i.survey_number}</TableCell>
                  <TableCell>{i.crop_name}</TableCell>
                  <TableCell className="tnum">{ha(i.area_sown_ha)}</TableCell>
                  <TableCell className="tnum">{ha(i.area_affected_ha)}</TableCell>
                  <TableCell className="tnum">{i.damage_percent}%</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>

          {r.filed_late && (
            <p className="mt-3 rounded-md bg-sev-moderate-bg px-3 py-2 text-xs text-sev-moderate">
              Filed more than 48 hours after the incident. Flagged, not refused
              — a village with no signal for three days is not a fraudulent
              claim.
              {r.late_reason && <> Reason given: {r.late_reason}</>}
            </p>
          )}

          {/* Admitted after the district stopped taking claims, because the
              farmer had finished the form before it did. Said plainly: the
              total this district signed off did not include this claim, and
              it is the one case where a handset's own clock decided whether a
              claim exists rather than merely how it is flagged. */}
          {r.arrived_after_close && (
            <p className="mt-3 rounded-md bg-sev-moderate-bg px-3 py-2 text-xs text-sev-moderate">
              Completed on the farmer&rsquo;s phone before this event stopped
              taking claims, and delivered after it. Taken on that basis rather
              than refused — the delay was the queue&rsquo;s, waiting for a
              signal or for the rest of the evidence. It was not in the total
              the district closed on.
            </p>
          )}

          {/* The delay the queue introduced, which is not the farmer's.
              The window above is measured on when the form was completed;
              this says how long it then took to arrive, so a long gap is
              something an officer can see and weigh rather than infer from a
              flag that no longer reflects it. */}
          {r.filed_at && (
            <p className="mt-3 text-xs text-muted-foreground">
              Completed on the farmer&rsquo;s phone {istDateTime(r.filed_at)},
              reached the office {istDateTime(r.reported_at)}
              {(() => {
                const days = Math.floor(
                  (Date.parse(r.reported_at) - Date.parse(r.filed_at)) / 86400000,
                );
                return days >= 1
                  ? ` — ${days} day${days > 1 ? "s" : ""} in the queue, waiting for a signal or for the rest of the evidence.`
                  : ".";
              })()}
            </p>
          )}
        </div>
      </Card>

      {/* The farmer's own evidence ------------------------------------- */}
      <EvidencePanel reportId={r.id} />

      {/* The blind ---------------------------------------------------- */}
      {brief.peer_finding_withheld && (
        <Card className="px-4 py-4">
          <div className="flex gap-3">
            <EyeOff className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
            <div>
              <p className="text-sm font-medium">
                Your colleague has recorded their finding.
              </p>
              <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
                It is withheld until you record yours. You verify different
                things — one the land record and ownership, the other the crop
                and its condition — and a second opinion anchored to the first
                is not a second opinion.
              </p>
            </div>
          </div>
        </Card>
      )}

      {/* What the panel made of it, in full -------------------------- */}
      {/* Above the decision and not behind a print button. An officer asked to
          confirm a percentage has to be able to read what produced it; the
          quadrat form that used to sit here asked them to count plants by hand
          instead, which is the data entry this system exists to remove. */}
      {canVerify && brief.assessment?.id && (
        <AssessmentReport
          assessmentId={brief.assessment.id}
          surveyNumber={r.items[0]?.survey_number}
        />
      )}

      {/* Record a finding --------------------------------------------- */}

      {canVerify && (
        <RecordFinding
          reportId={r.id}
          lines={r.items.length}
          modelDamage={brief.assessment?.damage_percent ?? null}
          modelCropPresence={brief.assessment?.crop_presence ?? null}
          onDone={reload}
        />
      )}

      {brief.assigned && brief.already_recorded && !brief.may_amend && (
        <Note>
          You recorded your finding on this report. A verification is final once
          submitted — that is what makes the blind a guarantee rather than a
          convention.
        </Note>
      )}

      {/* Converging ---------------------------------------------------- */}
      {/* Both have filed, so the blind has served its purpose and each can
          read the other. Two officers a few points apart can close that gap
          themselves; sending it up as a disagreement spends a Circle Officer's
          week on something the two people who saw the field can settle. */}
      {brief.may_amend && (
        <RecordFinding
          reportId={r.id}
          lines={r.items.length}
          modelDamage={brief.assessment?.damage_percent ?? null}
          modelCropPresence={brief.assessment?.crop_presence ?? null}
          mode="amend"
          mine={brief.findings?.find((f) => f.officer_id === user?.user_id)}
          onDone={reload}
        />
      )}

      {!brief.assigned && (
        <Note>
          You are not the allocated authority for this report, so you cannot
          record a finding on it. Only the Talathi and Krushi Sahayak posted to{" "}
          {r.place.village_name} may. Reach over a village is not the same as
          being its verifier.
        </Note>
      )}

      {/* Findings and consensus --------------------------------------- */}
      {brief.findings && brief.findings.length > 0 && (
        <FindingsPanel findings={brief.findings} consensus={consensus} />
      )}

      {consensus?.complete && can("report:adjudicate") && (
        <Adjudicate
          reportId={r.id}
          consensus={consensus}
          suggestedArea={r.items.reduce((t, i) => t + i.area_affected_ha, 0)}
          onDone={reload}
        />
      )}
    </div>
  );
}

/* --------------------------------------------------------------------- */

const OUTCOMES: { value: Outcome; label: string; needsFigure: boolean; hint: string }[] = [
  { value: "confirmed", label: "Confirmed", needsFigure: true,
    hint: "The loss is as reported, or close to it." },
  { value: "partly_confirmed", label: "Partly confirmed", needsFigure: true,
    hint: "A loss occurred, of a different extent." },
  { value: "rejected", label: "No loss found", needsFigure: false,
    hint: "No loss attributable to the notified calamity." },
  { value: "not_traceable", label: "Not traceable", needsFigure: false,
    hint: "The parcel or the farmer could not be found." },
];

function RecordFinding({
  reportId,
  lines,
  /**
   * What the panel concluded, or null where no assessment has run yet -- a
   * claim can reach an officer before its evidence has been through it. With
   * a figure the officer confirms or overrules; without one they record their
   * own, as they always did.
   */
  modelDamage,
  modelCropPresence,
  onDone,
  /**
   * "amend" is the second act: both authorities have filed, each has read the
   * other, and either may record a revised finding while the report is still
   * theirs. The earlier one is kept.
   */
  mode = "record",
  /** The officer's own standing finding, so a revision starts from it. */
  mine,
}: {
  reportId: string;
  lines: number;
  onDone: () => void;
  mode?: "record" | "amend";
  mine?: Finding;
  modelDamage?: number | null;
  modelCropPresence?: string | null;
}) {
  const amending = mode === "amend";
  const [outcome, setOutcome] = useState<Outcome>(mine?.outcome ?? "confirmed");
  const [damage, setDamage] = useState(
    mine?.damage_percent != null
      ? String(mine.damage_percent)
      : modelDamage != null
        ? String(modelDamage)
        : "",
  );
  const [area, setArea] = useState(
    mine?.area_affected_ha != null ? String(mine.area_affected_ha) : "",
  );
  const [findings, setFindings] = useState("");

  // Whether this officer is taking the panel's figure or replacing it. Only
  // asked where there is a panel figure to take.
  const [agrees, setAgrees] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const needsFigure = OUTCOMES.find((o) => o.value === outcome)?.needsFigure ?? false;

  // Whether the panel reached something an officer can simply agree with.
  //
  // A damage figure is one: it is derived from the farmer's own clips, and
  // confirming it is the ordinary case. So is a parcel with no crop on it --
  // there is no loss to assess, and the claim is refused on that. Anything
  // else (crop contested, or no figure and no clear absence) is the case a
  // person is actually needed for, and the officer is asked to decide.
  const settled: { outcome: Outcome; says: string } | null =
    modelDamage != null
      ? { outcome: "confirmed", says: `${modelDamage}% damage` }
      : modelCropPresence === "absent"
        ? { outcome: "rejected", says: "no crop on this parcel, so no loss" }
        : null;

  /** Confirming takes the panel's finding as it stands. */
  async function confirm() {
    if (!settled || busy) return;
    setBusy(true);
    setError(null);
    try {
      const body = {
        outcome: settled.outcome,
        damage_percent: modelDamage,
        area_affected_ha: null,
        findings: "",
        saw_model_assessment: true,
      };
      if (amending) {
        await api.amendVerification(reportId, body);
      } else {
        await api.verifyReport(reportId, body);
      }
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Something went wrong.");
      setBusy(false);
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    const body = {
      outcome,
      damage_percent: needsFigure ? Number(damage) : null,
      area_affected_ha: needsFigure && area ? Number(area) : null,
      findings,
      // True, and it matters that it is true. internal/calibration reports
      // informed and blind findings separately so that agreement with the
      // model cannot be mistaken for evidence the model is right; sending
      // false here while showing the officer the figure would corrupt the
      // only measure this platform has of its own accuracy.
      saw_model_assessment: modelDamage !== null,
    };
    try {
      if (amending) {
        await api.amendVerification(reportId, body);
      } else {
        await api.verifyReport(reportId, body);
      }
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Something went wrong.");
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader
        title={amending ? "Revise your finding" : "Record your finding"}
        subtitle={
          amending
            ? "What you record now stands in place of your earlier finding, which is kept and stays readable."
            : "What the farmer's photographs and the record show. Final once submitted."
        }
      />
      <form onSubmit={submit} className="space-y-5 px-4 py-4">
        {settled && (
          <div className="rounded-md border border-border bg-muted/40 px-3 py-3">
            <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
              <span className="text-sm text-muted-foreground">
                The assessment found
              </span>
              <span className="font-semibold">{settled.says}</span>
              {modelCropPresence && (
                <span className="text-xs text-faint">
                  · crop {modelCropPresence}
                </span>
              )}
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              Read from the farmer&apos;s own clips and the satellite record.
              Confirming takes it as your finding; nothing else is asked.
            </p>

            <div className="mt-3 flex flex-wrap gap-2">
              <Button type="button" disabled={busy} onClick={() => void confirm()}>
                {busy && <Loader2 className="size-4 animate-spin" />}
                Confirm this
              </Button>
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setAgrees(false)}>
                Overrule it
              </Button>
            </div>

            {!agrees && (
              <p className="mt-2 text-xs text-faint">
                An overrule needs your own figure and a reason. Both are kept
                with the claim and read by whoever decides it.
              </p>
            )}
          </div>
        )}

        {(!settled || !agrees) && (
          <>
        <div>
          <Label className="mb-2 block">Outcome</Label>
          <div className="grid gap-2 sm:grid-cols-2">
            {OUTCOMES.map((o) => (
              <button
                key={o.value}
                type="button"
                onClick={() => setOutcome(o.value)}
                className={
                  "rounded-md border px-3 py-2 text-left transition-colors " +
                  (outcome === o.value
                    ? "border-primary bg-primary/5"
                    : "border-border hover:bg-muted")
                }
              >
                <div className="text-sm font-medium">{o.label}</div>
                <div className="mt-0.5 text-xs text-muted-foreground">{o.hint}</div>
              </button>
            ))}
          </div>
        </div>

        {needsFigure && (modelDamage == null || !agrees) && (
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="damage">
                {modelDamage == null ? "Damage (%)" : "Your figure (%)"}
              </Label>
              <Input
                id="damage"
                type="number"
                min={0}
                max={100}
                step="0.01"
                value={damage}
                onChange={(e) => setDamage(e.target.value)}
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="area">Area affected (ha)</Label>
              <Input
                id="area"
                type="number"
                min={0}
                step="0.0001"
                value={area}
                onChange={(e) => setArea(e.target.value)}
              />
            </div>
          </div>
        )}

        <div className="space-y-1.5">
          <Label htmlFor="findings">
            {modelDamage != null && !agrees
              ? "Why you are overruling the assessment"
              : "What you found in the evidence"}
          </Label>
          <Textarea
            id="findings"
            rows={4}
            // An overrule replaces a figure the rest of the process would
            // otherwise have used, so it is worth a sentence somebody can
            // argue with rather than "checked".
            minLength={modelDamage != null && !agrees ? 25 : 10}
            required
            value={findings}
            onChange={(e) => setFindings(e.target.value)}
            placeholder={
              lines === 1
                ? "e.g. 7/12 verified, survey number matches the record; hail damage across the eastern portion of the plot"
                : "Record what you saw on each survey number."
            }
          />
          <p className="text-xs text-faint">
            {amending
              ? "Say what changed your mind. A revised figure with no account of why reads, to anyone looking at it later, as two officers deciding to match."
              : "A figure with no reasoning cannot be argued with, and a panchnama that cannot be argued with is not one."}
          </p>
          {/* Said before it is written, not discovered afterwards. A remark
              written as an internal note and one written to be read by the
              person it concerns are different pieces of writing, and an
              officer is entitled to know which they are producing. */}
          <p className="text-xs leading-relaxed text-muted-foreground">
            The farmer will read this, with your rank but not your name, once
            both of you have recorded.
          </p>
        </div>

        {error && (
          <p role="alert" className="rounded-md bg-sev-catastrophic-bg px-3 py-2 text-sm text-sev-catastrophic">
            {error}
          </p>
        )}

        <div className="flex items-center gap-3">
          <Button type="submit" disabled={busy}>
            {busy && <Loader2 className="size-4 animate-spin" />}
            {amending ? "Submit revised finding" : "Submit finding"}
          </Button>
          <span className="text-xs text-faint">
            {amending
              ? "Both findings stay on the record, and the report shows which came first."
              : "This cannot be edited afterwards."}
          </span>
        </div>
          </>
        )}

        {settled && agrees && error && (
          <p role="alert" className="rounded-md bg-sev-catastrophic-bg px-3 py-2 text-sm text-sev-catastrophic">
            {error}
          </p>
        )}
      </form>
    </Card>
  );
}

function FindingsPanel({
  findings,
  consensus,
}: {
  findings: Finding[];
  consensus?: Consensus;
}) {
  return (
    <Card>
      <CardHeader
        title="Findings"
        subtitle="Recorded independently by the village's allocated authorities"
        action={
          consensus?.complete ? (
            <StateBadge styles={routeStyles} state={consensus.route} />
          ) : undefined
        }
      />

      <div className="divide-y divide-border">
        {findings.map((f) => {
          const o = outcomeLabels[f.outcome];
          return (
            <div key={f.id} className="px-4 py-4">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                <span className="text-sm font-medium">{f.officer_name}</span>
                <span className="text-xs text-muted-foreground">
                  {roleLabel(f.role)}
                </span>
                <span className={`rounded px-1.5 py-0.5 text-xs font-medium ${o?.bg} ${o?.text}`}>
                  {o?.label ?? f.outcome}
                </span>
                {f.damage_percent !== null && (
                  <span className="tnum text-sm font-semibold">
                    {f.damage_percent}%
                  </span>
                )}
                {f.revised ? (
                  <span className="text-xs text-faint">
                    revised after reading the other finding
                  </span>
                ) : (
                  f.saw_peer_finding && (
                    <span className="text-xs text-faint">recorded second</span>
                  )
                )}
                <span className="ml-auto text-xs text-faint">
                  {istDateTime(f.visited_at ?? f.submitted_at)}
                </span>
              </div>
              <p className="mt-1.5 text-sm leading-relaxed text-muted-foreground">
                {f.findings}
              </p>
            </div>
          );
        })}
      </div>

      {consensus?.complete && (
        <div className="border-t border-border bg-muted/40 px-4 py-4">
          <dl className="grid grid-cols-2 gap-5 sm:grid-cols-4">
            <Figure
              label="First"
              value={consensus.first_percent === null ? "—" : `${consensus.first_percent}%`}
            />
            <Figure
              label="Second"
              value={consensus.second_percent === null ? "—" : `${consensus.second_percent}%`}
            />
            <Figure
              label="Divergence"
              value={consensus.divergence === null ? "—" : `${consensus.divergence.toFixed(1)}`}
              unit="points"
              tone={
                consensus.route === "consensus" ? "good"
                  : consensus.route === "arbitration" ? "warn" : "bad"
              }
            />
            <Figure
              label="Mean"
              value={consensus.mean === null ? "—" : `${consensus.mean}%`}
            />
          </dl>
          <p className="mt-3 text-sm leading-relaxed text-foreground">
            {consensus.explanation}
          </p>
        </div>
      )}
    </Card>
  );
}

function Adjudicate({
  reportId,
  consensus,
  suggestedArea,
  onDone,
}: {
  reportId: string;
  consensus: Consensus;
  suggestedArea: number;
  onDone: () => void;
}) {
  const [percent, setPercent] = useState(
    consensus.mean !== null ? String(consensus.mean) : "",
  );
  const [area, setArea] = useState(String(Number(suggestedArea.toFixed(4))));
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.adjudicateReport(reportId, {
        adjudicated_percent: Number(percent),
        adjudicated_area_ha: Number(area),
        reason,
      });
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Something went wrong.");
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader
        title="Adjudicate"
        subtitle={`This report routes to the ${roleLabel(consensus.adjudicated_by)}`}
        action={<ShieldCheck className="size-4 text-muted-foreground" />}
      />
      <form onSubmit={submit} className="space-y-5 px-4 py-4">
        <Note>
          The figure you set is the one that leaves this system. It is informed
          by the two findings above; it is not obliged to be either of them, and
          the reason you give is what a grievance will be answered with.
        </Note>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="adj-percent">Adjudicated damage (%)</Label>
            <Input
              id="adj-percent"
              type="number"
              min={0}
              max={100}
              step="0.01"
              value={percent}
              onChange={(e) => setPercent(e.target.value)}
              required
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="adj-area">Adjudicated area (ha)</Label>
            <Input
              id="adj-area"
              type="number"
              min={0}
              step="0.0001"
              value={area}
              onChange={(e) => setArea(e.target.value)}
              required
            />
          </div>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="adj-reason">Reason</Label>
          <Textarea
            id="adj-reason"
            rows={3}
            minLength={10}
            required
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder="e.g. both authorities within the consensus band; adjudicated on their mean"
          />
        </div>

        {error && (
          <p role="alert" className="rounded-md bg-sev-catastrophic-bg px-3 py-2 text-sm text-sev-catastrophic">
            {error}
          </p>
        )}

        <Button type="submit" disabled={busy}>
          {busy && <Loader2 className="size-4 animate-spin" />}
          Record the decision
        </Button>
      </form>
    </Card>
  );
}

export default function VerifyPage() {
  return (
    <Suspense fallback={<Skeleton className="h-64 w-full" />}>
      <VerifyView />
    </Suspense>
  );
}
