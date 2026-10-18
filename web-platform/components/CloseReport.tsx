"use client";

import { useState } from "react";
import { Archive, Loader2 } from "lucide-react";

import { api } from "@/lib/api";
import type { ReportSummary } from "@/lib/types";
import { useSession } from "@/components/SessionProvider";
import { Button } from "@/components/ui/button";

/**
 * Closing a claim that has been decided.
 *
 * # Why this was missing and why it matters
 *
 * The report state machine has always allowed `verified` and `adjudicated` to
 * move to `closed`, the route has always existed, and nothing anywhere ever
 * called it. So every claim this platform has ever decided is still open: the
 * worklists carry them, `TerminalStates` describes a state nothing reaches,
 * and the farmer's own progress screen ends on a step that can never complete.
 *
 * # Why it is not automatic
 *
 * Closing is an administrative act, not a consequence. A decided claim may
 * still be appealed, and a claim closed the moment the District signed would
 * shut that door by a side effect nobody chose. So a person closes it, and the
 * appeal window is theirs to judge.
 */
export function CloseReport({
  report,
  onDone,
}: {
  report: ReportSummary;
  onDone: () => void;
}) {
  const { can } = useSession();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // The same two the server accepts. Offering the button anywhere else would
  // be offering a control that can only produce a refusal.
  const closable = report.state === "verified" || report.state === "adjudicated";
  if (!closable || !can("report:review")) return null;

  async function close() {
    setBusy(true);
    setError(null);
    try {
      await api.transitionReport(report.id, "closed");
      onDone();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not close it.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <span className="inline-flex items-center gap-2">
      {error && (
        <span role="alert" className="text-xs text-sev-catastrophic">
          {error}
        </span>
      )}
      <Button
        size="sm"
        variant="outline"
        onClick={close}
        disabled={busy}
        title="Close this claim. It has been decided and nothing further is owed on it."
      >
        {busy ? (
          <Loader2 className="size-3.5 animate-spin" />
        ) : (
          <Archive className="size-3.5" />
        )}
        Close
      </Button>
    </span>
  );
}
