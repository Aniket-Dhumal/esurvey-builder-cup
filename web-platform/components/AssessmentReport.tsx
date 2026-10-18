"use client";

import { useEffect, useState } from "react";
import { AlertCircle, Loader2 } from "lucide-react";

import { Card, CardHeader } from "@/components/ui";
import { PrintDocument } from "@/components/PrintReport";
import { api } from "@/lib/api";

/**
 * The assessment report, on the page where the decision is made.
 *
 * # Why it is here and not only a print button
 *
 * The officer records a finding on this screen, and until now the only way to
 * read the document behind it was to print it: a button in the corner of a
 * different page, which nobody under time pressure presses. What the officer
 * actually saw was a damage percentage and a badge — the conclusion with none
 * of the working — and they were then asked to confirm it.
 *
 * That is not confirmation, it is assent. The report is where the panel says
 * what each instrument found, what it could not find, which part of the field
 * the evidence came from, and what the figure cannot support. An officer
 * agreeing with a number they have read the reasoning for is doing the job;
 * one agreeing with a number on a card is rubber-stamping.
 *
 * So the document is on the page, above the decision, and open by default.
 *
 * # Why an iframe
 *
 * The report is a complete document with its own stylesheet, laid out for A4
 * and rendered by the server so that a print today and a print at an appeal
 * next year are the same page. Injecting it into this page would put its CSS
 * in with the console's and give an officer a document that looks like neither.
 * The frame keeps it exactly as it will be printed.
 *
 * Loaded through a blob URL rather than srcDoc: the document carries its
 * photographs inline as data URIs and runs to megabytes, which is not a thing
 * to pass through an HTML attribute.
 */
export function AssessmentReport({
  assessmentId,
  surveyNumber,
}: {
  assessmentId: string;
  surveyNumber?: string;
}) {
  // Stamped with the assessment it belongs to, rather than cleared at the top
  // of the effect. Clearing there is a synchronous setState inside an effect,
  // which React 19 rejects — and it is also how a stale document gets shown
  // for a moment under the wrong claim's heading.
  const [loaded, setLoaded] = useState<{
    id: string;
    url: string | null;
    failed: boolean;
  } | null>(null);
  const current = loaded?.id === assessmentId ? loaded : null;

  useEffect(() => {
    let stale = false;
    let objectUrl: string | null = null;

    api
      .assessmentDossierHtml(assessmentId)
      .then((html) => {
        // The officer moved to another claim while this was in flight.
        // Creating the URL now would leak it.
        if (stale) return;
        objectUrl = URL.createObjectURL(
          new Blob([html], { type: "text/html" }),
        );
        setLoaded({ id: assessmentId, url: objectUrl, failed: false });
      })
      .catch(() => {
        if (!stale) setLoaded({ id: assessmentId, url: null, failed: true });
      });

    return () => {
      stale = true;
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [assessmentId]);

  return (
    <Card>
      <CardHeader
        title="Assessment report"
        subtitle="What each instrument found, and what it could not. Read this before recording a finding below."
        action={
          <PrintDocument
            fetchHtml={() => api.assessmentDossierHtml(assessmentId)}
            what={
              surveyNumber
                ? `Print the assessment of survey ${surveyNumber}`
                : "Print the assessment"
            }
            noun="assessment"
            label="Print"
            className="border border-border"
          />
        }
      />

      {current?.failed ? (
        <div className="flex gap-2 px-4 py-4 text-sm text-muted-foreground">
          <AlertCircle className="mt-0.5 size-4 shrink-0" />
          <p>
            The assessment report could not be loaded. The claim and the
            evidence above are unaffected — but record a finding only if you
            are content to decide without it.
          </p>
        </div>
      ) : current?.url == null ? (
        <div className="flex items-center gap-2 px-4 py-8 text-sm text-muted-foreground">
          <Loader2 className="size-4 animate-spin" />
          Preparing the assessment report…
        </div>
      ) : (
        <iframe
          src={current.url}
          title="Assessment report"
          // No scripts: the document is static HTML and a stylesheet, and
          // nothing it contains needs to run.
          sandbox=""
          className="h-[70vh] w-full border-0 bg-white"
        />
      )}
    </Card>
  );
}
