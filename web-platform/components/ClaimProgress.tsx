"use client";

import { useEffect, useState } from "react";
import { Check, Circle, Loader2, TriangleAlert } from "lucide-react";

import { api } from "@/lib/api";
import type {
  ClaimProgress as Progress,
  ProgressStage,
  ProgressStageKey,
} from "@/lib/types";
import { istDateTime, roleLabel } from "@/lib/format";
import { Card, CardHeader, Skeleton } from "@/components/ui";

/**
 * Where a claim has got to.
 *
 * # Why an officer needs this and not just the state badge
 *
 * A claim's state says what it is; this says what it is waiting for, which is
 * a different question and the one somebody asks when a farmer rings up. The
 * badge reads "acknowledged" whether the claim reached its officers an hour
 * ago or has been unallocated for a fortnight because nobody is posted to the
 * village.
 *
 * # Why `attention` looks different
 *
 * A stage that is current and not moving on its own is not progress. Drawn in
 * the same grey as "in progress" it reads as the system working, which is
 * exactly the impression that let claims sit unallocated with nobody noticing.
 */

const STAGE_LABEL: Record<ProgressStageKey, string> = {
  received: "Received",
  checked: "Checked",
  field: "Field verification",
  decision: "Decision",
  closed: "Closed",
};

/**
 * Why a stage is where it is, in the office's own words.
 *
 * The server sends keys: the farmer's handset renders them in Marathi from
 * strings it carries, and this renders the same keys for an officer. A key
 * with no entry here falls through to nothing rather than showing the reader
 * a raw identifier.
 */
const WHY_LABEL: Record<string, string> = {
  evidence_arriving: "photographs and clips still uploading",
  evidence_short: "sent on without all the evidence the handset promised",
  assessing: "assessment running",
  assessment_unavailable: "no assessment — the claim goes on without one",
  no_authority: "waiting to be allocated",
  awaiting_both_officers: "neither officer has recorded yet",
  awaiting_second_officer: "one of the two has recorded",
  with_circle: "with the Circle Officer",
  with_tehsil: "with the Tehsildar",
  with_district: "with the District",
  returned_for_more: "sent back for more",
  withdrawn: "withdrawn",
};

export function ClaimProgress({ reportId }: { reportId: string }) {
  const [progress, setProgress] = useState<Progress | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let live = true;
    api
      .claimProgress(reportId)
      .then((p) => live && setProgress(p))
      .catch(() => live && setFailed(true));
    return () => {
      live = false;
    };
  }, [reportId]);

  if (failed) return null;
  if (!progress) {
    return (
      <Card>
        <CardHeader title="Progress" />
        <div className="p-5">
          <Skeleton className="h-16 w-full" />
        </div>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader
        title="Progress"
        subtitle="Where this claim is, and what it is waiting for"
      />

      <div className="px-4 py-4">
        <ol className="flex flex-wrap gap-x-2 gap-y-3">
          {progress.stages.map((s, i) => (
            <Step key={s.key} stage={s} last={i === progress.stages.length - 1} />
          ))}
        </ol>

        {/* What is stopping it, said plainly and only to an officer. This is
            the sentence that tells a District which village to staff. */}
        {progress.stuck && (
          <p className="mt-4 flex gap-2 rounded-md bg-sev-severe-bg px-3 py-2.5 text-xs leading-relaxed text-sev-severe">
            <TriangleAlert className="mt-0.5 size-4 shrink-0" />
            <span>
              This claim is not moving: {progress.stuck}. It will go on by
              itself once that is fixed.
            </span>
          </p>
        )}

        {progress.evidence_short && (
          <p className="mt-3 text-xs leading-relaxed text-sev-moderate">
            The farmer&rsquo;s phone promised more evidence than arrived. The
            claim was sent on after two days rather than held.
          </p>
        )}

        {progress.remarks && progress.remarks.length > 0 && (
          <Remarks progress={progress} />
        )}
      </div>
    </Card>
  );
}

function Step({ stage, last }: { stage: ProgressStage; last: boolean }) {
  const why = stage.why ? WHY_LABEL[stage.why] : undefined;

  const tone =
    stage.state === "done"
      ? "text-sev-negligible"
      : stage.state === "attention"
        ? "text-sev-severe"
        : stage.state === "current"
          ? "text-primary"
          : "text-faint";

  return (
    <li className="flex min-w-[9rem] flex-1 items-start gap-2">
      <span className={`mt-0.5 shrink-0 ${tone}`}>
        {stage.state === "done" ? (
          <Check className="size-4" />
        ) : stage.state === "attention" ? (
          <TriangleAlert className="size-4" />
        ) : stage.state === "current" ? (
          <Loader2 className="size-4 animate-spin" />
        ) : (
          <Circle className="size-4" />
        )}
      </span>
      <span className="min-w-0">
        <span className={`block text-xs font-medium ${tone}`}>
          {STAGE_LABEL[stage.key]}
        </span>
        {stage.at && (
          <span className="block text-[11px] text-faint">
            {istDateTime(stage.at)}
          </span>
        )}
        {why && (
          <span className="block text-[11px] leading-snug text-muted-foreground">
            {why}
          </span>
        )}
      </span>
      {!last && <span className="mt-2 hidden h-px flex-1 bg-border sm:block" />}
    </li>
  );
}

/**
 * What each officer recorded.
 *
 * Shown here because the same words go to the farmer, and an officer should be
 * able to see what the farmer is reading. The note says so: a remark written
 * as an internal aide-memoire and a remark written to be read by the person it
 * concerns are different pieces of writing, and an officer is entitled to know
 * which one they are producing.
 */
function Remarks({ progress }: { progress: Progress }) {
  return (
    <div className="mt-5 border-t border-border pt-4">
      <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        What each officer recorded
      </h3>
      <p className="mt-1 text-[11px] text-faint">
        The farmer can read these, and the figure, now that both village
        officers have filed.
      </p>

      <ol className="mt-3 space-y-3">
        {progress.remarks?.map((r, i) => (
          <li key={i} className="text-xs leading-relaxed">
            <span className="font-medium text-foreground">
              {roleLabel(r.role)}
            </span>
            {r.by && <span className="text-muted-foreground"> · {r.by}</span>}
            {r.percent != null && (
              <span className="text-muted-foreground">
                {" "}
                · {r.percent.toFixed(1)}%
              </span>
            )}
            <span className="text-faint"> · {istDateTime(r.at)}</span>
            <p className="mt-0.5 text-muted-foreground">&ldquo;{r.text}&rdquo;</p>
          </li>
        ))}
      </ol>
    </div>
  );
}
