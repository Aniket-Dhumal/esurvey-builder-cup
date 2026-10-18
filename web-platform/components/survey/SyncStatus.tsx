"use client";

import { useCallback, useEffect, useState } from "react";
import { CheckCircle2, CloudOff, Loader2, RefreshCw, TriangleAlert } from "lucide-react";

import { useOnline } from "@/lib/sensors";
import {
  pendingBoundaries,
  pendingCaptures,
  pendingDrafts,
} from "@/lib/offline/db";
import { startAutoSync, sync, type SyncResult } from "@/lib/offline/sync";

/**
 * What is still on the phone and what has reached the office.
 *
 * A farmer who photographed a field with no signal needs to know two things,
 * and only two: that nothing was lost, and whether it has gone yet. Anything
 * more — retry counts, queue internals — is noise to them and reassurance to
 * nobody.
 *
 * The bar disappears entirely when there is nothing outstanding. A permanent
 * status indicator that usually says "all fine" is one people stop reading.
 */
export function SyncStatus({ onSynced }: { onSynced?: () => void }) {
  const online = useOnline();
  const [queued, setQueued] = useState<{
    drafts: number;
    captures: number;
    boundaries: number;
  }>({ drafts: 0, captures: 0, boundaries: 0 });
  const [busy, setBusy] = useState(false);
  const [justSent, setJustSent] = useState(false);

  const refresh = useCallback(
    () =>
      Promise.all([
        pendingDrafts(),
        pendingCaptures(),
        pendingBoundaries(),
      ]).then(([d, c, b]) => {
        setQueued({
          drafts: d.filter((x) => !x.serverId).length,
          captures: c.length,
          // A walked boundary counts here for the same reason a photograph
          // does: it is five minutes of somebody pacing a field edge, and a
          // bar that says nothing is outstanding while it sits unsent is the
          // bar telling them their work is safe when it has not left.
          boundaries: b.length,
        });
      }),
    [],
  );

  useEffect(() => {
    void refresh();

    const stop = startAutoSync((r: SyncResult) => {
      void refresh();
      if (r.draftsSent > 0 || r.capturesSent > 0 || r.boundariesSent > 0) {
        setJustSent(true);
        onSynced?.();
        window.setTimeout(() => setJustSent(false), 4000);
      }
    });

    // The service worker nudges every open tab when the network returns.
    const onMessage = (e: MessageEvent) => {
      if (e.data === "sync-now") void handleSync();
    };
    navigator.serviceWorker?.addEventListener("message", onMessage);

    return () => {
      stop();
      navigator.serviceWorker?.removeEventListener("message", onMessage);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function handleSync() {
    setBusy(true);
    await sync();
    await refresh();
    setBusy(false);
  }

  const outstanding = queued.drafts + queued.captures + queued.boundaries;

  if (justSent && outstanding === 0) {
    return (
      <div className="flex items-center gap-2 rounded-lg bg-sev-negligible-bg px-3 py-2.5 text-sm text-sev-negligible">
        <CheckCircle2 className="size-4 shrink-0" />
        Everything has reached the office.
      </div>
    );
  }

  if (outstanding === 0) return null;

  const items: string[] = [];
  if (queued.drafts) items.push(`${queued.drafts} report${queued.drafts > 1 ? "s" : ""}`);
  if (queued.captures) {
    items.push(`${queued.captures} photograph${queued.captures > 1 ? "s" : ""}`);
  }
  if (queued.boundaries) {
    items.push(
      `${queued.boundaries} field boundar${queued.boundaries > 1 ? "ies" : "y"}`,
    );
  }

  return (
    <div
      className={
        "flex items-center gap-2.5 rounded-lg px-3 py-2.5 text-sm " +
        (online ? "bg-muted" : "bg-sev-moderate-bg text-sev-moderate")
      }
    >
      {online ? (
        busy ? (
          <Loader2 className="size-4 shrink-0 animate-spin text-muted-foreground" />
        ) : (
          <TriangleAlert className="size-4 shrink-0 text-muted-foreground" />
        )
      ) : (
        <CloudOff className="size-4 shrink-0" />
      )}

      <div className="min-w-0 flex-1">
        <div className={online ? "text-foreground" : ""}>
          {items.join(" and ")} waiting on your phone
        </div>
        <div className={"text-xs " + (online ? "text-muted-foreground" : "opacity-80")}>
          {online
            ? busy
              ? "Sending now…"
              : "Nothing is lost. It will be sent automatically."
            : "Nothing is lost. It will be sent when the network returns."}
        </div>
      </div>

      {online && !busy && (
        <button
          onClick={handleSync}
          className="shrink-0 rounded-md border border-border px-2.5 py-1.5 text-xs"
        >
          <RefreshCw className="mr-1 inline size-3.5" />
          Send now
        </button>
      )}
    </div>
  );
}
