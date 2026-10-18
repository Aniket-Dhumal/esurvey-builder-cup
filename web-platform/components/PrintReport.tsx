"use client";

import { useState } from "react";
import { Loader2, Printer } from "lucide-react";

import { api } from "@/lib/api";
import { cn } from "@/lib/utils";

/**
 * Printing a crop loss report.
 *
 * # Why this is not a link
 *
 * It was one: `<a href={reportPrintUrl(id)} target="_blank">`, in three
 * places, and it had never worked. This console keeps its access token in
 * memory rather than in a cookie — deliberately, because a cookie is sent on
 * every request whether or not the page meant to make one — and a browser
 * opening a new tab carries neither. The endpoint answered 401 and the
 * officer got a JSON error object in a fresh tab, which reads like the report
 * itself is broken rather than the link to it.
 *
 * So the HTML is fetched the way every other authenticated thing is fetched,
 * and written into a window this page opens.
 *
 * # Why the window opens before the fetch
 *
 * A popup blocker allows `window.open` during a click and refuses it a moment
 * later. Opening after the `await` is the version that works on the machine it
 * was written on and is blocked on half the machines it runs on, so the window
 * is opened first and filled when the report arrives.
 */
export function PrintReport({
  reportId,
  reference,
  className,
  label,
}: {
  reportId: string;
  /** Named in the accessible label, so a row of these is not forty "Print"s. */
  reference?: string;
  className?: string;
  /** Shown beside the icon. Omitted for the icon-only cell in a table. */
  label?: string;
}) {
  return (
    <PrintDocument
      fetchHtml={() => api.reportPrintHtml(reportId)}
      what={reference ? `Print ${reference}` : "Print this report"}
      noun="report"
      className={className}
      label={label}
    />
  );
}

/**
 * The general case: any authenticated endpoint that answers with a page.
 *
 * Two documents use it — the farmer's filing and the assessment behind it —
 * and they are not the same paper. Sharing the mechanism and not the wording
 * keeps "Could not open the report" from appearing over an assessment.
 */
export function PrintDocument({
  fetchHtml,
  what,
  noun,
  className,
  label,
}: {
  fetchHtml: () => Promise<string>;
  /** The accessible label. */
  what: string;
  /** What it is, for the preparing screen and the failure message. */
  noun: string;
  className?: string;
  label?: string;
}) {
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);

  async function print() {
    if (busy) return;
    setBusy(true);
    setFailed(false);

    // Opened inside the click, before anything is awaited.
    //
    // Deliberately without `noopener`: the specification says window.open
    // returns null when it is given, because the opener is not allowed a
    // handle on the result. The tab still opens — so with noopener the
    // officer got a permanently blank tab and this component believed the
    // popup had been blocked.
    //
    // It costs nothing here. noopener exists to stop a page at someone
    // else's origin reaching back through window.opener, and what goes into
    // this window is our own report HTML on our own origin.
    const w = window.open("", "_blank");
    if (!w) {
      // Blocked. Saying so is the whole remedy: nothing this component can do
      // will open a window the browser has refused, and a silent no-op leaves
      // the officer clicking a button that does nothing.
      setBusy(false);
      setFailed(true);
      return;
    }

    // Something to look at while the report is fetched. A blank tab reads as
    // a failure, and this one is replaced the moment the HTML lands.
    w.document.write(
      "<!doctype html><title>Preparing…</title>" +
        '<body style="font:14px system-ui;padding:2rem;color:#52525b">' +
        `Preparing the ${noun}…</body>`,
    );

    try {
      const html = await fetchHtml();
      w.document.open();
      w.document.write(html);
      w.document.close();
      // After close(), so the stylesheet in the document has been applied and
      // the print preview shows the report rather than unstyled text.
      w.onload = () => w.focus();
    } catch {
      w.close();
      setFailed(true);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <button
        type="button"
        onClick={() => void print()}
        disabled={busy}
        aria-label={what}
        className={cn(
          "inline-flex items-center gap-1.5 rounded text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:opacity-50",
          label ? "px-2 py-1 text-sm" : "size-7 justify-center",
          className,
        )}>
        {busy ? (
          <Loader2 className="size-4 animate-spin" />
        ) : (
          <Printer className="size-4" />
        )}
        {label}
      </button>
      {failed && (
        <span role="alert" className="ml-2 text-xs text-destructive">
          Could not open the {noun}. Allow pop-ups for this site and try
          again.
        </span>
      )}
    </>
  );
}
