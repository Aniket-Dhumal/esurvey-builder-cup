"use client";

import { useCallback, useRef, useState } from "react";
import { Download, Mic, Send, Square, Upload, VolumeX, Wrench } from "lucide-react";

import { api, ApiError } from "@/lib/api";
import type {
  AssistantAnswer,
  AssistantMessage,
  ExportFormat,
  PreparedExport,
} from "@/lib/api";
import { useDictation, useReadAloud } from "@/lib/dictation";
import {
  Card,
  CardHeader,
  EmptyState,
  ErrorPanel,
  Note,
} from "@/components/ui";
import { Button } from "@/components/ui/button";
import { PageHeader } from "@/components/dashboard";

/**
 * The officer's assistant.
 *
 * # What it will not do
 *
 * Approve, return, reassign or close anything. It reads claims within this
 * officer's own posting, explains what a figure rests on, fills their own report
 * format, and suggests. The suggestion appears beside a button that takes the
 * officer to the ordinary screen for that action, so the decision is recorded
 * against the person who made it -- which is what an appeal asks about.
 *
 * # Why the steps are on screen
 *
 * Because an officer acting on a figure needs to know where it came from. "I read
 * these four claims" is the difference between an answer they can check and one they
 * must take on trust, and an assistant that hides its working is one whose mistakes
 * are invisible until somebody has signed them.
 *
 * # Why speaking to it is the same as typing to it
 *
 * An officer working a queue has a keyboard and an ear, and the ear is free. So the
 * question can be spoken -- but only the question. What the microphone produces is a
 * transcript, and the transcript is posted to the same endpoint the box below posts
 * to, so the model that heard it is never the model that answers and every limit on
 * the assistant applies identically to both. See lib/dictation.ts, and the route in
 * the platform that will not hand the relay a claim.
 *
 * The answer is read back by this browser, from the text already on the screen. It is
 * the cheap direction: nothing leaves the console to do it, and a browser with no
 * voices installed loses the speech and keeps the answer.
 */
export default function AssistantPage() {
  const [history, setHistory] = useState<AssistantMessage[]>([]);
  const [answers, setAnswers] = useState<AssistantAnswer[]>([]);
  const [question, setQuestion] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const {
    speak: readAnswer,
    stop: stopReading,
    speaking,
    silent,
  } = useReadAloud();

  const [files, setFiles] = useState<File[]>([]);
  const attachInput = useRef<HTMLInputElement>(null);

  const ask = useCallback(
    async (said: string) => {
      const asked = said.trim();
      if (!asked || busy) return;

      // Whatever was being read out belongs to the previous question. An answer
      // finishing underneath the next one is the fastest way to have somebody turn
      // this off for good.
      stopReading();

      setBusy(true);
      setError(null);
      // Cleared before the request, so a slow answer does not leave the officer
      // wondering whether their question went.
      setQuestion("");
      const asking: AssistantMessage[] = [
        ...history,
        { role: "user", text: asked },
      ];

      try {
        const answer = await api.askAssistant(asked, history, files);
        // The files belonged to that question. Keeping them would attach them
        // again to the next one, which costs the officer the upload twice and
        // puts a second audit line against a file nobody re-attached.
        setFiles([]);
        if (attachInput.current) attachInput.current.value = "";
        setAnswers((a) => [...a, answer]);
        setHistory([...asking, { role: "assistant", text: answer.text }]);
        readAnswer(answer.text);
      } catch (e) {
        // Put in the box, whether it was typed or spoken. A spoken question that
        // failed on the network is a question the officer can send again with one
        // press instead of saying the whole thing twice.
        setQuestion(asked);
        if (e instanceof ApiError && e.code === "AGENT_UNAVAILABLE") {
          setError(
            "The assistant is not available. Everything else on this console is unaffected.",
          );
        } else {
          setError(e instanceof Error ? e.message : "The assistant failed.");
        }
      } finally {
        setBusy(false);
      }
    },
    [busy, files, history, readAnswer, stopReading],
  );

  const dictation = useDictation((spoken) => void ask(spoken));
  const listening = dictation.state !== "idle";

  return (
    <div className="space-y-6">
      <PageHeader
        title="Assistant"
        subtitle="Ask about the claims in your area. It reads and drafts; it cannot approve, return or change anything."
      />

      {error && <ErrorPanel message={error} />}

      <FormatFiller />
      <Exporter />

      <Card>
        <CardHeader title="Ask" />
        <div className="space-y-4 p-4">
          {answers.length === 0 && !busy && (
            <EmptyState
              title="Nothing asked yet"
              body='Try "how many claims are waiting on me", or "why is CLR-2026-496-00001 short of evidence".'
            />
          )}

          {answers.map((answer, i) => (
            <Answer key={i} answer={answer} />
          ))}

          {busy && (
            <p className="text-sm text-muted-foreground">Working…</p>
          )}

          {/*
            What has been heard, as it is heard.

            Shown rather than held back until the end because a transcript is the one
            part of this the officer has to check: a misheard village name produces a
            confident answer about the wrong village, and seeing the words appear is
            how they catch it before pressing anything.
          */}
          {listening && (
            <div className="rounded-md border border-dashed p-3">
              <p className="text-xs text-muted-foreground">
                {dictation.state === "opening"
                  ? "Opening the microphone…"
                  : dictation.state === "settling"
                    ? "Finishing…"
                    : "Listening. Press stop when you have finished the question."}
              </p>
              {dictation.heard && (
                <p className="mt-1 text-sm">{dictation.heard}</p>
              )}
            </div>
          )}

          {/*
            What the officer is told before attaching anything.

            Permanent beside the control rather than a dialog. A dialog shown every
            time is a dialog clicked through, and this is the one warning that has to
            land: nothing inspects what is in an image or a PDF, so the judgement is
            genuinely the officer's and the honest thing is to say so. A filter that
            worked most of the time would be worse, because it would teach them the
            system is protecting them when it is not.
          */}
          <div className="rounded-md border border-dashed p-3">
            <p className="text-xs text-muted-foreground">
              A file you attach is sent to Google&rsquo;s Gemini model to be read.
              Nothing checks what is inside it first &mdash; that judgement is yours.
              Do not attach an Aadhaar card, a bank passbook or anything else you
              would not send outside this office. Who attached which file, of what
              type and what size, is recorded against this question.
            </p>
            <p className="mt-2 text-xs text-muted-foreground">
              PNG, JPEG or WebP image, PDF, .xlsx workbook, CSV, or plain text or
              Markdown. Up to 4 files, 8 MB each, 16 MB together.
            </p>
            <input
              ref={attachInput}
              type="file"
              multiple
              accept=".png,.jpg,.jpeg,.webp,.pdf,.xlsx,.csv,.txt,.md,.markdown"
              className="mt-2 block w-full text-xs"
              onChange={(e) => setFiles(Array.from(e.target.files ?? []))}
            />
            {files.length > 0 && (
              <ul className="mt-2 space-y-1">
                {files.map((f) => (
                  <li key={f.name} className="text-xs">
                    {f.name}{" "}
                    <span className="text-muted-foreground">
                      ({Math.round(f.size / 1024)} KB)
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div className="flex gap-2">
            <input
              className="flex-1 rounded-md border px-3 py-2 text-sm"
              placeholder="Ask about your claims"
              value={question}
              onChange={(e) => setQuestion(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  void ask(question);
                }
              }}
              disabled={busy}
            />

            {/*
              Always pressable, and it explains itself when it cannot work.

              The alternative was greying it out from a capability check on render,
              which this console cannot do honestly: it is a static export, so the
              button is drawn once with no browser behind it and again in the tab, and
              the two would disagree. More to the point, whether the officer will
              allow a microphone is not knowable until they are asked -- and they are
              asked here, on the press, and nowhere else.
            */}
            {listening ? (
              <Button variant="destructive" onClick={dictation.stop}>
                <Square className="mr-2 size-4" />
                Stop
              </Button>
            ) : (
              <Button
                variant="outline"
                disabled={busy}
                onClick={() => {
                  stopReading();
                  dictation.start();
                }}
              >
                <Mic className="mr-2 size-4" />
                Speak
              </Button>
            )}

            {speaking && (
              <Button variant="outline" onClick={stopReading}>
                <VolumeX className="mr-2 size-4" />
                Stop reading
              </Button>
            )}

            <Button
              onClick={() => void ask(question)}
              disabled={busy || listening || !question.trim()}
            >
              <Send className="mr-2 size-4" />
              Ask
            </Button>
          </div>

          {/*
            Said once, where it happened. A button that did nothing and explained
            nothing is the failure this is here to avoid; the typed box above it is
            untouched in every one of these cases.
          */}
          {dictation.unavailable && <Note>{dictation.unavailable}</Note>}
          {silent && <Note>{silent}</Note>}
        </div>
      </Card>
    </div>
  );
}

/**
 * One document the conversation prepared.
 *
 * # Why the file is fetched rather than linked
 *
 * A link would need the officer's token in a URL, and a URL is the one place a
 * credential must never go: it reaches logs, proxies, browser history and the
 * clipboard. So the button fetches with the session the console already holds and
 * hands the bytes to the browser.
 *
 * # Why it can be pressed again
 *
 * The prepared document lives for ten minutes and survives being downloaded, so a
 * failed or interrupted download is one more press rather than one more question.
 * After that it is gone and the honest thing is to say so and let them re-ask --
 * the figures will be today's, which is better than a statement of a queue that has
 * moved.
 */
function PreparedDownload({ prepared }: { prepared: PreparedExport }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchIt = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      const out = await api.preparedExport(prepared.id);
      const url = URL.createObjectURL(out.blob);
      const a = document.createElement("a");
      a.href = url;
      // The name the server sanitised, not the one shown above: they can differ by
      // a character where the title held something a filename cannot.
      a.download = out.filename;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      if (e instanceof ApiError && e.code === "NO_PREPARED_EXPORT") {
        setError(
          "That document has expired. Ask again and it will be prepared with today’s figures.",
        );
      } else {
        setError(e instanceof Error ? e.message : "The download failed.");
      }
    } finally {
      setBusy(false);
    }
  }, [prepared.id]);

  return (
    <div className="rounded-md border bg-muted/40 p-3">
      <div className="flex items-center gap-3">
        <Button
          variant="outline"
          size="sm"
          disabled={busy}
          onClick={() => void fetchIt()}
        >
          <Download className="mr-2 size-4" />
          {busy ? "Fetching…" : "Download"}
        </Button>
        <div className="min-w-0 text-xs">
          <p className="truncate font-medium">{prepared.filename}</p>
          <p className="text-muted-foreground">
            {prepared.rows} claim(s) &middot; {prepared.format.toUpperCase()}{" "}
            &middot; {Math.max(1, Math.round(prepared.bytes / 1024))} KB
          </p>
        </div>
      </div>
      {error && <p className="mt-2 text-xs text-destructive">{error}</p>}
    </div>
  );
}

function Answer({ answer }: { answer: AssistantAnswer }) {
  return (
    <div className="space-y-2 rounded-md border p-3">
      <p className="whitespace-pre-wrap text-sm">{answer.text}</p>

      {answer.steps.length > 0 && (
        <details className="text-xs text-muted-foreground">
          <summary className="cursor-pointer select-none">
            <Wrench className="mr-1 inline size-3" />
            What it looked at ({answer.steps.length})
          </summary>
          <ul className="mt-2 space-y-1">
            {answer.steps.map((s, i) => (
              <li key={i}>
                <span className="font-mono">{s.tool}</span>
                {s.failed ? (
                  <span className="text-destructive"> — {s.failed}</span>
                ) : (
                  <span> — {s.summary}</span>
                )}
                <span className="ml-1 opacity-60">{s.duration_ms} ms</span>
              </li>
            ))}
          </ul>
        </details>
      )}

      {/*
        The documents this answer prepared, as buttons rather than a link.
        //
        Rendered newest first. A conversation that corrected itself leaves more than
        one -- "make that Excel" prepares a second rather than replacing the first --
        and the last is the one the officer meant. The earlier ones stay reachable
        because somebody who corrected a filter and then wanted the original back
        should not have to ask for it twice.
      */}
      {answer.prepared_exports && answer.prepared_exports.length > 0 && (
        <div className="space-y-2">
          {[...answer.prepared_exports].reverse().map((p) => (
            <PreparedDownload key={p.id} prepared={p} />
          ))}
        </div>
      )}

      {answer.proposed && (
        <div className="rounded-md bg-muted p-3 text-sm">
          <p className="font-medium">
            Suggested: {answer.proposed.action}
            {answer.proposed.report_id ? ` — ${answer.proposed.report_id}` : ""}
          </p>
          <p className="text-muted-foreground">{answer.proposed.reason}</p>
          {/*
            A link, not a button that acts. The assistant has no way to approve or
            return anything, and the record of who decided has to name a person.
          */}
          {answer.proposed.report_id && (
            <a
              className="mt-2 inline-block underline"
              href={`/reports/${encodeURIComponent(answer.proposed.report_id)}`}
            >
              Open the claim and decide
            </a>
          )}
        </div>
      )}
    </div>
  );
}

/**
 * Upload a report format, get it back with real figures in it.
 *
 * Only the column headings go to the model. The values are read from the officer's
 * own claims, by the server, with their own posting applied -- so a wrong mapping
 * shows up as a column of wrong figures that they spot at once, rather than as a
 * plausible number invented in the right place.
 */
/**
 * The same figures, in whichever format the officer asked for.
 *
 * # Why this sits beside the format filler rather than replacing it
 *
 * They answer different questions. The filler takes the office's own document and
 * writes into it, so what comes back is their layout with our figures -- which is
 * what somebody needs when a format is prescribed and the columns are not theirs to
 * choose. This produces a document from nothing, which is what somebody needs when
 * they have no format to hand and simply want the data in a shape they can use.
 *
 * # What is not sent
 *
 * Nothing about a claim. The request carries a format and the same filters the claim
 * list already takes; the server resolves the rows from the officer's own posting, so
 * there is no parameter through which a claim they cannot already open could arrive.
 * The model is not involved at all -- this is the server writing a file.
 */
function Exporter() {
  const [busy, setBusy] = useState<ExportFormat | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const run = useCallback(async (format: ExportFormat) => {
    setBusy(format);
    setError(null);
    setNote(null);
    try {
      const out = await api.exportAs({ format });

      // Handed straight to the browser, and nothing kept. The document is the
      // officer's own figures and this console has no business storing a copy.
      const url = URL.createObjectURL(out.blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = out.filename;
      a.click();
      URL.revokeObjectURL(url);

      setNote(`${out.rows} claim(s) exported as ${out.filename}.`);
    } catch (e) {
      if (e instanceof ApiError && e.code === "TOO_MANY_ROWS") {
        // The server names its own limit, and that sentence is more use than
        // anything written here: it says what to narrow by.
        setError(e.message);
      } else if (e instanceof ApiError && e.code === "PDF_UNAVAILABLE") {
        setError(
          "The PDF converter is not answering. The other formats still work.",
        );
      } else if (e instanceof ApiError && e.code === "NO_ROWS") {
        setError("No claims matched, so there was nothing to put in a file.");
      } else {
        setError(e instanceof Error ? e.message : "The export failed.");
      }
    } finally {
      setBusy(null);
    }
  }, []);

  // Ordered by what an office actually reaches for: the spreadsheet first, the
  // printable document next, and the two that are occasionally wanted last.
  const formats: { format: ExportFormat; label: string; hint: string }[] = [
    { format: "xlsx", label: "Excel", hint: "figures to work with" },
    { format: "csv", label: "CSV", hint: "for another system" },
    { format: "pdf", label: "PDF", hint: "to print or file" },
    { format: "docx", label: "Word", hint: "to edit and circulate" },
    { format: "pptx", label: "PowerPoint", hint: "for a review meeting" },
  ];

  return (
    <Card>
      <CardHeader title="Export the claims" />
      <div className="space-y-3 p-4">
        <p className="text-sm text-muted-foreground">
          Your area&rsquo;s claims as a file. Choose the format you need.
        </p>
        {error && <ErrorPanel message={error} />}
        {note && <p className="text-sm">{note}</p>}
        <div className="flex flex-wrap gap-2">
          {formats.map((f) => (
            <Button
              key={f.format}
              variant="outline"
              disabled={busy !== null}
              onClick={() => void run(f.format)}
              title={f.hint}
            >
              <Download className="mr-2 size-4" />
              {busy === f.format ? "Preparing…" : f.label}
            </Button>
          ))}
        </div>
      </div>
    </Card>
  );
}

function FormatFiller() {
  const input = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const onPick = useCallback(async (file: File) => {
    setBusy(true);
    setError(null);
    setNote(null);
    try {
      const out = await api.fillReportFormat(file);

      // Handed straight to the browser. Nothing is kept here: the workbook is the
      // officer's own document with their own figures in it, and this console has no
      // business storing a copy.
      const url = URL.createObjectURL(out.blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = out.filename;
      a.click();
      URL.revokeObjectURL(url);

      setNote(
        `${out.rows} claims written into ${out.columnsFilled} column(s). Columns it could not match were left exactly as they were.`,
      );
    } catch (e) {
      if (e instanceof ApiError && e.code === "NO_COLUMNS_MATCHED") {
        setError(
          "None of those column headings matched anything this can fill. The file was left alone.",
        );
      } else if (e instanceof ApiError && e.code === "NEEDS_XLSX") {
        setError(
          "Send the format as .xlsx. A PDF cannot be written back into, and would come back as a different document.",
        );
      } else {
        setError(e instanceof Error ? e.message : "The format could not be filled.");
      }
    } finally {
      setBusy(false);
      if (input.current) input.current.value = "";
    }
  }, []);

  return (
    <Card>
      <CardHeader title="Fill a report format" />
      <div className="space-y-3 p-4">
        <p className="text-sm text-muted-foreground">
          Upload your office&rsquo;s own .xlsx format. It comes back with your
          area&rsquo;s claims in it, keeping your columns, headings and layout.
        </p>
        {error && <ErrorPanel message={error} />}
        {note && <p className="text-sm">{note}</p>}
        <input
          ref={input}
          type="file"
          accept=".xlsx"
          className="hidden"
          onChange={(e) => {
            const file = e.target.files?.[0];
            if (file) void onPick(file);
          }}
        />
        <Button
          variant="outline"
          disabled={busy}
          onClick={() => input.current?.click()}
        >
          <Upload className="mr-2 size-4" />
          {busy ? "Filling…" : "Choose a format"}
        </Button>
      </div>
    </Card>
  );
}
